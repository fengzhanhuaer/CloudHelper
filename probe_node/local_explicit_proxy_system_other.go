//go:build !windows

package main

import (
	"encoding/json"
	"os"
)

type probeLocalExplicitProxySystemBackup struct {
	Version   int               `json:"version"`
	UpdatedAt string            `json:"updated_at,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

func applyProbeLocalExplicitProxySystemSettingsPlatform(httpAddr string, socksAddr string) error {
	setProbeLocalExplicitProxyProcessEnv(httpAddr, socksAddr)
	return nil
}

func restoreProbeLocalExplicitProxySystemSettingsPlatform() error {
	path, err := resolveProbeLocalExplicitProxySystemBackupPath()
	if err != nil {
		return err
	}
	clearProbeLocalExplicitProxyProcessEnv()
	_ = os.Remove(path)
	return nil
}

func captureProbeLocalExplicitProxyProcessEnv() map[string]string {
	out := map[string]string{}
	for _, key := range probeLocalExplicitProxyEnvKeys() {
		out[key] = os.Getenv(key)
	}
	return out
}

func setProbeLocalExplicitProxyProcessEnv(httpAddr string, socksAddr string) {
	if httpAddr != "" {
		_ = os.Setenv("HTTP_PROXY", "http://"+httpAddr)
		_ = os.Setenv("HTTPS_PROXY", "http://"+httpAddr)
		_ = os.Setenv("http_proxy", "http://"+httpAddr)
		_ = os.Setenv("https_proxy", "http://"+httpAddr)
	}
	if socksAddr != "" {
		_ = os.Setenv("ALL_PROXY", "socks5://"+socksAddr)
		_ = os.Setenv("all_proxy", "socks5://"+socksAddr)
	}
	_ = os.Setenv("NO_PROXY", "localhost,127.0.0.1,::1")
	_ = os.Setenv("no_proxy", "localhost,127.0.0.1,::1")
}

func clearProbeLocalExplicitProxyProcessEnv() {
	restoreProbeLocalExplicitProxyProcessEnv(map[string]string{})
}

func restoreProbeLocalExplicitProxyProcessEnv(values map[string]string) {
	for _, key := range probeLocalExplicitProxyEnvKeys() {
		if values != nil {
			if value, ok := values[key]; ok && value != "" {
				_ = os.Setenv(key, value)
				continue
			}
		}
		_ = os.Unsetenv(key)
	}
}

func probeLocalExplicitProxyEnvKeys() []string {
	return []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"}
}

func mustProbeLocalJSON(value any) []byte {
	raw, _ := json.MarshalIndent(value, "", "  ")
	return raw
}
