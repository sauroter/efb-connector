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

func (s *AuthService) isDevMode() bool {
	return s.resendAPIKey == "" ||
		strings.HasPrefix(s.resendAPIKey, "re_test") ||
		s.resendAPIKey == "placeholder"
}

// SendEmail dispatches a multipart (HTML + plain-text) email via the
// Resend HTTP API. In dev mode the email is logged instead of sent so
// magic links and notifications are still observable in development.
//
// All outgoing email content is rendered by internal/mailer; this
// method is the transport layer only and should not be called directly
// from feature code.
func (s *AuthService) SendEmail(to, subject, htmlBody, textBody string) error {
	if s.isDevMode() {
		slog.Warn("DEV MODE: email not sent",
			"to", to,
			"subject", subject,
			"html_len", len(htmlBody),
			"text_len", len(textBody),
		)
		return nil
	}

	payload := map[string]interface{}{
		"from":    s.emailFrom,
		"to":      []string{to},
		"subject": subject,
		"html":    htmlBody,
		"text":    textBody,
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
