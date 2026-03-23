package i18n

import (
	"net/http"
	"strings"
)

// UserLangProvider loads a user's preferred language from the database.
// Returns empty string if user not found or no preference set.
type UserLangProvider interface {
	// GetUserPreferredLang returns the preferred_lang for the user identified
	// by the session cookie. Returns "" if not logged in or no preference set.
	GetUserPreferredLang(r *http.Request) string
}

// Middleware detects the user's language and stores it in the request context.
// Detection order: DB user preference → "lang" cookie → Accept-Language header → EN.
func Middleware(provider UserLangProvider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lang := detect(r, provider)
			w.Header().Set("Content-Language", string(lang))
			ctx := WithLang(r.Context(), lang)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func detect(r *http.Request, provider UserLangProvider) Lang {
	// 1. User DB preference (if logged in).
	if provider != nil {
		if pref := provider.GetUserPreferredLang(r); pref != "" {
			return ParseLang(pref)
		}
	}

	// 2. "lang" cookie.
	if c, err := r.Cookie("lang"); err == nil {
		switch c.Value {
		case "de":
			return DE
		case "en":
			return EN
		}
	}

	// 3. Accept-Language header.
	if acceptsGerman(r.Header.Get("Accept-Language")) {
		return DE
	}

	return EN
}

// acceptsGerman checks if the Accept-Language header prefers German.
// Simple heuristic: returns true if "de" appears before "en" or if "de" is
// present and "en" is not.
func acceptsGerman(header string) bool {
	header = strings.ToLower(header)
	deIdx := strings.Index(header, "de")
	if deIdx < 0 {
		return false
	}
	enIdx := strings.Index(header, "en")
	if enIdx < 0 {
		return true // de present, en not
	}
	return deIdx < enIdx
}
