# Local testing: EFB consent-gate flow

The connector detects EFB v2026.1's track-usage consent gate, surfaces it
on the dashboard, and emails the user. This doc walks through testing
those flows against the local mock EFB — no real Kanu-EFB account needed.

## Knobs

| Knob                                                              | Effect                                                                                |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| `DEV_MODE=true`                                                   | Wires `MockEFBProvider` and `MockGarminProvider`. Magic-link emails print to stdout.  |
| `DEV_MOCK_EFB_CONSENT=1`                                          | Starts the mock with the simulated consent gate **already active**.                   |
| `POST /internal/admin/dev/mock-efb/consent-gate?on=1` (Bearer)    | Flips the gate **on** at runtime.                                                     |
| `POST /internal/admin/dev/mock-efb/consent-gate?on=0` (Bearer)    | Flips the gate **off** at runtime.                                                    |

In `DEV_MODE`, `INTERNAL_SECRET` defaults to `dev-secret`.

## Quickstart

```sh
# Optional: clean DB to start from scratch
make clean

# Start the server with the consent gate already active
make dev-consent
```

In another terminal:

```sh
# 1. Sign up — paste your email, then check the server stdout for a line like
#    "DEV MODE: magic link email not sent ... link=http://localhost:8080/auth/verify?token=…"
# 2. Open that URL in the browser to log in.
# 3. Connect Garmin: any non-empty user/pass works (mock accepts anything).
# 4. Connect EFB: any non-empty user/pass.
#    -> The save handler calls CheckConsentGate. With the gate active, you
#       should land on /dashboard with a flash and an amber "Action required
#       on EFB" banner.
# 5. Trigger a sync from the dashboard.
#    -> The sync engine logs "error_category=consent_required" and
#       "sent efb consent-required email" with the email payload.
#    -> Activity appears in /internal/admin/activity-errors.
```

Inspect the logged email body / state:

```sh
# What does activity-errors show? (include_body to see the captured page)
curl -s -H "Authorization: Bearer dev-secret" \
  'http://localhost:8080/internal/admin/activity-errors?include_body=1' | jq .

# Verify consent_required state in SQLite
sqlite3 efb-connector.db "SELECT user_id, consent_required, consent_notified_at FROM efb_credentials;"
```

## Flip the gate at runtime

Without restarting the server you can flip the mock's state to test the
self-healing path:

```sh
# Turn the gate OFF (uploads will start succeeding)
curl -X POST -H "Authorization: Bearer dev-secret" \
  'http://localhost:8080/internal/admin/dev/mock-efb/consent-gate?on=0'
# -> {"consent_gate": false}

# Now trigger a sync from the dashboard or via the admin endpoint.
# The dashboard banner should disappear (sync's success path calls
# ClearEFBConsentRequired).

# Turn it back ON
curl -X POST -H "Authorization: Bearer dev-secret" \
  'http://localhost:8080/internal/admin/dev/mock-efb/consent-gate?on=1'
# -> {"consent_gate": true}
```

The endpoint returns `404` outside `DEV_MODE` because the real
`*efb.EFBClient` does not implement `efb.ConsentGateController`.

## What to look for in the logs

Successful detection:

```text
ERROR failed to upload GPX to EFB ... error_category=consent_required
INFO  sent efb consent-required email user_id=N to=you@example.com
```

Email rate limit (won't email twice within 7 days):

```text
ERROR failed to upload GPX to EFB ... error_category=consent_required
# (no "sent efb consent-required email" line on the second sync)
```

Self-healing on success:

```text
INFO activity uploaded successfully ...
# After this the dashboard banner is gone on next page load.
```

## Settings-save flow specifically

To exercise the proactive detection in `handleEFBSettingsSave`:

1. Make sure the gate is **on** (`make dev-consent`, or curl the toggle).
2. Sign in.
3. Go to `/settings/efb`, enter any user/pass, save.
4. You should be redirected to `/dashboard` with the banner visible and
   the `flash.efb_saved_consent_required` flash message.
5. Flip the gate **off**, then re-save credentials at `/settings/efb`.
6. You should see the regular `flash.efb_saved` flash and no banner.

## Cleaning up

```sh
make clean        # removes the dev DB
```
