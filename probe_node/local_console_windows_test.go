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
