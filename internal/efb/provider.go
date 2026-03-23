package efb

import (
	"context"
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
	CreateTripFromTrack(ctx context.Context, trackID string, startTime time.Time, durationSecs float64) error
}
