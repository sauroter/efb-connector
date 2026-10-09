package garmin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"efb-connector/internal/crypto"
)

// TestGarminFetchPyRegression drives scripts/garmin_fetch_test.py through
// `python3 -m unittest` so the pure-Python filter / regex logic gets
// regression coverage in CI without needing a separate Python test runner
// in the workflow file. Skipped when python3 is not on PATH (Windows dev
// box, broken installer).
func TestGarminFetchPyRegression(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("python3 invocation pattern differs on Windows")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; skipping garmin_fetch_test.py regression run")
	}

	// Locate the project root: this test sits in internal/garmin/, so
	// repo root is two levels up.
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve project root: %v", err)
	}

	cmd := exec.Command("python3", "-m", "unittest", "scripts.garmin_fetch_test")
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("garmin_fetch_test.py failed:\n%s\nerror: %v", out, err)
	}
}

// writeMockScript writes a Python mock script to dir/garmin_mock.py and
// returns its path.  The script reads credentials from stdin, validates them
// are valid JSON, then behaves according to the first CLI argument.
func writeMockScript(t *testing.T, dir, body string) string {
	t.Helper()
	script := filepath.Join(dir, "garmin_mock.py")
	content := `#!/usr/bin/env python3
import json, sys, os, argparse

# Read credentials from stdin (ignore EOF gracefully)
try:
    raw = sys.stdin.read()
    creds = json.loads(raw) if raw.strip() else {}
except Exception:
    creds = {}

` + body
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatalf("writeMockScript: %v", err)
	}
	return script
}

// newCreds returns a populated GarminCredentials for use in tests.
func newCreds() GarminCredentials {
	return GarminCredentials{
		Email:          "test@example.com",
		Password:       "secret",
		TokenStorePath: "/tmp/tokens",
	}
}

// ---- ListActivities ---------------------------------------------------------

