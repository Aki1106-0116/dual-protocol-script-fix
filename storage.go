package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Reads never turn an I/O failure into an empty/default configuration. Last
// verified bytes remain available for viewing; mutations require a readable,
// verified on-disk configuration and successful durable writes.
type recoveryRecord struct {
	Path string `json:"path"`
	Hash string `json:"sha256"`
	Data []byte `json:"data"`
}
type StorageIssue struct {
	Path    string    `json:"path"`
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

var configFiles = struct {
	sync.Mutex
	good   map[string][]byte
	backed map[string]string
	issues map[string]StorageIssue
}{good: map[string][]byte{}, backed: map[string]string{}, issues: map[string]StorageIssue{}}

var fileGuards = struct {
	sync.Mutex
	locks map[string]*sync.Mutex
}{locks: map[string]*sync.Mutex{}}

func lockConfigFile(path string) func() {
	fileGuards.Lock()
	guard := fileGuards.locks[path]
	if guard == nil {
		guard = &sync.Mutex{}
		fileGuards.locks[path] = guard
	}
	fileGuards.Unlock()
	guard.Lock()
	return guard.Unlock
}

const maxConfigBytes = 4 << 20

func readSmallFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return nil, fmt.Errorf("配置不是普通文件或超过 4 MiB: %s", path)
	}
	data := make([]byte, info.Size())
	n, err := f.ReadAt(data, 0)
	if err != nil && n != len(data) {
		return nil, err
	}
	return data, nil
}
func rememberFile(path string, data []byte) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	configFiles.Lock()
	backed := configFiles.backed[path] == hash
	configFiles.good[path] = append([]byte(nil), data...)
	delete(configFiles.issues, path)
	configFiles.Unlock()
	if backed {
		return
	}
	record, _ := json.Marshal(recoveryRecord{Path: path, Hash: hash, Data: data})
	if err := atomicWriteRaw(path+".fix-recovery", record, 0600); err != nil {
		recordStorageIssue(path, "无法更新恢复副本: "+err.Error())
	} else {
		configFiles.Lock()
		configFiles.backed[path] = hash
		configFiles.Unlock()
	}
}
func recordStorageIssue(path, message string) {
	configFiles.Lock()
	defer configFiles.Unlock()
	issue := configFiles.issues[path]
	if issue.Since.IsZero() {
		issue.Since = time.Now()
	}
	issue.Path = path
	issue.Message = message
	configFiles.issues[path] = issue
}

func clearStorageIssue(path string) {
	configFiles.Lock()
	delete(configFiles.issues, path)
	configFiles.Unlock()
}
func storageIssues() []StorageIssue {
	configFiles.Lock()
	defer configFiles.Unlock()
	out := make([]StorageIssue, 0, len(configFiles.issues))
	for _, v := range configFiles.issues {
		out = append(out, v)
	}
	return out
}
func readConfigFile(path string, validate func([]byte) error) ([]byte, error) {
	unlock := lockConfigFile(path)
	defer unlock()
	return readConfigFileLocked(path, validate)
}

func readConfigFileLocked(path string, validate func([]byte) error) ([]byte, error) {
	data, err := readSmallFile(path)
	if err == nil {
		err = validate(data)
		if err == nil {
			rememberFile(path, data)
			return data, nil
		}
	}
	// A deliberate removal is never resurrected by stale backups.
	if os.IsNotExist(err) {
		configFiles.Lock()
		delete(configFiles.good, path)
		delete(configFiles.backed, path)
		configFiles.Unlock()
		return nil, err
	}
	cause := err
	configFiles.Lock()
	cached := append([]byte(nil), configFiles.good[path]...)
	configFiles.Unlock()
	if len(cached) == 0 {
		raw, e := readSmallFile(path + ".fix-recovery")
		if e == nil {
			var rec recoveryRecord
			if json.Unmarshal(raw, &rec) == nil && rec.Path == path {
				sum := sha256.Sum256(rec.Data)
				if rec.Hash == hex.EncodeToString(sum[:]) {
					cached = rec.Data
				}
			}
		}
	}
	if len(cached) > 0 && validate(cached) == nil {
		recordStorageIssue(path, "读取失败，正在展示最后一次验证的配置；写入/重启前必须恢复磁盘可读性: "+cause.Error())
		return cached, nil
	}
	recordStorageIssue(path, "配置读取失败，未修改运行中的节点: "+cause.Error())
	return nil, fmt.Errorf("%s: %w；请检查磁盘/文件系统与系统日志", path, cause)
}
func ensureReadableConfig(path string, validate func([]byte) error) error {
	unlock := lockConfigFile(path)
	defer unlock()
	data, err := readSmallFile(path)
	if err == nil && validate(data) == nil {
		return nil
	}
	if os.IsNotExist(err) {
		return err
	}
	recovered, e := readConfigFileLocked(path, validate)
	if e != nil {
		return e
	}
	mode := os.FileMode(0600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	if e = atomicWriteRaw(path, recovered, mode); e != nil {
		return fmt.Errorf("磁盘配置无法恢复，已保留运行中的服务，禁止重启: %w", e)
	}
	actual, e := readSmallFile(path)
	if e != nil || !bytes.Equal(actual, recovered) {
		return errors.New("恢复后配置无法读回，已禁止重启；请修复服务器磁盘")
	}
	rememberFile(path, actual)
	return nil
}
func validateXray(data []byte) error {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	in, _ := root["inbounds"].([]any)
	if len(in) == 0 {
		return errors.New("Xray 缺少入站")
	}
	// Decode exact tags rather than treating a tag inside comments/other keys as
	// evidence of a supported inbound when selecting a service combination.
	return nil
}
func validateHY2(data []byte) error { _, err := transformHY2Config(data, 0); return err }
func validateNonempty(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("配置为空")
	}
	return nil
}
func validateJSON(data []byte) error {
	if !json.Valid(data) {
		return errors.New("JSON 无效")
	}
	return nil
}
func atomicWriteRaw(path string, data []byte, mode os.FileMode) error {
	service := ""
	if path == defaultXrayConfig {
		service = "xray"
	}
	if path == defaultHY2Config {
		service = "hysteria-server"
	}
	if service != "" {
		mode = 0600
	}
	temp, err := writeSiblingTemp(path, data, mode)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	if service != "" && runtime.GOOS == "linux" {
		output, err := exec.Command("systemctl", "show", service, "-p", "User", "--value").Output()
		if err != nil {
			return fmt.Errorf("读取 %s 服务用户失败，未提交私密配置: %w", service, err)
		}
		user := strings.TrimSpace(string(output))
		if user != "" && user != "root" {
			if output, err := exec.Command("setfacl", "-m", "u:"+user+":r", temp).CombinedOutput(); err != nil {
				return fmt.Errorf("设置 %s 配置访问权限失败: %v: %s", service, err, output)
			}
		}
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
