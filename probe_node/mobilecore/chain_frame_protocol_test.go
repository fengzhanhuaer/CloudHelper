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
