// Package testutil contains shared helpers for tests across packages.
//
// Importing this package from non-test code is unsupported.
package testutil

import (
	"testing"
	"time"

	"efb-connector/internal/database"
)

// TestKey is the canonical 32-byte AES-256 key used in unit and integration
// tests. Treating it as a constant lets tests share encrypted state — e.g.
// integration tests that round-trip credentials through the database.
var TestKey = []byte("12345678901234567890123456789012")

// OpenTestDB opens an in-memory SQLite database with all migrations applied
// and registers a Cleanup to close it when the test ends.
func OpenTestDB(t testing.TB) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:", TestKey)
	if err != nil {
		t.Fatalf("testutil: open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// WaitFor polls cond every interval until it returns true or the deadline
// elapses. Calls t.Fatalf with msg if the condition never holds. Use this
// instead of bare time.Sleep loops when waiting for asynchronous state to
// settle in tests — the test fails fast on regressions and finishes as
// soon as the condition is met.
func WaitFor(t testing.TB, timeout, interval time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("WaitFor: %s (timeout after %s)", msg, timeout)
		}
		time.Sleep(interval)
	}
}
