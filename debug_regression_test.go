package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRouteTagsMustBeActualVLESSInboundTags(t *testing.T) {
	for _, source := range []string{
		`{"inbounds":[{"tag":"other","protocol":"vless","remark":"vless-reality-vision-in"}]}`,
		`{"inbounds":[{"tag":"vless-xhttp-in","protocol":"socks"}]}`,
	} {
		if _, err := transformXrayConfig([]byte(source), 31001); err == nil {
			t.Fatal("false inbound tag accepted")
		}
	}
	source := []byte(`{"inbounds":[{"tag":"vless-xhttp-in","protocol":"vless","remark":"vless-reality-vision-in"}]}`)
	output, err := transformXrayConfig(source, 31001)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	json.Unmarshal(output, &config)
	rule := config["routing"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if rule["inboundTag"].([]any)[0] != "vless-xhttp-in" {
		t.Fatal("wrong inbound selected")
	}
}

func TestHY2MarkerTextInSecretsAndUnknownOutboundsArePreserved(t *testing.T) {
	source := []byte("listen: :45678\nauth:\n  type: password\n  password: '" + hy2BlockBegin + "'\n")
	direct, err := transformHY2Config(source, 0)
	if err != nil || !bytes.Equal(source, direct) {
		t.Fatalf("secret changed: %s %v", direct, err)
	}
	if _, err := transformHY2Config(append(source, []byte("\"outbounds\": []\n")...), 31001); err == nil {
		t.Fatal("quoted unmanaged key overwritten")
	}
	if _, err := transformHY2Config(append(source, []byte(hy2BlockEnd+"\n")...), 0); err == nil {
		t.Fatal("orphan marker accepted")
	}
	duplicate := append(source, []byte(hy2BlockBegin+"\n"+hy2BlockEnd+"\n"+hy2BlockBegin+"\n"+hy2BlockEnd+"\n")...)
	if _, err := transformHY2Config(duplicate, 0); err == nil {
		t.Fatal("duplicate managed blocks accepted")
	}
}

func TestRecoveryBackupRetriesAfterWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	os.WriteFile(path, []byte("verified"), 0600)
	backup := path + ".fix-recovery"
	if err := os.Mkdir(backup, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := readConfigFile(path, validateNonempty); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, issue := range storageIssues() {
			if issue.Path == path {
				found = true
			}
		}
		if !found {
			t.Fatal("failed backup diagnostic cleared")
		}
	}
	if err := os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	readConfigFile(path, validateNonempty)
	os.WriteFile(path, nil, 0600)
	configFiles.Lock()
	delete(configFiles.good, path)
	configFiles.Unlock()
	got, err := readConfigFile(path, validateNonempty)
	if err != nil || string(got) != "verified" {
		t.Fatal("backup never retried", err)
	}
}

func TestConcurrentConfigSnapshotsMatchFinalCommittedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				if err := atomicWriteFile(path, []byte(fmt.Sprintf(`{"value":%d}`, value*8+j)), 0600); err != nil {
					t.Error(err)
					return
				}
				if _, err := readConfigFile(path, validateJSON); err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	primary, _ := os.ReadFile(path)
	backup, err := os.ReadFile(path + ".fix-recovery")
	var record recoveryRecord
	if err != nil || json.Unmarshal(backup, &record) != nil || !bytes.Equal(primary, record.Data) {
		t.Fatal("stale recovery snapshot")
	}
}

