package web

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"efb-connector/internal/database"
	"efb-connector/internal/i18n"
)

// A template that fails after it has produced output must not leak that
// output: the client gets a clean 500, not a half page with an error
// appended.
func TestRender_TemplateErrorYieldsClean500(t *testing.T) {
	tmpl, err := parseTemplates(fstest.MapFS{
		"pages/boom.html": {Data: []byte(`<!DOCTYPE html><p>before</p>{{call .Boom}}<p>after</p>`)},
	}, "test")
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	s := &Server{templates: tmpl, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	rec := httptest.NewRecorder()
	s.render(rec, httptest.NewRequest(http.MethodGet, "/", nil), "boom.html", map[string]any{
		"Boom": func() (string, error) { return "", errors.New("boom") },
	})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "<!DOCTYPE") || strings.Contains(body, "before") {
		t.Errorf("partial page leaked into error response: %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want the plain-text error type", ct)
	}
}

func TestRender_UnknownTemplateYields500(t *testing.T) {
	h := newTestHarness(t)
	rec := httptest.NewRecorder()
	h.server.render(rec, httptest.NewRequest(http.MethodGet, "/", nil), "nope.html", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// Every full page goes through the base layout exactly once, in the
// request's language. Catches a page that forgets {{template "base" .}},
// calls it twice, or loses the footer.
func TestPages_RenderOneDocumentPerLanguage(t *testing.T) {
	public := []string{"/", "/login", "/auth/verify?token=dW5rbm93bg", "/impressum", "/privacy"}
	private := []string{"/dashboard", "/settings", "/settings/garmin", "/settings/efb", "/sync/history"}

	for _, lang := range []string{"de", "en"} {
		t.Run(lang, func(t *testing.T) {
			h := newTestHarness(t)
			srvURL, _ := url.Parse(h.srv.URL)
			h.client.Jar.SetCookies(srvURL, []*http.Cookie{{Name: "lang", Value: lang, Path: "/"}})

			checkBody := func(path, body string) {
				t.Helper()
				if n := strings.Count(body, "<!DOCTYPE html>"); n != 1 {
					t.Errorf("%s: %d doctypes, want 1", path, n)
				}
				if !strings.Contains(body, `<html lang="`+lang+`">`) {
					t.Errorf("%s: missing <html lang=%q>", path, lang)
				}
				if n := strings.Count(body, `<footer class="container"`); n != 1 {
					t.Errorf("%s: %d site footers, want 1", path, n)
				}
				// Versioned so a release's CSS isn't masked by a cached copy.
				if !strings.Contains(body, `/static/styles.css?v=test"`) {
					t.Errorf("%s: stylesheet link not versioned", path)
				}
			}
			check := func(path string) {
				t.Helper()
				checkBody(path, getBody(t, h.client, h.srv.URL+path))
			}
			for _, p := range public {
				check(p)
			}

			// Pages that need a mailer or a live Garmin MFA session to reach
			// over HTTP are rendered directly.
			for _, page := range []string{"login_sent.html", "settings_garmin_mfa.html"} {
				rec := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r = r.WithContext(i18n.WithLang(r.Context(), i18n.ParseLang(lang)))
				h.server.render(rec, r, page, map[string]any{"Email": "x@example.com", "CSRFToken": "tok"})
				if rec.Code != http.StatusOK {
					t.Errorf("%s: status %d, want 200", page, rec.Code)
				}
				checkBody(page, rec.Body.String())
			}

			uid := loginAs(t, h, "layout-"+lang+"@example.com")
			if err := h.db.UpdatePreferredLang(uid, lang); err != nil {
				t.Fatalf("UpdatePreferredLang: %v", err)
			}
			for _, p := range private {
				check(p)
			}
		})
	}
}

// The legal pages are one route each; the template picks the text by
// language rather than the handler picking a file.
func TestLegalPages_FollowRequestLanguage(t *testing.T) {
	h := newTestHarness(t)
	srvURL, _ := url.Parse(h.srv.URL)
	cases := []struct{ lang, path, want, notWant string }{
		{"de", "/impressum", "<h1>Impressum</h1>", "Legal Notice"},
		{"en", "/impressum", "<h1>Legal Notice</h1>", "<h1>Impressum</h1>"},
		{"de", "/privacy", "<h1>Datenschutzerklärung</h1>", "Privacy Policy"},
		{"en", "/privacy", "<h1>Privacy Policy</h1>", "Datenschutzerklärung"},
	}
	for _, c := range cases {
		h.client.Jar.SetCookies(srvURL, []*http.Cookie{{Name: "lang", Value: c.lang, Path: "/"}})
		body := getBody(t, h.client, h.srv.URL+c.path)
		if !strings.Contains(body, c.want) {
			t.Errorf("%s %s: missing %q", c.lang, c.path, c.want)
		}
		if strings.Contains(body, c.notWant) {
			t.Errorf("%s %s: contains other language's %q", c.lang, c.path, c.notWant)
		}
	}
}

// The htmx error fragment comes from a template, styled by class, with the
// translated message escaped by html/template.
func TestSyncTrigger_HXErrorRendersFragment(t *testing.T) {
	h := newTestHarness(t)
	loginAs(t, h, "sync-error@example.com")
	csrf := csrfTokenFor(t, h)

	form := url.Values{"csrf_token": {csrf}, "start_date": {"nope"}, "end_date": {"2026-01-01"}}
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/sync/trigger", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Origin", h.srv.URL)
	resp, err := h.raw.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", resp.StatusCode, body)
	}
	want := i18n.T(i18n.EN, "flash.invalid_start_date")
	if !strings.Contains(body, `class="sync-error"`) || !strings.Contains(body, want) {
		t.Errorf("body = %q, want sync-error fragment with %q", body, want)
	}
	if strings.Contains(body, "style=") {
		t.Errorf("body = %q, want no inline style", body)
	}
}

