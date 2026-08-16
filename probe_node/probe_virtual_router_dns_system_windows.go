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

	"golang.org/x/sys/windows/registry"
)

const probeVirtualRouterDNSBackupFileName = "virtual_router_dns_backup.json"

type probeVirtualRouterDNSBackup struct {
	InterfaceGUID  string   `json:"interface_guid"`
	InterfaceIndex int      `json:"interface_index"`
	DNSServers     []string `json:"dns_servers"`
	AppliedDNS     []string `json:"applied_dns"`
	Automatic      *bool    `json:"automatic,omitempty"`
	UpdatedAt      string   `json:"updated_at"`
}

type probeVirtualRouterPersistentDNS struct {
	Servers     []string
	DHCPServers []string
	Automatic   bool
}

var (
	probeVirtualRouterResolvePrimaryDNSAdapter = func(excludedIfIndex int) (windowsAdapterInfo, error) {
		return probeLocalResolveWindowsPrimaryDNSAdapter(excludedIfIndex)
	}
	probeVirtualRouterSetInterfaceDNS = func(interfaceGUID string, dnsServers []string) error {
		return probeLocalSetWindowsInterfaceDNS(interfaceGUID, dnsServers)
	}
	probeVirtualRouterResetInterfaceDNS = func(interfaceGUID string) error {
		return probeLocalResetWindowsInterfaceDNS(interfaceGUID)
	}
	probeVirtualRouterReadPersistentDNS = readProbeVirtualRouterPersistentDNS
)

func applyProbeVirtualRouterSystemDNS() error {
	adapter, err := probeVirtualRouterResolvePrimaryDNSAdapter(currentProbeVirtualRouterTUNIfIndex())
	if err != nil {
		return err
	}
	if strings.TrimSpace(adapter.AdapterGUID) == "" {
		return errors.New("primary dns adapter guid is empty")
	}
	desired := []string{probeVirtualRouterDNSListenHost}
	backup, ok := loadProbeVirtualRouterDNSBackupBestEffort()
	if ok && strings.EqualFold(strings.TrimSpace(backup.InterfaceGUID), strings.TrimSpace(adapter.AdapterGUID)) {
		backup, err = reconcileProbeVirtualRouterDNSBackupMode(backup)
		if err != nil {
			return err
		}
	}
	if !probeVirtualRouterDNSBackupUsableForAdapter(backup, ok, adapter.AdapterGUID) {
		backup, err = snapshotProbeVirtualRouterDNSBackup(adapter, desired)
		if err != nil {
			return err
		}
		if err := persistProbeVirtualRouterDNSBackup(backup); err != nil {
			return err
		}
	}
	if sameProbeVirtualRouterDNSServers(adapter.DNSServers, desired) {
		return nil
	}
	if err := probeVirtualRouterSetInterfaceDNS(adapter.AdapterGUID, desired); err != nil {
		return err
	}
	logProbeInfof("probe virtual router system dns applied: if_index=%d dns=%s", adapter.InterfaceIndex, strings.Join(desired, ","))
	return nil
}

func restoreProbeVirtualRouterSystemDNS() error {
	backup, ok := loadProbeVirtualRouterDNSBackupBestEffort()
	if !ok {
		adapter, err := probeVirtualRouterResolvePrimaryDNSAdapter(currentProbeVirtualRouterTUNIfIndex())
		if err != nil {
			return err
		}
		desired := []string{probeVirtualRouterDNSListenHost}
		if !sameProbeVirtualRouterDNSServers(adapter.DNSServers, desired) {
			return nil
		}
		backup, err = snapshotProbeVirtualRouterDNSBackup(adapter, desired)
		if err != nil {
			return err
		}
		if err := persistProbeVirtualRouterDNSBackup(backup); err != nil {
			return err
		}
	} else {
		var err error
		backup, err = reconcileProbeVirtualRouterDNSBackupMode(backup)
		if err != nil {
			return err
		}
	}
	automatic := probeVirtualRouterDNSBackupAutomatic(backup)
	if strings.TrimSpace(backup.InterfaceGUID) == "" || (!automatic && len(backup.DNSServers) == 0) {
		_ = removeProbeVirtualRouterDNSBackup()
		return nil
	}
	var err error
	if automatic {
		err = probeVirtualRouterResetInterfaceDNS(backup.InterfaceGUID)
	} else {
		err = probeVirtualRouterSetInterfaceDNS(backup.InterfaceGUID, backup.DNSServers)
	}
	if err != nil {
		return err
	}
	_ = removeProbeVirtualRouterDNSBackup()
	logProbeInfof("probe virtual router system dns restored: if_index=%d automatic=%v dns=%s", backup.InterfaceIndex, automatic, strings.Join(backup.DNSServers, ","))
	return nil
}

func probeVirtualRouterDNSBackupUsableForAdapter(backup probeVirtualRouterDNSBackup, ok bool, adapterGUID string) bool {
	return ok &&
		strings.EqualFold(strings.TrimSpace(backup.InterfaceGUID), strings.TrimSpace(adapterGUID)) &&
		backup.Automatic != nil &&
		(*backup.Automatic || len(backup.DNSServers) > 0)
}

