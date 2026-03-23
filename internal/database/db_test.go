package database

import (
	"testing"
	"time"
)

// testKey is a 32-byte AES key used in all tests.
var testKey = []byte("12345678901234567890123456789012")

// openTestDB opens an in-memory SQLite database with migrations applied and
// returns it together with a cleanup function.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:", testKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ──────────────────────────────────────────────
// Migration tests
// ──────────────────────────────────────────────

func TestOpen_MigrationsRun(t *testing.T) {
	db := openTestDB(t)

	// Verify all expected tables exist.
	tables := []string{
		"users", "magic_links", "sessions",
		"garmin_credentials", "efb_credentials",
		"synced_activities", "sync_runs", "migrations",
	}
	for _, tbl := range tables {
		var count int
		err := db.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&count)
		if err != nil || count == 0 {
			t.Errorf("expected table %q to exist", tbl)
		}
	}
}

func TestOpen_MigrationsIdempotent(t *testing.T) {
	db := openTestDB(t)

	// Running migrations again on the same DB should be a no-op.
	if err := db.runMigrations(); err != nil {
		t.Fatalf("second runMigrations: %v", err)
	}
}

func TestOpen_InvalidKeyLength(t *testing.T) {
	_, err := Open(":memory:", []byte("tooshort"))
	if err == nil {
		t.Fatal("expected error for short key, got nil")
	}
}

// ──────────────────────────────────────────────
// User tests
// ──────────────────────────────────────────────

func TestCreateUser(t *testing.T) {
	db := openTestDB(t)

	u, err := db.CreateUser("alice@example.com")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if u.Email != "alice@example.com" {
		t.Errorf("email = %q", u.Email)
	}
	if !u.IsActive {
		t.Error("expected is_active = true by default")
	}
	if !u.SyncEnabled {
		t.Error("expected sync_enabled = true by default")
	}
	if u.SyncDays != 3 {
		t.Errorf("sync_days = %d, want 3", u.SyncDays)
	}
	if u.AutoCreateTrips {
		t.Error("expected auto_create_trips = false by default")
	}
}

func TestCreateUser_DuplicateEmail(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.CreateUser("dup@example.com"); err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}
	if _, err := db.CreateUser("dup@example.com"); err == nil {
		t.Fatal("expected error on duplicate email, got nil")
	}
}

