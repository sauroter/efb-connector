package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"efb-connector/internal/auth"
	"efb-connector/internal/garmin"
)

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

	// Validate credentials with MFA support.
	tokenStorePath := s.garminTokenStorePath(userID)
	creds := garmin.GarminCredentials{
		Email:          email,
		Password:       password,
		TokenStorePath: tokenStorePath,
	}
	status, err := s.garmin.ValidateWithMFA(context.Background(), userID, creds)
	if err != nil {
		s.logger.Warn("garmin credential validation failed", "user_id", userID, "error", err)
		if errors.Is(err, garmin.ErrGarminUnavailable) {
			setFlash(w, "flash.garmin_unavailable")
		} else {
			setFlash(w, "flash.garmin_invalid")
		}
		http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
		return
	}

	if status == "needs_mfa" {
		// Save credentials (not yet valid) so they persist across the
		// redirect to the MFA form.  They'll be marked valid once MFA
		// completes.
		if err := s.db.SaveGarminCredentials(userID, email, password); err != nil {
			s.logger.Error("failed to save garmin credentials", "user_id", userID, "error", err)
			setFlash(w, "flash.save_credentials_failed")
			http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
			return
		}
		if err := s.db.InvalidateGarminCredentials(userID, "MFA verification pending"); err != nil {
			s.logger.Error("failed to invalidate garmin credentials for MFA", "user_id", userID, "error", err)
		}
		http.Redirect(w, r, "/settings/garmin/mfa", http.StatusSeeOther)
		return
	}

	// Save encrypted credentials (already validated).
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

// handleGarminMFA renders the MFA code entry form.
func (s *Server) handleGarminMFA(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if !s.garmin.HasMFASession(userID) {
		setFlash(w, "flash.garmin_mfa_expired")
		http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
		return
	}

	s.render(w, r, "settings_garmin_mfa.html", map[string]any{
		"Flash":     flash(w, r),
		"CSRFToken": s.auth.CSRFToken(r),
	})
}

// handleGarminMFASubmit completes the MFA verification.
func (s *Server) handleGarminMFASubmit(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	code := strings.TrimSpace(r.FormValue("mfa_code"))
	if code == "" {
		setFlash(w, "flash.garmin_mfa_invalid")
		http.Redirect(w, r, "/settings/garmin/mfa", http.StatusSeeOther)
		return
	}

	if err := s.garmin.CompleteMFA(userID, code); err != nil {
		s.logger.Warn("garmin MFA verification failed", "user_id", userID, "error", err)
		setFlash(w, "flash.garmin_mfa_invalid")
		// The MFA session is consumed on failure, redirect to re-enter credentials.
		http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
		return
	}

	// Mark credentials as valid (they were saved with is_valid=0 before MFA).
	if err := s.db.RevalidateGarminCredentials(userID); err != nil {
		s.logger.Error("failed to mark garmin credentials valid after MFA", "user_id", userID, "error", err)
		setFlash(w, "flash.save_credentials_failed")
		http.Redirect(w, r, "/settings/garmin", http.StatusSeeOther)
		return
	}

	s.logger.Info("garmin credentials saved after MFA", "user_id", userID)
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

	// Proactive check: EFB v2026.1 added a track-usage consent gate.
	// The session set up by ValidateCredentials still lives on the
	// shared EFBProvider's cookie jar, so we can immediately ask EFB
	// whether the upload form is available for this user. If not,
	// flag the user so the dashboard banner appears, and use a
	// consent-aware flash on the redirect.
	if consentRequired, err := s.efb.CheckConsentGate(context.Background()); err != nil {
		s.logger.Warn("efb consent check failed (credentials saved anyway)",
			"user_id", userID, "error", err)
	} else if consentRequired {
		if mErr := s.db.MarkEFBConsentRequired(userID); mErr != nil {
			s.logger.Error("failed to mark efb consent required at save",
				"user_id", userID, "error", mErr)
		}
		// The amber dashboard banner carries the action ask; the brief
		// "saved" flash is kept just for transient confirmation that the
		// credentials were stored.
	}

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
