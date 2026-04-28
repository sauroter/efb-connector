package metrics

import "testing"

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Internal routes collapse to a single label.
		{"/internal/sync/run-all", "/internal/*"},
		{"/internal/admin/users/42/sync", "/internal/*"},
		{"/internal/", "/internal/*"},

		// Auth routes collapse.
		{"/auth/verify", "/auth/*"},
		{"/auth/logout", "/auth/*"},

		// Static assets collapse.
		{"/static/style.css", "/static/*"},
		{"/static/img/logo.svg", "/static/*"},

		// Known top-level routes pass through.
		{"/", "/"},
		{"/login", "/login"},
		{"/dashboard", "/dashboard"},
		{"/impressum", "/impressum"},
		{"/privacy", "/privacy"},
		{"/settings/garmin", "/settings/garmin"},
		{"/settings/efb", "/settings/efb"},
		{"/settings/garmin/delete", "/settings/garmin/delete"},
		{"/settings/efb/delete", "/settings/efb/delete"},
		{"/sync/trigger", "/sync/trigger"},
		{"/sync/status", "/sync/status"},
		{"/sync/history", "/sync/history"},
		{"/account/delete", "/account/delete"},
		{"/health", "/health"},
		{"/favicon.ico", "/favicon.ico"},

		// Unknown paths collapse to /other to bound label cardinality.
		{"/randomly/made/up", "/other"},
		{"/settings/garmin/mfa", "/other"}, // sub-route not in known list
		{"", "/other"},
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := NormalizePath(c.in); got != c.want {
				t.Errorf("NormalizePath(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestObserveHTTPRequest_DoesNotPanic(t *testing.T) {
	// Smoke test: just verify the call path doesn't blow up on edge inputs.
	// Prometheus state is global so we don't assert on counter values.
	ObserveHTTPRequest("GET", "/dashboard", 200, 0.123)
	ObserveHTTPRequest("POST", "/internal/sync/run-all", 401, 0.001)
	ObserveHTTPRequest("GET", "/some/unknown/path", 404, 0.05)
}

func TestObserveSyncRun_DoesNotPanic(t *testing.T) {
	ObserveSyncRun("cron", "completed", 12.5, 10, 8, 1, 1, 8)
	ObserveSyncRun("manual", "error", 0.5, 0, 0, 0, 0, 0)
}
