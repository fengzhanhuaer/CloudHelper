//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProbeVirtualRouterTUNDataPlane struct {
	stats      probeVirtualRouterTUNDataPlaneStats
	closeErr   error
	writeErr   error
	closeCalls int
	writeCalls int
}

func (f *fakeProbeVirtualRouterTUNDataPlane) Close() error {
	f.closeCalls++
	return f.closeErr
}

func (f *fakeProbeVirtualRouterTUNDataPlane) Stats() probeVirtualRouterTUNDataPlaneStats {
	return f.stats
}

func (f *fakeProbeVirtualRouterTUNDataPlane) WritePacket(_ []byte) error {
	f.writeCalls++
	return f.writeErr
}

func stubProbeVirtualRouterTUNDataPlaneRouteTarget(t *testing.T, luid uint64, ifIndex int) {
	t.Helper()
	if luid == 0 {
		luid = 101
	}
	if ifIndex <= 0 {
		ifIndex = 9
	}
	probeLocalGetWintunAdapterLUIDFromHandle = func(_ string, _ uintptr) (uint64, error) {
		return luid, nil
	}
	probeLocalEnsureWindowsInterfaceIPv4ByLUID = func(gotLUID uint64, ip string, prefix int) error {
		if gotLUID != luid {
			t.Fatalf("route target luid=%d want %d", gotLUID, luid)
		}
		if strings.TrimSpace(ip) != probeLocalTUNInterfaceIPv4 {
			t.Fatalf("route target ip=%s want %s", ip, probeLocalTUNInterfaceIPv4)
		}
		if prefix != probeLocalTUNRouteIPv4PrefixLen {
			t.Fatalf("route target prefix=%d want %d", prefix, probeLocalTUNRouteIPv4PrefixLen)
		}
		return nil
	}
	probeLocalConvertInterfaceLUIDToIndex = func(gotLUID uint64) (int, error) {
		if gotLUID != luid {
			t.Fatalf("convert luid=%d want %d", gotLUID, luid)
		}
		return ifIndex, nil
	}
	probeLocalFindWindowsAdapterByLUID = func(gotLUID uint64) (windowsAdapterInfo, error) {
		if gotLUID != luid {
			t.Fatalf("find adapter luid=%d want %d", gotLUID, luid)
		}
		return windowsAdapterInfo{InterfaceIndex: ifIndex, InterfaceLUID: luid, AdapterGUID: "{test-guid}", IPv4Addrs: []string{probeLocalTUNInterfaceIPv4}, DNSServers: []string{probeLocalTUNInterfaceIPv4}}, nil
	}
	probeLocalFindWindowsAdapterByIfIndex = func(gotIfIndex int) (windowsAdapterInfo, error) {
		if gotIfIndex != ifIndex {
			t.Fatalf("find adapter ifindex=%d want %d", gotIfIndex, ifIndex)
		}
		return windowsAdapterInfo{InterfaceIndex: ifIndex, InterfaceLUID: luid, AdapterGUID: "{test-guid}", IPv4Addrs: []string{probeLocalTUNInterfaceIPv4}, DNSServers: []string{probeLocalTUNInterfaceIPv4}}, nil
	}
	probeLocalSetWindowsInterfaceDNS = func(string, []string) error { return nil }
}

func TestProbeVirtualRouterTUNDataPlaneStartStopLifecycle(t *testing.T) {
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(resetProbeVirtualRouterTUNDataPlaneHooksForTest)
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	createCalls := 0
	closeAdapterCalls := 0
	runnerCreateCalls := 0
	fake := &fakeProbeVirtualRouterTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true, RXPackets: 1, RXBytes: 10}}

	probeLocalWindowsRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		if name == "powershell" {
			joined := name + " " + strings.Join(args, " ")
			if !strings.Contains(joined, "$exclude=9") {
				t.Fatalf("prepare command did not use handle-resolved ifindex: %s", joined)
			}
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		}
		return "", nil
	}
	stubProbeVirtualRouterTUNDataPlaneRouteTarget(t, 101, 9)
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) {
		createCalls++
		return uintptr(11), nil
	}
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error {
		closeAdapterCalls++
		return nil
	}
	probeVirtualRouterNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeVirtualRouterTUNDataPlane, error) {
		runnerCreateCalls++
		return fake, nil
	}
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	if err := startProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("startProbeVirtualRouterTUNDataPlane returned error: %v", err)
	}
	if err := startProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("startProbeVirtualRouterTUNDataPlane second call returned error: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("create calls=%d, want 1", createCalls)
	}
	if runnerCreateCalls != 1 {
		t.Fatalf("runner create calls=%d, want 1", runnerCreateCalls)
	}
	stats := probeVirtualRouterTUNDataPlaneStatsSnapshot()
	if !stats.Running {
		t.Fatal("expected running stats true")
	}
	if err := stopProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("stopProbeVirtualRouterTUNDataPlane returned error: %v", err)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("close calls=%d, want 1", fake.closeCalls)
	}
	if closeAdapterCalls != 1 {
		t.Fatalf("close adapter calls=%d, want 1", closeAdapterCalls)
	}
}

