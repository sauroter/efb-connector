package integration

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"efb-connector/internal/auth"
	"efb-connector/internal/database"
	"efb-connector/internal/efb"
	"efb-connector/internal/garmin"
	syncsvc "efb-connector/internal/sync"
	"efb-connector/internal/web"
)

var testKey = []byte("12345678901234567890123456789012")

// testServer bundles the httptest server, auth service, and mocks so that
// individual tests can inspect state.
type testServer struct {
	srv     *httptest.Server
	db      *database.DB
	auth    *auth.AuthService
	garmin  *garmin.MockGarminProvider
	efb     *efb.MockEFBProvider
	client  *http.Client // follows redirects, has cookie jar
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()

	db, err := database.Open(":memory:", testKey)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	logger := slog.Default()
	authService := auth.NewAuthService(db, "placeholder", "", "", testKey)
	rateLimiter := auth.NewRateLimiter()
	gp := garmin.NewMockGarminProvider()
	ep := efb.NewMockEFBProvider(logger)
	syncEngine := syncsvc.NewSyncEngine(db, gp, ep, logger)
	syncEngine.DisableSleep()

	s, err := web.NewServer(web.ServerDeps{
		DB:             db,
		Auth:           authService,
		SyncEngine:     syncEngine,
		Garmin:         gp,
		EFB:            ep,
		RateLimiter:    rateLimiter,
		InternalSecret: "test-secret",
		BaseURL:        "",
		Logger:         logger,
		TemplatesDir:   "../../templates",
	})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	// Use TLS server so Secure cookies are sent back by the cookie jar.
	ts := httptest.NewTLSServer(s.Routes())
	t.Cleanup(ts.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	return &testServer{
		srv:    ts,
		db:     db,
		auth:   authService,
		garmin: gp,
		efb:    ep,
		client: client,
	}
}

// login creates a user, generates a magic link, verifies it to create a
// session, and sets the session cookie on the test client.
func (ts *testServer) login(t *testing.T, email string) int64 {
	t.Helper()

	token, err := ts.auth.GenerateMagicLink(email)
	if err != nil {
		t.Fatalf("generate magic link: %v", err)
	}

	// Visit the verify URL — this creates the user + session and sets cookie.
	resp, err := ts.client.Get(ts.srv.URL + "/auth/verify?token=" + token)
	if err != nil {
		t.Fatalf("verify magic link: %v", err)
	}
	resp.Body.Close()

	user, err := ts.db.GetUserByEmail(email)
	if err != nil || user == nil {
		t.Fatalf("user not created after verify: %v", err)
	}
	return user.ID
}

// csrfToken fetches the dashboard and extracts the csrf_token hidden input.
func (ts *testServer) csrfToken(t *testing.T) string {
	t.Helper()
	resp, err := ts.client.Get(ts.srv.URL + "/dashboard")
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	const marker = `name="csrf_token" value="`
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatal("csrf_token not found in dashboard")
	}
	start := idx + len(marker)
	end := strings.Index(body[start:], `"`)
	return body[start : start+end]
}

// postForm is a helper that POSTs form data with a CSRF token.
func (ts *testServer) postForm(t *testing.T, path string, values url.Values) *http.Response {
	t.Helper()
	csrf := ts.csrfToken(t)
	values.Set("csrf_token", csrf)
	resp, err := ts.client.PostForm(ts.srv.URL+path, values)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// ──────────────────────────────────────────────
// Tests
// ──────────────────────────────────────────────

func TestIntegration_LoginFlow(t *testing.T) {
	ts := newTestServer(t)

	// Unauthenticated request to /dashboard should redirect to /login.
	resp, err := ts.client.Get(ts.srv.URL + "/dashboard")
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	resp.Body.Close()
	if !strings.HasSuffix(resp.Request.URL.Path, "/login") {
		t.Errorf("expected redirect to /login, got %s", resp.Request.URL.Path)
	}

	// Login via magic link.
	ts.login(t, "test@example.com")

	// Now /dashboard should be accessible.
	resp, err = ts.client.Get(ts.srv.URL + "/dashboard")
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("dashboard status = %d, want 200", resp.StatusCode)
	}
	if !strings.HasSuffix(resp.Request.URL.Path, "/dashboard") {
		t.Errorf("expected to stay on /dashboard, got %s", resp.Request.URL.Path)
	}
}

