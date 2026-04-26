//go:build windows

package main

import (
	"errors"
	"testing"
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
	t.Cleanup(resetProbeLocalTUNDataPlaneHooksForTest)

	createCalls := 0
	closeAdapterCalls := 0
	runnerCreateCalls := 0
	fake := &fakeProbeLocalTUNDataPlane{stats: probeLocalTUNDataPlaneStats{Running: true, RXPackets: 1, RXBytes: 10}}

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

func TestProbeLocalTUNDataPlaneStartRunnerFailureClosesAdapter(t *testing.T) {
	resetProbeLocalTUNDataPlaneHooksForTest()
	t.Cleanup(resetProbeLocalTUNDataPlaneHooksForTest)

	closeAdapterCalls := 0
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