func TestProbeVirtualRouterTUNDataPlaneStartPreparesDirectRouteTargetOnce(t *testing.T) {
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(resetProbeVirtualRouterTUNDataPlaneHooksForTest)
	resetProbeRouteDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeRouteDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	prepareCalls := 0
	createCalls := 0
	closeAdapterCalls := 0
	probeLocalWindowsRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch name {
		case "powershell":
			prepareCalls++
			if !strings.Contains(joined, "$exclude=9") {
				t.Fatalf("prepare command did not exclude tun ifindex: %s", joined)
			}
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		default:
			return "", nil
		}
	}
	stubProbeVirtualRouterTUNDataPlaneRouteTarget(t, 101, 9)
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) {
		createCalls++
		return uintptr(11), nil
	}
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error {
		closeAdapterCalls++
		return nil
	}
	probeVirtualRouterNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeVirtualRouterTUNDataPlane, error) {
		return &fakeProbeVirtualRouterTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true}}, nil
	}
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	if err := startProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("startProbeVirtualRouterTUNDataPlane returned error: %v", err)
	}
	if prepareCalls != 1 {
		t.Fatalf("prepareCalls=%d want 1", prepareCalls)
	}
	if createCalls != 1 {
		t.Fatalf("createCalls=%d want 1", createCalls)
	}
	if err := stopProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("stopProbeVirtualRouterTUNDataPlane returned error: %v", err)
	}
	if closeAdapterCalls != 1 {
		t.Fatalf("closeAdapterCalls=%d want 1", closeAdapterCalls)
	}
}

func TestProbeVirtualRouterTUNDataPlaneStartRestartsStaleRunner(t *testing.T) {
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(resetProbeVirtualRouterTUNDataPlaneHooksForTest)
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	stale := &fakeProbeVirtualRouterTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: false, RXPackets: 2, RXBytes: 20}}
	fresh := &fakeProbeVirtualRouterTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true, RXPackets: 3, RXBytes: 30}}
	createCalls := 0
	closeAdapterCalls := 0
	runners := []*fakeProbeVirtualRouterTUNDataPlane{stale, fresh}
	probeLocalWindowsRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		if name == "powershell" {
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		}
		return "", nil
	}
	stubProbeVirtualRouterTUNDataPlaneRouteTarget(t, 101, 9)
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) {
		createCalls++
		return uintptr(10 + createCalls), nil
	}
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error {
		closeAdapterCalls++
		return nil
	}
	probeVirtualRouterNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeVirtualRouterTUNDataPlane, error) {
		if len(runners) == 0 {
			t.Fatal("unexpected extra runner creation")
		}
		runner := runners[0]
		runners = runners[1:]
		return runner, nil
	}
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	if err := startProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("first startProbeVirtualRouterTUNDataPlane returned error: %v", err)
	}
	if err := startProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("second startProbeVirtualRouterTUNDataPlane returned error: %v", err)
	}
	if createCalls != 2 {
		t.Fatalf("create calls=%d, want 2", createCalls)
	}
	if stale.closeCalls != 1 {
		t.Fatalf("stale close calls=%d, want 1", stale.closeCalls)
	}
	stats := probeVirtualRouterTUNDataPlaneStatsSnapshot()
	if !stats.Running || stats.RXPackets != 3 {
		t.Fatalf("stats=%+v, want fresh running runner", stats)
	}
	if err := stopProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("stopProbeVirtualRouterTUNDataPlane returned error: %v", err)
	}
	if fresh.closeCalls != 1 {
		t.Fatalf("fresh close calls=%d, want 1", fresh.closeCalls)
	}
	if closeAdapterCalls != 2 {
		t.Fatalf("close adapter calls=%d, want 2", closeAdapterCalls)
	}
}

