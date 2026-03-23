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

// SectionEnrichment contains river condition data for a single river section.
type SectionEnrichment struct {
	SectionName  string   // e.g., "Saalach — Slalom Lofer"
	Grade        string   // e.g., "III-IV"
	SpotGrades   []string // e.g., ["V", "VI"]
	GaugeName    string   // e.g., "Unterjettenberg"
	GaugeReading string   // e.g., "16 cm"
	GaugeFlow    string   // e.g., "14.6 m³/s"
	WaterLevel   string   // e.g., "Low water"
}

// TripEnrichment contains river condition data to append to the trip comment.
type TripEnrichment struct {
	Sections []SectionEnrichment
}

// FormatComment produces the enrichment text block to append to a trip comment.
// Each section gets its own indented line. Only shown if Sections is non-empty.
func (e *TripEnrichment) FormatComment() string {
	if e == nil || len(e.Sections) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "Rivermap:")

	for _, se := range e.Sections {
		line := "  " + se.SectionName
		if se.Grade != "" {
			line += " (" + se.Grade + ")"
		}
		// Append gauge info if available.
		if se.GaugeName != "" {
			line += " | " + se.GaugeName + ":"
			if se.GaugeFlow != "" {
				line += " " + se.GaugeFlow
			}
			if se.WaterLevel != "" {
				line += " \u2014 " + se.WaterLevel
			}
		}
		lines = append(lines, line)
	}

	lines = append(lines, "Data: rivermap.org (CC BY-SA 4.0)")

	return strings.Join(lines, "\n") + "\n---"
}
