package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"github.com/cloudhelper/probe_node/internal/routewire"
	"testing"
)

type binaryPathWriter struct {
	bytes.Buffer
	capable bool
}

func (w *binaryPathWriter) BinaryPathSupported() bool { return w.capable }

func TestProbeVirtualRouterBinaryPathNegotiation(t *testing.T) {
	packet := []byte("unchanged packet")
	frame, err := buildProbeVirtualRouterIPFrame(packet, []string{"26", "27"}, nil)
	if err != nil || !routewire.IsBinary(frame.Control) {
		t.Fatal("packet path is not binary", err)
	}
	for _, capable := range []bool{false, true} {
		w := &binaryPathWriter{capable: capable}
		if err := writeProbeVirtualRouterWireFrameRaw(w, frame); err != nil {
			t.Fatal(err)
		}
		got, err := readProbeVirtualRouterWireFrame(bufio.NewReader(&w.Buffer))
		if err != nil {
			t.Fatal(err)
		}
		if routewire.IsBinary(got.Control) != capable || !bytes.Equal(got.Data, packet) {
			t.Fatal("negotiation or payload mismatch")
		}
		if !capable && !json.Valid(got.Control) {
			t.Fatal("legacy received non-JSON")
		}
		control, err := probeVirtualRouterFrameControl(got, nil)
		if err != nil || len(control.Path) != 2 || control.Path[1] != "27" {
			t.Fatal("path mismatch", err)
		}
	}
}
