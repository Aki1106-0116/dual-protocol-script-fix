package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

type Manager struct {
	mu             sync.RWMutex
	persistMu      sync.Mutex
	operationMu    sync.Mutex
	refreshMu      sync.Mutex
	tunnels        map[int]*Tunnel
	nodes          []Node
	fetched        time.Time
	workDir        string
	maxSlots       int
	router         *Router
	jobs           JobStore
	connectSlots   chan struct{}
	portPicker     func(map[int]bool) (int, error)
	stopping       bool
	restoreError   error
	lifetime       context.Context
	cancelLifetime context.CancelFunc
	stopHealth     chan struct{}
	badNodes       map[string]time.Time
}

func NewManager(maxSlots int, workDir string, router *Router) *Manager {
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	return &Manager{
		lifetime: lifetime, cancelLifetime: cancelLifetime,
		portPicker: freeRandomPort,
		tunnels:    make(map[int]*Tunnel), workDir: workDir,
		maxSlots: maxSlots, router: router, connectSlots: make(chan struct{}, 3), stopHealth: make(chan struct{}), badNodes: map[string]time.Time{},
	}
}

func (m *Manager) RefreshNodes() (int, error) {
	started := time.Now()
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	m.mu.RLock()
	alreadyFresh, count := m.fetched.After(started), len(m.nodes)
	m.mu.RUnlock()
	if alreadyFresh {
		return count, nil
	}
	nodes, err := fetchNodes(40 * time.Second)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	m.nodes, m.fetched = nodes, time.Now()
	m.mu.Unlock()
	return len(nodes), nil
}

func (m *Manager) Nodes() ([]Node, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]Node(nil), m.nodes...)
	return out, m.fetched
}

func (m *Manager) Tunnels() []*Tunnel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Tunnel, 0, len(m.tunnels))
	for _, tunnel := range m.tunnels {
		out = append(out, tunnel)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}

func (m *Manager) TunnelViews() []TunnelView {
	return sortedTunnelViews(m.Tunnels())
}

func (m *Manager) tunnel(slot int) (*Tunnel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tunnel, ok := m.tunnels[slot]
	return tunnel, ok
}

func (m *Manager) freeSlotLocked() (int, error) {
	for slot := 1; slot <= m.maxSlots; slot++ {
		if _, exists := m.tunnels[slot]; !exists {
			return slot, nil
		}
	}
	return 0, fmt.Errorf("出口槽位已满（上限 %d）", m.maxSlots)
}

func (m *Manager) Start(node Node) (*Tunnel, error) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	m.mu.Lock()
	if m.restoreError != nil {
		err := m.restoreError
		m.mu.Unlock()
		return nil, fmt.Errorf("已保存出口尚未恢复，拒绝覆盖状态: %w", err)
	}
	if m.stopping || m.lifetime.Err() != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("管理器正在停止")
	}
	if len(m.tunnels) >= m.maxSlots {
		m.mu.Unlock()
		return nil, fmt.Errorf("出口已满")
	}
	if until := m.badNodes[node.HostName]; time.Now().Before(until) {
		m.mu.Unlock()
		return nil, fmt.Errorf("节点暂时冷却中")
	}
	for _, t := range m.tunnels {
		if t.nodeNow().HostName == node.HostName {
			m.mu.Unlock()
			return nil, fmt.Errorf("节点已被其他出口使用")
		}
	}
	slot, err := m.freeSlotLocked()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	taken := map[int]bool{}
	for _, tunnel := range m.tunnels {
		taken[tunnel.view().Port] = true
	}
	port, err := freeRandomPort(taken)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	tunnel := newTunnel(slot, port, node)
	tunnel.portAllocator = func() (net.Listener, error) { return m.reserveReplacementListener(tunnel) }
	m.tunnels[slot] = tunnel
	m.mu.Unlock()
	if err := m.saveState(); err != nil {
		m.mu.Lock()
		delete(m.tunnels, slot)
		m.mu.Unlock()
		return nil, fmt.Errorf("保存新出口失败，未开始连接: %w", err)
	}
	go m.bringUp(tunnel)
	return tunnel, nil
}

func (m *Manager) reserveReplacementListener(tunnel *Tunnel) (net.Listener, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tunnel.isStopped() {
		return nil, fmt.Errorf("出口已停止")
	}
	taken := map[int]bool{}
	for _, other := range m.tunnels {
		taken[other.view().Port] = true
	}
	for attempt := 0; attempt < 8; attempt++ {
		port, err := m.portPicker(taken)
		if err != nil {
			return nil, err
		}
		listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			taken[port] = true
			continue
		}
		tunnel.setPort(port)
		return listener, nil
	}
	return nil, fmt.Errorf("无法保留新的 SOCKS5 端口")
}

