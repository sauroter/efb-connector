// Package i18n provides lightweight internationalization for the web UI.
// Translations are stored as Go maps (en.go, de.go) with no external dependencies.
package i18n

import "context"

// Lang represents a supported language.
type Lang string

const (
	EN Lang = "en"
	DE Lang = "de"
)

type contextKey string

const langKey contextKey = "lang"

// FromContext returns the language stored in the context, defaulting to EN.
func FromContext(ctx context.Context) Lang {
	if l, ok := ctx.Value(langKey).(Lang); ok {
		return l
	}
	return EN
}

// WithLang stores a language in the context.
func WithLang(ctx context.Context, l Lang) context.Context {
	return context.WithValue(ctx, langKey, l)
}

// T returns the translation for the given key in the given language.
// Falls back to English if not found in the target language,
// then to the raw key if not found in English either.
func T(lang Lang, key string) string {
	var m map[string]string
	switch lang {
	case DE:
		m = De
	default:
		m = En
	}
	if v, ok := m[key]; ok {
		return v
	}
	// Fallback to English.
	if v, ok := En[key]; ok {
		return v
	}
	return key
}

// ParseLang parses a language string, returning EN for unknown values.
func ParseLang(s string) Lang {
	switch s {
	case "de":
		return DE
	default:
		return EN
	}
}
