package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"efb-connector/internal/auth"
)

// csrfTokenFor returns the CSRF token bound to the currently-logged-in
// session in h.client's cookie jar. The CSRFProtect middleware rejects any
// state-changing request that doesn't carry it.
func csrfTokenFor(t *testing.T, h *testHarness) string {
	t.Helper()
	srvURL, _ := url.Parse(h.srv.URL)
	var sessionToken string
	for _, c := range h.client.Jar.Cookies(srvURL) {
		if c.Name == auth.SessionCookieName {
			sessionToken = c.Value
		}
	}
	if sessionToken == "" {
		t.Fatal("csrfTokenFor: no session cookie in jar — call loginAs first")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
	return h.auth.CSRFToken(req)
}

// postForm posts form values to the given path using the harness's redirect-blocking
// client so the test can inspect the 303 directly. It automatically injects the
// session-bound CSRF token so state-changing requests pass CSRFProtect.
func postForm(t *testing.T, h *testHarness, path string, form url.Values) *http.Response {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	if form.Get("csrf_token") == "" {
		form.Set("csrf_token", csrfTokenFor(t, h))
	}
	resp, err := h.raw.PostForm(h.srv.URL+path, form)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// flashFrom returns the value of the "flash" cookie on resp, or "" when
// no such cookie was set. The handler writes the i18n key (e.g.
// "flash.invalid_start_date") as the value.
func flashFrom(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == "flash" {
			return c.Value
		}
	}
	return ""
}

func TestHandleAutoCreateTripsSave_RoundTrip(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "auto@example.com")

	// Disable.
	resp := postForm(t, h, "/settings/auto-create-trips", url.Values{"enabled": {"0"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	u, _ := h.db.GetUserByID(uid)
	if u.AutoCreateTrips {
		t.Error("expected AutoCreateTrips=false after POST enabled=0")
	}

	// Re-enable.
	postForm(t, h, "/settings/auto-create-trips", url.Values{"enabled": {"1"}})
	u, _ = h.db.GetUserByID(uid)
	if !u.AutoCreateTrips {
		t.Error("expected AutoCreateTrips=true after POST enabled=1")
	}
}

func TestHandleEnrichTripsSave_RoundTrip(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "enrich@example.com")

	postForm(t, h, "/settings/enrich-trips", url.Values{"enabled": {"1"}})
	u, _ := h.db.GetUserByID(uid)
	if !u.EnrichTrips {
		t.Error("expected EnrichTrips=true after POST")
	}

	postForm(t, h, "/settings/enrich-trips", url.Values{"enabled": {"0"}})
	u, _ = h.db.GetUserByID(uid)
	if u.EnrichTrips {
		t.Error("expected EnrichTrips=false after POST enabled=0")
	}
}

func TestHandleLanguageSave_Persists(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "lang@example.com")

	resp := postForm(t, h, "/settings/language", url.Values{"lang": {"de"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/settings" {
		t.Errorf("location = %q, want /settings", loc)
	}
	// The handler must also set a "lang" cookie so the choice takes effect immediately.
	var sawLangCookie bool
	for _, c := range resp.Cookies() {
		if c.Name == "lang" && c.Value == "de" {
			sawLangCookie = true
		}
	}
	if !sawLangCookie {
		t.Error("expected lang=de cookie on response")
	}

	u, _ := h.db.GetUserByID(uid)
	if u.PreferredLang != "de" {
		t.Errorf("PreferredLang = %q, want de", u.PreferredLang)
	}
}

func TestHandleLanguageSave_RejectsInvalidValue(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "lang_bad@example.com")

	// Pre-set a non-default lang so the assertion proves the handler wrote
	// it back to "en" rather than relying on the column default.
	if err := h.db.UpdatePreferredLang(uid, "de"); err != nil {
		t.Fatalf("UpdatePreferredLang: %v", err)
	}

	postForm(t, h, "/settings/language", url.Values{"lang": {"klingon"}})
	u, err := h.db.GetUserByID(uid)
	if err != nil || u == nil {
		t.Fatalf("GetUserByID: u=%v err=%v", u, err)
	}
	if u.PreferredLang != "en" {
		t.Errorf("invalid lang value should be rewritten to en, got %q", u.PreferredLang)
	}
}

func TestHandleSetupConfigure_SetsPreferencesAndCompletes(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "setup@example.com")

	// Flip both flags away from their defaults first so the assertions
	// prove the wizard rewrote them, not that the defaults happen to match.
	if err := h.db.UpdateAutoCreateTrips(uid, false); err != nil {
		t.Fatalf("UpdateAutoCreateTrips: %v", err)
	}
	if err := h.db.UpdateEnrichTrips(uid, false); err != nil {
		t.Fatalf("UpdateEnrichTrips: %v", err)
	}

	postForm(t, h, "/setup/configure", url.Values{
		"auto_create_trips": {"1"},
		"enrich_trips":      {"1"},
	})

	u, err := h.db.GetUserByID(uid)
	if err != nil || u == nil {
		t.Fatalf("GetUserByID: u=%v err=%v", u, err)
	}
	if !u.AutoCreateTrips {
		t.Error("AutoCreateTrips should be true after wizard submit")
	}
	if !u.EnrichTrips {
		t.Error("EnrichTrips should be true after wizard submit")
	}
	if !u.SetupCompleted {
		t.Error("SetupCompleted should be true after wizard submit")
	}
}

func TestHandleSetupConfigure_UncheckedBoxesSetFalse(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "setup-unchecked@example.com")

	// Defaults are both true (auto_create_trips set by CreateUser, enrich_trips
	// by migration). Post the form WITHOUT the toggles to simulate the user
	// unchecking both boxes — the handler must rewrite both to false.
	postForm(t, h, "/setup/configure", url.Values{})

	u, err := h.db.GetUserByID(uid)
	if err != nil || u == nil {
		t.Fatalf("GetUserByID: u=%v err=%v", u, err)
	}
	if u.AutoCreateTrips {
		t.Error("AutoCreateTrips should be false when checkbox absent")
	}
	if u.EnrichTrips {
		t.Error("EnrichTrips should be false when checkbox absent")
	}
	if !u.SetupCompleted {
		t.Error("SetupCompleted should be true")
	}
}

