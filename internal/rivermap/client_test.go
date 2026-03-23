package rivermap

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// mockSectionsResponse returns a JSON response with the given sections.
func mockSectionsResponse(sections ...sectionJSON) []byte {
	resp := sectionsResponse{Sections: sections}
	b, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	return b
}

// newSectionsServer creates a test server that returns the given response
// body for GET /v2/sections.
func newSectionsServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/sections", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body) //nolint:errcheck
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newReadingsServer creates a test server that returns the given response
// body for GET /v2/stations/{id}/readings.
func newReadingsServer(t *testing.T, stationID string, body []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/stations/"+stationID+"/readings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body) //nolint:errcheck
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// testLogger returns a silent logger for tests.
func testLogger() *slog.Logger {
	return slog.Default()
}

// rawJSON is a helper that marshals v to json.RawMessage for test data.
func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ---------------------------------------------------------------------------
// RefreshCache tests
// ---------------------------------------------------------------------------

func TestRefreshCache_ParsesSections(t *testing.T) {
	body := mockSectionsResponse(
		sectionJSON{
			ID:    "sec-1",
			River: map[string]string{"de": "Saalach", "en": "Saalach"},
			SectionName: map[string]json.RawMessage{
				"de": rawJSON(sectionNameDetail{From: "Lofer", To: "Scheffsnoth", FormattedName: "[Lofer - Scheffsnoth]"}),
			},
			Grade:         "III-IV",
			SpotGrades:    []string{"V"},
			PutInLatLng:   [2]float64{47582670, 12702775},
			TakeOutLatLng: [2]float64{47603386, 12704719},
			Calibration: &struct {
				StationID string  `json:"stationId"`
				Unit      string  `json:"unit"`
				LW        float64 `json:"lw"`
				MW        float64 `json:"mw"`
				HW        float64 `json:"hw"`
			}{
				StationID: "station-1",
				Unit:      "cm",
				LW:        30,
				MW:        60,
				HW:        120,
			},
		},
		sectionJSON{
			ID:    "sec-2",
			River: map[string]string{"de": "Isar", "en": "Isar"},
			SectionName: map[string]json.RawMessage{
				"en": rawJSON(sectionNameDetail{From: "Munich", To: "Freising"}),
			},
			Grade:         "II",
			PutInLatLng:   [2]float64{48137154, 11576124},
			TakeOutLatLng: [2]float64{48400000, 11750000},
			Calibration:   nil,
		},
	)

	srv := newSectionsServer(t, body)
	c := NewClient("test-key", srv.URL, "", testLogger())

	if err := c.RefreshCache(context.Background()); err != nil {
		t.Fatalf("RefreshCache failed: %v", err)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(c.sections))
	}

	// Verify first section.
	s1 := c.sections[0]
	if s1.ID != "sec-1" {
		t.Errorf("section 0 ID: expected 'sec-1', got %q", s1.ID)
	}
	if s1.River["de"] != "Saalach" {
		t.Errorf("section 0 river.de: expected 'Saalach', got %q", s1.River["de"])
	}
	if s1.Grade != "III-IV" {
		t.Errorf("section 0 grade: expected 'III-IV', got %q", s1.Grade)
	}
	if len(s1.SpotGrades) != 1 || s1.SpotGrades[0] != "V" {
		t.Errorf("section 0 spotGrades: expected ['V'], got %v", s1.SpotGrades)
	}

	// Verify coordinate conversion from micro-degrees.
	expectedLat := 47582670.0 / 1e6
	expectedLng := 12702775.0 / 1e6
	if s1.PutInLatLng[0] != expectedLat || s1.PutInLatLng[1] != expectedLng {
		t.Errorf("section 0 putInLatLng: expected [%f, %f], got %v",
			expectedLat, expectedLng, s1.PutInLatLng)
	}

	// Verify calibration.
	if s1.Calibration == nil {
		t.Fatal("section 0 calibration: expected non-nil")
	}
	if s1.Calibration.StationID != "station-1" {
		t.Errorf("section 0 calibration stationId: expected 'station-1', got %q", s1.Calibration.StationID)
	}
	if s1.Calibration.LW != 30 || s1.Calibration.MW != 60 || s1.Calibration.HW != 120 {
		t.Errorf("section 0 calibration thresholds: expected 30/60/120, got %v/%v/%v",
			s1.Calibration.LW, s1.Calibration.MW, s1.Calibration.HW)
	}

	// Verify display names.
	if s1.SectionFrom != "Lofer" || s1.SectionTo != "Scheffsnoth" {
		t.Errorf("section 0 display names: expected 'Lofer'/'Scheffsnoth', got %q/%q",
			s1.SectionFrom, s1.SectionTo)
	}

	// Verify second section (no calibration, English fallback).
	s2 := c.sections[1]
	if s2.ID != "sec-2" {
		t.Errorf("section 1 ID: expected 'sec-2', got %q", s2.ID)
	}
	if s2.Calibration != nil {
		t.Errorf("section 1 calibration: expected nil, got %v", s2.Calibration)
	}
	if s2.SectionFrom != "Munich" || s2.SectionTo != "Freising" {
		t.Errorf("section 1 display names: expected 'Munich'/'Freising', got %q/%q",
			s2.SectionFrom, s2.SectionTo)
	}
}

