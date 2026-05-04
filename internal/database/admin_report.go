package database

import (
	"database/sql"
	"fmt"
	"time"
)

// FunnelCounts captures user counts at each step of the
// signup→syncing onboarding funnel for the admin report page.
//
// Each later step is a strict subset of the previous: a user counted in
// FirstSyncCompleted is also counted in EFBConnected, GarminConnected, and
// SignedUp.
type FunnelCounts struct {
	SignedUp           int `json:"signed_up"`
	GarminConnected    int `json:"garmin_connected"`
	EFBConnected       int `json:"efb_connected"`
	FirstSyncCompleted int `json:"first_sync_completed"`
	SyncedInLast7Days  int `json:"synced_in_last_7d"`
}

// StuckUser represents a fully connected user whose syncs are not progressing
// (no completed sync in the last 7 days, or no successful sync ever).
type StuckUser struct {
	UserID             int64      `json:"user_id"`
	Email              string     `json:"email"`
	SignupAt           time.Time  `json:"signup_at"`
	LastSuccessfulSync *time.Time `json:"last_successful_sync"`
	LastAttemptAt      *time.Time `json:"last_attempt_at"`
	LastAttemptStatus  string     `json:"last_attempt_status"`
	LastAttemptTrigger string     `json:"last_attempt_trigger"`
	LastErrorMessage   string     `json:"last_error_message"`
	ConsentRequired    bool       `json:"consent_required"`
}

// UserActivity is one row in the per-user activity overview table.
type UserActivity struct {
	UserID             int64      `json:"user_id"`
	Email              string     `json:"email"`
	IsActive           bool       `json:"is_active"`
	SyncEnabled        bool       `json:"sync_enabled"`
	GarminValid        bool       `json:"garmin_valid"`
	EFBValid           bool       `json:"efb_valid"`
	ConsentRequired    bool       `json:"consent_required"`
	LastSuccessfulSync *time.Time `json:"last_successful_sync"`
	LastAttemptAt      *time.Time `json:"last_attempt_at"`
	LastAttemptStatus  string     `json:"last_attempt_status"`
	Successful7Days    int        `json:"successful_7d"`
	Status             string     `json:"status"` // synced | failing | consent_required | disconnected | never_synced
}

// RecentFailures bundles the two failure feeds shown on the report page.
type RecentFailures struct {
	SyncRuns   []SyncRunWithEmail     `json:"sync_runs"`
	Activities []FailedActivityDetail `json:"activities"`
}

// SyncRunWithEmail is a SyncRun joined with the user's email so the report can
// render a row without a separate user lookup.
type SyncRunWithEmail struct {
	SyncRun
	Email string `json:"email"`
}

// CountUsersSyncedSince returns the number of distinct users that have a
// completed sync_run with finished_at within the given SQLite relative
// modifier (e.g. "-7 days", "-24 hours").
//
// The window argument is a SQLite datetime modifier and is NOT user-supplied;
// it must come from a static caller such as the metrics layer.
func (d *DB) CountUsersSyncedSince(window string) (int, error) {
	var n int
	err := d.db.QueryRow(`
		SELECT COUNT(DISTINCT user_id)
		  FROM sync_runs
		 WHERE status = 'completed'
		   AND finished_at IS NOT NULL
		   AND finished_at > datetime('now', ?)
	`, window).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("database: count users synced since %q: %w", window, err)
	}
	return n, nil
}

// GetFunnelCounts returns user counts at each onboarding step.
func (d *DB) GetFunnelCounts() (*FunnelCounts, error) {
	var fc FunnelCounts
	err := d.db.QueryRow(`
		SELECT
		    COUNT(*) FILTER (WHERE u.is_active = 1)                                         AS signed_up,
		    COUNT(*) FILTER (WHERE u.is_active = 1 AND gc.user_id IS NOT NULL)              AS garmin_connected,
		    COUNT(*) FILTER (WHERE u.is_active = 1 AND gc.user_id IS NOT NULL AND ec.user_id IS NOT NULL) AS efb_connected,
		    COUNT(*) FILTER (WHERE u.is_active = 1 AND first.user_id IS NOT NULL)           AS first_sync_completed,
		    COUNT(*) FILTER (WHERE u.is_active = 1 AND last7.user_id IS NOT NULL)           AS synced_in_last_7d
		  FROM users u
		  LEFT JOIN garmin_credentials gc ON gc.user_id = u.id
		  LEFT JOIN efb_credentials    ec ON ec.user_id = u.id
		  LEFT JOIN (
		      SELECT DISTINCT user_id FROM sync_runs WHERE status = 'completed'
		  ) first ON first.user_id = u.id
		  LEFT JOIN (
		      SELECT DISTINCT user_id FROM sync_runs
		       WHERE status = 'completed' AND finished_at > datetime('now','-7 days')
		  ) last7 ON last7.user_id = u.id
	`).Scan(
		&fc.SignedUp,
		&fc.GarminConnected,
		&fc.EFBConnected,
		&fc.FirstSyncCompleted,
		&fc.SyncedInLast7Days,
	)
	if err != nil {
		return nil, fmt.Errorf("database: get funnel counts: %w", err)
	}
	return &fc, nil
}

