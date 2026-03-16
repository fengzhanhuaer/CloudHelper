//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

func restartCurrentProcess() error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && resolved != "" {
		exePath = resolved
	}
	return syscall.Exec(exePath, os.Args, os.Environ())
}
