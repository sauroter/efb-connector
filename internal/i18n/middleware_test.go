package i18n

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubProvider struct {
	pref string
}

func (s stubProvider) GetUserPreferredLang(*http.Request) string { return s.pref }

func TestFromContext_Default(t *testing.T) {
	if l := FromContext(context.Background()); l != EN {
		t.Errorf("FromContext default = %q, want EN", l)
	}
}

func TestWithLangAndFromContext(t *testing.T) {
	ctx := WithLang(context.Background(), DE)
	if l := FromContext(ctx); l != DE {
		t.Errorf("FromContext after WithLang(DE) = %q, want DE", l)
	}
}

func TestMiddleware_UserPreferenceWins(t *testing.T) {
	var seen Lang
	h := Middleware(stubProvider{pref: "de"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "lang", Value: "en"})
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if seen != DE {
		t.Errorf("seen = %q, want DE (user pref wins)", seen)
	}
	if got := rr.Header().Get("Content-Language"); got != "de" {
		t.Errorf("Content-Language header = %q, want de", got)
	}
}

func TestMiddleware_CookieOverridesHeader(t *testing.T) {
	var seen Lang
	h := Middleware(stubProvider{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "lang", Value: "de"})
	req.Header.Set("Accept-Language", "en-US")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if seen != DE {
		t.Errorf("seen = %q, want DE", seen)
	}
}

func TestMiddleware_AcceptLanguageGerman(t *testing.T) {
	var seen Lang
	h := Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Language", "de-DE,de;q=0.9,en;q=0.5")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if seen != DE {
		t.Errorf("seen = %q, want DE", seen)
	}
}

func TestMiddleware_DefaultsToEnglish(t *testing.T) {
	var seen Lang
	h := Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if seen != EN {
		t.Errorf("seen = %q, want EN", seen)
	}
}

func TestAcceptsGerman(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"en-US", false},
		{"de", true},
		{"de-AT,en;q=0.5", true},
		{"en-US,de;q=0.5", false},
		{"  de-DE  ;q=0.9, en;q=0.5", true},
	}
	for _, tc := range cases {
		if got := acceptsGerman(tc.header); got != tc.want {
			t.Errorf("acceptsGerman(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestMiddleware_CookieEnglishWinsOverHeader(t *testing.T) {
	var seen Lang
	h := Middleware(stubProvider{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "lang", Value: "en"})
	req.Header.Set("Accept-Language", "de-DE")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if seen != EN {
		t.Errorf("seen = %q, want EN (cookie overrides header)", seen)
	}
}
