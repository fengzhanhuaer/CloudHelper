package mobilecore

import (
	"bufio"
	"bytes"
	"net"
	"testing"
)

func TestMobileChainFrameRoundTrip(t *testing.T) {
	frame := mobileChainFrame{
		Kind:     mobileChainFrameKindControl,
		Flags:    0x55,
		StreamID: 0x0102030405060708,
		Seq:      0x1122334455667788,
		Control:  []byte(`{"op":"hello","node_id":"n1"}`),
		Data:     []byte("payload"),
	}

	encoded, err := encodeMobileChainFrame(frame, nil)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	decoded, err := decodeMobileChainFrame(encoded)
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

func TestMobileChainFramedPacketRoundTrip(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	want := []byte("hello mobile")
	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		errCh <- writeMobileChainFramedPacket(right, want)
	}()

	buf := make([]byte, mobileChainFrameMaxPayload)
	n, err := readMobileChainFramedPacketInto(bufio.NewReader(left), buf)
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

func TestMobileChainFrameStreamAdaptiveChunkUsesUpperLayerHints(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	clientSession, err := newMobileChainFrameClient(clientConn)
	if err != nil {
		t.Fatalf("client frame session: %v", err)
	}
	defer clientSession.Close()
	serverSession, err := newMobileChainFrameServer(serverConn)
	if err != nil {
		t.Fatalf("server frame session: %v", err)
	}
	defer serverSession.Close()

	stream := newMobileChainFrameStream(clientSession, 1)
	stream.setOpenRequest(linkTunnelOpenRequest{Type: "open", AppProtocol: "rdp", LatencySensitive: true})
	if got := stream.Priority(); got != "realtime" {
		t.Fatalf("rdp priority=%q, want realtime", got)
	}
	if got := stream.frameDataChunkBytes(17); got != 17 {
		t.Fatalf("small realtime write chunk=%d, want exact available bytes", got)
	}
	if got := stream.frameDataChunkBytes(32 * 1024); got != mobileChainFrameRealtimeDataBytes {
		t.Fatalf("rdp chunk=%d, want %d", got, mobileChainFrameRealtimeDataBytes)
	}

	stream.setMobileOpenRequest(mobileChainTunnelOpenRequest{Type: "open", Priority: "bulk"})
	if got := stream.frameDataChunkBytes(128 * 1024); got != mobileChainFrameBulkDataBytes {
		t.Fatalf("bulk chunk=%d, want %d", got, mobileChainFrameBulkDataBytes)
	}
}
