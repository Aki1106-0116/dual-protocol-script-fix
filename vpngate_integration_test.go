//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLinuxVPNGateStrictCertificate(t *testing.T) {
	if os.Getenv("DPS_RUN_LIVE_VPN") != "1" {
		t.Skip("opt-in live VPN check")
	}
	nodes, e := fetchNodes(20 * time.Second)
	if e != nil {
		t.Fatal(e)
	}
	node := nodes[0]
	profile, e := normalizeVPNProfile(node)
	if e != nil {
		t.Fatal(e)
	}
	tunnel := newTunnel(200, 50000, node)
	if e := tunnel.setupNetns(); e != nil {
		t.Fatal(e)
	}
	defer tunnel.teardownNetns()
	dir := t.TempDir()
	config := filepath.Join(dir, "strict.ovpn")
	logfile := filepath.Join(dir, "strict.log")
	auth := filepath.Join(dir, "auth")
	os.WriteFile(config, []byte(strings.ReplaceAll(profile.Config, "tls-cert-profile insecure\n", "")), 0600)
	os.WriteFile(auth, []byte("vpn\nvpn\n"), 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ip", "netns", "exec", tunnel.nsName(), "openvpn", "--config", config, "--auth-user-pass", auth, "--verb", "3", "--log", logfile)
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for i := 0; i < 20; i++ {
		data, _ := os.ReadFile(logfile)
		if strings.Contains(string(data), "Initialization Sequence Completed") {
			t.Log("DEFAULT_CERT_PROFILE_CONNECTED")
			cancel()
			<-done
			return
		}
		select {
		case e := <-done:
			t.Logf("DEFAULT_CERT_PROFILE_FAILED %v: %s", e, tailVPNLog(logfile))
			return
		case <-time.After(time.Second):
		}
	}
	cancel()
	<-done
	t.Logf("DEFAULT_CERT_PROFILE_TIMEOUT: %s", tailVPNLog(logfile))
}

// Explicit opt-in and isolated guest only; normal test runs never contact
// volunteers or create production namespaces.
func TestLinuxLiveVPNGate(t *testing.T) {
	if os.Getenv("DPS_RUN_LIVE_VPN") != "1" {
		t.Skip("opt-in live VPN check")
	}
	if os.Geteuid() != 0 {
		t.Fatal("requires root in isolated guest")
	}
	if err := prepareHost(); err != nil {
		t.Fatal(err)
	}
	m := NewManager(1, t.TempDir(), nil)
	defer m.Shutdown()
	go m.WatchHealth()
	count, err := m.RefreshNodes()
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := m.Nodes()
	t.Logf("official nodes=%d", count)
	tunnel, err := m.Start(nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		view := tunnel.view()
		if view.Status == "up" {
			t.Logf("LIVE_VPN_OK country=%s endpoint=%s exit=%s", view.Node.CountryCode, view.Node.IP, view.ExitIP)
			c, e := dialerInNetns(tunnel.nsName())("tcp", "example.com:80")
			if e != nil {
				t.Fatal("namespace DNS/TCP", e)
			}
			c.Close()
			if e := run("ip", "netns", "exec", tunnel.nsName(), "iptables", "-I", "OUTPUT", "1", "-p", "udp", "--dport", "53", "-j", "DROP"); e != nil {
				t.Fatal(e)
			}
			c, e = dialerInNetns(tunnel.nsName())("tcp", "example.com:80")
			if e != nil {
				t.Fatal("UDP DNS blocked; TCP fallback failed", e)
			}
			c.Close()
			if _, e := tunnel.probeExitIP(); e != nil {
				t.Fatal("health probe failed when only UDP DNS blocked", e)
			}
			t.Log("UDP_DNS_BLOCKED_TCP_FALLBACK_OK")
			checkLiveClientDNSAndWeb(t, tunnel)
			// Drop both DNS transports to the primary provider. Successful TCP
			// latency alone cannot detect this failure; require client DNS and
			// an actual verified HTTPS page through the same selected exit.
			if e := run("ip", "netns", "exec", tunnel.nsName(), "iptables", "-I", "OUTPUT", "1", "-d", "1.1.1.1", "-p", "tcp", "--dport", "53", "-j", "DROP"); e != nil {
				t.Fatal(e)
			}
			checkLiveClientDNSAndWeb(t, tunnel)
			t.Log("PRIMARY_DNS_AND_UDP_BLOCKED_CLIENT_DNS_HTTPS_OK")
			if e := run("ip", "netns", "exec", tunnel.nsName(), "iptables", "-D", "OUTPUT", "-d", "1.1.1.1", "-p", "tcp", "--dport", "53", "-j", "DROP"); e != nil {
				t.Fatal(e)
			}
			if e := run("ip", "netns", "exec", tunnel.nsName(), "iptables", "-D", "OUTPUT", "-p", "udp", "--dport", "53", "-j", "DROP"); e != nil {
				t.Fatal(e)
			}
			first := view.Node.HostName
			tunnel.mu.RLock()
			process := tunnel.ovpn.Process
			tunnel.mu.RUnlock()
			if e := process.Kill(); e != nil {
				t.Fatal(e)
			}
			failoverDeadline := time.Now().Add(4 * time.Minute)
			for time.Now().Before(failoverDeadline) {
				v := tunnel.view()
				if v.Status == "up" && v.Node.HostName != first {
					t.Logf("LIVE_FAILOVER_OK endpoint=%s exit=%s slot=%d port=%d", v.Node.IP, v.ExitIP, v.Slot, v.Port)
					return
				}
				time.Sleep(time.Second)
			}
			t.Fatal("automatic failover did not recover")
			return
		}
		if view.Status == "failed" {
			t.Fatalf("all live candidates failed: %s", view.Error)
		}
		time.Sleep(time.Second)
	}
	t.Fatal("live VPN deadline exceeded")
}

func checkLiveClientDNSAndWeb(t *testing.T, tunnel *Tunnel) {
	t.Helper()
	address := fmt.Sprintf("127.0.0.1:%d", tunnel.view().Port)
	for _, host := range []string{"192.168.1.1", "2001:4860:4860::8888"} {
		resolver := net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dnsTestPacketConn(address, socksTarget{host: host, port: 53})
		}}
		for _, network := range []string{"ip4", "ip6"} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			ips, err := resolver.LookupIP(ctx, network, "example.com")
			cancel()
			if err != nil || len(ips) == 0 {
				t.Fatalf("live mobile DNS endpoint=%s family=%s ips=%v err=%v", host, network, ips, err)
			}
			t.Logf("LIVE_CLIENT_DNS_OK endpoint=%s family=%s answers=%d", host, network, len(ips))
		}
	}
	proxy, _ := url.Parse("socks5://" + address)
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSHandshakeTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 25 * time.Second}
	response, err := client.Get("https://example.com/")
	if err != nil {
		t.Fatal("live HTTPS via hostname SOCKS CONNECT", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 65536))
	if err != nil || response.StatusCode != 200 || !strings.Contains(string(body), "Example Domain") {
		t.Fatalf("live HTTPS page status=%d read=%v", response.StatusCode, err)
	}
	t.Log("LIVE_CLIENT_HTTPS_PAGE_OK")
}
