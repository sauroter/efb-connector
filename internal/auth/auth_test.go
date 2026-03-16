package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"efb-connector/internal/database"
)

// testKey is a 32-byte AES key used in all tests.
var testKey = []byte("12345678901234567890123456789012")

// openTestDB opens an in-memory SQLite database with migrations applied.
func openTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:", testKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestService creates an AuthService backed by an in-memory DB.
func newTestService(t *testing.T) *AuthService {
	t.Helper()
	db := openTestDB(t)
	return NewAuthService(db, "test-resend-key", "https://example.com", testKey)
}

// ──────────────────────────────────────────────
// Magic link tests
// ──────────────────────────────────────────────

func TestGenerateMagicLink(t *testing.T) {
	svc := newTestService(t)

	token, err := svc.GenerateMagicLink("alice@example.com")
	if err != nil {
		t.Fatalf("GenerateMagicLink: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	// Base64url tokens should not contain +, /, or =
	if strings.ContainsAny(token, "+/=") {
		t.Errorf("token contains non-base64url characters: %q", token)
	}
}

func TestValidateMagicLink_NewUser(t *testing.T) {
	svc := newTestService(t)

	token, err := svc.GenerateMagicLink("newuser@example.com")
	if err != nil {
		t.Fatalf("GenerateMagicLink: %v", err)
	}

	userID, err := svc.ValidateMagicLink(token)
	if err != nil {
		t.Fatalf("ValidateMagicLink: %v", err)
	}
	if userID == 0 {
		t.Error("expected non-zero user ID")
	}
}

func TestValidateMagicLink_ExistingUser(t *testing.T) {
	svc := newTestService(t)

	// Pre-create the user.
	user, err := svc.db.CreateUser("existing@example.com")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	token, err := svc.GenerateMagicLink("existing@example.com")
	if err != nil {
		t.Fatalf("GenerateMagicLink: %v", err)
	}

	userID, err := svc.ValidateMagicLink(token)
	if err != nil {
		t.Fatalf("ValidateMagicLink: %v", err)
	}
	if userID != user.ID {
		t.Errorf("userID = %d, want %d", userID, user.ID)
	}
}

func TestValidateMagicLink_InvalidToken(t *testing.T) {
	svc := newTestService(t)

	_, err := svc.ValidateMagicLink("totally-bogus-token")
	if err == nil {
		t.Error("expected error for invalid token, got nil")
	}
}

func TestValidateMagicLink_UsedTwice(t *testing.T) {
	svc := newTestService(t)

	token, _ := svc.GenerateMagicLink("once@example.com")
	if _, err := svc.ValidateMagicLink(token); err != nil {
		t.Fatalf("first validate: %v", err)
	}

	_, err := svc.ValidateMagicLink(token)
	if err == nil {
		t.Error("expected error on second use, got nil")
	}
}

// ──────────────────────────────────────────────
// Session tests
// ──────────────────────────────────────────────

func TestCreateAndValidateSession(t *testing.T) {
	svc := newTestService(t)
	user, _ := svc.db.CreateUser("sess@example.com")

	token, err := svc.CreateSession(user.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty session token")
	}

	uid, err := svc.ValidateSession(token)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if uid != user.ID {
		t.Errorf("userID = %d, want %d", uid, user.ID)
	}
}

func TestValidateSession_InvalidToken(t *testing.T) {
	svc := newTestService(t)

	_, err := svc.ValidateSession("invalid-session-token")
	if err == nil {
		t.Error("expected error for invalid session, got nil")
	}
}

func TestDestroySession(t *testing.T) {
	svc := newTestService(t)
	user, _ := svc.db.CreateUser("destroy@example.com")

	token, _ := svc.CreateSession(user.ID)

	if err := svc.DestroySession(token); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}

	_, err := svc.ValidateSession(token)
	if err == nil {
		t.Error("expected error after destroy, got nil")
	}
}

// ──────────────────────────────────────────────
// Middleware tests
// ──────────────────────────────────────────────

func TestRequireAuth_NoSession(t *testing.T) {
	svc := newTestService(t)

	handler := svc.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestRequireAuth_ValidSession(t *testing.T) {
	svc := newTestService(t)
	user, _ := svc.db.CreateUser("auth@example.com")
	token, _ := svc.CreateSession(user.ID)

	var gotUserID int64
	handler := svc.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := UserFromContext(r.Context())
		if !ok {
			t.Error("user ID not found in context")
			return
		}
		gotUserID = uid
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotUserID != user.ID {
		t.Errorf("gotUserID = %d, want %d", gotUserID, user.ID)
	}
}

func TestRequireAuth_InvalidSession(t *testing.T) {
	svc := newTestService(t)

	handler := svc.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "bad-token"})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
}

