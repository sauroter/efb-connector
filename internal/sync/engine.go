// Package sync implements the per-user sync orchestration: fetch activities
// from Garmin, upload GPX files to EFB, and track results in the database.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	stdsync "sync"
	"time"

	"efb-connector/internal/database"
	"efb-connector/internal/efb"
	"efb-connector/internal/garmin"
	"efb-connector/internal/i18n"
	"efb-connector/internal/metrics"
	"efb-connector/internal/rivermap"
)

// Mailer is the minimal templated-email dependency used by the engine
// for user-facing notifications (e.g. the EFB consent-required email).
// Implemented by *mailer.Mailer in production and a fake in tests; kept
// narrow here so the sync package doesn't import internal/mailer.
type Mailer interface {
	Send(to string, lang i18n.Lang, name string, data map[string]any, subjectArgs ...any) error
}

// ConsentEmailRateLimit caps how often a single user is emailed about
// the EFB v2026.1 track-usage consent gate while it remains unresolved.
const ConsentEmailRateLimit = 7 * 24 * time.Hour

// efbConsentURL is the EFB tracks page where the user accepts the
// v2026.1 track-usage agreement. Linked from the consent-required
// email and surfaced as the action target there.
const efbConsentURL = "https://efb.kanu-efb.de/interpretation/usersmap"

// SyncEngine orchestrates the per-user sync flow.
type SyncEngine struct {
	db     *database.DB
	garmin garmin.GarminProvider
	logger *slog.Logger

	// newEFBSession creates a fresh EFBProvider for each user sync. Each
	// instance gets its own cookie jar / session, which prevents concurrent
	// workers from overwriting each other's EFB sessions.
	newEFBSession func() efb.EFBProvider

	// tokenStoreBase is the base directory for per-user Garmin token stores.
	tokenStoreBase string

	// rivermap is the optional Rivermap client for trip enrichment. nil if not configured.
	rivermap *rivermap.Client

	// mailer sends the consent-required notification email. nil disables
	// the email path (engine still flips the DB flag).
	mailer Mailer

	// nowFunc returns the current time. Overridable in tests so the
	// 7-day email rate limit can be exercised without sleeping.
	nowFunc func() time.Time

	// sleepFunc is called between uploads; overridden in tests to avoid delays.
	sleepFunc func(min, max time.Duration)
}

// Option configures a [SyncEngine] at construction time.
type Option func(*SyncEngine)

// WithRivermap enables Rivermap enrichment for trip entries.
func WithRivermap(c *rivermap.Client) Option {
	return func(s *SyncEngine) { s.rivermap = c }
}

// WithMailer wires the templated email dispatcher used for user-facing
// notifications (currently: the EFB consent-required email). When
// unset the engine still flips the consent_required DB flag but no
// email is dispatched.
func WithMailer(m Mailer) Option {
	return func(s *SyncEngine) { s.mailer = m }
}

// WithoutSleep removes inter-upload delays. Intended for tests and dev mode.
func WithoutSleep() Option {
	return func(s *SyncEngine) {
		s.sleepFunc = func(_, _ time.Duration) {}
	}
}

