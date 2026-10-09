// Package database provides the SQLite-backed persistence layer for efb-connector.
// It uses modernc.org/sqlite (pure Go, no CGO required).
package database

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // register "sqlite" driver
)

// DB wraps a *sql.DB together with the AES-256 encryption key used to protect
// credential fields at rest.
type DB struct {
	db            *sql.DB
	encryptionKey []byte
}

// Open opens (or creates) the SQLite database at path, applies any pending
// migrations, enables WAL journal mode and enforces foreign keys, then returns
// a ready-to-use *DB.
//
// encryptionKey must be exactly 32 bytes (AES-256); it is used by the
// credential helpers to encrypt/decrypt sensitive fields.
func Open(path string, encryptionKey []byte) (*DB, error) {
	if len(encryptionKey) != 32 {
		return nil, fmt.Errorf("database: encryption key must be 32 bytes, got %d", len(encryptionKey))
	}

	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("database: open %q: %w", path, err)
	}

	// SQLite works best with a single writer connection.
	sqlDB.SetMaxOpenConns(1)

	// Enable WAL mode for better concurrent read performance and durability.
	if _, err := sqlDB.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("database: enable WAL: %w", err)
	}

	// Enforce foreign-key constraints (off by default in SQLite).
	if _, err := sqlDB.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("database: enable foreign keys: %w", err)
	}

	d := &DB{db: sqlDB, encryptionKey: encryptionKey}

	if err := d.runMigrations(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	return d, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

// Ping verifies the database connection is alive.
func (d *DB) Ping() error {
	return d.db.Ping()
}

// runMigrations creates the migrations tracking table (if absent) and applies
// any migrations not yet recorded there.
func (d *DB) runMigrations() error {
	// Ensure the migrations bookkeeping table exists.
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		id         INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		return fmt.Errorf("database: create migrations table: %w", err)
	}

	// Find highest applied migration index.
	var maxID sql.NullInt64
	if err := d.db.QueryRow(`SELECT MAX(id) FROM migrations`).Scan(&maxID); err != nil {
		return fmt.Errorf("database: query migrations: %w", err)
	}

	startIdx := 0
	if maxID.Valid {
		startIdx = int(maxID.Int64) + 1
	}

	for i := startIdx; i < len(migrations); i++ {
		if err := d.applyMigration(i, migrations[i]); err != nil {
			return fmt.Errorf("database: migration %d: %w", i, err)
		}
	}

	return nil
}

// applyMigration splits sql on ";\n" boundaries, executes each non-empty
// statement and records id in the migrations table, all in one transaction.
// Either the schema change and its bookkeeping row both land or neither
// does: recording outside the transaction would let a crash between the two
// re-run an applied migration on next start, and a repeated ALTER TABLE ADD
// COLUMN fails, so the server would refuse to boot. SQLite supports DDL
// inside transactions.
func (d *DB) applyMigration(id int, sql string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmts := strings.Split(sql, ";\n")
	for _, stmt := range stmts {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:min(40, len(stmt))], err)
		}
	}

	if _, err := tx.Exec(`INSERT INTO migrations (id) VALUES (?)`, id); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}
