package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestProbeLocalDNSFakeIPPersistsOnFlushAndReloads(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	decision := probeLocalDNSRouteDecision{Group: "media", Action: "tunnel", SelectedChainID: "chain-1", TunnelNodeID: "chain:chain-1"}
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision)
	if !ok || strings.TrimSpace(fakeIP) == "" {
		t.Fatalf("allocate fake ip failed: ip=%q ok=%v", fakeIP, ok)
	}
	cachePath, err := resolveProbeLocalDNSCachePath()
	if err != nil {
		t.Fatalf("resolve dns cache path failed: %v", err)
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fake ip allocation should wait for batched persist, stat err=%v", err)
	}
	flushProbeLocalDNSCacheToDisk()
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("fake ip allocation should persist cache after flush: %v", err)
	}

	resetProbeLocalDNSServiceForTest()

	entry, ok := lookupProbeLocalDNSFakeIPEntry(fakeIP)
	if !ok {
		t.Fatalf("reloaded fake ip %s not found", fakeIP)
	}
	if entry.Domain != "api.example.com" || entry.FakeIP != fakeIP {
		t.Fatalf("reloaded fake ip entry=%+v", entry)
	}
	reusedIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision)
	if !ok || reusedIP != fakeIP {
		t.Fatalf("reused fake ip=%q ok=%v want=%q", reusedIP, ok, fakeIP)
	}
}

func TestProbeLocalDNSFakeIPSkipsVirtualRouterProbeReserve(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	decision := probeLocalDNSRouteDecision{Group: "media", Action: "tunnel", SelectedChainID: "chain-1", TunnelNodeID: "chain:chain-1"}
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision)
	if !ok {
		t.Fatalf("allocate fake ip failed")
	}
	if fakeIP != "198.18.4.1" {
		t.Fatalf("fake ip=%q, want first ordinary fake ip after reserved 1024 probe IPs", fakeIP)
	}
}

func TestClearProbeLocalDNSUnifiedCacheRemovesPersistedCacheFile(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	decision := probeLocalDNSRouteDecision{Group: "media", Action: "tunnel", SelectedChainID: "chain-1", TunnelNodeID: "chain:chain-1"}
	storeProbeLocalDNSCacheRecords("api.example.com", []string{"203.0.113.20"})
	if fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision); !ok || strings.TrimSpace(fakeIP) == "" {
		t.Fatalf("allocate fake ip failed: ip=%q ok=%v", fakeIP, ok)
	}
	flushProbeLocalDNSCacheToDisk()
	cachePath, err := resolveProbeLocalDNSCachePath()
	if err != nil {
		t.Fatalf("resolve dns cache path failed: %v", err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("expected persisted cache before clear: %v", err)
	}

	flushCalls := 0
	probeLocalFlushSystemDNSCache = func() error {
		flushCalls++
		return nil
	}
	clearProbeLocalDNSUnifiedCache()

	if flushCalls != 1 {
		t.Fatalf("system dns flush calls=%d, want 1", flushCalls)
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache file after clear err=%v, want not exist", err)
	}
	resetProbeLocalDNSServiceForTest()
	if got := queryProbeLocalDNSUnifiedRecords(); len(got) != 0 {
		t.Fatalf("records reloaded after clear=%+v", got)
	}
}

func TestProbeVirtualRouterLocalSettingsMissingFieldKeepsDefaultEnabled(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)
	path, err := resolveProbeVirtualRouterLocalSettingsPath()
	if err != nil {
		t.Fatalf("resolve settings path failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"virtual_router_enabled":false}`), 0o644); err != nil {
		t.Fatalf("write settings failed: %v", err)
	}
	settings := loadProbeVirtualRouterLocalSettings()
	if settings.VirtualRouterEnabled || !settings.VirtualDNSEnabled {
		t.Fatalf("settings=%+v, want router=false dns default true", settings)
	}
}

func TestResolveProbeVirtualRouterDNSResponseUsesControllerFakeIPForExitRule(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:         "rr-reddit",
			Name:       "reddit",
			Action:     "probe_exit",
			ExitNodeID: "9",
			Entries:    []string{"domain_suffix:reddit.com"},
		}},
	})
	defer restore()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	oldRequest := probeRequestRouteFakeIP
	probeRequestRouteFakeIP = func(ctx context.Context, controllerBaseURL string, identity nodeIdentity, domain string, rule probeVirtualRouterRouteRule) (probeVirtualRouterFakeIPEntry, probeVirtualRouterFakeIPLibrary, error) {
		entry := probeVirtualRouterFakeIPEntry{
			Domain:     domain,
			FakeIP:     "198.18.4.9",
			RuleID:     rule.ID,
			Action:     rule.Action,
			ExitNodeID: rule.ExitNodeID,
			ExpiresAt:  time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339),
			UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		}
		return entry, probeVirtualRouterFakeIPLibrary{
			Version:   2,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Items:     []probeVirtualRouterFakeIPEntry{entry},
		}, nil
	}
	rememberProbeVirtualRouterController(nodeIdentity{NodeID: "1", Secret: "secret-1"}, "https://controller.example")
	t.Cleanup(func() {
		probeRequestRouteFakeIP = oldRequest
		rememberProbeVirtualRouterController(nodeIdentity{}, "")
	})

	packet, err := buildProbeLocalDNSQueryA("www.reddit.com")
	if err != nil {
		t.Fatalf("build dns query failed: %v", err)
	}
	response, domain, ips, decision, err := resolveProbeLocalDNSResponse(packet)
	if err != nil {
		t.Fatalf("resolve virtual router dns failed: %v", err)
	}
	if domain != "www.reddit.com" || len(ips) != 0 {
		t.Fatalf("domain=%q ips=%v", domain, ips)
	}
	if decision.Action != "tunnel" || decision.TunnelNodeID != "9" {
		t.Fatalf("decision=%+v", decision)
	}
	if got := strings.Join(extractProbeLocalDNSResponseIPsBestEffort(response), ","); got != "198.18.4.9" {
		t.Fatalf("response fake ip=%q", got)
	}
	if item, ok := currentProbeVirtualRouterFakeIPEntryByDomain("www.reddit.com"); !ok || item.FakeIP != "198.18.4.9" {
		t.Fatalf("cached fake ip entry=%+v ok=%v", item, ok)
	}
}