// NewSyncEngine creates a SyncEngine with the given dependencies.
// newEFBSession is called once per user sync to obtain a fresh EFBProvider
// with its own session state, avoiding cookie-jar collisions between
// concurrent workers.
//
// Optional behavior (Rivermap enrichment, email notifications, sleep
// disabling) is configured via [Option] values.
func NewSyncEngine(db *database.DB, gp garmin.GarminProvider, newEFBSession func() efb.EFBProvider, logger *slog.Logger, opts ...Option) *SyncEngine {
	var tokenBase string
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		tokenBase = "/data/garmin_tokens"
	} else {
		home, _ := os.UserHomeDir()
		tokenBase = filepath.Join(home, ".config", "efb-connector", "garmin_tokens")
	}
	s := &SyncEngine{
		db:             db,
		garmin:         gp,
		newEFBSession:  newEFBSession,
		logger:         logger,
		tokenStoreBase: tokenBase,
		nowFunc:        time.Now,
		sleepFunc: func(min, max time.Duration) {
			jitter := min + time.Duration(rand.Int64N(int64(max-min)))
			time.Sleep(jitter)
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ErrInvalidDateRange is returned when the caller provides an invalid custom
// date range (e.g. start after end, range too large).
var ErrInvalidDateRange = errors.New("sync: invalid date range")

// MaxCustomRangeDays is the maximum span allowed for a custom date range sync.
const MaxCustomRangeDays = 365

// SyncOptions configures a single sync run. Zero-valued fields fall back to
// the user's default SyncDays setting.
type SyncOptions struct {
	Start time.Time
	End   time.Time
}

// activityToSync holds either a new activity from Garmin or a previously failed
// activity being retried.
type activityToSync struct {
	garminID     string
	name         string
	actType      string
	date         string
	startTime    time.Time
	durationSecs float64
	startLat     float64
	startLng     float64
	endLat       float64
	endLng       float64
	isRetry      bool
}

// SyncUser runs a full sync for one user using the default time window.
// Returns the sync_run ID.
func (s *SyncEngine) SyncUser(ctx context.Context, userID int64, trigger string) (int64, error) {
	return s.SyncUserWithOptions(ctx, userID, trigger, SyncOptions{})
}

// SyncUserWithOptions runs a sync for one user. If opts specifies a custom
// date range it is validated and used; otherwise the user's SyncDays default
// is applied. Returns the sync_run ID.
func (s *SyncEngine) SyncUserWithOptions(ctx context.Context, userID int64, trigger string, opts SyncOptions) (int64, error) {
	log := s.logger.With("user_id", userID, "trigger", trigger)

	// Load the user once, used for both time window resolution and feature flags.
	user, err := s.db.GetUserByID(userID)
	if err != nil {
		return 0, fmt.Errorf("sync: get user: %w", err)
	}
	if user == nil {
		return 0, fmt.Errorf("sync: user %d not found", userID)
	}

	// Resolve time window.
	start, end, err := s.resolveTimeWindowFromUser(user, opts)
	if err != nil {
		return 0, err
	}
	log.Info("sync time window", "start", start.Format("2006-01-02"), "end", end.Format("2006-01-02"))

	// 1. Create sync_run record.
	runID, err := s.db.CreateSyncRun(userID, trigger)
	if err != nil {
		return 0, fmt.Errorf("sync: create sync run: %w", err)
	}
	log = log.With("run_id", runID)
	log.Info("sync run started")
	syncStart := time.Now()

	// Run the sync and capture results.
	found, synced, skipped, failed, tripsCreated, syncErr := s.doSync(ctx, userID, runID, log, start, end, user.AutoCreateTrips, user.EnrichTrips)

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
	if updateErr := s.db.UpdateSyncRun(runID, status, found, synced, skipped, failed, tripsCreated, errMsg); updateErr != nil {
		log.Error("failed to update sync run", "error", updateErr)
	}

	log.Info("sync run finished",
		"status", status,
		"found", found,
		"synced", synced,
		"skipped", skipped,
		"failed", failed,
		"trips_created", tripsCreated,
	)

	metrics.ObserveSyncRun(trigger, status, time.Since(syncStart).Seconds(), found, synced, skipped, failed, tripsCreated)

	return runID, syncErr
}

// resolveTimeWindowFromUser returns the start/end for a sync run given an
// already-loaded user. Custom ranges are validated; zero-valued opts fall back
// to the user's SyncDays default.
func (s *SyncEngine) resolveTimeWindowFromUser(user *database.User, opts SyncOptions) (time.Time, time.Time, error) {
	if !opts.Start.IsZero() && !opts.End.IsZero() {
		if !opts.Start.Before(opts.End) {
			return time.Time{}, time.Time{}, fmt.Errorf("%w: start must be before end", ErrInvalidDateRange)
		}
		now := time.Now()
		end := opts.End
		if end.After(now) {
			end = now
		}
		if end.Sub(opts.Start).Hours()/24 > float64(MaxCustomRangeDays) {
			return time.Time{}, time.Time{}, fmt.Errorf("%w: range exceeds %d days", ErrInvalidDateRange, MaxCustomRangeDays)
		}
		return opts.Start, end, nil
	}

	end := time.Now()
	start := end.AddDate(0, 0, -user.SyncDays)
	return start, end, nil
}

// doSync performs the actual sync work and returns counters.
func (s *SyncEngine) doSync(ctx context.Context, userID, runID int64, log *slog.Logger, start, end time.Time, autoCreateTrips, enrichTrips bool) (found, synced, skipped, failed, tripsCreated int, err error) {
	// 2. Get Garmin credentials.
	garminEmail, garminPass, err := s.db.GetGarminCredentials(userID)
	if err != nil {
		return 0, 0, 0, 0, 0, fmt.Errorf("sync: get garmin credentials: %w", err)
	}
	tokenDir := filepath.Join(s.tokenStoreBase, fmt.Sprintf("%d", userID))
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		log.Error("failed to create garmin token store", "error", err)
	}
	garminCreds := garmin.GarminCredentials{
		Email:          garminEmail,
		Password:       garminPass,
		TokenStorePath: tokenDir,
	}

	// 3. List activities from Garmin.
	activities, err := s.garmin.ListActivities(ctx, garminCreds, start, end)
	if err != nil {
		// On auth failure: mark credentials invalid.
		if errors.Is(err, garmin.ErrGarminAuth) || errors.Is(err, garmin.ErrGarminMFARequired) {
			log.Warn("garmin auth failure, invalidating credentials", "error", err)
			if invErr := s.db.InvalidateGarminCredentials(userID, err.Error()); invErr != nil {
				log.Error("failed to invalidate garmin credentials", "error", invErr)
			}
		}
		return 0, 0, 0, 0, 0, fmt.Errorf("sync: list activities: %w", err)
	}

	log.Info("fetched garmin activities", "count", len(activities))

	// Self-heal: ListActivities succeeded, so the stored Garmin creds are good.
	// No-op when is_valid is already 1; rescues users stuck after a transient failure.
	if revErr := s.db.RevalidateGarminCredentials(userID); revErr != nil {
		log.Warn("failed to revalidate garmin credentials", "error", revErr)
	}

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
			garminID:     act.ProviderID,
			name:         act.Name,
			actType:      act.Type,
			date:         act.Date.Format("2006-01-02"),
			startTime:    act.StartTime,
			durationSecs: act.DurationSecs,
			startLat:     act.StartLat,
			startLng:     act.StartLng,
			endLat:       act.EndLat,
			endLng:       act.EndLng,
			isRetry:      failedSet[act.ProviderID],
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
		return found, 0, skipped, 0, 0, nil
	}

	// 7b. Decrypt EFB credentials.
	efbUser, efbPass, err := s.db.GetEFBCredentials(userID)
	if err != nil {
		return found, 0, skipped, 0, 0, fmt.Errorf("sync: get efb credentials: %w", err)
	}

	// Create a fresh EFB session for this user to avoid cookie-jar
	// collisions when multiple workers sync concurrently.
	efbClient := s.newEFBSession()

	// 7c. Login to EFB (once per sync run).
	if err := efbClient.Login(ctx, efbUser, efbPass); err != nil {
		log.Warn("efb login failed, invalidating credentials", "error", err)
		if invErr := s.db.InvalidateEFBCredentials(userID, err.Error()); invErr != nil {
			log.Error("failed to invalidate efb credentials", "error", invErr)
		}
		// Mark all queued activities as failed.
		for _, act := range toSync {
			if recErr := s.db.RecordActivity(userID, act.garminID, act.name, act.actType, act.date, "failed", "efb login failed"); recErr != nil {
				log.Error("failed to record activity", "activity_id", act.garminID, "error", recErr)
			}
			failed++
		}
		return found, 0, skipped, failed, 0, fmt.Errorf("sync: efb login: %w", err)
	}
	log.Info("efb login successful")

	// Self-heal: login succeeded, so the stored EFB creds are good.
	// No-op when is_valid is already 1; rescues users stuck after a transient failure.
	if revErr := s.db.RevalidateEFBCredentials(userID); revErr != nil {
		log.Warn("failed to revalidate efb credentials", "error", revErr)
	}

	// 7. Process each activity.
	for i, act := range toSync {
		log := log.With("activity_id", act.garminID, "activity_name", act.name)

		// 7a. Download GPX from Garmin.
		gpxData, err := s.garmin.DownloadGPX(ctx, garminCreds, act.garminID)
		if err != nil {
			log.Error("failed to download GPX", "error", err)
			if recErr := s.db.RecordActivity(userID, act.garminID, act.name, act.actType, act.date, "failed", err.Error()); recErr != nil {
				log.Error("failed to record activity", "error", recErr)
			}
			if act.isRetry {
				if incErr := s.db.IncrementRetryCount(userID, act.garminID); incErr != nil {
					log.Error("failed to increment retry count", "error", incErr)
				}
				s.checkAndMarkPermanentFailure(userID, act.garminID, log)
			}
			failed++
			continue
		}

		// 7d. Upload GPX to EFB.
		filename := fmt.Sprintf("garmin_%s.gpx", act.garminID)
		uploadErr := efbClient.Upload(ctx, gpxData, filename)
		if uploadErr != nil {
			cat := classifyEFBError(uploadErr)
			log.Error("failed to upload GPX to EFB",
				"error", uploadErr,
				"error_category", cat,
			)
			if recErr := s.recordUploadFailure(userID, act, uploadErr); recErr != nil {
				log.Error("failed to record activity", "error", recErr)
			}
			// Suppress retry-count increment on consent_required: it's a
			// user-action gate, not a transient retriable failure, so we
			// don't want activities marked permanent_failure while the
			// banner is still asking the user to consent.
			if act.isRetry && cat != "consent_required" {
				if incErr := s.db.IncrementRetryCount(userID, act.garminID); incErr != nil {
					log.Error("failed to increment retry count", "error", incErr)
				}
				s.checkAndMarkPermanentFailure(userID, act.garminID, log)
			}
			if cat == "consent_required" {
				s.handleConsentRequired(userID, log)
			}
			failed++

			// On session expiry or 5xx, stop this user's sync — remaining
			// uploads would fail identically.
			if cat == "session_expired" || cat == "server_error" {
				log.Warn("EFB non-recoverable error, stopping sync for this user", "error_category", cat)
				reason := fmt.Sprintf("skipped: EFB %s", cat)
				for _, remaining := range toSync[i+1:] {
					if recErr := s.db.RecordActivity(userID, remaining.garminID, remaining.name, remaining.actType, remaining.date, "failed", reason); recErr != nil {
						log.Error("failed to record activity", "activity_id", remaining.garminID, "error", recErr)
					}
					failed++
				}
				if cat == "session_expired" {
					log.Warn("invalidating EFB credentials due to session expiry")
					if invErr := s.db.InvalidateEFBCredentials(userID, uploadErr.Error()); invErr != nil {
						log.Error("failed to invalidate efb credentials", "error", invErr)
					}
				}
				return found, synced, skipped, failed, tripsCreated, fmt.Errorf("sync: EFB %s, aborting: %w", cat, uploadErr)
			}
			continue
		}

		// 7d: Success.
		log.Info("activity uploaded successfully")
		if recErr := s.db.RecordActivity(userID, act.garminID, act.name, act.actType, act.date, "success", ""); recErr != nil {
			log.Error("failed to record successful activity", "error", recErr)
		}
		// Self-healing for the consent gate: any successful upload proves
		// the user has consented, so clear the flag (idempotent — no-op
		// when not set).
		if clrErr := s.db.ClearEFBConsentRequired(userID); clrErr != nil {
			log.Warn("failed to clear efb consent flag", "error", clrErr)
		}
		synced++

		// 7e: Create trip from the uploaded track (if enabled).
		// Trip creation failure is non-fatal — log and continue.
		if autoCreateTrips && !act.startTime.IsZero() {
			trackID, findErr := efbClient.FindUnassociatedTrack(ctx, filename)
			if findErr != nil {
				log.Warn("failed to find track for trip creation", "error", findErr)
			} else if trackID == "" {
				log.Warn("track not found on EFB for trip creation", "filename", filename)
			} else {
				// Build enrichment from Rivermap if available and enabled.
				var enrichment *efb.TripEnrichment
				if enrichTrips && s.rivermap != nil {
					enrichment = s.buildEnrichment(ctx, act, log)
				}
				if tripErr := efbClient.CreateTripFromTrack(ctx, trackID, act.startTime, act.durationSecs, enrichment); tripErr != nil {
					log.Warn("failed to create trip from track", "error", tripErr)
				} else {
					log.Info("trip created from track", "track_id", trackID)
					tripsCreated++
				}
			}
		}

		// 7f. Sleep between uploads (be gentle with EFB).
		if i < len(toSync)-1 {
			s.sleepFunc(5*time.Second, 10*time.Second)
		}
	}

	return found, synced, skipped, failed, tripsCreated, nil
}

