//go:build windows

package main

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

func TestProbeLocalTUNRouteTargetIPv4PrefixLengthAlwaysCoversVirtualNetwork(t *testing.T) {
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)

	enableProbeVirtualRouterLocalSettingsForTest(false, false)
	if got := probeLocalTUNRouteTargetIPv4PrefixLength(); got != probeLocalTUNRouteIPv4PrefixLen {
		t.Fatalf("base-only route target prefix=%d want %d", got, probeLocalTUNRouteIPv4PrefixLen)
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
	dnsSetCalls := 0
	oldFindAdapterByLUID := probeLocalFindWindowsAdapterByLUID
	oldSetInterfaceDNS := probeLocalSetWindowsInterfaceDNS
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
	probeLocalFindWindowsAdapterByLUID = func(luid uint64) (windowsAdapterInfo, error) {
		if luid != 77 {
			t.Fatalf("dns adapter luid=%d want 77", luid)
		}
		return windowsAdapterInfo{InterfaceIndex: 40, AdapterGUID: "{00000000-0000-0000-0000-000000000077}"}, nil
	}
	probeLocalSetWindowsInterfaceDNS = func(guid string, servers []string) error {
		dnsSetCalls++
		if guid != "{00000000-0000-0000-0000-000000000077}" || len(servers) != 1 || servers[0] != probeLocalTUNInterfaceIPv4 {
			t.Fatalf("unexpected tun dns reconcile guid=%q servers=%v", guid, servers)
		}
		return nil
	}
	probeLocalUpsertWindowsInterfaceIPv4 = func(ifIndex int, ip string, prefixLength int) error {
		t.Fatalf("expected LUID path, got ifIndex=%d ip=%s prefix=%d", ifIndex, ip, prefixLength)
		return nil
	}
	t.Cleanup(func() {
		probeLocalFindWindowsAdapterByLUID = oldFindAdapterByLUID
		probeLocalSetWindowsInterfaceDNS = oldSetInterfaceDNS
	})

	if err := ensureProbeVirtualRouterPlatformInterfaceIP("198.18.0.21"); err != nil {
		t.Fatalf("ensureProbeVirtualRouterPlatformInterfaceIP returned error: %v", err)
	}
	if upsertCalls != 1 {
		t.Fatalf("upsertCalls=%d want 1", upsertCalls)
	}
	if dnsSetCalls != 1 {
		t.Fatalf("dnsSetCalls=%d want 1", dnsSetCalls)
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

func TestEnsureProbeVirtualRouterPlatformInterfaceIPWindowsBaseOnlyKeepsVirtualRouteAndCleansTakeover(t *testing.T) {
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
	oldFindAdapterByLUID := probeLocalFindWindowsAdapterByLUID
	oldSetInterfaceDNS := probeLocalSetWindowsInterfaceDNS
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		created = append(created, routeDef)
		return true, nil
	}
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}
	probeLocalUpsertWindowsInterfaceIPv4ByLUID = func(luid uint64, ifIndex int, ip string, prefixLength int) error {
		if prefixLength != probeLocalTUNRouteIPv4PrefixLen {
			t.Fatalf("base-only prefixLength=%d want %d", prefixLength, probeLocalTUNRouteIPv4PrefixLen)
		}
		return nil
	}
	dnsSetCalls := 0
	probeLocalFindWindowsAdapterByLUID = func(luid uint64) (windowsAdapterInfo, error) {
		return windowsAdapterInfo{
			InterfaceIndex: 40,
			AdapterGUID:    "{00000000-0000-0000-0000-000000000077}",
		}, nil
	}
	probeLocalSetWindowsInterfaceDNS = func(guid string, servers []string) error {
		dnsSetCalls++
		if guid != "{00000000-0000-0000-0000-000000000077}" || len(servers) != 1 || servers[0] != probeLocalTUNInterfaceIPv4 {
			t.Fatalf("unexpected fixed tun dns guid=%q servers=%v", guid, servers)
		}
		return nil
	}
	t.Cleanup(func() {
		probeLocalCreateWindowsRouteEntry = oldCreateRoute
		probeLocalDeleteWindowsRouteEntry = oldDeleteRoute
		probeLocalUpsertWindowsInterfaceIPv4ByLUID = oldUpsertIPv4ByLUID
		probeLocalFindWindowsAdapterByLUID = oldFindAdapterByLUID
		probeLocalSetWindowsInterfaceDNS = oldSetInterfaceDNS
	})

	if err := ensureProbeVirtualRouterPlatformInterfaceIP("198.18.0.21"); err != nil {
		t.Fatalf("ensure disabled virtual router route failed: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created routes=%+v, want virtual network route only", created)
	}
	if len(deleted) != 1 {
		t.Fatalf("deleted routes=%+v, want takeover route only", deleted)
	}
	if dnsSetCalls != 1 {
		t.Fatalf("dnsSetCalls=%d want 1", dnsSetCalls)
	}
	assertProbeVirtualRouterWindowsRouteDef(t, created, probeRouteWindowsRouteDef{Prefix: "198.18.0.0", Mask: "255.254.0.0", Gateway: probeLocalTUNRouteGatewayIPv4, IfIndex: 40})
	assertProbeVirtualRouterWindowsRouteDef(t, deleted, probeRouteWindowsRouteDef{Prefix: probeVirtualRouterWindowsRouteSplitPrefixA, IfIndex: 40})
}

