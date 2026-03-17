# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
# Build the web server
go build -o efb-connector ./cmd/server

# Build the CLI tool (preserved)
go build -o gpx-uploader ./cmd/cli

# Run the web server locally
ENCRYPTION_KEY=<base64key> RESEND_API_KEY=<key> INTERNAL_SECRET=<secret> BASE_URL=http://localhost:8080 go run ./cmd/server

# Run tests
go test ./...

# Run a single test
go test ./... -run TestName
```

## Project Overview

This is a Go web server (`efb-connector`) that provides a multi-tenant portal for syncing GPS tracks to the Kanu-EFB portal (https://efb.kanu-efb.de/). The server:

1. Authenticates users via magic links (email-based, no passwords)
2. Stores encrypted EFB and Garmin credentials per user in SQLite
3. Syncs GPS tracks from Garmin Connect to Kanu-EFB on a schedule

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

## CLI Tool (preserved)

The original CLI tool is preserved at `cmd/cli/`:

```bash
./gpx-uploader path/to/file.gpx
```

Credentials are resolved in this order:
1. **1Password CLI** (if configured in `config.json`)
2. **Environment variables:** `EFBUSERNAME` and `EFBPASSWORD`
3. **Interactive prompts** (fallback)
