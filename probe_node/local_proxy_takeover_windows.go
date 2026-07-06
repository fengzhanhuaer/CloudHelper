//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	probeLocalWindowsRouteSplitPrefixA = "0.0.0.0"
	probeLocalWindowsRouteSplitMaskA   = "128.0.0.0"
	probeLocalWindowsRouteSplitPrefixB = "128.0.0.0"
	probeLocalWindowsRouteSplitMaskB   = "128.0.0.0"
	probeLocalWindowsHostRouteMask     = "255.255.255.255"
	probeLocalWindowsRouteMetric       = 3
)

type probeLocalWindowsRouteDef struct {
	Prefix        string
	Mask          string
	Gateway       string
	InterfaceLUID uint64
	IfIndex       int
}

type probeLocalWindowsRouteTarget struct {
	Gateway        string
	InterfaceLUID  uint64
	InterfaceIndex int
}

type probeLocalTUNPrimaryDNSBackup struct {
	Version        int      `json:"version"`
	UpdatedAt      string   `json:"updated_at"`
	InterfaceIndex int      `json:"interface_index"`
	InterfaceGUID  string   `json:"interface_guid"`
	InterfaceName  string   `json:"interface_name,omitempty"`
	DNSServers     []string `json:"dns_servers"`
	AppliedDNS     []string `json:"applied_dns,omitempty"`
}

var (
	probeLocalWindowsRunCommand = runProbeLocalCommand
)

func resolveProbeLocalWindowsRouteTarget() (probeLocalWindowsRouteTarget, error) {
	gateway := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_GATEWAY"))
	if gateway == "" {
		return probeLocalWindowsRouteTarget{}, errors.New("missing PROBE_LOCAL_TUN_GATEWAY")
	}
	rawInterfaceLUID := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_LUID"))
	if rawInterfaceLUID == "" {
		rawInterfaceIndex := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX"))
		if rawInterfaceIndex == "" {
			return probeLocalWindowsRouteTarget{}, errors.New("missing PROBE_LOCAL_TUN_IF_LUID or PROBE_LOCAL_TUN_IF_INDEX")
		}
		interfaceIndex, parseErr := strconv.Atoi(rawInterfaceIndex)
		if parseErr != nil || interfaceIndex <= 0 {
			return probeLocalWindowsRouteTarget{}, fmt.Errorf("invalid PROBE_LOCAL_TUN_IF_INDEX=%q", rawInterfaceIndex)
		}
		return probeLocalWindowsRouteTarget{Gateway: gateway, InterfaceLUID: 0, InterfaceIndex: interfaceIndex}, nil
	}
	interfaceLUID, parseErr := strconv.ParseUint(rawInterfaceLUID, 10, 64)
	if parseErr != nil || interfaceLUID == 0 {
		return probeLocalWindowsRouteTarget{}, fmt.Errorf("invalid PROBE_LOCAL_TUN_IF_LUID=%q", rawInterfaceLUID)
	}
	interfaceIndex, indexErr := interfaceIndexFromLUID(interfaceLUID)
	if indexErr != nil {
		return probeLocalWindowsRouteTarget{}, fmt.Errorf("resolve interface index from PROBE_LOCAL_TUN_IF_LUID failed: %w", indexErr)
	}
	return probeLocalWindowsRouteTarget{Gateway: gateway, InterfaceLUID: interfaceLUID, InterfaceIndex: interfaceIndex}, nil
}

