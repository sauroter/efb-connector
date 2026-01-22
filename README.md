# EFB Connector

A CLI tool that syncs water sport activities from Garmin Connect to the [Kanu-EFB portal](https://efb.kanu-efb.de/).

## Features

- Fetch water sport activities (kayaking, SUP, canoeing, rowing) from Garmin Connect
- Upload GPX files to the EFB portal
- Automatic daily sync via Fly.io scheduled machine
- Supports 1Password for credential management

## Installation

### Prerequisites

- Go 1.24+
- Python 3.12+ with `garminconnect` package

### Build

```bash
go build -o gpx-uploader ./cmd
pip install -r requirements.txt
```

## Usage

### Upload a GPX file

```bash
./gpx-uploader upload path/to/file.gpx
```

### List Garmin activities

```bash
./gpx-uploader list --days 30
```

### Fetch GPX from Garmin

```bash
./gpx-uploader fetch <activity_id> --output ./downloads
```

### Sync (fetch from Garmin + upload to EFB)

```bash
./gpx-uploader sync --days 3
```

## Configuration

### Environment Variables

```bash
# EFB Portal credentials
export EFBUSERNAME="your-username"
export EFBPASSWORD="your-password"

# Garmin Connect credentials
export GARMIN_EMAIL="your-email"
export GARMIN_PASSWORD="your-password"
```

### 1Password Integration

Create `config.json` (see `config.json.example`):

```json
{
  "onepassword": {
    "account": "my.1password.com",
    "vault": "Private",
    "item": "EFB Portal",
    "username_field": "username",
    "password_field": "password"
  },
  "garmin": {
    "onepassword": {
      "account": "my.1password.com",
      "vault": "Private",
      "item": "Garmin Connect",
      "email_field": "username",
      "password_field": "password"
    }
  }
}
```

## Fly.io Deployment

The app can be deployed to Fly.io for automatic daily syncing.

### Deploy

```bash
# Create app
fly apps create efb-connector-sync

# Set secrets
fly secrets set EFBUSERNAME="..." EFBPASSWORD="..." \
    GARMIN_EMAIL="..." GARMIN_PASSWORD="..." \
    -a efb-connector-sync

# Deploy with daily schedule
fly machine run . --schedule daily -a efb-connector-sync --region fra
```

### Monitor

```bash
fly logs -a efb-connector-sync
fly machine status <machine-id> -a efb-connector-sync
```

## Supported Activity Types

- Kayaking
- Stand Up Paddleboarding (SUP)
- Canoeing
- Rowing
- Paddling
- Whitewater Rafting

## License

MIT
