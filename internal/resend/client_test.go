package resend

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// mockResendServer records requests and returns canned responses.
type mockResendServer struct {
	mu       sync.Mutex
	requests []mockRequest
	server   *httptest.Server
}

type mockRequest struct {
	Method string
	Path   string
	Body   string
}

func newMockResendServer() *mockResendServer {
	m := &mockResendServer{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := ""
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			body = string(b)
		}

		m.mu.Lock()
		m.requests = append(m.requests, mockRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   body,
		})
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		// Route responses based on method + path patterns.
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/contacts":
			json.NewEncoder(w).Encode(map[string]string{"object": "contact", "id": "contact-123"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/segments/"):
			json.NewEncoder(w).Encode(map[string]string{"id": "seg-assign-123"})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/segments/"):
			json.NewEncoder(w).Encode(map[string]string{"id": "seg-remove-123"})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/contacts/"):
			json.NewEncoder(w).Encode(map[string]string{"object": "contact", "id": "del-123"})
		case r.Method == http.MethodPost && r.URL.Path == "/segments":
			json.NewEncoder(w).Encode(map[string]string{"object": "segment", "id": "seg-new-123", "name": "test"})
		case r.Method == http.MethodGet && r.URL.Path == "/segments":
			json.NewEncoder(w).Encode(map[string]any{
				"object":   "list",
				"has_more": false,
				"data": []map[string]string{
					{"id": "seg-1", "name": "Active Syncers"},
					{"id": "seg-2", "name": "Needs Setup"},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/templates":
			json.NewEncoder(w).Encode(map[string]string{"id": "tmpl-123", "object": "template"})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/templates/"):
			json.NewEncoder(w).Encode(map[string]string{"id": "tmpl-123", "object": "template"})
		case r.Method == http.MethodGet && r.URL.Path == "/templates":
			json.NewEncoder(w).Encode(map[string]any{
				"object":   "list",
				"has_more": false,
				"data": []map[string]string{
					{"id": "tmpl-1", "name": "Garmin Upgrade DE", "alias": "garmin-upgrade-de"},
				},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publish"):
			json.NewEncoder(w).Encode(map[string]string{"id": "tmpl-123", "object": "template"})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}
	}))
	return m
}

func (m *mockResendServer) close() { m.server.Close() }

func (m *mockResendServer) getRequests() []mockRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]mockRequest, len(m.requests))
	copy(cp, m.requests)
	return cp
}

// setEndpoints overrides package-level endpoint vars to point at the mock
// server and returns a cleanup function that restores them.
func (m *mockResendServer) setEndpoints() func() {
	origContacts := contactsEndpoint
	origSegments := segmentsEndpoint
	origTemplates := templatesEndpoint

	contactsEndpoint = m.server.URL + "/contacts"
	segmentsEndpoint = m.server.URL + "/segments"
	templatesEndpoint = m.server.URL + "/templates"

	return func() {
		contactsEndpoint = origContacts
		segmentsEndpoint = origSegments
		templatesEndpoint = origTemplates
	}
}

func TestCreateContact(t *testing.T) {
	m := newMockResendServer()
	defer m.close()
	restore := m.setEndpoints()
	defer restore()

	c := NewClient("re_live_test", testLogger())

	err := c.CreateContact("user@example.com", map[string]string{"preferred_lang": "de"})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}

	reqs := m.getRequests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Method != http.MethodPost || reqs[0].Path != "/contacts" {
		t.Errorf("unexpected request: %s %s", reqs[0].Method, reqs[0].Path)
	}
	if !strings.Contains(reqs[0].Body, `"email":"user@example.com"`) {
		t.Errorf("body missing email: %s", reqs[0].Body)
	}
	if !strings.Contains(reqs[0].Body, `"preferred_lang":"de"`) {
		t.Errorf("body missing properties: %s", reqs[0].Body)
	}
}

func TestAddToSegment(t *testing.T) {
	m := newMockResendServer()
	defer m.close()
	restore := m.setEndpoints()
	defer restore()

	c := NewClient("re_live_test", testLogger())

	err := c.AddToSegment("user@example.com", "seg-abc")
	if err != nil {
		t.Fatalf("AddToSegment: %v", err)
	}

	reqs := m.getRequests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Method != http.MethodPost {
		t.Errorf("expected POST, got %s", reqs[0].Method)
	}
	if !strings.Contains(reqs[0].Path, "/contacts/user@example.com/segments/seg-abc") {
		t.Errorf("unexpected path: %s", reqs[0].Path)
	}
}

func TestRemoveFromSegment(t *testing.T) {
	m := newMockResendServer()
	defer m.close()
	restore := m.setEndpoints()
	defer restore()

	c := NewClient("re_live_test", testLogger())

	err := c.RemoveFromSegment("user@example.com", "seg-abc")
	if err != nil {
		t.Fatalf("RemoveFromSegment: %v", err)
	}

	reqs := m.getRequests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Method != http.MethodDelete {
		t.Errorf("expected DELETE, got %s", reqs[0].Method)
	}
}

func TestDeleteContact(t *testing.T) {
	m := newMockResendServer()
	defer m.close()
	restore := m.setEndpoints()
	defer restore()

	c := NewClient("re_live_test", testLogger())

	err := c.DeleteContact("user@example.com")
	if err != nil {
		t.Fatalf("DeleteContact: %v", err)
	}

	reqs := m.getRequests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Method != http.MethodDelete {
		t.Errorf("expected DELETE, got %s", reqs[0].Method)
	}
	if reqs[0].Path != "/contacts/user@example.com" {
		t.Errorf("unexpected path: %s", reqs[0].Path)
	}
}

