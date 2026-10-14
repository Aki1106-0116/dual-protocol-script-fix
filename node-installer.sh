#!/usr/bin/env bash
set -Eeuo pipefail
BACKEND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DPS_BACKEND_ENTRY="${BACKEND_DIR}/node-installer.sh"
for module in node-common node-services node-transaction node-menu; do
  source "$BACKEND_DIR/lib/${module}.sh"
done
main() {
  require_root
  case "${1:-}" in
    web-export) web_export_config; exit $? ;;
    web-apply) web_apply_config; exit $? ;;
    web-reinstall) web_reinstall_stack; exit $? ;;
    restart) config_lock; restart_all; exit $? ;;
    refresh-exports)
      config_lock
      load_state
      ensure_hysteria_capabilities
      systemctl daemon-reload
      if [[ "${HY2_CERT_SOURCE:-}" == "acme" ]] && id hysteria >/dev/null 2>&1; then
        chown -R hysteria:hysteria /var/lib/hysteria/acme
      fi
      candidate="$(mktemp "${HY2_CONFIG}.refresh.XXXXXX")"
      cp "$HY2_CONFIG" "$candidate"
      python3 "$STORAGE_HELPER" write "$HY2_CONFIG" "$candidate" 600
      rm -f "$candidate"
      generate_main_outputs
      exit $? ;;
  esac
  check_os
  install_self_command || true
  if [[ "${1:-}" == "menu" ]]; then menu; exit 0; fi
  if load_state; then
    warn "检测到已安装组合：${STACK_MODE}。输入 tui 进入控制面板，或选择切换/卸载。"
    menu
    exit 0
  fi
  config_lock
  prompt_swap_setup
  choose_stack_initial
  install_selected_stack
}

main "$@"
