//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestResolveProbeLocalWindowsRouteTargetRequiresEnv(t *testing.T) {
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "")
	_, err := resolveProbeLocalWindowsRouteTarget()
	if err == nil || !strings.Contains(err.Error(), "PROBE_LOCAL_TUN_GATEWAY") {
		t.Fatalf("expected missing gateway error, got: %v", err)
	}

	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "0")
	_, err = resolveProbeLocalWindowsRouteTarget()
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

	created, err := ensureProbeLocalWindowsSplitRoute(probeLocalWindowsRouteSplitPrefixA, probeLocalWindowsRouteSplitMaskA, "198.18.0.1", 9)
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

	if err := deleteProbeLocalWindowsSplitRoute(probeLocalWindowsRouteSplitPrefixA, probeLocalWindowsRouteSplitMaskA, "198.18.0.1", 9); err != nil {
		t.Fatalf("delete should ignore missing route, got err: %v", err)
	}
}

func resetProbeLocalWindowsNativeRouteHooksForTest() {
	probeLocalCreateWindowsRouteEntry = ensureProbeLocalWindowsRouteNative
	probeLocalDeleteWindowsRouteEntry = deleteProbeLocalWindowsRouteNative
	probeLocalListWindowsRouteEntries = listProbeLocalWindowsIPv4RouteEntries
	probeLocalResolveWindowsPrimaryEgressRoute = resolveProbeLocalWindowsPrimaryEgressRouteTarget
	probeLocalSnapshotWindowsIPv4Routes = snapshotProbeLocalWindowsIPv4Routes
	probeLocalSetWindowsInterfaceDNS = setProbeLocalWindowsInterfaceDNS
	probeLocalFindWindowsAdapterByIfIndex = windowsFindAdapterByIfIndex
}

