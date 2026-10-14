package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func requirePost(w http.ResponseWriter, req *http.Request) bool {
	if req.Method == http.MethodPost {
		return true
	}
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只允许 POST"})
	return false
}

func apiStatus(manager *Manager, router *Router) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		tunnels := manager.Tunnels()
		writeJSON(w, http.StatusOK, map[string]any{
			"router": router.Status(tunnels), "exits": sortedTunnelViews(tunnels), "storage_issues": storageIssues(), "max_exits": manager.maxSlots,
		})
	}
}

func apiRegions(manager *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, manager.Regions())
	}
}

func apiRefresh(manager *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !requirePost(w, req) {
			return
		}
		count, err := manager.RefreshNodes()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"count": count})
	}
}

func apiProvision(manager *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !requirePost(w, req) {
			return
		}
		count, err := strconv.Atoi(req.URL.Query().Get("count"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "count 参数无效"})
			return
		}
		job, err := manager.Provision(ProvisionRequest{Region: req.URL.Query().Get("region"), Count: count})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"job": job.ID()})
	}
}

func apiJobs(manager *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, manager.jobs.Views())
	}
}

func apiJobDismiss(manager *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !requirePost(w, req) {
			return
		}
		manager.jobs.Dismiss(req.URL.Query().Get("id"))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func apiSwap(manager *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !requirePost(w, req) {
			return
		}
		slot, err := strconv.Atoi(req.URL.Query().Get("slot"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slot 参数无效"})
			return
		}
		if err := manager.Swap(slot); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "正在切换"})
	}
}

func apiStop(manager *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !requirePost(w, req) {
			return
		}
		slot, err := strconv.Atoi(req.URL.Query().Get("slot"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slot 参数无效"})
			return
		}
		if err := manager.Stop(slot); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func apiBind(manager *Manager, router *Router) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !requirePost(w, req) {
			return
		}
		protocol := strings.ToLower(req.URL.Query().Get("protocol"))
		slot, err := strconv.Atoi(req.URL.Query().Get("slot"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slot 参数无效"})
			return
		}
		if err := manager.Bind(protocol, slot); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func apiNodeConfig(service *NodeConfigService, manager *Manager, router *Router) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			ctx, cancel := context.WithTimeout(req.Context(), 15*time.Second)
			defer cancel()
			config, err := service.Load(ctx)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, config)
		case http.MethodPost:
			var config NodeConfig
			decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 32<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&config); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "节点配置请求无效: " + err.Error()})
				return
			}
			if err := decoder.Decode(&struct{}{}); err != io.EOF {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "节点配置请求只能包含一个 JSON 对象"})
				return
			}

			// Configuration writes are server-side transactions. Do not abort the
			// installer (and bypass its rollback trap) merely because the browser
			// closed the page or changed networks while ACME was still running.
			ctx, cancel := context.WithTimeout(manager.lifetime, 12*time.Minute)
			defer cancel()
			panelRestart, err := service.Apply(ctx, config)
			if err != nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			result := map[string]any{
				"ok":            true,
				"panel_restart": panelRestart,
			}
			if err := router.Apply(manager.Tunnels()); err != nil {
				result["warning"] = "节点服务已保存并重启，但家宽出口绑定暂未恢复，将由 15 秒巡检重试: " + err.Error()
			}
			if panelRestart {
				result["panel_domain"] = config.HY2Domain
				service.SchedulePanelSync()
			}
			writeJSON(w, http.StatusOK, result)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只允许 GET 或 POST"})
		}
	}
}

func apiNodeLinks(outputDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只允许 GET"})
			return
		}
		links, err := LoadNodeLinks(outputDir)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, links)
	}
}

