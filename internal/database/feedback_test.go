package database

import (
	"testing"
)

func TestCreateAndGetFeedback(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("fb@example.com")

	if err := db.CreateFeedback(u.ID, "bug", "something broke"); err != nil {
		t.Fatalf("CreateFeedback: %v", err)
	}
	if err := db.CreateFeedback(u.ID, "idea", "make it faster"); err != nil {
		t.Fatalf("CreateFeedback: %v", err)
	}

	got, err := db.GetAllFeedback(10)
	if err != nil {
		t.Fatalf("GetAllFeedback: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	for _, fb := range got {
		if fb.UserEmail != "fb@example.com" {
			t.Errorf("UserEmail = %q", fb.UserEmail)
		}
		if fb.UserID != u.ID {
			t.Errorf("UserID = %d, want %d", fb.UserID, u.ID)
		}
		if fb.Category != "bug" && fb.Category != "idea" {
			t.Errorf("unexpected category %q", fb.Category)
		}
	}
}

func TestGetAllFeedback_LimitsOrdering(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("fb-limit@example.com")

	for i := 0; i < 5; i++ {
		if err := db.CreateFeedback(u.ID, "bug", "msg"); err != nil {
			t.Fatalf("CreateFeedback: %v", err)
		}
	}

	got, err := db.GetAllFeedback(3)
	if err != nil {
		t.Fatalf("GetAllFeedback: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("limit not honored: got %d, want 3", len(got))
	}
}

func TestCountFeedbackToday(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("fb-count@example.com")

	n, err := db.CountFeedbackToday(u.ID)
	if err != nil {
		t.Fatalf("CountFeedbackToday: %v", err)
	}
	if n != 0 {
		t.Errorf("initial count = %d, want 0", n)
	}

	_ = db.CreateFeedback(u.ID, "bug", "a")
	_ = db.CreateFeedback(u.ID, "bug", "b")

	n, err = db.CountFeedbackToday(u.ID)
	if err != nil {
		t.Fatalf("CountFeedbackToday: %v", err)
	}
	if n != 2 {
		t.Errorf("count after 2 inserts = %d, want 2", n)
	}

	// Other users' feedback should not count.
	u2, _ := db.CreateUser("other@example.com")
	_ = db.CreateFeedback(u2.ID, "bug", "x")

	n, _ = db.CountFeedbackToday(u.ID)
	if n != 2 {
		t.Errorf("count after other-user insert = %d, want 2", n)
	}
}
