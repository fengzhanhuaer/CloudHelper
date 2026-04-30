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

	calls := make([]string, 0, 8)
	probeLocalWindowsRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
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

func TestApplyProbeLocalProxyTakeoverSuccessWithRouteOnly(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsTakeoverStateForTest()
	})

	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	calls := make([]string, 0, 8)
	probeLocalWindowsRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
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

	probeLocalWindowsTakeoverState.mu.Lock()
	enabled := probeLocalWindowsTakeoverState.enabled
	gateway := probeLocalWindowsTakeoverState.tunGateway
	ifIndex := probeLocalWindowsTakeoverState.tunIfIndex
	probeLocalWindowsTakeoverState.mu.Unlock()
	if !enabled || gateway != "198.18.0.1" || ifIndex != 9 {
		t.Fatalf("unexpected takeover state: enabled=%v gateway=%q ifIndex=%d", enabled, gateway, ifIndex)
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
	if got := currentProbeLocalTUNDNSListenHost(); got != "198.18.0.1" {
		t.Fatalf("host=%q", got)
	}

	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.tunGateway = "bad-ip"
	probeLocalWindowsTakeoverState.mu.Unlock()
	if got := currentProbeLocalTUNDNSListenHost(); got != "" {
		t.Fatalf("expected empty host for invalid ip, got=%q", got)
	}
}
