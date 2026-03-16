# Implementation Plan: EFB-Connector v2.0

**Spec:** `docs/superpowers/specs/2026-03-16-efb-connector-v2-design.md`

## Context

Transform the single-user CLI tool into a multi-tenant hosted service. Users connect Garmin + EFB accounts, paddling activities sync daily. This is a large project — the plan is organized into sequential phases that each produce a testable, working increment.

## Phase 1: Restructure into internal packages (foundation)

Extract existing logic from `cmd/main.go` into reusable `internal/` packages. Preserve CLI functionality.

### Steps

1. **Create package structure**
   ```
   internal/efb/
   internal/garmin/
   internal/crypto/
   internal/database/
   internal/auth/
   internal/sync/
   internal/web/
   ```

2. **Extract EFB client** → `internal/efb/client.go`
   - Move `createEFBClient()` and `uploadGPXFile()` from `cmd/main.go`
   - Refactor into `EFBClient` struct with `Login()`, `Upload()` methods
   - Add session caching support (cache cookie, validate before use, re-login on expiry)
   - Add `ValidateCredentials()` for the account connection flow
   - Source: `cmd/main.go` lines with `createEFBClient`, `uploadGPXFile`, form POST to `/login`, multipart POST to `/interpretation/usersmap`

3. **Define Garmin provider interface** → `internal/garmin/provider.go`
   ```go
   type GarminProvider interface {
       ListActivities(ctx, creds, start, end) ([]Activity, error)
       DownloadGPX(ctx, creds, activityID) ([]byte, error)
       ValidateCredentials(ctx, creds) error
   }
   ```

4. **Implement PythonGarminProvider** → `internal/garmin/python.go`
   - Wraps `scripts/garmin_fetch.py` subprocess calls
   - Passes credentials via stdin JSON (not env vars)
   - Per-user token store path: `/data/garmin_tokens/<user_id>/`
   - Handles JSON output parsing from Python script

5. **Update `scripts/garmin_fetch.py`**
   - Accept credentials from stdin JSON: `{"email": "...", "password": "...", "tokenstore": "..."}`
   - Add `validate` subcommand (attempt login, return success/failure)
   - Keep backward compatibility for CLI usage (fall back to env vars if no stdin)

6. **Move CLI to `cmd/cli/main.go`**
   - Rename `cmd/main.go` → `cmd/cli/main.go`
   - Update to use `internal/efb` and `internal/garmin` packages
   - Verify CLI still works: `go run ./cmd/cli upload <file>`, `go run ./cmd/cli sync`

### Verification
- `go build ./cmd/cli` succeeds
- `go test ./internal/efb/... ./internal/garmin/...` passes
- CLI `sync` command works as before

---

## Phase 2: Crypto + Database layer

### Steps

1. **Implement AES-256-GCM** → `internal/crypto/aes.go`
   - `Encrypt(plaintext, key []byte) ([]byte, error)` — random nonce prepended
   - `Decrypt(ciphertext, key []byte) ([]byte, error)` — extract nonce, decrypt
   - `GenerateKey() ([]byte, error)` — 32 random bytes
   - Unit test: round-trip encrypt/decrypt, wrong key fails, tampered ciphertext fails

2. **Implement database layer** → `internal/database/`
   - `db.go` — `Open(path)`, migration runner, `Close()`
   - `migrations.go` — embedded SQL migration constants (the schema from spec Section 2)
   - `users.go` — `CreateUser`, `GetUserByEmail`, `GetUserByID`, `DeleteUser`, `GetSyncableUsers`
   - `credentials.go` — `SaveGarminCredentials`, `GetGarminCredentials`, `DeleteGarminCredentials` (same for EFB), all encrypt/decrypt on save/load
   - `activities.go` — `RecordActivity`, `IsActivitySynced`, `GetFailedActivities`, `MarkPermanentFailure`
   - `sync_runs.go` — `CreateSyncRun`, `UpdateSyncRun`, `GetSyncRun`, `GetSyncHistory`
   - `sessions.go` — `CreateSession`, `GetSession`, `DeleteSession`, `CreateMagicLink`, `ValidateMagicLink`, `CleanupExpired`
   - Use `modernc.org/sqlite` driver

