package main

import (
	"net"
	"strings"
	"syscall"
)

// probeLocalEgressDialControl is retained for compatibility with older tests and
// builds, but route-based bypass is now preferred over per-socket interface
// binding. Direct relay/bootstrap traffic is kept out of TUN by explicit host
// routes instead of IP_UNICAST_IF / IPV6_UNICAST_IF.
var probeLocalEgressDialControl func(network, address string, c syscall.RawConn) error

// applyProbeLocalEgressDialer returns the dialer unchanged. Bypass is handled by
// system routes before dialing.
func applyProbeLocalEgressDialer(d *net.Dialer) *net.Dialer {
	return d
}

// probeLocalEgressListenConfig returns a plain ListenConfig. Bypass is handled
// by explicit system routes before the packet socket is opened.
func probeLocalEgressListenConfig() *net.ListenConfig {
	return &net.ListenConfig{}
}

// probeLocalEgressDialNetwork 根据目标地址推断带族后缀的 dial network（tcp4/tcp6/udp4/udp6），
// 让接口绑定能精确设置 IP_UNICAST_IF 或 IPV6_UNICAST_IF，避免 dual-stack socket 的绑定歧义。
// base 为 "tcp" 或 "udp"；无法判定地址族时回退到 base。
func probeLocalEgressDialNetwork(base, targetAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return base
	}
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]")))
	if ip == nil {
		return base
	}
	if ip.To4() != nil {
		return base + "4"
	}
	return base + "6"
}
