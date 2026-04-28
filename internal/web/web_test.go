package web

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"efb-connector/internal/auth"
	"efb-connector/internal/database"
	"efb-connector/internal/efb"
	"efb-connector/internal/garmin"
	syncsvc "efb-connector/internal/sync"
	"efb-connector/internal/testutil"
)

// noRedirectClient returns an HTTP client that does NOT follow redirects, so
// tests can assert on 303 responses directly.
func noRedirectClient(jar http.CookieJar) *http.Client {
	return &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// testHarness bundles the dependencies needed to drive HTTP handlers in tests.
type testHarness struct {
	srv    *httptest.Server
	db     *database.DB
	auth   *auth.AuthService
	garmin *garmin.MockGarminProvider
	efb    *efb.MockEFBProvider
	server *Server
	client *http.Client // follows redirects, has cookie jar
	raw    *http.Client // does not follow redirects
}

// newTestHarness boots an in-memory web server backed by mock Garmin and EFB
// providers. It exposes both a redirect-following and a redirect-blocking
// HTTP client, plus direct access to the database and mocks.
func newTestHarness(t *testing.T) *testHarness {
	t.Helper()

	db := testutil.OpenTestDB(t)
	logger := slog.New(slog.NewTextHandler(testWriter{t: t}, nil))

	authService := auth.NewAuthService(db, "placeholder", "", "", testutil.TestKey)
	rateLimiter := auth.NewRateLimiter()
	gp := garmin.NewMockGarminProvider()
	ep := efb.NewMockEFBProvider(logger)
	syncEngine := syncsvc.NewSyncEngine(db, gp, func() efb.EFBProvider { return ep }, logger, syncsvc.WithoutSleep())

	s, err := NewServer(ServerDeps{
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
		Version:        "test",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ts := httptest.NewTLSServer(s.Routes())
	t.Cleanup(ts.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}

	return &testHarness{
		srv:    ts,
		db:     db,
		auth:   authService,
		garmin: gp,
		efb:    ep,
		server: s,
		client: client,
		raw:    noRedirectClient(jar),
	}
}

// testWriter forwards slog output to t.Log, suppressing it from stdout while
// still surfacing on test failure for debugging.
type testWriter struct {
	t *testing.T
}

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
