package main

import (
	"net"
	"syscall"
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

func TestApplyProbeLocalEgressDialerNilControlIsNoop(t *testing.T) {
	// 当出口绑定钩子未安装（非 Windows 或未启用）时，dialer.Control 应保持为 nil。
	old := probeLocalEgressDialControl
	t.Cleanup(func() { probeLocalEgressDialControl = old })

	probeLocalEgressDialControl = nil
	d := applyProbeLocalEgressDialer(&net.Dialer{})
	if d.Control != nil {
		t.Fatal("dialer Control should stay nil when egress hook is unset")
	}

	called := false
	probeLocalEgressDialControl = func(string, string, syscall.RawConn) error {
		called = true
		return nil
	}
	d2 := applyProbeLocalEgressDialer(&net.Dialer{})
	if d2.Control == nil {
		t.Fatal("dialer Control should be set when egress hook is installed")
	}
	_ = d2.Control("tcp4", "1.2.3.4:443", nil)
	if !called {
		t.Fatal("installed egress control should be invoked")
	}
}