func (m *Manager) bringUp(tunnel *Tunnel) {
	select {
	case m.connectSlots <- struct{}{}:
		defer func() { <-m.connectSlots }()
	case <-tunnel.done:
		return
	}
	tunnel.lifeMu.Lock()
	defer tunnel.lifeMu.Unlock()
	if tunnel.isStopped() {
		return
	}
	candidates := m.candidatesFor(tunnel.nodeNow())
	var lastErr error
	for i, node := range candidates {
		if tunnel.isStopped() {
			return
		}
		if i > 0 {
			m.mu.Lock()
			until := m.badNodes[node.HostName]
			m.mu.Unlock()
			if time.Now().Before(until) {
				continue
			}
		}
		if i > 0 && m.nodeInUse(node.HostName, tunnel.Slot) {
			continue
		}
		m.mu.Lock()
		claimed := false
		for slot, other := range m.tunnels {
			if slot != tunnel.Slot && other.nodeNow().HostName == node.HostName {
				claimed = true
				break
			}
		}
		if !claimed {
			tunnel.replaceNode(node)
		}
		m.mu.Unlock()
		if claimed {
			continue
		}
		detail := ""
		if i > 0 {
			detail = fmt.Sprintf("第 %d 个同地区候选", i+1)
		}
		tunnel.setState("starting", "", detail)
		if err := tunnel.setupNetns(); err != nil {
			lastErr = err
			continue
		}
		if err := tunnel.startOpenVPN(m.workDir); err != nil {
			lastErr = err
			m.markBad(node.HostName)
			tunnel.killOpenVPN()
			tunnel.teardownNetns()
			continue
		}
		if err := tunnel.ensureSOCKS(); err != nil {
			lastErr = err
			m.markBad(node.HostName)
			tunnel.killOpenVPN()
			tunnel.teardownNetns()
			continue
		}
		exitIP, err := tunnel.probeExitIP()
		if err != nil {
			lastErr = err
			m.markBad(node.HostName)
			tunnel.killOpenVPN()
			tunnel.teardownNetns()
			continue
		}
		if tunnel.isStopped() {
			tunnel.killOpenVPN()
			tunnel.teardownNetns()
			return
		}
		tunnel.setState("up", exitIP, "")
		if err := m.saveState(); err != nil {
			log.Printf("保存隧道状态失败: %v", err)
		}
		m.notifyRouter()
		return
	}
	message := fmt.Sprintf("尝试 %d 个节点均失败", len(candidates))
	if lastErr != nil {
		message += ": " + lastErr.Error()
	}
	tunnel.setState("failed", "", message)
	if err := m.saveState(); err != nil {
		log.Printf("保存隧道状态失败: %v", err)
	}
}

func (m *Manager) candidatesFor(first Node) []Node {
	const maxCandidates = 6
	m.mu.RLock()
	defer m.mu.RUnlock()
	used := map[string]bool{first.HostName: true}
	for _, tunnel := range m.tunnels {
		used[tunnel.nodeNow().HostName] = true
	}
	region := first.CountryCode
	if region == "" {
		for _, node := range m.nodes {
			if node.HostName == first.HostName {
				region = node.CountryCode
				break
			}
		}
	}
	out := []Node{}
	if !time.Now().Before(m.badNodes[first.HostName]) {
		for _, fresh := range m.nodes {
			if fresh.HostName == first.HostName {
				first = fresh
				break
			}
		}
		out = append(out, first)
	}
	for _, node := range m.nodes {
		if len(out) >= maxCandidates {
			break
		}
		if used[node.HostName] || time.Now().Before(m.badNodes[node.HostName]) || (region != "" && node.CountryCode != region) {
			continue
		}
		out = append(out, node)
	}
	return out
}

func (m *Manager) Stop(slot int) error {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	m.mu.Lock()
	tunnel, ok := m.tunnels[slot]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("出口槽位 %d 不存在", slot)
	}

	if m.router != nil {
		if err := m.router.DropSlot(slot, m.Tunnels()); err != nil {
			_ = m.router.Apply(m.Tunnels())
			return fmt.Errorf("停止前切回直连失败，出口仍保留: %w", err)
		}
	}
	tunnel.stop()
	m.mu.Lock()
	if m.tunnels[slot] == tunnel {
		delete(m.tunnels, slot)
	}
	m.mu.Unlock()
	if err := m.saveState(); err != nil {
		log.Printf("保存隧道状态失败: %v", err)
	}
	return nil
}

func (m *Manager) Bind(protocol string, slot int) error {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	m.mu.RLock()
	stopping := m.stopping || m.lifetime.Err() != nil
	m.mu.RUnlock()
	if stopping {
		return fmt.Errorf("管理器正在停止")
	}
	if m.router == nil {
		return fmt.Errorf("路由管理器不可用")
	}
	return m.router.Bind(protocol, slot, m.Tunnels())
}

