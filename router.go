package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	managedXrayTag         = "dual-protocol-script-fix-vless"
	legacyManagedXrayTag   = "jb" + "-gateway-vless"
	previousManagedXrayTag = "dual" + "-protocol-script-vless"
	previousHY2BlockBegin  = "# BEGIN DUAL" + "-PROTOCOL-SCRIPT MANAGED OUTBOUND"
	previousHY2BlockEnd    = "# END DUAL" + "-PROTOCOL-SCRIPT MANAGED OUTBOUND"
	hy2BlockBegin          = "# BEGIN DUAL-PROTOCOL-SCRIPT-FIX MANAGED OUTBOUND"
	hy2BlockEnd            = "# END DUAL-PROTOCOL-SCRIPT-FIX MANAGED OUTBOUND"
	firstFixHY2BlockBegin  = "# BEGIN DUAL-PROTOCOL-SCRIPT-FIX-FIX MANAGED OUTBOUND"
	firstFixHY2BlockEnd    = "# END DUAL-PROTOCOL-SCRIPT-FIX-FIX MANAGED OUTBOUND"
	legacyHY2BlockBegin    = "# BEGIN " + "JB" + "-GATEWAY MANAGED OUTBOUND"
	legacyHY2BlockEnd      = "# END " + "JB" + "-GATEWAY MANAGED OUTBOUND"
)

type Router struct {
	mu          sync.Mutex
	workDir     string
	bindingPath string
	xrayConfig  string
	hy2Config   string
	xrayBin     string
	state       BindingState
	lastApplied time.Time
	lastError   string
}

func NewRouter(workDir, xrayConfig, hy2Config, xrayBin string) (*Router, error) {
	router := &Router{
		workDir: workDir, bindingPath: filepath.Join(workDir, "bindings.json"),
		xrayConfig: xrayConfig, hy2Config: hy2Config, xrayBin: xrayBin,
	}
	if err := router.load(); err != nil {
		return nil, err
	}
	return router, nil
}

func (r *Router) load() error {
	data, err := readConfigFile(r.bindingPath, validateBindingState)
	if os.IsNotExist(err) {
		r.state = BindingState{}
		return r.saveLocked()
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &r.state); err != nil {
		return fmt.Errorf("解析协议绑定状态失败: %w", err)
	}
	return nil
}

func validateBindingState(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, protocol := range []string{"vless", "hy2"} {
		var binding map[string]json.RawMessage
		if err := json.Unmarshal(fields[protocol], &binding); err != nil {
			return fmt.Errorf("协议绑定 %s 无效: %w", protocol, err)
		}
		var slot *int
		if err := json.Unmarshal(binding["slot"], &slot); err != nil || slot == nil || *slot < 0 || *slot > 200 {
			return fmt.Errorf("协议绑定 %s 的 slot 缺失或无效", protocol)
		}
	}
	return nil
}

func (r *Router) saveLocked() error {
	data, err := json.MarshalIndent(r.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.workDir, 0700); err != nil {
		return err
	}
	return atomicWriteFile(r.bindingPath, data, 0600)
}

func (r *Router) Bind(protocol string, slot int, tunnels []*Tunnel) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock, err := acquireConfigLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol != "vless" && protocol != "hy2" {
		return fmt.Errorf("未知协议 %q", protocol)
	}
	if slot < 0 {
		return errors.New("slot 不能小于 0")
	}
	if slot > 0 {
		tunnel := findTunnel(tunnels, slot)
		if tunnel == nil {
			return fmt.Errorf("出口槽位 %d 不存在", slot)
		}
		if tunnel.statusNow() != "up" {
			return fmt.Errorf("出口槽位 %d 尚未连通", slot)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.state
	if protocol == "vless" {
		r.state.VLESS.Slot = slot
	} else {
		r.state.HY2.Slot = slot
	}
	if err := r.saveLocked(); err != nil {
		r.state = old
		return err
	}
	if err := r.applyLocked(tunnels); err != nil {
		r.state = old
		saveErr := r.saveLocked()
		rollbackErr := r.applyLocked(tunnels)
		if rollbackErr != nil || saveErr != nil {
			return fmt.Errorf("应用绑定失败: %v；保存旧绑定: %v；恢复旧路由: %v", err, saveErr, rollbackErr)
		}
		return err
	}
	return nil
}

func (r *Router) DropSlot(slot int, tunnels []*Tunnel) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock, err := acquireConfigLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.state
	changed := false
	if r.state.VLESS.Slot == slot {
		r.state.VLESS.Slot = 0
		changed = true
	}
	if r.state.HY2.Slot == slot {
		r.state.HY2.Slot = 0
		changed = true
	}
	if !changed {
		return nil
	}
	if err := r.saveLocked(); err != nil {
		r.state = old
		return err
	}
	if err := r.applyLocked(tunnels); err != nil {
		r.state = old
		saveErr := r.saveLocked()
		rollbackErr := r.applyLocked(tunnels)
		if saveErr != nil || rollbackErr != nil {
			return fmt.Errorf("移除绑定失败: %v；保存旧绑定: %v；恢复旧路由: %v", err, saveErr, rollbackErr)
		}
		return err
	}
	return nil
}

