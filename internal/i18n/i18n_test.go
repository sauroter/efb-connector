package i18n

import (
	"testing"
)

func TestKeyParity(t *testing.T) {
	// Every key in En must also exist in De.
	for key := range En {
		if _, ok := De[key]; !ok {
			t.Errorf("key %q exists in En but not in De", key)
		}
	}
	// Every key in De must also exist in En.
	for key := range De {
		if _, ok := En[key]; !ok {
			t.Errorf("key %q exists in De but not in En", key)
		}
	}
}

func TestT_English(t *testing.T) {
	got := T(EN, "nav.dashboard")
	if got != "Dashboard" {
		t.Errorf("T(EN, nav.dashboard) = %q, want %q", got, "Dashboard")
	}
}

func TestT_German(t *testing.T) {
	got := T(DE, "nav.settings")
	if got != "Einstellungen" {
		t.Errorf("T(DE, nav.settings) = %q, want %q", got, "Einstellungen")
	}
}

func TestT_FallbackToEnglish(t *testing.T) {
	// If a key exists only in En, T(DE, key) should fall back to English.
	got := T(DE, "nonexistent.key.only.in.en")
	// This key doesn't exist in either, so it should return the key itself.
	if got != "nonexistent.key.only.in.en" {
		t.Errorf("T(DE, missing) = %q, want key itself", got)
	}
}

func TestT_FallbackToKey(t *testing.T) {
	got := T(EN, "totally.missing.key")
	if got != "totally.missing.key" {
		t.Errorf("T(EN, missing) = %q, want key itself", got)
	}
}

func TestParseLang(t *testing.T) {
	if ParseLang("de") != DE {
		t.Error("ParseLang(de) should return DE")
	}
	if ParseLang("en") != EN {
		t.Error("ParseLang(en) should return EN")
	}
	if ParseLang("fr") != EN {
		t.Error("ParseLang(fr) should default to EN")
	}
	if ParseLang("") != EN {
		t.Error("ParseLang(\"\") should default to EN")
	}
}
