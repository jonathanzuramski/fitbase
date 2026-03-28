#!/bin/sh
set -e

PUID=${PUID:-1000}
PGID=${PGID:-1000}

echo "--------------------------------------------"
echo "  fitbase"
echo "--------------------------------------------"
echo "  PUID:  ${PUID}"
echo "  PGID:  ${PGID}"
echo "  Port:  ${FITBASE_PORT:-8780}"
echo "  Data:  /data"
echo "--------------------------------------------"

# Create group and user with the requested IDs
addgroup -g "$PGID" -S fitbase 2>/dev/null || true
adduser -u "$PUID" -S -G fitbase -D -h /data fitbase 2>/dev/null || true

# Ensure data directory exists and is fully owned by the right user
mkdir -p /data/watch /data/archive
echo "Setting /data ownership to ${PUID}:${PGID}..."
chown -R fitbase:fitbase /data

# Verify data directory is writable
if su-exec fitbase touch /data/.write-test 2>/dev/null; then
    rm -f /data/.write-test
    echo "Data directory OK"
else
    echo "ERROR: /data is not writable by UID ${PUID}. Check your volume permissions."
    exit 1
fi

echo "Starting fitbase..."

# Drop to unprivileged user and exec the app (becomes PID 1)
exec su-exec fitbase /usr/local/bin/fitbase
