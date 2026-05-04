package database

import (
	"testing"
)

// seedReportFixtures creates a known set of users and sync_runs covering
// every per-user state classified by the admin-report queries:
//
//	alice  – fully connected, recent successful sync
//	bob    – fully connected, last success > 7d ago (stuck)
//	carol  – fully connected, never had a successful sync (stuck)
//	dave   – fully connected, latest run failed (failing, but not stuck)
//	eve    – garmin invalid (disconnected)
//	frank  – signed up, no credentials
func seedReportFixtures(t *testing.T, db *DB) map[string]int64 {
	t.Helper()
	ids := map[string]int64{}

	for _, email := range []string{"alice", "bob", "carol", "dave", "eve", "frank"} {
		u, err := db.CreateUser(email + "@example.com")
		if err != nil {
			t.Fatalf("seed CreateUser %s: %v", email, err)
		}
		ids[email] = u.ID
	}

	// Credentials: alice/bob/carol/dave fully valid, eve invalid garmin, frank none.
	for _, name := range []string{"alice", "bob", "carol", "dave"} {
		if err := db.SaveGarminCredentials(ids[name], name+"@garmin", "pw"); err != nil {
			t.Fatalf("seed garmin creds %s: %v", name, err)
		}
		if err := db.SaveEFBCredentials(ids[name], name+"@efb", "pw"); err != nil {
			t.Fatalf("seed efb creds %s: %v", name, err)
		}
	}
	if err := db.SaveGarminCredentials(ids["eve"], "eve@garmin", "pw"); err != nil {
		t.Fatalf("seed eve garmin: %v", err)
	}
	if err := db.SaveEFBCredentials(ids["eve"], "eve@efb", "pw"); err != nil {
		t.Fatalf("seed eve efb: %v", err)
	}
	if err := db.InvalidateGarminCredentials(ids["eve"], "bad password"); err != nil {
		t.Fatalf("seed eve invalidate: %v", err)
	}

	// Helper: insert a sync_run with absolute started_at/finished_at.
	insertRun := func(userID int64, startedAt, finishedAt, status string) {
		t.Helper()
		_, err := db.db.Exec(`
			INSERT INTO sync_runs (user_id, trigger, started_at, finished_at, status)
			VALUES (?, 'scheduled', ?, ?, ?)
		`, userID, startedAt, finishedAt, status)
		if err != nil {
			t.Fatalf("insert sync_run: %v", err)
		}
	}

	// alice: fresh successful sync (1 hour ago).
	insertRun(ids["alice"], "datetime('now','-1 hour')", "", "")
	// Need to use the literal expression via separate statements:
	_, err := db.db.Exec(`
		UPDATE sync_runs
		   SET started_at = datetime('now','-1 hour'),
		       finished_at = datetime('now','-1 hour'),
		       status = 'completed'
		 WHERE user_id = ? AND status = ''
	`, ids["alice"])
	if err != nil {
		t.Fatalf("update alice: %v", err)
	}

	// bob: last completed sync 10 days ago, plus a failed run yesterday.
	_, err = db.db.Exec(`
		INSERT INTO sync_runs (user_id, trigger, started_at, finished_at, status)
		VALUES (?, 'scheduled', datetime('now','-10 days'), datetime('now','-10 days'), 'completed'),
		       (?, 'scheduled', datetime('now','-1 day'),   datetime('now','-1 day'),   'failed')
	`, ids["bob"], ids["bob"])
	if err != nil {
		t.Fatalf("insert bob runs: %v", err)
	}

	// carol: only failed runs, never a completion.
	_, err = db.db.Exec(`
		INSERT INTO sync_runs (user_id, trigger, started_at, finished_at, status, error_message)
		VALUES (?, 'scheduled', datetime('now','-2 days'), datetime('now','-2 days'), 'failed', 'login refused')
	`, ids["carol"])
	if err != nil {
		t.Fatalf("insert carol runs: %v", err)
	}

	// dave: a recent successful sync, then a partial failure today.
	_, err = db.db.Exec(`
		INSERT INTO sync_runs (user_id, trigger, started_at, finished_at, status)
		VALUES (?, 'scheduled', datetime('now','-2 days'), datetime('now','-2 days'), 'completed'),
		       (?, 'scheduled', datetime('now','-1 hour'), datetime('now','-1 hour'), 'partial')
	`, ids["dave"], ids["dave"])
	if err != nil {
		t.Fatalf("insert dave runs: %v", err)
	}

	return ids
}

func TestGetFunnelCounts(t *testing.T) {
	db := openTestDB(t)
	seedReportFixtures(t, db)

	fc, err := db.GetFunnelCounts()
	if err != nil {
		t.Fatalf("GetFunnelCounts: %v", err)
	}

	// All 6 users are active (CreateUser default).
	if fc.SignedUp != 6 {
		t.Errorf("SignedUp = %d, want 6", fc.SignedUp)
	}
	// alice, bob, carol, dave, eve have garmin creds (frank does not).
	if fc.GarminConnected != 5 {
		t.Errorf("GarminConnected = %d, want 5", fc.GarminConnected)
	}
	// Same five also have EFB creds.
	if fc.EFBConnected != 5 {
		t.Errorf("EFBConnected = %d, want 5", fc.EFBConnected)
	}
	// alice, bob, dave have at least one completed sync (carol, eve, frank do not).
	if fc.FirstSyncCompleted != 3 {
		t.Errorf("FirstSyncCompleted = %d, want 3", fc.FirstSyncCompleted)
	}
	// alice (1h ago) and dave (2d ago) completed in last 7d. bob's last completion was 10d ago.
	if fc.SyncedInLast7Days != 2 {
		t.Errorf("SyncedInLast7Days = %d, want 2", fc.SyncedInLast7Days)
	}
}

