package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"efb-connector/internal/auth"
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
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<div id="sync-status"><p style="color:#991b1b;">You can only sync once per hour. Please try again later.</p></div>`))
			return
		}
		setFlash(w, "You can only sync once per hour. Please try again later.")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
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
			s.syncError(w, r, "Invalid start date format.")
			return
		}
		endDate, err := time.Parse("2006-01-02", endStr)
		if err != nil {
			s.syncError(w, r, "Invalid end date format.")
			return
		}
		// Include the full end day.
		endWithFullDay := endDate.AddDate(0, 0, 1)
		opts = sync.SyncOptions{Start: startDate, End: endWithFullDay}
		trigger = "manual_custom"

		// Validate date range synchronously before launching goroutine,
		// so the user gets immediate feedback and doesn't burn rate limit.
		if !startDate.Before(endWithFullDay) {
			s.syncError(w, r, "Start date must be before end date.")
			return
		}
		if endWithFullDay.Sub(startDate).Hours()/24 > float64(sync.MaxCustomRangeDays) {
			s.syncError(w, r, fmt.Sprintf("Date range cannot exceed %d days.", sync.MaxCustomRangeDays))
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
		s.render(w, "sync_status.html", map[string]any{
			"HasRun": true,
			"Status": "running",
		})
		return
	}
	setFlash(w, "Sync started. This may take a few minutes.")
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
			"ErrorMessage":      run.ErrorMessage,
		}
	} else {
		data = map[string]any{
			"HasRun": false,
		}
	}

	s.render(w, "sync_status.html", data)
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

	s.render(w, "sync_history.html", map[string]any{
		"Flash":     flash(w, r),
		"CSRFToken": s.auth.CSRFToken(r),
		"Runs":      runs,
	})
}

// syncError returns an error message for sync form submissions.
func (s *Server) syncError(w http.ResponseWriter, r *http.Request, msg string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<div id="sync-status"><p style="color:#991b1b;">` + msg + `</p></div>`))
		return
	}
	setFlash(w, msg)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}