func TestBuildProbeVirtualRouterWindowsPublishedRouteDefsDoesNotAddProbeHostRoutes(t *testing.T) {
	probeVirtualRouterState.mu.Lock()
	previousConfig := probeVirtualRouterState.config
	probeVirtualRouterState.config = probeVirtualRouterConfig{ProbeIPs: []probeVirtualRouterProbeIP{
		{NodeID: "7", IP: "198.18.0.7"},
		{NodeID: "17", IP: "198.18.0.17"},
	}}
	probeVirtualRouterState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = previousConfig
		probeVirtualRouterState.mu.Unlock()
	})

	routes := buildProbeVirtualRouterWindowsPublishedRouteDefs(77, 40)
	if len(routes) != 0 {
		t.Fatalf("routes=%+v, probe nodes are covered by the shared fake IP route", routes)
	}
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

func TestEnsureProbeVirtualRouterWindowsPublishedRoutesKeepsPhysicalSubnetAndCleansStaleTUNRoute(t *testing.T) {
	resetProbeVirtualRouterWindowsRouteStateForTest()
	probeVirtualRouterState.mu.Lock()
	previousConfig := probeVirtualRouterState.config
	probeVirtualRouterState.config = probeVirtualRouterConfig{RouteRules: []probeVirtualRouterRouteRule{
		{ID: "linux-router-22-local", Name: "Local subnet", Action: "probe_exit", ExitNodeID: "22", Entries: []string{"cidr:172.18.52.0/22"}},
		{ID: "linux-router-23-remote", Name: "Remote subnet", Action: "probe_exit", ExitNodeID: "23", Entries: []string{"cidr:192.168.50.0/24"}},
	}}
	probeVirtualRouterState.mu.Unlock()
	oldListRoutes := probeLocalListWindowsRouteEntries
	oldCreateRoute := probeLocalCreateWindowsRouteEntry
	oldDeleteRoute := probeLocalDeleteWindowsRouteEntry
	t.Cleanup(func() {
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = previousConfig
		probeVirtualRouterState.mu.Unlock()
		probeLocalListWindowsRouteEntries = oldListRoutes
		probeLocalCreateWindowsRouteEntry = oldCreateRoute
		probeLocalDeleteWindowsRouteEntry = oldDeleteRoute
		resetProbeVirtualRouterWindowsRouteStateForTest()
	})

	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{
			{Prefix: "172.18.52.0", PrefixLength: 22, NextHop: "0.0.0.0", IfIndex: 22},
			{Prefix: "192.168.0.0", PrefixLength: 16, NextHop: "172.18.55.254", IfIndex: 22, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "172.18.52.0", PrefixLength: 22, NextHop: probeLocalTUNRouteGatewayIPv4, IfIndex: 40, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "192.168.50.42", PrefixLength: 32, NextHop: "0.0.0.0", IfIndex: 22, Metric: 1, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "192.168.50.43", PrefixLength: 32, NextHop: "192.168.1.1", IfIndex: 22, Metric: 1, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "192.168.50.44", PrefixLength: 32, NextHop: "0.0.0.0", IfIndex: 22, Metric: 256, Protocol: 2},
		}, nil
	}
	var created []probeRouteWindowsRouteDef
	var deleted []probeRouteWindowsRouteDef
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		created = append(created, routeDef)
		return true, nil
	}
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}

	if err := ensureProbeVirtualRouterWindowsPublishedRoutes(77, 40); err != nil {
		t.Fatalf("ensure published routes returned error: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted=%+v, want stale local-subnet TUN route and stale remote on-link host route", deleted)
	}
	assertProbeVirtualRouterWindowsRouteDef(t, deleted, probeRouteWindowsRouteDef{
		Prefix: "172.18.52.0", Mask: "255.255.252.0", Gateway: probeLocalTUNRouteGatewayIPv4, IfIndex: 40,
	})
	assertProbeVirtualRouterWindowsRouteDef(t, deleted, probeRouteWindowsRouteDef{
		Prefix: "192.168.50.42", Mask: probeRouteWindowsHostRouteMask, Gateway: "0.0.0.0", IfIndex: 22,
	})
	if len(created) != 1 {
		t.Fatalf("created=%+v, want remote published route only", created)
	}
	assertProbeVirtualRouterWindowsRouteDef(t, created, probeRouteWindowsRouteDef{
		Prefix: "192.168.50.0", Mask: "255.255.255.0", Gateway: probeLocalTUNRouteGatewayIPv4, IfIndex: 40,
	})
	probeVirtualRouterWindowsRouteState.mu.Lock()
	applied := append([]probeRouteWindowsRouteDef(nil), probeVirtualRouterWindowsRouteState.publishedRouteDefs...)
	probeVirtualRouterWindowsRouteState.mu.Unlock()
	if len(applied) != 1 {
		t.Fatalf("applied=%+v, want remote published route only", applied)
	}
	assertProbeVirtualRouterWindowsRouteDef(t, applied, probeRouteWindowsRouteDef{Prefix: "192.168.50.0", IfIndex: 40})
}

