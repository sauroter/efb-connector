package garmin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"efb-connector/internal/crypto"
)

// PythonGarminProvider implements GarminProvider by shelling out to the
// garmin_fetch.py script.  Credentials are passed to the subprocess via stdin
// as a JSON object — never via environment variables — so that the process
// table remains clean in a multi-tenant environment.
//
// NOTE: The Python script does not yet read credentials from stdin (that
// change happens in Task 12).  The Go code is already written correctly;
// end-to-end operation will work once Task 12 is completed.
type PythonGarminProvider struct {
	// scriptPath is the absolute or relative path to garmin_fetch.py.
	scriptPath string

	// encryptionKey is the AES-256 key used to encrypt/decrypt Garmin
	// OAuth token files at rest.  When nil, token encryption is disabled
	// (useful for tests).
	encryptionKey []byte
}

// NewPythonGarminProvider returns a PythonGarminProvider that invokes the
// Python script at scriptPath.  encryptionKey is the AES-256 key used to
// encrypt Garmin OAuth tokens at rest; pass nil to disable token encryption.
func NewPythonGarminProvider(scriptPath string, encryptionKey []byte) *PythonGarminProvider {
	return &PythonGarminProvider{scriptPath: scriptPath, encryptionKey: encryptionKey}
}

// stdinCreds is the JSON envelope written to the subprocess's stdin.
type stdinCreds struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	TokenStore string `json:"tokenstore"`
}

// listActivityJSON mirrors the JSON objects emitted by `garmin_fetch.py list
// --json`.
type listActivityJSON struct {
	ID           interface{} `json:"id"`             // Garmin returns a number; use interface{} for robustness
	Name         string      `json:"name"`
	Type         string      `json:"type"`
	ParentTypeID *int        `json:"parent_type_id"` // Garmin's stable parent category ID; not mapped to Activity (filtering is in Python)
	Date         string      `json:"date"`           // "YYYY-MM-DD"
	StartTime    string      `json:"start_time"`     // "YYYY-MM-DD HH:MM:SS"
	StartLat     float64     `json:"start_lat"`
	StartLng     float64     `json:"start_lng"`
	Duration     float64     `json:"duration"`       // seconds
	Distance     float64     `json:"distance"`       // metres
}

// ListActivities runs `python <script> list --days <N> --json`, writes the
// credentials to stdin and parses the returned JSON array.
//
// The days argument is derived from the difference between end and start,
// rounded up to the nearest whole day.  A minimum of 1 day is always used.
func (p *PythonGarminProvider) ListActivities(
	ctx context.Context,
	creds GarminCredentials,
	start, end time.Time,
) ([]Activity, error) {
	days := daysSpan(start, end)

	stdout, stderr, err := p.run(ctx, creds,
		"list",
		"--days", strconv.Itoa(days),
		"--json",
	)
	if err != nil {
		return nil, classifyError(err, stderr)
	}

	var raw []listActivityJSON
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return nil, fmt.Errorf("garmin: failed to parse list output: %w\nstdout: %s", err, stdout)
	}

	activities := make([]Activity, 0, len(raw))
	for _, r := range raw {
		id, err := toStringID(r.ID)
		if err != nil {
			return nil, fmt.Errorf("garmin: unexpected activity id type: %w", err)
		}

		date, err := time.Parse("2006-01-02", r.Date)
		if err != nil {
			return nil, fmt.Errorf("garmin: failed to parse activity date %q: %w", r.Date, err)
		}

		startTime := date // fallback to date-only
		if r.StartTime != "" {
			if st, err := time.Parse("2006-01-02 15:04:05", r.StartTime); err == nil {
				startTime = st
			}
		}

		activities = append(activities, Activity{
			ProviderID:   id,
			Name:         r.Name,
			Type:         r.Type,
			Date:         date,
			StartTime:    startTime,
			StartLat:     r.StartLat,
			StartLng:     r.StartLng,
			DurationSecs: r.Duration,
			DistanceM:    r.Distance,
		})
	}

	// Filter to requested window — the Python script works in whole days so
	// some activities may fall outside [start, end).
	var filtered []Activity
	for _, a := range activities {
		if !a.Date.Before(start.Truncate(24*time.Hour)) && a.Date.Before(end) {
			filtered = append(filtered, a)
		}
	}

	return filtered, nil
}