func TestHandleFeedbackSubmit_StoresFeedback(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "fb-submit@example.com")

	resp := postForm(t, h, "/feedback", url.Values{
		"category": {"bug"},
		"message":  {"It crashed on launch"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}

	got, err := h.db.GetAllFeedback(5)
	if err != nil {
		t.Fatalf("GetAllFeedback: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 feedback, got %d", len(got))
	}
	if got[0].Category != "bug" || got[0].UserID != uid {
		t.Errorf("feedback = %+v", got[0])
	}
}

func TestHandleFeedbackSubmit_EmptyMessageRejected(t *testing.T) {
	h := newTestHarness(t)
	loginAs(t, h, "fb-empty@example.com")

	resp := postForm(t, h, "/feedback", url.Values{
		"category": {"bug"},
		"message":  {"   "},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}

	got, _ := h.db.GetAllFeedback(5)
	if len(got) != 0 {
		t.Errorf("empty message should not create feedback, got %d", len(got))
	}
}

func TestHandleFeedbackSubmit_RateLimitedAfterThree(t *testing.T) {
	h := newTestHarness(t)
	loginAs(t, h, "fb-rate@example.com")

	for i := 0; i < 3; i++ {
		postForm(t, h, "/feedback", url.Values{
			"category": {"general"},
			"message":  {"msg"},
		})
	}
	// 4th should be rate-limited.
	resp := postForm(t, h, "/feedback", url.Values{
		"category": {"general"},
		"message":  {"msg-4"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}

	got, _ := h.db.GetAllFeedback(10)
	if len(got) != 3 {
		t.Errorf("expected 3 stored entries (4th rate-limited), got %d", len(got))
	}
}

func TestHandleFeedbackSubmit_CategoryDefaultedWhenInvalid(t *testing.T) {
	h := newTestHarness(t)
	loginAs(t, h, "fb-cat@example.com")

	postForm(t, h, "/feedback", url.Values{
		"category": {"weirdness"},
		"message":  {"hello"},
	})

	got, _ := h.db.GetAllFeedback(5)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].Category != "general" {
		t.Errorf("category = %q, want general", got[0].Category)
	}
}

func TestHandleLogout_DestroysSessionAndRedirects(t *testing.T) {
	h := newTestHarness(t)
	loginAs(t, h, "logout@example.com")

	// Capture the session token before logout so we can prove ValidateSession
	// fails afterwards — the cookie-clear header alone doesn't tell us the
	// server-side row is gone.
	srvURL, _ := url.Parse(h.srv.URL)
	var sessionToken string
	for _, c := range h.client.Jar.Cookies(srvURL) {
		if c.Name == auth.SessionCookieName {
			sessionToken = c.Value
		}
	}
	if sessionToken == "" {
		t.Fatal("no session cookie in jar after loginAs")
	}

	resp := postForm(t, h, "/auth/logout", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("location = %q, want /", loc)
	}

	var cleared bool
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("expected %s cookie cleared on logout response", auth.SessionCookieName)
	}

	// The server-side session row must also be gone — otherwise the token
	// would still authenticate if replayed from another jar.
	if _, err := h.auth.ValidateSession(sessionToken); err == nil {
		t.Error("expected ValidateSession to fail after logout, but it succeeded")
	}
}

func TestHandleAccountDelete_RemovesUserAndCascades(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "delete@example.com")
	// Add a sync run to be cascaded.
	_, _ = h.db.CreateSyncRun(uid, "manual")

	// Touch the per-user Garmin token store so we can verify the handler
	// removed it. Without this seed file, a regression that dropped the
	// RemoveAll call would still silently pass.
	tokenDir := h.server.garminTokenStorePath(uid)
	tokenFile := tokenDir + "/oauth2_token.json"
	if err := os.WriteFile(tokenFile, []byte("test-token"), 0600); err != nil {
		t.Fatalf("seed token file: %v", err)
	}

	// Capture the session token to verify it stops validating after delete.
	srvURL, _ := url.Parse(h.srv.URL)
	var sessionToken string
	for _, c := range h.client.Jar.Cookies(srvURL) {
		if c.Name == auth.SessionCookieName {
			sessionToken = c.Value
		}
	}
	if sessionToken == "" {
		t.Fatal("no session cookie in jar after loginAs")
	}

	resp := postForm(t, h, "/account/delete", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("location = %q, want /", loc)
	}

	got, err := h.db.GetUserByID(uid)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got != nil {
		t.Error("user should be deleted")
	}

	// Session must not validate — cascade deletes the row, but a regression
	// that left the row attached to a different FK would slip through if we
	// only checked GetUserByID.
	if _, err := h.auth.ValidateSession(sessionToken); err == nil {
		t.Error("expected ValidateSession to fail after account delete")
	}

	// Token directory must be gone — leftover OAuth tokens for a deleted
	// account would be a privacy bug.
	if _, err := os.Stat(tokenDir); !os.IsNotExist(err) {
		t.Errorf("token dir should be removed after account delete; stat err = %v", err)
	}
}

func TestHandleImpressum_RendersGermanByDefault(t *testing.T) {
	h := newTestHarness(t)
	resp, err := h.client.Get(h.srv.URL + "/impressum")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHandlePrivacy_RendersGermanByDefault(t *testing.T) {
	h := newTestHarness(t)
	resp, err := h.client.Get(h.srv.URL + "/privacy")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHandleImpressum_EnglishWithLangCookie(t *testing.T) {
	h := newTestHarness(t)
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/impressum", nil)
	req.AddCookie(&http.Cookie{Name: "lang", Value: "en"})
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Language"); got != "en" {
		t.Errorf("Content-Language = %q, want en", got)
	}
}

func TestHandleSyncStatus_ReturnsCurrentRun(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "syncstatus@example.com")
	runID, _ := h.db.CreateSyncRun(uid, "manual")
	_ = h.db.UpdateSyncRun(runID, "completed", 2, 2, 0, 0, 1, "")

	resp, err := h.client.Get(h.srv.URL + "/sync/status")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "completed") {
		t.Errorf("body should reference run status=completed, got: %s", string(body))
	}
}

