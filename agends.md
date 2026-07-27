# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
# Local dev server (mock EFB + Garmin)
make dev

# Build the web server binary
make build

# Run all tests
make test

# Lint
make lint

# Clean build artifacts and local dev DB
make clean

# Start dev server with EFB v2026.1 consent-gate mock active
make dev-consent

# Run tests with coverage and per-function summary
make cover

# Inspect Fly egress IP allocations (debug EFB rate-limit)
make egress-status

# Rotate Fly egress IPv6 pair when EFB rate-limits the deployed IP
make rotate-egress

# Run a single test
go test ./... -run TestName
```

## Project Overview

This is a Go web server (`efb-connector`) that provides a multi-tenant portal for syncing GPS tracks to the Kanu-EFB portal (https://efb.kanu-efb.de/). The server:

1. Authenticates users via magic links (email-based, no passwords)
2. Stores encrypted EFB and Garmin credentials per user in SQLite
3. Syncs GPS tracks from Garmin Connect to Kanu-EFB on a schedule

## Architecture

- `cmd/server/` — main HTTP server binary
- `cmd/efb-probe/` — one-shot CLI for validating EFB login rate-limit detection
- `internal/` — handlers, services, storage
- `tests/integration/` — end-to-end HTTP tests; `tests/openapi/` — spec-vs-routes validator
- `infrastructure/` — Fly.io deployment configs, Grafana dashboards, ops scripts

Bulk sync is fire-and-forget: cron `POST`s `/internal/sync/run-all` (returns `202`), and the server runs per-user sync paced at ~30s/user with jitter. The endpoint must stay non-blocking — long-running work happens in goroutines.

## Authentication

User authentication uses magic links sent via email (Resend API). No passwords stored.

EFB and Garmin credentials are stored encrypted (AES-256-GCM) in the SQLite database.

## Configuration

The server is configured via environment variables:

| Variable | Description |
|---|---|
| `ENCRYPTION_KEY` | Base64-encoded 32-byte key for credential encryption |
| `RESEND_API_KEY` | Resend API key for sending magic link emails |
| `INTERNAL_SECRET` | Secret for internal/cron API endpoints |
| `BASE_URL` | Public base URL (e.g. `https://efb-connector.sauroter.de`) |
| `PORT` | HTTP listen port (default: `8080`) |
| `DB_PATH` | Path to SQLite database file (default: `efb-connector.db`) |
| `DEV_MODE` | Set to `true` for local dev (mock EFB + Garmin, relaxed env requirements) |
| `RESEND_MANAGEMENT_KEY` | Resend full-access API key for contacts/segments (optional) |
| `RESEND_SEGMENT_ACTIVE` | Resend segment ID for "Active Syncers" (optional) |
| `RESEND_SEGMENT_NEEDS_SETUP` | Resend segment ID for "Needs Setup" users (optional) |
| `EMAIL_FROM` | From-address for magic link emails |
| `FEEDBACK_EMAIL` | Recipient for user feedback submissions |
| `RIVERMAP_API_KEY` | Optional; enables river-section/gauge enrichment from Rivermap |
| `METRICS_PORT` | Prometheus metrics endpoint port (default: `9091`) |
| `DEV_MOCK_EFB_CONSENT` | When `DEV_MODE=true`, set to `1` to start the mock EFB with the v2026.1 consent gate active |

## API Documentation

The full REST API is documented in [`openapi.yaml`](openapi.yaml) (OpenAPI 3.1). A validation test in `tests/openapi/` ensures the spec stays in sync with registered routes — add new endpoints to both `server.go` and `openapi.yaml`.

## Operator endpoints

All `/internal/admin/*` routes require `Authorization: Bearer $INTERNAL_SECRET`. The full list lives in `openapi.yaml`; these are the ones you reach for most often during incident triage:

- `GET /internal/admin/users/{id}/sync-history` — last 50 sync_runs for a user (status, found/synced/failed counters, `error_message`, `raw_count`, `type_keys_seen`).
- `GET /internal/admin/users/{id}/garmin/activities-raw?days=N` — every activity Garmin returns for the user, **bypassing the water-sport filter**. First thing to hit on any "no imports" feedback: it answers "what activity types does this user actually have?" Hits Garmin directly using cached tokens; subject to rate limits. `days` capped at 365.
- `GET /internal/admin/activity-errors?include_body=1` — recent failed activity uploads with EFB response excerpts (capped at 5 stored bodies).
- `POST /internal/admin/users/{id}/debug-upload` — dry-run a single EFB upload using stored credentials, returning the raw EFB response. Does not mutate `synced_activities` or `sync_runs`.

