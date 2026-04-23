// Package auth provides authentication services for the efb-connector web UI,
// including magic link login, session management, CSRF protection, rate
// limiting, and email delivery via Resend.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	"efb-connector/internal/database"
	"efb-connector/internal/resend"
)

// AuthService is the central authentication service. It bridges the database
// layer with HTTP concerns such as cookies, tokens, and email delivery.
type AuthService struct {
	db            *database.DB
	resendAPIKey  string
	baseURL       string
	emailFrom     string
	encryptionKey []byte // used to derive CSRF secret via HKDF

	// Resend contacts integration (nil = disabled).
	Resend         *resend.Client
	ResendSegSetup string // segment ID for "Needs Setup" users
}

// NewAuthService creates a new AuthService.
//   - db is the database handle (must not be nil).
//   - resendAPIKey is the Resend API key used for sending magic link emails.
//   - baseURL is the application base URL, e.g. "https://efb.example.com".
//   - encryptionKey must be 32 bytes (AES-256); it is used to derive CSRF
//     secrets via HKDF.
func NewAuthService(db *database.DB, resendAPIKey, baseURL, emailFrom string, encryptionKey []byte) *AuthService {
	if emailFrom == "" {
		emailFrom = "EFB Connector <noreply@efb-connector.com>"
	}
	return &AuthService{
		db:            db,
		resendAPIKey:  resendAPIKey,
		baseURL:       baseURL,
		emailFrom:     emailFrom,
		encryptionKey: encryptionKey,
	}
}

// GenerateMagicLink creates a one-time magic link token for the given email.
// It generates 32 random bytes, stores the SHA-256 hash in the database with a
// 15-minute expiry, and returns the raw token as a base64url-encoded string.
func (s *AuthService) GenerateMagicLink(email string) (token string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generate random token: %w", err)
	}

	token = base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256Hex(raw)

	expiresAt := time.Now().Add(15 * time.Minute)
	if err := s.db.CreateMagicLink(email, hash, expiresAt); err != nil {
		return "", fmt.Errorf("auth: create magic link: %w", err)
	}

	return token, nil
}

// ValidateMagicLink decodes the base64url token, hashes it, validates the
// magic link in the database, and returns the associated user ID.
// If no user exists for the email, one is automatically created.
func (s *AuthService) ValidateMagicLink(token string) (userID int64, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, fmt.Errorf("auth: decode magic link token: %w", err)
	}

	hash := sha256Hex(raw)
	email, err := s.db.ValidateMagicLink(hash)
	if err != nil {
		return 0, fmt.Errorf("auth: validate magic link: %w", err)
	}

	user, err := s.db.GetUserByEmail(email)
	if err != nil {
		return 0, fmt.Errorf("auth: get user by email: %w", err)
	}

	if user == nil {
		user, err = s.db.CreateUser(email)
		if err != nil {
			return 0, fmt.Errorf("auth: auto-create user: %w", err)
		}

		// Best-effort: add new user to Resend as a "Needs Setup" contact.
		if s.Resend != nil && s.ResendSegSetup != "" {
			segID := s.ResendSegSetup
			go func() {
				if err := s.Resend.CreateContact(email, nil); err != nil {
					slog.Warn("resend: create contact failed", "email", email, "error", err)
					return
				}
				if err := s.Resend.AddToSegment(email, segID); err != nil {
					slog.Warn("resend: add to needs-setup segment failed", "email", email, "error", err)
				}
			}()
		}
	}

	return user.ID, nil
}

// sha256Hex returns the lowercase hex-encoded SHA-256 digest of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}
