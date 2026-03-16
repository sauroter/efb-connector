#!/usr/bin/env bash
set -euo pipefail

APP_NAME="efb-connector"

echo "Destroying app $APP_NAME and all resources..."
fly apps destroy "$APP_NAME" --yes

echo "Destroy complete!"
