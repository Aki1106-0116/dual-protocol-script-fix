#!/usr/bin/env bash
# Sourced by node-installer.sh; functions have no independent entry point.
install_caddy_official() {
  log "安装/升级 Caddy 官方包"
  install -d -m 0755 /usr/share/keyrings
  rm -f /usr/share/keyrings/caddy-stable-archive-keyring.gpg /etc/apt/sources.list.d/caddy-stable.list
  curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' > /etc/apt/sources.list.d/caddy-stable.list
  chmod 0644 /usr/share/keyrings/caddy-stable-archive-keyring.gpg /etc/apt/sources.list.d/caddy-stable.list
  apt-get update
  apt-get install -y caddy
  systemctl disable --now caddy >/dev/null 2>&1 || true
  ok "Caddy 安装完成：$(caddy version 2>/dev/null || echo unknown)"
}

install_xray_official() {
  log "安装/升级 Xray 官方最新版"
  bash -c "$(curl -fsSL https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install
  [[ -x "$XRAY_BIN" ]] || { err "Xray 安装失败，未找到 $XRAY_BIN"; exit 1; }
  systemctl disable --now xray >/dev/null 2>&1 || true
  ok "Xray 安装完成：$($XRAY_BIN version | head -n 1)"
}


generate_reality_x25519_keys() {
  local output new_private new_public

  if [[ ! -x "${DPS_BIN:-/usr/local/bin/dual-protocol-script-fix}" ]]; then
    err "未找到 REALITY 安全密钥助手：${DPS_BIN:-/usr/local/bin/dual-protocol-script-fix}"
    return 1
  fi
  if [[ ! -x "$XRAY_BIN" ]]; then
    err "未找到可执行的 Xray：$XRAY_BIN"
    return 1
  fi

  if ! output="$("${DPS_BIN:-/usr/local/bin/dual-protocol-script-fix}" keypair --xray "$XRAY_BIN" 2>&1)"; then
    err "REALITY X25519 密钥生成或配对校验失败；旧密钥和旧配置均未修改。"
    printf '%s
' "$output" >&2
    return 1
  fi
  new_private="$(printf '%s
' "$output" | sed -n 's/^PRIVATE=//p' | head -n 1)"
  new_public="$(printf '%s
' "$output" | sed -n 's/^PUBLIC=//p' | head -n 1)"
  if [[ -z "$new_private" || -z "$new_public" ]]; then
    err "密钥助手未返回完整密钥对；旧密钥和旧配置均未修改。"
    return 1
  fi

  # Only commit to the live shell state after both keys passed independent
  # X25519 derivation verification in dual-protocol-script-fix.
  REALITY_PRIVATE_KEY="$new_private"
  REALITY_PUBLIC_KEY="$new_public"
  ok "REALITY X25519 密钥已生成并验证配对关系。"
}

ensure_reality_keys_present() {
  if [[ -z "${REALITY_PRIVATE_KEY:-}" || -z "${REALITY_PUBLIC_KEY:-}" ]]; then
    warn "检测到 REALITY 密钥不完整，尝试安全重建。"
    generate_reality_x25519_keys || return 1
  fi
  if [[ -z "${REALITY_SHORT_ID:-}" ]]; then
    REALITY_SHORT_ID="$(generate_shortid)"
  fi
  if [[ -z "${REALITY_PRIVATE_KEY:-}" || -z "${REALITY_PUBLIC_KEY:-}" || -z "${REALITY_SHORT_ID:-}" ]]; then
    err "REALITY 密钥仍不完整；拒绝生成配置。"
    return 1
  fi
}

