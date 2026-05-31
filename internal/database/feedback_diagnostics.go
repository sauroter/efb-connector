package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// FeedbackDiagnostics summarises a user's current sync/credential state at
// the moment they submit feedback. Attached to the operator-notification
// email so triage doesn't need a separate DB lookup. All boolean fields
// reflect "what the user themselves would see on the dashboard right now".
type FeedbackDiagnostics struct {
	// HasGarminCredentials and HasEFBCredentials reflect whether a row
	// exists in the respective credentials table. The *Invalid flags only
	// make sense when these are true.
	HasGarminCredentials bool
	GarminInvalid        bool
	GarminLastError      string

	HasEFBCredentials  bool
	EFBInvalid         bool
	EFBLastError       string
	EFBConsentRequired bool

	// LastSync is set when the user has at least one sync_run, regardless
	// of its outcome. Embedded values follow the sync_runs schema.
	LastSync *LastSyncSummary
}

// LastSyncSummary is the subset of a sync_run row included in feedback
// diagnostics.
type LastSyncSummary struct {
	ID                int64
	Trigger           string
	StartedAt         time.Time
	Status            string
	ActivitiesFound   int
	ActivitiesSynced  int
	ActivitiesSkipped int
	ActivitiesFailed  int
	ErrorMessage      string
}

// GetFeedbackDiagnostics collects the diagnostic snapshot for userID.
// Missing rows in any of the joined tables are reported via the
// Has*Credentials and LastSync==nil signals, never as errors.
func (d *DB) GetFeedbackDiagnostics(userID int64) (*FeedbackDiagnostics, error) {
	var out FeedbackDiagnostics

	var gIsValid sql.NullInt64
	var gLastErr sql.NullString
	err := d.db.QueryRow(
		`SELECT is_valid, COALESCE(last_error, '') FROM garmin_credentials WHERE user_id = ?`,
		userID,
	).Scan(&gIsValid, &gLastErr)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// no row → leave HasGarminCredentials false
	case err != nil:
		return nil, fmt.Errorf("database: feedback diagnostics garmin: %w", err)
	default:
		out.HasGarminCredentials = true
		out.GarminInvalid = !gIsValid.Valid || gIsValid.Int64 != 1
		out.GarminLastError = gLastErr.String
	}

	var eIsValid sql.NullInt64
	var eLastErr sql.NullString
	var eConsent sql.NullInt64
	err = d.db.QueryRow(
		`SELECT is_valid, COALESCE(last_error, ''), COALESCE(consent_required, 0)
		   FROM efb_credentials WHERE user_id = ?`,
		userID,
	).Scan(&eIsValid, &eLastErr, &eConsent)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// no row
	case err != nil:
		return nil, fmt.Errorf("database: feedback diagnostics efb: %w", err)
	default:
		out.HasEFBCredentials = true
		out.EFBInvalid = !eIsValid.Valid || eIsValid.Int64 != 1
		out.EFBLastError = eLastErr.String
		out.EFBConsentRequired = eConsent.Int64 == 1
	}

	runs, err := d.GetSyncHistory(userID, 1)
	if err != nil {
		return nil, fmt.Errorf("database: feedback diagnostics sync: %w", err)
	}
	if len(runs) > 0 {
		r := runs[0]
		out.LastSync = &LastSyncSummary{
			ID:                r.ID,
			Trigger:           r.Trigger,
			StartedAt:         r.StartedAt,
			Status:            r.Status,
			ActivitiesFound:   r.ActivitiesFound,
			ActivitiesSynced:  r.ActivitiesSynced,
			ActivitiesSkipped: r.ActivitiesSkipped,
			ActivitiesFailed:  r.ActivitiesFailed,
			ErrorMessage:      r.ErrorMessage,
		}
	}

	return &out, nil
}
