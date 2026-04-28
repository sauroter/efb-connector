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

	"efb-connector/internal/crypto"
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

	p := NewPythonGarminProvider(script, nil)
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

	p := NewPythonGarminProvider(script, nil)
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

	p := NewPythonGarminProvider(script, nil)
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

	p := NewPythonGarminProvider(script, nil)
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

// ---- Helper: daysSpan -------------------------------------------------------

func TestDaysSpan(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		start, end time.Time
		want       int
	}{
		{base, base.Add(24 * time.Hour), 2},
		{base, base.Add(7 * 24 * time.Hour), 8},
		{base, base, 1},                     // zero span → minimum 1
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
