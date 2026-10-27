package garmin

import (
	"context"
	"fmt"
	"sync"
	"time"
)

var _ GarminProvider = (*MockGarminProvider)(nil)

// MockGarminProvider implements GarminProvider with canned data. It is
// intended for local development and integration tests.
//
// In dev mode one instance is shared by the web handlers and every sync
// worker, so all methods take mu. The exported fields are configuration:
// set them before the provider is in use.
type MockGarminProvider struct {
	mu sync.Mutex

	// Activities to return from ListActivities, which keeps only those
	// inside the requested window. If nil, default sample activities are
	// generated.
	Activities []Activity

	// Diagnostics returned alongside Activities. Zero value is fine —
	// callers must tolerate empty diagnostics.
	Diagnostics ListDiagnostics

	// ListErr, if set, is returned by ListActivities.
	ListErr error

	// GPXData maps activity IDs to GPX bytes. Missing IDs get a minimal
	// valid GPX document.
	GPXData map[string][]byte

	// DownloadErr maps activity IDs to errors returned by DownloadGPX.
	DownloadErr map[string]error

	// ValidateErr, if set, is returned by ValidateCredentials.
	ValidateErr error

	// SimulateMFA, if true, causes ValidateWithMFA to return "needs_mfa"
	// and accept any code in CompleteMFA.
	SimulateMFA bool

	// MFAErr, if set, is returned by CompleteMFA.
	MFAErr error

	// mfaPending tracks users with pending MFA sessions.
	mfaPending map[int64]bool

	// lastOpts is the ListOptions value passed to the most recent
	// ListActivities call; read it through LastListOptions.
	lastOpts ListOptions
}

// NewMockGarminProvider returns a MockGarminProvider with sample activities.
func NewMockGarminProvider() *MockGarminProvider {
	return &MockGarminProvider{
		Activities: defaultActivities(),
	}
}

// ListActivities returns the canned activities that fall within
// [start, end), filtered the same way as the real provider. The sample
// activities carry a time of day in Date where the real provider's is
// date-only; normalising them to midnight would change which ones survive
// a short window.
func (m *MockGarminProvider) ListActivities(_ context.Context, _ GarminCredentials, start, end time.Time, opts ListOptions) ([]Activity, ListDiagnostics, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastOpts = opts
	if m.ListErr != nil {
		return nil, ListDiagnostics{}, m.ListErr
	}
	all := m.Activities
	if all == nil {
		all = defaultActivities()
	}
	var filtered []Activity
	for _, a := range all {
		if inWindow(a, start, end) {
			filtered = append(filtered, a)
		}
	}
	return filtered, m.Diagnostics, nil
}

// LastListOptions returns the ListOptions passed to the most recent
// ListActivities call, so tests can assert on per-call preferences (e.g.
// MatchByName).
func (m *MockGarminProvider) LastListOptions() ListOptions {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastOpts
}

// ListActivitiesRaw returns every canned activity, ignoring the window —
// the water-sport filter distinction is enforced by the Python script in
// production. Tests that need to distinguish should set
// MockGarminProvider.Activities to a curated raw set.
func (m *MockGarminProvider) ListActivitiesRaw(_ context.Context, _ GarminCredentials, _ int) ([]Activity, ListDiagnostics, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListErr != nil {
		return nil, ListDiagnostics{}, m.ListErr
	}
	if m.Activities != nil {
		return m.Activities, m.Diagnostics, nil
	}
	return defaultActivities(), m.Diagnostics, nil
}

func (m *MockGarminProvider) DownloadGPX(_ context.Context, _ GarminCredentials, activityID string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DownloadErr != nil {
		if err, ok := m.DownloadErr[activityID]; ok {
			return nil, err
		}
	}
	if m.GPXData != nil {
		if data, ok := m.GPXData[activityID]; ok {
			return data, nil
		}
	}
	return []byte(minimalGPX(activityID)), nil
}

func (m *MockGarminProvider) ValidateCredentials(_ context.Context, _ GarminCredentials) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ValidateErr
}

func (m *MockGarminProvider) ValidateWithMFA(_ context.Context, userID int64, _ GarminCredentials) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ValidateErr != nil {
		return "", m.ValidateErr
	}
	if m.SimulateMFA {
		if m.mfaPending == nil {
			m.mfaPending = make(map[int64]bool)
		}
		m.mfaPending[userID] = true
		return "needs_mfa", nil
	}
	return "ok", nil
}

func (m *MockGarminProvider) CompleteMFA(userID int64, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mfaPending == nil || !m.mfaPending[userID] {
		return fmt.Errorf("garmin: no MFA session for user %d", userID)
	}
	if m.MFAErr != nil {
		delete(m.mfaPending, userID)
		return m.MFAErr
	}
	delete(m.mfaPending, userID)
	return nil
}

func (m *MockGarminProvider) HasMFASession(userID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mfaPending == nil {
		return false
	}
	return m.mfaPending[userID]
}

func defaultActivities() []Activity {
	now := time.Now()
	return []Activity{
		{ProviderID: "mock-1", Name: "Morning Paddle", Type: "kayaking", Date: now.Add(-2 * time.Hour), StartTime: now.Add(-2 * time.Hour), StartLat: 47.58, StartLng: 12.70, EndLat: 47.60, EndLng: 12.71, DurationSecs: 3600, DistanceM: 5000},
		{ProviderID: "mock-2", Name: "Evening SUP", Type: "sup", Date: now.Add(-26 * time.Hour), StartTime: now.Add(-26 * time.Hour), StartLat: 47.59, StartLng: 12.71, EndLat: 47.61, EndLng: 12.72, DurationSecs: 2700, DistanceM: 3500},
		{ProviderID: "mock-3", Name: "Weekend Canoe Trip", Type: "canoeing", Date: now.Add(-50 * time.Hour), StartTime: now.Add(-50 * time.Hour), StartLat: 47.60, StartLng: 12.72, EndLat: 47.62, EndLng: 12.73, DurationSecs: 7200, DistanceM: 12000},
	}
}

func minimalGPX(activityID string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1" creator="mock-garmin">
  <metadata><name>Mock Activity %s</name></metadata>
  <trk><name>Mock Track</name><trkseg>
    <trkpt lat="52.5200" lon="13.4050"><ele>34</ele></trkpt>
    <trkpt lat="52.5210" lon="13.4060"><ele>35</ele></trkpt>
  </trkseg></trk>
</gpx>`, activityID)
}
