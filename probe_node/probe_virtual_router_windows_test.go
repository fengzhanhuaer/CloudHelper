//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestProbeLocalTUNRouteTargetIPv4PrefixLengthFollowsLocalFeatures(t *testing.T) {
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)

	enableProbeVirtualRouterLocalSettingsForTest(false, false)
	if got := probeLocalTUNRouteTargetIPv4PrefixLength(); got != 32 {
		t.Fatalf("base-only route target prefix=%d want 32", got)
	}

	enableProbeVirtualRouterLocalSettingsForTest(true, false)
	if got := probeLocalTUNRouteTargetIPv4PrefixLength(); got != probeLocalTUNRouteIPv4PrefixLen {
		t.Fatalf("local router route target prefix=%d want %d", got, probeLocalTUNRouteIPv4PrefixLen)
	}

	enableProbeVirtualRouterLocalSettingsForTest(false, true)
	if got := probeLocalTUNRouteTargetIPv4PrefixLength(); got != probeLocalTUNRouteIPv4PrefixLen {
		t.Fatalf("local dns route target prefix=%d want %d", got, probeLocalTUNRouteIPv4PrefixLen)
	}
}

func TestEnsureProbeVirtualRouterPlatformInterfaceIPWindowsAppliesTakeoverAndLocalBypassRoutes(t *testing.T) {
	if !probeProductVRouteTakeoverEnabled() {
		t.Skip("active product deliberately disables host takeover routes")
	}
	resetProbeLocalTUNInstallWindowsHooksForTest()
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	resetProbeRouteWindowsStateForTest()
	resetProbeVirtualRouterWindowsRouteStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(true, false)
	t.Cleanup(func() {
		resetProbeLocalTUNInstallWindowsHooksForTest()
		resetProbeVirtualRouterTUNDataPlaneHooksForTest()
		resetProbeRouteWindowsStateForTest()
		resetProbeVirtualRouterWindowsRouteStateForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	probeVirtualRouterTUNDataPlaneState.dataPlane = &fakeProbeVirtualRouterTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true}}
	probeVirtualRouterTUNDataPlaneState.interfaceLUID = 77
	probeVirtualRouterTUNDataPlaneState.ifIndex = 40
	probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterTUNDataPlaneState.mu.Lock()
		probeVirtualRouterTUNDataPlaneState.dataPlane = nil
		probeVirtualRouterTUNDataPlaneState.interfaceLUID = 0
		probeVirtualRouterTUNDataPlaneState.ifIndex = 0
		probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	})

	var routeCreateCalls []probeRouteWindowsRouteDef
	oldCreateRoute := probeLocalCreateWindowsRouteEntry
	oldResolvePrimaryEgress := probeLocalResolveWindowsPrimaryEgressRoute
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		routeCreateCalls = append(routeCreateCalls, routeDef)
		return true, nil
	}
	probeLocalResolveWindowsPrimaryEgressRoute = func(excludedIfIndex int) (probeRouteWindowsDirectRouteTarget, error) {
		if excludedIfIndex != 40 {
			t.Fatalf("excludedIfIndex=%d want 40", excludedIfIndex)
		}
		return probeRouteWindowsDirectRouteTarget{InterfaceIndex: 13, NextHop: "192.168.50.1"}, nil
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
	assertProbeVirtualRouterWindowsRouteDef(t, routeCreateCalls, probeRouteWindowsRouteDef{
		Prefix:        "198.18.0.0",
		Mask:          "255.254.0.0",
		Gateway:       probeLocalTUNRouteGatewayIPv4,
		InterfaceLUID: 77,
		IfIndex:       40,
	})
	assertProbeVirtualRouterWindowsRouteDef(t, routeCreateCalls, probeRouteWindowsRouteDef{
		Prefix:        probeVirtualRouterWindowsRouteSplitPrefixA,
		Mask:          probeVirtualRouterWindowsRouteSplitMaskA,
		Gateway:       probeLocalTUNRouteGatewayIPv4,
		InterfaceLUID: 77,
		IfIndex:       40,
	})
	assertProbeVirtualRouterWindowsRouteDef(t, routeCreateCalls, probeRouteWindowsRouteDef{
		Prefix:        probeVirtualRouterWindowsRouteSplitPrefixB,
		Mask:          probeVirtualRouterWindowsRouteSplitMaskB,
		Gateway:       probeLocalTUNRouteGatewayIPv4,
		InterfaceLUID: 77,
		IfIndex:       40,
	})
	for _, prefix := range []string{"10.0.0.0", "172.16.0.0", "192.168.0.0"} {
		assertProbeVirtualRouterWindowsRouteDef(t, routeCreateCalls, probeRouteWindowsRouteDef{
			Prefix:  prefix,
			Gateway: "192.168.50.1",
			IfIndex: 13,
		})
	}
}