// GetStuckUsers returns active users with valid Garmin + EFB credentials whose
// most recent sync_run finished more than 7 days ago, or who have never had a
// completed sync. Useful for spotting silent failures.
func (d *DB) GetStuckUsers() ([]StuckUser, error) {
	rows, err := d.db.Query(`
		SELECT
		    u.id, u.email, u.created_at,
		    last_ok.finished_at      AS last_successful_sync,
		    last_any.started_at      AS last_attempt_at,
		    COALESCE(last_any.status, '')        AS last_attempt_status,
		    COALESCE(last_any.trigger, '')       AS last_attempt_trigger,
		    COALESCE(last_any.error_message, '') AS last_error_message,
		    COALESCE(ec.consent_required, 0)     AS consent_required
		  FROM users u
		  JOIN garmin_credentials gc ON gc.user_id = u.id AND gc.is_valid = 1
		  JOIN efb_credentials    ec ON ec.user_id = u.id AND ec.is_valid = 1
		  LEFT JOIN (
		      SELECT user_id, MAX(finished_at) AS finished_at
		        FROM sync_runs
		       WHERE status = 'completed' AND finished_at IS NOT NULL
		       GROUP BY user_id
		  ) last_ok ON last_ok.user_id = u.id
		  LEFT JOIN (
		      SELECT user_id, started_at, status, trigger, error_message,
		             ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY started_at DESC) AS rn
		        FROM sync_runs
		  ) last_any ON last_any.user_id = u.id AND last_any.rn = 1
		 WHERE u.is_active = 1 AND u.sync_enabled = 1
		   AND (last_ok.finished_at IS NULL OR last_ok.finished_at < datetime('now','-7 days'))
		 ORDER BY (last_ok.finished_at IS NULL) DESC, last_ok.finished_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("database: get stuck users: %w", err)
	}
	defer rows.Close()

	var out []StuckUser
	for rows.Next() {
		var s StuckUser
		var signupAt string
		var lastOK, lastAttempt sql.NullString
		var consentRequired int
		err := rows.Scan(
			&s.UserID, &s.Email, &signupAt,
			&lastOK, &lastAttempt,
			&s.LastAttemptStatus, &s.LastAttemptTrigger, &s.LastErrorMessage,
			&consentRequired,
		)
		if err != nil {
			return nil, fmt.Errorf("database: scan stuck user: %w", err)
		}
		s.SignupAt, _ = parseTime(signupAt)
		if lastOK.Valid {
			t, _ := parseTime(lastOK.String)
			s.LastSuccessfulSync = &t
		}
		if lastAttempt.Valid {
			t, _ := parseTime(lastAttempt.String)
			s.LastAttemptAt = &t
		}
		s.ConsentRequired = consentRequired != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetUserActivityOverview returns a sortable per-user table summarising
// last-sync status, credential validity, and recent successful-sync count.
// One row per user (active and inactive), ordered by last successful sync
// descending (NULLs last), then signup date.
func (d *DB) GetUserActivityOverview() ([]UserActivity, error) {
	rows, err := d.db.Query(`
		SELECT
		    u.id, u.email, u.is_active, u.sync_enabled,
		    COALESCE(gc.is_valid, 0)                AS garmin_valid,
		    COALESCE(ec.is_valid, 0)                AS efb_valid,
		    COALESCE(ec.consent_required, 0)        AS consent_required,
		    last_ok.finished_at                     AS last_successful_sync,
		    last_any.started_at                     AS last_attempt_at,
		    COALESCE(last_any.status, '')           AS last_attempt_status,
		    COALESCE(s7.cnt, 0)                     AS successful_7d
		  FROM users u
		  LEFT JOIN garmin_credentials gc ON gc.user_id = u.id
		  LEFT JOIN efb_credentials    ec ON ec.user_id = u.id
		  LEFT JOIN (
		      SELECT user_id, MAX(finished_at) AS finished_at
		        FROM sync_runs
		       WHERE status = 'completed' AND finished_at IS NOT NULL
		       GROUP BY user_id
		  ) last_ok ON last_ok.user_id = u.id
		  LEFT JOIN (
		      SELECT user_id, started_at, status,
		             ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY started_at DESC) AS rn
		        FROM sync_runs
		  ) last_any ON last_any.user_id = u.id AND last_any.rn = 1
		  LEFT JOIN (
		      SELECT user_id, COUNT(*) AS cnt
		        FROM sync_runs
		       WHERE status = 'completed'
		         AND finished_at > datetime('now','-7 days')
		       GROUP BY user_id
		  ) s7 ON s7.user_id = u.id
		 ORDER BY (last_ok.finished_at IS NULL) ASC, last_ok.finished_at DESC, u.created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("database: get user activity overview: %w", err)
	}
	defer rows.Close()

	var out []UserActivity
	for rows.Next() {
		var ua UserActivity
		var isActive, syncEnabled, garminValid, efbValid, consentRequired int
		var lastOK, lastAttempt sql.NullString
		err := rows.Scan(
			&ua.UserID, &ua.Email, &isActive, &syncEnabled,
			&garminValid, &efbValid, &consentRequired,
			&lastOK, &lastAttempt,
			&ua.LastAttemptStatus, &ua.Successful7Days,
		)
		if err != nil {
			return nil, fmt.Errorf("database: scan user activity: %w", err)
		}
		ua.IsActive = isActive != 0
		ua.SyncEnabled = syncEnabled != 0
		ua.GarminValid = garminValid != 0
		ua.EFBValid = efbValid != 0
		ua.ConsentRequired = consentRequired != 0
		if lastOK.Valid {
			t, _ := parseTime(lastOK.String)
			ua.LastSuccessfulSync = &t
		}
		if lastAttempt.Valid {
			t, _ := parseTime(lastAttempt.String)
			ua.LastAttemptAt = &t
		}
		ua.Status = classifyUserStatus(ua)
		out = append(out, ua)
	}
	return out, rows.Err()
}

