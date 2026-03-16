package garmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
lp.add_argument("--json", action="store_true")
args = parser.parse_args()

activities = [
    {"id": 123456, "name": "Morning Paddle", "type": "kayaking",
     "date": "2026-03-10", "duration": 3600.0, "distance": 5000.0},
    {"id": 789012, "name": "River Run",      "type": "canoeing",
     "date": "2026-03-12", "duration": 7200.0, "distance": 12000.0},
]
print(json.dumps(activities))
`)

	p := NewPythonGarminProvider(script)
	ctx := context.Background()

	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	activities, err := p.ListActivities(ctx, newCreds(), start, end)
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
	if a0.Type != "kayaking" {
		t.Errorf("a0.Type = %q, want %q", a0.Type, "kayaking")
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

	// Second activity
	a1 := activities[1]
	if a1.ProviderID != "789012" {
		t.Errorf("a1.ProviderID = %q, want %q", a1.ProviderID, "789012")
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
lp.add_argument("--json", action="store_true")
parser.parse_args()
print(json.dumps([]))
`)

	p := NewPythonGarminProvider(script)
	activities, err := p.ListActivities(context.Background(), newCreds(),
		time.Now().Add(-24*time.Hour), time.Now())
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
lp.add_argument("--json", action="store_true")
parser.parse_args()
print("garmin: authentication failed", file=sys.stderr)
sys.exit(1)
`)

	p := NewPythonGarminProvider(script)
	_, err := p.ListActivities(context.Background(), newCreds(),
		time.Now().Add(-24*time.Hour), time.Now())
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
lp.add_argument("--json", action="store_true")
parser.parse_args()
print("MFA required by Garmin", file=sys.stderr)
sys.exit(1)
`)

	p := NewPythonGarminProvider(script)
	_, err := p.ListActivities(context.Background(), newCreds(),
		time.Now().Add(-24*time.Hour), time.Now())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrGarminMFARequired) {
		t.Errorf("expected ErrGarminMFARequired, got: %v", err)
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
lp.add_argument("--json", action="store_true")
parser.parse_args()
print("this is not JSON")
`)

	p := NewPythonGarminProvider(script)
	_, err := p.ListActivities(context.Background(), newCreds(),
		time.Now().Add(-24*time.Hour), time.Now())
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

	p := NewPythonGarminProvider(script)
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
	p := NewPythonGarminProvider(script)
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

	p := NewPythonGarminProvider(script)
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

	p := NewPythonGarminProvider(script)
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

	p := NewPythonGarminProvider(script)
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

	p := NewPythonGarminProvider(script)
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

	p := NewPythonGarminProvider(script)
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
print("CAPTCHA required", file=sys.stderr)
sys.exit(1)
`)

	p := NewPythonGarminProvider(script)
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

	p := NewPythonGarminProvider(script)
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

// ---- Helper: daysSpan -------------------------------------------------------

func TestDaysSpan(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		start, end time.Time
		want       int
	}{
		{base, base.Add(24 * time.Hour), 2},
		{base, base.Add(7 * 24 * time.Hour), 8},
		{base, base, 1}, // zero span → minimum 1
		{base, base.Add(-1 * time.Hour), 1}, // negative span → minimum 1
	}
	for _, tt := range tests {
		got := daysSpan(tt.start, tt.end)
		if got != tt.want {
			t.Errorf("daysSpan(%v, %v) = %d, want %d", tt.start, tt.end, got, tt.want)
		}
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
