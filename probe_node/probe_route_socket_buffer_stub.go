//go:build !linux

package main

import "net"

func probeRouteTCPConnForceSocketBuffer(_ *net.TCPConn, _ int, _ int) (bool, error, error) {
	return false, nil, nil
}

func probeRouteTCPConnSocketBufferKernelScale() int {
	return 1
}