// classifyUserStatus derives a single status badge for the per-user table.
func classifyUserStatus(ua UserActivity) string {
	if !ua.GarminValid || !ua.EFBValid {
		return "disconnected"
	}
	if ua.ConsentRequired {
		return "consent_required"
	}
	if ua.LastSuccessfulSync == nil {
		return "never_synced"
	}
	if ua.LastAttemptStatus == "failed" || ua.LastAttemptStatus == "partial" {
		return "failing"
	}
	if time.Since(*ua.LastSuccessfulSync) > 7*24*time.Hour {
		return "stale"
	}
	return "synced"
}

// GetRecentFailures returns the most recent failed/partial sync runs (joined
// with email) and the most recent failed activity uploads. Both lists are
// capped at limit rows.
func (d *DB) GetRecentFailures(limit int) (*RecentFailures, error) {
	rows, err := d.db.Query(`
		SELECT sr.id, sr.user_id, sr.trigger, sr.started_at, sr.finished_at, sr.status,
		       sr.activities_found, sr.activities_synced, sr.activities_skipped,
		       sr.activities_failed, sr.trips_created, sr.error_message,
		       u.email
		  FROM sync_runs sr
		  JOIN users u ON u.id = sr.user_id
		 WHERE sr.status IN ('failed', 'partial')
		 ORDER BY sr.started_at DESC
		 LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("database: get recent failed sync runs: %w", err)
	}
	defer rows.Close()

	var runs []SyncRunWithEmail
	for rows.Next() {
		var r SyncRunWithEmail
		var startedAt string
		var finishedAt sql.NullString
		var errMsg *string
		err := rows.Scan(
			&r.ID, &r.UserID, &r.Trigger, &startedAt, &finishedAt, &r.Status,
			&r.ActivitiesFound, &r.ActivitiesSynced, &r.ActivitiesSkipped,
			&r.ActivitiesFailed, &r.TripsCreated, &errMsg,
			&r.Email,
		)
		if err != nil {
			return nil, fmt.Errorf("database: scan sync run with email: %w", err)
		}
		r.StartedAt, _ = parseTime(startedAt)
		if finishedAt.Valid {
			t, _ := parseTime(finishedAt.String)
			r.FinishedAt = &t
		}
		if errMsg != nil {
			r.ErrorMessage = *errMsg
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	acts, err := d.GetRecentFailedActivities(limit, false)
	if err != nil {
		return nil, err
	}

	return &RecentFailures{SyncRuns: runs, Activities: acts}, nil
}