// The dashboard and /sync/status share one partial, so a dashboard opened
// while a sync is running polls for updates just like the htmx response.
func TestDashboard_RunningSyncPolls(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "running@example.com")
	if err := h.db.UpdateSetupCompleted(uid, true); err != nil {
		t.Fatalf("UpdateSetupCompleted: %v", err)
	}
	if _, err := h.db.CreateSyncRun(uid, "manual"); err != nil { // status "running"
		t.Fatalf("CreateSyncRun: %v", err)
	}

	for _, path := range []string{"/dashboard", "/sync/status"} {
		body := getBody(t, h.client, h.srv.URL+path)
		if !strings.Contains(body, `hx-get="/sync/status"`) {
			t.Errorf("%s: running sync does not poll", path)
		}
		if !strings.Contains(body, i18n.T(i18n.EN, "sync.syncing")) {
			t.Errorf("%s: missing syncing text", path)
		}
	}
}

// A "running" row the server lost (restart, OOM) is never finished, so it
// must not keep a dashboard polling forever: past staleRunAfter it renders
// as a static badge.
func TestSyncStatus_StaleRunningRunDoesNotPoll(t *testing.T) {
	h := newTestHarness(t)
	rec := httptest.NewRecorder()
	h.server.render(rec, httptest.NewRequest(http.MethodGet, "/", nil), "sync_status.html", map[string]any{
		"LastSync": &database.SyncRun{Status: "running", StartedAt: time.Now().Add(-staleRunAfter - time.Minute)},
	})
	body := rec.Body.String()
	if strings.Contains(body, "hx-get") {
		t.Errorf("stale running run polls: %q", body)
	}
	if !strings.Contains(body, "status-running") {
		t.Errorf("stale running run lost its badge: %q", body)
	}
}

var (
	templateKeyRE = regexp.MustCompile(`(?:call|tf) \$?\.T "([^"]+)"`)
	goFlashKeyRE  = regexp.MustCompile(`"(flash\.[a-z0-9_.]+)"`)
)

// A missing translation key renders as the raw key, silently. Check every
// literal key used by the templates and every flash key set from Go.
func TestTranslationKeysExist(t *testing.T) {
	tmplKeys := map[string]bool{}
	err := fs.WalkDir(templatesFS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		src, err := fs.ReadFile(templatesFS, p)
		if err != nil {
			return err
		}
		for _, m := range templateKeyRE.FindAllStringSubmatch(string(src), -1) {
			tmplKeys[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
	// Guard against a regex that silently stops matching.
	if len(tmplKeys) < 150 {
		t.Fatalf("found only %d template keys; is the regex still right?", len(tmplKeys))
	}

	goKeys := map[string]bool{}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range goFlashKeyRE.FindAllStringSubmatch(string(src), -1) {
			goKeys[m[1]] = true
		}
	}
	if len(goKeys) < 20 {
		t.Fatalf("found only %d flash keys in Go sources; is the regex still right?", len(goKeys))
	}

	for _, set := range []map[string]bool{tmplKeys, goKeys} {
		for k := range set {
			if _, ok := i18n.En[k]; !ok {
				t.Errorf("translation key %q is used but missing from the i18n bundle", k)
			}
		}
	}
}
