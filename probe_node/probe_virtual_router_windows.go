//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
)

const (
	probeVirtualRouterWindowsRouteSplitPrefixA = "0.0.0.0"
	probeVirtualRouterWindowsRouteSplitMaskA   = "128.0.0.0"
	probeVirtualRouterWindowsRouteSplitPrefixB = "128.0.0.0"
	probeVirtualRouterWindowsRouteSplitMaskB   = "128.0.0.0"
)

var probeVirtualRouterWindowsRouteState = struct {
	mu                 sync.Mutex
	fakeRouteDef       probeRouteWindowsRouteDef
	fakeApplied        bool
	takeoverRouteDefs  []probeRouteWindowsRouteDef
	publishedRouteDefs []probeRouteWindowsRouteDef
}{}

func ensureProbeVirtualRouterPlatformInterfaceIP(ip string) error {
	cleanIP := strings.TrimSpace(ip)
	if cleanIP == "" {
		return nil
	}
	if !probeVirtualRouterTUNDataPlaneRunning() {
		if err := startProbeVirtualRouterTUNDataPlane(); err != nil {
			return err
		}
	}
	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	interfaceLUID := probeVirtualRouterTUNDataPlaneState.interfaceLUID
	ifIndex := probeVirtualRouterTUNDataPlaneState.ifIndex
	probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	prefixLength := probeLocalTUNRouteIPv4PrefixLen
	if !probeVirtualRouterWindowsFakeIPRouteRequired() {
		prefixLength = 32
	}
	if interfaceLUID > 0 {
		if err := probeLocalUpsertWindowsInterfaceIPv4ByLUID(interfaceLUID, ifIndex, cleanIP, prefixLength); err != nil {
			return err
		}
		return ensureProbeVirtualRouterWindowsRoutes(interfaceLUID, ifIndex)
	}
	if ifIndex > 0 {
		if err := probeLocalUpsertWindowsInterfaceIPv4(ifIndex, cleanIP, prefixLength); err != nil {
			return err
		}
		return ensureProbeVirtualRouterWindowsRoutes(0, ifIndex)
	}
	return nil
}

func cleanupProbeVirtualRouterPlatformRoutes() error {
	return cleanupProbeVirtualRouterWindowsRoutes()
}

func cleanupProbeVirtualRouterPlatformTakeoverRoutes() error {
	return cleanupProbeVirtualRouterWindowsTakeoverRoutes()
}

func ensureProbeVirtualRouterWindowsRoutes(interfaceLUID uint64, ifIndex int) error {
	if ifIndex <= 0 {
		return nil
	}
	if probeVirtualRouterWindowsFakeIPRouteRequired() {
		if err := ensureProbeVirtualRouterWindowsFakeIPRoute(interfaceLUID, ifIndex); err != nil {
			return err
		}
	} else if err := cleanupProbeVirtualRouterWindowsFakeIPRoute(interfaceLUID, ifIndex); err != nil {
		return err
	}
	if err := ensureProbeVirtualRouterWindowsPublishedRoutes(interfaceLUID, ifIndex); err != nil {
		return err
	}
	if !probeProductVRouteTakeoverEnabled() || !probeVirtualRouterLocalEntryEnabled() {
		return cleanupProbeVirtualRouterWindowsTakeoverRoutes()
	}
	if err := ensureProbeVirtualRouterWindowsTakeoverRoutes(interfaceLUID, ifIndex); err != nil {
		return err
	}
	cleanupProbeRouteDirectBypassForVirtualRouterRules(currentProbeVirtualRouterConfig())
	return nil
}

func probeVirtualRouterWindowsFakeIPRouteRequired() bool {
	return probeVirtualRouterLocalEntryEnabled() || probeVirtualRouterLocalDNSEnabled()
}

