package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"
)

// SessionMaxAge is the lifetime of a session cookie (30 days).
const SessionMaxAge = 30 * 24 * time.Hour

// SessionCookieName is the name of the HTTP cookie that carries the session token.
const SessionCookieName = "session"

// CreateSession generates a new session token for userID. It stores the
// SHA-256 hash in the database with a 30-day expiry and returns the raw token
// as a base64url-encoded string suitable for use as a cookie value.
func (s *AuthService) CreateSession(userID int64) (token string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generate session token: %w", err)
	}

	token = base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256Hex(raw)

	expiresAt := time.Now().Add(SessionMaxAge)
	if err := s.db.CreateSession(userID, hash, expiresAt); err != nil {
		return "", fmt.Errorf("auth: create session: %w", err)
	}

	return token, nil
}

// ValidateSession decodes the base64url token, hashes it, and validates the
// session in the database. On success it returns the owning user ID.
func (s *AuthService) ValidateSession(token string) (userID int64, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, fmt.Errorf("auth: decode session token: %w", err)
	}

	hash := sha256Hex(raw)
	userID, err = s.db.GetSession(hash)
	if err != nil {
		return 0, fmt.Errorf("auth: validate session: %w", err)
	}

	return userID, nil
}

// DestroySession invalidates the session identified by the raw token.
func (s *AuthService) DestroySession(token string) error {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return fmt.Errorf("auth: decode session token for destroy: %w", err)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	return s.db.DeleteSession(hash)
}
