//go:build windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func requireProbeLocalRealNICIntegration(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("PROBE_LOCAL_REAL_NIC_TEST")) != "1" {
		t.Skip("skip real NIC integration test: set PROBE_LOCAL_REAL_NIC_TEST=1")
	}
	if !isWindowsAdmin() {
		t.Skip("skip real NIC integration test: requires administrator")
	}
}

func TestEnsureProbeLocalWindowsRouteTargetConfiguredRealNIC(t *testing.T) {
	requireProbeLocalRealNICIntegration(t)
	resetProbeLocalTUNInstallWindowsHooksForTest()
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	wd, wdErr := os.Getwd()
	if wdErr != nil {
		t.Fatalf("os.Getwd failed: %v", wdErr)
	}
	wintunPath := filepath.Join(wd, "lib", "wintun", "amd64", "wintun.dll")
	if _, statErr := os.Stat(wintunPath); statErr != nil {
		t.Skipf("skip real NIC integration test: wintun.dll missing at %s (%v)", wintunPath, statErr)
	}
	probeLocalResolveWintunPath = func() (string, error) { return wintunPath, nil }

	adapter, exists, err := findProbeLocalWintunAdapter()
	if err != nil {
		t.Fatalf("findProbeLocalWintunAdapter failed: %v", err)
	}
	if !exists {
		t.Skip("skip real NIC integration test: wintun adapter not found")
	}
	if adapter.InterfaceIndex <= 0 {
		t.Skip("skip real NIC integration test: wintun interface index is invalid")
	}

	if err := ensureProbeLocalWindowsRouteTargetConfigured(); err != nil {
		t.Fatalf("ensureProbeLocalWindowsRouteTargetConfigured failed: %v", err)
	}

	if gotGateway := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_GATEWAY")); gotGateway != probeLocalTUNRouteGatewayIPv4 {
		t.Fatalf("PROBE_LOCAL_TUN_GATEWAY=%q, want %q", gotGateway, probeLocalTUNRouteGatewayIPv4)
	}
	rawIfIndex := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX"))
	if rawIfIndex == "" {
		t.Fatal("PROBE_LOCAL_TUN_IF_INDEX is empty")
	}
	ifIndex, parseErr := strconv.Atoi(rawIfIndex)
	if parseErr != nil || ifIndex <= 0 {
		t.Fatalf("invalid PROBE_LOCAL_TUN_IF_INDEX=%q err=%v", rawIfIndex, parseErr)
	}

	info, findErr := windowsFindAdapterByIfIndex(ifIndex)
	if findErr != nil {
		t.Fatalf("windowsFindAdapterByIfIndex(%d) failed: %v", ifIndex, findErr)
	}
	foundIP := false
	for _, ipText := range info.IPv4Addrs {
		if strings.EqualFold(strings.TrimSpace(ipText), probeLocalTUNRouteGatewayIPv4) {
			foundIP = true
			break
		}
	}
	if !foundIP {
		t.Fatalf("route target ip missing on adapter if=%d addrs=%v", ifIndex, info.IPv4Addrs)
	}

	conn, bindErr := net.ListenPacket("udp4", net.JoinHostPort(probeLocalTUNRouteGatewayIPv4, "0"))
	if bindErr != nil {
		t.Fatalf("route target ip is not bindable: %v", bindErr)
	}
	_ = conn.Close()
}
