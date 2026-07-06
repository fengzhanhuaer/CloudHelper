//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const probeVirtualRouterDNSBackupFileName = "virtual_router_dns_backup.json"

type probeVirtualRouterDNSBackup struct {
	InterfaceGUID  string   `json:"interface_guid"`
	InterfaceIndex int      `json:"interface_index"`
	DNSServers     []string `json:"dns_servers"`
	AppliedDNS     []string `json:"applied_dns"`
	UpdatedAt      string   `json:"updated_at"`
}

func applyProbeVirtualRouterSystemDNS() error {
	adapter, err := probeLocalResolveWindowsPrimaryDNSAdapter(currentProbeVirtualRouterTUNIfIndex())
	if err != nil {
		return err
	}
	if strings.TrimSpace(adapter.AdapterGUID) == "" {
		return errors.New("primary dns adapter guid is empty")
	}
	desired := []string{probeVirtualRouterDNSListenHost}
	if sameProbeVirtualRouterDNSServers(adapter.DNSServers, desired) {
		return nil
	}
	backup, ok := loadProbeVirtualRouterDNSBackupBestEffort()
	if !ok || !strings.EqualFold(strings.TrimSpace(backup.InterfaceGUID), strings.TrimSpace(adapter.AdapterGUID)) {
		backup = probeVirtualRouterDNSBackup{
			InterfaceGUID:  adapter.AdapterGUID,
			InterfaceIndex: adapter.InterfaceIndex,
			DNSServers:     dedupeProbeLocalIPv4Strings(adapter.DNSServers),
			AppliedDNS:     desired,
			UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
		}
		if err := persistProbeVirtualRouterDNSBackup(backup); err != nil {
			return err
		}
	}
	if err := probeLocalSetWindowsInterfaceDNS(adapter.AdapterGUID, desired); err != nil {
		return err
	}
	logProbeInfof("probe virtual router system dns applied: if_index=%d dns=%s", adapter.InterfaceIndex, strings.Join(desired, ","))
	return nil
}

func restoreProbeVirtualRouterSystemDNS() error {
	backup, ok := loadProbeVirtualRouterDNSBackupBestEffort()
	if !ok {
		return nil
	}
	if strings.TrimSpace(backup.InterfaceGUID) == "" || len(backup.DNSServers) == 0 {
		_ = removeProbeVirtualRouterDNSBackup()
		return nil
	}
	if err := probeLocalSetWindowsInterfaceDNS(backup.InterfaceGUID, backup.DNSServers); err != nil {
		return err
	}
	_ = removeProbeVirtualRouterDNSBackup()
	logProbeInfof("probe virtual router system dns restored: if_index=%d dns=%s", backup.InterfaceIndex, strings.Join(backup.DNSServers, ","))
	return nil
}

func currentProbeVirtualRouterTUNIfIndex() int {
	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	defer probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	return probeVirtualRouterTUNDataPlaneState.ifIndex
}

func sameProbeVirtualRouterDNSServers(left []string, right []string) bool {
	a := dedupeProbeLocalIPv4Strings(left)
	b := dedupeProbeLocalIPv4Strings(right)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if net.ParseIP(a[i]).String() != net.ParseIP(b[i]).String() {
			return false
		}
	}
	return true
}

func resolveProbeVirtualRouterDNSBackupPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeVirtualRouterDNSBackupFileName), nil
}

func loadProbeVirtualRouterDNSBackupBestEffort() (probeVirtualRouterDNSBackup, bool) {
	path, err := resolveProbeVirtualRouterDNSBackupPath()
	if err != nil {
		return probeVirtualRouterDNSBackup{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return probeVirtualRouterDNSBackup{}, false
	}
	var backup probeVirtualRouterDNSBackup
	if err := json.Unmarshal(raw, &backup); err != nil {
		return probeVirtualRouterDNSBackup{}, false
	}
	backup.DNSServers = dedupeProbeLocalIPv4Strings(backup.DNSServers)
	backup.AppliedDNS = dedupeProbeLocalIPv4Strings(backup.AppliedDNS)
	return backup, true
}

func persistProbeVirtualRouterDNSBackup(backup probeVirtualRouterDNSBackup) error {
	backup.DNSServers = dedupeProbeLocalIPv4Strings(backup.DNSServers)
	backup.AppliedDNS = dedupeProbeLocalIPv4Strings(backup.AppliedDNS)
	backup.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	path, err := resolveProbeVirtualRouterDNSBackupPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func removeProbeVirtualRouterDNSBackup() error {
	path, err := resolveProbeVirtualRouterDNSBackupPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
