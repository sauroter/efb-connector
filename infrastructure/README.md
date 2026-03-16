# Fly.io Infrastructure

Shell scripts for deploying the efb-connector-sync app to Fly.io.

## Prerequisites

1. Install [flyctl](https://fly.io/docs/flyctl/install/)
2. Authenticate: `fly auth login`

## Setup Secrets

```bash
cp secrets.sh.example secrets.sh
# Edit secrets.sh with your credentials
```

## Deploy

```bash
source secrets.sh
./deploy.sh
```

The deploy script is idempotent - it can be run multiple times safely.

## Verify Deployment

```bash
# Check app status
fly status -a efb-connector-sync

# List volumes
fly volumes list -a efb-connector-sync

# List machines (should show scheduled machine)
fly machines list -a efb-connector-sync

# List secrets
fly secrets list -a efb-connector-sync
```

## Manual Trigger

To run the sync immediately instead of waiting for the daily schedule:

```bash
fly machines start <machine-id> -a efb-connector-sync
```

## Destroy

To tear down all resources:

```bash
./destroy.sh
```

This destroys the app, all machines, and all volumes.
