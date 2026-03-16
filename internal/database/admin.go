package database

import (
	"fmt"
	"os"
	"time"
)

// SystemStats holds aggregate system statistics for the admin status endpoint.
type SystemStats struct {
	TotalUsers    int    `json:"total_users"`
	ActiveUsers   int    `json:"active_users"`
	SyncableUsers int    `json:"syncable_users"`
	DBSizeBytes   int64  `json:"db_size_bytes"`
	DBPath        string `json:"db_path"`
}

// UserStatus holds per-user status information for the admin users endpoint.
type UserStatus struct {
	ID              int64      `json:"id"`
	Email           string     `json:"email"`
	CreatedAt       time.Time  `json:"created_at"`
	IsActive        bool       `json:"is_active"`
	SyncEnabled     bool       `json:"sync_enabled"`
	GarminConnected bool       `json:"garmin_connected"`
	GarminValid     bool       `json:"garmin_valid"`
	EFBConnected    bool       `json:"efb_connected"`
	EFBValid        bool       `json:"efb_valid"`
	LastSyncAt      *time.Time `json:"last_sync_at"`
	LastSyncStatus  string     `json:"last_sync_status,omitempty"`
}

// GetSystemStats returns aggregate statistics about the system.
func (d *DB) GetSystemStats() (*SystemStats, error) {
	stats := &SystemStats{}

	err := d.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&stats.TotalUsers)
	if err != nil {
		return nil, fmt.Errorf("database: count users: %w", err)
	}

	err = d.db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_active = 1`).Scan(&stats.ActiveUsers)
	if err != nil {
		return nil, fmt.Errorf("database: count active users: %w", err)
	}

	err = d.db.QueryRow(`
		SELECT COUNT(*) FROM users u
		  JOIN garmin_credentials gc ON gc.user_id = u.id AND gc.is_valid = 1
		  JOIN efb_credentials    ec ON ec.user_id = u.id AND ec.is_valid = 1
		 WHERE u.is_active = 1 AND u.sync_enabled = 1
	`).Scan(&stats.SyncableUsers)
	if err != nil {
		return nil, fmt.Errorf("database: count syncable users: %w", err)
	}

	// Get DB file size from the filesystem.
	var dbPath string
	err = d.db.QueryRow(`PRAGMA database_list`).Scan(new(int), new(string), &dbPath)
	if err == nil && dbPath != "" {
		stats.DBPath = dbPath
		if fi, ferr := os.Stat(dbPath); ferr == nil {
			stats.DBSizeBytes = fi.Size()
		}
	}

	return stats, nil
}

// GetAllUsersWithStatus returns all users with their credential and last sync status.
func (d *DB) GetAllUsersWithStatus() ([]UserStatus, error) {
	rows, err := d.db.Query(`
		SELECT
			u.id, u.email, u.created_at, u.is_active, u.sync_enabled,
			COALESCE(gc.user_id, 0) > 0 AS garmin_connected,
			COALESCE(gc.is_valid, 0) AS garmin_valid,
			COALESCE(ec.user_id, 0) > 0 AS efb_connected,
			COALESCE(ec.is_valid, 0) AS efb_valid,
			sr.started_at AS last_sync_at,
			sr.status AS last_sync_status
		FROM users u
		LEFT JOIN garmin_credentials gc ON gc.user_id = u.id
		LEFT JOIN efb_credentials    ec ON ec.user_id = u.id
		LEFT JOIN (
			SELECT user_id, started_at, status,
			       ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY started_at DESC) AS rn
			  FROM sync_runs
		) sr ON sr.user_id = u.id AND sr.rn = 1
		ORDER BY u.id
	`)
	if err != nil {
		return nil, fmt.Errorf("database: get all users with status: %w", err)
	}
	defer rows.Close()

	var users []UserStatus
	for rows.Next() {
		var us UserStatus
		var createdAt string
		var isActive, syncEnabled, garminConn, garminValid, efbConn, efbValid int
		var lastSyncAt, lastSyncStatus *string

		err := rows.Scan(
			&us.ID, &us.Email, &createdAt, &isActive, &syncEnabled,
			&garminConn, &garminValid, &efbConn, &efbValid,
			&lastSyncAt, &lastSyncStatus,
		)
		if err != nil {
			return nil, fmt.Errorf("database: scan user status: %w", err)
		}

		us.CreatedAt, _ = parseTime(createdAt)
		us.IsActive = isActive != 0
		us.SyncEnabled = syncEnabled != 0
		us.GarminConnected = garminConn != 0
		us.GarminValid = garminValid != 0
		us.EFBConnected = efbConn != 0
		us.EFBValid = efbValid != 0
		if lastSyncAt != nil {
			t, _ := parseTime(*lastSyncAt)
			us.LastSyncAt = &t
		}
		if lastSyncStatus != nil {
			us.LastSyncStatus = *lastSyncStatus
		}

		users = append(users, us)
	}
	return users, rows.Err()
}

// GetRecentFailedSyncRuns returns the most recent failed or partial sync runs
// across all users.
func (d *DB) GetRecentFailedSyncRuns(limit int) ([]SyncRun, error) {
	rows, err := d.db.Query(`
		SELECT id, user_id, trigger, started_at, finished_at, status,
		       activities_found, activities_synced, activities_skipped,
		       activities_failed, error_message
		  FROM sync_runs
		 WHERE status IN ('failed', 'partial')
		 ORDER BY started_at DESC
		 LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("database: get recent failed sync runs: %w", err)
	}
	defer rows.Close()

	var runs []SyncRun
	for rows.Next() {
		r, err := scanSyncRunRow(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *r)
	}
	return runs, rows.Err()
}
