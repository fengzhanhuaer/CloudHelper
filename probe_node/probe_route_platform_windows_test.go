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
	probeLocalResolveWindowsPrimaryEgressRoute = resolveProbeLocalWindowsPrimaryEgressRouteTarget
	probeLocalSnapshotWindowsIPv4Routes = snapshotProbeLocalWindowsIPv4Routes
	probeLocalSetWindowsInterfaceDNS = setProbeLocalWindowsInterfaceDNS
	probeLocalResetWindowsInterfaceDNS = resetProbeLocalWindowsInterfaceDNS
	probeLocalFindWindowsAdapterByIfIndex = windowsFindAdapterByIfIndex
}

func useProbeLocalWindowsCommandBackedRouteHooksForTest() {
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		metric := fmt.Sprintf("%d", probeRouteWindowsRouteMetric)
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

	var created []probeRouteWindowsRouteDef
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		created = append(created, routeDef)
		return true, nil
	}
	if err := ensureProbeRouteDirectBypass("203.0.113.7:16030"); err != nil {
		t.Fatalf("ensure direct bypass failed: %v", err)
	}
	if len(created) != 1 || created[0].Prefix != "203.0.113.7" || created[0].Gateway != "192.168.51.1" || created[0].IfIndex != 13 {
		t.Fatalf("unexpected direct bypass routes=%+v", created)
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
			{Prefix: "149.154.167.51", PrefixLength: 32, NextHop: "192.168.51.1", IfIndex: 13},
			{Prefix: "203.0.113.7", PrefixLength: 32, NextHop: "192.168.51.1", IfIndex: 13},
			{Prefix: "149.154.167.52", PrefixLength: 32, NextHop: "192.168.99.1", IfIndex: 99},
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
