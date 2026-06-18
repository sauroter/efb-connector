package garmin

import "testing"

func TestCategoryForTypeKey(t *testing.T) {
	cases := []struct {
		typeKey string
		wantCat string
		wantOK  bool
	}{
		{"kayaking", CategoryKayak, true},
		{"kayaking_v2", CategoryKayak, true},
		{"KAYAKING", CategoryKayak, true},
		{"canoeing", CategoryCanoe, true},
		{"paddling", CategoryPaddle, true},
		{"paddling_v2", CategoryPaddle, true},
		{"stand_up_paddleboarding", CategorySUP, true},
		{"stand_up_paddleboarding_v2", CategorySUP, true},
		{"rowing", CategoryRowing, true},
		{"rowing_v2", CategoryRowing, true},
		{"whitewater_rafting_kayaking", CategoryWhitewater, true},
		{"cycling", "", false},
		{"running", "", false},
		{"other", "", false},
		{"", "", false},
		{"   ", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.typeKey, func(t *testing.T) {
			cat, ok := CategoryForTypeKey(tc.typeKey)
			if ok != tc.wantOK || cat != tc.wantCat {
				t.Errorf("CategoryForTypeKey(%q) = (%q, %v), want (%q, %v)",
					tc.typeKey, cat, ok, tc.wantCat, tc.wantOK)
			}
		})
	}
}

func TestIsKnownCategory(t *testing.T) {
	for _, cat := range KnownCategories {
		if !IsKnownCategory(cat) {
			t.Errorf("IsKnownCategory(%q) = false, want true", cat)
		}
	}
	if IsKnownCategory("bogus") {
		t.Errorf("IsKnownCategory(\"bogus\") = true, want false")
	}
	if IsKnownCategory("") {
		t.Errorf("IsKnownCategory(\"\") = true, want false")
	}
}
