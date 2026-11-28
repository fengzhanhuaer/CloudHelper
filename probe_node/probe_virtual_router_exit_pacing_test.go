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
