//go:build linux && linux_router

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeLinuxRouterNFTTable     = "cloudhelper_router"
	probeLinuxRouterRouteTable   = "208"
	probeLinuxRouterRulePriority = "10080"
	probeLinuxRouterPacketMark   = "0x4348"
	probeLinuxRouterDirectTable  = "209"
	probeLinuxRouterDirectRule   = "10081"
	probeLinuxRouterReturnMark   = "0x4349"
)

var probeLinuxRouterLinuxState = struct {
	mu                    sync.Mutex
	interfaceName         string
	networkSignature      string
	sysctlOriginal        map[string]string
	snatCIDRsSignature    string
	dnsWhitelistSignature string
}{}

var probeLinuxRouterRunCommand = probeLocalLinuxRunCommand
var probeLinuxRouterApplyNFTScript = runProbeLinuxRouterNFTScript
var probeLinuxRouterStartDNSService = startProbeVirtualRouterDNSService
var probeLinuxRouterStopDNSService = stopProbeVirtualRouterDNSService
var probeLinuxRouterApplySystemDNS = func() error { return probeVirtualRouterApplySystemDNS() }
var probeLinuxRouterRestoreSystemDNS = func() error { return probeVirtualRouterRestoreSystemDNS() }
var probeLinuxRouterVirtualDNSConfigured = probeVirtualRouterLocalDNSEnabled
var probeLinuxRouterDNSStatus = currentProbeVirtualRouterDNSStatus
var probeLinuxRouterLookupIP = net.DefaultResolver.LookupIPAddr
var probeLinuxRouterDHCPServerDir = "/run/cloudhelper/probe_router/dhcp-server"
var probeLinuxRouterUDHCPCDir = "/etc/udhcpc"

const probeLinuxRouterUDHCPCHook = `#!/bin/sh
set -eu

case "${interface:-}" in
  ""|*[!A-Za-z0-9_.:-]*) exit 0 ;;
esac
server_identifier="${serverid:-}"
case "${server_identifier}" in
  ""|*[!0-9.]*) exit 0 ;;
esac
ip -4 route get "${server_identifier}" >/dev/null 2>&1 || exit 0
state_dir="/run/cloudhelper/probe_router/dhcp-server"
mkdir -p "${state_dir}"
umask 077
printf '%s\n' "${server_identifier}" > "${state_dir}/${interface}.new"
mv -f "${state_dir}/${interface}.new" "${state_dir}/${interface}"
`

func init() {
	probeLinuxRouterPlatformApply = applyProbeLinuxRouterPlatform
	probeLinuxRouterPlatformResolve = resolveProbeLinuxRouterNetwork
	probeLinuxRouterPlatformPrepare = ensureProbeLinuxRouterUDHCPCHooks
	probeLinuxRouterPlatformFailOpen = applyProbeLinuxRouterFailOpen
	probeLinuxRouterPlatformCleanup = cleanupProbeLinuxRouterPlatform
	probeLinuxRouterPlatformHealthy = probeLinuxRouterPlatformHealth
}

