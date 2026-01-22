# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
# Build the CLI tool
go build -o gpx-uploader ./cmd

# Run directly without building
go run ./cmd [path-to-gpx-file]

# Run tests
go test ./...

# Run a single test
go test ./... -run TestName
```

## Project Overview

This is a Go CLI tool (`efb-connector`) that uploads GPX files to the Kanu-EFB portal (https://efb.kanu-efb.de/). The tool:

1. Authenticates via form POST to the login endpoint
2. Maintains session cookies across requests
3. Uploads GPX files via multipart form data to the user map endpoint

## Authentication

Credentials are resolved in this order:
1. **1Password CLI** (if configured in `config.json`)
2. **Environment variables:** `EFBUSERNAME` and `EFBPASSWORD`
3. **Interactive prompts** (fallback)

## Configuration

To use 1Password integration:

```bash
cp config.json.example config.json
# Edit config.json with your 1Password account details
```

The config file is gitignored to keep your account details private.

## Usage

```bash
./gpx-uploader path/to/file.gpx
```
