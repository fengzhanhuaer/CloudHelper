//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	probeRouteWindowsSplitRoutePrefixA = "0.0.0.0"
	probeRouteWindowsSplitRouteMaskA   = "128.0.0.0"
	probeRouteWindowsSplitRoutePrefixB = "128.0.0.0"
	probeRouteWindowsSplitRouteMaskB   = "128.0.0.0"
	probeRouteWindowsHostRouteMask     = "255.255.255.255"
	probeRouteWindowsRouteMetric       = 3
)

type probeRouteWindowsRouteDef struct {
	Prefix        string
	Mask          string
	Gateway       string
	InterfaceLUID uint64
	IfIndex       int
}

type probeRouteWindowsTUNRouteTarget struct {
	Gateway        string
	InterfaceLUID  uint64
	InterfaceIndex int
}

var (
	probeLocalWindowsRunCommand = runProbeLocalCommand
)

func resolveProbeRouteWindowsTUNRouteTarget() (probeRouteWindowsTUNRouteTarget, error) {
	gateway := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_GATEWAY"))
	if gateway == "" {
		return probeRouteWindowsTUNRouteTarget{}, errors.New("missing PROBE_LOCAL_TUN_GATEWAY")
	}
	rawInterfaceLUID := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_LUID"))
	if rawInterfaceLUID == "" {
		rawInterfaceIndex := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX"))
		if rawInterfaceIndex == "" {
			return probeRouteWindowsTUNRouteTarget{}, errors.New("missing PROBE_LOCAL_TUN_IF_LUID or PROBE_LOCAL_TUN_IF_INDEX")
		}
		interfaceIndex, parseErr := strconv.Atoi(rawInterfaceIndex)
		if parseErr != nil || interfaceIndex <= 0 {
			return probeRouteWindowsTUNRouteTarget{}, fmt.Errorf("invalid PROBE_LOCAL_TUN_IF_INDEX=%q", rawInterfaceIndex)
		}
		return probeRouteWindowsTUNRouteTarget{Gateway: gateway, InterfaceLUID: 0, InterfaceIndex: interfaceIndex}, nil
	}
	interfaceLUID, parseErr := strconv.ParseUint(rawInterfaceLUID, 10, 64)
	if parseErr != nil || interfaceLUID == 0 {
		return probeRouteWindowsTUNRouteTarget{}, fmt.Errorf("invalid PROBE_LOCAL_TUN_IF_LUID=%q", rawInterfaceLUID)
	}
	interfaceIndex, indexErr := interfaceIndexFromLUID(interfaceLUID)
	if indexErr != nil {
		return probeRouteWindowsTUNRouteTarget{}, fmt.Errorf("resolve interface index from PROBE_LOCAL_TUN_IF_LUID failed: %w", indexErr)
	}
	return probeRouteWindowsTUNRouteTarget{Gateway: gateway, InterfaceLUID: interfaceLUID, InterfaceIndex: interfaceIndex}, nil
}

func parseProbeRouteTunnelIPv4Networks(cidrs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	seen := map[string]struct{}{}
	for _, cidr := range cidrs {
		ip, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil || ip == nil || ip.To4() == nil || network == nil || network.IP.To4() == nil {
			continue
		}
		key := network.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, network)
	}
	return out
}

