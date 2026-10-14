package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var netnsConfigRoot = "/etc/netns"

func (t *Tunnel) setupNetns() error {
	profile, err := normalizeVPNProfile(t.nodeNow())
	if err != nil {
		return err
	}
	ns, subnet := t.nsName(), t.subnet()
	hostVeth, nsVeth := fmt.Sprintf("dpsv%d", t.Slot), fmt.Sprintf("dpsp%d", t.Slot)
	t.teardownNetns()

	steps := [][]string{
		{"netns", "add", ns},
		{"netns", "exec", ns, "ip", "link", "set", "lo", "up"},
		{"link", "add", hostVeth, "type", "veth", "peer", "name", nsVeth},
		{"link", "set", nsVeth, "netns", ns},
		{"addr", "add", subnet + ".1/30", "dev", hostVeth},
		{"link", "set", hostVeth, "up"},
		{"netns", "exec", ns, "ip", "addr", "add", subnet + ".2/30", "dev", nsVeth},
		{"netns", "exec", ns, "ip", "link", "set", nsVeth, "up"},
		{"netns", "exec", ns, "ip", "route", "add", "default", "via", subnet + ".1"},
	}
	for _, args := range steps {
		if err := run("ip", args...); err != nil {
			t.teardownNetns()
			return err
		}
	}

	nsDir := filepath.Join(netnsConfigRoot, ns)
	if err := os.MkdirAll(nsDir, 0755); err != nil {
		t.teardownNetns()
		return fmt.Errorf("创建 %s 失败: %w", nsDir, err)
	}
	if err := os.WriteFile(filepath.Join(nsDir, "resolv.conf"), []byte("nameserver 1.1.1.1\nnameserver 8.8.8.8\n"), 0644); err != nil {
		t.teardownNetns()
		return fmt.Errorf("写入 netns DNS 失败: %w", err)
	}

	cidr := subnet + ".0/30"
	if err := ensureRule("nat", "POSTROUTING", "-s", cidr, "-j", "MASQUERADE"); err != nil {
		t.teardownNetns()
		return err
	}
	if err := ensureRuleInsert("filter", "FORWARD", "-s", cidr, "-j", "ACCEPT"); err != nil {
		t.teardownNetns()
		return err
	}
	if err := ensureRuleInsert("filter", "FORWARD", "-d", cidr, "-j", "ACCEPT"); err != nil {
		t.teardownNetns()
		return err
	}
	proto := "tcp"
	if profile.Proto == "udp" {
		proto = "udp"
	}
	for _, args := range [][]string{{"-A", "OUTPUT", "-o", "lo", "-j", "ACCEPT"}, {"-A", "OUTPUT", "-o", "tun0", "-j", "ACCEPT"}, {"-A", "OUTPUT", "-o", nsVeth, "-p", proto, "-d", profile.Host, "--dport", fmt.Sprint(profile.Port), "-j", "ACCEPT"}, {"-P", "OUTPUT", "DROP"}} {
		if err := run("ip", append([]string{"netns", "exec", ns, "iptables", "-w", "5"}, args...)...); err != nil {
			t.teardownNetns()
			return err
		}
	}
	return nil
}

func ensureRule(table, chain string, spec ...string) error {
	check := append([]string{"-w", "5", "-t", table, "-C", chain}, spec...)
	if exec.Command("iptables", check...).Run() == nil {
		return nil
	}
	add := append([]string{"-w", "5", "-t", table, "-A", chain}, spec...)
	return run("iptables", add...)
}

func ensureRuleInsert(table, chain string, spec ...string) error {
	check := append([]string{"-w", "5", "-t", table, "-C", chain}, spec...)
	if exec.Command("iptables", check...).Run() == nil {
		return nil
	}
	insert := append([]string{"-w", "5", "-t", table, "-I", chain, "1"}, spec...)
	return run("iptables", insert...)
}

