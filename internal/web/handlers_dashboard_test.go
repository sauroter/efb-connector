package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"efb-connector/internal/auth"
)

// loginAs creates a user with both credential rows saved, opens an
// authenticated session, and attaches the cookie to h.client.
func loginAs(t *testing.T, h *testHarness, email string) int64 {
	t.Helper()
	u, err := h.db.CreateUser(email)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := h.db.SaveGarminCredentials(u.ID, "g@example.com", "p"); err != nil {
		t.Fatalf("SaveGarminCredentials: %v", err)
	}
	if err := h.db.SaveEFBCredentials(u.ID, "efbuser", "p"); err != nil {
		t.Fatalf("SaveEFBCredentials: %v", err)
	}

	token, err := h.auth.CreateSession(u.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	srvURL, _ := url.Parse(h.srv.URL)
	h.client.Jar.SetCookies(srvURL, []*http.Cookie{{
		Name:  auth.SessionCookieName,
		Value: token,
		Path:  "/",
	}})
	return u.ID
}

func getBody(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestDashboard_NoCredsInvalidBannerWhenAllValid(t *testing.T) {
	h := newTestHarness(t)
	loginAs(t, h, "valid@example.com")

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	if strings.Contains(body, "Anmeldung prüfen") || strings.Contains(body, "Re-enter login") {
		t.Error("dashboard should not show creds-invalid banner when both are valid")
	}
}

func TestDashboard_ShowsBannerWhenGarminInvalid(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "garmin_inv@example.com")

	if err := h.db.InvalidateGarminCredentials(uid, "transient"); err != nil {
		t.Fatalf("InvalidateGarminCredentials: %v", err)
	}

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	if !strings.Contains(body, "/settings/garmin") {
		t.Error("banner should deep-link to /settings/garmin when garmin is invalid")
	}
	// The button label is one of the new i18n strings; verify at least one renders.
	if !strings.Contains(body, "Garmin") {
		t.Error("banner body should reference Garmin")
	}
	if strings.Contains(body, "/settings/efb\" role=\"button\"") {
		t.Error("EFB-specific button should not render when only garmin is invalid")
	}
}

func TestDashboard_ShowsBannerWhenEFBInvalid(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "efb_inv@example.com")

	if err := h.db.InvalidateEFBCredentials(uid, "transient"); err != nil {
		t.Fatalf("InvalidateEFBCredentials: %v", err)
	}

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	if !strings.Contains(body, "/settings/efb") {
		t.Error("banner should deep-link to /settings/efb when efb is invalid")
	}
	if strings.Contains(body, "/settings/garmin\" role=\"button\"") {
		t.Error("Garmin-specific button should not render when only efb is invalid")
	}
}

func TestSettings_BadgeShowsNeedsReauthForInvalidCreds(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "settings_inv@example.com")

	if err := h.db.InvalidateGarminCredentials(uid, "transient"); err != nil {
		t.Fatalf("InvalidateGarminCredentials: %v", err)
	}

	body := getBody(t, h.client, h.srv.URL+"/settings")
	// Either rendered translation of common.needs_reauth must be present.
	if !strings.Contains(body, "Anmeldung prüfen") && !strings.Contains(body, "Re-enter login") {
		t.Error("settings page should show needs-reauth badge when garmin is_valid=0")
	}
}
