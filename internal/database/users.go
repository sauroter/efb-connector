package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// User mirrors the users table.
type User struct {
	ID          int64
	Email       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	IsActive    bool
	SyncEnabled bool
	SyncDays    int
}

// CreateUser inserts a new user row and returns the fully-populated struct.
func (d *DB) CreateUser(email string) (*User, error) {
	res, err := d.db.Exec(
		`INSERT INTO users (email) VALUES (?)`,
		email,
	)
	if err != nil {
		return nil, fmt.Errorf("database: create user %q: %w", email, err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("database: get insert id: %w", err)
	}

	return d.GetUserByID(id)
}

// GetUserByEmail returns the user with the given email, or nil (no error) when
// no such row exists.
func (d *DB) GetUserByEmail(email string) (*User, error) {
	u, err := d.scanUser(d.db.QueryRow(
		`SELECT id, email, created_at, updated_at, is_active, sync_enabled, sync_days
		   FROM users WHERE email = ?`, email,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// GetUserByID returns the user with the given id, or nil (no error) when not
// found.
func (d *DB) GetUserByID(id int64) (*User, error) {
	u, err := d.scanUser(d.db.QueryRow(
		`SELECT id, email, created_at, updated_at, is_active, sync_enabled, sync_days
		   FROM users WHERE id = ?`, id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// DeleteUser removes the user and all cascaded rows (credentials, activities,
// sessions, sync_runs).
func (d *DB) DeleteUser(id int64) error {
	if _, err := d.db.Exec(`DELETE FROM users WHERE id = ?`, id); err != nil {
		return fmt.Errorf("database: delete user %d: %w", id, err)
	}
	return nil
}

// GetSyncableUsers returns users that are active, have sync enabled, and have
// both Garmin and EFB credentials marked as valid.
func (d *DB) GetSyncableUsers() ([]User, error) {
	rows, err := d.db.Query(`
		SELECT u.id, u.email, u.created_at, u.updated_at, u.is_active, u.sync_enabled, u.sync_days
		  FROM users u
		  JOIN garmin_credentials gc ON gc.user_id = u.id AND gc.is_valid = 1
		  JOIN efb_credentials    ec ON ec.user_id = u.id AND ec.is_valid = 1
		 WHERE u.is_active = 1 AND u.sync_enabled = 1
	`)
	if err != nil {
		return nil, fmt.Errorf("database: get syncable users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		u, err := d.scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// scanUser scans a *sql.Row into a User.
func (d *DB) scanUser(row *sql.Row) (*User, error) {
	var u User
	var createdAt, updatedAt string
	var isActive, syncEnabled int

	err := row.Scan(
		&u.ID, &u.Email, &createdAt, &updatedAt,
		&isActive, &syncEnabled, &u.SyncDays,
	)
	if err != nil {
		return nil, err
	}

	u.CreatedAt, _ = parseTime(createdAt)
	u.UpdatedAt, _ = parseTime(updatedAt)
	u.IsActive = isActive != 0
	u.SyncEnabled = syncEnabled != 0
	return &u, nil
}

// scanUserRow scans a *sql.Rows (plural) into a User.
func (d *DB) scanUserRow(rows *sql.Rows) (*User, error) {
	var u User
	var createdAt, updatedAt string
	var isActive, syncEnabled int

	err := rows.Scan(
		&u.ID, &u.Email, &createdAt, &updatedAt,
		&isActive, &syncEnabled, &u.SyncDays,
	)
	if err != nil {
		return nil, fmt.Errorf("database: scan user: %w", err)
	}

	u.CreatedAt, _ = parseTime(createdAt)
	u.UpdatedAt, _ = parseTime(updatedAt)
	u.IsActive = isActive != 0
	u.SyncEnabled = syncEnabled != 0
	return &u, nil
}

// parseTime attempts to parse an SQLite datetime string.
func parseTime(s string) (time.Time, error) {
	layouts := []string{
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		time.RFC3339,
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("database: cannot parse time %q", s)
}