func useProbeLocalWindowsCommandBackedRouteHooksForTest() {
	probeLocalCreateWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) (bool, error) {
		metric := fmt.Sprintf("%d", probeLocalWindowsRouteMetric)
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
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) error {
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

	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeLocalWindowsDirectBypassRouteTarget, error) {
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $exclude=%d; $route=Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | Where-Object { $_.InterfaceIndex -ne $exclude -and $_.NextHop } | Sort-Object @{Expression='RouteMetric';Ascending=$true}, @{Expression='InterfaceMetric';Ascending=$true} | Select-Object -First 1 @{Name='interface_index';Expression={[int]$_.InterfaceIndex}}, @{Name='next_hop';Expression={[string]$_.NextHop}}; if (-not $route) { throw 'usable ipv4 default route not found' }; $route | ConvertTo-Json -Compress`, excludedIfIndex)
		output, err := probeLocalWindowsRunCommand(6*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
		if err != nil {
			trimmed := strings.TrimSpace(output)
			if trimmed != "" {
				return probeLocalWindowsDirectBypassRouteTarget{}, fmt.Errorf("detect windows bypass route target failed: %w: %s", err, trimmed)
			}
			return probeLocalWindowsDirectBypassRouteTarget{}, fmt.Errorf("detect windows bypass route target failed: %w", err)
		}
		var routeTarget probeLocalWindowsDirectBypassRouteTarget
		if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &routeTarget); err != nil {
			return probeLocalWindowsDirectBypassRouteTarget{}, fmt.Errorf("decode windows bypass route target failed: %w", err)
		}
		return routeTarget, nil
	}
	probeLocalSnapshotWindowsIPv4Routes = func() (string, error) {
		return probeLocalWindowsRunCommand(6*time.Second, "route", "PRINT", "-4")
	}
}

func TestProbeLocalWindowsFakeIPRoutePrefixAndMask(t *testing.T) {
	prefix, mask := probeLocalWindowsFakeIPRoutePrefixAndMask("198.19.0.0/16")
	if prefix != "198.19.0.0" || mask != "255.255.0.0" {
		t.Fatalf("prefix=%q mask=%q", prefix, mask)
	}
	prefix, mask = probeLocalWindowsFakeIPRoutePrefixAndMask("bad-cidr")
	if prefix != "198.18.0.0" || mask != "255.254.0.0" {
		t.Fatalf("fallback prefix=%q mask=%q", prefix, mask)
	}
}

func TestProbeLocalWindowsTakeoverRouteDefsIncludeTunnelCIDRRules(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	oldSystemDNS := probeLocalDNSSystemServers
	probeLocalDNSSystemServers = func() []string { return nil }
	t.Cleanup(func() {
		probeLocalDNSSystemServers = oldSystemDNS
	})

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "telegram", Rules: []string{"cidr:91.108.4.0/22"}},
		{Group: "direct-only", Rules: []string{"cidr:203.0.113.0/24"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}

	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "telegram", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"},
		{Group: "direct-only", Action: "direct"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	routeTarget := probeLocalWindowsRouteTarget{Gateway: "198.18.0.1", InterfaceIndex: 9}
	routeDefs := probeLocalWindowsTakeoverRouteDefs(routeTarget)
	if len(routeDefs) != 4 {
		t.Fatalf("route defs=%+v", routeDefs)
	}
	if routeDefs[0].Prefix != probeLocalWindowsRouteSplitPrefixA || routeDefs[0].Mask != probeLocalWindowsRouteSplitMaskA {
		t.Fatalf("split route A=%+v", routeDefs[0])
	}
	if routeDefs[1].Prefix != probeLocalWindowsRouteSplitPrefixB || routeDefs[1].Mask != probeLocalWindowsRouteSplitMaskB {
		t.Fatalf("split route B=%+v", routeDefs[1])
	}
	if routeDefs[2].Prefix != "198.18.0.0" || routeDefs[2].Mask != "255.254.0.0" {
		t.Fatalf("fake ip route=%+v", routeDefs[2])
	}
	if routeDefs[3].Prefix != "91.108.4.0" || routeDefs[3].Mask != "255.255.252.0" || routeDefs[3].Gateway != "198.18.0.1" || routeDefs[3].IfIndex != 9 {
		t.Fatalf("cidr route=%+v", routeDefs[3])
	}
}

func TestProbeLocalWindowsTakeoverRouteDefsIncludeSystemDNSCaptureRoutes(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	t.Setenv("PROBE_LOCAL_TUN_DNS_HOST", "198.18.0.2")
	oldSystemDNS := probeLocalDNSSystemServers
	probeLocalDNSSystemServers = func() []string {
		return []string{"192.168.1.1", "8.8.8.8", "192.168.1.1", "198.18.0.2", "127.0.0.1", "bad"}
	}
	t.Cleanup(func() {
		probeLocalDNSSystemServers = oldSystemDNS
	})

	routeTarget := probeLocalWindowsRouteTarget{Gateway: "198.18.0.1", InterfaceLUID: 19, InterfaceIndex: 9}
	routeDefs := probeLocalWindowsTakeoverRouteDefs(routeTarget)
	for _, prefix := range []string{"192.168.1.1", "8.8.8.8"} {
		var matched bool
		for _, routeDef := range routeDefs {
			if routeDef.Prefix == prefix && routeDef.Mask == probeLocalWindowsHostRouteMask && routeDef.Gateway == "198.18.0.1" && routeDef.InterfaceLUID == 19 && routeDef.IfIndex == 9 {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("missing dns capture route prefix=%s routeDefs=%+v", prefix, routeDefs)
		}
	}
	for _, blocked := range []string{"198.18.0.2", "127.0.0.1"} {
		for _, routeDef := range routeDefs {
			if routeDef.Prefix == blocked && routeDef.Mask == probeLocalWindowsHostRouteMask {
				t.Fatalf("unexpected blocked dns capture route=%+v", routeDef)
			}
		}
	}
}

func TestApplyProbeLocalProxyTakeoverRollbackOnFakeIPRouteFailure(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	oldRun := probeLocalWindowsRunCommand
	oldSystemDNS := probeLocalDNSSystemServers
	probeLocalDNSSystemServers = func() []string { return nil }
	useProbeLocalWindowsCommandBackedRouteHooksForTest()

	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	calls := make([]string, 0, 12)
	probeLocalWindowsRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if name == "powershell" {
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		}
		if len(args) >= 2 && strings.EqualFold(args[0], "ADD") && args[1] == "198.18.0.0" {
			return "", errors.New("add route failed")
		}
		if len(args) >= 1 && strings.EqualFold(args[0], "PRINT") {
			return "route print snapshot", nil
		}
		return "", nil
	}
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		probeLocalDNSSystemServers = oldSystemDNS
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	err := applyProbeLocalProxyTakeover()
	if err == nil {
		t.Fatalf("expected apply failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "rollback") {
		t.Fatalf("expected rollback marker in error, got: %v", err)
	}

	probeLocalWindowsTakeoverState.mu.Lock()
	enabled := probeLocalWindowsTakeoverState.enabled
	probeLocalWindowsTakeoverState.mu.Unlock()
	if enabled {
		t.Fatalf("takeover state should remain disabled after rollback failure path")
	}
}

func resetProbeLocalWindowsTakeoverStateForTest() {
	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.enabled = false
	probeLocalWindowsTakeoverState.routePrintOutput = ""
	probeLocalWindowsTakeoverState.tunGateway = ""
	probeLocalWindowsTakeoverState.tunInterfaceLUID = 0
	probeLocalWindowsTakeoverState.tunIfIndex = 0
	probeLocalWindowsTakeoverState.bypassGateway = ""
	probeLocalWindowsTakeoverState.bypassInterfaceIdx = 0
	probeLocalWindowsTakeoverState.routeDefs = nil
	probeLocalWindowsTakeoverState.mu.Unlock()
}

func hasWindowsRouteCommand(calls []string, verb string, prefix string) bool {
	v := strings.ToUpper(strings.TrimSpace(verb))
	p := strings.TrimSpace(prefix)
	for _, call := range calls {
		clean := strings.ToUpper(strings.TrimSpace(call))
		if strings.Contains(clean, "ROUTE "+v+" "+strings.ToUpper(p)+" ") {
			return true
		}
	}
	return false
}

func TestApplyProbeLocalProxyTakeoverSuccessWithFakeIPRouteOnly(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	oldRun := probeLocalWindowsRunCommand
	oldSystemDNS := probeLocalDNSSystemServers
	probeLocalDNSSystemServers = func() []string { return []string{"192.168.1.1", "8.8.8.8"} }
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		probeLocalDNSSystemServers = oldSystemDNS
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")
	dnsCalls := 0
	probeLocalSetWindowsInterfaceDNS = func(interfaceGUID string, dnsServers []string) error {
		dnsCalls++
		return nil
	}

	calls := make([]string, 0, 12)
	probeLocalWindowsRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if name == "powershell" {
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		}
		if len(args) >= 1 && strings.EqualFold(args[0], "PRINT") {
			return "route print snapshot", nil
		}
		return "", nil
	}

	err := applyProbeLocalProxyTakeover()
	if err != nil {
		t.Fatalf("expected takeover success with route-only path, got: %v", err)
	}
	if !hasWindowsRouteCommand(calls, "ADD", "198.18.0.0") {
		t.Fatalf("expected fake ip route add call, calls=%v", calls)
	}
	if !hasWindowsRouteCommand(calls, "ADD", "192.168.1.1") || !hasWindowsRouteCommand(calls, "ADD", "8.8.8.8") {
		t.Fatalf("expected dns capture route add calls=%v", calls)
	}
	for _, prefix := range []string{"10.0.0.0", "172.16.0.0", "192.168.0.0"} {
		if hasWindowsRouteCommand(calls, "ADD", prefix) {
			t.Fatalf("unexpected legacy route add prefix=%s calls=%v", prefix, calls)
		}
	}
	if dnsCalls != 0 {
		t.Fatalf("enable proxy should not modify interface dns, dnsCalls=%d", dnsCalls)
	}

	probeLocalWindowsTakeoverState.mu.Lock()
	enabled := probeLocalWindowsTakeoverState.enabled
	gateway := probeLocalWindowsTakeoverState.tunGateway
	ifIndex := probeLocalWindowsTakeoverState.tunIfIndex
	routeDefs := append([]probeLocalWindowsRouteDef(nil), probeLocalWindowsTakeoverState.routeDefs...)
	probeLocalWindowsTakeoverState.mu.Unlock()
	if !enabled || gateway != "198.18.0.1" || ifIndex != 9 || len(routeDefs) != 5 {
		t.Fatalf("unexpected takeover state: enabled=%v gateway=%q ifIndex=%d routeDefs=%+v", enabled, gateway, ifIndex, routeDefs)
	}
}

func TestCleanupProbeLocalWindowsStaleTunnelDirectBypassRoutes(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalWindowsTakeoverStateForTest()
	t.Cleanup(func() {
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "telegram", Rules: []string{"cidr:149.154.160.0/20"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "telegram", Action: "tunnel", SelectedChainID: "5_pub", TunnelNodeID: "chain:5_pub"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeLocalWindowsDirectBypassRouteTarget, error) {
		if excludedIfIndex != 22 {
			t.Fatalf("excluded ifIndex=%d", excludedIfIndex)
		}
		return probeLocalWindowsDirectBypassRouteTarget{InterfaceIndex: 13, NextHop: "192.168.51.1"}, nil
	}
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{
			{Prefix: "149.154.175.54", PrefixLength: 32, NextHop: "192.168.51.1", IfIndex: 13, Metric: uint32(probeLocalWindowsRouteMetric)},
			{Prefix: "149.154.167.222", PrefixLength: 32, NextHop: "192.168.51.254", IfIndex: 13, Metric: uint32(probeLocalWindowsRouteMetric)},
			{Prefix: "8.8.8.8", PrefixLength: 32, NextHop: "192.168.51.1", IfIndex: 13, Metric: uint32(probeLocalWindowsRouteMetric)},
		}, nil
	}
	deleted := make([]probeLocalWindowsRouteDef, 0, 1)
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}

	removed, err := cleanupProbeLocalWindowsStaleTunnelDirectBypassRoutes(probeLocalWindowsRouteTarget{InterfaceIndex: 22})
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if removed != 1 || len(deleted) != 1 {
		t.Fatalf("removed=%d deleted=%+v", removed, deleted)
	}
	if deleted[0].Prefix != "149.154.175.54" || deleted[0].Mask != probeLocalWindowsHostRouteMask || deleted[0].Gateway != "192.168.51.1" || deleted[0].IfIndex != 13 {
		t.Fatalf("unexpected deleted route=%+v", deleted[0])
	}
}

func TestEnsureProbeLocalExplicitDirectBypassWritesHostRoute(t *testing.T) {
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeLocalDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	probeLocalDirectBypassRouteTargetState.routeTarget = probeLocalWindowsDirectBypassRouteTarget{InterfaceIndex: 13, NextHop: "192.168.51.1"}
	probeLocalDirectBypassRouteTargetState.ready = true
	probeLocalDirectBypassRouteTargetState.mu.Unlock()

	var created []probeLocalWindowsRouteDef
	probeLocalCreateWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) (bool, error) {
		created = append(created, routeDef)
		return true, nil
	}

	if err := ensureProbeLocalExplicitDirectBypass("203.0.113.7:16030"); err != nil {
		t.Fatalf("ensure direct bypass failed: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created routes=%+v", created)
	}
	if created[0].Prefix != "203.0.113.7" || created[0].Mask != probeLocalWindowsHostRouteMask || created[0].Gateway != "192.168.51.1" || created[0].IfIndex != 13 {
		t.Fatalf("unexpected route=%+v", created[0])
	}
}

func TestEnsureProbeLocalExplicitDirectBypassRequiresPreparedTargetDuringTUN(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeLocalDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.enabled = true
	probeLocalWindowsTakeoverState.tunIfIndex = 9
	probeLocalWindowsTakeoverState.mu.Unlock()
	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeLocalWindowsDirectBypassRouteTarget, error) {
		t.Fatalf("should not re-detect egress route during active tun takeover; excluded=%d", excludedIfIndex)
		return probeLocalWindowsDirectBypassRouteTarget{}, nil
	}
	probeLocalCreateWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) (bool, error) {
		t.Fatalf("should not create route without prepared bypass target: %+v", routeDef)
		return false, nil
	}

	if err := ensureProbeLocalExplicitDirectBypass("203.0.113.7:16030"); err == nil {
		t.Fatal("expected missing prepared bypass target to fail during tun takeover")
	}
}

func TestEnsureProbeLocalExplicitDirectBypassRejectsTUNInterfaceTarget(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeLocalDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.enabled = true
	probeLocalWindowsTakeoverState.tunIfIndex = 9
	probeLocalWindowsTakeoverState.mu.Unlock()
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	probeLocalDirectBypassRouteTargetState.routeTarget = probeLocalWindowsDirectBypassRouteTarget{InterfaceIndex: 9, NextHop: "198.18.0.1"}
	probeLocalDirectBypassRouteTargetState.ready = true
	probeLocalDirectBypassRouteTargetState.mu.Unlock()
	probeLocalCreateWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) (bool, error) {
		t.Fatalf("should not create direct bypass route pointing to tun: %+v", routeDef)
		return false, nil
	}

	if err := ensureProbeLocalExplicitDirectBypass("203.0.113.7:16030"); err == nil {
		t.Fatal("expected tun interface bypass target to fail")
	}
}

func TestEnsureProbeLocalExplicitDirectBypassSkipsDNSCaptureTarget(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeLocalDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.enabled = true
	probeLocalWindowsTakeoverState.tunGateway = "198.18.0.1"
	probeLocalWindowsTakeoverState.tunIfIndex = 9
	probeLocalWindowsTakeoverState.routeDefs = []probeLocalWindowsRouteDef{
		{Prefix: "192.168.1.1", Mask: probeLocalWindowsHostRouteMask, Gateway: "198.18.0.1", IfIndex: 9},
	}
	probeLocalWindowsTakeoverState.mu.Unlock()
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	probeLocalDirectBypassRouteTargetState.routeTarget = probeLocalWindowsDirectBypassRouteTarget{InterfaceIndex: 13, NextHop: "192.168.51.1"}
	probeLocalDirectBypassRouteTargetState.ready = true
	probeLocalDirectBypassRouteTargetState.mu.Unlock()

	var created []probeLocalWindowsRouteDef
	probeLocalCreateWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) (bool, error) {
		created = append(created, routeDef)
		return true, nil
	}

	if err := ensureProbeLocalExplicitDirectBypass("192.168.1.1:53"); err != nil {
		t.Fatalf("ensure direct bypass failed: %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("dns capture target should not create direct bypass routes=%+v", created)
	}
	if err := ensureProbeLocalExplicitDirectBypass("192.168.1.1:443"); err != nil {
		t.Fatalf("ensure non-dns direct bypass failed: %v", err)
	}
	if len(created) != 1 || created[0].Prefix != "192.168.1.1" || created[0].Gateway != "192.168.51.1" {
		t.Fatalf("expected non-dns direct bypass route, got=%+v", created)
	}
}

func TestRestoreProbeLocalProxyDirectDeletesFakeIPRouteOnly(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	oldRun := probeLocalWindowsRunCommand
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.enabled = true
	probeLocalWindowsTakeoverState.tunGateway = "198.18.0.1"
	probeLocalWindowsTakeoverState.tunIfIndex = 9
	probeLocalWindowsTakeoverState.routeDefs = []probeLocalWindowsRouteDef{
		{Prefix: probeLocalWindowsRouteSplitPrefixA, Mask: probeLocalWindowsRouteSplitMaskA, Gateway: "198.18.0.1", IfIndex: 9},
		{Prefix: probeLocalWindowsRouteSplitPrefixB, Mask: probeLocalWindowsRouteSplitMaskB, Gateway: "198.18.0.1", IfIndex: 9},
		{Prefix: "198.18.0.0", Mask: "255.254.0.0", Gateway: "198.18.0.1", IfIndex: 9},
		{Prefix: "192.168.1.1", Mask: probeLocalWindowsHostRouteMask, Gateway: "198.18.0.1", IfIndex: 9},
	}
	probeLocalWindowsTakeoverState.mu.Unlock()

	calls := make([]string, 0, 8)
	dnsCalls := 0
	probeLocalSetWindowsInterfaceDNS = func(interfaceGUID string, dnsServers []string) error {
		dnsCalls++
		return nil
	}
	probeLocalWindowsRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if len(args) >= 1 && strings.EqualFold(args[0], "PRINT") {
			return "route print snapshot", nil
		}
		return "", nil
	}

	if err := restoreProbeLocalProxyDirect(); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if !hasWindowsRouteCommand(calls, "DELETE", probeLocalWindowsRouteSplitPrefixA) {
		t.Fatalf("expected split route A delete calls=%v", calls)
	}
	if !hasWindowsRouteCommand(calls, "DELETE", probeLocalWindowsRouteSplitPrefixB) {
		t.Fatalf("expected split route B delete calls=%v", calls)
	}
	if !hasWindowsRouteCommand(calls, "DELETE", "198.18.0.0") {
		t.Fatalf("expected fake ip route delete calls=%v", calls)
	}
	if !hasWindowsRouteCommand(calls, "DELETE", "192.168.1.1") {
		t.Fatalf("expected dns capture route delete calls=%v", calls)
	}
	if dnsCalls != 0 {
		t.Fatalf("restore proxy should not modify interface dns, dnsCalls=%d", dnsCalls)
	}
}

func TestCurrentProbeLocalTUNDNSListenHost(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	t.Cleanup(resetProbeLocalWindowsTakeoverStateForTest)

	if got := currentProbeLocalTUNDNSListenHost(); got != "" {
		t.Fatalf("expected empty host when disabled, got=%q", got)
	}

	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.enabled = true
	probeLocalWindowsTakeoverState.tunGateway = "198.18.0.1"
	probeLocalWindowsTakeoverState.mu.Unlock()
	t.Setenv("PROBE_LOCAL_TUN_DNS_HOST", "198.18.0.2")
	if got := currentProbeLocalTUNDNSListenHost(); got != "198.18.0.2" {
		t.Fatalf("host=%q", got)
	}

	t.Setenv("PROBE_LOCAL_TUN_DNS_HOST", "")
	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.tunGateway = "bad-ip"
	probeLocalWindowsTakeoverState.mu.Unlock()
	if got := currentProbeLocalTUNDNSListenHost(); got != "198.18.0.2" {
		t.Fatalf("expected default tun interface dns host, got=%q", got)
	}
}

func TestCurrentProbeLocalSystemDNSServersSkipsTUNDNS(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	t.Setenv("PROBE_LOCAL_TUN_DNS_HOST", "198.18.0.2")

	backup := probeLocalTUNPrimaryDNSBackup{
		Version:        1,
		InterfaceIndex: 12,
		InterfaceGUID:  "{11111111-1111-1111-1111-111111111111}",
		DNSServers:     []string{"198.18.0.2", "192.168.1.1", "8.8.8.8"},
		AppliedDNS:     []string{"198.18.0.2"},
	}
	if err := persistProbeLocalTUNPrimaryDNSBackup(backup); err != nil {
		t.Fatalf("persist backup failed: %v", err)
	}

	if got := strings.Join(currentProbeLocalSystemDNSServers(), ","); got != "192.168.1.1,8.8.8.8" {
		t.Fatalf("system dns=%q", got)
	}
}

func TestApplyRestoreProbeLocalTUNPrimaryDNSBackup(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_DNS_HOST", "198.18.0.2")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")
	resetProbeLocalWindowsTakeoverStateForTest()
	resetProbeLocalDNSServiceForTest()
	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.enabled = true
	probeLocalWindowsTakeoverState.tunGateway = "198.18.0.1"
	probeLocalWindowsTakeoverState.tunIfIndex = 9
	probeLocalWindowsTakeoverState.mu.Unlock()
	t.Cleanup(func() {
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeLocalDNSServiceForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	probeLocalDNSListenPacket = func(_, _ string) (net.PacketConn, error) {
		return net.ListenPacket("udp", "127.0.0.1:0")
	}
	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeLocalWindowsDirectBypassRouteTarget, error) {
		if excludedIfIndex != 9 {
			t.Fatalf("excluded ifindex=%d, want 9", excludedIfIndex)
		}
		return probeLocalWindowsDirectBypassRouteTarget{InterfaceIndex: 12, NextHop: "192.168.1.1"}, nil
	}
	probeLocalFindWindowsAdapterByIfIndex = func(interfaceIndex int) (windowsAdapterInfo, error) {
		if interfaceIndex != 12 {
			return windowsAdapterInfo{}, fmt.Errorf("unexpected interface index: %d", interfaceIndex)
		}
		return windowsAdapterInfo{
			InterfaceIndex: 12,
			Name:           "Ethernet",
			AdapterGUID:    "{11111111-1111-1111-1111-111111111111}",
			DNSServers:     []string{"192.168.1.1", "8.8.8.8"},
		}, nil
	}
	dnsCalls := make([][]string, 0, 2)
	probeLocalSetWindowsInterfaceDNS = func(interfaceGUID string, dnsServers []string) error {
		call := append([]string{strings.TrimSpace(interfaceGUID)}, dnsServers...)
		dnsCalls = append(dnsCalls, call)
		return nil
	}

	if err := applyProbeLocalTUNPrimaryDNS(); err != nil {
		t.Fatalf("applyProbeLocalTUNPrimaryDNS returned error: %v", err)
	}
	if len(dnsCalls) != 1 {
		t.Fatalf("dns calls=%v, want 1", dnsCalls)
	}
	if dnsCalls[0][0] != "{11111111-1111-1111-1111-111111111111}" || dnsCalls[0][1] != "198.18.0.2" {
		t.Fatalf("apply dns call=%v", dnsCalls[0])
	}
	if got := strings.Join(currentProbeLocalSystemDNSServers(), ","); got != "192.168.1.1,8.8.8.8" {
		t.Fatalf("system dns from backup=%q", got)
	}
	backupPath, err := resolveProbeLocalTUNPrimaryDNSBackupPath()
	if err != nil {
		t.Fatalf("resolve backup path failed: %v", err)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("expected backup file: %v", err)
	}

	if err := restoreProbeLocalTUNPrimaryDNS(); err != nil {
		t.Fatalf("restoreProbeLocalTUNPrimaryDNS returned error: %v", err)
	}
	if len(dnsCalls) != 2 {
		t.Fatalf("dns calls=%v, want 2", dnsCalls)
	}
	if got := strings.Join(dnsCalls[1][1:], ","); got != "192.168.1.1,8.8.8.8" {
		t.Fatalf("restore dns servers=%q", got)
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup file should be removed, stat err=%v", err)
	}
}

func TestApplyProbeLocalTUNPrimaryDNSRejectsTUNOnlySystemDNS(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_DNS_HOST", "198.18.0.2")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")
	resetProbeLocalWindowsTakeoverStateForTest()
	resetProbeLocalDNSServiceForTest()
	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.enabled = true
	probeLocalWindowsTakeoverState.tunGateway = "198.18.0.1"
	probeLocalWindowsTakeoverState.tunIfIndex = 9
	probeLocalWindowsTakeoverState.mu.Unlock()
	t.Cleanup(func() {
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeLocalDNSServiceForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	probeLocalDNSListenPacket = func(_, _ string) (net.PacketConn, error) {
		return net.ListenPacket("udp", "127.0.0.1:0")
	}
	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeLocalWindowsDirectBypassRouteTarget, error) {
		return probeLocalWindowsDirectBypassRouteTarget{InterfaceIndex: 12, NextHop: "192.168.1.1"}, nil
	}
	probeLocalFindWindowsAdapterByIfIndex = func(interfaceIndex int) (windowsAdapterInfo, error) {
		return windowsAdapterInfo{
			InterfaceIndex: interfaceIndex,
			Name:           "Ethernet",
			AdapterGUID:    "{11111111-1111-1111-1111-111111111111}",
			DNSServers:     []string{"198.18.0.2"},
		}, nil
	}
	probeLocalSetWindowsInterfaceDNS = func(interfaceGUID string, dnsServers []string) error {
		t.Fatalf("unexpected dns apply for polluted system dns: guid=%s dns=%v", interfaceGUID, dnsServers)
		return nil
	}

	err := applyProbeLocalTUNPrimaryDNS()
	if err == nil || !strings.Contains(err.Error(), "match tun dns") {
		t.Fatalf("expected polluted system dns error, got: %v", err)
	}
	if got := strings.Join(currentProbeLocalSystemDNSServers(), ","); got != "" {
		t.Fatalf("system dns should skip tun dns only value, got=%q", got)
	}
}
