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

// User-lifecycle metrics.
var (
	UserSignupsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "user_signups_total",
		Help: "Total number of users that have ever signed up.",
	})
)

func init() {
	prometheus.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		SyncRunsTotal,
		SyncActivitiesTotal,
		SyncDuration,
		UserSignupsTotal,
	)
}

// ObserveHTTPRequest records metrics for an HTTP request. pattern is the
// ServeMux pattern that matched it (see RouteLabel), not the raw URL path;
// method is the raw request method and is bounded by MethodLabel.
func ObserveHTTPRequest(method, pattern string, status int, durationSeconds float64) {
	m := MethodLabel(method)
	p := RouteLabel(pattern)
	s := fmt.Sprintf("%d", status)
	HTTPRequestsTotal.WithLabelValues(m, p, s).Inc()
	HTTPRequestDuration.WithLabelValues(m, p).Observe(durationSeconds)
}

// MethodLabel bounds the method label. Go accepts any token as a request
// method, so labelling by r.Method verbatim would let a client mint a new
// series per request ("FOOBAR /"). Standard methods pass through; anything
// else is reported as "OTHER".
func MethodLabel(method string) string {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return method
	}
	return "OTHER"
}

// ObserveSyncRun records metrics for a completed sync run.
func ObserveSyncRun(trigger, status string, durationSeconds float64, found, synced, skipped, failed, tripsCreated int) {
	SyncRunsTotal.WithLabelValues(trigger, status).Inc()
	SyncActivitiesTotal.WithLabelValues("synced").Add(float64(synced))
	SyncActivitiesTotal.WithLabelValues("skipped").Add(float64(skipped))
	SyncActivitiesTotal.WithLabelValues("failed").Add(float64(failed))
	SyncActivitiesTotal.WithLabelValues("trips_created").Add(float64(tripsCreated))
	SyncDuration.WithLabelValues(trigger).Observe(durationSeconds)
}

// RegisterDBGauges registers gauges that query the database on each scrape.
// It is safe to call multiple times (e.g. in tests); duplicate registrations
// are silently ignored.
func RegisterDBGauges(db *database.DB) {
	_ = prometheus.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "users_total",
		Help: "Total number of registered users.",
	}, func() float64 {
		stats, err := db.GetSystemStats()
		if err != nil {
			return 0
		}
		return float64(stats.TotalUsers)
	}))

	_ = prometheus.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "users_active",
		Help: "Number of active users.",
	}, func() float64 {
		stats, err := db.GetSystemStats()
		if err != nil {
			return 0
		}
		return float64(stats.ActiveUsers)
	}))

	_ = prometheus.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "users_syncable",
		Help: "Number of fully connected users (valid Garmin + EFB credentials, sync enabled).",
	}, func() float64 {
		stats, err := db.GetSystemStats()
		if err != nil {
			return 0
		}
		return float64(stats.SyncableUsers)
	}))

	_ = prometheus.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "db_size_bytes",
		Help: "Size of the SQLite database file in bytes.",
	}, func() float64 {
		stats, err := db.GetSystemStats()
		if err != nil {
			return 0
		}
		return float64(stats.DBSizeBytes)
	}))

	_ = prometheus.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "users_synced_distinct",
		Help:        "Distinct users with at least one completed sync run in the last window.",
		ConstLabels: prometheus.Labels{"window": "7d"},
	}, func() float64 {
		n, err := db.CountUsersSyncedSince("-7 days")
		if err != nil {
			return 0
		}
		return float64(n)
	}))

	// Distinct users the nightly bulk run actually reached in the last 24h.
	// Durable (DB-backed), so it survives the mid-run server restart that
	// zeroes the in-memory run-all state. Alert when this falls well below
	// users_syncable: that means the nightly run was abandoned partway and
	// most users were silently skipped.
	_ = prometheus.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "nightly_run_users_reached",
		Help:        "Distinct users reached by the scheduled nightly run in the last window.",
		ConstLabels: prometheus.Labels{"window": "24h"},
	}, func() float64 {
		n, err := db.CountUsersReachedByScheduledRunSince("-24 hours")
		if err != nil {
			return 0
		}
		return float64(n)
	}))
}

// RouteLabel turns the ServeMux pattern that served a request ("GET /settings",
// "POST /internal/admin/users/{id}/sync") into the path label: the method is
// dropped (it has its own label) and wildcards stay unexpanded, so cardinality
// is bounded by the route table. An empty pattern — nothing matched — is
// reported as "/other".
func RouteLabel(pattern string) string {
	if pattern == "" {
		return "/other"
	}
	if _, path, ok := strings.Cut(pattern, " "); ok {
		return path
	}
	return pattern
}
