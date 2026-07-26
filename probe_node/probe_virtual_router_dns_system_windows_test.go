//go:build windows

package main

import (
	"errors"
	"net"
	"os"
	"reflect"
	"testing"
)

const probeVirtualRouterTestDNSAdapterGUID = "{11111111-1111-1111-1111-111111111111}"

func resetProbeVirtualRouterDNSSystemHooksForTest() {
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
}

func TestApplyProbeVirtualRouterSystemDNSRebuildsMissingAutomaticBackup(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterDNSSystemHooksForTest()
	t.Cleanup(resetProbeVirtualRouterDNSSystemHooksForTest)

	probeVirtualRouterResolvePrimaryDNSAdapter = func(int) (windowsAdapterInfo, error) {
		return windowsAdapterInfo{
			AdapterGUID:    probeVirtualRouterTestDNSAdapterGUID,
			InterfaceIndex: 15,
			DNSServers:     []string{"198.18.0.2"},
		}, nil
	}
	probeVirtualRouterReadPersistentDNS = func(string) (probeVirtualRouterPersistentDNS, error) {
		return probeVirtualRouterPersistentDNS{Servers: []string{"172.20.10.11", "172.20.10.14"}, Automatic: true}, nil
	}
	probeVirtualRouterSetInterfaceDNS = func(string, []string) error {
		t.Fatal("already-applied DNS should not be written again")
		return nil
	}

	if err := applyProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("applyProbeVirtualRouterSystemDNS returned error: %v", err)
	}
	backup, ok := loadProbeVirtualRouterDNSBackupBestEffort()
	if !ok {
		t.Fatal("missing rebuilt DNS backup")
	}
	if !probeVirtualRouterDNSBackupAutomatic(backup) || !reflect.DeepEqual(backup.DNSServers, []string{"172.20.10.11", "172.20.10.14"}) {
		t.Fatalf("rebuilt DNS backup=%+v", backup)
	}
}

func TestApplyProbeVirtualRouterSystemDNSMigratesLegacyAutomaticBackup(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterDNSSystemHooksForTest()
	t.Cleanup(resetProbeVirtualRouterDNSSystemHooksForTest)

	legacy := probeVirtualRouterDNSBackup{
		InterfaceGUID:  probeVirtualRouterTestDNSAdapterGUID,
		InterfaceIndex: 15,
		DNSServers:     []string{"172.20.10.11", "172.20.10.14"},
		AppliedDNS:     []string{"198.18.0.2"},
	}
	if err := persistProbeVirtualRouterDNSBackup(legacy); err != nil {
		t.Fatalf("persist legacy DNS backup: %v", err)
	}
	probeVirtualRouterResolvePrimaryDNSAdapter = func(int) (windowsAdapterInfo, error) {
		return windowsAdapterInfo{
			AdapterGUID:    probeVirtualRouterTestDNSAdapterGUID,
			InterfaceIndex: 15,
			DNSServers:     []string{"198.18.0.2"},
		}, nil
	}
	probeVirtualRouterReadPersistentDNS = func(string) (probeVirtualRouterPersistentDNS, error) {
		return probeVirtualRouterPersistentDNS{Servers: []string{"172.20.10.11", "172.20.10.14"}, Automatic: true}, nil
	}
	probeVirtualRouterSetInterfaceDNS = func(string, []string) error {
		t.Fatal("already-applied DNS should not be written again")
		return nil
	}

	if err := applyProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("applyProbeVirtualRouterSystemDNS returned error: %v", err)
	}
	backup, ok := loadProbeVirtualRouterDNSBackupBestEffort()
	if !ok || !probeVirtualRouterDNSBackupAutomatic(backup) {
		t.Fatalf("migrated DNS backup=%+v ok=%v", backup, ok)
	}
}

