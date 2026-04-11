package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// resendEndpoint is the Resend API endpoint for sending emails.
// It is a package-level variable so tests can override it.
var resendEndpoint = "https://api.resend.com/emails"

// SendMagicLinkEmail sends a magic link login email to the given address via
// the Resend HTTP API. The magic link URL is constructed from baseURL and
// token.
func (s *AuthService) isDevMode() bool {
	return s.resendAPIKey == "" ||
		strings.HasPrefix(s.resendAPIKey, "re_test") ||
		s.resendAPIKey == "placeholder"
}

// SendEmail sends an email via the Resend HTTP API.  In dev mode the email is
// logged instead of sent.
func (s *AuthService) SendEmail(to, subject, htmlBody string) error {
	if s.isDevMode() {
		slog.Warn("DEV MODE: email not sent",
			"to", to,
			"subject", subject,
		)
		return nil
	}

	payload := map[string]interface{}{
		"from":    s.emailFrom,
		"to":      []string{to},
		"subject": subject,
		"html":    htmlBody,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("auth: marshal email payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, resendEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("auth: create email request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.resendAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth: send email: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("auth: resend API error (status %d): %s", resp.StatusCode, respBody)
	}

	return nil
}

// SendMagicLinkEmail sends a magic link login email to the given address.
func (s *AuthService) SendMagicLinkEmail(to, token, baseURL, lang string) error {
	link := baseURL + "/auth/verify?token=" + token

	if s.isDevMode() {
		slog.Warn("DEV MODE: magic link email not sent — click the link below to log in",
			"to", to,
			"link", link,
		)
		return nil
	}

	subject := "Your EFB Connector Login Link"
	htmlBody := fmt.Sprintf(`<p>Click to log in: <a href="%s">Log in to EFB Connector</a></p><p>This link expires in 15 minutes.</p>`, link)
	if lang == "de" {
		subject = "Dein EFB Connector Login-Link"
		htmlBody = fmt.Sprintf(`<p>Klicke hier, um dich anzumelden: <a href="%s">Bei EFB Connector anmelden</a></p><p>Dieser Link ist 15 Minuten gültig.</p>`, link)
	}

	return s.SendEmail(to, subject, htmlBody)
}
