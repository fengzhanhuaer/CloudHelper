//go:build linux_router

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	probeLinuxRouterConfigFileName = "probe_linux_router_config.json"
	probeLinuxRouterHealthInterval = 20 * time.Second
)

var probeLinuxRouterInterfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,32}$`)

var (
	probeLinuxRouterPlatformApply = func(snapshot probeLinuxRouterSnapshot) (string, error) {
		return "", errors.New("linux router platform is unavailable")
	}
	probeLinuxRouterPlatformResolve = func(snapshot probeLinuxRouterSnapshot) (probeLinuxRouterSnapshot, error) {
		return snapshot, nil
	}
	probeLinuxRouterPlatformPrepare              = func() error { return nil }
	probeLinuxRouterPlatformFailOpen             = func(snapshot probeLinuxRouterSnapshot) error { return nil }
	probeLinuxRouterPlatformCleanup              = func(snapshot *probeLinuxRouterSnapshot) error { return nil }
	probeLinuxRouterPlatformHealthy              = func(snapshot probeLinuxRouterSnapshot) error { return nil }
	probeLinuxRouterPlatformRestoreInterfaceAuto = func(string) (string, bool, error) {
		return "", false, errors.New("linux router network configuration is unavailable")
	}
)

var probeLinuxRouterRuntimeState = struct {
	mu             sync.RWMutex
	nodeID         string
	desired        *probeLinuxRouterSnapshot
	report         probeLinuxRouterRuntimeReport
	stopCh         chan struct{}
	running        bool
	manualFailOpen bool
	applied        *probeLinuxRouterSnapshot
}{}

type probeLinuxRouterLocalConfig struct {
	GatewayProxy probeLinuxRouterGatewayConfig `json:"gateway_proxy"`
	LocalIPProxy probeLinuxRouterLocalIPConfig `json:"local_ip_proxy"`
	OneArmRouter probeLinuxRouterOneArmConfig  `json:"one_arm_router"`
}

type probeLinuxRouterPersistedConfig struct {
	Version      int                           `json:"version"`
	NodeID       string                        `json:"node_id"`
	Revision     int64                         `json:"revision"`
	GatewayProxy probeLinuxRouterGatewayConfig `json:"gateway_proxy"`
	LocalIPProxy probeLinuxRouterLocalIPConfig `json:"local_ip_proxy"`
	OneArmRouter probeLinuxRouterOneArmConfig  `json:"one_arm_router"`
}

type probeLinuxRouterLocalConfigError struct {
	err error
}

func (e *probeLinuxRouterLocalConfigError) Error() string {
	return e.err.Error()
}

func (e *probeLinuxRouterLocalConfigError) Unwrap() error {
	return e.err
}

func init() {
	probeMihomoASNDatabaseEnabled = true
	probeProductActivateMihomoASNDatabase = activateProbeLinuxRouterASNDatabase
	probeLinuxRouterRouteConfigApplier = applyProbeLinuxRouterSnapshot
	probeProductLinuxRouterReport = currentProbeLinuxRouterReport
	probeProductAllowsForwardedTUNPacket = probeLinuxRouterAllowsForwardedTUNPacket
	probeProductRejectsTUNPacket = probeLinuxRouterRejectsTUNPacket
	probeProductHandleDirectTUNPacket = probeLinuxRouterHandleDirectTUNPacket
	probeProductTargetsLocalDelivery = probeLinuxRouterTargetsLocalDelivery
	probeProductVirtualRouterConfigApplied = probeLinuxRouterVirtualRouterConfigApplied
	probeProductOwnsFakeIPRoute = probeLinuxRouterOwnsFakeIPRoute
	probeProductVirtualRouterRouteRuleEntryMatchesIP = probeLinuxRouterRouteRuleEntryMatchesIP
}

func applyProbeProductRouteConfig(snapshot *probeSpecialExitSnapshot, nodeID string) error {
	return applyProbeMihomoRouteConfig(snapshot, nodeID, false)
}

func probeProductSpecialExitReport() probeSpecialExitRuntimeReport {
	return probeMihomoSpecialExitReport()
}

func startProbeProductRuntime(nodeID string) error {
	// The router dataplane remains available even when the optional Mihomo
	// companion has no snapshot yet or cannot start.
	_ = startProbeMihomoRuntime(nodeID, false)
	if err := probeLinuxRouterPlatformPrepare(); err != nil {
		logProbeWarnf("prepare linux router DHCP integration failed: %v", err)
	}
	probeLinuxRouterRuntimeState.mu.Lock()
	probeLinuxRouterRuntimeState.nodeID = strings.TrimSpace(nodeID)
	if !probeLinuxRouterRuntimeState.running {
		probeLinuxRouterRuntimeState.stopCh = make(chan struct{})
		probeLinuxRouterRuntimeState.running = true
		go probeLinuxRouterHealthLoop(probeLinuxRouterRuntimeState.stopCh)
	}
	probeLinuxRouterRuntimeState.mu.Unlock()

	if cached, err := loadProbeLinuxRouterSnapshot(); err == nil && cached != nil {
		if err := validateProbeLinuxRouterSnapshot(cached, nodeID); err != nil {
			return fmt.Errorf("cached router config is invalid: %w", err)
		}
		probeLinuxRouterRuntimeState.mu.Lock()
		probeLinuxRouterRuntimeState.desired = cloneProbeLinuxRouterSnapshot(cached)
		probeLinuxRouterRuntimeState.mu.Unlock()
		go reconcileProbeLinuxRouterRuntime()
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func stopProbeProductRuntime() {
	stopProbeMihomoRuntime()
	closeProbeLinuxRouterASNDatabase()
	resetProbeLinuxRouterSNIState()
	resetProbeLinuxRouterQUICState()
	probeLinuxRouterRuntimeState.mu.Lock()
	if probeLinuxRouterRuntimeState.running {
		close(probeLinuxRouterRuntimeState.stopCh)
		probeLinuxRouterRuntimeState.running = false
	}
	desired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	probeLinuxRouterRuntimeState.mu.Unlock()
	if desired != nil && (desired.GatewayProxy.Enabled || desired.OneArmRouter.Enabled) {
		_ = probeLinuxRouterPlatformFailOpen(*desired)
	} else {
		_ = probeLinuxRouterPlatformCleanup(desired)
	}
}

func applyProbeLinuxRouterSnapshot(_ *probeLinuxRouterSnapshot, nodeID string) error {
	// Router settings are local-only. Ignore controller snapshots so an older
	// controller cannot overwrite the local file during a rolling upgrade.
	probeLinuxRouterRuntimeState.mu.Lock()
	probeLinuxRouterRuntimeState.nodeID = strings.TrimSpace(nodeID)
	probeLinuxRouterRuntimeState.mu.Unlock()
	return nil
}

func reconcileProbeLinuxRouterRuntime() error {
	probeLinuxRouterRuntimeState.mu.RLock()
	desired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	manualFailOpen := probeLinuxRouterRuntimeState.manualFailOpen
	probeLinuxRouterRuntimeState.mu.RUnlock()
	if desired == nil {
		return nil
	}
	if manualFailOpen {
		err := probeLinuxRouterPlatformFailOpen(*desired)
		setProbeLinuxRouterReport(desired, currentProbeLinuxRouterReport().Interface, true, err)
		return err
	}
	if !probeLinuxRouterAnyModeEnabled(*desired) {
		err := probeLinuxRouterPlatformCleanup(desired)
		setProbeLinuxRouterReport(desired, "", false, err)
		return err
	}
	effective, err := probeLinuxRouterPlatformResolve(*desired)
	if err != nil {
		failOpen := false
		if desired.GatewayProxy.Enabled || desired.OneArmRouter.Enabled {
			failOpen = true
			if fallbackErr := probeLinuxRouterPlatformFailOpen(*desired); fallbackErr != nil {
				err = errors.Join(err, fmt.Errorf("fail-open: %w", fallbackErr))
			}
		}
		setProbeLinuxRouterReport(desired, "", failOpen, err)
		return err
	}
	iface, err := probeLinuxRouterPlatformApply(effective)
	if err != nil {
		failOpen := false
		if desired.GatewayProxy.Enabled || desired.OneArmRouter.Enabled {
			failOpen = true
			if fallbackErr := probeLinuxRouterPlatformFailOpen(*desired); fallbackErr != nil {
				err = errors.Join(err, fmt.Errorf("fail-open: %w", fallbackErr))
			}
		} else if cleanupErr := probeLinuxRouterPlatformCleanup(desired); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup failed local IP proxy state: %w", cleanupErr))
		}
		setProbeLinuxRouterReport(&effective, iface, failOpen, err)
		return err
	}
	setProbeLinuxRouterReport(&effective, iface, false, nil)
	return nil
}

func probeLinuxRouterHealthLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(probeLinuxRouterHealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			probeLinuxRouterHealthCheckOnce()
		}
	}
}

func probeLinuxRouterHealthCheckOnce() {
	probeLinuxRouterRuntimeState.mu.RLock()
	desired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	report := probeLinuxRouterRuntimeState.report
	manualFailOpen := probeLinuxRouterRuntimeState.manualFailOpen
	probeLinuxRouterRuntimeState.mu.RUnlock()
	if manualFailOpen || desired == nil || !probeLinuxRouterAnyModeEnabled(*desired) {
		return
	}

	// Fail-open intentionally removes policy rules. Rebuild them before checking
	// health so a startup attempt made before the TUN is ready can recover.
	if report.FailOpen || !report.Healthy {
		if err := reconcileProbeLinuxRouterRuntime(); err != nil {
			return
		}
		report = currentProbeLinuxRouterReport()
	}
	if err := probeLinuxRouterPlatformHealthy(*desired); err != nil {
		if reconcileErr := reconcileProbeLinuxRouterRuntime(); reconcileErr == nil {
			if retryErr := probeLinuxRouterPlatformHealthy(*desired); retryErr == nil {
				return
			} else {
				err = retryErr
			}
		} else {
			return
		}
		if desired.GatewayProxy.Enabled || desired.OneArmRouter.Enabled {
			_ = probeLinuxRouterPlatformFailOpen(*desired)
		}
		setProbeLinuxRouterReport(desired, report.Interface, desired.GatewayProxy.Enabled || desired.OneArmRouter.Enabled, err)
	}
}

func probeLinuxRouterSNATCIDRs(config probeVirtualRouterConfig) []string {
	seen := make(map[string]struct{})
	var out []string
	appendCIDR := func(raw string) {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || !prefix.Addr().Is4() {
			return
		}
		value := prefix.Masked().String()
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if cidr := strings.TrimSpace(config.FakeIPCIDR); cidr != "" {
		appendCIDR(cidr)
	} else {
		appendCIDR(probeLocalFakeIPDefaultCIDR)
	}
	for _, rule := range config.RouteRules {
		action := sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID)
		if action != "probe_exit" && action != "reject" {
			continue
		}
		for _, entry := range rule.Entries {
			kind, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
			if ok && strings.EqualFold(strings.TrimSpace(kind), "cidr") {
				appendCIDR(value)
			}
		}
	}
	sort.Strings(out)
	return out
}

func currentProbeLinuxRouterLocalState() (*probeLinuxRouterSnapshot, bool, string) {
	probeLinuxRouterRuntimeState.mu.RLock()
	defer probeLinuxRouterRuntimeState.mu.RUnlock()
	return cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired), probeLinuxRouterRuntimeState.manualFailOpen, probeLinuxRouterRuntimeState.nodeID
}

func setProbeLinuxRouterManualFailOpen(enabled bool) error {
	probeLinuxRouterRuntimeState.mu.Lock()
	desired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	if desired == nil {
		probeLinuxRouterRuntimeState.mu.Unlock()
		return errors.New("router config is not available")
	}
	if enabled && !probeLinuxRouterAnyModeEnabled(*desired) {
		probeLinuxRouterRuntimeState.mu.Unlock()
		return errors.New("router data plane is already disabled")
	}
	probeLinuxRouterRuntimeState.manualFailOpen = enabled
	probeLinuxRouterRuntimeState.mu.Unlock()
	return reconcileProbeLinuxRouterRuntime()
}

func applyProbeLinuxRouterLocalConfig(config probeLinuxRouterLocalConfig) error {
	config.GatewayProxy.Interface = strings.TrimSpace(config.GatewayProxy.Interface)
	if config.GatewayProxy.Interface == "" {
		config.GatewayProxy.Interface = "auto"
	}
	// The physical interface address belongs to the upstream DHCP/static network
	// configuration. It is discovered at runtime and is never owned here.
	config.GatewayProxy.GatewayAddress = ""
	config.GatewayProxy.UpstreamGateway = strings.TrimSpace(config.GatewayProxy.UpstreamGateway)
	config.GatewayProxy.LANCIDRs = normalizeProbeLinuxRouterLocalCIDRs(config.GatewayProxy.LANCIDRs)
	config.GatewayProxy.DNSWhitelistIPs = normalizeProbeLinuxRouterDNSWhitelistIPs(config.GatewayProxy.DNSWhitelistIPs)
	config.GatewayProxy.DNSWhitelistDomains = normalizeProbeLinuxRouterDNSWhitelistDomains(config.GatewayProxy.DNSWhitelistDomains)
	config.LocalIPProxy.PublishedCIDRs = normalizeProbeLinuxRouterLocalCIDRs(config.LocalIPProxy.PublishedCIDRs)
	if len(config.LocalIPProxy.PublishedCIDRs) == 0 {
		config.LocalIPProxy.PublishedCIDRs = append([]string(nil), config.GatewayProxy.LANCIDRs...)
	}
	config.LocalIPProxy.AllowedNodeIDs = normalizeProbeLinuxRouterLocalNodeIDs(config.LocalIPProxy.AllowedNodeIDs)
	config.OneArmRouter.SubnetCIDR = normalizeProbeLinuxRouterOneArmSubnet(config.OneArmRouter.SubnetCIDR)
	if config.OneArmRouter.Enabled && (config.GatewayProxy.Enabled || config.LocalIPProxy.Enabled) {
		return &probeLinuxRouterLocalConfigError{err: errors.New("side router and one-arm router modes cannot be enabled at the same time")}
	}
	if config.LocalIPProxy.Enabled && len(config.LocalIPProxy.AllowedNodeIDs) == 0 {
		return &probeLinuxRouterLocalConfigError{err: errors.New("at least one allowed node is required when local IP proxy is enabled")}
	}

	probeLinuxRouterRuntimeState.mu.RLock()
	nodeID := strings.TrimSpace(probeLinuxRouterRuntimeState.nodeID)
	desired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	probeLinuxRouterRuntimeState.mu.RUnlock()
	if nodeID == "" {
		return errors.New("router identity is not available")
	}
	if desired == nil {
		desired = &probeLinuxRouterSnapshot{
			Version: 1, NodeID: nodeID, Revision: 1,
		}
	}
	previousSHA := strings.ToLower(strings.TrimSpace(desired.SHA256))
	desired.Version = 1
	desired.NodeID = nodeID
	desired.GatewayProxy = config.GatewayProxy
	desired.LocalIPProxy = config.LocalIPProxy
	desired.OneArmRouter = config.OneArmRouter
	nextSHA := probeLinuxRouterSnapshotSHA256(*desired)
	if previousSHA != "" && !strings.EqualFold(previousSHA, nextSHA) {
		desired.Revision++
	}
	if desired.Revision < 1 {
		desired.Revision = 1
	}
	desired.SHA256 = nextSHA
	if err := validateProbeLinuxRouterSnapshot(desired, nodeID); err != nil {
		return &probeLinuxRouterLocalConfigError{err: err}
	}
	if probeLinuxRouterAnyModeEnabled(*desired) {
		if _, err := probeLinuxRouterPlatformResolve(*desired); err != nil {
			return &probeLinuxRouterLocalConfigError{err: err}
		}
	}
	if err := persistProbeLinuxRouterSnapshot(desired); err != nil {
		return err
	}

	probeLinuxRouterRuntimeState.mu.Lock()
	probeLinuxRouterRuntimeState.desired = cloneProbeLinuxRouterSnapshot(desired)
	probeLinuxRouterRuntimeState.manualFailOpen = false
	probeLinuxRouterRuntimeState.mu.Unlock()
	return reconcileProbeLinuxRouterRuntime()
}

func probeLinuxRouterSideModeEnabled(snapshot probeLinuxRouterSnapshot) bool {
	return snapshot.GatewayProxy.Enabled || snapshot.LocalIPProxy.Enabled
}

func probeLinuxRouterAnyModeEnabled(snapshot probeLinuxRouterSnapshot) bool {
	return probeLinuxRouterSideModeEnabled(snapshot) || snapshot.OneArmRouter.Enabled
}

func probeLinuxRouterOwnsFakeIPRoute() bool {
	probeLinuxRouterRuntimeState.mu.RLock()
	defer probeLinuxRouterRuntimeState.mu.RUnlock()
	return probeLinuxRouterRuntimeState.desired != nil && probeLinuxRouterAnyModeEnabled(*probeLinuxRouterRuntimeState.desired)
}

func normalizeProbeLinuxRouterOneArmSubnet(value string) string {
	value = strings.TrimSpace(value)
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.Addr().Is4() {
		return value
	}
	return prefix.Masked().String()
}

func probeLinuxRouterOneArmGatewayCIDR(value string) (string, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Bits() < 8 || prefix.Bits() > 30 {
		return "", errors.New("one-arm router subnet must be a private IPv4 CIDR with prefix length 8 through 30")
	}
	prefix = prefix.Masked()
	fakePool := netip.MustParsePrefix("198.18.0.0/15")
	if probeLinuxRouterPrefixesOverlap(prefix, fakePool) {
		return "", errors.New("one-arm router subnet overlaps the virtual router fake IP pool")
	}
	return netip.PrefixFrom(prefix.Addr().Next(), prefix.Bits()).String(), nil
}

func probeLinuxRouterPrefixesOverlap(left, right netip.Prefix) bool {
	return left.Contains(right.Addr()) || right.Contains(left.Addr())
}

func probeLinuxRouterGatewaySubnet(value string) string {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || !prefix.Addr().Is4() {
		return ""
	}
	return prefix.Masked().String()
}

func normalizeProbeLinuxRouterLocalNodeIDs(values []string) []string {
	probeLinuxRouterRuntimeState.mu.RLock()
	localNodeID := normalizeProbeRouteNodeID(probeLinuxRouterRuntimeState.nodeID)
	probeLinuxRouterRuntimeState.mu.RUnlock()
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		nodeID := normalizeProbeRouteNodeID(raw)
		if nodeID == "" || nodeID == localNodeID {
			continue
		}
		if _, ok := seen[nodeID]; ok {
			continue
		}
		seen[nodeID] = struct{}{}
		out = append(out, nodeID)
	}
	sort.Strings(out)
	return out
}

func normalizeProbeLinuxRouterLocalCIDRs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			value := strings.TrimSpace(raw)
			if value != "" {
				out = append(out, value)
			}
			continue
		}
		value := prefix.Masked().String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeProbeLinuxRouterDNSWhitelistIPs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if addr, err := netip.ParseAddr(value); err == nil {
			value = addr.Unmap().String()
		}
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeProbeLinuxRouterDNSWhitelistDomains(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.Trim(strings.TrimSpace(raw), "."))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func validateProbeLinuxRouterDNSWhitelistDomain(domain string) bool {
	if len(domain) > 253 || !strings.Contains(domain, ".") || normalizeProbeVirtualRouterDomain(domain) != domain {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if char != '-' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
				return false
			}
		}
	}
	return true
}

func setProbeLinuxRouterReport(snapshot *probeLinuxRouterSnapshot, iface string, failOpen bool, applyErr error) {
	report := probeLinuxRouterRuntimeReport{Healthy: applyErr == nil, FailOpen: failOpen, Interface: strings.TrimSpace(iface), UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	if snapshot != nil {
		report.AppliedRevision = snapshot.Revision
		report.AppliedSHA256 = strings.ToLower(strings.TrimSpace(snapshot.SHA256))
		report.GatewayProxyEnabled = snapshot.GatewayProxy.Enabled
		report.LocalIPProxyEnabled = snapshot.LocalIPProxy.Enabled
		report.OneArmRouterEnabled = snapshot.OneArmRouter.Enabled
		report.GatewayAddress = snapshot.GatewayProxy.GatewayAddress
		report.UpstreamGateway = snapshot.GatewayProxy.UpstreamGateway
		report.OneArmSubnetCIDR = snapshot.OneArmRouter.SubnetCIDR
		if gateway, err := probeLinuxRouterOneArmGatewayCIDR(snapshot.OneArmRouter.SubnetCIDR); err == nil {
			report.OneArmGateway = gateway
		}
		report.PublishedCIDRs = append([]string(nil), snapshot.LocalIPProxy.PublishedCIDRs...)
		report.AllowedNodeIDs = append([]string(nil), snapshot.LocalIPProxy.AllowedNodeIDs...)
	}
	if applyErr != nil {
		report.LastApplyError = strings.TrimSpace(applyErr.Error())
	}
	probeLinuxRouterRuntimeState.mu.Lock()
	probeLinuxRouterRuntimeState.report = report
	if applyErr == nil && !failOpen && snapshot != nil {
		probeLinuxRouterRuntimeState.applied = cloneProbeLinuxRouterSnapshot(snapshot)
	} else {
		probeLinuxRouterRuntimeState.applied = nil
	}
	probeLinuxRouterRuntimeState.mu.Unlock()
	_, _ = triggerProbeImmediateReport()
}

func currentProbeLinuxRouterReport() probeLinuxRouterRuntimeReport {
	probeLinuxRouterRuntimeState.mu.RLock()
	defer probeLinuxRouterRuntimeState.mu.RUnlock()
	report := probeLinuxRouterRuntimeState.report
	report.PublishedCIDRs = append([]string(nil), report.PublishedCIDRs...)
	report.AllowedNodeIDs = append([]string(nil), report.AllowedNodeIDs...)
	stats := probeVirtualRouterTUNDataPlaneStatsSnapshot()
	report.TUNRXPackets = stats.RXPackets
	report.TUNRXBytes = stats.RXBytes
	report.TUNTXPackets = stats.TXPackets
	report.TUNTXBytes = stats.TXBytes
	report.LatencyMS = currentProbeLinuxRouterLatencyMS()
	return report
}

func currentProbeLinuxRouterLatencyMS() int64 {
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	defer probeVirtualRouterRuntimeStatsState.mu.Unlock()
	var best int64
	for _, stats := range probeVirtualRouterRuntimeStatsState.items {
		if stats == nil || stats.LastPingLatencyMS <= 0 || strings.TrimSpace(stats.LastPingError) != "" {
			continue
		}
		if best == 0 || stats.LastPingLatencyMS < best {
			best = stats.LastPingLatencyMS
		}
	}
	return best
}

func probeLinuxRouterAllowsForwardedTUNPacket(packet []byte, _ string, _ []string) bool {
	sourceIP, err := netip.ParseAddr(strings.TrimSpace(probeVirtualRouterIPv4Source(packet)))
	if err != nil || !sourceIP.Is4() {
		return false
	}
	snapshot := currentProbeLinuxRouterRoutingSnapshot()
	if snapshot == nil {
		return false
	}
	return (snapshot.GatewayProxy.Enabled && probeLinuxRouterIPInCIDRs(sourceIP, snapshot.GatewayProxy.LANCIDRs)) ||
		(snapshot.LocalIPProxy.Enabled && probeLinuxRouterIPInCIDRs(sourceIP, snapshot.LocalIPProxy.PublishedCIDRs)) ||
		(snapshot.OneArmRouter.Enabled && probeLinuxRouterIPInCIDRs(sourceIP, []string{snapshot.OneArmRouter.SubnetCIDR}))
}

func probeLinuxRouterHandleDirectTUNPacket(packet []byte, dstIP string) bool {
	if !probeLinuxRouterAllowsForwardedTUNPacket(packet, dstIP, nil) {
		return false
	}
	if rule, ok := currentProbeVirtualRouterRouteRuleForIP(dstIP); ok && sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) == "reject" {
		return true
	}
	if err := writeProbeVirtualRouterTUNPacket(packet); err != nil {
		setProbeLinuxRouterReportFromCurrent(err)
	}
	return true
}

func probeLinuxRouterRejectsTUNPacket(packet []byte, dstIP string, path []string) bool {
	if !probeLinuxRouterAllowsForwardedTUNPacket(packet, dstIP, path) {
		return false
	}
	return probeLinuxRouterSNIRejectsPacket(packet) || probeLinuxRouterQUICRejectsPacket(packet)
}

func probeLinuxRouterTargetsLocalDelivery(dstIP string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(dstIP))
	if err != nil || !addr.Is4() {
		return false
	}
	snapshot := currentProbeLinuxRouterRoutingSnapshot()
	if snapshot == nil {
		return false
	}
	return (snapshot.GatewayProxy.Enabled && probeLinuxRouterIPInCIDRs(addr, snapshot.GatewayProxy.LANCIDRs)) ||
		(snapshot.LocalIPProxy.Enabled && probeLinuxRouterIPInCIDRs(addr, snapshot.LocalIPProxy.PublishedCIDRs)) ||
		(snapshot.OneArmRouter.Enabled && probeLinuxRouterIPInCIDRs(addr, []string{snapshot.OneArmRouter.SubnetCIDR}))
}

func currentProbeLinuxRouterRoutingSnapshot() *probeLinuxRouterSnapshot {
	probeLinuxRouterRuntimeState.mu.RLock()
	defer probeLinuxRouterRuntimeState.mu.RUnlock()
	if probeLinuxRouterRuntimeState.applied != nil {
		return cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.applied)
	}
	return cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
}

func setProbeLinuxRouterReportFromCurrent(err error) {
	probeLinuxRouterRuntimeState.mu.RLock()
	desired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	iface := probeLinuxRouterRuntimeState.report.Interface
	probeLinuxRouterRuntimeState.mu.RUnlock()
	setProbeLinuxRouterReport(desired, iface, desired != nil && (desired.GatewayProxy.Enabled || desired.OneArmRouter.Enabled), err)
}

func probeLinuxRouterIPInCIDRs(addr netip.Addr, cidrs []string) bool {
	for _, raw := range cidrs {
		if prefix, err := netip.ParsePrefix(strings.TrimSpace(raw)); err == nil && prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func validateProbeLinuxRouterSnapshot(snapshot *probeLinuxRouterSnapshot, nodeID string) error {
	if snapshot == nil || snapshot.Version != 1 {
		return errors.New("router config version must be 1")
	}
	if strings.TrimSpace(snapshot.NodeID) == "" || strings.TrimSpace(snapshot.NodeID) != strings.TrimSpace(nodeID) {
		return errors.New("router config node_id does not match local identity")
	}
	if snapshot.Revision < 1 {
		return errors.New("router config revision must be positive")
	}
	iface := strings.TrimSpace(snapshot.GatewayProxy.Interface)
	if iface != "auto" && !probeLinuxRouterInterfacePattern.MatchString(iface) {
		return errors.New("router interface is invalid")
	}
	if raw := strings.TrimSpace(snapshot.GatewayProxy.GatewayAddress); raw != "" {
		gateway, err := netip.ParsePrefix(raw)
		if err != nil || !gateway.Addr().Is4() || !gateway.Addr().IsPrivate() {
			return errors.New("router gateway_address must be empty or a private IPv4 CIDR")
		}
	}
	if raw := strings.TrimSpace(snapshot.GatewayProxy.UpstreamGateway); raw != "" {
		upstream, err := netip.ParseAddr(raw)
		if err != nil || !upstream.Is4() || !upstream.IsPrivate() {
			return errors.New("router upstream_gateway must be empty or a private IPv4 address")
		}
	}
	fakePool := netip.MustParsePrefix("198.18.0.0/15")
	for _, values := range [][]string{snapshot.GatewayProxy.LANCIDRs, snapshot.LocalIPProxy.PublishedCIDRs} {
		if len(values) > 32 {
			return errors.New("router config allows at most 32 CIDRs per list")
		}
		for _, raw := range values {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
			if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Bits() < 8 {
				return fmt.Errorf("router CIDR %q is invalid", raw)
			}
			prefix = prefix.Masked()
			if prefix.Contains(fakePool.Addr()) || fakePool.Contains(prefix.Addr()) {
				return fmt.Errorf("router CIDR %q overlaps the virtual router fake IP pool", raw)
			}
		}
	}
	if len(snapshot.GatewayProxy.DNSWhitelistIPs)+len(snapshot.GatewayProxy.DNSWhitelistDomains) > 64 {
		return errors.New("router DNS whitelist allows at most 64 IPs and domains")
	}
	for _, raw := range snapshot.GatewayProxy.DNSWhitelistIPs {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil || !addr.Is4() || addr.IsUnspecified() || addr.IsMulticast() {
			return fmt.Errorf("router DNS whitelist IP %q is invalid", raw)
		}
	}
	for _, domain := range snapshot.GatewayProxy.DNSWhitelistDomains {
		if !validateProbeLinuxRouterDNSWhitelistDomain(domain) {
			return fmt.Errorf("router DNS whitelist domain %q is invalid", domain)
		}
	}
	if snapshot.LocalIPProxy.Enabled && len(snapshot.LocalIPProxy.AllowedNodeIDs) == 0 {
		return errors.New("router allowed node IDs are required when local IP proxy is enabled")
	}
	if snapshot.OneArmRouter.Enabled && probeLinuxRouterSideModeEnabled(*snapshot) {
		return errors.New("side router and one-arm router modes cannot be enabled at the same time")
	}
	if snapshot.OneArmRouter.Enabled {
		if _, err := probeLinuxRouterOneArmGatewayCIDR(snapshot.OneArmRouter.SubnetCIDR); err != nil {
			return err
		}
	}
	return nil
}

func probeLinuxRouterSnapshotSHA256(snapshot probeLinuxRouterSnapshot) string {
	payload := struct {
		NodeID       string                        `json:"node_id"`
		GatewayProxy probeLinuxRouterGatewayConfig `json:"gateway_proxy"`
		LocalIPProxy probeLinuxRouterLocalIPConfig `json:"local_ip_proxy"`
		OneArmRouter probeLinuxRouterOneArmConfig  `json:"one_arm_router"`
	}{snapshot.NodeID, snapshot.GatewayProxy, snapshot.LocalIPProxy, snapshot.OneArmRouter}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func persistProbeLinuxRouterSnapshot(snapshot *probeLinuxRouterSnapshot) error {
	dataDir, err := resolveDataDir()
	if err != nil {
		return err
	}
	persisted := probeLinuxRouterPersistedConfig{
		Version:      snapshot.Version,
		NodeID:       snapshot.NodeID,
		Revision:     snapshot.Revision,
		GatewayProxy: snapshot.GatewayProxy,
		LocalIPProxy: snapshot.LocalIPProxy,
		OneArmRouter: snapshot.OneArmRouter,
	}
	raw, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, probeLinuxRouterConfigFileName), raw, 0o600)
}

func loadProbeLinuxRouterSnapshot() (*probeLinuxRouterSnapshot, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, probeLinuxRouterConfigFileName))
	if err != nil {
		return nil, err
	}
	var persisted probeLinuxRouterPersistedConfig
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return nil, err
	}
	snapshot := probeLinuxRouterSnapshot{
		Version:      persisted.Version,
		NodeID:       persisted.NodeID,
		Revision:     persisted.Revision,
		GatewayProxy: persisted.GatewayProxy,
		LocalIPProxy: persisted.LocalIPProxy,
		OneArmRouter: persisted.OneArmRouter,
	}
	migrateProbeLinuxRouterAutomaticNetworkConfig(&snapshot)
	snapshot.SHA256 = probeLinuxRouterSnapshotSHA256(snapshot)
	return &snapshot, nil
}

func migrateProbeLinuxRouterAutomaticNetworkConfig(snapshot *probeLinuxRouterSnapshot) {
	if snapshot == nil {
		return
	}
	legacySubnet := probeLinuxRouterGatewaySubnet(snapshot.GatewayProxy.GatewayAddress)
	snapshot.GatewayProxy.GatewayAddress = ""
	if legacySubnet == "" {
		return
	}
	if len(snapshot.GatewayProxy.LANCIDRs) == 1 && snapshot.GatewayProxy.LANCIDRs[0] == legacySubnet {
		snapshot.GatewayProxy.LANCIDRs = nil
	}
	if len(snapshot.LocalIPProxy.PublishedCIDRs) == 1 && snapshot.LocalIPProxy.PublishedCIDRs[0] == legacySubnet {
		snapshot.LocalIPProxy.PublishedCIDRs = nil
	}
}

func cloneProbeLinuxRouterSnapshot(snapshot *probeLinuxRouterSnapshot) *probeLinuxRouterSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := *snapshot
	clone.GatewayProxy.LANCIDRs = append([]string(nil), snapshot.GatewayProxy.LANCIDRs...)
	clone.GatewayProxy.DNSWhitelistIPs = append([]string(nil), snapshot.GatewayProxy.DNSWhitelistIPs...)
	clone.GatewayProxy.DNSWhitelistDomains = append([]string(nil), snapshot.GatewayProxy.DNSWhitelistDomains...)
	clone.LocalIPProxy.PublishedCIDRs = append([]string(nil), snapshot.LocalIPProxy.PublishedCIDRs...)
	clone.LocalIPProxy.AllowedNodeIDs = append([]string(nil), snapshot.LocalIPProxy.AllowedNodeIDs...)
	sort.Strings(clone.GatewayProxy.LANCIDRs)
	sort.Strings(clone.GatewayProxy.DNSWhitelistIPs)
	sort.Strings(clone.GatewayProxy.DNSWhitelistDomains)
	sort.Strings(clone.LocalIPProxy.PublishedCIDRs)
	sort.Strings(clone.LocalIPProxy.AllowedNodeIDs)
	return &clone
}