// handleConsentRequired flags the user as needing the EFB v2026.1
// track-usage consent and dispatches a notification email if one
// hasn't been sent in the last ConsentEmailRateLimit window. Failures
// are logged and swallowed — the sync loop must keep going.
func (s *SyncEngine) handleConsentRequired(userID int64, log *slog.Logger) {
	if mErr := s.db.MarkEFBConsentRequired(userID); mErr != nil {
		log.Error("failed to mark efb consent required", "error", mErr)
		return
	}

	if s.mailer == nil {
		return
	}

	_, notifiedAt, err := s.db.GetEFBConsentState(userID)
	if err != nil {
		log.Warn("failed to read efb consent state for rate limit", "error", err)
		return
	}
	now := s.nowFunc()
	if notifiedAt != nil && now.Sub(*notifiedAt) < ConsentEmailRateLimit {
		return // within rate-limit window
	}

	user, err := s.db.GetUserByID(userID)
	if err != nil || user == nil || user.Email == "" {
		log.Warn("cannot send consent email: user lookup failed",
			"error", err)
		return
	}

	if sendErr := s.mailer.Send(
		user.Email,
		i18n.ParseLang(user.PreferredLang),
		"efb_consent",
		map[string]any{"ConsentURL": efbConsentURL},
	); sendErr != nil {
		log.Error("failed to send efb consent email", "error", sendErr)
		return
	}
	if recErr := s.db.RecordEFBConsentNotified(userID, now); recErr != nil {
		log.Warn("failed to record efb consent notified timestamp", "error", recErr)
	}
	log.Info("sent efb consent-required email", "to", user.Email)
}

