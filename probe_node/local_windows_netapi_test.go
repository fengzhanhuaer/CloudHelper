//go:build windows

package main

import (
	"errors"
	"net"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestEnsureProbeLocalWindowsInterfaceIPv4AddressByLUIDReconcilesDNSWithRouterSwitch(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		currentDNS []string
		wantSet    int
		wantReset  int
	}{
		{name: "enabled", enabled: true, wantSet: 1},
		{name: "disabled clears stale dns", currentDNS: []string{probeLocalTUNInterfaceIPv4}, wantReset: 1},
		{name: "disabled already clear"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldFindAdapter := probeLocalFindWindowsAdapterByLUID
			oldUpsertIPv4 := probeLocalUpsertWindowsInterfaceIPv4ByLUID
			oldSetDNS := probeLocalSetWindowsInterfaceDNS
			oldResetDNS := probeLocalResetWindowsInterfaceDNS
			t.Cleanup(func() {
				probeLocalFindWindowsAdapterByLUID = oldFindAdapter
				probeLocalUpsertWindowsInterfaceIPv4ByLUID = oldUpsertIPv4
				probeLocalSetWindowsInterfaceDNS = oldSetDNS
				probeLocalResetWindowsInterfaceDNS = oldResetDNS
				resetProbeVirtualRouterLocalSettingsForTest()
			})

			resetProbeVirtualRouterLocalSettingsForTest()
			probeVirtualRouterLocalSettingsState.mu.Lock()
			probeVirtualRouterLocalSettingsState.loaded = true
			probeVirtualRouterLocalSettingsState.settings = probeVirtualRouterLocalSettings{
				VirtualRouterEnabled: tt.enabled,
				VirtualDNSEnabled:    tt.enabled,
			}
			probeVirtualRouterLocalSettingsState.mu.Unlock()

			probeLocalFindWindowsAdapterByLUID = func(luid uint64) (windowsAdapterInfo, error) {
				if luid != 77 {
					t.Fatalf("luid=%d, want 77", luid)
				}
				return windowsAdapterInfo{InterfaceIndex: 13, InterfaceLUID: luid, AdapterGUID: "{6BA2B7A3-1C2D-4E63-9E3C-6F7A8B9C0D21}", DNSServers: tt.currentDNS}, nil
			}
			probeLocalUpsertWindowsInterfaceIPv4ByLUID = func(luid uint64, ifIndex int, ip string, prefix int) error {
				if luid != 77 || ifIndex != 13 || ip != "198.18.0.7" || prefix != 30 {
					t.Fatalf("unexpected address ensure: luid=%d if_index=%d ip=%s prefix=%d", luid, ifIndex, ip, prefix)
				}
				return nil
			}
			setCalls := 0
			resetCalls := 0
			probeLocalSetWindowsInterfaceDNS = func(guid string, servers []string) error {
				setCalls++
				if guid == "" || len(servers) != 1 || servers[0] != probeLocalTUNInterfaceIPv4 {
					t.Fatalf("unexpected dns set: guid=%q servers=%v", guid, servers)
				}
				return nil
			}
			probeLocalResetWindowsInterfaceDNS = func(guid string) error {
				resetCalls++
				if guid == "" {
					t.Fatal("empty adapter guid")
				}
				return nil
			}
			if err := ensureProbeLocalWindowsInterfaceIPv4AddressByLUID(77, "198.18.0.7", 30); err != nil {
				t.Fatalf("ensure interface address: %v", err)
			}
			if setCalls != tt.wantSet || resetCalls != tt.wantReset {
				t.Fatalf("dns calls set=%d reset=%d, want %d/%d", setCalls, resetCalls, tt.wantSet, tt.wantReset)
			}
		})
	}
}

