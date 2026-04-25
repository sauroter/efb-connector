// Package efb provides an HTTP client for the Kanu-EFB portal
// (https://efb.kanu-efb.de/). It handles authentication via form-based login
// and GPX file uploads to the user map endpoint.
package efb

import (
	"bytes"
	"context"
	"fmt"
	gohtml "html"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is the base URL for the Kanu-EFB portal.
const DefaultBaseURL = "https://efb.kanu-efb.de"

// Compiled regular expressions used by parseUnassociatedTrack and
// parseFormFields. Defined at package level to avoid recompilation on each
// call (and within loops).
var (
	trackIDRe        = regexp.MustCompile(`name=['"]track_id:(\d+)['"]`)
	editRe           = regexp.MustCompile(`name=['"]edit:(\d+)['"]`)
	inputRe          = regexp.MustCompile(`<input\b[^>]*>`)
	nameRe           = regexp.MustCompile(`name=['"]([^'"]*?)['"]`)
	valueRe          = regexp.MustCompile(`value=['"]([^'"]*?)['"]`)
	typeRe           = regexp.MustCompile(`type=['"]([^'"]*?)['"]`)
	selectRe         = regexp.MustCompile(`(?s)<select\b[^>]*name=['"]([^'"]*?)['"][^>]*>(.*?)</select>`)
	selectedOptionRe = regexp.MustCompile(`<option\b[^>]*\bselected\b[^>]*>`)
	firstOptionRe    = regexp.MustCompile(`<option\b[^>]*value=['"]([^'"]*?)['"]`)
	textareaRe       = regexp.MustCompile(`(?s)<textarea\b[^>]*name=['"]([^'"]*?)['"][^>]*>(.*?)</textarea>`)
)

const (
	defaultLoginPath      = "/login"
	defaultUploadPath     = "/interpretation/usersmap"
	defaultTripCreatePath = "/trips/create"
)

// EFBClient is an authenticated HTTP client for the Kanu-EFB portal.
// After a successful Login call the cookie jar retains the session, so
// subsequent Upload calls do not require re-authentication.
//
// Session persistence across process restarts (exporting/importing the cookie
// jar to/from the database) is intentionally deferred to Phase 4 (Sync
// Engine), once the database layer exists.
type EFBClient struct {
	baseURL    string
	loginURL   string
	uploadURL  string
	httpClient *http.Client
}

// NewEFBClient returns a new EFBClient that talks to baseURL.
// Pass DefaultBaseURL for production use.
// The underlying HTTP client is configured with a cookie jar so that the
// session cookie received after Login is sent with every subsequent request.
func NewEFBClient(baseURL string) *EFBClient {
	// Strip trailing slash for predictable URL construction.
	baseURL = strings.TrimRight(baseURL, "/")

	jar, err := cookiejar.New(nil)
	if err != nil {
		// cookiejar.New only returns an error when the PublicSuffixList is
		// non-nil and malformed, which cannot happen with a nil argument.
		panic(fmt.Sprintf("efb: failed to create cookie jar: %v", err))
	}

	return &EFBClient{
		baseURL:   baseURL,
		loginURL:  baseURL + defaultLoginPath,
		uploadURL: baseURL + defaultUploadPath,
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
	}
}

// Login authenticates with the EFB portal using the provided credentials.
// On success the session cookie is stored in the client's cookie jar and all
// subsequent requests will be authenticated automatically.
//
// Login detects authentication failure by checking whether the server
// redirects back to the login page after the POST — the standard behaviour
// of the EFB portal when credentials are invalid.
func (c *EFBClient) Login(ctx context.Context, username, password string) error {
	formData := url.Values{}
	formData.Set("username", username)
	formData.Set("password", password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.loginURL,
		strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("efb: failed to build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("efb: login request failed: %w", err)
	}
	defer resp.Body.Close()
	// Consume body so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)

	// If the server redirected us back to the login page, the credentials
	// were rejected.
	if strings.HasSuffix(resp.Request.URL.Path, defaultLoginPath) {
		return fmt.Errorf("efb: login failed: invalid credentials")
	}

	return nil
}

// Upload sends gpxData to the EFB portal as a multipart file upload.
// filename is used as the submitted file name (typically the basename of the
// source file).  The caller must have called Login first.
//
// Upload returns an error when the server response does not contain the
// success marker "Datenbank gespeichert".
func (c *EFBClient) Upload(ctx context.Context, gpxData []byte, filename string) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// The portal expects the file in a field named "selectFile".
	part, err := writer.CreateFormFile("selectFile", filename)
	if err != nil {
		return fmt.Errorf("efb: failed to create form file field: %w", err)
	}
	if _, err = io.Copy(part, bytes.NewReader(gpxData)); err != nil {
		return fmt.Errorf("efb: failed to write GPX data: %w", err)
	}

	// The portal requires a submit-button field to trigger processing.
	if err = writer.WriteField("uploadFile", "Datei hochladen"); err != nil {
		return fmt.Errorf("efb: failed to write uploadFile field: %w", err)
	}

	if err = writer.Close(); err != nil {
		return fmt.Errorf("efb: failed to finalise multipart body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.uploadURL, body)
	if err != nil {
		return fmt.Errorf("efb: failed to build upload request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Origin", c.baseURL)
	req.Header.Set("Referer", c.uploadURL)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("efb: upload request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("efb: failed to read upload response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("efb: upload failed with status %d: %s",
			resp.StatusCode, summariseResponse(respBody))
	}

	respText := string(respBody)

	// The portal returns a page containing "Datenbank gespeichert" on success.
	if strings.Contains(respText, "Datenbank gespeichert") {
		return nil
	}

	// Detect session expiry: the portal redirected us to the login page.
	if isLoginPage(respText) {
		return fmt.Errorf("efb: session expired during upload (got login page)")
	}

	return fmt.Errorf("efb: upload did not succeed: %s", summariseResponse(respBody))
}

// isLoginPage returns true if the HTML body looks like the EFB login page,
// indicating that the session has expired or was never established.
func isLoginPage(body string) bool {
	return strings.Contains(body, "Benutzername hier eingeben") ||
		(strings.Contains(body, "<title>") &&
			strings.Contains(body, "eFB") &&
			strings.Contains(body, "username") &&
			strings.Contains(body, "password"))
}

// UploadFile is a convenience wrapper around Upload that reads a GPX file
// from disk and uploads it.  The basename of filePath is used as the
// submitted file name.
func (c *EFBClient) UploadFile(ctx context.Context, filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("efb: failed to read file %q: %w", filePath, err)
	}
	return c.Upload(ctx, data, filepath.Base(filePath))
}

// ValidateCredentials logs in with the provided credentials and then verifies
// that the session is active.  It is intended for use during account
// connection flows where the caller needs to confirm that a given
// username/password pair is accepted by the portal before storing them.
func (c *EFBClient) ValidateCredentials(ctx context.Context, username, password string) error {
	if err := c.Login(ctx, username, password); err != nil {
		return err
	}
	if !c.IsSessionValid(ctx) {
		return fmt.Errorf("efb: session is not valid after login")
	}
	return nil
}

// IsSessionValid reports whether the current session cookie is still accepted
// by the portal.  It performs a GET to the upload page and checks that the
// server does not redirect to the login page.
//
// Returns false when there is no active session, when the session has expired,
// or when the request fails.
func (c *EFBClient) IsSessionValid(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.uploadURL, nil)
	if err != nil {
		return false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	// If the server redirected us to the login page the session has expired.
	return !strings.HasSuffix(resp.Request.URL.Path, defaultLoginPath)
}

// FindUnassociatedTrack searches the tracks page (/interpretation/usersmap)
// for a track matching gpxFilename that does not yet have a trip.
// Returns the EFB track ID or empty string if not found / already associated.
func (c *EFBClient) FindUnassociatedTrack(ctx context.Context, gpxFilename string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.uploadURL, nil)
	if err != nil {
		return "", fmt.Errorf("efb: failed to build tracks request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("efb: tracks request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("efb: failed to read tracks response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("efb: tracks page returned status %d", resp.StatusCode)
	}

	// Detect if we were redirected to the login page.
	if isLoginPage(string(body)) {
		return "", fmt.Errorf("efb: session expired, got login page instead of tracks")
	}

	return parseUnassociatedTrack(string(body), gpxFilename), nil
}

// parseUnassociatedTrack scans the tracks page HTML for a track row containing
// gpxFilename. If the row has a "track_id:NNN" button (no trip yet), the track
// ID is returned. If the row has an "edit:NNN" button (trip exists) or the
// filename is not found, an empty string is returned.
func parseUnassociatedTrack(htmlBody, gpxFilename string) string {
	// Each track row is a <div style="overflow:auto;..."> block containing
	// the filename and either a track_id:NNN or edit:NNN input button.
	// We find the enclosing row boundaries rather than using a fixed window
	// to avoid bleeding into adjacent track rows.

	const rowDelimiter = `<div style="overflow:auto`

	idx := 0
	for {
		pos := strings.Index(htmlBody[idx:], gpxFilename)
		if pos == -1 {
			return ""
		}
		pos += idx

		// Search backwards from the match to find the nearest row delimiter.
		start := strings.LastIndex(htmlBody[:pos], rowDelimiter)
		if start == -1 {
			start = 0
		}

		// Search forwards to find the next row delimiter (or end of string).
		end := strings.Index(htmlBody[pos:], rowDelimiter)
		if end == -1 {
			end = len(htmlBody)
		} else {
			end += pos
		}

		chunk := htmlBody[start:end]

		// Look for track_id:NNN pattern — means no trip yet.
		trackIDMatch := trackIDRe.FindStringSubmatch(chunk)
		if trackIDMatch != nil {
			return trackIDMatch[1]
		}

		// Look for edit:NNN pattern — means trip already exists.
		editMatch := editRe.FindStringSubmatch(chunk)
		if editMatch != nil {
			return ""
		}

		idx = pos + len(gpxFilename)
	}
}

// CreateTripFromTrack navigates to the trip creation form for the given EFB
// track ID, fills in start/end times, and submits the form.
func (c *EFBClient) CreateTripFromTrack(ctx context.Context, trackID string, startTime time.Time, durationSecs float64, enrichment *TripEnrichment) error {
	// Step 1: POST to /interpretation/usersmap to simulate clicking the
	// "Fahrt neu anlegen" image button, which redirects to /trips/create.
	clickFieldName := fmt.Sprintf("track_id:%s", trackID)
	formData := url.Values{}
	formData.Set(clickFieldName+".x", "1")
	formData.Set(clickFieldName+".y", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.uploadURL,
		strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("efb: failed to build track click request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", c.baseURL)
	req.Header.Set("Referer", c.uploadURL)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("efb: track click request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("efb: failed to read trip form response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("efb: trip form returned status %d: %s",
			resp.StatusCode, truncateBody(body))
	}

	if !strings.Contains(string(body), "begdate") {
		return fmt.Errorf("efb: trip creation form not found after track click (status %d)", resp.StatusCode)
	}

	// Step 2: Parse the form HTML to extract all field values.
	formValues := parseFormFields(string(body))

	// Step 3: Fill in start and end times.
	endTime := startTime.Add(time.Duration(durationSecs) * time.Second)

	formValues.Set("beghour", fmt.Sprintf("%d", startTime.Hour()))
	formValues.Set("begminute", fmt.Sprintf("%d", startTime.Minute()))
	formValues.Set("enddate", endTime.Format("02.01.2006"))
	formValues.Set("endhour", fmt.Sprintf("%d", endTime.Hour()))
	formValues.Set("endminute", fmt.Sprintf("%d", endTime.Minute()))
	formValues.Set("save", "speichern")

	// Prepend enrichment data to the comment field if provided.
	if enrichment != nil {
		existing := formValues.Get("comment")
		formValues.Set("comment", enrichment.FormatComment()+"\n"+existing)
	}

	// Step 4: POST the completed form.
	tripCreateURL := c.baseURL + defaultTripCreatePath
	req2, err := http.NewRequestWithContext(ctx, http.MethodPost, tripCreateURL,
		strings.NewReader(formValues.Encode()))
	if err != nil {
		return fmt.Errorf("efb: failed to build trip save request: %w", err)
	}
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.Header.Set("Origin", c.baseURL)
	req2.Header.Set("Referer", tripCreateURL)

	resp2, err := c.httpClient.Do(req2)
	if err != nil {
		return fmt.Errorf("efb: trip save request failed: %w", err)
	}
	defer resp2.Body.Close()

	body2, err := io.ReadAll(resp2.Body)
	if err != nil {
		return fmt.Errorf("efb: failed to read trip save response: %w", err)
	}

	// Accept 200 OK or redirect (3xx followed to a success page).
	if resp2.StatusCode != http.StatusOK {
		return fmt.Errorf("efb: trip save failed with status %d: %s",
			resp2.StatusCode, truncateBody(body2))
	}

	// Verify the response does not contain error indicators.
	respText := string(body2)
	if strings.Contains(respText, "Fehler") || strings.Contains(respText, "error") {
		return fmt.Errorf("efb: trip creation may have failed (response contains error indicator)")
	}

	return nil
}

// parseFormFields extracts all <input>, <select>, and <textarea> name/value
// pairs from the HTML. For <select> elements, the value of the <option> with
// the "selected" attribute is used. For multi-value fields (e.g. name ending
// with "[]"), multiple values are preserved via url.Values.Add.
func parseFormFields(html string) url.Values {
	vals := url.Values{}

	// Parse <input> elements.
	for _, match := range inputRe.FindAllString(html, -1) {
		nameMatch := nameRe.FindStringSubmatch(match)
		if nameMatch == nil {
			continue
		}
		name := nameMatch[1]

		// Skip submit and image buttons — we set "save" explicitly.
		typeMatch := typeRe.FindStringSubmatch(match)
		if typeMatch != nil {
			t := strings.ToLower(typeMatch[1])
			if t == "submit" || t == "image" || t == "button" {
				continue
			}
			// For checkboxes/radios, only include if "checked".
			if t == "checkbox" || t == "radio" {
				if !strings.Contains(match, "checked") {
					continue
				}
			}
		}

		value := ""
		valueMatch := valueRe.FindStringSubmatch(match)
		if valueMatch != nil {
			value = gohtml.UnescapeString(valueMatch[1])
		}
		vals.Add(name, value)
	}

	// Parse <select> elements with their selected options.
	for _, match := range selectRe.FindAllStringSubmatch(html, -1) {
		name := match[1]
		opts := match[2]

		// Find the <option> tag with "selected", then extract value from it.
		if selMatch := selectedOptionRe.FindString(opts); selMatch != "" {
			if valMatch := valueRe.FindStringSubmatch(selMatch); len(valMatch) > 1 {
				vals.Add(name, gohtml.UnescapeString(valMatch[1]))
				continue
			}
		}
		// No selected option; try the first option's value.
		firstOption := firstOptionRe.FindStringSubmatch(opts)
		if firstOption != nil {
			vals.Add(name, gohtml.UnescapeString(firstOption[1]))
		}
	}

	// Parse <textarea> elements.
	for _, match := range textareaRe.FindAllStringSubmatch(html, -1) {
		vals.Add(match[1], gohtml.UnescapeString(match[2]))
	}

	return vals
}

// titleRe extracts the content of the first <title> tag.
var titleRe = regexp.MustCompile(`(?i)<title[^>]*>\s*(.*?)\s*</title>`)

// alertRe extracts text from Bootstrap-style alert/error divs that the EFB
// portal uses for user-visible messages.
var alertRe = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*(?:alert|error|warning)[^"]*"[^>]*>(.*?)</div>`)

// tagStripRe matches HTML tags for stripping.
var tagStripRe = regexp.MustCompile(`<[^>]+>`)

// efbHints are German-language patterns the EFB portal may embed in its HTML
// when an upload is silently rejected. Each entry maps a substring to a
// human-readable hint.
var efbHints = []struct {
	pattern string
	hint    string
}{
	{"bereits vorhanden", "duplicate track already exists"},
	{"existiert bereits", "track already exists"},
	{"Keine GPS", "no GPS data in file"},
	{"keine Trackpunkte", "no trackpoints in file"},
	{"nicht gelesen", "file could not be read"},
	{"nicht verarbeitet", "file could not be processed"},
	{"Datei ist zu", "file size rejected"},
	{"ungültig", "invalid file"},
	{"Fehler beim", "processing error"},
}

// summariseResponse returns a concise summary of an HTML response body for
// use in error messages. It extracts the page title, scans for EFB-specific
// error hints and alert messages, falling back to truncated body text. This
// avoids dumping raw HTML into structured logs while preserving actionable
// diagnostic information.
func summariseResponse(b []byte) string {
	s := string(b)

	var parts []string

	// Extract page title.
	if m := titleRe.FindStringSubmatch(s); m != nil {
		title := strings.TrimSpace(m[1])
		if title != "" {
			parts = append(parts, fmt.Sprintf("page title: %q", title))
		}
	}

	// Scan for known EFB error/warning patterns.
	for _, h := range efbHints {
		if strings.Contains(s, h.pattern) {
			parts = append(parts, fmt.Sprintf("hint: %s", h.hint))
			break // one hint is enough
		}
	}

	// Extract text from alert/error/warning divs.
	if m := alertRe.FindStringSubmatch(s); m != nil {
		text := strings.TrimSpace(tagStripRe.ReplaceAllString(m[1], " "))
		text = strings.Join(strings.Fields(text), " ") // collapse whitespace
		if len(text) > 200 {
			text = text[:200] + "…"
		}
		if text != "" {
			parts = append(parts, fmt.Sprintf("alert: %q", text))
		}
	}

	if len(parts) > 0 {
		return fmt.Sprintf("%s (%d bytes)", strings.Join(parts, "; "), len(b))
	}

	// Not HTML or no recognisable content — truncate the raw body.
	const maxLen = 200
	if len(b) <= maxLen {
		return s
	}
	return string(b[:maxLen]) + "…"
}

// truncateBody returns up to 500 bytes of body as a string, preventing full
// HTML error pages from appearing in error messages.
func truncateBody(b []byte) string {
	const maxLen = 500
	if len(b) <= maxLen {
		return string(b)
	}
	return string(b[:maxLen]) + "…"
}
