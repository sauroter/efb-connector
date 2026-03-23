package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"efb-connector/internal/database"
	"efb-connector/internal/efb"
	"efb-connector/internal/garmin"
	"efb-connector/internal/rivermap"
)

// ──────────────────────────────────────────────
// Test helpers
// ──────────────────────────────────────────────

// testKey is a 32-byte AES key used in all tests.
var testKey = []byte("12345678901234567890123456789012")

// openTestDB opens an in-memory SQLite database with migrations applied.
func openTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:", testKey)
	if err != nil {
		t.Fatalf("Open DB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// mockGarminProvider implements garmin.GarminProvider for testing.
type mockGarminProvider struct {
	activities    []garmin.Activity
	listErr       error
	gpxData       map[string][]byte // activityID → GPX bytes
	downloadErr   map[string]error  // activityID → error
	validateErr   error
	lastListStart time.Time
	lastListEnd   time.Time
}

func (m *mockGarminProvider) ListActivities(_ context.Context, _ garmin.GarminCredentials, start, end time.Time) ([]garmin.Activity, error) {
	m.lastListStart = start
	m.lastListEnd = end
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.activities, nil
}

func (m *mockGarminProvider) DownloadGPX(_ context.Context, _ garmin.GarminCredentials, activityID string) ([]byte, error) {
	if m.downloadErr != nil {
		if err, ok := m.downloadErr[activityID]; ok {
			return nil, err
		}
	}
	if data, ok := m.gpxData[activityID]; ok {
		return data, nil
	}
	return []byte(`<?xml version="1.0"?><gpx></gpx>`), nil
}

func (m *mockGarminProvider) ValidateCredentials(_ context.Context, _ garmin.GarminCredentials) error {
	return m.validateErr
}

// newMockEFBServer creates a test HTTP server simulating the EFB portal.
// It accepts login with username "efbuser" / password "efbpass".
// Uploads return "Datenbank gespeichert" on success.
func newMockEFBServer(t *testing.T) *httptest.Server {
	t.Helper()
	const sessionCookie = "mock-session"

	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		user := r.FormValue("username")
		pass := r.FormValue("password")
		if user == "efbuser" && pass == "efbpass" {
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "1"})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		// Bad credentials.
		http.Redirect(w, r, "/login", http.StatusFound)
	})

	mux.HandleFunc("/interpretation/usersmap", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		hasSession := err == nil && cookie.Value == "1"

		switch r.Method {
		case http.MethodGet:
			if !hasSession {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html>usersmap</html>"))
		case http.MethodPost:
			if !hasSession {
				http.Error(w, "not authenticated", http.StatusForbidden)
				return
			}
			_ = r.ParseMultipartForm(10 << 20)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Datenbank gespeichert"))
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>home</html>"))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newMockEFBServer5xx creates a server that returns 500 on upload.
func newMockEFBServer5xx(t *testing.T) *httptest.Server {
	t.Helper()
	const sessionCookie = "mock-session"

	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "1"})
		http.Redirect(w, r, "/", http.StatusFound)
	})

	mux.HandleFunc("/interpretation/usersmap", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html>usersmap</html>"))
			return
		}
		_ = r.ParseMultipartForm(10 << 20)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newMockEFBServerBadLogin creates a server that always rejects login.
func newMockEFBServerBadLogin(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newMockEFBServerPartialFailure creates a server that fails on the Nth upload.
func newMockEFBServerPartialFailure(t *testing.T, failOnUploadN int) *httptest.Server {
	t.Helper()
	const sessionCookie = "mock-session"
	var uploadCount atomic.Int32

	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "1"})
		http.Redirect(w, r, "/", http.StatusFound)
	})

	mux.HandleFunc("/interpretation/usersmap", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html>usersmap</html>"))
			return
		}
		_ = r.ParseMultipartForm(10 << 20)
		n := int(uploadCount.Add(1))
		if n == failOnUploadN {
			// Simulate a non-5xx upload failure (e.g., missing success marker).
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Upload fehlgeschlagen"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Datenbank gespeichert"))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// noSleep is a sleep function that does nothing, used in tests to avoid delays.
func noSleep(_, _ time.Duration) {}

