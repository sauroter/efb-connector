#!/usr/bin/env bash
# send-postmortem-broadcast.sh — One-off transparency broadcast announcing
# the v0.9.7 cron-dropout fix to the Resend "Active Syncers" segment.
#
# Default behaviour is draft-only — the broadcast is created in Resend but
# not sent. Pass --send to also send it. This two-step deliberately avoids
# fat-fingering 80+ emails.
#
# Required env:
#   RESEND_MANAGEMENT_KEY    — Resend API key (full access)
#   RESEND_SEGMENT_ACTIVE    — Segment ID for "Active Syncers"
#   EMAIL_FROM               — From: header (e.g. 'EFB Connector <noreply@…>')
set -euo pipefail

: "${RESEND_MANAGEMENT_KEY:?RESEND_MANAGEMENT_KEY required}"
: "${RESEND_SEGMENT_ACTIVE:?RESEND_SEGMENT_ACTIVE required}"
: "${EMAIL_FROM:?EMAIL_FROM required}"

API="https://api.resend.com"
AUTH="Authorization: Bearer ${RESEND_MANAGEMENT_KEY}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

NAME="Postmortem: nightly sync (v0.9.7)"
SUBJECT="Wichtige Information: Sync-Problem behoben / Important: sync issue resolved"
HTML_FILE="$SCRIPT_DIR/templates/postmortem-2026-05-04.html"
TEXT_FILE="$SCRIPT_DIR/templates/postmortem-2026-05-04.txt"

SEND=0
if [ "${1:-}" = "--send" ]; then
  SEND=1
fi

log() { echo "$@" >&2; }
throttle() { sleep 0.25; }  # Resend rate limit: 5 req/s

[ -f "$HTML_FILE" ] || { log "missing $HTML_FILE"; exit 1; }
[ -f "$TEXT_FILE" ] || { log "missing $TEXT_FILE"; exit 1; }

# ── Verify segment exists ────────────────────────────────────────────────────

log "=== Verifying segment ==="
SEG_INFO=$(curl -sSf -H "$AUTH" "$API/segments/$RESEND_SEGMENT_ACTIVE")
SEG_NAME=$(python3 -c "import sys,json; print(json.loads(sys.argv[1]).get('name',''))" "$SEG_INFO")
log "  ok '$SEG_NAME' ($RESEND_SEGMENT_ACTIVE)"

# ── Create broadcast ─────────────────────────────────────────────────────────

log ""
log "=== Creating broadcast ==="

PAYLOAD=$(python3 - "$RESEND_SEGMENT_ACTIVE" "$EMAIL_FROM" "$NAME" "$SUBJECT" "$HTML_FILE" "$TEXT_FILE" <<'PY'
import json, sys
segment_id, sender, name, subject, html_path, text_path = sys.argv[1:7]
with open(html_path, encoding="utf-8") as f:
    html = f.read()
with open(text_path, encoding="utf-8") as f:
    text = f.read()
print(json.dumps({
    "segment_id": segment_id,
    "from":       sender,
    "name":       name,
    "subject":    subject,
    "html":       html,
    "text":       text,
}))
PY
)

throttle
RESP=$(curl -sSf -X POST "$API/broadcasts" \
  -H "$AUTH" \
  -H "Content-Type: application/json" \
  -d "$PAYLOAD")

BROADCAST_ID=$(python3 -c "import sys,json; print(json.loads(sys.argv[1])['id'])" "$RESP")
log "  ++ created broadcast: $BROADCAST_ID"
log "  dashboard: https://resend.com/broadcasts/$BROADCAST_ID"

# ── Send (only with --send) ──────────────────────────────────────────────────

if [ "$SEND" -eq 0 ]; then
  log ""
  log "=== Draft only ==="
  log "  Review the draft in the Resend dashboard. To send, re-run with --send:"
  log "    bash $0 --send"
  exit 0
fi

log ""
log "=== Sending broadcast ==="
log "  segment: '$SEG_NAME' ($RESEND_SEGMENT_ACTIVE)"
log "  broadcast: $BROADCAST_ID"
read -r -p "Type SEND to confirm dispatch to all segment members: " CONFIRM </dev/tty
if [ "$CONFIRM" != "SEND" ]; then
  log "  aborted; broadcast left as draft: $BROADCAST_ID"
  exit 1
fi

throttle
curl -sSf -X POST "$API/broadcasts/$BROADCAST_ID/send" \
  -H "$AUTH" > /dev/null
log "  sent: $BROADCAST_ID"
log "  check delivery in the dashboard within ~5 minutes."
