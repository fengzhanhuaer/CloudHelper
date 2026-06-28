package main

import (
	"bufio"
	"bytes"
	"net"
	"testing"
	"time"
)

func TestProbeChainFrameRoundTrip(t *testing.T) {
	frame := probeChainFrame{
		Kind:     probeChainFrameKindControl,
		Flags:    0x1234,
		StreamID: 0x0102030405060708,
		Seq:      0x1122334455667788,
		Control:  []byte(`{"op":"hello","node_id":"n1"}`),
		Data:     []byte("payload"),
	}

	encoded, err := encodeProbeChainFrame(frame, nil)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	decoded, err := decodeProbeChainFrame(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if decoded.Kind != frame.Kind || decoded.Flags != frame.Flags || decoded.StreamID != frame.StreamID || decoded.Seq != frame.Seq {
		t.Fatalf("decoded header mismatch: %#v", decoded)
	}
	if !bytes.Equal(decoded.Control, frame.Control) {
		t.Fatalf("control mismatch: got %q want %q", decoded.Control, frame.Control)
	}
	if !bytes.Equal(decoded.Data, frame.Data) {
		t.Fatalf("data mismatch: got %q want %q", decoded.Data, frame.Data)
	}
}

func TestProbeChainFramedPacketRoundTrip(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	want := []byte("hello frame")
	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		errCh <- writeProbeChainFramedPacket(right, want)
	}()

	got, err := readProbeChainFramedPacket(bufio.NewReader(left))
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("payload mismatch: got %q want %q", got, want)
	}
}

func TestProbeChainFrameRejectsOversizeControl(t *testing.T) {
	_, err := encodeProbeChainFrame(probeChainFrame{
		Kind:    probeChainFrameKindControl,
		Control: bytes.Repeat([]byte("a"), probeChainFrameMaxControlBytes+1),
	}, nil)
	if err == nil {
		t.Fatal("expected oversize control to fail")
	}
}

func TestProbeChainFrameSessionPingPongStats(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	clientSession, err := newProbeChainFrameClient(clientConn)
	if err != nil {
		t.Fatalf("client frame session: %v", err)
	}
	defer clientSession.Close()
	serverSession, err := newProbeChainFrameServer(serverConn)
	if err != nil {
		t.Fatalf("server frame session: %v", err)
	}
	defer serverSession.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		clientStats := clientSession.PingStats()
		serverStats := serverSession.PingStats()
		if clientStats.PongsReceived > 0 && clientStats.RTT > 0 && serverStats.PongsReceived > 0 && serverStats.RTT > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ping-pong stats not updated: client=%+v server=%+v", clientSession.PingStats(), serverSession.PingStats())
}

func TestProbeChainFrameSessionRTTQueryReturnsRemoteStats(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	clientSession, err := newProbeChainFrameClient(clientConn)
	if err != nil {
		t.Fatalf("client frame session: %v", err)
	}
	defer clientSession.Close()
	serverSession, err := newProbeChainFrameServer(serverConn)
	if err != nil {
		t.Fatalf("server frame session: %v", err)
	}
	defer serverSession.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if serverSession.PingStats().PongsReceived > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	result, err := clientSession.QueryRemoteRTT(time.Second)
	if err != nil {
		t.Fatalf("query remote rtt: %v", err)
	}
	if result.PingsSent == 0 {
		t.Fatalf("remote rtt result missing ping stats: %+v", result)
	}
	if result.Responder == "" || result.ResponderRemote == "" {
		t.Fatalf("remote rtt result missing responder addrs: %+v", result)
	}
	if result.CollectedAtUnixNano == 0 {
		t.Fatalf("remote rtt result missing collection time: %+v", result)
	}
}

func TestProbeChainFrameSessionIOStatsAreDirectional(t *testing.T) {
	clientSession, serverSession := newProbeChainFrameSessionPairForTest(t)
	defer clientSession.Close()
	defer serverSession.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		stream, err := serverSession.Accept()
		if err != nil {
			return
		}
		if frameStream, ok := stream.(*probeChainFrameStream); ok {
			_ = frameStream.RespondOpen(probeChainTunnelOpenResponse{OK: true})
		}
		accepted <- stream
	}()

	client, err := clientSession.OpenWithRequest(probeChainTunnelOpenRequest{Type: "test", Priority: "realtime"}, time.Second)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer client.Close()
	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(time.Second):
		t.Fatalf("server stream not accepted")
	}
	defer server.Close()

	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := server.Read(buf); err != nil {
		t.Fatalf("server read: %v", err)
	}

	clientStats := clientSession.IOStats()
	serverStats := serverSession.IOStats()
	if clientStats.FramesSent == 0 || clientStats.FrameBytesSent == 0 || clientStats.LastFrameSentAt.IsZero() {
		t.Fatalf("client send stats not updated: %+v", clientStats)
	}
	if serverStats.FramesReceived == 0 || serverStats.FrameBytesReceived == 0 || serverStats.LastFrameReceivedAt.IsZero() {
		t.Fatalf("server receive stats not updated: %+v", serverStats)
	}
}

func TestProbeChainFrameStreamAdaptiveChunkDoesNotWaitForFullFrame(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	clientSession, err := newProbeChainFrameClient(clientConn)
	if err != nil {
		t.Fatalf("client frame session: %v", err)
	}
	defer clientSession.Close()
	serverSession, err := newProbeChainFrameServer(serverConn)
	if err != nil {
		t.Fatalf("server frame session: %v", err)
	}
	defer serverSession.Close()

	stream := newProbeChainFrameStream(clientSession, 1)
	stream.setOpenRequest(probeChainTunnelOpenRequest{Type: "open", Priority: "realtime"})
	if got := stream.frameDataChunkBytes(17); got != 17 {
		t.Fatalf("small realtime write chunk=%d, want exact available bytes", got)
	}
	if got := stream.frameDataChunkBytes(8 * 1024); got != probeChainFrameRealtimeDataBytes {
		t.Fatalf("realtime chunk=%d, want %d", got, probeChainFrameRealtimeDataBytes)
	}

	stream.setOpenRequest(probeChainTunnelOpenRequest{Type: "open", Priority: "bulk"})
	if got := stream.frameDataChunkBytes(128 * 1024); got != probeChainFrameBulkDataBytes {
		t.Fatalf("bulk chunk=%d, want %d", got, probeChainFrameBulkDataBytes)
	}
	if got := stream.frameDataChunkBytes(1024); got != 1024 {
		t.Fatalf("small bulk write chunk=%d, want exact available bytes", got)
	}

	stream.setOpenRequest(probeChainTunnelOpenRequest{Type: "open", AppProtocol: "rdp", LatencySensitive: true})
	if got := stream.Priority(); got != "realtime" {
		t.Fatalf("rdp priority=%q, want realtime", got)
	}
	if got := stream.frameDataChunkBytes(32 * 1024); got != probeChainFrameRealtimeDataBytes {
		t.Fatalf("rdp chunk=%d, want %d", got, probeChainFrameRealtimeDataBytes)
	}
}
