//go:build linux || android

package main

import (
	"strings"
	"testing"
)

func TestSumPhysicalNetworkTrafficExcludesVirtualInterfaces(t *testing.T) {
	proc := strings.NewReader("" +
		"Inter-| Receive | Transmit\n" +
		" eth0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n" +
		" cloudhelper0: 9000 0 0 0 0 0 0 0 12000 0 0 0 0 0 0 0\n" +
		" lo: 5000 0 0 0 0 0 0 0 5000 0 0 0 0 0 0 0\n")
	rx, tx, ok := sumPhysicalNetworkTraffic(proc, func(name string) bool { return name == "eth0" })
	if !ok || rx != 100 || tx != 200 {
		t.Fatalf("traffic=(%d,%d,%t), want (100,200,true)", rx, tx, ok)
	}
}
