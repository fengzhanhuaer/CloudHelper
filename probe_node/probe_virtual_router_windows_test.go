//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestEnsureProbeVirtualRouterPlatformInterfaceIPWindowsAppliesTakeoverAndLocalBypassRoutes(t *testing.T) {
	resetProbeLocalTUNInstallWindowsHooksForTest()
	resetProbeLocalTUNDataPlaneHooksForTest()
	resetProbeLocalWindowsTakeoverStateForTest()
	resetProbeVirtualRouterWindowsRouteStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(true, false)
	t.Cleanup(func() {
		resetProbeLocalTUNInstallWindowsHooksForTest()
		resetProbeLocalTUNDataPlaneHooksForTest()
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeVirtualRouterWindowsRouteStateForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
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
	oldResolvePrimaryEgress := probeLocalResolveWindowsPrimaryEgressRoute
	probeLocalCreateWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) (bool, error) {
		routeCreateCalls = append(routeCreateCalls, routeDef)
		return true, nil
	}
	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeLocalWindowsDirectBypassRouteTarget, error) {
		if excludedIfIndex != 40 {
			t.Fatalf("excludedIfIndex=%d want 40", excludedIfIndex)
		}
		return probeLocalWindowsDirectBypassRouteTarget{InterfaceIndex: 13, NextHop: "192.168.50.1"}, nil
	}
	t.Cleanup(func() {
		probeLocalCreateWindowsRouteEntry = oldCreateRoute
		probeLocalResolveWindowsPrimaryEgressRoute = oldResolvePrimaryEgress
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
	if len(routeCreateCalls) != 6 {
		t.Fatalf("routeCreateCalls=%d want 6 calls=%+v", len(routeCreateCalls), routeCreateCalls)
	}
	assertProbeVirtualRouterWindowsRouteDef(t, routeCreateCalls, probeLocalWindowsRouteDef{
		Prefix:        "198.18.0.0",
		Mask:          "255.254.0.0",
		Gateway:       probeLocalTUNRouteGatewayIPv4,
		InterfaceLUID: 77,
		IfIndex:       40,
	})
	assertProbeVirtualRouterWindowsRouteDef(t, routeCreateCalls, probeLocalWindowsRouteDef{
		Prefix:        probeVirtualRouterWindowsRouteSplitPrefixA,
		Mask:          probeVirtualRouterWindowsRouteSplitMaskA,
		Gateway:       probeLocalTUNRouteGatewayIPv4,
		InterfaceLUID: 77,
		IfIndex:       40,
	})
	assertProbeVirtualRouterWindowsRouteDef(t, routeCreateCalls, probeLocalWindowsRouteDef{
		Prefix:        probeVirtualRouterWindowsRouteSplitPrefixB,
		Mask:          probeVirtualRouterWindowsRouteSplitMaskB,
		Gateway:       probeLocalTUNRouteGatewayIPv4,
		InterfaceLUID: 77,
		IfIndex:       40,
	})
	for _, prefix := range []string{"10.0.0.0", "172.16.0.0", "192.168.0.0"} {
		assertProbeVirtualRouterWindowsRouteDef(t, routeCreateCalls, probeLocalWindowsRouteDef{
			Prefix:  prefix,
			Gateway: "192.168.50.1",
			IfIndex: 13,
		})
	}
}

func TestEnsureProbeVirtualRouterPlatformInterfaceIPWindowsDisabledKeepsFakeRouteAndCleansTakeoverRoutes(t *testing.T) {
	resetProbeLocalTUNInstallWindowsHooksForTest()
	resetProbeLocalTUNDataPlaneHooksForTest()
	resetProbeLocalWindowsTakeoverStateForTest()
	resetProbeVirtualRouterWindowsRouteStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(false, false)
	t.Cleanup(func() {
		resetProbeLocalTUNInstallWindowsHooksForTest()
		resetProbeLocalTUNDataPlaneHooksForTest()
		resetProbeLocalWindowsTakeoverStateForTest()
		resetProbeVirtualRouterWindowsRouteStateForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	probeVirtualRouterWindowsRouteState.mu.Lock()
	probeVirtualRouterWindowsRouteState.takeoverRouteDefs = []probeLocalWindowsRouteDef{{
		Prefix:  probeVirtualRouterWindowsRouteSplitPrefixA,
		Mask:    probeVirtualRouterWindowsRouteSplitMaskA,
		Gateway: probeLocalTUNRouteGatewayIPv4,
		IfIndex: 40,
	}}
	probeVirtualRouterWindowsRouteState.mu.Unlock()
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

	var created []probeLocalWindowsRouteDef
	var deleted []probeLocalWindowsRouteDef
	oldCreateRoute := probeLocalCreateWindowsRouteEntry
	oldDeleteRoute := probeLocalDeleteWindowsRouteEntry
	probeLocalCreateWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) (bool, error) {
		created = append(created, routeDef)
		return true, nil
	}
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeLocalWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}
	t.Cleanup(func() {
		probeLocalCreateWindowsRouteEntry = oldCreateRoute
		probeLocalDeleteWindowsRouteEntry = oldDeleteRoute
	})

	if err := ensureProbeVirtualRouterPlatformInterfaceIP("198.18.0.21"); err != nil {
		t.Fatalf("ensure disabled virtual router route failed: %v", err)
	}
	if len(created) != 1 || strings.TrimSpace(created[0].Prefix) != "198.18.0.0" {
		t.Fatalf("created routes=%+v, want only fake ip route", created)
	}
	if len(deleted) != 1 || strings.TrimSpace(deleted[0].Prefix) != probeVirtualRouterWindowsRouteSplitPrefixA {
		t.Fatalf("deleted takeover routes=%+v", deleted)
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
	probeVirtualRouterWindowsRouteState.fakeRouteDef = routeDef
	probeVirtualRouterWindowsRouteState.fakeApplied = true
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
	applied := probeVirtualRouterWindowsRouteState.fakeApplied
	probeVirtualRouterWindowsRouteState.mu.Unlock()
	if applied {
		t.Fatalf("virtual router windows route state should be cleared")
	}
}

func resetProbeVirtualRouterWindowsRouteStateForTest() {
	probeVirtualRouterWindowsRouteState.mu.Lock()
	probeVirtualRouterWindowsRouteState.fakeRouteDef = probeLocalWindowsRouteDef{}
	probeVirtualRouterWindowsRouteState.fakeApplied = false
	probeVirtualRouterWindowsRouteState.takeoverRouteDefs = nil
	probeVirtualRouterWindowsRouteState.mu.Unlock()
}

func assertProbeVirtualRouterWindowsRouteDef(t *testing.T, routes []probeLocalWindowsRouteDef, want probeLocalWindowsRouteDef) {
	t.Helper()
	for _, routeDef := range routes {
		if strings.TrimSpace(want.Prefix) != "" && strings.TrimSpace(routeDef.Prefix) != strings.TrimSpace(want.Prefix) {
			continue
		}
		if strings.TrimSpace(want.Mask) != "" && strings.TrimSpace(routeDef.Mask) != strings.TrimSpace(want.Mask) {
			continue
		}
		if strings.TrimSpace(want.Gateway) != "" && strings.TrimSpace(routeDef.Gateway) != strings.TrimSpace(want.Gateway) {
			continue
		}
		if want.InterfaceLUID != 0 && routeDef.InterfaceLUID != want.InterfaceLUID {
			continue
		}
		if want.IfIndex != 0 && routeDef.IfIndex != want.IfIndex {
			continue
		}
		return
	}
	t.Fatalf("missing route %+v in %+v", want, routes)
}
