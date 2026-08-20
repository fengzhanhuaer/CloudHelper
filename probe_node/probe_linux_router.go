//go:build linux_router

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
	probeLinuxRouterPlatformFailOpen = func(snapshot probeLinuxRouterSnapshot) error { return nil }
	probeLinuxRouterPlatformCleanup  = func(snapshot *probeLinuxRouterSnapshot) error { return nil }
	probeLinuxRouterPlatformHealthy  = func(snapshot probeLinuxRouterSnapshot) error { return nil }
)

var probeLinuxRouterRuntimeState = struct {
	mu             sync.RWMutex
	nodeID         string
	desired        *probeLinuxRouterSnapshot
	report         probeLinuxRouterRuntimeReport
	stopCh         chan struct{}
	running        bool
	manualFailOpen bool
}{}

type probeLinuxRouterLocalConfig struct {
	GatewayProxy probeLinuxRouterGatewayConfig `json:"gateway_proxy"`
	LocalIPProxy probeLinuxRouterLocalIPConfig `json:"local_ip_proxy"`
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
	probeLinuxRouterRouteConfigApplier = applyProbeLinuxRouterSnapshot
	probeProductLinuxRouterReport = currentProbeLinuxRouterReport
	probeProductAllowsForwardedTUNPacket = probeLinuxRouterAllowsForwardedTUNPacket
	probeProductHandleDirectTUNPacket = probeLinuxRouterHandleDirectTUNPacket
	probeProductTargetsLocalDelivery = probeLinuxRouterTargetsLocalDelivery
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
	probeLinuxRouterRuntimeState.mu.Lock()
	if probeLinuxRouterRuntimeState.running {
		close(probeLinuxRouterRuntimeState.stopCh)
		probeLinuxRouterRuntimeState.running = false
	}
	desired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	probeLinuxRouterRuntimeState.mu.Unlock()
	if desired != nil && desired.GatewayProxy.Enabled {
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
	if !desired.GatewayProxy.Enabled && !desired.LocalIPProxy.Enabled {
		err := probeLinuxRouterPlatformCleanup(desired)
		setProbeLinuxRouterReport(desired, "", false, err)
		return err
	}
	iface, err := probeLinuxRouterPlatformApply(*desired)
	if err != nil {
		failOpen := false
		if desired.GatewayProxy.Enabled {
			failOpen = true
			if fallbackErr := probeLinuxRouterPlatformFailOpen(*desired); fallbackErr != nil {
				err = errors.Join(err, fmt.Errorf("fail-open: %w", fallbackErr))
			}
		} else if cleanupErr := probeLinuxRouterPlatformCleanup(desired); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup failed local IP proxy state: %w", cleanupErr))
		}
		setProbeLinuxRouterReport(desired, iface, failOpen, err)
		return err
	}
	setProbeLinuxRouterReport(desired, iface, false, nil)
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
	if manualFailOpen || desired == nil || (!desired.GatewayProxy.Enabled && !desired.LocalIPProxy.Enabled) {
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
		if desired.GatewayProxy.Enabled {
			_ = probeLinuxRouterPlatformFailOpen(*desired)
		}
		setProbeLinuxRouterReport(desired, report.Interface, desired.GatewayProxy.Enabled, err)
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
	if enabled && !desired.GatewayProxy.Enabled && !desired.LocalIPProxy.Enabled {
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
	config.GatewayProxy.GatewayAddress = normalizeProbeLinuxRouterGatewayAddress(config.GatewayProxy.GatewayAddress, config.GatewayProxy.Interface)
	config.GatewayProxy.UpstreamGateway = strings.TrimSpace(config.GatewayProxy.UpstreamGateway)
	config.GatewayProxy.LANCIDRs = normalizeProbeLinuxRouterLocalCIDRs(config.GatewayProxy.LANCIDRs)
	if len(config.GatewayProxy.LANCIDRs) == 0 {
		if subnet := probeLinuxRouterGatewaySubnet(config.GatewayProxy.GatewayAddress); subnet != "" {
			config.GatewayProxy.LANCIDRs = []string{subnet}
		}
	}
	config.LocalIPProxy.PublishedCIDRs = normalizeProbeLinuxRouterLocalCIDRs(config.LocalIPProxy.PublishedCIDRs)
	if len(config.LocalIPProxy.PublishedCIDRs) == 0 {
		config.LocalIPProxy.PublishedCIDRs = append([]string(nil), config.GatewayProxy.LANCIDRs...)
	}
	config.LocalIPProxy.AllowedNodeIDs = normalizeProbeLinuxRouterLocalNodeIDs(config.LocalIPProxy.AllowedNodeIDs)
	if config.LocalIPProxy.Enabled && len(config.LocalIPProxy.PublishedCIDRs) == 0 {
		return &probeLinuxRouterLocalConfigError{err: errors.New("at least one published CIDR is required when local IP proxy is enabled")}
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
	if err := persistProbeLinuxRouterSnapshot(desired); err != nil {
		return err
	}

	probeLinuxRouterRuntimeState.mu.Lock()
	probeLinuxRouterRuntimeState.desired = cloneProbeLinuxRouterSnapshot(desired)
	probeLinuxRouterRuntimeState.manualFailOpen = false
	probeLinuxRouterRuntimeState.mu.Unlock()
	return reconcileProbeLinuxRouterRuntime()
}

func normalizeProbeLinuxRouterGatewayAddress(value, interfaceName string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "/") {
		return value
	}
	address, err := netip.ParseAddr(value)
	if err != nil || !address.Is4() {
		return value
	}

	prefixBits := 24
	interfaces, err := net.Interfaces()
	if err == nil {
		selectedInterface := strings.TrimSpace(interfaceName)
		for _, iface := range interfaces {
			if selectedInterface != "" && selectedInterface != "auto" && iface.Name != selectedInterface {
				continue
			}
			interfaceAddresses, addressErr := iface.Addrs()
			if addressErr != nil {
				continue
			}
			for _, interfaceAddress := range interfaceAddresses {
				prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(interfaceAddress.String()))
				if parseErr == nil && prefix.Addr().Is4() && prefix.Contains(address) {
					prefixBits = prefix.Bits()
					return netip.PrefixFrom(address, prefixBits).String()
				}
			}
		}
	}
	return netip.PrefixFrom(address, prefixBits).String()
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

func setProbeLinuxRouterReport(snapshot *probeLinuxRouterSnapshot, iface string, failOpen bool, applyErr error) {
	report := probeLinuxRouterRuntimeReport{Healthy: applyErr == nil, FailOpen: failOpen, Interface: strings.TrimSpace(iface), UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	if snapshot != nil {
		report.AppliedRevision = snapshot.Revision
		report.AppliedSHA256 = strings.ToLower(strings.TrimSpace(snapshot.SHA256))
		report.GatewayProxyEnabled = snapshot.GatewayProxy.Enabled
		report.LocalIPProxyEnabled = snapshot.LocalIPProxy.Enabled
		report.GatewayAddress = snapshot.GatewayProxy.GatewayAddress
		report.PublishedCIDRs = append([]string(nil), snapshot.LocalIPProxy.PublishedCIDRs...)
		report.AllowedNodeIDs = append([]string(nil), snapshot.LocalIPProxy.AllowedNodeIDs...)
	}
	if applyErr != nil {
		report.LastApplyError = strings.TrimSpace(applyErr.Error())
	}
	probeLinuxRouterRuntimeState.mu.Lock()
	probeLinuxRouterRuntimeState.report = report
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
	probeLinuxRouterRuntimeState.mu.RLock()
	desired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	probeLinuxRouterRuntimeState.mu.RUnlock()
	if desired == nil {
		return false
	}
	return (desired.GatewayProxy.Enabled && probeLinuxRouterIPInCIDRs(sourceIP, desired.GatewayProxy.LANCIDRs)) ||
		(desired.LocalIPProxy.Enabled && probeLinuxRouterIPInCIDRs(sourceIP, desired.LocalIPProxy.PublishedCIDRs))
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

func probeLinuxRouterTargetsLocalDelivery(dstIP string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(dstIP))
	if err != nil || !addr.Is4() {
		return false
	}
	probeLinuxRouterRuntimeState.mu.RLock()
	desired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	probeLinuxRouterRuntimeState.mu.RUnlock()
	if desired == nil {
		return false
	}
	return (desired.GatewayProxy.Enabled && probeLinuxRouterIPInCIDRs(addr, desired.GatewayProxy.LANCIDRs)) ||
		(desired.LocalIPProxy.Enabled && probeLinuxRouterIPInCIDRs(addr, desired.LocalIPProxy.PublishedCIDRs))
}

func setProbeLinuxRouterReportFromCurrent(err error) {
	probeLinuxRouterRuntimeState.mu.RLock()
	desired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	iface := probeLinuxRouterRuntimeState.report.Interface
	probeLinuxRouterRuntimeState.mu.RUnlock()
	setProbeLinuxRouterReport(desired, iface, desired != nil && desired.GatewayProxy.Enabled, err)
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
	gateway, err := netip.ParsePrefix(strings.TrimSpace(snapshot.GatewayProxy.GatewayAddress))
	if err != nil || !gateway.Addr().Is4() || !gateway.Addr().IsPrivate() {
		return errors.New("router gateway_address must be a private IPv4 CIDR")
	}
	upstream, err := netip.ParseAddr(strings.TrimSpace(snapshot.GatewayProxy.UpstreamGateway))
	if err != nil || !gateway.Masked().Contains(upstream) || upstream == gateway.Addr() {
		return errors.New("router upstream_gateway is outside the gateway subnet")
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
	if snapshot.LocalIPProxy.Enabled && len(snapshot.LocalIPProxy.PublishedCIDRs) == 0 {
		return errors.New("router published CIDRs are required when local IP proxy is enabled")
	}
	if snapshot.LocalIPProxy.Enabled && len(snapshot.LocalIPProxy.AllowedNodeIDs) == 0 {
		return errors.New("router allowed node IDs are required when local IP proxy is enabled")
	}
	expectedSHA := probeLinuxRouterSnapshotSHA256(*snapshot)
	if strings.TrimSpace(snapshot.SHA256) == "" || !strings.EqualFold(snapshot.SHA256, expectedSHA) {
		return errors.New("router config sha256 does not match payload")
	}
	return nil
}

func probeLinuxRouterSnapshotSHA256(snapshot probeLinuxRouterSnapshot) string {
	payload := struct {
		NodeID       string                        `json:"node_id"`
		GatewayProxy probeLinuxRouterGatewayConfig `json:"gateway_proxy"`
		LocalIPProxy probeLinuxRouterLocalIPConfig `json:"local_ip_proxy"`
	}{snapshot.NodeID, snapshot.GatewayProxy, snapshot.LocalIPProxy}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func persistProbeLinuxRouterSnapshot(snapshot *probeLinuxRouterSnapshot) error {
	dataDir, err := resolveDataDir()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
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
	var snapshot probeLinuxRouterSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func cloneProbeLinuxRouterSnapshot(snapshot *probeLinuxRouterSnapshot) *probeLinuxRouterSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := *snapshot
	clone.GatewayProxy.LANCIDRs = append([]string(nil), snapshot.GatewayProxy.LANCIDRs...)
	clone.LocalIPProxy.PublishedCIDRs = append([]string(nil), snapshot.LocalIPProxy.PublishedCIDRs...)
	clone.LocalIPProxy.AllowedNodeIDs = append([]string(nil), snapshot.LocalIPProxy.AllowedNodeIDs...)
	sort.Strings(clone.GatewayProxy.LANCIDRs)
	sort.Strings(clone.LocalIPProxy.PublishedCIDRs)
	sort.Strings(clone.LocalIPProxy.AllowedNodeIDs)
	return &clone
}
