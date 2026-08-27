//go:build linux_router

package main

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testProbeLinuxRouterIPv4Packet(src [4]byte, dst [4]byte) []byte {
	packet := make([]byte, 20)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[9] = 17
	copy(packet[12:16], src[:])
	copy(packet[16:20], dst[:])
	return packet
}

func TestValidateProbeLinuxRouterSnapshotUsesSemanticValidation(t *testing.T) {
	snapshot := probeLinuxRouterSnapshot{
		Version: 1, NodeID: "21", Revision: 2,
		GatewayProxy: probeLinuxRouterGatewayConfig{Interface: "auto", UpstreamGateway: "192.168.1.1", LANCIDRs: []string{"192.168.1.0/24"}, DNSEnabled: true},
		LocalIPProxy: probeLinuxRouterLocalIPConfig{PublishedCIDRs: []string{"192.168.1.0/24"}, AllowedNodeIDs: []string{"1"}},
	}
	snapshot.SHA256 = "stale-local-fingerprint"
	if err := validateProbeLinuxRouterSnapshot(&snapshot, "21"); err != nil {
		t.Fatal(err)
	}
	snapshot.GatewayProxy.UpstreamGateway = "203.0.113.1"
	if err := validateProbeLinuxRouterSnapshot(&snapshot, "21"); err == nil {
		t.Fatal("semantically invalid snapshot unexpectedly accepted")
	}
}

func TestProbeLinuxRouterOneArmGatewayAndModeValidation(t *testing.T) {
	gateway, err := probeLinuxRouterOneArmGatewayCIDR("192.168.205.77/24")
	if err != nil || gateway != "192.168.205.1/24" {
		t.Fatalf("one-arm gateway=%q err=%v", gateway, err)
	}
	for _, subnet := range []string{"", "198.18.0.0/24", "192.168.1.0/31", "203.0.113.0/24"} {
		if _, err := probeLinuxRouterOneArmGatewayCIDR(subnet); err == nil {
			t.Fatalf("invalid one-arm subnet %q was accepted", subnet)
		}
	}
	snapshot := probeLinuxRouterSnapshot{
		Version: 1, NodeID: "21", Revision: 1,
		GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: true, Interface: "auto"},
		OneArmRouter: probeLinuxRouterOneArmConfig{Enabled: true, SubnetCIDR: "192.168.205.0/24"},
	}
	if err := validateProbeLinuxRouterSnapshot(&snapshot, "21"); err == nil || !strings.Contains(err.Error(), "cannot be enabled") {
		t.Fatalf("mutually exclusive router modes error=%v", err)
	}
}