func TestListActivities_ParsesJSON(t *testing.T) {
	dir := t.TempDir()
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
lp = sub.add_parser("list")
lp.add_argument("--days", type=int, default=30)
lp.add_argument("--start")
lp.add_argument("--end")
lp.add_argument("--json", action="store_true")
args = parser.parse_args()

activities = [
    {"id": 123456, "name": "Morning Paddle", "type": "kayaking_v2",
     "parent_type_id": 228, "date": "2026-03-10", "start_time": "2026-03-10 14:30:00",
     "start_lat": 47.58, "start_lng": 12.70,
     "end_lat": 47.60, "end_lng": 12.71,
     "duration": 3600.0, "distance": 5000.0},
    {"id": 789012, "name": "River Run", "type": "canoeing",
     "parent_type_id": 228, "date": "2026-03-12", "start_time": "2026-03-12 09:15:00",
     "start_lat": 47.58, "start_lng": 12.70,
     "end_lat": 47.61, "end_lng": 12.72,
     "duration": 7200.0, "distance": 12000.0},
]
print(json.dumps(activities))
`)

	p := NewPythonGarminProvider(script, nil)
	ctx := context.Background()

	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	activities, _, err := p.ListActivities(ctx, newCreds(), start, end, ListOptions{})
	if err != nil {
		t.Fatalf("ListActivities returned error: %v", err)
	}

	if len(activities) != 2 {
		t.Fatalf("expected 2 activities, got %d", len(activities))
	}

	// First activity
	a0 := activities[0]
	if a0.ProviderID != "123456" {
		t.Errorf("a0.ProviderID = %q, want %q", a0.ProviderID, "123456")
	}
	if a0.Name != "Morning Paddle" {
		t.Errorf("a0.Name = %q, want %q", a0.Name, "Morning Paddle")
	}
	if a0.Type != "kayaking_v2" {
		t.Errorf("a0.Type = %q, want %q", a0.Type, "kayaking_v2")
	}
	if a0.DurationSecs != 3600.0 {
		t.Errorf("a0.DurationSecs = %v, want 3600.0", a0.DurationSecs)
	}
	if a0.DistanceM != 5000.0 {
		t.Errorf("a0.DistanceM = %v, want 5000.0", a0.DistanceM)
	}
	wantDate := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	if !a0.Date.Equal(wantDate) {
		t.Errorf("a0.Date = %v, want %v", a0.Date, wantDate)
	}
	wantStartTime := time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC)
	if !a0.StartTime.Equal(wantStartTime) {
		t.Errorf("a0.StartTime = %v, want %v", a0.StartTime, wantStartTime)
	}
	if a0.StartLat != 47.58 {
		t.Errorf("a0.StartLat = %v, want 47.58", a0.StartLat)
	}
	if a0.StartLng != 12.70 {
		t.Errorf("a0.StartLng = %v, want 12.70", a0.StartLng)
	}
	if a0.EndLat != 47.60 {
		t.Errorf("a0.EndLat = %v, want 47.60", a0.EndLat)
	}
	if a0.EndLng != 12.71 {
		t.Errorf("a0.EndLng = %v, want 12.71", a0.EndLng)
	}

	// Second activity
	a1 := activities[1]
	if a1.ProviderID != "789012" {
		t.Errorf("a1.ProviderID = %q, want %q", a1.ProviderID, "789012")
	}
}

// A custom range in the past must reach Garmin as that range. Sending only a
// day count made the script fetch the most recent N days instead, which the
// window filter then emptied.
func TestListActivities_PastWindowSendsExplicitDates(t *testing.T) {
	dir := t.TempDir()
	// Simulates Garmin: inclusive date query over a fixed history.
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
lp = sub.add_parser("list")
lp.add_argument("--days", type=int, default=30)
lp.add_argument("--start")
lp.add_argument("--end")
lp.add_argument("--json", action="store_true")
args = parser.parse_args()
if args.start is None or args.end is None:
    print("expected --start and --end, got --days %d" % args.days, file=sys.stderr)
    sys.exit(1)
with open(os.path.join(os.path.dirname(__file__), "window"), "w") as f:
    f.write(args.start + " " + args.end)

history = ["2025-10-30", "2025-10-31", "2025-11-01", "2025-11-30", "2025-12-01"]
print(json.dumps([
    {"id": i, "name": "Kajak", "type": "kayaking_v2", "parent_type_id": 228,
     "date": d, "start_time": d + " 10:00:00"}
    for i, d in enumerate(history) if args.start <= d <= args.end
]))
`)

	p := NewPythonGarminProvider(script, nil)
	start := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC) // exclusive, as the custom-range handler passes it

	activities, _, err := p.ListActivities(context.Background(), newCreds(), start, end, ListOptions{})
	if err != nil {
		t.Fatalf("ListActivities returned error: %v", err)
	}

	window, err := os.ReadFile(filepath.Join(dir, "window"))
	if err != nil {
		t.Fatalf("mock script did not record the requested window: %v", err)
	}
	if got, want := string(window), "2025-10-31 2025-12-01"; got != want {
		t.Errorf("requested Garmin window = %q, want %q (one day of slack on each side)", got, want)
	}

	// The slack days 2025-10-31 and 2025-12-01 are fetched but trimmed by
	// the window filter.
	var got []string
	for _, a := range activities {
		got = append(got, a.Date.Format("2006-01-02"))
	}
	want := []string{"2025-11-01", "2025-11-30"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("activity dates = %v, want %v", got, want)
	}
}

func TestListActivities_EmptyList(t *testing.T) {
	dir := t.TempDir()
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
lp = sub.add_parser("list")
lp.add_argument("--days", type=int, default=30)
lp.add_argument("--start")
lp.add_argument("--end")
lp.add_argument("--json", action="store_true")
parser.parse_args()
print(json.dumps([]))
`)

	p := NewPythonGarminProvider(script, nil)
	activities, _, err := p.ListActivities(context.Background(), newCreds(),
		time.Now().Add(-24*time.Hour), time.Now(), ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(activities) != 0 {
		t.Errorf("expected empty slice, got %d activities", len(activities))
	}
}

func TestListActivities_ScriptExitNonZero(t *testing.T) {
	dir := t.TempDir()
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
lp = sub.add_parser("list")
lp.add_argument("--days", type=int, default=30)
lp.add_argument("--start")
lp.add_argument("--end")
lp.add_argument("--json", action="store_true")
parser.parse_args()
print("garmin: authentication failed", file=sys.stderr)
sys.exit(1)
`)

	p := NewPythonGarminProvider(script, nil)
	_, _, err := p.ListActivities(context.Background(), newCreds(),
		time.Now().Add(-24*time.Hour), time.Now(), ListOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrGarminAuth) {
		t.Errorf("expected ErrGarminAuth, got: %v", err)
	}
}