// setupUser creates a user with both Garmin and EFB credentials in the DB.
func setupUser(t *testing.T, db *database.DB) *database.User {
	t.Helper()
	u, err := db.CreateUser(fmt.Sprintf("user-%d@example.com", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.SaveGarminCredentials(u.ID, "garmin@example.com", "garminpass"); err != nil {
		t.Fatalf("SaveGarminCredentials: %v", err)
	}
	if err := db.SaveEFBCredentials(u.ID, "efbuser", "efbpass"); err != nil {
		t.Fatalf("SaveEFBCredentials: %v", err)
	}
	return u
}

// makeActivities creates N test activities with sequential IDs.
func makeActivities(n int) []garmin.Activity {
	acts := make([]garmin.Activity, n)
	for i := range n {
		startTime := time.Now().Add(-time.Duration(i) * time.Hour)
		acts[i] = garmin.Activity{
			ProviderID:   fmt.Sprintf("act-%d", i+1),
			Name:         fmt.Sprintf("Paddling Session %d", i+1),
			Type:         "kayaking",
			Date:         startTime,
			StartTime:    startTime,
			DurationSecs: 3600,
			DistanceM:    5000,
		}
	}
	return acts
}

// newEngine creates a SyncEngine with noSleep for testing.
func newEngine(db *database.DB, gp garmin.GarminProvider, ec *efb.EFBClient) *SyncEngine {
	e := NewSyncEngine(db, gp, ec, discardLogger())
	e.sleepFunc = noSleep
	return e
}

// discardLogger returns a logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.Default()
}

// ──────────────────────────────────────────────
// Tests
// ──────────────────────────────────────────────

func TestSyncUser_HappyPath(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{activities: makeActivities(3)}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("SyncUser: %v", err)
	}

	run, err := db.GetSyncRun(runID)
	if err != nil {
		t.Fatalf("GetSyncRun: %v", err)
	}
	if run == nil {
		t.Fatal("expected sync run, got nil")
	}
	if run.Status != "completed" {
		t.Errorf("status = %q, want completed", run.Status)
	}
	if run.ActivitiesFound != 3 {
		t.Errorf("found = %d, want 3", run.ActivitiesFound)
	}
	if run.ActivitiesSynced != 3 {
		t.Errorf("synced = %d, want 3", run.ActivitiesSynced)
	}
	if run.ActivitiesSkipped != 0 {
		t.Errorf("skipped = %d, want 0", run.ActivitiesSkipped)
	}
	if run.ActivitiesFailed != 0 {
		t.Errorf("failed = %d, want 0", run.ActivitiesFailed)
	}

	// Verify activities are recorded as synced.
	for _, act := range gp.activities {
		synced, err := db.IsActivitySynced(user.ID, act.ProviderID)
		if err != nil {
			t.Fatalf("IsActivitySynced(%q): %v", act.ProviderID, err)
		}
		if !synced {
			t.Errorf("activity %q should be marked as synced", act.ProviderID)
		}
	}
}

func TestSyncUser_Idempotency(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{activities: makeActivities(3)}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	// First sync.
	runID1, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("first SyncUser: %v", err)
	}
	run1, _ := db.GetSyncRun(runID1)
	if run1.ActivitiesSynced != 3 {
		t.Fatalf("first run synced = %d, want 3", run1.ActivitiesSynced)
	}

	// Second sync: same activities, should all be skipped.
	runID2, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("second SyncUser: %v", err)
	}

	run2, _ := db.GetSyncRun(runID2)
	if run2.Status != "completed" {
		t.Errorf("status = %q, want completed", run2.Status)
	}
	if run2.ActivitiesSynced != 0 {
		t.Errorf("second run synced = %d, want 0", run2.ActivitiesSynced)
	}
	if run2.ActivitiesSkipped != 3 {
		t.Errorf("second run skipped = %d, want 3", run2.ActivitiesSkipped)
	}
}

func TestSyncUser_RetryLogic(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	// First sync: activity download fails.
	gp := &mockGarminProvider{
		activities: makeActivities(1),
		downloadErr: map[string]error{
			"act-1": fmt.Errorf("garmin: temporary network error"),
		},
	}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID1, _ := engine.SyncUser(context.Background(), user.ID, "manual")
	run1, _ := db.GetSyncRun(runID1)
	if run1.ActivitiesFailed != 1 {
		t.Fatalf("first run failed = %d, want 1", run1.ActivitiesFailed)
	}

	// Second sync: fix the download, activity should be retried.
	gp.downloadErr = nil
	runID2, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("second SyncUser: %v", err)
	}

	run2, _ := db.GetSyncRun(runID2)
	if run2.ActivitiesSynced != 1 {
		t.Errorf("second run synced = %d, want 1", run2.ActivitiesSynced)
	}
}

