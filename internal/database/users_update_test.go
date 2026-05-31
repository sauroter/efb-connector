package database

import "testing"

// mustGetUser fetches a user by id, fatalling on any error or missing row.
// Used by the update tests instead of `got, _ := db.GetUserByID(id)` so a
// nil dereference can't mask the real failure.
func mustGetUser(t *testing.T, db *DB, id int64) *User {
	t.Helper()
	u, err := db.GetUserByID(id)
	if err != nil {
		t.Fatalf("GetUserByID(%d): %v", id, err)
	}
	if u == nil {
		t.Fatalf("GetUserByID(%d): user not found", id)
	}
	return u
}

func TestUpdateAutoCreateTrips(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("uact@example.com")

	if err := db.UpdateAutoCreateTrips(u.ID, false); err != nil {
		t.Fatalf("UpdateAutoCreateTrips(false): %v", err)
	}
	if mustGetUser(t, db, u.ID).AutoCreateTrips {
		t.Error("expected AutoCreateTrips=false")
	}

	if err := db.UpdateAutoCreateTrips(u.ID, true); err != nil {
		t.Fatalf("UpdateAutoCreateTrips(true): %v", err)
	}
	if !mustGetUser(t, db, u.ID).AutoCreateTrips {
		t.Error("expected AutoCreateTrips=true")
	}
}

func TestUpdateEnrichTrips(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("uet@example.com")

	// enrich_trips defaults to 1 (migration 0005), so we start by flipping
	// to false. Each subsequent assertion must reflect an actual write.
	if err := db.UpdateEnrichTrips(u.ID, false); err != nil {
		t.Fatalf("UpdateEnrichTrips(false): %v", err)
	}
	if mustGetUser(t, db, u.ID).EnrichTrips {
		t.Error("expected EnrichTrips=false after first Update")
	}

	if err := db.UpdateEnrichTrips(u.ID, true); err != nil {
		t.Fatalf("UpdateEnrichTrips(true): %v", err)
	}
	if !mustGetUser(t, db, u.ID).EnrichTrips {
		t.Error("expected EnrichTrips=true after toggle back")
	}
}

func TestUpdatePreferredLang(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("uplang@example.com")

	if err := db.UpdatePreferredLang(u.ID, "de"); err != nil {
		t.Fatalf("UpdatePreferredLang: %v", err)
	}
	if got := mustGetUser(t, db, u.ID).PreferredLang; got != "de" {
		t.Errorf("PreferredLang = %q, want de", got)
	}
}

func TestUpdateSetupCompleted(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("usetup@example.com")

	if err := db.UpdateSetupCompleted(u.ID, true); err != nil {
		t.Fatalf("UpdateSetupCompleted(true): %v", err)
	}
	if !mustGetUser(t, db, u.ID).SetupCompleted {
		t.Error("expected SetupCompleted=true")
	}

	if err := db.UpdateSetupCompleted(u.ID, false); err != nil {
		t.Fatalf("UpdateSetupCompleted(false): %v", err)
	}
	if mustGetUser(t, db, u.ID).SetupCompleted {
		t.Error("expected SetupCompleted=false")
	}
}

func TestPing(t *testing.T) {
	db := openTestDB(t)
	if err := db.Ping(); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestEFBCredentialsExist(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("efbex@example.com")

	exists, err := db.EFBCredentialsExist(u.ID)
	if err != nil {
		t.Fatalf("EFBCredentialsExist: %v", err)
	}
	if exists {
		t.Error("no row should report exists=false")
	}

	_ = db.SaveEFBCredentials(u.ID, "u", "p")
	exists, err = db.EFBCredentialsExist(u.ID)
	if err != nil {
		t.Fatalf("EFBCredentialsExist after save: %v", err)
	}
	if !exists {
		t.Error("should report exists=true after SaveEFBCredentials")
	}
}

func TestGetActivityStatus(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("actstatus@example.com")

	// Missing activity returns ("", nil).
	status, err := db.GetActivityStatus(u.ID, "missing")
	if err != nil {
		t.Fatalf("GetActivityStatus missing: %v", err)
	}
	if status != "" {
		t.Errorf("missing activity status = %q, want empty", status)
	}

	// Existing activity returns its status.
	_ = db.RecordActivity(u.ID, "act-1", "A", "run", "2024-01-01", "success", "")
	status, err = db.GetActivityStatus(u.ID, "act-1")
	if err != nil {
		t.Fatalf("GetActivityStatus: %v", err)
	}
	if status != "success" {
		t.Errorf("status = %q, want success", status)
	}
}