func apiPanelConfig(service *PanelConfigService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			config, err := service.Load()
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, config)
		case http.MethodPost:
			var config PanelConfig
			if err := decodeOneJSON(w, req, 16<<10, &config); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "面板配置请求无效: " + err.Error()})
				return
			}
			updated, err := service.Apply(config)
			if err != nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "domain": updated.Domain, "port": updated.Port,
				"path": updated.Path, "max_exits": updated.MaxExits,
			})
			service.ScheduleRestart()
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只允许 GET 或 POST"})
		}
	}
}

func apiNodeReinstall(service *NodeConfigService, manager *Manager, router *Router) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !requirePost(w, req) {
			return
		}
		var config NodeConfig
		if err := decodeOneJSON(w, req, 32<<10, &config); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "重装配置请求无效: " + err.Error()})
			return
		}
		// Reinstallation must either commit or run the installer's rollback even
		// when the requesting browser disconnects midway through the operation.
		ctx, cancel := context.WithTimeout(manager.lifetime, 18*time.Minute)
		defer cancel()
		if err := service.Reinstall(ctx, config); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		result := map[string]any{
			"ok": true, "panel_restart": true, "panel_domain": config.HY2Domain,
		}
		if err := router.Apply(manager.Tunnels()); err != nil {
			result["warning"] = "协议组合已重装，但家宽出口绑定将由巡检重试: " + err.Error()
		}
		writeJSON(w, http.StatusOK, result)
		// The certificate source may change between Caddy and HY2 ACME even if
		// the domain did not change, so always resync and restart the panel.
		service.SchedulePanelSync()
	}
}

func decodeOneJSON(w http.ResponseWriter, req *http.Request, limit int64, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}

func apiRestartServices(router *Router) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !requirePost(w, req) {
			return
		}
		var input struct {
			Service string `json:"service"`
		}
		if err := decodeOneJSON(w, req, 4096, &input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "重启请求无效: " + err.Error()})
			return
		}
		ctxLock, cancelLock := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelLock()
		unlock, lockErr := acquireConfigLock(ctxLock)
		if lockErr != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": lockErr.Error()})
			return
		}
		defer unlock()
		stack, _ := detectStack(router.xrayConfig)
		services := []string{}
		switch strings.TrimSpace(strings.ToLower(input.Service)) {
		case "xray":
			services = []string{"xray"}
		case "hysteria2", "hysteria-server":
			services = []string{"hysteria-server"}
		case "caddy":
			if stack != "xhttp_hy2" {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "当前 REALITY 组合不安装 Caddy"})
				return
			}
			services = []string{"caddy"}
		case "all":
			if stack != "xhttp_hy2" && stack != "reality_hy2" {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "当前节点组合未识别，拒绝批量重启"})
				return
			}
			services = []string{"xray"}
			if stack == "xhttp_hy2" {
				services = append(services, "caddy")
			}
			services = append(services, "hysteria-server")
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "只允许重启 Xray、Hysteria2、Caddy 或当前组合全部服务"})
			return
		}

		for _, service := range services {
			path := router.xrayConfig
			validate := validateXray
			if service == "hysteria-server" {
				path = router.hy2Config
				validate = validateHY2
			}
			if service == "caddy" {
				path = "/etc/caddy/Caddyfile"
				validate = validateNonempty
			}
			if err := ensureReadableConfig(path, validate); err != nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
		}

		ctx, cancel := context.WithTimeout(req.Context(), 90*time.Second)
		defer cancel()
		results := make(map[string]string, len(services))
		for _, service := range services {
			output, restartErr := exec.CommandContext(ctx, "systemctl", "restart", service).CombinedOutput()
			activeErr := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", service).Run()
			if restartErr != nil || activeErr != nil {
				message := commandMessage(output)
				if message == "" {
					message = fmt.Sprintf("restart=%v active=%v", restartErr, activeErr)
				}
				writeJSON(w, http.StatusBadGateway, map[string]any{
					"error":   service + " 重启或健康检查失败: " + message,
					"results": results,
				})
				return
			}
			results[service] = "active"
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stack": stack, "results": results})
	}
}
