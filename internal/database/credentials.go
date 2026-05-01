package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"efb-connector/internal/crypto"
)

// ──────────────────────────────────────────────
// Garmin credentials
// ──────────────────────────────────────────────

// SaveGarminCredentials encrypts email and password with the DB key and upserts
// a row in garmin_credentials for userID.
func (d *DB) SaveGarminCredentials(userID int64, email, password string) error {
	encEmail, err := crypto.Encrypt([]byte(email), d.encryptionKey)
	if err != nil {
		return fmt.Errorf("database: encrypt garmin email: %w", err)
	}

	encPass, err := crypto.Encrypt([]byte(password), d.encryptionKey)
	if err != nil {
		return fmt.Errorf("database: encrypt garmin password: %w", err)
	}

	_, err = d.db.Exec(`
		INSERT INTO garmin_credentials (user_id, email_encrypted, password_encrypted, is_valid, last_error, updated_at)
		VALUES (?, ?, ?, 1, NULL, datetime('now'))
		ON CONFLICT(user_id) DO UPDATE SET
			email_encrypted    = excluded.email_encrypted,
			password_encrypted = excluded.password_encrypted,
			is_valid           = 1,
			last_error         = NULL,
			updated_at         = datetime('now')
	`, userID, encEmail, encPass)
	if err != nil {
		return fmt.Errorf("database: save garmin credentials for user %d: %w", userID, err)
	}
	return nil
}

// GetGarminCredentials returns the decrypted email and password for userID.
// Returns sql.ErrNoRows-wrapped error when no credentials exist.
func (d *DB) GetGarminCredentials(userID int64) (email, password string, err error) {
	var encEmail, encPass []byte
	err = d.db.QueryRow(
		`SELECT email_encrypted, password_encrypted FROM garmin_credentials WHERE user_id = ?`,
		userID,
	).Scan(&encEmail, &encPass)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("database: no garmin credentials for user %d", userID)
	}
	if err != nil {
		return "", "", fmt.Errorf("database: query garmin credentials: %w", err)
	}

	emailBytes, err := crypto.Decrypt(encEmail, d.encryptionKey)
	if err != nil {
		return "", "", fmt.Errorf("database: decrypt garmin email: %w", err)
	}

	passBytes, err := crypto.Decrypt(encPass, d.encryptionKey)
	if err != nil {
		return "", "", fmt.Errorf("database: decrypt garmin password: %w", err)
	}

	return string(emailBytes), string(passBytes), nil
}