func TestSyncUser_RetryCapAt3(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{
		activities: makeActivities(1),
		downloadErr: map[string]error{
			"act-1": fmt.Errorf("garmin: persistent error"),
		},
	}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	// Run sync 4 times with persistent failure.
	for i := range 4 {
		engine.SyncUser(context.Background(), user.ID, "manual")
		_ = i
	}

	// After 3 retries (initial + 3 retries => retry_count reaches 3),
	// the activity should be marked as permanent_failure and not retried.
	gp.downloadErr = nil // fix the error
	runID, _ := engine.SyncUser(context.Background(), user.ID, "manual")
	run, _ := db.GetSyncRun(runID)

	// The activity should not appear as something to sync (it is a permanent failure).
	// But it won't be "skipped" either since it's not status=success.
	// It should simply not be attempted.
	if run.ActivitiesSynced != 0 {
		t.Errorf("synced = %d, want 0 (permanent failure should not be retried)", run.ActivitiesSynced)
	}
}

func TestSyncUser_GarminAuthFailure(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{
		listErr: garmin.ErrGarminAuth,
	}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err == nil {
		t.Fatal("expected error for garmin auth failure")
	}

	run, _ := db.GetSyncRun(runID)
	if run.Status != "failed" {
		t.Errorf("status = %q, want failed", run.Status)
	}

	// Verify garmin credentials are invalidated (user should no longer be syncable).
	users, _ := db.GetSyncableUsers()
	for _, u := range users {
		if u.ID == user.ID {
			t.Error("user should not be syncable after garmin auth failure")
		}
	}
}

func TestSyncUser_GarminMFARequired(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{
		listErr: garmin.ErrGarminMFARequired,
	}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	_, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err == nil {
		t.Fatal("expected error for garmin MFA required")
	}

	// Verify garmin credentials are invalidated.
	users, _ := db.GetSyncableUsers()
	for _, u := range users {
		if u.ID == user.ID {
			t.Error("user should not be syncable after garmin MFA required")
		}
	}
}

func TestSyncUser_EFB5xx(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer5xx(t)

	gp := &mockGarminProvider{activities: makeActivities(3)}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err == nil {
		t.Fatal("expected error for EFB 5xx")
	}
	if !strings.Contains(err.Error(), "5xx") {
		t.Errorf("error should mention 5xx, got: %v", err)
	}

	run, _ := db.GetSyncRun(runID)
	if run.Status != "failed" {
		t.Errorf("status = %q, want failed", run.Status)
	}
	// First upload fails with 5xx, remaining 2 are marked failed too.
	if run.ActivitiesFailed != 3 {
		t.Errorf("failed = %d, want 3", run.ActivitiesFailed)
	}
	if run.ActivitiesSynced != 0 {
		t.Errorf("synced = %d, want 0", run.ActivitiesSynced)
	}
}

func TestSyncUser_EFBLoginFailure(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServerBadLogin(t)

	gp := &mockGarminProvider{activities: makeActivities(2)}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err == nil {
		t.Fatal("expected error for EFB login failure")
	}

	run, _ := db.GetSyncRun(runID)
	if run.Status != "failed" {
		t.Errorf("status = %q, want failed", run.Status)
	}

	// Verify EFB credentials are invalidated.
	users, _ := db.GetSyncableUsers()
	for _, u := range users {
		if u.ID == user.ID {
			t.Error("user should not be syncable after EFB login failure")
		}
	}
}

func TestSyncUser_NoNewActivities(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{activities: nil}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("SyncUser: %v", err)
	}

	run, _ := db.GetSyncRun(runID)
	if run.Status != "completed" {
		t.Errorf("status = %q, want completed", run.Status)
	}
	if run.ActivitiesFound != 0 {
		t.Errorf("found = %d, want 0", run.ActivitiesFound)
	}
}