// recordUploadFailure persists a failed-upload row, populating the
// response_status_code / response_size_bytes / response_body_excerpt
// columns when the error carries them (i.e. a *efb.UploadRejectedError —
// HTTP 200 + no success marker + not a login page).
//
// The body excerpt is stored only for the silent-rejection case because
// that's the only situation where the existing error_message is not
// actionable. For session_expired / network / server_error / Garmin
// download failures the message already carries the cause.
func (s *SyncEngine) recordUploadFailure(userID int64, act activityToSync, uploadErr error) error {
	var rej *efb.UploadRejectedError
	if errors.As(uploadErr, &rej) {
		return s.db.RecordActivityWithResponse(
			userID, act.garminID, act.name, act.actType, act.date,
			"failed", uploadErr.Error(),
			rej.StatusCode, rej.BodySize, rej.BodyExcerpt,
		)
	}
	return s.db.RecordActivity(
		userID, act.garminID, act.name, act.actType, act.date,
		"failed", uploadErr.Error(),
	)
}

// RecheckEFBConsent logs in with the user's stored EFB credentials and
// runs CheckConsentGate, returning whether the gate is still active.
// Used by the dashboard "I've accepted" button so the user can confirm
// consent without triggering a full sync (and burning the rate limit).
//
// Routes through newEFBSession so dev mode uses the in-process mock and
// production gets a fresh per-call EFBClient with its own cookie jar.
//
// Returns (consentRequired, nil) on a successful check, or (_, err) for
// transport / login failures — the caller decides how to surface those.
func (s *SyncEngine) RecheckEFBConsent(ctx context.Context, userID int64) (bool, error) {
	username, password, err := s.db.GetEFBCredentials(userID)
	if err != nil {
		return false, fmt.Errorf("recheck-consent: get efb credentials: %w", err)
	}

	client := s.newEFBSession()
	if err := client.Login(ctx, username, password); err != nil {
		return false, fmt.Errorf("recheck-consent: efb login: %w", err)
	}

	consentRequired, err := client.CheckConsentGate(ctx)
	if err != nil {
		return false, fmt.Errorf("recheck-consent: check gate: %w", err)
	}
	return consentRequired, nil
}

