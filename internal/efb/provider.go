package efb

import "context"

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
}