func TestListActivities_MFAError(t *testing.T) {
	dir := t.TempDir()
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
lp = sub.add_parser("list")
lp.add_argument("--days", type=int, default=30)
lp.add_argument("--start")
lp.add_argument("--end")
lp.add_argument("--json", action="store_true")
parser.parse_args()
print("MFA required by Garmin", file=sys.stderr)
sys.exit(1)
`)

	p := NewPythonGarminProvider(script, nil)
	_, _, err := p.ListActivities(context.Background(), newCreds(),
		time.Now().Add(-24*time.Hour), time.Now(), ListOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrGarminMFARequired) {
		t.Errorf("expected ErrGarminMFARequired, got: %v", err)
	}
}

func TestListActivities_ForwardsMatchByNameFlag(t *testing.T) {
	dir := t.TempDir()
	// Mock script that records whether --match-by-name was passed by
	// returning a distinct activity name based on the flag.
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
lp = sub.add_parser("list")
lp.add_argument("--days", type=int, default=30)
lp.add_argument("--start")
lp.add_argument("--end")
lp.add_argument("--json", action="store_true")
lp.add_argument("--no-filter", action="store_true")
lp.add_argument("--match-by-name", action="store_true")
ns = parser.parse_args()
print(json.dumps([{
    "id": 1,
    "name": "match-by-name=" + str(ns.match_by_name),
    "type": "kayaking_v2",
    "parent_type_id": 228,
    "date": "2026-05-01",
    "start_time": "2026-05-01 12:00:00",
    "start_lat": 0, "start_lng": 0, "end_lat": 0, "end_lng": 0,
    "duration": 0, "distance": 0,
}]))
`)

	p := NewPythonGarminProvider(script, nil)
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)

	// Off: expect "match-by-name=False" in returned name.
	acts, _, err := p.ListActivities(context.Background(), newCreds(), start, end, ListOptions{})
	if err != nil {
		t.Fatalf("ListActivities (off): %v", err)
	}
	if len(acts) != 1 || acts[0].Name != "match-by-name=False" {
		t.Fatalf("expected one activity with name 'match-by-name=False', got %+v", acts)
	}

	// On: expect "match-by-name=True".
	acts, _, err = p.ListActivities(context.Background(), newCreds(), start, end, ListOptions{MatchByName: true})
	if err != nil {
		t.Fatalf("ListActivities (on): %v", err)
	}
	if len(acts) != 1 || acts[0].Name != "match-by-name=True" {
		t.Fatalf("expected one activity with name 'match-by-name=True', got %+v", acts)
	}
}

