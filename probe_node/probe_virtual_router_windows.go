//go:build windows

package main

import (
	"fmt"
	"strings"
	"sync"
)

var probeVirtualRouterWindowsRouteState = struct {
	mu       sync.Mutex
	routeDef probeLocalWindowsRouteDef
	applied  bool
}{}

func ensureProbeVirtualRouterPlatformInterfaceIP(ip string) error {
	cleanIP := strings.TrimSpace(ip)
	if cleanIP == "" {
		return nil
	}
	if !probeLocalTUNDataPlaneRunning() {
		if err := startProbeLocalTUNDataPlane(); err != nil {
			return err
		}
	}
	probeLocalTUNDataPlaneState.mu.Lock()
	interfaceLUID := probeLocalTUNDataPlaneState.interfaceLUID
	ifIndex := probeLocalTUNDataPlaneState.ifIndex
	probeLocalTUNDataPlaneState.mu.Unlock()
	if interfaceLUID > 0 {
		if err := probeLocalUpsertWindowsInterfaceIPv4ByLUID(interfaceLUID, ifIndex, cleanIP, probeLocalTUNRouteIPv4PrefixLen); err != nil {
			return err
		}
		return ensureProbeVirtualRouterWindowsFakeIPRoute(interfaceLUID, ifIndex)
	}
	if ifIndex > 0 {
		if err := probeLocalUpsertWindowsInterfaceIPv4(ifIndex, cleanIP, probeLocalTUNRouteIPv4PrefixLen); err != nil {
			return err
		}
		return ensureProbeVirtualRouterWindowsFakeIPRoute(0, ifIndex)
	}
	return nil
}

func cleanupProbeVirtualRouterPlatformRoutes() error {
	return cleanupProbeVirtualRouterWindowsFakeIPRoute()
}

func ensureProbeVirtualRouterWindowsFakeIPRoute(interfaceLUID uint64, ifIndex int) error {
	if ifIndex <= 0 {
		return nil
	}
	prefix, mask := probeLocalWindowsFakeIPRoutePrefixAndMask(currentProbeVirtualRouterFakeIPCIDR())
	routeDef := probeLocalWindowsRouteDef{
		Prefix:        prefix,
		Mask:          mask,
		Gateway:       probeLocalTUNRouteGatewayIPv4,
		InterfaceLUID: interfaceLUID,
		IfIndex:       ifIndex,
	}

	probeVirtualRouterWindowsRouteState.mu.Lock()
	oldRouteDef := probeVirtualRouterWindowsRouteState.routeDef
	needDeleteOld := probeVirtualRouterWindowsRouteState.applied && !probeVirtualRouterWindowsRouteDefEqual(oldRouteDef, routeDef)
	probeVirtualRouterWindowsRouteState.mu.Unlock()

	if needDeleteOld {
		if err := deleteProbeVirtualRouterWindowsFakeIPRoute(oldRouteDef); err != nil {
			return fmt.Errorf("cleanup old windows virtual router fake ip route failed: %w", err)
		}
	}
	if _, err := ensureProbeLocalWindowsRoute(routeDef); err != nil {
		return fmt.Errorf("ensure windows virtual router fake ip route failed: prefix=%s mask=%s if_index=%d: %w", routeDef.Prefix, routeDef.Mask, ifIndex, err)
	}

	probeVirtualRouterWindowsRouteState.mu.Lock()
	probeVirtualRouterWindowsRouteState.routeDef = routeDef
	probeVirtualRouterWindowsRouteState.applied = true
	probeVirtualRouterWindowsRouteState.mu.Unlock()
	return nil
}

func cleanupProbeVirtualRouterWindowsFakeIPRoute() error {
	probeVirtualRouterWindowsRouteState.mu.Lock()
	routeDef := probeVirtualRouterWindowsRouteState.routeDef
	applied := probeVirtualRouterWindowsRouteState.applied
	probeVirtualRouterWindowsRouteState.routeDef = probeLocalWindowsRouteDef{}
	probeVirtualRouterWindowsRouteState.applied = false
	probeVirtualRouterWindowsRouteState.mu.Unlock()
	if !applied {
		return nil
	}
	return deleteProbeVirtualRouterWindowsFakeIPRoute(routeDef)
}

func deleteProbeVirtualRouterWindowsFakeIPRoute(routeDef probeLocalWindowsRouteDef) error {
	if strings.TrimSpace(routeDef.Prefix) == "" || strings.TrimSpace(routeDef.Mask) == "" {
		return nil
	}
	return deleteProbeLocalWindowsRoute(routeDef)
}

func probeVirtualRouterWindowsRouteDefEqual(a, b probeLocalWindowsRouteDef) bool {
	return strings.EqualFold(strings.TrimSpace(a.Prefix), strings.TrimSpace(b.Prefix)) &&
		strings.EqualFold(strings.TrimSpace(a.Mask), strings.TrimSpace(b.Mask)) &&
		strings.EqualFold(strings.TrimSpace(a.Gateway), strings.TrimSpace(b.Gateway)) &&
		a.InterfaceLUID == b.InterfaceLUID &&
		a.IfIndex == b.IfIndex
}
