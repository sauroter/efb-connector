// Package sync implements the per-user sync orchestration: fetch activities
// from Garmin, upload GPX files to EFB, and track results in the database.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"efb-connector/internal/database"
	"efb-connector/internal/efb"
	"efb-connector/internal/garmin"
)

// SyncEngine orchestrates the per-user sync flow.
type SyncEngine struct {
	db     *database.DB
	garmin garmin.GarminProvider
	efb    *efb.EFBClient
	logger *slog.Logger

	// sleepFunc is called between uploads; overridden in tests to avoid delays.
	sleepFunc func(min, max time.Duration)
}

// NewSyncEngine creates a SyncEngine with the given dependencies.
func NewSyncEngine(db *database.DB, gp garmin.GarminProvider, ec *efb.EFBClient, logger *slog.Logger) *SyncEngine {
	return &SyncEngine{
		db:     db,
		garmin: gp,
		efb:    ec,
		logger: logger,
		sleepFunc: func(min, max time.Duration) {
			jitter := min + time.Duration(rand.Int64N(int64(max-min)))
			time.Sleep(jitter)
		},
	}
}

// activityToSync holds either a new activity from Garmin or a previously failed
// activity being retried.
type activityToSync struct {
	garminID string
	name     string
	actType  string
	date     string
	isRetry  bool
}

// SyncUser runs a full sync for one user. Returns the sync_run ID.
func (s *SyncEngine) SyncUser(ctx context.Context, userID int64, trigger string) (int64, error) {
	log := s.logger.With("user_id", userID, "trigger", trigger)

	// 1. Create sync_run record.
	runID, err := s.db.CreateSyncRun(userID, trigger)
	if err != nil {
		return 0, fmt.Errorf("sync: create sync run: %w", err)
	}
	log = log.With("run_id", runID)
	log.Info("sync run started")

	// Run the sync and capture results.
	found, synced, skipped, failed, syncErr := s.doSync(ctx, userID, runID, log)

	// 8. Determine final status.
	status := "completed"
	errMsg := ""
	if syncErr != nil {
		errMsg = syncErr.Error()
		if synced > 0 {
			status = "partial"
		} else {
			status = "failed"
		}
	} else if failed > 0 && synced > 0 {
		status = "partial"
	} else if failed > 0 && synced == 0 {
		status = "failed"
	}

	// Update sync_run with final counts and status.
	if updateErr := s.db.UpdateSyncRun(runID, status, found, synced, skipped, failed, errMsg); updateErr != nil {
		log.Error("failed to update sync run", "error", updateErr)
	}

	log.Info("sync run finished",
		"status", status,
		"found", found,
		"synced", synced,
		"skipped", skipped,
		"failed", failed,
	)

	return runID, syncErr
}

