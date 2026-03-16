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

// ValidateMagicLink checks that tokenHash exists, has not been used, and has
// not expired. On success it marks the link as used and returns the associated
// email.
func (d *DB) ValidateMagicLink(tokenHash string) (email string, err error) {
	var id int64
	var expiresAtStr string
	var usedAt sql.NullString

	err = d.db.QueryRow(`
		SELECT id, email, expires_at, used_at
		  FROM magic_links WHERE token_hash = ?
	`, tokenHash).Scan(&id, &email, &expiresAtStr, &usedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("database: magic link not found")
	}
	if err != nil {
		return "", fmt.Errorf("database: query magic link: %w", err)
	}

	if usedAt.Valid {
		return "", fmt.Errorf("database: magic link already used")
	}

	expiresAt, err := parseTime(expiresAtStr)
	if err != nil {
		return "", fmt.Errorf("database: parse magic link expiry: %w", err)
	}
	if time.Now().After(expiresAt) {
		return "", fmt.Errorf("database: magic link expired")
	}

	if _, err := d.db.Exec(
		`UPDATE magic_links SET used_at = datetime('now') WHERE id = ?`, id,
	); err != nil {
		return "", fmt.Errorf("database: mark magic link used: %w", err)
	}

	return email, nil
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

// CleanupExpired deletes all expired magic_links and sessions.
func (d *DB) CleanupExpired() error {
	if _, err := d.db.Exec(
		`DELETE FROM magic_links WHERE expires_at < datetime('now')`,
	); err != nil {
		return fmt.Errorf("database: cleanup expired magic links: %w", err)
	}

	if _, err := d.db.Exec(
		`DELETE FROM sessions WHERE expires_at < datetime('now')`,
	); err != nil {
		return fmt.Errorf("database: cleanup expired sessions: %w", err)
	}

	return nil
}
