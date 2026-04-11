package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// handleAdminStatus returns system-wide statistics.
func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}

	stats, err := s.db.GetSystemStats()
	if err != nil {
		s.logger.Error("admin: get system stats", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleAdminUsers returns all users with their credential and sync status.
func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}

	users, err := s.db.GetAllUsersWithStatus()
	if err != nil {
		s.logger.Error("admin: get all users", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}

// handleAdminUserSyncHistory returns sync history for a specific user.
func (s *Server) handleAdminUserSyncHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}

	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	runs, err := s.db.GetSyncHistory(userID, 50)
	if err != nil {
		s.logger.Error("admin: get user sync history", "user_id", userID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(runs)
}

// handleAdminUserSync triggers a sync for a specific user, bypassing the rate limiter.
func (s *Server) handleAdminUserSync(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}

	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	s.logger.Info("admin: triggering sync for user", "user_id", userID)
	runID, syncErr := s.syncEngine.SyncUser(context.Background(), userID, "admin")
	if syncErr != nil {
		s.logger.Error("admin: sync failed", "user_id", userID, "run_id", runID, "error", syncErr)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{
			"status": "error",
			"run_id": runID,
			"error":  syncErr.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "completed",
		"run_id": runID,
	})
}

// handleAdminErrors returns recent failed/partial sync runs across all users.
func (s *Server) handleAdminErrors(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}

	runs, err := s.db.GetRecentFailedSyncRuns(50)
	if err != nil {
		s.logger.Error("admin: get recent errors", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(runs)
}

// handleAdminNotifyGarminUpgrade sends a notification email to all users with
// Garmin credentials about the garminconnect library upgrade.
func (s *Server) handleAdminNotifyGarminUpgrade(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}

	users, err := s.db.GetAllUsersWithStatus()
	if err != nil {
		s.logger.Error("admin: get users for notification", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var sent int
	var errs []string
	for _, u := range users {
		if !u.GarminConnected {
			continue
		}

		subject, body := garminUpgradeEmail(u.PreferredLang, s.configBaseURL)
		if err := s.auth.SendEmail(u.Email, subject, body); err != nil {
			s.logger.Error("admin: send upgrade notification", "user_id", u.ID, "error", err)
			errs = append(errs, fmt.Sprintf("user %d: %v", u.ID, err))
			continue
		}
		sent++
		s.logger.Info("admin: sent garmin upgrade notification", "user_id", u.ID, "email", u.Email)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"sent":   sent,
		"errors": errs,
	})
}

func garminUpgradeEmail(lang, baseURL string) (subject, body string) {
	settingsURL := baseURL + "/settings/garmin"

	if lang == "de" {
		return "EFB Connector: Garmin-Integration aktualisiert",
			fmt.Sprintf(`<p>Hallo,</p>
<p>wir haben die Garmin-Integration des EFB Connectors aktualisiert, um eine bessere Kompatibilität mit Garmin Connect sicherzustellen.</p>
<p>Deine Verbindung wird beim nächsten Sync automatisch neu aufgebaut. Falls dabei Probleme auftreten, kannst du deine Garmin-Zugangsdaten unter dem folgenden Link neu eingeben:</p>
<p><a href="%s">Garmin-Einstellungen öffnen</a></p>
<p>Viele Grüße,<br>EFB Connector</p>`, settingsURL)
	}

	return "EFB Connector: Garmin Integration Updated",
		fmt.Sprintf(`<p>Hi,</p>
<p>We've updated the EFB Connector's Garmin integration to improve compatibility with Garmin Connect.</p>
<p>Your connection will be re-established automatically on the next sync. If you experience any issues, you can re-enter your Garmin credentials here:</p>
<p><a href="%s">Open Garmin Settings</a></p>
<p>Best,<br>EFB Connector</p>`, settingsURL)
}