func TestCSRFProtect_GET_Passes(t *testing.T) {
	svc := newTestService(t)

	called := false
	handler := svc.CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/page", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("GET handler should be called without CSRF check")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestCSRFProtect_POST_MissingToken(t *testing.T) {
	svc := newTestService(t)
	user, _ := svc.db.CreateUser("csrf@example.com")
	token, _ := svc.CreateSession(user.ID)

	handler := svc.CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestCSRFProtect_POST_ValidToken(t *testing.T) {
	svc := newTestService(t)
	user, _ := svc.db.CreateUser("csrfok@example.com")
	sessionToken, _ := svc.CreateSession(user.ID)

	// Generate the expected CSRF token.
	csrfToken := svc.csrfToken(sessionToken)

	called := false
	handler := svc.CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	form := url.Values{"csrf_token": {csrfToken}}
	req := httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("handler should be called with valid CSRF token")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestCSRFProtect_POST_WrongToken(t *testing.T) {
	svc := newTestService(t)
	user, _ := svc.db.CreateUser("csrfbad@example.com")
	sessionToken, _ := svc.CreateSession(user.ID)

	handler := svc.CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	form := url.Values{"csrf_token": {"wrong-csrf-token"}}
	req := httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestCSRFToken_ViaPublicMethod(t *testing.T) {
	svc := newTestService(t)
	user, _ := svc.db.CreateUser("csrfpub@example.com")
	sessionToken, _ := svc.CreateSession(user.ID)

	req := httptest.NewRequest(http.MethodGet, "/form", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionToken})

	token := svc.CSRFToken(req)
	if token == "" {
		t.Error("expected non-empty CSRF token")
	}

	// Should be deterministic for the same session+path.
	token2 := svc.CSRFToken(req)
	if token != token2 {
		t.Errorf("CSRF token not deterministic: %q vs %q", token, token2)
	}

	// Same session, different path should yield the same token (session-scoped, not path-scoped).
	req2 := httptest.NewRequest(http.MethodGet, "/other", nil)
	req2.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionToken})
	token3 := svc.CSRFToken(req2)
	if token != token3 {
		t.Error("CSRF token should be the same across paths for the same session")
	}
}

func TestCSRFToken_NoCookie(t *testing.T) {
	svc := newTestService(t)

	req := httptest.NewRequest(http.MethodGet, "/form", nil)
	token := svc.CSRFToken(req)
	if token != "" {
		t.Errorf("expected empty CSRF token without cookie, got %q", token)
	}
}

func TestUserFromContext_Missing(t *testing.T) {
	_, ok := UserFromContext(context.Background())
	if ok {
		t.Error("expected ok=false for empty context")
	}
}

func TestUserFromContext_Present(t *testing.T) {
	ctx := context.WithValue(context.Background(), userIDKey, int64(42))
	uid, ok := UserFromContext(ctx)
	if !ok {
		t.Error("expected ok=true")
	}
	if uid != 42 {
		t.Errorf("uid = %d, want 42", uid)
	}
}

// ──────────────────────────────────────────────
// Rate limiter tests
// ──────────────────────────────────────────────

func TestRateLimiter_AllowLogin(t *testing.T) {
	rl := NewRateLimiter()

	// First call should be allowed.
	if !rl.AllowLogin("test@example.com", "1.2.3.4") {
		t.Error("first login should be allowed")
	}

	// Rapid successive calls for the same email should eventually be denied.
	denied := false
	for i := 0; i < 10; i++ {
		if !rl.AllowLogin("test@example.com", "1.2.3.4") {
			denied = true
			break
		}
	}
	if !denied {
		t.Error("expected rate limit to kick in for repeated logins")
	}
}

func TestRateLimiter_AllowLogin_DifferentEmails(t *testing.T) {
	rl := NewRateLimiter()

	// Different emails should have independent limits.
	if !rl.AllowLogin("a@example.com", "1.2.3.4") {
		t.Error("first login for a@ should be allowed")
	}
	if !rl.AllowLogin("b@example.com", "5.6.7.8") {
		t.Error("first login for b@ should be allowed")
	}
}

