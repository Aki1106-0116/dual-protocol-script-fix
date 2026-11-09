package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

var version = "dev"

const (
	defaultWorkDir    = "/var/lib/dual-protocol-script-fix"
	defaultXrayConfig = "/usr/local/etc/xray/config.json"
	defaultHY2Config  = "/etc/hysteria/config.yaml"
	defaultXrayBin    = "/usr/local/bin/xray"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "keypair":
			runKeypairCommand(os.Args[2:])
			return
		case "apply":
			runApplyCommand(os.Args[2:])
			return
		case "status":
			runStatusCommand(os.Args[2:])
			return
		}
	}
	runServer()
}

func runKeypairCommand(args []string) {
	flags := flag.NewFlagSet("keypair", flag.ExitOnError)
	xray := flags.String("xray", defaultXrayBin, "Xray 可执行文件")
	_ = flags.Parse(args)
	pair, err := generateRealityKeyPair(*xray)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("PRIVATE=%s\nPUBLIC=%s\n", pair.Private, pair.Public)
}

func commonPaths(name string, args []string) (*flag.FlagSet, *string, *string, *string, *string) {
	flags := flag.NewFlagSet(name, flag.ExitOnError)
	workDir := flags.String("dir", defaultWorkDir, "工作目录")
	xrayConfig := flags.String("xray-config", defaultXrayConfig, "Xray 配置")
	hy2Config := flags.String("hy2-config", defaultHY2Config, "Hysteria2 配置")
	xrayBin := flags.String("xray-bin", defaultXrayBin, "Xray 可执行文件")
	_ = flags.Parse(args)
	return flags, workDir, xrayConfig, hy2Config, xrayBin
}

func runApplyCommand(args []string) {
	if os.Geteuid() != 0 {
		log.Fatal("apply 需要 root 权限")
	}
	_, workDir, xrayConfig, hy2Config, xrayBin := commonPaths("apply", args)
	router, err := NewRouter(*workDir, *xrayConfig, *hy2Config, *xrayBin)
	if err != nil {
		log.Fatal(err)
	}
	tunnels, err := loadPersistedTunnels(*workDir)
	if err != nil {
		log.Fatal(err)
	}
	if err := router.Apply(tunnels); err != nil {
		log.Fatal(err)
	}
	fmt.Println("协议出口路由已同步")
}

func runStatusCommand(args []string) {
	_, workDir, xrayConfig, hy2Config, xrayBin := commonPaths("status", args)
	router, err := NewRouter(*workDir, *xrayConfig, *hy2Config, *xrayBin)
	if err != nil {
		log.Fatal(err)
	}
	tunnels, err := loadPersistedTunnels(*workDir)
	if err != nil {
		log.Fatal(err)
	}
	data, _ := json.MarshalIndent(router.Status(tunnels), "", "  ")
	fmt.Println(string(data))
}

func loadPersistedTunnels(workDir string) ([]*Tunnel, error) {
	data, err := readConfigFile(filepath.Join(workDir, "tunnels.json"), validateTunnelState)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	var tunnels []*Tunnel
	for _, saved := range state.Tunnels {
		node := Node{
			HostName: saved.HostName, IP: saved.IP,
			CountryCode: saved.CountryCode, Country: saved.Country, Config: saved.Config,
		}
		tunnel := newTunnel(saved.Slot, saved.Port, node)
		conn, probeErr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(saved.Port)), 300*time.Millisecond)
		if probeErr == nil {
			_ = conn.SetDeadline(time.Now().Add(300 * time.Millisecond))
			_, probeErr = conn.Write([]byte{5, 1, 0})
			reply := make([]byte, 2)
			if probeErr == nil {
				_, probeErr = io.ReadFull(conn, reply)
				if probeErr == nil && (reply[0] != 5 || reply[1] != 0) {
					probeErr = fmt.Errorf("不是受支持的 SOCKS5 服务")
				}
			}
			_ = conn.Close()
		}
		if probeErr == nil {
			tunnel.setState("up", "", "本机 SOCKS5 已响应；具体出口健康状态请查看面板")
		} else {
			tunnel.setState("unknown", "", "持久化记录；本机 SOCKS5 未响应，请查看面板连接状态")
		}
		tunnels = append(tunnels, tunnel)
	}
	return tunnels, nil
}

