package main

import (
	"testing"
	"time"
)

func TestProbeExitPacerRespondsToDownstream(t *testing.T) {
	now := time.Unix(100, 0)
	p := probeExitTCPPacer{}
	p.update(now, false, false)
	start := p.bytesPerSecond
	// A small response and an idle flow should not build up unused sending credit.
	p.update(now.Add(time.Second), false, false)
	if p.bytesPerSecond != start {
		t.Fatalf("idle rate=%d, want %d", p.bytesPerSecond, start)
	}
	p.update(now.Add(2*time.Second), false, true)
	fast := p.bytesPerSecond
	if fast <= start {
		t.Fatalf("rate failed to grow with demand: %d (started at %d)", fast, start)
	}
	p.update(now.Add(2*time.Second+time.Millisecond), true, true)
	if p.bytesPerSecond >= fast {
		t.Fatalf("rate failed to slow when downstream backed up: %d (was %d)", p.bytesPerSecond, fast)
	}
	queued := 64 << 10
	p.recordSend(now, queued)
	if wait := p.wait(now.Add(10 * time.Second)); wait != 0 {
		t.Fatalf("an idle flow is still delayed: %s", wait)
	}
	p.recordSend(now.Add(10*time.Second), queued)
	if wait := p.wait(now.Add(10 * time.Second)); wait <= 0 || wait > time.Second {
		t.Fatalf("first write after idle did not limit the next burst: %s", wait)
	}
}

func TestProbeExitFlowsShareOneRateLimit(t *testing.T) {
	pacer := newProbeExitNodeLimiter()
	now := time.Unix(100, 0)
	if !pacer.AllowN(now, probeExitPacingBatch) || pacer.AllowN(now, probeExitPacingBatch) {
		t.Fatal("concurrent flows had independent burst allowances")
	}
	if pacer.AllowN(now.Add(time.Millisecond/2), probeExitPacingBatch) || !pacer.AllowN(now.Add(2*time.Millisecond), probeExitPacingBatch) {
		t.Fatal("shared budget did not refill at 500 Mbps")
	}
}

func TestProbeExitPacerAdaptsToFreshPeerReports(t *testing.T) {
	now := time.Unix(100, 0)
	p := probeExitTCPPacer{bytesPerSecond: 20_000_000}
	if !p.applyPeerPressure(now, now.UnixNano()) || p.bytesPerSecond >= 20_000_000 {
		t.Fatal("fresh peer report failed to slow the flow")
	}
	reduced := p.bytesPerSecond
	if !p.applyPeerPressure(now.Add(50*time.Millisecond), now.UnixNano()) || p.bytesPerSecond != reduced {
		t.Fatal("one peer report cut the send rate more than once")
	}
	if p.applyPeerPressure(now.Add(time.Second), now.UnixNano()) || p.bytesPerSecond != reduced {
		t.Fatal("stale peer pressure still affects the flow")
	}
	p.update(now.Add(time.Second), false, true)
	if p.bytesPerSecond <= reduced {
		t.Fatal("send rate did not recover after remote congestion ended")
	}
}

func TestProbeTUNPressureOnlyAppliesToTheAuthenticatedNeighbor(t *testing.T) {
	now := time.Unix(100, 0)
	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{localNodeID: "27", peerNodeID: "26"}}
	record := func(path ...string) { rt.recordPeerTUNPressure(now, path) }
	record("14", "26", "27")
	record("16", "27")
	record("27", "26")
	if rt.peerTUNPressure.Load() != 0 {
		t.Fatal("a report without the direct peer path changed the pacing state")
	}
	record("26", "27")
	if rt.peerTUNPressure.Load() != now.UnixNano() {
		t.Fatal("the authenticated peer's report was lost")
	}
	rt.recordPeerTUNPressure(now.Add(time.Millisecond), []string{"26", "27"})
	if rt.peerTUNPressure.Load() != now.UnixNano() {
		t.Fatal("duplicate rapid reports from the same peer took effect")
	}
}