func TestSyncUser_MixedResults(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	// Second upload fails (non-5xx).
	srv := newMockEFBServerPartialFailure(t, 2)

	gp := &mockGarminProvider{activities: makeActivities(3)}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	// err should be nil since the sync partially succeeded (no fatal error).
	if err != nil {
		t.Fatalf("SyncUser: %v", err)
	}

	run, _ := db.GetSyncRun(runID)
	if run.Status != "partial" {
		t.Errorf("status = %q, want partial", run.Status)
	}
	if run.ActivitiesSynced != 2 {
		t.Errorf("synced = %d, want 2", run.ActivitiesSynced)
	}
	if run.ActivitiesFailed != 1 {
		t.Errorf("failed = %d, want 1", run.ActivitiesFailed)
	}
}

func TestSyncUser_GPXDownloadFailure(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{
		activities: makeActivities(3),
		downloadErr: map[string]error{
			"act-2": fmt.Errorf("garmin: download failed"),
		},
	}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("SyncUser: %v", err)
	}

	run, _ := db.GetSyncRun(runID)
	if run.Status != "partial" {
		t.Errorf("status = %q, want partial", run.Status)
	}
	if run.ActivitiesSynced != 2 {
		t.Errorf("synced = %d, want 2", run.ActivitiesSynced)
	}
	if run.ActivitiesFailed != 1 {
		t.Errorf("failed = %d, want 1", run.ActivitiesFailed)
	}
}

func TestSyncAllUsers(t *testing.T) {
	db := openTestDB(t)
	srv := newMockEFBServer(t)

	// Create two syncable users.
	u1 := setupUser(t, db)
	u2 := setupUser(t, db)

	gp := &mockGarminProvider{activities: makeActivities(2)}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	err := engine.SyncAllUsers(context.Background())
	if err != nil {
		t.Fatalf("SyncAllUsers: %v", err)
	}

	// Verify both users got synced.
	for _, uid := range []int64{u1.ID, u2.ID} {
		history, err := db.GetSyncHistory(uid, 1)
		if err != nil {
			t.Fatalf("GetSyncHistory(%d): %v", uid, err)
		}
		if len(history) != 1 {
			t.Errorf("user %d: expected 1 sync run, got %d", uid, len(history))
			continue
		}
		if history[0].Status != "completed" {
			t.Errorf("user %d: status = %q, want completed", uid, history[0].Status)
		}
		if history[0].ActivitiesSynced != 2 {
			t.Errorf("user %d: synced = %d, want 2", uid, history[0].ActivitiesSynced)
		}
	}
}

