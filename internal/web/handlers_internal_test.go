package web

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Internal endpoint authorization
// ─────────────────────────────────────────────────────────────────────────────

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

func TestInternalSyncAll_AcceptsCorrectAuth(t *testing.T) {
	h := newTestHarness(t)

	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/internal/sync/run-all", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("content-type = %q, want application/x-ndjson", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	// With no users, the engine emits a single completion line.
	if !strings.Contains(string(body), `"status":"completed"`) {
		t.Errorf("expected NDJSON to include status:completed, got %q", string(body))
	}
}

func TestAdminEndpoints_AllRejectMissingAuth(t *testing.T) {
	h := newTestHarness(t)

	cases := []struct {
		method, path string
	}{
		{"GET", "/internal/admin/status"},
		{"GET", "/internal/admin/users"},
		{"GET", "/internal/admin/users/1/sync-history"},
		{"POST", "/internal/admin/users/1/sync"},
		{"POST", "/internal/admin/users/1/debug-upload"},
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
