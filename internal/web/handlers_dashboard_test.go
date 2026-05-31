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

func TestDashboard_ShowsNoActivitiesHintAfterCleanZeroRun(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "axel-like@example.com")

	// Finish onboarding so the getting-started checklist doesn't preempt
	// the hint banner.
	if err := h.db.UpdateSetupCompleted(uid, true); err != nil {
		t.Fatalf("UpdateSetupCompleted: %v", err)
	}

	// Simulate a clean sync run that found nothing — the silent-failure
	// scenario from feedback #216 (user 216).
	runID, err := h.db.CreateSyncRun(uid, "scheduled")
	if err != nil {
		t.Fatalf("CreateSyncRun: %v", err)
	}
	if err := h.db.UpdateSyncRun(runID, "completed", 0, 0, 0, 0, 0, ""); err != nil {
		t.Fatalf("UpdateSyncRun: %v", err)
	}
	if err := h.db.RecordSyncDiagnostics(runID, 12, []string{"cycling", "other", "running"}); err != nil {
		t.Fatalf("RecordSyncDiagnostics: %v", err)
	}

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	for _, want := range []string{
		"No matching activities found", // hint heading EN
		"cycling, other, running",      // surfaced typeKeys
		"Kajakfahren",                  // mention of how to fix in EN i18n string
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard body missing %q", want)
		}
	}
}

func TestDashboard_NoActivitiesHintSuppressedDuringSetup(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "setup-still-incomplete@example.com")

	// SetupCompleted defaults to false from CreateUser; with a zero-find
	// run the hint conditions would otherwise be met, but the
	// getting-started checklist takes precedence and the hint must
	// not render.
	runID, _ := h.db.CreateSyncRun(uid, "scheduled")
	_ = h.db.UpdateSyncRun(runID, "completed", 0, 0, 0, 0, 0, "")

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	if strings.Contains(body, "No matching activities found") {
		t.Error("hint banner must be suppressed while ShowGettingStarted is true")
	}
}

func TestDashboard_NoActivitiesHintSuppressedWhenSyncErrored(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "errored@example.com")
	if err := h.db.UpdateSetupCompleted(uid, true); err != nil {
		t.Fatalf("UpdateSetupCompleted: %v", err)
	}

	runID, _ := h.db.CreateSyncRun(uid, "scheduled")
	_ = h.db.UpdateSyncRun(runID, "failed", 0, 0, 0, 0, 0, "garmin: temporarily unavailable")

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	if strings.Contains(body, "No matching activities found") {
		t.Error("hint banner must be suppressed when last sync errored — the error message itself explains nothing was imported")
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
