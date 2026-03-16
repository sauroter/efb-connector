// Package metrics defines Prometheus metrics for the efb-connector service.
package metrics

import (
	"fmt"
	"strings"

	"efb-connector/internal/database"

	"github.com/prometheus/client_golang/prometheus"
)

// HTTP metrics.
var (
	HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests.",
	}, []string{"method", "path", "status"})

	HTTPRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)

// Sync metrics.
var (
	SyncRunsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sync_runs_total",
		Help: "Total number of sync runs by trigger and status.",
	}, []string{"trigger", "status"})

	SyncActivitiesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sync_activities_total",
		Help: "Total number of activities processed by result.",
	}, []string{"result"})

	SyncDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "sync_duration_seconds",
		Help:    "Sync run duration in seconds.",
		Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600},
	}, []string{"trigger"})
)

func init() {
	prometheus.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		SyncRunsTotal,
		SyncActivitiesTotal,
		SyncDuration,
	)
}

// ObserveHTTPRequest records metrics for an HTTP request.
func ObserveHTTPRequest(method, path string, status int, durationSeconds float64) {
	p := NormalizePath(path)
	s := fmt.Sprintf("%d", status)
	HTTPRequestsTotal.WithLabelValues(method, p, s).Inc()
	HTTPRequestDuration.WithLabelValues(method, p).Observe(durationSeconds)
}

// ObserveSyncRun records metrics for a completed sync run.
func ObserveSyncRun(trigger, status string, durationSeconds float64, found, synced, skipped, failed int) {
	SyncRunsTotal.WithLabelValues(trigger, status).Inc()
	SyncActivitiesTotal.WithLabelValues("synced").Add(float64(synced))
	SyncActivitiesTotal.WithLabelValues("skipped").Add(float64(skipped))
	SyncActivitiesTotal.WithLabelValues("failed").Add(float64(failed))
	SyncDuration.WithLabelValues(trigger).Observe(durationSeconds)
}

// RegisterDBGauges registers gauges that query the database on each scrape.
func RegisterDBGauges(db *database.DB) {
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "users_total",
		Help: "Total number of registered users.",
	}, func() float64 {
		stats, err := db.GetSystemStats()
		if err != nil {
			return 0
		}
		return float64(stats.TotalUsers)
	}))

	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "users_active",
		Help: "Number of active users.",
	}, func() float64 {
		stats, err := db.GetSystemStats()
		if err != nil {
			return 0
		}
		return float64(stats.ActiveUsers)
	}))

	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "users_syncable",
		Help: "Number of fully connected users (valid Garmin + EFB credentials, sync enabled).",
	}, func() float64 {
		stats, err := db.GetSystemStats()
		if err != nil {
			return 0
		}
		return float64(stats.SyncableUsers)
	}))
}

// NormalizePath reduces HTTP paths to route patterns to avoid high-cardinality labels.
func NormalizePath(path string) string {
	if strings.HasPrefix(path, "/internal/") {
		return "/internal/*"
	}
	if strings.HasPrefix(path, "/auth/") {
		return "/auth/*"
	}
	if strings.HasPrefix(path, "/static/") {
		return "/static/*"
	}
	// Known routes — return as-is.
	switch path {
	case "/", "/login", "/dashboard", "/impressum", "/privacy",
		"/settings/garmin", "/settings/efb", "/settings/garmin/delete", "/settings/efb/delete",
		"/sync/trigger", "/sync/status", "/sync/history",
		"/account/delete", "/health", "/metrics", "/favicon.ico":
		return path
	}
	return "/other"
}