func TestSyncUserSegment_Active(t *testing.T) {
	m := newMockResendServer()
	defer m.close()
	restore := m.setEndpoints()
	defer restore()

	c := NewClient("re_live_test", testLogger())

	err := c.SyncUserSegment("user@example.com", true, "seg-active", "seg-setup")
	if err != nil {
		t.Fatalf("SyncUserSegment: %v", err)
	}

	reqs := m.getRequests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests (add + remove), got %d", len(reqs))
	}
	// First: add to active
	if reqs[0].Method != http.MethodPost || !strings.Contains(reqs[0].Path, "seg-active") {
		t.Errorf("first request should add to active: %s %s", reqs[0].Method, reqs[0].Path)
	}
	// Second: remove from setup
	if reqs[1].Method != http.MethodDelete || !strings.Contains(reqs[1].Path, "seg-setup") {
		t.Errorf("second request should remove from setup: %s %s", reqs[1].Method, reqs[1].Path)
	}
}

func TestSyncUserSegment_NeedsSetup(t *testing.T) {
	m := newMockResendServer()
	defer m.close()
	restore := m.setEndpoints()
	defer restore()

	c := NewClient("re_live_test", testLogger())

	err := c.SyncUserSegment("user@example.com", false, "seg-active", "seg-setup")
	if err != nil {
		t.Fatalf("SyncUserSegment: %v", err)
	}

	reqs := m.getRequests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	// First: add to setup
	if reqs[0].Method != http.MethodPost || !strings.Contains(reqs[0].Path, "seg-setup") {
		t.Errorf("first request should add to setup: %s %s", reqs[0].Method, reqs[0].Path)
	}
	// Second: remove from active
	if reqs[1].Method != http.MethodDelete || !strings.Contains(reqs[1].Path, "seg-active") {
		t.Errorf("second request should remove from active: %s %s", reqs[1].Method, reqs[1].Path)
	}
}

func TestDevMode_SkipsHTTPCalls(t *testing.T) {
	m := newMockResendServer()
	defer m.close()
	restore := m.setEndpoints()
	defer restore()

	for _, key := range []string{"", "placeholder", "re_test_abc"} {
		c := NewClient(key, testLogger())

		_ = c.CreateContact("test@example.com", nil)
		_ = c.AddToSegment("test@example.com", "seg-1")
		_ = c.RemoveFromSegment("test@example.com", "seg-1")
		_ = c.DeleteContact("test@example.com")
		_, _ = c.CreateSegment("test")
		_, _ = c.ListSegments()
		_, _ = c.CreateTemplate("t", "t", "s", "<p>hi</p>")
		_ = c.UpdateTemplate("t", "s", "<p>hi</p>")
		_, _ = c.ListTemplates()
		_ = c.PublishTemplate("t")

		reqs := m.getRequests()
		if len(reqs) != 0 {
			t.Errorf("dev mode key %q: expected 0 requests, got %d", key, len(reqs))
		}
	}
}

func TestCreateSegment(t *testing.T) {
	m := newMockResendServer()
	defer m.close()
	restore := m.setEndpoints()
	defer restore()

	c := NewClient("re_live_test", testLogger())

	id, err := c.CreateSegment("Active Syncers")
	if err != nil {
		t.Fatalf("CreateSegment: %v", err)
	}
	if id != "seg-new-123" {
		t.Errorf("expected id seg-new-123, got %s", id)
	}
}

func TestListSegments(t *testing.T) {
	m := newMockResendServer()
	defer m.close()
	restore := m.setEndpoints()
	defer restore()

	c := NewClient("re_live_test", testLogger())

	segs, err := c.ListSegments()
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if len(segs) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segs))
	}
	if segs[0].Name != "Active Syncers" {
		t.Errorf("unexpected segment name: %s", segs[0].Name)
	}
}

func TestTemplateLifecycle(t *testing.T) {
	m := newMockResendServer()
	defer m.close()
	restore := m.setEndpoints()
	defer restore()

	c := NewClient("re_live_test", testLogger())

	id, err := c.CreateTemplate("Test Template", "test-tmpl", "Subject", "<p>Hello</p>")
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if id != "tmpl-123" {
		t.Errorf("expected tmpl-123, got %s", id)
	}

	if err := c.UpdateTemplate("test-tmpl", "New Subject", "<p>Updated</p>"); err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}

	if err := c.PublishTemplate("test-tmpl"); err != nil {
		t.Fatalf("PublishTemplate: %v", err)
	}

	tmpls, err := c.ListTemplates()
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(tmpls) != 1 {
		t.Fatalf("expected 1 template, got %d", len(tmpls))
	}

	reqs := m.getRequests()
	if len(reqs) != 4 {
		t.Fatalf("expected 4 requests, got %d", len(reqs))
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()

	origContacts := contactsEndpoint
	contactsEndpoint = srv.URL + "/contacts"
	defer func() { contactsEndpoint = origContacts }()

	c := NewClient("re_live_test", testLogger())

	err := c.CreateContact("test@example.com", nil)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "API error (status 400)") {
		t.Errorf("unexpected error message: %v", err)
	}
}
