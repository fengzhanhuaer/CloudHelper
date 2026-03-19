//go:build windows

package main

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const probeWintunRelativePath = "temp/Lib/wintun/amd64/wintun.dll"

//go:embed lib/wintun/amd64/wintun.dll
var embeddedProbeWintunAMD64 []byte

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
	targetPath, err := resolveProbeWintunPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(targetPath, embeddedProbeWintunAMD64, 0o644)
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