// DebugUploadResult is the dry-run output of [SyncEngine.DebugUploadOnce]:
// the captured upload attempt for an admin to inspect without mutating any
// DB state.
type DebugUploadResult struct {
	UserID           int64               `json:"user_id"`
	GarminActivityID string              `json:"garmin_activity_id"`
	GPXSizeBytes     int                 `json:"gpx_size_bytes"`
	Upload           DebugUploadResponse `json:"upload"`
}

// DebugUploadResponse mirrors [efb.RawUploadResult] for JSON serialisation.
// Body is capped to MaxDebugBodyBytes; Truncated indicates whether the
// original response was larger.
type DebugUploadResponse struct {
	RequestURL            string      `json:"request_url"`
	StatusCode            int         `json:"status_code"`
	FinalURL              string      `json:"final_url"`
	ResponseHeaders       http.Header `json:"response_headers"`
	ResponseBody          string      `json:"response_body"`
	BodySizeBytes         int         `json:"body_size_bytes"`
	Truncated             bool        `json:"truncated"`
	ContainsSuccessMarker bool        `json:"contains_success_marker"`
	IsLoginPage           bool        `json:"is_login_page"`
}

// MaxDebugBodyBytes caps the response body returned by DebugUploadOnce so
// the JSON payload stays bounded even if EFB hands back a huge page.
const MaxDebugBodyBytes = 64 * 1024

