// Package resend provides a client for the Resend Contacts, Segments, and
// Templates APIs.  It follows the same raw-HTTP pattern used in
// internal/auth/email.go — no SDK dependency.
package resend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// Endpoint base URLs — package-level vars so tests can override them.
var (
	contactsEndpoint  = "https://api.resend.com/contacts"
	segmentsEndpoint  = "https://api.resend.com/segments"
	templatesEndpoint = "https://api.resend.com/templates"
)

// Client wraps the Resend HTTP API for contacts, segments, and templates.
type Client struct {
	apiKey string
	logger *slog.Logger
}

// NewClient creates a Resend client.  If apiKey is empty, a placeholder, or a
// test key the client operates in dev mode (all calls are logged but skipped).
func NewClient(apiKey string, logger *slog.Logger) *Client {
	return &Client{apiKey: apiKey, logger: logger}
}

// isDevMode returns true when the API key indicates a non-production
// environment.  Matches the logic in internal/auth/email.go.
func (c *Client) isDevMode() bool {
	return c.apiKey == "" ||
		strings.HasPrefix(c.apiKey, "re_test") ||
		c.apiKey == "placeholder"
}

// ---------- Contacts ----------

// CreateContact creates or updates a contact in Resend.  Resend treats a
// create with an existing email as an upsert.
func (c *Client) CreateContact(email string, properties map[string]string) error {
	if c.isDevMode() {
		c.logger.Warn("DEV MODE: resend create contact skipped", "email", email)
		return nil
	}

	payload := map[string]any{
		"email": email,
	}
	if len(properties) > 0 {
		payload["properties"] = properties
	}

	_, err := c.doJSON(http.MethodPost, contactsEndpoint, payload)
	if err != nil {
		return fmt.Errorf("resend: create contact %q: %w", email, err)
	}
	return nil
}

// AddToSegment adds a contact (by email) to a segment.
func (c *Client) AddToSegment(email, segmentID string) error {
	if c.isDevMode() {
		c.logger.Warn("DEV MODE: resend add-to-segment skipped", "email", email, "segment", segmentID)
		return nil
	}

	endpoint := fmt.Sprintf("%s/%s/segments/%s", contactsEndpoint, url.PathEscape(email), url.PathEscape(segmentID))
	_, err := c.doJSON(http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("resend: add %q to segment %s: %w", email, segmentID, err)
	}
	return nil
}

// RemoveFromSegment removes a contact (by email) from a segment.
func (c *Client) RemoveFromSegment(email, segmentID string) error {
	if c.isDevMode() {
		c.logger.Warn("DEV MODE: resend remove-from-segment skipped", "email", email, "segment", segmentID)
		return nil
	}

	endpoint := fmt.Sprintf("%s/%s/segments/%s", contactsEndpoint, url.PathEscape(email), url.PathEscape(segmentID))
	_, err := c.doJSON(http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("resend: remove %q from segment %s: %w", email, segmentID, err)
	}
	return nil
}

// DeleteContact removes a contact by email.
func (c *Client) DeleteContact(email string) error {
	if c.isDevMode() {
		c.logger.Warn("DEV MODE: resend delete contact skipped", "email", email)
		return nil
	}

	endpoint := fmt.Sprintf("%s/%s", contactsEndpoint, url.PathEscape(email))
	_, err := c.doJSON(http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("resend: delete contact %q: %w", email, err)
	}
	return nil
}

// SyncUserSegment ensures a contact is in the correct segment and removed from
// the other.  If isActive is true the contact is placed in activeSegID and
// removed from needsSetupSegID; otherwise the reverse.
func (c *Client) SyncUserSegment(email string, isActive bool, activeSegID, needsSetupSegID string) error {
	var addSeg, removeSeg string
	if isActive {
		addSeg = activeSegID
		removeSeg = needsSetupSegID
	} else {
		addSeg = needsSetupSegID
		removeSeg = activeSegID
	}

	if addSeg != "" {
		if err := c.AddToSegment(email, addSeg); err != nil {
			return err
		}
	}
	if removeSeg != "" {
		// Best-effort removal — the contact may not be in this segment.
		if err := c.RemoveFromSegment(email, removeSeg); err != nil {
			c.logger.Warn("resend: remove from segment failed (may not be a member)", "email", email, "segment", removeSeg, "error", err)
		}
	}
	return nil
}

