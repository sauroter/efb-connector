// Package rivermap provides an HTTP client for the Rivermap API
// (https://api.rivermap.org). It fetches whitewater section data,
// matches GPS coordinates to sections by proximity, and retrieves
// gauge station readings with water level classification.
package rivermap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the Rivermap API base URL.
const DefaultBaseURL = "https://api.rivermap.org"

// maxProximityKm is the maximum distance (in km) for a GPS coordinate
// to be considered a match for a section's put-in point.
const maxProximityKm = 2.0

// cacheMaxAge is how long a disk cache file is considered fresh before
// a refresh from the API is attempted. Well within the 2/hour rate limit.
const cacheMaxAge = 24 * time.Hour

// earthRadiusKm is the mean radius of the Earth in kilometres.
const earthRadiusKm = 6371.0

// Client is an HTTP client for the Rivermap API. It caches section data
// on disk and in memory, and provides GPS proximity matching and gauge
// reading retrieval.
type Client struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string
	logger     *slog.Logger
	cacheDir   string // directory for disk cache files (empty = no disk cache)

	mu       sync.RWMutex
	sections []Section
	cachedAt time.Time
}

// Section represents a whitewater river section from the Rivermap API.
type Section struct {
	ID            string
	River         map[string]string // lang -> name
	Grade         string
	SpotGrades    []string
	PutInLatLng   [2]float64 // [lat, lng] in WGS-84 degrees
	TakeOutLatLng [2]float64
	Calibration   *Calibration
	// For display
	SectionFrom string // from sectionName.de or .en
	SectionTo   string
}

// Calibration holds gauge thresholds for a station associated with a section.
type Calibration struct {
	StationID string
	Unit      string  // "cm", "m3s", etc.
	LW        float64 // low water threshold
	MW        float64 // medium water
	HW        float64 // high water
}

// Reading holds a single gauge measurement from a station.
type Reading struct {
	Timestamp int64
	Value     float64
	Unit      string // "cm" or "m3s"
}

// Station represents a gauge station from the Rivermap API.
type Station struct {
	ID     string
	Name   string
	River  map[string]string
	LatLng [2]float64
}

// DisplayName produces a human-readable name for the section, e.g.
// "Saalach [Lofer - Scheffsnoth]". Falls back to the river name alone
// when section from/to are not available.
func (s *Section) DisplayName() string {
	river := s.River["de"]
	if river == "" {
		river = s.River["en"]
	}
	if s.SectionFrom != "" && s.SectionTo != "" {
		return fmt.Sprintf("%s [%s - %s]", river, s.SectionFrom, s.SectionTo)
	}
	return river
}

