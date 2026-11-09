package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newRouteFixture(t *testing.T, bin, inboundTag, stream, flow string) *Router {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DPS_CONFIG_LOCK", filepath.Join(dir, "config.lock"))
	xrayPath, hy2Path := filepath.Join(dir, "config.json"), filepath.Join(dir, "config.yaml")
	xray := fmt.Sprintf(`{
  "outbounds": [{"tag":"direct","protocol":"freedom"}],
  "inbounds": [{
    "tag":%q,"listen":"127.0.0.1","port":10443,"protocol":"vless",
    "settings":{"clients":[{"id":"6adfd606-8362-4a56-90a1-0f74884e7c32","flow":%q}],"decryption":"none"},
    "streamSettings":%s
  }]
}
`, inboundTag, flow, stream)
	for path, content := range map[string]string{xrayPath: xray, hy2Path: "listen: :10444\nauth:\n  type: password\n  password: fixture\n"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewRouter(filepath.Join(dir, "state"), xrayPath, hy2Path, bin)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func readRouteFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

const xhttpRouteFixture = `{"network":"xhttp","security":"none","xhttpSettings":{"path":"/fixture","mode":"auto"}}`

func TestXrayDirectStartupDoesNotRewriteConfig(t *testing.T) {
	r := newRouteFixture(t, filepath.Join(t.TempDir(), "nonexistent-xray"), "vless-xhttp-in", xhttpRouteFixture, "")
	before := readRouteFixture(t, r.xrayConfig)
	if err := r.Apply(nil); err != nil {
		t.Fatalf("startup tried to validate or restart an unchanged direct route: %v", err)
	}
	if after := readRouteFixture(t, r.xrayConfig); !bytes.Equal(before, after) {
		t.Fatal("startup changed the direct configuration")
	}
}

// Run against an official Xray binary with a service command stub: no host
// services are restarted, and no VPN Gate connection is needed.
func TestXrayRouteCompatibility(t *testing.T) {
	bin := os.Getenv("DPS_TEST_XRAY")
	if bin == "" {
		t.Skip("set DPS_TEST_XRAY to the path of an official Xray binary")
	}
	binDir := t.TempDir()
	switch runtime.GOOS {
	case "linux":
		if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	case "windows":
		stub := os.Getenv("DPS_TEST_SYSTEMCTL")
		if stub == "" {
			t.Skip("set DPS_TEST_SYSTEMCTL to an executable that exits successfully")
		}
		if err := os.WriteFile(filepath.Join(binDir, "systemctl.exe"), readRouteFixture(t, stub), 0700); err != nil {
			t.Fatal(err)
		}
	default:
		t.Skip("no service fixture for this platform")
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	realityStream := fmt.Sprintf(`{"network":"raw","security":"reality","realitySettings":{
		"target":"example.com:443","serverNames":["example.com"],"privateKey":%q,"shortIds":["fedcba9876543210"]}}`,
		base64.RawURLEncoding.EncodeToString(key.Bytes()))
	for _, tc := range []struct{ name, tag, stream, flow string }{
		{"XHTTP", "vless-xhttp-in", xhttpRouteFixture, ""},
		{"REALITY", "vless-reality-vision-in", realityStream, "xtls-rprx-vision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRouteFixture(t, bin, tc.tag, tc.stream, tc.flow)
			slot := newTunnel(1, 23232, Node{})
			slot.setState("up", "203.0.113.1", "")
			tunnels := []*Tunnel{slot}
			for _, protocol := range []string{"vless", "hy2"} {
				if err := r.Bind(protocol, 1, tunnels); err != nil {
					t.Fatalf("bind %s: %v", protocol, err)
				}
			}
			oldXray, oldHY2, oldBinding := readRouteFixture(t, r.xrayConfig), readRouteFixture(t, r.hy2Config), readRouteFixture(t, r.bindingPath)
			if !bytes.Contains(oldXray, []byte(managedXrayTag)) || !bytes.Contains(oldXray, []byte(`"port": 23232`)) ||
				!bytes.Contains(oldHY2, []byte("addr: 127.0.0.1:23232")) {
				t.Fatal("binding did not install the managed routes")
			}
			// Simulate panel startup after losing its last applied route, with
			// the saved binding and restored tunnel still present.
			direct, err := transformXrayConfig(oldXray, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(r.xrayConfig, direct, 0600); err != nil {
				t.Fatal(err)
			}
			r, err = NewRouter(r.workDir, r.xrayConfig, r.hy2Config, bin)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Apply(tunnels); err != nil || !bytes.Equal(oldXray, readRouteFixture(t, r.xrayConfig)) {
				t.Fatalf("startup did not restore the saved route: %v", err)
			}
			bad := newTunnel(2, 65536, Node{})
			bad.setState("up", "203.0.113.2", "")
			// If the failure path tries to reapply slot 1, it would either
			// change the live route or fail preflight for a second time.
			slot.Port = 65537
			err = r.Bind("vless", 2, append(tunnels, bad))
			slot.Port = 23232
			if !errors.Is(err, errXrayRoutePreflight) || strings.Contains(err.Error(), "恢复旧路由") {
				t.Fatalf("wanted a single preflight failure: %v", err)
			}
			for path, before := range map[string][]byte{r.xrayConfig: oldXray, r.hy2Config: oldHY2, r.bindingPath: oldBinding} {
				if !bytes.Equal(before, readRouteFixture(t, path)) {
					t.Fatalf("preflight failure changed %s", path)
				}
			}
			if r.state.VLESS.Slot != 1 || r.state.HY2.Slot != 1 {
				t.Fatal("failed bind changed live binding state")
			}
			if err := r.DropSlot(1, tunnels); err != nil {
				t.Fatalf("unbind: %v", err)
			}
			if bytes.Contains(readRouteFixture(t, r.xrayConfig), []byte(managedXrayTag)) ||
				bytes.Contains(readRouteFixture(t, r.hy2Config), []byte(hy2BlockBegin)) ||
				r.state.VLESS.Slot != 0 || r.state.HY2.Slot != 0 || r.lastError != "" {
				t.Fatal("unbind did not restore direct routing")
			}
			if files, err := filepath.Glob(filepath.Join(filepath.Dir(r.xrayConfig), ".xray-route-*")); err != nil || len(files) != 0 {
				t.Fatalf("temporary Xray configs were not removed: %v (%v)", files, err)
			}
		})
	}
}