// ---------------------------------------------------------------------------
// FindSection tests
// ---------------------------------------------------------------------------

func TestFindSection_ProximityMatch(t *testing.T) {
	c := NewClient("test-key", "http://unused", "", testLogger())

	// Pre-populate cache with two sections.
	c.mu.Lock()
	c.sections = []Section{
		{
			ID:          "sec-A",
			PutInLatLng: [2]float64{47.582670, 12.702775}, // Lofer area
		},
		{
			ID:          "sec-B",
			PutInLatLng: [2]float64{48.137154, 11.576124}, // Munich area
		},
	}
	c.mu.Unlock()

	// Query near section A's put-in (< 2 km away).
	found := c.FindSection(47.583, 12.703)
	if found == nil {
		t.Fatal("expected to find a section, got nil")
	}
	if found.ID != "sec-A" {
		t.Errorf("expected section 'sec-A', got %q", found.ID)
	}
}

func TestFindSection_TooFar(t *testing.T) {
	c := NewClient("test-key", "http://unused", "", testLogger())

	// Pre-populate cache with one section in Austria.
	c.mu.Lock()
	c.sections = []Section{
		{
			ID:          "sec-A",
			PutInLatLng: [2]float64{47.582670, 12.702775},
		},
	}
	c.mu.Unlock()

	// Query from Berlin (~500 km away).
	found := c.FindSection(52.520, 13.405)
	if found != nil {
		t.Errorf("expected nil for distant query, got section %q", found.ID)
	}
}

func TestFindSection_EmptyCache(t *testing.T) {
	c := NewClient("test-key", "http://unused", "", testLogger())

	found := c.FindSection(47.583, 12.703)
	if found != nil {
		t.Errorf("expected nil for empty cache, got section %q", found.ID)
	}
}

// ---------------------------------------------------------------------------
// GetReadingsAt tests
// ---------------------------------------------------------------------------

func TestGetReadingsAt_FindsClosest(t *testing.T) {
	// Target time: 2024-03-21 13:00:00 UTC (Unix: 1711018800)
	targetTime := time.Unix(1711018800, 0).UTC()

	readingsBody, _ := json.Marshal(readingsResponse{
		Readings: map[string][]readingJSON{
			"cm": {
				{Ts: 1711018800, V: 47},  // exactly at target
				{Ts: 1711020600, V: 48},  // 30 min later
				{Ts: 1711015200, V: 45},  // 1 hour before
			},
			"m3s": {
				{Ts: 1711020600, V: 12.3}, // 30 min later
				{Ts: 1711015200, V: 11.0}, // 1 hour before
			},
		},
	})

	srv := newReadingsServer(t, "station-1", readingsBody)
	c := NewClient("test-key", srv.URL, "", testLogger())

	level, flow, err := c.GetReadingsAt(context.Background(), "station-1", targetTime)
	if err != nil {
		t.Fatalf("GetReadingsAt failed: %v", err)
	}

	// Level: closest to target is the exact match at ts=1711018800.
	if level == nil {
		t.Fatal("expected level reading, got nil")
	}
	if level.Timestamp != 1711018800 {
		t.Errorf("level timestamp: expected 1711018800, got %d", level.Timestamp)
	}
	if level.Value != 47 {
		t.Errorf("level value: expected 47, got %f", level.Value)
	}
	if level.Unit != "cm" {
		t.Errorf("level unit: expected 'cm', got %q", level.Unit)
	}

	// Flow: closest to target is ts=1711020600 (30 min) vs ts=1711015200 (1 hour).
	if flow == nil {
		t.Fatal("expected flow reading, got nil")
	}
	if flow.Timestamp != 1711020600 {
		t.Errorf("flow timestamp: expected 1711020600, got %d", flow.Timestamp)
	}
	if flow.Value != 12.3 {
		t.Errorf("flow value: expected 12.3, got %f", flow.Value)
	}
	if flow.Unit != "m3s" {
		t.Errorf("flow unit: expected 'm3s', got %q", flow.Unit)
	}
}

