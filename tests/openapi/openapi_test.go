package openapi_test

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const specPath = "../../openapi.yaml"

// registeredRoutes lists every route registered in server.go Routes().
// Format: "METHOD /path"
var registeredRoutes = []string{
	"GET /static/{path}",
	"GET /favicon.ico",
	"GET /",
	"GET /login",
	"POST /login",
	"GET /auth/verify",
	"GET /impressum",
	"GET /privacy",
	"POST /auth/logout",
	"GET /dashboard",
	"GET /settings",
	"GET /settings/garmin",
	"POST /settings/garmin",
	"POST /settings/garmin/delete",
	"GET /settings/garmin/mfa",
	"POST /settings/garmin/mfa",
	"GET /settings/efb",
	"POST /settings/efb",
	"POST /settings/efb/delete",
	"POST /settings/auto-create-trips",
	"POST /settings/enrich-trips",
	"POST /settings/language",
	"POST /setup/configure",
	"POST /account/delete",
	"POST /sync/trigger",
	"GET /sync/status",
	"GET /sync/history",
	"POST /feedback",
	"POST /internal/sync/run-all",
	"GET /internal/admin/status",
	"GET /internal/admin/users",
	"GET /internal/admin/users/{id}/sync-history",
	"POST /internal/admin/users/{id}/sync",
	"GET /internal/admin/errors",
	"GET /internal/admin/feedback",
	"POST /internal/admin/notify-garmin-upgrade",
	"GET /health",
}

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(specPath)
	if err != nil {
		t.Fatalf("load openapi spec: %v", err)
	}
	return doc
}

func TestSpecValid(t *testing.T) {
	doc := loadSpec(t)
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("openapi spec validation failed: %v", err)
	}
}

func TestSpecCoversAllRoutes(t *testing.T) {
	doc := loadSpec(t)

	// Build a set of "METHOD /path" from the spec.
	specRoutes := make(map[string]bool)
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			specRoutes[strings.ToUpper(method)+" "+path] = true
		}
	}

	for _, route := range registeredRoutes {
		if !specRoutes[route] {
			t.Errorf("route %q registered in server.go but missing from openapi.yaml", route)
		}
	}
}

func TestServerRoutesMatchList(t *testing.T) {
	// Parse server.go to extract registered routes and ensure our list stays
	// in sync. This catches new endpoints added to server.go but not to the
	// registeredRoutes list (and transitively, the OpenAPI spec).
	f, err := os.Open("../../internal/web/server.go")
	if err != nil {
		t.Fatalf("open server.go: %v", err)
	}
	defer f.Close()

	known := make(map[string]bool, len(registeredRoutes))
	for _, r := range registeredRoutes {
		known[r] = true
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		route := extractRoute(line)
		if route == "" {
			continue
		}
		if !known[route] {
			t.Errorf("route %q found in server.go but not in registeredRoutes list — add it to the test and to openapi.yaml", route)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan server.go: %v", err)
	}
}

// extractRoute parses a mux registration line and returns "METHOD /path" or "".
func extractRoute(line string) string {
	// Match patterns like:
	//   mux.HandleFunc("GET /login", ...)
	//   mux.Handle("POST /auth/logout", ...)
	//   mux.Handle("GET /static/", ...)
	for _, prefix := range []string{"mux.HandleFunc(", "mux.Handle("} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		// Extract the quoted route pattern.
		rest := line[len(prefix):]
		if len(rest) == 0 || rest[0] != '"' {
			continue
		}
		end := strings.Index(rest[1:], "\"")
		if end < 0 {
			continue
		}
		pattern := rest[1 : end+1]

		// Normalize: "GET /static/" → "GET /static/{path}"
		if pattern == "GET /static/" {
			return "GET /static/{path}"
		}

		return pattern
	}
	return ""
}
