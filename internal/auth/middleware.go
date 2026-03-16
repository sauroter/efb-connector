package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"

	"golang.org/x/crypto/hkdf"
)

// contextKey is an unexported type used to store values in context, preventing
// collisions with other packages.
type contextKey string

const userIDKey contextKey = "userID"

// RequireAuth is HTTP middleware that enforces authentication. It reads the
// "session" cookie, validates it, and stores the user ID in the request
// context. If the session is missing or invalid the request is redirected to
// /login.
func (s *AuthService) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		userID, err := s.ValidateSession(cookie.Value)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		ctx := context.WithValue(r.Context(), userIDKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CSRFProtect is HTTP middleware that validates the "csrf_token" form field on
// POST (and other state-changing) requests. It compares the submitted token
// against the expected HMAC for the session.
func (s *AuthService) CSRFProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut ||
			r.Method == http.MethodPatch || r.Method == http.MethodDelete {

			cookie, err := r.Cookie(SessionCookieName)
			if err != nil || cookie.Value == "" {
				http.Error(w, "Forbidden: missing session", http.StatusForbidden)
				return
			}

			submitted := r.FormValue("csrf_token")
			expected := s.csrfToken(cookie.Value, r.URL.Path)

			if !hmac.Equal([]byte(submitted), []byte(expected)) {
				http.Error(w, "Forbidden: invalid CSRF token", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// CSRFToken generates a CSRF token for the current request. It must be called
// after RequireAuth so that a session cookie is present. The token is
// HMAC-SHA256(session_token || request_path, csrf_secret).
func (s *AuthService) CSRFToken(r *http.Request) string {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	return s.csrfToken(cookie.Value, r.URL.Path)
}

// csrfToken computes HMAC-SHA256(sessionToken || path, csrfSecret).
func (s *AuthService) csrfToken(sessionToken, path string) string {
	secret := s.deriveCSRFSecret()
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sessionToken))
	mac.Write([]byte(path))
	return hex.EncodeToString(mac.Sum(nil))
}

// deriveCSRFSecret uses HKDF-SHA256 to derive a 32-byte CSRF secret from the
// encryption key.
func (s *AuthService) deriveCSRFSecret() []byte {
	reader := hkdf.New(sha256.New, s.encryptionKey, nil, []byte("efb-connector-csrf"))
	secret := make([]byte, 32)
	if _, err := io.ReadFull(reader, secret); err != nil {
		// This should never fail given a valid key, but panic to avoid
		// silently using a zero key.
		panic("auth: HKDF derivation failed: " + err.Error())
	}
	return secret
}

// UserFromContext extracts the user ID stored by RequireAuth middleware.
// Returns (0, false) if not present.
func UserFromContext(ctx context.Context) (int64, bool) {
	uid, ok := ctx.Value(userIDKey).(int64)
	return uid, ok
}
