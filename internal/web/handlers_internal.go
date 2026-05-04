package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	syncsvc "efb-connector/internal/sync"
)

// runAllTimeout caps how long a single fire-and-forget run-all execution
// may take server-side. Generous: each user takes a few seconds, and we
// want to outlive transient slowness without leaving a stuck "in progress"
// flag forever.
const runAllTimeout = 30 * time.Minute

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

// handleInternalSyncAll kicks off a sync for all eligible users in a
// detached goroutine and returns 202 Accepted immediately. Progress is
// observable via GET /internal/sync/run-all/status. A 409 Conflict is
// returned if a run is already in progress.
//
// We deliberately decouple sync execution from the HTTP request lifecycle:
// the original streaming-NDJSON design tied the workers' context to the
// request, and Fly's edge proxy was cutting that connection at ~30–90 s,
// silently aborting ~60% of nightly syncs.
//
// Protected by:
//
//	Authorization: Bearer <INTERNAL_SECRET>
func (s *Server) handleInternalSyncAll(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}

	startedAt := time.Now()

	s.runAllMu.Lock()
	if s.runAllState.InProgress {
		state := s.runAllState
		s.runAllMu.Unlock()
		writeJSON(w, http.StatusConflict, runAllStatusBody(state))
		return
	}
	s.runAllState = runAllState{
		InProgress: true,
		StartedAt:  startedAt,
	}
	s.runAllMu.Unlock()

	s.logger.Info("internal sync-all triggered")

	s.runAllWG.Add(1)
	go s.runSyncAll()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":     "started",
		"started_at": startedAt.UTC().Format(time.RFC3339),
	})
}

// runSyncAll is the goroutine body for the fire-and-forget run-all. It
// uses runAllRootCtx (not the request context) so a proxy / client
// disconnect cannot cancel the work mid-flight, but a server Shutdown
// can.
func (s *Server) runSyncAll() {
	defer s.runAllWG.Done()

	ctx, cancel := context.WithTimeout(s.runAllRootCtx, runAllTimeout)
	defer cancel()

	defer func() {
		now := time.Now()

		s.runAllMu.Lock()
		defer s.runAllMu.Unlock()

		if rec := recover(); rec != nil {
			// Capture the panic into the state so polling clients see it.
			s.runAllState.Error = fmt.Sprintf("panic: %v", rec)
			s.logger.Error("sync-all panicked",
				"panic", rec,
				"stack", string(debug.Stack()),
			)
		}
		s.runAllState.InProgress = false
		s.runAllState.FinishedAt = &now
	}()

	// Resolve the user list ourselves so TotalUsers and the iterated set
	// come from the same query — avoids spurious workflow failures from
	// snapshot drift between two reads.
	users, err := s.db.GetSyncableUsers()
	if err != nil {
		s.runAllMu.Lock()
		s.runAllState.Error = fmt.Sprintf("get syncable users: %v", err)
		s.runAllMu.Unlock()
		s.logger.Error("sync-all failed", "error", err)
		return
	}
	s.runAllMu.Lock()
	s.runAllState.TotalUsers = len(users)
	s.runAllMu.Unlock()

	onProgress := func(result syncsvc.UserSyncResult) {
		s.runAllMu.Lock()
		defer s.runAllMu.Unlock()
		if result.Status == "failed" {
			s.runAllState.Failed++
		} else {
			s.runAllState.Synced++
		}
	}

	if err := s.syncEngine.SyncUsers(ctx, users, 2, onProgress); err != nil {
		s.runAllMu.Lock()
		s.runAllState.Error = err.Error()
		s.runAllMu.Unlock()
		s.logger.Error("sync-all failed", "error", err)
		return
	}
	s.logger.Info("sync-all completed", "total_users", len(users))
}

// handleInternalSyncAllStatus returns the current/last run-all state as
// JSON. Safe to poll on a tight cadence; reads are cheap and lock-bounded.
//
// Protected by:
//
//	Authorization: Bearer <INTERNAL_SECRET>
func (s *Server) handleInternalSyncAllStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}

	s.runAllMu.Lock()
	state := s.runAllState
	s.runAllMu.Unlock()

	writeJSON(w, http.StatusOK, runAllStatusBody(state))
}

// runAllStatusBody renders a runAllState into the public JSON shape used
// by the status endpoint and the 409-conflict body.
func runAllStatusBody(state runAllState) map[string]any {
	body := map[string]any{
		"in_progress": state.InProgress,
		"total_users": state.TotalUsers,
		"synced":      state.Synced,
		"failed":      state.Failed,
		"error":       state.Error,
	}
	if !state.StartedAt.IsZero() {
		body["started_at"] = state.StartedAt.UTC().Format(time.RFC3339)
	}
	if state.FinishedAt != nil {
		body["finished_at"] = state.FinishedAt.UTC().Format(time.RFC3339)
	}
	return body
}

// writeJSON serializes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
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