func (t *Tunnel) teardownNetns() {
	ns, subnet := t.nsName(), t.subnet()
	cidr := subnet + ".0/30"
	runQuiet("ip", "netns", "del", ns)
	runQuiet("ip", "link", "del", fmt.Sprintf("dpsv%d", t.Slot))
	runQuiet("iptables", "-w", "5", "-t", "nat", "-D", "POSTROUTING", "-s", cidr, "-j", "MASQUERADE")
	runQuiet("iptables", "-w", "5", "-D", "FORWARD", "-s", cidr, "-j", "ACCEPT")
	runQuiet("iptables", "-w", "5", "-D", "FORWARD", "-d", cidr, "-j", "ACCEPT")
	_ = os.RemoveAll(filepath.Join(netnsConfigRoot, ns))
}

func (t *Tunnel) startOpenVPN(workDir string) error {
	err := t.startOpenVPNAttempt(workDir, false)
	if err == nil {
		return nil
	}
	profile, e := normalizeVPNProfile(t.nodeNow())
	if e != nil || !profile.LegacyCertificate {
		return err
	}
	message := strings.ToLower(tailVPNLog(filepath.Join(workDir, t.nsName()+".log")))
	if !legacyClientCertificateFailure(message) {
		return err
	}
	t.killOpenVPN()
	return t.startOpenVPNAttempt(workDir, true)
}
func legacyClientCertificateFailure(message string) bool {
	return strings.Contains(message, "ca md too weak") && strings.Contains(message, "cannot load inline certificate file")
}
func (t *Tunnel) startOpenVPNAttempt(workDir string, compatibility bool) error {
	node := t.nodeNow()
	profile, err := normalizeVPNProfile(node)
	if err != nil {
		return err
	}
	configPath := filepath.Join(workDir, t.nsName()+".ovpn")
	authPath := filepath.Join(workDir, t.nsName()+"-auth.txt")
	logPath := filepath.Join(workDir, t.nsName()+".log")
	if err := os.WriteFile(configPath, []byte(profile.Config), 0600); err != nil {
		return fmt.Errorf("写 OpenVPN 配置失败: %w", err)
	}
	if err := os.WriteFile(authPath, []byte("vpn\nvpn\n"), 0600); err != nil {
		return fmt.Errorf("写 VPN Gate 凭据失败: %w", err)
	}
	cmd := exec.Command("ip", "netns", "exec", t.nsName(), "openvpn",
		"--config", configPath,
		"--auth-user-pass", authPath,
		"--auth-nocache",
		"--dev", "tun0",
		"--connect-retry-max", "2",
		"--connect-timeout", "20",
		"--data-ciphers", "AES-128-CBC:AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305",
		"--script-security", "1",
		"--verb", "3",
		"--log", logPath,
	)
	if compatibility {
		cmd.Args = append(cmd.Args, "--tls-cert-profile", "insecure")
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 OpenVPN 失败: %w", err)
	}
	t.mu.Lock()
	t.ovpn = cmd
	done := make(chan struct{})
	t.ovpnDone = done
	t.mu.Unlock()
	go func() { _ = cmd.Wait(); close(done) }()

	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("ip", "netns", "exec", t.nsName(), "ip", "-4", "addr", "show", "tun0").Output()
		logData, _ := os.ReadFile(logPath)
		if err == nil && strings.Contains(string(out), "inet ") && strings.Contains(string(logData), "Initialization Sequence Completed") {
			return nil
		}
		select {
		case <-done:
			return fmt.Errorf("OpenVPN 提前退出: %s", tailVPNLog(logPath))
		case <-t.done:
			return fmt.Errorf("出口已停止")
		case <-time.After(time.Second):
		}

	}
	return fmt.Errorf("等待 tun0 就绪超时，详见 %s", logPath)
}

