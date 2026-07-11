// Package crypto provides password hashing and secure random token
// generation, built entirely on the standard library (no x/crypto) so it
// has zero external dependencies.
package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// pbkdf2Iterations is the work factor for password hashing. OWASP's 2023
// guidance for PBKDF2-HMAC-SHA256 is >= 600,000 iterations; bump this over
// time as hardware gets faster. Changing it doesn't invalidate existing
// hashes — the iteration count is stored alongside each hash.
const pbkdf2Iterations = 600_000

const (
	saltBytes = 16
	keyBytes  = 32
)

// HashPassword derives a salted PBKDF2-HMAC-SHA256 hash of password and
// encodes it, along with the salt and iteration count, into a single
// string safe to store in a database column.
//
// Format: pbkdf2-sha256$<iterations>$<salt-b64>$<hash-b64>
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password must not be empty")
	}

	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}

	hash := pbkdf2(password, salt, pbkdf2Iterations, keyBytes)

	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s",
		pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword reports whether password matches encodedHash, previously
// produced by HashPassword. Comparison is constant-time.
func VerifyPassword(password, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false, fmt.Errorf("unrecognized hash format")
	}

	iterations, err := strconv.Atoi(parts[1])
	if err != nil {
		return false, fmt.Errorf("invalid iteration count in hash: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false, fmt.Errorf("invalid salt encoding in hash: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false, fmt.Errorf("invalid hash encoding: %w", err)
	}

	got := pbkdf2(password, salt, iterations, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// pbkdf2 implements PBKDF2 (RFC 8018) with HMAC-SHA256 as the PRF.
func pbkdf2(password string, salt []byte, iterations, keyLen int) []byte {
	prf := hmac.New(sha256.New, []byte(password))
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen

	var derived []byte
	buf := make([]byte, 4)
	for block := 1; block <= numBlocks; block++ {
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)

		prf.Reset()
		prf.Write(salt)
		prf.Write(buf)
		u := prf.Sum(nil)

		t := make([]byte, len(u))
		copy(t, u)

		for i := 1; i < iterations; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		derived = append(derived, t...)
	}
	return derived[:keyLen]
}

// GenerateToken returns a cryptographically random, URL-safe token with n
// bytes of entropy (the encoded string will be longer than n). Use this
// for session tokens, password-reset tokens, and email-verification tokens.
func GenerateToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns a hex-encoded HMAC-SHA256 of token using pepper as the
// key. Store this — never the raw token — in the database; the raw token
// only ever lives in the client's cookie/header. pepper should come from
// config.SessionSecret and stay out of version control.
func HashToken(token, pepper string) string {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(token))
	return fmt.Sprintf("%x", mac.Sum(nil))
}