install_hysteria_official() {
  log "安装/升级 Hysteria2 官方最新版"
  bash <(curl -fsSL https://get.hy2.sh/)
  [[ -x "$HY2_BIN" ]] || { err "Hysteria2 安装失败，未找到 $HY2_BIN"; exit 1; }
  systemctl disable --now hysteria-server >/dev/null 2>&1 || true
  ok "Hysteria2 安装完成：$($HY2_BIN version 2>/dev/null | head -n 1 || echo installed)"
}

ensure_hysteria_capabilities() {
  local override_dir="/etc/systemd/system/hysteria-server.service.d"
  local override_file="${override_dir}/jb-combo-capabilities.conf"
  local caps=()
  # ACME HTTP-01 需要绑定 TCP/80；端口跳跃需要修改 nftables/iptables。
  [[ "${HY2_CERT_SOURCE:-}" == "acme" ]] && caps+=(CAP_NET_BIND_SERVICE)
  if [[ "${HY2_PORT_MODE:-}" == "hop" ]]; then
    caps+=(CAP_NET_ADMIN CAP_NET_RAW)
  fi
  if [[ "${HY2_PORT:-65535}" =~ ^[0-9]+$ ]] && (( HY2_PORT < 1024 )); then
    caps+=(CAP_NET_BIND_SERVICE)
  fi

  if ((${#caps[@]} == 0)); then
    rm -f "$override_file" 2>/dev/null || true
    return 0
  fi

  # 去重，避免 CapabilityBoundingSet 出现重复项。
  local unique=() cap seen
  for cap in "${caps[@]}"; do
    seen=0
    for existing in "${unique[@]:-}"; do
      [[ "$existing" == "$cap" ]] && { seen=1; break; }
    done
    (( seen == 0 )) && unique+=("$cap")
  done

  mkdir -p "$override_dir"
  cat > "$override_file" <<EOF
[Service]
AmbientCapabilities=${unique[*]}
CapabilityBoundingSet=${unique[*]}
NoNewPrivileges=false
EOF
}

create_speedtest_site() {
  log "生成 speedtest 伪装主页"
  mkdir -p "$WEB_ROOT/assets" "$WEB_ROOT/files" "$HY2_WEB_ROOT"
  cat > "$WEB_ROOT/index.html" <<'HTML'
<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Private Network Speed Test</title><link rel="stylesheet" href="/assets/app.css"><link rel="icon" href="/favicon.ico"></head><body><main class="wrap"><section class="hero"><p class="eyebrow">Private Network Diagnostics</p><h1>Speed Test</h1><p class="sub">Measure connectivity to this private edge endpoint. This page is intended for internal network verification.</p><button id="start">Start test</button></section><section class="grid"><article class="card"><span class="label">Latency</span><strong id="latency">—</strong><small>HTTP round-trip</small></article><article class="card"><span class="label">Download</span><strong id="download">—</strong><small>sample object</small></article><article class="card"><span class="label">Endpoint</span><strong id="endpoint">Online</strong><small>status check</small></article></section><section class="panel"><h2>About this endpoint</h2><p>This service provides a simple private connectivity check for authorized network users. Test results may vary with routing, congestion, and client location.</p><pre id="log">Ready.</pre></section></main><script src="/assets/app.js"></script></body></html>
HTML
  cat > "$WEB_ROOT/404.html" <<'HTML'
<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Not Found</title><link rel="stylesheet" href="/assets/app.css"></head><body><main class="wrap"><section class="hero"><p class="eyebrow">Private Network Diagnostics</p><h1>404</h1><p class="sub">The requested diagnostic resource was not found.</p><a class="button" href="/">Back to status page</a></section></main></body></html>
HTML
  cat > "$WEB_ROOT/assets/app.css" <<'CSS'
:root{color-scheme:light dark;font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}*{box-sizing:border-box}body{margin:0;min-height:100vh;background:radial-gradient(circle at top left,rgba(120,120,120,.18),transparent 28%),#101216;color:#f6f7f9}.wrap{width:min(980px,calc(100% - 32px));margin:0 auto;padding:56px 0}.hero{padding:36px;border:1px solid rgba(255,255,255,.10);border-radius:28px;background:rgba(255,255,255,.06);box-shadow:0 24px 80px rgba(0,0,0,.28)}.eyebrow{margin:0 0 10px;letter-spacing:.14em;text-transform:uppercase;font-size:12px;color:#b9c0ca}h1{margin:0;font-size:clamp(44px,7vw,86px);line-height:.95}.sub{max-width:680px;color:#c8ced8;font-size:18px;line-height:1.6}button,.button{display:inline-block;margin-top:14px;border:0;border-radius:999px;padding:13px 20px;background:#f7f7f8;color:#101216;font-weight:700;text-decoration:none;cursor:pointer}.grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px;margin:18px 0}.card,.panel{border:1px solid rgba(255,255,255,.10);border-radius:24px;background:rgba(255,255,255,.055);padding:24px}.label{display:block;color:#aeb7c4;font-size:13px;text-transform:uppercase;letter-spacing:.12em}strong{display:block;margin:12px 0 6px;font-size:30px}small,.panel p{color:#c8ced8}pre{overflow:auto;background:rgba(0,0,0,.25);border-radius:16px;padding:16px;color:#dce3ee}@media(max-width:760px){.grid{grid-template-columns:1fr}.hero{padding:26px}}
CSS
  cat > "$WEB_ROOT/assets/app.js" <<'JS'
const $=id=>document.getElementById(id);function line(t){$('log').textContent+=`\n${new Date().toLocaleTimeString()}  ${t}`}async function latencyTest(){const s=[];for(let i=0;i<5;i++){const t0=performance.now();await fetch(`/assets/ping.txt?ts=${Date.now()}-${i}`,{cache:'no-store'});s.push(performance.now()-t0)}s.sort((a,b)=>a-b);return s[Math.floor(s.length/2)]}async function downloadTest(){const t0=performance.now();const r=await fetch(`/files/1mb.bin?ts=${Date.now()}`,{cache:'no-store'});const b=await r.blob();return b.size*8/((performance.now()-t0)/1000)/1000/1000}$('start').addEventListener('click',async()=>{$('log').textContent='Running diagnostics...';$('start').disabled=true;try{line('Latency test started');const ms=await latencyTest();$('latency').textContent=`${ms.toFixed(0)} ms`;line(`Median latency: ${ms.toFixed(1)} ms`);line('Download test started');const mbps=await downloadTest();$('download').textContent=`${mbps.toFixed(1)} Mbps`;line(`Estimated download: ${mbps.toFixed(2)} Mbps`);$('endpoint').textContent='Healthy';line('Diagnostics complete')}catch(e){$('endpoint').textContent='Error';line(`Error: ${e.message}`)}finally{$('start').disabled=false}});
JS
  printf 'pong\n' > "$WEB_ROOT/assets/ping.txt"
  printf 'User-agent: *\nDisallow: /internal/\n' > "$WEB_ROOT/robots.txt"
  printf '\000\000\001\000\001\000\020\020\000\000\001\000\040\000\000\000\000\000\026\000\000\000' > "$WEB_ROOT/favicon.ico"
  [[ -f "$WEB_ROOT/files/1mb.bin" ]] || dd if=/dev/urandom of="$WEB_ROOT/files/1mb.bin" bs=1M count=1 status=none
  cat > "$HY2_WEB_ROOT/index.html" <<'HTML'
<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Speedtest</title><style>body{margin:0;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#111827;color:#f9fafb;display:grid;min-height:100vh;place-items:center}.card{max-width:680px;margin:24px;padding:40px;border:1px solid rgba(255,255,255,.12);border-radius:28px;background:rgba(255,255,255,.06);box-shadow:0 20px 80px rgba(0,0,0,.35)}h1{font-size:42px;margin:0 0 12px}p{line-height:1.7;color:#d1d5db}</style></head><body><main class="card"><h1>Speedtest 新站建设中</h1><p>该站点正在部署网络诊断与边缘测速功能。请稍后再访问。</p></main></body></html>
HTML
  cat > "$HY2_WEB_ROOT/404.html" <<'HTML'
<!doctype html><html><head><meta charset="utf-8"><title>Not Found</title></head><body><h1>404 Not Found</h1></body></html>
HTML
  chown -R caddy:caddy "$WEB_ROOT" "$HY2_WEB_ROOT" 2>/dev/null || true
  chmod -R u=rwX,go=rX "$WEB_ROOT" "$HY2_WEB_ROOT"
}

hy2_masquerade_html() {
  cat <<'HTML'
<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Speedtest</title></head>
<body style="margin:0;font-family:system-ui,-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:#111827;color:#f9fafb;display:grid;min-height:100vh;place-items:center">
<main style="max-width:680px;margin:24px;padding:40px;border:1px solid rgba(255,255,255,.12);border-radius:28px;background:rgba(255,255,255,.06);box-shadow:0 20px 80px rgba(0,0,0,.35)">
<h1 style="font-size:42px;margin:0 0 12px">Speedtest 新站建设中</h1>
<p style="line-height:1.7;color:#d1d5db">该站点正在部署网络诊断与边缘测速功能。请稍后再访问。</p>
</main>
</body>
</html>
HTML
}

collect_fp() {
  echo "uTLS/客户端指纹：1) chrome  2) firefox" >&2
  read -r -p "请选择 [默认 1]: " c
  case "${c:-1}" in 2) FP="firefox" ;; *) FP="chrome" ;; esac
}

collect_hy2_port() {
  ensure_port_tools
  echo
  echo "Hysteria2 端口模式：" >&2
  echo "  1) 端口跳跃 Port Hopping（默认，推荐）" >&2
  echo "  2) 单端口" >&2
  read -r -p "请选择 [默认 1]: " pm
  if [[ "${pm:-1}" == "2" ]]; then
    HY2_PORT_MODE="single"
    while true; do
      read -r -p "请输入 Hysteria2 UDP 端口 [回车随机 2000-65535，禁止 80/443/2053/8443]: " p
      [[ -z "$p" ]] && p="$(shuf -i 2000-65535 -n 1)"
      [[ "$p" =~ ^[0-9]+$ ]] || { warn "端口必须是数字。"; continue; }
      p=$((10#$p))
      (( p >= 1 && p <= 65535 )) || { warn "端口范围必须是 1-65535。"; continue; }
      hy2_reserved_port "$p" && { warn "端口 $p 被脚本禁止使用。"; continue; }
      port_in_use_udp "$p" && { warn "UDP $p 已被占用。"; continue; }
      HY2_PORT="$p"; HY2_FIRST_PORT=""; HY2_END_PORT=""; HY2_HOP_INTERVAL=""; HY2_MIN_HOP_INTERVAL=""; HY2_MAX_HOP_INTERVAL=""
      break
    done
  else
    HY2_PORT_MODE="hop"
    while true; do
      read -r -p "请输入起始/主端口 [默认随机 2500-10000]: " fp
      [[ -z "$fp" ]] && fp="$(shuf -i 2500-10000 -n 1)"
      [[ "$fp" =~ ^[0-9]+$ ]] || { warn "端口必须是数字。"; continue; }
      fp=$((10#$fp))
      (( fp >= 1 && fp < 65535 )) || { warn "起始端口必须是 1-65534。"; continue; }
      hy2_reserved_port "$fp" && { warn "端口 $fp 被脚本禁止使用。"; continue; }
      port_in_use_udp "$fp" && { warn "UDP $fp 已被占用。"; continue; }
      break
    done
    while true; do
      local def_end=$((fp+75)); (( def_end > 65535 )) && def_end=65535
      read -r -p "请输入结束端口 [默认 $def_end]: " ep
      ep="${ep:-$def_end}"
      [[ "$ep" =~ ^[0-9]+$ ]] || { warn "端口必须是数字。"; continue; }
      ep=$((10#$ep))
      (( ep > fp && ep <= 65535 )) || { warn "结束端口必须大于起始端口且不超过 65535。"; continue; }
      hy2_range_contains_reserved "$fp" "$ep" && { warn "端口范围 $fp-$ep 包含 80/443/2053/8443，禁止使用。"; continue; }
      local conflict=0 p
      for p in $(seq "$fp" "$ep"); do
        if port_in_use_udp "$p"; then conflict=1; warn "UDP $p 已被占用。"; break; fi
      done
      (( conflict == 0 )) || continue
      break
    done
    HY2_PORT="$fp"; HY2_FIRST_PORT="$fp"; HY2_END_PORT="$ep"
    echo "端口跳跃间隔：1) 固定 25s（默认）  2) 随机 15-25s" >&2
    read -r -p "请选择 [默认 1]: " hi
    if [[ "${hi:-1}" == "2" ]]; then
      HY2_HOP_INTERVAL=""; HY2_MIN_HOP_INTERVAL="15"; HY2_MAX_HOP_INTERVAL="25"
    else
      HY2_HOP_INTERVAL="25"; HY2_MIN_HOP_INTERVAL=""; HY2_MAX_HOP_INTERVAL=""
    fi
  fi
}

collect_hy2_obfs() {
  echo >&2
  echo "Hysteria2 混淆设置：" >&2
  echo "  1) 不启用混淆（默认，推荐）" >&2
  echo "  2) 启用 Gecko 混淆（实验性；使用默认分片 512-1200 字节）" >&2
  read -r -p "请选择 [默认 1]: " oc
  case "${oc:-1}" in
    2)
      HY2_OBFS_TYPE="gecko"
      read -r -p "Gecko 混淆密码 [回车自动生成]: " op
      HY2_OBFS_PASSWORD="${op:-$(generate_password)}"
      HY2_GECKO_MIN_PACKET_SIZE="512"
      HY2_GECKO_MAX_PACKET_SIZE="1200"
      ;;
    *)
      HY2_OBFS_TYPE="none"
      HY2_OBFS_PASSWORD=""
      HY2_GECKO_MIN_PACKET_SIZE="512"
      HY2_GECKO_MAX_PACKET_SIZE="1200"
      ;;
  esac
}

collect_hy2_common() {
  read -r -p "Hysteria2 密码 [回车自动生成]: " hp
  HY2_PASSWORD="${hp:-$(generate_password)}"
  collect_hy2_port
  collect_hy2_obfs
}

find_caddy_cert_pair() {
  local domain="$1" cert key base
  base="${CADDY_DATA}/certificates"
  [[ -d "$base" ]] || return 1
  cert="$(find "$base" -type f -name "${domain}.crt" 2>/dev/null | sort | tail -n 1 || true)"
  key="$(find "$base" -type f -name "${domain}.key" 2>/dev/null | sort | tail -n 1 || true)"
  if [[ -n "$cert" && -n "$key" && -s "$cert" && -s "$key" ]]; then
    echo "$cert|$key"
    return 0
  fi
  return 1
}

wait_for_caddy_cert() {
  local domain="$1" pair="" i
  log "等待 Caddy 为 ${domain} 签发/加载证书"
  for i in $(seq 1 60); do
    pair="$(find_caddy_cert_pair "$domain" || true)"
    if [[ -n "$pair" ]]; then
      HY2_CERT_PATH="${pair%%|*}"
      HY2_KEY_PATH="${pair#*|}"
      ok "找到证书：$HY2_CERT_PATH"
      return 0
    fi
    sleep 2
  done
  err "未能在 Caddy 存储中找到 ${domain} 的证书。请确认域名直连本机、TCP 80/443 放行，且 CDN 暂时关闭。"
  journalctl -u caddy --no-pager -n 100 >&2 || true
  exit 1
}

grant_cert_read_permissions() {
  local cert="$1" key="$2" user="hysteria" configured_user path dir cur part
  [[ -f "$cert" && -f "$key" ]] || return 1
  configured_user="$(systemctl show hysteria-server -p User --value 2>/dev/null || true)"
  [[ "$configured_user" == "root" ]] && return 0
  [[ -n "$configured_user" ]] && user="$configured_user"
  if ! id "$user" >/dev/null 2>&1 || ! cmd_exists setfacl; then
    err "无法为 Hysteria 服务用户 $user 设置证书 ACL；拒绝把私钥设为全局可读。"
    return 1
  fi
  for path in "$cert" "$key"; do
    dir="$(dirname "$(readlink -f "$path")")"; cur=""
    local -a parts
    IFS='/' read -ra parts <<< "$dir"
    for part in "${parts[@]}"; do
      [[ -z "$part" ]] && continue
      cur="$cur/$part"
      setfacl -m "u:${user}:--x" "$cur" || return 1
    done
  done
  setfacl -m "u:${user}:r" "$(readlink -f "$cert")" "$(readlink -f "$key")" || return 1
  setfacl -d -m "u:${user}:r" "$(dirname "$(readlink -f "$cert")")" || return 1
  setfacl -d -m "u:${user}:r" "$(dirname "$(readlink -f "$key")")" || return 1
}

write_xhttp_xray_config() {
  mkdir -p "$(dirname "$XRAY_CONFIG")"

  local tmp rollback="${XRAY_CONFIG}.dps-last-good"
  tmp="$(mktemp --suffix=.json "${XRAY_CONFIG}.tmp.XXXXXX")"

  # Use the documented compatibility-stable fields. Xray versions that silently
  # ignore unknown settings.users would otherwise accept an inbound with no users.
  cat > "$tmp" <<JSON
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    {
      "tag": "vless-xhttp-in",
      "listen": "127.0.0.1",
      "port": ${XHTTP_BACKEND_PORT},
      "protocol": "vless",
      "settings": {
        "clients": [ { "id": "${XHTTP_UUID}", "email": "xhttp@${XHTTP_DOMAIN}" } ],
        "decryption": "none"
      },
      "streamSettings": {
        "network": "xhttp",
        "security": "none",
        "xhttpSettings": {
          "path": "${XHTTP_PATH}",
          "mode": "auto",
          "extra": { "xPaddingBytes": "100-1000" }
        }
      }
    }
  ],
  "outbounds": [ { "tag": "direct", "protocol": "freedom" } ]
}
JSON

  if ! "$XRAY_BIN" run -test -config "$tmp" -format json >/tmp/xray-test.log 2>&1; then
    warn "当前 Xray 不接受新版 XHTTP 字段，正在验证兼容字段。"
    cat > "$tmp" <<JSON
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    {
      "tag": "vless-xhttp-in",
      "listen": "127.0.0.1",
      "port": ${XHTTP_BACKEND_PORT},
      "protocol": "vless",
      "settings": {
        "clients": [ { "id": "${XHTTP_UUID}", "email": "xhttp@${XHTTP_DOMAIN}" } ],
        "decryption": "none"
      },
      "streamSettings": {
        "network": "xhttp",
        "security": "none",
        "xhttpSettings": {
          "path": "${XHTTP_PATH}",
          "mode": "auto",
          "extra": { "xPaddingBytes": "100-1000" }
        }
      }
    }
  ],
  "outbounds": [ { "tag": "direct", "protocol": "freedom" } ]
}
JSON
    if ! "$XRAY_BIN" run -test -config "$tmp" -format json >/tmp/xray-test.log 2>&1; then
      err "Xray 同时拒绝新版与兼容版 XHTTP 配置；现有配置未修改。"
      cat /tmp/xray-test.log >&2
      rm -f "$tmp"
      return 1
    fi
  fi

  if [[ -s "$XRAY_CONFIG" ]]; then
    cp -a "$XRAY_CONFIG" "$rollback"
    chmod 600 "$rollback" 2>/dev/null || true
  fi
  if ! python3 "$STORAGE_HELPER" write "$XRAY_CONFIG" "$tmp" 644; then
    err "写入 XHTTP 配置失败；现有配置未修改。"
    rm -f "$tmp"
    return 1
  fi
  rm -f "$tmp"
}

write_reality_xray_config() {
  mkdir -p "$(dirname "$XRAY_CONFIG")"
  ensure_reality_keys_present || return 1

  local tmp rollback="${XRAY_CONFIG}.dps-last-good"
  tmp="$(mktemp --suffix=.json "${XRAY_CONFIG}.tmp.XXXXXX")"
  cat > "$tmp" <<JSON
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    {
      "tag": "vless-reality-vision-in",
      "listen": "0.0.0.0",
      "port": 443,
      "protocol": "vless",
      "settings": {
        "clients": [ { "id": "${REALITY_UUID}", "flow": "xtls-rprx-vision", "email": "reality" } ],
        "decryption": "none"
      },
      "streamSettings": {
        "network": "raw",
        "security": "reality",
        "realitySettings": {
          "show": false,
          "target": "${REALITY_SNI}:443",
          "xver": 0,
          "serverNames": [ "${REALITY_SNI}" ],
          "privateKey": "${REALITY_PRIVATE_KEY}",
          "shortIds": [ "${REALITY_SHORT_ID}" ]
        }
      }
    }
  ],
  "outbounds": [ { "tag": "direct", "protocol": "freedom" } ]
}
JSON

  if ! "$XRAY_BIN" run -test -config "$tmp" -format json >/tmp/xray-test.log 2>&1; then
    warn "当前 Xray 不接受 network=raw/target，正在验证旧别名 tcp/dest。"
    cat > "$tmp" <<JSON
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    {
      "tag": "vless-reality-vision-in",
      "listen": "0.0.0.0",
      "port": 443,
      "protocol": "vless",
      "settings": {
        "clients": [ { "id": "${REALITY_UUID}", "flow": "xtls-rprx-vision", "email": "reality" } ],
        "decryption": "none"
      },
      "streamSettings": {
        "network": "tcp",
        "security": "reality",
        "realitySettings": {
          "show": false,
          "dest": "${REALITY_SNI}:443",
          "xver": 0,
          "serverNames": [ "${REALITY_SNI}" ],
          "privateKey": "${REALITY_PRIVATE_KEY}",
          "shortIds": [ "${REALITY_SHORT_ID}" ]
        }
      }
    }
  ],
  "outbounds": [ { "tag": "direct", "protocol": "freedom" } ]
}
JSON
    if ! "$XRAY_BIN" run -test -config "$tmp" -format json >/tmp/xray-test.log 2>&1; then
      err "Xray 同时拒绝新版与兼容版 REALITY 配置；旧配置未修改。"
      cat /tmp/xray-test.log >&2
      rm -f "$tmp"
      return 1
    fi
  fi
  if [[ -s "$XRAY_CONFIG" ]]; then
    cp -a "$XRAY_CONFIG" "$rollback"
    chmod 600 "$rollback" 2>/dev/null || true
  fi
  if ! python3 "$STORAGE_HELPER" write "$XRAY_CONFIG" "$tmp" 644; then
    err "写入 REALITY 配置失败；旧配置仍保存在 $rollback"
    rm -f "$tmp"
    return 1
  fi
  rm -f "$tmp"
}