func TestProbeVirtualRouterWindowsRouteEntryIsManagedLocalBypass(t *testing.T) {
	tests := []struct {
		name  string
		entry probeLocalWindowsRouteEntry
		want  bool
	}{
		{
			name:  "managed private bypass",
			entry: probeLocalWindowsRouteEntry{Prefix: "192.168.0.0", PrefixLength: 16, NextHop: "172.18.55.254", IfIndex: 22, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			want:  true,
		},
		{
			name:  "physical on-link route",
			entry: probeLocalWindowsRouteEntry{Prefix: "192.168.0.0", PrefixLength: 16, NextHop: "0.0.0.0", IfIndex: 22, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
		},
		{
			name:  "user route with another metric",
			entry: probeLocalWindowsRouteEntry{Prefix: "192.168.0.0", PrefixLength: 16, NextHop: "172.18.55.254", IfIndex: 22, Metric: 9, Protocol: probeRouteWindowsProtocolNetMgmt},
		},
		{
			name:  "specific private route",
			entry: probeLocalWindowsRouteEntry{Prefix: "192.168.50.0", PrefixLength: 24, NextHop: "172.18.55.254", IfIndex: 22, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := netip.MustParsePrefix(fmt.Sprintf("%s/%d", tt.entry.Prefix, tt.entry.PrefixLength))
			if got := probeVirtualRouterWindowsRouteEntryIsManagedLocalBypass(tt.entry, prefix); got != tt.want {
				t.Fatalf("managed local bypass=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnsureProbeVirtualRouterWindowsPublishedRoutesCleansRestartStaleRouteWhenUnselected(t *testing.T) {
	resetProbeVirtualRouterWindowsRouteStateForTest()
	probeVirtualRouterState.mu.Lock()
	previousConfig := probeVirtualRouterState.config
	probeVirtualRouterState.config = probeVirtualRouterConfig{FakeIPCIDR: "198.18.0.0/15"}
	probeVirtualRouterState.mu.Unlock()
	oldListRoutes := probeLocalListWindowsRouteEntries
	oldDeleteRoute := probeLocalDeleteWindowsRouteEntry
	t.Cleanup(func() {
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = previousConfig
		probeVirtualRouterState.mu.Unlock()
		probeLocalListWindowsRouteEntries = oldListRoutes
		probeLocalDeleteWindowsRouteEntry = oldDeleteRoute
		resetProbeVirtualRouterWindowsRouteStateForTest()
	})

	probeLocalListWindowsRouteEntries = func() ([]probeLocalWindowsRouteEntry, error) {
		return []probeLocalWindowsRouteEntry{
			{Prefix: "172.18.52.0", PrefixLength: 22, NextHop: probeLocalTUNRouteGatewayIPv4, IfIndex: 40, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "198.18.0.0", PrefixLength: 15, NextHop: probeLocalTUNRouteGatewayIPv4, IfIndex: 40, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
			{Prefix: "0.0.0.0", PrefixLength: 1, NextHop: probeLocalTUNRouteGatewayIPv4, IfIndex: 40, Metric: probeRouteWindowsRouteMetric, Protocol: probeRouteWindowsProtocolNetMgmt},
		}, nil
	}
	var deleted []probeRouteWindowsRouteDef
	probeLocalDeleteWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) error {
		deleted = append(deleted, routeDef)
		return nil
	}

	if err := ensureProbeVirtualRouterWindowsPublishedRoutes(77, 40); err != nil {
		t.Fatalf("ensure published routes returned error: %v", err)
	}
	if len(deleted) != 1 {
		t.Fatalf("deleted=%+v, want only stale published route", deleted)
	}
	assertProbeVirtualRouterWindowsRouteDef(t, deleted, probeRouteWindowsRouteDef{
		Prefix: "172.18.52.0", Mask: "255.255.252.0", Gateway: probeLocalTUNRouteGatewayIPv4, IfIndex: 40,
	})
}

func resetProbeVirtualRouterWindowsRouteStateForTest() {
	probeVirtualRouterWindowsRouteState.mu.Lock()
	probeVirtualRouterWindowsRouteState.fakeRouteDef = probeRouteWindowsRouteDef{}
	probeVirtualRouterWindowsRouteState.fakeApplied = false
	probeVirtualRouterWindowsRouteState.takeoverRouteDefs = nil
	probeVirtualRouterWindowsRouteState.publishedRouteDefs = nil
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
