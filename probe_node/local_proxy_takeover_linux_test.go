//go:build linux

package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestApplyProbeLocalProxyTakeoverLinuxSuccessWithLocalBypass(t *testing.T) {
	resetProbeLocalLinuxTakeoverStateForTest()
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	t.Cleanup(func() {
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
		resetProbeLocalLinuxTakeoverStateForTest()
	})

	t.Setenv("PROBE_LOCAL_TUN_DEV", "probe0")
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	probeLocalLinuxStat = func(name string) (os.FileInfo, error) {
		if name != "/dev/net/tun" {
			return nil, errors.New("unexpected stat path")
		}
		return fakeProbeLocalLinuxFileInfo{}, nil
	}
	probeLocalLinuxLookPath = func(file string) (string, error) {
		if file != "ip" {
			return "", errors.New("unexpected lookpath")
		}
		return "/sbin/ip", nil
	}
	calls := make([]string, 0, 8)
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if strings.Join(args, " ") == "-4 route show default" {
			return "default via 192.168.1.1 dev eth0", nil
		}
		return "", nil
	}

	if err := applyProbeLocalProxyTakeover(); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	for _, prefix := range probeLocalLinuxTakeoverRoutePrefixes() {
		if !hasLinuxRouteCommand(calls, "replace", prefix) {
			t.Fatalf("missing replace prefix=%s calls=%v", prefix, calls)
		}
	}

	probeLocalLinuxTakeoverState.mu.Lock()
	enabled := probeLocalLinuxTakeoverState.enabled
	dev := probeLocalLinuxTakeoverState.tunDevice
	gateway := probeLocalLinuxTakeoverState.tunGateway
	probeLocalLinuxTakeoverState.mu.Unlock()
	if !enabled || dev != "probe0" || gateway != "198.18.0.1" {
		t.Fatalf("unexpected state enabled=%v dev=%q gateway=%q", enabled, dev, gateway)
	}
}

func TestApplyProbeLocalProxyTakeoverLinuxRollbackOnLocalBypassFailure(t *testing.T) {
	resetProbeLocalLinuxTakeoverStateForTest()
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	t.Cleanup(func() {
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
		resetProbeLocalLinuxTakeoverStateForTest()
	})

	t.Setenv("PROBE_LOCAL_TUN_DEV", "probe0")
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	probeLocalLinuxStat = func(name string) (os.FileInfo, error) { return fakeProbeLocalLinuxFileInfo{}, nil }
	probeLocalLinuxLookPath = func(file string) (string, error) { return "/sbin/ip", nil }
	calls := make([]string, 0, 12)
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, name+" "+joined)
		if joined == "-4 route show default" {
			return "default via 192.168.1.1 dev eth0", nil
		}
		if strings.Contains(joined, "route replace 172.16.0.0/12") {
			return "", errors.New("replace local bypass failed")
		}
		return "", nil
	}

	err := applyProbeLocalProxyTakeover()
	if err == nil {
		t.Fatalf("expected apply failure")
	}
	for _, prefix := range []string{probeLocalLinuxRouteSplitPrefixA, probeLocalLinuxRouteSplitPrefixB, "10.0.0.0/8"} {
		if !hasLinuxRouteCommand(calls, "del", prefix) {
			t.Fatalf("missing rollback delete prefix=%s calls=%v", prefix, calls)
		}
	}
	if hasLinuxRouteCommand(calls, "del", "172.16.0.0/12") {
		t.Fatalf("failed prefix should not be deleted as applied route: calls=%v", calls)
	}
}

func TestRestoreProbeLocalProxyDirectLinuxDeletesLocalBypassRoutes(t *testing.T) {
	resetProbeLocalLinuxTakeoverStateForTest()
	oldRun := probeLocalLinuxRunCommand
	t.Cleanup(func() {
		probeLocalLinuxRunCommand = oldRun
		resetProbeLocalLinuxTakeoverStateForTest()
	})

	probeLocalLinuxTakeoverState.mu.Lock()
	probeLocalLinuxTakeoverState.enabled = true
	probeLocalLinuxTakeoverState.tunDevice = "probe0"
	probeLocalLinuxTakeoverState.tunGateway = "198.18.0.1"
	probeLocalLinuxTakeoverState.mu.Unlock()

	calls := make([]string, 0, 8)
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}

	if err := restoreProbeLocalProxyDirect(); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	for _, prefix := range probeLocalLinuxTakeoverRoutePrefixes() {
		if !hasLinuxRouteCommand(calls, "del", prefix) {
			t.Fatalf("missing delete prefix=%s calls=%v", prefix, calls)
		}
	}
}

func resetProbeLocalLinuxTakeoverStateForTest() {
	probeLocalLinuxTakeoverState.mu.Lock()
	probeLocalLinuxTakeoverState.enabled = false
	probeLocalLinuxTakeoverState.defaultRouteSnapshot = ""
	probeLocalLinuxTakeoverState.tunDevice = ""
	probeLocalLinuxTakeoverState.tunGateway = ""
	probeLocalLinuxTakeoverState.mu.Unlock()
}

func hasLinuxRouteCommand(calls []string, verb string, prefix string) bool {
	wantVerb := strings.ToLower(strings.TrimSpace(verb))
	wantPrefix := strings.TrimSpace(prefix)
	for _, call := range calls {
		clean := strings.TrimSpace(call)
		if strings.Contains(clean, " route "+wantVerb+" "+wantPrefix) {
			return true
		}
	}
	return false
}

type fakeProbeLocalLinuxFileInfo struct{}

func (fakeProbeLocalLinuxFileInfo) Name() string       { return "tun" }
func (fakeProbeLocalLinuxFileInfo) Size() int64        { return 0 }
func (fakeProbeLocalLinuxFileInfo) Mode() os.FileMode  { return os.ModeCharDevice }
func (fakeProbeLocalLinuxFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeProbeLocalLinuxFileInfo) IsDir() bool        { return false }
func (fakeProbeLocalLinuxFileInfo) Sys() any           { return nil }
