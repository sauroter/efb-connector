package efb

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// UploadRecord records a single upload for test assertions.
type UploadRecord struct {
	Filename string
	Size     int
}

// MockEFBProvider implements EFBProvider by accepting all credentials and
// logging uploads. It is intended for local development and integration tests.
//
// In dev mode it can be flipped into "consent-gated" state via
// SetConsentGate so the local stack reproduces the EFB v2026.1
// track-usage consent flow end-to-end without talking to real EFB.
type MockEFBProvider struct {
	logger      *slog.Logger
	mu          sync.Mutex
	uploads     []UploadRecord
	consentGate atomic.Bool
}

// ConsentGateController is the optional interface MockEFBProvider
// satisfies so the dev-only admin endpoint can flip the simulated
// consent-gate state at runtime. The production *EFBClient does not
// implement this interface, so the endpoint is effectively dev-only.
type ConsentGateController interface {
	SetConsentGate(on bool)
	ConsentGate() bool
}

// NewMockEFBProvider returns a MockEFBProvider that logs to the given logger.
func NewMockEFBProvider(logger *slog.Logger) *MockEFBProvider {
	return &MockEFBProvider{logger: logger}
}

// SetConsentGate flips the simulated consent-gate state. When on, Upload
// returns a *UploadRejectedError carrying the consent-page body and
// CheckConsentGate / UploadRaw report the gate is active — same shape
// production hits when the user hasn't accepted EFB's track-usage
// agreement.
func (m *MockEFBProvider) SetConsentGate(on bool) {
	m.consentGate.Store(on)
	m.logger.Info("[mock-efb] consent gate", "on", on)
}

// ConsentGate reports the current simulated consent-gate state.
func (m *MockEFBProvider) ConsentGate() bool {
	return m.consentGate.Load()
}

// mockConsentGateBody is the response body the mock returns when the
// consent gate is active. It includes both markers IsConsentRequiredBody
// looks for, so the engine's detection path triggers correctly.
const mockConsentGateBody = `<html><head><title>eFB - elektronisches Fahrtenbuch des DKV</title></head><body>
<p>Das Hochladen kann erst durchgeführt werden, wenn Ihr
   der anonymisierten Verwendung Eurer Tracks zugestimmt habt.</p>
<form method="post"><input type="submit" name="commit_tracks" value="ich stimme zu"/></form>
</body></html>`

func (m *MockEFBProvider) Login(_ context.Context, username, _ string) error {
	m.logger.Info("[mock-efb] login", "username", username)
	return nil
}

func (m *MockEFBProvider) Upload(_ context.Context, gpxData []byte, filename string) error {
	if m.consentGate.Load() {
		m.logger.Info("[mock-efb] upload rejected: consent gate active", "filename", filename)
		body := []byte(mockConsentGateBody)
		return &UploadRejectedError{
			StatusCode:  http.StatusOK,
			BodySize:    len(body),
			BodyExcerpt: string(body),
			Summary:     summariseResponse(body),
		}
	}

	m.mu.Lock()
	m.uploads = append(m.uploads, UploadRecord{Filename: filename, Size: len(gpxData)})
	m.mu.Unlock()
	m.logger.Info("[mock-efb] upload", "filename", filename, "size", len(gpxData))
	return nil
}

func (m *MockEFBProvider) ValidateCredentials(_ context.Context, username, _ string) error {
	m.logger.Info("[mock-efb] validate credentials", "username", username)
	return nil
}

func (m *MockEFBProvider) FindUnassociatedTrack(_ context.Context, gpxFilename string) (string, error) {
	m.logger.Info("[mock-efb] find unassociated track", "gpxFilename", gpxFilename)
	return "mock-track-1", nil
}

func (m *MockEFBProvider) CreateTripFromTrack(_ context.Context, trackID string, startTime time.Time, durationSecs float64, enrichment *TripEnrichment) error {
	m.logger.Info("[mock-efb] create trip from track",
		"trackID", trackID,
		"startTime", startTime,
		"durationSecs", durationSecs,
		"hasEnrichment", enrichment != nil,
	)
	return nil
}

// CheckConsentGate reports whether the mock is currently simulating the
// EFB v2026.1 track-usage consent gate.
func (m *MockEFBProvider) CheckConsentGate(_ context.Context) (bool, error) {
	return m.consentGate.Load(), nil
}

// UploadRaw mirrors Upload but returns the raw response shape used by
// the debug-upload admin endpoint. When the consent gate is active it
// returns the consent-page body without recording the upload.
func (m *MockEFBProvider) UploadRaw(_ context.Context, gpxData []byte, filename string) (*RawUploadResult, error) {
	if m.consentGate.Load() {
		m.logger.Info("[mock-efb] upload-raw rejected: consent gate active", "filename", filename)
		body := []byte(mockConsentGateBody)
		return &RawUploadResult{
			RequestURL:            "mock://upload",
			FinalURL:              "mock://upload",
			StatusCode:            http.StatusOK,
			Header:                http.Header{"Content-Type": []string{"text/html; charset=UTF-8"}},
			Body:                  body,
			BodySize:              len(body),
			ContainsSuccessMarker: false,
			IsLoginPage:           false,
		}, nil
	}

	m.mu.Lock()
	m.uploads = append(m.uploads, UploadRecord{Filename: filename, Size: len(gpxData)})
	m.mu.Unlock()
	m.logger.Info("[mock-efb] upload raw", "filename", filename, "size", len(gpxData))

	body := []byte(filename + " in Datenbank gespeichert!")
	return &RawUploadResult{
		RequestURL:            "mock://upload",
		FinalURL:              "mock://upload",
		StatusCode:            http.StatusOK,
		Header:                http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
		Body:                  body,
		BodySize:              len(body),
		ContainsSuccessMarker: true,
		IsLoginPage:           false,
	}, nil
}

// Uploads returns a copy of all recorded uploads.
func (m *MockEFBProvider) Uploads() []UploadRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]UploadRecord, len(m.uploads))
	copy(out, m.uploads)
	return out
}
