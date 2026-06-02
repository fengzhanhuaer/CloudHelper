package main

import (
	"errors"
	"testing"
	"time"
)

func TestResolveProbeLocalProxyRouteDecisionByDomainFallbackDirect(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}

	decision := resolveProbeLocalProxyRouteDecisionByDomain("unmatched.example")
	if decision.Group != "fallback" {
		t.Fatalf("group=%q", decision.Group)
	}
	if decision.Action != "direct" {
		t.Fatalf("action=%q", decision.Action)
	}
	if decision.Reject {
		t.Fatal("reject should be false")
	}
}

func TestResolveProbeLocalProxyRouteDecisionByDomainTunnel(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "media", Rules: []string{"domain_suffix:example.com"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}

	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "media", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	decision := resolveProbeLocalProxyRouteDecisionByDomain("api.example.com")
	if decision.Group != "media" {
		t.Fatalf("group=%q", decision.Group)
	}
	if decision.Action != "tunnel" {
		t.Fatalf("action=%q", decision.Action)
	}
	if decision.SelectedChainID != "chain-proxy-1" {
		t.Fatalf("selected_chain_id=%q", decision.SelectedChainID)
	}
	if decision.TunnelNodeID != "chain:chain-proxy-1" {
		t.Fatalf("tunnel_node_id=%q", decision.TunnelNodeID)
	}
	if decision.Reject {
		t.Fatal("reject should be false")
	}
}

func TestResolveProbeLocalProxyRouteDecisionByDomainReject(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "blocked", Rules: []string{"domain_suffix:blocked.example"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}

	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "blocked", Action: "reject"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	decision := resolveProbeLocalProxyRouteDecisionByDomain("x.blocked.example")
	if decision.Group != "blocked" {
		t.Fatalf("group=%q", decision.Group)
	}
	if decision.Action != "reject" {
		t.Fatalf("action=%q", decision.Action)
	}
	if !decision.Reject {
		t.Fatal("reject should be true")
	}
}

func TestOpenProbeLocalExplicitProxyTunnelStreamUsesProxyGroupRejectRule(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "blocked", Rules: []string{"domain_suffix:blocked.example"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}

	state := defaultProbeLocalProxyStateFile()
	state.Proxy.Enabled = true
	state.Proxy.Mode = probeLocalProxyModeTUN
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "blocked", Action: "reject"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	conn, err := openProbeLocalExplicitProxyTunnelStream("tcp", "api.blocked.example:443")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("expected no connection for rejected route")
	}
	var rejectErr *probeLocalRouteRejectError
	if !errors.As(err, &rejectErr) {
		t.Fatalf("expected route reject error, got %v", err)
	}
	if rejectErr.Group != "blocked" {
		t.Fatalf("reject group=%q", rejectErr.Group)
	}
}

func TestResolveProbeLocalProxyRouteDecisionByIPCidrTunnel(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "telegram", Rules: []string{"cidr:91.108.4.0/22"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}

	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "telegram", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	decision := resolveProbeLocalProxyRouteDecisionByIP("91.108.4.10")
	if decision.Group != "telegram" {
		t.Fatalf("group=%q", decision.Group)
	}
	if decision.Action != "tunnel" {
		t.Fatalf("action=%q", decision.Action)
	}
	if decision.SelectedChainID != "chain-proxy-1" {
		t.Fatalf("selected_chain_id=%q", decision.SelectedChainID)
	}
	if decision.TunnelNodeID != "chain:chain-proxy-1" {
		t.Fatalf("tunnel_node_id=%q", decision.TunnelNodeID)
	}

	outside := resolveProbeLocalProxyRouteDecisionByIP("91.108.8.1")
	if outside.Group != "fallback" || outside.Action != "direct" {
		t.Fatalf("outside decision=%+v", outside)
	}
}

