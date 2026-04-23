// Package garmin provides an abstraction over the Garmin Connect data source,
// allowing the sync engine to retrieve water-sport activities and their GPX
// tracks without depending on a specific implementation (unofficial Python
// library today, official OAuth API in the future).
package garmin

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors returned by GarminProvider implementations.
var (
	// ErrGarminAuth is returned when authentication fails due to wrong
	// credentials.
	ErrGarminAuth = errors.New("garmin: authentication failed")

	// ErrGarminMFARequired is returned when Garmin requires an MFA
	// challenge that cannot be satisfied automatically.
	ErrGarminMFARequired = errors.New("garmin: MFA verification required")

	// ErrGarminUnavailable is returned when Garmin is temporarily blocking
	// connections (rate limiting, CAPTCHA, Cloudflare challenges).  The user
	// cannot do anything about this — it resolves on its own.
	ErrGarminUnavailable = errors.New("garmin: temporarily unavailable")
)

// GarminCredentials holds the authentication material and optional token
// cache location for a single Garmin Connect account.
type GarminCredentials struct {
	// Email is the Garmin Connect account email address.
	Email string

	// Password is the Garmin Connect account password.
	Password string

	// TokenStorePath is the directory where cached authentication tokens are
	// persisted between runs, e.g. /data/garmin_tokens/42/.  An empty string
	// means no caching.
	TokenStorePath string
}

// Activity represents a single water-sport activity retrieved from Garmin
// Connect.
type Activity struct {
	// ProviderID is the opaque activity identifier assigned by Garmin
	// (typically the numeric activity ID, represented as a string for
	// provider-independence).
	ProviderID string

	// Name is the human-readable name of the activity.
	Name string

	// Type is the Garmin activity type key (e.g. kayaking_v2, paddling_v2,
	// rowing_v2).  Filtering is done by parentTypeId in the Python script,
	// so any water_sports subtype (parentTypeId 228) may appear here.
	Type string

	// Date is the local start date of the activity (date only, no time component).
	Date time.Time

	// StartTime is the full local start time of the activity (date + time).
	StartTime time.Time

	// StartLat is the latitude of the activity starting point (WGS-84 degrees).
	StartLat float64
	// StartLng is the longitude of the activity starting point (WGS-84 degrees).
	StartLng float64

	// EndLat is the latitude of the activity ending point (WGS-84 degrees).
	EndLat float64
	// EndLng is the longitude of the activity ending point (WGS-84 degrees).
	EndLng float64

	// DurationSecs is the total elapsed duration of the activity in seconds.
	DurationSecs float64

	// DistanceM is the total distance covered during the activity in metres.
	DistanceM float64
}

// GarminProvider is the interface that wraps Garmin Connect access.
// Implementations must be safe for concurrent use by multiple goroutines.
type GarminProvider interface {
	// ListActivities returns all water-sport activities for the given account
	// that fall within the half-open interval [start, end).
	ListActivities(ctx context.Context, creds GarminCredentials, start, end time.Time) ([]Activity, error)

	// DownloadGPX returns the raw GPX bytes for the activity identified by
	// activityID (the value of Activity.ProviderID).
	DownloadGPX(ctx context.Context, creds GarminCredentials, activityID string) ([]byte, error)

	// ValidateCredentials verifies that the supplied credentials are accepted
	// by Garmin Connect.  It returns ErrGarminAuth when the credentials are
	// wrong and ErrGarminMFARequired when an interactive challenge is needed.
	ValidateCredentials(ctx context.Context, creds GarminCredentials) error

	// ValidateWithMFA starts an interactive credential validation that
	// supports MFA.  It returns "ok" when credentials are accepted without
	// MFA, or "needs_mfa" when the user must supply an MFA code via
	// CompleteMFA.
	ValidateWithMFA(ctx context.Context, userID int64, creds GarminCredentials) (string, error)

	// CompleteMFA sends the MFA code to complete a previously started
	// interactive validation.  Returns ErrGarminAuth on invalid code.
	CompleteMFA(userID int64, code string) error

	// HasMFASession reports whether an active MFA session exists for the
	// given user.
	HasMFASession(userID int64) bool
}
