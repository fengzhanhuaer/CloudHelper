//go:build windows

package main

import (
	"math/bits"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// Windows socket 选项：把出站 socket 绑定到指定接口索引。
// x/sys/windows v0.35.0 未导出这两个常量，按 Win32 头文件取标准值（均为 31，分别属于 IPPROTO_IP / IPPROTO_IPV6 层级）。
const (
	probeWindowsIPUnicastIf   = 31 // IP_UNICAST_IF
	probeWindowsIPv6UnicastIf = 31 // IPV6_UNICAST_IF
)

func init() {
	probeLocalEgressDialControl = nil
}

// probeLocalBindEgressInterfaceControl 在拨号 socket 上设置 IP_UNICAST_IF / IPV6_UNICAST_IF，
// 把出站流量强制绑定到当前物理出口接口，绕过 TUN 安装的 0.0.0.0/1 + 128.0.0.0/1 默认路由，
// 替代逐 IP host route 绕行。
//
// 接口索引取自 currentProbeLocalWindowsDirectBypassRouteTarget()（运行期可被刷新 ticker 更新）；
// 未准备好或索引无效时返回 nil（不绑定），退回内核默认选路——这正是 TUN 未接管时期望的行为。
//
// 字节序坑：IPv4 的 IP_UNICAST_IF 接口索引须为网络字节序（大端），IPv6 的 IPV6_UNICAST_IF 用主机序。
func probeLocalBindEgressInterfaceControl(network, address string, c syscall.RawConn) error {
	target, ok := currentProbeLocalWindowsDirectBypassRouteTarget()
	if !ok || target.InterfaceIndex <= 0 {
		return nil
	}
	idx := uint32(target.InterfaceIndex)
	v4Index := int(bits.ReverseBytes32(idx)) // big-endian for IPv4
	v6Index := int(idx)                      // host order for IPv6

	var setErr error
	ctrlErr := c.Control(func(fd uintptr) {
		h := windows.Handle(fd)
		switch {
		case strings.HasSuffix(network, "4"):
			setErr = windows.SetsockoptInt(h, windows.IPPROTO_IP, probeWindowsIPUnicastIf, v4Index)
		case strings.HasSuffix(network, "6"):
			setErr = windows.SetsockoptInt(h, windows.IPPROTO_IPV6, probeWindowsIPv6UnicastIf, v6Index)
		default:
			// network 未指定地址族（"tcp" / "udp"）：best-effort 两者都设。
			// 纯 v4 socket 上设 v6 选项会失败（反之亦然），任一成功即认为已绑定。
			err4 := windows.SetsockoptInt(h, windows.IPPROTO_IP, probeWindowsIPUnicastIf, v4Index)
			err6 := windows.SetsockoptInt(h, windows.IPPROTO_IPV6, probeWindowsIPv6UnicastIf, v6Index)
			if err4 != nil && err6 != nil {
				setErr = err4
			}
		}
	})
	if ctrlErr != nil {
		return ctrlErr
	}
	return setErr
}
