package openapi_test

import (
	"bufio"
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"efb-connector/internal/garmin"
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
	"POST /auth/verify",
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
	"POST /settings/match-by-name",
	"POST /settings/activity-types",
	"POST /settings/language",
	"POST /setup/configure",
	"POST /account/delete",
	"POST /sync/trigger",
	"POST /sync/efb/recheck-consent",
	"GET /sync/status",
	"GET /sync/history",
	"POST /feedback",
	"POST /internal/sync/run-all",
	"GET /internal/sync/run-all/status",
	"GET /internal/admin/status",
	"GET /internal/admin/users",
	"GET /internal/admin/users/{id}/sync-history",
	"POST /internal/admin/users/{id}/sync",
	"POST /internal/admin/users/{id}/debug-upload",
	"GET /internal/admin/users/{id}/garmin/activities-raw",
	"POST /internal/admin/users/{id}/efb/revalidate",
	"GET /internal/admin/errors",
	"GET /internal/admin/activity-errors",
	"GET /internal/admin/activity-errors/{id}",
	"GET /internal/admin/feedback",
	"GET /internal/admin/report",
	"POST /internal/admin/notify-garmin-upgrade",
	"POST /internal/admin/sync-resend-contacts",
	"POST /internal/admin/dev/mock-efb/consent-gate",
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

// The activity-type enum in the spec is a third hand-maintained copy of
// garmin.KnownCategories (the others being the i18n bundles, which
// TestActivityTypeLabelsExist guards). Without this test, adding a category
// leaves the published contract advertising a stale list and generated
// clients rejecting a value the server accepts.
func TestSpecActivityTypeEnumMatchesKnownCategories(t *testing.T) {
	doc := loadSpec(t)

	item := doc.Paths.Value("/settings/activity-types")
	if item == nil || item.Post == nil {
		t.Fatal("spec has no POST /settings/activity-types")
	}
	body := item.Post.RequestBody.Value.Content.Get("application/x-www-form-urlencoded")
	if body == nil || body.Schema == nil {
		t.Fatal("POST /settings/activity-types has no urlencoded schema")
	}
	prop := body.Schema.Value.Properties["category"]
	if prop == nil || prop.Value.Items == nil {
		t.Fatal("schema has no `category` array property")
	}

	got := make([]string, 0, len(prop.Value.Items.Value.Enum))
	for _, v := range prop.Value.Items.Value.Enum {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("enum value %v is not a string", v)
		}
		got = append(got, s)
	}

	if !slices.Equal(got, garmin.KnownCategories) {
		t.Errorf("openapi.yaml category enum = %v,\n want %v\n(update the enum in openapi.yaml to match garmin.KnownCategories)", got, garmin.KnownCategories)
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
