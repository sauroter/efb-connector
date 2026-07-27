package garmin

import (
	"regexp"
	"strings"
)

// WaterSportsParentTypeID is Garmin's "Water Sports" category id. It mirrors
// WATER_SPORTS_PARENT_TYPE_ID in scripts/garmin_fetch.py, which uses it as the
// admission filter for the whole sync.
//
// Note what that category actually contains: not just paddle sports, but
// sailing, windsurfing, motorboating and wakeboarding too. Everything under it
// reaches the sync engine, so every one of those needs a category below —
// otherwise it uploads to a canoe logbook unconditionally, which is exactly how
// sailing leaked into eFB.
//
// Membership below was established empirically in July 2026 rather than
// guessed, by cross-referencing sync_runs.type_keys_seen (recorded pre-filter)
// against activities_found (recorded post-filter) across the whole user base.
// A typeKey that appears in runs which found activities is inside 228; one that
// only ever appears in runs finding nothing is outside it:
//
//	inside  228: kayaking_v2, paddling_v2, stand_up_paddleboarding_v2,
//	             rowing_v2, sailing_v2, windsurfing_v2, boating_v2,
//	             wakeboarding_v2, surfing_v2
//	outside 228: open_water_swimming, lap_swimming, swimming, apnea_diving,
//	             single_gas_diving, indoor_rowing, water_sports
//
// Swimming and diving are the surprises worth remembering: Garmin files both
// under their own parents, so they never reach this code and must NOT get
// categories — a checkbox that can never match anything is worse than none.
const WaterSportsParentTypeID = 228

// GenericFitnessParentTypeID is Garmin's catch-all parent (mirrors
// GENERIC_FITNESS_PARENT_TYPE_ID in scripts/garmin_fetch.py). Activities the
// opt-in match_by_name fallback recovers are filed here with typeKey "other" —
// the Venu 3 "Sonstiges" case — so they can only be categorised by name.
const GenericFitnessParentTypeID = 17

// Water-sport category keys exposed to the per-user activity-type filter
// (users.selected_activity_types). Keys are stable identifiers — the
// human-readable labels live in internal/i18n.
const (
	// Paddle sports — the core of what a Kanu logbook is for.
	CategoryKayak      = "kayak"
	CategoryCanoe      = "canoe"
	CategoryPaddle     = "paddle"
	CategorySUP        = "sup"
	CategoryRowing     = "rowing"
	CategoryWhitewater = "whitewater"

	// The rest of Garmin's Water Sports category: demonstrably not paddling,
	// so off by default. A user who does want their sailing in eFB ticks the
	// box; nobody has to notice a leak to stop one.
	CategorySailing   = "sailing"
	CategoryWindsurf  = "windsurf"
	CategorySurfing   = "surfing"
	CategoryMotorboat = "motorboat"

	// CategoryOtherWater is a catch-all rather than a Garmin type: it claims
	// any activity filed under WaterSportsParentTypeID whose typeKey matches
	// no prefix below. It exists so that a water-sport type Garmin adds after
	// this code was written lands in a box the user can untick, instead of
	// repeating the sailing leak.
	//
	// On by default, unlike the named non-paddle categories above. We can't
	// tell whether an unrecognised water sport is paddling, so dropping it
	// would silently lose real trips — the failure mode this connector exists
	// to avoid. Visible-but-wrong beats missing.
	CategoryOtherWater = "other_water"
)

// KnownCategories is the stable display order for the activity-type filter
// UI. Adding a new category here automatically surfaces it in the settings
// page; the i18n bundles must gain a matching activity_type.<key> label.
//
// Order matters twice over: it is the checkbox order on /settings, and it is
// the match order in [CategoryForTypeKey]. Paddle sports come first because
// they are what most users are here for; other_water is last because it only
// ever matches what nothing else did.
var KnownCategories = []string{
	CategoryKayak,
	CategoryCanoe,
	CategoryPaddle,
	CategorySUP,
	CategoryRowing,
	CategoryWhitewater,
	CategorySailing,
	CategoryWindsurf,
	CategorySurfing,
	CategoryMotorboat,
	CategoryOtherWater,
}

// categoryPrefixes maps each stable category key to the Garmin typeKey
// prefixes that belong to it. Prefix match catches future variants like
// rowing_v2 / kayaking_indoor without code changes, and nested types such as
// sail_race / sail_expedition, which Garmin files under sailing_v2 rather than
// directly under the water-sports parent.
//
// Swimming and diving are deliberately absent — see the membership note on
// [WaterSportsParentTypeID]. Should Garmin ever move them, they arrive as
// [CategoryOtherWater] and stay switchable.
var categoryPrefixes = map[string][]string{
	CategoryKayak:      {"kayaking"},
	CategoryCanoe:      {"canoeing"},
	CategoryPaddle:     {"paddling"},
	CategorySUP:        {"stand_up_paddleboarding"},
	CategoryRowing:     {"rowing"},
	CategoryWhitewater: {"whitewater_rafting_kayaking"},
	CategorySailing:    {"sail", "offshore_grinding", "onshore_grinding"},
	CategoryWindsurf:   {"windsurf", "kitesurf", "kiteboarding"},
	CategorySurfing:    {"surfing", "wakeboard", "wakesurf", "waterski", "water_ski", "water_tubing"},
	CategoryMotorboat:  {"boating", "motorboating"},

	// No prefixes by design — [CategoryForActivity] assigns this one by
	// parent id. The entry must exist so [IsKnownCategory] accepts it when
	// the settings form posts it back.
	CategoryOtherWater: {},
}

