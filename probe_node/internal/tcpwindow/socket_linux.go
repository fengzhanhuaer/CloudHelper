package tcpwindow

import (
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Control runs before connect or listen, while window scaling is negotiable.
// When an unprivileged app cannot request this budget, preserve OS autotuning.
func Control(_, _ string, raw syscall.RawConn) error {
	return raw.Control(func(fd uintptr) {
		socketBudget(int(fd), unix.SO_RCVBUFFORCE, unix.SO_RCVBUF, "/proc/sys/net/core/rmem_max")
		socketBudget(int(fd), unix.SO_SNDBUFFORCE, unix.SO_SNDBUF, "/proc/sys/net/core/wmem_max")
	})
}

func nativeSetter(conn *net.TCPConn) func(bool, int) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil
	}
	var rx, tx int
	if raw.Control(func(fd uintptr) {
		rx, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF)
		tx, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUF)
	}) != nil || (rx < MaxBuffer && tx < MaxBuffer) {
		return nil
	}
	return func(receive bool, size int) {
		opt, force := unix.SO_SNDBUF, unix.SO_SNDBUFFORCE
		if receive {
			opt, force = unix.SO_RCVBUF, unix.SO_RCVBUFFORCE
		}
		if (receive && rx < MaxBuffer) || (!receive && tx < MaxBuffer) {
			return
		}
		_ = raw.Control(func(fd uintptr) {
			if unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, force, size) != nil {
				_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, opt, size)
			}
		})
	}
}

func socketBudget(fd, force, normal int, limitFile string) {
	if unix.SetsockoptInt(fd, unix.SOL_SOCKET, force, Payload) == nil {
		return
	}
	raw, err := os.ReadFile(limitFile)
	if err != nil {
		return
	}
	limit, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err == nil && limit >= Payload {
		_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, normal, Payload)
	}
}