func TestResolveProbeLocalProxyRouteDecisionByIPPrefersDNSDirectHint(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "direct-site", Rules: []string{"domain_suffix:example.com"}},
		{Group: "cdn-cidr", Rules: []string{"cidr:203.0.113.0/24"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}

	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "direct-site", Action: "direct"},
		{Group: "cdn-cidr", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	domainDecision := resolveProbeLocalProxyRouteDecisionByDomain("www.example.com")
	if domainDecision.Group != "direct-site" || domainDecision.Action != "direct" {
		t.Fatalf("domain decision=%+v", domainDecision)
	}
	storeProbeLocalDNSRouteHints("www.example.com", []string{"203.0.113.10"}, domainDecision)

	ipDecision := resolveProbeLocalProxyRouteDecisionByIP("203.0.113.10")
	if ipDecision.Group != "direct-site" || ipDecision.Action != "direct" {
		t.Fatalf("ip decision should prefer dns direct hint, got %+v", ipDecision)
	}
	if ipDecision.SelectedChainID != "" || ipDecision.TunnelNodeID != "" {
		t.Fatalf("direct hint should not carry tunnel chain: %+v", ipDecision)
	}
}

func TestResolveProbeLocalProxyRouteDecisionByIPCIDROverridesFallbackDNSHint(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "telegram", Rules: []string{"cidr:149.154.160.0/20"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}

	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "fallback", Action: "direct"},
		{Group: "telegram", Action: "tunnel", SelectedChainID: "5_pub", TunnelNodeID: "chain:5_pub"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	fallbackDecision := resolveProbeLocalProxyRouteDecisionByDomain("unmatched.example")
	if fallbackDecision.Group != "fallback" || fallbackDecision.Action != "direct" {
		t.Fatalf("fallback decision=%+v", fallbackDecision)
	}
	storeProbeLocalDNSRouteHints("unmatched.example", []string{"149.154.175.54"}, fallbackDecision)

	decision := resolveProbeLocalProxyRouteDecisionByIP("149.154.175.54")
	if decision.Group != "telegram" || decision.Action != "tunnel" {
		t.Fatalf("decision should prefer tunnel cidr over fallback hint, got %+v", decision)
	}
	if decision.SelectedChainID != "5_pub" {
		t.Fatalf("selected_chain_id=%q", decision.SelectedChainID)
	}
}

func TestResolveProbeLocalProxyRouteDecisionByIPLoadsPersistedDNSRouteHint(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "media", Rules: []string{"domain_suffix:example.com"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "media", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}
	now := time.Now().UTC()
	if err := persistProbeLocalDNSCacheRecordsToDisk([]probeLocalDNSPersistRecord{{
		Domain:    "api.example.com",
		Group:     "media",
		RealIPs:   []string{"203.0.113.20"},
		UpdatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}}, probeLocalFakeIPDefaultCIDR); err != nil {
		t.Fatalf("persist dns cache failed: %v", err)
	}
	probeLocalDNSState.mu.Lock()
	probeLocalDNSState.cacheLoaded = false
	probeLocalDNSState.cache = make(map[string]probeLocalDNSUnifiedRecord)
	probeLocalDNSState.routeIPHints = make(map[string]probeLocalDNSRouteHintEntry)
	probeLocalDNSState.routeHints = make(map[string]probeLocalDNSRouteHintEntry)
	probeLocalDNSState.fakeDomainToIP = make(map[string]string)
	probeLocalDNSState.fakeIPToEntry = make(map[string]probeLocalDNSFakeIPRuntimeEntry)
	probeLocalDNSState.mu.Unlock()

	decision := resolveProbeLocalProxyRouteDecisionByIP("203.0.113.20")
	if decision.Group != "media" || decision.Action != "tunnel" {
		t.Fatalf("decision=%+v", decision)
	}
	if decision.SelectedChainID != "chain-proxy-1" || decision.TunnelNodeID != "chain:chain-proxy-1" {
		t.Fatalf("decision chain=%+v", decision)
	}
}

func TestProbeLocalTunnelCIDRRulesOnlyIncludesTunnelGroups(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "telegram", Rules: []string{"cidr:91.108.4.0/22"}},
		{Group: "direct-only", Rules: []string{"cidr:203.0.113.0/24"}},
		{Group: "bad", Rules: []string{"cidr:not-a-cidr"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}

	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "telegram", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"},
		{Group: "direct-only", Action: "direct"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	rules := probeLocalTunnelCIDRRules()
	if len(rules) != 1 || rules[0] != "91.108.4.0/22" {
		t.Fatalf("cidr rules=%v", rules)
	}
}
