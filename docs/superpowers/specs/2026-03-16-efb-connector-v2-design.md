# EFB-Connector v2.0 — Multi-Tenant Hosted Service

## Context

The current efb-connector is a single-user CLI tool that syncs paddling activities from Garmin Connect to Kanu-EFB. It runs as a daily scheduled Fly.io machine. v2.0 transforms this into a multi-tenant hosted service where multiple users can connect their Garmin and EFB accounts and have paddling activities synced automatically.

**Design principles:**
- As secure as possible (encrypted credentials, no passwords for our service, minimal data)
- Cheap to run (machine auto-stops when idle, SQLite, no external services beyond email)
- Easy to use (two account connections, minimal settings, simple UI)
- Don't store unnecessary data (especially no tracks/GPX files)
- Be careful with EFB (fragile tooling — gentle rate limiting, session caching)

## 1. System Architecture

```
   Browser ──HTTPS──▶ Fly.io Proxy ──▶ Go HTTP Server (auto-starts on request)
                                            │
                            ┌───────────────┼───────────────┐
                            ▼               ▼               ▼
                       Auth Module     Web Handlers     Sync Engine
                            │               │               │
                            └───────┬───────┘               │
                                    ▼                       ▼
                               SQLite DB            ┌───────┴───────┐
                            (on Fly volume)         ▼               ▼
                                              Garmin Provider  EFB Client
                                              (Go interface)   (Go HTTP)
                                                    │
                                              Python subprocess
                                              (garminconnect)
```

**Single Fly.io machine** in Frankfurt (fra). Auto-stops when idle (`auto_stop_machines = true`), cold-starts on incoming HTTP request. The sync trigger comes from an external cron (Fly.io scheduled machine or external service) that hits an internal endpoint.

### Components

| Component | Responsibility |
|-----------|---------------|
| **HTTP Server** | Go stdlib `net/http` (1.24 ServeMux). Serves UI, API endpoints. |
| **Auth Module** | Magic link generation/validation, session cookies, CSRF, rate limiting. |
| **Sync Engine** | Per-user sync orchestration. Same code path whether triggered by cron, manual button, or future webhook. |
| **Garmin Provider** | Go interface abstracting Garmin data access. Current impl: Python subprocess. Future: official OAuth API. |
| **EFB Client** | Login via form POST, session caching, GPX upload via multipart form. Extracted from current `cmd/main.go`. |
| **SQLite** | All persistent state. On Fly.io volume `/data`. |

## 2. Data Model

```sql
CREATE TABLE users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    email        TEXT    NOT NULL UNIQUE,
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    is_active    INTEGER NOT NULL DEFAULT 1,
    sync_enabled INTEGER NOT NULL DEFAULT 1,
    sync_days    INTEGER NOT NULL DEFAULT 3
);

CREATE TABLE magic_links (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    email      TEXT    NOT NULL,
    token_hash TEXT    NOT NULL UNIQUE,
    expires_at TEXT    NOT NULL,
    used_at    TEXT,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT    NOT NULL UNIQUE,
    expires_at TEXT    NOT NULL,
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    last_seen  TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE garmin_credentials (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id             INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    email_encrypted     BLOB    NOT NULL,
    password_encrypted  BLOB    NOT NULL,
    -- Token store path is derived: /data/garmin_tokens/<user_id>/
    is_valid            INTEGER NOT NULL DEFAULT 1,
    last_error          TEXT,
    updated_at          TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE efb_credentials (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id             INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    username_encrypted  BLOB    NOT NULL,
    password_encrypted  BLOB    NOT NULL,
    session_cookie      BLOB,    -- encrypted cached session
    session_expires_at  TEXT,
    is_valid            INTEGER NOT NULL DEFAULT 1,
    last_error          TEXT,
    updated_at          TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- Idempotency: UNIQUE(user_id, garmin_activity_id) prevents duplicate uploads
CREATE TABLE synced_activities (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    garmin_activity_id TEXT    NOT NULL,
    activity_name      TEXT,
    activity_type      TEXT,
    activity_date      TEXT,
    synced_at          TEXT    NOT NULL DEFAULT (datetime('now')),
    upload_status      TEXT    NOT NULL DEFAULT 'success', -- success|failed|permanent_failure
    retry_count        INTEGER NOT NULL DEFAULT 0,
    error_message      TEXT,
    UNIQUE(user_id, garmin_activity_id)
);

CREATE TABLE sync_runs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    trigger             TEXT    NOT NULL DEFAULT 'scheduled', -- scheduled|manual|webhook
    started_at          TEXT    NOT NULL DEFAULT (datetime('now')),
    finished_at         TEXT,
    status              TEXT    NOT NULL DEFAULT 'running', -- running|success|partial|failed
    activities_found    INTEGER DEFAULT 0,
    activities_synced   INTEGER DEFAULT 0,
    activities_skipped  INTEGER DEFAULT 0,
    activities_failed   INTEGER DEFAULT 0,
    error_message       TEXT
);
```

