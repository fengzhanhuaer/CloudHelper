//go:build windows

package main

import "testing"

func TestProbeVRouteWindowsProxyServerValueUsesSOCKS5(t *testing.T) {
	got := probeVRouteWindowsProxyServerValue("127.0.0.1:18080", "127.0.0.1:18081")
	want := "http=127.0.0.1:18080;https=127.0.0.1:18080;socks=socks5://127.0.0.1:18081"
	if got != want {
		t.Fatalf("windows proxy server value=%q want=%q", got, want)
	}
}

func TestProbeVRouteSystemProxyAddressMapsWildcardToLoopback(t *testing.T) {
	got, err := probeVRouteSystemProxyAddress("0.0.0.0:18080")
	if err != nil || got != "127.0.0.1:18080" {
		t.Fatalf("system proxy address=%q err=%v", got, err)
	}
}
