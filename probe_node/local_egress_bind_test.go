package main

import (
	"net"
	"testing"
)

func TestProbeLocalEgressDialNetwork(t *testing.T) {
	cases := []struct {
		name   string
		base   string
		target string
		want   string
	}{
		{name: "ipv4 tcp", base: "tcp", target: "1.2.3.4:443", want: "tcp4"},
		{name: "ipv6 tcp", base: "tcp", target: "[2001:db8::1]:443", want: "tcp6"},
		{name: "ipv4 udp", base: "udp", target: "8.8.8.8:53", want: "udp4"},
		{name: "ipv6 udp", base: "udp", target: "[2606:4700::1111]:53", want: "udp6"},
		{name: "hostname falls back to base", base: "tcp", target: "example.com:443", want: "tcp"},
		{name: "invalid target falls back to base", base: "udp", target: "not-an-addr", want: "udp"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := probeLocalEgressDialNetwork(tt.base, tt.target); got != tt.want {
				t.Fatalf("probeLocalEgressDialNetwork(%q,%q)=%q want %q", tt.base, tt.target, got, tt.want)
			}
		})
	}
}

func TestApplyProbeLocalEgressDialerDoesNotBindInterface(t *testing.T) {
	// Egress bypass is route-based; the dialer must not install a per-socket
	// interface binding hook.
	d := applyProbeLocalEgressDialer(&net.Dialer{})
	if d.Control != nil {
		t.Fatal("dialer Control should stay nil")
	}
}
