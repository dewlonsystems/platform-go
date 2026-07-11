// Package errors provides a centralized error type for the HTTP API.
//
// It separates internal error detail (for logs) from what's safe to send
// to a client, maps errors to HTTP status codes, and gives every other
// package (auth, database, ratelimit, mail) one consistent way to fail.
package errors

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dewlonsystems/platform-go/logger"
)

// Code is a stable, machine-readable identifier for an error kind.
// Clients can safely branch on these — keep them stable once shipped.
type Code string

const (
	CodeBadInput      Code = "BAD_INPUT"
	CodeUnauthorized  Code = "UNAUTHORIZED"
	CodeForbidden     Code = "FORBIDDEN"
	CodeNotFound      Code = "NOT_FOUND"
	CodeConflict      Code = "CONFLICT"
	CodeUnprocessable Code = "UNPROCESSABLE_ENTITY"
	CodeRateLimited   Code = "TOO_MANY_REQUESTS"
	CodeLocked        Code = "ACCOUNT_LOCKED"
	CodeInternal      Code = "INTERNAL_ERROR"
)

var httpStatus = map[Code]int{
	CodeBadInput:      http.StatusBadRequest,
	CodeUnauthorized:  http.StatusUnauthorized,
	CodeForbidden:     http.StatusForbidden,
	CodeNotFound:      http.StatusNotFound,
	CodeConflict:      http.StatusConflict,
	CodeUnprocessable: http.StatusUnprocessableEntity,
	CodeRateLimited:   http.StatusTooManyRequests,
	CodeLocked:        http.StatusLocked,
	CodeInternal:      http.StatusInternalServerError,
}

// AppError is the centralized error type for the application.
//
// Message is safe to show a client. Err and any wrapped internal detail
// are logged server-side but never serialized in a response.
type AppError struct {
	Code       Code              `json:"code"`
	Message    string            `json:"message"`
	Fields     map[string]string `json:"fields,omitempty"` // per-field validation errors
	HTTPStatus int               `json:"-"`
	Err        error             `json:"-"`
}

// Error implements the error interface.
func (e *AppError) Error() string {
	if e.Err != nil {
		return string(e.Code) + ": " + e.Message + " (internal: " + e.Err.Error() + ")"
	}
	return string(e.Code) + ": " + e.Message
}

// Unwrap lets errors.Is / errors.As see through to the underlying cause.
func (e *AppError) Unwrap() error {
	return e.Err
}

// Is lets callers write errors.Is(err, errors.NewNotFound("", nil)) (or a
// shared sentinel) to test "was this a not-found?" by Code alone.
func (e *AppError) Is(target error) bool {
	var t *AppError
	if errors.As(target, &t) {
		return e.Code == t.Code
	}
	return false
}

// WithFields attaches per-field validation messages, e.g. {"email": "already in use"}.
func (e *AppError) WithFields(fields map[string]string) *AppError {
	e.Fields = fields
	return e
}

// -----------------------------------------------------------------------------
// Constructors
// -----------------------------------------------------------------------------

// newErr builds an AppError, defaulting HTTPStatus to 500 if code isn't in
// httpStatus. This guards against a map lookup on an unmapped Code silently
// producing HTTPStatus == 0, which would make WriteJSON/WriteFiberJSON call
// WriteHeader(0) — an invalid, silently-misbehaving response.
func newErr(code Code, message string, err error) *AppError {
	status, ok := httpStatus[code]
	if !ok {
		status = http.StatusInternalServerError
	}
	return &AppError{Code: code, Message: message, HTTPStatus: status, Err: err}
}

func NewBadInput(message string, err error) *AppError { return newErr(CodeBadInput, message, err) }
func NewUnauthorized(message string, err error) *AppError {
	return newErr(CodeUnauthorized, message, err)
}
func NewForbidden(message string, err error) *AppError { return newErr(CodeForbidden, message, err) }
func NewNotFound(message string, err error) *AppError  { return newErr(CodeNotFound, message, err) }
func NewConflict(message string, err error) *AppError  { return newErr(CodeConflict, message, err) }
func NewUnprocessable(message string, err error) *AppError {
	return newErr(CodeUnprocessable, message, err)
}
func NewRateLimited(message string, err error) *AppError {
	return newErr(CodeRateLimited, message, err)
}
func NewLocked(message string, err error) *AppError   { return newErr(CodeLocked, message, err) }
func NewInternal(message string, err error) *AppError { return newErr(CodeInternal, message, err) }

// -----------------------------------------------------------------------------
// Conversion & inspection helpers
// -----------------------------------------------------------------------------

// AsAppError converts any error into an *AppError, defaulting to a 500 to
// avoid accidentally leaking internal errors (raw SQL errors, etc.) to clients.
func AsAppError(err error) *AppError {
	if err == nil {
		return nil
	}
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr
	}
	return NewInternal("an unexpected error occurred", err)
}

// HasCode reports whether err (or anything it wraps) is an *AppError with the given code.
func HasCode(err error, code Code) bool {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code == code
	}
	return false
}

// FromDBError maps common pgx/Postgres errors onto AppError, so query code
// doesn't need to know Postgres SQLSTATE codes itself. Pass a not-found
// message for the pgx.ErrNoRows case since "not found" means different
// things depending on what was being queried.
func FromDBError(err error, notFoundMessage string) *AppError {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return NewNotFound(notFoundMessage, err)
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// SQLSTATE codes: https://www.postgresql.org/docs/current/errcodes-appendix.html
		switch pgErr.Code {
		case "23505": // unique_violation
			return NewConflict("that record already exists", err)
		case "23503": // foreign_key_violation
			return NewUnprocessable("referenced record does not exist", err)
		case "23514", "23502": // check_violation, not_null_violation
			return NewBadInput("invalid data", err)
		}
	}
	return NewInternal("database error", err)
}

// -----------------------------------------------------------------------------
// HTTP response helpers
// -----------------------------------------------------------------------------

type errorResponse struct {
	Error *AppError `json:"error"`
}

// WriteJSON normalizes err via AsAppError, logs the internal cause for
// server errors, and writes a clean JSON response — never leaking Err or
// HTTPStatus internals beyond the intended status code.
//
// For net/http-based handlers/middleware. For Fiber, use WriteFiberJSON.
func WriteJSON(w http.ResponseWriter, r *http.Request, err error) {
	ae := AsAppError(err)

	if ae.HTTPStatus >= http.StatusInternalServerError {
		logger.Log.Error("request failed",
			"code", ae.Code,
			"method", r.Method,
			"path", r.URL.Path,
			"err", ae.Err,
		)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ae.HTTPStatus)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: ae})
}

// WriteFiberJSON is the Fiber v3 equivalent of WriteJSON: normalizes err via
// AsAppError, logs the internal cause for server errors, and writes a clean
// JSON response via the Fiber context. Returns the error from c.Status().JSON
// so it can be returned directly from a Fiber handler.
//
// Note: importing this function pulls github.com/gofiber/fiber/v3 into any
// project that imports this errors package, even non-Fiber ones. If you
// later want a Fiber-free "errors" package for net/http-only projects, this
// function (and the fiber import) is the piece to move into a separate
// subpackage, e.g. errors/fibererrors.
func WriteFiberJSON(c fiber.Ctx, err error) error {
	ae := AsAppError(err)

	if ae.HTTPStatus >= http.StatusInternalServerError {
		logger.Log.Error("request failed",
			"code", ae.Code,
			"method", c.Method(),
			"path", c.Path(),
			"err", ae.Err,
		)
	}

	return c.Status(ae.HTTPStatus).JSON(errorResponse{Error: ae})
}