func TestGetReadingsAt_NoReadings(t *testing.T) {
	// Empty readings for the station.
	readingsBody, _ := json.Marshal(readingsResponse{
		Readings: map[string][]readingJSON{},
	})

	srv := newReadingsServer(t, "station-1", readingsBody)
	c := NewClient("test-key", srv.URL, "", testLogger())

	level, flow, err := c.GetReadingsAt(context.Background(), "station-1", time.Now())
	if err != nil {
		t.Fatalf("GetReadingsAt failed: %v", err)
	}
	if level != nil {
		t.Errorf("expected nil level, got %+v", level)
	}
	if flow != nil {
		t.Errorf("expected nil flow, got %+v", flow)
	}
}

func TestGetReadingsAt_OnlyLevel(t *testing.T) {
	// Station has cm readings but no m3s.
	readingsBody, _ := json.Marshal(readingsResponse{
		Readings: map[string][]readingJSON{
			"cm": {
				{Ts: 1711018800, V: 55},
			},
		},
	})

	srv := newReadingsServer(t, "station-1", readingsBody)
	c := NewClient("test-key", srv.URL, "", testLogger())

	level, flow, err := c.GetReadingsAt(context.Background(), "station-1", time.Unix(1711018800, 0))
	if err != nil {
		t.Fatalf("GetReadingsAt failed: %v", err)
	}
	if level == nil {
		t.Fatal("expected level reading, got nil")
	}
	if level.Value != 55 {
		t.Errorf("level value: expected 55, got %f", level.Value)
	}
	if flow != nil {
		t.Errorf("expected nil flow, got %+v", flow)
	}
}

// ---------------------------------------------------------------------------
// ClassifyLevel tests
// ---------------------------------------------------------------------------

func TestClassifyLevel(t *testing.T) {
	cal := &Calibration{
		StationID: "station-1",
		Unit:      "cm",
		LW:        30,
		MW:        60,
		HW:        120,
	}

	tests := []struct {
		value    float64
		expected string
	}{
		{10, "Low water"},
		{30, "Low water"},       // exactly at LW threshold
		{31, "Medium water"},
		{60, "Medium water"},    // exactly at MW threshold
		{61, "High water"},
		{120, "High water"},     // exactly at HW threshold
		{121, "Very high water"},
		{200, "Very high water"},
	}

	for _, tc := range tests {
		got := ClassifyLevel(tc.value, cal)
		if got != tc.expected {
			t.Errorf("ClassifyLevel(%v): expected %q, got %q", tc.value, tc.expected, got)
		}
	}
}

func TestClassifyLevel_NilCalibration(t *testing.T) {
	got := ClassifyLevel(50, nil)
	if got != "Unknown" {
		t.Errorf("ClassifyLevel with nil calibration: expected 'Unknown', got %q", got)
	}
}