func applyProbeLinuxRouterPlatform(snapshot probeLinuxRouterSnapshot) (string, error) {
	tunIP := strings.TrimSpace(currentProbeVirtualRouterLocalIP())
	if tunIP == "" {
		return "", errors.New("virtual router local IP is not ready")
	}
	if err := ensureProbeVirtualRouterPlatformInterfaceIP(tunIP); err != nil {
		return "", fmt.Errorf("prepare virtual router TUN: %w", err)
	}
	tunDev := probeRouteLinuxTUNDeviceName()
	iface, err := resolveProbeLinuxRouterInterface(snapshot.GatewayProxy.Interface, tunDev)
	if err != nil {
		return "", err
	}

	if err := applyProbeLinuxRouterSysctls([][2]string{{"net.ipv4.ip_forward", "1"}, {"net.ipv4.conf.all.rp_filter", "2"}, {"net.ipv4.conf." + iface + ".rp_filter", "2"}, {"net.ipv4.conf." + tunDev + ".rp_filter", "2"}}); err != nil {
		return iface, err
	}
	if err := applyProbeLinuxRouterPolicyRouting(snapshot, iface, tunDev); err != nil {
		return iface, err
	}
	snatCIDRs := probeLinuxRouterSNATCIDRs(currentProbeVirtualRouterConfig())
	dnsWhitelistIPs, err := resolveProbeLinuxRouterDNSWhitelist(snapshot.GatewayProxy)
	if err != nil {
		return iface, err
	}
	if err := replaceProbeLinuxRouterNFTTable(buildProbeLinuxRouterNFTScript(snapshot, iface, tunDev, tunIP, snatCIDRs, dnsWhitelistIPs, false)); err != nil {
		return iface, err
	}
	if err := reconcileProbeLinuxRouterDNSRuntime(snapshot.GatewayProxy.Enabled && snapshot.GatewayProxy.DNSEnabled); err != nil {
		return iface, err
	}
	probeLinuxRouterLinuxState.mu.Lock()
	probeLinuxRouterLinuxState.interfaceName = iface
	probeLinuxRouterLinuxState.networkSignature = probeLinuxRouterNetworkSignature(snapshot, iface)
	probeLinuxRouterLinuxState.snatCIDRsSignature = strings.Join(snatCIDRs, ",")
	probeLinuxRouterLinuxState.dnsWhitelistSignature = strings.Join(dnsWhitelistIPs, ",")
	probeLinuxRouterLinuxState.mu.Unlock()
	return iface, nil
}

func applyProbeLinuxRouterFailOpen(snapshot probeLinuxRouterSnapshot) error {
	tunDev := probeRouteLinuxTUNDeviceName()
	effective, iface, err := resolveProbeLinuxRouterNetworkForMode(snapshot, false)
	if err != nil {
		return err
	}
	if snapshot.GatewayProxy.Enabled {
		_, _ = probeLinuxRouterRunCommand(5*time.Second, "sysctl", "-w", "net.ipv4.ip_forward=1")
	}
	if err := cleanupProbeLinuxRouterPolicyRouting(); err != nil {
		return err
	}
	dnsErr := reconcileProbeLinuxRouterDNSRuntime(false)
	probeLinuxRouterLinuxState.mu.Lock()
	probeLinuxRouterLinuxState.snatCIDRsSignature = ""
	probeLinuxRouterLinuxState.dnsWhitelistSignature = ""
	probeLinuxRouterLinuxState.mu.Unlock()
	nftErr := replaceProbeLinuxRouterNFTTable(buildProbeLinuxRouterNFTScript(effective, iface, tunDev, "", nil, nil, true))
	return errors.Join(dnsErr, nftErr)
}

func cleanupProbeLinuxRouterPlatform(snapshot *probeLinuxRouterSnapshot) error {
	var allErr error
	if err := cleanupProbeLinuxRouterPolicyRouting(); err != nil {
		allErr = errors.Join(allErr, err)
	}
	if _, err := probeLinuxRouterRunCommand(5*time.Second, "nft", "delete", "table", "ip", probeLinuxRouterNFTTable); err != nil && !probeLinuxRouterCommandMissingObject(err) {
		allErr = errors.Join(allErr, err)
	}
	if err := reconcileProbeLinuxRouterDNSRuntime(false); err != nil {
		allErr = errors.Join(allErr, err)
	}
	probeLinuxRouterLinuxState.mu.Lock()
	probeLinuxRouterLinuxState.interfaceName = ""
	probeLinuxRouterLinuxState.networkSignature = ""
	probeLinuxRouterLinuxState.snatCIDRsSignature = ""
	probeLinuxRouterLinuxState.dnsWhitelistSignature = ""
	probeLinuxRouterLinuxState.mu.Unlock()
	if err := restoreProbeLinuxRouterSysctls(); err != nil {
		allErr = errors.Join(allErr, err)
	}
	return allErr
}

