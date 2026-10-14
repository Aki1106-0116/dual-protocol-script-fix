package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func LoadBasePath(dir string) (string, bool, error) {
	path := filepath.Join(dir, "basepath")
	if data, err := readConfigFile(path, validateBasePath); err == nil {
		base := normalizeBasePath(string(data))
		if !panelPathPattern.MatchString(strings.TrimPrefix(base, "/")) {
			return "", false, fmt.Errorf("管理路径文件为空")
		}
		return base, false, nil
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	token := randomHex(12)
	if token == "" {
		return "", false, fmt.Errorf("无法生成安全随机访问路径")
	}
	base := "/" + token
	if err := atomicWriteFile(path, []byte(strings.TrimPrefix(base, "/")+"\n"), 0600); err != nil {
		return "", false, err
	}
	return base, true, nil
}

func validateBasePath(data []byte) error {
	if !panelPathPattern.MatchString(strings.TrimPrefix(normalizeBasePath(string(data)), "/")) {
		return fmt.Errorf("管理路径文件格式无效")
	}
	return nil
}

func normalizeBasePath(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "/")
	if value == "" {
		return ""
	}
	return "/" + value
}

func StripBasePath(base string, next http.Handler) http.Handler {
	base = normalizeBasePath(base)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == base {
			http.Redirect(w, req, base+"/", http.StatusTemporaryRedirect)
			return
		}
		if !strings.HasPrefix(req.URL.Path, base+"/") {
			http.NotFound(w, req)
			return
		}
		req.URL.Path = strings.TrimPrefix(req.URL.Path, base)
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		next.ServeHTTP(w, req)
	})
}
