//go:build windows

package main

import (
	"errors"
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
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
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
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
	})

	if err := deleteProbeLocalWindowsSplitRoute(probeLocalWindowsRouteSplitPrefixA, probeLocalWindowsRouteSplitMaskA, "198.18.0.1", 9); err != nil {
		t.Fatalf("delete should ignore missing route, got err: %v", err)
	}
}

func TestApplyProbeLocalProxyTakeoverRollbackOnSecondRouteFailure(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	oldRun := probeLocalWindowsRunCommand

	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	calls := make([]string, 0, 12)
	probeLocalWindowsRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if name == "powershell" {
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		}
		if len(args) >= 2 && strings.EqualFold(args[0], "ADD") && args[1] == probeLocalWindowsRouteSplitPrefixB {
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
	})

	err := applyProbeLocalProxyTakeover()
	if err == nil {
		t.Fatalf("expected apply failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "rollback") {
		t.Fatalf("expected rollback marker in error, got: %v", err)
	}

	if !hasWindowsRouteCommand(calls, "DELETE", probeLocalWindowsRouteSplitPrefixA) {
		t.Fatalf("expected rollback delete for first split route, calls=%v", calls)
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

func TestApplyProbeLocalProxyTakeoverRollbackOnLocalBypassFailure(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsTakeoverStateForTest()
	})

	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	calls := make([]string, 0, 16)
	probeLocalWindowsRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if name == "powershell" {
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		}
		if len(args) >= 2 && strings.EqualFold(args[0], "ADD") && args[1] == "172.16.0.0" {
			return "", errors.New("add local bypass failed")
		}
		if len(args) >= 1 && strings.EqualFold(args[0], "PRINT") {
			return "route print snapshot", nil
		}
		return "", nil
	}

	err := applyProbeLocalProxyTakeover()
	if err == nil {
		t.Fatalf("expected local bypass failure")
	}
	for _, prefix := range []string{probeLocalWindowsRouteSplitPrefixA, probeLocalWindowsRouteSplitPrefixB, "10.0.0.0"} {
		if !hasWindowsRouteCommand(calls, "DELETE", prefix) {
			t.Fatalf("expected rollback delete for prefix=%s calls=%v", prefix, calls)
		}
	}
	if hasWindowsRouteCommand(calls, "DELETE", "172.16.0.0") {
		t.Fatalf("failed local bypass route should not be deleted as created route: calls=%v", calls)
	}
}

func TestApplyProbeLocalProxyTakeoverSuccessWithRouteOnly(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsTakeoverStateForTest()
	})

	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

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
	if !hasWindowsRouteCommand(calls, "ADD", probeLocalWindowsRouteSplitPrefixA) {
		t.Fatalf("expected split route A add call, calls=%v", calls)
	}
	if !hasWindowsRouteCommand(calls, "ADD", probeLocalWindowsRouteSplitPrefixB) {
		t.Fatalf("expected split route B add call, calls=%v", calls)
	}
	for _, prefix := range []string{"10.0.0.0", "172.16.0.0", "192.168.0.0"} {
		if !hasWindowsRouteCommand(calls, "ADD", prefix) {
			t.Fatalf("expected local bypass route add prefix=%s calls=%v", prefix, calls)
		}
	}

	probeLocalWindowsTakeoverState.mu.Lock()
	enabled := probeLocalWindowsTakeoverState.enabled
	gateway := probeLocalWindowsTakeoverState.tunGateway
	ifIndex := probeLocalWindowsTakeoverState.tunIfIndex
	bypassGateway := probeLocalWindowsTakeoverState.bypassGateway
	bypassIfIndex := probeLocalWindowsTakeoverState.bypassInterfaceIdx
	probeLocalWindowsTakeoverState.mu.Unlock()
	if !enabled || gateway != "198.18.0.1" || ifIndex != 9 || bypassGateway != "192.168.1.1" || bypassIfIndex != 12 {
		t.Fatalf("unexpected takeover state: enabled=%v gateway=%q ifIndex=%d bypassGateway=%q bypassIfIndex=%d", enabled, gateway, ifIndex, bypassGateway, bypassIfIndex)
	}
}

func TestRestoreProbeLocalProxyDirectDeletesLocalBypassRoutes(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsTakeoverStateForTest()
	})

	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.enabled = true
	probeLocalWindowsTakeoverState.tunGateway = "198.18.0.1"
	probeLocalWindowsTakeoverState.tunIfIndex = 9
	probeLocalWindowsTakeoverState.bypassGateway = "192.168.1.1"
	probeLocalWindowsTakeoverState.bypassInterfaceIdx = 12
	probeLocalWindowsTakeoverState.mu.Unlock()

	calls := make([]string, 0, 8)
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
	for _, prefix := range []string{probeLocalWindowsRouteSplitPrefixA, probeLocalWindowsRouteSplitPrefixB, "10.0.0.0", "172.16.0.0", "192.168.0.0"} {
		if !hasWindowsRouteCommand(calls, "DELETE", prefix) {
			t.Fatalf("expected delete prefix=%s calls=%v", prefix, calls)
		}
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
	if got := currentProbeLocalTUNDNSListenHost(); got != "" {
		t.Fatalf("expected empty host for invalid ip, got=%q", got)
	}
}
