# Fly.io Infrastructure

Shell scripts for deploying the efb-connector web service to Fly.io.

## Prerequisites

1. Install [flyctl](https://fly.io/docs/flyctl/install/)
2. Authenticate: `fly auth login`

## Setup Secrets

```bash
cp secrets.sh.example secrets.sh
# Edit secrets.sh with your credentials:
# ENCRYPTION_KEY, RESEND_API_KEY, INTERNAL_SECRET, BASE_URL
```

## Resend Infrastructure

The `resend-setup.sh` script manages Resend segments and email templates as code.
It is idempotent — creates resources if missing, updates existing ones.

```bash
# Local: creates segments/templates, prints IDs
export RESEND_API_KEY="re_..."
./resend-setup.sh

# CI: also stages Fly secrets automatically (requires FLY_API_TOKEN)
```

This runs automatically in the CD workflow on every release.

Template HTML files live in `infrastructure/templates/`.

## Deploy

```bash
source secrets.sh
./deploy.sh
```

The deploy script is idempotent — it can be run multiple times safely.

## Verify Deployment

```bash
# Check app status
fly status -a efb-connector

# List volumes
fly volumes list -a efb-connector

# List machines
fly machines list -a efb-connector

# List secrets
fly secrets list -a efb-connector

# Health check
curl https://efb-connector.sauroter.de/health
```

## Manual Sync (all users)

```bash
curl -X POST https://efb-connector.sauroter.de/internal/sync/run-all \
  -H "Authorization: Bearer <INTERNAL_SECRET>"
```

## Destroy

To tear down all resources:

```bash
./destroy.sh
```

This destroys the app, all machines, and all volumes.
