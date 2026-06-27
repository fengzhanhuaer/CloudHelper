//go:build windows

package main

import "strings"

func ensureProbeVirtualRouterPlatformInterfaceIP(ip string) error {
	cleanIP := strings.TrimSpace(ip)
	if cleanIP == "" {
		return nil
	}
	probeLocalTUNDataPlaneState.mu.Lock()
	interfaceLUID := probeLocalTUNDataPlaneState.interfaceLUID
	ifIndex := probeLocalTUNDataPlaneState.ifIndex
	probeLocalTUNDataPlaneState.mu.Unlock()
	if interfaceLUID > 0 {
		return probeLocalUpsertWindowsInterfaceIPv4ByLUID(interfaceLUID, ifIndex, cleanIP, probeLocalTUNRouteIPv4PrefixLen)
	}
	if ifIndex > 0 {
		return probeLocalUpsertWindowsInterfaceIPv4(ifIndex, cleanIP, probeLocalTUNRouteIPv4PrefixLen)
	}
	return nil
}
