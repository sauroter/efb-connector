package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// isValidEmail
// ─────────────────────────────────────────────────────────────────────────────

func TestIsValidEmail(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"a@b.c", true},
		{"foo+bar@example.com", true},
		{"first.last@sub.example.org", true},

		{"", false},
		{"plain", false},
		{"@example.com", false},
		{"foo@", false},
		{"foo@bar", false},      // no dot in domain
		{"foo@.com", false},     // empty before dot
		{"foo@bar.", false},     // empty after dot
		{"foo@@bar.com", false}, // multiple @
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := isValidEmail(c.in); got != c.want {
				t.Errorf("isValidEmail(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// /login form
// ─────────────────────────────────────────────────────────────────────────────

func TestLoginGet_Renders(t *testing.T) {
	h := newTestHarness(t)

	resp, err := h.client.Get(h.srv.URL + "/login")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `name="email"`) {
		t.Error("login page should contain an email input")
	}
}

func TestLoginPost_EmptyEmail_RedirectsToLogin(t *testing.T) {
	h := newTestHarness(t)

	resp, err := h.raw.PostForm(h.srv.URL+"/login", url.Values{"email": {""}})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("location = %q, want /login", loc)
	}
}

func TestLoginPost_InvalidEmail_RedirectsToLogin(t *testing.T) {
	h := newTestHarness(t)

	resp, err := h.raw.PostForm(h.srv.URL+"/login", url.Values{"email": {"not-an-email"}})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// /auth/verify magic link
// ─────────────────────────────────────────────────────────────────────────────

func TestVerifyMagicLink_MissingToken_RedirectsToLogin(t *testing.T) {
	h := newTestHarness(t)

	resp, err := h.raw.Get(h.srv.URL + "/auth/verify")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("location = %q, want /login", loc)
	}
}

func TestVerifyMagicLink_InvalidToken_RedirectsToLogin(t *testing.T) {
	h := newTestHarness(t)

	resp, err := h.raw.Get(h.srv.URL + "/auth/verify?token=notarealone")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Authenticated routes reject unauthenticated callers
// ─────────────────────────────────────────────────────────────────────────────

func TestProtectedRoutes_RedirectWhenUnauthenticated(t *testing.T) {
	h := newTestHarness(t)

	cases := []struct {
		method, path string
	}{
		{"GET", "/dashboard"},
		{"GET", "/settings"},
		{"GET", "/settings/garmin"},
		{"GET", "/settings/efb"},
		{"GET", "/sync/status"},
		{"GET", "/sync/history"},
		{"POST", "/sync/trigger"},
		{"POST", "/auth/logout"},
		{"POST", "/account/delete"},
		{"POST", "/feedback"},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			req, _ := http.NewRequest(c.method, h.srv.URL+c.path, nil)
			resp, err := h.raw.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", c.method, c.path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther {
				t.Errorf("%s %s: status = %d, want 303", c.method, c.path, resp.StatusCode)
			}
			if loc := resp.Header.Get("Location"); !strings.HasSuffix(loc, "/login") {
				t.Errorf("%s %s: location = %q, want suffix /login", c.method, c.path, loc)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Static + landing
// ─────────────────────────────────────────────────────────────────────────────

func TestLanding_RendersAtRoot(t *testing.T) {
	h := newTestHarness(t)

	resp, err := h.client.Get(h.srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestLanding_NotFoundOnUnknownPath(t *testing.T) {
	h := newTestHarness(t)

	// Paths that don't match a registered route fall through to the
	// landing handler (mux matches "GET /") which returns 404 for non-root.
	resp, err := h.client.Get(h.srv.URL + "/totally-unknown")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