func TestResolveProbeVirtualRouterDNSResponseDirectAndReject(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		RouteRules: []probeVirtualRouterRouteRule{
			{
				ID:      "rr-direct",
				Name:    "direct",
				Action:  "direct",
				Entries: []string{"domain_suffix:direct.example"},
			},
			{
				ID:      "rr-reject",
				Name:    "reject",
				Action:  "reject",
				Entries: []string{"domain_keyword:block-me"},
			},
		},
	})
	defer restore()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	storeProbeLocalDNSCacheRecords("api.direct.example", []string{"203.0.113.88"})
	directPacket, err := buildProbeLocalDNSQueryA("api.direct.example")
	if err != nil {
		t.Fatalf("build direct dns query failed: %v", err)
	}
	directResponse, _, directIPs, directDecision, err := resolveProbeLocalDNSResponse(directPacket)
	if err != nil {
		t.Fatalf("resolve direct dns failed: %v", err)
	}
	if directDecision.Action != "direct" || strings.Join(directIPs, ",") != "203.0.113.88" {
		t.Fatalf("direct decision=%+v ips=%v", directDecision, directIPs)
	}
	if got := strings.Join(extractProbeLocalDNSResponseIPsBestEffort(directResponse), ","); got != "203.0.113.88" {
		t.Fatalf("direct response ips=%q", got)
	}
	if _, ok := currentProbeVirtualRouterFakeIPEntryByDomain("api.direct.example"); ok {
		t.Fatalf("direct rule should not allocate fake ip")
	}

	rejectPacket, err := buildProbeLocalDNSQueryA("block-me.example")
	if err != nil {
		t.Fatalf("build reject dns query failed: %v", err)
	}
	rejectResponse, _, _, rejectDecision, err := resolveProbeLocalDNSResponse(rejectPacket)
	if err != nil {
		t.Fatalf("resolve reject dns failed: %v", err)
	}
	if !rejectDecision.Reject || dnsResponseRCodeForTest(t, rejectResponse) != dnsmessage.RCodeRefused {
		t.Fatalf("reject decision=%+v rcode=%v", rejectDecision, dnsResponseRCodeForTest(t, rejectResponse))
	}
}

func setProbeVirtualRouterDNSConfigForTest(t *testing.T, config probeVirtualRouterConfig) func() {
	t.Helper()
	probeVirtualRouterState.mu.Lock()
	oldConfig := probeVirtualRouterState.config
	oldLocalNodeID := probeVirtualRouterState.localNodeID
	oldLocalIP := probeVirtualRouterState.localIP
	oldNodeToIP := probeVirtualRouterState.nodeToIP
	oldIPToNode := probeVirtualRouterState.ipToNode
	oldNeighbors := probeVirtualRouterState.neighbors
	oldRulesByID := probeVirtualRouterState.rulesByID
	oldSignature := probeVirtualRouterState.topologySignature
	sanitized := sanitizeProbeVirtualRouterConfigForCache(config)
	index := buildProbeVirtualRouterTopologyIndex(sanitized)
	probeVirtualRouterState.config = sanitized
	probeVirtualRouterState.localNodeID = "1"
	probeVirtualRouterState.localIP = index.nodeToIP["1"]
	probeVirtualRouterState.nodeToIP = index.nodeToIP
	probeVirtualRouterState.ipToNode = index.ipToNode
	probeVirtualRouterState.neighbors = index.neighbors
	probeVirtualRouterState.rulesByID = index.rulesByID
	probeVirtualRouterState.topologySignature = "test"
	probeVirtualRouterState.mu.Unlock()
	return func() {
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = oldConfig
		probeVirtualRouterState.localNodeID = oldLocalNodeID
		probeVirtualRouterState.localIP = oldLocalIP
		probeVirtualRouterState.nodeToIP = oldNodeToIP
		probeVirtualRouterState.ipToNode = oldIPToNode
		probeVirtualRouterState.neighbors = oldNeighbors
		probeVirtualRouterState.rulesByID = oldRulesByID
		probeVirtualRouterState.topologySignature = oldSignature
		probeVirtualRouterState.mu.Unlock()
	}
}

func dnsResponseRCodeForTest(t *testing.T, packet []byte) dnsmessage.RCode {
	t.Helper()
	parser := dnsmessage.Parser{}
	header, err := parser.Start(packet)
	if err != nil {
		t.Fatalf("parse dns response failed: %v", err)
	}
	return header.RCode
}
