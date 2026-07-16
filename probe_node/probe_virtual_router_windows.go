//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
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
	mu                sync.Mutex
	fakeRouteDef      probeRouteWindowsRouteDef
	fakeApplied       bool
	takeoverRouteDefs []probeRouteWindowsRouteDef
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
		return ensureProbeVirtualRouterWindowsRoutes(interfaceLUID, ifIndex)
	}
	if ifIndex > 0 {
		if err := probeLocalUpsertWindowsInterfaceIPv4(ifIndex, cleanIP, probeLocalTUNRouteIPv4PrefixLen); err != nil {
			return err
		}
		return ensureProbeVirtualRouterWindowsRoutes(0, ifIndex)
	}
	return nil
}

func cleanupProbeVirtualRouterPlatformRoutes() error {
	return cleanupProbeVirtualRouterWindowsRoutes()
}

func ensureProbeVirtualRouterWindowsRoutes(interfaceLUID uint64, ifIndex int) error {
	if ifIndex <= 0 {
		return nil
	}
	if err := ensureProbeVirtualRouterWindowsFakeIPRoute(interfaceLUID, ifIndex); err != nil {
		return err
	}
	if !probeVirtualRouterLocalEntryEnabled() {
		return cleanupProbeVirtualRouterWindowsTakeoverRoutes()
	}
	if err := ensureProbeVirtualRouterWindowsTakeoverRoutes(interfaceLUID, ifIndex); err != nil {
		return err
	}
	cleanupProbeRouteDirectBypassForVirtualRouterRules(currentProbeVirtualRouterConfig())
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
	probeVirtualRouterWindowsRouteState.fakeRouteDef = probeRouteWindowsRouteDef{}
	probeVirtualRouterWindowsRouteState.fakeApplied = false
	probeVirtualRouterWindowsRouteState.takeoverRouteDefs = nil
	probeVirtualRouterWindowsRouteState.mu.Unlock()

	var allErr error
	for _, routeDef := range takeoverRouteDefs {
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
