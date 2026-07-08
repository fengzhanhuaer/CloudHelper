//go:build !windows

package main

import (
	"net"

	"golang.org/x/sys/unix"
)

func probeRouteTCPConnSocketBufferSnapshot(conn *net.TCPConn) (int, int, error) {
	if conn == nil {
		return 0, 0, nil
	}
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var readBuffer int
	var writeBuffer int
	var controlErr error
	err = rawConn.Control(func(fd uintptr) {
		readBuffer, controlErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF)
		if controlErr != nil {
			return
		}
		writeBuffer, controlErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUF)
	})
	if err != nil {
		return readBuffer, writeBuffer, err
	}
	return readBuffer, writeBuffer, controlErr
}
