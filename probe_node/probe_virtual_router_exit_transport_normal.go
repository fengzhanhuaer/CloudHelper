//go:build !linux_router

package main

import "net"

func dialProbeVirtualRouterProductExitTCP(target probeVirtualRouterExitTarget) (net.Conn, error) {
	targets, err := probeVirtualRouterExitAddressesForTarget(target)
	if err != nil {
		return nil, err
	}
	return dialProbeVirtualRouterExitTCP(targets)
}

func dialProbeVirtualRouterProductExitUDP(target probeVirtualRouterExitTarget) (net.Conn, error) {
	targets, err := probeVirtualRouterExitAddressesForTarget(target)
	if err != nil {
		return nil, err
	}
	return dialProbeVirtualRouterExitUDP(targets)
}

func probeProductAllowsPhysicalICMPExit() bool {
	return true
}
