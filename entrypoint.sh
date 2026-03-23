#!/bin/sh
# Fix ownership of /data volume for existing deployments where files
# were created by a root-user container. Then drop to unprivileged user.
chown -R app:app /data 2>/dev/null || true
exec su-exec app ./efb-connector "$@"
