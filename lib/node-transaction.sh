#!/usr/bin/env bash
# Sourced by node-installer.sh; functions have no independent entry point.
web_export_config() {
  config_lock
  load_state || { err "节点尚未安装，无法读取配置。"; return 1; }
  local reality_private_key_configured="0"
  [[ -n "${REALITY_PRIVATE_KEY:-}" ]] && reality_private_key_configured="1"
  export STACK_MODE MAIN_MODE NODE_NAME FP
  export XHTTP_DOMAIN XHTTP_PATH XHTTP_UUID XHTTP_BACKEND_PORT
  export REALITY_ADDRESS REALITY_SNI REALITY_UUID REALITY_PUBLIC_KEY REALITY_SHORT_ID
  export HY2_DOMAIN HY2_PASSWORD HY2_PORT_MODE HY2_PORT HY2_FIRST_PORT HY2_END_PORT
  export HY2_HOP_INTERVAL HY2_MIN_HOP_INTERVAL HY2_MAX_HOP_INTERVAL
  export HY2_CERT_SOURCE HY2_ACME_EMAIL HY2_OBFS_TYPE HY2_OBFS_PASSWORD
  export HY2_GECKO_MIN_PACKET_SIZE HY2_GECKO_MAX_PACKET_SIZE
  REALITY_PRIVATE_KEY_CONFIGURED="$reality_private_key_configured" python3 - <<'PY'
import json
import os

fields = (
    "STACK_MODE MAIN_MODE NODE_NAME FP "
    "XHTTP_DOMAIN XHTTP_PATH XHTTP_UUID XHTTP_BACKEND_PORT "
    "REALITY_ADDRESS REALITY_SNI REALITY_UUID REALITY_PUBLIC_KEY REALITY_SHORT_ID "
    "HY2_DOMAIN HY2_PASSWORD HY2_PORT_MODE HY2_PORT HY2_FIRST_PORT HY2_END_PORT "
    "HY2_HOP_INTERVAL HY2_MIN_HOP_INTERVAL HY2_MAX_HOP_INTERVAL "
    "HY2_CERT_SOURCE HY2_ACME_EMAIL HY2_OBFS_TYPE HY2_OBFS_PASSWORD "
    "HY2_GECKO_MIN_PACKET_SIZE HY2_GECKO_MAX_PACKET_SIZE"
).split()
result = {name.lower(): os.environ.get(name, "") for name in fields}
for name in (
	"XHTTP_BACKEND_PORT "
    "HY2_PORT HY2_FIRST_PORT HY2_END_PORT "
    "HY2_HOP_INTERVAL HY2_MIN_HOP_INTERVAL HY2_MAX_HOP_INTERVAL "
    "HY2_GECKO_MIN_PACKET_SIZE HY2_GECKO_MAX_PACKET_SIZE"
).split():
    value = os.environ.get(name, "")
    result[name.lower()] = int(value) if value.isdigit() else 0
result["reality_private_key_configured"] = (
    os.environ.get("REALITY_PRIVATE_KEY_CONFIGURED") == "1"
)
print(json.dumps(result, ensure_ascii=False, separators=(",", ":")))
PY
}

web_require_apply_environment() {
  local name
  [[ "${DPS_WEB_APPLY:-}" == "1" ]] || {
    err "拒绝缺少 Web 应用标记的非交互请求。"
    return 1
  }
  for name in \
    DPS_WEB_NODE_NAME DPS_WEB_FINGERPRINT \
    DPS_WEB_XHTTP_DOMAIN DPS_WEB_XHTTP_PATH DPS_WEB_XHTTP_UUID DPS_WEB_XHTTP_BACKEND_PORT \
    DPS_WEB_REALITY_ADDRESS DPS_WEB_REALITY_SNI DPS_WEB_REALITY_UUID DPS_WEB_ROTATE_REALITY_KEYS \
    DPS_WEB_HY2_DOMAIN DPS_WEB_HY2_PASSWORD DPS_WEB_HY2_PORT_MODE DPS_WEB_HY2_PORT \
    DPS_WEB_HY2_FIRST_PORT DPS_WEB_HY2_END_PORT DPS_WEB_HY2_HOP_INTERVAL \
    DPS_WEB_HY2_MIN_HOP_INTERVAL DPS_WEB_HY2_MAX_HOP_INTERVAL \
    DPS_WEB_HY2_ACME_EMAIL DPS_WEB_HY2_OBFS_TYPE DPS_WEB_HY2_OBFS_PASSWORD \
    DPS_WEB_HY2_GECKO_MIN_PACKET_SIZE DPS_WEB_HY2_GECKO_MAX_PACKET_SIZE; do
    [[ -v "$name" ]] || {
      err "Web 配置请求缺少字段：$name"
      return 1
    }
  done
}