func TestReconcileProbeVirtualRouterDNSDoesNotApplySystemDNSWhenListenerFails(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterDNSSystemHooksForTest()
	stopProbeVirtualRouterDNSService()
	oldListenPacket := probeVirtualRouterDNSListenPacket
	oldListen := probeVirtualRouterDNSListen
	t.Cleanup(func() {
		probeVirtualRouterDNSListenPacket = oldListenPacket
		probeVirtualRouterDNSListen = oldListen
		stopProbeVirtualRouterDNSService()
		resetProbeVirtualRouterDNSSystemHooksForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.loaded = true
	probeVirtualRouterLocalSettingsState.settings = probeVirtualRouterLocalSettings{VirtualDNSEnabled: true}
	probeVirtualRouterLocalSettingsState.mu.Unlock()
	probeVirtualRouterDNSListenPacket = func(string, string) (net.PacketConn, error) {
		return nil, errors.New("dns port unavailable")
	}
	probeVirtualRouterDNSListen = func(string, string) (net.Listener, error) {
		t.Fatal("TCP listener should not start after UDP listener failure")
		return nil, nil
	}
	probeVirtualRouterResolvePrimaryDNSAdapter = func(int) (windowsAdapterInfo, error) {
		t.Fatal("system DNS must not be resolved when the local listener failed")
		return windowsAdapterInfo{}, nil
	}

	reconcileProbeVirtualRouterDNSRuntime()
}

func TestRestoreProbeVirtualRouterSystemDNSWithoutBackupReturnsToAutomaticDNS(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterDNSSystemHooksForTest()
	t.Cleanup(resetProbeVirtualRouterDNSSystemHooksForTest)

	probeVirtualRouterResolvePrimaryDNSAdapter = func(int) (windowsAdapterInfo, error) {
		return windowsAdapterInfo{
			AdapterGUID:    probeVirtualRouterTestDNSAdapterGUID,
			InterfaceIndex: 15,
			DNSServers:     []string{"198.18.0.2"},
		}, nil
	}
	probeVirtualRouterReadPersistentDNS = func(string) (probeVirtualRouterPersistentDNS, error) {
		return probeVirtualRouterPersistentDNS{Servers: []string{"172.20.10.11", "172.20.10.14"}, Automatic: true}, nil
	}
	resetGUID := ""
	probeVirtualRouterResetInterfaceDNS = func(interfaceGUID string) error {
		resetGUID = interfaceGUID
		return nil
	}
	probeVirtualRouterSetInterfaceDNS = func(string, []string) error {
		t.Fatal("automatic DNS restore should clear the override")
		return nil
	}

	if err := restoreProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("restoreProbeVirtualRouterSystemDNS returned error: %v", err)
	}
	if resetGUID != probeVirtualRouterTestDNSAdapterGUID {
		t.Fatalf("reset interface guid=%q", resetGUID)
	}
	path, err := resolveProbeVirtualRouterDNSBackupPath()
	if err != nil {
		t.Fatalf("resolve backup path: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup should be removed after restore: %v", err)
	}
}

func TestRestoreProbeVirtualRouterSystemDNSWithoutBackupLeavesUnmanagedDNSAlone(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterDNSSystemHooksForTest()
	t.Cleanup(resetProbeVirtualRouterDNSSystemHooksForTest)

	probeVirtualRouterResolvePrimaryDNSAdapter = func(int) (windowsAdapterInfo, error) {
		return windowsAdapterInfo{
			AdapterGUID:    probeVirtualRouterTestDNSAdapterGUID,
			InterfaceIndex: 15,
			DNSServers:     []string{"8.8.8.8"},
		}, nil
	}
	probeVirtualRouterReadPersistentDNS = func(string) (probeVirtualRouterPersistentDNS, error) {
		t.Fatal("unmanaged DNS should not require persistent settings")
		return probeVirtualRouterPersistentDNS{}, nil
	}
	probeVirtualRouterResetInterfaceDNS = func(string) error {
		t.Fatal("unmanaged DNS should not be reset")
		return nil
	}
	probeVirtualRouterSetInterfaceDNS = func(string, []string) error {
		t.Fatal("unmanaged DNS should not be changed")
		return nil
	}

	if err := restoreProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("restoreProbeVirtualRouterSystemDNS returned error: %v", err)
	}
}

func TestRestoreProbeVirtualRouterSystemDNSMigratesLegacyAutomaticBackup(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterDNSSystemHooksForTest()
	t.Cleanup(resetProbeVirtualRouterDNSSystemHooksForTest)

	legacy := probeVirtualRouterDNSBackup{
		InterfaceGUID:  probeVirtualRouterTestDNSAdapterGUID,
		InterfaceIndex: 15,
		DNSServers:     []string{"172.20.10.11", "172.20.10.14"},
		AppliedDNS:     []string{"198.18.0.2"},
	}
	if err := persistProbeVirtualRouterDNSBackup(legacy); err != nil {
		t.Fatalf("persist legacy DNS backup: %v", err)
	}
	probeVirtualRouterReadPersistentDNS = func(string) (probeVirtualRouterPersistentDNS, error) {
		return probeVirtualRouterPersistentDNS{Servers: []string{"172.20.10.11", "172.20.10.14"}, Automatic: true}, nil
	}
	resetGUID := ""
	probeVirtualRouterResetInterfaceDNS = func(interfaceGUID string) error {
		resetGUID = interfaceGUID
		return nil
	}
	probeVirtualRouterSetInterfaceDNS = func(string, []string) error {
		t.Fatal("legacy automatic DNS backup should clear the override")
		return nil
	}

	if err := restoreProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("restoreProbeVirtualRouterSystemDNS returned error: %v", err)
	}
	if resetGUID != probeVirtualRouterTestDNSAdapterGUID {
		t.Fatalf("reset interface guid=%q", resetGUID)
	}
}

func TestRestoreProbeVirtualRouterSystemDNSPreservesStaticBackup(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterDNSSystemHooksForTest()
	t.Cleanup(resetProbeVirtualRouterDNSSystemHooksForTest)

	automatic := false
	backup := probeVirtualRouterDNSBackup{
		InterfaceGUID:  probeVirtualRouterTestDNSAdapterGUID,
		InterfaceIndex: 15,
		DNSServers:     []string{"8.8.8.8", "1.1.1.1"},
		AppliedDNS:     []string{"198.18.0.2"},
		Automatic:      &automatic,
	}
	if err := persistProbeVirtualRouterDNSBackup(backup); err != nil {
		t.Fatalf("persist DNS backup: %v", err)
	}
	var restored []string
	probeVirtualRouterSetInterfaceDNS = func(interfaceGUID string, dnsServers []string) error {
		if interfaceGUID != probeVirtualRouterTestDNSAdapterGUID {
			t.Fatalf("restore interface guid=%q", interfaceGUID)
		}
		restored = append([]string(nil), dnsServers...)
		return nil
	}
	probeVirtualRouterResetInterfaceDNS = func(string) error {
		t.Fatal("static DNS backup should not reset to automatic")
		return nil
	}

	if err := restoreProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("restoreProbeVirtualRouterSystemDNS returned error: %v", err)
	}
	if !reflect.DeepEqual(restored, backup.DNSServers) {
		t.Fatalf("restored DNS=%v want=%v", restored, backup.DNSServers)
	}
}
