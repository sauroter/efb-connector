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
	if err := h.db.RecordSyncDiagnostics(runID, 12, []string{"cycling", "other", "running"}, 0, 0); err != nil {
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

// A run that found nothing because the user's selection dropped everything
// must NOT get the "re-tag your activity in Garmin" advice — their activity
// type is already correct, and the fix is a checkbox on /settings. Telling a
// sailor to change their activity type is worse than saying nothing.
func TestDashboard_FilteredOutHintReplacesNoActivitiesHint(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "sailor@example.com")

	if err := h.db.UpdateSetupCompleted(uid, true); err != nil {
		t.Fatalf("UpdateSetupCompleted: %v", err)
	}

	runID, err := h.db.CreateSyncRun(uid, "scheduled")
	if err != nil {
		t.Fatalf("CreateSyncRun: %v", err)
	}
	if err := h.db.UpdateSyncRun(runID, "completed", 0, 0, 0, 0, 0, ""); err != nil {
		t.Fatalf("UpdateSyncRun: %v", err)
	}
	// Two sailing activities passed Garmin's filter and were dropped here.
	if err := h.db.RecordSyncDiagnostics(runID, 4, []string{"hiking", "sailing_v2"}, 0, 2); err != nil {
		t.Fatalf("RecordSyncDiagnostics: %v", err)
	}

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	for _, want := range []string{
		"Activities filtered out by your selection",
		"hiking, sailing_v2",
		"Choose activity types",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard body missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"No matching activities found",
		"Kajakfahren",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("dashboard must not show the re-tag advice %q when the filter dropped the activities", unwanted)
		}
	}
}

// A run where Garmin returned nothing at all (raw_count 0) — e.g. a fresh
// account whose last trip is older than sync_days — must not get the re-tag
// advice either: there is nothing to re-tag, the fix is a custom date range.
func TestDashboard_EmptyWindowHintReplacesNoActivitiesHint(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "fresh-account@example.com")

	if err := h.db.UpdateSetupCompleted(uid, true); err != nil {
		t.Fatalf("UpdateSetupCompleted: %v", err)
	}

	runID, err := h.db.CreateSyncRun(uid, "manual")
	if err != nil {
		t.Fatalf("CreateSyncRun: %v", err)
	}
	if err := h.db.UpdateSyncRun(runID, "completed", 0, 0, 0, 0, 0, ""); err != nil {
		t.Fatalf("UpdateSyncRun: %v", err)
	}
	if err := h.db.RecordSyncDiagnostics(runID, 0, []string{}, 0, 0); err != nil {
		t.Fatalf("RecordSyncDiagnostics: %v", err)
	}

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	for _, want := range []string{
		"No activities in the sync window",
		"no activities at all for the last 3 days",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard body missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"No matching activities found",
		"Kajakfahren",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("dashboard must not show the re-tag advice %q when Garmin returned no activities", unwanted)
		}
	}
}

// An empty custom-range run must not get the empty-window hint: its window
// isn't sync_days, and the hint would send the user back to the custom
// range they just used. The re-tag advice would be wrong too.
func TestDashboard_EmptyCustomRangeShowsNoHint(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "custom-range@example.com")

	if err := h.db.UpdateSetupCompleted(uid, true); err != nil {
		t.Fatalf("UpdateSetupCompleted: %v", err)
	}

	runID, err := h.db.CreateSyncRun(uid, "manual_custom")
	if err != nil {
		t.Fatalf("CreateSyncRun: %v", err)
	}
	if err := h.db.UpdateSyncRun(runID, "completed", 0, 0, 0, 0, 0, ""); err != nil {
		t.Fatalf("UpdateSyncRun: %v", err)
	}
	if err := h.db.RecordSyncDiagnostics(runID, 0, []string{}, 0, 0); err != nil {
		t.Fatalf("RecordSyncDiagnostics: %v", err)
	}

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	for _, heading := range []string{"No activities in the sync window", "No matching activities found"} {
		if strings.Contains(body, heading) {
			t.Errorf("hint banner %q must not render for an empty custom-range run", heading)
		}
	}
}

// When Garmin returned activities but none were water sports, the re-tag
// advice is the right one — the empty-window hint must not take over.
func TestDashboard_NonWaterActivitiesKeepNoActivitiesHint(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "runner@example.com")

	if err := h.db.UpdateSetupCompleted(uid, true); err != nil {
		t.Fatalf("UpdateSetupCompleted: %v", err)
	}

	runID, err := h.db.CreateSyncRun(uid, "scheduled")
	if err != nil {
		t.Fatalf("CreateSyncRun: %v", err)
	}
	if err := h.db.UpdateSyncRun(runID, "completed", 0, 0, 0, 0, 0, ""); err != nil {
		t.Fatalf("UpdateSyncRun: %v", err)
	}
	if err := h.db.RecordSyncDiagnostics(runID, 2, []string{"running"}, 0, 0); err != nil {
		t.Fatalf("RecordSyncDiagnostics: %v", err)
	}

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	if !strings.Contains(body, "No matching activities found") {
		t.Error("generic no-activities hint should render when Garmin returned only non-water activities")
	}
	if strings.Contains(body, "No activities in the sync window") {
		t.Error("empty-window hint must require raw_count 0")
	}
}

// A clean zero run without recorded diagnostics (legacy row, or diagnostics
// write failed) can't be told apart, so it keeps the generic hint.
func TestDashboard_EmptyWindowHintNeedsDiagnostics(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "no-diagnostics@example.com")

	if err := h.db.UpdateSetupCompleted(uid, true); err != nil {
		t.Fatalf("UpdateSetupCompleted: %v", err)
	}

	runID, _ := h.db.CreateSyncRun(uid, "scheduled")
	_ = h.db.UpdateSyncRun(runID, "completed", 0, 0, 0, 0, 0, "")

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	if strings.Contains(body, "No activities in the sync window") {
		t.Error("empty-window hint must require recorded diagnostics")
	}
	if !strings.Contains(body, "No matching activities found") {
		t.Error("generic no-activities hint should still render without diagnostics")
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
	_ = h.db.RecordSyncDiagnostics(runID, 0, []string{}, 0, 0)

	body := getBody(t, h.client, h.srv.URL+"/dashboard")
	for _, heading := range []string{"No matching activities found", "No activities in the sync window"} {
		if strings.Contains(body, heading) {
			t.Errorf("hint banner %q must be suppressed while ShowGettingStarted is true", heading)
		}
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
	for _, heading := range []string{"No matching activities found", "No activities in the sync window"} {
		if strings.Contains(body, heading) {
			t.Errorf("hint banner %q must be suppressed when last sync errored — the error message itself explains nothing was imported", heading)
		}
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
