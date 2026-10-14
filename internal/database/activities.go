package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MaxStoredResponseBodies is the global cap on how many synced_activities
// rows may carry a non-NULL response_body_excerpt at any given time.
// When [DB.RecordActivityWithResponse] writes a new excerpt, older excerpts
// past this cap are nulled out in the same transaction.
const MaxStoredResponseBodies = 5

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

	// Diagnostic columns populated by RecordActivityWithResponse on
	// upload failures that reached the HTTP layer. Zero / empty when
	// not applicable.
	ResponseStatusCode  int
	ResponseSizeBytes   int
	ResponseBodyExcerpt string

	// TripMeta is what trip creation needs, recorded so a retry rebuilt
	// from this table can still create the trip. Zero on rows recorded
	// before migration 0016. Only GetFailedActivities loads it, so it is
	// kept out of the admin JSON that embeds this struct rather than show
	// a misleading zero there.
	TripMeta ActivityTripMeta `json:"-"`
}

// ActivityTripMeta is the part of a Garmin activity that trip creation needs
// beyond what identifies it: when it started (local wall clock), how long it
// lasted, and where it started and ended.
//
// It is stored all-or-nothing, keyed on StartTime: a zero StartTime writes
// NULL to every column, and the upsert keeps whatever the row already holds
// for a NULL — so a later write that lacks the metadata (a retry of an older
// row) never erases it.
type ActivityTripMeta struct {
	StartTime    time.Time
	DurationSecs float64
	StartLat     float64
	StartLng     float64
	EndLat       float64
	EndLng       float64
}

// tripMetaTimeLayout is the start_time column format: the local wall-clock
// form the Garmin script emits. The zone is deliberately not stored — trip
// creation works on the wall clock.
const tripMetaTimeLayout = "2006-01-02 15:04:05"

// args returns the six column values for an INSERT, all NULL when m carries
// no start time.
func (m ActivityTripMeta) args() []any {
	if m.StartTime.IsZero() {
		return []any{nil, nil, nil, nil, nil, nil}
	}
	return []any{m.StartTime.Format(tripMetaTimeLayout), m.DurationSecs, m.StartLat, m.StartLng, m.EndLat, m.EndLng}
}

// RecordActivity upserts an activity row: insert on first encounter, update on
// conflict (same user_id + garmin_activity_id). Trip metadata already on the
// row is kept; see [DB.RecordActivityWithMeta] to set it.
func (d *DB) RecordActivity(userID int64, garminID, name, actType, date, status, errMsg string) error {
	return d.RecordActivityWithMeta(userID, garminID, name, actType, date, status, errMsg, ActivityTripMeta{})
}

// RecordActivityWithMeta upserts an activity row like [DB.RecordActivity] and
// also stores meta, so a later retry of this activity can create its trip.
func (d *DB) RecordActivityWithMeta(userID int64, garminID, name, actType, date, status, errMsg string, meta ActivityTripMeta) error {
	args := append([]any{userID, garminID, name, actType, date, status, nullableStr(errMsg)}, meta.args()...)
	_, err := d.db.Exec(`
		INSERT INTO synced_activities
			(user_id, garmin_activity_id, activity_name, activity_type, activity_date, upload_status, error_message,
			 start_time, duration_secs, start_lat, start_lng, end_lat, end_lng)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, garmin_activity_id) DO UPDATE SET
			activity_name  = excluded.activity_name,
			activity_type  = excluded.activity_type,
			activity_date  = excluded.activity_date,
			upload_status  = excluded.upload_status,
			error_message  = excluded.error_message,
			`+tripMetaUpsert+`,
			synced_at      = datetime('now')
	`, args...)
	if err != nil {
		return fmt.Errorf("database: record activity %q for user %d: %w", garminID, userID, err)
	}
	return nil
}

// tripMetaUpsert is the ON CONFLICT assignment list for the trip metadata
// columns: a NULL in the new write keeps the stored value.
const tripMetaUpsert = `start_time    = COALESCE(excluded.start_time, start_time),
			duration_secs = COALESCE(excluded.duration_secs, duration_secs),
			start_lat     = COALESCE(excluded.start_lat, start_lat),
			start_lng     = COALESCE(excluded.start_lng, start_lng),
			end_lat       = COALESCE(excluded.end_lat, end_lat),
			end_lng       = COALESCE(excluded.end_lng, end_lng)`