func (m *Manager) Swap(slot int) error {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	if m.lifetime.Err() != nil {
		return fmt.Errorf("管理器正在停止")
	}
	tunnel, ok := m.tunnel(slot)
	if !ok {
		return fmt.Errorf("出口槽位 %d 不存在", slot)
	}
	if tunnel.statusNow() == "starting" {
		return fmt.Errorf("该出口正在连接中")
	}
	region := tunnel.nodeNow().CountryCode
	picks, err := m.pickNodes(region, 1)
	if err != nil {
		return err
	}
	m.mu.Lock()
	for otherSlot, other := range m.tunnels {
		if otherSlot != slot && other.nodeNow().HostName == picks[0].HostName {
			m.mu.Unlock()
			return fmt.Errorf("候选节点已被其他出口使用，请重试")
		}
	}
	queued := m.queueReconnect(tunnel, "用户手动切换", &picks[0])
	m.mu.Unlock()
	if !queued {
		return fmt.Errorf("出口状态已经变化，请等待当前连接完成")
	}
	return nil
}

func (m *Manager) reconnect(tunnel *Tunnel, reason string) {
	m.queueReconnect(tunnel, reason, nil)
}

func (m *Manager) queueReconnect(tunnel *Tunnel, reason string, node *Node) bool {
	return m.queueReconnectAt(tunnel, reason, node, 0)
}

func (m *Manager) queueReconnectAt(tunnel *Tunnel, reason string, node *Node, generation uint64) bool {
	if m.lifetime.Err() != nil {
		return false
	}
	tunnel.mu.Lock()
	if tunnel.stopped || tunnel.status == "starting" || (generation != 0 && tunnel.generation != generation) {
		tunnel.mu.Unlock()
		return false
	}
	oldHost := tunnel.Node.HostName
	if node != nil {
		tunnel.Node = *node
	}
	tunnel.status = "starting"
	tunnel.generation++
	tunnel.errText = reason
	tunnel.exitIP = ""
	tunnel.since = time.Now()
	tunnel.mu.Unlock()
	if generation != 0 {
		m.markBad(oldHost)
	}
	go func() {
		tunnel.closeClients()
		m.mu.RLock()
		age := time.Since(m.fetched)
		m.mu.RUnlock()
		if age > time.Minute {
			if _, err := m.RefreshNodes(); err != nil {
				log.Printf("切换前刷新出口列表失败，继续尝试已加载候选: %v", err)
			}
		}
		if !tunnel.resetForReconnect() {
			return
		}
		m.bringUp(tunnel)
	}()
	return true
}
func (m *Manager) markBad(host string) {
	m.mu.Lock()
	m.badNodes[host] = time.Now().Add(5 * time.Minute)
	m.mu.Unlock()
}
func (m *Manager) nodeInUse(host string, exceptSlot int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for slot, tunnel := range m.tunnels {
		if slot != exceptSlot && tunnel.nodeNow().HostName == host {
			return true
		}
	}
	return false
}

func (m *Manager) pickNodes(region string, count int) ([]Node, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	used := map[string]bool{}
	for _, tunnel := range m.tunnels {
		used[tunnel.nodeNow().HostName] = true
	}
	var out []Node
	for _, node := range m.nodes {
		if len(out) >= count {
			break
		}
		if used[node.HostName] || time.Now().Before(m.badNodes[node.HostName]) || (region != "" && !strings.EqualFold(node.CountryCode, region)) {
			continue
		}
		out = append(out, node)
	}
	if len(out) == 0 {
		if region != "" {
			return nil, fmt.Errorf("%s 暂无空闲可用节点", region)
		}
		return nil, fmt.Errorf("暂无空闲可用节点，请刷新列表")
	}
	return out, nil
}

func (m *Manager) Regions() []RegionStat {
	m.mu.RLock()
	defer m.mu.RUnlock()
	used := map[string]bool{}
	for _, tunnel := range m.tunnels {
		used[tunnel.nodeNow().HostName] = true
	}
	grouped := map[string]*RegionStat{}
	for _, node := range m.nodes {
		if used[node.HostName] || node.CountryCode == "" {
			continue
		}
		stat := grouped[node.CountryCode]
		if stat == nil {
			stat = &RegionStat{Code: node.CountryCode, Name: node.Country}
			grouped[node.CountryCode] = stat
		}
		stat.Available++
		if node.SpeedMbps > stat.BestSpeed {
			stat.BestSpeed = node.SpeedMbps
		}
		if node.Ping > 0 && (stat.BestPing == 0 || node.Ping < stat.BestPing) {
			stat.BestPing = node.Ping
		}
	}
	out := make([]RegionStat, 0, len(grouped))
	for _, stat := range grouped {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Available != out[j].Available {
			return out[i].Available > out[j].Available
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func (m *Manager) notifyRouter() {
	if m.router == nil {
		return
	}
	if err := m.router.Apply(m.Tunnels()); err != nil {
		log.Printf("同步协议出口失败: %v", err)
	}
}

func (m *Manager) Shutdown() {
	m.cancelLifetime()
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return
	}
	m.stopping = true
	close(m.stopHealth)
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, tunnel := range m.Tunnels() {
		wg.Add(1)
		go func(t *Tunnel) { defer wg.Done(); t.stop() }(tunnel)
	}
	wg.Wait()
}

func prepareHost() error {
	if err := exec.Command("sysctl", "-qw", "net.ipv4.ip_forward=1").Run(); err != nil {
		return fmt.Errorf("开启 IPv4 转发失败: %w", err)
	}
	return nil
}
