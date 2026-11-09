package main

import (
	"log"
	"sort"
	"time"
)

const (
	healthInterval = 10 * time.Second
	healthFailures = 2
)

func (m *Manager) WatchHealth() {
	failures := map[*Tunnel]int{}
	next := map[*Tunnel]time.Time{}
	inflight := map[*Tunnel]bool{}
	checkedGeneration := map[*Tunnel]uint64{}
	type check struct {
		t          *Tunnel
		generation uint64
		ok         bool
	}
	results := make(chan check, 4)
	tasks := make(chan check, 4)
	for i := 0; i < 4; i++ {
		go func() {
			for {
				select {
				case <-m.stopHealth:
					return
				case task := <-tasks:
					task.ok = m.tunnelHealthy(task.t, task.generation)
					select {
					case results <- task:
					case <-m.stopHealth:
						return
					}
				}
			}
		}()
	}
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()
	refresh := time.NewTicker(5 * time.Minute)
	defer refresh.Stop()
	for {
		select {
		case <-m.stopHealth:
			return
		case <-refresh.C:
			go func() {
				if _, err := m.RefreshNodes(); err != nil {
					log.Printf("刷新出口池失败，保留已加载列表: %v", err)
				}
			}()
		case c := <-results:
			delete(inflight, c.t)
			if c.t.currentGeneration() != c.generation {
				delete(next, c.t)
				delete(failures, c.t)
				continue
			}
			if checkedGeneration[c.t] != c.generation {
				delete(failures, c.t)
				checkedGeneration[c.t] = c.generation
			}
			next[c.t] = time.Now().Add(healthInterval)
			if c.t.statusNow() != "up" {
				continue
			}
			if c.ok {
				failures[c.t] = 0
				continue
			}
			failures[c.t]++
			if failures[c.t] >= healthFailures {
				failures[c.t] = 0
				m.queueReconnectAt(c.t, "健康检查失败，自动切换同地区节点", nil, c.generation)
			}
		case <-ticker.C:
			m.mu.RLock()
			restoreErr := m.restoreError
			m.mu.RUnlock()
			if restoreErr != nil {
				if _, err := m.restoreState(); err != nil {
					log.Printf("出口状态仍不可恢复: %v", err)
				}
			}
			tunnels := m.Tunnels()
			live := map[*Tunnel]bool{}
			bound := map[int]bool{}
			if m.router != nil {
				m.router.mu.Lock()
				bound[m.router.state.VLESS.Slot] = true
				bound[m.router.state.HY2.Slot] = true
				m.router.mu.Unlock()
			}
			sort.SliceStable(tunnels, func(i, j int) bool {
				a, b := tunnels[i], tunnels[j]
				if bound[a.Slot] != bound[b.Slot] {
					return bound[a.Slot]
				}
				return next[a].Before(next[b])
			})
			for _, t := range tunnels {
				live[t] = true
				t.mu.RLock()
				status, retry, done, generation := t.status, t.nextRetry, t.ovpnDone, t.generation
				t.mu.RUnlock()
				if status == "failed" && !time.Now().Before(retry) {
					m.reconnect(t, "自动重试可用候选")
					continue
				}
				if status != "up" {
					continue
				}
				if done != nil {
					select {
					case <-done:
						m.queueReconnectAt(t, "OpenVPN 已退出，立即更换候选", nil, generation)
						continue
					default:
					}
				}
				if !inflight[t] && !time.Now().Before(next[t]) && len(inflight) < 4 {
					inflight[t] = true
					tasks <- check{t: t, generation: t.currentGeneration()}
				}
			}
			for t := range next {
				if !live[t] {
					delete(failures, t)
					delete(next, t)
					delete(checkedGeneration, t)
				}
			}
		}
	}
}
func (m *Manager) tunnelHealthy(t *Tunnel, generation uint64) bool {
	t.mu.RLock()
	done := t.ovpnDone
	t.mu.RUnlock()
	if done != nil {
		select {
		case <-done:
			return false
		default:
		}
	}
	ip, err := t.probeExitIP()
	if err != nil {
		return false
	}
	return t.publishHealthIP(generation, ip)
}
