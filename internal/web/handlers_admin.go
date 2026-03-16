package web

import (
	"context"
	"encoding/json"
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
