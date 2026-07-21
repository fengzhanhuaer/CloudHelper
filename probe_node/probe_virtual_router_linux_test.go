//go:build linux

package main

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnsureProbeVirtualRouterPlatformInterfaceIPLinuxAddsDNSAndNodeIP(t *testing.T) {
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	oldNewRunner := probeVirtualRouterLinuxNewTUNDataPlaneRunner
	resetProbeVirtualRouterLinuxRouteStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(func() {
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
		probeVirtualRouterLinuxNewTUNDataPlaneRunner = oldNewRunner
		resetProbeVirtualRouterLinuxRouteStateForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
		_ = stopProbeVirtualRouterTUNDataPlane()
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
		if strings.Join(args, " ") == "-o -4 addr show dev probe0" {
			return strings.Join([]string{
				"7: probe0    inet 198.18.0.2/15 scope global probe0",
				"7: probe0    inet 198.18.0.11/15 scope global secondary probe0",
				"7: probe0    inet 198.18.0.21/15 scope global secondary probe0",
				"7: probe0    inet 10.20.30.40/24 scope global probe0",
			}, "\n"), nil
		}
		return "", nil
	}
	probeVirtualRouterLinuxNewTUNDataPlaneRunner = func(dev string) (probeVirtualRouterTUNDataPlane, error) {
		return &fakeProbeVirtualRouterLinuxTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true}}, nil
	}

	if err := ensureProbeVirtualRouterPlatformInterfaceIP("198.18.0.11"); err != nil {
		t.Fatalf("ensure failed: %v", err)
	}

	for _, want := range []string{
		"ip link set dev probe0 up",
		"ip -4 addr replace 198.18.0.2/15 dev probe0",
		"ip -4 route replace 198.18.0.0/15 dev probe0 src 198.18.0.2",
		"ip -o -4 addr show dev probe0",
		"ip -4 addr del 198.18.0.21/15 dev probe0",
		"ip -4 addr replace 198.18.0.11/15 dev probe0",
		"ip -4 route replace 198.18.0.0/15 dev probe0 src 198.18.0.11",
	} {
		if !hasProbeVirtualRouterLinuxCommand(calls, want) {
			t.Fatalf("missing command %q calls=%v", want, calls)
		}
	}
	for _, unwanted := range []string{
		"ip -4 addr del 198.18.0.2/15 dev probe0",
		"ip -4 addr del 198.18.0.11/15 dev probe0",
		"ip -4 addr del 10.20.30.40/24 dev probe0",
	} {
		if hasProbeVirtualRouterLinuxCommand(calls, unwanted) {
			t.Fatalf("unexpected command %q calls=%v", unwanted, calls)
		}
	}
}

func TestEnsureProbeVirtualRouterPlatformInterfaceIPLinuxAppliesTakeoverAndLocalBypassRoutes(t *testing.T) {
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	oldNewRunner := probeVirtualRouterLinuxNewTUNDataPlaneRunner
	resetProbeVirtualRouterLinuxRouteStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(true, false)
	t.Cleanup(func() {
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
		probeVirtualRouterLinuxNewTUNDataPlaneRunner = oldNewRunner
		resetProbeVirtualRouterLinuxRouteStateForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
		_ = stopProbeVirtualRouterTUNDataPlane()
	})

	t.Setenv("PROBE_LOCAL_TUN_DEV", "probe0")
	probeLocalLinuxStat = func(name string) (os.FileInfo, error) { return fakeProbeLocalLinuxFileInfo{}, nil }
	probeLocalLinuxLookPath = func(file string) (string, error) { return "/sbin/ip", nil }
	calls := make([]string, 0, 10)
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, name+" "+joined)
		if joined == "-4 route show default" {
			return "default via 192.168.50.1 dev eth0 proto dhcp metric 100\n", nil
		}
		return "", nil
	}
	probeVirtualRouterLinuxNewTUNDataPlaneRunner = func(dev string) (probeVirtualRouterTUNDataPlane, error) {
		return &fakeProbeVirtualRouterLinuxTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true}}, nil
	}

	if err := ensureProbeVirtualRouterPlatformInterfaceIP("198.18.0.11"); err != nil {
		t.Fatalf("ensure failed: %v", err)
	}
	for _, want := range []string{
		"ip -4 route replace 0.0.0.0/1 dev probe0 src 198.18.0.11 metric 3",
		"ip -4 route replace 128.0.0.0/1 dev probe0 src 198.18.0.11 metric 3",
		"ip -4 route replace 10.0.0.0/8 via 192.168.50.1 dev eth0 metric 3",
		"ip -4 route replace 172.16.0.0/12 via 192.168.50.1 dev eth0 metric 3",
		"ip -4 route replace 192.168.0.0/16 via 192.168.50.1 dev eth0 metric 3",
	} {
		if !hasProbeVirtualRouterLinuxCommand(calls, want) {
			t.Fatalf("missing command %q calls=%v", want, calls)
		}
	}
}

