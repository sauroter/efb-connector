#!/usr/bin/env bash
# resend-setup.sh — Idempotent IaC for Resend segments and email templates.
#
# Creates segments and templates if they don't exist, updates templates if they
# do, and publishes all templates.
#
# When FLY_API_TOKEN is set (e.g. in CI), automatically updates Fly secrets.
# Otherwise prints the IDs for manual use.
#
# Required env:
#   RESEND_API_KEY    — Resend API key
#
# Optional env:
#   FLY_API_TOKEN     — if set, runs `flyctl secrets set` automatically
#   FLY_APP           — Fly app name (default: efb-connector)
set -euo pipefail

: "${RESEND_API_KEY:?RESEND_API_KEY required}"

API="https://api.resend.com"
AUTH="Authorization: Bearer ${RESEND_API_KEY}"
FLY_APP="${FLY_APP:-efb-connector}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ── Helpers ──────────────────────────────────────────────────────────────────

log() { echo "$@" >&2; }

json_val() {
  python3 -c "import sys,json; print(json.loads(sys.argv[1])[sys.argv[2]])" "$1" "$2"
}

json_find() {
  python3 -c "
import sys, json
data = json.loads(sys.argv[1]).get('data', [])
key, val = sys.argv[2], sys.argv[3]
matches = [item['id'] for item in data if item.get(key) == val]
print(matches[0] if matches else '')
" "$1" "$2" "$3"
}

# ── Segments ─────────────────────────────────────────────────────────────────

log "=== Segments ==="

SEGMENTS_LIST=$(curl -sSf -H "$AUTH" "$API/segments")

ensure_segment() {
  local name="$1"
  local existing
  existing=$(json_find "$SEGMENTS_LIST" name "$name")

  if [ -n "$existing" ]; then
    log "  ok '$name': $existing"
    echo "$existing"
  else
    local resp
    resp=$(curl -sSf -X POST "$API/segments" \
      -H "$AUTH" \
      -H "Content-Type: application/json" \
      -d "{\"name\": \"$name\"}")
    local id
    id=$(json_val "$resp" id)
    log "  ++ '$name': $id"
    echo "$id"
  fi
}

ACTIVE_ID=$(ensure_segment "Active Syncers")
SETUP_ID=$(ensure_segment "Needs Setup")

# ── Templates ────────────────────────────────────────────────────────────────

log ""
log "=== Templates ==="

TEMPLATES_LIST=$(curl -sSf -H "$AUTH" "$API/templates")

ensure_template() {
  local alias="$1"
  local name="$2"
  local subject="$3"
  local html_file="$4"

  local html
  html=$(cat "$html_file")

  local existing
  existing=$(json_find "$TEMPLATES_LIST" alias "$alias")

  if [ -n "$existing" ]; then
    log "  ~~ '$alias' ($existing)"
    local payload
    payload=$(python3 -c "
import json, sys
print(json.dumps({'subject': sys.argv[1], 'html': sys.argv[2]}))
" "$subject" "$html")
    curl -sSf -X PATCH "$API/templates/$alias" \
      -H "$AUTH" \
      -H "Content-Type: application/json" \
      -d "$payload" > /dev/null
  else
    log "  ++ '$alias'"
    local payload
    payload=$(python3 -c "
import json, sys
print(json.dumps({'name': sys.argv[1], 'alias': sys.argv[2], 'subject': sys.argv[3], 'html': sys.argv[4]}))
" "$name" "$alias" "$subject" "$html")
    curl -sSf -X POST "$API/templates" \
      -H "$AUTH" \
      -H "Content-Type: application/json" \
      -d "$payload" > /dev/null
  fi
}

ensure_template \
  "garmin-upgrade-de" \
  "Garmin Upgrade (DE)" \
  "EFB Connector: Garmin-Integration aktualisiert" \
  "$SCRIPT_DIR/templates/garmin-upgrade-de.html"

ensure_template \
  "garmin-upgrade-en" \
  "Garmin Upgrade (EN)" \
  "EFB Connector: Garmin Integration Updated" \
  "$SCRIPT_DIR/templates/garmin-upgrade-en.html"

# ── Publish Templates ────────────────────────────────────────────────────────

log ""
log "=== Publishing Templates ==="

publish_template() {
  local alias="$1"
  curl -sSf -X POST "$API/templates/$alias/publish" \
    -H "$AUTH" > /dev/null
  log "  ok '$alias'"
}

publish_template "garmin-upgrade-de"
publish_template "garmin-upgrade-en"

# ── Update Fly Secrets ───────────────────────────────────────────────────────

log ""
log "=== Fly Secrets ==="

if [ -n "${FLY_API_TOKEN:-}" ]; then
  log "  Updating Fly secrets for $FLY_APP..."
  flyctl secrets set \
    RESEND_SEGMENT_ACTIVE="$ACTIVE_ID" \
    RESEND_SEGMENT_NEEDS_SETUP="$SETUP_ID" \
    --app "$FLY_APP" \
    --stage
  log "  ok secrets staged (applied on next deploy)"
else
  log "  FLY_API_TOKEN not set — skipping automatic update."
  log ""
  log "  Set manually:"
  log "    fly secrets set \\"
  log "      RESEND_SEGMENT_ACTIVE=$ACTIVE_ID \\"
  log "      RESEND_SEGMENT_NEEDS_SETUP=$SETUP_ID \\"
  log "      --app $FLY_APP"
fi

log ""
log "=== Done ==="