func TestListActivities_ForwardsNameKeywords(t *testing.T) {
	dir := t.TempDir()
	// Mock script that echoes the received --name-keyword values (joined
	// by "|") as the activity name, so the test can see exactly what the
	// Go side forwarded.
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
lp = sub.add_parser("list")
lp.add_argument("--days", type=int, default=30)
lp.add_argument("--start")
lp.add_argument("--end")
lp.add_argument("--json", action="store_true")
lp.add_argument("--no-filter", action="store_true")
lp.add_argument("--match-by-name", action="store_true")
lp.add_argument("--name-keyword", action="append", dest="name_keywords", default=[])
ns = parser.parse_args()
print(json.dumps([{
    "id": 1,
    "name": "keywords=" + "|".join(ns.name_keywords),
    "type": "other",
    "parent_type_id": 17,
    "date": "2026-05-01",
    "start_time": "2026-05-01 12:00:00",
    "start_lat": 0, "start_lng": 0, "end_lat": 0, "end_lng": 0,
    "duration": 0, "distance": 0,
}]))
`)

	p := NewPythonGarminProvider(script, nil)
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	// A keyword starting with "-" is a plausible thing to type and must not
	// be mistaken for a flag by argparse — that would fail every sync for
	// that user with an error that never mentions keywords.
	keywords := []string{"Drachenboot", "Outrigger Canoe", "-Kanu"}

	// With match-by-name on, every keyword is forwarded as its own flag —
	// including one containing a space and one starting with a dash.
	acts, _, err := p.ListActivities(context.Background(), newCreds(), start, end, ListOptions{
		MatchByName:  true,
		NameKeywords: keywords,
	})
	if err != nil {
		t.Fatalf("ListActivities (on): %v", err)
	}
	if len(acts) != 1 || acts[0].Name != "keywords=Drachenboot|Outrigger Canoe|-Kanu" {
		t.Fatalf("expected forwarded keywords, got %+v", acts)
	}

	// With match-by-name off, keywords are meaningless and must not be sent.
	acts, _, err = p.ListActivities(context.Background(), newCreds(), start, end, ListOptions{
		NameKeywords: keywords,
	})
	if err != nil {
		t.Fatalf("ListActivities (off): %v", err)
	}
	if len(acts) != 1 || acts[0].Name != "keywords=" {
		t.Fatalf("expected no forwarded keywords, got %+v", acts)
	}
}

func TestParseListDiagnostics_RequiresLineStart(t *testing.T) {
	// A Python traceback line that happens to contain the substring
	// "DIAGNOSTICS: " mid-line must not be parsed as a real diagnostics
	// envelope. The marker has to be the first non-whitespace token.
	stderr := `Some debug output
Traceback (most recent call last):
  File "x.py", line 42, in <module>
    raise ValueError("printing DIAGNOSTICS: {fake} for context")
DIAGNOSTICS: {"raw_count": 5, "type_keys_seen": ["cycling"], "name_matched_count": 1}
`

	diag := parseListDiagnostics(stderr)
	if diag.RawCount != 5 {
		t.Errorf("RawCount = %d, want 5 (parser took the real line, not the traceback)", diag.RawCount)
	}
	if diag.NameMatchedCount != 1 {
		t.Errorf("NameMatchedCount = %d, want 1", diag.NameMatchedCount)
	}
}

func TestParseListDiagnostics_IgnoresInlineMarker(t *testing.T) {
	// No real DIAGNOSTICS line at all — only an inline mention inside
	// a traceback. Must return zero value, not fish out the inline JSON.
	stderr := `raise ValueError("fake DIAGNOSTICS: {\"raw_count\": 999}")`

	diag := parseListDiagnostics(stderr)
	if diag.RawCount != 0 || diag.TypeKeysSeen != nil {
		t.Errorf("expected zero diagnostics, got %+v", diag)
	}
}

func TestListActivities_DiagnosticsParsedFromStderr(t *testing.T) {
	dir := t.TempDir()
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
lp = sub.add_parser("list")
lp.add_argument("--days", type=int, default=30)
lp.add_argument("--start")
lp.add_argument("--end")
lp.add_argument("--json", action="store_true")
parser.parse_args()
# Emit a diagnostics line on stderr alongside the (filtered) JSON list.
print('DIAGNOSTICS: {"raw_count": 351, "type_keys_seen": ["cycling","other","running"], "name_matched_count": 2}', file=sys.stderr)
print(json.dumps([]))
`)

	p := NewPythonGarminProvider(script, nil)
	_, diag, err := p.ListActivities(context.Background(), newCreds(),
		time.Now().Add(-24*time.Hour), time.Now(), ListOptions{})
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	if diag.RawCount != 351 {
		t.Errorf("RawCount = %d, want 351", diag.RawCount)
	}
	if got, want := strings.Join(diag.TypeKeysSeen, ","), "cycling,other,running"; got != want {
		t.Errorf("TypeKeysSeen = %v, want %s", diag.TypeKeysSeen, want)
	}
	if diag.NameMatchedCount != 2 {
		t.Errorf("NameMatchedCount = %d, want 2", diag.NameMatchedCount)
	}
}

func TestListActivities_MissingDiagnosticsLineIsTolerated(t *testing.T) {
	dir := t.TempDir()
	// Old-style script with no DIAGNOSTICS line at all — Python rollback
	// shouldn't break Go callers.
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
lp = sub.add_parser("list")
lp.add_argument("--days", type=int, default=30)
lp.add_argument("--start")
lp.add_argument("--end")
lp.add_argument("--json", action="store_true")
parser.parse_args()
print(json.dumps([]))
`)

	p := NewPythonGarminProvider(script, nil)
	_, diag, err := p.ListActivities(context.Background(), newCreds(),
		time.Now().Add(-24*time.Hour), time.Now(), ListOptions{})
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	if diag.RawCount != 0 || diag.TypeKeysSeen != nil {
		t.Errorf("expected zero diagnostics, got %+v", diag)
	}
}

func TestListActivities_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
lp = sub.add_parser("list")
lp.add_argument("--days", type=int, default=30)
lp.add_argument("--start")
lp.add_argument("--end")
lp.add_argument("--json", action="store_true")
parser.parse_args()
print("this is not JSON")
`)

	p := NewPythonGarminProvider(script, nil)
	_, _, err := p.ListActivities(context.Background(), newCreds(),
		time.Now().Add(-24*time.Hour), time.Now(), ListOptions{})
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse list output") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ---- DownloadGPX ------------------------------------------------------------

