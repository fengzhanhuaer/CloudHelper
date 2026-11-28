package main

import (
	"io"
	"net"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
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

func TestProbeRelayReportsDownstreamQueuedBytesToUpstream(t *testing.T) {
	upRuntime := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{localNodeID: "26", peerNodeID: "27"}}
	downRuntime := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{localNodeID: "26", peerNodeID: "16"}}
	up := newProbeVirtualRouterFrameLink("relay-26-up", upRuntime, nil, nil)
	a, b := net.Pipe()
	defer b.Close()
	down := newProbeVirtualRouterFrameLink("relay-26-down", downRuntime, a, nil)
	defer stopProbeVirtualRouterFrameLink(up)
	defer stopProbeVirtualRouterFrameLink(down)
	packet := make([]byte, 1500)
	packet[0], packet[9] = 0x45, 6
	now := time.Unix(100, 0)
	reportProbeVirtualRouterRelayPressure(up, down, packet, now)
	if up.txControl.Len() != 0 {
		t.Fatal("empty downstream sent a pressure report")
	}
	for range probeVirtualRouterCarrierTXBufferFrames {
		if !down.carrier.tx.TryPush(probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypeIP, Data: make([]byte, 40)}) {
			t.Fatal("could not populate downstream queue with small packets")
		}
	}
	now = now.Add(probeRelayPressureSampleGap)
	reportProbeVirtualRouterRelayPressure(up, down, packet, now)
	if up.txControl.Len() != 0 {
		t.Fatal("small packets exceeded the pressure threshold by count only")
	}
	down.carrier.tx.Drain(nil)
	for range probeVirtualRouterCarrierTXBufferFrames * 3 / 4 {
		if !down.carrier.tx.TryPush(probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypeIP, Data: packet}) {
			t.Fatal("could not fill the downstream carrier")
		}
	}
	now = now.Add(probeRelayPressureSampleGap)
	reportProbeVirtualRouterRelayPressure(up, down, packet, now)
	frame, ok := up.txControl.TryPop()
	if !ok {
		t.Fatal("congested downstream did not send a report")
	}
	control, err := probeVirtualRouterFrameControl(frame, nil)
	if err != nil || len(control.Path) != 2 || control.Path[0] != "26" || control.Path[1] != "27" {
		t.Fatalf("expected authenticated adjacent path: %+v err=%v", control, err)
	}
	exit := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{localNodeID: "27", peerNodeID: "26"}}
	if err := handleProbeVirtualRouterBusinessFrame(exit, nil, frame.MainType, frame.SubType, frame.Data, control.Path); err != nil || exit.peerTUNPressure.Load() == 0 {
		t.Fatalf("exit did not record pressure from node 26: %v", err)
	}
	reportProbeVirtualRouterRelayPressure(up, down, packet, now.Add(time.Millisecond))
	if up.txControl.Len() != 0 {
		t.Fatal("duplicate pressure reports were not rate-limited")
	}
	down.carrier.tx.Drain(nil)
	now = now.Add(probeRelayPressureSampleGap)
	reportProbeVirtualRouterRelayPressure(up, down, packet, now)
	if up.txControl.Len() != 0 {
		t.Fatal("the relay kept reporting after the downstream queue drained")
	}
	downRuntime.recordPeerTUNPressure(now, []string{"16", "26"})
	reportProbeVirtualRouterRelayPressure(up, down, packet, now.Add(probeRelayPressureSampleGap))
	if up.txControl.Len() != 1 {
		t.Fatal("the relay did not propagate a fresh report from the next hop")
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
	if !p.applyPeerPressure(now, now.UnixNano()) || p.bytesPerSecond != 18_750_000 {
		t.Fatal("fresh peer report must make a modest 6.25% rate reduction")
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
	if p.bytesPerSecond > reduced*9/8 {
		t.Fatal("the first recovery step was large enough to recreate a burst")
	}
}

func TestProbeExitPacerSustainedPressureUsesTenKilobitFloor(t *testing.T) {
	const floor = 1250 // 10 kbps expressed as payload bytes per second.
	now := time.Unix(100, 0)
	p := probeExitTCPPacer{bytesPerSecond: probeExitPacingInitial}
	p.applyPeerPressure(now, now.UnixNano())
	if p.bytesPerSecond != probeExitPacingInitial {
		t.Fatal("a single report reduced the rate below its starting value")
	}
	for range 160 {
		now = now.Add(250 * time.Millisecond)
		p.applyPeerPressure(now, now.UnixNano())
	}
	if p.bytesPerSecond != floor {
		t.Fatalf("sustained remote pressure stopped at %d B/s, want %d B/s", p.bytesPerSecond, floor)
	}
	p.update(now, true, true)
	if p.bytesPerSecond != floor {
		t.Fatalf("local pressure changed the floor to %d B/s", p.bytesPerSecond)
	}
	recovery := now.Add(probeExitPeerReportTTL + time.Millisecond)
	if p.applyPeerPressure(recovery, now.UnixNano()) {
		t.Fatal("expired pressure still blocks rate recovery")
	}
	p.update(recovery, false, true)
	if p.bytesPerSecond <= floor {
		t.Fatal("rate did not recover after the pressure stopped")
	}
}

func TestProbeExitPacedConnKeepsWritesSmallAtTenKilobits(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	link := channel.New(1, 1500, "")
	defer link.Close()
	c := &probeExitPacedConn{
		Conn:     a,
		netstack: &probeVirtualRouterExitNetstack{linkEP: link},
		closed:   make(chan struct{}),
		pacer:    probeExitTCPPacer{bytesPerSecond: 1250},
	}
	defer c.Close()
	if err := b.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var payload [250]byte
	done := make(chan error, 1)
	go func() {
		_, err := c.Write(payload[:])
		done <- err
	}()
	var received [250]byte
	n, err := b.Read(received[:])
	if err != nil || n != 125 {
		t.Fatalf("first write delivered %d bytes, want a 100 ms block of 125 bytes: %v", n, err)
	}
	if _, err := io.ReadFull(b, received[n:]); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
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