func TestLoadProbeLinuxRouterSnapshotIgnoresLegacySHA(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PROBE_NODE_DATA_DIR", dataDir)
	const legacyConfig = `{
  "version": 1,
  "node_id": "21",
  "revision": 2,
  "sha256": "stale-after-schema-change",
  "gateway_proxy": {
    "enabled": true,
    "interface": "auto",
    "gateway_address": "192.168.1.150/24",
    "upstream_gateway": "192.168.1.1",
    "lan_cidrs": ["192.168.1.0/24"],
    "dns_enabled": true
  },
  "local_ip_proxy": {
    "enabled": true,
    "published_cidrs": ["192.168.1.0/24"],
    "allowed_node_ids": ["1"]
  }
}`
	configPath := filepath.Join(dataDir, probeLinuxRouterConfigFileName)
	if err := os.WriteFile(configPath, []byte(legacyConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := loadProbeLinuxRouterSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SHA256 != probeLinuxRouterSnapshotSHA256(*snapshot) {
		t.Fatal("runtime router config fingerprint was not rebuilt")
	}
	if snapshot.GatewayProxy.GatewayAddress != "" || len(snapshot.GatewayProxy.LANCIDRs) != 0 || len(snapshot.LocalIPProxy.PublishedCIDRs) != 0 {
		t.Fatalf("legacy automatic network fields were not migrated: %+v", snapshot)
	}
	if err := validateProbeLinuxRouterSnapshot(snapshot, "21"); err != nil {
		t.Fatalf("legacy router config is invalid: %v", err)
	}
	if err := persistProbeLinuxRouterSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"sha256"`) {
		t.Fatalf("local router config still persists sha256: %s", raw)
	}
}

func TestProbeLinuxRouterGatewaySubnet(t *testing.T) {
	if got := probeLinuxRouterGatewaySubnet("192.168.51.105/24"); got != "192.168.51.0/24" {
		t.Fatalf("gateway subnet=%q", got)
	}
}

func TestProbeLinuxRouterDNSWhitelistNormalizationAndValidation(t *testing.T) {
	if got := normalizeProbeLinuxRouterDNSWhitelistIPs([]string{" 8.8.8.8 ", "8.8.8.8", "1.1.1.1"}); !reflect.DeepEqual(got, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Fatalf("normalized DNS whitelist IPs=%v", got)
	}
	if got := normalizeProbeLinuxRouterDNSWhitelistDomains([]string{" DNS.Google. ", "dns.google", "one.one.one.one"}); !reflect.DeepEqual(got, []string{"dns.google", "one.one.one.one"}) {
		t.Fatalf("normalized DNS whitelist domains=%v", got)
	}
	if !validateProbeLinuxRouterDNSWhitelistDomain("dns.google") || validateProbeLinuxRouterDNSWhitelistDomain("https://dns.google/dns-query") {
		t.Fatal("DNS whitelist domain validation is incorrect")
	}
}

func TestValidateProbeLinuxRouterSnapshotRejectsInvalidDNSWhitelistEntry(t *testing.T) {
	snapshot := probeLinuxRouterSnapshot{
		Version: 1, NodeID: "21", Revision: 1,
		GatewayProxy: probeLinuxRouterGatewayConfig{
			Interface: "auto", GatewayAddress: "192.168.1.150/24", UpstreamGateway: "192.168.1.1",
			LANCIDRs: []string{"192.168.1.0/24"}, DNSWhitelistEnabled: true, DNSWhitelistIPs: []string{"not-an-ip"},
		},
	}
	snapshot.SHA256 = probeLinuxRouterSnapshotSHA256(snapshot)
	if err := validateProbeLinuxRouterSnapshot(&snapshot, "21"); err == nil {
		t.Fatal("invalid DNS whitelist IP unexpectedly accepted")
	}
}

func TestProbeLinuxRouterMatchesASNWithLocalDatabaseLookup(t *testing.T) {
	previous := probeLinuxRouterASNForIP
	probeLinuxRouterASNForIP = func(ip net.IP) (uint, bool) {
		if ip.String() != "1.1.1.1" {
			return 0, false
		}
		return 13335, true
	}
	t.Cleanup(func() { probeLinuxRouterASNForIP = previous })

	if !probeLinuxRouterRouteRuleEntryMatchesIP(net.ParseIP("1.1.1.1"), "asn:13335") {
		t.Fatal("router did not match ASN returned by the local database")
	}
	if probeLinuxRouterRouteRuleEntryMatchesIP(net.ParseIP("1.1.1.1"), "asn:15169") {
		t.Fatal("router matched a different ASN")
	}
	if probeLinuxRouterRouteRuleEntryMatchesIP(net.ParseIP("1.1.1.1"), "cidr:1.1.1.0/24") {
		t.Fatal("router ASN hook must not consume non-ASN entries")
	}
}

func TestActivateProbeLinuxRouterASNDatabaseRejectsInvalidCandidate(t *testing.T) {
	dir := t.TempDir()
	candidate := filepath.Join(dir, "candidate.mmdb")
	database := filepath.Join(dir, "ASN.mmdb")
	if err := os.WriteFile(candidate, []byte("not a maxmind database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database, []byte("existing cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := activateProbeLinuxRouterASNDatabase(candidate, database); err == nil {
		t.Fatal("invalid ASN candidate was accepted")
	}
	raw, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "existing cache" {
		t.Fatalf("invalid candidate replaced existing cache: %q", raw)
	}
}

func TestProbeLinuxRouterSNATCIDRsIncludeFakeIPAndRoutedCIDRs(t *testing.T) {
	config := probeVirtualRouterConfig{
		FakeIPCIDR: "198.18.0.0/15",
		RouteRules: []probeVirtualRouterRouteRule{
			{Action: "probe_exit", ExitNodeID: "17", Entries: []string{"domain_suffix:openai.com", "cidr:149.154.160.0/20"}},
			{Action: "reject", Entries: []string{"cidr:203.0.113.9/32"}},
			{Action: "direct", Entries: []string{"cidr:192.0.2.0/24"}},
		},
	}
	want := []string{"149.154.160.0/20", "198.18.0.0/15", "203.0.113.9/32"}
	if got := probeLinuxRouterSNATCIDRs(config); !reflect.DeepEqual(got, want) {
		t.Fatalf("SNAT CIDRs=%v, want %v", got, want)
	}
}

func TestProbeLinuxRouterOnlyAllowsConfiguredLANSource(t *testing.T) {
	oldDesired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	t.Cleanup(func() {
		probeLinuxRouterRuntimeState.mu.Lock()
		probeLinuxRouterRuntimeState.desired = oldDesired
		probeLinuxRouterRuntimeState.mu.Unlock()
	})
	probeLinuxRouterRuntimeState.mu.Lock()
	probeLinuxRouterRuntimeState.desired = &probeLinuxRouterSnapshot{GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: true, LANCIDRs: []string{"192.168.1.0/24"}}}
	probeLinuxRouterRuntimeState.mu.Unlock()
	if !probeLinuxRouterAllowsForwardedTUNPacket(testProbeLinuxRouterIPv4Packet([4]byte{192, 168, 1, 20}, [4]byte{1, 1, 1, 1}), "1.1.1.1", nil) {
		t.Fatal("configured LAN source was rejected")
	}
	if probeLinuxRouterAllowsForwardedTUNPacket(testProbeLinuxRouterIPv4Packet([4]byte{10, 0, 0, 20}, [4]byte{1, 1, 1, 1}), "1.1.1.1", nil) {
		t.Fatal("host/non-LAN source was accepted")
	}
}

func TestProbeLinuxRouterAllowsPublishedSubnetReturnWithGatewayDisabled(t *testing.T) {
	oldDesired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	t.Cleanup(func() {
		probeLinuxRouterRuntimeState.mu.Lock()
		probeLinuxRouterRuntimeState.desired = oldDesired
		probeLinuxRouterRuntimeState.mu.Unlock()
	})
	probeLinuxRouterRuntimeState.mu.Lock()
	probeLinuxRouterRuntimeState.desired = &probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: false, LANCIDRs: []string{"192.168.1.0/24"}},
		LocalIPProxy: probeLinuxRouterLocalIPConfig{Enabled: true, PublishedCIDRs: []string{"192.168.50.0/24"}},
	}
	probeLinuxRouterRuntimeState.mu.Unlock()
	if !probeLinuxRouterAllowsForwardedTUNPacket(testProbeLinuxRouterIPv4Packet([4]byte{192, 168, 50, 20}, [4]byte{198, 18, 0, 3}), "198.18.0.3", nil) {
		t.Fatal("published subnet return packet was rejected when local IP proxy was enabled independently")
	}
	if probeLinuxRouterAllowsForwardedTUNPacket(testProbeLinuxRouterIPv4Packet([4]byte{192, 168, 1, 20}, [4]byte{198, 18, 0, 3}), "198.18.0.3", nil) {
		t.Fatal("gateway LAN source was accepted while gateway proxy was disabled")
	}
}

func TestCurrentProbeLinuxRouterReportUsesBestHealthyAdjacentLatency(t *testing.T) {
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		"vrouter-21-1": {LastPingLatencyMS: 42},
		"vrouter-21-2": {LastPingLatencyMS: 17},
		"vrouter-21-3": {LastPingLatencyMS: 3, LastPingError: "timeout"},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldStats
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	if got := currentProbeLinuxRouterReport().LatencyMS; got != 17 {
		t.Fatalf("latency_ms=%d, want best healthy adjacent RTT 17", got)
	}
}

func TestProbeLinuxRouterIgnoresControllerSnapshot(t *testing.T) {
	probeLinuxRouterRuntimeState.mu.Lock()
	oldNodeID := probeLinuxRouterRuntimeState.nodeID
	oldDesired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	local := &probeLinuxRouterSnapshot{Version: 1, NodeID: "21", Revision: 3, GatewayProxy: probeLinuxRouterGatewayConfig{GatewayAddress: "192.168.1.150/24"}}
	probeLinuxRouterRuntimeState.nodeID = "21"
	probeLinuxRouterRuntimeState.desired = cloneProbeLinuxRouterSnapshot(local)
	probeLinuxRouterRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		probeLinuxRouterRuntimeState.mu.Lock()
		probeLinuxRouterRuntimeState.nodeID = oldNodeID
		probeLinuxRouterRuntimeState.desired = oldDesired
		probeLinuxRouterRuntimeState.mu.Unlock()
	})

	controller := &probeLinuxRouterSnapshot{Version: 1, NodeID: "21", Revision: 99, GatewayProxy: probeLinuxRouterGatewayConfig{GatewayAddress: "192.168.99.1/24"}}
	if err := applyProbeLinuxRouterSnapshot(controller, "21"); err != nil {
		t.Fatal(err)
	}
	desired, _, _ := currentProbeLinuxRouterLocalState()
	if desired == nil || desired.Revision != 3 || desired.GatewayProxy.GatewayAddress != "192.168.1.150/24" {
		t.Fatalf("controller snapshot changed local config: %+v", desired)
	}
	if err := applyProbeLinuxRouterSnapshot(nil, "21"); err != nil {
		t.Fatal(err)
	}
	desired, _, _ = currentProbeLinuxRouterLocalState()
	if desired == nil || desired.Revision != 3 {
		t.Fatalf("nil controller snapshot cleared local config: %+v", desired)
	}
}

func TestProbeLinuxRouterHealthCheckRebuildsFailOpenBeforeHealth(t *testing.T) {
	probeLinuxRouterRuntimeState.mu.Lock()
	oldDesired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	oldReport := probeLinuxRouterRuntimeState.report
	oldManualFailOpen := probeLinuxRouterRuntimeState.manualFailOpen
	probeLinuxRouterRuntimeState.desired = &probeLinuxRouterSnapshot{
		Version: 1,
		NodeID:  "21",
		GatewayProxy: probeLinuxRouterGatewayConfig{
			Enabled: true,
		},
	}
	probeLinuxRouterRuntimeState.report = probeLinuxRouterRuntimeReport{Healthy: false, FailOpen: true}
	probeLinuxRouterRuntimeState.manualFailOpen = false
	probeLinuxRouterRuntimeState.mu.Unlock()

	oldApply := probeLinuxRouterPlatformApply
	oldFailOpen := probeLinuxRouterPlatformFailOpen
	oldHealthy := probeLinuxRouterPlatformHealthy
	t.Cleanup(func() {
		probeLinuxRouterRuntimeState.mu.Lock()
		probeLinuxRouterRuntimeState.desired = oldDesired
		probeLinuxRouterRuntimeState.report = oldReport
		probeLinuxRouterRuntimeState.manualFailOpen = oldManualFailOpen
		probeLinuxRouterRuntimeState.mu.Unlock()
		probeLinuxRouterPlatformApply = oldApply
		probeLinuxRouterPlatformFailOpen = oldFailOpen
		probeLinuxRouterPlatformHealthy = oldHealthy
	})

	var calls []string
	probeLinuxRouterPlatformApply = func(probeLinuxRouterSnapshot) (string, error) {
		calls = append(calls, "apply")
		return "eth0", nil
	}
	probeLinuxRouterPlatformHealthy = func(probeLinuxRouterSnapshot) error {
		calls = append(calls, "health")
		return nil
	}
	probeLinuxRouterPlatformFailOpen = func(probeLinuxRouterSnapshot) error {
		calls = append(calls, "fail-open")
		return nil
	}

	probeLinuxRouterHealthCheckOnce()

	if !reflect.DeepEqual(calls, []string{"apply", "health"}) {
		t.Fatalf("calls=%v, want apply before health", calls)
	}
	report := currentProbeLinuxRouterReport()
	if !report.Healthy || report.FailOpen || report.Interface != "eth0" {
		t.Fatalf("unexpected recovered report: %+v", report)
	}
}

func TestProbeLinuxRouterHealthCheckRepairsDriftBeforeFailOpen(t *testing.T) {
	probeLinuxRouterRuntimeState.mu.Lock()
	oldDesired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	oldReport := probeLinuxRouterRuntimeState.report
	oldManualFailOpen := probeLinuxRouterRuntimeState.manualFailOpen
	probeLinuxRouterRuntimeState.desired = &probeLinuxRouterSnapshot{
		Version:      1,
		NodeID:       "21",
		GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: true},
	}
	probeLinuxRouterRuntimeState.report = probeLinuxRouterRuntimeReport{Healthy: true, Interface: "eth0"}
	probeLinuxRouterRuntimeState.manualFailOpen = false
	probeLinuxRouterRuntimeState.mu.Unlock()

	oldApply := probeLinuxRouterPlatformApply
	oldFailOpen := probeLinuxRouterPlatformFailOpen
	oldHealthy := probeLinuxRouterPlatformHealthy
	t.Cleanup(func() {
		probeLinuxRouterRuntimeState.mu.Lock()
		probeLinuxRouterRuntimeState.desired = oldDesired
		probeLinuxRouterRuntimeState.report = oldReport
		probeLinuxRouterRuntimeState.manualFailOpen = oldManualFailOpen
		probeLinuxRouterRuntimeState.mu.Unlock()
		probeLinuxRouterPlatformApply = oldApply
		probeLinuxRouterPlatformFailOpen = oldFailOpen
		probeLinuxRouterPlatformHealthy = oldHealthy
	})

	var calls []string
	probeLinuxRouterPlatformApply = func(probeLinuxRouterSnapshot) (string, error) {
		calls = append(calls, "apply")
		return "eth0", nil
	}
	healthChecks := 0
	probeLinuxRouterPlatformHealthy = func(probeLinuxRouterSnapshot) error {
		calls = append(calls, "health")
		healthChecks++
		if healthChecks == 1 {
			return errors.New("router proxy CIDR selection changed")
		}
		return nil
	}
	probeLinuxRouterPlatformFailOpen = func(probeLinuxRouterSnapshot) error {
		calls = append(calls, "fail-open")
		return nil
	}

	probeLinuxRouterHealthCheckOnce()

	if !reflect.DeepEqual(calls, []string{"health", "apply", "health"}) {
		t.Fatalf("calls=%v, want health, apply, health", calls)
	}
	report := currentProbeLinuxRouterReport()
	if !report.Healthy || report.FailOpen || report.Interface != "eth0" {
		t.Fatalf("unexpected repaired report: %+v", report)
	}
}