func TestProbeLocalRepairWindowsInterfaceIPv4AddressIgnoresDeleteInvalidParameter(t *testing.T) {
	deleteCalls := 0
	upsertCalls := 0
	probeLocalDeleteWindowsInterfaceIPv4 = func(interfaceIndex int, ipText string) error {
		deleteCalls++
		if interfaceIndex != 18 {
			t.Fatalf("interfaceIndex=%d, want 18", interfaceIndex)
		}
		if ipText != probeLocalTUNInterfaceIPv4 {
			t.Fatalf("ipText=%q, want %q", ipText, probeLocalTUNInterfaceIPv4)
		}
		return errors.New("DeleteUnicastIpAddressEntry failed: code=87")
	}
	probeLocalUpsertWindowsInterfaceIPv4 = func(interfaceIndex int, ipText string, prefixLength int) error {
		upsertCalls++
		if interfaceIndex != 18 {
			t.Fatalf("interfaceIndex=%d, want 18", interfaceIndex)
		}
		if ipText != probeLocalTUNInterfaceIPv4 {
			t.Fatalf("ipText=%q, want %q", ipText, probeLocalTUNInterfaceIPv4)
		}
		if prefixLength != probeLocalTUNRouteIPv4PrefixLen {
			t.Fatalf("prefixLength=%d, want %d", prefixLength, probeLocalTUNRouteIPv4PrefixLen)
		}
		return nil
	}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := probeLocalRepairWindowsInterfaceIPv4Address(18, probeLocalTUNInterfaceIPv4, probeLocalTUNRouteIPv4PrefixLen); err != nil {
		t.Fatalf("probeLocalRepairWindowsInterfaceIPv4Address returned error: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("deleteCalls=%d, want 1", deleteCalls)
	}
	if upsertCalls != 1 {
		t.Fatalf("upsertCalls=%d, want 1", upsertCalls)
	}
}

func TestProbeLocalRepairWindowsInterfaceIPv4AddressIgnoresDeleteError(t *testing.T) {
	deleteCalls := 0
	upsertCalls := 0
	probeLocalDeleteWindowsInterfaceIPv4 = func(interfaceIndex int, ipText string) error {
		deleteCalls++
		return errors.New("DeleteUnicastIpAddressEntry failed: code=5")
	}
	probeLocalUpsertWindowsInterfaceIPv4 = func(interfaceIndex int, ipText string, prefixLength int) error {
		upsertCalls++
		return nil
	}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := probeLocalRepairWindowsInterfaceIPv4Address(18, probeLocalTUNInterfaceIPv4, probeLocalTUNRouteIPv4PrefixLen); err != nil {
		t.Fatalf("probeLocalRepairWindowsInterfaceIPv4Address returned error: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("deleteCalls=%d, want 1", deleteCalls)
	}
	if upsertCalls != 1 {
		t.Fatalf("upsertCalls=%d, want 1", upsertCalls)
	}
}

func TestUpsertProbeLocalWindowsInterfaceIPv4AddressCreateOnly(t *testing.T) {
	createCalls := 0
	probeLocalCallCreateUnicastIPAddressEntry = func(row *probeLocalMIBUnicastIPAddressRow) (uintptr, error) {
		createCalls++
		if row == nil {
			t.Fatal("row is nil")
		}
		if row.SkipAsSource != 1 {
			t.Fatalf("SkipAsSource=%d, want 1 for base tun ip", row.SkipAsSource)
		}
		return 0, nil
	}
	t.Cleanup(func() {
		probeLocalCallCreateUnicastIPAddressEntry = probeLocalCallCreateUnicastIPAddressEntryDefault
		probeLocalCallSetUnicastIPAddressEntry = probeLocalCallSetUnicastIPAddressEntryDefault
	})

	if err := upsertProbeLocalWindowsInterfaceIPv4Address(19, "198.18.0.2", 15); err != nil {
		t.Fatalf("upsertProbeLocalWindowsInterfaceIPv4Address returned error: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("createCalls=%d, want 1", createCalls)
	}
}

func TestUpsertProbeLocalWindowsInterfaceIPv4AddressUsesVRouterIPAsSource(t *testing.T) {
	createCalls := 0
	probeLocalCallCreateUnicastIPAddressEntry = func(row *probeLocalMIBUnicastIPAddressRow) (uintptr, error) {
		createCalls++
		if row == nil {
			t.Fatal("row is nil")
		}
		if row.SkipAsSource != 0 {
			t.Fatalf("SkipAsSource=%d, want 0 for vrouter ip", row.SkipAsSource)
		}
		return 0, nil
	}
	t.Cleanup(func() {
		probeLocalCallCreateUnicastIPAddressEntry = probeLocalCallCreateUnicastIPAddressEntryDefault
		probeLocalCallSetUnicastIPAddressEntry = probeLocalCallSetUnicastIPAddressEntryDefault
	})

	if err := upsertProbeLocalWindowsInterfaceIPv4Address(19, "198.18.0.18", 15); err != nil {
		t.Fatalf("upsertProbeLocalWindowsInterfaceIPv4Address returned error: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("createCalls=%d, want 1", createCalls)
	}
}

func TestUpsertProbeLocalWindowsInterfaceIPv4AddressUpdatesExistingSkipAsSource(t *testing.T) {
	createCalls := 0
	setCalls := 0
	probeLocalCallCreateUnicastIPAddressEntry = func(row *probeLocalMIBUnicastIPAddressRow) (uintptr, error) {
		createCalls++
		if row == nil {
			t.Fatal("row is nil")
		}
		if row.SkipAsSource != 1 {
			t.Fatalf("create SkipAsSource=%d, want 1", row.SkipAsSource)
		}
		return uintptr(windows.ERROR_OBJECT_ALREADY_EXISTS), nil
	}
	probeLocalCallSetUnicastIPAddressEntry = func(row *probeLocalMIBUnicastIPAddressRow) (uintptr, error) {
		setCalls++
		if row == nil {
			t.Fatal("row is nil")
		}
		if row.SkipAsSource != 1 {
			t.Fatalf("set SkipAsSource=%d, want 1", row.SkipAsSource)
		}
		return 0, nil
	}
	t.Cleanup(func() {
		probeLocalCallCreateUnicastIPAddressEntry = probeLocalCallCreateUnicastIPAddressEntryDefault
		probeLocalCallSetUnicastIPAddressEntry = probeLocalCallSetUnicastIPAddressEntryDefault
	})

	if err := upsertProbeLocalWindowsInterfaceIPv4Address(19, "198.18.0.2", 15); err != nil {
		t.Fatalf("upsertProbeLocalWindowsInterfaceIPv4Address returned error: %v", err)
	}
	if createCalls != 1 || setCalls != 1 {
		t.Fatalf("create/set calls=%d/%d, want 1/1", createCalls, setCalls)
	}
}

func TestSetProbeLocalWindowsInterfaceDNSFallsBackToQWords(t *testing.T) {
	ptrCalls := 0
	qwordCalls := 0
	dwordCalls := 0
	probeLocalCallSetInterfaceDNSSettingsByPtr = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		ptrCalls++
		return 87, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByQW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		qwordCalls++
		if guid == nil || settings == nil {
			t.Fatal("guid/settings nil")
		}
		if settings.Flags != probeLocalDNSSettingNameServer|probeLocalDNSSettingSearchList {
			t.Fatalf("flags=%d", settings.Flags)
		}
		return 0, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByDW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		dwordCalls++
		return 0, nil
	}
	t.Cleanup(func() {
		probeLocalCallSetInterfaceDNSSettingsByPtr = probeLocalCallSetInterfaceDNSSettingsByPtrDefault
		probeLocalCallSetInterfaceDNSSettingsByQW = probeLocalCallSetInterfaceDNSSettingsByQWordsDefault
		probeLocalCallSetInterfaceDNSSettingsByDW = probeLocalCallSetInterfaceDNSSettingsByDWordsDefault
	})

	err := setProbeLocalWindowsInterfaceDNS("{6BA2B7A3-1C2D-4E63-9E3C-6F7A8B9C0D21}", []string{"198.18.0.2"})
	if err != nil {
		t.Fatalf("setProbeLocalWindowsInterfaceDNS returned error: %v", err)
	}
	if ptrCalls != 1 || qwordCalls != 1 || dwordCalls != 0 {
		t.Fatalf("unexpected call count ptr=%d qwords=%d dwords=%d", ptrCalls, qwordCalls, dwordCalls)
	}
}

func TestResetProbeLocalWindowsInterfaceDNSClearsNameServerOverride(t *testing.T) {
	ptrCalls := 0
	probeLocalCallSetInterfaceDNSSettingsByPtr = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		ptrCalls++
		if guid == nil || settings == nil {
			t.Fatal("guid/settings nil")
		}
		if settings.Flags != probeLocalDNSSettingNameServer|probeLocalDNSSettingSearchList {
			t.Fatalf("flags=%d", settings.Flags)
		}
		if got := windows.UTF16PtrToString(settings.NameServer); got != "" {
			t.Fatalf("name server override=%q, want empty", got)
		}
		return 0, nil
	}
	t.Cleanup(func() {
		probeLocalCallSetInterfaceDNSSettingsByPtr = probeLocalCallSetInterfaceDNSSettingsByPtrDefault
	})

	if err := resetProbeLocalWindowsInterfaceDNS("{6BA2B7A3-1C2D-4E63-9E3C-6F7A8B9C0D21}"); err != nil {
		t.Fatalf("resetProbeLocalWindowsInterfaceDNS returned error: %v", err)
	}
	if ptrCalls != 1 {
		t.Fatalf("ptr calls=%d, want 1", ptrCalls)
	}
}

func TestSetProbeLocalWindowsInterfaceDNSUsesDWordsAsLastFallback(t *testing.T) {
	ptrCalls := 0
	qwordCalls := 0
	dwordCalls := 0
	probeLocalCallSetInterfaceDNSSettingsByPtr = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		ptrCalls++
		return 87, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByQW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		qwordCalls++
		return 87, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByDW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		dwordCalls++
		return 0, nil
	}
	t.Cleanup(func() {
		probeLocalCallSetInterfaceDNSSettingsByPtr = probeLocalCallSetInterfaceDNSSettingsByPtrDefault
		probeLocalCallSetInterfaceDNSSettingsByQW = probeLocalCallSetInterfaceDNSSettingsByQWordsDefault
		probeLocalCallSetInterfaceDNSSettingsByDW = probeLocalCallSetInterfaceDNSSettingsByDWordsDefault
	})

	err := setProbeLocalWindowsInterfaceDNS("{6BA2B7A3-1C2D-4E63-9E3C-6F7A8B9C0D21}", []string{"198.18.0.2"})
	if err != nil {
		t.Fatalf("setProbeLocalWindowsInterfaceDNS returned error: %v", err)
	}
	if ptrCalls != 1 || qwordCalls != 1 || dwordCalls != 1 {
		t.Fatalf("unexpected call count ptr=%d qwords=%d dwords=%d", ptrCalls, qwordCalls, dwordCalls)
	}
}

func TestSetProbeLocalWindowsInterfaceDNSReturnsErrorWhenAllCallsFail(t *testing.T) {
	probeLocalCallSetInterfaceDNSSettingsByPtr = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		return 87, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByQW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		return 87, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByDW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		return 1168, nil
	}
	t.Cleanup(func() {
		probeLocalCallSetInterfaceDNSSettingsByPtr = probeLocalCallSetInterfaceDNSSettingsByPtrDefault
		probeLocalCallSetInterfaceDNSSettingsByQW = probeLocalCallSetInterfaceDNSSettingsByQWordsDefault
		probeLocalCallSetInterfaceDNSSettingsByDW = probeLocalCallSetInterfaceDNSSettingsByDWordsDefault
	})

	err := setProbeLocalWindowsInterfaceDNS("{6BA2B7A3-1C2D-4E63-9E3C-6F7A8B9C0D21}", []string{"198.18.0.2"})
	if err == nil || !strings.Contains(err.Error(), "SetInterfaceDnsSettings(dwords)") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelectProbeLocalWindowsPrimaryEgressRouteTargetPrefersLowerTotalMetric(t *testing.T) {
	rows := []probeLocalMIBIPForwardRow2{
		newProbeLocalTestDefaultRouteRow(11, "192.168.1.1", 5),
		newProbeLocalTestDefaultRouteRow(12, "10.0.0.1", 5),
	}
	adapters := []windowsAdapterInfo{
		{InterfaceIndex: 11, OperStatus: windows.IfOperStatusUp, IPv4Metric: 50},
		{InterfaceIndex: 12, OperStatus: windows.IfOperStatusUp, IPv4Metric: 5},
	}

	got, err := selectProbeLocalWindowsPrimaryEgressRouteTarget(rows, adapters, 9)
	if err != nil {
		t.Fatalf("selectProbeLocalWindowsPrimaryEgressRouteTarget returned error: %v", err)
	}
	if got.InterfaceIndex != 12 || got.NextHop != "10.0.0.1" {
		t.Fatalf("routeTarget=%+v", got)
	}
}

func TestSelectProbeLocalWindowsPrimaryEgressRouteTargetExcludesTunInterface(t *testing.T) {
	rows := []probeLocalMIBIPForwardRow2{
		newProbeLocalTestDefaultRouteRow(9, "198.18.0.1", 1),
		newProbeLocalTestDefaultRouteRow(12, "10.0.0.1", 10),
	}
	adapters := []windowsAdapterInfo{
		{InterfaceIndex: 9, OperStatus: windows.IfOperStatusUp, IPv4Metric: 1},
		{InterfaceIndex: 12, OperStatus: windows.IfOperStatusUp, IPv4Metric: 5},
	}

	got, err := selectProbeLocalWindowsPrimaryEgressRouteTarget(rows, adapters, 9)
	if err != nil {
		t.Fatalf("selectProbeLocalWindowsPrimaryEgressRouteTarget returned error: %v", err)
	}
	if got.InterfaceIndex != 12 || got.NextHop != "10.0.0.1" {
		t.Fatalf("routeTarget=%+v", got)
	}
}

func TestSelectProbeLocalWindowsPrimaryEgressRouteTargetSkipsDisconnectedAdapter(t *testing.T) {
	rows := []probeLocalMIBIPForwardRow2{
		newProbeLocalTestDefaultRouteRow(10, "192.168.1.1", 1),
		newProbeLocalTestDefaultRouteRow(12, "10.0.0.1", 20),
	}
	adapters := []windowsAdapterInfo{
		{InterfaceIndex: 10, OperStatus: windows.IfOperStatusDown, IPv4Metric: 1},
		{InterfaceIndex: 12, OperStatus: windows.IfOperStatusUp, IPv4Metric: 5},
	}

	got, err := selectProbeLocalWindowsPrimaryEgressRouteTarget(rows, adapters, 9)
	if err != nil {
		t.Fatalf("selectProbeLocalWindowsPrimaryEgressRouteTarget returned error: %v", err)
	}
	if got.InterfaceIndex != 12 || got.NextHop != "10.0.0.1" {
		t.Fatalf("routeTarget=%+v", got)
	}
}

func TestSelectProbeLocalWindowsPrimaryEgressRouteTargetReturnsErrorWithoutUpAdapter(t *testing.T) {
	rows := []probeLocalMIBIPForwardRow2{
		newProbeLocalTestDefaultRouteRow(10, "192.168.1.1", 1),
	}
	adapters := []windowsAdapterInfo{
		{InterfaceIndex: 10, OperStatus: windows.IfOperStatusDown, IPv4Metric: 1},
	}

	_, err := selectProbeLocalWindowsPrimaryEgressRouteTarget(rows, adapters, 9)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "usable ipv4 default route not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func newProbeLocalTestDefaultRouteRow(interfaceIndex int, nextHop string, metric uint32) probeLocalMIBIPForwardRow2 {
	row := probeLocalMIBIPForwardRow2{
		InterfaceIndex: uint32(interfaceIndex),
		Metric:         metric,
	}
	row.DestinationPrefix.Prefix.Family = windows.AF_INET
	row.NextHop.Family = windows.AF_INET
	copy(row.DestinationPrefix.Prefix.Data[:4], net.ParseIP("0.0.0.0").To4())
	copy(row.NextHop.Data[:4], net.ParseIP(nextHop).To4())
	return row
}
