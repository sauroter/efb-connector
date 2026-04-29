package mailer

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"efb-connector/internal/i18n"
)

// fakeSender records every dispatch and lets tests inject errors.
type fakeSender struct {
	mu      sync.Mutex
	calls   []sentEmail
	failure error
}

type sentEmail struct {
	To      string
	Subject string
	HTML    string
	Text    string
}

func (f *fakeSender) SendEmail(to, subject, htmlBody, textBody string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failure != nil {
		return f.failure
	}
	f.calls = append(f.calls, sentEmail{to, subject, htmlBody, textBody})
	return nil
}

func (f *fakeSender) last() sentEmail {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return sentEmail{}
	}
	return f.calls[len(f.calls)-1]
}

func newTestMailer(t *testing.T, sender Sender) *Mailer {
	t.Helper()
	m, err := New(sender, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("mailer.New: %v", err)
	}
	return m
}

func TestMailer_MagicLink_RendersBothBodiesAndLocalisedSubject(t *testing.T) {
	cases := []struct {
		name        string
		lang        i18n.Lang
		wantSubject string
		wantPhrase  string // a body phrase distinctive to the language
	}{
		{"english", i18n.EN, "Your EFB Connector Login Link", "Click the link below"},
		{"german", i18n.DE, "Dein EFB Connector Login-Link", "Klicke auf den folgenden Link"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeSender{}
			m := newTestMailer(t, fs)

			link := "https://efb.example.com/auth/verify?token=abc123"
			if err := m.Send("user@example.com", tc.lang, "magic_link", map[string]any{"Link": link}); err != nil {
				t.Fatalf("Send: %v", err)
			}

			got := fs.last()
			if got.Subject != tc.wantSubject {
				t.Errorf("subject = %q, want %q", got.Subject, tc.wantSubject)
			}
			if !strings.Contains(got.HTML, tc.wantPhrase) {
				t.Errorf("html missing localised phrase %q in:\n%s", tc.wantPhrase, got.HTML)
			}
			if !strings.Contains(got.Text, tc.wantPhrase) {
				t.Errorf("text missing localised phrase %q in:\n%s", tc.wantPhrase, got.Text)
			}
			if !strings.Contains(got.HTML, link) || !strings.Contains(got.Text, link) {
				t.Errorf("magic link missing from rendered bodies\nhtml: %s\ntext: %s", got.HTML, got.Text)
			}
		})
	}
}

func TestMailer_EFBConsent_RendersInBothLanguages(t *testing.T) {
	cases := []struct {
		lang       i18n.Lang
		wantInBody string
	}{
		{i18n.EN, "anonymised"},
		{i18n.DE, "anonymisierten"},
	}
	consentURL := "https://efb.kanu-efb.de/interpretation/usersmap"

	for _, tc := range cases {
		t.Run(string(tc.lang), func(t *testing.T) {
			fs := &fakeSender{}
			m := newTestMailer(t, fs)
			if err := m.Send("u@example.com", tc.lang, "efb_consent", map[string]any{"ConsentURL": consentURL}); err != nil {
				t.Fatalf("Send: %v", err)
			}
			got := fs.last()
			if !strings.Contains(got.HTML, tc.wantInBody) {
				t.Errorf("html missing %q in:\n%s", tc.wantInBody, got.HTML)
			}
			// CTA link must reach the EFB tracks page in both bodies.
			if !strings.Contains(got.HTML, consentURL) || !strings.Contains(got.Text, consentURL) {
				t.Errorf("consent URL missing from rendered bodies")
			}
			// The "ich stimme zu" instruction is preserved verbatim in
			// both languages because it's the literal label on EFB.
			if !strings.Contains(got.HTML, "ich stimme zu") {
				t.Errorf("html should preserve literal EFB label 'ich stimme zu'")
			}
		})
	}
}