// RecordActivityWithResponse upserts an activity row like [DB.RecordActivity]
// and additionally captures HTTP response diagnostics from the EFB upload
// attempt. statusCode and sizeBytes are stored on every failure that
// reached the HTTP layer; bodyExcerpt is stored only when non-empty (the
// caller passes "" for cases where the existing error_message is already
// actionable, see plan).
//
// To bound disk growth, the same transaction nulls out
// response_body_excerpt on all rows past the [MaxStoredResponseBodies]
// most-recent-by-synced_at, so at most that many full bodies are retained.
func (d *DB) RecordActivityWithResponse(
	userID int64,
	garminID, name, actType, date, status, errMsg string,
	statusCode, sizeBytes int,
	bodyExcerpt string,
	meta ActivityTripMeta,
) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("database: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.Exec(`
		INSERT INTO synced_activities
			(user_id, garmin_activity_id, activity_name, activity_type, activity_date,
			 upload_status, error_message,
			 response_status_code, response_size_bytes, response_body_excerpt,
			 start_time, duration_secs, start_lat, start_lng, end_lat, end_lng)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, garmin_activity_id) DO UPDATE SET
			activity_name         = excluded.activity_name,
			activity_type         = excluded.activity_type,
			activity_date         = excluded.activity_date,
			upload_status         = excluded.upload_status,
			error_message         = excluded.error_message,
			response_status_code  = excluded.response_status_code,
			response_size_bytes   = excluded.response_size_bytes,
			response_body_excerpt = excluded.response_body_excerpt,
			`+tripMetaUpsert+`,
			synced_at             = datetime('now')
	`, append([]any{
		userID, garminID, name, actType, date,
		status, nullableStr(errMsg),
		nullableInt(statusCode), nullableInt(sizeBytes), nullableStr(bodyExcerpt),
	}, meta.args()...)...); err != nil {
		return fmt.Errorf("database: record activity with response %q for user %d: %w", garminID, userID, err)
	}

	// Enforce the global cap: keep at most MaxStoredResponseBodies non-NULL
	// excerpts. Only runs when we just stored a new excerpt — otherwise
	// nothing changed and the cleanup is a no-op.
	if bodyExcerpt != "" {
		if _, err = tx.Exec(`
			UPDATE synced_activities
			   SET response_body_excerpt = NULL
			 WHERE response_body_excerpt IS NOT NULL
			   AND id NOT IN (
			       SELECT id FROM synced_activities
			        WHERE response_body_excerpt IS NOT NULL
			        ORDER BY synced_at DESC, id DESC
			        LIMIT ?
			   )
		`, MaxStoredResponseBodies); err != nil {
			return fmt.Errorf("database: prune response bodies: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("database: commit response diagnostics: %w", err)
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
		       activity_date, synced_at, upload_status, retry_count, error_message,
		       response_status_code, response_size_bytes, response_body_excerpt,
		       start_time, duration_secs, start_lat, start_lng, end_lat, end_lng
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
//
// includeBody controls whether ResponseBodyExcerpt is loaded; the column
// can hold up to 16 KB per row and is opt-in to keep the default response
// payload small.
func (d *DB) GetRecentFailedActivities(limit int, includeBody bool) ([]FailedActivityDetail, error) {
	rows, err := d.db.Query(`
		SELECT sa.id, sa.user_id, sa.garmin_activity_id, sa.activity_name,
		       sa.activity_type, sa.activity_date, sa.synced_at, sa.upload_status,
		       sa.retry_count, sa.error_message,
		       sa.response_status_code, sa.response_size_bytes, sa.response_body_excerpt,
		       u.email
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
		fa, err := scanFailedActivityDetail(rows)
		if err != nil {
			return nil, err
		}
		if !includeBody {
			fa.ResponseBodyExcerpt = ""
		}
		results = append(results, *fa)
	}
	return results, rows.Err()
}

