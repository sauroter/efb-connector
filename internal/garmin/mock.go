package garmin

import (
	"context"
	"fmt"
	"time"
)

// MockGarminProvider implements GarminProvider with canned data. It is
// intended for local development and integration tests.
type MockGarminProvider struct {
	// Activities to return from ListActivities. If nil, default sample
	// activities are generated.
	Activities []Activity

	// ListErr, if set, is returned by ListActivities.
	ListErr error

	// GPXData maps activity IDs to GPX bytes. Missing IDs get a minimal
	// valid GPX document.
	GPXData map[string][]byte

	// DownloadErr maps activity IDs to errors returned by DownloadGPX.
	DownloadErr map[string]error

	// ValidateErr, if set, is returned by ValidateCredentials.
	ValidateErr error
}

// NewMockGarminProvider returns a MockGarminProvider with sample activities.
func NewMockGarminProvider() *MockGarminProvider {
	return &MockGarminProvider{
		Activities: defaultActivities(),
	}
}

func (m *MockGarminProvider) ListActivities(_ context.Context, _ GarminCredentials, _, _ time.Time) ([]Activity, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	if m.Activities != nil {
		return m.Activities, nil
	}
	return defaultActivities(), nil
}

func (m *MockGarminProvider) DownloadGPX(_ context.Context, _ GarminCredentials, activityID string) ([]byte, error) {
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
	return m.ValidateErr
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
