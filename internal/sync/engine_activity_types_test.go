package sync

import (
	"context"
	"slices"
	"testing"
	"time"

	"efb-connector/internal/database"
	"efb-connector/internal/efb"
	"efb-connector/internal/garmin"
)

// deselect turns off the given categories for a user, leaving everything else
// selected — the state you get by unticking boxes on /settings.
func deselect(t *testing.T, db *database.DB, userID int64, cats ...string) {
	t.Helper()
	user, err := db.GetUserByID(userID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	kept := make([]string, 0, len(user.SelectedActivityTypes))
	for _, c := range user.SelectedActivityTypes {
		if !slices.Contains(cats, c) {
			kept = append(kept, c)
		}
	}
	if err := db.UpdateSelectedActivityTypes(userID, kept); err != nil {
		t.Fatalf("UpdateSelectedActivityTypes: %v", err)
	}
}

// Regression test for the reported bug: a Fenix "Sail" activity reached eFB
// because Garmin files sailing under the same Water Sports parent as kayaking,
// and the old filter had no category that could express "not this one".
//
// No deselect() here on purpose — sailing must be off for a user who never
// touched the setting, or the fix relies on every affected paddler noticing
// and unticking a box.
func TestSyncUser_ActivityTypes_SailingDroppedByDefault(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	now := time.Now()
	gp := &mockGarminProvider{activities: []garmin.Activity{
		{ProviderID: "kayak-1", Name: "Morning paddle", Type: "kayaking", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 3600, DistanceM: 5000},
		{ProviderID: "sail-1", Name: "Ostsee", Type: "sailing_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 14400, DistanceM: 30000},
	}}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("SyncUser: %v", err)
	}

	run, err := db.GetSyncRun(runID)
	if err != nil {
		t.Fatalf("GetSyncRun: %v", err)
	}
	if run.ActivitiesSynced != 1 {
		t.Errorf("ActivitiesSynced = %d, want 1 (sailing dropped)", run.ActivitiesSynced)
	}
	if run.ExcludedCount != 1 {
		t.Errorf("ExcludedCount = %d, want 1", run.ExcludedCount)
	}
	if synced, _ := db.IsActivitySynced(user.ID, "sail-1"); synced {
		t.Error("sailing activity should NOT be synced when sailing is deselected")
	}
	if synced, _ := db.IsActivitySynced(user.ID, "kayak-1"); !synced {
		t.Error("kayak activity should be synced")
	}
}

// The flip side: someone who genuinely wants their sailing in eFB opts in, and
// it syncs. Off by default must not mean unavailable.
func TestSyncUser_ActivityTypes_SailingSelectedIsSynced(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	if slices.Contains(user.SelectedActivityTypes, garmin.CategorySailing) {
		t.Fatalf("new user should NOT start with sailing selected, got %v", user.SelectedActivityTypes)
	}
	selected := append(user.SelectedActivityTypes, garmin.CategorySailing)
	if err := db.UpdateSelectedActivityTypes(user.ID, selected); err != nil {
		t.Fatalf("UpdateSelectedActivityTypes: %v", err)
	}

	now := time.Now()
	gp := &mockGarminProvider{activities: []garmin.Activity{
		{ProviderID: "sail-1", Name: "Ostsee", Type: "sailing_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 14400, DistanceM: 30000},
	}}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, _ := engine.SyncUser(context.Background(), user.ID, "manual")
	run, _ := db.GetSyncRun(runID)
	if run.ActivitiesSynced != 1 {
		t.Errorf("ActivitiesSynced = %d, want 1", run.ActivitiesSynced)
	}
	if run.ExcludedCount != 0 {
		t.Errorf("ExcludedCount = %d, want 0", run.ExcludedCount)
	}
}

// Mixed Garmin response with paddling + rowing. With rowing deselected the
// sync engine must upload only the paddling activities and persist the drop
// counter on the sync_run.
func TestSyncUser_ActivityTypes_FiltersRowing(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	deselect(t, db, user.ID, garmin.CategoryRowing)

	now := time.Now()
	gp := &mockGarminProvider{activities: []garmin.Activity{
		{ProviderID: "kayak-1", Name: "Morning paddle", Type: "kayaking", Date: now, StartTime: now, DurationSecs: 3600, DistanceM: 5000},
		{ProviderID: "row-1", Name: "Erg row", Type: "rowing_v2", Date: now, StartTime: now, DurationSecs: 1800, DistanceM: 3000},
		{ProviderID: "sup-1", Name: "Lake SUP", Type: "stand_up_paddleboarding", Date: now, StartTime: now, DurationSecs: 2400, DistanceM: 4000},
	}}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, err := engine.SyncUser(context.Background(), user.ID, "manual")
	if err != nil {
		t.Fatalf("SyncUser: %v", err)
	}

	run, err := db.GetSyncRun(runID)
	if err != nil {
		t.Fatalf("GetSyncRun: %v", err)
	}
	if run.ActivitiesFound != 2 {
		t.Errorf("ActivitiesFound = %d, want 2 (rowing dropped)", run.ActivitiesFound)
	}
	if run.ActivitiesSynced != 2 {
		t.Errorf("ActivitiesSynced = %d, want 2", run.ActivitiesSynced)
	}
	if run.ExcludedCount != 1 {
		t.Errorf("ExcludedCount = %d, want 1", run.ExcludedCount)
	}

	if synced, _ := db.IsActivitySynced(user.ID, "row-1"); synced {
		t.Error("rowing activity should NOT be synced when rowing is deselected")
	}
	if synced, _ := db.IsActivitySynced(user.ID, "kayak-1"); !synced {
		t.Error("kayak activity should be synced")
	}
}

// Default for a fresh account: every paddle sport syncs untouched. The point
// of narrowing the default is to stop sailing, not to make paddlers opt in to
// their own sport.
func TestSyncUser_ActivityTypes_DefaultKeepsEveryPaddleSport(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	now := time.Now()
	gp := &mockGarminProvider{activities: []garmin.Activity{
		{ProviderID: "kayak-1", Name: "Morning paddle", Type: "kayaking_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 3600, DistanceM: 5000},
		{ProviderID: "canoe-1", Name: "Kanutour", Type: "canoeing_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 3600, DistanceM: 5000},
		{ProviderID: "paddle-1", Name: "Paddeln", Type: "paddling_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 3600, DistanceM: 5000},
		{ProviderID: "sup-1", Name: "Lake SUP", Type: "stand_up_paddleboarding_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 2400, DistanceM: 4000},
		{ProviderID: "row-1", Name: "Rudern", Type: "rowing_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 1800, DistanceM: 3000},
		{ProviderID: "ww-1", Name: "Wildwasser", Type: "whitewater_rafting_kayaking", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 1800, DistanceM: 3000},
	}}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, _ := engine.SyncUser(context.Background(), user.ID, "manual")
	run, _ := db.GetSyncRun(runID)
	if run.ActivitiesSynced != 6 {
		t.Errorf("ActivitiesSynced = %d, want 6", run.ActivitiesSynced)
	}
	if run.ExcludedCount != 0 {
		t.Errorf("ExcludedCount = %d, want 0 — no paddle sport may be dropped by default", run.ExcludedCount)
	}
}

// The non-paddle categories are all off out of the box, not just sailing.
func TestSyncUser_ActivityTypes_NonPaddlingDroppedByDefault(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	now := time.Now()
	gp := &mockGarminProvider{activities: []garmin.Activity{
		{ProviderID: "kayak-1", Name: "Morning paddle", Type: "kayaking_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 3600, DistanceM: 5000},
		{ProviderID: "sail-1", Name: "Ostsee", Type: "sailing_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 7200, DistanceM: 20000},
		{ProviderID: "wind-1", Name: "Windsurf", Type: "windsurfing_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 3600, DistanceM: 8000},
		{ProviderID: "boat-1", Name: "Motorboot", Type: "boating_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 3600, DistanceM: 25000},
		{ProviderID: "wake-1", Name: "Wakeboard", Type: "wakeboarding_v2", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 1200, DistanceM: 2000},
	}}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, _ := engine.SyncUser(context.Background(), user.ID, "manual")
	run, _ := db.GetSyncRun(runID)
	if run.ActivitiesSynced != 1 {
		t.Errorf("ActivitiesSynced = %d, want 1 (only the kayak)", run.ActivitiesSynced)
	}
	if run.ExcludedCount != 4 {
		t.Errorf("ExcludedCount = %d, want 4", run.ExcludedCount)
	}
}

// A typeKey we don't recognise, filed outside the water-sports parent, is kept
// no matter what the user deselected. Dropping it would risk losing a genuine
// paddling activity just because this table is out of date.
func TestSyncUser_ActivityTypes_UnknownTypeKeyKept(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	deselect(t, db, user.ID, garmin.CategoryRowing, garmin.CategoryOtherWater)

	now := time.Now()
	gp := &mockGarminProvider{activities: []garmin.Activity{
		{ProviderID: "mystery-1", Name: "Mystery sport", Type: "hydrofoiling_v9", ParentTypeID: 17, Date: now, StartTime: now, DurationSecs: 1200, DistanceM: 2000},
	}}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, _ := engine.SyncUser(context.Background(), user.ID, "manual")
	run, _ := db.GetSyncRun(runID)
	if run.ActivitiesSynced != 1 {
		t.Errorf("ActivitiesSynced = %d, want 1 (unknown typeKey kept)", run.ActivitiesSynced)
	}
	if run.ExcludedCount != 0 {
		t.Errorf("ExcludedCount = %d, want 0", run.ExcludedCount)
	}
}

// Activities recovered by the match_by_name fallback arrive as typeKey
// "other" under the generic-fitness parent, so neither the typeKey table nor
// the parent id says anything about them. They must still obey the user's
// selection — otherwise unticking Rowing does nothing for exactly the Venu-3
// users the fallback exists for, which is what the settings copy promises.
func TestSyncUser_ActivityTypes_NameMatchedRespectSelection(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	if err := db.UpdateMatchByName(user.ID, true); err != nil {
		t.Fatalf("UpdateMatchByName: %v", err)
	}
	deselect(t, db, user.ID, garmin.CategoryRowing)

	now := time.Now()
	gp := &mockGarminProvider{activities: []garmin.Activity{
		{ProviderID: "row-1", Name: "Rudern am Morgen", Type: "other", ParentTypeID: 17, Date: now, StartTime: now, DurationSecs: 1800, DistanceM: 3000},
		{ProviderID: "kajak-1", Name: "Seekajak Feierabend", Type: "other", ParentTypeID: 17, Date: now, StartTime: now, DurationSecs: 3600, DistanceM: 5000},
	}}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, _ := engine.SyncUser(context.Background(), user.ID, "manual")
	run, _ := db.GetSyncRun(runID)
	if run.ActivitiesSynced != 1 {
		t.Errorf("ActivitiesSynced = %d, want 1 (kayak kept, rowing dropped)", run.ActivitiesSynced)
	}
	if run.ExcludedCount != 1 {
		t.Errorf("ExcludedCount = %d, want 1", run.ExcludedCount)
	}
	if synced, _ := db.IsActivitySynced(user.ID, "row-1"); synced {
		t.Error("name-matched rowing must obey the deselected Rowing category")
	}
	if synced, _ := db.IsActivitySynced(user.ID, "kajak-1"); !synced {
		t.Error("name-matched kayak should still sync")
	}
}

// ...but a name-matched activity whose name matches no category keyword is
// still kept: unknown must never mean dropped.
func TestSyncUser_ActivityTypes_NameMatchedUnknownKept(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	if err := db.UpdateMatchByName(user.ID, true); err != nil {
		t.Fatalf("UpdateMatchByName: %v", err)
	}
	deselect(t, db, user.ID, garmin.CategoryRowing, garmin.CategoryOtherWater)

	now := time.Now()
	gp := &mockGarminProvider{activities: []garmin.Activity{
		{ProviderID: "myst-1", Name: "Wassertraining", Type: "other", ParentTypeID: 17, Date: now, StartTime: now, DurationSecs: 1800, DistanceM: 3000},
	}}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, _ := engine.SyncUser(context.Background(), user.ID, "manual")
	run, _ := db.GetSyncRun(runID)
	if run.ActivitiesSynced != 1 {
		t.Errorf("ActivitiesSynced = %d, want 1 (unclassifiable name kept)", run.ActivitiesSynced)
	}
	if run.ExcludedCount != 0 {
		t.Errorf("ExcludedCount = %d, want 0", run.ExcludedCount)
	}
}

// The catch-all: an unrecognised typeKey that Garmin *did* file under Water
// Sports is a water sport we haven't named yet, so "other water sports" claims
// it and the user can switch it off. Without this, the next sport Garmin adds
// would leak into eFB exactly the way sailing did.
func TestSyncUser_ActivityTypes_UnknownWaterSportIsOtherWater(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	deselect(t, db, user.ID, garmin.CategoryOtherWater)

	now := time.Now()
	gp := &mockGarminProvider{activities: []garmin.Activity{
		{ProviderID: "kayak-1", Name: "Morning paddle", Type: "kayaking", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 3600, DistanceM: 5000},
		{ProviderID: "mystery-1", Name: "Mystery sport", Type: "hydrofoiling_v9", ParentTypeID: 228, Date: now, StartTime: now, DurationSecs: 1200, DistanceM: 2000},
	}}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, _ := engine.SyncUser(context.Background(), user.ID, "manual")
	run, _ := db.GetSyncRun(runID)
	if run.ActivitiesSynced != 1 {
		t.Errorf("ActivitiesSynced = %d, want 1 (unnamed water sport dropped)", run.ActivitiesSynced)
	}
	if run.ExcludedCount != 1 {
		t.Errorf("ExcludedCount = %d, want 1", run.ExcludedCount)
	}
	if synced, _ := db.IsActivitySynced(user.ID, "mystery-1"); synced {
		t.Error("unnamed water sport should NOT sync when other_water is deselected")
	}
}