// DeleteGarminCredentials removes the Garmin credential row for userID.
func (d *DB) DeleteGarminCredentials(userID int64) error {
	if _, err := d.db.Exec(`DELETE FROM garmin_credentials WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("database: delete garmin credentials for user %d: %w", userID, err)
	}
	return nil
}

// InvalidateGarminCredentials marks the Garmin credentials for userID as
// invalid and records an error message.
func (d *DB) InvalidateGarminCredentials(userID int64, errMsg string) error {
	_, err := d.db.Exec(`
		UPDATE garmin_credentials
		   SET is_valid = 0, last_error = ?, updated_at = datetime('now')
		 WHERE user_id = ?
	`, errMsg, userID)
	if err != nil {
		return fmt.Errorf("database: invalidate garmin credentials for user %d: %w", userID, err)
	}
	return nil
}

// RevalidateGarminCredentials clears the is_valid=0 flag (e.g. after MFA or
// a successful sync). The conditional WHERE makes it a no-op when already valid,
// so the engine can call it on every successful run without spurious writes.
func (d *DB) RevalidateGarminCredentials(userID int64) error {
	_, err := d.db.Exec(`
		UPDATE garmin_credentials
		   SET is_valid = 1, last_error = NULL, updated_at = datetime('now')
		 WHERE user_id = ? AND is_valid = 0
	`, userID)
	if err != nil {
		return fmt.Errorf("database: revalidate garmin credentials for user %d: %w", userID, err)
	}
	return nil
}

// GetGarminCredentialsValid returns is_valid for the user's Garmin row, or
// (false, nil) when no row exists.
func (d *DB) GetGarminCredentialsValid(userID int64) (bool, error) {
	var v int
	err := d.db.QueryRow(
		`SELECT is_valid FROM garmin_credentials WHERE user_id = ?`,
		userID,
	).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("database: query garmin is_valid for user %d: %w", userID, err)
	}
	return v == 1, nil
}

// ──────────────────────────────────────────────
// EFB credentials
// ──────────────────────────────────────────────

// SaveEFBCredentials encrypts username and password with the DB key and upserts
// a row in efb_credentials for userID.
func (d *DB) SaveEFBCredentials(userID int64, username, password string) error {
	encUser, err := crypto.Encrypt([]byte(username), d.encryptionKey)
	if err != nil {
		return fmt.Errorf("database: encrypt efb username: %w", err)
	}

	encPass, err := crypto.Encrypt([]byte(password), d.encryptionKey)
	if err != nil {
		return fmt.Errorf("database: encrypt efb password: %w", err)
	}

	_, err = d.db.Exec(`
		INSERT INTO efb_credentials (user_id, username_encrypted, password_encrypted, is_valid, last_error, updated_at)
		VALUES (?, ?, ?, 1, NULL, datetime('now'))
		ON CONFLICT(user_id) DO UPDATE SET
			username_encrypted = excluded.username_encrypted,
			password_encrypted = excluded.password_encrypted,
			is_valid           = 1,
			last_error         = NULL,
			updated_at         = datetime('now')
	`, userID, encUser, encPass)
	if err != nil {
		return fmt.Errorf("database: save efb credentials for user %d: %w", userID, err)
	}
	return nil
}

// GetEFBCredentials returns the decrypted username and password for userID.
func (d *DB) GetEFBCredentials(userID int64) (username, password string, err error) {
	var encUser, encPass []byte
	err = d.db.QueryRow(
		`SELECT username_encrypted, password_encrypted FROM efb_credentials WHERE user_id = ?`,
		userID,
	).Scan(&encUser, &encPass)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("database: no efb credentials for user %d", userID)
	}
	if err != nil {
		return "", "", fmt.Errorf("database: query efb credentials: %w", err)
	}

	userBytes, err := crypto.Decrypt(encUser, d.encryptionKey)
	if err != nil {
		return "", "", fmt.Errorf("database: decrypt efb username: %w", err)
	}

	passBytes, err := crypto.Decrypt(encPass, d.encryptionKey)
	if err != nil {
		return "", "", fmt.Errorf("database: decrypt efb password: %w", err)
	}

	return string(userBytes), string(passBytes), nil
}

// DeleteEFBCredentials removes the EFB credential row for userID.
func (d *DB) DeleteEFBCredentials(userID int64) error {
	if _, err := d.db.Exec(`DELETE FROM efb_credentials WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("database: delete efb credentials for user %d: %w", userID, err)
	}
	return nil
}

// MarkEFBConsentRequired sets consent_required=1 on the user's EFB row,
// indicating that uploads are blocked by the EFB v2026.1 track-usage
// consent gate until the user clicks "ich stimme zu" on the EFB portal.
// Idempotent: a no-op when the flag is already set.
func (d *DB) MarkEFBConsentRequired(userID int64) error {
	_, err := d.db.Exec(`
		UPDATE efb_credentials
		   SET consent_required = 1, updated_at = datetime('now')
		 WHERE user_id = ?
	`, userID)
	if err != nil {
		return fmt.Errorf("database: mark efb consent required for user %d: %w", userID, err)
	}
	return nil
}

// ClearEFBConsentRequired sets consent_required=0 on the user's EFB row.
// Called from the sync engine after a successful upload, so the dashboard
// banner disappears as soon as the user consents and a sync re-runs.
// Does not touch consent_notified_at — that timestamp persists across
// resolve cycles to keep the email rate limit honest.
func (d *DB) ClearEFBConsentRequired(userID int64) error {
	_, err := d.db.Exec(`
		UPDATE efb_credentials
		   SET consent_required = 0, updated_at = datetime('now')
		 WHERE user_id = ? AND consent_required = 1
	`, userID)
	if err != nil {
		return fmt.Errorf("database: clear efb consent required for user %d: %w", userID, err)
	}
	return nil
}

// GetEFBConsentState returns the user's consent flag and the timestamp
// of the last consent-required notification email (or nil if never sent).
// Returns (false, nil, nil) when the user has no efb_credentials row.
func (d *DB) GetEFBConsentState(userID int64) (required bool, notifiedAt *time.Time, err error) {
	var req int
	var notified *string
	err = d.db.QueryRow(`
		SELECT consent_required, consent_notified_at
		  FROM efb_credentials
		 WHERE user_id = ?
	`, userID).Scan(&req, &notified)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("database: get efb consent state for user %d: %w", userID, err)
	}
	if notified != nil {
		t, _ := parseTime(*notified)
		notifiedAt = &t
	}
	return req == 1, notifiedAt, nil
}