func ensureProbeVirtualRouterWindowsPublishedRoutes(interfaceLUID uint64, ifIndex int) error {
	routeDefs := buildProbeVirtualRouterWindowsPublishedRouteDefs(interfaceLUID, ifIndex)
	probeVirtualRouterWindowsRouteState.mu.Lock()
	oldRouteDefs := append([]probeRouteWindowsRouteDef(nil), probeVirtualRouterWindowsRouteState.publishedRouteDefs...)
	if probeVirtualRouterWindowsRouteDefsEqual(oldRouteDefs, routeDefs) {
		probeVirtualRouterWindowsRouteState.mu.Unlock()
		return nil
	}
	probeVirtualRouterWindowsRouteState.mu.Unlock()
	var allErr error
	for _, oldRouteDef := range oldRouteDefs {
		if err := deleteProbeVirtualRouterWindowsRoute(oldRouteDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	for _, routeDef := range routeDefs {
		if err := rejectProbeVirtualRouterWindowsPublishedRouteCollision(routeDef, ifIndex); err != nil {
			allErr = errors.Join(allErr, err)
			continue
		}
		if _, err := ensureProbeVirtualRouterWindowsRoute(routeDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	if allErr != nil {
		return allErr
	}
	probeVirtualRouterWindowsRouteState.mu.Lock()
	probeVirtualRouterWindowsRouteState.publishedRouteDefs = append([]probeRouteWindowsRouteDef(nil), routeDefs...)
	probeVirtualRouterWindowsRouteState.mu.Unlock()
	return nil
}

func buildProbeVirtualRouterWindowsPublishedRouteDefs(interfaceLUID uint64, ifIndex int) []probeRouteWindowsRouteDef {
	config := currentProbeVirtualRouterConfig()
	seen := make(map[string]struct{})
	out := make([]probeRouteWindowsRouteDef, 0)
	for _, rule := range config.RouteRules {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(rule.ID)), "linux-router-") || sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) != "probe_exit" {
			continue
		}
		for _, entry := range rule.Entries {
			key, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
			if !ok || (strings.ToLower(strings.TrimSpace(key)) != "cidr" && strings.ToLower(strings.TrimSpace(key)) != "ip_cidr") {
				continue
			}
			prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
			if err != nil || !prefix.Addr().Is4() {
				continue
			}
			prefix = prefix.Masked()
			if _, exists := seen[prefix.String()]; exists {
				continue
			}
			seen[prefix.String()] = struct{}{}
			mask := net.IP(net.CIDRMask(prefix.Bits(), 32)).String()
			out = append(out, probeRouteWindowsRouteDef{Prefix: prefix.Addr().String(), Mask: mask, Gateway: probeLocalTUNRouteGatewayIPv4, InterfaceLUID: interfaceLUID, IfIndex: ifIndex})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix+out[i].Mask < out[j].Prefix+out[j].Mask })
	return out
}

func rejectProbeVirtualRouterWindowsPublishedRouteCollision(routeDef probeRouteWindowsRouteDef, tunIfIndex int) error {
	prefixLength, err := probeLocalIPv4PrefixLengthFromMask(routeDef.Mask)
	if err != nil {
		return err
	}
	candidate, err := netip.ParsePrefix(fmt.Sprintf("%s/%d", routeDef.Prefix, prefixLength))
	if err != nil {
		return err
	}
	entries, err := probeLocalListWindowsRouteEntries()
	if err != nil {
		return fmt.Errorf("inspect windows routes for published subnet: %w", err)
	}
	for _, item := range entries {
		if item.PrefixLength == 0 || item.IfIndex == tunIfIndex {
			continue
		}
		existing, parseErr := netip.ParsePrefix(fmt.Sprintf("%s/%d", item.Prefix, item.PrefixLength))
		if parseErr != nil {
			continue
		}
		if candidate.Contains(existing.Addr()) || existing.Contains(candidate.Addr()) {
			return fmt.Errorf("published subnet %s overlaps local route %s", candidate.String(), existing.String())
		}
	}
	return nil
}

func ensureProbeVirtualRouterWindowsFakeIPRoute(interfaceLUID uint64, ifIndex int) error {
	prefix, mask := probeVirtualRouterWindowsRoutePrefixAndMask(currentProbeVirtualRouterFakeIPCIDR())
	routeDef := probeRouteWindowsRouteDef{
		Prefix:        prefix,
		Mask:          mask,
		Gateway:       probeLocalTUNRouteGatewayIPv4,
		InterfaceLUID: interfaceLUID,
		IfIndex:       ifIndex,
	}

	probeVirtualRouterWindowsRouteState.mu.Lock()
	oldRouteDef := probeVirtualRouterWindowsRouteState.fakeRouteDef
	needDeleteOld := probeVirtualRouterWindowsRouteState.fakeApplied && !probeVirtualRouterWindowsRouteDefEqual(oldRouteDef, routeDef)
	probeVirtualRouterWindowsRouteState.mu.Unlock()

	if needDeleteOld {
		if err := deleteProbeVirtualRouterWindowsRoute(oldRouteDef); err != nil {
			return fmt.Errorf("cleanup old windows virtual router fake ip route failed: %w", err)
		}
	}
	if _, err := ensureProbeVirtualRouterWindowsRoute(routeDef); err != nil {
		return fmt.Errorf("ensure windows virtual router fake ip route failed: prefix=%s mask=%s if_index=%d: %w", routeDef.Prefix, routeDef.Mask, ifIndex, err)
	}

	probeVirtualRouterWindowsRouteState.mu.Lock()
	probeVirtualRouterWindowsRouteState.fakeRouteDef = routeDef
	probeVirtualRouterWindowsRouteState.fakeApplied = true
	probeVirtualRouterWindowsRouteState.mu.Unlock()
	return nil
}

func cleanupProbeVirtualRouterWindowsFakeIPRoute(interfaceLUID uint64, ifIndex int) error {
	prefix, mask := probeVirtualRouterWindowsRoutePrefixAndMask(currentProbeVirtualRouterFakeIPCIDR())
	expected := probeRouteWindowsRouteDef{
		Prefix:        prefix,
		Mask:          mask,
		Gateway:       probeLocalTUNRouteGatewayIPv4,
		InterfaceLUID: interfaceLUID,
		IfIndex:       ifIndex,
	}

	probeVirtualRouterWindowsRouteState.mu.Lock()
	oldRouteDef := probeVirtualRouterWindowsRouteState.fakeRouteDef
	oldApplied := probeVirtualRouterWindowsRouteState.fakeApplied
	probeVirtualRouterWindowsRouteState.fakeRouteDef = probeRouteWindowsRouteDef{}
	probeVirtualRouterWindowsRouteState.fakeApplied = false
	probeVirtualRouterWindowsRouteState.mu.Unlock()

	var allErr error
	if oldApplied {
		allErr = errors.Join(allErr, deleteProbeVirtualRouterWindowsRoute(oldRouteDef))
	}
	if !oldApplied || !probeVirtualRouterWindowsRouteDefEqual(oldRouteDef, expected) {
		allErr = errors.Join(allErr, deleteProbeVirtualRouterWindowsRoute(expected))
	}
	return allErr
}

func ensureProbeVirtualRouterWindowsTakeoverRoutes(interfaceLUID uint64, ifIndex int) error {
	if ifIndex <= 0 {
		return nil
	}
	routeDefs, err := buildProbeVirtualRouterWindowsTakeoverRouteDefs(interfaceLUID, ifIndex)
	if err != nil {
		return err
	}
	probeVirtualRouterWindowsRouteState.mu.Lock()
	oldRouteDefs := append([]probeRouteWindowsRouteDef(nil), probeVirtualRouterWindowsRouteState.takeoverRouteDefs...)
	if probeVirtualRouterWindowsRouteDefsEqual(oldRouteDefs, routeDefs) {
		probeVirtualRouterWindowsRouteState.mu.Unlock()
		return nil
	}
	probeVirtualRouterWindowsRouteState.mu.Unlock()

	var allErr error
	for _, oldRouteDef := range oldRouteDefs {
		if err := deleteProbeVirtualRouterWindowsRoute(oldRouteDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	for _, routeDef := range routeDefs {
		if _, err := ensureProbeVirtualRouterWindowsRoute(routeDef); err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("ensure windows virtual router takeover route failed: prefix=%s mask=%s if_index=%d: %w", routeDef.Prefix, routeDef.Mask, routeDef.IfIndex, err))
		}
	}
	if allErr != nil {
		return allErr
	}
	probeVirtualRouterWindowsRouteState.mu.Lock()
	probeVirtualRouterWindowsRouteState.takeoverRouteDefs = append([]probeRouteWindowsRouteDef(nil), routeDefs...)
	probeVirtualRouterWindowsRouteState.mu.Unlock()
	return nil
}

func buildProbeVirtualRouterWindowsTakeoverRouteDefs(interfaceLUID uint64, ifIndex int) ([]probeRouteWindowsRouteDef, error) {
	routeDefs := []probeRouteWindowsRouteDef{
		{Prefix: probeVirtualRouterWindowsRouteSplitPrefixA, Mask: probeVirtualRouterWindowsRouteSplitMaskA, Gateway: probeLocalTUNRouteGatewayIPv4, InterfaceLUID: interfaceLUID, IfIndex: ifIndex},
		{Prefix: probeVirtualRouterWindowsRouteSplitPrefixB, Mask: probeVirtualRouterWindowsRouteSplitMaskB, Gateway: probeLocalTUNRouteGatewayIPv4, InterfaceLUID: interfaceLUID, IfIndex: ifIndex},
	}
	bypassTarget, err := probeLocalResolveWindowsPrimaryEgressRoute(ifIndex)
	if err != nil {
		return nil, fmt.Errorf("resolve windows virtual router local bypass route failed: %w", err)
	}
	routeDefs = append(routeDefs, probeVirtualRouterWindowsLocalBypassRouteDefs(bypassTarget)...)
	return dedupeProbeVirtualRouterWindowsRouteDefs(routeDefs), nil
}

func cleanupProbeVirtualRouterWindowsRoutes() error {
	probeVirtualRouterWindowsRouteState.mu.Lock()
	fakeRouteDef := probeVirtualRouterWindowsRouteState.fakeRouteDef
	fakeApplied := probeVirtualRouterWindowsRouteState.fakeApplied
	takeoverRouteDefs := append([]probeRouteWindowsRouteDef(nil), probeVirtualRouterWindowsRouteState.takeoverRouteDefs...)
	publishedRouteDefs := append([]probeRouteWindowsRouteDef(nil), probeVirtualRouterWindowsRouteState.publishedRouteDefs...)
	probeVirtualRouterWindowsRouteState.fakeRouteDef = probeRouteWindowsRouteDef{}
	probeVirtualRouterWindowsRouteState.fakeApplied = false
	probeVirtualRouterWindowsRouteState.takeoverRouteDefs = nil
	probeVirtualRouterWindowsRouteState.publishedRouteDefs = nil
	probeVirtualRouterWindowsRouteState.mu.Unlock()

	var allErr error
	for _, routeDef := range takeoverRouteDefs {
		if err := deleteProbeVirtualRouterWindowsRoute(routeDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	for _, routeDef := range publishedRouteDefs {
		if err := deleteProbeVirtualRouterWindowsRoute(routeDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	if fakeApplied {
		if err := deleteProbeVirtualRouterWindowsRoute(fakeRouteDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	return allErr
}

func cleanupProbeVirtualRouterWindowsTakeoverRoutes() error {
	probeVirtualRouterWindowsRouteState.mu.Lock()
	routeDefs := append([]probeRouteWindowsRouteDef(nil), probeVirtualRouterWindowsRouteState.takeoverRouteDefs...)
	probeVirtualRouterWindowsRouteState.takeoverRouteDefs = nil
	probeVirtualRouterWindowsRouteState.mu.Unlock()

	var allErr error
	for _, routeDef := range routeDefs {
		if err := deleteProbeVirtualRouterWindowsRoute(routeDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	return allErr
}

func deleteProbeVirtualRouterWindowsRoute(routeDef probeRouteWindowsRouteDef) error {
	if strings.TrimSpace(routeDef.Prefix) == "" || strings.TrimSpace(routeDef.Mask) == "" {
		return nil
	}
	return probeVirtualRouterDeleteWindowsRoute(routeDef)
}

func ensureProbeVirtualRouterWindowsRoute(routeDef probeRouteWindowsRouteDef) (bool, error) {
	return probeLocalCreateWindowsRouteEntry(routeDef)
}

func probeVirtualRouterDeleteWindowsRoute(routeDef probeRouteWindowsRouteDef) error {
	if strings.TrimSpace(routeDef.Gateway) == "" || (routeDef.InterfaceLUID == 0 && routeDef.IfIndex <= 0) {
		return nil
	}
	return probeLocalDeleteWindowsRouteEntry(routeDef)
}

func probeVirtualRouterWindowsRoutePrefixAndMask(cidr string) (string, string) {
	cleanCIDR := strings.TrimSpace(cidr)
	if cleanCIDR == "" {
		cleanCIDR = probeLocalFakeIPDefaultCIDR
	}
	ip, network, err := net.ParseCIDR(cleanCIDR)
	if err != nil || network == nil || ip == nil || ip.To4() == nil {
		ip, network, _ = net.ParseCIDR(probeLocalFakeIPDefaultCIDR)
	}
	if network == nil {
		return "198.18.0.0", "255.254.0.0"
	}
	prefix := network.IP.To4()
	if prefix == nil {
		return "198.18.0.0", "255.254.0.0"
	}
	mask := net.IP(network.Mask).String()
	if strings.TrimSpace(mask) == "" {
		mask = "255.254.0.0"
	}
	return prefix.String(), mask
}

func probeVirtualRouterWindowsLocalBypassRouteDefs(routeTarget probeRouteWindowsDirectRouteTarget) []probeRouteWindowsRouteDef {
	return []probeRouteWindowsRouteDef{
		{Prefix: "10.0.0.0", Mask: "255.0.0.0", Gateway: routeTarget.NextHop, InterfaceLUID: routeTarget.InterfaceLUID, IfIndex: routeTarget.InterfaceIndex},
		{Prefix: "172.16.0.0", Mask: "255.240.0.0", Gateway: routeTarget.NextHop, InterfaceLUID: routeTarget.InterfaceLUID, IfIndex: routeTarget.InterfaceIndex},
		{Prefix: "192.168.0.0", Mask: "255.255.0.0", Gateway: routeTarget.NextHop, InterfaceLUID: routeTarget.InterfaceLUID, IfIndex: routeTarget.InterfaceIndex},
	}
}

func dedupeProbeVirtualRouterWindowsRouteDefs(routeDefs []probeRouteWindowsRouteDef) []probeRouteWindowsRouteDef {
	out := make([]probeRouteWindowsRouteDef, 0, len(routeDefs))
	seen := make(map[string]struct{}, len(routeDefs))
	for _, routeDef := range routeDefs {
		key := strings.Join([]string{
			strings.TrimSpace(routeDef.Prefix),
			strings.TrimSpace(routeDef.Mask),
			strings.TrimSpace(routeDef.Gateway),
			fmt.Sprintf("%d", routeDef.InterfaceLUID),
			fmt.Sprintf("%d", routeDef.IfIndex),
		}, "|")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, routeDef)
	}
	return out
}

func probeVirtualRouterWindowsRouteDefsEqual(a, b []probeRouteWindowsRouteDef) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if !probeVirtualRouterWindowsRouteDefEqual(a[index], b[index]) {
			return false
		}
	}
	return true
}

func probeVirtualRouterWindowsRouteDefEqual(a, b probeRouteWindowsRouteDef) bool {
	return strings.EqualFold(strings.TrimSpace(a.Prefix), strings.TrimSpace(b.Prefix)) &&
		strings.EqualFold(strings.TrimSpace(a.Mask), strings.TrimSpace(b.Mask)) &&
		strings.EqualFold(strings.TrimSpace(a.Gateway), strings.TrimSpace(b.Gateway)) &&
		a.InterfaceLUID == b.InterfaceLUID &&
		a.IfIndex == b.IfIndex
}
