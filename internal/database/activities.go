package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SyncedActivity mirrors the synced_activities table.
type SyncedActivity struct {
	ID               int64
	UserID           int64
	GarminActivityID string
	ActivityName     string
	ActivityType     string
	ActivityDate     string
	SyncedAt         time.Time
	UploadStatus     string
	RetryCount       int
	ErrorMessage     string
}

// RecordActivity upserts an activity row: insert on first encounter, update on
// conflict (same user_id + garmin_activity_id).
func (d *DB) RecordActivity(userID int64, garminID, name, actType, date, status, errMsg string) error {
	_, err := d.db.Exec(`
		INSERT INTO synced_activities
			(user_id, garmin_activity_id, activity_name, activity_type, activity_date, upload_status, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, garmin_activity_id) DO UPDATE SET
			activity_name  = excluded.activity_name,
			activity_type  = excluded.activity_type,
			activity_date  = excluded.activity_date,
			upload_status  = excluded.upload_status,
			error_message  = excluded.error_message,
			synced_at      = datetime('now')
	`, userID, garminID, name, actType, date, status, nullableStr(errMsg))
	if err != nil {
		return fmt.Errorf("database: record activity %q for user %d: %w", garminID, userID, err)
	}
	return nil
}

// IsActivitySynced returns true when the activity exists with status "success".
func (d *DB) IsActivitySynced(userID int64, garminID string) (bool, error) {
	var count int
	err := d.db.QueryRow(
		`SELECT COUNT(*) FROM synced_activities
		  WHERE user_id = ? AND garmin_activity_id = ? AND upload_status = 'success'`,
		userID, garminID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("database: is activity synced: %w", err)
	}
	return count > 0, nil
}

// GetActivityStatus returns the upload_status for an activity, or "" if the
// activity does not exist in the database.
func (d *DB) GetActivityStatus(userID int64, garminID string) (string, error) {
	var status string
	err := d.db.QueryRow(
		`SELECT upload_status FROM synced_activities
		  WHERE user_id = ? AND garmin_activity_id = ?`,
		userID, garminID,
	).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("database: get activity status: %w", err)
	}
	return status, nil
}

// GetFailedActivities returns activities with status "failed" and retry_count < 3.
func (d *DB) GetFailedActivities(userID int64) ([]SyncedActivity, error) {
	rows, err := d.db.Query(`
		SELECT id, user_id, garmin_activity_id, activity_name, activity_type,
		       activity_date, synced_at, upload_status, retry_count, error_message
		  FROM synced_activities
		 WHERE user_id = ? AND upload_status = 'failed' AND retry_count < 3
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("database: get failed activities: %w", err)
	}
	defer rows.Close()

	var acts []SyncedActivity
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		acts = append(acts, *a)
	}
	return acts, rows.Err()
}

// IncrementRetryCount adds 1 to retry_count for the given activity.
func (d *DB) IncrementRetryCount(userID int64, garminID string) error {
	_, err := d.db.Exec(`
		UPDATE synced_activities
		   SET retry_count = retry_count + 1
		 WHERE user_id = ? AND garmin_activity_id = ?
	`, userID, garminID)
	if err != nil {
		return fmt.Errorf("database: increment retry count for activity %q: %w", garminID, err)
	}
	return nil
}

// MarkPermanentFailure sets status = "permanent_failure" for the given activity.
func (d *DB) MarkPermanentFailure(userID int64, garminID string) error {
	_, err := d.db.Exec(`
		UPDATE synced_activities
		   SET upload_status = 'permanent_failure'
		 WHERE user_id = ? AND garmin_activity_id = ?
	`, userID, garminID)
	if err != nil {
		return fmt.Errorf("database: mark permanent failure for activity %q: %w", garminID, err)
	}
	return nil
}

// FailedActivityDetail extends SyncedActivity with the user's email for
// admin views that show failures across all users.
type FailedActivityDetail struct {
	SyncedActivity
	Email string
}

// GetRecentFailedActivities returns the most recent failed or permanent_failure
// activities across all users, joined with the user's email.
func (d *DB) GetRecentFailedActivities(limit int) ([]FailedActivityDetail, error) {
	rows, err := d.db.Query(`
		SELECT sa.id, sa.user_id, sa.garmin_activity_id, sa.activity_name,
		       sa.activity_type, sa.activity_date, sa.synced_at, sa.upload_status,
		       sa.retry_count, sa.error_message, u.email
		  FROM synced_activities sa
		  JOIN users u ON sa.user_id = u.id
		 WHERE sa.upload_status IN ('failed', 'permanent_failure')
		 ORDER BY sa.synced_at DESC
		 LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("database: get recent failed activities: %w", err)
	}
	defer rows.Close()

	var results []FailedActivityDetail
	for rows.Next() {
		var fa FailedActivityDetail
		var syncedAt string
		var errMsg *string

		err := rows.Scan(
			&fa.ID, &fa.UserID, &fa.GarminActivityID,
			&fa.ActivityName, &fa.ActivityType, &fa.ActivityDate,
			&syncedAt, &fa.UploadStatus, &fa.RetryCount, &errMsg,
			&fa.Email,
		)
		if err != nil {
			return nil, fmt.Errorf("database: scan failed activity: %w", err)
		}

		fa.SyncedAt, _ = parseTime(syncedAt)
		if errMsg != nil {
			fa.ErrorMessage = *errMsg
		}
		results = append(results, fa)
	}
	return results, rows.Err()
}

// scanActivity scans a *sql.Rows row into a SyncedActivity.
func scanActivity(rows interface {
	Scan(...any) error
}) (*SyncedActivity, error) {
	var a SyncedActivity
	var syncedAt string
	var errMsg *string

	err := rows.Scan(
		&a.ID, &a.UserID, &a.GarminActivityID,
		&a.ActivityName, &a.ActivityType, &a.ActivityDate,
		&syncedAt, &a.UploadStatus, &a.RetryCount, &errMsg,
	)
	if err != nil {
		return nil, fmt.Errorf("database: scan activity: %w", err)
	}

	a.SyncedAt, _ = parseTime(syncedAt)
	if errMsg != nil {
		a.ErrorMessage = *errMsg
	}
	return &a, nil
}

// nullableStr converts an empty string to nil for nullable TEXT columns.
func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