// defaultSelected is the subset of [KnownCategories] that syncs unless the
// user says otherwise: the paddle sports this connector exists for, plus the
// [CategoryOtherWater] catch-all.
//
// Everything omitted here is a water sport Garmin files next to paddling but
// which has no business in a canoe logbook by default. Kept as its own list
// rather than a flag on each category so the default is legible in one place —
// this is the answer to "why did my sailing sync?", and it should not require
// reading a table to find.
var defaultSelected = []string{
	CategoryKayak,
	CategoryCanoe,
	CategoryPaddle,
	CategorySUP,
	CategoryRowing,
	CategoryWhitewater,
	CategoryOtherWater,
}

// IsKnownCategory reports whether key is one of [KnownCategories]. Settings
// handlers use this to reject unknown values posted from the filter form.
func IsKnownCategory(key string) bool {
	_, ok := categoryPrefixes[key]
	return ok
}

// DefaultSelectedCategories returns the category set a brand-new user starts
// with — see [defaultSelected]. Returned as a fresh slice so callers can't
// mutate it.
//
// Migration 0014 applies the same default to existing users, minus anything
// they had already excluded, so this list and that migration's key list must
// agree.
func DefaultSelectedCategories() []string {
	out := make([]string, len(defaultSelected))
	copy(out, defaultSelected)
	return out
}

// CategoryForTypeKey maps a Garmin activity typeKey to one of the known
// water-sport categories. Returns ("", false) for unrecognised typeKeys so
// the caller can keep them (conservative default: never silently drop
// activities under an unrecognised typeKey).
//
// Prefer [CategoryForActivity] when the parent type id is available — it also
// catches water-sport types that postdate this table.
func CategoryForTypeKey(typeKey string) (string, bool) {
	tk := strings.ToLower(strings.TrimSpace(typeKey))
	if tk == "" {
		return "", false
	}
	for _, cat := range KnownCategories {
		for _, prefix := range categoryPrefixes[cat] {
			if strings.HasPrefix(tk, prefix) {
				return cat, true
			}
		}
	}
	return "", false
}

// nameCategoryPatterns maps an activity *name* to a category, for activities
// the match_by_name fallback recovered from Garmin's generic-fitness parent.
// Those arrive with typeKey "other", so the typeKey table can say nothing
// about them — the name is the only signal, and it is the same signal
// scripts/garmin_fetch.py used to admit them in the first place.
//
// Deliberately mirrors WATER_SPORT_NAME_PATTERN in that script: if the two
// drift, an activity gets admitted there and then classified as something
// else here. Order matters — first match wins, and the SUP alternatives are
// checked before the looser paddle fragments so "Stand Up Paddling" is not
// claimed by [CategoryPaddle].
//
// The stand-up alternative ends at "padd" rather than the script's "paddl",
// deliberately: German "Stand-Up-Paddeln" has an e the script's fragment
// misses. There it costs nothing (a bare "paddel" alternative admits the
// activity anyway) but here it would file a SUP session under paddling.
var nameCategoryPatterns = []struct {
	category string
	pattern  *regexp.Regexp
}{
	{CategorySUP, regexp.MustCompile(`(?i)\bsup\b|stand[- ]?up[- ]?padd`)},
	{CategoryKayak, regexp.MustCompile(`(?i)kajak|kayak`)},
	{CategoryCanoe, regexp.MustCompile(`(?i)kanu|canoe`)},
	{CategoryRowing, regexp.MustCompile(`(?i)rudern|rowing`)},
	{CategoryPaddle, regexp.MustCompile(`(?i)paddel|paddl`)},
}

// CategoryForActivityName maps a name-matched activity to a category. Returns
// ("", false) when no keyword matches, which the caller treats as "keep" —
// same conservative default as an unrecognised typeKey.
func CategoryForActivityName(name string) (string, bool) {
	for _, p := range nameCategoryPatterns {
		if p.pattern.MatchString(name) {
			return p.category, true
		}
	}
	return "", false
}

// CategoryForActivity maps an activity to a category using its typeKey, then
// its Garmin parent type id, then — for activities the name fallback let in —
// its name. Anything Garmin filed under Water Sports is a water sport by
// definition, so an unrecognised typeKey there is [CategoryOtherWater] rather
// than "unknown".
//
// The name step exists because match_by_name activities carry typeKey "other"
// under the generic-fitness parent, so neither of the first two steps says
// anything about them. Without it they bypass the user's selection entirely
// and unticking a category silently does nothing for exactly the users who
// needed the fallback — see scripts/garmin_fetch.py:name_matches_water_sport.
//
// Activities matching none of the three still return ("", false) and are kept
// by the caller — a paddling activity carrying some typeKey we've never seen
// must never be dropped just because this table is out of date.
func CategoryForActivity(typeKey string, parentTypeID int, name string) (string, bool) {
	if cat, ok := CategoryForTypeKey(typeKey); ok {
		return cat, true
	}
	if parentTypeID == WaterSportsParentTypeID {
		return CategoryOtherWater, true
	}
	if parentTypeID == GenericFitnessParentTypeID {
		return CategoryForActivityName(name)
	}
	return "", false
}
