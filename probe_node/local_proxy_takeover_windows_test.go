//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return nil, nil
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

func resetProbeLocalWindowsTakeoverStateForTest() {
	resetProbeLocalDirectBypassStateForTest()
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

func TestEnsureProbeLocalDirectBypassWritesHostRoute(t *testing.T) {
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
	if err := ensureProbeLocalDirectBypass("203.0.113.7:16030"); err != nil {
		t.Fatalf("ensure direct bypass failed: %v", err)
	}
	if len(created) != 1 || created[0].Prefix != "203.0.113.7" || created[0].Gateway != "192.168.51.1" || created[0].IfIndex != 13 {
		t.Fatalf("unexpected direct bypass routes=%+v", created)
	}
}

func TestEnsureProbeLocalDirectBypassSkipsFakeIPTarget(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeLocalDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	probeLocalDirectBypassRouteTargetState.routeTarget = probeLocalWindowsDirectBypassRouteTarget{InterfaceIndex: 13, NextHop: "192.168.51.1"}
	probeLocalDirectBypassRouteTargetState.ready = true
	probeLocalDirectBypassRouteTargetState.mu.Unlock()
	probeLocalCreateWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) (bool, error) {
		t.Fatalf("should not create direct bypass route for fake ip: %+v", routeDef)
		return false, nil
	}
	if err := ensureProbeLocalDirectBypass("198.18.0.3:443"); err != nil {
		t.Fatalf("ensure fake ip direct bypass should be skipped without error: %v", err)
	}
}

func TestEnsureProbeLocalDirectBypassRejectsTUNInterfaceTarget(t *testing.T) {
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeLocalDirectBypassStateForTest()
		resetProbeLocalTUNDataPlaneHooksForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	probeLocalTUNDataPlaneState.mu.Lock()
	probeLocalTUNDataPlaneState.ifIndex = 9
	probeLocalTUNDataPlaneState.mu.Unlock()
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	probeLocalDirectBypassRouteTargetState.routeTarget = probeLocalWindowsDirectBypassRouteTarget{InterfaceIndex: 9, NextHop: "198.18.0.1"}
	probeLocalDirectBypassRouteTargetState.ready = true
	probeLocalDirectBypassRouteTargetState.mu.Unlock()
	probeLocalCreateWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) (bool, error) {
		t.Fatalf("should not create direct bypass route pointing to tun: %+v", routeDef)
		return false, nil
	}
	if err := ensureProbeLocalDirectBypass("203.0.113.7:16030"); err == nil {
		t.Fatal("expected direct bypass target pointing to tun to fail")
	}
}

func TestCurrentProbeLocalTUNDNSListenHost(t *testing.T) {
	t.Setenv("PROBE_LOCAL_TUN_DNS_HOST", "198.18.0.2")
	if got := currentProbeLocalTUNDNSListenHost(); got != "198.18.0.2" {
		t.Fatalf("dns listen host=%q", got)
	}
}

func TestCurrentProbeLocalSystemDNSServersUsesBackupFirst(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	backup := probeLocalTUNPrimaryDNSBackup{
		Version:        1,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
		InterfaceIndex: 9,
		InterfaceGUID:  "{11111111-1111-1111-1111-111111111111}",
		DNSServers:     []string{"192.168.1.1", "8.8.8.8", "198.18.0.2", "bad"},
	}
	if err := persistProbeLocalTUNPrimaryDNSBackup(backup); err != nil {
		t.Fatalf("persist backup failed: %v", err)
	}
	if got := strings.Join(currentProbeLocalSystemDNSServers(), ","); got != "192.168.1.1,8.8.8.8" {
		t.Fatalf("system dns from backup=%q", got)
	}
}

func TestApplyProbeLocalTUNPrimaryDNSUsesBackupAndRestores(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")
	t.Setenv("PROBE_LOCAL_TUN_DNS_HOST", "198.18.0.2")
	oldSystemDNS := probeLocalDNSSystemServers
	probeLocalDNSSystemServers = func() []string { return []string{"192.168.1.1", "8.8.8.8"} }
	defer func() {
		probeLocalDNSSystemServers = oldSystemDNS
		resetProbeLocalWindowsNativeRouteHooksForTest()
		resetProbeLocalDNSServiceForTest()
	}()

	probeLocalFindWindowsAdapterByIfIndex = func(ifIndex int) (windowsAdapterInfo, error) {
		if ifIndex != 9 {
			t.Fatalf("ifIndex=%d", ifIndex)
		}
		return windowsAdapterInfo{InterfaceIndex: ifIndex, AdapterGUID: "{11111111-1111-1111-1111-111111111111}", DNSServers: []string{"192.168.1.1", "8.8.8.8"}}, nil
	}
	var applied [][]string
	probeLocalSetWindowsInterfaceDNS = func(interfaceGUID string, dnsServers []string) error {
		applied = append(applied, append([]string(nil), dnsServers...))
		return nil
	}
	probeLocalDNSListenPacket = func(_, _ string) (net.PacketConn, error) {
		return nil, errors.New("skip listener in test")
	}
	if err := applyProbeLocalTUNPrimaryDNS(); err != nil {
		t.Fatalf("apply primary dns failed: %v", err)
	}
	if len(applied) != 1 || strings.Join(applied[0], ",") != "198.18.0.2" {
		t.Fatalf("applied dns=%v", applied)
	}
	if err := restoreProbeLocalTUNPrimaryDNS(); err != nil {
		t.Fatalf("restore primary dns failed: %v", err)
	}
	if len(applied) != 2 || strings.Join(applied[1], ",") != "192.168.1.1,8.8.8.8" {
		t.Fatalf("restored dns=%v", applied)
	}
}
