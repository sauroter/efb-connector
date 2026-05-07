package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Internal endpoint authorization
// ─────────────────────────────────────────────────────────────────────────────

func TestAdminReport_RequiresAuth(t *testing.T) {
	h := newTestHarness(t)

	resp, err := h.client.Get(h.srv.URL + "/internal/admin/report")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestAdminReport_RendersHTML(t *testing.T) {
	h := newTestHarness(t)

	// Seed a single user so the rendered tables are non-empty.
	if _, err := h.db.CreateUser("renders@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/internal/admin/report", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html...", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	html := string(body)

	for _, want := range []string{
		"Admin Report",
		"Funnel",
		"Stuck users",
		"All users",
		"Recent sync failures",
		"Recent activity upload failures",
		"renders@example.com",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestInternalSyncAll_RejectsMissingAuth(t *testing.T) {
	h := newTestHarness(t)

	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/internal/sync/run-all", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestInternalSyncAll_RejectsWrongAuth(t *testing.T) {
	h := newTestHarness(t)

	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/internal/sync/run-all", nil)
	req.Header.Set("Authorization", "Bearer wrong-secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestInternalSyncAll_ReturnsAcceptedAndCompletes(t *testing.T) {
	h := newTestHarness(t)

	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/internal/sync/run-all", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "started" {
		t.Errorf("body.status = %v, want started", body["status"])
	}

	// The detached goroutine should finish quickly with no syncable users
	// in the test harness — poll the status endpoint for up to 2s.
	if !waitForRunAllCompletion(t, h, 2*time.Second) {
		t.Fatalf("run-all never reported in_progress=false")
	}
}

func TestInternalSyncAll_RejectsConcurrentRun(t *testing.T) {
	h := newTestHarness(t)

	// Manually flag a run as in-progress; the second POST should hit 409
	// without spawning a goroutine.
	h.server.runAllMu.Lock()
	h.server.runAllState = runAllState{
		InProgress: true,
		StartedAt:  time.Now(),
		TotalUsers: 5,
	}
	h.server.runAllMu.Unlock()

	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/internal/sync/run-all", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["in_progress"] != true {
		t.Errorf("body.in_progress = %v, want true", body["in_progress"])
	}
	if body["total_users"].(float64) != 5 {
		t.Errorf("body.total_users = %v, want 5", body["total_users"])
	}
}

// TestInternalSyncAll_DetachesFromRequestContext verifies the load-bearing
// fix for the original silent-cron-dropout bug: the goroutine must keep
// running even after the originating HTTP request's context is cancelled.
func TestInternalSyncAll_DetachesFromRequestContext(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.srv.URL+"/internal/sync/run-all", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()

	// Cancel the request context immediately — equivalent to Fly's edge
	// proxy dropping the connection.
	cancel()

	if !waitForRunAllCompletion(t, h, 2*time.Second) {
		t.Fatalf("run-all aborted when request context was cancelled — should have continued in background")
	}

	body := readStatus(t, h)
	if body["error"] != "" {
		t.Errorf("body.error = %q, want empty (run should complete cleanly)", body["error"])
	}
}

func TestInternalSyncAllStatus_RejectsMissingAuth(t *testing.T) {
	h := newTestHarness(t)

	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/internal/sync/run-all/status", nil)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestInternalSyncAllStatus_ReportsIdleBeforeFirstRun(t *testing.T) {
	h := newTestHarness(t)

	body := readStatus(t, h)
	if body["in_progress"] != false {
		t.Errorf("body.in_progress = %v, want false", body["in_progress"])
	}
	// No started_at field on a never-run state.
	if _, ok := body["started_at"]; ok {
		t.Errorf("body.started_at present, expected absent on never-run state")
	}
}

func TestInternalSyncAllStatus_ReportsCompletionAfterRun(t *testing.T) {
	h := newTestHarness(t)

	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/internal/sync/run-all", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, _ := h.client.Do(req)
	resp.Body.Close()

	if !waitForRunAllCompletion(t, h, 2*time.Second) {
		t.Fatalf("run-all never reported in_progress=false")
	}

	body := readStatus(t, h)
	if body["in_progress"] != false {
		t.Errorf("body.in_progress = %v, want false", body["in_progress"])
	}
	if _, ok := body["started_at"]; !ok {
		t.Errorf("body.started_at missing after run")
	}
	if _, ok := body["finished_at"]; !ok {
		t.Errorf("body.finished_at missing after run")
	}
	if body["error"] != "" {
		t.Errorf("body.error = %q, want empty", body["error"])
	}
}

func TestAdminEndpoints_AllRejectMissingAuth(t *testing.T) {
	h := newTestHarness(t)

	cases := []struct {
		method, path string
	}{
		{"GET", "/internal/sync/run-all/status"},
		{"GET", "/internal/admin/status"},
		{"GET", "/internal/admin/users"},
		{"GET", "/internal/admin/users/1/sync-history"},
		{"POST", "/internal/admin/users/1/sync"},
		{"POST", "/internal/admin/users/1/debug-upload"},
		{"POST", "/internal/admin/users/1/efb/revalidate"},
		{"GET", "/internal/admin/errors"},
		{"GET", "/internal/admin/activity-errors"},
		{"GET", "/internal/admin/activity-errors/1"},
		{"GET", "/internal/admin/feedback"},
		{"POST", "/internal/admin/notify-garmin-upgrade"},
		{"POST", "/internal/admin/sync-resend-contacts"},
		{"POST", "/internal/admin/dev/mock-efb/consent-gate"},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			req, _ := http.NewRequest(c.method, h.srv.URL+c.path, nil)
			resp, err := h.client.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", c.method, c.path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s: status = %d, want 401", c.method, c.path, resp.StatusCode)
			}
		})
	}
}