web_valid_uuid() {
  [[ "$1" =~ ^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$ ]]
}

web_valid_secret() {
  local value="$1" min="${2:-1}" max="${3:-256}"
  python3 - "$value" "$min" "$max" <<'PY' >/dev/null 2>&1
import sys

value = sys.argv[1]
minimum = int(sys.argv[2])
maximum = int(sys.argv[3])
raise SystemExit(
    0 if minimum <= len(value) <= maximum
    and not any(ord(char) < 32 or ord(char) == 127 for char in value)
    else 1
)
PY
}

web_valid_uint() {
  local value="$1" min="$2" max="$3"
  [[ "$value" =~ ^[0-9]+$ ]] || return 1
  value=$((10#$value))
  (( value >= min && value <= max ))
}

web_backup_path() {
  local source="$1" name="$2"
  if [[ -e "$source" ]]; then
    cp -a "$source" "${WEB_BACKUP_DIR}/${name}"
  else
    : > "${WEB_BACKUP_DIR}/${name}.missing"
  fi
}

web_restore_path() {
  local destination="$1" name="$2"
  if [[ -e "${WEB_BACKUP_DIR}/${name}.missing" ]]; then
    rm -f "$destination"
  elif [[ -e "${WEB_BACKUP_DIR}/${name}" ]]; then
    python3 "$STORAGE_HELPER" write "$destination" "${WEB_BACKUP_DIR}/${name}" "$(stat -c %a "${WEB_BACKUP_DIR}/${name}")"
  fi
}

web_rollback_config() {
  local failed_xhttp_domain="${XHTTP_DOMAIN:-}" failed_hy2_domain="${HY2_DOMAIN:-}"
  local rollback_failed=0
  set +e
  err "新节点配置未通过校验或服务健康检查，正在恢复修改前的完整配置。"
  web_restore_path "$STATE_FILE" state || rollback_failed=1
  web_restore_path "$XRAY_CONFIG" xray || rollback_failed=1
  web_restore_path "$HY2_CONFIG" hy2 || rollback_failed=1
  web_restore_path "$CADDYFILE" caddy || rollback_failed=1
  load_state >/dev/null 2>&1 || rollback_failed=1
  generate_main_outputs >/dev/null 2>&1 || rollback_failed=1
  systemctl restart xray >/dev/null 2>&1 || rollback_failed=1
  systemctl is-active --quiet xray || rollback_failed=1
  if [[ "${CADDY_REQUIRED:-0}" == "1" ]]; then
    systemctl restart caddy >/dev/null 2>&1 || rollback_failed=1
    systemctl is-active --quiet caddy || rollback_failed=1
  else
    systemctl disable --now caddy >/dev/null 2>&1 || true
  fi
  ensure_hysteria_capabilities >/dev/null 2>&1 || true
  systemctl restart hysteria-server >/dev/null 2>&1 || rollback_failed=1
  systemctl is-active --quiet hysteria-server || rollback_failed=1
  web_cleanup_replaced_domains "$failed_xhttp_domain" "$failed_hy2_domain" >/dev/null 2>&1 || true
  set -e
  if (( rollback_failed != 0 )); then
    err "回滚未能完整恢复配置/服务；备份保留在 $WEB_BACKUP_DIR，请检查服务器磁盘和服务日志。"
    WEB_PRESERVE_BACKUP=1
    return 1
  fi
  ok "旧配置、分享链接和组合服务已恢复并检查。"
}

web_validate_cert_pair() {
  local domain="$1" cert="$2" key="$3" cert_pub="" key_pub=""
  [[ -s "$cert" && -s "$key" ]] || return 1
  openssl x509 -in "$cert" -noout -checkend 3600 >/dev/null 2>&1 || return 1
  openssl x509 -in "$cert" -noout -checkhost "$domain" >/dev/null 2>&1 || return 1
  cert_pub="$(openssl x509 -in "$cert" -pubkey -noout 2>/dev/null | openssl pkey -pubin -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
  key_pub="$(openssl pkey -in "$key" -pubout -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
  [[ -n "$cert_pub" && "$cert_pub" == "$key_pub" ]]
}

web_wait_for_caddy_cert_only() {
  local domain="$1" pair="" cert="" key="" i
  log "等待 Caddy 为 ${domain} 签发/加载证书"
  for i in $(seq 1 60); do
    pair="$(find_caddy_cert_pair "$domain" || true)"
    cert="${pair%%|*}"
    key="${pair#*|}"
    if [[ -n "$pair" ]] && web_validate_cert_pair "$domain" "$cert" "$key"; then
      ok "已验证 ${domain} 的 Caddy 证书。"
      return 0
    fi
    sleep 2
  done
  err "未能找到 ${domain} 的 Caddy 证书；请检查 DNS 直连、TCP 80/443 与 CDN 状态。"
  journalctl -u caddy --no-pager -n 100 >&2 || true
  return 1
}

web_find_hy2_acme_pair() {
  local domain="$1" base="/var/lib/hysteria/acme/certificates" cert="" key=""
  [[ -d "$base" ]] || return 1
  cert="$(find "$base" -type f -name "${domain}.crt" 2>/dev/null | sort | tail -n 1 || true)"
  key="$(find "$base" -type f -name "${domain}.key" 2>/dev/null | sort | tail -n 1 || true)"
  web_validate_cert_pair "$domain" "$cert" "$key" || return 1
  printf '%s|%s\n' "$cert" "$key"
}

web_wait_for_hy2_acme_cert() {
  local domain="$1" pair="" i
  log "等待 Hysteria2 为 ${domain} 完成 ACME 签发并验证证书"
  for i in $(seq 1 90); do
    pair="$(web_find_hy2_acme_pair "$domain" || true)"
    if [[ -n "$pair" ]]; then
      HY2_CERT_PATH="${pair%%|*}"
      HY2_KEY_PATH="${pair#*|}"
      ok "已验证 ${domain} 的 Hysteria2 ACME 证书。"
      return 0
    fi
    sleep 2
  done
  err "未能取得 ${domain} 的 Hysteria2 ACME 证书；请确认 DNS 已直连本 VPS、CDN 代理关闭，且 TCP 80 可从公网访问。"
  journalctl -u hysteria-server --no-pager -n 100 >&2 || true
  return 1
}

web_start_caddy_if_changed() {
  # Password/name/UUID/port-only saves do not need to restart Caddy or disturb
  # its certificate manager. Domain/path/backend changes still take effect.
  if [[ -f "$WEB_BACKUP_DIR/caddy" ]] && cmp -s "$CADDYFILE" "$WEB_BACKUP_DIR/caddy" &&
     systemctl is-active --quiet caddy; then
    log "Caddy 配置未变化，复用运行中的服务及现有证书。"
    return 0
  fi
  start_caddy
}

web_remove_domain_assets_from_root() {
  local root="$1" domain="$2" resolved_root="" candidate="" resolved=""
  [[ -n "$domain" && -d "$root" ]] || return 0
  valid_domain "$domain" || return 1
  resolved_root="$(readlink -f -- "$root" 2>/dev/null || true)"
  [[ -n "$resolved_root" && "$resolved_root" != "/" ]] || return 1

  while IFS= read -r -d '' candidate; do
    resolved="$(readlink -f -- "$candidate" 2>/dev/null || true)"
    case "$resolved" in
      "$resolved_root"/*) rm -rf -- "$resolved" ;;
      *) warn "跳过不在证书存储目录内的路径：$candidate" ;;
    esac
  done < <(find "$resolved_root" -depth -type d -name "$domain" -print0 2>/dev/null)

  while IFS= read -r -d '' candidate; do
    resolved="$(readlink -f -- "$candidate" 2>/dev/null || true)"
    case "$resolved" in
      "$resolved_root"/*) rm -f -- "$resolved" ;;
      *) warn "跳过不在证书存储目录内的文件：$candidate" ;;
    esac
  done < <(find "$resolved_root" -type f \( -name "${domain}.crt" -o -name "${domain}.key" -o -name "${domain}.json" \) -print0 2>/dev/null)
}

web_cleanup_replaced_domains() {
  local old_xhttp_domain="${1:-}" old_hy2_domain="${2:-}" domain=""
  for domain in "$old_xhttp_domain" "$old_hy2_domain"; do
    [[ -n "$domain" ]] || continue
    if [[ "$domain" == "${XHTTP_DOMAIN:-}" || "$domain" == "${HY2_DOMAIN:-}" ]]; then
      continue
    fi
    web_remove_domain_assets_from_root "${CADDY_DATA:-/var/lib/caddy/.local/share/caddy}/certificates" "$domain" || return 1
    web_remove_domain_assets_from_root "/var/lib/hysteria/acme/certificates" "$domain" || return 1
    ok "已清理旧域名 ${domain} 的证书缓存；新服务配置也不会再为它自动续签。"
  done
}

web_apply_exit() {
  local rc=$?
  trap - EXIT HUP INT TERM
  if (( rc != 0 )) && [[ "${WEB_APPLY_COMMITTED:-0}" != "1" ]]; then
    web_rollback_config || true
  fi
  if [[ "${WEB_PRESERVE_BACKUP:-0}" != "1" ]]; then
    [[ -z "${WEB_BACKUP_DIR:-}" ]] || rm -rf -- "$WEB_BACKUP_DIR"
  fi
  exit "$rc"
}

web_validate_and_assign_config() {
  local normalized panel_port="" web_min_hop="" web_max_hop="" web_fixed_hop=""

  web_valid_secret "$DPS_WEB_NODE_NAME" 1 80 || {
    err "节点名称长度必须为 1-80，且不能包含控制字符。"
    return 1
  }
  [[ "$DPS_WEB_FINGERPRINT" == "chrome" || "$DPS_WEB_FINGERPRINT" == "firefox" ]] || {
    err "客户端指纹只能是 chrome 或 firefox。"
    return 1
  }
  NODE_NAME="$DPS_WEB_NODE_NAME"
  FP="$DPS_WEB_FINGERPRINT"

  normalized="$(normalize_domain "$DPS_WEB_HY2_DOMAIN")"
  valid_domain "$normalized" || { err "HY2 域名格式无效。"; return 1; }
  HY2_DOMAIN="$normalized"
  web_valid_secret "$DPS_WEB_HY2_PASSWORD" 1 256 || {
    err "HY2 密码长度必须为 1-256，且不能包含控制字符。"
    return 1
  }
  HY2_PASSWORD="$DPS_WEB_HY2_PASSWORD"

  case "$DPS_WEB_HY2_PORT_MODE" in
    single)
      web_valid_uint "$DPS_WEB_HY2_PORT" 1 65535 || { err "HY2 单端口无效。"; return 1; }
      HY2_PORT=$((10#$DPS_WEB_HY2_PORT))
      hy2_reserved_port "$HY2_PORT" && { err "HY2 端口禁止使用 80/443/2053/8443。"; return 1; }
      HY2_PORT_MODE="single"
      HY2_FIRST_PORT=""; HY2_END_PORT=""
      HY2_HOP_INTERVAL=""; HY2_MIN_HOP_INTERVAL=""; HY2_MAX_HOP_INTERVAL=""
      ;;
    hop)
      web_valid_uint "$DPS_WEB_HY2_FIRST_PORT" 1 65534 || { err "HY2 跳跃起始端口无效。"; return 1; }
      web_valid_uint "$DPS_WEB_HY2_END_PORT" 2 65535 || { err "HY2 跳跃结束端口无效。"; return 1; }
      HY2_FIRST_PORT=$((10#$DPS_WEB_HY2_FIRST_PORT))
      HY2_END_PORT=$((10#$DPS_WEB_HY2_END_PORT))
      (( HY2_END_PORT > HY2_FIRST_PORT )) || { err "HY2 结束端口必须大于起始端口。"; return 1; }
      hy2_range_contains_reserved "$HY2_FIRST_PORT" "$HY2_END_PORT" && {
        err "HY2 跳跃范围不能包含 80/443/2053/8443。"
        return 1
      }
      HY2_PORT_MODE="hop"
      HY2_PORT="$HY2_FIRST_PORT"
      web_min_hop="$DPS_WEB_HY2_MIN_HOP_INTERVAL"
      web_max_hop="$DPS_WEB_HY2_MAX_HOP_INTERVAL"
      web_fixed_hop="$DPS_WEB_HY2_HOP_INTERVAL"
      [[ "$web_min_hop" == "0" ]] && web_min_hop=""
      [[ "$web_max_hop" == "0" ]] && web_max_hop=""
      [[ "$web_fixed_hop" == "0" ]] && web_fixed_hop=""
      if [[ -n "$web_min_hop" || -n "$web_max_hop" ]]; then
        [[ -n "$web_min_hop" && -n "$web_max_hop" ]] || {
          err "随机跳跃必须同时填写最小和最大间隔。"
          return 1
        }
        web_valid_uint "$web_min_hop" 5 600 || { err "最小跳跃间隔无效。"; return 1; }
        web_valid_uint "$web_max_hop" 5 600 || { err "最大跳跃间隔无效。"; return 1; }
        HY2_MIN_HOP_INTERVAL=$((10#$web_min_hop))
        HY2_MAX_HOP_INTERVAL=$((10#$web_max_hop))
        (( HY2_MAX_HOP_INTERVAL >= HY2_MIN_HOP_INTERVAL )) || {
          err "最大跳跃间隔不能小于最小跳跃间隔。"
          return 1
        }
        HY2_HOP_INTERVAL=""
      else
        web_valid_uint "$web_fixed_hop" 5 600 || { err "固定跳跃间隔无效。"; return 1; }
        HY2_HOP_INTERVAL=$((10#$web_fixed_hop))
        HY2_MIN_HOP_INTERVAL=""; HY2_MAX_HOP_INTERVAL=""
      fi
      ;;
    *)
      err "HY2 端口模式只能是 single 或 hop。"
      return 1
      ;;
  esac

  if [[ -s /etc/default/dual-protocol-script-fix ]]; then
    panel_port="$(sed -n 's/^WEB_PORT=//p' /etc/default/dual-protocol-script-fix | head -n 1)"
  fi
  if [[ "$panel_port" =~ ^[0-9]+$ ]]; then
    if [[ "$HY2_PORT_MODE" == "single" && "$HY2_PORT" -eq "$panel_port" ]] ||
       [[ "$HY2_PORT_MODE" == "hop" && "$panel_port" -ge "$HY2_FIRST_PORT" && "$panel_port" -le "$HY2_END_PORT" ]]; then
      err "HY2 端口/跳跃范围不能包含当前 Web TLS 端口 $panel_port。"
      return 1
    fi
  fi

  case "$DPS_WEB_HY2_OBFS_TYPE" in
    none)
      HY2_OBFS_TYPE="none"
      HY2_OBFS_PASSWORD=""
      HY2_GECKO_MIN_PACKET_SIZE="512"
      HY2_GECKO_MAX_PACKET_SIZE="1200"
      ;;
    gecko)
      web_valid_secret "$DPS_WEB_HY2_OBFS_PASSWORD" 1 256 || {
        err "Gecko 混淆密码长度必须为 1-256，且不能包含控制字符。"
        return 1
      }
      web_valid_uint "$DPS_WEB_HY2_GECKO_MIN_PACKET_SIZE" 64 1500 || {
        err "Gecko 最小分片必须在 64-1500。"
        return 1
      }
      web_valid_uint "$DPS_WEB_HY2_GECKO_MAX_PACKET_SIZE" 64 1500 || {
        err "Gecko 最大分片必须在 64-1500。"
        return 1
      }
      HY2_GECKO_MIN_PACKET_SIZE=$((10#$DPS_WEB_HY2_GECKO_MIN_PACKET_SIZE))
      HY2_GECKO_MAX_PACKET_SIZE=$((10#$DPS_WEB_HY2_GECKO_MAX_PACKET_SIZE))
      (( HY2_GECKO_MAX_PACKET_SIZE >= HY2_GECKO_MIN_PACKET_SIZE )) || {
        err "Gecko 最大分片不能小于最小分片。"
        return 1
      }
      HY2_OBFS_TYPE="gecko"
      HY2_OBFS_PASSWORD="$DPS_WEB_HY2_OBFS_PASSWORD"
      ;;
    *)
      return 1
      ;;
  esac

  case "$MAIN_MODE" in
    xhttp)
      normalized="$(normalize_domain "$DPS_WEB_XHTTP_DOMAIN")"
      valid_domain "$normalized" || { err "XHTTP 域名格式无效。"; return 1; }
      [[ "$normalized" != "$HY2_DOMAIN" ]] || { err "XHTTP 与 HY2 必须使用不同域名。"; return 1; }
      XHTTP_DOMAIN="$normalized"
      XHTTP_PATH="$(normalize_path "$DPS_WEB_XHTTP_PATH")" || { err "XHTTP 路径格式无效。"; return 1; }
      web_valid_uuid "$DPS_WEB_XHTTP_UUID" || { err "XHTTP UUID 格式无效。"; return 1; }
      web_valid_uint "$DPS_WEB_XHTTP_BACKEND_PORT" 1024 65535 || { err "XHTTP 后端端口无效。"; return 1; }
      XHTTP_UUID="${DPS_WEB_XHTTP_UUID,,}"
      XHTTP_BACKEND_PORT=$((10#$DPS_WEB_XHTTP_BACKEND_PORT))
      MAIN_DOMAIN="$XHTTP_DOMAIN"
      MANAGED_DOMAINS="$XHTTP_DOMAIN $HY2_DOMAIN"
      ;;
    reality)
      normalized="$(normalize_host_address "$DPS_WEB_REALITY_ADDRESS")"
      valid_host_address "$normalized" || { err "REALITY 连接地址格式无效。"; return 1; }
      REALITY_ADDRESS="$normalized"
      normalized="$(normalize_domain "$DPS_WEB_REALITY_SNI")"
      valid_domain "$normalized" || { err "REALITY 目标 SNI 格式无效。"; return 1; }
      REALITY_SNI="$normalized"
      web_valid_uuid "$DPS_WEB_REALITY_UUID" || { err "REALITY UUID 格式无效。"; return 1; }
      REALITY_UUID="${DPS_WEB_REALITY_UUID,,}"
      [[ "$DPS_WEB_ROTATE_REALITY_KEYS" == "0" || "$DPS_WEB_ROTATE_REALITY_KEYS" == "1" ]] || {
        err "REALITY 密钥轮换参数无效。"
        return 1
      }
      if [[ "$DPS_WEB_ROTATE_REALITY_KEYS" == "1" ]]; then
        generate_reality_x25519_keys || return 1
        REALITY_SHORT_ID="$(generate_shortid)"
      else
        ensure_reality_keys_present || return 1
      fi
      web_valid_secret "$DPS_WEB_HY2_ACME_EMAIL" 3 254 || { err "ACME 邮箱格式无效。"; return 1; }
      [[ "$DPS_WEB_HY2_ACME_EMAIL" =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]] || {
        err "ACME 邮箱格式无效。"
        return 1
      }
      HY2_ACME_EMAIL="$DPS_WEB_HY2_ACME_EMAIL"
      MAIN_DOMAIN="$REALITY_ADDRESS"
      MANAGED_DOMAINS="$HY2_DOMAIN"
      ;;
    *)
      err "当前节点组合不受 Web 配置器支持：${MAIN_MODE:-unknown}"
      return 1
      ;;
  esac
}

web_apply_config() {
  config_lock
  for config_path in "$STATE_FILE" "$XRAY_CONFIG" "$HY2_CONFIG"; do
    python3 "$STORAGE_HELPER" recover "$config_path" || return 1
  done
  if [[ -e "$CADDYFILE" ]]; then python3 "$STORAGE_HELPER" recover "$CADDYFILE" || return 1; fi
  load_state || { err "节点尚未安装，无法保存配置。"; return 1; }
  web_require_apply_environment || return 1

  local old_hy2_domain old_xhttp_domain
  old_hy2_domain="$(normalize_domain "$HY2_DOMAIN")"
  old_xhttp_domain="$(normalize_domain "${XHTTP_DOMAIN:-}")"

  WEB_APPLY_COMMITTED=0
  python3 "$STORAGE_HELPER" checkpoint "$STATE_FILE"
  WEB_BACKUP_DIR="$(mktemp -d /tmp/dual-protocol-web-config.XXXXXX)"
  chmod 700 "$WEB_BACKUP_DIR"
  web_backup_path "$STATE_FILE" state
  web_backup_path "$XRAY_CONFIG" xray
  web_backup_path "$HY2_CONFIG" hy2
  web_backup_path "$CADDYFILE" caddy
  trap web_apply_exit EXIT
  trap 'exit 143' HUP INT TERM

  web_validate_and_assign_config
  case "$MAIN_MODE" in
    xhttp)
      write_xhttp_xray_config
      write_caddyfile_xhttp_hy2
      start_xray
      web_start_caddy_if_changed
      if [[ "$XHTTP_DOMAIN" != "$old_xhttp_domain" ]]; then
        web_wait_for_caddy_cert_only "$XHTTP_DOMAIN"
      fi
      if [[ "$HY2_DOMAIN" != "$old_hy2_domain" ]]; then
        web_wait_for_caddy_cert_only "$HY2_DOMAIN"
        wait_for_caddy_cert "$HY2_DOMAIN"
        grant_cert_read_permissions "$HY2_CERT_PATH" "$HY2_KEY_PATH"
      else
        log "HY2 域名未变化，复用 Caddy 已有证书，不重新申请。"
      fi
      write_hy2_config_tls
      start_hy2
      ;;
    reality)
      write_reality_xray_config
      write_hy2_config_acme
      start_xray
      reality_local_self_test
      start_hy2
      if [[ "$HY2_DOMAIN" != "$old_hy2_domain" ]]; then
        web_wait_for_hy2_acme_cert "$HY2_DOMAIN"
      else
        log "HY2 域名未变化，复用原 ACME 存储，保留自动续期，不强制重新申请。"
      fi
      ;;
  esac

  save_state
  generate_main_outputs
  WEB_APPLY_COMMITTED=1
  trap - EXIT HUP INT TERM
  rm -rf -- "$WEB_BACKUP_DIR"
  WEB_BACKUP_DIR=""
  web_cleanup_replaced_domains "$old_xhttp_domain" "$old_hy2_domain" || warn "节点已经更新，但部分旧域名证书缓存清理失败。"
  ok "节点配置已保存，Xray/Hysteria2/Caddy 已按当前组合完成健康检查。"
}

web_prepare_reinstall_target() {
  case "${DPS_WEB_TARGET_MODE:-}" in
    xhttp)
      MAIN_MODE="xhttp"; STACK_MODE="xhttp_hy2"
      CADDY_REQUIRED="1"; WEB_ENABLED="1"; HY2_CERT_SOURCE="caddy"
      ;;
    reality)
      MAIN_MODE="reality"; STACK_MODE="reality_hy2"
      CADDY_REQUIRED="0"; WEB_ENABLED="0"; HY2_CERT_SOURCE="acme"
      ;;
    *)
      err "网页重装目标只能是 xhttp 或 reality。"
      return 1
      ;;
  esac
}

web_require_tcp_port_free() {
  local port="$1" purpose="$2"
  if port_in_use_tcp "$port"; then
    err "TCP ${port} 已被其他进程占用，无法重装 ${purpose}。"
    show_port_users
    return 1
  fi
}

web_reinstall_stack() {
  config_lock
  for config_path in "$STATE_FILE" "$XRAY_CONFIG" "$HY2_CONFIG"; do
    python3 "$STORAGE_HELPER" recover "$config_path" || return 1
  done
  if [[ -e "$CADDYFILE" ]]; then python3 "$STORAGE_HELPER" recover "$CADDYFILE" || return 1; fi
  load_state || { err "节点尚未安装，无法从网页重装组合。"; return 1; }
  web_require_apply_environment || return 1
  [[ -n "${DPS_WEB_TARGET_MODE:-}" ]] || { err "缺少网页重装目标。"; return 1; }

  local old_hy2_domain="$HY2_DOMAIN" old_xhttp_domain="${XHTTP_DOMAIN:-}"
  WEB_APPLY_COMMITTED=0
  python3 "$STORAGE_HELPER" checkpoint "$STATE_FILE"
  WEB_BACKUP_DIR="$(mktemp -d /tmp/dual-protocol-reinstall.XXXXXX)"
  chmod 700 "$WEB_BACKUP_DIR"
  web_backup_path "$STATE_FILE" state
  web_backup_path "$XRAY_CONFIG" xray
  web_backup_path "$HY2_CONFIG" hy2
  web_backup_path "$CADDYFILE" caddy
  trap web_apply_exit EXIT
  trap 'exit 143' HUP INT TERM

  web_prepare_reinstall_target
  web_validate_and_assign_config
  systemctl stop xray hysteria-server caddy >/dev/null 2>&1 || true

  case "$MAIN_MODE" in
    xhttp)
      web_require_tcp_port_free 80 "XHTTP + HY2"
      web_require_tcp_port_free 443 "XHTTP + HY2"
      install_base_packages
      [[ -x "$CADDY_BIN" ]] || install_caddy_official
      [[ -x "$XRAY_BIN" ]] || install_xray_official
      [[ -x "$HY2_BIN" ]] || install_hysteria_official
      create_speedtest_site
      write_xhttp_xray_config
      write_caddyfile_xhttp_hy2
      start_xray
      start_caddy
      web_wait_for_caddy_cert_only "$XHTTP_DOMAIN"
      web_wait_for_caddy_cert_only "$HY2_DOMAIN"
      wait_for_caddy_cert "$HY2_DOMAIN"
      grant_cert_read_permissions "$HY2_CERT_PATH" "$HY2_KEY_PATH"
      write_hy2_config_tls
      start_hy2
      ;;
    reality)
      web_require_tcp_port_free 80 "REALITY + HY2 的 ACME"
      web_require_tcp_port_free 443 "REALITY"
      install_base_packages
      [[ -x "$XRAY_BIN" ]] || install_xray_official
      [[ -x "$HY2_BIN" ]] || install_hysteria_official
      write_reality_xray_config
      write_hy2_config_acme
      systemctl disable caddy >/dev/null 2>&1 || true
      start_xray
      reality_local_self_test
      start_hy2
      web_wait_for_hy2_acme_cert "$HY2_DOMAIN"
      ;;
  esac


  save_state
  generate_main_outputs
  WEB_APPLY_COMMITTED=1
  trap - EXIT HUP INT TERM
  rm -rf -- "$WEB_BACKUP_DIR"
  WEB_BACKUP_DIR=""
  web_cleanup_replaced_domains "$old_xhttp_domain" "$old_hy2_domain" || warn "协议组合已经重装，但部分旧域名证书缓存清理失败。"
  if [[ "$MAIN_MODE" == "reality" ]]; then
    apt-get purge -y caddy >/dev/null 2>&1 || true
    rm -f /usr/bin/caddy
    rm -rf /etc/caddy
    rm -rf /var/lib/caddy
  fi
  ok "协议组合已从网页重装完成，节点分享链接已重新生成。"
}