// RecordEFBConsentNotified bumps consent_notified_at to at, used by the
// sync engine to rate-limit consent-required emails (≤ once per 7 days).
func (d *DB) RecordEFBConsentNotified(userID int64, at time.Time) error {
	_, err := d.db.Exec(`
		UPDATE efb_credentials
		   SET consent_notified_at = ?, updated_at = datetime('now')
		 WHERE user_id = ?
	`, at.UTC().Format("2006-01-02 15:04:05"), userID)
	if err != nil {
		return fmt.Errorf("database: record efb consent notified for user %d: %w", userID, err)
	}
	return nil
}

// InvalidateEFBCredentials marks the EFB credentials for userID as invalid.
func (d *DB) InvalidateEFBCredentials(userID int64, errMsg string) error {
	_, err := d.db.Exec(`
		UPDATE efb_credentials
		   SET is_valid = 0, last_error = ?, updated_at = datetime('now')
		 WHERE user_id = ?
	`, errMsg, userID)
	if err != nil {
		return fmt.Errorf("database: invalidate efb credentials for user %d: %w", userID, err)
	}
	return nil
}

// RevalidateEFBCredentials clears the is_valid=0 flag after a successful
// EFB login. Conditional WHERE makes it a no-op when already valid, so the
// engine can call it on every successful login without spurious writes.
func (d *DB) RevalidateEFBCredentials(userID int64) error {
	_, err := d.db.Exec(`
		UPDATE efb_credentials
		   SET is_valid = 1, last_error = NULL, updated_at = datetime('now')
		 WHERE user_id = ? AND is_valid = 0
	`, userID)
	if err != nil {
		return fmt.Errorf("database: revalidate efb credentials for user %d: %w", userID, err)
	}
	return nil
}

// GetEFBCredentialsValid returns is_valid for the user's EFB row, or
// (false, nil) when no row exists.
func (d *DB) GetEFBCredentialsValid(userID int64) (bool, error) {
	var v int
	err := d.db.QueryRow(
		`SELECT is_valid FROM efb_credentials WHERE user_id = ?`,
		userID,
	).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("database: query efb is_valid for user %d: %w", userID, err)
	}
	return v == 1, nil
}

// SaveEFBSession encrypts cookie and stores it alongside expiresAt for userID.
// The efb_credentials row must already exist (created via SaveEFBCredentials).
func (d *DB) SaveEFBSession(userID int64, cookie []byte, expiresAt time.Time) error {
	encCookie, err := crypto.Encrypt(cookie, d.encryptionKey)
	if err != nil {
		return fmt.Errorf("database: encrypt efb session cookie: %w", err)
	}

	_, err = d.db.Exec(`
		UPDATE efb_credentials
		   SET session_cookie     = ?,
		       session_expires_at = ?,
		       updated_at         = datetime('now')
		 WHERE user_id = ?
	`, encCookie, expiresAt.UTC().Format("2006-01-02 15:04:05"), userID)
	if err != nil {
		return fmt.Errorf("database: save efb session for user %d: %w", userID, err)
	}
	return nil
}

// GetEFBSession returns the decrypted session cookie and its expiry for userID.
func (d *DB) GetEFBSession(userID int64) (cookie []byte, expiresAt time.Time, err error) {
	var encCookie []byte
	var expiresAtStr sql.NullString

	err = d.db.QueryRow(
		`SELECT session_cookie, session_expires_at FROM efb_credentials WHERE user_id = ?`,
		userID,
	).Scan(&encCookie, &expiresAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, fmt.Errorf("database: no efb credentials for user %d", userID)
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("database: query efb session: %w", err)
	}

	if len(encCookie) == 0 {
		return nil, time.Time{}, nil
	}

	cookie, err = crypto.Decrypt(encCookie, d.encryptionKey)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("database: decrypt efb session cookie: %w", err)
	}

	if expiresAtStr.Valid {
		expiresAt, _ = parseTime(expiresAtStr.String)
	}

	return cookie, expiresAt, nil
}