func TestSnapshotProbeVirtualRouterRuntimeStatsIncludesLinuxTUNDataPlaneStats(t *testing.T) {
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	t.Cleanup(resetProbeVirtualRouterTUNDataPlaneHooksForTest)
	fake := &fakeProbeVirtualRouterLinuxTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true, RXPackets: 12, RXBytes: 1200, TXPackets: 4, TXBytes: 400}}
	probeLocalLinuxTUNDataPlaneState.mu.Lock()
	probeLocalLinuxTUNDataPlaneState.runner = fake
	probeLocalLinuxTUNDataPlaneState.dev = "probe0"
	probeLocalLinuxTUNDataPlaneState.mu.Unlock()

	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldItems := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		"vrouter-tun-stats": {},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldItems
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	stats := snapshotProbeVirtualRouterRuntimeStats("vrouter-tun-stats")
	if stats == nil {
		t.Fatalf("stats missing")
	}
	if !stats.TUNDataPlane || stats.TUNRXPackets != 12 || stats.TUNRXBytes != 1200 || stats.TUNTXPackets != 4 || stats.TUNTXBytes != 400 {
		t.Fatalf("unexpected tun stats: %+v", stats)
	}
}

func TestEnsureProbeVirtualRouterPlatformInterfaceIPLinuxCreatesDefaultDeviceWhenUnset(t *testing.T) {
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	oldNewRunner := probeVirtualRouterLinuxNewTUNDataPlaneRunner
	resetProbeVirtualRouterLinuxRouteStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(func() {
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
		probeVirtualRouterLinuxNewTUNDataPlaneRunner = oldNewRunner
		resetProbeVirtualRouterLinuxRouteStateForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
		_ = stopProbeVirtualRouterTUNDataPlane()
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
	probeVirtualRouterLinuxNewTUNDataPlaneRunner = func(dev string) (probeVirtualRouterTUNDataPlane, error) {
		return &fakeProbeVirtualRouterLinuxTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true}}, nil
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
	probeLocalTUNRouteFeatureEnabled = func() bool { return true }
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	oldNewRunner := probeVirtualRouterLinuxNewTUNDataPlaneRunner
	resetProbeVirtualRouterLinuxRouteStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(func() {
		probeLocalTUNRouteFeatureEnabled = func() bool { return false }
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
		probeVirtualRouterLinuxNewTUNDataPlaneRunner = oldNewRunner
		resetProbeVirtualRouterLinuxRouteStateForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
		_ = stopProbeVirtualRouterTUNDataPlane()
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = probeVirtualRouterConfig{}
		probeVirtualRouterState.localNodeID = ""
		probeVirtualRouterState.mu.Unlock()
	})

	probeLocalLinuxStat = func(name string) (os.FileInfo, error) { return fakeProbeLocalLinuxFileInfo{}, nil }
	probeLocalLinuxLookPath = func(file string) (string, error) { return "/sbin/ip", nil }
	var mu sync.Mutex
	calls := make([]string, 0, 8)
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	starts := 0
	probeVirtualRouterLinuxNewTUNDataPlaneRunner = func(dev string) (probeVirtualRouterTUNDataPlane, error) {
		mu.Lock()
		defer mu.Unlock()
		starts++
		return &fakeProbeVirtualRouterLinuxTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true}}, nil
	}

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "19", IP: "198.18.0.21"},
		},
	}, "19")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		started := starts
		mu.Unlock()
		if started == 1 && probeVirtualRouterTUNDataPlaneRunning() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 {
		t.Fatalf("linux tun data plane starts=%d want 1", starts)
	}
	if !probeVirtualRouterTUNDataPlaneRunning() {
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

func resetProbeVirtualRouterLinuxRouteStateForTest() {
	probeVirtualRouterLinuxRouteState.mu.Lock()
	probeVirtualRouterLinuxRouteState.fakeRouteDef = probeVirtualRouterLinuxRouteDef{}
	probeVirtualRouterLinuxRouteState.fakeApplied = false
	probeVirtualRouterLinuxRouteState.takeoverRouteDefs = nil
	probeVirtualRouterLinuxRouteState.mu.Unlock()
}
