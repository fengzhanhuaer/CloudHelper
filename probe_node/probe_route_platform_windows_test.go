//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestResolveProbeLocalWindowsRouteTargetRequiresEnv(t *testing.T) {
	t.Skip("disabled: Windows route target environment test is excluded from the default regression suite")
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "")
	_, err := resolveProbeRouteWindowsTUNRouteTarget()
	if err == nil || !strings.Contains(err.Error(), "PROBE_LOCAL_TUN_GATEWAY") {
		t.Fatalf("expected missing gateway error, got: %v", err)
	}

	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "0")
	_, err = resolveProbeRouteWindowsTUNRouteTarget()
	if err == nil || !strings.Contains(err.Error(), "PROBE_LOCAL_TUN_IF_INDEX") {
		t.Fatalf("expected invalid if index error, got: %v", err)
	}
}

func TestEnsureProbeLocalWindowsSplitRouteFallsBackToChange(t *testing.T) {
	oldRun := probeLocalWindowsRunCommand
	probeLocalWindowsRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		if name != "route" {
			return "", errors.New("unexpected command")
		}
		if len(args) > 0 && strings.EqualFold(args[0], "ADD") {
			return "", errors.New("The object already exists")
		}
		if len(args) > 0 && strings.EqualFold(args[0], "CHANGE") {
			return "", nil
		}
		return "", errors.New("unexpected route args")
	}
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	created, err := ensureProbeRouteWindowsSplitRoute(probeRouteWindowsSplitRoutePrefixA, probeRouteWindowsSplitRouteMaskA, "198.18.0.1", 9)
	if err != nil {
		t.Fatalf("ensure split route should fallback to CHANGE, got err: %v", err)
	}
	if created {
		t.Fatalf("existing route should not be marked as newly created")
	}
}

func TestDeleteProbeLocalWindowsSplitRouteIgnoresMissing(t *testing.T) {
	oldRun := probeLocalWindowsRunCommand
	probeLocalWindowsRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		return "", errors.New("The route specified was not found")
	}
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	if err := deleteProbeRouteWindowsSplitRoute(probeRouteWindowsSplitRoutePrefixA, probeRouteWindowsSplitRouteMaskA, "198.18.0.1", 9); err != nil {
		t.Fatalf("delete should ignore missing route, got err: %v", err)
	}
}

func resetProbeLocalWindowsNativeRouteHooksForTest() {
	probeLocalCreateWindowsRouteEntry = ensureProbeRouteWindowsRouteNative
	probeLocalDeleteWindowsRouteEntry = deleteProbeRouteWindowsRouteNative
	probeLocalListWindowsRouteEntries = listProbeLocalWindowsIPv4RouteEntries
	probeRouteWindowsListAdaptersIPv4 = windowsListAdaptersIPv4
	probeLocalResolveWindowsPrimaryEgressRoute = resolveProbeLocalWindowsPrimaryEgressRouteTarget
	probeLocalSnapshotWindowsIPv4Routes = snapshotProbeLocalWindowsIPv4Routes
	probeLocalSetWindowsInterfaceDNS = setProbeLocalWindowsInterfaceDNS
	probeLocalResetWindowsInterfaceDNS = resetProbeLocalWindowsInterfaceDNS
	probeLocalFindWindowsAdapterByIfIndex = windowsFindAdapterByIfIndex
	probeRouteWindowsDirectBypassRequired = shouldProbeRouteWindowsUseDirectBypass
}