func TestSyncAllUsers_ContextCancelled(t *testing.T) {
	db := openTestDB(t)
	srv := newMockEFBServer(t)

	// Create users but cancel context immediately.
	_ = setupUser(t, db)
	_ = setupUser(t, db)

	gp := &mockGarminProvider{activities: makeActivities(1)}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := engine.SyncAllUsers(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestSyncUser_UserNotFound(t *testing.T) {
	db := openTestDB(t)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	_, err := engine.SyncUser(context.Background(), 99999, "manual")
	if err == nil {
		t.Fatal("expected error for non-existent user")
	}
}

func TestSyncUserWithOptions_CustomRange(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{activities: makeActivities(2)}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	start := time.Date(2025, 1, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 1, 20, 0, 0, 0, 0, time.UTC)

	runID, err := engine.SyncUserWithOptions(context.Background(), user.ID, "manual_custom", SyncOptions{
		Start: start,
		End:   end,
	})
	if err != nil {
		t.Fatalf("SyncUserWithOptions: %v", err)
	}

	run, _ := db.GetSyncRun(runID)
	if run.Status != "completed" {
		t.Errorf("status = %q, want completed", run.Status)
	}

	// Verify the custom range was passed to the Garmin provider.
	if !gp.lastListStart.Equal(start) {
		t.Errorf("ListActivities start = %v, want %v", gp.lastListStart, start)
	}
	if !gp.lastListEnd.Equal(end) {
		t.Errorf("ListActivities end = %v, want %v", gp.lastListEnd, end)
	}
}

func TestSyncUserWithOptions_DefaultFallback(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{activities: nil}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	before := time.Now()
	_, err := engine.SyncUserWithOptions(context.Background(), user.ID, "manual", SyncOptions{})
	if err != nil {
		t.Fatalf("SyncUserWithOptions: %v", err)
	}

	// Default SyncDays is 3. The start should be ~3 days before now.
	expectedStart := before.AddDate(0, 0, -user.SyncDays)
	diff := gp.lastListStart.Sub(expectedStart)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("ListActivities start = %v, expected ~%v (diff=%v)", gp.lastListStart, expectedStart, diff)
	}
}

func TestSyncUserWithOptions_InvalidRange(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	// Start after end.
	_, err := engine.SyncUserWithOptions(context.Background(), user.ID, "manual_custom", SyncOptions{
		Start: time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2025, 3, 10, 0, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("expected error for start > end")
	}
	if !errors.Is(err, ErrInvalidDateRange) {
		t.Errorf("error = %v, want ErrInvalidDateRange", err)
	}
}

func TestSyncUserWithOptions_RangeTooLarge(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	gp := &mockGarminProvider{}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	// Range of ~400 days exceeds the 365-day limit.
	_, err := engine.SyncUserWithOptions(context.Background(), user.ID, "manual_custom", SyncOptions{
		Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("expected error for range > 365 days")
	}
	if !errors.Is(err, ErrInvalidDateRange) {
		t.Errorf("error = %v, want ErrInvalidDateRange", err)
	}
}

// ──────────────────────────────────────────────
// Mock EFB provider for trip-creation tests
// ──────────────────────────────────────────────

// mockEFBProvider implements efb.EFBProvider with controllable behaviour.
type mockEFBProvider struct {
	loginErr          error
	uploadErr         error
	findTrackResult   string
	findTrackErr      error
	createTripErr     error

	// Call tracking.
	findTrackCalled   bool
	findTrackFilename string
	createTripCalled  bool
	createTripTrackID string
	lastEnrichment    *efb.TripEnrichment
	uploadCount       int
}

func (m *mockEFBProvider) Login(_ context.Context, _, _ string) error {
	return m.loginErr
}

func (m *mockEFBProvider) Upload(_ context.Context, _ []byte, _ string) error {
	m.uploadCount++
	return m.uploadErr
}

func (m *mockEFBProvider) ValidateCredentials(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockEFBProvider) FindUnassociatedTrack(_ context.Context, gpxFilename string) (string, error) {
	m.findTrackCalled = true
	m.findTrackFilename = gpxFilename
	return m.findTrackResult, m.findTrackErr
}

func (m *mockEFBProvider) CreateTripFromTrack(_ context.Context, trackID string, _ time.Time, _ float64, enrichment *efb.TripEnrichment) error {
	m.createTripCalled = true
	m.createTripTrackID = trackID
	m.lastEnrichment = enrichment
	return m.createTripErr
}

// newEngineWithProvider creates a SyncEngine with a custom EFB provider and noSleep.
func newEngineWithProvider(db *database.DB, gp garmin.GarminProvider, ep efb.EFBProvider) *SyncEngine {
	e := NewSyncEngine(db, gp, ep, discardLogger())
	e.sleepFunc = noSleep
	return e
}

// ──────────────────────────────────────────────
// Auto-create trip tests
// ──────────────────────────────────────────────

func TestSync_AutoCreateTrips_Enabled(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)

	// Enable auto-create trips for this user.
	if err := db.UpdateAutoCreateTrips(user.ID, true); err != nil {
		t.Fatalf("UpdateAutoCreateTrips: %v", err)
	}

	mockEFB := &mockEFBProvider{
		findTrackResult: "track-123",
	}
	gp := &mockGarminProvider{activities: makeActivities(1)}
	engine := newEngineWithProvider(db, gp, mockEFB)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("SyncUser: %v", err)
	}

	run, err := db.GetSyncRun(runID)
	if err != nil {
		t.Fatalf("GetSyncRun: %v", err)
	}
	if run.ActivitiesSynced != 1 {
		t.Errorf("synced = %d, want 1", run.ActivitiesSynced)
	}
	if run.TripsCreated != 1 {
		t.Errorf("trips_created = %d, want 1", run.TripsCreated)
	}

	// Verify FindUnassociatedTrack was called with the correct filename.
	if !mockEFB.findTrackCalled {
		t.Error("expected FindUnassociatedTrack to be called")
	}
	expectedFilename := "garmin_act-1.gpx"
	if mockEFB.findTrackFilename != expectedFilename {
		t.Errorf("FindUnassociatedTrack filename = %q, want %q", mockEFB.findTrackFilename, expectedFilename)
	}

	// Verify CreateTripFromTrack was called with the correct track ID.
	if !mockEFB.createTripCalled {
		t.Error("expected CreateTripFromTrack to be called")
	}
	if mockEFB.createTripTrackID != "track-123" {
		t.Errorf("CreateTripFromTrack trackID = %q, want %q", mockEFB.createTripTrackID, "track-123")
	}
}

func TestSync_AutoCreateTrips_Disabled(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	// AutoCreateTrips is false by default — no need to set it.

	mockEFB := &mockEFBProvider{
		findTrackResult: "track-123",
	}
	gp := &mockGarminProvider{activities: makeActivities(1)}
	engine := newEngineWithProvider(db, gp, mockEFB)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("SyncUser: %v", err)
	}

	run, err := db.GetSyncRun(runID)
	if err != nil {
		t.Fatalf("GetSyncRun: %v", err)
	}
	if run.ActivitiesSynced != 1 {
		t.Errorf("synced = %d, want 1", run.ActivitiesSynced)
	}
	if run.TripsCreated != 0 {
		t.Errorf("trips_created = %d, want 0", run.TripsCreated)
	}

	// Verify FindUnassociatedTrack was NOT called.
	if mockEFB.findTrackCalled {
		t.Error("expected FindUnassociatedTrack NOT to be called when AutoCreateTrips is disabled")
	}
	if mockEFB.createTripCalled {
		t.Error("expected CreateTripFromTrack NOT to be called when AutoCreateTrips is disabled")
	}
}