func TestDownloadGPX_ReturnsFileBytes(t *testing.T) {
	const wantGPX = `<?xml version="1.0"?><gpx version="1.1"><trk><name>Test</name></trk></gpx>`

	dir := t.TempDir()
	// The mock script creates the GPX file at the expected path and prints it.
	script := writeMockScript(t, dir, fmt.Sprintf(`
import argparse, os
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
fp = sub.add_parser("fetch")
fp.add_argument("activity_id")
fp.add_argument("--output", "-o", default=".")
args = parser.parse_args()

gpx_content = %q
filepath = os.path.join(args.output, "activity_" + str(args.activity_id) + ".gpx")
with open(filepath, "w") as f:
    f.write(gpx_content)
print(filepath)
`, wantGPX))

	p := NewPythonGarminProvider(script, nil)
	got, err := p.DownloadGPX(context.Background(), newCreds(), "99887766")
	if err != nil {
		t.Fatalf("DownloadGPX returned error: %v", err)
	}
	if string(got) != wantGPX {
		t.Errorf("got GPX content %q, want %q", got, wantGPX)
	}
}

func TestDownloadGPX_TempDirCleaned(t *testing.T) {
	// Verify that no garmin-gpx-* temp dirs leak after DownloadGPX returns.
	dir := t.TempDir()

	script := writeMockScript(t, dir, `
import argparse, os
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
fp = sub.add_parser("fetch")
fp.add_argument("activity_id")
fp.add_argument("--output", "-o", default=".")
args = parser.parse_args()

gpx_path = os.path.join(args.output, "activity_" + str(args.activity_id) + ".gpx")
with open(gpx_path, "w") as f:
    f.write("<gpx/>")
print(gpx_path)
`)

	// Wrap the provider to intercept the temp dir.  Since PythonGarminProvider
	// creates the temp dir internally we verify by checking that the file
	// returned is valid and no temp dirs leaking by calling os.ReadDir on os.TempDir.
	p := NewPythonGarminProvider(script, nil)
	tmpsBefore, _ := filepath.Glob(filepath.Join(os.TempDir(), "garmin-gpx-*"))

	data, err := p.DownloadGPX(context.Background(), newCreds(), "42")
	if err != nil {
		t.Fatalf("DownloadGPX returned error: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty GPX data")
	}

	tmpsAfter, _ := filepath.Glob(filepath.Join(os.TempDir(), "garmin-gpx-*"))
	if len(tmpsAfter) > len(tmpsBefore) {
		t.Errorf("temp dir not cleaned up: before=%d, after=%d dirs",
			len(tmpsBefore), len(tmpsAfter))
	}
}

func TestDownloadGPX_ScriptExitNonZero(t *testing.T) {
	dir := t.TempDir()
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
fp = sub.add_parser("fetch")
fp.add_argument("activity_id")
fp.add_argument("--output", "-o", default=".")
parser.parse_args()
print("Error fetching GPX", file=sys.stderr)
sys.exit(1)
`)

	p := NewPythonGarminProvider(script, nil)
	_, err := p.DownloadGPX(context.Background(), newCreds(), "99")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "subprocess error") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestDownloadGPX_EmptyStdout(t *testing.T) {
	dir := t.TempDir()
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
fp = sub.add_parser("fetch")
fp.add_argument("activity_id")
fp.add_argument("--output", "-o", default=".")
parser.parse_args()
# Print nothing — simulate script that succeeds but gives no path.
`)

	p := NewPythonGarminProvider(script, nil)
	_, err := p.DownloadGPX(context.Background(), newCreds(), "99")
	if err == nil {
		t.Fatal("expected error for empty stdout, got nil")
	}
	if !strings.Contains(err.Error(), "did not return a file path") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestDownloadGPX_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	// Script returns a path that escapes the temp directory.
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
fp = sub.add_parser("fetch")
fp.add_argument("activity_id")
fp.add_argument("--output", "-o", default=".")
parser.parse_args()
print("/etc/passwd")
`)

	p := NewPythonGarminProvider(script, nil)
	_, err := p.DownloadGPX(context.Background(), newCreds(), "99")
	if err == nil {
		t.Fatal("expected path traversal error, got nil")
	}
	if !strings.Contains(err.Error(), "escapes temp directory") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ---- ValidateCredentials ----------------------------------------------------

func TestValidateCredentials_Success(t *testing.T) {
	dir := t.TempDir()
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
sub.add_parser("validate")
parser.parse_args()
# Exit 0 — credentials are valid.
`)

	p := NewPythonGarminProvider(script, nil)
	if err := p.ValidateCredentials(context.Background(), newCreds()); err != nil {
		t.Errorf("ValidateCredentials returned unexpected error: %v", err)
	}
}

