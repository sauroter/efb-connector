package database

import (
	"path/filepath"
	"testing"
)

func TestGetSystemStats(t *testing.T) {
	db := openTestDB(t)

	// No users initially.
	stats, err := db.GetSystemStats()
	if err != nil {
		t.Fatalf("GetSystemStats: %v", err)
	}
	if stats.TotalUsers != 0 || stats.ActiveUsers != 0 || stats.SyncableUsers != 0 {
		t.Errorf("expected zeroed stats, got %+v", stats)
	}

	// One fully syncable user, one inactive user.
	u1, _ := db.CreateUser("a@example.com")
	_ = db.SaveGarminCredentials(u1.ID, "g@g.com", "p")
	_ = db.SaveEFBCredentials(u1.ID, "u", "p")

	u2, _ := db.CreateUser("b@example.com")
	if _, err := db.db.Exec(`UPDATE users SET is_active = 0 WHERE id = ?`, u2.ID); err != nil {
		t.Fatalf("deactivate user: %v", err)
	}

	stats, err = db.GetSystemStats()
	if err != nil {
		t.Fatalf("GetSystemStats: %v", err)
	}
	if stats.TotalUsers != 2 {
		t.Errorf("TotalUsers = %d, want 2", stats.TotalUsers)
	}
	if stats.ActiveUsers != 1 {
		t.Errorf("ActiveUsers = %d, want 1", stats.ActiveUsers)
	}
	if stats.SyncableUsers != 1 {
		t.Errorf("SyncableUsers = %d, want 1", stats.SyncableUsers)
	}
}

// TestGetSystemStats_FileBackedDBPopulatesPath covers the PRAGMA
// database_list / os.Stat branch in GetSystemStats, which is silently
// skipped for :memory: DBs (empty file column). A regression that broke
// the PRAGMA scan column index or the Stat call would otherwise slip
// through every other test in this file.
func TestGetSystemStats_FileBackedDBPopulatesPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats.db")
	db, err := Open(path, testKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.CreateUser("seed@example.com"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	stats, err := db.GetSystemStats()
	if err != nil {
		t.Fatalf("GetSystemStats: %v", err)
	}
	if stats.DBPath == "" {
		t.Error("DBPath should be populated for a file-backed DB")
	}
	if stats.DBSizeBytes == 0 {
		t.Error("DBSizeBytes should be >0 after running migrations + inserting a user")
	}
}

func TestGetAllUsersWithStatus(t *testing.T) {
	db := openTestDB(t)

	u1, _ := db.CreateUser("alice@example.com")
	_ = db.SaveGarminCredentials(u1.ID, "g@g.com", "p")
	_ = db.SaveEFBCredentials(u1.ID, "u", "p")
	_, _ = db.CreateSyncRun(u1.ID, "scheduled")

	u2, _ := db.CreateUser("bob@example.com")
	_ = db.SaveGarminCredentials(u2.ID, "g@g.com", "p")
	_ = db.InvalidateGarminCredentials(u2.ID, "bad")

	users, err := db.GetAllUsersWithStatus()
	if err != nil {
		t.Fatalf("GetAllUsersWithStatus: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}

	byEmail := map[string]UserStatus{}
	for _, u := range users {
		byEmail[u.Email] = u
	}

	alice := byEmail["alice@example.com"]
	if !alice.GarminConnected || !alice.GarminValid {
		t.Error("alice should have garmin connected & valid")
	}
	if !alice.EFBConnected || !alice.EFBValid {
		t.Error("alice should have efb connected & valid")
	}
	if alice.LastSyncAt == nil {
		t.Error("alice should have last_sync_at set")
	}

	bob := byEmail["bob@example.com"]
	if !bob.GarminConnected || bob.GarminValid {
		t.Error("bob garmin should be connected but invalid")
	}
	if bob.EFBConnected {
		t.Error("bob should not have efb connected")
	}
}

func TestGetRecentFailedSyncRuns(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("x@example.com")

	successID, _ := db.CreateSyncRun(u.ID, "scheduled")
	_ = db.UpdateSyncRun(successID, "completed", 1, 1, 0, 0, 0, "")

	failedID, _ := db.CreateSyncRun(u.ID, "scheduled")
	_ = db.UpdateSyncRun(failedID, "failed", 1, 0, 0, 1, 0, "boom")

	partialID, _ := db.CreateSyncRun(u.ID, "manual")
	_ = db.UpdateSyncRun(partialID, "partial", 2, 1, 0, 1, 0, "one failed")

	runs, err := db.GetRecentFailedSyncRuns(10)
	if err != nil {
		t.Fatalf("GetRecentFailedSyncRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 failed/partial runs, got %d", len(runs))
	}
	for _, r := range runs {
		if r.Status != "failed" && r.Status != "partial" {
			t.Errorf("unexpected status %q included", r.Status)
		}
	}
}
