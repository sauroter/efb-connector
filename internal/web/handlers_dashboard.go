package web

import (
	"net/http"
	"os"
	"strings"
	"time"

	"efb-connector/internal/auth"
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

	// Check whether EFB is currently consent-gated for this user.
	// Surfaced as a banner so the user knows to click "ich stimme zu"
	// on the EFB portal before sync can succeed.
	var efbConsentRequired bool
	if efbConnected {
		req, _, csErr := s.db.GetEFBConsentState(userID)
		if csErr != nil {
			s.logger.Warn("dashboard: get efb consent state", "user_id", userID, "error", csErr)
		} else {
			efbConsentRequired = req
		}
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
		"EFBConsentRequired": efbConsentRequired,
		"EFBConsentURL":      "https://efb.kanu-efb.de/interpretation/usersmap",
		"LastSync":           lastSync,
		"SyncDays":           user.SyncDays,
		"AutoCreateTrips":    user.AutoCreateTrips,
		"Today":              time.Now().Format("2006-01-02"),
		"ShowGettingStarted": showGettingStarted,
		"SetupStep":          setupStep,
	})
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
		"EFBUsername":     efbUsername,
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

	// Best-effort: move user from "Needs Setup" to "Active Syncers" in Resend.
	if s.resend != nil && s.resendSegActive != "" {
		user, _ := s.db.GetUserByID(userID)
		if user != nil {
			segActive := s.resendSegActive
			segSetup := s.resendSegSetup
			email := user.Email
			logger := s.logger
			go func() {
				if err := s.resend.SyncUserSegment(email, true, segActive, segSetup); err != nil {
					logger.Warn("resend: sync segment failed on setup completion", "email", email, "error", err)
				}
			}()
		}
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

	// Capture email before deletion for Resend cleanup.
	var userEmail string
	if user, _ := s.db.GetUserByID(userID); user != nil {
		userEmail = user.Email
	}

	// Delete user and all cascaded data.
	if err := s.db.DeleteUser(userID); err != nil {
		s.logger.Error("failed to delete user", "user_id", userID, "error", err)
		setFlash(w, "flash.delete_account_failed")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	// Best-effort: remove contact from Resend.
	if s.resend != nil && userEmail != "" {
		logger := s.logger
		go func() {
			if err := s.resend.DeleteContact(userEmail); err != nil {
				logger.Warn("resend: delete contact failed", "email", userEmail, "error", err)
			}
		}()
	}

	// Remove cached Garmin token files from disk.
	tokenDir := s.garminTokenStorePath(userID)
	if err := os.RemoveAll(tokenDir); err != nil {
		s.logger.Warn("failed to remove garmin token store", "user_id", userID, "error", err)
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
