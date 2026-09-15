package garmin

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Limits on users.name_keywords. Generous for a list of sport names, tight
// enough that a pasted paragraph is rejected rather than stored.
const (
	MaxNameKeywords   = 20
	MinNameKeywordLen = 2 // runes; a single letter would match almost every name
	MaxNameKeywordLen = 40
)

// ErrNameKeywordsInvalid is returned (wrapped, with detail) by
// ParseNameKeywords when the input violates one of the limits above.
var ErrNameKeywordsInvalid = errors.New("invalid name keywords")

// ParseNameKeywords turns the comma-separated text a user typed into the
// settings form into the list stored on users.name_keywords: entries are
// trimmed, empties dropped, duplicates removed case-insensitively (first
// spelling wins), and each survivor must be MinNameKeywordLen..
// MaxNameKeywordLen runes long with at most MaxNameKeywords in total.
// Empty input yields (nil, nil).
func ParseNameKeywords(raw string) ([]string, error) {
	var out []string
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		kw := strings.TrimSpace(part)
		if kw == "" {
			continue
		}
		if n := utf8.RuneCountInString(kw); n < MinNameKeywordLen || n > MaxNameKeywordLen {
			return nil, fmt.Errorf("%w: %q must be %d–%d characters", ErrNameKeywordsInvalid, kw, MinNameKeywordLen, MaxNameKeywordLen)
		}
		key := strings.ToLower(kw)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, kw)
		if len(out) > MaxNameKeywords {
			return nil, fmt.Errorf("%w: more than %d keywords", ErrNameKeywordsInvalid, MaxNameKeywords)
		}
	}
	return out, nil
}

// NameMatchesKeywords reports whether name contains any of the user's
// keywords as a case-insensitive substring. Blank keywords never match.
//
// This is the Go mirror of the extra_keywords branch in
// scripts/garmin_fetch.py:name_matches_water_sport, and must stay a plain
// substring test like it: the script decides what is admitted, this decides
// what bypasses the category selection in the sync engine, and the two must
// agree on the same activities. The caller applies the parent-17 guard.
//
// Known, accepted drift: Go's strings.ToLower and Python's str.lower differ
// on a handful of code points (Turkish dotted İ folds to one code point here
// and two there), so a name containing one of those could be admitted by the
// script yet not exempted from the selection. Irrelevant for the German and
// English names this serves; noted so nobody "fixes" one side alone.
func NameMatchesKeywords(name string, keywords []string) bool {
	if name == "" {
		return false
	}
	lowered := strings.ToLower(name)
	for _, kw := range keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if strings.Contains(lowered, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}
