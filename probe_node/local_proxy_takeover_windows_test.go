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
	_, _, err := resolveProbeLocalWindowsRouteTarget()
	if err == nil || !strings.Contains(err.Error(), "PROBE_LOCAL_TUN_GATEWAY") {
		t.Fatalf("expected missing gateway error, got: %v", err)
	}

	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "0")
	_, _, err = resolveProbeLocalWindowsRouteTarget()
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

func TestApplyProbeLocalProxyTakeoverRollbackOnFakeIPRouteFailure(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	oldRun := probeLocalWindowsRunCommand
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
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
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
	for _, prefix := range []string{probeLocalWindowsRouteSplitPrefixA, probeLocalWindowsRouteSplitPrefixB, "10.0.0.0", "172.16.0.0", "192.168.0.0"} {
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
	if !enabled || gateway != "198.18.0.1" || ifIndex != 9 || len(routeDefs) != 1 || routeDefs[0].Prefix != "198.18.0.0" {
		t.Fatalf("unexpected takeover state: enabled=%v gateway=%q ifIndex=%d routeDefs=%+v", enabled, gateway, ifIndex, routeDefs)
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
	probeLocalWindowsTakeoverState.routeDefs = []probeLocalWindowsRouteDef{{Prefix: "198.18.0.0", Mask: "255.254.0.0", Gateway: "198.18.0.1", IfIndex: 9}}
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
	if !hasWindowsRouteCommand(calls, "DELETE", "198.18.0.0") {
		t.Fatalf("expected fake ip route delete calls=%v", calls)
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