func snapshotProbeVirtualRouterDNSBackup(adapter windowsAdapterInfo, desired []string) (probeVirtualRouterDNSBackup, error) {
	persistent, persistentErr := probeVirtualRouterReadPersistentDNS(adapter.AdapterGUID)
	servers := filterProbeLocalSystemDNSUpstreamServers(adapter.DNSServers)
	if len(servers) == 0 {
		servers = filterProbeLocalSystemDNSUpstreamServers(persistent.Servers)
	}
	automatic := persistentErr == nil && persistent.Automatic
	if persistentErr == nil && probeVirtualRouterPersistentDNSIsAppliedOverride(persistent, desired) {
		automatic = true
		servers = filterProbeLocalSystemDNSUpstreamServers(persistent.DHCPServers)
	}
	if len(servers) == 0 && !automatic {
		if persistentErr != nil {
			return probeVirtualRouterDNSBackup{}, persistentErr
		}
		return probeVirtualRouterDNSBackup{}, errors.New("original primary dns configuration is unavailable")
	}
	return probeVirtualRouterDNSBackup{
		InterfaceGUID:  adapter.AdapterGUID,
		InterfaceIndex: adapter.InterfaceIndex,
		DNSServers:     servers,
		AppliedDNS:     dedupeProbeLocalIPv4Strings(desired),
		Automatic:      &automatic,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func reconcileProbeVirtualRouterDNSBackupMode(backup probeVirtualRouterDNSBackup) (probeVirtualRouterDNSBackup, error) {
	if probeVirtualRouterDNSBackupAutomatic(backup) {
		return backup, nil
	}
	persistent, err := probeVirtualRouterReadPersistentDNS(backup.InterfaceGUID)
	if backup.Automatic != nil {
		if err != nil || !probeVirtualRouterDNSBackupWasAutomatic(backup, persistent) {
			return backup, nil
		}
		automatic := true
		backup.Automatic = &automatic
		if err := persistProbeVirtualRouterDNSBackup(backup); err != nil {
			return backup, err
		}
		logProbeInfof("probe virtual router dns backup repaired as automatic: if_index=%d dns=%s", backup.InterfaceIndex, strings.Join(backup.DNSServers, ","))
		return backup, nil
	}

	automatic := false
	if err != nil {
		logProbeWarnf("probe virtual router legacy dns backup mode detection failed; preserving static restore: if_index=%d err=%v", backup.InterfaceIndex, err)
	} else {
		automatic = persistent.Automatic || probeVirtualRouterDNSBackupWasAutomatic(backup, persistent)
		servers := filterProbeLocalSystemDNSUpstreamServers(persistent.Servers)
		if probeVirtualRouterPersistentDNSIsAppliedOverride(persistent, backup.AppliedDNS) {
			servers = filterProbeLocalSystemDNSUpstreamServers(persistent.DHCPServers)
		}
		if len(servers) > 0 {
			backup.DNSServers = servers
		}
	}
	backup.Automatic = &automatic
	backup.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := persistProbeVirtualRouterDNSBackup(backup); err != nil {
		return backup, err
	}
	return backup, nil
}

func probeVirtualRouterDNSBackupWasAutomatic(backup probeVirtualRouterDNSBackup, persistent probeVirtualRouterPersistentDNS) bool {
	return len(backup.DNSServers) > 0 &&
		probeVirtualRouterPersistentDNSIsAppliedOverride(persistent, backup.AppliedDNS) &&
		sameProbeVirtualRouterDNSServers(backup.DNSServers, persistent.DHCPServers)
}

func probeVirtualRouterPersistentDNSIsAppliedOverride(persistent probeVirtualRouterPersistentDNS, applied []string) bool {
	return !persistent.Automatic &&
		len(persistent.Servers) > 0 &&
		len(persistent.DHCPServers) > 0 &&
		sameProbeVirtualRouterDNSServers(persistent.Servers, applied)
}

func probeVirtualRouterDNSBackupAutomatic(backup probeVirtualRouterDNSBackup) bool {
	return backup.Automatic != nil && *backup.Automatic
}

func readProbeVirtualRouterPersistentDNS(interfaceGUID string) (probeVirtualRouterPersistentDNS, error) {
	interfaceGUID = strings.TrimSpace(interfaceGUID)
	if interfaceGUID == "" {
		return probeVirtualRouterPersistentDNS{}, errors.New("primary dns adapter guid is empty")
	}
	path := `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters\Interfaces\` + interfaceGUID
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return probeVirtualRouterPersistentDNS{}, err
	}
	defer key.Close()
	staticServers, err := readProbeVirtualRouterDNSRegistryValue(key, "NameServer")
	if err != nil {
		return probeVirtualRouterPersistentDNS{}, err
	}
	dhcpServers, err := readProbeVirtualRouterDNSRegistryValue(key, "DhcpNameServer")
	if err != nil {
		return probeVirtualRouterPersistentDNS{}, err
	}
	if len(staticServers) > 0 {
		return probeVirtualRouterPersistentDNS{Servers: staticServers, DHCPServers: dhcpServers}, nil
	}
	return probeVirtualRouterPersistentDNS{Servers: dhcpServers, DHCPServers: dhcpServers, Automatic: true}, nil
}

func readProbeVirtualRouterDNSRegistryValue(key registry.Key, name string) ([]string, error) {
	value, _, err := key.GetStringValue(name)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	items := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
	return dedupeProbeLocalIPv4Strings(items), nil
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