// ---------- Segments ----------

// Segment represents a Resend segment.
type Segment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CreateSegment creates a new segment and returns its ID.
func (c *Client) CreateSegment(name string) (string, error) {
	if c.isDevMode() {
		c.logger.Warn("DEV MODE: resend create segment skipped", "name", name)
		return "dev-segment-id", nil
	}

	body, err := c.doJSON(http.MethodPost, segmentsEndpoint, map[string]string{"name": name})
	if err != nil {
		return "", fmt.Errorf("resend: create segment %q: %w", name, err)
	}

	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("resend: parse create-segment response: %w", err)
	}
	return resp.ID, nil
}

// ListSegments returns all segments.
func (c *Client) ListSegments() ([]Segment, error) {
	if c.isDevMode() {
		return nil, nil
	}

	body, err := c.doJSON(http.MethodGet, segmentsEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("resend: list segments: %w", err)
	}

	var resp struct {
		Data []Segment `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("resend: parse list-segments response: %w", err)
	}
	return resp.Data, nil
}

// ---------- Templates ----------

// Template represents a Resend email template.
type Template struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Alias string `json:"alias"`
}

// CreateTemplate creates a new email template and returns its ID.
func (c *Client) CreateTemplate(name, alias, subject, html string) (string, error) {
	if c.isDevMode() {
		c.logger.Warn("DEV MODE: resend create template skipped", "name", name)
		return "dev-template-id", nil
	}

	payload := map[string]string{
		"name":    name,
		"alias":   alias,
		"subject": subject,
		"html":    html,
	}

	body, err := c.doJSON(http.MethodPost, templatesEndpoint, payload)
	if err != nil {
		return "", fmt.Errorf("resend: create template %q: %w", name, err)
	}

	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("resend: parse create-template response: %w", err)
	}
	return resp.ID, nil
}

// UpdateTemplate updates an existing template by alias.
func (c *Client) UpdateTemplate(alias, subject, html string) error {
	if c.isDevMode() {
		c.logger.Warn("DEV MODE: resend update template skipped", "alias", alias)
		return nil
	}

	endpoint := fmt.Sprintf("%s/%s", templatesEndpoint, url.PathEscape(alias))
	payload := map[string]string{
		"subject": subject,
		"html":    html,
	}

	_, err := c.doJSON(http.MethodPatch, endpoint, payload)
	if err != nil {
		return fmt.Errorf("resend: update template %q: %w", alias, err)
	}
	return nil
}

// ListTemplates returns all templates.
func (c *Client) ListTemplates() ([]Template, error) {
	if c.isDevMode() {
		return nil, nil
	}

	body, err := c.doJSON(http.MethodGet, templatesEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("resend: list templates: %w", err)
	}

	var resp struct {
		Data []Template `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("resend: parse list-templates response: %w", err)
	}
	return resp.Data, nil
}

// PublishTemplate publishes a template by ID or alias.
func (c *Client) PublishTemplate(idOrAlias string) error {
	if c.isDevMode() {
		c.logger.Warn("DEV MODE: resend publish template skipped", "id", idOrAlias)
		return nil
	}

	endpoint := fmt.Sprintf("%s/%s/publish", templatesEndpoint, url.PathEscape(idOrAlias))
	_, err := c.doJSON(http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("resend: publish template %q: %w", idOrAlias, err)
	}
	return nil
}

// ---------- HTTP helpers ----------

// doJSON performs an HTTP request with optional JSON body and returns the
// response body.  It sets Authorization and Content-Type headers.
func (c *Client) doJSON(method, endpoint string, payload any) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal payload: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, endpoint, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, respBody)
	}

	return respBody, nil
}