// doSync performs the actual sync work and returns counters.
func (s *SyncEngine) doSync(ctx context.Context, userID, runID int64, log *slog.Logger) (found, synced, skipped, failed int, err error) {
	// 2. Get user from DB.
	user, err := s.db.GetUserByID(userID)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("sync: get user: %w", err)
	}
	if user == nil {
		return 0, 0, 0, 0, fmt.Errorf("sync: user %d not found", userID)
	}

	// 3. Get Garmin credentials.
	garminEmail, garminPass, err := s.db.GetGarminCredentials(userID)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("sync: get garmin credentials: %w", err)
	}
	garminCreds := garmin.GarminCredentials{
		Email:    garminEmail,
		Password: garminPass,
	}

	// 4. List activities from Garmin.
	end := time.Now()
	start := end.AddDate(0, 0, -user.SyncDays)

	activities, err := s.garmin.ListActivities(ctx, garminCreds, start, end)
	if err != nil {
		// On auth failure: mark credentials invalid.
		if errors.Is(err, garmin.ErrGarminAuth) || errors.Is(err, garmin.ErrGarminMFARequired) {
			log.Warn("garmin auth failure, invalidating credentials", "error", err)
			if invErr := s.db.InvalidateGarminCredentials(userID, err.Error()); invErr != nil {
				log.Error("failed to invalidate garmin credentials", "error", invErr)
			}
		}
		return 0, 0, 0, 0, fmt.Errorf("sync: list activities: %w", err)
	}

	log.Info("fetched garmin activities", "count", len(activities))

	// 5 & 6. Build list of activities to sync (new + retriable).
	//
	// Build a set of failed activity IDs eligible for retry so we can mark
	// activities coming from the Garmin list as retries when they were
	// previously recorded as failed.
	failedActs, err := s.db.GetFailedActivities(userID)
	if err != nil {
		log.Error("failed to get failed activities for retry", "error", err)
	}
	failedSet := make(map[string]bool, len(failedActs))
	for _, fa := range failedActs {
		failedSet[fa.GarminActivityID] = true
	}

	var toSync []activityToSync
	queued := make(map[string]bool)

	for _, act := range activities {
		// Check current status in DB.
		status, err := s.db.GetActivityStatus(userID, act.ProviderID)
		if err != nil {
			log.Error("failed to check activity status", "activity_id", act.ProviderID, "error", err)
			continue
		}

		switch status {
		case "success":
			// Already synced successfully -- skip.
			skipped++
			continue
		case "permanent_failure":
			// Exhausted retries -- skip.
			skipped++
			continue
		case "failed":
			// Eligible for retry only if in the failedSet (retry_count < 3).
			if !failedSet[act.ProviderID] {
				// retry_count >= 3 but not yet marked permanent_failure;
				// treat as exhausted.
				skipped++
				continue
			}
		}

		toSync = append(toSync, activityToSync{
			garminID: act.ProviderID,
			name:     act.Name,
			actType:  act.Type,
			date:     act.Date.Format("2006-01-02"),
			isRetry:  failedSet[act.ProviderID],
		})
		queued[act.ProviderID] = true
	}

	// Include previously failed activities eligible for retry that were NOT
	// already in the Garmin list (e.g. activities that fell outside the
	// current sync_days window but still deserve a retry).
	for _, fa := range failedActs {
		if queued[fa.GarminActivityID] {
			continue
		}
		toSync = append(toSync, activityToSync{
			garminID: fa.GarminActivityID,
			name:     fa.ActivityName,
			actType:  fa.ActivityType,
			date:     fa.ActivityDate,
			isRetry:  true,
		})
	}

	found = len(toSync) + skipped

	if len(toSync) == 0 {
		log.Info("no new activities to sync")
		return found, 0, skipped, 0, nil
	}

	// 7b. Decrypt EFB credentials.
	efbUser, efbPass, err := s.db.GetEFBCredentials(userID)
	if err != nil {
		return found, 0, skipped, 0, fmt.Errorf("sync: get efb credentials: %w", err)
	}

	// 7c. Login to EFB (once per sync run).
	if err := s.efb.Login(ctx, efbUser, efbPass); err != nil {
		log.Warn("efb login failure, invalidating credentials", "error", err)
		if invErr := s.db.InvalidateEFBCredentials(userID, err.Error()); invErr != nil {
			log.Error("failed to invalidate efb credentials", "error", invErr)
		}
		// Mark all queued activities as failed.
		for _, act := range toSync {
			_ = s.db.RecordActivity(userID, act.garminID, act.name, act.actType, act.date, "failed", "efb login failed")
			failed++
		}
		return found, 0, skipped, failed, fmt.Errorf("sync: efb login: %w", err)
	}

	// 7. Process each activity.
	for i, act := range toSync {
		log := log.With("activity_id", act.garminID, "activity_name", act.name)

		// 7a. Download GPX from Garmin.
		gpxData, err := s.garmin.DownloadGPX(ctx, garminCreds, act.garminID)
		if err != nil {
			log.Error("failed to download GPX", "error", err)
			_ = s.db.RecordActivity(userID, act.garminID, act.name, act.actType, act.date, "failed", err.Error())
			if act.isRetry {
				_ = s.db.IncrementRetryCount(userID, act.garminID)
				s.checkAndMarkPermanentFailure(userID, act.garminID, log)
			}
			failed++
			continue
		}

		// 7d. Upload GPX to EFB.
		filename := fmt.Sprintf("garmin_%s.gpx", act.garminID)
		uploadErr := s.efb.Upload(ctx, gpxData, filename)
		if uploadErr != nil {
			log.Error("failed to upload GPX to EFB", "error", uploadErr)
			_ = s.db.RecordActivity(userID, act.garminID, act.name, act.actType, act.date, "failed", uploadErr.Error())
			if act.isRetry {
				_ = s.db.IncrementRetryCount(userID, act.garminID)
				s.checkAndMarkPermanentFailure(userID, act.garminID, log)
			}
			failed++

			// 7d: On 5xx, stop this user's sync.
			if isServer5xxError(uploadErr) {
				log.Warn("EFB returned 5xx, stopping sync for this user")
				// Mark remaining activities as failed.
				for _, remaining := range toSync[i+1:] {
					_ = s.db.RecordActivity(userID, remaining.garminID, remaining.name, remaining.actType, remaining.date, "failed", "skipped due to EFB 5xx")
					failed++
				}
				return found, synced, skipped, failed, fmt.Errorf("sync: EFB 5xx error, aborting: %w", uploadErr)
			}
			continue
		}

		// 7d: Success.
		log.Info("activity uploaded successfully")
		_ = s.db.RecordActivity(userID, act.garminID, act.name, act.actType, act.date, "success", "")
		synced++

		// 7f. Sleep between uploads (be gentle with EFB).
		if i < len(toSync)-1 {
			s.sleepFunc(5*time.Second, 10*time.Second)
		}
	}

	return found, synced, skipped, failed, nil
}

