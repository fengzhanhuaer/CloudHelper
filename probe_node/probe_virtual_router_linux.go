//go:build linux

package main

import (
	"fmt"
	"net"
	"strings"
	"sync"
)

var probeVirtualRouterLinuxRouteState = struct {
	mu      sync.Mutex
	applied bool
}{}

func ensureProbeVirtualRouterPlatformInterfaceIP(ip string) error {
	cleanIP := strings.TrimSpace(ip)
	if cleanIP == "" {
		return nil
	}
	if parsed := net.ParseIP(cleanIP); parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("invalid linux virtual router ipv4: %s", cleanIP)
	}
	dev, err := ensureProbeLocalLinuxTUNDeviceReady()
	if err != nil {
		return err
	}
	if cleanIP != probeLocalTUNInterfaceIPv4 {
		if err := ensureProbeLocalLinuxInterfaceIPv4(dev, cleanIP); err != nil {
			return err
		}
	}
	if !probeVirtualRouterLocalEntryEnabled() {
		return cleanupProbeVirtualRouterPlatformRoutes()
	}
	if err := ensureProbeLocalLinuxVirtualRoute(dev, cleanIP); err != nil {
		return err
	}
	if err := startProbeLocalTUNDataPlane(); err != nil {
		return err
	}
	probeVirtualRouterLinuxRouteState.mu.Lock()
	probeVirtualRouterLinuxRouteState.applied = true
	probeVirtualRouterLinuxRouteState.mu.Unlock()
	return nil
}

func cleanupProbeVirtualRouterPlatformRoutes() error {
	probeVirtualRouterLinuxRouteState.mu.Lock()
	applied := probeVirtualRouterLinuxRouteState.applied
	probeVirtualRouterLinuxRouteState.applied = false
	probeVirtualRouterLinuxRouteState.mu.Unlock()
	if !applied {
		return nil
	}
	probeLocalLinuxTakeoverState.mu.Lock()
	takeoverEnabled := probeLocalLinuxTakeoverState.enabled
	probeLocalLinuxTakeoverState.mu.Unlock()
	if takeoverEnabled {
		return nil
	}
	dev, err := resolveProbeLocalLinuxTUNDeviceName()
	if err != nil {
		return err
	}
	return deleteProbeLocalLinuxSplitRoute(probeLocalLinuxVirtualRouteCIDR, dev, "")
}