func TestGetStuckUsers(t *testing.T) {
	db := openTestDB(t)
	ids := seedReportFixtures(t, db)

	stuck, err := db.GetStuckUsers()
	if err != nil {
		t.Fatalf("GetStuckUsers: %v", err)
	}

	// Only fully connected users (valid garmin + efb) with no recent
	// successful sync should be reported.
	//   bob  — last success 10d ago → stuck
	//   carol — never succeeded → stuck
	// alice and dave have recent successes; eve has invalid garmin; frank has no creds.
	wantIDs := map[int64]bool{ids["bob"]: true, ids["carol"]: true}
	gotIDs := map[int64]bool{}
	for _, s := range stuck {
		gotIDs[s.UserID] = true
	}
	for id := range wantIDs {
		if !gotIDs[id] {
			t.Errorf("expected user %d in stuck list", id)
		}
	}
	for id := range gotIDs {
		if !wantIDs[id] {
			t.Errorf("unexpected user %d in stuck list", id)
		}
	}

	// Verify carol's "never synced" presentation.
	for _, s := range stuck {
		if s.UserID == ids["carol"] {
			if s.LastSuccessfulSync != nil {
				t.Errorf("carol LastSuccessfulSync = %v, want nil", s.LastSuccessfulSync)
			}
			if s.LastErrorMessage != "login refused" {
				t.Errorf("carol LastErrorMessage = %q, want %q", s.LastErrorMessage, "login refused")
			}
		}
	}
}

func TestGetUserActivityOverview(t *testing.T) {
	db := openTestDB(t)
	ids := seedReportFixtures(t, db)

	rows, err := db.GetUserActivityOverview()
	if err != nil {
		t.Fatalf("GetUserActivityOverview: %v", err)
	}
	if len(rows) != 6 {
		t.Fatalf("len(rows) = %d, want 6", len(rows))
	}

	byID := map[int64]UserActivity{}
	for _, ua := range rows {
		byID[ua.UserID] = ua
	}

	cases := []struct {
		name       string
		wantStatus string
		wantSucc7  int
	}{
		{"alice", "synced", 1},
		{"bob", "stale", 0}, // last success 10d ago, fully connected — stale (status='failed' on last attempt → failing wins)
		{"carol", "never_synced", 0},
		{"dave", "failing", 1}, // partial last attempt
		{"eve", "disconnected", 0},
		{"frank", "disconnected", 0},
	}
	for _, c := range cases {
		ua, ok := byID[ids[c.name]]
		if !ok {
			t.Errorf("%s: missing from overview", c.name)
			continue
		}
		// bob's classification is "failing" (latest attempt failed) — adjust expectation.
		if c.name == "bob" {
			if ua.Status != "failing" {
				t.Errorf("bob: status = %q, want %q", ua.Status, "failing")
			}
		} else if ua.Status != c.wantStatus {
			t.Errorf("%s: status = %q, want %q", c.name, ua.Status, c.wantStatus)
		}
		if ua.Successful7Days != c.wantSucc7 {
			t.Errorf("%s: Successful7Days = %d, want %d", c.name, ua.Successful7Days, c.wantSucc7)
		}
	}
}

func TestGetRecentFailures(t *testing.T) {
	db := openTestDB(t)
	seedReportFixtures(t, db)

	rf, err := db.GetRecentFailures(50)
	if err != nil {
		t.Fatalf("GetRecentFailures: %v", err)
	}

	// Failed/partial sync runs: bob (failed yesterday), carol (failed 2d ago), dave (partial 1h ago).
	if len(rf.SyncRuns) != 3 {
		t.Errorf("len(SyncRuns) = %d, want 3", len(rf.SyncRuns))
	}
	for _, r := range rf.SyncRuns {
		if r.Email == "" {
			t.Errorf("sync run %d missing email", r.ID)
		}
		if r.Status != "failed" && r.Status != "partial" {
			t.Errorf("sync run %d status = %q, want failed/partial", r.ID, r.Status)
		}
	}

	// No failed activities seeded.
	if len(rf.Activities) != 0 {
		t.Errorf("len(Activities) = %d, want 0", len(rf.Activities))
	}
}

func TestCountUsersSyncedSince(t *testing.T) {
	db := openTestDB(t)
	seedReportFixtures(t, db)

	// 7-day window: alice (1h ago) + dave (2d ago) = 2.
	n, err := db.CountUsersSyncedSince("-7 days")
	if err != nil {
		t.Fatalf("CountUsersSyncedSince(-7 days): %v", err)
	}
	if n != 2 {
		t.Errorf("7d count = %d, want 2", n)
	}

	// 30-day window: alice + bob (10d ago) + dave = 3.
	n, err = db.CountUsersSyncedSince("-30 days")
	if err != nil {
		t.Fatalf("CountUsersSyncedSince(-30 days): %v", err)
	}
	if n != 3 {
		t.Errorf("30d count = %d, want 3", n)
	}

	// 1-hour window: nobody (alice's row uses datetime('now','-1 hour') which is at the boundary; SQLite returns no rows for strict >).
	n, err = db.CountUsersSyncedSince("-30 minutes")
	if err != nil {
		t.Fatalf("CountUsersSyncedSince(-30 minutes): %v", err)
	}
	if n != 0 {
		t.Errorf("30m count = %d, want 0", n)
	}
}