### Verification
- `go test ./internal/crypto/...` — round-trip tests
- `go test ./internal/database/...` — CRUD tests with in-memory SQLite

---

## Phase 3: Auth module

### Steps

1. **Magic link flow** → `internal/auth/magic_link.go`
   - `GenerateMagicLink(email) (token, error)` — creates token, stores hash in DB, returns raw token for email
   - `ValidateMagicLink(token) (userID, error)` — verifies hash, expiry, marks used, creates user if first login

2. **Session management** → `internal/auth/session.go`
   - `CreateSession(userID) (token, error)` — creates session, returns raw token for cookie
   - `ValidateSession(token) (userID, error)` — checks hash against DB, verifies expiry, updates last_seen
   - `DestroySession(token) error`

3. **Middleware** → `internal/auth/middleware.go`
   - `RequireAuth(next http.Handler) http.Handler` — validates session cookie, sets user on context, redirects to `/login` if invalid
   - `CSRFProtect(next http.Handler) http.Handler` — validates CSRF token on POST requests
   - `CSRFToken(r *http.Request) string` — generates CSRF token for templates

4. **Rate limiting** → `internal/auth/ratelimit.go`
   - Per-key in-memory limiter using `golang.org/x/time/rate`
   - `AllowLogin(email, ip) bool`
   - `AllowSync(userID) bool`

5. **Email sending** → `internal/auth/email.go`
   - `SendMagicLink(email, token) error` — POST to Resend API, no SDK dependency

### Verification
- `go test ./internal/auth/...` — unit tests for all components
- Integration test: generate magic link → validate → create session → validate session → destroy

---

## Phase 4: Sync engine

### Steps

1. **Sync engine** → `internal/sync/engine.go`
   - `SyncEngine` struct with deps: `database.DB`, `garmin.GarminProvider`, `efb.EFBClient`, `crypto key`
   - `SyncUser(ctx, userID, trigger) (runID, error)` — the per-user sync flow from spec Section 5
   - `SyncAllUsers(ctx) error` — iterate syncable users with 30-60s stagger

2. **Per-user sync logic:**
   - Create sync_run → decrypt Garmin creds → ListActivities → filter already-synced → for each new activity: DownloadGPX → Upload to EFB → record result → discard GPX
   - Retry failed activities (retry_count < 3), increment counter, mark permanent_failure at 3
   - Handle errors per spec: auth failures invalidate credentials, temp failures log and retry next day

3. **EFB session caching in sync:**
   - Login once per sync run, cache session
   - 5-10s delay between uploads
   - Stop on 5xx

### Verification
- `go test ./internal/sync/...` — test with mock GarminProvider and mock EFB server
- Test idempotency: run sync twice, second run skips already-synced activities
- Test retry: failed activity retried on next run, capped at 3

---

## Phase 5: Web server + UI

### Steps

1. **Server entry point** → `cmd/server/main.go`
   - Parse env vars (PORT, DB_PATH, ENCRYPTION_KEY, RESEND_API_KEY, INTERNAL_SECRET)
   - Initialize DB, crypto, auth, sync engine, EFB client, Garmin provider
   - Set up routes, start HTTP server
   - Structured logging with `log/slog`

2. **Routes + handlers** → `internal/web/`
   - `server.go` — HTTP server setup, route registration
   - `handlers_public.go` — `GET /`, `GET /login`, `POST /login`, `GET /auth/verify`, `POST /auth/logout`
   - `handlers_dashboard.go` — `GET /dashboard`, `GET /settings/garmin`, `POST /settings/garmin`, `POST /settings/garmin/delete`, same for EFB, `POST /account/delete`
   - `handlers_sync.go` — `POST /sync/trigger`, `GET /sync/status`, `GET /sync/history`
   - `handlers_internal.go` — `POST /internal/sync/run-all` (shared secret auth), `GET /health`
   - `middleware.go` — logging, recovery, auth check wrapping

