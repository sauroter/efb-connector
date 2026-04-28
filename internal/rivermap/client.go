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
	stations map[string]string // station ID -> human-readable name
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
	if s.SectionFrom != "" {
		return fmt.Sprintf("%s — %s", river, s.SectionFrom)
	}
	return river
}

// StationName returns the human-readable name for a gauge station ID.
// If the station is not in the cache, the raw ID is returned as a fallback.
func (c *Client) StationName(id string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if name, ok := c.stations[id]; ok {
		return name
	}
	return id
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
		if err := os.MkdirAll(cacheDir, 0700); err != nil {
			logger.Warn("rivermap: cache dir create failed; continuing without disk cache", "dir", cacheDir, "error", err)
			cacheDir = ""
		}
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

// stationsResponse is the top-level JSON envelope from GET /v2/stations.
type stationsResponse struct {
	Stations []stationJSON `json:"stations"`
}

// stationJSON mirrors a single station object in the API response.
type stationJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// sectionNameDetail holds the structured form of a section name.
type sectionNameDetail struct {
	From          string `json:"from"`
	To            string `json:"to"`
	FormattedName string `json:"formattedName"`
}

// sectionJSON mirrors a single section object in the API response.
type sectionJSON struct {
	ID            string                     `json:"id"`
	River         map[string]string          `json:"river"`
	SectionName   map[string]json.RawMessage `json:"sectionName"` // values are either string or sectionNameDetail
	Grade         string                     `json:"grade"`
	SpotGrades    []string                   `json:"spotGrades"`
	PutInLatLng   [2]float64                 `json:"putInLatLng"`
	TakeOutLatLng [2]float64                 `json:"takeOutLatLng"`
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
	// --- Sections ---
	// 1. Try loading sections from disk cache.
	var sections []Section
	if c.cacheDir != "" {
		if data, err := c.loadDiskCacheRaw("sections.json"); err == nil {
			if s, err := parseSections(data); err == nil {
				sections = s
				c.logger.Info("rivermap: loaded sections from disk cache", "count", len(sections))
			}
		}
	}

	// 2. Fetch sections from API if disk cache miss.
	if sections == nil {
		body, err := c.fetchSectionsAPI(ctx)
		if err != nil {
			return err
		}
		s, err := parseSections(body)
		if err != nil {
			return err
		}
		sections = s
		if c.cacheDir != "" {
			if err := c.writeDiskCache("sections.json", body); err != nil {
				c.logger.Warn("rivermap: failed to write sections disk cache", "error", err)
			}
		}
		c.logger.Info("rivermap: refreshed section cache from API", "count", len(sections))
	}

	// --- Stations ---
	stations := c.loadStations(ctx)

	c.mu.Lock()
	c.sections = sections
	c.stations = stations
	c.cachedAt = time.Now()
	c.mu.Unlock()

	return nil
}

// cacheFilePath returns the path to a named disk cache file.
func (c *Client) cacheFilePath(filename string) string {
	return filepath.Join(c.cacheDir, filename)
}

// loadDiskCacheRaw reads a named cache file if it exists and is younger
// than cacheMaxAge. Returns the raw bytes.
func (c *Client) loadDiskCacheRaw(filename string) ([]byte, error) {
	path := c.cacheFilePath(filename)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if time.Since(info.ModTime()) > cacheMaxAge {
		return nil, fmt.Errorf("cache expired")
	}
	return os.ReadFile(path)
}

// writeDiskCache writes raw data to a named cache file atomically.
func (c *Client) writeDiskCache(filename string, data []byte) error {
	path := c.cacheFilePath(filename)
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

// loadStations loads station names from disk cache or API. Returns a map of
// station ID to name. Failures are logged but non-fatal (returns empty map).
func (c *Client) loadStations(ctx context.Context) map[string]string {
	// Try disk cache first.
	if c.cacheDir != "" {
		if data, err := c.loadDiskCacheRaw("stations.json"); err == nil {
			if m, err := parseStations(data); err == nil {
				c.logger.Info("rivermap: loaded stations from disk cache", "count", len(m))
				return m
			}
		}
	}

	// Fetch from API.
	body, err := c.fetchStationsAPI(ctx)
	if err != nil {
		c.logger.Warn("rivermap: failed to fetch stations", "error", err)
		return make(map[string]string)
	}

	m, err := parseStations(body)
	if err != nil {
		c.logger.Warn("rivermap: failed to parse stations", "error", err)
		return make(map[string]string)
	}

	if c.cacheDir != "" {
		if err := c.writeDiskCache("stations.json", body); err != nil {
			c.logger.Warn("rivermap: failed to write stations disk cache", "error", err)
		}
	}

	c.logger.Info("rivermap: refreshed station cache from API", "count", len(m))
	return m
}

// fetchStationsAPI makes the HTTP request to the Rivermap stations endpoint.
func (c *Client) fetchStationsAPI(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v2/stations", nil)
	if err != nil {
		return nil, fmt.Errorf("rivermap: failed to build stations request: %w", err)
	}
	req.Header.Set("X-Key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rivermap: stations request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rivermap: failed to read stations response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rivermap: stations returned status %d", resp.StatusCode)
	}
	return body, nil
}

// parseStations parses the raw JSON from the stations API into a map of ID -> name.
func parseStations(body []byte) (map[string]string, error) {
	var raw stationsResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("rivermap: failed to parse stations JSON: %w", err)
	}
	m := make(map[string]string, len(raw.Stations))
	for _, s := range raw.Stations {
		if s.ID != "" && s.Name != "" {
			m[s.ID] = s.Name
		}
	}
	return m, nil
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

// findNearestByTakeOut returns the cached section whose take-out point is
// closest to the given GPS coordinate, or nil if no section is within maxProximityKm.
func (c *Client) findNearestByTakeOut(lat, lng float64) *Section {
	var best *Section
	bestDist := math.MaxFloat64

	for i := range c.sections {
		d := haversineKm(lat, lng, c.sections[i].TakeOutLatLng[0], c.sections[i].TakeOutLatLng[1])
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

// sectionRiverName returns the river name for a section, preferring "de" then "en".
func sectionRiverName(s *Section) string {
	if name := s.River["de"]; name != "" {
		return name
	}
	return s.River["en"]
}

// FindSections returns all sections the track passes through, ordered
// downstream. It finds the start section (nearest put-in to track start)
// and end section (nearest take-out to track end), then returns all
// sections on the same river between them.
func (c *Client) FindSections(startLat, startLng, endLat, endLng float64) []Section {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 1. Find start section: closest put-in to (startLat, startLng).
	startSection := c.findNearestPutIn(startLat, startLng)

	// 2. Find end section: closest take-out to (endLat, endLng).
	endSection := c.findNearestByTakeOut(endLat, endLng)

	// 3. If neither found: return nil.
	if startSection == nil && endSection == nil {
		return nil
	}

	// 4. If only start found (no end match): return just [startSection].
	if startSection == nil {
		return nil
	}
	if endSection == nil {
		return []Section{*startSection}
	}

	// 5. If same section: return just one.
	if startSection.ID == endSection.ID {
		return []Section{*startSection}
	}

	// 6. If both found and same river: collect all sections on that river,
	//    sort by put-in latitude, and return the contiguous slice.
	startRiver := sectionRiverName(startSection)
	endRiver := sectionRiverName(endSection)
	if startRiver == "" || endRiver == "" || startRiver != endRiver {
		return []Section{*startSection}
	}

	// Collect all sections on this river.
	var riverSections []Section
	for i := range c.sections {
		if sectionRiverName(&c.sections[i]) == startRiver {
			riverSections = append(riverSections, c.sections[i])
		}
	}

	// Sort by put-in latitude descending (higher lat = further upstream in
	// typical European rivers flowing roughly north/south or west).
	// Use haversine distance from start section's put-in as the sort key
	// for a more robust ordering.
	sortByDistFromStart(riverSections, startSection.PutInLatLng[0], startSection.PutInLatLng[1])

	// Find indices of start and end sections in sorted list.
	startIdx := -1
	endIdx := -1
	for i, s := range riverSections {
		if s.ID == startSection.ID {
			startIdx = i
		}
		if s.ID == endSection.ID {
			endIdx = i
		}
	}

	if startIdx == -1 || endIdx == -1 {
		return []Section{*startSection}
	}

	// Ensure correct order (start first).
	if startIdx > endIdx {
		startIdx, endIdx = endIdx, startIdx
	}

	return riverSections[startIdx : endIdx+1]
}

// findNearestPutIn is an internal version of FindSection that does NOT
// acquire the read lock (caller must hold it).
func (c *Client) findNearestPutIn(lat, lng float64) *Section {
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

// sortByDistFromStart sorts sections by haversine distance from a reference
// point (the start section's put-in).
func sortByDistFromStart(sections []Section, refLat, refLng float64) {
	for i := 0; i < len(sections); i++ {
		for j := i + 1; j < len(sections); j++ {
			di := haversineKm(refLat, refLng, sections[i].PutInLatLng[0], sections[i].PutInLatLng[1])
			dj := haversineKm(refLat, refLng, sections[j].PutInLatLng[0], sections[j].PutInLatLng[1])
			if di > dj {
				sections[i], sections[j] = sections[j], sections[i]
			}
		}
	}
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
// calibration thresholds: below LW is too low to paddle, LW–MW is low
// water, MW–HW is medium water, above HW is high water.
func ClassifyLevel(value float64, cal *Calibration) string {
	if cal == nil {
		return "Unknown"
	}
	if cal.LW == 0 && cal.MW == 0 && cal.HW == 0 {
		return "Unknown"
	}
	switch {
	case value < cal.LW:
		return "Too low"
	case value <= cal.MW:
		return "Low water"
	case value <= cal.HW:
		return "Medium water"
	default:
		return "High water"
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