func TestValidateCredentials_AuthFailure(t *testing.T) {
	dir := t.TempDir()
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
sub.add_parser("validate")
parser.parse_args()
print("login failed: invalid credentials", file=sys.stderr)
sys.exit(1)
`)

	p := NewPythonGarminProvider(script, nil)
	err := p.ValidateCredentials(context.Background(), newCreds())
	if err == nil {
		t.Fatal("expected ErrGarminAuth, got nil")
	}
	if !errors.Is(err, ErrGarminAuth) {
		t.Errorf("expected ErrGarminAuth, got: %v", err)
	}
}

func TestValidateCredentials_MFARequired(t *testing.T) {
	dir := t.TempDir()
	script := writeMockScript(t, dir, `
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
sub.add_parser("validate")
parser.parse_args()
print("MFA required", file=sys.stderr)
sys.exit(1)
`)

	p := NewPythonGarminProvider(script, nil)
	err := p.ValidateCredentials(context.Background(), newCreds())
	if err == nil {
		t.Fatal("expected ErrGarminMFARequired, got nil")
	}
	if !errors.Is(err, ErrGarminMFARequired) {
		t.Errorf("expected ErrGarminMFARequired, got: %v", err)
	}
}

// ---- Credentials passed via stdin -------------------------------------------

// TestCredentialsPassedViaStdin verifies that the Go code writes a valid JSON
// credentials envelope to the subprocess stdin — the core security requirement
// for multi-tenant use.
func TestCredentialsPassedViaStdin(t *testing.T) {
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "received_creds.json")

	script := writeMockScript(t, dir, fmt.Sprintf(`
import argparse
parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd")
sub.add_parser("validate")
parser.parse_args()

# Write received creds to a file for the test to inspect.
with open(%q, "w") as f:
    f.write(json.dumps(creds))
`, credsFile))

	p := NewPythonGarminProvider(script, nil)
	_ = p.ValidateCredentials(context.Background(), GarminCredentials{
		Email:          "user@example.com",
		Password:       "p@ssw0rd",
		TokenStorePath: "/data/tokens/7",
	})

	raw, err := os.ReadFile(credsFile)
	if err != nil {
		t.Fatalf("creds file not written by mock script: %v", err)
	}

	var got stdinCreds
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not parse received creds: %v", err)
	}

	if got.Email != "user@example.com" {
		t.Errorf("Email = %q, want %q", got.Email, "user@example.com")
	}
	if got.Password != "p@ssw0rd" {
		t.Errorf("Password = %q, want %q", got.Password, "p@ssw0rd")
	}
	if got.TokenStore != "/data/tokens/7" {
		t.Errorf("TokenStore = %q, want %q", got.TokenStore, "/data/tokens/7")
	}
}

// ---- Helper: toStringID -----------------------------------------------------

func TestToStringID(t *testing.T) {
	tests := []struct {
		input   interface{}
		want    string
		wantErr bool
	}{
		{float64(123456), "123456", false},
		{float64(123456.7), "123456.7", false},
		{"abc123", "abc123", false},
		{json.Number("987"), "987", false},
		{nil, "", true},
	}
	for _, tt := range tests {
		got, err := toStringID(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("toStringID(%v) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("toStringID(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---- Token encryption -------------------------------------------------------

func TestTokenEncryptionRoundTrip(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Simulate a tokenstore with a plaintext token file (garminconnect 0.3.x format).
	tokenStoreDir := t.TempDir()
	tokens := []byte(`{"di_token":"at","di_refresh_token":"rt","di_client_id":"cid"}`)

	if err := os.WriteFile(filepath.Join(tokenStoreDir, "garmin_tokens.json"), tokens, 0600); err != nil {
		t.Fatal(err)
	}

	// Encrypt the tokens.
	encryptTokenStore(key, tokenStoreDir, tokenStoreDir)

	// Verify .enc file exists.
	if _, err := os.Stat(filepath.Join(tokenStoreDir, "garmin_tokens.json.enc")); err != nil {
		t.Errorf("expected garmin_tokens.json.enc to exist: %v", err)
	}

	// Verify plaintext file was removed.
	if _, err := os.Stat(filepath.Join(tokenStoreDir, "garmin_tokens.json")); err == nil {
		t.Error("expected garmin_tokens.json to be removed after encryption")
	}

	// Decrypt to a new directory and verify contents match.
	decryptDir := t.TempDir()
	decryptTokenStore(key, tokenStoreDir, decryptDir)

	got, err := os.ReadFile(filepath.Join(decryptDir, "garmin_tokens.json"))
	if err != nil {
		t.Fatalf("failed to read decrypted garmin_tokens.json: %v", err)
	}
	if string(got) != string(tokens) {
		t.Errorf("garmin_tokens.json: got %q, want %q", got, tokens)
	}
}

func TestTokenEncryption_MissingFilesSkipped(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Empty directories — nothing to encrypt or decrypt.
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Should not panic or error.
	encryptTokenStore(key, srcDir, dstDir)
	decryptTokenStore(key, srcDir, dstDir)
}

func TestTokenEncryption_CorruptedFileSkipped(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Write garbage as an encrypted file.
	if err := os.WriteFile(filepath.Join(srcDir, "garmin_tokens.json.enc"), []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}

	// Should not panic — corrupted files are silently skipped.
	decryptTokenStore(key, srcDir, dstDir)

	// The decrypted file should not exist.
	if _, err := os.Stat(filepath.Join(dstDir, "garmin_tokens.json")); err == nil {
		t.Error("expected garmin_tokens.json to not be created from corrupted .enc file")
	}
}

func TestCleanupLegacyTokens(t *testing.T) {
	dir := t.TempDir()

	// Create legacy garth-era token files.
	for _, name := range []string{"oauth1_token.json.enc", "oauth2_token.json.enc", "oauth1_token.json", "oauth2_token.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// Create the new-format token file that should be preserved.
	if err := os.WriteFile(filepath.Join(dir, "garmin_tokens.json.enc"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	cleanupLegacyTokens(dir)

	// Legacy files should be gone.
	for _, name := range []string{"oauth1_token.json.enc", "oauth2_token.json.enc", "oauth1_token.json", "oauth2_token.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("expected %s to be removed", name)
		}
	}

	// New-format file should be preserved.
	if _, err := os.Stat(filepath.Join(dir, "garmin_tokens.json.enc")); err != nil {
		t.Errorf("expected garmin_tokens.json.enc to be preserved: %v", err)
	}
}

// TestMakeTokenTempDir_NoSymlinkedAncestors locks in the fix for a silent
// token-caching failure on macOS: garminconnect >= 0.3.10 refuses a tokenstore
// path with a symlink anywhere in its ancestry, and os.MkdirTemp("") returns a
// path under the symlinked /var on Darwin. Without resolution, token load and
// dump both fail inside garminconnect's suppressed error handling, so every
// Garmin call silently falls back to a full SSO credential login.
func TestMakeTokenTempDir_NoSymlinkedAncestors(t *testing.T) {
	dir, err := makeTokenTempDir()
	if err != nil {
		t.Fatalf("makeTokenTempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	if dir != resolved {
		t.Errorf("token temp dir %q has a symlinked ancestor (resolves to %q); "+
			"garminconnect >=0.3.10 rejects such a tokenstore", dir, resolved)
	}
}

// The directory must still be usable for the decrypted token files.
func TestMakeTokenTempDir_Writable(t *testing.T) {
	dir, err := makeTokenTempDir()
	if err != nil {
		t.Fatalf("makeTokenTempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	tokenFile := filepath.Join(dir, "garmin_tokens.json")
	if err := os.WriteFile(tokenFile, []byte(`{"di_token":"t"}`), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("stat token file: %v", err)
	}
}

// ---- MFA subprocess lifetime ------------------------------------------------

// startSession starts name with args as an MFASession's subprocess.
func startSession(t *testing.T, name string, args ...string) *MFASession {
	t.Helper()
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	s := &MFASession{cmd: cmd, stdin: stdin, stdout: bufio.NewScanner(stdout), created: time.Now()}
	t.Cleanup(s.terminate)
	return s
}

// A subprocess that never answers must not block the reader forever.
func TestMFASessionScanLine_TimesOut(t *testing.T) {
	s := startSession(t, "sleep", "30")

	start := time.Now()
	ok, reason := s.scanLine(context.Background(), 200*time.Millisecond)
	if ok {
		t.Fatal("scanLine returned a line from a silent subprocess")
	}
	if !errors.Is(reason, ErrGarminUnavailable) {
		t.Errorf("reason = %v, want ErrGarminUnavailable", reason)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("scanLine took %v, want ~200ms", elapsed)
	}
}

func TestMFASessionScanLine_ContextCancel(t *testing.T) {
	s := startSession(t, "sleep", "30")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	ok, reason := s.scanLine(ctx, time.Minute)
	if ok {
		t.Fatal("scanLine returned a line from a silent subprocess")
	}
	if !errors.Is(reason, context.DeadlineExceeded) {
		t.Errorf("reason = %v, want context.DeadlineExceeded", reason)
	}
	if errors.Is(reason, ErrGarminUnavailable) {
		t.Errorf("reason = %v: a cancelled request is not Garmin being unavailable", reason)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("scanLine took %v, want ~200ms", elapsed)
	}
}

// A pending MFA session must survive the end of the request that started
// it: the handler passes r.Context(), which is cancelled as soon as the
// redirect to the MFA form is written.
func TestValidateWithMFA_SessionOutlivesContext(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "mfa_mock.py")
	body := `import json, sys
sys.stdin.readline()
print(json.dumps({"status": "needs_mfa"}), flush=True)
json.loads(sys.stdin.readline())
print(json.dumps({"status": "ok"}), flush=True)
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	p := NewPythonGarminProvider(script, nil)
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	status, err := p.ValidateWithMFA(ctx, 7, GarminCredentials{Email: "e", Password: "p"})
	if err != nil || status != "needs_mfa" {
		t.Fatalf("ValidateWithMFA = %q, %v; want needs_mfa", status, err)
	}
	cancel()
	time.Sleep(100 * time.Millisecond) // let a CommandContext kill land, if any

	if err := p.CompleteMFA(7, "123456"); err != nil {
		t.Fatalf("CompleteMFA after request context ended: %v", err)
	}
}

