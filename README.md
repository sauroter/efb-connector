# EFB Connector

A multi-tenant web service that automatically syncs water sport activities from Garmin Connect to the [Kanu-EFB electronic logbook](https://efb.kanu-efb.de/).

**Live instance:** [efb-connector.sauroter.de](https://efb-connector.sauroter.de)

## Features

- **Automatic daily sync** — paddling activities are synced from Garmin Connect to Kanu-EFB every day at ~04:00 UTC
- **Trip logbook entries** — automatically create Fahrten (logbook entries) in EFB from synced tracks with pre-filled start/end times
- **River data enrichment** — trip entries enriched with river section name, difficulty grade, and gauge readings via [Rivermap](https://rivermap.ch)
- **Manual sync & history** — trigger syncs on demand, sync custom date ranges, and review a full sync log with detailed status
- **Passwordless authentication** — sign in with a magic link sent to your email
- **Garmin MFA support** *(beta)* — two-factor authentication on Garmin? Enter your verification code during setup
- **Encrypted credential storage** — Garmin and EFB credentials encrypted at rest with AES-256-GCM
- **Guided onboarding** — 4-step setup wizard walks new users through connecting accounts and configuring preferences
- **Self-service management** — connect/disconnect accounts, manage preferences, view history, delete your data

## Supported Activity Types

- Kayaking
- Stand Up Paddleboarding (SUP)
- Canoeing
- Rowing
- Paddling
- Whitewater Rafting

## How It Works

1. **Sign in** with your email — a magic link is sent, no password needed
2. **Connect** your Garmin Connect and Kanu-EFB accounts (credentials are validated and stored encrypted)
3. **Configure** your preferences — auto-create trip entries, enrich with river data
4. **Relax** — your activities sync automatically every day, or trigger a manual sync anytime

## Getting Started

### Prerequisites

- Go 1.25+
- Python 3.12+ with `garminconnect` package
- GNU Make (included in devbox)

Or use [devbox](https://www.jetify.com/devbox) to get everything automatically:

```bash
devbox shell
```

### Local development

The fastest way to run the server locally uses **dev mode**, which substitutes mock implementations for Garmin Connect and Kanu-EFB so no external accounts are needed:

```bash
make dev
```

This starts the server on `http://localhost:8080` with mock providers. Magic link emails are printed to stdout instead of being sent.

### Build

```bash
make build
```

### Run tests

```bash
make test
```

### All Make targets

| Target | Description |
|---|---|
| `make dev` | Run server in dev mode (mock EFB + Garmin, auto-generated encryption key) |
| `make build` | Build the `efb-connector` binary |
| `make test` | Run all tests (unit + integration) |
| `make lint` | Run `go vet` |
| `make clean` | Remove built binaries and local dev database |

### Running with real providers

```bash
ENCRYPTION_KEY=<base64-encoded-32-byte-key> \
RESEND_API_KEY=<resend-api-key> \
INTERNAL_SECRET=<secret> \
BASE_URL=http://localhost:8080 \
go run ./cmd/server
```

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

## Deployment

The service is deployed on [Fly.io](https://fly.io) in the Frankfurt (fra) region with a persistent volume for the SQLite database.

```bash
fly deploy
```

See [`infrastructure/README.md`](infrastructure/README.md) for detailed deployment and operations instructions.

## API Documentation

The full REST API (public, authenticated, and internal endpoints) is documented in [`openapi.yaml`](openapi.yaml) using the OpenAPI 3.1 specification. You can view it with any OpenAPI-compatible tool (Swagger UI, Redoc, etc.).

## CLI Tool

The original CLI tool is preserved at `cmd/cli/` for standalone GPX uploads:

```bash
./gpx-uploader path/to/file.gpx
```

Credentials are resolved in order: 1Password CLI, environment variables (`EFBUSERNAME`/`EFBPASSWORD`), interactive prompts.

## Legal

- [Impressum](https://efb-connector.sauroter.de/impressum)
- [Datenschutzerklärung](https://efb-connector.sauroter.de/privacy)

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full text.