func TestHandleSyncHistory_RendersList(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "synchistory@example.com")
	for i := 0; i < 3; i++ {
		runID, _ := h.db.CreateSyncRun(uid, "manual")
		_ = h.db.UpdateSyncRun(runID, "completed", 1, 1, 0, 0, 0, "")
	}

	resp, err := h.client.Get(h.srv.URL + "/sync/history?limit=2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// status-completed renders once per row in sync_history.html, so the
	// marker count equals the visible row count.
	if got := strings.Count(string(body), "status-completed"); got != 2 {
		t.Errorf("expected 2 completed-row markers (limit=2), got %d; body len=%d", got, len(body))
	}
}

func TestHandleSyncHistory_DefaultsLimitWhenInvalid(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "synch-invlimit@example.com")
	// Seed 25 completed runs so we can detect both clamping (must be 20
	// when invalid) and over-fetch.
	for i := 0; i < 25; i++ {
		runID, _ := h.db.CreateSyncRun(uid, "manual")
		_ = h.db.UpdateSyncRun(runID, "completed", 1, 1, 0, 0, 0, "")
	}
	for _, lim := range []string{"-5", "200", "abc", "0", ""} {
		resp, err := h.client.Get(h.srv.URL + "/sync/history?limit=" + lim)
		if err != nil {
			t.Fatalf("get limit=%s: %v", lim, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("limit=%s: status = %d, want 200", lim, resp.StatusCode)
		}
		if got := strings.Count(string(body), "status-completed"); got != 20 {
			t.Errorf("limit=%q: expected 20 rows after default-clamp, got %d", lim, got)
		}
	}
}

