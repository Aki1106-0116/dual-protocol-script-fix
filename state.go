package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

type persistedTunnel struct {
	Slot        int    `json:"slot"`
	Port        int    `json:"port"`
	HostName    string `json:"hostname"`
	IP          string `json:"ip"`
	CountryCode string `json:"country_code"`
	Country     string `json:"country"`
	Config      string `json:"config"`
}

type persistedState struct {
	Tunnels []persistedTunnel `json:"tunnels"`
}

func (m *Manager) statePath() string {
	return filepath.Join(m.workDir, "tunnels.json")
}

func (m *Manager) saveState() error {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	m.mu.RLock()
	restoreErr := m.restoreError
	m.mu.RUnlock()
	if restoreErr != nil {
		return fmt.Errorf("拒绝覆盖未恢复的出口状态: %w", restoreErr)
	}
	state := persistedState{}
	for _, tunnel := range m.Tunnels() {
		view := tunnel.view()
		if view.Status == "stopped" {
			continue
		}
		state.Tunnels = append(state.Tunnels, persistedTunnel{
			Slot: view.Slot, Port: view.Port,
			HostName: view.Node.HostName, IP: view.Node.IP,
			CountryCode: view.Node.CountryCode, Country: view.Node.Country,
			Config: view.Node.Config,
		})
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := validateTunnelState(data); err != nil {
		return fmt.Errorf("出口快照无效，保留已有文件: %w", err)
	}
	path := m.statePath()
	return atomicWriteFile(path, data, 0600)
}

func (m *Manager) restoreState() (int, error) {
	tunnels, err := m.registerSavedTunnels()
	if err != nil {
		return 0, err
	}
	for _, tunnel := range tunnels {
		go m.bringUp(tunnel)
	}
	return len(tunnels), nil
}

// Reserve every saved slot before HTTP starts serving and before any network
// work can write a partial snapshot. New requests cannot replace saved exits.
func (m *Manager) registerSavedTunnels() (restored []*Tunnel, err error) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	defer func() { m.mu.Lock(); m.restoreError = err; m.mu.Unlock() }()
	data, err := readConfigFile(m.statePath(), validateTunnelState)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析隧道状态失败: %w", err)
	}
	known := map[string]Node{}
	nodes, _ := m.Nodes()
	for _, node := range nodes {
		known[node.HostName] = node
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return nil, fmt.Errorf("管理器正在停止")
	}
	for _, saved := range state.Tunnels {
		node, ok := known[saved.HostName]
		if !ok {
			node = Node{
				HostName: saved.HostName, IP: saved.IP,
				CountryCode: saved.CountryCode, Country: saved.Country,
			}
		}
		if !ok {
			node.Config = saved.Config
		}
		tunnel := newTunnel(saved.Slot, saved.Port, node)
		tunnel.portAllocator = func() (net.Listener, error) { return m.reserveReplacementListener(tunnel) }
		if m.tunnels[saved.Slot] != nil {
			continue
		}
		m.tunnels[saved.Slot] = tunnel
		restored = append(restored, tunnel)
	}
	return restored, nil
}

func validateTunnelState(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if _, exists := raw["tunnels"]; !exists {
		return fmt.Errorf("隧道状态缺少 tunnels 字段")
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if len(state.Tunnels) > 200 {
		return fmt.Errorf("隧道状态超过最大槽位数")
	}
	slots, ports, hosts := map[int]bool{}, map[int]bool{}, map[string]bool{}
	for _, saved := range state.Tunnels {
		if saved.Slot < 1 || saved.Slot > 200 || saved.Port < 1024 || saved.Port > 65535 || saved.HostName == "" || slots[saved.Slot] || ports[saved.Port] || hosts[saved.HostName] {
			return fmt.Errorf("隧道状态包含无效或重复的槽位、端口或节点")
		}
		slots[saved.Slot] = true
		ports[saved.Port] = true
		hosts[saved.HostName] = true
	}
	return nil
}