reality_local_self_test() {
  local port tmp log_file curl_error http_error https_rc pid="" ready=0 i
  port="$(find_free_local_port)"
  tmp="$(mktemp --suffix=.json /tmp/dps-reality-client.XXXXXX)"
  log_file="/tmp/dps-reality-selftest.log"
  curl_error="$(mktemp /tmp/dps-reality-curl.XXXXXX)"
  http_error="$(mktemp /tmp/dps-reality-http.XXXXXX)"

  cat > "$tmp" <<JSON
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    {
      "listen": "127.0.0.1",
      "port": ${port},
      "protocol": "socks",
      "settings": { "auth": "noauth", "udp": false }
    }
  ],
  "outbounds": [
    {
      "tag": "reality-selftest",
      "protocol": "vless",
      "settings": {
        "vnext": [
          {
            "address": "127.0.0.1",
            "port": 443,
            "users": [
              {
                "id": "${REALITY_UUID}",
                "encryption": "none",
                "flow": "xtls-rprx-vision"
              }
            ]
          }
        ]
      },
      "streamSettings": {
        "network": "raw",
        "security": "reality",
        "realitySettings": {
          "serverName": "${REALITY_SNI}",
          "fingerprint": "${FP}",
          "password": "${REALITY_PUBLIC_KEY}",
          "shortId": "${REALITY_SHORT_ID}",
          "spiderX": "/"
        }
      }
    }
  ]
}
JSON

  if ! "$XRAY_BIN" run -test -config "$tmp" -format json >"$log_file" 2>&1; then
    err "本机 REALITY 客户端自测配置未通过。"
    cat "$log_file" >&2
    rm -f "$tmp" "$curl_error" "$http_error"
    return 1
  fi

  "$XRAY_BIN" run -config "$tmp" -format json >"$log_file" 2>&1 &
  pid=$!
  for i in $(seq 1 40); do
    if ss -H -ltn "sport = :${port}" 2>/dev/null | grep -q .; then
      ready=1
      break
    fi
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.25
  done
  if [[ "$ready" != "1" ]]; then
    err "本机 REALITY 自测客户端没有启动。"
    cat "$log_file" >&2
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -f "$tmp" "$curl_error" "$http_error"
    return 1
  fi

  if curl -sS --output /dev/null --connect-timeout 8 --max-time 20 \
    --socks5-hostname "127.0.0.1:${port}" "https://${REALITY_SNI}/" 2>"$curl_error"; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -f "$tmp" "$curl_error" "$http_error"
    ok "REALITY 本机完整握手与代理请求自测通过。"
    return 0
  else
    https_rc=$?

  # Debian 13 ships OpenSSL 3.5, whose local TLS client can report
  # unexpected EOF after XTLS Vision has already established a working
  # REALITY/VLESS stream. Verify the same stream with plain HTTP before
  # rejecting the node, so this platform-specific TLS false negative does
  if curl -sS --output /dev/null --connect-timeout 8 --max-time 20 \
    --socks5-hostname "127.0.0.1:${port}" "http://example.com/" 2>"$http_error"; then
    if grep -qiF 'unexpected eof while reading' "$curl_error"; then
      warn "本机 HTTPS 探针返回 curl ${https_rc} 的 unexpected EOF，但 REALITY 握手与代理通道已通过 HTTP 探针；跳过 Debian 13/OpenSSL 本地 TLS 假阴性。"
    else
      warn "本机 HTTPS 探针失败，但 REALITY 握手与代理通道已通过 HTTP 探针；继续保存节点。"
    fi
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -f "$tmp" "$curl_error" "$http_error"
    ok "REALITY 本机完整握手与代理通道自测通过。"
    return 0
  fi

    err "本机 REALITY 完整握手失败；不会把该节点标记为可用。"
    if [[ -s "$curl_error" ]]; then
      err "REALITY 自测 curl 错误：$(tr '
