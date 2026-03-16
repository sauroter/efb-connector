package web

import (
	"context"
	"net/http"
	"strings"

	"efb-connector/internal/auth"
	"efb-connector/internal/garmin"
)

// handleDashboard renders the main dashboard showing connection status, last
// sync run, and any warnings.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	user, err := s.db.GetUserByID(userID)
	if err != nil || user == nil {
		s.logger.Error("failed to load user", "user_id", userID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Check Garmin credentials status.
	garminConnected := false
	_, _, garminErr := s.db.GetGarminCredentials(userID)
	if garminErr == nil {
		garminConnected = true
	}

	// Check EFB credentials status.
	efbConnected := false
	_, _, efbErr := s.db.GetEFBCredentials(userID)
	if efbErr == nil {
		efbConnected = true
	}

	// Get the most recent sync run.
	syncRuns, err := s.db.GetSyncHistory(userID, 1)
	if err != nil {
		s.logger.Error("failed to load sync history", "user_id", userID, "error", err)
	}

	var lastSync map[string]any
	if len(syncRuns) > 0 {
		run := syncRuns[0]
		lastSync = map[string]any{
			"ID":                run.ID,
			"Status":            run.Status,
			"StartedAt":         run.StartedAt,
			"FinishedAt":        run.FinishedAt,
			"ActivitiesFound":   run.ActivitiesFound,
			"ActivitiesSynced":  run.ActivitiesSynced,
			"ActivitiesSkipped": run.ActivitiesSkipped,
			"ActivitiesFailed":  run.ActivitiesFailed,
			"ErrorMessage":      run.ErrorMessage,
		}
	}

	s.render(w, "dashboard.html", map[string]any{
		"Flash":           flash(w, r),
		"CSRFToken":       s.auth.CSRFToken(r),
		"User":            user,
		"GarminConnected": garminConnected,
		"EFBConnected":    efbConnected,
		"LastSync":        lastSync,
	})
}

// handleGarminSettingsForm renders the Garmin credentials form.
func (s *Server) handleGarminSettingsForm(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Check if credentials exist (without exposing them).
	connected := false
	email, _, err := s.db.GetGarminCredentials(userID)
	if err == nil {
		connected = true
	}

	s.render(w, "settings_garmin.html", map[string]any{
		"Flash":     flash(w, r),
		"CSRFToken": s.auth.CSRFToken(r),
		"Connected": connected,
		"Email":     email,
	})
}

// handleGarminSettingsSave validates Garmin credentials via the GarminProvider,
// encrypts them, and stores them in the database.
func (s *Server) handleGarminSettingsSave(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	if email == "" || password == "" {
		setFlash(w, "Email and password are required.")
		http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
		return
	}

	// Validate credentials with the Garmin provider.
	tokenStorePath := s.garminTokenStorePath(userID)
	creds := garmin.GarminCredentials{
		Email:          email,
		Password:       password,
		TokenStorePath: tokenStorePath,
	}
	if err := s.garmin.ValidateCredentials(context.Background(), creds); err != nil {
		s.logger.Warn("garmin credential validation failed", "user_id", userID, "error", err)
		setFlash(w, "Garmin credentials are invalid. Please check and try again.")
		http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
		return
	}

	// Save encrypted credentials.
	if err := s.db.SaveGarminCredentials(userID, email, password); err != nil {
		s.logger.Error("failed to save garmin credentials", "user_id", userID, "error", err)
		setFlash(w, "Failed to save credentials. Please try again.")
		http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
		return
	}

	s.logger.Info("garmin credentials saved", "user_id", userID)
	setFlash(w, "Garmin credentials saved successfully.")
	http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
}

// handleGarminSettingsDelete removes the user's Garmin credentials.
func (s *Server) handleGarminSettingsDelete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := s.db.DeleteGarminCredentials(userID); err != nil {
		s.logger.Error("failed to delete garmin credentials", "user_id", userID, "error", err)
		setFlash(w, "Failed to delete credentials.")
		http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
		return
	}

	s.logger.Info("garmin credentials deleted", "user_id", userID)
	setFlash(w, "Garmin credentials removed.")
	http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
}

// handleEFBSettingsForm renders the EFB credentials form.
func (s *Server) handleEFBSettingsForm(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	connected := false
	username, _, err := s.db.GetEFBCredentials(userID)
	if err == nil {
		connected = true
	}

	s.render(w, "settings_efb.html", map[string]any{
		"Flash":     flash(w, r),
		"CSRFToken": s.auth.CSRFToken(r),
		"Connected": connected,
		"Username":  username,
	})
}

// handleEFBSettingsSave validates EFB credentials, encrypts, and stores them.
func (s *Server) handleEFBSettingsSave(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	if username == "" || password == "" {
		setFlash(w, "Username and password are required.")
		http.Redirect(w, r, "/settings/efb", http.StatusSeeOther)
		return
	}

	// Validate credentials against the EFB portal.
	if err := s.efb.ValidateCredentials(context.Background(), username, password); err != nil {
		s.logger.Warn("efb credential validation failed", "user_id", userID, "error", err)
		setFlash(w, "EFB credentials are invalid. Please check and try again.")
		http.Redirect(w, r, "/settings/efb", http.StatusSeeOther)
		return
	}

	// Save encrypted credentials.
	if err := s.db.SaveEFBCredentials(userID, username, password); err != nil {
		s.logger.Error("failed to save efb credentials", "user_id", userID, "error", err)
		setFlash(w, "Failed to save credentials. Please try again.")
		http.Redirect(w, r, "/settings/efb", http.StatusSeeOther)
		return
	}

	s.logger.Info("efb credentials saved", "user_id", userID)
	setFlash(w, "EFB credentials saved successfully.")
	http.Redirect(w, r, "/settings/efb", http.StatusSeeOther)
}

// handleEFBSettingsDelete removes the user's EFB credentials.
func (s *Server) handleEFBSettingsDelete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := s.db.DeleteEFBCredentials(userID); err != nil {
		s.logger.Error("failed to delete efb credentials", "user_id", userID, "error", err)
		setFlash(w, "Failed to delete credentials.")
		http.Redirect(w, r, "/settings/efb", http.StatusSeeOther)
		return
	}

	s.logger.Info("efb credentials deleted", "user_id", userID)
	setFlash(w, "EFB credentials removed.")
	http.Redirect(w, r, "/settings/efb", http.StatusSeeOther)
}

// handleAccountDelete deletes the user and all associated data, then redirects
// to the landing page.
func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Destroy the current session.
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil && cookie.Value != "" {
		_ = s.auth.DestroySession(cookie.Value)
	}

	// Delete user and all cascaded data.
	if err := s.db.DeleteUser(userID); err != nil {
		s.logger.Error("failed to delete user", "user_id", userID, "error", err)
		setFlash(w, "Failed to delete account. Please try again.")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	// Clear the session cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	s.logger.Info("user account deleted", "user_id", userID)
	setFlash(w, "Your account and all data have been deleted.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
