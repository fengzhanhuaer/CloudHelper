//go:build windows

package main

import (
	"net"

	"golang.org/x/sys/windows"
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
		readBuffer, controlErr = windows.GetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_RCVBUF)
		if controlErr != nil {
			return
		}
		writeBuffer, controlErr = windows.GetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_SNDBUF)
	})
	if err != nil {
		return readBuffer, writeBuffer, err
	}
	return readBuffer, writeBuffer, controlErr
}