// A subprocess that exits without a line is not a timeout: scanLine reports
// no reason, leaving classification to the subprocess's stderr.
func TestMFASessionScanLine_ExitIsNotTimeout(t *testing.T) {
	s := startSession(t, "true")

	ok, reason := s.scanLine(context.Background(), time.Minute)
	if ok {
		t.Fatal("scanLine returned a line from a subprocess that printed nothing")
	}
	if reason != nil {
		t.Errorf("reason = %v, want nil for a plain exit", reason)
	}
}

// writeMFAScript writes a stand-in for garmin_fetch.py validate-mfa and
// returns its path. Skips the test when python3 is not on PATH.
func writeMFAScript(t *testing.T, body string) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	script := filepath.Join(t.TempDir(), "mfa_mock.py")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return script
}

// assertMFATimeout checks that err is a read timeout and was not routed
// through classifyError, whose "mfa" keyword match would turn it into
// ErrGarminMFARequired and show the user "invalid credentials/code".
func assertMFATimeout(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrGarminUnavailable) {
		t.Errorf("err = %v, want ErrGarminUnavailable", err)
	}
	if errors.Is(err, ErrGarminMFARequired) {
		t.Errorf("err = %v: a timeout must not read as ErrGarminMFARequired", err)
	}
}

func TestValidateWithMFA_ReadTimeoutIsUnavailable(t *testing.T) {
	script := writeMFAScript(t, `import sys, time
sys.stdin.readline()
time.sleep(30)
`)
	p := NewPythonGarminProvider(script, nil)
	defer p.Close()
	p.mfaReadTimeout = 200 * time.Millisecond

	start := time.Now()
	status, err := p.ValidateWithMFA(context.Background(), 7, GarminCredentials{Email: "e", Password: "p"})
	if status != "" {
		t.Errorf("status = %q, want empty", status)
	}
	assertMFATimeout(t, err)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("ValidateWithMFA took %v, want ~200ms", elapsed)
	}
	if p.HasMFASession(7) {
		t.Error("timed-out validation left an MFA session behind")
	}
}