func reconcileProbeLinuxRouterDNSRuntime(enabled bool) error {
	if enabled || probeLinuxRouterVirtualDNSConfigured() {
		if err := probeLinuxRouterStartDNSService(); err != nil {
			return fmt.Errorf("start LAN DNS: %w", err)
		}
		if err := probeLinuxRouterApplySystemDNS(); err != nil {
			return fmt.Errorf("apply router system DNS: %w", err)
		}
		return nil
	}
	probeLinuxRouterStopDNSService()
	if err := probeLinuxRouterRestoreSystemDNS(); err != nil {
		return fmt.Errorf("restore router system DNS: %w", err)
	}
	return nil
}

func applyProbeLinuxRouterSysctls(settings [][2]string) error {
	for _, setting := range settings {
		key := strings.TrimSpace(setting[0])
		value := strings.TrimSpace(setting[1])
		if key == "" || value == "" {
			continue
		}
		probeLinuxRouterLinuxState.mu.Lock()
		_, saved := probeLinuxRouterLinuxState.sysctlOriginal[key]
		probeLinuxRouterLinuxState.mu.Unlock()
		if !saved {
			original, err := probeLinuxRouterRunCommand(5*time.Second, "sysctl", "-n", key)
			if err != nil {
				return fmt.Errorf("read %s: %w", key, err)
			}
			original = strings.TrimSpace(original)
			probeLinuxRouterLinuxState.mu.Lock()
			if probeLinuxRouterLinuxState.sysctlOriginal == nil {
				probeLinuxRouterLinuxState.sysctlOriginal = make(map[string]string)
			}
			if _, exists := probeLinuxRouterLinuxState.sysctlOriginal[key]; !exists {
				probeLinuxRouterLinuxState.sysctlOriginal[key] = original
			}
			probeLinuxRouterLinuxState.mu.Unlock()
		}
		if _, err := probeLinuxRouterRunCommand(5*time.Second, "sysctl", "-w", key+"="+value); err != nil {
			return fmt.Errorf("apply %s: %w", key, err)
		}
	}
	return nil
}

func restoreProbeLinuxRouterSysctls() error {
	probeLinuxRouterLinuxState.mu.Lock()
	originals := probeLinuxRouterLinuxState.sysctlOriginal
	probeLinuxRouterLinuxState.sysctlOriginal = nil
	probeLinuxRouterLinuxState.mu.Unlock()
	keys := make([]string, 0, len(originals))
	for key := range originals {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var allErr error
	for _, key := range keys {
		value := strings.TrimSpace(originals[key])
		if value == "" {
			continue
		}
		if _, err := probeLinuxRouterRunCommand(5*time.Second, "sysctl", "-w", key+"="+value); err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("restore %s: %w", key, err))
		}
	}
	return allErr
}

