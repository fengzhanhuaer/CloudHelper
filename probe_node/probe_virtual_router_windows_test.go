//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestEnsureProbeVirtualRouterPlatformInterfaceIPWindowsAppliesOnlyFakeIPRoute(t *testing.T) {
	resetProbeLocalTUNInstallWindowsHooksForTest()
	resetProbeLocalTUNDataPlaneHooksForTest()
	resetProbeLocalWindowsTakeoverStateForTest()
	resetProbeVirtualRouterWindowsRouteStateForTest()
	t.Cleanup(func() {
		resetProbeLocalTUNInstallWindowsHooksForTest()
		resetProbeLocalTUNDataPlaneHooksForTest()
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeVirtualRouterWindowsRouteStateForTest()
	})

	probeLocalTUNDataPlaneState.mu.Lock()
	probeLocalTUNDataPlaneState.dataPlane = &fakeProbeLocalTUNDataPlane{stats: probeLocalTUNDataPlaneStats{Running: true}}
	probeLocalTUNDataPlaneState.interfaceLUID = 77
	probeLocalTUNDataPlaneState.ifIndex = 40
	probeLocalTUNDataPlaneState.mu.Unlock()
	t.Cleanup(func() {
		probeLocalTUNDataPlaneState.mu.Lock()
		probeLocalTUNDataPlaneState.dataPlane = nil
		probeLocalTUNDataPlaneState.interfaceLUID = 0
		probeLocalTUNDataPlaneState.ifIndex = 0
		probeLocalTUNDataPlaneState.mu.Unlock()
	})

	var routeCreateCalls []probeLocalWindowsRouteDef
	oldCreateRoute := probeLocalCreateWindowsRouteEntry
	probeLocalCreateWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) (bool, error) {
		routeCreateCalls = append(routeCreateCalls, routeDef)
		switch strings.TrimSpace(routeDef.Prefix) {
		case probeLocalWindowsRouteSplitPrefixA, probeLocalWindowsRouteSplitPrefixB:
			t.Fatalf("virtual router must not create global takeover route: %+v", routeDef)
		}
		if strings.TrimSpace(routeDef.Mask) == probeLocalWindowsHostRouteMask {
			t.Fatalf("virtual router must not create DNS capture route: %+v", routeDef)
		}
		return true, nil
	}
	t.Cleanup(func() {
		probeLocalCreateWindowsRouteEntry = oldCreateRoute
	})

	upsertCalls := 0
	probeLocalUpsertWindowsInterfaceIPv4ByLUID = func(luid uint64, ifIndex int, ip string, prefixLength int) error {
		upsertCalls++
		if luid != 77 {
			t.Fatalf("luid=%d want 77", luid)
		}
		if ifIndex != 40 {
			t.Fatalf("ifIndex=%d want 40", ifIndex)
		}
		if strings.TrimSpace(ip) != "198.18.0.21" {
			t.Fatalf("ip=%q want 198.18.0.21", ip)
		}
		if prefixLength != probeLocalTUNRouteIPv4PrefixLen {
			t.Fatalf("prefixLength=%d want %d", prefixLength, probeLocalTUNRouteIPv4PrefixLen)
		}
		return nil
	}
	probeLocalUpsertWindowsInterfaceIPv4 = func(ifIndex int, ip string, prefixLength int) error {
		t.Fatalf("expected LUID path, got ifIndex=%d ip=%s prefix=%d", ifIndex, ip, prefixLength)
		return nil
	}

	if err := ensureProbeVirtualRouterPlatformInterfaceIP("198.18.0.21"); err != nil {
		t.Fatalf("ensureProbeVirtualRouterPlatformInterfaceIP returned error: %v", err)
	}
	if upsertCalls != 1 {
		t.Fatalf("upsertCalls=%d want 1", upsertCalls)
	}
	if len(routeCreateCalls) != 1 {
		t.Fatalf("routeCreateCalls=%d want 1 calls=%+v", len(routeCreateCalls), routeCreateCalls)
	}
	routeDef := routeCreateCalls[0]
	if strings.TrimSpace(routeDef.Prefix) != "198.18.0.0" || strings.TrimSpace(routeDef.Mask) != "255.254.0.0" {
		t.Fatalf("route=%+v want fake ip 198.18.0.0/15", routeDef)
	}
	if strings.TrimSpace(routeDef.Gateway) != probeLocalTUNRouteGatewayIPv4 || routeDef.InterfaceLUID != 77 || routeDef.IfIndex != 40 {
		t.Fatalf("route target=%+v want gateway=%s luid=77 ifindex=40", routeDef, probeLocalTUNRouteGatewayIPv4)
	}
	if _, enabled := currentProbeLocalWindowsTakeoverIfIndex(); enabled {
		t.Fatalf("virtual router interface IP setup must not enable global takeover")
	}
}

func TestCleanupProbeVirtualRouterPlatformRoutesWindowsDeletesFakeIPRoute(t *testing.T) {
	resetProbeLocalWindowsTakeoverStateForTest()
	resetProbeVirtualRouterWindowsRouteStateForTest()
	t.Cleanup(func() {
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeVirtualRouterWindowsRouteStateForTest()
	})

	routeDef := probeLocalWindowsRouteDef{
		Prefix:        "198.18.0.0",
		Mask:          "255.254.0.0",
		Gateway:       probeLocalTUNRouteGatewayIPv4,
		InterfaceLUID: 77,
		IfIndex:       40,
	}
	probeVirtualRouterWindowsRouteState.mu.Lock()
	probeVirtualRouterWindowsRouteState.routeDef = routeDef
	probeVirtualRouterWindowsRouteState.applied = true
	probeVirtualRouterWindowsRouteState.mu.Unlock()

	var deleted []probeLocalWindowsRouteDef
	oldDeleteRoute := probeLocalDeleteWindowsRouteEntry
	probeLocalDeleteWindowsRouteEntry = func(got probeLocalWindowsRouteDef) error {
		deleted = append(deleted, got)
		return nil
	}
	t.Cleanup(func() {
		probeLocalDeleteWindowsRouteEntry = oldDeleteRoute
	})

	if err := cleanupProbeVirtualRouterPlatformRoutes(); err != nil {
		t.Fatalf("cleanupProbeVirtualRouterPlatformRoutes returned error: %v", err)
	}
	if len(deleted) != 1 || !probeVirtualRouterWindowsRouteDefEqual(deleted[0], routeDef) {
		t.Fatalf("deleted=%+v want %+v", deleted, routeDef)
	}
	probeVirtualRouterWindowsRouteState.mu.Lock()
	applied := probeVirtualRouterWindowsRouteState.applied
	probeVirtualRouterWindowsRouteState.mu.Unlock()
	if applied {
		t.Fatalf("virtual router windows route state should be cleared")
	}
}

func resetProbeVirtualRouterWindowsRouteStateForTest() {
	probeVirtualRouterWindowsRouteState.mu.Lock()
	probeVirtualRouterWindowsRouteState.routeDef = probeLocalWindowsRouteDef{}
	probeVirtualRouterWindowsRouteState.applied = false
	probeVirtualRouterWindowsRouteState.mu.Unlock()
}
