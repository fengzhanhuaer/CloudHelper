//go:build linux || android

package mobilecore

import (
	"strings"
	"testing"
)

func TestSumPhysicalNetworkTrafficExcludesVirtualInterfaces(t *testing.T) {
	proc := strings.NewReader("" +
		"Inter-| Receive | Transmit\n" +
		" wlan0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n" +
		" tun0: 9000 0 0 0 0 0 0 0 12000 0 0 0 0 0 0 0\n")
	rx, tx, ok := sumPhysicalNetworkTraffic(proc, func(name string) bool { return name == "wlan0" })
	if !ok || rx != 100 || tx != 200 {
		t.Fatalf("traffic=(%d,%d,%t), want (100,200,true)", rx, tx, ok)
	}
}