func (r *Router) Apply(tunnels []*Tunnel) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock, err := acquireConfigLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applyLocked(tunnels)
}

func (r *Router) applyLocked(tunnels []*Tunnel) error {
	if err := ensureReadableConfig(r.xrayConfig, validateXray); err != nil {
		r.lastError = err.Error()
		return err
	}
	if err := ensureReadableConfig(r.hy2Config, validateHY2); err != nil {
		r.lastError = err.Error()
		return err
	}
	oldXray, err := readSmallFile(r.xrayConfig)
	if err != nil {
		return err
	}
	vlessPort := boundPort(r.state.VLESS.Slot, tunnels)
	hy2Port := boundPort(r.state.HY2.Slot, tunnels)
	if vlessPort < 0 || hy2Port < 0 {
		err := errors.New("绑定出口尚未恢复，保留原路由且不回落直连")
		r.lastError = err.Error()
		return err
	}
	if err := r.applyXray(vlessPort); err != nil {
		r.lastError = err.Error()
		return err
	}
	if err := r.applyHY2(hy2Port); err != nil {
		r.lastError = err.Error()
		current, _ := readSmallFile(r.xrayConfig)
		if !bytes.Equal(current, oldXray) {
			if rollbackErr := replaceAndRestart(r.xrayConfig, oldXray, 0644, "xray"); rollbackErr != nil {
				return fmt.Errorf("HY2 路由失败: %v；Xray 回滚失败: %v", err, rollbackErr)
			}
		}
		return err
	}
	r.lastApplied, r.lastError = time.Now(), ""
	return nil
}

func findTunnel(tunnels []*Tunnel, slot int) *Tunnel {
	for _, tunnel := range tunnels {
		if tunnel.Slot == slot {
			return tunnel
		}
	}
	return nil
}

func boundPort(slot int, tunnels []*Tunnel) int {
	if slot == 0 {
		return 0
	}
	tunnel := findTunnel(tunnels, slot)
	if tunnel == nil {
		return -1
	}
	return tunnel.view().Port
}

func (r *Router) Status(tunnels []*Tunnel) RouterStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	stack, label := detectStack(r.xrayConfig)
	status := RouterStatus{
		Stack: stack, MainLabel: label,
		XrayConfig: r.xrayConfig, HY2Config: r.hy2Config,
		LastApplied: r.lastApplied, LastError: r.lastError,
	}
	status.Routes = []ProtocolRoute{
		r.routeStatus("vless", label, r.state.VLESS.Slot, tunnels),
		r.routeStatus("hy2", "Hysteria2", r.state.HY2.Slot, tunnels),
	}
	return status
}

func (r *Router) routeStatus(protocol, label string, slot int, tunnels []*Tunnel) ProtocolRoute {
	route := ProtocolRoute{Protocol: protocol, Label: label, Slot: slot, Mode: "direct", Ready: true}
	if slot == 0 {
		return route
	}
	route.Mode = "vpngate"
	tunnel := findTunnel(tunnels, slot)
	if tunnel == nil {
		route.Ready = false
		route.Note = "绑定的出口不存在；保留绑定并等待恢复，不自动回落直连"
		return route
	}
	view := tunnel.view()
	route.ExitIP, route.Country = view.ExitIP, view.Node.CountryCode
	route.Ready = view.Status == "up"
	if !route.Ready {
		route.Note = view.Error
		if route.Note == "" {
			route.Note = "出口正在连接"
		}
	}
	if protocol == "hy2" {
		route.Note = strings.TrimSpace(route.Note + " TCP 与 UDP（含手机 DNS）均通过所选家宽出口；HY2 入站 QUIC/UDP 不受影响。")
	}
	return route
}

func detectStack(path string) (string, string) {
	data, err := readConfigFile(path, validateXray)
	if err != nil {
		if !os.IsNotExist(err) {
			return "storage_error", "VLESS（配置存储异常）"
		}
		return "not_installed", "VLESS"
	}
	var document struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return "storage_error", "VLESS"
	}
	for _, in := range document.Inbounds {
		if in.Protocol != "vless" {
			continue
		}
		switch in.Tag {
		case "vless-xhttp-in":
			return "xhttp_hy2", "VLESS · XHTTP · TLS"
		case "vless-reality-vision-in":
			return "reality_hy2", "VLESS · REALITY · Vision"
		}
	}
	return "unknown", "VLESS"
}

