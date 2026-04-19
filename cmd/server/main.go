// Command server starts the efb-connector web server.
//
// Required environment variables:
//   - ENCRYPTION_KEY: base64-encoded 32-byte AES-256 key
//   - RESEND_API_KEY: API key for the Resend email service
//   - INTERNAL_SECRET: shared secret for internal admin endpoints
//
// Optional environment variables:
//   - PORT: HTTP listen port (default 8080)
//   - DB_PATH: path to the SQLite database file (default /data/efb-connector.db)
//   - BASE_URL: public URL for magic link emails (e.g. https://efb-connector.fly.dev)
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"efb-connector/internal/auth"
	"efb-connector/internal/database"
	"efb-connector/internal/efb"
	"efb-connector/internal/garmin"
	"efb-connector/internal/rivermap"
	syncsvc "efb-connector/internal/sync"
	"efb-connector/internal/web"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var version = "dev"

func main() {
	// Structured JSON logging for production.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	// ── Parse environment variables ──

	devMode := os.Getenv("DEV_MODE") == "true"

	port := envOr("PORT", "8080")
	dbPath := envOr("DB_PATH", "/data/efb-connector.db")
	baseURL := envOr("BASE_URL", "")
	emailFrom := envOr("EMAIL_FROM", "")

	encKeyB64 := os.Getenv("ENCRYPTION_KEY")
	if encKeyB64 == "" {
		return fmt.Errorf("ENCRYPTION_KEY environment variable is required")
	}
	encryptionKey, err := base64.StdEncoding.DecodeString(encKeyB64)
	if err != nil {
		return fmt.Errorf("ENCRYPTION_KEY is not valid base64: %w", err)
	}
	if len(encryptionKey) != 32 {
		return fmt.Errorf("ENCRYPTION_KEY must decode to 32 bytes, got %d", len(encryptionKey))
	}

	feedbackEmail := envOr("FEEDBACK_EMAIL", "")

	resendAPIKey := os.Getenv("RESEND_API_KEY")
	internalSecret := os.Getenv("INTERNAL_SECRET")

	if devMode {
		logger.Warn("DEV_MODE is active — using mock EFB and Garmin providers")
		if resendAPIKey == "" {
			resendAPIKey = "placeholder"
		}
		if internalSecret == "" {
			internalSecret = "dev-secret"
		}
		if dbPath == "/data/efb-connector.db" {
			dbPath = "efb-connector.db"
		}
	} else {
		if resendAPIKey == "" {
			return fmt.Errorf("RESEND_API_KEY environment variable is required")
		}
		if internalSecret == "" {
			return fmt.Errorf("INTERNAL_SECRET environment variable is required")
		}
	}

	// ── Initialize dependencies ──

	logger.Info("opening database", "path", dbPath)
	db, err := database.Open(dbPath, encryptionKey)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	authService := auth.NewAuthService(db, resendAPIKey, baseURL, emailFrom, encryptionKey)
	rateLimiter := auth.NewRateLimiter()

	var garminProvider garmin.GarminProvider
	var efbProvider efb.EFBProvider

	if devMode {
		garminProvider = garmin.NewMockGarminProvider()
		efbProvider = efb.NewMockEFBProvider(logger)
	} else {
		garminProvider = garmin.NewPythonGarminProvider("scripts/garmin_fetch.py", encryptionKey)
		efbProvider = efb.NewEFBClient(efb.DefaultBaseURL)
	}

	// Optional: Rivermap enrichment
	var rivermapClient *rivermap.Client
	if rivermapKey := os.Getenv("RIVERMAP_API_KEY"); rivermapKey != "" {
		// Use /data/rivermap_cache on Fly.io, local dir otherwise.
		rivermapCacheDir := "rivermap_cache"
		if info, err := os.Stat("/data"); err == nil && info.IsDir() {
			rivermapCacheDir = "/data/rivermap_cache"
		}
		rivermapClient = rivermap.NewClient(rivermapKey, rivermap.DefaultBaseURL, rivermapCacheDir, logger)
		if err := rivermapClient.RefreshCache(context.Background()); err != nil {
			logger.Warn("failed to load rivermap data (enrichment will be unavailable)", "error", err)
			rivermapClient = nil
		} else {
			logger.Info("rivermap data loaded")
		}
	}

	syncEngine := syncsvc.NewSyncEngine(db, garminProvider, efbProvider, logger)
	if rivermapClient != nil {
		syncEngine.SetRivermapClient(rivermapClient)
	}
	if devMode {
		syncEngine.DisableSleep()
	}

	// ── Create server ──

	srv, err := web.NewServer(web.ServerDeps{
		DB:             db,
		Auth:           authService,
		SyncEngine:     syncEngine,
		Garmin:         garminProvider,
		EFB:            efbProvider,
		RateLimiter:    rateLimiter,
		InternalSecret: internalSecret,
		BaseURL:        baseURL,
		FeedbackEmail:  feedbackEmail,
		Logger:         logger,
		TemplatesDir:   "templates",
		Version:        version,
	})
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	httpServer := &http.Server{
		Addr:         ":" + port,
		Handler:      srv.Routes(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// ── Metrics-only server (internal port, no auth) ──
	metricsPort := envOr("METRICS_PORT", "9091")
	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", promhttp.Handler())
	metricsServer := &http.Server{
		Addr:    ":" + metricsPort,
		Handler: metricsMux,
	}
	go func() {
		logger.Info("metrics server starting", "addr", metricsServer.Addr)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server error", "error", err)
		}
	}()

	// ── Periodic cleanup of expired sessions and magic links ──

	stopCleanup := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := db.CleanupExpired(); err != nil {
					logger.Error("cleanup expired records failed", "error", err)
				} else {
					logger.Info("cleanup expired records completed")
				}
			case <-stopCleanup:
				return
			}
		}
	}()

	// ── Graceful shutdown ──

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server starting", "addr", httpServer.Addr, "version", version)
		errCh <- httpServer.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		logger.Info("shutdown signal received", "signal", sig.String())
	case err := <-errCh:
		if err != http.ErrServerClosed {
			return fmt.Errorf("server error: %w", err)
		}
	}

	// Give in-flight requests up to 30 seconds to complete.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	close(stopCleanup)
	logger.Info("shutting down server")
	if err := metricsServer.Shutdown(ctx); err != nil {
		logger.Warn("metrics server shutdown error", "error", err)
	}
	if err := httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}

	logger.Info("server stopped")
	return nil
}

// envOr returns the value of the named environment variable, or fallback if
// unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