' ' ' <"$curl_error" | sed 's/[[:space:]]\+/ /g' | cut -c1-800)"
    fi
    if [[ -s "$http_error" ]]; then
      err "REALITY 自测 HTTP 代理探针错误：$(tr '
' ' ' <"$http_error" | sed 's/[[:space:]]\+/ /g' | cut -c1-800)"
    fi
    if [[ -s "$log_file" ]]; then
      err "REALITY 自测客户端日志（最近 30 行）："
      tail -n 30 "$log_file" >&2
    fi
    journalctl -u xray --no-pager -n 80 -o cat 2>/dev/null \
      | grep -vF 'Special user nobody configured' \
      | tail -n 30 >&2 || true
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -f "$tmp" "$curl_error" "$http_error"
    return 1
  fi
}

write_caddyfile_xhttp_hy2() {
  mkdir -p "$(dirname "$CADDYFILE")"
  backup_file "$CADDYFILE"
  local candidate
  candidate="$(mktemp "${CADDYFILE}.tmp.XXXXXX")"
  cat > "$candidate" <<CADDY
${XHTTP_DOMAIN} {
    encode zstd gzip
    header {
        -Server
        X-Content-Type-Options "nosniff"
        Referrer-Policy "no-referrer-when-downgrade"
    }
    @xhttp path ${XHTTP_PATH} ${XHTTP_PATH}/*
    handle @xhttp {
        reverse_proxy h2c://127.0.0.1:${XHTTP_BACKEND_PORT} {
            flush_interval -1
        }
    }
    handle {
        root * ${WEB_ROOT}
        file_server
    }
    handle_errors {
        root * ${WEB_ROOT}
        rewrite * /404.html
        file_server
    }
}
${HY2_DOMAIN} {
    encode zstd gzip
    header {
        -Server
    }
    root * ${HY2_WEB_ROOT}
    file_server
    handle_errors {
        root * ${HY2_WEB_ROOT}
        rewrite * /404.html
        file_server
    }
}
CADDY
  local validation_log
  validation_log="$(mktemp "${CADDYFILE}.validate.XXXXXX")"
  if ! "$CADDY_BIN" validate --config "$candidate" --adapter caddyfile >"$validation_log" 2>&1; then
    err "新 Caddyfile 校验失败，原配置未修改"
    cat "$validation_log" >&2
    rm -f "$candidate" "$validation_log"
    return 1
  fi
  python3 "$STORAGE_HELPER" write "$CADDYFILE" "$candidate" 644
  rm -f "$candidate" "$validation_log"
}

hy2_listen_value() {
  if [[ "${HY2_PORT_MODE}" == "hop" ]]; then
    echo ":${HY2_FIRST_PORT}-${HY2_END_PORT}"
  else
    echo ":${HY2_PORT}"
  fi
}


hy2_obfs_yaml_block() {
  case "${HY2_OBFS_TYPE:-none}" in
    gecko)
      local passq
      passq="$(yaml_quote "${HY2_OBFS_PASSWORD:-}")"
      cat <<YAML

obfs:
  type: gecko
  gecko:
    password: ${passq}
    minPacketSize: ${HY2_GECKO_MIN_PACKET_SIZE:-512}
    maxPacketSize: ${HY2_GECKO_MAX_PACKET_SIZE:-1200}
YAML
      ;;
    salamander)
      local passq
      passq="$(yaml_quote "${HY2_OBFS_PASSWORD:-}")"
      cat <<YAML

obfs:
  type: salamander
  salamander:
    password: ${passq}
YAML
      ;;
  esac
}

write_hy2_config_tls() {
  mkdir -p "$(dirname "$HY2_CONFIG")"
  local certq keyq pwdq listen obfs_block
  listen="$(hy2_listen_value)"
  certq="$(yaml_quote "$HY2_CERT_PATH")"; keyq="$(yaml_quote "$HY2_KEY_PATH")"; pwdq="$(yaml_quote "$HY2_PASSWORD")"
  obfs_block="$(hy2_obfs_yaml_block)"
  local candidate
  candidate="$(mktemp "${HY2_CONFIG}.tmp.XXXXXX")"
  cat > "$candidate" <<YAML
listen: ${listen}

tls:
  cert: ${certq}
  key: ${keyq}

quic:
  initStreamReceiveWindow: 16777216
  maxStreamReceiveWindow: 16777216
  initConnReceiveWindow: 33554432
  maxConnReceiveWindow: 33554432

auth:
  type: password
  password: ${pwdq}
${obfs_block}

bandwidth:
  up: 100 mbps
  down: 100 mbps

masquerade:
  type: string
  string:
    content: |
$(hy2_masquerade_html | sed 's/^/      /')
    headers:
      content-type: text/html; charset=utf-8
      server: nginx
    statusCode: 200
YAML
  python3 "$STORAGE_HELPER" write "$HY2_CONFIG" "$candidate" 644
  rm -f "$candidate"
}

write_hy2_config_acme() {
  mkdir -p "$(dirname "$HY2_CONFIG")"
  mkdir -p /var/lib/hysteria/acme
  if id hysteria >/dev/null 2>&1; then
    chown -R hysteria:hysteria /var/lib/hysteria/acme
  fi
  chmod 700 /var/lib/hysteria/acme

  local pwdq emailq listen domainq obfs_block
  listen="$(hy2_listen_value)"
  pwdq="$(yaml_quote "$HY2_PASSWORD")"; emailq="$(yaml_quote "$HY2_ACME_EMAIL")"; domainq="$(yaml_quote "$HY2_DOMAIN")"
  obfs_block="$(hy2_obfs_yaml_block)"
  local candidate
  candidate="$(mktemp "${HY2_CONFIG}.tmp.XXXXXX")"
  cat > "$candidate" <<YAML
listen: ${listen}

acme:
  domains:
    - ${domainq}
  email: ${emailq}
  ca: letsencrypt
  dir: /var/lib/hysteria/acme
  type: http
  http:
    altPort: 80

quic:
  initStreamReceiveWindow: 16777216
  maxStreamReceiveWindow: 16777216
  initConnReceiveWindow: 33554432
  maxConnReceiveWindow: 33554432

auth:
  type: password
  password: ${pwdq}
${obfs_block}

bandwidth:
  up: 100 mbps
  down: 100 mbps

masquerade:
  type: string
  string:
    content: |
$(hy2_masquerade_html | sed 's/^/      /')
    headers:
      content-type: text/html; charset=utf-8
      server: nginx
    statusCode: 200
YAML
  python3 "$STORAGE_HELPER" write "$HY2_CONFIG" "$candidate" 644
  rm -f "$candidate"
}

write_service_restart_override() {
  local svc="$1"
  local dir="/etc/systemd/system/${svc}.service.d"
  local file="${dir}/jb-combo-restart.conf"
  mkdir -p "$dir"
  cat > "$file" <<'EOF'
[Unit]
StartLimitIntervalSec=300
StartLimitBurst=10

[Service]
Restart=on-failure
RestartSec=5s
EOF
}

ensure_service_resilience() {
  local svc
  for svc in "$@"; do
    [[ -n "$svc" ]] || continue
    if systemctl list-unit-files "${svc}.service" 2>/dev/null | grep -q "^${svc}\.service" || [[ -f "/etc/systemd/system/${svc}.service" || -f "/lib/systemd/system/${svc}.service" || -f "/usr/lib/systemd/system/${svc}.service" ]]; then
      write_service_restart_override "$svc"
    fi
  done
  systemctl daemon-reload
}

remove_service_resilience_overrides() {
  local svc dir file
  for svc in caddy xray hysteria-server; do
    dir="/etc/systemd/system/${svc}.service.d"
    file="${dir}/jb-combo-restart.conf"
    rm -f "$file" 2>/dev/null || true
    rm -f "${dir}/jb-combo-capabilities.conf" 2>/dev/null || true
    rmdir "$dir" 2>/dev/null || true
  done
}

start_xray() {
  ensure_service_resilience xray
  systemctl enable --now xray
  systemctl restart xray
  sleep 1
  systemctl is-active --quiet xray || { err "Xray 启动失败"; journalctl -u xray --no-pager -n 100; exit 1; }
}

start_caddy() {
  ensure_service_resilience caddy
  systemctl enable --now caddy
  systemctl restart caddy
  sleep 2
  systemctl is-active --quiet caddy || { err "Caddy 启动失败"; journalctl -u caddy --no-pager -n 120; exit 1; }
}

start_hy2() {
  ensure_hysteria_capabilities
  ensure_service_resilience hysteria-server
  systemctl enable --now hysteria-server
  systemctl restart hysteria-server
  sleep 3
  systemctl is-active --quiet hysteria-server || { err "Hysteria2 启动失败"; journalctl -u hysteria-server --no-pager -n 120; exit 1; }
}

generate_hy2_outputs() {
  mkdir -p "$OUT_DIR"
  local server_host server_port_string auth_enc sni_enc link insecure="0" obfs_query="" obfs_pass_enc=""
  server_host="$HY2_DOMAIN"
  if [[ "$HY2_PORT_MODE" == "hop" ]]; then
    server_port_string="${HY2_FIRST_PORT}-${HY2_END_PORT}"
  else
    server_port_string="${HY2_PORT}"
  fi
  auth_enc="$(urlencode "$HY2_PASSWORD")"
  sni_enc="$(urlencode "$HY2_DOMAIN")"
  if [[ "${HY2_OBFS_TYPE:-none}" != "none" && -n "${HY2_OBFS_PASSWORD:-}" ]]; then
    obfs_pass_enc="$(urlencode "$HY2_OBFS_PASSWORD")"
    obfs_query="&obfs=$(urlencode "$HY2_OBFS_TYPE")&obfs-password=${obfs_pass_enc}"
  fi
  if [[ "$HY2_PORT_MODE" == "hop" ]]; then
    if [[ -n "${HY2_MIN_HOP_INTERVAL:-}" && -n "${HY2_MAX_HOP_INTERVAL:-}" ]]; then
      link="hysteria2://${auth_enc}@${server_host}:${HY2_PORT}?security=tls&mport=${HY2_FIRST_PORT}-${HY2_END_PORT}&mportHopInt=${HY2_MIN_HOP_INTERVAL}-${HY2_MAX_HOP_INTERVAL}${obfs_query}&insecure=${insecure}&sni=${sni_enc}#$(urlencode "${HY2_DOMAIN} HY2")"
    else
      link="hysteria2://${auth_enc}@${server_host}:${HY2_PORT}?security=tls&mport=${HY2_FIRST_PORT}-${HY2_END_PORT}&mportHopInt=${HY2_HOP_INTERVAL:-25}${obfs_query}&insecure=${insecure}&sni=${sni_enc}#$(urlencode "${HY2_DOMAIN} HY2")"
    fi
  else
    link="hysteria2://${auth_enc}@${server_host}:${HY2_PORT}?security=tls${obfs_query}&insecure=${insecure}&sni=${sni_enc}#$(urlencode "${HY2_DOMAIN} HY2")"
  fi
  echo "$link" > "${OUT_DIR}/hy2-url.txt"

  cat > "$HY2_CLIENT_YAML" <<YAML
server: '${server_host}:${server_port_string}'
auth: '$(printf "%s" "$HY2_PASSWORD" | sed "s/'/''/g")'
tls:
  sni: '${HY2_DOMAIN}'
  insecure: false
YAML
  if [[ "${HY2_OBFS_TYPE:-none}" == "gecko" && -n "${HY2_OBFS_PASSWORD:-}" ]]; then
    cat >> "$HY2_CLIENT_YAML" <<YAML
obfs:
  type: gecko
  gecko:
    password: '$(printf "%s" "$HY2_OBFS_PASSWORD" | sed "s/'/''/g")'
    minPacketSize: ${HY2_GECKO_MIN_PACKET_SIZE:-512}
    maxPacketSize: ${HY2_GECKO_MAX_PACKET_SIZE:-1200}
YAML
  elif [[ "${HY2_OBFS_TYPE:-none}" == "salamander" && -n "${HY2_OBFS_PASSWORD:-}" ]]; then
    cat >> "$HY2_CLIENT_YAML" <<YAML
obfs:
  type: salamander
  salamander:
    password: '$(printf "%s" "$HY2_OBFS_PASSWORD" | sed "s/'/''/g")'
YAML
  fi
  cat >> "$HY2_CLIENT_YAML" <<YAML
quic:
  initStreamReceiveWindow: 16777216
  maxStreamReceiveWindow: 16777216
  initConnReceiveWindow: 33554432
  maxConnReceiveWindow: 33554432
socks5:
  listen: 127.0.0.1:5678
YAML
  if [[ "$HY2_PORT_MODE" == "hop" ]]; then
    cat >> "$HY2_CLIENT_YAML" <<YAML
transport:
  type: udp
  udp:
YAML
    if [[ -n "${HY2_MIN_HOP_INTERVAL:-}" && -n "${HY2_MAX_HOP_INTERVAL:-}" ]]; then
      cat >> "$HY2_CLIENT_YAML" <<YAML
    minHopInterval: ${HY2_MIN_HOP_INTERVAL}s
    maxHopInterval: ${HY2_MAX_HOP_INTERVAL}s
YAML
    else
      cat >> "$HY2_CLIENT_YAML" <<YAML
    hopInterval: ${HY2_HOP_INTERVAL:-25}s
YAML
    fi
  fi

  HY2_JSON_FILE="$HY2_CLIENT_JSON" \
  HY2_JSON_SERVER="${server_host}:${server_port_string}" \
  HY2_JSON_AUTH="$HY2_PASSWORD" \
  HY2_JSON_SNI="$HY2_DOMAIN" \
  HY2_JSON_PORT_MODE="$HY2_PORT_MODE" \
  HY2_JSON_HOP_INTERVAL="${HY2_HOP_INTERVAL:-25}" \
  HY2_JSON_MIN_HOP_INTERVAL="${HY2_MIN_HOP_INTERVAL:-}" \
  HY2_JSON_MAX_HOP_INTERVAL="${HY2_MAX_HOP_INTERVAL:-}" \
  HY2_JSON_OBFS_TYPE="${HY2_OBFS_TYPE:-none}" \
  HY2_JSON_OBFS_PASSWORD="${HY2_OBFS_PASSWORD:-}" \
  HY2_JSON_GECKO_MIN_PACKET_SIZE="${HY2_GECKO_MIN_PACKET_SIZE:-512}" \
  HY2_JSON_GECKO_MAX_PACKET_SIZE="${HY2_GECKO_MAX_PACKET_SIZE:-1200}" \
  python3 - <<'PY'
import json, os
obj = {
    "server": os.environ["HY2_JSON_SERVER"],
    "auth": os.environ["HY2_JSON_AUTH"],
    "tls": {"sni": os.environ["HY2_JSON_SNI"], "insecure": False},
    "quic": {
        "initStreamReceiveWindow": 16777216,
        "maxStreamReceiveWindow": 16777216,
        "initConnReceiveWindow": 33554432,
        "maxConnReceiveWindow": 33554432,
    },
    "socks5": {"listen": "127.0.0.1:5678"},
}
obfs_type = os.environ.get("HY2_JSON_OBFS_TYPE", "none")
obfs_password = os.environ.get("HY2_JSON_OBFS_PASSWORD", "")
if obfs_type == "gecko" and obfs_password:
    obj["obfs"] = {
        "type": "gecko",
        "gecko": {
            "password": obfs_password,
            "minPacketSize": int(os.environ.get("HY2_JSON_GECKO_MIN_PACKET_SIZE", "512")),
            "maxPacketSize": int(os.environ.get("HY2_JSON_GECKO_MAX_PACKET_SIZE", "1200")),
        },
    }
# 【已修正】将原本存在笔误的 obj_type 改正为声明的 obfs_type 变量
elif obfs_type == "salamander" and obfs_password:
    obj["obfs"] = {"type": "salamander", "salamander": {"password": obfs_password}}
if os.environ["HY2_JSON_PORT_MODE"] == "hop":
    udp = {}
    min_i = os.environ.get("HY2_JSON_MIN_HOP_INTERVAL", "")
    max_i = os.environ.get("HY2_JSON_MAX_HOP_INTERVAL", "")
    if min_i and max_i:
        udp["minHopInterval"] = f"{min_i}s"
        udp["maxHopInterval"] = f"{max_i}s"
    else:
        udp["hopInterval"] = f"{os.environ.get('HY2_JSON_HOP_INTERVAL', '25')}s"
    obj["transport"] = {"type": "udp", "udp": udp}
with open(os.environ["HY2_JSON_FILE"], "w", encoding="utf-8") as f:
    json.dump(obj, f, ensure_ascii=False, indent=2)
    f.write("\n")
PY
}

generate_main_outputs_uncommitted() {
  mkdir -p "$OUT_DIR"
  local main_link="" name_enc path_enc extra extra_enc
  case "$MAIN_MODE" in
    xhttp)
      path_enc="$(urlencode "$XHTTP_PATH")"; extra='{"xPaddingBytes":"100-1000"}'; extra_enc="$(urlencode "$extra")"; name_enc="$(urlencode "${NODE_NAME:-${XHTTP_DOMAIN} XHTTP}")"
      main_link="vless://${XHTTP_UUID}@${XHTTP_DOMAIN}:443?mode=auto&path=${path_enc}&security=tls&alpn=h2&encryption=none&extra=${extra_enc}&insecure=0&host=${XHTTP_DOMAIN}&fp=${FP}&type=xhttp&allowInsecure=0&sni=${XHTTP_DOMAIN}#${name_enc}"
      cat > "$XRAY_CLIENT_JSON" <<JSON
{
  "outbounds": [
    {
      "tag": "proxy",
      "protocol": "vless",
      "settings": { "vnext": [ { "address": "${XHTTP_DOMAIN}", "port": 443, "users": [ { "id": "${XHTTP_UUID}", "encryption": "none" } ] } ] },
      "streamSettings": {
        "network": "xhttp",
        "security": "tls",
        "tlsSettings": { "serverName": "${XHTTP_DOMAIN}", "allowInsecure": false, "alpn": ["h2"], "fingerprint": "${FP}" },
        "xhttpSettings": { "host": "${XHTTP_DOMAIN}", "path": "${XHTTP_PATH}", "mode": "auto", "extra": { "xPaddingBytes": "100-1000" } }
      }
    }
  ]
}
JSON
      ;;
    reality)
      ensure_reality_keys_present
      local reality_uri_host
      reality_uri_host="$(uri_host "$REALITY_ADDRESS")"
      name_enc="$(urlencode "${NODE_NAME:-REALITY Vision}")"
      main_link="vless://${REALITY_UUID}@${reality_uri_host}:443?encryption=none&security=reality&sni=${REALITY_SNI}&fp=${FP}&pbk=${REALITY_PUBLIC_KEY}&sid=${REALITY_SHORT_ID}&type=tcp&flow=xtls-rprx-vision&spx=%2F#${name_enc}"
      cat > "$XRAY_CLIENT_JSON" <<JSON
{
  "outbounds": [
    {
      "tag": "proxy",
      "protocol": "vless",
      "settings": { "vnext": [ { "address": "${REALITY_ADDRESS}", "port": 443, "users": [ { "id": "${REALITY_UUID}", "encryption": "none", "flow": "xtls-rprx-vision" } ] } ] },
      "streamSettings": {
        "network": "raw",
        "security": "reality",
        "realitySettings": { "serverName": "${REALITY_SNI}", "fingerprint": "${FP}", "password": "${REALITY_PUBLIC_KEY}", "shortId": "${REALITY_SHORT_ID}", "spiderX": "/" }
      }
    }
  ]
}
JSON
      ;;
  esac
  echo "$main_link" > "${OUT_DIR}/main-url.txt"
  generate_hy2_outputs
  cat > "$INFO_FILE" <<EOF
组合模式: ${STACK_MODE}
主节点模式: ${MAIN_MODE}
管理命令: tui

主节点链接:
${main_link}

Hysteria2 链接:
$(cat "${OUT_DIR}/hy2-url.txt")

主节点客户端 JSON: ${XRAY_CLIENT_JSON}
Hysteria2 客户端 YAML: ${HY2_CLIENT_YAML}
Hysteria2 客户端 JSON: ${HY2_CLIENT_JSON}
Hysteria2 混淆: ${HY2_OBFS_TYPE:-none}
Hysteria2 Gecko 分片: ${HY2_GECKO_MIN_PACKET_SIZE:-512}-${HY2_GECKO_MAX_PACKET_SIZE:-1200}
Swap 创建: ${SWAP_MANAGED:-0}
Swap 文件: ${SWAP_FILE_PATH:-$SWAP_FILE}
Swap 大小GB: ${SWAP_SIZE_GB:-}

服务状态命令:
systemctl status xray --no-pager
systemctl status caddy --no-pager
systemctl status hysteria-server --no-pager
EOF
}

generate_main_outputs() {
  local destination="$OUT_DIR" stage name
  mkdir -p "$destination"
  chmod 700 "$destination"
  stage="$(mktemp -d "${destination}.stage.XXXXXX")"
  local OUT_DIR="$stage" INFO_FILE="$stage/node-info.txt"
  local XRAY_CLIENT_JSON="$stage/xray-client.json" HY2_CLIENT_JSON="$stage/hy2-client.json" HY2_CLIENT_YAML="$stage/hy2-client.yaml"
  generate_main_outputs_uncommitted
  MSYS2_ENV_CONV_EXCL="${MSYS2_ENV_CONV_EXCL:-};DPS_OUTPUT_STAGE;DPS_OUTPUT_DESTINATION" \
    DPS_OUTPUT_STAGE="$stage" DPS_OUTPUT_DESTINATION="$destination" \
    python3 - "$stage/node-info.txt" <<'PY'
import os,pathlib,sys
p=pathlib.Path(sys.argv[1])
p.write_text(p.read_text().replace(os.environ['DPS_OUTPUT_STAGE'],os.environ['DPS_OUTPUT_DESTINATION']))
PY
  for name in main-url.txt hy2-url.txt node-info.txt xray-client.json hy2-client.json hy2-client.yaml; do
    if ! python3 "$STORAGE_HELPER" write "$destination/$name" "$stage/$name" 600; then
      rm -rf -- "$stage"
      return 1
    fi
  done
  rm -rf -- "$stage"
}

collect_xhttp_hy2_inputs() {
  warn "请先确保两个域名都 A/AAAA 直连本服务器，暂时不要开启 CDN，且 TCP 80/443 放行。"
  pause_confirm "确认后按回车继续..."
  XHTTP_DOMAIN="$(prompt_domain "请输入 XHTTP 域名")"
  while true; do
    HY2_DOMAIN="$(prompt_domain "请输入 Hysteria2 域名（必须不同于 XHTTP 域名）")"
    [[ "$HY2_DOMAIN" != "$XHTTP_DOMAIN" ]] && break
    warn "XHTTP + HY2 组合要求两个不同域名。"
  done
  XHTTP_UUID="$(new_uuid)"
  local def_path="/${XHTTP_UUID:0:8}" p
  while true; do
    read -r -p "请输入 XHTTP 路径 [默认 ${def_path}]: " p
    p="${p:-$def_path}"
    if XHTTP_PATH="$(normalize_path "$p")"; then break; fi
    warn "路径格式不正确。"
  done
  XHTTP_BACKEND_PORT="$(find_free_local_port)"
  collect_fp
  read -r -p "节点名称 [默认 ${XHTTP_DOMAIN} XHTTP + HY2]: " n
  NODE_NAME="${n:-${XHTTP_DOMAIN} XHTTP + HY2}"
  collect_hy2_common
  MAIN_MODE="xhttp"; STACK_MODE="xhttp_hy2"; MAIN_DOMAIN="$XHTTP_DOMAIN"; CADDY_REQUIRED="1"; WEB_ENABLED="1"; HY2_CERT_SOURCE="caddy"; MANAGED_DOMAINS="$XHTTP_DOMAIN $HY2_DOMAIN"
}

collect_reality_hy2_inputs() {
  warn "REALITY 占用 TCP 443，不安装 Caddy，不申请网页证书。HY2 使用自身 ACME，需要 TCP 80 空闲且 HY2 域名直连本机。"
  pause_confirm "确认后按回车继续..."
  REALITY_ADDRESS="$(public_ip)"
  REALITY_ADDRESS="$(prompt_host_address "请输入 REALITY 连接地址（VPS IP 或域名）" "$REALITY_ADDRESS")"
  REALITY_SNI="$(prompt_domain "请输入 REALITY 目标 SNI" "nokia.com")"
  collect_fp
  REALITY_UUID="$(new_uuid)"
  generate_reality_x25519_keys
  REALITY_SHORT_ID="$(generate_shortid)"
  HY2_DOMAIN="$(prompt_domain "请输入 Hysteria2 ACME 域名")"
  HY2_ACME_EMAIL="acme-$(date +%s)@gmail.com"
  read -r -p "ACME 邮箱 [默认 ${HY2_ACME_EMAIL}]: " he
  HY2_ACME_EMAIL="${he:-$HY2_ACME_EMAIL}"
  read -r -p "节点名称 [默认 REALITY + HY2]: " n
  NODE_NAME="${n:-REALITY + HY2}"
  collect_hy2_common
  MAIN_MODE="reality"; STACK_MODE="reality_hy2"; MAIN_DOMAIN="$REALITY_ADDRESS"; CADDY_REQUIRED="0"; WEB_ENABLED="0"; HY2_CERT_SOURCE="acme"; MANAGED_DOMAINS="$HY2_DOMAIN"
}

install_stack_xhttp_hy2() {
  install_base_packages
  ensure_tcp_80_443_free_for_caddy
  install_caddy_official
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
  save_state
  generate_main_outputs
  install_self_command
}

install_stack_reality_hy2() {
  install_base_packages
  ensure_tcp_443_free_for_reality
  if port_in_use_tcp 80; then
    warn "HY2 ACME HTTP-01 需要 TCP 80 空闲。当前 TCP 80 被占用。"
    show_port_users
    read -r -p "是否停止常见服务释放 TCP 80？[y/N]: " ans
    [[ "${ans:-}" =~ ^[Yy]$ ]] || { err "请先释放 TCP 80。"; exit 1; }
    stop_known_services
    sleep 2
    if port_in_use_tcp 80; then
      err "TCP 80 仍被占用，HY2 ACME HTTP-01 无法签发证书。"
      show_port_users
      exit 1
    fi
  fi
  [[ -x "$XRAY_BIN" ]] || install_xray_official
  [[ -x "$HY2_BIN" ]] || install_hysteria_official
  write_reality_xray_config
  write_hy2_config_acme
  systemctl disable --now caddy >/dev/null 2>&1 || true
  start_xray
  reality_local_self_test || exit 1
  start_hy2
  web_wait_for_hy2_acme_cert "$HY2_DOMAIN"
  save_state
  generate_main_outputs
  install_self_command
}

install_selected_stack() {
  case "$STACK_MODE" in
    xhttp_hy2) install_stack_xhttp_hy2 ;;
    reality_hy2) install_stack_reality_hy2 ;;
    *) err "未知或已移除的 STACK_MODE: $STACK_MODE"; exit 1 ;;
  esac
  print_result
}

cleanup_managed_caddy_certs() {
  load_state || true
  local d base
  base="${CADDY_DATA}/certificates"
  [[ -d "$base" ]] || return 0
  for d in ${MANAGED_DOMAINS:-}; do
    [[ -n "$d" ]] || continue
    find "$base" -type d -name "$d" -prune -exec rm -rf {} + 2>/dev/null || true
  done
}

uninstall_current_stack() {
  load_state || return 0
  warn "正在卸载当前组合：${STACK_MODE:-unknown}"
  systemctl stop xray caddy hysteria-server >/dev/null 2>&1 || true
  systemctl disable xray caddy hysteria-server >/dev/null 2>&1 || true
  remove_service_resilience_overrides
  cleanup_managed_caddy_certs
  rm -f "$XRAY_CONFIG" "$HY2_CONFIG" "$CADDYFILE"
  rm -rf "$WEB_ROOT" "$HY2_WEB_ROOT" "$OUT_DIR"
  rm -rf /etc/hysteria
  # Remove service/binary pieces installed by official installers; reinstallers will put them back when needed.
  rm -f /usr/local/bin/hysteria
  rm -f /etc/systemd/system/hysteria-server.service /lib/systemd/system/hysteria-server.service
  rm -f /etc/systemd/system/xray.service /etc/systemd/system/xray@.service /lib/systemd/system/xray.service /lib/systemd/system/xray@.service
  rm -f /usr/local/bin/xray
  rm -rf /usr/local/etc/xray /usr/local/share/xray /var/log/xray
  if [[ "${CADDY_REQUIRED:-0}" == "1" ]]; then
    apt-get purge -y caddy >/dev/null 2>&1 || true
    rm -f /usr/bin/caddy
    rm -rf /etc/caddy
  fi
  systemctl daemon-reload
  rm -f "$STATE_FILE"
  ok "当前组合已卸载。"
}

full_uninstall() {
  if load_state; then
    echo "当前组合：${STACK_MODE:-unknown}"
  fi
  read -r -p "确认彻底卸载脚本安装的一切？输入 YES 继续: " ans
  [[ "$ans" == "YES" ]] || { warn "已取消。"; return 0; }
  uninstall_current_stack || true
  apt-get purge -y caddy >/dev/null 2>&1 || true
  if [[ -e "$JB_CMD_FALLBACK" && "$(readlink -f "$JB_CMD_FALLBACK" 2>/dev/null || true)" == "$(readlink -f "$JB_CMD" 2>/dev/null || true)" ]]; then
    rm -f "$JB_CMD_FALLBACK"
  fi
  rm -f /usr/bin/caddy "$JB_CMD" "$THIS_SCRIPT"
  rm -rf "$STATE_DIR"
  systemctl disable --now dual-protocol-script-fix >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/dual-protocol-script-fix.service /etc/default/dual-protocol-script-fix /etc/default/dual-protocol-script-fix.fix-recovery
  rm -f /usr/local/bin/tui /usr/local/bin/dual-protocol-script-fix
  rm -rf /usr/local/lib/dual-protocol-script-fix /var/lib/dual-protocol-script-fix
  systemctl daemon-reload
  ok "彻底卸载完成。"
}

choose_stack_initial() {
  echo
  echo "请选择要安装的组合："
  echo "  1) VLESS + XHTTP + TLS  + Hysteria2"
  echo "  2) VLESS + REALITY + Vision + Hysteria2"
  while true; do
    read -r -p "请输入选项 [1-2]: " ch
    case "$ch" in
      1) collect_xhttp_hy2_inputs; break ;;
      2) install_base_packages; [[ -x "$XRAY_BIN" ]] || install_xray_official; collect_reality_hy2_inputs; break ;;
      *) warn "请输入 1-2。" ;;
    esac
  done
}

switch_to_stack() {
  local target="$1"
  load_state || true
  warn "即将收集目标配置并切换组合：$target（失败恢复原配置）"
  read -r -p "输入 SWITCH 确认: " ans
  [[ "$ans" == "SWITCH" ]] || { warn "已取消。"; return 0; }
  case "$target" in
    xhttp_hy2) collect_xhttp_hy2_inputs ;;
    reality_hy2) collect_reality_hy2_inputs ;;
    *) err "目标组合已移除或不存在：$target"; exit 1 ;;
  esac
  apply_current_shell_config reinstall
  load_state
  print_result
}

apply_current_shell_config() {
  DPS_ROTATE_KEYS="${DPS_ROTATE_KEYS:-0}"
  export DPS_WEB_NODE_NAME="${NODE_NAME:-}"
  export DPS_WEB_FINGERPRINT="${FP:-}"
  export DPS_WEB_XHTTP_DOMAIN="${XHTTP_DOMAIN:-}"
  export DPS_WEB_XHTTP_PATH="${XHTTP_PATH:-}"
  export DPS_WEB_XHTTP_UUID="${XHTTP_UUID:-}"
  export DPS_WEB_XHTTP_BACKEND_PORT="${XHTTP_BACKEND_PORT:-}"
  export DPS_WEB_REALITY_ADDRESS="${REALITY_ADDRESS:-}"
  export DPS_WEB_REALITY_SNI="${REALITY_SNI:-}"
  export DPS_WEB_REALITY_UUID="${REALITY_UUID:-}"
  export DPS_WEB_ROTATE_REALITY_KEYS="${DPS_ROTATE_KEYS:-}"
  local field
  for field in HY2_DOMAIN HY2_PASSWORD HY2_PORT_MODE HY2_PORT HY2_FIRST_PORT HY2_END_PORT HY2_HOP_INTERVAL HY2_MIN_HOP_INTERVAL HY2_MAX_HOP_INTERVAL HY2_ACME_EMAIL HY2_OBFS_TYPE HY2_OBFS_PASSWORD HY2_GECKO_MIN_PACKET_SIZE HY2_GECKO_MAX_PACKET_SIZE; do
    export "DPS_WEB_${field}=${!field-}"
  done
  export DPS_WEB_APPLY=1
  if [[ "${1:-}" == "reinstall" ]]; then
    export DPS_WEB_TARGET_MODE="$MAIN_MODE"
    ( web_reinstall_stack )
  else
    ( web_apply_config )
  fi
}

change_hy2_password() {
  load_state || return 1
  read -r -p "新 HY2 密码 [回车自动生成]: " p
  HY2_PASSWORD="${p:-$(generate_password)}"
  apply_current_shell_config
}

change_hy2_port() {
  load_state || return 1
  collect_hy2_port
  apply_current_shell_config
}

change_xhttp_path() {
  load_state || return 1
  [[ "$MAIN_MODE" == "xhttp" ]] || return 0
  local p
  read -r -p "新的 XHTTP 路径: " p
  XHTTP_PATH="$(normalize_path "$p")" || return 1
  apply_current_shell_config
}

regen_main_uuid_or_credential() {
  load_state || return 1
  if [[ "$MAIN_MODE" == "xhttp" ]]; then XHTTP_UUID="$(new_uuid)"; else REALITY_UUID="$(new_uuid)"; fi
  apply_current_shell_config
}

change_fp() {
  load_state || return 1
  case "$MAIN_MODE" in xhttp|reality) collect_fp ;; *) warn "当前主节点模式无 uTLS 指纹设置或已不支持。"; return 0 ;; esac
  save_state; generate_main_outputs
  ok "客户端指纹参数已更新。"
}

change_reality_target() {
  load_state || return 1
  [[ "$MAIN_MODE" == "reality" ]] || return 0
  REALITY_ADDRESS="$(prompt_host_address "新的 REALITY 连接地址" "$REALITY_ADDRESS")"
  REALITY_SNI="$(prompt_domain "新的 REALITY 目标 SNI" "$REALITY_SNI")"
  DPS_ROTATE_KEYS=1
  apply_current_shell_config
  DPS_ROTATE_KEYS=0
}

