package garmin

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestParseNameKeywords_TrimsDropsEmptyAndDedupes(t *testing.T) {
	got, err := ParseNameKeywords(" Drachenboot , outrigger,, DRACHENBOOT ,  ,Outrigger Canoe ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"Drachenboot", "outrigger", "Outrigger Canoe"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseNameKeywords_EmptyInputIsNil(t *testing.T) {
	for _, raw := range []string{"", "   ", ",,,", " , , "} {
		got, err := ParseNameKeywords(raw)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", raw, err)
		}
		if got != nil {
			t.Fatalf("%q: got %q, want nil", raw, got)
		}
	}
}

func TestParseNameKeywords_RejectsLimits(t *testing.T) {
	tooMany := make([]string, 0, MaxNameKeywords+1)
	for i := 0; i <= MaxNameKeywords; i++ {
		tooMany = append(tooMany, fmt.Sprintf("kw%d", i))
	}
	cases := map[string]string{
		"too short": "Drachenboot, x",
		"too long":  "Drachenboot, " + strings.Repeat("a", MaxNameKeywordLen+1),
		"too many":  strings.Join(tooMany, ","),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseNameKeywords(raw)
			if !errors.Is(err, ErrNameKeywordsInvalid) {
				t.Fatalf("got err %v (keywords %q), want ErrNameKeywordsInvalid", err, got)
			}
		})
	}
}

func TestParseNameKeywords_AcceptsExactlyTheLimits(t *testing.T) {
	parts := make([]string, 0, MaxNameKeywords)
	for i := 0; i < MaxNameKeywords; i++ {
		parts = append(parts, strings.Repeat(string(rune('a'+i)), MaxNameKeywordLen))
	}
	got, err := ParseNameKeywords(strings.Join(parts, ","))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != MaxNameKeywords {
		t.Fatalf("got %d keywords, want %d", len(got), MaxNameKeywords)
	}
}

func TestParseNameKeywords_LengthCountsRunesNotBytes(t *testing.T) {
	// 40 umlauts are 80 bytes but must still be accepted.
	got, err := ParseNameKeywords(strings.Repeat("ä", MaxNameKeywordLen))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %q, want one keyword", got)
	}
}

func TestNameMatchesKeywords(t *testing.T) {
	cases := []struct {
		name     string
		keywords []string
		want     bool
	}{
		{"Drachenboot Training", []string{"Drachenboot"}, true},
		{"drachenboottraining", []string{"DRACHENBOOT"}, true},
		{"Outrigger Session", []string{"Drachenboot", "outrigger"}, true},
		{"Sunday Walk", []string{"Drachenboot", "outrigger"}, false},
		{"Sunday Walk", nil, false},
		{"Sunday Walk", []string{"", "  "}, false},
		{"", []string{"Drachenboot"}, false},
	}
	for _, c := range cases {
		if got := NameMatchesKeywords(c.name, c.keywords); got != c.want {
			t.Errorf("NameMatchesKeywords(%q, %q) = %v, want %v", c.name, c.keywords, got, c.want)
		}
	}
}
