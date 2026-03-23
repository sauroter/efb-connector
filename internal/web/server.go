// Package web implements the HTTP server, routes, and handlers for the
// efb-connector multi-tenant web UI.
package web

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"efb-connector/internal/auth"
	"efb-connector/internal/database"
	"efb-connector/internal/efb"
	"efb-connector/internal/garmin"
	"efb-connector/internal/metrics"
	"efb-connector/internal/sync"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server holds all dependencies needed by the HTTP handlers.
type Server struct {
	db             *database.DB
	auth           *auth.AuthService
	syncEngine     *sync.SyncEngine
	garmin         garmin.GarminProvider
	efb            efb.EFBProvider
	rateLimiter    *auth.RateLimiter
	internalSecret string
	configBaseURL  string // e.g. "https://efb-connector.fly.dev" (may be empty)
	logger         *slog.Logger
	templates      *template.Template
	version        string
}

// ServerDeps bundles the dependencies required to construct a Server.
type ServerDeps struct {
	DB             *database.DB
	Auth           *auth.AuthService
	SyncEngine     *sync.SyncEngine
	Garmin         garmin.GarminProvider
	EFB            efb.EFBProvider
	RateLimiter    *auth.RateLimiter
	InternalSecret string
	BaseURL        string // configured base URL (e.g. "https://efb-connector.fly.dev")
	Logger         *slog.Logger
	TemplatesDir   string // path to the templates/ directory
	Version        string // build version (set via ldflags)
}

// NewServer creates a Server with the given dependencies and parses all
// templates from the templates directory.
func NewServer(deps ServerDeps) (*Server, error) {
	tmpl, err := parseTemplates(deps.TemplatesDir, deps.Version)
	if err != nil {
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}

	metrics.RegisterDBGauges(deps.DB)

	return &Server{
		db:             deps.DB,
		auth:           deps.Auth,
		syncEngine:     deps.SyncEngine,
		garmin:         deps.Garmin,
		efb:            deps.EFB,
		rateLimiter:    deps.RateLimiter,
		internalSecret: deps.InternalSecret,
		configBaseURL:  deps.BaseURL,
		logger:         deps.Logger,
		templates:      tmpl,
		version:        deps.Version,
	}, nil
}

// Routes returns the fully-configured HTTP handler with all routes registered
// and wrapped in logging + recovery middleware.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// ── Static files ──
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/favicon.svg")
	})

	// ── Public routes (no auth required) ──
	mux.HandleFunc("GET /", s.handleLanding)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("GET /auth/verify", s.handleVerifyMagicLink)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	mux.HandleFunc("GET /impressum", s.handleImpressum)
	mux.HandleFunc("GET /privacy", s.handlePrivacy)

	// ── Authenticated routes (wrapped in RequireAuth + CSRFProtect) ──
	protect := func(h http.HandlerFunc) http.Handler {
		return s.auth.RequireAuth(s.auth.CSRFProtect(h))
	}
	mux.Handle("GET /dashboard", protect(s.handleDashboard))
	mux.Handle("GET /settings/garmin", protect(s.handleGarminSettingsForm))
	mux.Handle("POST /settings/garmin", protect(s.handleGarminSettingsSave))
	mux.Handle("POST /settings/garmin/delete", protect(s.handleGarminSettingsDelete))
	mux.Handle("GET /settings/efb", protect(s.handleEFBSettingsForm))
	mux.Handle("POST /settings/efb", protect(s.handleEFBSettingsSave))
	mux.Handle("POST /settings/efb/delete", protect(s.handleEFBSettingsDelete))
	mux.Handle("POST /settings/auto-create-trips", protect(s.handleAutoCreateTripsSave))
	mux.Handle("POST /account/delete", protect(s.handleAccountDelete))
	mux.Handle("POST /sync/trigger", protect(s.handleSyncTrigger))
	mux.Handle("GET /sync/status", protect(s.handleSyncStatus))
	mux.Handle("GET /sync/history", protect(s.handleSyncHistory))

	// ── Internal / admin routes ──
	mux.HandleFunc("POST /internal/sync/run-all", s.handleInternalSyncAll)
	mux.HandleFunc("GET /internal/admin/status", s.handleAdminStatus)
	mux.HandleFunc("GET /internal/admin/users", s.handleAdminUsers)
	mux.HandleFunc("GET /internal/admin/users/{id}/sync-history", s.handleAdminUserSyncHistory)
	mux.HandleFunc("POST /internal/admin/users/{id}/sync", s.handleAdminUserSync)
	mux.HandleFunc("GET /internal/admin/errors", s.handleAdminErrors)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.Handle("GET /metrics", promhttp.Handler())

	// Wrap the entire mux in logging + recovery middleware.
	return s.recovery(s.logging(mux))
}

// render executes the named template with data and writes the result to w.
// On error it logs the failure and sends a 500 response.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("template render failed", "template", name, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// flash reads and clears the "flash" cookie, returning its value (or "").
func flash(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie("flash")
	if err != nil {
		return ""
	}
	// Clear the cookie immediately.
	http.SetCookie(w, &http.Cookie{
		Name:   "flash",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	return cookie.Value
}

// setFlash sets a one-time flash message cookie.
func setFlash(w http.ResponseWriter, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "flash",
		Value:    msg,
		Path:     "/",
		MaxAge:   60, // generous: should be consumed on the next page load
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// garminTokenStorePath returns the per-user Garmin token store directory,
// creating it if it doesn't exist. Uses /data/garmin_tokens/<userID> if /data
// exists (Fly.io), otherwise ~/.config/efb-connector/garmin_tokens/<userID>.
func (s *Server) garminTokenStorePath(userID int64) string {
	var base string
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		base = "/data/garmin_tokens"
	} else {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config", "efb-connector", "garmin_tokens")
	}
	dir := filepath.Join(base, fmt.Sprintf("%d", userID))
	if err := os.MkdirAll(dir, 0700); err != nil {
		s.logger.Error("failed to create garmin token store", "user_id", userID, "error", err)
	}
	return dir
}

// parseTemplates loads all templates from the given directory.
func parseTemplates(dir string, version string) (*template.Template, error) {
	tmpl := template.New("").Funcs(template.FuncMap{
		"version": func() string { return version },
	})

	// Parse partials first so they are available to top-level templates.
	partials := filepath.Join(dir, "partials", "*.html")
	if matches, _ := filepath.Glob(partials); len(matches) > 0 {
		if _, err := tmpl.ParseGlob(partials); err != nil {
			return nil, err
		}
	}

	// Parse top-level templates.
	pages := filepath.Join(dir, "*.html")
	if matches, _ := filepath.Glob(pages); len(matches) > 0 {
		if _, err := tmpl.ParseGlob(pages); err != nil {
			return nil, err
		}
	}

	return tmpl, nil
}
