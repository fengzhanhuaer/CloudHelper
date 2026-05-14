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
	"sync"
	"time"
)

const (
	probeLocalWindowsRouteSplitPrefixA = "0.0.0.0"
	probeLocalWindowsRouteSplitMaskA   = "128.0.0.0"
	probeLocalWindowsRouteSplitPrefixB = "128.0.0.0"
	probeLocalWindowsRouteSplitMaskB   = "128.0.0.0"
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

var probeLocalWindowsTakeoverState = struct {
	mu                 sync.Mutex
	enabled            bool
	routePrintOutput   string
	tunGateway         string
	tunInterfaceLUID   uint64
	tunIfIndex         int
	bypassGateway      string
	bypassInterfaceIdx int
	routeDefs          []probeLocalWindowsRouteDef
}{}

var (
	probeLocalWindowsRunCommand = runProbeLocalCommand
)

func applyProbeLocalProxyTakeover() error {
	routeTarget, err := resolveProbeLocalWindowsRouteTarget()
	if err != nil {
		return err
	}

	probeLocalWindowsTakeoverState.mu.Lock()
	if probeLocalWindowsTakeoverState.enabled {
		probeLocalWindowsTakeoverState.mu.Unlock()
		return nil
	}
	probeLocalWindowsTakeoverState.mu.Unlock()

	out, err := probeLocalSnapshotWindowsIPv4Routes()
	if err != nil {
		return fmt.Errorf("inspect windows route table failed: %w", err)
	}

	routeDefs := probeLocalWindowsTakeoverRouteDefs(routeTarget)
	createdRoutes := make([]probeLocalWindowsRouteDef, 0, 5)
	for _, routeDef := range routeDefs {
		created, routeErr := ensureProbeLocalWindowsRoute(routeDef)
		if routeErr != nil {
			var rollbackErr error
			for i := len(createdRoutes) - 1; i >= 0; i-- {
				if delErr := deleteProbeLocalWindowsRoute(createdRoutes[i]); delErr != nil {
					rollbackErr = errors.Join(rollbackErr, delErr)
				}
			}
			if rollbackErr != nil {
				return fmt.Errorf("apply windows takeover route %s/%s failed: %w (rollback failed: %v)", routeDef.Prefix, routeDef.Mask, routeErr, rollbackErr)
			}
			return fmt.Errorf("apply windows takeover route %s/%s failed after rollback: %w", routeDef.Prefix, routeDef.Mask, routeErr)
		}
		if created {
			createdRoutes = append(createdRoutes, routeDef)
		}
	}

	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.enabled = true
	probeLocalWindowsTakeoverState.routePrintOutput = out
	probeLocalWindowsTakeoverState.tunGateway = routeTarget.Gateway
	probeLocalWindowsTakeoverState.tunInterfaceLUID = routeTarget.InterfaceLUID
	probeLocalWindowsTakeoverState.tunIfIndex = routeTarget.InterfaceIndex
	probeLocalWindowsTakeoverState.bypassGateway = ""
	probeLocalWindowsTakeoverState.bypassInterfaceIdx = 0
	probeLocalWindowsTakeoverState.routeDefs = append([]probeLocalWindowsRouteDef(nil), routeDefs...)
	probeLocalWindowsTakeoverState.mu.Unlock()

	logProbeInfof(
		"probe local proxy takeover applied on windows fake-ip mode: gateway=%s if_luid=%d if_index=%d routes=%d route_snapshot_len=%d",
		routeTarget.Gateway,
		routeTarget.InterfaceLUID,
		routeTarget.InterfaceIndex,
		len(routeDefs),
		len(strings.TrimSpace(out)),
	)
	return nil
}

func restoreProbeLocalProxyDirect() error {
	probeLocalWindowsTakeoverState.mu.Lock()
	wasEnabled := probeLocalWindowsTakeoverState.enabled
	routeTarget := probeLocalWindowsRouteTarget{
		Gateway:        probeLocalWindowsTakeoverState.tunGateway,
		InterfaceLUID:  probeLocalWindowsTakeoverState.tunInterfaceLUID,
		InterfaceIndex: probeLocalWindowsTakeoverState.tunIfIndex,
	}
	routeDefs := append([]probeLocalWindowsRouteDef(nil), probeLocalWindowsTakeoverState.routeDefs...)
	probeLocalWindowsTakeoverState.enabled = false
	probeLocalWindowsTakeoverState.routePrintOutput = ""
	probeLocalWindowsTakeoverState.tunGateway = ""
	probeLocalWindowsTakeoverState.tunInterfaceLUID = 0
	probeLocalWindowsTakeoverState.tunIfIndex = 0
	probeLocalWindowsTakeoverState.bypassGateway = ""
	probeLocalWindowsTakeoverState.bypassInterfaceIdx = 0
	probeLocalWindowsTakeoverState.routeDefs = nil
	probeLocalWindowsTakeoverState.mu.Unlock()

	if !wasEnabled {
		return nil
	}

	var allErr error
	if len(routeDefs) == 0 {
		routeDefs = probeLocalWindowsTakeoverRouteDefs(routeTarget)
	}
	for _, routeDef := range routeDefs {
		if err := deleteProbeLocalWindowsRoute(routeDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	if _, err := probeLocalSnapshotWindowsIPv4Routes(); err != nil {
		logProbeWarnf("probe local proxy restore on windows route inspect failed: %v", err)
	}
	if allErr != nil {
		return fmt.Errorf("restore windows proxy takeover failed: %w", allErr)
	}
	return nil
}

func resolveProbeLocalWindowsRouteTarget() (probeLocalWindowsRouteTarget, error) {
	gateway := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_GATEWAY"))
	if gateway == "" {
		return probeLocalWindowsRouteTarget{}, errors.New("missing PROBE_LOCAL_TUN_GATEWAY")
	}
	rawInterfaceLUID := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_LUID"))
	if rawInterfaceLUID == "" {
		return probeLocalWindowsRouteTarget{}, errors.New("missing PROBE_LOCAL_TUN_IF_LUID")
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

func probeLocalWindowsTakeoverRouteDefs(routeTarget probeLocalWindowsRouteTarget) []probeLocalWindowsRouteDef {
	prefix, mask := probeLocalWindowsFakeIPRoutePrefixAndMask(currentProbeLocalDNSFakeIPCIDR())
	routeDefs := []probeLocalWindowsRouteDef{
		{Prefix: prefix, Mask: mask, Gateway: routeTarget.Gateway, InterfaceLUID: routeTarget.InterfaceLUID, IfIndex: routeTarget.InterfaceIndex},
	}
	for _, cidr := range probeLocalTunnelCIDRRules() {
		cidrPrefix, cidrMask := probeLocalWindowsCIDRRoutePrefixAndMask(cidr)
		if cidrPrefix == "" || cidrMask == "" {
			continue
		}
		routeDefs = append(routeDefs, probeLocalWindowsRouteDef{
			Prefix:        cidrPrefix,
			Mask:          cidrMask,
			Gateway:       routeTarget.Gateway,
			InterfaceLUID: routeTarget.InterfaceLUID,
			IfIndex:       routeTarget.InterfaceIndex,
		})
	}
	return dedupeProbeLocalWindowsRouteDefs(routeDefs)
}

func probeLocalWindowsLocalBypassRouteDefs(routeTarget probeLocalWindowsDirectBypassRouteTarget) []probeLocalWindowsRouteDef {
	return []probeLocalWindowsRouteDef{
		{Prefix: "10.0.0.0", Mask: "255.0.0.0", Gateway: routeTarget.NextHop, IfIndex: routeTarget.InterfaceIndex},
		{Prefix: "172.16.0.0", Mask: "255.240.0.0", Gateway: routeTarget.NextHop, IfIndex: routeTarget.InterfaceIndex},
		{Prefix: "192.168.0.0", Mask: "255.255.0.0", Gateway: routeTarget.NextHop, IfIndex: routeTarget.InterfaceIndex},
	}
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
	probeLocalWindowsTakeoverState.mu.Lock()
	enabled := probeLocalWindowsTakeoverState.enabled
	gateway := strings.TrimSpace(probeLocalWindowsTakeoverState.tunGateway)
	probeLocalWindowsTakeoverState.mu.Unlock()
	if !enabled {
		return ""
	}
	return resolveProbeLocalTUNDNSListenHostForGateway(gateway)
}

func currentProbeLocalSystemDNSServers() []string {
	if backup, ok := loadProbeLocalTUNPrimaryDNSBackupBestEffort(); ok {
		return filterProbeLocalTUNPrimaryDNSServers(backup.DNSServers)
	}
	probeLocalWindowsTakeoverState.mu.Lock()
	tunInterfaceLUID := probeLocalWindowsTakeoverState.tunInterfaceLUID
	tunIfIndex := probeLocalWindowsTakeoverState.tunIfIndex
	probeLocalWindowsTakeoverState.mu.Unlock()
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
