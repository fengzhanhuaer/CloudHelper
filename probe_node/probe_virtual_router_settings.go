package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const probeVirtualRouterLocalSettingsFileName = "probe_virtual_router_settings.json"

type probeVirtualRouterLocalSettings struct {
	VirtualRouterEnabled bool   `json:"virtual_router_enabled"`
	VirtualDNSEnabled    bool   `json:"virtual_dns_enabled"`
	UpdatedAt            string `json:"updated_at,omitempty"`
}

var probeVirtualRouterLocalSettingsState = struct {
	mu       sync.RWMutex
	loaded   bool
	settings probeVirtualRouterLocalSettings
}{}

func defaultProbeVirtualRouterLocalSettings() probeVirtualRouterLocalSettings {
	return probeVirtualRouterLocalSettings{
		VirtualRouterEnabled: true,
		VirtualDNSEnabled:    true,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339),
	}
}

func resolveProbeVirtualRouterLocalSettingsPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeVirtualRouterLocalSettingsFileName), nil
}

func loadProbeVirtualRouterLocalSettings() probeVirtualRouterLocalSettings {
	probeVirtualRouterLocalSettingsState.mu.RLock()
	if probeVirtualRouterLocalSettingsState.loaded {
		settings := probeVirtualRouterLocalSettingsState.settings
		probeVirtualRouterLocalSettingsState.mu.RUnlock()
		return settings
	}
	probeVirtualRouterLocalSettingsState.mu.RUnlock()

	settings := defaultProbeVirtualRouterLocalSettings()
	path, err := resolveProbeVirtualRouterLocalSettingsPath()
	if err == nil {
		if raw, readErr := os.ReadFile(path); readErr == nil && len(strings.TrimSpace(string(raw))) > 0 {
			var payload struct {
				VirtualRouterEnabled *bool  `json:"virtual_router_enabled"`
				VirtualDNSEnabled    *bool  `json:"virtual_dns_enabled"`
				UpdatedAt            string `json:"updated_at,omitempty"`
			}
			if jsonErr := json.Unmarshal(raw, &payload); jsonErr == nil {
				if payload.VirtualRouterEnabled != nil {
					settings.VirtualRouterEnabled = *payload.VirtualRouterEnabled
				}
				if payload.VirtualDNSEnabled != nil {
					settings.VirtualDNSEnabled = *payload.VirtualDNSEnabled
				}
				settings.UpdatedAt = strings.TrimSpace(payload.UpdatedAt)
				if settings.UpdatedAt == "" {
					settings.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				}
			}
		}
	}
	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.settings = settings
	probeVirtualRouterLocalSettingsState.loaded = true
	probeVirtualRouterLocalSettingsState.mu.Unlock()
	return settings
}

func saveProbeVirtualRouterLocalSettings(settings probeVirtualRouterLocalSettings) (probeVirtualRouterLocalSettings, error) {
	settings.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	path, err := resolveProbeVirtualRouterLocalSettingsPath()
	if err != nil {
		return settings, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return settings, err
	}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return settings, err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return settings, err
	}
	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.settings = settings
	probeVirtualRouterLocalSettingsState.loaded = true
	probeVirtualRouterLocalSettingsState.mu.Unlock()
	reconcileProbeLocalDNSRuntime()
	if settings.VirtualRouterEnabled {
		scheduleProbeVirtualRouterLocalInterfaceIPEnsure("local_settings_updated")
	} else if err := cleanupProbeVirtualRouterPlatformRoutes(); err != nil {
		logProbeWarnf("cleanup virtual router platform routes after settings update failed: %v", err)
	}
	return settings, nil
}

func probeVirtualRouterLocalEntryEnabled() bool {
	return loadProbeVirtualRouterLocalSettings().VirtualRouterEnabled
}

func probeVirtualRouterLocalDNSEnabled() bool {
	return loadProbeVirtualRouterLocalSettings().VirtualDNSEnabled
}

func resetProbeVirtualRouterLocalSettingsForTest() {
	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.loaded = false
	probeVirtualRouterLocalSettingsState.settings = probeVirtualRouterLocalSettings{}
	probeVirtualRouterLocalSettingsState.mu.Unlock()
}
