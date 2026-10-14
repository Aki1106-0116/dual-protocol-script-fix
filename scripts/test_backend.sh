#!/usr/bin/env bash
# Sandbox all paths and external services. No real package or service changes.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SANDBOX="$(mktemp -d)"
cleanup_test() { case "$SANDBOX" in /tmp/tmp.*) rm -rf -- "$SANDBOX" ;; *) echo "保留测试目录：$SANDBOX" >&2 ;; esac; }
trap cleanup_test EXIT
if [[ -n "${TEST_PYTHON:-}" ]]; then python3() { "$TEST_PYTHON" "$@"; }; fi
for module in node-common node-services node-transaction node-menu; do source "$ROOT/lib/${module}.sh"; done
STATE_DIR="$SANDBOX/state";STATE_FILE="$STATE_DIR/state.env";XRAY_CONFIG="$SANDBOX/xray/config.json";HY2_CONFIG="$SANDBOX/hy2/config.yaml";CADDYFILE="$SANDBOX/caddy/Caddyfile";OUT_DIR="$SANDBOX/output"
HY2_CLIENT_YAML="$OUT_DIR/hy2-client.yaml";HY2_CLIENT_JSON="$OUT_DIR/hy2-client.json";XRAY_CLIENT_JSON="$OUT_DIR/xray-client.json";INFO_FILE="$OUT_DIR/node-info.txt"
STORAGE_HELPER="$ROOT/scripts/storage.py";mkdir -p "$STATE_DIR" "$(dirname "$XRAY_CONFIG")" "$(dirname "$HY2_CONFIG")" "$(dirname "$CADDYFILE")" "$OUT_DIR"
export DPS_CONFIG_LOCK_HELD=1
XRAY_BIN="$SANDBOX/fake-xray";printf '#!/usr/bin/env bash\nexit 0\n' > "$XRAY_BIN";chmod +x "$XRAY_BIN"
systemctl() { return 0; }
ensure_hysteria_capabilities() { return 0; }
start_xray() { return 0; }
start_caddy() { return 0; }
start_hy2() { return 0; }
reality_local_self_test() { return 0; }
web_cleanup_replaced_domains() { return 0; }
write_hy2_config_acme() { write_hy2_config_tls; }
write_caddyfile_xhttp_hy2() { printf 'test\n' > "$CADDYFILE"; }
MAIN_MODE=reality;STACK_MODE=reality_hy2;NODE_NAME='fixture';FP=chrome;REALITY_ADDRESS=203.0.113.10;REALITY_SNI=example.com;REALITY_UUID=123e4567-e89b-12d3-a456-426614174000;REALITY_PRIVATE_KEY=fixture-private;REALITY_PUBLIC_KEY=fixture-public;REALITY_SHORT_ID=abcdef
HY2_DOMAIN=h.example.com;HY2_PASSWORD='original-secret';HY2_PORT_MODE=single;HY2_PORT=45678;HY2_CERT_SOURCE=acme;HY2_ACME_EMAIL=admin@example.com;HY2_OBFS_TYPE=none;HY2_GECKO_MIN_PACKET_SIZE=512;HY2_GECKO_MAX_PACKET_SIZE=1200;HY2_CERT_PATH=/fixture/cert;HY2_KEY_PATH=/fixture/key;CADDY_REQUIRED=0
save_state;write_reality_xray_config;write_hy2_config_tls;generate_main_outputs
web_export_config > "$SANDBOX/export.json"
python3 - "$SANDBOX/export.json" <<'PY'
import json,sys
c=json.load(open(sys.argv[1]));assert c['main_mode']=='reality';assert isinstance(c['xhttp_backend_port'],int);assert 'reality_private_key' not in c
PY
HY2_PASSWORD='new-secret-with-$()-quotes'
apply_current_shell_config
load_state
[[ "$HY2_PASSWORD" == 'new-secret-with-$()-quotes' ]] || { echo 'saved credentials differ' >&2; exit 1; }
cp "$XRAY_CONFIG" "$SANDBOX/old-xray";cp "$HY2_CONFIG" "$SANDBOX/old-hy2";cp "$STATE_FILE" "$SANDBOX/old-state"
HY2_PASSWORD=must-rollback
# Failure inside an unconditional transaction triggers the EXIT rollback trap.
start_hy2() { echo 'injected HY2 startup failure' >&2; return 1; }
set +e
(set -e;apply_current_shell_config)
RC=$?
set -e
[[ "$RC" -ne 0 ]] || { echo 'failure reported success' >&2;exit 1; }
cmp "$XRAY_CONFIG" "$SANDBOX/old-xray";cmp "$HY2_CONFIG" "$SANDBOX/old-hy2";cmp "$STATE_FILE" "$SANDBOX/old-state"
start_hy2() { return 0; }
load_state;MAIN_MODE=xhttp;STACK_MODE=xhttp_hy2;CADDY_REQUIRED=1;HY2_CERT_SOURCE=caddy;XHTTP_DOMAIN=x.example.com;XHTTP_UUID=123e4567-e89b-12d3-a456-426614174000;XHTTP_PATH=/fixture;XHTTP_BACKEND_PORT=10080
save_state;write_xhttp_xray_config;write_hy2_config_tls;generate_main_outputs
HY2_PORT_MODE=hop;HY2_FIRST_PORT=30000;HY2_END_PORT=30050;HY2_PORT=30000;HY2_HOP_INTERVAL=25;HY2_OBFS_TYPE=gecko;HY2_OBFS_PASSWORD='gecko-secret'
apply_current_shell_config
python3 - "$XRAY_CONFIG" "$HY2_CONFIG" "$HY2_CLIENT_JSON" "$XRAY_CLIENT_JSON" <<'PY'
import json,sys,yaml
x=json.load(open(sys.argv[1]));assert x['inbounds'][0]['tag']=='vless-xhttp-in';assert x['inbounds'][0]['port']==10080
h=yaml.safe_load(open(sys.argv[2]));assert h['listen']==':30000-30050';assert h['obfs']['type']=='gecko';assert h['auth']['password']=='new-secret-with-$()-quotes'
c=json.load(open(sys.argv[3]));assert c['server']=='h.example.com:30000-30050';assert c['obfs']['gecko']['password']=='gecko-secret'
json.load(open(sys.argv[4]))
PY
grep -Fq "$OUT_DIR/hy2-client.yaml" "$INFO_FILE"
echo 'Backend regression passed: export types, Reality save, startup rollback, XHTTP, hopping, Gecko, client JSON/YAML.'
