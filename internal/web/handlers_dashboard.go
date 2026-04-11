package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

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
	hasSynced := false
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
			"TripsCreated":      run.TripsCreated,
			"ErrorMessage":      run.ErrorMessage,
		}
		hasSynced = run.Status == "success" || run.Status == "completed" || run.Status == "partial"
	}

	// Compute getting-started state.
	// SetupStep: 1=need Garmin, 2=need EFB, 3=configure preferences, 4=run first sync, 0=all done.
	setupStep := 0
	showGettingStarted := false
	if !garminConnected {
		setupStep = 1
		showGettingStarted = true
	} else if !efbConnected {
		setupStep = 2
		showGettingStarted = true
	} else if !user.SetupCompleted {
		setupStep = 3
		showGettingStarted = true
	} else if !hasSynced {
		setupStep = 4
		showGettingStarted = true
	}

	s.render(w, r, "dashboard.html", map[string]any{
		"Flash":              flash(w, r),
		"CSRFToken":          s.auth.CSRFToken(r),
		"User":               user,
		"GarminConnected":    garminConnected,
		"EFBConnected":       efbConnected,
		"LastSync":           lastSync,
		"SyncDays":           user.SyncDays,
		"AutoCreateTrips":    user.AutoCreateTrips,
		"Today":              time.Now().Format("2006-01-02"),
		"ShowGettingStarted": showGettingStarted,
		"SetupStep":          setupStep,
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

	s.render(w, r, "settings_garmin.html", map[string]any{
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
		setFlash(w, "flash.email_password_required")
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
		if errors.Is(err, garmin.ErrGarminMFARequired) {
			setFlash(w, "flash.garmin_mfa_required")
		} else {
			setFlash(w, "flash.garmin_invalid")
		}
		http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
		return
	}

	// Save encrypted credentials.
	if err := s.db.SaveGarminCredentials(userID, email, password); err != nil {
		s.logger.Error("failed to save garmin credentials", "user_id", userID, "error", err)
		setFlash(w, "flash.save_credentials_failed")
		http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
		return
	}

	s.logger.Info("garmin credentials saved", "user_id", userID)
	setFlash(w, "flash.garmin_saved")
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
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
		setFlash(w, "flash.delete_credentials_failed")
		http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
		return
	}

	// Remove cached token files so stale tokens don't survive a re-connect.
	tokenDir := s.garminTokenStorePath(userID)
	if err := os.RemoveAll(tokenDir); err != nil {
		s.logger.Warn("failed to remove garmin token store", "user_id", userID, "error", err)
	}

	s.logger.Info("garmin credentials deleted", "user_id", userID)
	setFlash(w, "flash.garmin_removed")
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

	s.render(w, r, "settings_efb.html", map[string]any{
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
		setFlash(w, "flash.username_password_required")
		http.Redirect(w, r, "/settings/efb", http.StatusSeeOther)
		return
	}

	// Validate credentials against the EFB portal.
	if err := s.efb.ValidateCredentials(context.Background(), username, password); err != nil {
		s.logger.Warn("efb credential validation failed", "user_id", userID, "error", err)
		setFlash(w, "flash.efb_invalid")
		http.Redirect(w, r, "/settings/efb", http.StatusSeeOther)
		return
	}

	// Save encrypted credentials.
	if err := s.db.SaveEFBCredentials(userID, username, password); err != nil {
		s.logger.Error("failed to save efb credentials", "user_id", userID, "error", err)
		setFlash(w, "flash.save_credentials_failed")
		http.Redirect(w, r, "/settings/efb", http.StatusSeeOther)
		return
	}

	s.logger.Info("efb credentials saved", "user_id", userID)
	setFlash(w, "flash.efb_saved")
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
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
		setFlash(w, "flash.delete_credentials_failed")
		http.Redirect(w, r, "/settings/efb", http.StatusSeeOther)
		return
	}

	s.logger.Info("efb credentials deleted", "user_id", userID)
	setFlash(w, "flash.efb_removed")
	http.Redirect(w, r, "/settings/efb", http.StatusSeeOther)
}

// handleSettings renders the consolidated settings page.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
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

	garminConnected := false
	garminEmail, _, garminErr := s.db.GetGarminCredentials(userID)
	if garminErr == nil {
		garminConnected = true
	}

	efbConnected := false
	efbUsername, _, efbErr := s.db.GetEFBCredentials(userID)
	if efbErr == nil {
		efbConnected = true
	}

	s.render(w, r, "settings.html", map[string]any{
		"Flash":           flash(w, r),
		"CSRFToken":       s.auth.CSRFToken(r),
		"User":            user,
		"GarminConnected": garminConnected,
		"GarminEmail":     garminEmail,
		"EFBConnected":    efbConnected,
		"EFBUsername":    efbUsername,
		"AutoCreateTrips": user.AutoCreateTrips,
		"EnrichTrips":     user.EnrichTrips,
	})
}