func TestRateLimiter_AllowLogin_IPLimit(t *testing.T) {
	rl := NewRateLimiter()

	// Exhaust the IP limit (20/hour) with different emails.
	denied := false
	for i := 0; i < 25; i++ {
		email := strings.Replace("user-N@example.com", "N", strings.Repeat("x", i), 1)
		if !rl.AllowLogin(email, "10.0.0.1") {
			denied = true
			break
		}
	}
	if !denied {
		t.Error("expected IP rate limit to kick in")
	}
}

func TestRateLimiter_AllowSync(t *testing.T) {
	rl := NewRateLimiter()

	// First sync should be allowed.
	if !rl.AllowSync(1) {
		t.Error("first sync should be allowed")
	}

	// Immediate second sync should be denied (1/hour).
	if rl.AllowSync(1) {
		t.Error("second immediate sync should be denied")
	}
}

func TestRateLimiter_AllowSync_DifferentUsers(t *testing.T) {
	rl := NewRateLimiter()

	if !rl.AllowSync(1) {
		t.Error("first sync for user 1 should be allowed")
	}
	if !rl.AllowSync(2) {
		t.Error("first sync for user 2 should be allowed")
	}
}

// ──────────────────────────────────────────────
// Email tests (using a mock HTTP server)
// ──────────────────────────────────────────────

func TestSendMagicLinkEmail(t *testing.T) {
	var receivedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}

		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-resend-key" {
			t.Errorf("Authorization = %q, want 'Bearer test-resend-key'", auth)
		}

		ct := r.Header.Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "test-email-id"}`))
	}))
	defer server.Close()

	// Override the resend endpoint for this test.
	oldEndpoint := resendEndpoint
	resendEndpoint = server.URL
	defer func() { resendEndpoint = oldEndpoint }()

	svc := newTestService(t)
	err := svc.SendMagicLinkEmail("user@example.com", "test-token-123", "https://example.com")
	if err != nil {
		t.Fatalf("SendMagicLinkEmail: %v", err)
	}

	// Verify the payload.
	if from, ok := receivedBody["from"].(string); !ok || !strings.Contains(from, "EFB Connector") {
		t.Errorf("from = %q", receivedBody["from"])
	}

	to, ok := receivedBody["to"].([]interface{})
	if !ok || len(to) != 1 || to[0].(string) != "user@example.com" {
		t.Errorf("to = %v", receivedBody["to"])
	}

	html, ok := receivedBody["html"].(string)
	if !ok || !strings.Contains(html, "https://example.com/auth/verify?token=test-token-123") {
		t.Errorf("html does not contain expected link: %q", html)
	}
}

func TestSendMagicLinkEmail_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer server.Close()

	oldEndpoint := resendEndpoint
	resendEndpoint = server.URL
	defer func() { resendEndpoint = oldEndpoint }()

	svc := newTestService(t)
	err := svc.SendMagicLinkEmail("user@example.com", "token", "https://example.com")
	if err == nil {
		t.Error("expected error for API failure, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status code: %v", err)
	}
}

// ──────────────────────────────────────────────
// Integration: full magic link -> session flow
// ──────────────────────────────────────────────

func TestFullLoginFlow(t *testing.T) {
	svc := newTestService(t)

	// 1. Generate magic link.
	token, err := svc.GenerateMagicLink("flow@example.com")
	if err != nil {
		t.Fatalf("GenerateMagicLink: %v", err)
	}

	// 2. Validate magic link -> get user ID.
	userID, err := svc.ValidateMagicLink(token)
	if err != nil {
		t.Fatalf("ValidateMagicLink: %v", err)
	}
	if userID == 0 {
		t.Fatal("expected non-zero user ID")
	}

	// 3. Create session.
	sessionToken, err := svc.CreateSession(userID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 4. Validate session.
	gotUID, err := svc.ValidateSession(sessionToken)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if gotUID != userID {
		t.Errorf("session userID = %d, want %d", gotUID, userID)
	}

	// 5. Use RequireAuth middleware.
	var contextUID int64
	handler := svc.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, _ := UserFromContext(r.Context())
		contextUID = uid
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if contextUID != userID {
		t.Errorf("context userID = %d, want %d", contextUID, userID)
	}

	// 6. Destroy session.
	if err := svc.DestroySession(sessionToken); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}

	// 7. Session should be invalid now.
	_, err = svc.ValidateSession(sessionToken)
	if err == nil {
		t.Error("expected error after session destroy")
	}
}
