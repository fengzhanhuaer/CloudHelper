//go:build mihomo_exit

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	originalWorkingDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root, err := os.MkdirTemp("", "cloudhelper-mihomo-exit-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Chdir(root); err != nil {
		_ = os.RemoveAll(root)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("PROBE_NODE_DATA_DIR", filepath.Join(root, "data"))
	code := m.Run()
	_ = os.Chdir(originalWorkingDir)
	_ = os.RemoveAll(root)
	os.Exit(code)
}