No GPX data is ever stored. Activity metadata is kept only for idempotency and user-facing sync history.

## 3. Security Design

### Credential Encryption

- **Algorithm:** AES-256-GCM (authenticated encryption)
- **Master key:** 32-byte random key, base64-encoded, stored as Fly.io secret `ENCRYPTION_KEY`
- **Per-field nonces:** Each encrypted field gets a unique 12-byte random nonce, prepended to ciphertext
- **Key rotation:** Re-encrypt all credentials via admin endpoint if key is compromised

### Magic Link Auth Flow

1. User enters email at `/login`
2. Server generates 32-byte random token, stores `SHA-256(token)` with 15-minute expiry
3. Sends email with link: `https://<domain>/auth/verify?token=<base64url(token)>`
4. On click: hash received token, look up in `magic_links`, verify expiry, mark as used
5. Create session: 32-byte random token, store `SHA-256` in `sessions` (30-day expiry)
6. Set cookie: `session=<token>; HttpOnly; Secure; SameSite=Lax; Max-Age=2592000`
7. First login auto-creates user record

### CSRF Protection

- Hidden `csrf_token` in every form
- Token = `HMAC-SHA256(session_id || form_action, csrf_secret)` where `csrf_secret` is derived from `ENCRYPTION_KEY` via HKDF
- **Note:** This is deterministic per session+action — intentional simplicity tradeoff. A leaked CSRF token is valid for the session lifetime (30 days), but this is acceptable given: SameSite=Lax cookies prevent most CSRF vectors, the service has no high-value state-changing actions beyond credential management, and per-request nonces would require server-side nonce storage.

### Rate Limiting

| Endpoint | Limit |
|----------|-------|
| `POST /login` per email | 5/hour |
| `POST /login` per IP | 20/hour |
| `POST /sync/trigger` per user | 1/hour |
| EFB uploads per user per sync | max 20, 5-10s delays |

In-memory rate limiter (`golang.org/x/time/rate`). Acceptable to lose state on machine restart.

## 4. Garmin Integration Abstraction

```go
type GarminProvider interface {
    ListActivities(ctx context.Context, creds GarminCredentials, start, end time.Time) ([]Activity, error)
    DownloadGPX(ctx context.Context, creds GarminCredentials, activityID string) ([]byte, error)
    ValidateCredentials(ctx context.Context, creds GarminCredentials) error
}

type Activity struct {
    ProviderID   string
    Name         string
    Type         string    // kayaking, sup, canoeing, rowing, paddling, rafting
    Date         time.Time
    DurationSecs float64
    DistanceM    float64
}
```

**Current impl (`PythonGarminProvider`):** Calls `garmin_fetch.py` via subprocess. Credentials passed via stdin JSON (not env vars — security for multi-tenant).

### Per-user Garmin token caching

Each user gets an isolated token store directory at `/data/garmin_tokens/<user_id>/` on the persistent volume. The garminconnect library writes session/OAuth tokens to this directory after a successful login. On subsequent calls, the library reads cached tokens and avoids a fresh login (which is slow and may trigger MFA). The Go code passes the per-user token store path to the Python subprocess via stdin JSON. The directory is created on first Garmin credential save and deleted on account deletion (see 10c).

**Future impl (`OAuthGarminProvider`):** Official Garmin API. OAuth tokens replace email/password. Webhook endpoint replaces polling. The `GarminProvider` interface stays the same — sync engine doesn't change.

### Python script changes