func TestSync_TripCreationFailure_DoesNotFailSync(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)

	// Enable auto-create trips.
	if err := db.UpdateAutoCreateTrips(user.ID, true); err != nil {
		t.Fatalf("UpdateAutoCreateTrips: %v", err)
	}

	mockEFB := &mockEFBProvider{
		findTrackResult: "track-456",
		createTripErr:   fmt.Errorf("efb: trip form submission failed"),
	}
	gp := &mockGarminProvider{activities: makeActivities(2)}
	engine := newEngineWithProvider(db, gp, mockEFB)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("SyncUser: %v (trip creation failure should not fail sync)", err)
	}

	run, err := db.GetSyncRun(runID)
	if err != nil {
		t.Fatalf("GetSyncRun: %v", err)
	}

	// Both activities should be reported as successfully synced (uploaded),
	// even though trip creation failed.
	if run.Status != "completed" {
		t.Errorf("status = %q, want completed", run.Status)
	}
	if run.ActivitiesSynced != 2 {
		t.Errorf("synced = %d, want 2", run.ActivitiesSynced)
	}
	if run.ActivitiesFailed != 0 {
		t.Errorf("failed = %d, want 0", run.ActivitiesFailed)
	}

	// Verify trip creation was attempted.
	if !mockEFB.createTripCalled {
		t.Error("expected CreateTripFromTrack to be called")
	}
}