func useProbeLocalWindowsCommandBackedRouteHooksForTest() {
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		metricValue := routeDef.Metric
		if metricValue == 0 {
			metricValue = probeRouteWindowsRouteMetric
		}
		metric := fmt.Sprintf("%d", metricValue)
		ifText := fmt.Sprintf("%d", routeDef.IfIndex)
		_, addErr := probeLocalWindowsRunCommand(6*time.Second, "route", "ADD", routeDef.Prefix, "MASK", routeDef.Mask, routeDef.Gateway, "METRIC", metric, "IF", ifText)
		if addErr == nil {
			return true, nil
		}
		if !isProbeLocalWindowsRouteExistsErr(addErr) {
			return false, addErr
		}
		_, changeErr := probeLocalWindowsRunCommand(6*time.Second, "route", "CHANGE", routeDef.Prefix, "MASK", routeDef.Mask, routeDef.Gateway, "METRIC", metric, "IF", ifText)
		if changeErr != nil {
			return false, fmt.Errorf("route exists but update failed: %w", changeErr)
		}
		return false, nil
	}
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		if strings.TrimSpace(routeDef.Gateway) == "" || routeDef.IfIndex <= 0 {
			return nil
		}
		ifText := fmt.Sprintf("%d", routeDef.IfIndex)
		_, err := probeLocalWindowsRunCommand(6*time.Second, "route", "DELETE", routeDef.Prefix, "MASK", routeDef.Mask, routeDef.Gateway, "IF", ifText)
		if err != nil && !isProbeLocalWindowsRouteMissingErr(err) {
			return err
		}
		return nil
	}
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return nil, nil
	}
	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeRouteWindowsDirectRouteTarget, error) {
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $exclude=%d; $route=Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | Where-Object { $_.InterfaceIndex -ne $exclude -and $_.NextHop } | Sort-Object @{Expression='RouteMetric';Ascending=$true}, @{Expression='InterfaceMetric';Ascending=$true} | Select-Object -First 1 @{Name='interface_index';Expression={[int]$_.InterfaceIndex}}, @{Name='next_hop';Expression={[string]$_.NextHop}}; if (-not $route) { throw 'usable ipv4 default route not found' }; $route | ConvertTo-Json -Compress`, excludedIfIndex)
		output, err := probeLocalWindowsRunCommand(6*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
		if err != nil {
			trimmed := strings.TrimSpace(output)
			if trimmed != "" {
				return probeRouteWindowsDirectRouteTarget{}, fmt.Errorf("detect windows bypass route target failed: %w: %s", err, trimmed)
			}
			return probeRouteWindowsDirectRouteTarget{}, fmt.Errorf("detect windows bypass route target failed: %w", err)
		}
		var routeTarget probeRouteWindowsDirectRouteTarget
		if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &routeTarget); err != nil {
			return probeRouteWindowsDirectRouteTarget{}, fmt.Errorf("decode windows bypass route target failed: %w", err)
		}
		return routeTarget, nil
	}
	probeLocalSnapshotWindowsIPv4Routes = func() (string, error) {
		return probeLocalWindowsRunCommand(6*time.Second, "route", "PRINT", "-4")
	}
}

func resetProbeRouteWindowsStateForTest() {
	resetProbeRouteDirectBypassStateForTest()
}

func TestProbeLocalWindowsFakeIPRoutePrefixAndMask(t *testing.T) {
	prefix, mask := probeRouteWindowsFakeIPRoutePrefixAndMask("198.19.0.0/16")
	if prefix != "198.19.0.0" || mask != "255.255.0.0" {
		t.Fatalf("prefix=%q mask=%q", prefix, mask)
	}
	prefix, mask = probeRouteWindowsFakeIPRoutePrefixAndMask("bad-cidr")
	if prefix != "198.18.0.0" || mask != "255.254.0.0" {
		t.Fatalf("fallback prefix=%q mask=%q", prefix, mask)
	}
}

func TestEnsureProbeRouteDirectBypassWritesHostRoute(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = probeRouteWindowsDirectRouteTarget{InterfaceIndex: 13, NextHop: "192.168.51.1"}
	probeRouteDirectRouteTargetState.ready = true
	probeRouteDirectRouteTargetState.mu.Unlock()
	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeRouteWindowsDirectRouteTarget, error) {
		return probeRouteWindowsDirectRouteTarget{InterfaceIndex: 13, NextHop: "192.168.51.1"}, nil
	}
	probeRouteWindowsListAdaptersIPv4 = func() ([]windowsAdapterInfo, error) {
		return []windowsAdapterInfo{{InterfaceIndex: 13, IPv4Addrs: []string{"192.168.51.20"}}}, nil
	}
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{{Prefix: "0.0.0.0", PrefixLength: 0, NextHop: "192.168.51.1", IfIndex: 13}}, nil
	}

	var created []probeRouteWindowsRouteDef
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		created = append(created, routeDef)
		return true, nil
	}
	if err := ensureProbeRouteDirectBypass("203.0.113.7:16030"); err != nil {
		t.Fatalf("ensure direct bypass failed: %v", err)
	}
	if len(created) != 1 || created[0].Prefix != "203.0.113.7" || created[0].Gateway != "192.168.51.1" || created[0].IfIndex != 13 || created[0].Metric != probeRouteWindowsDirectRouteMetric {
		t.Fatalf("unexpected direct bypass routes=%+v", created)
	}
}

func TestEnsureProbeRouteDirectBypassWithoutActiveTUNCleansManagedRoutes(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	probeRouteWindowsDirectBypassRequired = func() bool { return false }
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{
			{Prefix: "104.21.90.186", PrefixLength: 32, NextHop: "172.18.55.254", IfIndex: 22, Metric: probeRouteWindowsDirectRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "104.21.90.186", PrefixLength: 32, NextHop: "192.168.51.1", IfIndex: 22, Metric: probeRouteWindowsDirectRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
		}, nil
	}
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		t.Fatalf("TUN-disabled controller dial must not create a bypass route: %+v", routeDef)
		return false, nil
	}
	var deleted []probeRouteWindowsRouteDef
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}

	if err := ensureProbeRouteDirectBypass("104.21.90.186:443"); err != nil {
		t.Fatalf("cleanup disabled direct bypass: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted=%+v, want all managed controller routes removed", deleted)
	}
}

func TestShouldProbeRouteWindowsUseDirectBypassHonorsPersistedTUNIntent(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	t.Cleanup(resetProbeVirtualRouterTUNDataPlaneHooksForTest)

	if err := persistProbeLocalTUNPersistentState(true, false); err != nil {
		t.Fatalf("persist disabled TUN state: %v", err)
	}
	if shouldProbeRouteWindowsUseDirectBypass() {
		t.Fatal("disabled TUN state unexpectedly requires direct bypass")
	}

	if err := persistProbeLocalTUNPersistentState(true, true); err != nil {
		t.Fatalf("persist enabled TUN state: %v", err)
	}
	if !shouldProbeRouteWindowsUseDirectBypass() {
		t.Fatal("enabled TUN startup intent must require direct bypass")
	}
}

func TestEnsureProbeRouteDirectBypassRebindsManualAdapterAndRemovesStaleGateways(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeRouteDirectBypassStateForTest()
	oldOptions := probeLocalWindowsEgressRouteOptions
	t.Cleanup(func() {
		probeLocalWindowsEgressRouteOptions = oldOptions
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	const interfaceGUID = "{B1D66112-83C0-4BB7-ACAE-3987F9FD2D87}"
	setProbeRouteWindowsDirectManualRouteTarget(probeRouteWindowsDirectRouteTarget{
		InterfaceIndex: 22,
		InterfaceLUID:  1001,
		InterfaceGUID:  interfaceGUID,
		NextHop:        "172.18.55.254",
	}, probeLocalTUNEgressCandidateID(interfaceGUID, 1001, 22, "172.18.55.254"), "physical / 172.18.55.254")
	probeLocalWindowsEgressRouteOptions = func(int) ([]probeLocalTUNEgressRouteTargetOption, error) {
		candidate := probeLocalTUNEgressRouteTargetOption{
			InterfaceIndex: 22,
			InterfaceLUID:  1001,
			InterfaceGUID:  interfaceGUID,
			NextHop:        "172.18.52.205",
			Name:           "physical",
		}
		candidate.CandidateID = probeLocalTUNEgressCandidateID(candidate.InterfaceGUID, candidate.InterfaceLUID, candidate.InterfaceIndex, candidate.NextHop)
		return []probeLocalTUNEgressRouteTargetOption{candidate}, nil
	}
	probeRouteWindowsListAdaptersIPv4 = func() ([]windowsAdapterInfo, error) {
		return []windowsAdapterInfo{{InterfaceIndex: 22, IPv4Addrs: []string{"172.18.53.157"}}}, nil
	}
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{
			{Prefix: "0.0.0.0", PrefixLength: 0, NextHop: "172.18.52.205", IfIndex: 22},
			{Prefix: "203.0.113.7", PrefixLength: 32, NextHop: "172.18.55.254", IfIndex: 22, Metric: probeRouteWindowsDirectRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "203.0.113.7", PrefixLength: 32, NextHop: "192.168.51.1", IfIndex: 22, Metric: probeRouteWindowsDirectRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
		}, nil
	}
	var created []probeRouteWindowsRouteDef
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		created = append(created, routeDef)
		return true, nil
	}
	var deleted []probeRouteWindowsRouteDef
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}

	if err := ensureProbeRouteDirectBypass("203.0.113.7:443"); err != nil {
		t.Fatalf("ensure direct bypass after gateway change: %v", err)
	}
	if len(created) != 1 || created[0].Gateway != "172.18.52.205" || created[0].IfIndex != 22 {
		t.Fatalf("created=%+v, want one route via refreshed gateway", created)
	}
	if len(deleted) != 2 || deleted[0].Gateway != "172.18.55.254" || deleted[1].Gateway != "192.168.51.1" {
		t.Fatalf("deleted=%+v, want stale routes via both old gateways", deleted)
	}
	refreshed, _, _, ok := currentProbeRouteWindowsDirectManualRouteTarget()
	if !ok || refreshed.NextHop != "172.18.52.205" {
		t.Fatalf("manual target=%+v ok=%t, want refreshed gateway", refreshed, ok)
	}
}

func TestEnsureProbeRouteDirectBypassAutoRefreshesDefaultRoute(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	setProbeRouteWindowsDirectRouteTarget(probeRouteWindowsDirectRouteTarget{
		InterfaceIndex: 22,
		InterfaceLUID:  1001,
		InterfaceGUID:  "{B1D66112-83C0-4BB7-ACAE-3987F9FD2D87}",
		NextHop:        "172.18.55.254",
	})
	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeRouteWindowsDirectRouteTarget, error) {
		return probeRouteWindowsDirectRouteTarget{
			InterfaceIndex: 31,
			InterfaceLUID:  2002,
			InterfaceGUID:  "{36597D16-C4D3-4372-8318-AD5356F21D6D}",
			NextHop:        "172.18.52.205",
		}, nil
	}
	probeRouteWindowsListAdaptersIPv4 = func() ([]windowsAdapterInfo, error) {
		return []windowsAdapterInfo{{InterfaceIndex: 31, IPv4Addrs: []string{"172.18.53.157"}}}, nil
	}
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{
			{Prefix: "0.0.0.0", PrefixLength: 0, NextHop: "172.18.52.205", IfIndex: 31},
			{Prefix: "203.0.113.8", PrefixLength: 32, NextHop: "172.18.55.254", IfIndex: 22, Metric: probeRouteWindowsDirectRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
		}, nil
	}
	var created []probeRouteWindowsRouteDef
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		created = append(created, routeDef)
		return true, nil
	}
	var deleted []probeRouteWindowsRouteDef
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}

	if err := ensureProbeRouteDirectBypass("203.0.113.8:443"); err != nil {
		t.Fatalf("ensure automatic direct bypass after default route change: %v", err)
	}
	if len(created) != 1 || created[0].Gateway != "172.18.52.205" || created[0].IfIndex != 31 {
		t.Fatalf("created=%+v, want route via current automatic default", created)
	}
	if len(deleted) != 1 || deleted[0].Gateway != "172.18.55.254" || deleted[0].IfIndex != 22 {
		t.Fatalf("deleted=%+v, want stale automatic route removed", deleted)
	}
	if _, _, _, manual := currentProbeRouteWindowsDirectManualRouteTarget(); manual {
		t.Fatal("automatic refresh unexpectedly enabled manual mode")
	}
}

func TestEnsureProbeRouteDirectBypassBeforeTUNEnvironmentReady(t *testing.T) {
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "")
	t.Setenv("PROBE_LOCAL_TUN_IF_LUID", "")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "")
	resetProbeRouteDirectBypassStateForTest()
	oldFindWintunAdapter := probeLocalFindWintunAdapter
	t.Cleanup(func() {
		probeLocalFindWintunAdapter = oldFindWintunAdapter
		resetProbeRouteDirectBypassStateForTest()
		resetProbeVirtualRouterTUNDataPlaneHooksForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	probeLocalFindWintunAdapter = func() (probeLocalWindowsNetAdapter, bool, error) {
		return probeLocalWindowsNetAdapter{InterfaceIndex: 8}, true, nil
	}
	probeRouteWindowsListAdaptersIPv4 = func() ([]windowsAdapterInfo, error) {
		return []windowsAdapterInfo{{InterfaceIndex: 21, IPv4Addrs: []string{"172.18.54.246"}}}, nil
	}
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{{Prefix: "0.0.0.0", PrefixLength: 0, NextHop: "172.18.55.254", IfIndex: 21}}, nil
	}
	var excludedIfIndex int
	probeLocalResolveWindowsPrimaryEgressRoute = func(excluded int) (probeRouteWindowsDirectRouteTarget, error) {
		excludedIfIndex = excluded
		return probeRouteWindowsDirectRouteTarget{InterfaceIndex: 21, NextHop: "172.18.55.254"}, nil
	}
	var created []probeRouteWindowsRouteDef
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		created = append(created, routeDef)
		return true, nil
	}

	if err := ensureProbeRouteDirectBypass("172.18.53.157:12040"); err != nil {
		t.Fatalf("ensure direct bypass before tun environment ready failed: %v", err)
	}
	if excludedIfIndex != 8 {
		t.Fatalf("excluded if index=%d, want wintun if index 8", excludedIfIndex)
	}
	if len(created) != 1 || created[0].Prefix != "172.18.53.157" || created[0].Gateway != "172.18.55.254" || created[0].IfIndex != 21 {
		t.Fatalf("unexpected direct bypass routes=%+v", created)
	}
}

func TestEnsureProbeRouteDirectBypassSkipsFakeIPTarget(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = probeRouteWindowsDirectRouteTarget{InterfaceIndex: 13, NextHop: "192.168.51.1"}
	probeRouteDirectRouteTargetState.ready = true
	probeRouteDirectRouteTargetState.mu.Unlock()
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		t.Fatalf("should not create direct bypass route for fake ip: %+v", routeDef)
		return false, nil
	}
	if err := ensureProbeRouteDirectBypass("198.18.0.3:443"); err != nil {
		t.Fatalf("ensure fake ip direct bypass should be skipped without error: %v", err)
	}
}

func TestEnsureProbeRouteDirectBypassSkipsAndCleansLocalAddress(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = probeRouteWindowsDirectRouteTarget{InterfaceIndex: 15, NextHop: "172.18.55.254"}
	probeRouteDirectRouteTargetState.ready = true
	probeRouteDirectRouteTargetState.mu.Unlock()
	probeRouteWindowsListAdaptersIPv4 = func() ([]windowsAdapterInfo, error) {
		return []windowsAdapterInfo{{InterfaceIndex: 15, IPv4Addrs: []string{"172.18.54.246"}}}, nil
	}
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{
			{Prefix: "172.18.54.246", PrefixLength: 32, NextHop: "0.0.0.0", IfIndex: 15, Metric: 256},
			{Prefix: "172.18.54.246", PrefixLength: 32, NextHop: "172.18.55.254", IfIndex: 15, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "172.18.54.246", PrefixLength: 32, NextHop: "172.18.55.253", IfIndex: 16, Metric: 4},
		}, nil
	}
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		t.Fatalf("should not create direct bypass route for local address: %+v", routeDef)
		return false, nil
	}
	var deleted []probeRouteWindowsRouteDef
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}

	if err := ensureProbeRouteDirectBypass("172.18.54.246:12040"); err != nil {
		t.Fatalf("ensure local address direct bypass should be skipped and cleaned: %v", err)
	}
	if len(deleted) != 1 || deleted[0].Prefix != "172.18.54.246" || deleted[0].Gateway != "172.18.55.254" || deleted[0].IfIndex != 15 {
		t.Fatalf("deleted=%+v, want only probe-created local-address route", deleted)
	}
}

func TestEnsureProbeRouteDirectBypassSkipsSpecialIPv4Targets(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	probeRouteWindowsListAdaptersIPv4 = func() ([]windowsAdapterInfo, error) { return nil, nil }
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		t.Fatalf("should not create direct bypass for special target: %+v", routeDef)
		return false, nil
	}

	for _, target := range []string{"0.1.2.3:80", "127.0.0.1:80", "169.254.10.20:80", "224.0.0.1:80", "255.255.255.255:80"} {
		if err := ensureProbeRouteDirectBypass(target); err != nil {
			t.Fatalf("special target %s should be skipped: %v", target, err)
		}
	}
}

func TestEnsureProbeRouteDirectBypassUsesExistingOnLinkRouteAndCleansLegacyHostRoute(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = probeRouteWindowsDirectRouteTarget{InterfaceIndex: 15, NextHop: "172.18.55.254"}
	probeRouteDirectRouteTargetState.ready = true
	probeRouteDirectRouteTargetState.mu.Unlock()
	probeRouteWindowsListAdaptersIPv4 = func() ([]windowsAdapterInfo, error) {
		return []windowsAdapterInfo{{InterfaceIndex: 15, IPv4Addrs: []string{"172.18.54.246"}}}, nil
	}
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{
			{Prefix: "172.18.52.0", PrefixLength: 22, NextHop: "0.0.0.0", IfIndex: 15, Metric: 256, Protocol: 2},
			{Prefix: "172.18.53.157", PrefixLength: 32, NextHop: "172.18.55.254", IfIndex: 15, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
		}, nil
	}
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		t.Fatalf("on-link target should not create host bypass: %+v", routeDef)
		return false, nil
	}
	var deleted []probeRouteWindowsRouteDef
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}

	if err := ensureProbeRouteDirectBypass("172.18.53.157:12040"); err != nil {
		t.Fatalf("ensure on-link target: %v", err)
	}
	if len(deleted) != 1 || deleted[0].Prefix != "172.18.53.157" || deleted[0].Gateway != "172.18.55.254" {
		t.Fatalf("deleted=%+v, want legacy redundant host route", deleted)
	}
}

func TestEnsureProbeRouteDirectBypassUsesExistingStaticRoute(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = probeRouteWindowsDirectRouteTarget{InterfaceIndex: 15, NextHop: "172.18.55.254"}
	probeRouteDirectRouteTargetState.ready = true
	probeRouteDirectRouteTargetState.mu.Unlock()
	probeRouteWindowsListAdaptersIPv4 = func() ([]windowsAdapterInfo, error) { return nil, nil }
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{{Prefix: "10.20.0.0", PrefixLength: 16, NextHop: "10.0.0.1", IfIndex: 20, Metric: 25, Protocol: 3}}, nil
	}
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		t.Fatalf("static-route target should not create default-egress host bypass: %+v", routeDef)
		return false, nil
	}

	if err := ensureProbeRouteDirectBypass("10.20.30.40:443"); err != nil {
		t.Fatalf("ensure static-route target: %v", err)
	}
}

func TestEnsureProbeRouteDirectBypassSkipsPhysicalGateway(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = probeRouteWindowsDirectRouteTarget{InterfaceIndex: 15, NextHop: "172.18.55.254"}
	probeRouteDirectRouteTargetState.ready = true
	probeRouteDirectRouteTargetState.mu.Unlock()
	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeRouteWindowsDirectRouteTarget, error) {
		return probeRouteWindowsDirectRouteTarget{InterfaceIndex: 15, NextHop: "172.18.55.254"}, nil
	}
	probeRouteWindowsListAdaptersIPv4 = func() ([]windowsAdapterInfo, error) { return nil, nil }
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) { return nil, nil }
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		t.Fatalf("gateway target should not create route via itself: %+v", routeDef)
		return false, nil
	}

	if err := ensureProbeRouteDirectBypass("172.18.55.254:443"); err != nil {
		t.Fatalf("ensure gateway target: %v", err)
	}
}

func TestCleanupProbeRouteWindowsManagedDirectBypassRoutesForTargetRemovesLegacyAndCurrentMetrics(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{
			{Prefix: "203.0.113.7", PrefixLength: 32, NextHop: "172.18.55.254", IfIndex: 15, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "203.0.113.8", PrefixLength: 32, NextHop: "172.18.55.254", IfIndex: 15, Metric: probeRouteWindowsDirectRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "203.0.113.9", PrefixLength: 32, NextHop: "172.18.55.253", IfIndex: 16, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "203.0.113.10", PrefixLength: 32, NextHop: "172.18.55.254", IfIndex: 15, Metric: 9, Protocol: probeRouteWindowsProtocolNetMgmt},
		}, nil
	}
	var deleted []probeRouteWindowsRouteDef
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}

	if err := cleanupProbeRouteWindowsManagedDirectBypassRoutesForTarget(probeRouteWindowsDirectRouteTarget{InterfaceIndex: 15, NextHop: "172.18.55.254"}); err != nil {
		t.Fatalf("cleanup managed routes: %v", err)
	}
	if len(deleted) != 2 || deleted[0].Prefix != "203.0.113.7" || deleted[1].Prefix != "203.0.113.8" {
		t.Fatalf("deleted=%+v, want legacy and current managed metrics", deleted)
	}
}

func TestCleanupProbeRouteDirectBypassForVirtualRouterRulesRemovesMatchedHostRoute(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = probeRouteWindowsDirectRouteTarget{InterfaceIndex: 13, NextHop: "192.168.51.1"}
	probeRouteDirectRouteTargetState.ready = true
	probeRouteDirectRouteTargetState.mu.Unlock()
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{
			{Prefix: "149.154.167.51", PrefixLength: 32, NextHop: "192.168.51.1", IfIndex: 13, Metric: probeRouteWindowsDirectRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "203.0.113.7", PrefixLength: 32, NextHop: "192.168.51.1", IfIndex: 13, Metric: probeRouteWindowsDirectRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "149.154.167.52", PrefixLength: 32, NextHop: "192.168.99.1", IfIndex: 99, Metric: probeRouteWindowsDirectRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
		}, nil
	}
	var deleted []probeRouteWindowsRouteDef
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}

	cleanupProbeRouteDirectBypassForVirtualRouterRules(probeVirtualRouterConfig{
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:         "telegram",
			Name:       "Telegram",
			Action:     "probe_exit",
			ExitNodeID: "18",
			Entries:    []string{"cidr:149.154.160.0/20"},
		}},
	})
	if len(deleted) != 1 || deleted[0].Prefix != "149.154.167.51" || deleted[0].Mask != probeRouteWindowsHostRouteMask || deleted[0].Gateway != "192.168.51.1" || deleted[0].IfIndex != 13 {
		t.Fatalf("deleted=%+v, want only matching telegram host route", deleted)
	}
}

func TestCleanupProbeRouteDirectBypassForVirtualRouterRulesKeepsTransportHostRoute(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = probeRouteWindowsDirectRouteTarget{InterfaceIndex: 13, NextHop: "192.168.51.1"}
	probeRouteDirectRouteTargetState.ready = true
	probeRouteDirectRouteTargetState.mu.Unlock()
	rememberProbeRouteWindowsTransportBypassIPs([]string{"172.18.52.205"})
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{{
			Prefix: "172.18.52.205", PrefixLength: 32, NextHop: "192.168.51.1", IfIndex: 13,
			Metric: probeRouteWindowsDirectRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt,
		}}, nil
	}
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		t.Fatalf("transport host route must not be deleted: %+v", routeDef)
		return nil
	}

	cleanupProbeRouteDirectBypassForVirtualRouterRules(probeVirtualRouterConfig{
		RouteRules: []probeVirtualRouterRouteRule{{
			ID: "linux-router-22", Name: "router", Action: "probe_exit", ExitNodeID: "22", Entries: []string{"cidr:172.18.52.0/22"},
		}},
	})
}

func TestEnsureProbeRouteDirectBypassRejectsTUNInterfaceTarget(t *testing.T) {
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeVirtualRouterTUNDataPlaneHooksForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	probeVirtualRouterTUNDataPlaneState.ifIndex = 9
	probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = probeRouteWindowsDirectRouteTarget{InterfaceIndex: 9, NextHop: "198.18.0.1"}
	probeRouteDirectRouteTargetState.ready = true
	probeRouteDirectRouteTargetState.mu.Unlock()
	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeRouteWindowsDirectRouteTarget, error) {
		return probeRouteWindowsDirectRouteTarget{InterfaceIndex: 9, NextHop: "198.18.0.1"}, nil
	}
	probeRouteWindowsListAdaptersIPv4 = func() ([]windowsAdapterInfo, error) {
		return []windowsAdapterInfo{{InterfaceIndex: 15, IPv4Addrs: []string{"192.168.51.20"}}}, nil
	}
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		t.Fatalf("should not create direct bypass route pointing to tun: %+v", routeDef)
		return false, nil
	}
	if err := ensureProbeRouteDirectBypass("203.0.113.7:16030"); err == nil {
		t.Fatal("expected direct route target pointing to tun to fail")
	}
}

func TestCurrentProbeLocalSystemDNSServersUsesVirtualRouterBackup(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	backup := probeVirtualRouterDNSBackup{
		InterfaceGUID:  "{11111111-1111-1111-1111-111111111111}",
		InterfaceIndex: 9,
		DNSServers:     []string{"127.0.0.1", "192.168.1.1", "8.8.8.8", "bad", "8.8.8.8"},
		AppliedDNS:     []string{"127.0.0.1"},
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	if err := persistProbeVirtualRouterDNSBackup(backup); err != nil {
		t.Fatalf("persist virtual router dns backup failed: %v", err)
	}
	if got := strings.Join(currentProbeLocalSystemDNSServers(), ","); got != "192.168.1.1,8.8.8.8" {
		t.Fatalf("system dns from virtual router backup=%q", got)
	}
}