func probeRouteIPInAnyNetwork(ip net.IP, networks []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, network := range networks {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func ensureProbeRouteDirectBypass(targetAddr string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return err
	}
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return errors.New("empty bypass target host")
	}
	var ips []string
	if ip := net.ParseIP(cleanHost); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			ips = []string{ip4.String()}
		}
	} else {
		ips, err = lookupProbeLocalIPv4ForBypass(cleanHost)
		if err != nil {
			return err
		}
	}
	if len(ips) == 0 {
		return fmt.Errorf("bypass target has no ipv4 address: %s", cleanHost)
	}
	if probeRouteWindowsDirectBypassIPsContainProtectedRange(ips) {
		logProbeWarnf("probe route direct route skipped for protected tun target: target=%s ips=%s", strings.TrimSpace(targetAddr), strings.Join(ips, ","))
		return nil
	}
	excludedIfIndex := currentProbeVirtualRouterTUNDataPlaneIfIndex()
	bypassTarget, ok := currentProbeRouteWindowsDirectRouteTarget()
	if !ok || bypassTarget.InterfaceIndex <= 0 || strings.TrimSpace(bypassTarget.NextHop) == "" {
		if excludedIfIndex <= 0 {
			var excludeErr error
			excludedIfIndex, excludeErr = resolveProbeRouteWindowsDirectBypassExcludedIfIndex()
			if excludeErr != nil {
				return excludeErr
			}
		}
		bypassTarget, err = probeLocalResolveWindowsPrimaryEgressRoute(excludedIfIndex)
		if err != nil {
			return err
		}
		setProbeRouteWindowsDirectRouteTarget(bypassTarget)
		logProbeInfof("probe route direct route route target resolved on demand: excluded_if_index=%d if_index=%d next_hop=%s", excludedIfIndex, bypassTarget.InterfaceIndex, strings.TrimSpace(bypassTarget.NextHop))
	}
	if excludedIfIndex > 0 && bypassTarget.InterfaceIndex == excludedIfIndex {
		logProbeWarnf("probe route direct route target rejected because it points to tun: target=%s ips=%s if_index=%d next_hop=%s", strings.TrimSpace(targetAddr), strings.Join(ips, ","), bypassTarget.InterfaceIndex, strings.TrimSpace(bypassTarget.NextHop))
		return fmt.Errorf("direct route target points to tun interface: if_index=%d", bypassTarget.InterfaceIndex)
	}
	var allErr error
	for _, ipText := range ips {
		ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
		if ip4 == nil {
			continue
		}
		routeDef := probeRouteWindowsRouteDef{
			Prefix:        ip4.String(),
			Mask:          probeRouteWindowsHostRouteMask,
			Gateway:       strings.TrimSpace(bypassTarget.NextHop),
			InterfaceLUID: bypassTarget.InterfaceLUID,
			IfIndex:       bypassTarget.InterfaceIndex,
		}
		created, routeErr := ensureProbeRouteWindowsRoute(routeDef)
		if routeErr != nil {
			staleTarget := bypassTarget
			refreshedTarget, refreshErr := resolveProbeRouteWindowsDirectRouteTarget()
			if refreshErr == nil && !sameProbeRouteWindowsDirectRouteTarget(staleTarget, refreshedTarget) {
				setProbeRouteWindowsDirectRouteTarget(refreshedTarget)
				bypassTarget = refreshedTarget
				routeDef.Gateway = strings.TrimSpace(refreshedTarget.NextHop)
				routeDef.InterfaceLUID = refreshedTarget.InterfaceLUID
				routeDef.IfIndex = refreshedTarget.InterfaceIndex
				created, routeErr = ensureProbeRouteWindowsRoute(routeDef)
				logProbeWarnf("probe route direct route target refreshed after route failure: target=%s old={%s} new={%s} retry_err=%v", strings.TrimSpace(targetAddr), describeProbeLocalTUNEgressTarget(staleTarget), describeProbeLocalTUNEgressTarget(refreshedTarget), routeErr)
			} else if refreshErr != nil {
				routeErr = errors.Join(routeErr, refreshErr)
			}
		}
		if routeErr != nil {
			logProbeWarnf("probe route direct route host route failed: target=%s ip=%s gateway=%s if_index=%d interface_luid=%d err=%v", strings.TrimSpace(targetAddr), routeDef.Prefix, routeDef.Gateway, routeDef.IfIndex, routeDef.InterfaceLUID, routeErr)
			allErr = errors.Join(allErr, routeErr)
		} else if created {
			logProbeInfof("probe route direct route host route created: target=%s ip=%s gateway=%s if_index=%d interface_luid=%d", strings.TrimSpace(targetAddr), routeDef.Prefix, routeDef.Gateway, routeDef.IfIndex, routeDef.InterfaceLUID)
		}
	}
	return allErr
}

func cleanupProbeRouteDirectBypassForVirtualRouterRules(config probeVirtualRouterConfig) {
	bypassTarget, ok := currentProbeRouteWindowsDirectRouteTarget()
	if !ok || bypassTarget.InterfaceIndex <= 0 || strings.TrimSpace(bypassTarget.NextHop) == "" {
		return
	}
	entries, err := probeLocalListWindowsRouteEntries()
	if err != nil {
		logProbeWarnf("probe route direct bypass cleanup list routes failed: err=%v", err)
		return
	}
	excludedIfIndex := currentProbeVirtualRouterTUNDataPlaneIfIndex()
	for _, entry := range entries {
		if entry.PrefixLength != 32 || entry.IfIndex <= 0 {
			continue
		}
		if excludedIfIndex > 0 && entry.IfIndex == excludedIfIndex {
			continue
		}
		if entry.IfIndex != bypassTarget.InterfaceIndex || !strings.EqualFold(strings.TrimSpace(entry.NextHop), strings.TrimSpace(bypassTarget.NextHop)) {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(entry.Prefix)).To4()
		if ip == nil || !probeVirtualRouterConfigRoutesIPViaProbeExit(config, ip) {
			continue
		}
		routeDef := probeRouteWindowsRouteDef{
			Prefix:  ip.String(),
			Mask:    probeRouteWindowsHostRouteMask,
			Gateway: strings.TrimSpace(entry.NextHop),
			IfIndex: entry.IfIndex,
		}
		if err := deleteProbeRouteWindowsRoute(routeDef); err != nil {
			logProbeWarnf("probe route direct bypass cleanup failed: ip=%s gateway=%s if_index=%d err=%v", routeDef.Prefix, routeDef.Gateway, routeDef.IfIndex, err)
			continue
		}
		logProbeInfof("probe route direct bypass cleanup removed virtual-router target route: ip=%s gateway=%s if_index=%d", routeDef.Prefix, routeDef.Gateway, routeDef.IfIndex)
	}
}

