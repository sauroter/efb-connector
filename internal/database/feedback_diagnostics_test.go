package database

import (
	"reflect"
	"testing"
)

func TestRecordSyncDiagnostics_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("diag@example.com")
	runID, err := db.CreateSyncRun(u.ID, "scheduled")
	if err != nil {
		t.Fatalf("CreateSyncRun: %v", err)
	}

	keys := []string{"cycling", "other", "running"}
	if err := db.RecordSyncDiagnostics(runID, 351, keys, 2, 1); err != nil {
		t.Fatalf("RecordSyncDiagnostics: %v", err)
	}

	r, err := db.GetSyncRun(runID)
	if err != nil {
		t.Fatalf("GetSyncRun: %v", err)
	}
	if r.RawCount != 351 {
		t.Errorf("RawCount = %d, want 351", r.RawCount)
	}
	if !reflect.DeepEqual(r.TypeKeysSeen, keys) {
		t.Errorf("TypeKeysSeen = %v, want %v", r.TypeKeysSeen, keys)
	}
	if r.NameMatchedCount != 2 {
		t.Errorf("NameMatchedCount = %d, want 2", r.NameMatchedCount)
	}
	if r.ExcludedCount != 1 {
		t.Errorf("ExcludedCount = %d, want 1", r.ExcludedCount)
	}
}

func TestRecordSyncDiagnostics_NilTypeKeysWritesNull(t *testing.T) {
	// A nil slice means "we never reached Garmin" (e.g. auth failure
	// before listing). Distinct from an empty slice ("Garmin returned
	// nothing"). The persisted row must reflect that distinction so the
	// dashboard hint logic in #4 can branch correctly.
	db := openTestDB(t)
	u, _ := db.CreateUser("diag-nil@example.com")
	runID, _ := db.CreateSyncRun(u.ID, "scheduled")

	if err := db.RecordSyncDiagnostics(runID, 0, nil, 0, 0); err != nil {
		t.Fatalf("RecordSyncDiagnostics: %v", err)
	}
	r, _ := db.GetSyncRun(runID)
	if r.TypeKeysSeen != nil {
		t.Errorf("TypeKeysSeen = %v, want nil", r.TypeKeysSeen)
	}
	if r.NameMatchedCount != 0 {
		t.Errorf("NameMatchedCount = %d, want 0", r.NameMatchedCount)
	}
	if r.ExcludedCount != 0 {
		t.Errorf("ExcludedCount = %d, want 0", r.ExcludedCount)
	}
}

func TestGetFeedbackDiagnostics_NewUserNoCredentialsNoSyncs(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("noop@example.com")

	d, err := db.GetFeedbackDiagnostics(u.ID)
	if err != nil {
		t.Fatalf("GetFeedbackDiagnostics: %v", err)
	}
	if d.HasGarminCredentials {
		t.Error("HasGarminCredentials = true, want false")
	}
	if d.HasEFBCredentials {
		t.Error("HasEFBCredentials = true, want false")
	}
	if d.EFBConsentRequired {
		t.Error("EFBConsentRequired = true, want false")
	}
	if d.LastSync != nil {
		t.Errorf("LastSync = %+v, want nil", d.LastSync)
	}
}

func TestGetFeedbackDiagnostics_HealthyUserWithSyncRun(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("healthy@example.com")
	if err := db.SaveGarminCredentials(u.ID, "g@example.com", "p"); err != nil {
		t.Fatalf("SaveGarminCredentials: %v", err)
	}
	if err := db.SaveEFBCredentials(u.ID, "user", "p"); err != nil {
		t.Fatalf("SaveEFBCredentials: %v", err)
	}

	runID, err := db.CreateSyncRun(u.ID, "scheduled")
	if err != nil {
		t.Fatalf("CreateSyncRun: %v", err)
	}
	if err := db.UpdateSyncRun(runID, "completed", 3, 2, 1, 0, 2, ""); err != nil {
		t.Fatalf("UpdateSyncRun: %v", err)
	}

	d, err := db.GetFeedbackDiagnostics(u.ID)
	if err != nil {
		t.Fatalf("GetFeedbackDiagnostics: %v", err)
	}
	if !d.HasGarminCredentials || d.GarminInvalid {
		t.Errorf("Garmin status = %+v, want has=true invalid=false", d)
	}
	if !d.HasEFBCredentials || d.EFBInvalid || d.EFBConsentRequired {
		t.Errorf("EFB status = %+v, want has=true invalid=false consent=false", d)
	}
	if d.LastSync == nil {
		t.Fatal("LastSync = nil, want non-nil")
	}
	if d.LastSync.Status != "completed" {
		t.Errorf("LastSync.Status = %q, want completed", d.LastSync.Status)
	}
	if d.LastSync.ActivitiesFound != 3 || d.LastSync.ActivitiesSynced != 2 {
		t.Errorf("LastSync counts = %+v, want found=3 synced=2", d.LastSync)
	}
}

func TestGetFeedbackDiagnostics_InvalidCredentialsAndConsentGate(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("stuck@example.com")
	if err := db.SaveGarminCredentials(u.ID, "g@example.com", "p"); err != nil {
		t.Fatalf("SaveGarminCredentials: %v", err)
	}
	if err := db.SaveEFBCredentials(u.ID, "user", "p"); err != nil {
		t.Fatalf("SaveEFBCredentials: %v", err)
	}
	if err := db.InvalidateGarminCredentials(u.ID, "auth failed"); err != nil {
		t.Fatalf("InvalidateGarminCredentials: %v", err)
	}
	if err := db.MarkEFBConsentRequired(u.ID); err != nil {
		t.Fatalf("MarkEFBConsentRequired: %v", err)
	}

	d, err := db.GetFeedbackDiagnostics(u.ID)
	if err != nil {
		t.Fatalf("GetFeedbackDiagnostics: %v", err)
	}
	if !d.HasGarminCredentials || !d.GarminInvalid {
		t.Errorf("Garmin = %+v, want has=true invalid=true", d)
	}
	if d.GarminLastError != "auth failed" {
		t.Errorf("GarminLastError = %q, want %q", d.GarminLastError, "auth failed")
	}
	if !d.EFBConsentRequired {
		t.Errorf("EFBConsentRequired = false, want true")
	}
}
