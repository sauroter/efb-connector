package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	syncsvc "efb-connector/internal/sync"
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

// handleInternalSyncAll triggers a sync for all eligible users. It streams
// NDJSON progress lines to keep the HTTP connection alive so that the Fly.io
// machine stays running (auto_stop_machines only stops when there are no
// active connections). Uses a worker pool of 2 for concurrent processing.
//
// Protected by:
//
//	Authorization: Bearer <INTERNAL_SECRET>
func (s *Server) handleInternalSyncAll(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}

	// Extend the write deadline for this long-running request (default is 30s).
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(20 * time.Minute)); err != nil {
		s.logger.Error("failed to extend write deadline", "error", err)
	}

	// Bound the total sync duration.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()

	s.logger.Info("internal sync-all triggered")

	// Stream NDJSON progress: one JSON line per user as they complete.
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()

	enc := json.NewEncoder(w)
	var mu sync.Mutex

	err := s.syncEngine.SyncAllUsersProgress(ctx, 2, func(result syncsvc.UserSyncResult) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(result)
		// Reset write deadline after each progress write.
		_ = rc.SetWriteDeadline(time.Now().Add(20 * time.Minute))
		_ = rc.Flush()
	})

	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		s.logger.Error("sync-all failed", "error", err)
		_ = enc.Encode(map[string]string{"status": "error", "error": err.Error()})
	} else {
		s.logger.Info("sync-all completed")
		_ = enc.Encode(map[string]string{"status": "completed"})
	}
	_ = rc.Flush()
}

// handleHealth checks that the database is reachable and returns 200 OK.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if err := s.db.Ping(); err != nil {
		s.logger.Error("health check failed", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "unhealthy",
			"error":  err.Error(),
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"version": s.version,
	})
}