func (r *Router) applyXray(socksPort int) error {
	if err := ensureReadableConfig(r.xrayConfig, validateXray); err != nil {
		return err
	}
	data, err := readConfigFile(r.xrayConfig, validateXray)
	if os.IsNotExist(err) {
		return fmt.Errorf("Xray 节点配置尚未安装: %s", r.xrayConfig)
	}
	if err != nil {
		return err
	}
	transformed, err := transformXrayConfig(data, socksPort)
	if err != nil {
		return err
	}
	if bytes.Equal(data, transformed) {
		return nil
	}
	temp, err := writeSiblingTemp(r.xrayConfig, transformed, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	out, err := exec.Command(r.xrayBin, "run", "-test", "-config", temp, "-format", "json").CombinedOutput()
	if err != nil {
		return fmt.Errorf("Xray 拒绝新路由配置，旧配置未改动: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return replaceAndRestart(r.xrayConfig, transformed, 0644, "xray")
}

func transformXrayConfig(data []byte, socksPort int) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("解析 Xray JSON 失败: %w", err)
	}
	rawOutbounds, _ := root["outbounds"].([]any)
	outbounds := make([]any, 0, len(rawOutbounds)+1)
	for _, raw := range rawOutbounds {
		item, _ := raw.(map[string]any)
		if tag, _ := item["tag"].(string); tag == managedXrayTag || tag == legacyManagedXrayTag || tag == previousManagedXrayTag {
			continue
		}
		outbounds = append(outbounds, raw)
	}
	if socksPort > 0 {
		settings := map[string]any{
			"servers": []any{
				map[string]any{"address": "127.0.0.1", "port": socksPort},
			},
		}
		if usesModernXraySchema(root) {
			settings = map[string]any{
				"address": "127.0.0.1",
				"port":    socksPort,
			}
		}
		outbounds = append(outbounds, map[string]any{
			"tag": managedXrayTag, "protocol": "socks",
			"settings": settings,
		})
	}
	root["outbounds"] = outbounds

	routing, _ := root["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{"domainStrategy": "AsIs"}
	}
	rawRules, _ := routing["rules"].([]any)
	rules := make([]any, 0, len(rawRules)+1)
	for _, raw := range rawRules {
		item, _ := raw.(map[string]any)
		if tag, _ := item["outboundTag"].(string); tag == managedXrayTag || tag == legacyManagedXrayTag || tag == previousManagedXrayTag {
			continue
		}
		rules = append(rules, raw)
	}
	if socksPort > 0 {
		inboundTag := supportedInboundTag(root)
		if inboundTag == "" {
			return nil, errors.New("未找到受支持的 XHTTP/REALITY 入站，拒绝绑定家宽出口")
		}
		rules = append([]any{map[string]any{
			"type": "field", "inboundTag": []any{inboundTag}, "outboundTag": managedXrayTag,
		}}, rules...)
	}
	routing["rules"] = rules
	root["routing"] = routing
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func supportedInboundTag(root map[string]any) string {
	inbounds, _ := root["inbounds"].([]any)
	for _, raw := range inbounds {
		in, _ := raw.(map[string]any)
		if in["protocol"] == "vless" && (in["tag"] == "vless-reality-vision-in" || in["tag"] == "vless-xhttp-in") {
			return in["tag"].(string)
		}
	}
	return ""
}

func usesModernXraySchema(root map[string]any) bool {
	inbounds, _ := root["inbounds"].([]any)
	for _, raw := range inbounds {
		inbound, _ := raw.(map[string]any)
		if inbound["protocol"] != "vless" {
			continue
		}
		settings, _ := inbound["settings"].(map[string]any)
		if _, ok := settings["users"]; ok {
			return true
		}
		stream, _ := inbound["streamSettings"].(map[string]any)
		if _, ok := stream["method"]; ok {
			return true
		}
	}
	return false
}

func (r *Router) applyHY2(socksPort int) error {
	if err := ensureReadableConfig(r.hy2Config, validateHY2); err != nil {
		return err
	}
	data, err := readConfigFile(r.hy2Config, validateHY2)
	if os.IsNotExist(err) {
		return fmt.Errorf("Hysteria2 节点配置尚未安装: %s", r.hy2Config)
	}
	if err != nil {
		return err
	}
	transformed, err := transformHY2Config(data, socksPort)
	if err != nil {
		return err
	}
	if bytes.Equal(data, transformed) {
		return nil
	}
	return replaceAndRestart(r.hy2Config, transformed, 0644, "hysteria-server")
}

func transformHY2Config(data []byte, socksPort int) ([]byte, error) {
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("HY2 YAML 无效，拒绝改动: %w", err)
	}
	if document["listen"] == nil || document["auth"] == nil {
		return nil, errors.New("HY2 缺少 listen/auth")
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if strings.TrimSpace(text) == "" || !strings.Contains(text, "listen:") || !strings.Contains(text, "auth:") {
		return nil, errors.New("Hysteria2 配置缺少 listen/auth，拒绝修改")
	}
	var stripErr error
	for _, markers := range [][2]string{{hy2BlockBegin, hy2BlockEnd}, {firstFixHY2BlockBegin, firstFixHY2BlockEnd}, {legacyHY2BlockBegin, legacyHY2BlockEnd}, {previousHY2BlockBegin, previousHY2BlockEnd}} {
		text, stripErr = stripHY2ManagedBlock(text, markers[0], markers[1])
		if stripErr != nil {
			return nil, stripErr
		}
	}
	text = strings.TrimRight(text, "\r\n") + "\n"
	var base map[string]any
	if err := yaml.Unmarshal([]byte(text), &base); err != nil {
		return nil, fmt.Errorf("HY2 基础配置无效: %w", err)
	}
	if socksPort > 0 {
		if _, exists := base["outbounds"]; exists {
			return nil, errors.New("存在非本项目管理的 HY2 outbounds，拒绝覆盖")
		}
		text += fmt.Sprintf(`
%s
# The loopback SOCKS5 relay supports both CONNECT and UDP ASSOCIATE.
# TCP and destination UDP (including DNS) use the selected VPN Gate exit.
outbounds:
  - name: dual-protocol-script-fix
    type: socks5
    socks5:
      addr: 127.0.0.1:%d
%s
`, hy2BlockBegin, socksPort, hy2BlockEnd)
	}
	var final map[string]any
	if err := yaml.Unmarshal([]byte(text), &final); err != nil {
		return nil, fmt.Errorf("生成的 HY2 路由无效，未提交: %w", err)
	}
	return []byte(text), nil
}

func stripHY2ManagedBlock(text, begin, endMarker string) (string, error) {
	lines := strings.Split(text, "\n")
	start, end := -1, -1
	for index, line := range lines {
		// Only standalone top-level comments are owned markers. A password or
		// a YAML block scalar containing the same text must never be stripped.
		switch strings.TrimRight(line, " \t\r") {
		case begin:
			if start >= 0 || end >= 0 {
				return "", errors.New("HY2 受管路由标记重复或顺序错误")
			}
			start = index
		case endMarker:
			if start < 0 || end >= 0 {
				return "", errors.New("HY2 受管路由标记不完整或重复")
			}
			end = index
		}
	}
	if start < 0 {
		return text, nil
	}
	if end < 0 {
		return "", errors.New("Hysteria2 受管路由块不完整，拒绝修改")
	}
	return strings.Join(append(lines[:start], lines[end+1:]...), "\n"), nil
}

func replaceAndRestart(path string, data []byte, mode os.FileMode, service string) error {
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, data, mode); err != nil {
		return err
	}
	restartErr := run("systemctl", "restart", service)
	activeErr := run("systemctl", "is-active", "--quiet", service)
	if restartErr == nil && activeErr == nil {
		return nil
	}
	rollbackErr := atomicWriteFile(path, old, mode)
	if rollbackErr != nil {
		return fmt.Errorf("%s 重启失败且配置回滚失败: restart=%v active=%v rollback=%v", service, restartErr, activeErr, rollbackErr)
	}
	rollbackRestartErr := run("systemctl", "restart", service)
	rollbackActiveErr := run("systemctl", "is-active", "--quiet", service)
	if rollbackRestartErr != nil || rollbackActiveErr != nil {
		return fmt.Errorf("%s 旧配置已恢复，但旧服务未恢复: restart=%v active=%v；原故障: %v %v", service, rollbackRestartErr, rollbackActiveErr, restartErr, activeErr)
	}
	return fmt.Errorf("%s 重启失败，已恢复旧配置: restart=%v active=%v", service, restartErr, activeErr)
}

func writeSiblingTemp(path string, data []byte, mode os.FileMode) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	unlock := lockConfigFile(path)
	defer unlock()
	if err := atomicWriteRaw(path, data, mode); err != nil {
		return err
	}
	actual, err := readSmallFile(path)
	if err != nil || !bytes.Equal(actual, data) {
		return fmt.Errorf("写入后读回校验失败: %s: %v", path, err)
	}
	rememberFile(path, data)
	return nil
}