func TestSync_RivermapEnrichment(t *testing.T) {
	// Set up a mock Rivermap server that returns a section matching
	// the test activity's start coordinates, plus gauge readings.
	mux := http.NewServeMux()

	// Sections endpoint: return one section near the test activity coords.
	type sectionJSON struct {
		ID          string            `json:"id"`
		River       map[string]string `json:"river"`
		SectionName map[string]struct {
			From          string `json:"from"`
			To            string `json:"to"`
			FormattedName string `json:"formattedName"`
		} `json:"sectionName"`
		Grade         string     `json:"grade"`
		SpotGrades    []string   `json:"spotGrades"`
		PutInLatLng   [2]float64 `json:"putInLatLng"`
		TakeOutLatLng [2]float64 `json:"takeOutLatLng"`
		Calibration   *struct {
			StationID string  `json:"stationId"`
			Unit      string  `json:"unit"`
			LW        float64 `json:"lw"`
			MW        float64 `json:"mw"`
			HW        float64 `json:"hw"`
		} `json:"calibration"`
	}

	sectionsResp := struct {
		Sections []sectionJSON `json:"sections"`
	}{
		Sections: []sectionJSON{
			{
				ID:    "test-sec-1",
				River: map[string]string{"de": "Saalach", "en": "Saalach"},
				SectionName: map[string]struct {
					From          string `json:"from"`
					To            string `json:"to"`
					FormattedName string `json:"formattedName"`
				}{
					"de": {From: "Lofer", To: "Scheffsnoth"},
				},
				Grade:      "III-IV",
				SpotGrades: []string{"V"},
				// Coordinates in micro-degrees (value * 1e6).
				// 47.58 => 47580000, 12.70 => 12700000
				PutInLatLng:   [2]float64{47580000, 12700000},
				TakeOutLatLng: [2]float64{47600000, 12710000},
				Calibration: &struct {
					StationID string  `json:"stationId"`
					Unit      string  `json:"unit"`
					LW        float64 `json:"lw"`
					MW        float64 `json:"mw"`
					HW        float64 `json:"hw"`
				}{
					StationID: "station-1",
					Unit:      "cm",
					LW:        30,
					MW:        60,
					HW:        120,
				},
			},
		},
	}

	sectionsBody, _ := json.Marshal(sectionsResp)

	mux.HandleFunc("/v2/sections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(sectionsBody) //nolint:errcheck
	})

	// Readings endpoint: return gauge readings near the activity's start time.
	mux.HandleFunc("/v2/stations/station-1/readings", func(w http.ResponseWriter, r *http.Request) {
		// Return a reading at a fixed timestamp.
		type readingJSON struct {
			Ts int64   `json:"ts"`
			V  float64 `json:"v"`
		}
		type readingsResp struct {
			Readings map[string]map[string][]readingJSON `json:"readings"`
		}
		// Use unix timestamp 0 + offset; the actual value doesn't matter much
		// as long as it's within the query window.
		resp := readingsResp{
			Readings: map[string]map[string][]readingJSON{
				"station-1": {
					"cm":  {{Ts: time.Now().Unix(), V: 47}},
					"m3s": {{Ts: time.Now().Unix(), V: 12.3}},
				},
			},
		}
		body, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body) //nolint:errcheck
	})

	rivermapSrv := httptest.NewServer(mux)
	t.Cleanup(rivermapSrv.Close)

	// Create the Rivermap client and load its cache.
	rmClient := rivermap.NewClient("test-key", rivermapSrv.URL, "", slog.Default())
	if err := rmClient.RefreshCache(context.Background()); err != nil {
		t.Fatalf("RefreshCache: %v", err)
	}

	// Set up database and user.
	db := openTestDB(t)
	user := setupUser(t, db)
	if err := db.UpdateAutoCreateTrips(user.ID, true); err != nil {
		t.Fatalf("UpdateAutoCreateTrips: %v", err)
	}

	// Create activities with coordinates matching the Rivermap section.
	acts := []garmin.Activity{
		{
			ProviderID:   "act-rm-1",
			Name:         "River Run",
			Type:         "kayaking",
			Date:         time.Now().Add(-time.Hour),
			StartTime:    time.Now().Add(-time.Hour),
			DurationSecs: 3600,
			DistanceM:    5000,
			StartLat:     47.58,
			StartLng:     12.70,
		},
	}

	mockEFB := &mockEFBProvider{
		findTrackResult: "track-enriched",
	}
	gp := &mockGarminProvider{activities: acts}
	engine := newEngineWithProvider(db, gp, mockEFB)
	engine.SetRivermapClient(rmClient)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("SyncUser: %v", err)
	}

	run, err := db.GetSyncRun(runID)
	if err != nil {
		t.Fatalf("GetSyncRun: %v", err)
	}
	if run.ActivitiesSynced != 1 {
		t.Errorf("synced = %d, want 1", run.ActivitiesSynced)
	}
	if run.TripsCreated != 1 {
		t.Errorf("trips_created = %d, want 1", run.TripsCreated)
	}

	// Verify enrichment was passed.
	if !mockEFB.createTripCalled {
		t.Fatal("expected CreateTripFromTrack to be called")
	}
	if mockEFB.lastEnrichment == nil {
		t.Fatal("expected enrichment to be non-nil")
	}

	en := mockEFB.lastEnrichment
	if en.SectionName != "Saalach [Lofer - Scheffsnoth]" {
		t.Errorf("SectionName = %q, want %q", en.SectionName, "Saalach [Lofer - Scheffsnoth]")
	}
	if en.Grade != "III-IV" {
		t.Errorf("Grade = %q, want %q", en.Grade, "III-IV")
	}
	if len(en.SpotGrades) != 1 || en.SpotGrades[0] != "V" {
		t.Errorf("SpotGrades = %v, want [V]", en.SpotGrades)
	}
	if en.GaugeName != "station-1" {
		t.Errorf("GaugeName = %q, want %q", en.GaugeName, "station-1")
	}
	if en.GaugeReading != "47 cm" {
		t.Errorf("GaugeReading = %q, want %q", en.GaugeReading, "47 cm")
	}
	if en.GaugeFlow != "12.3 m3s" {
		t.Errorf("GaugeFlow = %q, want %q", en.GaugeFlow, "12.3 m3s")
	}
	if en.WaterLevel != "Medium water" {
		t.Errorf("WaterLevel = %q, want %q", en.WaterLevel, "Medium water")
	}
}

