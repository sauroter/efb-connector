package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// handleInternalSyncAll triggers a sync for all eligible users. It is
// protected by a shared secret in the Authorization header:
//
//	Authorization: Bearer <INTERNAL_SECRET>
func (s *Server) handleInternalSyncAll(w http.ResponseWriter, r *http.Request) {
	// Validate shared secret.
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(s.internalSecret)) != 1 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Run sync in a background goroutine so the caller does not time out.
	go func() {
		s.logger.Info("internal sync-all triggered")
		if err := s.syncEngine.SyncAllUsers(context.Background()); err != nil {
			s.logger.Error("sync-all failed", "error", err)
		} else {
			s.logger.Info("sync-all completed")
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "accepted",
	})
}

// handleHealth is a simple health check endpoint that returns 200 OK.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}
