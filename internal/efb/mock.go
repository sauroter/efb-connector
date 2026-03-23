package efb

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// UploadRecord records a single upload for test assertions.
type UploadRecord struct {
	Filename string
	Size     int
}

// MockEFBProvider implements EFBProvider by accepting all credentials and
// logging uploads. It is intended for local development and integration tests.
type MockEFBProvider struct {
	logger  *slog.Logger
	mu      sync.Mutex
	uploads []UploadRecord
}

// NewMockEFBProvider returns a MockEFBProvider that logs to the given logger.
func NewMockEFBProvider(logger *slog.Logger) *MockEFBProvider {
	return &MockEFBProvider{logger: logger}
}

func (m *MockEFBProvider) Login(_ context.Context, username, _ string) error {
	m.logger.Info("[mock-efb] login", "username", username)
	return nil
}

func (m *MockEFBProvider) Upload(_ context.Context, gpxData []byte, filename string) error {
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

// Uploads returns a copy of all recorded uploads.
func (m *MockEFBProvider) Uploads() []UploadRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]UploadRecord, len(m.uploads))
	copy(out, m.uploads)
	return out
}