func probeLinuxRouterPlatformHealth(snapshot probeLinuxRouterSnapshot) error {
	if !probeVirtualRouterTUNDataPlaneRunning() {
		return errors.New("virtual router TUN data plane is not running")
	}
	effective, err := resolveProbeLinuxRouterNetwork(snapshot)
	if err != nil {
		return err
	}
	probeLinuxRouterLinuxState.mu.Lock()
	appliedNetwork := probeLinuxRouterLinuxState.networkSignature
	probeLinuxRouterLinuxState.mu.Unlock()
	if probeLinuxRouterNetworkSignature(effective, strings.TrimSpace(effective.GatewayProxy.Interface)) != appliedNetwork {
		return errors.New("router interface address or upstream gateway changed")
	}
	dnsRequired := snapshot.GatewayProxy.Enabled && snapshot.GatewayProxy.DNSEnabled || probeLinuxRouterVirtualDNSConfigured()
	if err := ensureProbeLinuxRouterDNSHealth(dnsRequired); err != nil {
		return err
	}
	if _, err := probeLinuxRouterRunCommand(5*time.Second, "nft", "list", "table", "ip", probeLinuxRouterNFTTable); err != nil {
		return fmt.Errorf("router nftables state is unavailable: %w", err)
	}
	expectedSNATCIDRs := strings.Join(probeLinuxRouterSNATCIDRs(currentProbeVirtualRouterConfig()), ",")
	probeLinuxRouterLinuxState.mu.Lock()
	appliedSNATCIDRs := probeLinuxRouterLinuxState.snatCIDRsSignature
	probeLinuxRouterLinuxState.mu.Unlock()
	if expectedSNATCIDRs != appliedSNATCIDRs {
		return errors.New("router proxy CIDR selection changed")
	}
	if snapshot.GatewayProxy.Enabled && snapshot.GatewayProxy.DNSWhitelistEnabled && len(snapshot.GatewayProxy.DNSWhitelistDomains) > 0 {
		if resolved, resolveErr := resolveProbeLinuxRouterDNSWhitelist(snapshot.GatewayProxy); resolveErr == nil {
			probeLinuxRouterLinuxState.mu.Lock()
			applied := probeLinuxRouterLinuxState.dnsWhitelistSignature
			probeLinuxRouterLinuxState.mu.Unlock()
			if strings.Join(resolved, ",") != applied {
				return errors.New("router DNS whitelist addresses changed")
			}
		}
	}
	if snapshot.GatewayProxy.Enabled || snapshot.LocalIPProxy.Enabled {
		output, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "rule", "show", "priority", probeLinuxRouterRulePriority)
		if err != nil {
			return fmt.Errorf("router policy rule is unavailable: %w", err)
		}
		if !strings.Contains(output, probeLinuxRouterRouteTable) {
			return errors.New("router policy rule is unavailable: expected table 208")
		}
		output, err = probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "rule", "show", "priority", probeLinuxRouterDirectRule)
		if err != nil {
			return fmt.Errorf("router TUN reinjection rule is unavailable: %w", err)
		}
		if !strings.Contains(output, probeLinuxRouterDirectTable) {
			return errors.New("router TUN reinjection rule is unavailable: expected table 209")
		}
	}
	return nil
}

func ensureProbeLinuxRouterDNSHealth(enabled bool) error {
	if !enabled {
		return nil
	}
	if !probeLinuxRouterDNSStatus().Enabled {
		return errors.New("router DNS service is not running")
	}
	// DHCP clients may rewrite resolv.conf after the router has started. Keep
	// the host resolver pointed at the DNS service bound to the virtual TUN IP.
	if err := probeLinuxRouterApplySystemDNS(); err != nil {
		return fmt.Errorf("maintain router system DNS: %w", err)
	}
	return nil
}

func resolveProbeLinuxRouterInterface(configured string, tunDev string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" && configured != "auto" {
		if !probeLinuxRouterInterfacePattern.MatchString(configured) || configured == tunDev {
			return "", errors.New("configured router interface is invalid")
		}
		return configured, nil
	}
	routes, err := listProbeLinuxRouterDefaultRoutes(tunDev)
	if err != nil {
		return "", fmt.Errorf("resolve router LAN interface: %w", err)
	}
	if len(routes) == 0 {
		return "", errors.New("resolved router LAN interface is unavailable")
	}
	target := routes[0].target
	if target.Dev == "" || target.Dev == tunDev || !probeLinuxRouterInterfacePattern.MatchString(target.Dev) {
		return "", errors.New("resolved router LAN interface is invalid")
	}
	return target.Dev, nil
}

func ensureProbeLinuxRouterUDHCPCHooks() error {
	for _, event := range []string{"bound", "renew"} {
		dir := filepath.Join(probeLinuxRouterUDHCPCDir, "post-"+event)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(dir, "90-cloudhelper-probe-router")
		if err := os.WriteFile(path, []byte(probeLinuxRouterUDHCPCHook), 0o755); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o755); err != nil {
			return err
		}
	}
	return nil
}

type probeLinuxRouterDefaultRoute struct {
	target probeVirtualRouterLinuxRouteTarget
	metric uint32
}

func listProbeLinuxRouterDefaultRoutes(excludedDev string) ([]probeLinuxRouterDefaultRoute, error) {
	output, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "route", "show", "default")
	if err != nil {
		return nil, fmt.Errorf("list default routes: %w", err)
	}
	routes := make([]probeLinuxRouterDefaultRoute, 0)
	for _, line := range strings.Split(output, "\n") {
		target, ok := parseProbeVirtualRouterLinuxDefaultRouteLine(line)
		if !ok || target.Dev == strings.TrimSpace(excludedDev) {
			continue
		}
		routes = append(routes, probeLinuxRouterDefaultRoute{target: target, metric: probeLocalLinuxDefaultRouteMetric(line)})
	}
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].metric < routes[j].metric })
	return routes, nil
}