// handleSetupConfigure saves preferences from the onboarding wizard (step 3)
// and marks the setup as completed, advancing the user to step 4.
func (s *Server) handleSetupConfigure(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	autoCreateTrips := r.FormValue("auto_create_trips") == "1"
	enrichTrips := r.FormValue("enrich_trips") == "1"

	if err := s.db.UpdateAutoCreateTrips(userID, autoCreateTrips); err != nil {
		s.logger.Error("failed to update auto_create_trips", "user_id", userID, "error", err)
		setFlash(w, "flash.save_preferences_failed")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	if err := s.db.UpdateEnrichTrips(userID, enrichTrips); err != nil {
		s.logger.Error("failed to update enrich_trips", "user_id", userID, "error", err)
	}

	if err := s.db.UpdateSetupCompleted(userID, true); err != nil {
		s.logger.Error("failed to update setup_completed", "user_id", userID, "error", err)
	}

	s.logger.Info("setup preferences configured", "user_id", userID, "auto_create_trips", autoCreateTrips, "enrich_trips", enrichTrips)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// handleAutoCreateTripsSave saves the auto_create_trips toggle for the current user.
func (s *Server) handleAutoCreateTripsSave(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Standard checkbox behaviour: field is present when checked, absent when not.
	enabled := r.FormValue("enabled") == "1"

	if err := s.db.UpdateAutoCreateTrips(userID, enabled); err != nil {
		s.logger.Error("failed to update auto_create_trips", "user_id", userID, "error", err)
		setFlash(w, "flash.save_setting_failed")
	}

	s.logger.Info("auto_create_trips updated", "user_id", userID, "enabled", enabled)

	// Redirect back to referring page (settings or dashboard wizard).
	if ref := r.Referer(); strings.Contains(ref, "/settings") {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// handleEnrichTripsSave saves the enrich_trips toggle for the current user.
func (s *Server) handleEnrichTripsSave(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	enabled := r.FormValue("enabled") == "1"

	if err := s.db.UpdateEnrichTrips(userID, enabled); err != nil {
		s.logger.Error("failed to update enrich_trips", "user_id", userID, "error", err)
		setFlash(w, "flash.save_setting_failed")
	}

	s.logger.Info("enrich_trips updated", "user_id", userID, "enabled", enabled)

	if ref := r.Referer(); strings.Contains(ref, "/settings") {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
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
		setFlash(w, "flash.delete_account_failed")
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
	setFlash(w, "flash.account_deleted")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLanguageSave saves the user's language preference and sets the lang cookie.
func (s *Server) handleLanguageSave(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	lang := r.FormValue("lang")
	if lang != "en" && lang != "de" {
		lang = "en"
	}

	if err := s.db.UpdatePreferredLang(userID, lang); err != nil {
		s.logger.Error("failed to update preferred_lang", "user_id", userID, "error", err)
		setFlash(w, "flash.save_setting_failed")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	// Also set cookie so it takes effect immediately on redirect.
	http.SetCookie(w, &http.Cookie{
		Name:     "lang",
		Value:    lang,
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: false,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	s.logger.Info("preferred_lang updated", "user_id", userID, "lang", lang)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
