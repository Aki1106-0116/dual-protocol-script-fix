package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStoppedTunnelClosesTrackedClients(t *testing.T) {
	tunnel := newTunnel(1, 0, Node{})
	a, b := net.Pipe()
	defer b.Close()
	tunnel.clients[a] = struct{}{}
	tunnel.closeClients()
	if _, e := a.Write([]byte("x")); e == nil {
		t.Fatal("client survived stop/swap")
	}
	if len(tunnel.clients) != 0 {
		t.Fatal("retained closed clients")
	}
}
func TestLateSOCKSListenerCannotSurviveStop(t *testing.T) {
	tunnel := newTunnel(1, 0, Node{})
	tunnel.stopped = true
	if e := tunnel.ensureSOCKS(); e == nil {
		t.Fatal("late listener accepted")
	}
	if tunnel.listener != nil {
		t.Fatal("late listener retained")
	}
}
func TestStoppingSlotCannotBeReusedUntilCleanupFinishes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unix stub executables")
	}
	originalRoot := netnsConfigRoot
	netnsConfigRoot = t.TempDir()
	t.Cleanup(func() { netnsConfigRoot = originalRoot })
	bin := t.TempDir()
	for _, name := range []string{"ip", "iptables"} {
		if e := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0700); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	m := NewManager(2, t.TempDir(), nil)
	tunnel := newTunnel(1, 12345, Node{})
	m.tunnels[1] = tunnel
	tunnel.lifeMu.Lock()
	done := make(chan error, 1)
	go func() { done <- m.Stop(1) }()
	deadline := time.Now().Add(2 * time.Second)
	for tunnel.statusNow() != "stopped" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	m.mu.Lock()
	slot, e := m.freeSlotLocked()
	m.mu.Unlock()
	tunnel.lifeMu.Unlock()
	if e != nil || slot != 2 {
		t.Fatalf("slot reused before cleanup: %d %v", slot, e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	m.mu.Lock()
	slot, e = m.freeSlotLocked()
	m.mu.Unlock()
	if e != nil || slot != 1 {
		t.Fatalf("slot never released: %d %v", slot, e)
	}
}

func TestCorruptConfigKeepsVerifiedSnapshotAndShowsDiagnostic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xray.json")
	good := []byte(`{"inbounds":[{"tag":"vless-reality-vision-in","protocol":"vless"}]}`)
	if e := os.WriteFile(path, good, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := readConfigFile(path, validateXray); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(`{"inbounds":`), 0600); e != nil {
		t.Fatal(e)
	}
	got, e := readConfigFile(path, validateXray)
	if e != nil || !bytes.Equal(got, good) {
		t.Fatalf("fallback: %s %v", got, e)
	}
	stack, _ := detectStack(path)
	if stack != "reality_hy2" {
		t.Fatal(stack)
	}
	found := false
	for _, issue := range storageIssues() {
		if issue.Path == path {
			found = true
		}
	}
	if !found {
		t.Fatal("missing storage diagnostic")
	}
	if e := ensureReadableConfig(path, validateXray); e != nil {
		t.Fatal(e)
	}
	actual, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(actual, good) {
		t.Fatal("repair did not read back")
	}
}
func TestDeletedConfigNeverResurrectedFromBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if e := atomicWriteFile(path, []byte("good"), 0600); e != nil {
		t.Fatal(e)
	}
	os.Remove(path)
	if _, e := readConfigFile(path, validateNonempty); !os.IsNotExist(e) {
		t.Fatalf("deleted state restored: %v", e)
	}
}
func TestRecoverySurvivesNewProcessCacheAndRejectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	good := []byte("last verified")
	atomicWriteFile(path, good, 0600)
	os.WriteFile(path, nil, 0600)
	configFiles.Lock()
	delete(configFiles.good, path)
	configFiles.Unlock()
	got, e := readConfigFile(path, validateNonempty)
	if e != nil || !bytes.Equal(got, good) {
		t.Fatal("disk backup failed", e)
	}
	os.WriteFile(path+".fix-recovery", []byte(`{"path":"wrong","data":"eA==","sha256":"invalid"}`), 0600)
	if _, e := readConfigFile(path, validateNonempty); e == nil {
		t.Fatal("tampered snapshot accepted")
	}
}
func TestUnrecoverableStorageDoesNotInventNodeStack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	os.Mkdir(path, 0700)
	stack, _ := detectStack(path)
	if stack != "storage_error" {
		t.Fatal(stack)
	}
	if e := ensureReadableConfig(path, validateXray); e == nil {
		t.Fatal("unsafe restart allowed")
	}
}
func TestHY2RejectsDuplicateAndInvalidYAML(t *testing.T) {
	for _, text := range []string{"listen: :1234\nauth: [broken", "listen: :1234\nauth: {}\nauth: {}\n"} {
		if _, e := transformHY2Config([]byte(text), 1234); e == nil {
			t.Fatal("invalid YAML accepted")
		}
	}
}
func TestBoundMissingExitDoesNotFallBackToDirect(t *testing.T) {
	if boundPort(4, nil) != -1 {
		t.Fatal("missing exit fell back")
	}
	if boundPort(0, nil) != 0 {
		t.Fatal("direct broken")
	}
}

