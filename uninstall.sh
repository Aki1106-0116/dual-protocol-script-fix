#!/usr/bin/env bash
set -Eeuo pipefail

[[ "${EUID}" -eq 0 ]] || { echo "需要 root 权限" >&2; exit 1; }
read -r -p "输入 UNINSTALL 删除 Dual Protocol Fix 家宽出口服务（不删除 Xray/HY2/Caddy 节点）：" answer
[[ "$answer" == "UNINSTALL" ]] || { echo "已取消"; exit 0; }

WORK_DIR="${WORK_DIR:-/var/lib/dual-protocol-script-fix}"
APP_BIN=/usr/local/bin/dual-protocol-script-fix
if [[ -s "$WORK_DIR/bindings.json" ]]; then
 backup="$(mktemp "$WORK_DIR/bindings.uninstall.XXXXXX")"
 cp -a "$WORK_DIR/bindings.json" "$backup"
 candidate="$(mktemp "$WORK_DIR/bindings.direct.XXXXXX")"
 printf '{"vless":{"slot":0},"hy2":{"slot":0}}\n' > "$candidate"
 python3 /usr/local/lib/dual-protocol-script-fix/storage.py write "$WORK_DIR/bindings.json" "$candidate" 600
 if ! "$APP_BIN" apply --dir "$WORK_DIR"; then
   python3 /usr/local/lib/dual-protocol-script-fix/storage.py write "$WORK_DIR/bindings.json" "$backup" 600
   echo '节点未能安全切回直连，已保留面板和出口；请先修复配置/服务后再卸载。' >&2
   exit 1
 fi
 rm -f "$backup" "$candidate"
fi
systemctl disable --now dual-protocol-script-fix >/dev/null 2>&1 || true
for ns in $(ip netns list 2>/dev/null | awk '/^dps[0-9]+/{print $1}'); do
  ip netns del "$ns" >/dev/null 2>&1 || true
done
rm -f /etc/systemd/system/dual-protocol-script-fix.service /etc/default/dual-protocol-script-fix
rm -f /usr/local/bin/dual-protocol-script-fix /usr/local/bin/tui
rm -rf /usr/local/lib/dual-protocol-script-fix
systemctl daemon-reload
echo "Dual Protocol Fix 已卸载。Xray、Hysteria2、Caddy 与节点配置均保留。"
