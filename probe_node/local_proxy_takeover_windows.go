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
	Prefix  string
	Mask    string
	Gateway string
	IfIndex int
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
	tunIfIndex         int
	bypassGateway      string
	bypassInterfaceIdx int
	routeDefs          []probeLocalWindowsRouteDef
}{}

var (
	probeLocalWindowsRunCommand = runProbeLocalCommand
)

func applyProbeLocalProxyTakeover() error {
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
	probeLocalWindowsTakeoverState.enabled = false
	probeLocalWindowsTakeoverState.routePrintOutput = ""
	probeLocalWindowsTakeoverState.tunGateway = ""
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
		routeDefs = probeLocalWindowsTakeoverRouteDefs(gateway, ifIndex)
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
		return dedupeProbeLocalIPv4Strings(backup.DNSServers)
	}
	probeLocalWindowsTakeoverState.mu.Lock()
	tunIfIndex := probeLocalWindowsTakeoverState.tunIfIndex
	probeLocalWindowsTakeoverState.mu.Unlock()
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

func applyProbeLocalTUNPrimaryDNS() error {
	_, tunIfIndex, err := resolveProbeLocalWindowsRouteTarget()
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
	adapter, err := probeLocalResolveWindowsPrimaryDNSAdapter(tunIfIndex)
	if err != nil {
		return fmt.Errorf("resolve windows primary dns adapter failed: %w", err)
	}
	if strings.TrimSpace(adapter.AdapterGUID) == "" {
		return errors.New("primary dns adapter guid is empty")
	}
	backup, exists := loadProbeLocalTUNPrimaryDNSBackupBestEffort()
	if !exists || !strings.EqualFold(strings.TrimSpace(backup.InterfaceGUID), strings.TrimSpace(adapter.AdapterGUID)) {
		backup = probeLocalTUNPrimaryDNSBackup{
			Version:        1,
			InterfaceIndex: adapter.InterfaceIndex,
			InterfaceGUID:  strings.TrimSpace(adapter.AdapterGUID),
			InterfaceName:  strings.TrimSpace(adapter.Name),
			DNSServers:     dedupeProbeLocalIPv4Strings(adapter.DNSServers),
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
	backup.DNSServers = dedupeProbeLocalIPv4Strings(backup.DNSServers)
	backup.AppliedDNS = dedupeProbeLocalIPv4Strings(backup.AppliedDNS)
	return backup, backup.InterfaceGUID != ""
}

func persistProbeLocalTUNPrimaryDNSBackup(backup probeLocalTUNPrimaryDNSBackup) error {
	if backup.Version <= 0 {
		backup.Version = 1
	}
	backup.InterfaceGUID = strings.TrimSpace(backup.InterfaceGUID)
	backup.DNSServers = dedupeProbeLocalIPv4Strings(backup.DNSServers)
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
