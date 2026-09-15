package database

import (
	"slices"
	"testing"

	"efb-connector/internal/garmin"
)

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

func TestUpdateSelectedActivityTypes(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("uselected@example.com")

	// A new account starts with the paddle sports plus the catch-all; the
	// column's own '[]' default would mean "sync nothing", so CreateUser has
	// to seed it.
	wantDefault := garmin.DefaultSelectedCategories()
	if !slices.Equal(u.SelectedActivityTypes, wantDefault) {
		t.Errorf("CreateUser selected = %v, want %v", u.SelectedActivityTypes, wantDefault)
	}
	if slices.Contains(u.SelectedActivityTypes, garmin.CategorySailing) {
		t.Error("sailing must not be selected for a new user")
	}

	want := []string{garmin.CategoryKayak, garmin.CategoryCanoe}
	if err := db.UpdateSelectedActivityTypes(u.ID, want); err != nil {
		t.Fatalf("UpdateSelectedActivityTypes: %v", err)
	}
	if got := mustGetUser(t, db, u.ID).SelectedActivityTypes; !slices.Equal(got, want) {
		t.Errorf("SelectedActivityTypes = %v, want %v", got, want)
	}

	// nil and empty both mean "nothing selected" and must round-trip as such
	// rather than blowing up on a NOT NULL column.
	if err := db.UpdateSelectedActivityTypes(u.ID, nil); err != nil {
		t.Fatalf("UpdateSelectedActivityTypes(nil): %v", err)
	}
	if got := mustGetUser(t, db, u.ID).SelectedActivityTypes; len(got) != 0 {
		t.Errorf("SelectedActivityTypes after nil = %v, want empty", got)
	}
}

// Empty means "sync nothing" under the selection model, so a value we cannot
// read must NOT decode to empty — that would silently stop a user's imports
// with a clean, successful-looking sync run. Only a real `[]` is honoured.
func TestDecodeSelectedCategories_FailsOpenOnUnreadableValues(t *testing.T) {
	defaults := garmin.DefaultSelectedCategories()

	for _, raw := range []string{"", "not json", "null", "{}", `{"kayak":true}`} {
		got := decodeSelectedCategories(raw)
		if !slices.Equal(got, defaults) {
			t.Errorf("decodeSelectedCategories(%q) = %v, want defaults %v", raw, got, defaults)
		}
	}

	// A deliberate "I unticked everything" must survive as empty.
	if got := decodeSelectedCategories(`[]`); len(got) != 0 {
		t.Errorf("decodeSelectedCategories(`[]`) = %v, want empty (user unticked all)", got)
	}
	// And a real selection round-trips untouched.
	if got := decodeSelectedCategories(`["kayak","sailing"]`); !slices.Equal(got, []string{"kayak", "sailing"}) {
		t.Errorf("decodeSelectedCategories = %v, want [kayak sailing]", got)
	}
}

// The column DEFAULT is a working selection, not '[]', so any INSERT that
// omits the column yields a user who syncs rather than one who silently
// never imports again.
func TestSelectedActivityTypes_ColumnDefaultIsUsable(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.db.Exec(`INSERT INTO users (email) VALUES (?)`, "raw-insert@example.com"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	u, err := db.GetUserByEmail("raw-insert@example.com")
	if err != nil || u == nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if !slices.Equal(u.SelectedActivityTypes, garmin.DefaultSelectedCategories()) {
		t.Errorf("column default gave %v, want %v", u.SelectedActivityTypes, garmin.DefaultSelectedCategories())
	}
}

func TestUpdateNameKeywords(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("ukeywords@example.com")

	if len(u.NameKeywords) != 0 {
		t.Errorf("CreateUser NameKeywords = %v, want empty", u.NameKeywords)
	}

	want := []string{"Drachenboot", "Outrigger Canoe"}
	if err := db.UpdateNameKeywords(u.ID, want); err != nil {
		t.Fatalf("UpdateNameKeywords: %v", err)
	}
	if got := mustGetUser(t, db, u.ID).NameKeywords; !slices.Equal(got, want) {
		t.Errorf("NameKeywords via GetUserByID = %v, want %v", got, want)
	}

	// The sync path reads users through GetSyncableUsers, which has its own
	// column list and scan function — both must carry the keywords too.
	if err := db.SaveGarminCredentials(u.ID, "g@example.com", "pw"); err != nil {
		t.Fatalf("SaveGarminCredentials: %v", err)
	}
	if err := db.SaveEFBCredentials(u.ID, "efb", "pw"); err != nil {
		t.Fatalf("SaveEFBCredentials: %v", err)
	}
	syncable, err := db.GetSyncableUsers()
	if err != nil {
		t.Fatalf("GetSyncableUsers: %v", err)
	}
	if len(syncable) != 1 || !slices.Equal(syncable[0].NameKeywords, want) {
		t.Errorf("GetSyncableUsers NameKeywords = %+v, want %v", syncable, want)
	}

	// nil clears the list without tripping the NOT NULL column.
	if err := db.UpdateNameKeywords(u.ID, nil); err != nil {
		t.Fatalf("UpdateNameKeywords(nil): %v", err)
	}
	if got := mustGetUser(t, db, u.ID).NameKeywords; len(got) != 0 {
		t.Errorf("NameKeywords after nil = %v, want empty", got)
	}
}

// Unlike the category selection there is no default to fall back to, and a
// missing keyword only loses an opt-in extra, so unreadable values decode to
// "no keywords" rather than failing the whole user scan.
func TestDecodeNameKeywords_UnreadableValuesMeanNone(t *testing.T) {
	for _, raw := range []string{"", "not json", "null", "{}", `["ok",1]`} {
		if got := decodeNameKeywords(raw); len(got) != 0 {
			t.Errorf("decodeNameKeywords(%q) = %v, want empty", raw, got)
		}
	}
	if got := decodeNameKeywords(`["Drachenboot","Outrigger"]`); !slices.Equal(got, []string{"Drachenboot", "Outrigger"}) {
		t.Errorf("decodeNameKeywords = %v, want [Drachenboot Outrigger]", got)
	}
}

func TestNameKeywords_ColumnDefaultIsUsable(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.db.Exec(`INSERT INTO users (email) VALUES (?)`, "raw-insert-kw@example.com"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	u, err := db.GetUserByEmail("raw-insert-kw@example.com")
	if err != nil || u == nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if len(u.NameKeywords) != 0 {
		t.Errorf("column default gave %v, want empty", u.NameKeywords)
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
