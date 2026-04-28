package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders_AppliedToAllResponses(t *testing.T) {
	h := newTestHarness(t)

	resp, err := h.client.Get(h.srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	want := map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	}
	for header, expected := range want {
		if got := resp.Header.Get(header); got != expected {
			t.Errorf("%s = %q, want %q", header, got, expected)
		}
	}
}

func TestRecovery_ReturnsInternalServerErrorOnPanic(t *testing.T) {
	h := newTestHarness(t)

	// Wrap a hand-rolled panicking handler in the same recovery middleware.
	// We invoke the middleware directly rather than registering a real route
	// to avoid mutating the test harness's mux.
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	wrapped := h.server.recovery(panicHandler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestStatusWriter_CapturesStatusCode(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}

	sw.WriteHeader(http.StatusTeapot)
	if sw.status != http.StatusTeapot {
		t.Errorf("captured status = %d, want %d", sw.status, http.StatusTeapot)
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("underlying recorder code = %d, want %d", rec.Code, http.StatusTeapot)
	}

	// Subsequent WriteHeader calls must not overwrite the first status.
	sw.WriteHeader(http.StatusInternalServerError)
	if sw.status != http.StatusTeapot {
		t.Errorf("status changed after second WriteHeader: %d", sw.status)
	}
}

func TestStatusWriter_WriteWithoutWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}

	if _, err := sw.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !sw.wroteHeader {
		t.Error("Write should mark wroteHeader=true")
	}
	if sw.status != http.StatusOK {
		t.Errorf("default status should remain 200, got %d", sw.status)
	}
}