`garmin_fetch.py` updated to read credentials from stdin:
```python
creds = json.loads(sys.stdin.readline())
# {"email": "...", "password": "...", "tokenstore": "/data/garmin_tokens/42/"}
```

New `validate` subcommand for credential testing during account connection.

### MFA / CAPTCHA handling

The garminconnect library caches session tokens (per-user token store). As long as tokens are valid, no fresh login is needed. When tokens expire and a fresh login is required, Garmin may trigger MFA or CAPTCHA challenges. These cannot be solved programmatically.

**Strategy:** The token store handles most cases transparently. When a fresh login fails due to MFA/CAPTCHA, the `PythonGarminProvider` returns a specific error type (`ErrGarminMFARequired`). The sync engine marks `garmin_credentials.is_valid = false` with `last_error = "Garmin requires additional verification"`. The dashboard shows: "Please log into Garmin Connect in your browser to complete verification, then re-enter your credentials here." Re-entering credentials triggers `ValidateCredentials`, which attempts a fresh login and refreshes the token store.

### Credential validation UX

`POST /settings/garmin` and `POST /settings/efb` are **synchronous** — the handler blocks while the Python subprocess or EFB login runs (5-15 seconds typical). The form submit button shows a loading spinner via htmx (`hx-indicator`). A 30-second timeout is set on the validation call. On success: flash message + redirect to dashboard. On failure: error message on the same form page.

## 5. Sync Engine

### Core principle: user-based, trigger-agnostic

Every sync is a single-user operation. The sync engine exposes:

```go
type SyncEngine struct { ... }

// SyncUser runs a sync for a single user. Returns the sync_run ID for status polling.
func (s *SyncEngine) SyncUser(ctx context.Context, userID int64, trigger string) (runID int64, err error)
func (s *SyncEngine) SyncAllUsers(ctx context.Context) error
```

`SyncUser` is the atomic unit. `SyncAllUsers` iterates users with staggered delays. Both are called from different trigger sources:

| Trigger | How it works |
|---------|-------------|
| **Daily cron** | External Fly.io scheduled machine (or cron service) hits `POST /internal/sync/run-all` with a shared secret. This calls `SyncAllUsers`. |
| **Manual button** | User clicks "Sync now" on dashboard. Launches `SyncUser` in a goroutine. Returns sync_run ID. Dashboard polls `GET /sync/status?run=<id>` via htmx (`hx-trigger="every 3s"`) to show progress. Polling stops when status != 'running'. |
| **Future webhook** | `POST /webhooks/garmin` receives push notification, identifies user, calls `SyncUser`. |

### Per-user sync flow

```
SyncUser(userID, trigger):
  1. Create sync_run record (status=running, trigger=trigger)
  2. Decrypt Garmin credentials
  3. ListActivities(last N days)
     → On auth failure: mark garmin_credentials.is_valid=false, fail sync
     → On temp failure: fail sync, retry next day
  4. Filter out activities in synced_activities with status=success (idempotency)
  5. Retry activities with status=failed AND retry_count < 3 (max 3 retries, then mark permanent_failure)
  6. For each new/retry activity:
     a. DownloadGPX → bytes in memory
     b. Decrypt EFB credentials, get/create session
     c. Upload to EFB
     d. Record result in synced_activities
     e. Discard GPX bytes
     f. Sleep 5-10s (gentle on EFB)
  7. Update sync_run with final counts
```

### Staggering (for SyncAllUsers)

30-60 seconds random delay between users. 100 users ≈ 50-75 minutes. Machine stays alive during the sync batch, then auto-stops when idle.

### Error handling

| Error | Action |
|-------|--------|
| Garmin auth failure | `garmin_credentials.is_valid = false`, dashboard warning |
| Garmin temp failure | Log, retry next day |
| EFB auth failure | `efb_credentials.is_valid = false`, dashboard warning |
| EFB 5xx | Stop user's sync, record failures, retry next day |
| Single GPX download failure | Record as failed, continue with others |

## 6. EFB Client

Extracted from current `cmd/main.go` (`createEFBClient`, `uploadGPXFile`).

### Session caching strategy

1. Check for cached session cookie in `efb_credentials.session_cookie`
2. Validate session: GET `/interpretation/usersmap`, check for redirect to `/login`
3. If expired: re-login with form POST, cache new session
4. Upload GPX via multipart POST
5. Verify response contains "Datenbank gespeichert"
6. On session expiry mid-upload: re-login once, retry