func TestCompleteMFA_ReadTimeoutIsUnavailable(t *testing.T) {
	script := writeMFAScript(t, `import json, sys, time
sys.stdin.readline()
print(json.dumps({"status": "needs_mfa"}), flush=True)
sys.stdin.readline()
time.sleep(30)
`)
	p := NewPythonGarminProvider(script, nil)
	defer p.Close()
	p.mfaReadTimeout = 200 * time.Millisecond

	status, err := p.ValidateWithMFA(context.Background(), 7, GarminCredentials{Email: "e", Password: "p"})
	if err != nil || status != "needs_mfa" {
		t.Fatalf("ValidateWithMFA = %q, %v; want needs_mfa", status, err)
	}

	start := time.Now()
	assertMFATimeout(t, p.CompleteMFA(7, "123456"))
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("CompleteMFA took %v, want ~200ms", elapsed)
	}
}

// A validation that reaches needs_mfa after Close must not park a session
// that nothing will reap.
func TestValidateWithMFA_AfterCloseStoresNoSession(t *testing.T) {
	script := writeMFAScript(t, `import json, sys
sys.stdin.readline()
print(json.dumps({"status": "needs_mfa"}), flush=True)
sys.stdin.readline()
`)
	p := NewPythonGarminProvider(script, nil)
	p.Close()

	status, err := p.ValidateWithMFA(context.Background(), 7, GarminCredentials{Email: "e", Password: "p"})
	if err == nil || status != "" {
		t.Fatalf("ValidateWithMFA after Close = %q, %v; want an error", status, err)
	}
	if p.HasMFASession(7) {
		t.Error("session stored on a closed provider")
	}
}