// DownloadGPX runs `python <script> fetch <activityID> --output <tempdir>`,
// writes the credentials to stdin, reads the GPX file the script creates in
// the temp directory, and returns its bytes.  The temp directory is always
// removed before returning.
func (p *PythonGarminProvider) DownloadGPX(
	ctx context.Context,
	creds GarminCredentials,
	activityID string,
) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "garmin-gpx-*")
	if err != nil {
		return nil, fmt.Errorf("garmin: failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	stdout, stderr, err := p.run(ctx, creds,
		"fetch", activityID,
		"--output", tmpDir,
	)
	if err != nil {
		return nil, classifyError(err, stderr)
	}

	// The script prints the file path to stdout.
	gpxPath := strings.TrimSpace(stdout)
	if gpxPath == "" {
		return nil, fmt.Errorf("garmin: fetch command did not return a file path")
	}

	// Resolve relative paths against tmpDir.
	if !filepath.IsAbs(gpxPath) {
		gpxPath = filepath.Join(tmpDir, gpxPath)
	}

	// Guard against path traversal: reject any path that escapes tmpDir.
	if !strings.HasPrefix(filepath.Clean(gpxPath), filepath.Clean(tmpDir)) {
		return nil, fmt.Errorf("garmin: GPX path %q escapes temp directory", gpxPath)
	}

	data, err := os.ReadFile(gpxPath)
	if err != nil {
		return nil, fmt.Errorf("garmin: failed to read GPX file %q: %w", gpxPath, err)
	}

	return data, nil
}

// ValidateCredentials runs `python <script> validate`, writing the credentials
// to stdin.  A zero exit code is treated as success.  Stderr is inspected for
// known error patterns to return typed sentinel errors.
//
// NOTE: The `validate` subcommand will be added to the Python script in
// Task 12.
func (p *PythonGarminProvider) ValidateCredentials(
	ctx context.Context,
	creds GarminCredentials,
) error {
	_, stderr, err := p.run(ctx, creds, "validate")
	if err != nil {
		return classifyError(err, stderr)
	}
	return nil
}

// run executes `python <scriptPath> <args...>`, writes the credentials JSON to
// the subprocess stdin, and returns (stdout, stderr, error).
//
// When encryption is enabled and the credentials include a TokenStorePath, the
// method transparently decrypts any existing .enc token files to a temporary
// directory before the subprocess starts, and re-encrypts them afterwards
// (removing plaintext copies from the real tokenstore).
func (p *PythonGarminProvider) run(
	ctx context.Context,
	creds GarminCredentials,
	args ...string,
) (stdout, stderr string, err error) {
	// ── Token encryption: decrypt existing tokens to a temp dir ──

	tokenStoreForSubprocess := creds.TokenStorePath
	useEncryption := creds.TokenStorePath != "" && p.encryptionKey != nil

	var tmpTokenDir string
	if useEncryption {
		tmpTokenDir, err = os.MkdirTemp("", "garmin-tokens-*")
		if err != nil {
			return "", "", fmt.Errorf("garmin: create temp token dir: %w", err)
		}
		defer os.RemoveAll(tmpTokenDir)

		decryptTokenStore(p.encryptionKey, creds.TokenStorePath, tmpTokenDir)
		tokenStoreForSubprocess = tmpTokenDir
	}

	// ── Build and run the subprocess ──

	cmdArgs := append([]string{p.scriptPath}, args...)
	cmd := exec.CommandContext(ctx, "python3", cmdArgs...)

	// Encode credentials as JSON and pipe them to stdin.
	credsJSON, err := json.Marshal(stdinCreds{
		Email:      creds.Email,
		Password:   creds.Password,
		TokenStore: tokenStoreForSubprocess,
	})
	if err != nil {
		return "", "", fmt.Errorf("garmin: failed to marshal credentials: %w", err)
	}
	cmd.Stdin = bytes.NewReader(credsJSON)

	// Restrict the subprocess environment to a minimal whitelist so that
	// server secrets (ENCRYPTION_KEY, INTERNAL_SECRET, DATABASE_URL, etc.)
	// present in the parent process are never visible to the Python script.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"PYTHONPATH=" + os.Getenv("PYTHONPATH"),
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	// ── Token encryption: re-encrypt tokens from temp dir back to real tokenstore ──

	if useEncryption {
		encryptTokenStore(p.encryptionKey, tmpTokenDir, creds.TokenStorePath)
	}

	return stdoutBuf.String(), stderrBuf.String(), runErr
}

