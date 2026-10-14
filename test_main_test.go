package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Tests coordinate through an owned temporary lock, never the running
// installation's /run lock. Production paths and permissions are unchanged.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dps-test-lock-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("DPS_CONFIG_LOCK", filepath.Join(dir, "config.lock")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
