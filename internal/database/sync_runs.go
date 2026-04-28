package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SyncRun mirrors the sync_runs table.
type SyncRun struct {
	ID                int64
	UserID            int64
	Trigger           string
	StartedAt         time.Time
	FinishedAt        *time.Time
	Status            string
	ActivitiesFound   int
	ActivitiesSynced  int
	ActivitiesSkipped int
	ActivitiesFailed  int
	TripsCreated      int
	ErrorMessage      string
}

// CreateSyncRun inserts a new sync_run row with status "running" and returns
// its ID.
func (d *DB) CreateSyncRun(userID int64, trigger string) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO sync_runs (user_id, trigger) VALUES (?, ?)`,
		userID, trigger,
	)
	if err != nil {
		return 0, fmt.Errorf("database: create sync run: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("database: get sync run id: %w", err)
	}
	return id, nil
}

// UpdateSyncRun sets finished_at, status and counters on a sync_run row.
func (d *DB) UpdateSyncRun(id int64, status string, found, synced, skipped, failed, tripsCreated int, errMsg string) error {
	_, err := d.db.Exec(`
		UPDATE sync_runs
		   SET finished_at          = datetime('now'),
		       status               = ?,
		       activities_found     = ?,
		       activities_synced    = ?,
		       activities_skipped   = ?,
		       activities_failed    = ?,
		       trips_created        = ?,
		       error_message        = ?
		 WHERE id = ?
	`, status, found, synced, skipped, failed, tripsCreated, nullableStr(errMsg), id)
	if err != nil {
		return fmt.Errorf("database: update sync run %d: %w", id, err)
	}
	return nil
}

// GetSyncRun returns the sync_run with the given id.
func (d *DB) GetSyncRun(id int64) (*SyncRun, error) {
	row := d.db.QueryRow(`
		SELECT id, user_id, trigger, started_at, finished_at, status,
		       activities_found, activities_synced, activities_skipped,
		       activities_failed, trips_created, error_message
		  FROM sync_runs WHERE id = ?
	`, id)

	r, err := scanSyncRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// GetSyncHistory returns the last limit sync_runs for a user, newest first.
func (d *DB) GetSyncHistory(userID int64, limit int) ([]SyncRun, error) {
	rows, err := d.db.Query(`
		SELECT id, user_id, trigger, started_at, finished_at, status,
		       activities_found, activities_synced, activities_skipped,
		       activities_failed, trips_created, error_message
		  FROM sync_runs
		 WHERE user_id = ?
		 ORDER BY started_at DESC
		 LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("database: get sync history: %w", err)
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

// scanSyncRun scans a *sql.Row.
func scanSyncRun(row *sql.Row) (*SyncRun, error) {
	var r SyncRun
	var startedAt string
	var finishedAt sql.NullString
	var errMsg *string

	err := row.Scan(
		&r.ID, &r.UserID, &r.Trigger, &startedAt, &finishedAt, &r.Status,
		&r.ActivitiesFound, &r.ActivitiesSynced, &r.ActivitiesSkipped,
		&r.ActivitiesFailed, &r.TripsCreated, &errMsg,
	)
	if err != nil {
		return nil, err
	}

	r.StartedAt, _ = parseTime(startedAt)
	if finishedAt.Valid {
		t, _ := parseTime(finishedAt.String)
		r.FinishedAt = &t
	}
	if errMsg != nil {
		r.ErrorMessage = *errMsg
	}
	return &r, nil
}

// scanSyncRunRow scans a *sql.Rows (plural).
func scanSyncRunRow(rows *sql.Rows) (*SyncRun, error) {
	var r SyncRun
	var startedAt string
	var finishedAt sql.NullString
	var errMsg *string

	err := rows.Scan(
		&r.ID, &r.UserID, &r.Trigger, &startedAt, &finishedAt, &r.Status,
		&r.ActivitiesFound, &r.ActivitiesSynced, &r.ActivitiesSkipped,
		&r.ActivitiesFailed, &r.TripsCreated, &errMsg,
	)
	if err != nil {
		return nil, fmt.Errorf("database: scan sync run: %w", err)
	}

	r.StartedAt, _ = parseTime(startedAt)
	if finishedAt.Valid {
		t, _ := parseTime(finishedAt.String)
		r.FinishedAt = &t
	}
	if errMsg != nil {
		r.ErrorMessage = *errMsg
	}
	return &r, nil
}