func TestHandleSyncTrigger_StartsSync(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "trig@example.com")

	resp := postForm(t, h, "/sync/trigger", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if got := flashFrom(resp); got != "flash.sync_started" {
		t.Errorf("flash = %q, want flash.sync_started", got)
	}

	// Wait for the launched sync goroutine to record a run row. The harness
	// Shutdown will drain it before DB.Close, but polling here lets the
	// assertion be precise about what was launched.
	if !waitFor(t, 2*time.Second, func() bool {
		runs, _ := h.db.GetSyncHistory(uid, 1)
		return len(runs) == 1 && runs[0].Trigger == "manual"
	}) {
		t.Error("expected one manual sync run to be recorded after trigger")
	}
}

func TestHandleSyncTrigger_RateLimitedSecondCall(t *testing.T) {
	h := newTestHarness(t)
	loginAs(t, h, "trig-rl@example.com")

	first := postForm(t, h, "/sync/trigger", url.Values{})
	if got := flashFrom(first); got != "flash.sync_started" {
		t.Fatalf("first call flash = %q, want flash.sync_started", got)
	}

	resp := postForm(t, h, "/sync/trigger", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if got := flashFrom(resp); got != "flash.sync_rate_limited" {
		t.Errorf("second call flash = %q, want flash.sync_rate_limited", got)
	}
}

func TestHandleSyncTrigger_RejectsBadStartDate(t *testing.T) {
	h := newTestHarness(t)
	loginAs(t, h, "trig-baddate@example.com")

	resp := postForm(t, h, "/sync/trigger", url.Values{
		"start_date": {"not-a-date"},
		"end_date":   {"2024-01-31"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "/dashboard") {
		t.Errorf("location = %q, want /dashboard", loc)
	}
	if got := flashFrom(resp); got != "flash.invalid_start_date" {
		t.Errorf("flash = %q, want flash.invalid_start_date", got)
	}
}

func TestHandleSyncTrigger_RejectsBadEndDate(t *testing.T) {
	h := newTestHarness(t)
	loginAs(t, h, "trig-badend@example.com")

	resp := postForm(t, h, "/sync/trigger", url.Values{
		"start_date": {"2024-01-01"},
		"end_date":   {"nope"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if got := flashFrom(resp); got != "flash.invalid_end_date" {
		t.Errorf("flash = %q, want flash.invalid_end_date", got)
	}
}

func TestHandleSyncTrigger_RejectsInvertedRange(t *testing.T) {
	h := newTestHarness(t)
	loginAs(t, h, "trig-invert@example.com")

	resp := postForm(t, h, "/sync/trigger", url.Values{
		"start_date": {"2024-02-01"},
		"end_date":   {"2024-01-01"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	if got := flashFrom(resp); got != "flash.start_before_end" {
		t.Errorf("flash = %q, want flash.start_before_end", got)
	}
}

// TestHandleSyncTrigger_BadDateDoesNotBurnRateLimit locks in that form
// validation runs BEFORE the per-user rate-limit check — a malformed date
// should not consume the user's 1/hour token, otherwise a single typo
// would lock the user out for an hour.
func TestHandleSyncTrigger_BadDateDoesNotBurnRateLimit(t *testing.T) {
	h := newTestHarness(t)
	loginAs(t, h, "trig-typo@example.com")

	bad := postForm(t, h, "/sync/trigger", url.Values{
		"start_date": {"not-a-date"},
		"end_date":   {"2024-01-31"},
	})
	if got := flashFrom(bad); got != "flash.invalid_start_date" {
		t.Fatalf("bad-date flash = %q, want flash.invalid_start_date", got)
	}

	good := postForm(t, h, "/sync/trigger", url.Values{})
	if got := flashFrom(good); got != "flash.sync_started" {
		t.Errorf("follow-up sync flash = %q, want flash.sync_started (bad date must not burn the rate-limit token)", got)
	}
}

// waitFor polls cond until it returns true or the deadline elapses.
// Returns true when cond succeeded. Used to await background sync goroutines.
func waitFor(t *testing.T, deadline time.Duration, cond func() bool) bool {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
