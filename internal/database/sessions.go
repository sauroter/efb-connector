package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CreateMagicLink inserts a magic_link row.
// tokenHash should already be a hash of the actual token (e.g. SHA-256 hex).
func (d *DB) CreateMagicLink(email, tokenHash string, expiresAt time.Time) error {
	_, err := d.db.Exec(`
		INSERT INTO magic_links (email, token_hash, expires_at)
		VALUES (?, ?, ?)
	`, email, tokenHash, expiresAt.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("database: create magic link: %w", err)
	}
	return nil
}

// Magic-link validation failure modes. They are distinguished so the login
// handler can tell the user which one happened: "already used" and "expired"
// used to render as the same message, which sent a real bug report down the
// wrong path.
var (
	ErrMagicLinkNotFound = errors.New("database: magic link not found")
	ErrMagicLinkUsed     = errors.New("database: magic link already used")
	ErrMagicLinkExpired  = errors.New("database: magic link expired")
)

// ValidateMagicLink consumes the link identified by tokenHash and returns the
// associated email. Consumption is a single conditional UPDATE, so concurrent
// callers cannot both succeed on the same token.
//
// expires_at is compared as a string: CreateMagicLink writes UTC in exactly the
// format SQLite's datetime('now') produces, so lexical order is chronological.
func (d *DB) ValidateMagicLink(tokenHash string) (email string, err error) {
	err = d.db.QueryRow(`
		UPDATE magic_links
		   SET used_at = datetime('now')
		 WHERE token_hash = ?
		   AND used_at IS NULL
		   AND expires_at > datetime('now')
		RETURNING email
	`, tokenHash).Scan(&email)

	if err == nil {
		return email, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("database: consume magic link: %w", err)
	}

	// The UPDATE matched nothing. Work out why, for the user-facing message.
	return "", d.classifyMagicLinkFailure(tokenHash)
}

// classifyMagicLinkFailure runs only on the failure path, to turn "no row was
// updated" into a specific reason.
func (d *DB) classifyMagicLinkFailure(tokenHash string) error {
	var expiresAtStr string
	var usedAt sql.NullString

	err := d.db.QueryRow(`
		SELECT expires_at, used_at FROM magic_links WHERE token_hash = ?
	`, tokenHash).Scan(&expiresAtStr, &usedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return ErrMagicLinkNotFound
	}
	if err != nil {
		return fmt.Errorf("database: query magic link: %w", err)
	}

	// Check used before expired: a link that was used and has since expired is
	// more usefully reported as used.
	if usedAt.Valid {
		return ErrMagicLinkUsed
	}

	expiresAt, err := parseTime(expiresAtStr)
	if err != nil {
		return fmt.Errorf("database: parse magic link expiry: %w", err)
	}
	if time.Now().After(expiresAt) {
		return ErrMagicLinkExpired
	}

	// Row exists, unused, unexpired — the UPDATE should have matched. Only
	// reachable if another caller consumed it between the two statements.
	return ErrMagicLinkUsed
}

// CreateSession inserts a session row for userID.
func (d *DB) CreateSession(userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := d.db.Exec(`
		INSERT INTO sessions (user_id, token_hash, expires_at)
		VALUES (?, ?, ?)
	`, userID, tokenHash, expiresAt.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("database: create session: %w", err)
	}
	return nil
}

// GetSession validates that the session exists and has not expired, updates
// last_seen, and returns the owning userID.
func (d *DB) GetSession(tokenHash string) (userID int64, err error) {
	var id int64
	var expiresAtStr string

	err = d.db.QueryRow(`
		SELECT id, user_id, expires_at FROM sessions WHERE token_hash = ?
	`, tokenHash).Scan(&id, &userID, &expiresAtStr)

	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("database: session not found")
	}
	if err != nil {
		return 0, fmt.Errorf("database: query session: %w", err)
	}

	expiresAt, err := parseTime(expiresAtStr)
	if err != nil {
		return 0, fmt.Errorf("database: parse session expiry: %w", err)
	}
	if time.Now().After(expiresAt) {
		return 0, fmt.Errorf("database: session expired")
	}

	if _, err := d.db.Exec(
		`UPDATE sessions SET last_seen = datetime('now') WHERE id = ?`, id,
	); err != nil {
		return 0, fmt.Errorf("database: update session last_seen: %w", err)
	}

	return userID, nil
}

// DeleteSession removes the session with the given tokenHash.
func (d *DB) DeleteSession(tokenHash string) error {
	if _, err := d.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("database: delete session: %w", err)
	}
	return nil
}

// magicLinkRetention keeps magic links around after they expire so
// classifyMagicLinkFailure can still tell a late clicker "this link expired"
// rather than the vaguer "invalid link". Links expire 15 minutes after issue,
// so a week of grace covers any realistic late click while still bounding the
// table. Sessions need no equivalent: GetSession returns the same generic error
// whether the row is expired or absent.
const magicLinkRetention = "-7 days"

// CleanupStats reports what a CleanupExpired sweep removed.
type CleanupStats struct {
	MagicLinks int64
	Sessions   int64
}

// CleanupExpired deletes expired sessions and magic links that are past the
// retention grace period, returning what it removed so callers can log it.
func (d *DB) CleanupExpired() (CleanupStats, error) {
	var stats CleanupStats

	res, err := d.db.Exec(
		`DELETE FROM magic_links WHERE expires_at < datetime('now', ?)`,
		magicLinkRetention,
	)
	if err != nil {
		return stats, fmt.Errorf("database: cleanup expired magic links: %w", err)
	}
	stats.MagicLinks = rowsAffected(res)

	res, err = d.db.Exec(
		`DELETE FROM sessions WHERE expires_at < datetime('now')`,
	)
	if err != nil {
		return stats, fmt.Errorf("database: cleanup expired sessions: %w", err)
	}
	stats.Sessions = rowsAffected(res)

	return stats, nil
}

// rowsAffected extracts a row count for logging. The delete has already
// succeeded by the time this runs, so a driver that cannot report a count
// yields 0 rather than failing the sweep.
func rowsAffected(res sql.Result) int64 {
	n, err := res.RowsAffected()
	if err != nil {
		return 0
	}
	return n
}
