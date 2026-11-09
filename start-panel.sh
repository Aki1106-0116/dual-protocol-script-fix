#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
ENV_FILE="${DPS_ENV_FILE:-/etc/default/dual-protocol-script-fix}"
LIB_DIR="/usr/local/lib/dual-protocol-script-fix"
exec 9>"${DPS_CONFIG_LOCK:-/run/dual-protocol-script-fix-config.lock}"
flock -w 60 9 || { echo '配置操作尚未完成，稍后重新启动面板' >&2;exit 1; }
python3 "$LIB_DIR/storage.py" recover "$ENV_FILE"
WORK_DIR="/var/lib/dual-protocol-script-fix"
source "$ENV_FILE"
flock -u 9
exec 9>&-
exec /usr/local/bin/dual-protocol-script-fix -dir "$WORK_DIR" -web "$WEB_PORT" -max "$MAX_EXITS" -domain "$PANEL_DOMAIN" -tls-cert "$TLS_CERT" -tls-key "$TLS_KEY"
