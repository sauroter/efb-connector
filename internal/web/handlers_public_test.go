package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"efb-connector/internal/auth"
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

// An unknown-but-well-formed token still renders the confirmation page: GET
// must not touch the database, so it cannot know the token is unknown. The
// failure surfaces on POST instead.
func TestVerifyMagicLinkForm_UnknownToken_RendersConfirmPage(t *testing.T) {
	h := newTestHarness(t)

	resp, err := h.raw.Get(h.srv.URL + "/auth/verify?token=notarealone")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `name="token"`) {
		t.Error("confirmation page missing token field")
	}
}

func TestVerifyMagicLinkForm_MalformedToken_RedirectsToLogin(t *testing.T) {
	h := newTestHarness(t)

	resp, err := h.raw.Get(h.srv.URL + "/auth/verify?token=%21%21%21")
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

// Regression test for the reported bug: Bitdefender and Outlook Safe Links
// prefetch links found in email. Go's ServeMux serves HEAD from a GET pattern,
// so a HEAD prefetch used to run the full verify handler and consume the
// one-time token before the user ever clicked.
func TestVerifyMagicLink_HeadDoesNotConsumeToken(t *testing.T) {
	h := newTestHarness(t)

	token, err := h.auth.GenerateMagicLink("scan@example.com")
	if err != nil {
		t.Fatalf("generate magic link: %v", err)
	}

	req, _ := http.NewRequest(http.MethodHead, h.srv.URL+"/auth/verify?token="+token, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Bitdefender)")
	resp, err := h.raw.Do(req)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	// Check the account first: ValidateMagicLink below auto-creates the user,
	// so asking afterwards would always find one.
	if u, _ := h.db.GetUserByEmail("scan@example.com"); u != nil {
		t.Error("HEAD prefetch created a user account")
	}
	if _, err := h.auth.ValidateMagicLink(token); err != nil {
		t.Fatalf("token consumed by HEAD prefetch: %v", err)
	}
}

// Same for a GET prefetch: Safe Links follows redirects with GET, so guarding
// HEAD alone would not be enough.
func TestVerifyMagicLink_GetDoesNotConsumeToken(t *testing.T) {
	h := newTestHarness(t)

	token, err := h.auth.GenerateMagicLink("scan@example.com")
	if err != nil {
		t.Fatalf("generate magic link: %v", err)
	}

	resp, err := h.raw.Get(h.srv.URL + "/auth/verify?token=" + token)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	// Check the account first: ValidateMagicLink below auto-creates the user,
	// so asking afterwards would always find one.
	if u, _ := h.db.GetUserByEmail("scan@example.com"); u != nil {
		t.Error("GET prefetch created a user account")
	}
	if _, err := h.auth.ValidateMagicLink(token); err != nil {
		t.Fatalf("token consumed by GET prefetch: %v", err)
	}
}

func TestVerifyMagicLink_GetThenPost_LogsIn(t *testing.T) {
	h := newTestHarness(t)

	token, err := h.auth.GenerateMagicLink("user@example.com")
	if err != nil {
		t.Fatalf("generate magic link: %v", err)
	}

	resp, err := h.raw.Get(h.srv.URL + "/auth/verify?token=" + token)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	const marker = `name="token" value="`
	idx := strings.Index(string(body), marker)
	if idx < 0 {
		t.Fatal("token field not found on confirmation page")
	}
	rest := string(body)[idx+len(marker):]
	formToken := rest[:strings.Index(rest, `"`)]
	if formToken != token {
		t.Fatalf("form token = %q, want %q", formToken, token)
	}

	resp, err = h.raw.PostForm(h.srv.URL+"/auth/verify", url.Values{"token": {formToken}})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/dashboard" {
		t.Errorf("location = %q, want /dashboard", loc)
	}
	var gotSession bool
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			gotSession = true
		}
	}
	if !gotSession {
		t.Error("no session cookie set")
	}
	if u, _ := h.db.GetUserByEmail("user@example.com"); u == nil {
		t.Error("user not created on confirm")
	}
}

func TestVerifyMagicLink_PostTwice_SecondSaysAlreadyUsed(t *testing.T) {
	h := newTestHarness(t)

	token, err := h.auth.GenerateMagicLink("user@example.com")
	if err != nil {
		t.Fatalf("generate magic link: %v", err)
	}

	resp, err := h.raw.PostForm(h.srv.URL+"/auth/verify", url.Values{"token": {token}})
	if err != nil {
		t.Fatalf("first post: %v", err)
	}
	resp.Body.Close()

	resp, err = h.raw.PostForm(h.srv.URL+"/auth/verify", url.Values{"token": {token}})
	if err != nil {
		t.Fatalf("second post: %v", err)
	}
	defer resp.Body.Close()

	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("location = %q, want /login", loc)
	}
	var flashVal string
	for _, c := range resp.Cookies() {
		if c.Name == "flash" {
			flashVal, _ = url.QueryUnescape(c.Value)
		}
	}
	if flashVal != "flash.login_link_used" {
		t.Errorf("flash = %q, want flash.login_link_used", flashVal)
	}
}

// A cross-origin auto-submit must not be able to log a victim into the
// attacker's account, where they would then store their Garmin credentials.
func TestVerifyMagicLink_CrossOriginPost_Rejected(t *testing.T) {
	h := newTestHarness(t)

	token, err := h.auth.GenerateMagicLink("attacker@example.com")
	if err != nil {
		t.Fatalf("generate magic link: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/auth/verify",
		strings.NewReader(url.Values{"token": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example.com")

	resp, err := h.raw.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if _, err := h.auth.ValidateMagicLink(token); err != nil {
		t.Errorf("token consumed by rejected cross-origin post: %v", err)
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
