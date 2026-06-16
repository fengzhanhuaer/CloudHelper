package main

import (
	"bufio"
	"bytes"
	"net"
	"testing"
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
