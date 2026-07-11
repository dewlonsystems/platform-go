package errors

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// -----------------------------------------------------------------------------
// newErr / AsAppError
// -----------------------------------------------------------------------------

func TestNewErr_UnmappedCodeDefaultsTo500(t *testing.T) {
	err := newErr(Code("SOME_UNKNOWN_CODE"), "oops", nil)
	if err.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("expected 500 default, got %d", err.HTTPStatus)
	}
}

func TestNewErr_KnownCodeMapsCorrectly(t *testing.T) {
	err := newErr(CodeNotFound, "missing", nil)
	if err.HTTPStatus != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", err.HTTPStatus)
	}
}

func TestAsAppError_WrapsPlainError(t *testing.T) {
	plain := &simpleErr{"db exploded"}
	ae := AsAppError(plain)
	if ae.Code != CodeInternal {
		t.Fatalf("expected internal code, got %s", ae.Code)
	}
	if ae.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", ae.HTTPStatus)
	}
}

func TestAsAppError_PassesThroughAppError(t *testing.T) {
	original := NewNotFound("user not found", nil)
	ae := AsAppError(original)
	if ae != original {
		t.Fatal("expected AsAppError to return the same *AppError, not wrap it again")
	}
}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

// -----------------------------------------------------------------------------
// FromDBError
// -----------------------------------------------------------------------------

func TestFromDBError_NoRows(t *testing.T) {
	ae := FromDBError(pgx.ErrNoRows, "user not found")
	if ae.Code != CodeNotFound {
		t.Fatalf("expected CodeNotFound, got %s", ae.Code)
	}
	if ae.Message != "user not found" {
		t.Fatalf("expected custom not-found message, got %q", ae.Message)
	}
}

func TestFromDBError_UniqueViolation(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505"}
	ae := FromDBError(pgErr, "unused")
	if ae.Code != CodeConflict {
		t.Fatalf("expected CodeConflict for 23505, got %s", ae.Code)
	}
}

func TestFromDBError_ForeignKeyViolation(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23503"}
	ae := FromDBError(pgErr, "unused")
	if ae.Code != CodeUnprocessable {
		t.Fatalf("expected CodeUnprocessable for 23503, got %s", ae.Code)
	}
}

func TestFromDBError_CheckAndNotNullViolations(t *testing.T) {
	for _, code := range []string{"23514", "23502"} {
		pgErr := &pgconn.PgError{Code: code}
		ae := FromDBError(pgErr, "unused")
		if ae.Code != CodeBadInput {
			t.Fatalf("expected CodeBadInput for %s, got %s", code, ae.Code)
		}
	}
}

func TestFromDBError_UnrecognizedSQLSTATE_FallsBackToInternal(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "99999"} // not one we specifically handle
	ae := FromDBError(pgErr, "unused")
	if ae.Code != CodeInternal {
		t.Fatalf("expected CodeInternal fallback, got %s", ae.Code)
	}
}

func TestFromDBError_NonPgError_FallsBackToInternal(t *testing.T) {
	ae := FromDBError(&simpleErr{"connection refused"}, "unused")
	if ae.Code != CodeInternal {
		t.Fatalf("expected CodeInternal for non-pg error, got %s", ae.Code)
	}
}

func TestFromDBError_NilReturnsNil(t *testing.T) {
	if FromDBError(nil, "unused") != nil {
		t.Fatal("expected nil in, nil out")
	}
}

// -----------------------------------------------------------------------------
// WriteJSON (net/http)
// -----------------------------------------------------------------------------

func TestWriteJSON_SetsStatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/users/42", nil)

	WriteJSON(rec, req, NewNotFound("user not found", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json content-type, got %q", ct)
	}

	var body errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Error.Code != CodeNotFound {
		t.Fatalf("expected code NOT_FOUND in body, got %s", body.Error.Code)
	}
	if body.Error.Message != "user not found" {
		t.Fatalf("unexpected message: %s", body.Error.Message)
	}
}

func TestWriteJSON_NeverLeaksInternalErr(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	WriteJSON(rec, req, NewInternal("something broke", &simpleErr{"raw sql: SELECT * FROM secrets"}))

	body := rec.Body.String()
	if bytes.Contains([]byte(body), []byte("secrets")) {
		t.Fatalf("internal error detail leaked into response body: %s", body)
	}
}

func TestWriteJSON_WrapsPlainErrorAsInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	WriteJSON(rec, req, &simpleErr{"unexpected"})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for a plain (non-AppError) error, got %d", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// WriteFiberJSON
// -----------------------------------------------------------------------------

func TestWriteFiberJSON_SetsStatusAndBody(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c fiber.Ctx) error {
		return WriteFiberJSON(c, NewConflict("already exists", nil))
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}

	var body errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Error.Code != CodeConflict {
		t.Fatalf("expected CONFLICT code, got %s", body.Error.Code)
	}
}

func TestWriteFiberJSON_NeverLeaksInternalErr(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c fiber.Ctx) error {
		return WriteFiberJSON(c, NewInternal("boom", &simpleErr{"raw sql: SELECT * FROM secrets"}))
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	if bytes.Contains(buf.Bytes(), []byte("secrets")) {
		t.Fatalf("internal error detail leaked into Fiber response body: %s", buf.String())
	}
}

// -----------------------------------------------------------------------------
// HasCode / Unwrap / Is / WithFields
// -----------------------------------------------------------------------------

func TestHasCode_MatchesWrappedAppError(t *testing.T) {
	err := NewConflict("dup", nil)
	if !HasCode(err, CodeConflict) {
		t.Fatal("expected HasCode to match CodeConflict")
	}
	if HasCode(err, CodeNotFound) {
		t.Fatal("expected HasCode to not match a different code")
	}
}

func TestHasCode_FalseForPlainError(t *testing.T) {
	if HasCode(&simpleErr{"oops"}, CodeInternal) {
		t.Fatal("expected HasCode to return false for a non-AppError")
	}
}

func TestUnwrap_ReturnsUnderlyingErr(t *testing.T) {
	underlying := &simpleErr{"root cause"}
	ae := NewInternal("wrapped", underlying)
	if ae.Unwrap() != underlying {
		t.Fatal("expected Unwrap to return the original underlying error")
	}
}

func TestIs_MatchesSameCode(t *testing.T) {
	a := NewNotFound("a", nil)
	b := NewNotFound("b", nil) // different message, same code
	if !a.Is(b) {
		t.Fatal("expected Is to match on Code regardless of message")
	}
}

func TestIs_DoesNotMatchDifferentCode(t *testing.T) {
	a := NewNotFound("a", nil)
	b := NewConflict("b", nil)
	if a.Is(b) {
		t.Fatal("expected Is to not match different codes")
	}
}

func TestWithFields_AttachesFieldsAndReturnsSelf(t *testing.T) {
	err := NewBadInput("invalid", nil).WithFields(map[string]string{
		"email": "already in use",
	})
	if err.Fields["email"] != "already in use" {
		t.Fatal("expected WithFields to attach the field map")
	}
}