func TestInvalidCredentialsAndPathsCannotPoisonRecovery(t *testing.T) {
	dir := t.TempDir()
	password := filepath.Join(dir, "password")
	base := filepath.Join(dir, "basepath")
	os.WriteFile(password, []byte("healthy-password"), 0600)
	os.WriteFile(base, []byte("healthy-path"), 0600)
	if _, _, err := NewAuth(dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadBasePath(dir); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(password, []byte("short"), 0600)
	os.WriteFile(base, []byte("bad/path"), 0600)
	auth, _, err := NewAuth(dir)
	if err != nil || auth.password != "healthy-password" {
		t.Fatal("valid password lost", err)
	}
	recovered, _, err := LoadBasePath(dir)
	if err != nil || recovered != "/healthy-path" {
		t.Fatal("valid path lost", err)
	}
	if err := ensureReadableConfig(password, validatePassword); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(password)
		if info.Mode().Perm() != 0600 {
			t.Fatal("private file recovery widened permissions")
		}
	}
}

func TestFullLoginMapsDoNotLockOutValidPassword(t *testing.T) {
	auth := &Auth{password: "healthy-password", sessions: map[string]time.Time{}, failures: map[string]loginFailure{}}
	for i := 0; i < 4096; i++ {
		auth.failures[fmt.Sprint(i)] = loginFailure{Updated: time.Now()}
	}
	for i := 0; i < 1024; i++ {
		auth.sessions[fmt.Sprint(i)] = time.Now().Add(time.Hour)
	}
	request := httptest.NewRequest("POST", "https://panel.example/api/login", strings.NewReader(`{"password":"healthy-password"}`))
	response := httptest.NewRecorder()
	auth.login(response, request)
	if response.Code != 200 || len(auth.sessions) > 1024 || len(auth.failures) > 4096 {
		t.Fatalf("login locked out: %d", response.Code)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "https://panel.example/api/login", strings.NewReader(`{"password":"wrong"}`))
			req.RemoteAddr = fmt.Sprintf("198.51.100.%d:1234", index)
			auth.login(httptest.NewRecorder(), req)
		}(i)
	}
	wg.Wait()
	if len(auth.failures) > 4096 {
		t.Fatal("parallel failures exceeded memory bound")
	}
}

