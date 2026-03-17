//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func restartCurrentProcess(executablePath string) error {
	exePath := strings.TrimSpace(executablePath)
	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			return err
		}
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && resolved != "" {
		exePath = resolved
	}
	return syscall.Exec(exePath, os.Args, os.Environ())
}
