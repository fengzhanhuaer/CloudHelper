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
	if interfaceLUID > 0 {
		if err := probeLocalUpsertWindowsInterfaceIPv4ByLUID(interfaceLUID, ifIndex, cleanIP, probeLocalTUNRouteIPv4PrefixLen); err != nil {
			return err
		}
		if err := reconcileProbeVirtualRouterWindowsInterfaceDNS(interfaceLUID, ifIndex); err != nil {
			return err
		}
		return ensureProbeVirtualRouterWindowsRoutes(interfaceLUID, ifIndex)
	}
	if ifIndex > 0 {
		if err := probeLocalUpsertWindowsInterfaceIPv4(ifIndex, cleanIP, probeLocalTUNRouteIPv4PrefixLen); err != nil {
			return err
		}
		if err := reconcileProbeVirtualRouterWindowsInterfaceDNS(0, ifIndex); err != nil {
			return err
		}
		return ensureProbeVirtualRouterWindowsRoutes(0, ifIndex)
	}
	return nil
}

func reconcileProbeVirtualRouterWindowsInterfaceDNS(interfaceLUID uint64, ifIndex int) error {
	var (
		adapter windowsAdapterInfo
		err     error
	)
	if interfaceLUID > 0 {
		adapter, err = probeLocalFindWindowsAdapterByLUID(interfaceLUID)
	} else {
		adapter, err = probeLocalFindWindowsAdapterByIfIndex(ifIndex)
	}
	if err != nil {
		return fmt.Errorf("resolve windows virtual router adapter before dns reconcile: %w", err)
	}
	if strings.TrimSpace(adapter.AdapterGUID) == "" {
		return errors.New("windows virtual router adapter guid is empty")
	}
	if err := reconcileProbeLocalWindowsTUNInterfaceDNS(adapter); err != nil {
		return fmt.Errorf("reconcile windows virtual router interface dns: %w", err)
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
	if err := ensureProbeVirtualRouterWindowsFakeIPRoute(interfaceLUID, ifIndex); err != nil {
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

func ensureProbeVirtualRouterWindowsPublishedRoutes(interfaceLUID uint64, ifIndex int) error {
	routeDefs := buildProbeVirtualRouterWindowsPublishedRouteDefs(interfaceLUID, ifIndex)
	probeVirtualRouterWindowsRouteState.mu.Lock()
	oldRouteDefs := append([]probeRouteWindowsRouteDef(nil), probeVirtualRouterWindowsRouteState.publishedRouteDefs...)
	probeVirtualRouterWindowsRouteState.mu.Unlock()

	entries, err := probeLocalListWindowsRouteEntries()
	if err != nil {
		return fmt.Errorf("inspect windows routes for published subnet: %w", err)
	}
	activeRouteDefs := make([]probeRouteWindowsRouteDef, 0, len(routeDefs))
	staleRouteDefs := listProbeVirtualRouterWindowsStalePublishedRoutes(interfaceLUID, ifIndex, entries, routeDefs)
	staleOnLinkHostRouteDefs := make([]probeRouteWindowsRouteDef, 0)
	for _, routeDef := range routeDefs {
		collides, stale, staleOnLinkHostRoutes, inspectErr := inspectProbeVirtualRouterWindowsPublishedRoute(routeDef, ifIndex, entries)
		if inspectErr != nil {
			return inspectErr
		}
		staleOnLinkHostRouteDefs = append(staleOnLinkHostRouteDefs, staleOnLinkHostRoutes...)
		if collides {
			if stale {
				staleRouteDefs = append(staleRouteDefs, routeDef)
			}
			continue
		}
		activeRouteDefs = append(activeRouteDefs, routeDef)
	}

	deleteRouteDefs := dedupeProbeVirtualRouterWindowsRouteDefs(append(oldRouteDefs, staleRouteDefs...))
	staleOnLinkHostRouteDefs = dedupeProbeVirtualRouterWindowsRouteDefs(staleOnLinkHostRouteDefs)
	if probeVirtualRouterWindowsRouteDefsEqual(oldRouteDefs, activeRouteDefs) && len(staleRouteDefs) == 0 && len(staleOnLinkHostRouteDefs) == 0 {
		return nil
	}
	var cleanupErr error
	for _, staleRouteDef := range staleOnLinkHostRouteDefs {
		if err := deleteProbeVirtualRouterWindowsRoute(staleRouteDef); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete stale on-link host route inside published subnet: prefix=%s if_index=%d: %w", staleRouteDef.Prefix, staleRouteDef.IfIndex, err))
		}
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	return replaceProbeVirtualRouterWindowsPublishedRoutes(deleteRouteDefs, activeRouteDefs)
}

func listProbeVirtualRouterWindowsStalePublishedRoutes(interfaceLUID uint64, ifIndex int, entries []probeLocalWindowsRouteEntry, desired []probeRouteWindowsRouteDef) []probeRouteWindowsRouteDef {
	fakePrefix, fakeMask := probeVirtualRouterWindowsRoutePrefixAndMask(currentProbeVirtualRouterFakeIPCIDR())
	out := make([]probeRouteWindowsRouteDef, 0)
	for _, entry := range entries {
		if entry.IfIndex != ifIndex || entry.Protocol != probeRouteWindowsProtocolNetMgmt || entry.Metric != probeRouteWindowsRouteMetric ||
			!strings.EqualFold(strings.TrimSpace(entry.NextHop), probeLocalTUNRouteGatewayIPv4) || entry.PrefixLength <= probeRouteWindowsTakeoverPrefixLen || entry.PrefixLength > 32 {
			continue
		}
		mask := net.IP(net.CIDRMask(entry.PrefixLength, 32)).String()
		candidate := probeRouteWindowsRouteDef{
			Prefix:        strings.TrimSpace(entry.Prefix),
			Mask:          mask,
			Gateway:       probeLocalTUNRouteGatewayIPv4,
			InterfaceLUID: interfaceLUID,
			IfIndex:       ifIndex,
		}
		if strings.EqualFold(candidate.Prefix, fakePrefix) && strings.EqualFold(candidate.Mask, fakeMask) {
			continue
		}
		keep := false
		for _, routeDef := range desired {
			if probeVirtualRouterWindowsRouteDefEqual(candidate, routeDef) {
				keep = true
				break
			}
		}
		if !keep {
			out = append(out, candidate)
		}
	}
	return dedupeProbeVirtualRouterWindowsRouteDefs(out)
}

func replaceProbeVirtualRouterWindowsPublishedRoutes(oldRouteDefs, routeDefs []probeRouteWindowsRouteDef) error {
	var allErr error
	for _, oldRouteDef := range oldRouteDefs {
		if err := deleteProbeVirtualRouterWindowsRoute(oldRouteDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	for _, routeDef := range routeDefs {
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

func inspectProbeVirtualRouterWindowsPublishedRoute(routeDef probeRouteWindowsRouteDef, tunIfIndex int, entries []probeLocalWindowsRouteEntry) (collides bool, stale bool, staleOnLinkHostRoutes []probeRouteWindowsRouteDef, err error) {
	prefixLength, err := probeLocalIPv4PrefixLengthFromMask(routeDef.Mask)
	if err != nil {
		return false, false, nil, err
	}
	candidate, err := netip.ParsePrefix(fmt.Sprintf("%s/%d", routeDef.Prefix, prefixLength))
	if err != nil {
		return false, false, nil, err
	}
	for _, item := range entries {
		if item.PrefixLength == 0 {
			continue
		}
		existing, parseErr := netip.ParsePrefix(fmt.Sprintf("%s/%d", item.Prefix, item.PrefixLength))
		if parseErr != nil {
			continue
		}
		if item.IfIndex == tunIfIndex {
			if existing == candidate && strings.EqualFold(strings.TrimSpace(item.NextHop), strings.TrimSpace(routeDef.Gateway)) {
				stale = true
			}
			continue
		}
		if item.PrefixLength == 32 && candidate.Bits() < 32 && candidate.Contains(existing.Addr()) &&
			item.Protocol == probeRouteWindowsProtocolNetMgmt && strings.TrimSpace(item.NextHop) == "0.0.0.0" {
			staleOnLinkHostRoutes = append(staleOnLinkHostRoutes, probeRouteWindowsRouteDef{
				Prefix:  existing.Addr().String(),
				Mask:    probeRouteWindowsHostRouteMask,
				Gateway: "0.0.0.0",
				IfIndex: item.IfIndex,
			})
		}
		if existing.Bits() <= candidate.Bits() && existing.Contains(candidate.Addr()) {
			collides = true
		}
	}
	return collides, stale, staleOnLinkHostRoutes, nil
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