func (t *Tunnel) ensureSOCKS() error {
	t.mu.Lock()
	if t.listener != nil {
		t.mu.Unlock()
		return nil
	}
	port := t.Port
	t.mu.Unlock()

	var listener net.Listener
	var err error
	for i := 0; i < 5; i++ {
		listener, err = net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		if t.portAllocator != nil {
			listener, err = t.portAllocator()
		} else {
			var replacement int
			replacement, err = freeRandomPort(map[int]bool{port: true})
			if err == nil {
				listener, err = net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", replacement))
			}
			if err == nil {
				t.setPort(replacement)
			}
		}
		if err != nil {
			return fmt.Errorf("SOCKS5 端口 %d 被占用且无法另选: %w", port, err)
		}
	}
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		_ = listener.Close()
		return fmt.Errorf("出口已经停止，已关闭迟到的 SOCKS5 监听")
	}
	t.listener = listener
	t.mu.Unlock()

	dial := dialerInNetnsContext(t.nsName())
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			t.mu.Lock()
			if t.stopped || t.status != "up" {
				t.mu.Unlock()
				conn.Close()
				continue
			}
			if t.clients == nil {
				t.clients = make(map[net.Conn]struct{})
			}
			t.clients[conn] = struct{}{}
			t.mu.Unlock()
			go func() {
				defer func() { t.mu.Lock(); delete(t.clients, conn); t.mu.Unlock() }()
				if t.statusNow() != "up" {
					conn.Close()
					return
				}
				serveSOCKSContext(conn, dial)
			}()
		}
	}()
	return nil
}

func (t *Tunnel) probeExitIP() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: dialerInNetnsContext(t.nsName()), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for _, url := range []string{"https://api.ipify.org", "https://checkip.amazonaws.com"} {
		requestCtx, requestCancel := context.WithTimeout(ctx, 5*time.Second)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
		if err != nil {
			requestCancel()
			continue
		}
		response, err := client.Do(req)
		if err == nil {
			data, e := io.ReadAll(io.LimitReader(response.Body, 128))
			response.Body.Close()
			requestCancel()
			ip := strings.TrimSpace(string(data))
			parsed := net.ParseIP(ip)
			if e == nil && response.StatusCode == http.StatusOK && parsed != nil && parsed.To4() != nil && parsed.IsGlobalUnicast() && !parsed.IsPrivate() {
				return ip, nil
			}
		} else {
			requestCancel()
		}
	}
	return "", fmt.Errorf("隧道互联网探测失败（已禁止直连回落）")
}
func tailVPNLog(path string) string {
	f, e := os.Open(path)
	if e != nil {
		return e.Error()
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e.Error()
	}
	offset := info.Size() - 4096
	if offset < 0 {
		offset = 0
	}
	f.Seek(offset, io.SeekStart)
	data, _ := io.ReadAll(io.LimitReader(f, 4096))
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "ERROR") || strings.Contains(line, "error") || strings.Contains(line, "AUTH_FAILED") || strings.Contains(line, "Cannot load") || strings.Contains(line, "Options error") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return "OpenVPN 已退出，请查看 " + path
	}
	return strings.Join(out, "; ")
}
func (t *Tunnel) killOpenVPN() {
	t.mu.Lock()
	cmd, done := t.ovpn, t.ovpnDone
	t.ovpn = nil
	t.ovpnDone = nil
	t.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		if done != nil {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func (t *Tunnel) closeClients() {
	t.mu.Lock()
	clients := make([]net.Conn, 0, len(t.clients))
	for c := range t.clients {
		clients = append(clients, c)
		delete(t.clients, c)
	}
	t.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
}
func (t *Tunnel) isStopped() bool { t.mu.RLock(); defer t.mu.RUnlock(); return t.stopped }
func (t *Tunnel) resetForReconnect() bool {
	t.lifeMu.Lock()
	defer t.lifeMu.Unlock()
	if t.isStopped() {
		return false
	}
	t.killOpenVPN()
	t.teardownNetns()
	return true
}
func (t *Tunnel) stop() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	close(t.done)
	t.status = "stopped"
	t.exitIP = ""
	if t.listener != nil {
		t.listener.Close()
		t.listener = nil
	}
	t.mu.Unlock()
	t.closeClients()
	t.killOpenVPN()
	t.lifeMu.Lock()
	defer t.lifeMu.Unlock()
	t.teardownNetns()
}
