package web

import (
	"fmt"
	"net/http"
	"time"

	"efb-connector/internal/metrics"
)

// logging is HTTP middleware that logs every request with method, path, status
// code, and duration. Metrics are labelled by the route pattern that serves
// the request, looked up on s.routeMux.
func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(sw, r)

		duration := time.Since(start)
		s.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", duration.Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
		metrics.ObserveHTTPRequest(r.Method, routePattern(s.routeMux, r), sw.status, duration.Seconds())
	})
}

// routePattern returns the mux pattern that serves r, or "" when none does.
// "GET /" is the landing page but also the mux's catch-all, so it only counts
// for the root path itself — otherwise every 404 would be labelled "/".
//
// This relies on every registered pattern carrying a method. The catch-all
// guard compares against "GET /" literally, so a method-less "/" (or another
// method-less pattern matched via a trailing-slash redirect) would slip past
// it and label unrelated paths with that pattern.
func routePattern(mux *http.ServeMux, r *http.Request) string {
	if mux == nil {
		return ""
	}
	_, pattern := mux.Handler(r)
	if pattern == "GET /" && r.URL.Path != "/" {
		return ""
	}
	return pattern
}

// recovery is HTTP middleware that recovers from panics in downstream handlers,
// logs the panic, and returns a 500 Internal Server Error.
func (s *Server) recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("panic recovered",
					"error", fmt.Sprintf("%v", rec),
					"method", r.Method,
					"path", r.URL.Path,
				)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// securityHeaders is HTTP middleware that sets common security-related response
// headers on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// statusWriter wraps http.ResponseWriter to capture the status code written.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (sw *statusWriter) WriteHeader(code int) {
	if !sw.wroteHeader {
		sw.status = code
		sw.wroteHeader = true
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if !sw.wroteHeader {
		sw.wroteHeader = true
	}
	return sw.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer so streaming handlers (NDJSON admin
// endpoints) still flush through the logging middleware. A plain type
// assertion to http.Flusher on the wrapper would otherwise fail.
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		sw.wroteHeader = true
		f.Flush()
	}
}

// Unwrap exposes the underlying writer to http.ResponseController, without
// which SetWriteDeadline cannot reach the connection and fails silently.
func (sw *statusWriter) Unwrap() http.ResponseWriter {
	return sw.ResponseWriter
}

// extendWriteDeadline lifts the server-wide WriteTimeout for one response.
// Handlers whose work budget exceeds it must call this, or the client sees
// the connection closed before the result is written.
func (s *Server) extendWriteDeadline(w http.ResponseWriter, d time.Duration) {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d)); err != nil {
		s.logger.Warn("could not extend write deadline", "error", err)
	}
}