func TestSavedSlotsRegisteredTogetherAndSurviveLowerLimit(t *testing.T) {
	m := NewManager(1, t.TempDir(), nil)
	state := persistedState{Tunnels: []persistedTunnel{{Slot: 1, Port: 31001, HostName: "one", Config: "old"}, {Slot: 2, Port: 31002, HostName: "two", Config: "old"}}}
	encoded, _ := json.Marshal(state)
	os.WriteFile(m.statePath(), encoded, 0600)
	m.nodes = []Node{{HostName: "one", Config: "fresh"}}
	restored, err := m.registerSavedTunnels()
	if err != nil || len(restored) != 2 || len(m.Tunnels()) != 2 || restored[0].nodeNow().Config != "fresh" {
		t.Fatalf("lost saved slots: %v %v", restored, err)
	}
	if err := m.saveState(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(m.statePath())
	var actual persistedState
	json.Unmarshal(raw, &actual)
	if len(actual.Tunnels) != 2 {
		t.Fatal("partial restore snapshot")
	}
	again, err := m.registerSavedTunnels()
	if err != nil || len(again) != 0 {
		t.Fatal("restoration not idempotent")
	}
}

func TestUnrecoverableSavedStateCannotBeOverwrittenByNewExit(t *testing.T) {
	m := NewManager(2, t.TempDir(), nil)
	os.WriteFile(m.statePath(), []byte("null"), 0600)
	if _, err := m.registerSavedTunnels(); err == nil {
		t.Fatal("null state accepted")
	}
	if _, err := m.Start(Node{HostName: "new"}); err == nil {
		t.Fatal("new exit overwrote unrecovered state")
	}
	if err := m.saveState(); err == nil {
		t.Fatal("unrecovered state overwritten")
	}
	actual, _ := os.ReadFile(m.statePath())
	if string(actual) != "null" {
		t.Fatal("state changed")
	}
	os.WriteFile(m.statePath(), []byte(`{"tunnels":[]}`), 0600)
	if _, err := m.registerSavedTunnels(); err != nil {
		t.Fatal(err)
	}
	if err := m.saveState(); err != nil {
		t.Fatal("state did not recover", err)
	}
}

func TestStaleHealthAndSwapCannotChangeNewConnection(t *testing.T) {
	tunnel := newTunnel(1, 31001, Node{HostName: "old"})
	tunnel.setState("up", "1.1.1.1", "")
	old := tunnel.currentGeneration()
	tunnel.setState("starting", "", "")
	tunnel.replaceNode(Node{HostName: "new"})
	tunnel.setState("up", "8.8.8.8", "")
	if tunnel.publishHealthIP(old, "9.9.9.9") || tunnel.exitIPNow() != "8.8.8.8" {
		t.Fatal("old probe overwrote new IP")
	}
	m := NewManager(2, t.TempDir(), nil)
	if m.queueReconnectAt(tunnel, "old failed probe", nil, old) || tunnel.statusNow() != "up" {
		t.Fatal("old probe restarted new connection")
	}
	tunnel.setState("starting", "", "")
	other := Node{HostName: "another"}
	if m.queueReconnect(tunnel, "manual swap", &other) || tunnel.nodeNow().HostName != "new" {
		t.Fatal("rejected swap changed active metadata")
	}
}

func TestRouteRollbackReportsUnavailableOldService(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("shell command stubs")
	}
	bin := t.TempDir()
	path := filepath.Join(t.TempDir(), "service.conf")
	os.WriteFile(path, []byte("old"), 0600)
	os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\nexit 1\n"), 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	err := replaceAndRestart(path, []byte("new"), 0600, "fixture")
	if err == nil || !strings.Contains(err.Error(), "旧服务未恢复") {
		t.Fatal("rollback failure hidden", err)
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != "old" {
		t.Fatal("old configuration not restored")
	}
}

func TestBindWaitsForStopOperationBeforeChoosingTunnel(t *testing.T) {
	r := &Router{workDir: t.TempDir()}
	m := NewManager(2, t.TempDir(), r)
	tunnel := newTunnel(1, 31001, Node{HostName: "old"})
	tunnel.setState("up", "1.1.1.1", "")
	m.tunnels[1] = tunnel
	m.operationMu.Lock()
	entered, finished := make(chan struct{}), make(chan int, 1)
	go func() {
		close(entered)
		response := httptest.NewRecorder()
		apiBind(m, r)(response, httptest.NewRequest("POST", "/api/bind?protocol=vless&slot=1", nil))
		finished <- response.Code
	}()
	<-entered
	select {
	case code := <-finished:
		m.operationMu.Unlock()
		t.Fatalf("bind bypassed stop operation: %d", code)
	case <-time.After(50 * time.Millisecond):
	}
	m.mu.Lock()
	delete(m.tunnels, 1)
	m.mu.Unlock()
	tunnel.setState("stopped", "", "")
	m.operationMu.Unlock()
	select {
	case code := <-finished:
		if code != 502 || r.state.VLESS.Slot != 0 {
			t.Fatal("deleted exit bound")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("binding did not resume")
	}
}

func TestNodeWritesCannotBypassOtherConfigurationOperations(t *testing.T) {
	t.Setenv("DPS_CONFIG_LOCK", filepath.Join(t.TempDir(), "config.lock"))
	unlock, err := acquireConfigLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	service := NewNodeConfigService("must-not-execute", "unused")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := service.Apply(ctx, validRealityNodeConfig()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("save bypassed shared lock", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if err := service.Reinstall(ctx2, validRealityNodeConfig()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("reinstall bypassed shared lock", err)
	}
}

func TestInvalidBindingStateCannotSilentlyBecomeDirect(t *testing.T) {
	for _, invalid := range []string{`null`, `{}`, `{"vless":null,"hy2":{"slot":0}}`, `{"vless":{"slot":-1},"hy2":{"slot":0}}`, `{"vless":{"slot":null},"hy2":{"slot":0}}`, `{"vless":{"slot":201},"hy2":{"slot":0}}`} {
		if validateBindingState([]byte(invalid)) == nil {
			t.Fatalf("invalid binding accepted: %s", invalid)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bindings.json")
	good := []byte(`{"vless":{"slot":2},"hy2":{"slot":3}}`)
	os.WriteFile(path, good, 0600)
	if _, err := readConfigFile(path, validateBindingState); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("null"), 0600)
	router, err := NewRouter(dir, "unused", "unused", "unused")
	if err != nil || router.state.VLESS.Slot != 2 || router.state.HY2.Slot != 3 {
		t.Fatal("corrupt binding silently changed routing", err)
	}
}

func TestBrowserDisconnectKeepsTransactionButShutdownRunsRollback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unix process-group cancellation")
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	script := filepath.Join(dir, "backend.sh")
	entered := filepath.Join(dir, "entered")
	rolledBack := filepath.Join(dir, "rolled-back")
	fixture := validRealityNodeConfig()
	encoded, _ := json.Marshal(fixture)
	os.WriteFile(configPath, encoded, 0600)
	t.Setenv("DPS_TEST_CONFIG", configPath)
	t.Setenv("DPS_TEST_ENTERED", entered)
	t.Setenv("DPS_TEST_ROLLBACK", rolledBack)
	body := "#!/bin/sh\ncase \"$1\" in\nweb-export) cat \"$DPS_TEST_CONFIG\";;\nweb-apply) trap 'printf rollback > \"$DPS_TEST_ROLLBACK\"; exit 143' TERM; touch \"$DPS_TEST_ENTERED\"; while :; do sleep 1; done;;\nesac\n"
	os.WriteFile(script, []byte(body), 0700)
	service := NewNodeConfigService(script, "unused")
	manager := NewManager(2, dir, nil)
	defer manager.Shutdown()
	request := httptest.NewRequest("POST", "/api/node-config", bytes.NewReader(encoded))
	browserContext, disconnect := context.WithCancel(context.Background())
	request = request.WithContext(browserContext)
	disconnect()
	done := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		apiNodeConfig(service, manager, nil)(response, request)
		done <- response.Code
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(entered); err != nil {
		t.Fatal("browser disconnect aborted transaction", err)
	}
	manager.Shutdown()
	select {
	case code := <-done:
		if code != 409 {
			t.Fatal("cancelled transaction reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("transaction did not finish rollback")
	}
	if _, err := os.Stat(rolledBack); err != nil {
		t.Fatal("shutdown bypassed rollback", err)
	}
}

func TestReplacementPortCannotTakeAnotherStartingSlotsReservation(t *testing.T) {
	firstProbe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secondProbe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		firstProbe.Close()
		t.Fatal(err)
	}
	reservedPort := firstProbe.Addr().(*net.TCPAddr).Port
	replacementPort := secondProbe.Addr().(*net.TCPAddr).Port
	firstProbe.Close()
	secondProbe.Close()
	m := NewManager(2, t.TempDir(), nil)
	tunnel := newTunnel(1, 31001, Node{HostName: "one"})
	other := newTunnel(2, reservedPort, Node{HostName: "two"})
	m.tunnels[1] = tunnel
	m.tunnels[2] = other
	m.portPicker = func(taken map[int]bool) (int, error) {
		if !taken[reservedPort] {
			t.Fatal("starting slot reservation omitted")
		}
		return replacementPort, nil
	}
	listener, err := m.reserveReplacementListener(tunnel)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if tunnel.view().Port != replacementPort || other.view().Port != reservedPort {
		t.Fatal("wrong slot port changed")
	}
	if err := m.saveState(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(m.statePath())
	other.setPort(replacementPort)
	if err := m.saveState(); err == nil {
		t.Fatal("duplicate port persisted")
	}
	after, _ := os.ReadFile(m.statePath())
	if !bytes.Equal(before, after) {
		t.Fatal("invalid snapshot replaced healthy state")
	}
}
