package mobilecore

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMobileRouteFrameRoundTrip(t *testing.T) {
	frame := mobileRouteFrame{
		Kind:     mobileRouteFrameKindControl,
		Flags:    0x55,
		StreamID: 0x0102030405060708,
		Seq:      0x1122334455667788,
		Control:  []byte(`{"op":"hello","node_id":"n1"}`),
		Data:     []byte("payload"),
	}

	encoded, err := encodeMobileRouteFrame(frame, nil)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	decoded, err := decodeMobileRouteFrame(encoded)
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

func TestMobileRouteFramedPacketRoundTrip(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	want := []byte("hello mobile")
	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		errCh <- writeMobileRouteFramedPacket(right, want)
	}()

	buf := make([]byte, mobileRouteFrameMaxPayload)
	n, err := readMobileRouteFramedPacketInto(bufio.NewReader(left), buf)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !bytes.Equal(buf[:n], want) {
		t.Fatalf("payload mismatch: got %q want %q", buf[:n], want)
	}
}

func TestMobileRouteFrameStreamAdaptiveChunkUsesUpperLayerHints(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	clientSession, err := newMobileRouteFrameClient(clientConn)
	if err != nil {
		t.Fatalf("client frame session: %v", err)
	}
	defer clientSession.Close()
	serverSession, err := newMobileRouteFrameServer(serverConn)
	if err != nil {
		t.Fatalf("server frame session: %v", err)
	}
	defer serverSession.Close()

	stream := newMobileRouteFrameStream(clientSession, 1)
	stream.setOpenRequest(routeTunnelOpenRequest{Type: "open", AppProtocol: "rdp", LatencySensitive: true})
	if got := stream.Priority(); got != "realtime" {
		t.Fatalf("rdp priority=%q, want realtime", got)
	}
	if got := stream.frameDataChunkBytes(17); got != 17 {
		t.Fatalf("small realtime write chunk=%d, want exact available bytes", got)
	}
	if got := stream.frameDataChunkBytes(32 * 1024); got != mobileRouteFrameRealtimeDataBytes {
		t.Fatalf("rdp chunk=%d, want %d", got, mobileRouteFrameRealtimeDataBytes)
	}

	stream.setMobileOpenRequest(mobileRouteTunnelOpenRequest{Type: "open", Priority: "bulk"})
	if got := stream.frameDataChunkBytes(128 * 1024); got != mobileRouteFrameBulkDataBytes {
		t.Fatalf("bulk chunk=%d, want %d", got, mobileRouteFrameBulkDataBytes)
	}
}

func TestMobileRouteFrameSessionNegotiatesConfigAndFinReset(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	clientSession, err := newMobileRouteFrameClient(clientConn)
	if err != nil {
		t.Fatalf("client frame session: %v", err)
	}
	defer clientSession.Close()
	serverSession, err := newMobileRouteFrameServer(serverConn)
	if err != nil {
		t.Fatalf("server frame session: %v", err)
	}
	defer serverSession.Close()

	waitMobileRouteFrameConfig(t, clientSession)
	waitMobileRouteFrameConfig(t, serverSession)

	accepted := make(chan *mobileRouteFrameStream, 2)
	go func() {
		for i := 0; i < 2; i++ {
			stream, acceptErr := serverSession.Accept()
			if acceptErr != nil {
				return
			}
			frameStream, ok := stream.(*mobileRouteFrameStream)
			if !ok {
				return
			}
			_ = frameStream.RespondMobileOpen(mobileRouteTunnelOpenResponse{OK: true})
			accepted <- frameStream
		}
	}()

	first, err := clientSession.OpenWithMobileRequest(mobileRouteTunnelOpenRequest{Type: "open", Network: "tcp", Address: "127.0.0.1:3389"}, time.Second)
	if err != nil {
		t.Fatalf("open first stream: %v", err)
	}
	if err := first.(*mobileRouteFrameStream).CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
	serverFirst := <-accepted
	_ = serverFirst.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := serverFirst.Read(make([]byte, 1)); err == nil {
		t.Fatal("server read after fin succeeded, want EOF")
	}

	second, err := clientSession.OpenWithMobileRequest(mobileRouteTunnelOpenRequest{Type: "open", Network: "tcp", Address: "127.0.0.1:3390"}, time.Second)
	if err != nil {
		t.Fatalf("open second stream: %v", err)
	}
	if err := second.(*mobileRouteFrameStream).Reset("boom"); err != nil {
		t.Fatalf("reset stream: %v", err)
	}
	serverSecond := <-accepted
	_ = serverSecond.SetReadDeadline(time.Now().Add(time.Second))
	_, err = serverSecond.Read(make([]byte, 1))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("server read after reset err=%v, want boom", err)
	}
}

func waitMobileRouteFrameConfig(t *testing.T, session *mobileRouteFrameSession) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		cfg := session.NegotiatedConfig()
		if len(cfg.Features) > 0 && cfg.MaxFrameData > 0 && cfg.RealtimeFrameData > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("frame config was not negotiated: %+v", session.NegotiatedConfig())
}
