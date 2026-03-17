//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	cmd := exec.Command(exePath, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
