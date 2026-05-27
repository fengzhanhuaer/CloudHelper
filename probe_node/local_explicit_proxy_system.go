package main

import (
	"os"
	"strings"
	"sync"
	"time"
)

const probeLocalExplicitProxySystemBackupFileName = "explicit_proxy_system_backup.json"

var probeLocalExplicitProxySystemState = struct {
	mu        sync.Mutex
	applied   bool
	lastError string
	updatedAt string
}{}

func applyProbeLocalExplicitProxySystemSettings(httpAddr string, socksAddr string) error {
	var err error
	if !isProbeLocalTestBinary() {
		err = applyProbeLocalExplicitProxySystemSettingsPlatform(strings.TrimSpace(httpAddr), strings.TrimSpace(socksAddr))
	}
	probeLocalExplicitProxySystemState.mu.Lock()
	probeLocalExplicitProxySystemState.applied = err == nil
	probeLocalExplicitProxySystemState.lastError = ""
	if err != nil {
		probeLocalExplicitProxySystemState.lastError = strings.TrimSpace(err.Error())
	}
	probeLocalExplicitProxySystemState.updatedAt = time.Now().UTC().Format(time.RFC3339)
	probeLocalExplicitProxySystemState.mu.Unlock()
	return err
}

func restoreProbeLocalExplicitProxySystemSettings() error {
	var err error
	if !isProbeLocalTestBinary() {
		err = restoreProbeLocalExplicitProxySystemSettingsPlatform()
	}
	probeLocalExplicitProxySystemState.mu.Lock()
	if err == nil {
		probeLocalExplicitProxySystemState.applied = false
		probeLocalExplicitProxySystemState.lastError = ""
	} else {
		probeLocalExplicitProxySystemState.lastError = strings.TrimSpace(err.Error())
	}
	probeLocalExplicitProxySystemState.updatedAt = time.Now().UTC().Format(time.RFC3339)
	probeLocalExplicitProxySystemState.mu.Unlock()
	return err
}

func snapshotProbeLocalExplicitProxySystemSettingsStatus() map[string]any {
	probeLocalExplicitProxySystemState.mu.Lock()
	defer probeLocalExplicitProxySystemState.mu.Unlock()
	return map[string]any{
		"applied":    probeLocalExplicitProxySystemState.applied,
		"last_error": strings.TrimSpace(probeLocalExplicitProxySystemState.lastError),
		"updated_at": strings.TrimSpace(probeLocalExplicitProxySystemState.updatedAt),
	}
}

func resolveProbeLocalExplicitProxySystemBackupPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return joinProbeLocalPath(dataDir, probeLocalExplicitProxySystemBackupFileName), nil
}

func isProbeLocalTestBinary() bool {
	name := strings.ToLower(strings.TrimSpace(os.Args[0]))
	return strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".test.exe")
}