func TestClassifyLevel_ZeroThresholds(t *testing.T) {
	cal := &Calibration{
		StationID: "station-1",
		Unit:      "cm",
		LW:        0,
		MW:        0,
		HW:        0,
	}

	got := ClassifyLevel(50, cal)
	if got != "Unknown" {
		t.Errorf("ClassifyLevel with zero thresholds: expected 'Unknown', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Haversine distance tests
// ---------------------------------------------------------------------------

func TestHaversineKm(t *testing.T) {
	// Lofer to Munich is approximately 100 km.
	d := haversineKm(47.582670, 12.702775, 48.137154, 11.576124)
	if d < 90 || d > 120 {
		t.Errorf("Lofer to Munich: expected ~100 km, got %.1f km", d)
	}

	// Same point should be 0.
	d = haversineKm(47.582670, 12.702775, 47.582670, 12.702775)
	if d != 0 {
		t.Errorf("same point: expected 0, got %f", d)
	}
}

// ---------------------------------------------------------------------------
// API key header test
// ---------------------------------------------------------------------------

func TestRefreshCache_SendsAPIKey(t *testing.T) {
	var receivedKey string
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/sections", func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("X-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"sections":[]}`)) //nolint:errcheck
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient("my-secret-key", srv.URL, "", testLogger())
	if err := c.RefreshCache(context.Background()); err != nil {
		t.Fatalf("RefreshCache failed: %v", err)
	}

	if receivedKey != "my-secret-key" {
		t.Errorf("expected X-Key 'my-secret-key', got %q", receivedKey)
	}
}

// ---------------------------------------------------------------------------
// Error handling tests
// ---------------------------------------------------------------------------

func TestRefreshCache_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/sections", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient("test-key", srv.URL, "", testLogger())
	err := c.RefreshCache(context.Background())
	if err == nil {
		t.Fatal("expected error for server 500, got nil")
	}
}

func TestGetReadingsAt_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/stations/station-1/readings", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient("test-key", srv.URL, "", testLogger())
	_, _, err := c.GetReadingsAt(context.Background(), "station-1", time.Now())
	if err == nil {
		t.Fatal("expected error for server 500, got nil")
	}
}

// ---------------------------------------------------------------------------
// Disk cache tests
// ---------------------------------------------------------------------------

func TestRefreshCache_WritesDiskCache(t *testing.T) {
	body := mockSectionsResponse(sectionJSON{
		ID:          "sec-1",
		River:       map[string]string{"de": "Saalach"},
		Grade:       "III",
		PutInLatLng: [2]float64{47582670, 12702775},
	})
	srv := newSectionsServer(t, body)
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()
	c := NewClient("test-key", srv.URL, cacheDir, testLogger())

	if err := c.RefreshCache(context.Background()); err != nil {
		t.Fatalf("RefreshCache failed: %v", err)
	}

	// Verify cache file was written.
	cachePath := c.sectionsCacheFile()
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache file not found: %v", err)
	}

	if len(c.sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(c.sections))
	}
}

func TestRefreshCache_LoadsFromDiskCache(t *testing.T) {
	body := mockSectionsResponse(sectionJSON{
		ID:          "sec-1",
		River:       map[string]string{"de": "Saalach"},
		Grade:       "III",
		PutInLatLng: [2]float64{47582670, 12702775},
	})

	// Write a cache file manually.
	cacheDir := t.TempDir()
	os.WriteFile(cacheDir+"/sections.json", body, 0600)

	// Create client pointing at a broken server — should not be called.
	c := NewClient("test-key", "http://127.0.0.1:1", cacheDir, testLogger())

	if err := c.RefreshCache(context.Background()); err != nil {
		t.Fatalf("RefreshCache from disk cache failed: %v", err)
	}

	if len(c.sections) != 1 {
		t.Fatalf("expected 1 section from disk cache, got %d", len(c.sections))
	}
	if c.sections[0].Grade != "III" {
		t.Errorf("expected grade III, got %q", c.sections[0].Grade)
	}
}

func TestRefreshCache_ExpiredDiskCacheCallsAPI(t *testing.T) {
	body := mockSectionsResponse(sectionJSON{
		ID:          "sec-1",
		River:       map[string]string{"de": "Saalach"},
		Grade:       "IV",
		PutInLatLng: [2]float64{47582670, 12702775},
	})

	// Write a cache file and backdate it beyond cacheMaxAge.
	cacheDir := t.TempDir()
	cachePath := cacheDir + "/sections.json"
	os.WriteFile(cachePath, []byte(`{"sections":[]}`), 0600)
	expired := time.Now().Add(-cacheMaxAge - time.Hour)
	os.Chtimes(cachePath, expired, expired)

	// The API server returns the real data.
	srv := newSectionsServer(t, body)
	t.Cleanup(srv.Close)

	c := NewClient("test-key", srv.URL, cacheDir, testLogger())

	if err := c.RefreshCache(context.Background()); err != nil {
		t.Fatalf("RefreshCache failed: %v", err)
	}

	// Should have fetched from API (1 section with grade IV, not 0 from expired cache).
	if len(c.sections) != 1 {
		t.Fatalf("expected 1 section from API, got %d", len(c.sections))
	}
	if c.sections[0].Grade != "IV" {
		t.Errorf("expected grade IV from API, got %q", c.sections[0].Grade)
	}
}