## Water-sport filter

`scripts/garmin_fetch.py:is_water_sport` filters Garmin activities to `parentTypeId == 228` plus a hardcoded legacy `typeKey` list. Anything that fails this filter is dropped silently.

Be careful what 228 means: it is Garmin's whole **Water Sports** category. Confirmed by production data to also contain `sailing_v2`, `windsurfing_v2`, `boating_v2`, `wakeboarding_v2` and `surfing_v2` — and confirmed *not* to contain swimming (`open_water_swimming`, `lap_swimming`) or diving (`apnea_diving`, `single_gas_diving`), which Garmin files under their own parents. Python admits everything under 228; narrowing it to what the user actually wants in their logbook happens in Go, via the per-user selection below. (A user reported sailing trips landing in eFB in July 2026 — that was this gap.)

To re-derive that membership without calling Garmin: `sync_runs.type_keys_seen` is recorded pre-filter and `activities_found` post-filter, so a run whose only water-sport candidate is typeKey X reveals whether X passes. Sweep it across users via `GET /internal/admin/users` + `/sync-history`.

**This recipe needs a correction for runs after migration 0014.** `activities_found` is now post-*selection* as well as post-filter, and sailing/windsurf/surfing/motorboat are deselected by default — so those typeKeys look identical to "outside 228" when judged on `activities_found` alone. Always check `excluded_count` too: `found = 0, excluded_count > 0` means the typeKey *did* pass Garmin's filter and was dropped by the user's selection. Judging without it would conclude `sailing_v2` is outside parent 228, the exact opposite of the truth.

Per-user selection: `users.selected_activity_types` (migration 0014) holds the category keys the user wants synced — kayak, canoe, paddle, sup, rowing, whitewater, sailing, windsurf, surfing, motorboat, other_water. **Defaults** (`garmin.DefaultSelectedCategories`, mirrored by the 0014 backfill): paddle sports on, sailing/windsurf/surfing/motorboat off, `other_water` on — we can't tell whether an unnamed water sport is paddling, and losing a real trip is worse than an unwanted one. `internal/garmin/categories.go` owns the keys, their typeKey prefixes, and `CategoryForActivity`, which falls back to `other_water` for any unrecognised typeKey under parent 228, and to a name match for the `match_by_name` activities Garmin files under parent 17. Note that `other_water` is default-**on**: a newly-added Garmin water sport still syncs by default — it just becomes untickable rather than invisible, and `excluded_count` can explain it afterwards. Naming a category is still required to make a new sport off-by-default. `internal/sync/engine.go` applies the selection before GPX download; the per-run drop count lands on `sync_runs.excluded_count`. Activities mapping to no category at all (unknown typeKey *outside* 228) are still kept — never silently drop a paddling activity because the prefix table is stale. Settings UI at `POST /settings/activity-types`.

Adding a category means: key + prefixes in `categories.go`, `activity_type.<key>` in **both** i18n bundles, and the enum in `openapi.yaml`. Decide explicitly whether it belongs in `defaultSelected`; if it does, note that existing users won't get it — the 0014 backfill froze their lists, so only accounts created afterwards pick it up. Anything left out of `defaultSelected` is opt-in for everyone.

Per-user opt-in escape hatch: `users.match_by_name` (column added in migration 0012, default off). When enabled, activities under `parent_type_id == 17` (generic fitness, e.g. Venu 3 "Sonstiges") are accepted if their `activityName` contains a water-sport keyword (Kajak, Kanu, Paddel, Rudern, SUP, kayak, canoe, paddle, row, stand up paddl). The parent-id guard prevents non-water-sport activities tagged with a specific parent from leaking in. Driven through the engine via `garmin.ListOptions.MatchByName` → `garmin_fetch.py --match-by-name`. Settings toggle at `POST /settings/match-by-name`.

When triaging "0 imports" feedback: the `"fetched garmin activities"` log line carries `raw_count`, `type_keys_seen`, and `name_matched_count`. Same data persists on `sync_runs` so the dashboard's "no matching activities" hint can render historical typeKeys without re-querying Garmin.