### Being gentle

- Sequential uploads, never parallel
- 5-10 second delay between uploads
- One login per sync run, reuse session
- Stop on 5xx errors (don't hammer)
- Max 20 uploads per user per sync run

## 7. Web UI

### Tech

- **Go `html/template`** — server-rendered, auto-escaping
- **htmx** — progressive enhancement (CDN, no build step)
- **PicoCSS** — classless CSS framework (CDN, no build step)
- **No JavaScript build pipeline**

### Routes

```
GET  /                     Landing page (auth → redirect /dashboard)
GET  /login                Email input form
POST /login                Send magic link
GET  /auth/verify          Verify magic link
POST /auth/logout          Destroy session

GET  /dashboard            Sync status, warnings, recent activity
GET  /settings/garmin      Garmin connection form
POST /settings/garmin      Save/test Garmin credentials
POST /settings/garmin/delete  Remove Garmin connection
GET  /settings/efb         EFB connection form
POST /settings/efb         Save/test EFB credentials
POST /settings/efb/delete  Remove EFB connection
POST /sync/trigger         Manual sync (rate limited: 1/hour)
GET  /sync/status          Sync run progress (htmx partial, polled every 3s)
GET  /sync/history         Sync history with per-activity detail
POST /account/delete       Delete account and all data

POST /internal/sync/run-all  Cron trigger (shared secret auth)
GET  /health               Health check
```

### User flow

1. Visit landing → "Get started" → enter email → magic link → click link
2. First login creates account → dashboard with two "Connect" cards
3. Connect Garmin (enter email/password, validated immediately)
4. Connect EFB (enter username/password, validated immediately)
5. Dashboard: "Sync active. Next sync: tomorrow ~04:00 UTC."
6. Daily: activities appear in sync history. Errors shown as warnings.

### Template structure

```
templates/
  layout.html
  landing.html
  login.html, login_sent.html, auth_error.html
  dashboard.html
  settings_garmin.html, settings_efb.html
  sync_history.html
  partials/sync_status.html, flash.html
static/
  favicon.ico
  (htmx + pico loaded from CDN)
```

## 8. Deployment

### fly.toml

```toml
app = "efb-connector"
primary_region = "fra"

[build]

[http_service]
  internal_port = 8080
  force_https = true
  auto_stop_machines = true
  auto_start_machines = true
  min_machines_running = 0

[mounts]
  source = "efb_data"
  destination = "/data"

[env]
  PORT = "8080"
  DB_PATH = "/data/efb-connector.db"
```

### Secrets (Fly.io)

| Secret | Purpose |
|--------|---------|
| `ENCRYPTION_KEY` | 32-byte base64 AES key for credential encryption |
| `RESEND_API_KEY` | Magic link email delivery |
| `INTERNAL_SECRET` | Shared secret for `/internal/sync/run-all` |

### Daily sync trigger

A Fly.io scheduled machine that runs daily at 04:00 UTC and does:
```bash
curl -X POST -H "Authorization: Bearer $INTERNAL_SECRET" https://efb-connector.fly.dev/internal/sync/run-all
```

This wakes the main machine (auto-start), which runs all syncs, then goes back to idle and auto-stops.

### Dockerfile

```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o efb-connector ./cmd/server

FROM python:3.12-alpine
WORKDIR /app
RUN mkdir -p /data
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY --from=builder /app/efb-connector ./
COPY scripts/ ./scripts/
COPY templates/ ./templates/
COPY static/ ./static/
CMD ["./efb-connector"]
```

Uses `modernc.org/sqlite` (pure Go, no CGO) for zero C dependency.

## 9. Migration Path

### Phase 1: Restructure Go code

Move `cmd/main.go` → `cmd/cli/main.go` (preserved CLI). Create `cmd/server/main.go` (new web server entry point). Extract shared logic into `internal/` packages:

```
cmd/
  cli/main.go            ← preserved CLI tool (moved from cmd/main.go)
  server/main.go         ← NEW: web server entry point
internal/
  efb/client.go          ← createEFBClient, uploadGPXFile from cmd/main.go
  garmin/provider.go     ← GarminProvider interface
  garmin/python.go       ← PythonGarminProvider (subprocess calls)
  crypto/aes.go          ← AES-256-GCM encrypt/decrypt
  database/              ← SQLite schema, migrations, queries
  auth/                  ← Magic link, sessions, CSRF, rate limiting
  sync/engine.go         ← Per-user sync orchestration
  web/                   ← HTTP handlers, templates, middleware
```

### Phase 2: Build multi-tenant layer

Database → crypto → auth → sync engine → web handlers → templates.

### Phase 3: Deploy

New Fly.io app, set secrets, create volume, deploy. Old scheduled machine can run in parallel during transition.

## 10. Go Dependencies

```
modernc.org/sqlite       -- pure-Go SQLite driver
golang.org/x/crypto      -- HKDF for key derivation
golang.org/x/time/rate   -- rate limiting
```

No external router (Go 1.24 `net/http.ServeMux` with method routing). No external template engine. No ORM.

## 10a. Logging

Structured logging with `log/slog` (stdlib). JSON format in production, text format in development. Log: sync starts/completions/errors, auth events (login, failed attempts), rate limit hits, EFB/Garmin interaction outcomes. No credential values in logs.

## 10b. Database Migrations

Migrations embedded as Go constants in `internal/database/migrations.go`. A `migrations` table tracks which migrations have run. On startup, the server runs any pending migrations. No external migration tool — keeps the binary self-contained.

## 10c. Account Deletion

`POST /account/delete` performs:
1. Delete user row (cascades to all related tables via `ON DELETE CASCADE`)
2. Delete Garmin token store directory: `os.RemoveAll("/data/garmin_tokens/<user_id>/")`
3. Destroy session cookie
4. Redirect to landing page with confirmation

## 10d. Auth Middleware

All routes under `/dashboard`, `/settings/*`, `/sync/*`, `/account/*` are wrapped in auth middleware that:
1. Reads session cookie, validates against `sessions` table
2. If invalid/expired: redirect to `/login`
3. If valid: set user context on request, update `last_seen` (if >1 hour stale)

## 10e. Session/Token Cleanup

During the daily sync run (or on server startup), prune:
- `magic_links` where `expires_at < now` or `used_at IS NOT NULL` and older than 24h
- `sessions` where `expires_at < now`

This prevents unbounded table growth.

## 10f. Email Delivery Failures

If Resend API returns an error when sending a magic link: show "We couldn't send the login email. Please try again in a few minutes." on the login page. The rate limiter still counts the attempt. A "Resend" link appears on the `login_sent.html` page (rate limited to the same 5/hour per email).

## 11. Verification Plan

1. **Unit tests:** crypto encrypt/decrypt round-trip, magic link generation/validation, session management, CSRF, sync idempotency logic
2. **Integration tests:** EFB client login+upload against a mock server, Garmin provider against mock subprocess
3. **Manual E2E:** Deploy to Fly.io staging, create account via magic link, connect test Garmin + EFB accounts, trigger manual sync, verify activity appears in EFB portal
4. **Security checks:** Verify credentials are encrypted in SQLite (inspect raw DB), verify GPX not persisted, verify rate limiting works, verify CSRF rejects forged tokens

## Critical Files to Modify

| File | Change |
|------|--------|
| `cmd/main.go` | Extract EFB client + Garmin subprocess logic into `internal/` |
| `scripts/garmin_fetch.py` | Accept credentials via stdin JSON, add `validate` subcommand |
| `Dockerfile` | Rewrite for web server (templates, static, pure-Go SQLite) |
| `fly.toml` | Reconfigure for HTTP service with auto-stop |
| `go.mod` | Add modernc.org/sqlite, golang.org/x/crypto, golang.org/x/time |

## Reusable Code from v1.0

| What | Where | How to reuse |
|------|-------|-------------|
| EFB login (form POST, cookie jar) | `cmd/main.go:createEFBClient()` | Extract to `internal/efb/client.go` |
| GPX upload (multipart form) | `cmd/main.go:uploadGPXFile()` | Extract to `internal/efb/client.go` |
| Garmin activity filtering | `scripts/garmin_fetch.py` WATER_SPORT_TYPES | Keep as-is, update credential passing |
| 1Password config loading | `cmd/main.go` | Drop for v2.0 (replaced by encrypted DB storage) |