func resolveProbeLinuxRouterNetwork(snapshot probeLinuxRouterSnapshot) (probeLinuxRouterSnapshot, error) {
	effective, _, err := resolveProbeLinuxRouterNetworkForMode(snapshot, true)
	return effective, err
}

func resolveProbeLinuxRouterNetworkForMode(snapshot probeLinuxRouterSnapshot, requireUpstream bool) (probeLinuxRouterSnapshot, string, error) {
	tunDev := probeRouteLinuxTUNDeviceName()
	iface, err := resolveProbeLinuxRouterInterface(snapshot.GatewayProxy.Interface, tunDev)
	if err != nil {
		return snapshot, "", err
	}
	prefixes, err := listProbeLinuxRouterInterfacePrefixes(iface)
	if err != nil {
		return snapshot, iface, err
	}
	routes, err := listProbeLinuxRouterDefaultRoutes(tunDev)
	if err != nil {
		return snapshot, iface, err
	}
	defaultGateway := netip.Addr{}
	for _, route := range routes {
		if route.target.Dev != iface {
			continue
		}
		if candidate, parseErr := netip.ParseAddr(strings.TrimSpace(route.target.Gateway)); parseErr == nil && candidate.Is4() {
			defaultGateway = candidate
			break
		}
	}

	configuredGateway := strings.TrimSpace(snapshot.GatewayProxy.UpstreamGateway)
	upstream := defaultGateway
	userConfigured := configuredGateway != ""
	if userConfigured {
		upstream, err = netip.ParseAddr(configuredGateway)
		if err != nil || !upstream.Is4() {
			return snapshot, iface, errors.New("configured upstream gateway is not a valid IPv4 address")
		}
	}
	prefix, ok := selectProbeLinuxRouterInterfacePrefix(prefixes, upstream)
	if !ok {
		return snapshot, iface, errors.New("router LAN interface has no usable private IPv4 address")
	}
	if requireUpstream {
		if !upstream.IsValid() {
			return snapshot, iface, errors.New("router upstream gateway was not supplied by the user or the system default route")
		}
		if upstream == prefix.Addr() {
			if userConfigured {
				return snapshot, iface, errors.New("configured upstream gateway cannot equal the router interface address")
			}
			upstream, err = readProbeLinuxRouterDHCPServer(iface)
			if err != nil {
				return snapshot, iface, fmt.Errorf("default gateway equals the router interface address: %w", err)
			}
		}
		if upstream == prefix.Addr() || !prefix.Masked().Contains(upstream) {
			return snapshot, iface, errors.New("router upstream gateway is outside the interface subnet")
		}
		snapshot.GatewayProxy.UpstreamGateway = upstream.String()
	}
	snapshot.GatewayProxy.Interface = iface
	snapshot.GatewayProxy.GatewayAddress = prefix.String()
	if len(snapshot.GatewayProxy.LANCIDRs) == 0 {
		snapshot.GatewayProxy.LANCIDRs = []string{prefix.Masked().String()}
	}
	if len(snapshot.LocalIPProxy.PublishedCIDRs) == 0 {
		snapshot.LocalIPProxy.PublishedCIDRs = []string{prefix.Masked().String()}
	}
	return snapshot, iface, nil
}

func listProbeLinuxRouterInterfacePrefixes(iface string) ([]netip.Prefix, error) {
	output, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "-o", "address", "show", "dev", iface)
	if err != nil {
		return nil, fmt.Errorf("read router interface address: %w", err)
	}
	var prefixes []netip.Prefix
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		for index := 0; index+1 < len(fields); index++ {
			if fields[index] != "inet" {
				continue
			}
			prefix, parseErr := netip.ParsePrefix(fields[index+1])
			if parseErr == nil && prefix.Addr().Is4() && prefix.Addr().IsPrivate() {
				prefixes = append(prefixes, prefix)
			}
			break
		}
	}
	return prefixes, nil
}

