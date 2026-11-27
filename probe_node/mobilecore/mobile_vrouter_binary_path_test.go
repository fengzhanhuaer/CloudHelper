package mobilecore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"github.com/cloudhelper/probe_node/internal/routewire"
	"testing"
)

func TestMobileBinaryPathNegotiation(t *testing.T) {
	frame, err := buildMobileVRouteIPFrame([]byte("packet"), []string{"26", "27"})
	if err != nil {
		t.Fatal(err)
	}
	if !routewire.IsBinary(frame.Control) {
		t.Fatal("packet path is not binary")
	}
	for _, capable := range []bool{false, true} {
		b, err := encodeMobileVRouteFramesForPeer([]mobileVRouteFrame{frame}, capable)
		if err != nil {
			t.Fatal(err)
		}
		got, err := readMobileVRouteFrame(bufio.NewReader(bytes.NewReader(b)))
		if err != nil {
			t.Fatal(err)
		}
		if routewire.IsBinary(got.Control) != capable || !bytes.Equal(got.Data, frame.Data) {
			t.Fatal("negotiation or payload mismatch")
		}
		if capable {
			path, err := routewire.ParsePath(got.Control)
			if err != nil || len(path) != 2 || path[1] != "27" {
				t.Fatal(path, err)
			}
		} else if !json.Valid(got.Control) {
			t.Fatal("legacy received non-JSON")
		}
	}
}