// DebugUploadOnce performs a one-shot upload attempt for an admin debug
// session. It logs in with the user's stored EFB credentials, downloads
// the requested Garmin activity, performs the upload, and returns the
// raw response. It does NOT update synced_activities, sync_runs, or any
// other state — the call is purely diagnostic.
//
// If garminID is empty the most recent activity in the user's default
// SyncDays window is used. Returns an error when login or GPX download
// fails; HTTP-level outcomes (any status, including silent rejection)
// are returned as a non-nil result with err == nil.
func (s *SyncEngine) DebugUploadOnce(ctx context.Context, userID int64, garminID string) (*DebugUploadResult, error) {
	user, err := s.db.GetUserByID(userID)
	if err != nil {
		return nil, fmt.Errorf("debug-upload: get user: %w", err)
	}
	if user == nil {
		return nil, fmt.Errorf("debug-upload: user %d not found", userID)
	}

	garminEmail, garminPass, err := s.db.GetGarminCredentials(userID)
	if err != nil {
		return nil, fmt.Errorf("debug-upload: get garmin credentials: %w", err)
	}
	tokenDir := filepath.Join(s.tokenStoreBase, fmt.Sprintf("%d", userID))
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		s.logger.Warn("debug-upload: create garmin token store", "user_id", userID, "error", err)
	}
	garminCreds := garmin.GarminCredentials{
		Email:          garminEmail,
		Password:       garminPass,
		TokenStorePath: tokenDir,
	}

	// Resolve garminID: if not provided, pick the most recent activity in
	// the user's default sync window.
	if garminID == "" {
		end := time.Now()
		start := end.AddDate(0, 0, -user.SyncDays)
		acts, err := s.garmin.ListActivities(ctx, garminCreds, start, end)
		if err != nil {
			return nil, fmt.Errorf("debug-upload: list activities: %w", err)
		}
		if len(acts) == 0 {
			return nil, fmt.Errorf("debug-upload: no garmin activities in window %s..%s",
				start.Format("2006-01-02"), end.Format("2006-01-02"))
		}
		// ListActivities returns newest-first by Garmin convention.
		garminID = acts[0].ProviderID
	}

	gpxData, err := s.garmin.DownloadGPX(ctx, garminCreds, garminID)
	if err != nil {
		return nil, fmt.Errorf("debug-upload: download GPX %q: %w", garminID, err)
	}

	efbUser, efbPass, err := s.db.GetEFBCredentials(userID)
	if err != nil {
		return nil, fmt.Errorf("debug-upload: get efb credentials: %w", err)
	}

	efbClient := s.newEFBSession()
	if err := efbClient.Login(ctx, efbUser, efbPass); err != nil {
		return nil, fmt.Errorf("debug-upload: efb login: %w", err)
	}

	rawClient, ok := efbClient.(rawUploader)
	if !ok {
		return nil, fmt.Errorf("debug-upload: efb provider does not support raw upload")
	}

	filename := fmt.Sprintf("garmin_%s.gpx", garminID)
	raw, err := rawClient.UploadRaw(ctx, gpxData, filename)
	if err != nil {
		return nil, fmt.Errorf("debug-upload: upload: %w", err)
	}

	truncated := len(raw.Body) > MaxDebugBodyBytes
	body := efb.TruncateUTF8(raw.Body, MaxDebugBodyBytes)

	return &DebugUploadResult{
		UserID:           userID,
		GarminActivityID: garminID,
		GPXSizeBytes:     len(gpxData),
		Upload: DebugUploadResponse{
			RequestURL:            raw.RequestURL,
			StatusCode:            raw.StatusCode,
			FinalURL:              raw.FinalURL,
			ResponseHeaders:       raw.Header,
			ResponseBody:          body,
			BodySizeBytes:         raw.BodySize,
			Truncated:             truncated,
			ContainsSuccessMarker: raw.ContainsSuccessMarker,
			IsLoginPage:           raw.IsLoginPage,
		},
	}, nil
}