func probeVirtualRouterConfigRoutesIPViaProbeExit(config probeVirtualRouterConfig, ip net.IP) bool {
	target := ip.To4()
	if target == nil {
		return false
	}
	config = sanitizeProbeVirtualRouterConfigForCache(config)
	for _, rule := range config.RouteRules {
		if sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) != "probe_exit" || normalizeProbeRouteNodeID(rule.ExitNodeID) == "" {
			continue
		}
		for _, entry := range rule.Entries {
			if probeVirtualRouterRouteRuleEntryMatchesIP(target, entry) {
				return true
			}
		}
	}
	return false
}

func probeRouteWindowsDirectBypassIPsContainProtectedRange(ips []string) bool {
	if len(ips) == 0 {
		return false
	}
	networks := parseProbeRouteTunnelIPv4Networks([]string{currentProbeLocalDNSFakeIPCIDR(), currentProbeVirtualRouterFakeIPCIDR()})
	if len(networks) == 0 {
		return false
	}
	for _, ipText := range ips {
		ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
		if ip4 == nil {
			continue
		}
		if probeRouteIPInAnyNetwork(ip4, networks) {
			return true
		}
	}
	return false
}

func ensureProbeRouteWindowsSplitRoute(prefix, mask, gateway string, ifIndex int) (bool, error) {
	return ensureProbeRouteWindowsRoute(probeRouteWindowsRouteDef{Prefix: prefix, Mask: mask, Gateway: gateway, IfIndex: ifIndex})
}

func ensureProbeRouteWindowsRoute(routeDef probeRouteWindowsRouteDef) (bool, error) {
	return probeLocalCreateWindowsRouteEntry(routeDef)
}

func deleteProbeRouteWindowsSplitRoute(prefix, mask, gateway string, ifIndex int) error {
	return deleteProbeRouteWindowsRoute(probeRouteWindowsRouteDef{Prefix: prefix, Mask: mask, Gateway: gateway, IfIndex: ifIndex})
}

func deleteProbeRouteWindowsRoute(routeDef probeRouteWindowsRouteDef) error {
	if strings.TrimSpace(routeDef.Gateway) == "" || (routeDef.InterfaceLUID == 0 && routeDef.IfIndex <= 0) {
		return nil
	}
	return probeLocalDeleteWindowsRouteEntry(routeDef)
}

func probeRouteWindowsFakeIPRoutePrefixAndMask(cidr string) (string, string) {
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

func isProbeLocalWindowsRouteExistsErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "object already exists") || strings.Contains(text, "对象已存在")
}

func isProbeLocalWindowsRouteMissingErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "route specified was not found") || strings.Contains(text, "找不到指定的路由")
}

func currentProbeLocalSystemDNSServers() []string {
	if backup, ok := loadProbeVirtualRouterDNSBackupBestEffort(); ok {
		return filterProbeLocalSystemDNSUpstreamServers(backup.DNSServers)
	}
	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	tunInterfaceLUID := probeVirtualRouterTUNDataPlaneState.interfaceLUID
	tunIfIndex := probeVirtualRouterTUNDataPlaneState.ifIndex
	probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	if tunIfIndex <= 0 && tunInterfaceLUID > 0 {
		if ifIndex, err := interfaceIndexFromLUID(tunInterfaceLUID); err == nil {
			tunIfIndex = ifIndex
		}
	}
	if tunIfIndex <= 0 {
		if routeTarget, err := resolveProbeRouteWindowsTUNRouteTarget(); err == nil {
			tunIfIndex = routeTarget.InterfaceIndex
		}
	}
	if tunIfIndex <= 0 {
		return nil
	}
	out, err := probeLocalResolveWindowsPrimaryDNSServers(tunIfIndex)
	if err != nil {
		logProbeWarnf("probe local system dns resolve failed: %v", err)
		return nil
	}
	return filterProbeLocalSystemDNSUpstreamServers(out)
}

func filterProbeLocalSystemDNSUpstreamServers(dnsServers []string) []string {
	out := make([]string, 0, len(dnsServers))
	seen := make(map[string]struct{}, len(dnsServers))
	virtualDNS := ""
	if ip := net.ParseIP(strings.TrimSpace(probeVirtualRouterDNSListenHost)).To4(); ip != nil {
		virtualDNS = ip.String()
	}
	for _, raw := range dnsServers {
		ip4 := net.ParseIP(strings.TrimSpace(raw)).To4()
		if ip4 == nil {
			continue
		}
		value := ip4.String()
		if virtualDNS != "" && value == virtualDNS {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func currentProbeVirtualRouterTUNDataPlaneIfIndex() int {
	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	tunInterfaceLUID := probeVirtualRouterTUNDataPlaneState.interfaceLUID
	tunIfIndex := probeVirtualRouterTUNDataPlaneState.ifIndex
	probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	if tunIfIndex > 0 {
		return tunIfIndex
	}
	if tunInterfaceLUID > 0 {
		if ifIndex, err := interfaceIndexFromLUID(tunInterfaceLUID); err == nil {
			return ifIndex
		}
	}
	return 0
}
