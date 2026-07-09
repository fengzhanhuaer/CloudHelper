//go:build linux

package main

import (
	"net"

	"golang.org/x/sys/unix"
)

func probeRouteTCPConnForceSocketBuffer(conn *net.TCPConn, readBytes int, writeBytes int) (bool, error, error) {
	if conn == nil {
		return false, nil, nil
	}
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return false, err, err
	}
	var readErr error
	var writeErr error
	err = rawConn.Control(func(fd uintptr) {
		if readBytes > 0 {
			readErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, readBytes)
		}
		if writeBytes > 0 {
			writeErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUFFORCE, writeBytes)
		}
	})
	if err != nil {
		if readErr == nil {
			readErr = err
		}
		if writeErr == nil {
			writeErr = err
		}
	}
	return true, readErr, writeErr
}

func probeRouteTCPConnSocketBufferKernelScale() int {
	return 2
}