func runServer() {
	flags := flag.NewFlagSet("dual-protocol-script-fix", flag.ExitOnError)
	webPort := flags.Int("web", 0, "Web 管理 TLS 端口")
	panelDomain := flags.String("domain", "", "Web 面板 TLS 域名")
	tlsCert := flags.String("tls-cert", "", "Web 面板证书文件")
	tlsKey := flags.String("tls-key", "", "Web 面板私钥文件")
	maxSlots := flags.Int("max", 12, "最多同时运行的家宽出口")
	workDir := flags.String("dir", defaultWorkDir, "工作目录")
	xrayConfig := flags.String("xray-config", defaultXrayConfig, "Xray 配置")
	hy2Config := flags.String("hy2-config", defaultHY2Config, "Hysteria2 配置")
	xrayBin := flags.String("xray-bin", defaultXrayBin, "Xray 可执行文件")
	showVersion := flags.Bool("version", false, "显示版本")
	_ = flags.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println("dual-protocol-script-fix", version)
		return
	}
	if os.Geteuid() != 0 {
		log.Fatal("服务需要 root 权限（创建 netns 与管理 iptables）")
	}
	if *maxSlots < 1 || *maxSlots > 200 {
		log.Fatal("-max 必须在 1-200 之间")
	}
	if *webPort < 1024 || *webPort > 65535 {
		log.Fatal("-web 必须是 1024-65535 之间的 TLS 端口")
	}
	certificates, err := newCertificateReloader(*panelDomain, *tlsCert, *tlsKey)
	if err != nil {
		log.Fatalf("初始化面板 TLS 失败: %v", err)
	}
	if err := os.MkdirAll(*workDir, 0700); err != nil {
		log.Fatalf("创建工作目录失败: %v", err)
	}
	if err := prepareHost(); err != nil {
		log.Fatal(err)
	}
	router, err := NewRouter(*workDir, *xrayConfig, *hy2Config, *xrayBin)
	if err != nil {
		log.Fatalf("初始化路由管理器失败: %v", err)
	}
	manager := NewManager(*maxSlots, *workDir, router)
	restoredTunnels, restoreErr := manager.registerSavedTunnels()
	if restoreErr != nil {
		log.Printf("恢复出口状态失败，保留管理入口并等待存储恢复: %v", restoreErr)
	}
	nodeConfig := NewNodeConfigService(defaultNodeInstaller, defaultPanelSync)
	panelConfig := NewPanelConfigService(*workDir, defaultPanelEnvFile, defaultPanelSync, defaultServiceName)
	go func() {
		primeCtx, primeCancel := context.WithTimeout(context.Background(), 20*time.Second)
		if _, err := nodeConfig.Load(primeCtx); err != nil {
			log.Printf("节点状态初次读取失败: %v", err)
		}
		primeCancel()
		if _, err := panelConfig.Load(); err != nil {
			log.Printf("面板配置初次读取失败: %v", err)
		}
		if count, err := manager.RefreshNodes(); err != nil {
			log.Printf("首次拉取 VPN Gate 列表失败，可稍后从 Web 刷新: %v", err)
		} else {
			log.Printf("已载入 %d 个 VPN Gate 节点", count)
		}
		for _, tunnel := range restoredTunnels {
			go manager.bringUp(tunnel)
		}
		if len(restoredTunnels) > 0 {
			log.Printf("正在恢复 %d 个家宽出口", len(restoredTunnels))
		}
		if err := router.Apply(manager.Tunnels()); err != nil {
			log.Printf("首次同步协议路由失败: %v", err)
		}
	}()
	go manager.WatchHealth()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-manager.stopHealth:
				return
			case <-ticker.C:
				if err := router.Apply(manager.Tunnels()); err != nil {
					log.Printf("路由配置巡检失败: %v", err)
				}
			}
		}
	}()

	auth, created, err := NewAuth(*workDir)
	if err != nil {
		log.Fatalf("初始化面板口令失败: %v", err)
	}
	if created {
		log.Printf("已生成面板口令: %s", filepath.Join(*workDir, "password"))
	}
	basePath, created, err := LoadBasePath(*workDir)
	if err != nil {
		log.Fatalf("初始化随机访问路径失败: %v", err)
	}
	if created {
		log.Printf("已生成随机访问路径: %s", filepath.Join(*workDir, "basepath"))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/status", apiStatus(manager, router))
	mux.HandleFunc("/api/regions", apiRegions(manager))
	mux.HandleFunc("/api/refresh", apiRefresh(manager))
	mux.HandleFunc("/api/provision", apiProvision(manager))
	mux.HandleFunc("/api/jobs", apiJobs(manager))
	mux.HandleFunc("/api/jobs/dismiss", apiJobDismiss(manager))
	mux.HandleFunc("/api/swap", apiSwap(manager))
	mux.HandleFunc("/api/stop", apiStop(manager))
	mux.HandleFunc("/api/bind", apiBind(manager, router))
	mux.HandleFunc("/api/node-config", apiNodeConfig(nodeConfig, manager, router))
	mux.HandleFunc("/api/node-links", apiNodeLinks(defaultNodeOutputDir))
	mux.HandleFunc("/api/node-reinstall", apiNodeReinstall(nodeConfig, manager, router))
	mux.HandleFunc("/api/panel-config", apiPanelConfig(panelConfig))
	mux.HandleFunc("/api/services/restart", apiRestartServices(router))

	addr := fmt.Sprintf(":%d", *webPort)
	log.Printf("管理面板: https://%s%s%s/", *panelDomain, addr, basePath)
	server := &http.Server{
		Addr:              addr,
		Handler:           StripBasePath(basePath, auth.Wrap(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: certificates.GetCertificate,
			NextProtos:     []string{"h2", "http/1.1"},
		},
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	shutdownDone := make(chan struct{})
	go func() {
		<-stop
		log.Println("停止接收请求，取消节点事务并等待回滚，清理运行态 netns")
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cleanupDone := make(chan struct{})
		go func() { manager.Shutdown(); close(cleanupDone) }()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("等待管理请求退出超时: %v", err)
		}
		select {
		case <-cleanupDone:
		case <-ctx.Done():
			log.Println("出口清理未在停机时限内完成")
		}
		close(shutdownDone)
	}()
	if err := server.ListenAndServeTLS("", ""); err != nil {
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}
	<-shutdownDone
}