func selectProbeLinuxRouterInterfacePrefix(prefixes []netip.Prefix, upstream netip.Addr) (netip.Prefix, bool) {
	if upstream.IsValid() {
		for _, prefix := range prefixes {
			if prefix.Contains(upstream) {
				return prefix, true
			}
		}
	}
	if len(prefixes) == 0 {
		return netip.Prefix{}, false
	}
	return prefixes[0], true
}

func readProbeLinuxRouterDHCPServer(iface string) (netip.Addr, error) {
	if !probeLinuxRouterInterfacePattern.MatchString(iface) {
		return netip.Addr{}, errors.New("DHCP interface is invalid")
	}
	raw, err := os.ReadFile(filepath.Join(probeLinuxRouterDHCPServerDir, iface))
	if err != nil {
		return netip.Addr{}, errors.New("DHCP Server Identifier is unavailable; renew the DHCP lease or enter an upstream gateway")
	}
	server, err := netip.ParseAddr(strings.TrimSpace(string(raw)))
	if err != nil || !server.Is4() || !server.IsPrivate() {
		return netip.Addr{}, errors.New("DHCP Server Identifier is invalid")
	}
	return server, nil
}

func probeLinuxRouterNetworkSignature(snapshot probeLinuxRouterSnapshot, iface string) string {
	return strings.Join([]string{strings.TrimSpace(iface), strings.TrimSpace(snapshot.GatewayProxy.GatewayAddress), strings.TrimSpace(snapshot.GatewayProxy.UpstreamGateway)}, "|")
}

func cleanupProbeLinuxRouterPolicyRouting() error {
	var allErr error
	for _, priority := range []string{probeLinuxRouterRulePriority, probeLinuxRouterDirectRule} {
		if _, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "rule", "del", "priority", priority); err != nil && !probeLinuxRouterCommandMissingObject(err) {
			allErr = errors.Join(allErr, fmt.Errorf("delete router policy rule %s: %w", priority, err))
		}
	}
	for _, table := range []string{probeLinuxRouterRouteTable, probeLinuxRouterDirectTable} {
		if _, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "route", "flush", "table", table); err != nil && !probeLinuxRouterCommandMissingObject(err) {
			allErr = errors.Join(allErr, fmt.Errorf("flush router policy table %s: %w", table, err))
		}
	}
	return allErr
}

func applyProbeLinuxRouterPolicyRouting(snapshot probeLinuxRouterSnapshot, iface string, tunDev string) error {
	if err := cleanupProbeLinuxRouterPolicyRouting(); err != nil {
		return err
	}
	if !snapshot.GatewayProxy.Enabled && !snapshot.LocalIPProxy.Enabled {
		return nil
	}
	if _, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "route", "replace", "table", probeLinuxRouterRouteTable, "default", "dev", tunDev); err != nil {
		return fmt.Errorf("apply router policy table: %w", err)
	}
	if _, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "rule", "add", "priority", probeLinuxRouterRulePriority, "fwmark", probeLinuxRouterPacketMark+"/0xffff", "lookup", probeLinuxRouterRouteTable); err != nil {
		return fmt.Errorf("apply router policy rule: %w", err)
	}
	for _, cidr := range probeLinuxRouterPhysicalCIDRs(snapshot) {
		if _, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "route", "replace", "table", probeLinuxRouterDirectTable, cidr, "dev", iface); err != nil {
			return fmt.Errorf("apply router physical route %s: %w", cidr, err)
		}
	}
	if _, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "route", "replace", "table", probeLinuxRouterDirectTable, "default", "via", snapshot.GatewayProxy.UpstreamGateway, "dev", iface); err != nil {
		return fmt.Errorf("apply router configured upstream: %w", err)
	}
	if _, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "rule", "add", "priority", probeLinuxRouterDirectRule, "iif", tunDev, "lookup", probeLinuxRouterDirectTable); err != nil {
		return fmt.Errorf("apply router TUN reinjection rule: %w", err)
	}
	return nil
}

