# Garmin MFA Support

## Context

Users with MFA/2FA enabled on their Garmin accounts cannot connect to efb-connector. When they enter credentials, the `garminconnect` library hits Garmin's MFA challenge, but our validation flow has no way to capture the user's TOTP code. The system shows a misleading message telling users to "log in via browser first" -- this doesn't help because browser login and the library's login are independent authentication sessions.

Bug report (user #52): enters credentials, gets told to log in via browser, completes Garmin login with security code in browser, but the message persists. This is an infinite loop with no escape.

The `garminconnect` 0.3.2 library (post-garth, uses its own `Client` class with a 5-strategy login cascade) fully supports MFA via `return_on_mfa=True` and `resume_login()`. We need to wire this into our web flow.

## Design

### Python: new `validate-mfa` subcommand

A new subcommand in `garmin_fetch.py` that supports an interactive stdin protocol:

**Input (stdin line 1):** credentials JSON
```json
{"email": "...", "password": "...", "tokenstore": "/path/to/tokens"}
```

**Flow:**
1. Create `Garmin(email, password, return_on_mfa=True)`
2. Call `client.login(tokenstore=tokenstore)`
3. If login succeeds (no MFA): print `{"status": "ok"}` to stdout, dump tokens, exit 0
4. If MFA required (`"needs_mfa"` returned): print `{"status": "needs_mfa"}` to stdout, then block reading stdin line 2

**Input (stdin line 2, only if MFA needed):**
```json
{"mfa_code": "123456"}
```

5. Call `client.resume_login({}, mfa_code)`
6. Dump tokens to tokenstore path
7. Print `{"status": "ok"}` to stdout, exit 0
8. On error: print `{"status": "error", "message": "..."}` to stdout (not stderr — Go reads stdout for the interactive protocol), exit 1

The `Garmin` object with its `_mfa_session` (holding SSO cookies), `_mfa_flow`, `_mfa_login_params`, etc. stays alive in the Python process while waiting for stdin line 2.

### Go: MFA session management

New types and methods on `PythonGarminProvider`:

```go
type MFASession struct {
    cmd     *exec.Cmd
    stdin   io.WriteCloser
    stdout  *bufio.Scanner
    stderr  bytes.Buffer
    created time.Time
}

// Added to PythonGarminProvider:
mfaSessions   map[int64]*MFASession  // keyed by user ID
mfaSessionsMu sync.Mutex
```

**New methods:**

- `ValidateWithMFA(ctx, userID, creds) → (status string, err error)`
  - Cancels any existing MFA session for this user
  - Starts `python3 garmin_fetch.py validate-mfa` subprocess
  - Writes credentials JSON to stdin (does NOT close stdin)
  - Reads first stdout line
  - If `"ok"`: clean up subprocess, return `"ok"`
  - If `"needs_mfa"`: store subprocess in `mfaSessions[userID]`, return `"needs_mfa"`
  - On error: clean up, return classified error

- `CompleteMFA(userID int64, code string) → error`
  - Looks up `mfaSessions[userID]`
  - Writes `{"mfa_code": "..."}` + newline to subprocess stdin, closes stdin
  - Reads response from stdout
  - If `"ok"`: clean up session, return nil
  - On error: clean up, return error

- `cleanupStaleMFASessions()` — goroutine running every 30 seconds, kills sessions older than 5 minutes. Called from provider constructor.

- `cancelMFASession(userID)` — kills any existing session for the user (called before starting a new one).

**Existing methods unchanged:** `ValidateCredentials`, `ListActivities`, `DownloadGPX` continue using the existing non-interactive `run()` method. The `validate` subcommand in Python is kept for backward compatibility but the web handlers will use `validate-mfa` instead.

### Web handler changes

**Modified: `handleGarminSettingsSave` (POST /settings/garmin)**
- Calls `ValidateWithMFA()` instead of `ValidateCredentials()`
- If `"ok"`: save credentials with `is_valid=1`, redirect to dashboard (unchanged)
- If `"needs_mfa"`: save credentials with `is_valid=0` (so they persist across the redirect), redirect to `/settings/garmin/mfa`
- If auth error: flash "invalid credentials" (unchanged)

**New: `handleGarminMFA` (GET /settings/garmin/mfa)**
- Renders `settings_garmin_mfa.html` — a simple form with a 6-digit code input
- If no MFA session exists for this user: redirect to `/settings/garmin` with flash

**New: `handleGarminMFASubmit` (POST /settings/garmin/mfa)**
- Reads `mfa_code` from form
- Calls `CompleteMFA(userID, code)`
- If success: update credentials to `is_valid=1`, redirect to dashboard with "Garmin credentials saved" flash
- If error: flash error, redirect back to `/settings/garmin/mfa`

**Routes (added to `server.go`):**
```
GET  /settings/garmin/mfa  → handleGarminMFA
POST /settings/garmin/mfa  → handleGarminMFASubmit
```

Add both to `openapi.yaml` as well.

### Template: `settings_garmin_mfa.html`

Minimal form matching existing styling:
- Heading: "Verification Code" / "Verifizierungscode"
- Explanation text: "Enter the 6-digit code from your authenticator app." / "Gib den 6-stelligen Code aus deiner Authenticator-App ein."
- Single text input (maxlength=6, pattern=[0-9]{6}, inputmode=numeric, autocomplete=one-time-code)
- Submit button
- Cancel link back to `/settings/garmin`

### i18n changes

**New keys:**
- `settings.mfa_heading` — "Verification Code" / "Verifizierungscode"
- `settings.mfa_description` — "Enter the 6-digit code from your authenticator app." / "Gib den 6-stelligen Code aus deiner Authenticator-App ein."
- `settings.mfa_placeholder` — "000000"
- `settings.mfa_submit` — "Verify" / "Verifizieren"
- `settings.mfa_cancel` — "Cancel" / "Abbrechen"
- `flash.garmin_mfa_invalid` — "Invalid verification code. Please try again." / "Ungultiger Verifizierungscode. Bitte versuche es erneut."
- `flash.garmin_mfa_expired` — "Verification session expired. Please re-enter your credentials." / "Verifizierungssitzung abgelaufen. Bitte gib deine Zugangsdaten erneut ein."

**Modified keys:**
- `flash.garmin_mfa_required` — **Remove**. This was the misleading "try your browser" message. No longer needed since MFA is handled interactively.

### Error classification changes

**`classifyError` in `python.go`:** Remove "captcha" from the MFA keyword list. CAPTCHAs are handled by the 5-strategy cascade in garminconnect 0.3.2 — they result in strategy fallthrough, not `_MFARequired`. If all strategies fail due to CAPTCHA/Cloudflare, the error surfaces as a connection error, not an MFA error.

**Sync engine (`engine.go`):** When credentials are invalidated during a sync due to `ErrGarminMFARequired`, the stored `last_error` should indicate that the user needs to re-authenticate with MFA through the settings page. No "try your browser" language.

### Token lifecycle

1. **First login with MFA:** validate-mfa subprocess completes MFA, dumps `garmin_tokens.json` to tokenstore dir. Go encrypts it at rest (existing `encryptTokenStore` flow).
2. **Subsequent syncs:** tokens are decrypted, loaded by `Garmin.login(tokenstore=...)` — no password login needed, no MFA. Token refresh happens automatically if tokens are near expiry.
3. **Token expiry (weeks/months later):** garminconnect can't refresh → sync fails → credentials invalidated → user re-enters credentials + MFA code through settings.

### Files to modify

| File | Change |
|------|--------|
| `scripts/garmin_fetch.py` | Add `validate-mfa` subcommand with interactive stdin protocol |
| `internal/garmin/python.go` | Add `MFASession`, `ValidateWithMFA`, `CompleteMFA`, cleanup goroutine |
| `internal/garmin/provider.go` | Add `ValidateWithMFA(ctx, userID, creds)` and `CompleteMFA(userID, code)` to `GarminProvider` interface |
| `internal/web/handlers_dashboard.go` | Modify `handleGarminSettingsSave` to use `ValidateWithMFA`, add MFA handlers |
| `internal/web/server.go` | Register new MFA routes |
| `templates/settings_garmin_mfa.html` | New MFA code entry template |
| `internal/i18n/en.go` | Add MFA-related translation keys, remove `flash.garmin_mfa_required` |
| `internal/i18n/de.go` | Same for German |
| `openapi.yaml` | Add MFA endpoints |

### Out of scope

- Garmin email-based MFA (where Garmin sends a code via email instead of TOTP). The garminconnect library handles this the same way (`_MFARequired` is raised regardless of method), so our flow supports it without changes. The `_mfa_method` field distinguishes "email" from "totp" but `_complete_mfa` handles both.
- "Remember this device" functionality. The token cache effectively serves this purpose — once tokens are cached, MFA isn't needed again until they expire.

## Verification

1. **Unit tests:** Mock `GarminProvider` returning `"needs_mfa"` from `ValidateWithMFA`, verify handler redirects to MFA page. Mock `CompleteMFA` success/failure, verify correct redirects and flash messages.
2. **Integration test with dev mode:** In dev mode, the mock Garmin provider should support MFA simulation (always require MFA, accept code "000000").
3. **Manual test:** With a real Garmin account that has MFA enabled, go through the full flow: enter credentials → get MFA form → enter code → verify tokens are cached → verify subsequent syncs work without MFA.
4. **Timeout test:** Start MFA flow, wait >5 minutes, verify session is cleaned up and user gets "expired" message.
5. **OpenAPI validation:** Run existing `tests/openapi/` test to ensure new routes are in spec.
