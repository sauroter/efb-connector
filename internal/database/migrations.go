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

	// 0010 – flip the recommended default for auto_create_trips to ON for
	// users who haven't yet completed onboarding. Migration 0002 added the
	// column with DEFAULT 0; in practice that meant new users finished
	// setup with tracks-only uploads and reported "doesn't work" because
	// no Fahrtenbuch entries appeared. Users who already completed
	// onboarding (setup_completed = 1) made an explicit choice and are
	// left untouched.
	`UPDATE users SET auto_create_trips = 1 WHERE setup_completed = 0;`,

	// 0011 – persist pre-filter Garmin diagnostics on every sync_run so the
	// dashboard can surface a "we saw cycling/running/other but no kayaking"
	// hint when activities_found=0. type_keys_seen is a JSON array of
	// strings (sorted, deduplicated by the Python script); raw_count is the
	// total activity count Garmin returned before the water-sport filter.
	// name_matched_count records how many activities the opt-in
	// match_by_name fallback recovered (always 0 when the flag is off);
	// it lets the dashboard render "we recovered N mis-tagged activities
	// this run" without re-querying Garmin. All three columns are nullable
	// so historical rows (and runs that hit Garmin auth failures before
	// listing anything) are not retroactively misrepresented.
	`ALTER TABLE sync_runs ADD COLUMN type_keys_seen TEXT;
ALTER TABLE sync_runs ADD COLUMN raw_count INTEGER;
ALTER TABLE sync_runs ADD COLUMN name_matched_count INTEGER;`,

	// 0012 – opt-in fallback: include activities tagged "Other" /
	// parent_type_id 17 (generic fitness) when their activityName contains
	// a water-sport keyword (Kajak, Kanu, Paddel, Rudern, SUP, kayak,
	// canoe, paddle, row, stand_up_paddl). Default off because the
	// keyword match is name-only and would otherwise import a
	// non-water-sport activity that just happens to mention "paddle" in
	// its name. Users on watches without a native kayak profile (Garmin
	// Venu 3 is the canonical case) opt in via the settings page.
	`ALTER TABLE users ADD COLUMN match_by_name INTEGER NOT NULL DEFAULT 0;`,

	// 0013 – per-user activity-type exclusion. excluded_activity_types is a
	// JSON-encoded array of stable category keys (kayak, canoe, paddle,
	// sup, rowing, whitewater) the user does NOT want synced to EFB.
	// Default '[]' preserves today's behaviour (all water sports sync).
	// Driven by the Wanderfahrerabzeichen use case: paddlers who also
	// row need to exclude rowing so it doesn't pollute their paddle-km.
	// excluded_count on sync_runs persists the per-run drop counter so
	// the dashboard / admin sync-history can surface "X activities
	// excluded by your filter" without re-running Garmin.
	`ALTER TABLE users ADD COLUMN excluded_activity_types TEXT NOT NULL DEFAULT '[]';
ALTER TABLE sync_runs ADD COLUMN excluded_count INTEGER NOT NULL DEFAULT 0;`,

	// 0014 – invert the activity-type filter: store what the user WANTS
	// synced (selected_activity_types) instead of what they excluded.
	//
	// Prompted by sailing showing up in eFB. Garmin's Water Sports parent
	// (228) admits sailing, surfing, windsurfing and motorboating as well as
	// paddle sports, and the exclusion model could not express "drop these"
	// for any category the code didn't already know: unrecognised typeKeys
	// were kept by design. Recording the positive selection instead means a
	// *named* category added after this migration is off until someone ticks
	// it.
	//
	// That is not a guarantee against the leak recurring, and must not be
	// read as one: an unrecognised typeKey under parent 228 is classified as
	// other_water, which is default-on (deliberately — see the note on
	// garmin.CategoryOtherWater). So a new Garmin water sport still uploads
	// by default; what this buys is that it lands in a box the user can
	// untick, and that excluded_count/the dashboard hint can explain it.
	//
	// The backfilled list is the paddle sports plus the other_water catch-all
	// (see garmin.DefaultSelectedCategories, which must stay in agreement),
	// minus whatever the user had already excluded. So this migration DOES
	// change behaviour for existing users, deliberately: sailing, surfing,
	// windsurfing and motorboating stop syncing on deploy. That is the bug
	// being fixed — a canoe logbook should not fill up with sailing trips
	// because nobody noticed to untick a box. Anyone who wants them back
	// ticks the box on /settings.
	//
	// Already-uploaded activities are untouched; the connector never deletes
	// from EFB, so historical sailing entries stay until removed by hand.
	//
	// The key list is hardcoded on purpose — a migration is a point-in-time
	// snapshot, and freezing it here is what makes later categories default
	// to off. excluded_activity_types is left in place (applied migrations
	// are never modified) but is no longer read.
	//
	// The input guard is not paranoia about our own writes (the column is NOT
	// NULL and only ever set from json.Marshal): it bounds the blast radius,
	// in two directions.
	//
	// json_each() raises on malformed input, which would fail the migration,
	// roll back execMulti's transaction, and make Open — and so the whole
	// server — refuse to start. Hence the CASE: an unreadable value degrades
	// to "excluded nothing" rather than taking the service down on deploy.
	//
	// json_valid() alone is not enough, though. `null` and `[null]` are both
	// valid JSON, and either makes json_each yield a SQL NULL, which turns
	// `NOT IN` into NULL — never true — for every candidate key, silently
	// backfilling an EMPTY selection. Under this model empty means "sync
	// nothing", so that row's owner would lose their imports permanently.
	// json_type() = 'array' rejects the first shape and the IS NOT NULL
	// filter rejects the second, so both fail open like every other bad value.
	//
	// The column DEFAULT is the same paddle-sport list rather than '[]' for
	// the same reason: any future INSERT that forgets the column gets a
	// working user, not a silently dead one.
	`ALTER TABLE users ADD COLUMN selected_activity_types TEXT NOT NULL DEFAULT '["kayak","canoe","paddle","sup","rowing","whitewater","other_water"]';
UPDATE users SET selected_activity_types = (
  SELECT json_group_array(v.value)
    FROM json_each('["kayak","canoe","paddle","sup","rowing","whitewater","other_water"]') v
   WHERE v.value NOT IN (
     SELECT value FROM json_each(
       CASE WHEN json_valid(users.excluded_activity_types)
             AND json_type(users.excluded_activity_types) = 'array'
            THEN users.excluded_activity_types ELSE '[]' END
     )
     WHERE value IS NOT NULL
   )
);`,
}
