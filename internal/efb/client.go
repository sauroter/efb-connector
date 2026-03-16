// Package efb provides an HTTP client for the Kanu-EFB portal
// (https://efb.kanu-efb.de/). It handles authentication via form-based login
// and GPX file uploads to the user map endpoint.
package efb

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultBaseURL is the base URL for the Kanu-EFB portal.
const DefaultBaseURL = "https://efb.kanu-efb.de"

const (
	defaultLoginPath  = "/login"
	defaultUploadPath = "/interpretation/usersmap"
)

// EFBClient is an authenticated HTTP client for the Kanu-EFB portal.
// After a successful Login call the cookie jar retains the session, so
// subsequent Upload calls do not require re-authentication.
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
			Jar: jar,
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
			resp.StatusCode, string(respBody))
	}

	// The portal returns a page containing "Datenbank gespeichert" on success.
	if !strings.Contains(string(respBody), "Datenbank gespeichert") {
		return fmt.Errorf("efb: upload did not succeed: %s", string(respBody))
	}

	return nil
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