func TestProbeVirtualRouterTUNDataPlaneStartRestartsWhenRouteTargetUnhealthy(t *testing.T) {
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(resetProbeVirtualRouterTUNDataPlaneHooksForTest)
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	first := &fakeProbeVirtualRouterTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true, RXPackets: 1}}
	second := &fakeProbeVirtualRouterTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true, RXPackets: 2}}
	runners := []*fakeProbeVirtualRouterTUNDataPlane{first, second}
	createCalls := 0
	closeAdapterCalls := 0
	healthChecks := 0
	probeLocalWindowsRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		if name == "powershell" {
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		}
		return "", nil
	}
	probeLocalGetWintunAdapterLUIDFromHandle = func(_ string, _ uintptr) (uint64, error) { return 101, nil }
	probeLocalEnsureWindowsInterfaceIPv4ByLUID = func(uint64, string, int) error {
		healthChecks++
		if healthChecks == 2 {
			return errors.New("adapter disappeared")
		}
		return nil
	}
	probeLocalConvertInterfaceLUIDToIndex = func(uint64) (int, error) { return 9, nil }
	probeLocalFindWindowsAdapterByLUID = func(uint64) (windowsAdapterInfo, error) {
		return windowsAdapterInfo{InterfaceIndex: 9, InterfaceLUID: 101, AdapterGUID: "{test-guid}", IPv4Addrs: []string{probeLocalTUNInterfaceIPv4}}, nil
	}
	probeLocalFindWindowsAdapterByIfIndex = func(int) (windowsAdapterInfo, error) {
		if healthChecks == 2 {
			return windowsAdapterInfo{}, errors.New("adapter disappeared")
		}
		return windowsAdapterInfo{InterfaceIndex: 9, InterfaceLUID: 101, AdapterGUID: "{test-guid}", IPv4Addrs: []string{probeLocalTUNInterfaceIPv4}}, nil
	}
	probeLocalSetWindowsInterfaceDNS = func(string, []string) error { return nil }
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) {
		createCalls++
		return uintptr(20 + createCalls), nil
	}
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error {
		closeAdapterCalls++
		return nil
	}
	probeVirtualRouterNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeVirtualRouterTUNDataPlane, error) {
		if len(runners) == 0 {
			t.Fatal("unexpected extra runner creation")
		}
		runner := runners[0]
		runners = runners[1:]
		return runner, nil
	}
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	if err := startProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("first startProbeVirtualRouterTUNDataPlane returned error: %v", err)
	}
	if err := startProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("second startProbeVirtualRouterTUNDataPlane returned error: %v", err)
	}
	if createCalls != 2 {
		t.Fatalf("create calls=%d, want 2", createCalls)
	}
	if first.closeCalls != 1 {
		t.Fatalf("first close calls=%d, want 1", first.closeCalls)
	}
	stats := probeVirtualRouterTUNDataPlaneStatsSnapshot()
	if !stats.Running || stats.RXPackets != 2 {
		t.Fatalf("stats=%+v, want second runner", stats)
	}
	if err := stopProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("stopProbeVirtualRouterTUNDataPlane returned error: %v", err)
	}
	if closeAdapterCalls != 2 {
		t.Fatalf("close adapter calls=%d, want 2", closeAdapterCalls)
	}
}

func TestProbeVirtualRouterTUNDataPlaneStartRunnerFailureClosesAdapter(t *testing.T) {
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(resetProbeVirtualRouterTUNDataPlaneHooksForTest)
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	closeAdapterCalls := 0
	probeLocalWindowsRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		if name == "powershell" {
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		}
		return "", nil
	}
	stubProbeVirtualRouterTUNDataPlaneRouteTarget(t, 101, 9)
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return uintptr(22), nil }
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error {
		closeAdapterCalls++
		return nil
	}
	probeVirtualRouterNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeVirtualRouterTUNDataPlane, error) {
		return nil, errors.New("runner failed")
	}
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	err := startProbeVirtualRouterTUNDataPlane()
	if err == nil {
		t.Fatal("expected startProbeVirtualRouterTUNDataPlane error")
	}
	if closeAdapterCalls != 1 {
		t.Fatalf("close adapter calls=%d, want 1", closeAdapterCalls)
	}
}

func TestProbeVirtualRouterTUNDataPlaneWriteWhenStopped(t *testing.T) {
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	t.Cleanup(resetProbeVirtualRouterTUNDataPlaneHooksForTest)

	if err := writeProbeVirtualRouterTUNPacket([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected writeProbeVirtualRouterTUNPacket error when stopped")
	}
}

func TestProbeVirtualRouterTUNDataPlaneRunnerHandleInboundPayloadDoesNotBlock(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	var startedOnce sync.Once

	runner := &probeVirtualRouterTUNDataPlaneRunner{
		inboundCh: make(chan []byte, 1),
		stopCh:    make(chan struct{}),
		onPacket: func([]byte) {
			startedOnce.Do(func() { close(started) })
			<-release
		},
	}
	go runner.inboundWorker()
	defer close(runner.stopCh)

	go func() {
		runner.handleInboundPayload([]byte{0x45, 0x00})
		close(returned)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("packet handler did not start")
	}

	select {
	case <-returned:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("handleInboundPayload should return before packet handler completes")
	}

	close(release)

	select {
	case <-started:
	default:
		t.Fatal("packet handler should have started")
	}
}

func TestProbeVirtualRouterTUNDataPlaneRunnerDropsInboundWhenQueueFull(t *testing.T) {
	calls := 0
	var logs []string
	runner := &probeVirtualRouterTUNDataPlaneRunner{
		inboundCh: make(chan []byte, 1),
		stopCh:    make(chan struct{}),
		onPacket: func([]byte) {
			calls++
		},
		logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	}
	defer close(runner.stopCh)
	runner.inboundCh <- []byte{0x45}

	runner.handleInboundPayload([]byte{0x45, 0x00})

	if calls != 0 {
		t.Fatalf("handler calls=%d, want 0 before worker consumes queue", calls)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "handler_queue_full") {
		t.Fatalf("logs=%v, want handler_queue_full drop", logs)
	}
}
