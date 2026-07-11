// Package auth implements session-based authentication: register/login
// against the users table, opaque session tokens stored (hashed) in the
// sessions table, and middleware to protect routes.
package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dewlonsystems/platform-go/config"
	"github.com/dewlonsystems/platform-go/errors"
	"github.com/dewlonsystems/platform-go/utils/crypto"
)

// SessionCookieName is the cookie the client stores its session token in.
const SessionCookieName = "session_token"

// User is the public-facing representation of an authenticated user.
type User struct {
	ID        int64
	Email     string
	Name      string
	CreatedAt time.Time
}

// Service provides registration, login, session validation, and logout,
// backed by Postgres.
type Service struct {
	db     *pgxpool.Pool
	pepper string // HMAC key session tokens are hashed with before storage
	ttl    time.Duration
}

// NewService builds an auth Service from a DB pool and app config.
func NewService(db *pgxpool.Pool, cfg *config.Config) *Service {
	return &Service{db: db, pepper: cfg.SessionSecret, ttl: cfg.SessionTTL}
}

// Register creates a new user with a hashed password. Returns a Conflict
// error if the email is already taken.
func (s *Service) Register(ctx context.Context, email, password, name string) (*User, error) {
	if len(password) < 8 {
		return nil, errors.NewBadInput("password must be at least 8 characters", nil)
	}

	hash, err := crypto.HashPassword(password)
	if err != nil {
		return nil, errors.NewInternal("failed to hash password", err)
	}

	u := &User{}
	err = s.db.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, name)
		VALUES ($1, $2, $3)
		RETURNING id, email, name, created_at
	`, email, hash, name).Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt)
	if err != nil {
		return nil, errors.FromDBError(err, "user not found")
	}

	return u, nil
}

// Login verifies credentials and, if valid, creates a new session and
// returns its raw token (set this as the session cookie) along with the
// user. On failure it always returns the same generic Unauthorized error,
// regardless of whether the email existed or the password was wrong, to
// avoid leaking which emails are registered.
func (s *Service) Login(ctx context.Context, email, password string) (token string, user *User, err error) {
	invalidCreds := errors.NewUnauthorized("invalid email or password", nil)

	var passwordHash string
	u := &User{}
	err = s.db.QueryRow(ctx, `
		SELECT id, email, name, created_at, password_hash FROM users WHERE email = $1
	`, email).Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt, &passwordHash)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil, invalidCreds
		}
		return "", nil, errors.FromDBError(err, "user not found")
	}

	ok, err := crypto.VerifyPassword(password, passwordHash)
	if err != nil || !ok {
		return "", nil, invalidCreds
	}

	token, err = s.createSession(ctx, u.ID)
	if err != nil {
		return "", nil, err
	}

	return token, u, nil
}

// createSession generates a random session token, stores its hash with an
// expiry, and returns the raw token to hand back to the client.
func (s *Service) createSession(ctx context.Context, userID int64) (string, error) {
	token, err := crypto.GenerateToken(32)
	if err != nil {
		return "", errors.NewInternal("failed to generate session token", err)
	}

	tokenHash := crypto.HashToken(token, s.pepper)
	expiresAt := time.Now().Add(s.ttl)

	_, err = s.db.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)
	`, tokenHash, userID, expiresAt)
	if err != nil {
		return "", errors.FromDBError(err, "user not found")
	}

	return token, nil
}

// ValidateSession looks up the user for a raw session token, rejecting
// missing or expired sessions.
func (s *Service) ValidateSession(ctx context.Context, token string) (*User, error) {
	tokenHash := crypto.HashToken(token, s.pepper)

	u := &User{}
	var expiresAt time.Time
	err := s.db.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.created_at, s.expires_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1
	`, tokenHash).Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt, &expiresAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, errors.NewUnauthorized("session not found or expired", nil)
		}
		return nil, errors.FromDBError(err, "session not found")
	}

	if time.Now().After(expiresAt) {
		// Best-effort cleanup; don't fail the request over it.
		_, _ = s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
		return nil, errors.NewUnauthorized("session not found or expired", nil)
	}

	return u, nil
}

// Logout revokes a session so the token can no longer be used.
func (s *Service) Logout(ctx context.Context, token string) error {
	tokenHash := crypto.HashToken(token, s.pepper)
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	if err != nil {
		return errors.FromDBError(err, "session not found")
	}
	return nil
}

// -----------------------------------------------------------------------------
// HTTP middleware
// -----------------------------------------------------------------------------

type contextKey int

const userContextKey contextKey = 0

// RequireAuth protects a handler, requiring a valid session cookie. On
// success the authenticated *User is available via UserFromContext.
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil {
			errors.WriteJSON(w, r, errors.NewUnauthorized("authentication required", nil))
			return
		}

		user, err := s.ValidateSession(r.Context(), cookie.Value)
		if err != nil {
			errors.WriteJSON(w, r, err)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserFromContext retrieves the authenticated user set by RequireAuth.
func UserFromContext(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(userContextKey).(*User)
	return u, ok
}

// SetSessionCookie writes the session token as an HttpOnly, Secure cookie.
// secure should be true in production (HTTPS) and can be false for local
// HTTP development.
func SetSessionCookie(w http.ResponseWriter, token string, ttl time.Duration, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

// ClearSessionCookie expires the session cookie client-side, for logout.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
