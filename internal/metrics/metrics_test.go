package metrics

import "testing"

func TestRouteLabel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"GET /settings", "/settings"},
		{"POST /settings/activity-types", "/settings/activity-types"},
		{"GET /internal/admin/users/{id}/sync-history", "/internal/admin/users/{id}/sync-history"},
		{"GET /static/", "/static/"},
		{"/no-method", "/no-method"},
		{"", "/other"},
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := RouteLabel(c.in); got != c.want {
				t.Errorf("RouteLabel(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestMethodLabel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"GET", "GET"},
		{"HEAD", "HEAD"},
		{"POST", "POST"},
		{"PUT", "PUT"},
		{"PATCH", "PATCH"},
		{"DELETE", "DELETE"},
		{"OPTIONS", "OPTIONS"},
		// Anything else collapses, so clients can't mint label values.
		{"FOOBAR", "OTHER"},
		{"get", "OTHER"},
		{"CONNECT", "OTHER"},
		{"TRACE", "OTHER"},
		{"", "OTHER"},
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := MethodLabel(c.in); got != c.want {
				t.Errorf("MethodLabel(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestObserveHTTPRequest_DoesNotPanic(t *testing.T) {
	// Smoke test: just verify the call path doesn't blow up on edge inputs.
	// Prometheus state is global so we don't assert on counter values.
	ObserveHTTPRequest("GET", "GET /dashboard", 200, 0.123)
	ObserveHTTPRequest("POST", "POST /internal/sync/run-all", 401, 0.001)
	ObserveHTTPRequest("GET", "", 404, 0.05)
}

func TestObserveSyncRun_DoesNotPanic(t *testing.T) {
	ObserveSyncRun("cron", "completed", 12.5, 10, 8, 1, 1, 8)
	ObserveSyncRun("manual", "error", 0.5, 0, 0, 0, 0, 0)
}

func TestUserSignupsTotal_DoesNotPanic(t *testing.T) {
	UserSignupsTotal.Inc()
	UserSignupsTotal.Add(2)
}