func TestIntegration_CredentialSetup(t *testing.T) {
	ts := newTestServer(t)
	userID := ts.login(t, "creds@example.com")

	// Save Garmin credentials.
	resp := ts.postForm(t, "/settings/garmin", url.Values{
		"email":    {"garmin@example.com"},
		"password": {"garminpass"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("garmin save status = %d, want 200", resp.StatusCode)
	}

	// Verify Garmin credentials are stored.
	email, _, err := ts.db.GetGarminCredentials(userID)
	if err != nil {
		t.Fatalf("get garmin creds: %v", err)
	}
	if email != "garmin@example.com" {
		t.Errorf("garmin email = %q, want garmin@example.com", email)
	}

	// Save EFB credentials.
	resp = ts.postForm(t, "/settings/efb", url.Values{
		"username": {"efbuser"},
		"password": {"efbpass"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("efb save status = %d, want 200", resp.StatusCode)
	}

	// Verify EFB credentials are stored.
	username, _, err := ts.db.GetEFBCredentials(userID)
	if err != nil {
		t.Fatalf("get efb creds: %v", err)
	}
	if username != "efbuser" {
		t.Errorf("efb username = %q, want efbuser", username)
	}
}

func TestIntegration_SyncTrigger(t *testing.T) {
	ts := newTestServer(t)
	userID := ts.login(t, "sync@example.com")

	// Set up credentials.
	if err := ts.db.SaveGarminCredentials(userID, "garmin@example.com", "pass"); err != nil {
		t.Fatalf("save garmin: %v", err)
	}
	if err := ts.db.SaveEFBCredentials(userID, "efbuser", "efbpass"); err != nil {
		t.Fatalf("save efb: %v", err)
	}

	// Trigger sync.
	resp := ts.postForm(t, "/sync/trigger", url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("sync trigger status = %d, want 200", resp.StatusCode)
	}

	// Wait for the background sync goroutine to finish.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := ts.db.GetSyncHistory(userID, 1)
		if len(runs) > 0 && runs[0].Status != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify sync completed.
	runs, err := ts.db.GetSyncHistory(userID, 1)
	if err != nil {
		t.Fatalf("get sync history: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected at least 1 sync run")
	}
	if runs[0].Status != "completed" {
		t.Errorf("sync status = %q, want completed", runs[0].Status)
	}
	if runs[0].ActivitiesSynced == 0 {
		t.Error("expected at least 1 activity synced")
	}

	// Verify mock EFB received uploads.
	uploads := ts.efb.Uploads()
	if len(uploads) == 0 {
		t.Error("expected mock EFB to have received uploads")
	}
}

func TestIntegration_SyncHistory(t *testing.T) {
	ts := newTestServer(t)
	userID := ts.login(t, "history@example.com")

	// Set up credentials and run a sync.
	if err := ts.db.SaveGarminCredentials(userID, "garmin@example.com", "pass"); err != nil {
		t.Fatalf("save garmin: %v", err)
	}
	if err := ts.db.SaveEFBCredentials(userID, "efbuser", "efbpass"); err != nil {
		t.Fatalf("save efb: %v", err)
	}

	resp := ts.postForm(t, "/sync/trigger", url.Values{})
	resp.Body.Close()

	// Wait for sync to finish.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := ts.db.GetSyncHistory(userID, 1)
		if len(runs) > 0 && runs[0].Status != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Load sync history page.
	resp, err := ts.client.Get(ts.srv.URL + "/sync/history")
	if err != nil {
		t.Fatalf("get sync history: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("sync history status = %d, want 200", resp.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	if !strings.Contains(body, "completed") {
		t.Error("sync history page should contain 'completed' status")
	}
}