// NewClient returns a new Rivermap API client. Pass DefaultBaseURL for
// production use. cacheDir specifies where to persist section data on
// disk (e.g. "/data/rivermap_cache"); pass "" to disable disk caching.
func NewClient(apiKey, baseURL, cacheDir string, logger *slog.Logger) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	if logger == nil {
		logger = slog.Default()
	}
	if cacheDir != "" {
		os.MkdirAll(cacheDir, 0700)
	}
	return &Client{
		apiKey:   apiKey,
		baseURL:  baseURL,
		cacheDir: cacheDir,
		logger:   logger,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// sectionsResponse is the top-level JSON envelope from GET /v2/sections.
type sectionsResponse struct {
	Sections []sectionJSON `json:"sections"`
}

// sectionNameDetail holds the structured form of a section name.
type sectionNameDetail struct {
	From          string `json:"from"`
	To            string `json:"to"`
	FormattedName string `json:"formattedName"`
}

// sectionJSON mirrors a single section object in the API response.
type sectionJSON struct {
	ID            string                       `json:"id"`
	River         map[string]string            `json:"river"`
	SectionName   map[string]json.RawMessage   `json:"sectionName"` // values are either string or sectionNameDetail
	Grade         string                       `json:"grade"`
	SpotGrades    []string                     `json:"spotGrades"`
	PutInLatLng   [2]float64                   `json:"putInLatLng"`
	TakeOutLatLng [2]float64                   `json:"takeOutLatLng"`
	Calibration   *struct {
		StationID string  `json:"stationId"`
		Unit      string  `json:"unit"`
		LW        float64 `json:"lw"`
		MW        float64 `json:"mw"`
		HW        float64 `json:"hw"`
	} `json:"calibration"`
}

// RefreshCache loads section data, preferring a fresh disk cache over an
// API call. The disk cache is used if it exists and is younger than
// cacheMaxAge (24h). Otherwise the API is called and the result is written
// to disk for subsequent restarts.
func (c *Client) RefreshCache(ctx context.Context) error {
	// 1. Try loading from disk cache.
	if c.cacheDir != "" {
		if sections, err := c.loadDiskCache(); err == nil {
			c.mu.Lock()
			c.sections = sections
			c.cachedAt = time.Now()
			c.mu.Unlock()
			c.logger.Info("rivermap: loaded sections from disk cache", "count", len(sections))
			return nil
		}
	}

	// 2. Fetch from API.
	body, err := c.fetchSectionsAPI(ctx)
	if err != nil {
		return err
	}

	sections, err := parseSections(body)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.sections = sections
	c.cachedAt = time.Now()
	c.mu.Unlock()

	// 3. Write to disk cache for next restart.
	if c.cacheDir != "" {
		if err := c.writeDiskCache(body); err != nil {
			c.logger.Warn("rivermap: failed to write disk cache", "error", err)
		}
	}

	c.logger.Info("rivermap: refreshed section cache from API", "count", len(sections))
	return nil
}

// sectionsCacheFile returns the path to the disk cache file.
func (c *Client) sectionsCacheFile() string {
	return filepath.Join(c.cacheDir, "sections.json")
}

// loadDiskCache reads and parses the cached sections file if it exists
// and is younger than cacheMaxAge.
func (c *Client) loadDiskCache() ([]Section, error) {
	path := c.sectionsCacheFile()
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if time.Since(info.ModTime()) > cacheMaxAge {
		return nil, fmt.Errorf("cache expired")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseSections(data)
}

// writeDiskCache writes the raw API response to disk for later use.
func (c *Client) writeDiskCache(data []byte) error {
	path := c.sectionsCacheFile()
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// fetchSectionsAPI makes the HTTP request to the Rivermap sections endpoint.
func (c *Client) fetchSectionsAPI(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v2/sections", nil)
	if err != nil {
		return nil, fmt.Errorf("rivermap: failed to build sections request: %w", err)
	}
	req.Header.Set("X-Key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rivermap: sections request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rivermap: failed to read sections response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rivermap: sections returned status %d", resp.StatusCode)
	}
	return body, nil
}

// parseSections parses the raw JSON from the sections API into Section structs.
func parseSections(body []byte) ([]Section, error) {
	var raw sectionsResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("rivermap: failed to parse sections JSON: %w", err)
	}

	sections := make([]Section, 0, len(raw.Sections))
	for _, s := range raw.Sections {
		sec := Section{
			ID:         s.ID,
			River:      s.River,
			Grade:      s.Grade,
			SpotGrades: s.SpotGrades,
			PutInLatLng: [2]float64{
				s.PutInLatLng[0] / 1e6,
				s.PutInLatLng[1] / 1e6,
			},
			TakeOutLatLng: [2]float64{
				s.TakeOutLatLng[0] / 1e6,
				s.TakeOutLatLng[1] / 1e6,
			},
		}

		// Extract display names from sectionName, preferring "de" then "en".
		// Values can be either a plain string or a structured object with from/to.
		for _, lang := range []string{"de", "en"} {
			raw, ok := s.SectionName[lang]
			if !ok {
				continue
			}
			// Try structured form first.
			var detail sectionNameDetail
			if err := json.Unmarshal(raw, &detail); err == nil && detail.From != "" {
				sec.SectionFrom = detail.From
				sec.SectionTo = detail.To
				break
			}
			// Fall back to plain string (used as the full name).
			var plain string
			if err := json.Unmarshal(raw, &plain); err == nil && plain != "" {
				sec.SectionFrom = plain
				break
			}
		}

		if s.Calibration != nil {
			sec.Calibration = &Calibration{
				StationID: s.Calibration.StationID,
				Unit:      s.Calibration.Unit,
				LW:        s.Calibration.LW,
				MW:        s.Calibration.MW,
				HW:        s.Calibration.HW,
			}
		}

		sections = append(sections, sec)
	}
	return sections, nil
}

// FindSection returns the cached section whose put-in point is closest
// to the given GPS coordinate, or nil if no section is within 2 km.
func (c *Client) FindSection(lat, lng float64) *Section {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var best *Section
	bestDist := math.MaxFloat64

	for i := range c.sections {
		d := haversineKm(lat, lng, c.sections[i].PutInLatLng[0], c.sections[i].PutInLatLng[1])
		if d < bestDist {
			bestDist = d
			best = &c.sections[i]
		}
	}

	if best == nil || bestDist > maxProximityKm {
		return nil
	}
	return best
}

// readingsResponse is the top-level JSON envelope from GET /v2/stations/{id}/readings.
// The single-station endpoint returns readings directly keyed by unit (e.g. "cm", "m3s"),
// not nested under the station ID.
type readingsResponse struct {
	Readings map[string][]readingJSON `json:"readings"`
}

// readingJSON mirrors a single reading object in the API response.
type readingJSON struct {
	Ts int64   `json:"ts"`
	V  float64 `json:"v"`
}

// GetReadingsAt fetches gauge readings for a station around a given time.
// It queries a 6-hour window (at -/+ 3 hours) and returns the readings
// closest to `at` for water level (cm) and flow (m3s). Either or both
// returned readings may be nil if no data is available.
func (c *Client) GetReadingsAt(ctx context.Context, stationID string, at time.Time) (level *Reading, flow *Reading, err error) {
	from := at.Add(-3 * time.Hour)
	to := at.Add(3 * time.Hour)

	fromStr := from.UTC().Format("200601021504")
	toStr := to.UTC().Format("200601021504")

	url := fmt.Sprintf("%s/v2/stations/%s/readings?from=%s&to=%s", c.baseURL, stationID, fromStr, toStr)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("rivermap: failed to build readings request: %w", err)
	}
	req.Header.Set("X-Key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("rivermap: readings request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("rivermap: failed to read readings response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("rivermap: readings returned status %d", resp.StatusCode)
	}

	var raw readingsResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, fmt.Errorf("rivermap: failed to parse readings JSON: %w", err)
	}

	if len(raw.Readings) == 0 {
		return nil, nil, nil
	}

	atUnix := at.Unix()

	level = findClosestReading(raw.Readings["cm"], atUnix, "cm")
	flow = findClosestReading(raw.Readings["m3s"], atUnix, "m3s")

	return level, flow, nil
}