func TestUser_AutoCreateTrips_ReadWrite(t *testing.T) {
	db := openTestDB(t)

	u, err := db.CreateUser("autotrips@example.com")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.AutoCreateTrips {
		t.Fatal("expected AutoCreateTrips = false after creation")
	}

	// Enable auto_create_trips via direct SQL UPDATE.
	if _, err := db.db.Exec(`UPDATE users SET auto_create_trips = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatalf("UPDATE auto_create_trips: %v", err)
	}

	// Re-read the user and verify the field is now true.
	got, err := db.GetUserByID(u.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if !got.AutoCreateTrips {
		t.Error("expected AutoCreateTrips = true after UPDATE")
	}
}

func TestGetUserByEmail_NotFound(t *testing.T) {
	db := openTestDB(t)

	u, err := db.GetUserByEmail("nobody@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if u != nil {
		t.Errorf("expected nil user, got %+v", u)
	}
}

func TestGetUserByEmail_Found(t *testing.T) {
	db := openTestDB(t)

	want, _ := db.CreateUser("bob@example.com")
	got, err := db.GetUserByEmail("bob@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.ID != want.ID {
		t.Errorf("ID = %d, want %d", got.ID, want.ID)
	}
}

func TestGetUserByID_NotFound(t *testing.T) {
	db := openTestDB(t)

	u, err := db.GetUserByID(9999)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if u != nil {
		t.Errorf("expected nil, got %+v", u)
	}
}

func TestDeleteUser(t *testing.T) {
	db := openTestDB(t)

	u, _ := db.CreateUser("todelete@example.com")
	if err := db.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	got, _ := db.GetUserByID(u.ID)
	if got != nil {
		t.Error("user should be gone after delete")
	}
}

func TestGetSyncableUsers(t *testing.T) {
	db := openTestDB(t)

	// Create two users; only one gets both valid credentials.
	u1, _ := db.CreateUser("sync1@example.com")
	u2, _ := db.CreateUser("sync2@example.com")

	// u1 gets both credential sets.
	_ = db.SaveGarminCredentials(u1.ID, "g@garmin.com", "gpass")
	_ = db.SaveEFBCredentials(u1.ID, "efbuser", "efbpass")

	// u2 gets only Garmin credentials.
	_ = db.SaveGarminCredentials(u2.ID, "g2@garmin.com", "gpass2")

	users, err := db.GetSyncableUsers()
	if err != nil {
		t.Fatalf("GetSyncableUsers: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 syncable user, got %d", len(users))
	}
	if users[0].ID != u1.ID {
		t.Errorf("wrong user returned: %d", users[0].ID)
	}
}

// ──────────────────────────────────────────────
// Credential tests
// ──────────────────────────────────────────────

func TestGarminCredentials_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("garmin@example.com")

	const wantEmail = "me@garmin.com"
	const wantPass = "s3cr3t"

	if err := db.SaveGarminCredentials(u.ID, wantEmail, wantPass); err != nil {
		t.Fatalf("SaveGarminCredentials: %v", err)
	}

	email, pass, err := db.GetGarminCredentials(u.ID)
	if err != nil {
		t.Fatalf("GetGarminCredentials: %v", err)
	}
	if email != wantEmail {
		t.Errorf("email = %q, want %q", email, wantEmail)
	}
	if pass != wantPass {
		t.Errorf("password mismatch")
	}
}

func TestGarminCredentials_Upsert(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("garmin_upsert@example.com")

	_ = db.SaveGarminCredentials(u.ID, "old@g.com", "old")
	_ = db.SaveGarminCredentials(u.ID, "new@g.com", "new")

	email, pass, err := db.GetGarminCredentials(u.ID)
	if err != nil {
		t.Fatalf("GetGarminCredentials: %v", err)
	}
	if email != "new@g.com" || pass != "new" {
		t.Errorf("upsert did not update: email=%q pass=%q", email, pass)
	}
}

func TestGarminCredentials_Delete(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("garmin_del@example.com")

	_ = db.SaveGarminCredentials(u.ID, "e@g.com", "p")
	if err := db.DeleteGarminCredentials(u.ID); err != nil {
		t.Fatalf("DeleteGarminCredentials: %v", err)
	}

	_, _, err := db.GetGarminCredentials(u.ID)
	if err == nil {
		t.Error("expected error after delete, got nil")
	}
}

func TestGarminCredentials_Invalidate(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("garmin_inv@example.com")

	_ = db.SaveGarminCredentials(u.ID, "e@g.com", "p")
	if err := db.InvalidateGarminCredentials(u.ID, "bad password"); err != nil {
		t.Fatalf("InvalidateGarminCredentials: %v", err)
	}

	users, _ := db.GetSyncableUsers()
	for _, su := range users {
		if su.ID == u.ID {
			t.Error("invalidated user should not appear in syncable users")
		}
	}
}

func TestEFBCredentials_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("efb@example.com")

	if err := db.SaveEFBCredentials(u.ID, "efbuser", "efbpass"); err != nil {
		t.Fatalf("SaveEFBCredentials: %v", err)
	}

	uname, pass, err := db.GetEFBCredentials(u.ID)
	if err != nil {
		t.Fatalf("GetEFBCredentials: %v", err)
	}
	if uname != "efbuser" || pass != "efbpass" {
		t.Errorf("got uname=%q pass=%q", uname, pass)
	}
}

func TestEFBCredentials_Delete(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("efb_del@example.com")

	_ = db.SaveEFBCredentials(u.ID, "u", "p")
	if err := db.DeleteEFBCredentials(u.ID); err != nil {
		t.Fatalf("DeleteEFBCredentials: %v", err)
	}

	_, _, err := db.GetEFBCredentials(u.ID)
	if err == nil {
		t.Error("expected error after delete, got nil")
	}
}

func TestEFBCredentials_Invalidate(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("efb_inv@example.com")

	_ = db.SaveEFBCredentials(u.ID, "u", "p")
	if err := db.InvalidateEFBCredentials(u.ID, "bad creds"); err != nil {
		t.Fatalf("InvalidateEFBCredentials: %v", err)
	}

	users, _ := db.GetSyncableUsers()
	for _, su := range users {
		if su.ID == u.ID {
			t.Error("invalidated efb user should not appear in syncable users")
		}
	}
}

func TestEFBSession_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("session@example.com")
	_ = db.SaveEFBCredentials(u.ID, "u", "p")

	cookie := []byte("session-cookie-value")
	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second)

	if err := db.SaveEFBSession(u.ID, cookie, expiresAt); err != nil {
		t.Fatalf("SaveEFBSession: %v", err)
	}

	gotCookie, gotExpiry, err := db.GetEFBSession(u.ID)
	if err != nil {
		t.Fatalf("GetEFBSession: %v", err)
	}
	if string(gotCookie) != string(cookie) {
		t.Errorf("cookie = %q, want %q", gotCookie, cookie)
	}
	// Allow 1-second tolerance for time round-trip through SQLite text format.
	if diff := gotExpiry.Sub(expiresAt); diff < -time.Second || diff > time.Second {
		t.Errorf("expiresAt diff = %v", diff)
	}
}

// ──────────────────────────────────────────────
// Activity tests
// ──────────────────────────────────────────────

func TestRecordActivity_InsertAndQuery(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("act@example.com")

	err := db.RecordActivity(u.ID, "act-1", "Morning Run", "running", "2024-01-01", "success", "")
	if err != nil {
		t.Fatalf("RecordActivity: %v", err)
	}

	synced, err := db.IsActivitySynced(u.ID, "act-1")
	if err != nil {
		t.Fatalf("IsActivitySynced: %v", err)
	}
	if !synced {
		t.Error("expected activity to be synced")
	}
}

func TestRecordActivity_Upsert(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("act_ups@example.com")

	_ = db.RecordActivity(u.ID, "act-1", "Old Name", "running", "2024-01-01", "failed", "err")
	_ = db.RecordActivity(u.ID, "act-1", "New Name", "running", "2024-01-01", "success", "")

	synced, _ := db.IsActivitySynced(u.ID, "act-1")
	if !synced {
		t.Error("expected success after upsert")
	}
}

func TestIsActivitySynced_False(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("act_ns@example.com")

	synced, err := db.IsActivitySynced(u.ID, "nonexistent")
	if err != nil {
		t.Fatalf("IsActivitySynced: %v", err)
	}
	if synced {
		t.Error("expected false for nonexistent activity")
	}
}

func TestGetFailedActivities(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("failed@example.com")

	_ = db.RecordActivity(u.ID, "act-1", "A", "run", "2024-01-01", "failed", "oops")
	_ = db.RecordActivity(u.ID, "act-2", "B", "run", "2024-01-02", "success", "")
	_ = db.RecordActivity(u.ID, "act-3", "C", "run", "2024-01-03", "failed", "nope")

	failed, err := db.GetFailedActivities(u.ID)
	if err != nil {
		t.Fatalf("GetFailedActivities: %v", err)
	}
	if len(failed) != 2 {
		t.Fatalf("expected 2 failed activities, got %d", len(failed))
	}
}

func TestIncrementRetryCount(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("retry@example.com")

	_ = db.RecordActivity(u.ID, "act-1", "A", "run", "2024-01-01", "failed", "err")
	_ = db.IncrementRetryCount(u.ID, "act-1")
	_ = db.IncrementRetryCount(u.ID, "act-1")
	_ = db.IncrementRetryCount(u.ID, "act-1") // retry_count = 3 → filtered out

	failed, _ := db.GetFailedActivities(u.ID)
	if len(failed) != 0 {
		t.Errorf("expected 0 results after retry_count>=3, got %d", len(failed))
	}
}

func TestMarkPermanentFailure(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("permfail@example.com")

	_ = db.RecordActivity(u.ID, "act-1", "A", "run", "2024-01-01", "failed", "err")
	if err := db.MarkPermanentFailure(u.ID, "act-1"); err != nil {
		t.Fatalf("MarkPermanentFailure: %v", err)
	}

	failed, _ := db.GetFailedActivities(u.ID)
	if len(failed) != 0 {
		t.Error("permanent failure should not appear in failed list")
	}
}

// ──────────────────────────────────────────────
// Sync run tests
// ──────────────────────────────────────────────

func TestCreateAndGetSyncRun(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("syncrun@example.com")

	id, err := db.CreateSyncRun(u.ID, "scheduled")
	if err != nil {
		t.Fatalf("CreateSyncRun: %v", err)
	}

	run, err := db.GetSyncRun(id)
	if err != nil {
		t.Fatalf("GetSyncRun: %v", err)
	}
	if run == nil {
		t.Fatal("expected run, got nil")
	}
	if run.Status != "running" {
		t.Errorf("status = %q, want running", run.Status)
	}
	if run.Trigger != "scheduled" {
		t.Errorf("trigger = %q, want scheduled", run.Trigger)
	}
}

func TestUpdateSyncRun(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("syncupdate@example.com")

	id, _ := db.CreateSyncRun(u.ID, "manual")
	err := db.UpdateSyncRun(id, "completed", 10, 8, 1, 1, "")
	if err != nil {
		t.Fatalf("UpdateSyncRun: %v", err)
	}

	run, _ := db.GetSyncRun(id)
	if run.Status != "completed" {
		t.Errorf("status = %q, want completed", run.Status)
	}
	if run.ActivitiesFound != 10 {
		t.Errorf("found = %d, want 10", run.ActivitiesFound)
	}
	if run.FinishedAt == nil {
		t.Error("finished_at should be set")
	}
}

func TestGetSyncHistory(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("synchistory@example.com")

	for i := 0; i < 5; i++ {
		db.CreateSyncRun(u.ID, "scheduled")
	}

	history, err := db.GetSyncHistory(u.ID, 3)
	if err != nil {
		t.Fatalf("GetSyncHistory: %v", err)
	}
	if len(history) != 3 {
		t.Errorf("expected 3 runs, got %d", len(history))
	}
}

func TestGetSyncRun_NotFound(t *testing.T) {
	db := openTestDB(t)

	run, err := db.GetSyncRun(9999)
	if err != nil {
		t.Fatalf("GetSyncRun: %v", err)
	}
	if run != nil {
		t.Error("expected nil for non-existent run")
	}
}

// ──────────────────────────────────────────────
// Session / magic link tests
// ──────────────────────────────────────────────

func TestMagicLink_ValidateSuccess(t *testing.T) {
	db := openTestDB(t)

	email := "magic@example.com"
	tokenHash := "abc123hash"
	expiresAt := time.Now().Add(15 * time.Minute)

	if err := db.CreateMagicLink(email, tokenHash, expiresAt); err != nil {
		t.Fatalf("CreateMagicLink: %v", err)
	}

	got, err := db.ValidateMagicLink(tokenHash)
	if err != nil {
		t.Fatalf("ValidateMagicLink: %v", err)
	}
	if got != email {
		t.Errorf("email = %q, want %q", got, email)
	}
}

func TestMagicLink_UsedTwice(t *testing.T) {
	db := openTestDB(t)

	_ = db.CreateMagicLink("m@e.com", "hash1", time.Now().Add(time.Hour))
	_, _ = db.ValidateMagicLink("hash1")

	_, err := db.ValidateMagicLink("hash1")
	if err == nil {
		t.Error("expected error on second use, got nil")
	}
}

func TestMagicLink_Expired(t *testing.T) {
	db := openTestDB(t)

	_ = db.CreateMagicLink("m@e.com", "expiredhash", time.Now().Add(-time.Minute))

	_, err := db.ValidateMagicLink("expiredhash")
	if err == nil {
		t.Error("expected error for expired link, got nil")
	}
}

func TestMagicLink_NotFound(t *testing.T) {
	db := openTestDB(t)

	_, err := db.ValidateMagicLink("notexist")
	if err == nil {
		t.Error("expected error for missing link, got nil")
	}
}

func TestSession_CreateAndGet(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("sess@example.com")

	tokenHash := "session-token-hash"
	expiresAt := time.Now().Add(24 * time.Hour)

	if err := db.CreateSession(u.ID, tokenHash, expiresAt); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	uid, err := db.GetSession(tokenHash)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if uid != u.ID {
		t.Errorf("userID = %d, want %d", uid, u.ID)
	}
}

func TestSession_Expired(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("sess_exp@example.com")

	_ = db.CreateSession(u.ID, "expsess", time.Now().Add(-time.Second))

	_, err := db.GetSession("expsess")
	if err == nil {
		t.Error("expected error for expired session, got nil")
	}
}

func TestSession_NotFound(t *testing.T) {
	db := openTestDB(t)

	_, err := db.GetSession("ghost-token")
	if err == nil {
		t.Error("expected error for missing session, got nil")
	}
}

func TestSession_Delete(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("sess_del@example.com")

	_ = db.CreateSession(u.ID, "deltok", time.Now().Add(time.Hour))
	if err := db.DeleteSession("deltok"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	_, err := db.GetSession("deltok")
	if err == nil {
		t.Error("expected error after delete, got nil")
	}
}

func TestCleanupExpired(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("cleanup@example.com")

	// Insert one expired and one valid magic link.
	_ = db.CreateMagicLink("cleanup@example.com", "expired-ml", time.Now().Add(-time.Minute))
	_ = db.CreateMagicLink("cleanup@example.com", "valid-ml", time.Now().Add(time.Hour))

	// Insert one expired and one valid session.
	_ = db.CreateSession(u.ID, "expired-sess", time.Now().Add(-time.Minute))
	_ = db.CreateSession(u.ID, "valid-sess", time.Now().Add(time.Hour))

	if err := db.CleanupExpired(); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}

	// Expired entries should be gone.
	_, err := db.ValidateMagicLink("expired-ml")
	if err == nil {
		t.Error("expired magic link should have been cleaned up")
	}

	_, err = db.GetSession("expired-sess")
	if err == nil {
		t.Error("expired session should have been cleaned up")
	}

	// Valid entries should survive.
	email, err := db.ValidateMagicLink("valid-ml")
	if err != nil {
		t.Errorf("valid magic link should survive cleanup: %v", err)
	}
	if email != "cleanup@example.com" {
		t.Errorf("magic link email = %q", email)
	}
}

// ──────────────────────────────────────────────
// Cascade delete test
// ──────────────────────────────────────────────

func TestDeleteUser_Cascade(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("cascade@example.com")

	_ = db.SaveGarminCredentials(u.ID, "g@g.com", "p")
	_ = db.SaveEFBCredentials(u.ID, "efbu", "efbp")
	_ = db.RecordActivity(u.ID, "act-1", "A", "run", "2024-01-01", "success", "")
	runID, _ := db.CreateSyncRun(u.ID, "scheduled")
	_ = db.CreateSession(u.ID, "cascade-tok", time.Now().Add(time.Hour))

	if err := db.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	// Verify all child rows are gone.
	tables := []struct {
		table  string
		column string
	}{
		{"garmin_credentials", "user_id"},
		{"efb_credentials", "user_id"},
		{"synced_activities", "user_id"},
		{"sync_runs", "user_id"},
		{"sessions", "user_id"},
	}

	for _, tc := range tables {
		var count int
		db.db.QueryRow(
			`SELECT COUNT(*) FROM `+tc.table+` WHERE `+tc.column+` = ?`, u.ID,
		).Scan(&count)
		if count != 0 {
			t.Errorf("expected 0 rows in %s after user delete, got %d", tc.table, count)
		}
	}

	_ = runID // used above
}
