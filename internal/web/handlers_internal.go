package web

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// requireInternalAuth checks the Authorization: Bearer <INTERNAL_SECRET> header.
// Returns true if authorized, false (and writes 401) if not.
func (s *Server) requireInternalAuth(w http.ResponseWriter, r *http.Request) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(s.internalSecret)) != 1 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// handleInternalSyncAll triggers a sync for all eligible users. It runs
// synchronously so that the Fly.io machine stays alive for the duration
// (auto_stop_machines only stops when there are no active connections).
//
// Protected by:
//
//	Authorization: Bearer <INTERNAL_SECRET>
func (s *Server) handleInternalSyncAll(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}

	s.logger.Info("internal sync-all triggered")
	if err := s.syncEngine.SyncAllUsers(r.Context()); err != nil {
		s.logger.Error("sync-all failed", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}

	s.logger.Info("sync-all completed")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "completed",
	})
}

// handleHealth checks that the database is reachable and returns 200 OK.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if err := s.db.Ping(); err != nil {
		s.logger.Error("health check failed", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "unhealthy",
			"error":  err.Error(),
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}