func TestAdminStatus_ReturnsSystemStatsJSON(t *testing.T) {
	h := newTestHarness(t)

	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/internal/admin/status", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}

func TestAdminUserEFBRevalidate_ClearsStaleInvalidFlag(t *testing.T) {
	h := newTestHarness(t)

	u, err := h.db.CreateUser("revalidate@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := h.db.SaveEFBCredentials(u.ID, "efbuser", "efbpass"); err != nil {
		t.Fatalf("save efb credentials: %v", err)
	}
	if err := h.db.InvalidateEFBCredentials(u.ID, "transient"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	url := h.srv.URL + "/internal/admin/users/" + strconv.FormatInt(u.ID, 10) + "/efb/revalidate"
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	valid, err := h.db.GetEFBCredentialsValid(u.ID)
	if err != nil {
		t.Fatalf("GetEFBCredentialsValid: %v", err)
	}
	if !valid {
		t.Errorf("is_valid still false after revalidate")
	}

	// Idempotent: second call also returns 204.
	req2, _ := http.NewRequest(http.MethodPost, url, nil)
	req2.Header.Set("Authorization", "Bearer test-secret")
	resp2, err := h.client.Do(req2)
	if err != nil {
		t.Fatalf("post 2: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Errorf("second call status = %d, want 204", resp2.StatusCode)
	}
}

func TestAdminUserEFBRevalidate_404WhenNoCredentialsRow(t *testing.T) {
	h := newTestHarness(t)

	u, err := h.db.CreateUser("nocreds@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	url := h.srv.URL + "/internal/admin/users/" + strconv.FormatInt(u.ID, 10) + "/efb/revalidate"
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAdminUserEFBRevalidate_400OnInvalidUserID(t *testing.T) {
	h := newTestHarness(t)

	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/internal/admin/users/notanumber/efb/revalidate", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Health endpoint
// ─────────────────────────────────────────────────────────────────────────────

func TestHealth_ReturnsOK(t *testing.T) {
	h := newTestHarness(t)

	resp, err := h.client.Get(h.srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("body = %q, expected status:ok", string(body))
	}
	if !strings.Contains(string(body), `"version":"test"`) {
		t.Errorf("body = %q, expected version:test", string(body))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// waitForRunAllCompletion polls the status endpoint until in_progress=false
// or the deadline elapses. Returns true on completion.
func waitForRunAllCompletion(t *testing.T, h *testHarness, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body := readStatus(t, h)
		if body["in_progress"] == false {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// readStatus performs an authorized GET on the run-all status endpoint and
// returns the decoded JSON body.
func readStatus(t *testing.T, h *testHarness) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/internal/sync/run-all/status", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status endpoint returned %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return body
}
