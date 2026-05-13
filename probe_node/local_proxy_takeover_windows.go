//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	probeLocalWindowsRouteSplitPrefixA = "0.0.0.0"
	probeLocalWindowsRouteSplitMaskA   = "128.0.0.0"
	probeLocalWindowsRouteSplitPrefixB = "128.0.0.0"
	probeLocalWindowsRouteSplitMaskB   = "128.0.0.0"
	probeLocalWindowsRouteMetric       = 3
)

type probeLocalWindowsRouteDef struct {
	Prefix  string
	Mask    string
	Gateway string
	IfIndex int
}

var probeLocalWindowsTakeoverState = struct {
	mu                 sync.Mutex
	enabled            bool
	routePrintOutput   string
	tunGateway         string
	tunIfIndex         int
	bypassGateway      string
	bypassInterfaceIdx int
	routeDefs          []probeLocalWindowsRouteDef
	dnsAdapterGUID     string
	originalDNSServers []string
	dnsChanged         bool
}{}

var (
	probeLocalWindowsRunCommand             = runProbeLocalCommand
	probeLocalEnsureWindowsRouteTargetReady = ensureProbeLocalWindowsRouteTargetConfigured
)

func applyProbeLocalProxyTakeover() error {
	if err := probeLocalEnsureWindowsRouteTargetReady(); err != nil {
		return fmt.Errorf("prepare windows tun route target failed: %w", err)
	}
	gateway, ifIndex, err := resolveProbeLocalWindowsRouteTarget()
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

	dnsAdapterGUID, originalDNSServers, dnsErr := prepareProbeLocalWindowsPrimaryDNSForFakeIPMode(ifIndex, gateway)
	if dnsErr != nil {
		return fmt.Errorf("prepare windows primary dns failed: %w", dnsErr)
	}

	createdRoutes := make([]probeLocalWindowsRouteDef, 0, 5)
	for _, routeDef := range probeLocalWindowsTakeoverRouteDefs(gateway, ifIndex) {
		created, routeErr := ensureProbeLocalWindowsRoute(routeDef)
		if routeErr != nil {
			var rollbackErr error
			for i := len(createdRoutes) - 1; i >= 0; i-- {
				if delErr := deleteProbeLocalWindowsRoute(createdRoutes[i]); delErr != nil {
					rollbackErr = errors.Join(rollbackErr, delErr)
				}
			}
			if restoreErr := restoreProbeLocalWindowsPrimaryDNS(dnsAdapterGUID, originalDNSServers); restoreErr != nil {
				rollbackErr = errors.Join(rollbackErr, restoreErr)
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
	probeLocalWindowsTakeoverState.tunGateway = gateway
	probeLocalWindowsTakeoverState.tunIfIndex = ifIndex
	probeLocalWindowsTakeoverState.bypassGateway = ""
	probeLocalWindowsTakeoverState.bypassInterfaceIdx = 0
	probeLocalWindowsTakeoverState.routeDefs = append([]probeLocalWindowsRouteDef(nil), probeLocalWindowsTakeoverRouteDefs(gateway, ifIndex)...)
	probeLocalWindowsTakeoverState.dnsAdapterGUID = dnsAdapterGUID
	probeLocalWindowsTakeoverState.originalDNSServers = append([]string(nil), originalDNSServers...)
	probeLocalWindowsTakeoverState.dnsChanged = strings.TrimSpace(dnsAdapterGUID) != ""
	probeLocalWindowsTakeoverState.mu.Unlock()

	logProbeInfof("probe local proxy takeover applied on windows fake-ip mode: gateway=%s if_index=%d route_snapshot_len=%d", gateway, ifIndex, len(strings.TrimSpace(out)))
	return nil
}

func restoreProbeLocalProxyDirect() error {
	probeLocalWindowsTakeoverState.mu.Lock()
	wasEnabled := probeLocalWindowsTakeoverState.enabled
	gateway := probeLocalWindowsTakeoverState.tunGateway
	ifIndex := probeLocalWindowsTakeoverState.tunIfIndex
	routeDefs := append([]probeLocalWindowsRouteDef(nil), probeLocalWindowsTakeoverState.routeDefs...)
	dnsAdapterGUID := strings.TrimSpace(probeLocalWindowsTakeoverState.dnsAdapterGUID)
	originalDNSServers := append([]string(nil), probeLocalWindowsTakeoverState.originalDNSServers...)
	dnsChanged := probeLocalWindowsTakeoverState.dnsChanged
	probeLocalWindowsTakeoverState.enabled = false
	probeLocalWindowsTakeoverState.routePrintOutput = ""
	probeLocalWindowsTakeoverState.tunGateway = ""
	probeLocalWindowsTakeoverState.tunIfIndex = 0
	probeLocalWindowsTakeoverState.bypassGateway = ""
	probeLocalWindowsTakeoverState.bypassInterfaceIdx = 0
	probeLocalWindowsTakeoverState.routeDefs = nil
	probeLocalWindowsTakeoverState.dnsAdapterGUID = ""
	probeLocalWindowsTakeoverState.originalDNSServers = nil
	probeLocalWindowsTakeoverState.dnsChanged = false
	probeLocalWindowsTakeoverState.mu.Unlock()

	if !wasEnabled {
		return nil
	}

	var allErr error
	if len(routeDefs) == 0 {
		routeDefs = probeLocalWindowsTakeoverRouteDefs(gateway, ifIndex)
	}
	for _, routeDef := range routeDefs {
		if err := deleteProbeLocalWindowsRoute(routeDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	if dnsChanged {
		allErr = errors.Join(allErr, restoreProbeLocalWindowsPrimaryDNS(dnsAdapterGUID, originalDNSServers))
	}
	if _, err := probeLocalSnapshotWindowsIPv4Routes(); err != nil {
		logProbeWarnf("probe local proxy restore on windows route inspect failed: %v", err)
	}
	if allErr != nil {
		return fmt.Errorf("restore windows proxy takeover failed: %w", allErr)
	}
	return nil
}

func resolveProbeLocalWindowsRouteTarget() (string, int, error) {
	gateway := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_GATEWAY"))
	if gateway == "" {
		return "", 0, errors.New("missing PROBE_LOCAL_TUN_GATEWAY")
	}
	rawIfIndex := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX"))
	if rawIfIndex == "" {
		return "", 0, errors.New("missing PROBE_LOCAL_TUN_IF_INDEX")
	}
	ifIndex, err := strconv.Atoi(rawIfIndex)
	if err != nil || ifIndex <= 0 {
		return "", 0, fmt.Errorf("invalid PROBE_LOCAL_TUN_IF_INDEX=%q", rawIfIndex)
	}
	return gateway, ifIndex, nil
}

func probeLocalWindowsTakeoverRouteDefs(gateway string, ifIndex int) []probeLocalWindowsRouteDef {
	prefix, mask := probeLocalWindowsFakeIPRoutePrefixAndMask(currentProbeLocalDNSFakeIPCIDR())
	return []probeLocalWindowsRouteDef{
		{Prefix: prefix, Mask: mask, Gateway: gateway, IfIndex: ifIndex},
	}
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
	if strings.TrimSpace(routeDef.Gateway) == "" || routeDef.IfIndex <= 0 {
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

func prepareProbeLocalWindowsPrimaryDNSForFakeIPMode(tunIfIndex int, gateway string) (string, []string, error) {
	routeTarget, err := probeLocalResolveWindowsPrimaryEgressRoute(tunIfIndex)
	if err != nil {
		return "", nil, err
	}
	adapter, err := probeLocalFindWindowsAdapterByIfIndex(routeTarget.InterfaceIndex)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(adapter.AdapterGUID) == "" {
		return "", nil, errors.New("primary adapter guid is empty")
	}
	dnsHost := resolveProbeLocalTUNDNSListenHostForGateway(gateway)
	if dnsHost == "" {
		return "", nil, errors.New("tun dns listen host is empty")
	}
	original := dedupeProbeLocalIPv4Strings(adapter.DNSServers)
	if err := probeLocalSetWindowsInterfaceDNS(adapter.AdapterGUID, []string{dnsHost}); err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(adapter.AdapterGUID), original, nil
}

func restoreProbeLocalWindowsPrimaryDNS(adapterGUID string, dnsServers []string) error {
	cleanGUID := strings.TrimSpace(adapterGUID)
	if cleanGUID == "" {
		return nil
	}
	servers := dedupeProbeLocalIPv4Strings(dnsServers)
	if len(servers) == 0 {
		logProbeWarnf("probe local proxy restore primary dns skipped: original dns list is empty adapter=%s", cleanGUID)
		return nil
	}
	if err := probeLocalSetWindowsInterfaceDNS(cleanGUID, servers); err != nil {
		return err
	}
	return nil
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
	probeLocalWindowsTakeoverState.mu.Lock()
	enabled := probeLocalWindowsTakeoverState.enabled
	servers := append([]string(nil), probeLocalWindowsTakeoverState.originalDNSServers...)
	tunIfIndex := probeLocalWindowsTakeoverState.tunIfIndex
	probeLocalWindowsTakeoverState.mu.Unlock()
	if len(servers) > 0 {
		return dedupeProbeLocalIPv4Strings(servers)
	}
	if enabled {
		return nil
	}
	if tunIfIndex <= 0 {
		if _, ifIndex, err := resolveProbeLocalWindowsRouteTarget(); err == nil {
			tunIfIndex = ifIndex
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
	return dedupeProbeLocalIPv4Strings(out)
}
