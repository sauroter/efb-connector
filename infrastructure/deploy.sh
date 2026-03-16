#!/usr/bin/env bash
set -euo pipefail

APP_NAME="efb-connector"
VOLUME_NAME="efb_data"
REGION="fra"
VOLUME_SIZE="1"

# Check required env vars
: "${ENCRYPTION_KEY:?ENCRYPTION_KEY required}"
: "${RESEND_API_KEY:?RESEND_API_KEY required}"
: "${INTERNAL_SECRET:?INTERNAL_SECRET required}"
: "${BASE_URL:?BASE_URL required}"
: "${EMAIL_FROM:?EMAIL_FROM required}"

echo "Deploying $APP_NAME..."

# Create app if not exists
if ! fly apps list | grep -q "$APP_NAME"; then
  echo "Creating app $APP_NAME..."
  fly apps create "$APP_NAME" --org personal
else
  echo "App $APP_NAME already exists"
fi

# Create volume if not exists
if ! fly volumes list -a "$APP_NAME" | grep -q "$VOLUME_NAME"; then
  echo "Creating volume $VOLUME_NAME..."
  fly volumes create "$VOLUME_NAME" \
    --app "$APP_NAME" \
    --region "$REGION" \
    --size "$VOLUME_SIZE" \
    --yes
else
  echo "Volume $VOLUME_NAME already exists"
fi

# Set secrets
echo "Setting secrets..."
fly secrets set \
  ENCRYPTION_KEY="$ENCRYPTION_KEY" \
  RESEND_API_KEY="$RESEND_API_KEY" \
  INTERNAL_SECRET="$INTERNAL_SECRET" \
  BASE_URL="$BASE_URL" \
  EMAIL_FROM="$EMAIL_FROM" \
  --app "$APP_NAME"

# Deploy image
echo "Deploying image..."
fly deploy --app "$APP_NAME"

echo "Deployment complete!"
