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
		{"sailing", CategorySailing, true},
		{"sailing_v2", CategorySailing, true},
		{"sail_race", CategorySailing, true},
		{"sail_expedition", CategorySailing, true},
		{"offshore_grinding", CategorySailing, true},
		{"onshore_grinding", CategorySailing, true},
		{"windsurfing", CategoryWindsurf, true},
		{"windsurfing_v2", CategoryWindsurf, true},
		{"kitesurfing", CategoryWindsurf, true},
		{"kiteboarding", CategoryWindsurf, true},
		{"surfing", CategorySurfing, true},
		{"surfing_v2", CategorySurfing, true},
		{"wakeboarding", CategorySurfing, true},
		{"wakesurfing", CategorySurfing, true},
		{"waterskiing", CategorySurfing, true},
		{"water_tubing", CategorySurfing, true},
		{"boating", CategoryMotorboat, true},
		{"boating_v2", CategoryMotorboat, true},
		{"motorboating", CategoryMotorboat, true},
		{"cycling", "", false},
		{"running", "", false},
		{"other", "", false},
		{"", "", false},
		{"   ", "", false},
		// other_water is assigned by parent id, never by typeKey — see
		// TestCategoryForActivity.
		{"other_water", "", false},
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

// The exact typeKeys observed passing Garmin's water-sports filter in
// production (July 2026 sweep of sync_runs.type_keys_seen). These are the
// strings that actually reach the sync engine, so every one must land in a
// category the user can switch off — sailing_v2 is the reported bug.
func TestCategoryForTypeKey_ProductionTypeKeys(t *testing.T) {
	observed := map[string]string{
		"kayaking_v2":                CategoryKayak,
		"paddling_v2":                CategoryPaddle,
		"stand_up_paddleboarding_v2": CategorySUP,
		"rowing_v2":                  CategoryRowing,
		"sailing_v2":                 CategorySailing,
		"windsurfing_v2":             CategoryWindsurf,
		"boating_v2":                 CategoryMotorboat,
		"wakeboarding_v2":            CategorySurfing,
		"surfing_v2":                 CategorySurfing,
	}
	for typeKey, want := range observed {
		got, ok := CategoryForTypeKey(typeKey)
		if !ok || got != want {
			t.Errorf("CategoryForTypeKey(%q) = (%q, %v), want (%q, true)", typeKey, got, ok, want)
		}
	}
}

// windsurfing must not be swallowed by the surfing prefix, and vice versa —
// the two categories exist so a sailor and a surfer can make different calls.
func TestCategoryForTypeKey_PrefixesDoNotCollide(t *testing.T) {
	if cat, _ := CategoryForTypeKey("windsurfing"); cat != CategoryWindsurf {
		t.Errorf("windsurfing = %q, want %q", cat, CategoryWindsurf)
	}
	if cat, _ := CategoryForTypeKey("kayaking_v2"); cat != CategoryKayak {
		t.Errorf("kayaking_v2 = %q, want %q", cat, CategoryKayak)
	}
	// whitewater_rafting_kayaking starts with "whitewater", not "kayaking",
	// so it must keep its own category rather than folding into kayak.
	if cat, _ := CategoryForTypeKey("whitewater_rafting_kayaking"); cat != CategoryWhitewater {
		t.Errorf("whitewater_rafting_kayaking = %q, want %q", cat, CategoryWhitewater)
	}
}

func TestCategoryForActivity(t *testing.T) {
	cases := []struct {
		name         string
		typeKey      string
		parentTypeID int
		actName      string
		wantCat      string
		wantOK       bool
	}{
		{"known typeKey wins over parent", "kayaking_v2", WaterSportsParentTypeID, "", CategoryKayak, true},
		{"known typeKey without parent", "sailing_v2", 0, "", CategorySailing, true},
		{"unknown typeKey under water sports", "hydrofoiling_v9", WaterSportsParentTypeID, "", CategoryOtherWater, true},
		{"unknown typeKey without parent", "hydrofoiling_v9", 0, "", "", false},
		{"empty typeKey under water sports", "", WaterSportsParentTypeID, "", CategoryOtherWater, true},

		// match_by_name recoveries: typeKey is "other" under the generic
		// parent, so the name is the only thing that can classify them. If
		// these fell through as unknown they would bypass the user's
		// selection entirely.
		{"name-matched rowing", "other", GenericFitnessParentTypeID, "Rudern am Morgen", CategoryRowing, true},
		{"name-matched kayak", "other", GenericFitnessParentTypeID, "Seekajak Tour", CategoryKayak, true},
		{"name-matched SUP", "other", GenericFitnessParentTypeID, "SUP am See", CategorySUP, true},
		{"name-matched compound paddle", "other", GenericFitnessParentTypeID, "Doppelpaddel", CategoryPaddle, true},
		{"generic parent, no keyword", "other", GenericFitnessParentTypeID, "Krafttraining", "", false},
		{"generic parent, empty name", "other", GenericFitnessParentTypeID, "", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, ok := CategoryForActivity(tc.typeKey, tc.parentTypeID, tc.actName)
			if ok != tc.wantOK || cat != tc.wantCat {
				t.Errorf("CategoryForActivity(%q, %d, %q) = (%q, %v), want (%q, %v)",
					tc.typeKey, tc.parentTypeID, tc.actName, cat, ok, tc.wantCat, tc.wantOK)
			}
		})
	}
}

// "Stand Up Paddling" contains both an SUP keyword and the looser "paddl"
// fragment; SUP must win or the two categories become indistinguishable.
func TestCategoryForActivityName_SUPBeatsPaddle(t *testing.T) {
	for _, name := range []string{"Stand Up Paddling", "stand-up-paddeln", "SUP-Workshop"} {
		if cat, _ := CategoryForActivityName(name); cat != CategorySUP {
			t.Errorf("CategoryForActivityName(%q) = %q, want %q", name, cat, CategorySUP)
		}
	}
	// The word-boundary guard from the Python side must survive the port.
	if cat, ok := CategoryForActivityName("Support-Training"); ok {
		t.Errorf("CategoryForActivityName(\"Support-Training\") = %q, want no match", cat)
	}
}

// The default is the whole point of the fix: paddle sports and the catch-all
// sync, the demonstrably-not-paddling water sports do not.
func TestDefaultSelectedCategories(t *testing.T) {
	want := []string{
		CategoryKayak, CategoryCanoe, CategoryPaddle,
		CategorySUP, CategoryRowing, CategoryWhitewater,
		CategoryOtherWater,
	}
	got := DefaultSelectedCategories()
	if len(got) != len(want) {
		t.Fatalf("DefaultSelectedCategories() = %v, want %v", got, want)
	}
	for i, cat := range want {
		if got[i] != cat {
			t.Errorf("index %d = %q, want %q", i, got[i], cat)
		}
	}

	// Every default must be a real category, or the settings page renders a
	// box that can never be ticked back on.
	for _, cat := range got {
		if !IsKnownCategory(cat) {
			t.Errorf("default %q is not a known category", cat)
		}
	}

	for _, cat := range []string{CategorySailing, CategoryWindsurf, CategorySurfing, CategoryMotorboat} {
		for _, sel := range got {
			if sel == cat {
				t.Errorf("%q must be off by default — it is not paddling", cat)
			}
		}
	}

	got[0] = "mutated"
	if DefaultSelectedCategories()[0] == "mutated" {
		t.Error("DefaultSelectedCategories() aliases its backing slice; callers can corrupt it")
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
