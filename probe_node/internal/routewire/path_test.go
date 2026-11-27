package routewire

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

func TestPathCodecCompatibility(t *testing.T) {
	path := []string{"26", "27"}
	b, err := MarshalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(b) != "0050010000020002323600023237" {
		t.Fatalf("wire %x", b)
	}
	got, err := ParsePath(b)
	if err != nil || !reflect.DeepEqual(got, path) {
		t.Fatalf("decode %v %v", got, err)
	}
	legacy, err := ForPeer(b, false)
	if err != nil || !json.Valid(legacy) {
		t.Fatalf("legacy %s %v", legacy, err)
	}
	var decoded struct {
		Path []string `json:"path"`
	}
	if err := json.Unmarshal(legacy, &decoded); err != nil || !reflect.DeepEqual(decoded.Path, path) {
		t.Fatal("legacy lost path", err)
	}
	modern, err := ForPeer(b, true)
	if err != nil || !bytes.Equal(modern, b) {
		t.Fatal("modern changed", err)
	}
	trace := []byte(`{"path":["26","27"],"trace":[{"event":"rx"}],"future":true}`)
	for _, capable := range []bool{false, true} {
		got, err := ForPeer(trace, capable)
		if err != nil || !bytes.Equal(got, trace) {
			t.Fatal("JSON extension lost", err)
		}
	}
}

func TestRejectMalformedPath(t *testing.T) {
	b, _ := MarshalPath([]string{"26", "27"})
	for n := 0; n < len(b); n++ {
		if _, err := ParsePath(b[:n]); err == nil {
			t.Fatalf("accepted prefix %d", n)
		}
	}
	for _, at := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		bad := bytes.Clone(b)
		bad[at] = 255
		if _, err := ParsePath(bad); err == nil {
			t.Fatalf("accepted corrupt header %d", at)
		}
	}
	if _, err := ParsePath(append(bytes.Clone(b), 0)); err == nil {
		t.Fatal("trailing data")
	}
	for _, path := range [][]string{{"1", "1"}, {"", "2"}, {"a\x00b", "2"}, {string([]byte{255}), "2"}, {"1"}} {
		if _, err := MarshalPath(path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}