3. **Templates** → `templates/`
   - `layout.html` — base layout with nav, flash messages
   - `landing.html` — simple explanation + "Get started" CTA
   - `login.html`, `login_sent.html`, `auth_error.html`
   - `dashboard.html` — connection status cards, last sync, warnings, "Sync now" button
   - `settings_garmin.html`, `settings_efb.html` — credential forms with htmx loading
   - `sync_history.html` — table of sync runs with expandable per-activity detail
   - `partials/sync_status.html` — htmx partial for manual sync progress polling
   - `partials/flash.html` — success/error flash messages

4. **Static assets** → `static/`
   - `favicon.ico`
   - htmx + PicoCSS loaded from CDN (referenced in layout.html)

### Verification
- `go build ./cmd/server` succeeds
- Start server locally with test SQLite DB
- Manual test: full user flow (landing → login → magic link → dashboard → connect accounts → trigger sync → view history)

---

## Phase 6: Deployment

### Steps

1. **Update Dockerfile** — multi-stage: Go builder (CGO_ENABLED=0) + Python runtime, copy templates + static + scripts
2. **Update fly.toml** — HTTP service with auto_stop/auto_start, persistent volume mount
3. **Set Fly.io secrets** — `ENCRYPTION_KEY`, `RESEND_API_KEY`, `INTERNAL_SECRET`
4. **Create Fly.io volume** — `fly volumes create efb_data --region fra --size 1`
5. **Configure Resend** — set up sender domain, verify DNS
6. **Set up daily sync trigger** — Fly.io scheduled machine or external cron hitting `/internal/sync/run-all`
7. **Deploy** — `fly deploy`
8. **Smoke test** — create account, connect test credentials, trigger manual sync, verify activity in EFB

### Verification
- App accessible at `https://efb-connector.fly.dev`
- Health endpoint returns 200
- Full E2E: signup → connect → sync → verify in EFB portal
- Verify: credentials encrypted in SQLite (inspect raw DB), no GPX on disk after sync

---

## Critical files

| File | Action |
|------|--------|
| `cmd/main.go` | Move to `cmd/cli/main.go`, refactor to use `internal/` |
| `cmd/server/main.go` | NEW: web server entry point |
| `scripts/garmin_fetch.py` | Modify: stdin credentials, `validate` subcommand |
| `internal/efb/client.go` | NEW: extracted from cmd/main.go |
| `internal/garmin/provider.go` | NEW: interface definition |
| `internal/garmin/python.go` | NEW: subprocess implementation |
| `internal/crypto/aes.go` | NEW: AES-256-GCM |
| `internal/database/*.go` | NEW: SQLite layer |
| `internal/auth/*.go` | NEW: magic link, sessions, CSRF, rate limiting |
| `internal/sync/engine.go` | NEW: per-user sync orchestration |
| `internal/web/*.go` | NEW: HTTP handlers |
| `templates/*.html` | NEW: Go templates |
| `Dockerfile` | Rewrite for web server |
| `fly.toml` | Reconfigure for HTTP service |
| `go.mod` | Add: modernc.org/sqlite, golang.org/x/crypto, golang.org/x/time |

## Dependencies to add

```
modernc.org/sqlite       -- pure-Go SQLite (no CGO)
golang.org/x/crypto      -- HKDF for key derivation
golang.org/x/time/rate   -- rate limiting
```

## Implementation order

Phases 1-4 are the backend core and should be built sequentially (each depends on the previous). Phase 5 (web UI) depends on all of 1-4. Phase 6 (deployment) depends on 5. Each phase is independently testable.
