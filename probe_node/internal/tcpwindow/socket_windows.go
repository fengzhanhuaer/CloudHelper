package tcpwindow

import (
	"net"
	"syscall"

	"golang.org/x/sys/windows"
)

func Control(_, _ string, raw syscall.RawConn) error {
	return raw.Control(func(fd uintptr) {
		_ = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_RCVBUF, Payload)
		_ = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_SNDBUF, Payload)
	})
}

func nativeSetter(conn *net.TCPConn) func(bool, int) {
	return func(receive bool, size int) {
		if receive {
			_ = conn.SetReadBuffer(size)
		} else {
			_ = conn.SetWriteBuffer(size)
		}
	}
}
