//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real Go -> Bash flock/state/export/transaction integration. External proxy
// startup and ACME issuance are replaced; no production service is touched.
func nodeBackendFixture(t *testing.T, mode string) (*NodeConfigService, string) {
	t.Helper()
	if os.Getenv("DPS_RUN_INTEGRATION") != "1" {
		t.Skip("requires isolated Linux integration runner with Bash/Python/YAML")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("DPS_CONFIG_LOCK", filepath.Join(dir, "config.lock"))
	t.Setenv("DPS_FIXTURE_DIR", dir)
	t.Setenv("DPS_FIXTURE_ROOT", root)
	t.Setenv("DPS_FIXTURE_MODE", mode)
	// The real writer and validator are exercised when Caddy is installed.
	caddy, err := exec.LookPath("caddy")
	if err != nil {
		t.Fatal("integration runner must install Caddy", err)
	}
	t.Setenv("DPS_FIXTURE_CADDY", caddy)
	backend := filepath.Join(dir, "backend.sh")
	body := `#!/usr/bin/env bash
set -Eeuo pipefail
for module in node-common node-services node-transaction; do source "$DPS_FIXTURE_ROOT/lib/$module.sh"; done
SANDBOX="$DPS_FIXTURE_DIR"
STATE_DIR="$SANDBOX/state"; STATE_FILE="$STATE_DIR/state.env"
XRAY_CONFIG="$SANDBOX/xray/config.json"; HY2_CONFIG="$SANDBOX/hy2/config.yaml"
CADDYFILE="$SANDBOX/caddy/Caddyfile"; CADDY_BIN="$DPS_FIXTURE_CADDY"
OUT_DIR="$SANDBOX/outputs"; INFO_FILE="$OUT_DIR/node-info.txt"
XRAY_CLIENT_JSON="$OUT_DIR/xray-client.json"; HY2_CLIENT_JSON="$OUT_DIR/hy2-client.json"; HY2_CLIENT_YAML="$OUT_DIR/hy2-client.yaml"
STORAGE_HELPER="$DPS_FIXTURE_ROOT/scripts/storage.py"
WEB_ROOT="$SANDBOX/www"; HY2_WEB_ROOT="$SANDBOX/hy2-www"
mkdir -p "$STATE_DIR" "$(dirname "$XRAY_CONFIG")" "$(dirname "$HY2_CONFIG")" "$(dirname "$CADDYFILE")" "$OUT_DIR" "$WEB_ROOT" "$HY2_WEB_ROOT"
XRAY_BIN="$SANDBOX/fake-xray"
printf '#!/bin/sh\nexit 0\n' > "$XRAY_BIN"; chmod +x "$XRAY_BIN"
systemctl() { [[ ! -f "$SANDBOX/caddy-down" || "$*" != 'is-active --quiet caddy' ]]; }
start_xray() { :; }; start_hy2() { :; }; reality_local_self_test() { :; }
start_caddy() { echo start >> "$SANDBOX/caddy-starts"; }
ensure_hysteria_capabilities() { :; }
write_hy2_config_acme() { write_hy2_config_tls; }
grant_cert_read_permissions() { :; }
web_cleanup_replaced_domains() { :; }
web_wait_for_caddy_cert_only() { echo "$1" >> "$SANDBOX/caddy-issuance"; }
wait_for_caddy_cert() { HY2_CERT_PATH="$SANDBOX/cert"; HY2_KEY_PATH="$SANDBOX/key"; }
web_wait_for_hy2_acme_cert() { echo "$1" >> "$SANDBOX/hy2-issuance"; }
case "$1" in
init)
MAIN_MODE="$DPS_FIXTURE_MODE"; STACK_MODE="${MAIN_MODE}_hy2"
NODE_NAME=fixture; FP=chrome
REALITY_ADDRESS=203.0.113.10; REALITY_SNI=example.com; REALITY_UUID=123e4567-e89b-12d3-a456-426614174000
REALITY_PRIVATE_KEY=fixture-private; REALITY_PUBLIC_KEY=fixture-public; REALITY_SHORT_ID=abcdef
XHTTP_DOMAIN=x.example.com; XHTTP_PATH=/fixture; XHTTP_UUID="$REALITY_UUID"; XHTTP_BACKEND_PORT=10080
HY2_DOMAIN=h.example.com; HY2_PASSWORD=original-secret; HY2_PORT_MODE=single; HY2_PORT=45678
HY2_ACME_EMAIL=admin@example.com; HY2_OBFS_TYPE=none; HY2_OBFS_PASSWORD=''
HY2_GECKO_MIN_PACKET_SIZE=512; HY2_GECKO_MAX_PACKET_SIZE=1200
HY2_CERT_PATH="$SANDBOX/cert"; HY2_KEY_PATH="$SANDBOX/key"
if [[ "$MAIN_MODE" == xhttp ]]; then
  CADDY_REQUIRED=1; HY2_CERT_SOURCE=caddy; write_xhttp_xray_config; write_caddyfile_xhttp_hy2
else CADDY_REQUIRED=0; HY2_CERT_SOURCE=acme; write_reality_xray_config; fi
printf unchanged-certificate > "$HY2_CERT_PATH"; printf unchanged-key > "$HY2_KEY_PATH"
save_state; write_hy2_config_tls; generate_main_outputs ;;
web-export)
  if [[ -f "$SANDBOX/fail-export" ]]; then echo 'injected state export failure' >&2; exit 1; fi
  web_export_config ;;
web-apply)
  web_apply_config
  touch "$SANDBOX/applied"
  if [[ -f "$SANDBOX/fail-after-apply" ]]; then touch "$SANDBOX/fail-export"; fi ;;
esac
`
	if err := os.WriteFile(backend, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(backend, "init").CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	if out, err := exec.Command("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes", "-days", "3", "-subj", "/CN=h.example.com", "-addext", "subjectAltName=DNS:h.example.com", "-keyout", filepath.Join(dir, "key"), "-out", filepath.Join(dir, "cert")).CombinedOutput(); err != nil {
		t.Fatalf("fixture certificate: %v %s", err, out)
	}
	service := NewNodeConfigService(backend, "unused")
	t.Cleanup(func() { clearStorageIssue(backend) })
	return service, dir
}

func TestLinuxNodeSaveRealBackendLockAndReadback(t *testing.T) {
	for _, mode := range []string{"reality", "xhttp"} {
		t.Run(mode, func(t *testing.T) {
			service, dir := nodeBackendFixture(t, mode)
			beforeCert, _ := os.ReadFile(filepath.Join(dir, "cert"))
			beforeKey, _ := os.ReadFile(filepath.Join(dir, "key"))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			current, err := service.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// Confirm that an unaware reader really blocks on the parent's flock.
			unlock, err := acquireConfigLock(ctx)
			if err != nil {
				t.Fatal(err)
			}
			blockedCtx, stop := context.WithTimeout(context.Background(), 300*time.Millisecond)
			_, err = service.load(blockedCtx, false, false)
			stop()
			unlock()
			if err == nil {
				t.Fatal("fixture did not reproduce parent/child flock conflict")
			}
			current.NodeName = "保存后的节点"
			current.HY2Password = "new-secret-$()-quotes"
			restart, err := service.Apply(ctx, current)
			if err != nil || restart {
				t.Fatalf("save unchanged domains: restart=%v err=%v", restart, err)
			}
			service.cacheMu.RLock()
			updated := *service.lastGood
			service.cacheMu.RUnlock()
			if updated.NodeName != current.NodeName || updated.HY2Password != current.HY2Password || updated.Revision == current.Revision {
				t.Fatal("successful save did not refresh validated config/revision")
			}
			for _, issue := range storageIssues() {
				if issue.Path == service.nodeScript {
					t.Fatal("successful readback left sticky homepage warning", issue.Message)
				}
			}
			for _, name := range []string{"hy2-issuance", "caddy-issuance", "caddy-starts"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatalf("unchanged domain/save invoked %s", name)
				}
			}
			cert, _ := os.ReadFile(filepath.Join(dir, "cert"))
			key, _ := os.ReadFile(filepath.Join(dir, "key"))
			if string(cert) != string(beforeCert) || string(key) != string(beforeKey) {
				t.Fatal("save replaced cached cert/key")
			}
			// A changed domain invokes issuance only for that domain.
			updated.HY2Domain = "new-h.example.com"
			restart, err = service.Apply(ctx, updated)
			if err != nil || !restart {
				t.Fatalf("changed HY2 domain: restart=%v err=%v", restart, err)
			}
			name := "hy2-issuance"
			if mode == "xhttp" {
				name = "caddy-issuance"
			}
			issued, _ := os.ReadFile(filepath.Join(dir, name))
			if strings.TrimSpace(string(issued)) != "new-h.example.com" {
				t.Fatalf("incorrect issued domain(s): %q", issued)
			}
			if mode == "xhttp" {
				again, err := service.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				again.XHTTPDomain = "new-x.example.com"
				if _, err := service.Apply(ctx, again); err != nil {
					t.Fatal(err)
				}
				issued, _ = os.ReadFile(filepath.Join(dir, name))
				if strings.TrimSpace(string(issued)) != "new-h.example.com\nnew-x.example.com" {
					t.Fatalf("XHTTP change reissued unrelated domain: %q", issued)
				}
				caddy, _ := os.ReadFile(filepath.Join(dir, "caddy", "Caddyfile"))
				if !strings.Contains(string(caddy), "new-x.example.com {") || strings.Contains(string(caddy), "python3") {
					t.Fatal("real Caddy writer did not commit correct config")
				}
			}
			t.Log("SAVE_READBACK_CACHE_CERT_REUSE_AND_DOMAIN_CHANGE_OK")
		})
	}
}

func TestLinuxNodeMutationCannotUseStaleExport(t *testing.T) {
	service, dir := nodeBackendFixture(t, "reality")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	current, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "fail-export"), []byte("fail"), 0600)
	if _, err := service.Apply(ctx, current); err == nil || !strings.Contains(err.Error(), "injected state export failure") {
		t.Fatal("mutation trusted cached state or hid diagnostic", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "applied")); !os.IsNotExist(err) {
		t.Fatal("mutation ran after state read failed")
	}
	if _, err := service.Load(ctx); err != nil {
		t.Fatal("read-only UI lost last verified config", err)
	}
}

func TestLinuxNodeSaveCannotReportSuccessWithoutReadback(t *testing.T) {
	service, dir := nodeBackendFixture(t, "reality")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	current, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(current)
	os.WriteFile(filepath.Join(dir, "fail-after-apply"), []byte("fail"), 0600)
	current.HY2Password = "changed-secret"
	if _, err := service.Apply(ctx, current); err == nil || !strings.Contains(err.Error(), "读回校验失败") {
		t.Fatal("reported success without fresh readable state", err)
	}
	service.cacheMu.RLock()
	after, _ := json.Marshal(service.lastGood)
	service.cacheMu.RUnlock()
	if string(before) != string(after) {
		t.Fatal("unverified committed state was cached")
	}
}
