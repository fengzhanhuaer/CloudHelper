//go:build windows

package main

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const probeWintunRelativePath = "temp/Lib/wintun/amd64/wintun.dll"

//go:embed lib/wintun/amd64/wintun.dll
var embeddedProbeWintunAMD64 []byte

var (
	probeWintunPathResolver = resolveProbeWintunPath
	probeWintunMkdirAll     = os.MkdirAll
	probeWintunWriteFile    = os.WriteFile
	probeWintunReadFile     = os.ReadFile
)

func ensureProbeEmbeddedWintunLibrary() error {
	if runtime.GOARCH != "amd64" {
		return nil
	}
	if len(embeddedProbeWintunAMD64) == 0 {
		return errors.New("embedded probe wintun.dll is empty")
	}
	if len(embeddedProbeWintunAMD64) < 2 || !bytes.Equal(embeddedProbeWintunAMD64[:2], []byte{'M', 'Z'}) {
		return errors.New("embedded probe wintun.dll has invalid pe header")
	}
	targetPath, err := probeWintunPathResolver()
	if err != nil {
		return err
	}
	if err := probeWintunMkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	if probeWintunBinaryUsable(targetPath) {
		return nil
	}
	if err := probeWintunWriteFile(targetPath, embeddedProbeWintunAMD64, 0o644); err != nil {
		if isProbeWintunFileInUseErr(err) && probeWintunBinaryUsable(targetPath) {
			return nil
		}
		return err
	}
	return nil
}

func resolveProbeWintunPath() (string, error) {
	candidates := make([]string, 0, 2)
	if exePath, err := os.Executable(); err == nil && exePath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(exePath), filepath.FromSlash(probeWintunRelativePath)))
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		candidates = append(candidates, filepath.Join(wd, filepath.FromSlash(probeWintunRelativePath)))
	}
	for _, candidate := range candidates {
		absPath, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		return absPath, nil
	}
	return "", errors.New("failed to resolve probe runtime temp directory")
}

func probeWintunBinaryUsable(path string) bool {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return false
	}
	raw, err := probeWintunReadFile(cleanPath)
	if err != nil {
		return false
	}
	if len(raw) < 2 {
		return false
	}
	return bytes.Equal(raw[:2], []byte{'M', 'Z'})
}

func isProbeWintunFileInUseErr(err error) bool {
	if err == nil {
		return false
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.Errno(32) || errno == syscall.Errno(5)
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return false
	}
	return strings.Contains(text, "being used by another process") || strings.Contains(text, "used by another process")
}
