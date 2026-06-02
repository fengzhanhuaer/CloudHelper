package main

import (
	"net"
	"strings"
	"syscall"
)

// probeLocalEgressDialControl 当非 nil 时被注入到所有"直连"出站拨号的
// net.Dialer.Control / net.ListenConfig.Control，用于把 socket 绑定到物理出口网卡接口
// （Windows 上即 IP_UNICAST_IF / IPV6_UNICAST_IF），从而绕过 TUN 安装的默认路由，
// 替代逐 IP host route 绕行。非 Windows 平台保持 nil（空操作）。
// Windows build 在 init() 中将其赋值为 probeLocalBindEgressInterfaceControl。
var probeLocalEgressDialControl func(network, address string, c syscall.RawConn) error

// applyProbeLocalEgressDialer 把出口接口绑定注入到给定 dialer，并原样返回以便链式调用。
func applyProbeLocalEgressDialer(d *net.Dialer) *net.Dialer {
	if d != nil && probeLocalEgressDialControl != nil {
		d.Control = probeLocalEgressDialControl
	}
	return d
}

// probeLocalEgressListenConfig 返回带出口接口绑定的 net.ListenConfig，
// 供需要自建 UDP socket 的场景使用（如 QUIC：先 ListenPacket 再 quic.Dial）。
func probeLocalEgressListenConfig() *net.ListenConfig {
	lc := &net.ListenConfig{}
	if probeLocalEgressDialControl != nil {
		lc.Control = probeLocalEgressDialControl
	}
	return lc
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
