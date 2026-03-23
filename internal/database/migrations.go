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
}