func TestSync_RivermapEnrichment_NoSectionMatch(t *testing.T) {
	// Rivermap server returns a section far from the activity coordinates.
	mux := http.NewServeMux()

	sectionsBody, _ := json.Marshal(struct {
		Sections []struct {
			ID            string            `json:"id"`
			River         map[string]string `json:"river"`
			Grade         string            `json:"grade"`
			PutInLatLng   [2]float64        `json:"putInLatLng"`
			TakeOutLatLng [2]float64        `json:"takeOutLatLng"`
		} `json:"sections"`
	}{
		Sections: []struct {
			ID            string            `json:"id"`
			River         map[string]string `json:"river"`
			Grade         string            `json:"grade"`
			PutInLatLng   [2]float64        `json:"putInLatLng"`
			TakeOutLatLng [2]float64        `json:"takeOutLatLng"`
		}{
			{
				ID:            "far-sec",
				River:         map[string]string{"de": "Isar"},
				Grade:         "II",
				PutInLatLng:   [2]float64{48137000, 11576000}, // Munich area
				TakeOutLatLng: [2]float64{48200000, 11600000},
			},
		},
	})

	mux.HandleFunc("/v2/sections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(sectionsBody) //nolint:errcheck
	})

	rivermapSrv := httptest.NewServer(mux)
	t.Cleanup(rivermapSrv.Close)

	rmClient := rivermap.NewClient("test-key", rivermapSrv.URL, "", slog.Default())
	if err := rmClient.RefreshCache(context.Background()); err != nil {
		t.Fatalf("RefreshCache: %v", err)
	}

	db := openTestDB(t)
	user := setupUser(t, db)
	if err := db.UpdateAutoCreateTrips(user.ID, true); err != nil {
		t.Fatalf("UpdateAutoCreateTrips: %v", err)
	}

	// Activity is in Austria, but section is in Munich -- too far.
	acts := []garmin.Activity{
		{
			ProviderID:   "act-no-match",
			Name:         "Paddle",
			Type:         "kayaking",
			Date:         time.Now().Add(-time.Hour),
			StartTime:    time.Now().Add(-time.Hour),
			DurationSecs: 3600,
			DistanceM:    5000,
			StartLat:     47.58,
			StartLng:     12.70,
		},
	}

	mockEFB := &mockEFBProvider{
		findTrackResult: "track-no-enrich",
	}
	gp := &mockGarminProvider{activities: acts}
	engine := newEngineWithProvider(db, gp, mockEFB)
	engine.SetRivermapClient(rmClient)

	_, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("SyncUser: %v", err)
	}

	// Trip should be created but WITHOUT enrichment.
	if !mockEFB.createTripCalled {
		t.Fatal("expected CreateTripFromTrack to be called")
	}
	if mockEFB.lastEnrichment != nil {
		t.Errorf("expected nil enrichment for non-matching section, got %+v", mockEFB.lastEnrichment)
	}
}

func TestIsServer5xxError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("some other error"), false},
		{fmt.Errorf("efb: upload failed with status 500: Internal Server Error"), true},
		{fmt.Errorf("efb: upload failed with status 503: Service Unavailable"), true},
		{fmt.Errorf("efb: upload failed with status 403: Forbidden"), false},
		{fmt.Errorf("efb: upload failed with status 200: OK"), false},
	}

	for _, tt := range tests {
		got := isServer5xxError(tt.err)
		if got != tt.want {
			t.Errorf("isServer5xxError(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