func fixtureVPNProfile(t *testing.T) Node {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test VPN"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	priv := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return Node{HostName: "vpn-test", IP: "203.0.113.20", CountryCode: "JP", Config: "client\ndev tun\nproto tcp\nremote vpn-test.opengw.net 443\ncipher AES-128-CBC\n<ca>\n" + cert + "</ca>\n<cert>\n" + cert + "</cert>\n<key>\n" + priv + "</key>\n"}
}
func TestVPNProfileUsesIPOwnRoutesAndCertificateVerification(t *testing.T) {
	n := fixtureVPNProfile(t)
	p, e := normalizeVPNProfile(n)
	if e != nil {
		t.Fatal(e)
	}
	for _, line := range []string{"remote 203.0.113.20 443", "redirect-gateway def1", "remote-cert-tls server", "data-ciphers-fallback AES-128-CBC", "allow-compression no"} {
		if !strings.Contains(p.Config, line) {
			t.Fatal("missing", line)
		}
	}
	if strings.Contains(p.Config, "opengw.net") {
		t.Fatal("stale DDNS retained")
	}
}

func TestCertificateCompatibilityOnlyForLocalClientSignatureFailure(t *testing.T) {
	if !legacyClientCertificateFailure("openssl ca md too weak: cannot load inline certificate file") {
		t.Fatal("specific compatibility case not detected")
	}
	for _, message := range []string{"AUTH_FAILED", "VERIFY ERROR: certificate has expired", "VERIFY ERROR: ca md too weak", "TLS handshake failed"} {
		if legacyClientCertificateFailure(strings.ToLower(message)) {
			t.Fatal("unsafe certificate downgrade", message)
		}
	}
}
func TestVPNProfileRejectsRootHooksAndFileReads(t *testing.T) {
	n := fixtureVPNProfile(t)
	for _, directive := range []string{"plugin /evil.so", "config /etc/shadow", "up /tmp/evil", "management 0.0.0.0 1234", "log /etc/passwd", "ca /etc/shadow", "script-security 3"} {
		candidate := n
		candidate.Config += "\n" + directive + "\n"
		if _, e := normalizeVPNProfile(candidate); e == nil {
			t.Fatal("unsafe directive accepted", directive)
		}
	}
}
func TestCandidatesSkipFailedNodesAndUseFreshConfiguration(t *testing.T) {
	m := NewManager(3, t.TempDir(), nil)
	first := Node{HostName: "one", CountryCode: "JP", Config: "stale"}
	m.nodes = []Node{{HostName: "one", CountryCode: "JP", Config: "fresh"}, {HostName: "two", CountryCode: "JP"}, {HostName: "three", CountryCode: "US"}}
	got := m.candidatesFor(first)
	if len(got) != 2 || got[0].Config != "fresh" {
		t.Fatalf("%#v", got)
	}
	m.markBad("one")
	got = m.candidatesFor(first)
	if len(got) != 1 || got[0].HostName != "two" {
		t.Fatalf("%#v", got)
	}
}
func TestStoppedTunnelCannotBeRevivedByLateCompletion(t *testing.T) {
	tunnel := newTunnel(1, 12345, Node{})
	tunnel.mu.Lock()
	tunnel.stopped = true
	close(tunnel.done)
	tunnel.status = "stopped"
	tunnel.mu.Unlock()
	tunnel.setState("up", "1.1.1.1", "")
	if tunnel.statusNow() != "stopped" {
		t.Fatal("late completion resurrected tunnel")
	}
}
func TestFailedTunnelBackoffIsBounded(t *testing.T) {
	tunnel := newTunnel(1, 12345, Node{})
	for i := 0; i < 10; i++ {
		start := time.Now()
		tunnel.setState("failed", "", "retry")
		delay := tunnel.nextRetry.Sub(start)
		if delay < 19*time.Second || delay > 321*time.Second {
			t.Fatal(delay)
		}
	}
}
func TestAuthRejectsCrossSiteMutationAndEmptyPassword(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "password"), []byte("\n"), 0600)
	if _, _, e := NewAuth(dir); e == nil {
		t.Fatal("empty password accepted")
	}
	os.WriteFile(filepath.Join(dir, "password"), []byte("test-secret"), 0600)
	auth, _, e := NewAuth(dir)
	if e != nil {
		t.Fatal(e)
	}
	called := false
	handler := auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	req := httptest.NewRequest("POST", "https://panel.example/api/login", strings.NewReader(`{"password":"test-secret"}`))
	req.Header.Set("Origin", "https://attacker.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 403 || called {
		t.Fatal("cross-site accepted")
	}
}
func TestPanelBadRequestNeverChangesPassword(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "panel.env")
	os.WriteFile(env, []byte("WEB_PORT=30000\nMAX_EXITS=12\nPANEL_DOMAIN=h.example.com\n"), 0600)
	os.WriteFile(filepath.Join(dir, "basepath"), []byte("abcdefghi"), 0600)
	os.WriteFile(filepath.Join(dir, "password"), []byte("old-secret"), 0600)
	s := NewPanelConfigService(dir, env, "unused", "unused")
	_, e := s.Apply(PanelConfig{Port: 30000, Path: "invalid/../../", MaxExits: 12, NewPassword: "new-secret"})
	if e == nil {
		t.Fatal("invalid path accepted")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "password"))
	if string(data) != "old-secret" {
		t.Fatal("password changed")
	}
}
func TestRemovedFeatureFieldsRejectedByAPI(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"tcp_brutal_enabled":true}`))
	var config NodeConfig
	if e := decodeOneJSON(httptest.NewRecorder(), req, 4096, &config); e == nil {
		t.Fatal("retired API field accepted")
	}
	config = validRealityNodeConfig()
	config.HY2ObfsType = "mimic"
	if e := validateNodeConfig(config); e == nil {
		t.Fatal("retired mode accepted")
	}
	data, _ := json.Marshal(config)
	if strings.Contains(string(data), "tcp_brutal") {
		t.Fatal("retired field exported")
	}
}
func TestConfigurationLockCanBeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if unlock, e := acquireConfigLock(ctx); e == nil {
		unlock()
		t.Fatal("cancelled lock acquired")
	}
}

func TestInvalidPanelEnvironmentDoesNotReplaceRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.env")
	good := []byte("WEB_PORT=30000\nMAX_EXITS=12\nPANEL_DOMAIN=h.example.com\n")
	if err := os.WriteFile(path, good, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSimpleEnvironment(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("WEB_PORT=broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := readSimpleEnvironment(path)
	if err != nil || values["WEB_PORT"] != "30000" {
		t.Fatalf("lost verified environment: %v %v", values, err)
	}
	if err := ensureReadableConfig(path, validatePanelEnvironment); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != string(good) {
		t.Fatal("invalid environment replaced recovery")
	}
}
