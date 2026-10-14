#!/usr/bin/env bash
# Menu and display functions; sourced by node-installer.sh.
show_info() {
  load_state || { warn "尚未安装。"; return 0; }
  echo
  echo "当前组合：${STACK_MODE}"
  echo "主节点：${MAIN_MODE}"
  echo "管理域名：${MANAGED_DOMAINS:-无}"
  echo
  [[ -f "${OUT_DIR}/main-url.txt" ]] && { echo "主节点链接："; cat "${OUT_DIR}/main-url.txt"; echo; }
  [[ -f "${OUT_DIR}/hy2-url.txt" ]] && { echo "Hysteria2 链接："; cat "${OUT_DIR}/hy2-url.txt"; echo; }
  echo "详情文件：$INFO_FILE"
}

restart_all() {
  load_state || return 1
  local service failed=0
  local -a services=(xray hysteria-server)
  [[ "${CADDY_REQUIRED:-0}" == "1" ]] && services+=(caddy)
  python3 "$STORAGE_HELPER" recover "$XRAY_CONFIG" || return 1
  python3 "$STORAGE_HELPER" recover "$HY2_CONFIG" || return 1
  if [[ "${CADDY_REQUIRED:-0}" == "1" ]]; then python3 "$STORAGE_HELPER" recover "$CADDYFILE" || return 1; fi
  ensure_service_resilience "${services[@]}"
  for service in "${services[@]}"; do
    if systemctl restart "$service" && systemctl is-active --quiet "$service"; then
      ok "$service 正常"
    else
      err "$service 重启或健康检查失败"
      failed=1
    fi
  done
  return "$failed"
}

menu() {
  require_root; check_os
  if ! load_state; then
    warn "尚未安装任何组合。"
    prompt_swap_setup
    choose_stack_initial
    install_selected_stack
    exit 0
  fi
  if [[ "${STACK_MODE:-}" == "naive_hy2" || "${MAIN_MODE:-}" == "naive" ]]; then
    warn "检测到旧版 Naive + HY2 状态；新版已移除该组合。请切换到 XHTTP/REALITY，或执行彻底卸载。"
  fi
  while true; do
    echo
    echo "================ dual-protocol-script-fix 节点控制面板 ================"
    echo "当前组合：${STACK_MODE}"
    echo "1) 查看节点信息/分享链接"
    echo "2) 重新生成主节点 UUID"
    echo "3) 修改 XHTTP 路径（仅 XHTTP）"
    echo "4) 修改 uTLS 指纹（XHTTP/REALITY）"
    echo "5) 修改 REALITY 连接地址/目标 SNI，并重生成密钥"
    echo "6) 修改 Hysteria2 密码"
    echo "7) 修改 Hysteria2 端口/端口跳跃"
    echo "8) 重启并检查服务"
    echo "9) 卸载当前组合并安装 XHTTP + HY2"
    echo "10) 卸载当前组合并安装 REALITY + Vision + HY2"
    echo "11) 彻底卸载"
    echo "0) 退出"
    read -r -p "请选择: " ch
    config_lock || continue
    case "$ch" in
      1) show_info ;;
      2) regen_main_uuid_or_credential ;;
      3) change_xhttp_path ;;
      4) change_fp ;;
      5) change_reality_target ;;
      6) change_hy2_password ;;
      7) change_hy2_port ;;
      8) restart_all ;;
      9) switch_to_stack xhttp_hy2 ;;
      10) switch_to_stack reality_hy2 ;;
      11) full_uninstall; exit 0 ;;
      0) exit 0 ;;
      *) warn "无效选项。" ;;
    esac
    config_unlock
  done
}

print_result() {
  echo
  echo -e "${GREEN}================ 部署完成 ================${NC}"
  echo "组合模式：${STACK_MODE}"
  echo "管理命令：tui"
  echo
  echo "主节点链接："
  cat "${OUT_DIR}/main-url.txt"
  echo
  echo "Hysteria2 链接："
  cat "${OUT_DIR}/hy2-url.txt"
  echo
  echo "详情文件：$INFO_FILE"
  echo
  echo "Hysteria2 混淆：${HY2_OBFS_TYPE:-none}"
  echo "Swap 创建：${SWAP_MANAGED:-0}  ${SWAP_FILE_PATH:-$SWAP_FILE} ${SWAP_SIZE_GB:-}GB"
  echo
  warn "注意："
  if [[ "${CADDY_REQUIRED:-0}" == "1" ]]; then
    echo "1) Caddy 负责证书签发/续签；HY2 只读 Caddy 证书路径，不复制、不搬动证书。"
    echo "2) 首次签发前请保持域名直连本机，暂时不要开启 CDN。"
    echo "3) 若后续开启 CDN，续签取决于 ACME HTTP-01 是否能正确回源到 Caddy。"
  else
    echo "1) REALITY 使用 TCP 443，不安装 Caddy，不需要网页证书。"
    echo "2) HY2 使用自身 ACME，HY2 域名需要直连本机，TCP 80 需要空闲。"
  fi
  echo "4) HY2 UDP 端口/端口范围已避开 80、443、2053、8443。"
  if [[ "${HY2_OBFS_TYPE:-none}" != "none" ]]; then
      echo "5) 已启用 HY2 ${HY2_OBFS_TYPE} 混淆；客户端必须使用相同混淆类型和密码。若分享链接客户端不识别 gecko，请使用生成的 HY2 客户端 YAML/JSON。"
    echo "6) 已为相关 systemd 服务写入异常退出自动重启策略：Restart=on-failure，RestartSec=5s。"
  else
    echo "5) 已为相关 systemd 服务写入异常退出自动重启策略：Restart=on-failure，RestartSec=5s。"
  fi
}

