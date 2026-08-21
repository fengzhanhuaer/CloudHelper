//go:build linux && linux_router

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
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
	mu                 sync.Mutex
	interfaceName      string
	gatewayAddress     string
	sysctlOriginal     map[string]string
	snatCIDRsSignature string
}{}

var probeLinuxRouterRunCommand = probeLocalLinuxRunCommand
var probeLinuxRouterApplyNFTScript = runProbeLinuxRouterNFTScript
var probeLinuxRouterStartDNSService = startProbeVirtualRouterDNSService
var probeLinuxRouterStopDNSService = stopProbeVirtualRouterDNSService
var probeLinuxRouterApplySystemDNS = func() error { return probeVirtualRouterApplySystemDNS() }
var probeLinuxRouterRestoreSystemDNS = func() error { return probeVirtualRouterRestoreSystemDNS() }
var probeLinuxRouterVirtualDNSConfigured = probeVirtualRouterLocalDNSEnabled
var probeLinuxRouterDNSStatus = currentProbeVirtualRouterDNSStatus

func init() {
	probeLinuxRouterPlatformApply = applyProbeLinuxRouterPlatform
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

	probeLinuxRouterLinuxState.mu.Lock()
	oldInterface := probeLinuxRouterLinuxState.interfaceName
	oldAddress := probeLinuxRouterLinuxState.gatewayAddress
	probeLinuxRouterLinuxState.mu.Unlock()
	if oldAddress != "" && (oldInterface != iface || oldAddress != snapshot.GatewayProxy.GatewayAddress) {
		_, _ = probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "address", "del", oldAddress, "dev", oldInterface)
	}
	if snapshot.GatewayProxy.Enabled {
		if _, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "address", "replace", snapshot.GatewayProxy.GatewayAddress, "dev", iface); err != nil {
			return iface, fmt.Errorf("apply gateway address: %w", err)
		}
	}
	if err := applyProbeLinuxRouterSysctls([][2]string{{"net.ipv4.ip_forward", "1"}, {"net.ipv4.conf.all.rp_filter", "2"}, {"net.ipv4.conf." + iface + ".rp_filter", "2"}, {"net.ipv4.conf." + tunDev + ".rp_filter", "2"}}); err != nil {
		return iface, err
	}
	if err := applyProbeLinuxRouterPolicyRouting(snapshot, iface, tunDev); err != nil {
		return iface, err
	}
	snatCIDRs := probeLinuxRouterSNATCIDRs(currentProbeVirtualRouterConfig())
	if err := replaceProbeLinuxRouterNFTTable(buildProbeLinuxRouterNFTScript(snapshot, iface, tunDev, tunIP, snatCIDRs, false)); err != nil {
		return iface, err
	}
	if err := reconcileProbeLinuxRouterDNSRuntime(snapshot.GatewayProxy.Enabled && snapshot.GatewayProxy.DNSEnabled); err != nil {
		return iface, err
	}
	probeLinuxRouterLinuxState.mu.Lock()
	probeLinuxRouterLinuxState.interfaceName = iface
	if snapshot.GatewayProxy.Enabled {
		probeLinuxRouterLinuxState.gatewayAddress = snapshot.GatewayProxy.GatewayAddress
	} else {
		probeLinuxRouterLinuxState.gatewayAddress = ""
	}
	probeLinuxRouterLinuxState.snatCIDRsSignature = strings.Join(snatCIDRs, ",")
	probeLinuxRouterLinuxState.mu.Unlock()
	return iface, nil
}

func applyProbeLinuxRouterFailOpen(snapshot probeLinuxRouterSnapshot) error {
	tunDev := probeRouteLinuxTUNDeviceName()
	iface, err := resolveProbeLinuxRouterInterface(snapshot.GatewayProxy.Interface, tunDev)
	if err != nil {
		return err
	}
	if snapshot.GatewayProxy.Enabled {
		if _, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "address", "replace", snapshot.GatewayProxy.GatewayAddress, "dev", iface); err != nil {
			return err
		}
		_, _ = probeLinuxRouterRunCommand(5*time.Second, "sysctl", "-w", "net.ipv4.ip_forward=1")
	}
	if err := cleanupProbeLinuxRouterPolicyRouting(); err != nil {
		return err
	}
	dnsErr := reconcileProbeLinuxRouterDNSRuntime(false)
	probeLinuxRouterLinuxState.mu.Lock()
	probeLinuxRouterLinuxState.snatCIDRsSignature = ""
	probeLinuxRouterLinuxState.mu.Unlock()
	nftErr := replaceProbeLinuxRouterNFTTable(buildProbeLinuxRouterNFTScript(snapshot, iface, tunDev, "", nil, true))
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
	iface := probeLinuxRouterLinuxState.interfaceName
	address := probeLinuxRouterLinuxState.gatewayAddress
	probeLinuxRouterLinuxState.interfaceName = ""
	probeLinuxRouterLinuxState.gatewayAddress = ""
	probeLinuxRouterLinuxState.snatCIDRsSignature = ""
	probeLinuxRouterLinuxState.mu.Unlock()
	if address == "" && snapshot != nil && snapshot.GatewayProxy.Enabled {
		address = snapshot.GatewayProxy.GatewayAddress
		resolved, err := resolveProbeLinuxRouterInterface(snapshot.GatewayProxy.Interface, probeRouteLinuxTUNDeviceName())
		if err == nil {
			iface = resolved
		}
	}
	if iface != "" && address != "" {
		if _, err := probeLinuxRouterRunCommand(5*time.Second, "ip", "-4", "address", "del", address, "dev", iface); err != nil && !probeLinuxRouterCommandMissingObject(err) {
			allErr = errors.Join(allErr, err)
		}
	}
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
	target, err := resolveProbeVirtualRouterLinuxPrimaryEgressRoute(tunDev)
	if err != nil {
		return "", fmt.Errorf("resolve router LAN interface: %w", err)
	}
	if target.Dev == "" || target.Dev == tunDev || !probeLinuxRouterInterfacePattern.MatchString(target.Dev) {
		return "", errors.New("resolved router LAN interface is invalid")
	}
	return target.Dev, nil
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

func buildProbeLinuxRouterNFTScript(snapshot probeLinuxRouterSnapshot, iface string, tunDev string, localIP string, snatCIDRs []string, failOpen bool) string {
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
	if snapshot.GatewayProxy.Enabled && snapshot.GatewayProxy.DNSEnabled && !failOpen {
		gateway, _ := netip.ParsePrefix(snapshot.GatewayProxy.GatewayAddress)
		rules = append(rules,
			"  chain dstnat { type nat hook prerouting priority dstnat; policy accept;",
			"    iifname "+ifaceQuote+" ip daddr "+gateway.Addr().String()+" udp dport 53 dnat to "+probeVirtualRouterDNSListenHost+":53",
			"    iifname "+ifaceQuote+" ip daddr "+gateway.Addr().String()+" tcp dport 53 dnat to "+probeVirtualRouterDNSListenHost+":53",
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
