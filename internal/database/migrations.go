package database

// migrations is the ordered list of SQL migrations to apply.
// Each entry is a self-contained SQL statement (or a batch of statements).
// Once applied, migrations are never modified — only new entries are appended.
var migrations = []string{
	// 0001 – initial schema
	`CREATE TABLE IF NOT EXISTS users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    email        TEXT    NOT NULL UNIQUE,
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    is_active    INTEGER NOT NULL DEFAULT 1,
    sync_enabled INTEGER NOT NULL DEFAULT 1,
    sync_days    INTEGER NOT NULL DEFAULT 3
);

CREATE TABLE IF NOT EXISTS magic_links (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    email      TEXT    NOT NULL,
    token_hash TEXT    NOT NULL UNIQUE,
    expires_at TEXT    NOT NULL,
    used_at    TEXT,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT    NOT NULL UNIQUE,
    expires_at TEXT    NOT NULL,
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    last_seen  TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS garmin_credentials (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id             INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    email_encrypted     BLOB    NOT NULL,
    password_encrypted  BLOB    NOT NULL,
    is_valid            INTEGER NOT NULL DEFAULT 1,
    last_error          TEXT,
    updated_at          TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS efb_credentials (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id             INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    username_encrypted  BLOB    NOT NULL,
    password_encrypted  BLOB    NOT NULL,
    session_cookie      BLOB,
    session_expires_at  TEXT,
    is_valid            INTEGER NOT NULL DEFAULT 1,
    last_error          TEXT,
    updated_at          TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS synced_activities (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    garmin_activity_id TEXT    NOT NULL,
    activity_name      TEXT,
    activity_type      TEXT,
    activity_date      TEXT,
    synced_at          TEXT    NOT NULL DEFAULT (datetime('now')),
    upload_status      TEXT    NOT NULL DEFAULT 'success',
    retry_count        INTEGER NOT NULL DEFAULT 0,
    error_message      TEXT,
    UNIQUE(user_id, garmin_activity_id)
);

CREATE TABLE IF NOT EXISTS sync_runs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    trigger             TEXT    NOT NULL DEFAULT 'scheduled',
    started_at          TEXT    NOT NULL DEFAULT (datetime('now')),
    finished_at         TEXT,
    status              TEXT    NOT NULL DEFAULT 'running',
    activities_found    INTEGER DEFAULT 0,
    activities_synced   INTEGER DEFAULT 0,
    activities_skipped  INTEGER DEFAULT 0,
    activities_failed   INTEGER DEFAULT 0,
    error_message       TEXT
);`,

	// 0002 – add auto_create_trips user preference
	`ALTER TABLE users ADD COLUMN auto_create_trips INTEGER NOT NULL DEFAULT 0;`,

	// 0003 – track trips created during sync
	`ALTER TABLE sync_runs ADD COLUMN trips_created INTEGER NOT NULL DEFAULT 0;`,

	// 0004 – track whether user has completed the onboarding preferences step
	`ALTER TABLE users ADD COLUMN setup_completed INTEGER NOT NULL DEFAULT 0;`,

	// 0005 – add enrich_trips user preference (default enabled for existing users)
	`ALTER TABLE users ADD COLUMN enrich_trips INTEGER NOT NULL DEFAULT 1;`,

	// 0006 – add preferred_lang user preference (empty = auto-detect)
	`ALTER TABLE users ADD COLUMN preferred_lang TEXT NOT NULL DEFAULT '';`,

	// 0007 – feedback table
	`CREATE TABLE IF NOT EXISTS feedback (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category   TEXT    NOT NULL DEFAULT 'general',
    message    TEXT    NOT NULL,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);`,

	// 0008 – capture upload-response diagnostics on synced_activities for
	// debugging silent EFB rejections (status code + size on every failure,
	// full body excerpt only when the existing summariseResponse heuristics
	// produced no actionable hint, capped to 16 KB per row and 5 rows total
	// by application logic in RecordActivityWithResponse).
	`ALTER TABLE synced_activities ADD COLUMN response_status_code INTEGER;
ALTER TABLE synced_activities ADD COLUMN response_size_bytes  INTEGER;
ALTER TABLE synced_activities ADD COLUMN response_body_excerpt TEXT;`,

	// 0009 – track EFB v2026.1 track-usage consent state per user. Set when
	// an upload (or settings save) hits the consent gate; cleared on first
	// successful upload. consent_notified_at rate-limits the user-facing
	// notification email to ≤ once per 7 days.
	`ALTER TABLE efb_credentials ADD COLUMN consent_required INTEGER NOT NULL DEFAULT 0;
ALTER TABLE efb_credentials ADD COLUMN consent_notified_at TEXT;`,
}