func parseProbeLocalTunnelIPv4Networks(cidrs []string) []*net.IPNet {
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

func probeLocalIPInAnyNetwork(ip net.IP, networks []*net.IPNet) bool {
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

func probeLocalWindowsLocalBypassRouteDefs(routeTarget probeLocalWindowsDirectBypassRouteTarget) []probeLocalWindowsRouteDef {
	return []probeLocalWindowsRouteDef{
		{Prefix: "10.0.0.0", Mask: "255.0.0.0", Gateway: routeTarget.NextHop, IfIndex: routeTarget.InterfaceIndex},
		{Prefix: "172.16.0.0", Mask: "255.240.0.0", Gateway: routeTarget.NextHop, IfIndex: routeTarget.InterfaceIndex},
		{Prefix: "192.168.0.0", Mask: "255.255.0.0", Gateway: routeTarget.NextHop, IfIndex: routeTarget.InterfaceIndex},
	}
}

func ensureProbeLocalDirectBypass(targetAddr string) error {
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
	if probeLocalWindowsDirectBypassIPsContainProtectedRange(ips) {
		logProbeWarnf("probe local direct bypass skipped for protected tun target: target=%s ips=%s", strings.TrimSpace(targetAddr), strings.Join(ips, ","))
		return nil
	}
	excludedIfIndex := currentProbeLocalTUNDataPlaneIfIndex()
	bypassTarget, ok := currentProbeLocalWindowsDirectBypassRouteTarget()
	if !ok || bypassTarget.InterfaceIndex <= 0 || strings.TrimSpace(bypassTarget.NextHop) == "" {
		if excludedIfIndex <= 0 {
			routeTarget, routeErr := resolveProbeLocalWindowsRouteTarget()
			if routeErr != nil {
				return routeErr
			}
			excludedIfIndex = routeTarget.InterfaceIndex
		}
		bypassTarget, err = probeLocalResolveWindowsPrimaryEgressRoute(excludedIfIndex)
		if err != nil {
			return err
		}
		setProbeLocalWindowsDirectBypassRouteTarget(bypassTarget)
		logProbeInfof("probe local direct bypass route target resolved on demand: excluded_if_index=%d if_index=%d next_hop=%s", excludedIfIndex, bypassTarget.InterfaceIndex, strings.TrimSpace(bypassTarget.NextHop))
	}
	if excludedIfIndex > 0 && bypassTarget.InterfaceIndex == excludedIfIndex {
		logProbeWarnf("probe local direct bypass target rejected because it points to tun: target=%s ips=%s if_index=%d next_hop=%s", strings.TrimSpace(targetAddr), strings.Join(ips, ","), bypassTarget.InterfaceIndex, strings.TrimSpace(bypassTarget.NextHop))
		return fmt.Errorf("direct bypass route target points to tun interface: if_index=%d", bypassTarget.InterfaceIndex)
	}
	var allErr error
	for _, ipText := range ips {
		ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
		if ip4 == nil {
			continue
		}
		routeDef := probeLocalWindowsRouteDef{
			Prefix:  ip4.String(),
			Mask:    probeLocalWindowsHostRouteMask,
			Gateway: strings.TrimSpace(bypassTarget.NextHop),
			IfIndex: bypassTarget.InterfaceIndex,
		}
		created, routeErr := ensureProbeLocalWindowsRoute(routeDef)
		if routeErr != nil {
			logProbeWarnf("probe local direct bypass host route failed: target=%s ip=%s gateway=%s if_index=%d err=%v", strings.TrimSpace(targetAddr), routeDef.Prefix, routeDef.Gateway, routeDef.IfIndex, routeErr)
			allErr = errors.Join(allErr, routeErr)
		} else if created {
			logProbeInfof("probe local direct bypass host route created: target=%s ip=%s gateway=%s if_index=%d", strings.TrimSpace(targetAddr), routeDef.Prefix, routeDef.Gateway, routeDef.IfIndex)
		}
	}
	return allErr
}

func probeLocalWindowsDirectBypassIPsContainProtectedRange(ips []string) bool {
	if len(ips) == 0 {
		return false
	}
	networks := parseProbeLocalTunnelIPv4Networks([]string{currentProbeLocalDNSFakeIPCIDR(), currentProbeVirtualRouterFakeIPCIDR()})
	if len(networks) == 0 {
		return false
	}
	for _, ipText := range ips {
		ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
		if ip4 == nil {
			continue
		}
		if probeLocalIPInAnyNetwork(ip4, networks) {
			return true
		}
	}
	return false
}

func ensureProbeLocalWindowsSplitRoute(prefix, mask, gateway string, ifIndex int) (bool, error) {
	return ensureProbeLocalWindowsRoute(probeLocalWindowsRouteDef{Prefix: prefix, Mask: mask, Gateway: gateway, IfIndex: ifIndex})
}

func ensureProbeLocalWindowsRoute(routeDef probeLocalWindowsRouteDef) (bool, error) {
	return probeLocalCreateWindowsRouteEntry(routeDef)
}

func deleteProbeLocalWindowsSplitRoute(prefix, mask, gateway string, ifIndex int) error {
	return deleteProbeLocalWindowsRoute(probeLocalWindowsRouteDef{Prefix: prefix, Mask: mask, Gateway: gateway, IfIndex: ifIndex})
}

func deleteProbeLocalWindowsRoute(routeDef probeLocalWindowsRouteDef) error {
	if strings.TrimSpace(routeDef.Gateway) == "" || (routeDef.InterfaceLUID == 0 && routeDef.IfIndex <= 0) {
		return nil
	}
	return probeLocalDeleteWindowsRouteEntry(routeDef)
}

func probeLocalWindowsFakeIPRoutePrefixAndMask(cidr string) (string, string) {
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

func probeLocalWindowsCIDRRoutePrefixAndMask(cidr string) (string, string) {
	ip, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil || network == nil || ip == nil || ip.To4() == nil {
		return "", ""
	}
	prefix := network.IP.To4()
	if prefix == nil {
		return "", ""
	}
	mask := net.IP(network.Mask).String()
	if strings.TrimSpace(mask) == "" {
		return "", ""
	}
	return prefix.String(), mask
}

func dedupeProbeLocalWindowsRouteDefs(routeDefs []probeLocalWindowsRouteDef) []probeLocalWindowsRouteDef {
	out := make([]probeLocalWindowsRouteDef, 0, len(routeDefs))
	seen := make(map[string]struct{}, len(routeDefs))
	for _, routeDef := range routeDefs {
		key := strings.Join([]string{
			strings.TrimSpace(routeDef.Prefix),
			strings.TrimSpace(routeDef.Mask),
			strings.TrimSpace(routeDef.Gateway),
			fmt.Sprintf("%d", routeDef.InterfaceLUID),
			strconv.Itoa(routeDef.IfIndex),
		}, "|")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, routeDef)
	}
	return out
}

func resolveProbeLocalTUNDNSListenHostForGateway(gateway string) string {
	host := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_DNS_HOST"))
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		return ip.To4().String()
	}
	if ip := net.ParseIP(strings.TrimSpace(probeLocalTUNInterfaceIPv4)); ip != nil && ip.To4() != nil {
		return ip.To4().String()
	}
	if ip := net.ParseIP(strings.TrimSpace(gateway)); ip != nil && ip.To4() != nil {
		return ip.To4().String()
	}
	return ""
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

func currentProbeLocalTUNDNSListenHost() string {
	return resolveProbeLocalTUNDNSListenHostForGateway(probeLocalTUNRouteGatewayIPv4)
}

func currentProbeLocalSystemDNSServers() []string {
	if backup, ok := loadProbeLocalTUNPrimaryDNSBackupBestEffort(); ok {
		return filterProbeLocalTUNPrimaryDNSServers(backup.DNSServers)
	}
	probeLocalTUNDataPlaneState.mu.Lock()
	tunInterfaceLUID := probeLocalTUNDataPlaneState.interfaceLUID
	tunIfIndex := probeLocalTUNDataPlaneState.ifIndex
	probeLocalTUNDataPlaneState.mu.Unlock()
	if tunIfIndex <= 0 && tunInterfaceLUID > 0 {
		if ifIndex, err := interfaceIndexFromLUID(tunInterfaceLUID); err == nil {
			tunIfIndex = ifIndex
		}
	}
	if tunIfIndex <= 0 {
		if routeTarget, err := resolveProbeLocalWindowsRouteTarget(); err == nil {
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
	return filterProbeLocalTUNPrimaryDNSServers(out)
}

func currentProbeLocalTUNDataPlaneIfIndex() int {
	probeLocalTUNDataPlaneState.mu.Lock()
	tunInterfaceLUID := probeLocalTUNDataPlaneState.interfaceLUID
	tunIfIndex := probeLocalTUNDataPlaneState.ifIndex
	probeLocalTUNDataPlaneState.mu.Unlock()
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

func applyProbeLocalTUNPrimaryDNS() error {
	routeTarget, err := resolveProbeLocalWindowsRouteTarget()
	if err != nil {
		return err
	}
	dnsHost := strings.TrimSpace(currentProbeLocalTUNDNSListenHost())
	if dnsHost == "" {
		dnsHost = probeLocalDNSListenHost
	}
	if ip := net.ParseIP(dnsHost); ip == nil || ip.To4() == nil {
		return fmt.Errorf("invalid tun dns host: %s", dnsHost)
	}
	reconcileProbeLocalDNSRuntime()
	adapter, err := probeLocalResolveWindowsPrimaryDNSAdapter(routeTarget.InterfaceIndex)
	if err != nil {
		return fmt.Errorf("resolve windows primary dns adapter failed: %w", err)
	}
	if strings.TrimSpace(adapter.AdapterGUID) == "" {
		return errors.New("primary dns adapter guid is empty")
	}
	backup, exists := loadProbeLocalTUNPrimaryDNSBackupBestEffort()
	systemDNSServers := []string(nil)
	if exists && strings.EqualFold(strings.TrimSpace(backup.InterfaceGUID), strings.TrimSpace(adapter.AdapterGUID)) {
		systemDNSServers = filterProbeLocalTUNPrimaryDNSServers(backup.DNSServers)
		if len(systemDNSServers) == 0 {
			return errors.New("primary dns backup has no usable dns servers")
		}
		backup.InterfaceIndex = adapter.InterfaceIndex
		backup.InterfaceGUID = strings.TrimSpace(adapter.AdapterGUID)
		backup.InterfaceName = strings.TrimSpace(adapter.Name)
		backup.DNSServers = systemDNSServers
	} else {
		systemDNSServers = filterProbeLocalTUNPrimaryDNSServers(adapter.DNSServers)
		if len(systemDNSServers) == 0 {
			return errors.New("primary dns adapter dns servers are empty or match tun dns")
		}
		backup = probeLocalTUNPrimaryDNSBackup{
			Version:        1,
			InterfaceIndex: adapter.InterfaceIndex,
			InterfaceGUID:  strings.TrimSpace(adapter.AdapterGUID),
			InterfaceName:  strings.TrimSpace(adapter.Name),
			DNSServers:     systemDNSServers,
		}
	}
	backup.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	backup.AppliedDNS = []string{net.ParseIP(dnsHost).To4().String()}
	if err := persistProbeLocalTUNPrimaryDNSBackup(backup); err != nil {
		return err
	}
	if err := probeLocalSetWindowsInterfaceDNS(adapter.AdapterGUID, backup.AppliedDNS); err != nil {
		return fmt.Errorf("set primary adapter dns to tun dns failed: %w", err)
	}
	logProbeInfof("probe local tun primary dns applied: if_index=%d dns=%s", adapter.InterfaceIndex, strings.Join(backup.AppliedDNS, ","))
	return nil
}

func restoreProbeLocalTUNPrimaryDNS() error {
	backup, ok := loadProbeLocalTUNPrimaryDNSBackupBestEffort()
	if !ok {
		return nil
	}
	if strings.TrimSpace(backup.InterfaceGUID) == "" {
		_ = deleteProbeLocalTUNPrimaryDNSBackup()
		return nil
	}
	if len(dedupeProbeLocalIPv4Strings(backup.DNSServers)) > 0 {
		if err := probeLocalSetWindowsInterfaceDNS(backup.InterfaceGUID, backup.DNSServers); err != nil {
			return fmt.Errorf("restore primary adapter dns failed: %w", err)
		}
	}
	if err := deleteProbeLocalTUNPrimaryDNSBackup(); err != nil {
		return err
	}
	logProbeInfof("probe local tun primary dns restored: if_index=%d", backup.InterfaceIndex)
	return nil
}

func loadProbeLocalTUNPrimaryDNSBackupBestEffort() (probeLocalTUNPrimaryDNSBackup, bool) {
	path, err := resolveProbeLocalTUNPrimaryDNSBackupPath()
	if err != nil {
		return probeLocalTUNPrimaryDNSBackup{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return probeLocalTUNPrimaryDNSBackup{}, false
	}
	backup := probeLocalTUNPrimaryDNSBackup{}
	if err := json.Unmarshal(raw, &backup); err != nil {
		logProbeWarnf("probe local tun dns backup decode failed: %v", err)
		return probeLocalTUNPrimaryDNSBackup{}, false
	}
	if backup.Version <= 0 {
		backup.Version = 1
	}
	backup.InterfaceGUID = strings.TrimSpace(backup.InterfaceGUID)
	backup.DNSServers = filterProbeLocalTUNPrimaryDNSServers(backup.DNSServers)
	backup.AppliedDNS = dedupeProbeLocalIPv4Strings(backup.AppliedDNS)
	return backup, backup.InterfaceGUID != ""
}

func persistProbeLocalTUNPrimaryDNSBackup(backup probeLocalTUNPrimaryDNSBackup) error {
	if backup.Version <= 0 {
		backup.Version = 1
	}
	backup.InterfaceGUID = strings.TrimSpace(backup.InterfaceGUID)
	backup.DNSServers = filterProbeLocalTUNPrimaryDNSServers(backup.DNSServers)
	backup.AppliedDNS = dedupeProbeLocalIPv4Strings(backup.AppliedDNS)
	if strings.TrimSpace(backup.UpdatedAt) == "" {
		backup.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	path, err := resolveProbeLocalTUNPrimaryDNSBackupPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func deleteProbeLocalTUNPrimaryDNSBackup() error {
	path, err := resolveProbeLocalTUNPrimaryDNSBackupPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func filterProbeLocalTUNPrimaryDNSServers(dnsServers []string) []string {
	tunHosts := probeLocalTUNDNSHosts()
	if len(dnsServers) == 0 {
		return nil
	}
	blocked := make(map[string]struct{}, len(tunHosts))
	for _, host := range tunHosts {
		blocked[host] = struct{}{}
	}
	out := make([]string, 0, len(dnsServers))
	seen := make(map[string]struct{}, len(dnsServers))
	for _, raw := range dnsServers {
		ip4 := net.ParseIP(strings.TrimSpace(raw)).To4()
		if ip4 == nil {
			continue
		}
		value := ip4.String()
		if _, ok := blocked[value]; ok {
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

func probeLocalTUNDNSHosts() []string {
	hosts := make([]string, 0, 2)
	if ip := net.ParseIP(strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_DNS_HOST"))); ip != nil && ip.To4() != nil {
		hosts = append(hosts, ip.To4().String())
	}
	if ip := net.ParseIP(strings.TrimSpace(probeLocalTUNInterfaceIPv4)); ip != nil && ip.To4() != nil {
		hosts = append(hosts, ip.To4().String())
	}
	return dedupeProbeLocalIPv4Strings(hosts)
}
