package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHTTPSPanelAuthenticationAssetsAndStatus(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "password"), []byte("test-secret"), 0600)
	xray := filepath.Join(dir, "xray.json")
	hy2 := filepath.Join(dir, "hy2.yaml")
	os.WriteFile(xray, []byte(`{"inbounds":[{"tag":"vless-reality-vision-in","protocol":"vless"}],"outbounds":[]}`), 0600)
	os.WriteFile(hy2, []byte("listen: :1234\nauth:\n  type: password\n  password: test\n"), 0600)
	router, e := NewRouter(dir, xray, hy2, "unused")
	if e != nil {
		t.Fatal(e)
	}
	manager := NewManager(12, dir, router)
	auth, _, e := NewAuth(dir)
	if e != nil {
		t.Fatal(e)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/status", apiStatus(manager, router))
	server := httptest.NewTLSServer(StripBasePath("/abcdefghi", auth.Wrap(mux)))
	defer server.Close()
	client := server.Client()
	get := func(path string, cookie *http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", server.URL+path, nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		r, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := get("/wrong/api/status", nil)
	if r.StatusCode != 404 {
		t.Fatal("random path protection missing")
	}
	r.Body.Close()
	r = get("/abcdefghi/api/status", nil)
	if r.StatusCode != 401 {
		t.Fatal("unauthenticated status exposed")
	}
	r.Body.Close()
	r = get("/abcdefghi/assets/index.js", nil)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/javascript; charset=utf-8" {
		t.Fatal("asset delivery failed")
	}
	r.Body.Close()
	r, e = client.Post(server.URL+"/abcdefghi/api/login", "application/json", bytes.NewBufferString(`{"password":"test-secret"}`))
	if e != nil {
		t.Fatal(e)
	}
	cookies := r.Cookies()
	r.Body.Close()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("cookie security missing")
	}
	r = get("/abcdefghi/api/status", cookies[0])
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	var response struct {
		Router RouterStatus `json:"router"`
		Max    int          `json:"max_exits"`
	}
	if r.StatusCode != 200 || json.Unmarshal(data, &response) != nil || response.Router.Stack != "reality_hy2" || response.Max != 12 {
		t.Fatalf("bad status %s", data)
	}
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("sensitive response cached")
	}
}

func BenchmarkUnchangedConfigurationRead(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "xray.json")
	data := []byte(`{"inbounds":[{"tag":"vless-reality-vision-in","protocol":"vless"}],"outbounds":[]}`)
	os.WriteFile(path, data, 0600)
	readConfigFile(path, validateXray)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, e := readConfigFile(path, validateXray); e != nil {
			b.Fatal(e)
		}
	}
}
