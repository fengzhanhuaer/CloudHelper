//go:build linux

package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEnsureProbeVirtualRouterPlatformInterfaceIPLinuxAddsDNSAndNodeIP(t *testing.T) {
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	oldNewRunner := probeLocalLinuxNewTUNDataPlaneRunner
	t.Cleanup(func() {
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
		probeLocalLinuxNewTUNDataPlaneRunner = oldNewRunner
		_ = stopProbeLocalTUNDataPlane()
	})

	t.Setenv("PROBE_LOCAL_TUN_DEV", "probe0")
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
	calls := make([]string, 0, 3)
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	probeLocalLinuxNewTUNDataPlaneRunner = func(dev string) (probeLocalTUNDataPlane, error) {
		return &fakeProbeLocalLinuxTUNRunner{stats: probeLocalTUNDataPlaneStats{Running: true}}, nil
	}

	if err := ensureProbeVirtualRouterPlatformInterfaceIP("198.18.0.11"); err != nil {
		t.Fatalf("ensure failed: %v", err)
	}

	for _, want := range []string{
		"ip link set dev probe0 up",
		"ip -4 addr replace 198.18.0.2/15 dev probe0",
		"ip -4 route replace 198.18.0.0/15 dev probe0 src 198.18.0.2",
		"ip -4 addr replace 198.18.0.11/15 dev probe0",
		"ip -4 route replace 198.18.0.0/15 dev probe0 src 198.18.0.11",
	} {
		if !hasProbeVirtualRouterLinuxCommand(calls, want) {
			t.Fatalf("missing command %q calls=%v", want, calls)
		}
	}
}

func TestEnsureProbeVirtualRouterPlatformInterfaceIPLinuxCreatesDefaultDeviceWhenUnset(t *testing.T) {
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	oldNewRunner := probeLocalLinuxNewTUNDataPlaneRunner
	t.Cleanup(func() {
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
		probeLocalLinuxNewTUNDataPlaneRunner = oldNewRunner
		_ = stopProbeLocalTUNDataPlane()
	})

	probeLocalLinuxStat = func(name string) (os.FileInfo, error) {
		if name != "/dev/net/tun" {
			return nil, errors.New("unexpected stat path")
		}
		return fakeProbeLocalLinuxFileInfo{}, nil
	}
	probeLocalLinuxLookPath = func(file string) (string, error) { return "/sbin/ip", nil }
	calls := make([]string, 0, 5)
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, name+" "+joined)
		if joined == "link show dev "+probeLocalLinuxDefaultTUNDeviceName {
			return "", errors.New("Cannot find device")
		}
		return "", nil
	}
	probeLocalLinuxNewTUNDataPlaneRunner = func(dev string) (probeLocalTUNDataPlane, error) {
		return &fakeProbeLocalLinuxTUNRunner{stats: probeLocalTUNDataPlaneStats{Running: true}}, nil
	}

	if err := ensureProbeVirtualRouterPlatformInterfaceIP("198.18.0.11"); err != nil {
		t.Fatalf("ensure without dev should create default device: %v", err)
	}
	for _, want := range []string{
		"ip link show dev cloudhelper0",
		"ip tuntap add dev cloudhelper0 mode tun",
		"ip link set dev cloudhelper0 up",
		"ip -4 addr replace 198.18.0.2/15 dev cloudhelper0",
		"ip -4 route replace 198.18.0.0/15 dev cloudhelper0 src 198.18.0.2",
		"ip -4 addr replace 198.18.0.11/15 dev cloudhelper0",
		"ip -4 route replace 198.18.0.0/15 dev cloudhelper0 src 198.18.0.11",
	} {
		if !hasProbeVirtualRouterLinuxCommand(calls, want) {
			t.Fatalf("missing command %q calls=%v", want, calls)
		}
	}
}

func TestEnsureProbeVirtualRouterPlatformInterfaceIPLinuxRejectsInvalidIP(t *testing.T) {
	if err := ensureProbeVirtualRouterPlatformInterfaceIP("not-an-ip"); err == nil {
		t.Fatalf("expected invalid ip error")
	}
}

func TestApplyProbeVirtualRouterConfigForNodeLinuxStartsTUNAndVirtualIP(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	oldNewRunner := probeLocalLinuxNewTUNDataPlaneRunner
	t.Cleanup(func() {
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
		probeLocalLinuxNewTUNDataPlaneRunner = oldNewRunner
		_ = stopProbeLocalTUNDataPlane()
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = probeVirtualRouterConfig{}
		probeVirtualRouterState.localNodeID = ""
		probeVirtualRouterState.mu.Unlock()
	})

	probeLocalLinuxStat = func(name string) (os.FileInfo, error) { return fakeProbeLocalLinuxFileInfo{}, nil }
	probeLocalLinuxLookPath = func(file string) (string, error) { return "/sbin/ip", nil }
	calls := make([]string, 0, 8)
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	starts := 0
	probeLocalLinuxNewTUNDataPlaneRunner = func(dev string) (probeLocalTUNDataPlane, error) {
		starts++
		return &fakeProbeLocalLinuxTUNRunner{stats: probeLocalTUNDataPlaneStats{Running: true}}, nil
	}

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "19", IP: "198.18.0.21"},
		},
	}, "19")

	if starts != 1 {
		t.Fatalf("linux tun data plane starts=%d want 1", starts)
	}
	if !probeLocalTUNDataPlaneRunning() {
		t.Fatalf("linux tun data plane should be running")
	}
	if !hasProbeVirtualRouterLinuxCommand(calls, "ip -4 addr replace 198.18.0.21/15 dev cloudhelper0") {
		t.Fatalf("missing local virtual ip command calls=%v", calls)
	}
	if !hasProbeVirtualRouterLinuxCommand(calls, "ip -4 route replace 198.18.0.0/15 dev cloudhelper0 src 198.18.0.21") {
		t.Fatalf("missing local virtual route command calls=%v", calls)
	}
}

func hasProbeVirtualRouterLinuxCommand(calls []string, want string) bool {
	for _, call := range calls {
		if strings.TrimSpace(call) == want {
			return true
		}
	}
	return false
}
