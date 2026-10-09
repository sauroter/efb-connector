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
		// Wrong method for a known path: the mux answers 405, no pattern.
		{http.MethodPost, "/settings", ""},
		// HEAD is served by GET patterns.
		{http.MethodHead, "/", "GET /"},
		{http.MethodHead, "/dashboard", "GET /dashboard"},
		// Non-canonical paths get a redirect; the mux reports the pattern
		// the redirect target will match.
		{http.MethodGet, "/static", "GET /static/"},
		{http.MethodGet, "/settings/../dashboard", "GET /dashboard"},
		// "/foo/../" cleans to "/", so the mux reports "GET /" for the
		// redirect — but the raw path isn't "/", so the catch-all guard
		// labels it unmatched. Correct: this response is a 301, not the
		// landing page, and the follow-up request for "/" counts as "GET /".
		{http.MethodGet, "/foo/../", ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := routePattern(h.server.routeMux, r); got != c.want {
			t.Errorf("%s %s: pattern = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}
