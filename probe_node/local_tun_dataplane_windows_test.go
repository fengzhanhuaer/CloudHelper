//go:build windows

package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeProbeLocalTUNDataPlane struct {
	stats      probeLocalTUNDataPlaneStats
	closeErr   error
	writeErr   error
	closeCalls int
	writeCalls int
}

func (f *fakeProbeLocalTUNDataPlane) Close() error {
	f.closeCalls++
	return f.closeErr
}

func (f *fakeProbeLocalTUNDataPlane) Stats() probeLocalTUNDataPlaneStats {
	return f.stats
}

func (f *fakeProbeLocalTUNDataPlane) WritePacket(_ []byte) error {
	f.writeCalls++
	return f.writeErr
}

func TestProbeLocalTUNDataPlaneStartStopLifecycle(t *testing.T) {
	resetProbeLocalTUNDataPlaneHooksForTest()
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(resetProbeLocalTUNDataPlaneHooksForTest)
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	createCalls := 0
	closeAdapterCalls := 0
	runnerCreateCalls := 0
	fake := &fakeProbeLocalTUNDataPlane{stats: probeLocalTUNDataPlaneStats{Running: true, RXPackets: 1, RXBytes: 10}}

	probeLocalWindowsRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		if name == "powershell" {
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		}
		return "", nil
	}
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
	probeLocalNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeLocalTUNDataPlane, error) {
		runnerCreateCalls++
		return fake, nil
	}
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	if err := startProbeLocalTUNDataPlane(); err != nil {
		t.Fatalf("startProbeLocalTUNDataPlane returned error: %v", err)
	}
	if err := startProbeLocalTUNDataPlane(); err != nil {
		t.Fatalf("startProbeLocalTUNDataPlane second call returned error: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("create calls=%d, want 1", createCalls)
	}
	if runnerCreateCalls != 1 {
		t.Fatalf("runner create calls=%d, want 1", runnerCreateCalls)
	}
	stats := probeLocalTUNDataPlaneStatsSnapshot()
	if !stats.Running {
		t.Fatal("expected running stats true")
	}
	if err := stopProbeLocalTUNDataPlane(); err != nil {
		t.Fatalf("stopProbeLocalTUNDataPlane returned error: %v", err)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("close calls=%d, want 1", fake.closeCalls)
	}
	if closeAdapterCalls != 1 {
		t.Fatalf("close adapter calls=%d, want 1", closeAdapterCalls)
	}
}

func TestProbeLocalTUNDataPlaneStartPreparesDirectBypassRouteTargetOnce(t *testing.T) {
	resetProbeLocalTUNDataPlaneHooksForTest()
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(resetProbeLocalTUNDataPlaneHooksForTest)
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(func() {
		resetProbeLocalDirectBypassStateForTest()
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})

	prepareCalls := 0
	routeCalls := 0
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
		case "route":
			routeCalls++
			if !strings.Contains(joined, "192.168.1.1") || !strings.Contains(joined, " IF 12") {
				t.Fatalf("route command used unexpected target: %s", joined)
			}
			return "", nil
		default:
			return "", nil
		}
	}
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
	probeLocalNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeLocalTUNDataPlane, error) {
		return &fakeProbeLocalTUNDataPlane{stats: probeLocalTUNDataPlaneStats{Running: true}}, nil
	}
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	if err := startProbeLocalTUNDataPlane(); err != nil {
		t.Fatalf("startProbeLocalTUNDataPlane returned error: %v", err)
	}
	if prepareCalls != 1 {
		t.Fatalf("prepareCalls=%d want 1", prepareCalls)
	}
	if createCalls != 1 {
		t.Fatalf("createCalls=%d want 1", createCalls)
	}
	if err := ensureProbeLocalDirectBypassForTarget("1.2.3.4:443"); err != nil {
		t.Fatalf("ensure bypass failed: %v", err)
	}
	if routeCalls != 1 {
		t.Fatalf("routeCalls=%d want 1", routeCalls)
	}
	if err := stopProbeLocalTUNDataPlane(); err != nil {
		t.Fatalf("stopProbeLocalTUNDataPlane returned error: %v", err)
	}
	if closeAdapterCalls != 1 {
		t.Fatalf("closeAdapterCalls=%d want 1", closeAdapterCalls)
	}
}

func TestProbeLocalTUNDataPlaneStartRunnerFailureClosesAdapter(t *testing.T) {
	resetProbeLocalTUNDataPlaneHooksForTest()
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(resetProbeLocalTUNDataPlaneHooksForTest)
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
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return uintptr(22), nil }
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error {
		closeAdapterCalls++
		return nil
	}
	probeLocalNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeLocalTUNDataPlane, error) {
		return nil, errors.New("runner failed")
	}
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	err := startProbeLocalTUNDataPlane()
	if err == nil {
		t.Fatal("expected startProbeLocalTUNDataPlane error")
	}
	if closeAdapterCalls != 1 {
		t.Fatalf("close adapter calls=%d, want 1", closeAdapterCalls)
	}
}

func TestProbeLocalTUNDataPlaneWriteWhenStopped(t *testing.T) {
	resetProbeLocalTUNDataPlaneHooksForTest()
	t.Cleanup(resetProbeLocalTUNDataPlaneHooksForTest)

	if err := writeProbeLocalTUNPacket([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected writeProbeLocalTUNPacket error when stopped")
	}
}

func TestProbeLocalTUNDataPlaneRunnerHandleInboundPayloadIsSynchronous(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})

	runner := &probeLocalTUNDataPlaneRunner{
		onPacket: func([]byte) {
			close(started)
			<-release
		},
	}

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
		t.Fatal("handleInboundPayload returned before packet handler completed")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("handleInboundPayload did not return after packet handler completed")
	}
}
