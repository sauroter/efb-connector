package sync

import (
	"context"
	"testing"
	"time"

	"efb-connector/internal/efb"
	"efb-connector/internal/garmin"
)

// Mixed Garmin response with paddling + rowing. With rowing excluded the
// sync engine must upload only the paddling activity and persist the drop
// counter on the sync_run.
func TestSyncUser_ExcludedActivityTypes_FiltersRowing(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	if err := db.UpdateExcludedActivityTypes(user.ID, []string{garmin.CategoryRowing}); err != nil {
		t.Fatalf("UpdateExcludedActivityTypes: %v", err)
	}

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
		t.Errorf("ActivitiesFound = %d, want 2 (rowing excluded)", run.ActivitiesFound)
	}
	if run.ActivitiesSynced != 2 {
		t.Errorf("ActivitiesSynced = %d, want 2", run.ActivitiesSynced)
	}
	if run.ExcludedCount != 1 {
		t.Errorf("ExcludedCount = %d, want 1", run.ExcludedCount)
	}

	rowSynced, _ := db.IsActivitySynced(user.ID, "row-1")
	if rowSynced {
		t.Error("rowing activity should NOT be synced when rowing is excluded")
	}
	kayakSynced, _ := db.IsActivitySynced(user.ID, "kayak-1")
	if !kayakSynced {
		t.Error("kayak activity should be synced")
	}
}

// Default (empty exclusion list): both rowing and paddling sync, no drops.
func TestSyncUser_ExcludedActivityTypes_DefaultKeepsAll(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	now := time.Now()
	gp := &mockGarminProvider{activities: []garmin.Activity{
		{ProviderID: "kayak-1", Name: "Morning paddle", Type: "kayaking", Date: now, StartTime: now, DurationSecs: 3600, DistanceM: 5000},
		{ProviderID: "row-1", Name: "Erg row", Type: "rowing", Date: now, StartTime: now, DurationSecs: 1800, DistanceM: 3000},
	}}
	ec := efb.NewEFBClient(srv.URL)
	engine := newEngine(db, gp, ec)

	runID, _ := engine.SyncUser(context.Background(), user.ID, "manual")
	run, _ := db.GetSyncRun(runID)
	if run.ActivitiesSynced != 2 {
		t.Errorf("ActivitiesSynced = %d, want 2", run.ActivitiesSynced)
	}
	if run.ExcludedCount != 0 {
		t.Errorf("ExcludedCount = %d, want 0 with no exclusions", run.ExcludedCount)
	}
}

// Unknown typeKeys must pass through even when exclusions are configured —
// conservative default so a new Garmin water sport isn't silently dropped.
func TestSyncUser_ExcludedActivityTypes_UnknownTypeKeyKept(t *testing.T) {
	db := openTestDB(t)
	user := setupUser(t, db)
	srv := newMockEFBServer(t)

	if err := db.UpdateExcludedActivityTypes(user.ID, []string{garmin.CategoryRowing}); err != nil {
		t.Fatalf("UpdateExcludedActivityTypes: %v", err)
	}

	now := time.Now()
	gp := &mockGarminProvider{activities: []garmin.Activity{
		{ProviderID: "mystery-1", Name: "Mystery sport", Type: "hydrofoiling_v9", Date: now, StartTime: now, DurationSecs: 1200, DistanceM: 2000},
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