func probeLinuxRouterPhysicalCIDRs(snapshot probeLinuxRouterSnapshot) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(snapshot.GatewayProxy.LANCIDRs)+len(snapshot.LocalIPProxy.PublishedCIDRs))
	appendCIDRs := func(enabled bool, values []string) {
		if !enabled {
			return
		}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	appendCIDRs(snapshot.GatewayProxy.Enabled, snapshot.GatewayProxy.LANCIDRs)
	appendCIDRs(snapshot.LocalIPProxy.Enabled, snapshot.LocalIPProxy.PublishedCIDRs)
	sort.Strings(out)
	return out
}

func replaceProbeLinuxRouterNFTTable(script string) error {
	if _, err := probeLinuxRouterRunCommand(5*time.Second, "nft", "delete", "table", "ip", probeLinuxRouterNFTTable); err != nil && !probeLinuxRouterCommandMissingObject(err) {
		return fmt.Errorf("delete previous router nftables table: %w", err)
	}
	if err := probeLinuxRouterApplyNFTScript(script); err != nil {
		return fmt.Errorf("apply router nftables table: %w", err)
	}
	return nil
}

func resolveProbeLinuxRouterDNSWhitelist(config probeLinuxRouterGatewayConfig) ([]string, error) {
	if !config.Enabled || !config.DNSWhitelistEnabled {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(config.DNSWhitelistIPs)+len(config.DNSWhitelistDomains)*2)
	for _, raw := range config.DNSWhitelistIPs {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err == nil && addr.Is4() {
			seen[addr.Unmap().String()] = struct{}{}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, domain := range config.DNSWhitelistDomains {
		addresses, err := probeLinuxRouterLookupIP(ctx, domain)
		if err != nil {
			return nil, fmt.Errorf("resolve DNS whitelist domain %s: %w", domain, err)
		}
		foundIPv4 := false
		for _, address := range addresses {
			addr, ok := netip.AddrFromSlice(address.IP)
			if !ok || !addr.Unmap().Is4() {
				continue
			}
			foundIPv4 = true
			seen[addr.Unmap().String()] = struct{}{}
		}
		if !foundIPv4 {
			return nil, fmt.Errorf("resolve DNS whitelist domain %s: no IPv4 address", domain)
		}
	}
	out := make([]string, 0, len(seen))
	for address := range seen {
		out = append(out, address)
	}
	sort.Strings(out)
	return out, nil
}

func buildProbeLinuxRouterNFTScript(snapshot probeLinuxRouterSnapshot, iface string, tunDev string, localIP string, snatCIDRs []string, dnsWhitelistIPs []string, failOpen bool) string {
	lanElements := strings.Join(snapshot.GatewayProxy.LANCIDRs, ", ")
	publishedElements := strings.Join(snapshot.LocalIPProxy.PublishedCIDRs, ", ")
	snatElements := strings.Join(snatCIDRs, ", ")
	if lanElements == "" {
		lanElements = "192.168.1.0/24"
	}
	if publishedElements == "" {
		publishedElements = "192.168.1.0/24"
	}
	if snatElements == "" {
		snatElements = probeLocalFakeIPDefaultCIDR
	}
	ifaceQuote := strconv.Quote(iface)
	tunQuote := strconv.Quote(tunDev)
	var rules []string
	rules = append(rules,
		"table ip "+probeLinuxRouterNFTTable+" {",
		"  set lan4 { type ipv4_addr; flags interval; elements = { "+lanElements+" } }",
		"  set published4 { type ipv4_addr; flags interval; elements = { "+publishedElements+" } }",
		"  set routed4 { type ipv4_addr; flags interval; elements = { "+snatElements+" } }",
	)
	if snapshot.GatewayProxy.Enabled && snapshot.GatewayProxy.DNSWhitelistEnabled && !failOpen {
		dnsElements := strings.Join(dnsWhitelistIPs, ", ")
		if dnsElements == "" {
			rules = append(rules, "  set dns_allow4 { type ipv4_addr; }")
		} else {
			rules = append(rules, "  set dns_allow4 { type ipv4_addr; elements = { "+dnsElements+" } }")
		}
	}
	if snapshot.GatewayProxy.Enabled && snapshot.GatewayProxy.DNSEnabled && !failOpen {
		gateway, _ := netip.ParsePrefix(snapshot.GatewayProxy.GatewayAddress)
		rules = append(rules,
			"  chain dstnat { type nat hook prerouting priority dstnat; policy accept;",
			"    iifname "+ifaceQuote+" ip daddr "+gateway.Addr().String()+" udp dport 53 dnat to "+probeVirtualRouterDNSListenHost+":53",
			"    iifname "+ifaceQuote+" ip daddr "+gateway.Addr().String()+" tcp dport 53 dnat to "+probeVirtualRouterDNSListenHost+":53",
			"  }",
		)
	}
	if snapshot.GatewayProxy.Enabled && !failOpen {
		rules = append(rules,
			"  chain preconntrack { type filter hook prerouting priority raw; policy accept;",
			"    iifname "+ifaceQuote+" ip saddr @lan4 ip daddr != @lan4 ip daddr != @routed4 notrack",
			"  }",
		)
	}
	if (snapshot.GatewayProxy.Enabled || snapshot.LocalIPProxy.Enabled) && !failOpen {
		rules = append(rules,
			"  chain premangle { type filter hook prerouting priority mangle; policy accept;",
		)
		if snapshot.LocalIPProxy.Enabled {
			rules = append(rules,
				"    iifname "+tunQuote+" ip daddr @published4 ct mark set "+probeLinuxRouterReturnMark,
				"    iifname "+ifaceQuote+" ct mark "+probeLinuxRouterReturnMark+" meta mark set "+probeLinuxRouterPacketMark,
			)
		}
		if snapshot.GatewayProxy.Enabled {
			rules = append(rules, "    iifname "+ifaceQuote+" ip saddr @lan4 ip daddr != @lan4 meta mark set "+probeLinuxRouterPacketMark)
		}
		rules = append(rules, "  }")
	}
	if snapshot.GatewayProxy.Enabled && snapshot.GatewayProxy.DNSWhitelistEnabled && !failOpen {
		rules = append(rules,
			"  chain dns_guard { type filter hook forward priority filter; policy accept;",
		)
		if len(dnsWhitelistIPs) > 0 {
			rules = append(rules,
				"    iifname "+ifaceQuote+" ip saddr @lan4 ip daddr @dns_allow4 udp dport { 53, 853 } accept",
				"    iifname "+ifaceQuote+" ip saddr @lan4 ip daddr @dns_allow4 tcp dport { 53, 853 } accept",
			)
		}
		rules = append(rules,
			"    iifname "+ifaceQuote+" ip saddr @lan4 udp dport { 53, 853 } drop",
			"    iifname "+ifaceQuote+" ip saddr @lan4 tcp dport { 53, 853 } drop",
			"  }",
		)
	}
	rules = append(rules, "  chain postrouting { type nat hook postrouting priority srcnat; policy accept;")
	if snapshot.GatewayProxy.Enabled {
		if !failOpen && strings.TrimSpace(localIP) != "" {
			rules = append(rules, "    oifname "+tunQuote+" ip saddr @lan4 ip daddr @routed4 snat to "+strings.TrimSpace(localIP))
		}
		rules = append(rules, "    oifname "+ifaceQuote+" ip saddr @lan4 masquerade")
	}
	if snapshot.LocalIPProxy.Enabled && !failOpen {
		rules = append(rules, "    iifname "+tunQuote+" oifname "+ifaceQuote+" ip daddr @published4 masquerade")
	}
	rules = append(rules, "  }", "}")
	return strings.Join(rules, "\n") + "\n"
}

func runProbeLinuxRouterNFTScript(script string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func probeLinuxRouterCommandMissingObject(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "no such file") || strings.Contains(text, "no such process") || strings.Contains(text, "does not exist") || strings.Contains(text, "cannot find")
}
