//go:build linux

package main

import (
	"fmt"
	"os"
	"strings"
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
	dnsReadyCalls := 0
	probeVirtualRouterDNSAfterTUNReady = func() { dnsReadyCalls++ }
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
	if dnsReadyCalls != 1 {
		t.Fatalf("dns ready calls=%d want 1", dnsReadyCalls)
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

func TestProbeVirtualRouterLinuxTUNDataPlaneAggregatesAbnormalSlowWrites(t *testing.T) {
	var logs []string
	runner := &probeVirtualRouterLinuxTUNDataPlaneRunner{
		dev:        "cloudhelper0",
		outboundCh: make(chan []byte, probeLocalLinuxTUNOutboundQueueFrames),
		logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	}
	runner.recordSlowWriteSummary(1375, 179, 69*time.Millisecond)
	runner.recordSlowWriteSummary(1464, 3075, 125*time.Millisecond)
	runner.flushSlowWriteSummary()
	if len(logs) != 1 {
		t.Fatalf("logs=%v, want one aggregate log", logs)
	}
	for _, want := range []string{
		"outbound stall detected",
		"dev=cloudhelper0",
		"packets=2",
		"write_max_ms=125",
		"bytes_max=1464",
		"queue_max=3075/4096",
	} {
		if !strings.Contains(logs[0], want) {
			t.Fatalf("log=%q, want %q", logs[0], want)
		}
	}
}

func TestProbeVirtualRouterLinuxTUNDataPlaneSuppressesSchedulingJitter(t *testing.T) {
	var logs []string
	runner := &probeVirtualRouterLinuxTUNDataPlaneRunner{
		outboundCh: make(chan []byte, probeLocalLinuxTUNOutboundQueueFrames),
		logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	}
	runner.recordSlowWriteSummary(1375, 179, 69*time.Millisecond)
	runner.flushSlowWriteSummary()
	if len(logs) != 0 {
		t.Fatalf("normal scheduling jitter should stay silent: %v", logs)
	}
}

func TestProbeVirtualRouterLinuxTUNDataPlaneLogsStallOncePerEpisode(t *testing.T) {
	var logs []string
	runner := &probeVirtualRouterLinuxTUNDataPlaneRunner{
		outboundCh: make(chan []byte, probeLocalLinuxTUNOutboundQueueFrames),
		logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	}
	recordStall := func() {
		runner.recordSlowWriteSummary(1464, 32, 150*time.Millisecond)
		runner.flushSlowWriteSummary()
	}
	recordStall()
	recordStall()
	if len(logs) != 1 {
		t.Fatalf("persistent stall logs=%v, want one per episode", logs)
	}
	runner.flushSlowWriteSummary()
	recordStall()
	if len(logs) != 2 {
		t.Fatalf("stall after quiet interval logs=%v, want a new episode", logs)
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
