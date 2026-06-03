package garmin

import "strings"

// Water-sport category keys exposed to the per-user activity-type filter
// (users.excluded_activity_types). Keys are stable identifiers — the
// human-readable labels live in internal/i18n.
const (
	CategoryKayak      = "kayak"
	CategoryCanoe      = "canoe"
	CategoryPaddle     = "paddle"
	CategorySUP        = "sup"
	CategoryRowing     = "rowing"
	CategoryWhitewater = "whitewater"
)

// KnownCategories is the stable display order for the activity-type filter
// UI. Adding a new category here automatically surfaces it in the settings
// page; the i18n bundles must gain a matching activity_type.<key> label.
var KnownCategories = []string{
	CategoryKayak,
	CategoryCanoe,
	CategoryPaddle,
	CategorySUP,
	CategoryRowing,
	CategoryWhitewater,
}

// categoryPrefixes maps each stable category key to the Garmin typeKey
// prefixes that belong to it. Prefix match catches future variants like
// rowing_v2 / kayaking_indoor without code changes.
var categoryPrefixes = map[string][]string{
	CategoryKayak:      {"kayaking"},
	CategoryCanoe:      {"canoeing"},
	CategoryPaddle:     {"paddling"},
	CategorySUP:        {"stand_up_paddleboarding"},
	CategoryRowing:     {"rowing"},
	CategoryWhitewater: {"whitewater_rafting_kayaking"},
}

// IsKnownCategory reports whether key is one of [KnownCategories]. Settings
// handlers use this to reject unknown values posted from the filter form.
func IsKnownCategory(key string) bool {
	_, ok := categoryPrefixes[key]
	return ok
}

// CategoryForTypeKey maps a Garmin activity typeKey to one of the known
// water-sport categories. Returns ("", false) for unrecognised typeKeys so
// the caller can keep them (conservative default: never silently drop
// activities under an unrecognised typeKey).
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