func TestEnsureProbeVirtualRouterPlatformInterfaceIPWindowsBaseOnlyUsesHostPrefixAndCleansFakeRoute(t *testing.T) {
	resetProbeLocalTUNInstallWindowsHooksForTest()
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	resetProbeRouteWindowsStateForTest()
	resetProbeVirtualRouterWindowsRouteStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(false, false)
	t.Cleanup(func() {
		resetProbeLocalTUNInstallWindowsHooksForTest()
		resetProbeVirtualRouterTUNDataPlaneHooksForTest()
		resetProbeRouteWindowsStateForTest()
		resetProbeVirtualRouterWindowsRouteStateForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	probeVirtualRouterWindowsRouteState.mu.Lock()
	probeVirtualRouterWindowsRouteState.fakeRouteDef = probeRouteWindowsRouteDef{
		Prefix:        "198.18.0.0",
		Mask:          "255.254.0.0",
		Gateway:       probeLocalTUNRouteGatewayIPv4,
		InterfaceLUID: 77,
		IfIndex:       40,
	}
	probeVirtualRouterWindowsRouteState.fakeApplied = true
	probeVirtualRouterWindowsRouteState.takeoverRouteDefs = []probeRouteWindowsRouteDef{{
		Prefix:  probeVirtualRouterWindowsRouteSplitPrefixA,
		Mask:    probeVirtualRouterWindowsRouteSplitMaskA,
		Gateway: probeLocalTUNRouteGatewayIPv4,
		IfIndex: 40,
	}}
	probeVirtualRouterWindowsRouteState.mu.Unlock()
	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	probeVirtualRouterTUNDataPlaneState.dataPlane = &fakeProbeVirtualRouterTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true}}
	probeVirtualRouterTUNDataPlaneState.interfaceLUID = 77
	probeVirtualRouterTUNDataPlaneState.ifIndex = 40
	probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterTUNDataPlaneState.mu.Lock()
		probeVirtualRouterTUNDataPlaneState.dataPlane = nil
		probeVirtualRouterTUNDataPlaneState.interfaceLUID = 0
		probeVirtualRouterTUNDataPlaneState.ifIndex = 0
		probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	})

	var created []probeRouteWindowsRouteDef
	var deleted []probeRouteWindowsRouteDef
	oldCreateRoute := probeLocalCreateWindowsRouteEntry
	oldDeleteRoute := probeLocalDeleteWindowsRouteEntry
	oldUpsertIPv4ByLUID := probeLocalUpsertWindowsInterfaceIPv4ByLUID
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		created = append(created, routeDef)
		return true, nil
	}
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}
	probeLocalUpsertWindowsInterfaceIPv4ByLUID = func(luid uint64, ifIndex int, ip string, prefixLength int) error {
		if prefixLength != 32 {
			t.Fatalf("base-only prefixLength=%d want 32", prefixLength)
		}
		return nil
	}
	t.Cleanup(func() {
		probeLocalCreateWindowsRouteEntry = oldCreateRoute
		probeLocalDeleteWindowsRouteEntry = oldDeleteRoute
		probeLocalUpsertWindowsInterfaceIPv4ByLUID = oldUpsertIPv4ByLUID
	})

	if err := ensureProbeVirtualRouterPlatformInterfaceIP("198.18.0.21"); err != nil {
		t.Fatalf("ensure disabled virtual router route failed: %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("created routes=%+v, want no fake ip or takeover route", created)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted routes=%+v, want fake ip and takeover routes", deleted)
	}
	assertProbeVirtualRouterWindowsRouteDef(t, deleted, probeRouteWindowsRouteDef{Prefix: "198.18.0.0", Mask: "255.254.0.0", Gateway: probeLocalTUNRouteGatewayIPv4, IfIndex: 40})
	assertProbeVirtualRouterWindowsRouteDef(t, deleted, probeRouteWindowsRouteDef{Prefix: probeVirtualRouterWindowsRouteSplitPrefixA, IfIndex: 40})
}

func TestCleanupProbeVirtualRouterPlatformRoutesWindowsDeletesFakeIPRoute(t *testing.T) {
	resetProbeRouteWindowsStateForTest()
	resetProbeVirtualRouterWindowsRouteStateForTest()
	t.Cleanup(func() {
		resetProbeRouteWindowsStateForTest()
		resetProbeVirtualRouterWindowsRouteStateForTest()
	})

	routeDef := probeRouteWindowsRouteDef{
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

	var deleted []probeRouteWindowsRouteDef
	oldDeleteRoute := probeLocalDeleteWindowsRouteEntry
	probeLocalDeleteWindowsRouteEntry = func(got probeRouteWindowsRouteDef) error {
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
	probeVirtualRouterWindowsRouteState.fakeRouteDef = probeRouteWindowsRouteDef{}
	probeVirtualRouterWindowsRouteState.fakeApplied = false
	probeVirtualRouterWindowsRouteState.takeoverRouteDefs = nil
	probeVirtualRouterWindowsRouteState.mu.Unlock()
}

func assertProbeVirtualRouterWindowsRouteDef(t *testing.T, routes []probeRouteWindowsRouteDef, want probeRouteWindowsRouteDef) {
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
