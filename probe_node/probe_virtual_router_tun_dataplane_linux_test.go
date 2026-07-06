//go:build linux

package main

import (
	"os"
	"testing"
	"time"
)

type fakeProbeVirtualRouterLinuxTUNDataPlane struct {
	stats      probeVirtualRouterTUNDataPlaneStats
	closeCalls int
	writeCalls int
	writeErr   error
}

func (f *fakeProbeVirtualRouterLinuxTUNDataPlane) Close() error {
	f.closeCalls++
	f.stats.Running = false
	return nil
}

func (f *fakeProbeVirtualRouterLinuxTUNDataPlane) Stats() probeVirtualRouterTUNDataPlaneStats {
	return f.stats
}

func (f *fakeProbeVirtualRouterLinuxTUNDataPlane) WritePacket(_ []byte) error {
	f.writeCalls++
	return f.writeErr
}

func TestStartProbeVirtualRouterTUNDataPlaneLinuxStartsRunner(t *testing.T) {
	stubProbeLocalLinuxTUNDeviceReadyForDataPlaneTest(t)
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	t.Cleanup(resetProbeVirtualRouterTUNDataPlaneHooksForTest)

	fake := &fakeProbeVirtualRouterLinuxTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true, RXPackets: 7, RXBytes: 99, TXPackets: 3, TXBytes: 33}}
	starts := 0
	probeVirtualRouterLinuxNewTUNDataPlaneRunner = func(dev string) (probeVirtualRouterTUNDataPlane, error) {
		starts++
		if dev != probeLocalLinuxDefaultTUNDeviceName {
			t.Fatalf("dev=%q want %q", dev, probeLocalLinuxDefaultTUNDeviceName)
		}
		return fake, nil
	}

	if err := startProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if err := startProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("second start failed: %v", err)
	}
	if starts != 1 {
		t.Fatalf("runner starts=%d want 1", starts)
	}
	stats := probeVirtualRouterTUNDataPlaneStatsSnapshot()
	if !stats.Running || stats.RXPackets != 7 || stats.RXBytes != 99 || stats.TXPackets != 3 || stats.TXBytes != 33 {
		t.Fatalf("stats=%+v", stats)
	}
	if err := writeProbeVirtualRouterTUNPacket([]byte{1, 2, 3}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if fake.writeCalls != 1 {
		t.Fatalf("writeCalls=%d want 1", fake.writeCalls)
	}
	if err := stopProbeVirtualRouterTUNDataPlane(); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("closeCalls=%d want 1", fake.closeCalls)
	}
	if err := writeProbeVirtualRouterTUNPacket([]byte{1}); err == nil {
		t.Fatalf("expected write failure after stop, got %v", err)
	}
}

func stubProbeLocalLinuxTUNDeviceReadyForDataPlaneTest(t *testing.T) {
	t.Helper()
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	t.Cleanup(func() {
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
	})
	probeLocalLinuxStat = func(name string) (os.FileInfo, error) { return fakeProbeLocalLinuxFileInfo{}, nil }
	probeLocalLinuxLookPath = func(file string) (string, error) { return "/sbin/ip", nil }
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		return "", nil
	}
}
