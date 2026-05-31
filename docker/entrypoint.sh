#!/bin/sh
set -eu

mkdir -p /app/data/mediaparser/cache /run/nginx /var/log/nginx

nginx -c /etc/nginx/nginx.conf

set -- /app/zbp \
  -u "${ONEBOT_WS_URL:-ws://127.0.0.1:3001}" \
  -webui "${WEBUI_ADDR:-0.0.0.0:3000}" \
  -n "${BOT_NICKNAME:-ZeroBot}" \
  -p "${COMMAND_PREFIX:-/}"

if [ -n "${ONEBOT_WS_TOKEN:-}" ]; then
  set -- "$@" -t "$ONEBOT_WS_TOKEN"
fi

# ZBP_ARGS and SUPER_USERS intentionally use shell splitting so users can pass
# ordinary flags such as "-d" and super-user ids such as "12345 67890".
# Do not put passwords, QQBot secrets or cookies here; save them in WebUI.
# shellcheck disable=SC2086
exec "$@" ${ZBP_ARGS:-} ${SUPER_USERS:-}
