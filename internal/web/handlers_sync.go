package web

import (
	"context"
	"html"
	"net/http"
	"strconv"
	"time"

	"efb-connector/internal/auth"
	"efb-connector/internal/i18n"
	"efb-connector/internal/sync"
)

// handleSyncTrigger launches a manual sync in a background goroutine and
// returns the sync_run ID. Rate-limited to 1 per hour per user.
func (s *Server) handleSyncTrigger(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if !s.rateLimiter.AllowSync(userID) {
		s.syncError(w, r, "flash.sync_rate_limited")
		return
	}

	_ = r.ParseForm()

	trigger := "manual"
	var opts sync.SyncOptions

	startStr := r.FormValue("start_date")
	endStr := r.FormValue("end_date")
	if startStr != "" && endStr != "" {
		startDate, err := time.Parse("2006-01-02", startStr)
		if err != nil {
			s.syncError(w, r, "flash.invalid_start_date")
			return
		}
		endDate, err := time.Parse("2006-01-02", endStr)
		if err != nil {
			s.syncError(w, r, "flash.invalid_end_date")
			return
		}
		// Include the full end day.
		endWithFullDay := endDate.AddDate(0, 0, 1)
		opts = sync.SyncOptions{Start: startDate, End: endWithFullDay}
		trigger = "manual_custom"

		// Validate date range synchronously before launching goroutine,
		// so the user gets immediate feedback and doesn't burn rate limit.
		if !startDate.Before(endWithFullDay) {
			s.syncError(w, r, "flash.start_before_end")
			return
		}
		if endWithFullDay.Sub(startDate).Hours()/24 > float64(sync.MaxCustomRangeDays) {
			s.syncError(w, r, "flash.date_range_exceeded")
			return
		}
	}

	// Launch the sync in a background goroutine.
	go func() {
		log := s.logger.With("user_id", userID, "trigger", trigger)
		log.Info("manual sync started")

		runID, err := s.syncEngine.SyncUserWithOptions(context.Background(), userID, trigger, opts)
		if err != nil {
			log.Error("manual sync failed", "run_id", runID, "error", err)
		} else {
			log.Info("manual sync completed", "run_id", runID)
		}
	}()

	s.logger.Info("sync triggered", "user_id", userID)

	if r.Header.Get("HX-Request") == "true" {
		// Return a "running" partial that will auto-poll for updates.
		s.render(w, r, "sync_status.html", map[string]any{
			"HasRun": true,
			"Status": "running",
		})
		return
	}
	setFlash(w, "flash.sync_started")
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// handleEFBRecheckConsent re-runs the EFB v2026.1 consent-gate check
// for the signed-in user. Used by the dashboard banner after the user
// clicks "ich stimme zu" on the EFB portal — they want to confirm the
// gate is gone and resume sync, not trigger a full sync attempt that
// might burn the per-user 1/hour rate limit and stay stuck.
//
// On confirmed consent we clear the flag and kick off one immediate
// sync via SyncEngine.SyncUser (bypassing the user rate limiter — the
// user explicitly took action to resume; this is not a poll). On a
// still-gated response we leave the flag and surface a hint flash so
// the user can retry.
//
// Routes the EFB call through SyncEngine.RecheckEFBConsent so dev mode
// uses the in-process mock and production gets a fresh per-call client.
func (s *Server) handleEFBRecheckConsent(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	consentRequired, err := s.syncEngine.RecheckEFBConsent(ctx, userID)
	if err != nil {
		s.logger.Warn("efb consent recheck failed", "user_id", userID, "error", err)
		setFlash(w, "flash.efb_consent_recheck_failed")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	if consentRequired {
		s.logger.Info("efb consent recheck: still required", "user_id", userID)
		setFlash(w, "flash.efb_consent_still_required")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	// Cleared — flip the flag and run one sync now.
	if clrErr := s.db.ClearEFBConsentRequired(userID); clrErr != nil {
		s.logger.Error("efb consent recheck: clear flag", "user_id", userID, "error", clrErr)
	}
	go func() {
		log := s.logger.With("user_id", userID, "trigger", "manual")
		log.Info("post-consent sync started")
		runID, syncErr := s.syncEngine.SyncUser(context.Background(), userID, "manual")
		if syncErr != nil {
			log.Error("post-consent sync failed", "run_id", runID, "error", syncErr)
		} else {
			log.Info("post-consent sync completed", "run_id", runID)
		}
	}()

	s.logger.Info("efb consent recheck: confirmed, sync triggered", "user_id", userID)
	setFlash(w, "flash.efb_consent_confirmed")
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// handleSyncStatus returns an htmx partial with the current sync status.
// Intended to be polled every few seconds from the dashboard.
func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Get the most recent sync run.
	runs, err := s.db.GetSyncHistory(userID, 1)
	if err != nil {
		s.logger.Error("failed to load sync status", "user_id", userID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var data map[string]any
	if len(runs) > 0 {
		run := runs[0]
		data = map[string]any{
			"HasRun":            true,
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
	} else {
		data = map[string]any{
			"HasRun": false,
		}
	}

	s.render(w, r, "sync_status.html", data)
}

// handleSyncHistory renders the full sync history page.
func (s *Server) handleSyncHistory(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	runs, err := s.db.GetSyncHistory(userID, limit)
	if err != nil {
		s.logger.Error("failed to load sync history", "user_id", userID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	s.render(w, r, "sync_history.html", map[string]any{
		"Flash":     flash(w, r),
		"CSRFToken": s.auth.CSRFToken(r),
		"Runs":      runs,
	})
}

// syncError returns an error message for sync form submissions.
// msg is an i18n key (e.g. "flash.sync_rate_limited").
func (s *Server) syncError(w http.ResponseWriter, r *http.Request, msg string) {
	if r.Header.Get("HX-Request") == "true" {
		lang := i18n.FromContext(r.Context())
		translated := html.EscapeString(i18n.T(lang, msg))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write([]byte(`<div id="sync-status"><p style="color:#991b1b;">` + translated + `</p></div>`)); err != nil {
			s.logger.Error("failed to write sync error response", "error", err)
		}
		return
	}
	setFlash(w, msg)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}
