//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

func TestProbeLocalProxyEnableStartsDataPlaneBeforeTakeover(t *testing.T) {
	_ = setupProbeLocalConsoleTest(t)
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsNativeRouteHooksForTest()
		resetProbeLocalProxyHooksForTest()
		resetProbeLocalTUNDataPlaneHooksForTest()
	})

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.mu.Unlock()
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")
	t.Setenv("PROBE_LOCAL_TUN_DNS_HOST", "198.18.0.2")
	stubProbeLocalConsoleTUNRouteTargetForTest(t)

	events := make([]string, 0, 2)
	probeLocalWindowsRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		if name == "powershell" {
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		}
		return "", nil
	}
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return uintptr(1), nil }
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error { return nil }
	probeLocalNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeLocalTUNDataPlane, error) {
		events = append(events, "data_plane")
		return &fakeProbeLocalTUNDataPlane{stats: probeLocalTUNDataPlaneStats{Running: true}}, nil
	}
	probeLocalApplyProxyTakeover = func() error {
		events = append(events, "takeover")
		return nil
	}
	probeLocalRestoreProxyDirect = func() error { return nil }

	_, proxyState, err := probeLocalControl.enableProxy()
	if err != nil {
		t.Fatalf("enableProxy returned error: %v", err)
	}
	if !proxyState.Enabled || proxyState.Mode != probeLocalProxyModeTUN {
		t.Fatalf("proxyState=%+v, want enabled tun", proxyState)
	}
	if strings.Join(events, ",") != "data_plane,takeover" {
		t.Fatalf("events=%v, want data_plane before takeover", events)
	}
}

func stubProbeLocalConsoleTUNRouteTargetForTest(t *testing.T) {
	t.Helper()
	const (
		luid    uint64 = 101
		ifIndex        = 9
	)
	probeLocalGetWintunAdapterLUIDFromHandle = func(_ string, _ uintptr) (uint64, error) {
		return luid, nil
	}
	probeLocalEnsureWindowsInterfaceIPv4ByLUID = func(gotLUID uint64, ip string, prefix int) error {
		if gotLUID != luid {
			t.Fatalf("tun route target luid=%d want %d", gotLUID, luid)
		}
		if strings.TrimSpace(ip) != probeLocalTUNInterfaceIPv4 {
			t.Fatalf("tun route target ip=%s want %s", ip, probeLocalTUNInterfaceIPv4)
		}
		if prefix != probeLocalTUNRouteIPv4PrefixLen {
			t.Fatalf("tun route target prefix=%d want %d", prefix, probeLocalTUNRouteIPv4PrefixLen)
		}
		return nil
	}
	probeLocalConvertInterfaceLUIDToIndex = func(gotLUID uint64) (int, error) {
		if gotLUID != luid {
			t.Fatalf("convert luid=%d want %d", gotLUID, luid)
		}
		return ifIndex, nil
	}
	probeLocalFindWindowsAdapterByIfIndex = func(gotIfIndex int) (windowsAdapterInfo, error) {
		if gotIfIndex != ifIndex {
			t.Fatalf("find adapter ifIndex=%d want %d", gotIfIndex, ifIndex)
		}
		return windowsAdapterInfo{InterfaceIndex: ifIndex, InterfaceLUID: luid}, nil
	}
}