// GetFailedActivity returns a single failed/permanent_failure activity by
// its row ID, including the full ResponseBodyExcerpt. Returns nil with no
// error when no row matches.
func (d *DB) GetFailedActivity(id int64) (*FailedActivityDetail, error) {
	row := d.db.QueryRow(`
		SELECT sa.id, sa.user_id, sa.garmin_activity_id, sa.activity_name,
		       sa.activity_type, sa.activity_date, sa.synced_at, sa.upload_status,
		       sa.retry_count, sa.error_message,
		       sa.response_status_code, sa.response_size_bytes, sa.response_body_excerpt,
		       u.email
		  FROM synced_activities sa
		  JOIN users u ON sa.user_id = u.id
		 WHERE sa.id = ?
		   AND sa.upload_status IN ('failed', 'permanent_failure')
	`, id)
	fa, err := scanFailedActivityDetail(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return fa, nil
}

func scanFailedActivityDetail(row interface {
	Scan(...any) error
}) (*FailedActivityDetail, error) {
	var fa FailedActivityDetail
	var syncedAt string
	var errMsg *string
	var statusCode, sizeBytes *int
	var bodyExcerpt *string

	err := row.Scan(
		&fa.ID, &fa.UserID, &fa.GarminActivityID,
		&fa.ActivityName, &fa.ActivityType, &fa.ActivityDate,
		&syncedAt, &fa.UploadStatus, &fa.RetryCount, &errMsg,
		&statusCode, &sizeBytes, &bodyExcerpt,
		&fa.Email,
	)
	if err != nil {
		return nil, err
	}

	fa.SyncedAt, _ = parseTime(syncedAt)
	if errMsg != nil {
		fa.ErrorMessage = *errMsg
	}
	if statusCode != nil {
		fa.ResponseStatusCode = *statusCode
	}
	if sizeBytes != nil {
		fa.ResponseSizeBytes = *sizeBytes
	}
	if bodyExcerpt != nil {
		fa.ResponseBodyExcerpt = *bodyExcerpt
	}
	return &fa, nil
}

// scanActivity scans a *sql.Rows row into a SyncedActivity. Expects the
// SELECT to include the response_status_code / response_size_bytes /
// response_body_excerpt columns after error_message, followed by the six
// trip metadata columns (start_time … end_lng).
func scanActivity(rows interface {
	Scan(...any) error
}) (*SyncedActivity, error) {
	var a SyncedActivity
	var syncedAt string
	var errMsg *string
	var statusCode, sizeBytes *int
	var bodyExcerpt *string
	var startTime sql.NullString
	var duration, startLat, startLng, endLat, endLng sql.NullFloat64

	err := rows.Scan(
		&a.ID, &a.UserID, &a.GarminActivityID,
		&a.ActivityName, &a.ActivityType, &a.ActivityDate,
		&syncedAt, &a.UploadStatus, &a.RetryCount, &errMsg,
		&statusCode, &sizeBytes, &bodyExcerpt,
		&startTime, &duration, &startLat, &startLng, &endLat, &endLng,
	)
	if err != nil {
		return nil, fmt.Errorf("database: scan activity: %w", err)
	}

	a.SyncedAt, _ = parseTime(syncedAt)
	if errMsg != nil {
		a.ErrorMessage = *errMsg
	}
	if statusCode != nil {
		a.ResponseStatusCode = *statusCode
	}
	if sizeBytes != nil {
		a.ResponseSizeBytes = *sizeBytes
	}
	if bodyExcerpt != nil {
		a.ResponseBodyExcerpt = *bodyExcerpt
	}
	// An unparseable start_time is treated like a missing one: the retry
	// still uploads, it just cannot create a trip.
	if startTime.Valid {
		if st, err := time.Parse(tripMetaTimeLayout, startTime.String); err == nil {
			a.TripMeta = ActivityTripMeta{
				StartTime:    st,
				DurationSecs: duration.Float64,
				StartLat:     startLat.Float64,
				StartLng:     startLng.Float64,
				EndLat:       endLat.Float64,
				EndLng:       endLng.Float64,
			}
		}
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

// nullableInt converts a zero int to nil for nullable INTEGER columns.
// Used so HTTP-layer status codes / sizes are NULL on rows where no upload
// reached the HTTP layer (e.g. Garmin download or login failures).
func nullableInt(i int) interface{} {
	if i == 0 {
		return nil
	}
	return i
}
