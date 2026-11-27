//go:build !linux && !windows

package tcpwindow

import (
	"net"
	"syscall"
)

func Control(_, _ string, _ syscall.RawConn) error { return nil }
func nativeSetter(_ *net.TCPConn) func(bool, int)  { return nil }
