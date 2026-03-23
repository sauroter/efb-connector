package efb

import (
	"context"
	"strings"
	"time"
)

// EFBProvider is the interface that wraps access to the Kanu-EFB portal.
// Implementations must be safe for concurrent use by multiple goroutines.
type EFBProvider interface {
	// Login authenticates with the EFB portal. On success the session is
	// retained for subsequent Upload calls.
	Login(ctx context.Context, username, password string) error

	// Upload sends GPX data to the EFB portal.
	Upload(ctx context.Context, gpxData []byte, filename string) error

	// ValidateCredentials verifies that the supplied credentials are accepted.
	ValidateCredentials(ctx context.Context, username, password string) error

	// FindUnassociatedTrack searches the tracks page for a track matching
	// gpxFilename that does not yet have a trip. Returns the EFB track ID
	// or empty string if not found / already associated.
	FindUnassociatedTrack(ctx context.Context, gpxFilename string) (string, error)

	// CreateTripFromTrack navigates to the trip creation form for the given
	// EFB track ID, fills in start/end times, and submits the form.
	// If enrichment is non-nil, the enrichment text is appended to the
	// trip comment.
	CreateTripFromTrack(ctx context.Context, trackID string, startTime time.Time, durationSecs float64, enrichment *TripEnrichment) error
}

// TripEnrichment contains river condition data to append to the trip comment.
type TripEnrichment struct {
	SectionName  string   // e.g., "Saalach [Lofer - Scheffsnoth]"
	Grade        string   // e.g., "III-IV"
	SpotGrades   []string // e.g., ["V", "VI"]
	GaugeName    string   // e.g., "Lofer"
	GaugeReading string   // e.g., "47 cm"
	GaugeFlow    string   // e.g., "12.3 m³/s"
	WaterLevel   string   // e.g., "Medium water"
}

// FormatComment produces the enrichment text block to append to a trip comment.
// Only lines with data are included.
func (e *TripEnrichment) FormatComment() string {
	if e == nil {
		return ""
	}

	var lines []string

	// Rivermap line: "Rivermap: SectionName (Grade)"
	if e.SectionName != "" {
		rm := "Rivermap: " + e.SectionName
		if e.Grade != "" {
			rm += " (" + e.Grade + ")"
		}
		lines = append(lines, rm)
	}

	// Gauge line: "Gauge: GaugeName (Reading / Flow) — WaterLevel"
	if e.GaugeName != "" || e.GaugeReading != "" || e.GaugeFlow != "" || e.WaterLevel != "" {
		var gauge string
		if e.GaugeName != "" {
			gauge = "Gauge: " + e.GaugeName
		} else {
			gauge = "Gauge:"
		}
		var parts []string
		if e.GaugeReading != "" {
			parts = append(parts, e.GaugeReading)
		}
		if e.GaugeFlow != "" {
			parts = append(parts, e.GaugeFlow)
		}
		if len(parts) > 0 {
			gauge += " (" + strings.Join(parts, " / ") + ")"
		}
		if e.WaterLevel != "" {
			gauge += " \u2014 " + e.WaterLevel
		}
		lines = append(lines, gauge)
	}

	// Spot grades line.
	if len(e.SpotGrades) > 0 {
		lines = append(lines, "Spot grades: "+strings.Join(e.SpotGrades, ", "))
	}

	if len(lines) == 0 {
		return ""
	}

	// Attribution line is always included when there is enrichment data.
	lines = append(lines, "Data: rivermap.org (CC BY-SA 4.0)")

	return "---\n" + strings.Join(lines, "\n")
}