// checkAndMarkPermanentFailure increments the retry count check and marks an
// activity as permanent_failure if it has exceeded 3 retries. The DB's
// GetFailedActivities already filters retry_count < 3, but we proactively mark
// the status so the activity is clearly terminal.
func (s *SyncEngine) checkAndMarkPermanentFailure(userID int64, garminID string, log *slog.Logger) {
	// After IncrementRetryCount, if the activity no longer appears in
	// GetFailedActivities (retry_count >= 3), mark it as permanent_failure.
	failed, err := s.db.GetFailedActivities(userID)
	if err != nil {
		return
	}
	for _, f := range failed {
		if f.GarminActivityID == garminID {
			return // still eligible for retry
		}
	}
	// No longer in failed list => retry_count >= 3, mark permanent.
	if err := s.db.MarkPermanentFailure(userID, garminID); err != nil {
		log.Error("failed to mark permanent failure", "error", err)
	}
}

// SyncAllUsers syncs all eligible users with staggered delays.
func (s *SyncEngine) SyncAllUsers(ctx context.Context) error {
	users, err := s.db.GetSyncableUsers()
	if err != nil {
		return fmt.Errorf("sync: get syncable users: %w", err)
	}

	s.logger.Info("starting sync for all users", "user_count", len(users))

	var totalSynced, totalFailed int
	for i, user := range users {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		log := s.logger.With("user_id", user.ID, "email", user.Email)
		log.Info("syncing user")

		runID, err := s.SyncUser(ctx, user.ID, "scheduled")
		if err != nil {
			log.Error("sync failed for user", "error", err, "run_id", runID)
			totalFailed++
		} else {
			totalSynced++
		}

		// Stagger between users (30-60 seconds).
		if i < len(users)-1 {
			s.sleepFunc(30*time.Second, 60*time.Second)
		}
	}

	s.logger.Info("sync all users finished",
		"total_users", len(users),
		"synced", totalSynced,
		"failed", totalFailed,
	)

	return nil
}

// isServer5xxError checks if an EFB upload error indicates a server-side 5xx
// error by inspecting the error message. The EFB client formats these as
// "efb: upload failed with status 5XX: ...".
func isServer5xxError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Look for the pattern "status 5" in the error message.
	return strings.Contains(msg, "status 5")
}
