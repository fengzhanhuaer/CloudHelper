package main

import (
	"io"
	"net"
	"runtime"
	"testing"
	"time"
)

func TestProbeExitTCPRelayReturnsAfterPeerReset(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	upstream, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	origin, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer origin.Close()
	inbound, client := net.Pipe()
	defer inbound.Close()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		relayProbeVirtualRouterExitTCPConns(inbound, upstream)
		close(done)
	}()
	if err := origin.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := origin.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	var payload [1]byte
	if _, err := io.ReadFull(client, payload[:]); err != nil || payload[0] != 1 {
		t.Fatalf("relay read before reset: payload=%v err=%v", payload, err)
	}
	if err := origin.SetLinger(0); err != nil {
		t.Fatal(err)
	}
	if err := origin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		client.Close()
		<-done
		t.Fatal("relay stayed blocked after the upstream TCP reset")
	}
}

func TestProbeVirtualRouterCarrierQueuesSharePayloadBudget(t *testing.T) {
	const packetSize = 1500
	const payloadBudget = 16 << 20
	links := []*probeVirtualRouterFrameLink{
		newProbeVirtualRouterFrameLink("memory-test-a", nil, nil, nil),
		newProbeVirtualRouterFrameLink("memory-test-b", nil, nil, nil),
		newProbeVirtualRouterFrameLink("memory-test-c", nil, nil, nil),
	}
	for _, link := range links {
		defer stopProbeVirtualRouterFrameLink(link)
	}
	runtime.GC()
	var before, loaded runtime.MemStats
	runtime.ReadMemStats(&before)
	queued := 0
	for i := 0; i < 4000; i++ {
		for _, link := range links {
			frame := probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypeIP, Data: make([]byte, packetSize)}
			if link.rx.TryPush(frame) {
				queued++
			}
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&loaded)
	t.Logf("backlogged payload=%d bytes, retained Go heap before=%d loaded=%d", queued*packetSize, before.HeapAlloc, loaded.HeapAlloc)
	if queued*packetSize > payloadBudget {
		t.Errorf("combined frame queues retained %d payload bytes, limit=%d", queued*packetSize, payloadBudget)
	}
	if queued*packetSize < payloadBudget-(512<<10) {
		t.Errorf("combined data queues only used %d bytes of the 16 MiB budget", queued*packetSize)
	}
	if _, ok := links[0].rx.TryPop(); !ok {
		t.Fatal("expected a queued frame to drain")
	}
	if !links[1].rx.TryPush(probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypeIP, Data: make([]byte, packetSize)}) {
		t.Fatal("draining one link should free capacity for another link")
	}
	for _, link := range links {
		stopProbeVirtualRouterFrameLink(link)
	}
	runtime.GC()
	var drained runtime.MemStats
	runtime.ReadMemStats(&drained)
	t.Logf("retained Go heap after draining=%d bytes", drained.HeapAlloc)
}

func BenchmarkProbeVirtualRouterQueueIPPacket(b *testing.B) {
	link := newProbeVirtualRouterFrameLink("benchmark-ip-queue", nil, nil, nil)
	defer stopProbeVirtualRouterFrameLink(link)
	packet := make([]byte, 1500)
	path := []string{"27", "26", "16"}
	b.ReportAllocs()
	b.SetBytes(int64(len(packet)))
	for b.Loop() {
		if err := writeProbeVirtualRouterIPFrame(link, packet, path, nil); err != nil {
			b.Fatal(err)
		}
		link.tx.TryPop()
	}
}

func TestProbeAdaptiveQueueCarrierBudgetReleasesWaitersAcrossQueues(t *testing.T) {
	budget := &probeQueueByteBudget{limit: 1024, controlReserve: 64}
	accounting := probeAdaptiveQueueByteAccounting[int]{
		budget: budget,
		measure: func(value int) (int, bool) {
			if value < 0 {
				return -value, true
			}
			return value, false
		},
	}
	options := probeAdaptiveQueueOptions{InitialCapacity: 2, MaxCapacity: 2}
	first := newProbeAdaptiveQueue(options, accounting)
	second := newProbeAdaptiveQueue(options, accounting)
	defer first.Close()
	defer second.Close()
	if !first.TryPush(960) || second.TryPush(1) || !second.TryPush(-64) {
		t.Fatal("expected business bytes to stop before the control reserve")
	}
	deadline := time.Now().Add(time.Second)
	pushed := make(chan error, 1)
	go func() { pushed <- second.PushUntil(896, deadline, nil) }()
	for second.adaptiveQueueSnapshot(time.Now()).FullEvents < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if second.adaptiveQueueSnapshot(time.Now()).FullEvents < 2 {
		t.Fatal("producer did not reach the budget wait")
	}
	first.Close()
	if err := <-pushed; err != nil {
		t.Fatalf("closing another queue did not release the producer: %v", err)
	}
	if got, ok := second.TryPop(); !ok || got != -64 {
		t.Fatalf("expected the reserved control item, got value=%d ok=%t", got, ok)
	}
	second.Close()
	if snapshot := budget.snapshot(); snapshot.QueuedBytes != 0 || snapshot.PeakQueuedBytes != 1024 {
		t.Fatalf("unexpected budget after close: %+v", snapshot)
	}
}

type probeVirtualRouterBinaryBatchWriter struct{ io.Writer }

func (probeVirtualRouterBinaryBatchWriter) BinaryPathSupported() bool { return true }

func BenchmarkProbeVirtualRouterWritePacketBatch(b *testing.B) {
	const packetSize = 1500
	frames := make([]probeVirtualRouterFrame, 32)
	for i := range frames {
		frame, err := buildProbeVirtualRouterIPFrame(make([]byte, packetSize), []string{"16", "26", "27"}, nil)
		if err != nil {
			b.Fatal(err)
		}
		frames[i] = frame
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(frames) * packetSize))
	writer := probeVirtualRouterBinaryBatchWriter{io.Discard}
	for b.Loop() {
		if err := writeProbeVirtualRouterWireFramesRaw(writer, frames); err != nil {
			b.Fatal(err)
		}
	}
}