// tokenFileNames is the set of OAuth token files managed by garth.
var tokenFileNames = []string{"oauth1_token.json", "oauth2_token.json"}

// decryptTokenStore decrypts .enc files from srcDir to dstDir as plaintext JSON.
// Missing or corrupted files are silently skipped — a fresh Garmin login will
// create new tokens.
func decryptTokenStore(key []byte, srcDir, dstDir string) {
	for _, name := range tokenFileNames {
		encPath := filepath.Join(srcDir, name+".enc")
		data, err := os.ReadFile(encPath)
		if err != nil {
			continue // no encrypted file yet
		}
		plaintext, err := crypto.Decrypt(data, key)
		if err != nil {
			slog.Warn("garmin: failed to decrypt token file, skipping",
				"file", encPath, "error", err)
			continue
		}
		if err := os.WriteFile(filepath.Join(dstDir, name), plaintext, 0600); err != nil {
			slog.Warn("garmin: failed to write decrypted token file",
				"file", name, "error", err)
		}
	}
}

// encryptTokenStore encrypts plaintext JSON files from srcDir and writes .enc
// files to dstDir.  It also removes plaintext copies from dstDir so that tokens
// are never stored unencrypted at rest.
func encryptTokenStore(key []byte, srcDir, dstDir string) {
	// Ensure the destination directory exists (first-time token creation).
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		slog.Warn("garmin: failed to create token store dir",
			"dir", dstDir, "error", err)
		return
	}

	for _, name := range tokenFileNames {
		plainPath := filepath.Join(srcDir, name)
		data, err := os.ReadFile(plainPath)
		if err != nil {
			continue // token file not present in temp dir
		}
		encrypted, err := crypto.Encrypt(data, key)
		if err != nil {
			slog.Warn("garmin: failed to encrypt token file",
				"file", name, "error", err)
			continue
		}
		// Atomic write: write to temp file then rename to prevent corruption
		// if the process crashes mid-write.
		encPath := filepath.Join(dstDir, name+".enc")
		tmpPath := encPath + ".tmp"
		if err := os.WriteFile(tmpPath, encrypted, 0600); err != nil {
			slog.Warn("garmin: failed to write encrypted token file",
				"file", name+".enc", "error", err)
			os.Remove(tmpPath)
			continue
		}
		if err := os.Rename(tmpPath, encPath); err != nil {
			slog.Warn("garmin: failed to rename encrypted token file",
				"file", name+".enc", "error", err)
			os.Remove(tmpPath)
			continue
		}
		// Remove any plaintext copy from the real tokenstore.
		os.Remove(filepath.Join(dstDir, name))
	}
}

// classifyError maps subprocess errors and stderr messages to typed sentinel
// errors where possible.
func classifyError(err error, stderr string) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "mfa") ||
		strings.Contains(lower, "captcha") ||
		strings.Contains(lower, "two-factor") ||
		strings.Contains(lower, "2fa") {
		return fmt.Errorf("%w: %s", ErrGarminMFARequired, stderr)
	}
	if strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "invalid credentials") ||
		strings.Contains(lower, "login failed") ||
		strings.Contains(lower, "unauthorized") {
		return fmt.Errorf("%w: %s", ErrGarminAuth, stderr)
	}
	if stderr != "" {
		return fmt.Errorf("garmin: subprocess error: %w\nstderr: %s", err, stderr)
	}
	return fmt.Errorf("garmin: subprocess error: %w", err)
}

// daysSpan returns the number of whole days that cover the interval [start, end),
// with a minimum of 1.
func daysSpan(start, end time.Time) int {
	d := int(end.Sub(start).Hours()/24) + 1
	if d < 1 {
		d = 1
	}
	return d
}

// toStringID converts a JSON number (float64 or json.Number) or string to a
// plain string representation suitable for use as ProviderID.
func toStringID(v interface{}) (string, error) {
	switch val := v.(type) {
	case string:
		return val, nil
	case float64:
		// JSON numbers without a decimal component should be rendered without
		// a trailing ".0".
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10), nil
		}
		return strconv.FormatFloat(val, 'f', -1, 64), nil
	case json.Number:
		return val.String(), nil
	case nil:
		return "", fmt.Errorf("nil activity id")
	default:
		return fmt.Sprintf("%v", val), nil
	}
}
