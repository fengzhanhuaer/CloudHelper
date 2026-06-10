package main

import (
	"net"
	"strings"
)

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
// 避免 dual-stack socket 在 UDP/QUIC 场景下选到不匹配的地址族。
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
