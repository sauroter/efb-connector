// Package mailer renders i18n-aware HTML and plain-text emails from
// embedded templates and dispatches them through a pluggable Sender.
//
// All outgoing emails go through this package: there is no direct
// inline-HTML email construction anywhere else in the codebase. To add
// a new email, drop a pair of <name>.html.tmpl + <name>.txt.tmpl files
// into templates/, add the email.<name>.* keys to internal/i18n, and
// call Mailer.Send from the relevant handler.
package mailer

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"log/slog"
	texttemplate "text/template"

	"efb-connector/internal/i18n"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// Sender dispatches a fully rendered email. *auth.AuthService implements
// this in production; tests provide a fake.
type Sender interface {
	SendEmail(to, subject, htmlBody, textBody string) error
}

// Mailer renders templated, i18n-aware emails.
//
// Templates are pre-parsed at construction time, once per supported
// language, with i18n.T/Tf bound into the FuncMap so template files can
// use {{ T "key" }} directly without threading lang through data.
type Mailer struct {
	sender    Sender
	htmlTmpls map[i18n.Lang]*htmltemplate.Template
	textTmpls map[i18n.Lang]*texttemplate.Template
	logger    *slog.Logger
}

// New parses all embedded templates for every supported language and
// returns a Mailer ready to Send.
func New(sender Sender, logger *slog.Logger) (*Mailer, error) {
	m := &Mailer{
		sender:    sender,
		htmlTmpls: make(map[i18n.Lang]*htmltemplate.Template),
		textTmpls: make(map[i18n.Lang]*texttemplate.Template),
		logger:    logger,
	}

	for _, lang := range []i18n.Lang{i18n.EN, i18n.DE} {
		l := lang
		t := func(key string) string { return i18n.T(l, key) }
		tf := func(key string, args ...any) string {
			return fmt.Sprintf(i18n.T(l, key), args...)
		}

		htmlTmpl, err := htmltemplate.New("").Funcs(htmltemplate.FuncMap{
			"T":  t,
			"Tf": tf,
		}).ParseFS(templatesFS, "templates/*.html.tmpl")
		if err != nil {
			return nil, fmt.Errorf("mailer: parse html templates (%s): %w", lang, err)
		}
		textTmpl, err := texttemplate.New("").Funcs(texttemplate.FuncMap{
			"T":  t,
			"Tf": tf,
		}).ParseFS(templatesFS, "templates/*.txt.tmpl")
		if err != nil {
			return nil, fmt.Errorf("mailer: parse text templates (%s): %w", lang, err)
		}

		m.htmlTmpls[lang] = htmlTmpl
		m.textTmpls[lang] = textTmpl
	}

	return m, nil
}

// Send renders templates "<name>.html.tmpl" and "<name>.txt.tmpl" in
// the requested language, looks up the subject from i18n key
// "email.<name>.subject" (formatted via subjectArgs if any), and
// dispatches via the underlying Sender.
//
// Unknown languages fall back to English; missing translation keys fall
// back through i18n.T's normal fallback chain (target lang → EN → raw key).
func (m *Mailer) Send(to string, lang i18n.Lang, name string, data map[string]any, subjectArgs ...any) error {
	htmlTmpl, ok := m.htmlTmpls[lang]
	if !ok {
		htmlTmpl = m.htmlTmpls[i18n.EN]
		lang = i18n.EN
	}
	textTmpl := m.textTmpls[lang]

	subject := i18n.T(lang, "email."+name+".subject")
	if len(subjectArgs) > 0 {
		subject = fmt.Sprintf(subject, subjectArgs...)
	}

	var htmlBuf bytes.Buffer
	if err := htmlTmpl.ExecuteTemplate(&htmlBuf, name+".html.tmpl", data); err != nil {
		return fmt.Errorf("mailer: render html %q: %w", name, err)
	}
	var textBuf bytes.Buffer
	if err := textTmpl.ExecuteTemplate(&textBuf, name+".txt.tmpl", data); err != nil {
		return fmt.Errorf("mailer: render text %q: %w", name, err)
	}

	return m.sender.SendEmail(to, subject, htmlBuf.String(), textBuf.String())
}
