#!/bin/sh

set -e

groupmod -g "$PGID" --non-unique user
usermod -u "$PUID" --non-unique user
chown -R "${PUID}:${PGID}" /app
exec gosu "${PUID}:${PGID}" /app/youtubedr-web \
    -bbdown "$YOUTUBEDR" \
    -addr "$LISTEN_HOST:$LISTEN_PORT" \
    -download "$DOWNLOAD"