// rawUploader is the optional interface an [efb.EFBProvider] implements
// when it can return the raw upload response. The production
// *efb.EFBClient satisfies it; mocks in tests can choose to as well.
type rawUploader interface {
	UploadRaw(ctx context.Context, gpxData []byte, filename string) (*efb.RawUploadResult, error)
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

// buildEnrichment queries the Rivermap client for section and gauge data
// matching the activity's start/end coordinates and time. Returns nil if no
// matching section is found or if the rivermap client is unavailable.
func (s *SyncEngine) buildEnrichment(ctx context.Context, act activityToSync, log *slog.Logger) *efb.TripEnrichment {
	sections := s.rivermap.FindSections(act.startLat, act.startLng, act.endLat, act.endLng)
	if len(sections) == 0 {
		log.Debug("no rivermap section found for activity", "lat", act.startLat, "lng", act.startLng)
		return nil
	}

	// Fetch gauge readings per unique station (deduplicate).
	type gaugeData struct {
		name  string
		level *rivermap.Reading
		flow  *rivermap.Reading
	}
	gaugeCache := map[string]*gaugeData{}

	enrichment := &efb.TripEnrichment{}
	for _, section := range sections {
		se := efb.SectionEnrichment{
			SectionName: section.DisplayName(),
			Grade:       section.Grade,
			SpotGrades:  section.SpotGrades,
		}

		if section.Calibration != nil {
			stationID := section.Calibration.StationID
			if _, ok := gaugeCache[stationID]; !ok {
				level, flow, err := s.rivermap.GetReadingsAt(ctx, stationID, act.startTime)
				if err != nil {
					log.Warn("failed to fetch gauge readings", "station", stationID, "error", err)
					gaugeCache[stationID] = &gaugeData{name: s.rivermap.StationName(stationID)}
				} else {
					gaugeCache[stationID] = &gaugeData{
						name:  s.rivermap.StationName(stationID),
						level: level,
						flow:  flow,
					}
				}
			}

			gd := gaugeCache[stationID]
			se.GaugeName = gd.name
			if gd.level != nil {
				se.GaugeReading = fmt.Sprintf("%.0f %s", gd.level.Value, gd.level.Unit)
			}
			if gd.flow != nil {
				se.GaugeFlow = fmt.Sprintf("%.1f %s", gd.flow.Value, gd.flow.Unit)
			}
			switch section.Calibration.Unit {
			case "m3s", "cfs", "lts":
				if gd.flow != nil {
					se.WaterLevel = rivermap.ClassifyLevel(gd.flow.Value, section.Calibration)
				}
			default:
				if gd.level != nil {
					se.WaterLevel = rivermap.ClassifyLevel(gd.level.Value, section.Calibration)
				}
			}
		}

		enrichment.Sections = append(enrichment.Sections, se)
	}

	return enrichment
}

// UserSyncResult holds the outcome of a single user's sync run, used for
// streaming progress back to callers.
type UserSyncResult struct {
	UserID  int64  `json:"user_id"`
	Email   string `json:"email"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
	Found   int    `json:"found"`
	Synced  int    `json:"synced"`
	Skipped int    `json:"skipped"`
	Failed  int    `json:"failed"`
	Trips   int    `json:"trips_created"`
}

// SyncAllUsers syncs all eligible users sequentially. It delegates to
// SyncAllUsersProgress with a single worker and no progress callback.
func (s *SyncEngine) SyncAllUsers(ctx context.Context) error {
	return s.SyncAllUsersProgress(ctx, 1, nil)
}

// SyncAllUsersProgress syncs all eligible users using a pool of concurrent
// workers. After each user completes, onProgress is called (if non-nil) with
// the result. The worker count limits concurrency to avoid overloading
// external APIs.
func (s *SyncEngine) SyncAllUsersProgress(ctx context.Context, workers int, onProgress func(UserSyncResult)) error {
	users, err := s.db.GetSyncableUsers()
	if err != nil {
		return fmt.Errorf("sync: get syncable users: %w", err)
	}

	if workers < 1 {
		workers = 1
	}

	s.logger.Info("starting sync for all users", "user_count", len(users), "workers", workers)

	// Feed users into a work channel.
	work := make(chan database.User, len(users))
	for _, u := range users {
		work <- u
	}
	close(work)

	// Collect results.
	type indexedResult struct {
		result UserSyncResult
	}
	results := make(chan indexedResult, len(users))

	// Spawn workers.
	var wg stdsync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for user := range work {
				if ctx.Err() != nil {
					return
				}

				log := s.logger.With("user_id", user.ID, "email", user.Email)
				log.Info("syncing user")

				runID, syncErr := s.SyncUser(ctx, user.ID, "scheduled")

				result := UserSyncResult{
					UserID: user.ID,
					Email:  user.Email,
				}

				if syncErr != nil {
					log.Error("sync failed for user", "error", syncErr, "run_id", runID)
					result.Status = "failed"
					result.Error = syncErr.Error()
				}

				// Read the sync run from DB to get accurate counters.
				if runID > 0 {
					if run, err := s.db.GetSyncRun(runID); err == nil && run != nil {
						result.Found = run.ActivitiesFound
						result.Synced = run.ActivitiesSynced
						result.Skipped = run.ActivitiesSkipped
						result.Failed = run.ActivitiesFailed
						result.Trips = run.TripsCreated
						// Only use DB status when SyncUser succeeded —
						// otherwise keep the "failed" status set above.
						if syncErr == nil {
							result.Status = run.Status
						}
					}
				}

				if onProgress != nil {
					onProgress(result)
				}

				results <- indexedResult{result: result}
			}
		}()
	}

	// Wait for all workers to finish, then close results.
	go func() {
		wg.Wait()
		close(results)
	}()

	var totalSynced, totalFailed int
	for r := range results {
		if r.result.Status == "failed" {
			totalFailed++
		} else {
			totalSynced++
		}
	}

	s.logger.Info("sync all users finished",
		"total_users", len(users),
		"synced", totalSynced,
		"failed", totalFailed,
	)

	// If context was cancelled, report it.
	if ctx.Err() != nil {
		return ctx.Err()
	}

	return nil
}

// server5xxRe matches the "status 5XX" pattern in EFB error messages, where
// XX are exactly two digits. This is more precise than a plain substring match
// to avoid false positives when EFB returns a 4xx response whose body happens
// to contain the text "status 5". The regexp is compiled once at package
// initialisation and reused on every call to isServer5xxError.
var server5xxRe = regexp.MustCompile(`status 5\d{2}`)

// isServer5xxError checks if an EFB upload error indicates a server-side 5xx
// error by inspecting the error message. The EFB client formats these as
// "efb: upload failed with status 5XX: ...".
func isServer5xxError(err error) bool {
	if err == nil {
		return false
	}
	return server5xxRe.MatchString(err.Error())
}

// classifyEFBError returns a short category string for an EFB error, suitable
// for use as a structured log field. Categories:
//
//	"session_expired"   — the EFB session is invalid (got login page)
//	"server_error"      — EFB returned a 5xx status
//	"consent_required"  — EFB v2026.1 track-usage consent gate active
//	"upload_rejected"   — EFB returned 200 but without the success marker
//	"network"           — connection/timeout error
//	"unknown"           — anything else
//
// "consent_required" must be checked before "upload_rejected" because the
// underlying error string contains both substrings on a consent-gate hit.
func classifyEFBError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "session expired"):
		return "session_expired"
	case isServer5xxError(err):
		return "server_error"
	case strings.Contains(msg, "EFB consent required"):
		return "consent_required"
	case strings.Contains(msg, "upload did not succeed"):
		return "upload_rejected"
	case strings.Contains(msg, "upload request failed"):
		return "network"
	default:
		return "unknown"
	}
}