func TestMailer_GarminUpgrade_RendersSettingsLink(t *testing.T) {
	fs := &fakeSender{}
	m := newTestMailer(t, fs)
	settingsURL := "https://app.example.com/settings/garmin"
	if err := m.Send("u@example.com", i18n.DE, "garmin_upgrade", map[string]any{"SettingsURL": settingsURL}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := fs.last()
	if !strings.Contains(got.Subject, "Garmin-Integration") {
		t.Errorf("DE subject missing Garmin topic: %q", got.Subject)
	}
	if !strings.Contains(got.HTML, settingsURL) || !strings.Contains(got.Text, settingsURL) {
		t.Errorf("settings URL missing from rendered bodies")
	}
}

func TestMailer_Feedback_FormattedSubjectAndUserContent(t *testing.T) {
	fs := &fakeSender{}
	m := newTestMailer(t, fs)

	err := m.Send(
		"admin@example.com",
		i18n.EN,
		"feedback",
		map[string]any{
			"UserEmail": "submitter@example.com",
			"UserID":    int64(42),
			"Category":  "bug",
			"Message":   "the dashboard sometimes flickers",
		},
		"bug",
	)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := fs.last()
	if got.Subject != "EFB Connector Feedback [bug]" {
		t.Errorf("subject = %q, want formatted with category", got.Subject)
	}
	for _, want := range []string{"submitter@example.com", "user #42", "bug", "the dashboard sometimes flickers"} {
		if !strings.Contains(got.HTML, want) {
			t.Errorf("html missing %q", want)
		}
		if !strings.Contains(got.Text, want) {
			t.Errorf("text missing %q", want)
		}
	}
}

func TestMailer_Feedback_EscapesHTMLInUserContent(t *testing.T) {
	// User-submitted content must never reach the rendered HTML body
	// unescaped. html/template's auto-escaping is the safety net.
	fs := &fakeSender{}
	m := newTestMailer(t, fs)
	err := m.Send(
		"admin@example.com",
		i18n.EN,
		"feedback",
		map[string]any{
			"UserEmail": "x@example.com",
			"UserID":    int64(1),
			"Category":  "bug",
			"Message":   "<script>alert('xss')</script>",
		},
		"bug",
	)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := fs.last()
	if strings.Contains(got.HTML, "<script>") {
		t.Errorf("html body must not contain raw <script> tag from user input:\n%s", got.HTML)
	}
}

func TestMailer_UnknownLang_FallsBackToEnglish(t *testing.T) {
	fs := &fakeSender{}
	m := newTestMailer(t, fs)
	if err := m.Send("u@example.com", i18n.Lang("fr"), "magic_link", map[string]any{"Link": "https://x.example/y"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := fs.last()
	if got.Subject != "Your EFB Connector Login Link" {
		t.Errorf("unknown lang must fall back to EN subject; got %q", got.Subject)
	}
}

// TestMailer_AllTemplates_NoUnresolvedKeysOrActions guards against the
// most common i18n regression: a missing key in one language. i18n.T's
// fallback returns the raw key when neither the target lang nor EN has
// a translation, so a stray "email.foo.bar" string in the rendered
// output means a translation file is incomplete. This test renders
// every template in every supported language and fails on either an
// unresolved i18n key or a leftover {{...}} template action.
func TestMailer_AllTemplates_NoUnresolvedKeysOrActions(t *testing.T) {
	cases := []struct {
		name        string
		data        map[string]any
		subjectArgs []any
	}{
		{"magic_link", map[string]any{"Link": "https://example.test/x"}, nil},
		{"efb_consent", map[string]any{"ConsentURL": "https://example.test/x"}, nil},
		{"garmin_upgrade", map[string]any{"SettingsURL": "https://example.test/x"}, nil},
		{
			"feedback",
			map[string]any{"UserEmail": "x@example.test", "UserID": int64(1), "Category": "general", "Message": "msg"},
			[]any{"general"},
		},
	}

	for _, tc := range cases {
		for _, lang := range []i18n.Lang{i18n.EN, i18n.DE} {
			t.Run(tc.name+"/"+string(lang), func(t *testing.T) {
				fs := &fakeSender{}
				m := newTestMailer(t, fs)
				if err := m.Send("u@example.test", lang, tc.name, tc.data, tc.subjectArgs...); err != nil {
					t.Fatalf("Send: %v", err)
				}
				got := fs.last()
				bodies := map[string]string{"subject": got.Subject, "html": got.HTML, "text": got.Text}
				for tag, body := range bodies {
					if body == "" {
						t.Errorf("%s body empty", tag)
					}
					if strings.Contains(body, "{{") || strings.Contains(body, "}}") {
						t.Errorf("%s contains unresolved template action: %q", tag, body)
					}
					if strings.Contains(body, "email.") {
						t.Errorf("%s contains unresolved i18n key (likely missing translation): %q", tag, body)
					}
				}
			})
		}
	}
}

func TestMailer_Send_PropagatesSenderError(t *testing.T) {
	wantErr := errors.New("transport down")
	fs := &fakeSender{failure: wantErr}
	m := newTestMailer(t, fs)
	err := m.Send("u@example.com", i18n.EN, "magic_link", map[string]any{"Link": "https://x"})
	if !errors.Is(err, wantErr) {
		t.Errorf("Send error = %v, want %v", err, wantErr)
	}
}