// findClosestReading returns the reading whose timestamp is closest to
// targetUnix, or nil if readings is empty.
func findClosestReading(readings []readingJSON, targetUnix int64, unit string) *Reading {
	if len(readings) == 0 {
		return nil
	}

	var best readingJSON
	bestDelta := int64(math.MaxInt64)

	for _, r := range readings {
		delta := r.Ts - targetUnix
		if delta < 0 {
			delta = -delta
		}
		if delta < bestDelta {
			bestDelta = delta
			best = r
		}
	}

	return &Reading{
		Timestamp: best.Ts,
		Value:     best.V,
		Unit:      unit,
	}
}

// ClassifyLevel categorises a gauge reading value according to the
// calibration thresholds for the associated section.
func ClassifyLevel(value float64, cal *Calibration) string {
	if cal == nil {
		return "Unknown"
	}
	// Handle zero thresholds: if all thresholds are zero, we cannot
	// classify meaningfully.
	if cal.LW == 0 && cal.MW == 0 && cal.HW == 0 {
		return "Unknown"
	}
	switch {
	case value <= cal.LW:
		return "Low water"
	case value <= cal.MW:
		return "Medium water"
	case value <= cal.HW:
		return "High water"
	default:
		return "Very high water"
	}
}

// haversineKm returns the great-circle distance in kilometres between
// two points specified in decimal degrees.
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	dLat := degreesToRadians(lat2 - lat1)
	dLng := degreesToRadians(lng2 - lng1)

	lat1Rad := degreesToRadians(lat1)
	lat2Rad := degreesToRadians(lat2)

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return earthRadiusKm * c
}

// degreesToRadians converts decimal degrees to radians.
func degreesToRadians(deg float64) float64 {
	return deg * math.Pi / 180.0
}
