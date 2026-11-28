package routewire

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// CarrierTCPRetransmits samples the kernel counter for the current socket.
// It returns nil for transports or kernels without a readable TCP counter.
func CarrierTCPRetransmits(conn net.Conn) *uint64 {
	for range 4 {
		switch c := conn.(type) {
		case *bufferedConn:
			conn = c.Conn
		case interface{ NetConn() net.Conn }:
			conn = c.NetConn()
		case syscall.Conn:
			raw, err := c.SyscallConn()
			if err != nil {
				return nil
			}
			var count *uint64
			if err := raw.Control(func(fd uintptr) {
				info, err := unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
				if err == nil {
					total := uint64(info.Total_retrans)
					count = &total
				}
			}); err != nil {
				return nil
			}
			return count
		default:
			return nil
		}
	}
	return nil
}
