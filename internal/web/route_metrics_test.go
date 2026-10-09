package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Metrics are labelled by the route that serves a request. Every registered
// route must get its own pattern (the old hardcoded list sent most settings
// routes to "/other"), and the "GET /" catch-all must not swallow unknown
// paths into the landing page's label.
func TestRoutePattern(t *testing.T) {
	h := newTestHarness(t)

	cases := []struct {
		method, path, want string
	}{
		{http.MethodGet, "/", "GET /"},
		{http.MethodGet, "/settings", "GET /settings"},
		{http.MethodPost, "/settings/activity-types", "POST /settings/activity-types"},
		{http.MethodPost, "/sync/efb/recheck-consent", "POST /sync/efb/recheck-consent"},
		{http.MethodGet, "/internal/admin/users/42/sync-history", "GET /internal/admin/users/{id}/sync-history"},
		{http.MethodGet, "/static/style.css", "GET /static/"},
		{http.MethodGet, "/wp-admin", ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := routePattern(h.server.routeMux, r); got != c.want {
			t.Errorf("%s %s: pattern = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}
