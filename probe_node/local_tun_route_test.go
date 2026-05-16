package main

import (
	"errors"
	"net"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestDecideProbeLocalRouteForTargetDirectWhenProxyDirectMode(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = false
	probeLocalControl.proxy.Mode = probeLocalProxyModeDirect
	probeLocalControl.mu.Unlock()

	route, err := decideProbeLocalRouteForTarget("api.example.com:443")
	if err != nil {
		t.Fatalf("decideProbeLocalRouteForTarget returned error: %v", err)
	}
	if !route.Direct || route.Reject {
		t.Fatalf("route=%+v", route)
	}
}

func TestDecideProbeLocalRouteForTargetTunnelByDomainRule(t *testing.T) {
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

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	route, err := decideProbeLocalRouteForTarget("api.example.com:443")
	if err != nil {
		t.Fatalf("decideProbeLocalRouteForTarget returned error: %v", err)
	}
	if route.Direct || route.Reject {
		t.Fatalf("route=%+v", route)
	}
	if route.Group != "media" {
		t.Fatalf("group=%q", route.Group)
	}
	if route.SelectedChainID != "chain-proxy-1" {
		t.Fatalf("selected_chain_id=%q", route.SelectedChainID)
	}
	if route.TunnelNodeID != "chain:chain-proxy-1" {
		t.Fatalf("tunnel_node_id=%q", route.TunnelNodeID)
	}
	if route.GroupRuntime == nil {
		t.Fatal("group_runtime should not be nil")
	}
}

func TestDecideProbeLocalRouteForTargetRejectByRule(t *testing.T) {
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

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	route, err := decideProbeLocalRouteForTarget("x.blocked.example:443")
	if err == nil {
		t.Fatal("expected reject error")
	}
	var rejectErr *probeLocalRouteRejectError
	if !errors.As(err, &rejectErr) {
		t.Fatalf("unexpected err type: %T err=%v", err, err)
	}
	if !route.Reject {
		t.Fatalf("route=%+v", route)
	}
}

func TestDecideProbeLocalRouteForTargetTunnelByFakeIP(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
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

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
	})
	storeProbeLocalDNSCacheRecords("api.example.com", []string{"203.0.113.44", "203.0.113.45"})

	dnsDecision := resolveProbeLocalProxyRouteDecisionByDomain("api.example.com")
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", dnsDecision)
	if !ok {
		t.Fatal("allocate fake ip failed")
	}
	if net.ParseIP(fakeIP) == nil {
		t.Fatalf("fake ip=%q", fakeIP)
	}

	route, err := decideProbeLocalRouteForTarget(net.JoinHostPort(fakeIP, "443"))
	if err != nil {
		t.Fatalf("decideProbeLocalRouteForTarget returned error: %v", err)
	}
	if route.Direct || route.Reject {
		t.Fatalf("route=%+v", route)
	}
	if route.SelectedChainID != "chain-proxy-1" {
		t.Fatalf("selected_chain_id=%q", route.SelectedChainID)
	}
	if route.TunnelNodeID != "chain:chain-proxy-1" {
		t.Fatalf("tunnel_node_id=%q", route.TunnelNodeID)
	}
	if route.GroupRuntime == nil {
		t.Fatal("group_runtime should not be nil")
	}
	if route.TargetAddr != "203.0.113.44:443" {
		t.Fatalf("target_addr=%q", route.TargetAddr)
	}
	if got := strings.Join(route.TargetAddrs, ","); got != "203.0.113.44:443,203.0.113.45:443" {
		t.Fatalf("target_addrs=%q", got)
	}
	records := queryProbeLocalDNSUnifiedRecords()
	if len(records) != 1 {
		t.Fatalf("unified records len=%d records=%+v", len(records), records)
	}
	record := records[0]
	if strings.TrimSpace(record.FakeIP) != fakeIP {
		t.Fatalf("record fake ip=%q want=%q record=%+v", record.FakeIP, fakeIP, record)
	}
	if got := strings.Join(record.RealIPs, ","); got != "203.0.113.44,203.0.113.45" {
		t.Fatalf("record real ips=%q record=%+v", got, record)
	}
}

func TestProbeLocalTunnelRouteTargetCandidatesDeduplicatesAndKeepsPrimary(t *testing.T) {
	route := probeLocalTunnelRouteDecision{
		TargetAddr:  "203.0.113.45:443",
		TargetAddrs: []string{"203.0.113.44:443", "203.0.113.45:443", "203.0.113.46:443"},
	}
	got := strings.Join(probeLocalTunnelRouteTargetCandidates(route), ",")
	if got != "203.0.113.45:443,203.0.113.44:443,203.0.113.46:443" {
		t.Fatalf("candidates=%q", got)
	}
}

func TestDecideProbeLocalRouteForTargetTunnelByCIDRRule(t *testing.T) {
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

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	route, err := decideProbeLocalRouteForTarget("91.108.4.10:443")
	if err != nil {
		t.Fatalf("decideProbeLocalRouteForTarget returned error: %v", err)
	}
	if route.Direct || route.Reject {
		t.Fatalf("route=%+v", route)
	}
	if route.Group != "telegram" {
		t.Fatalf("group=%q", route.Group)
	}
	if route.SelectedChainID != "chain-proxy-1" {
		t.Fatalf("selected_chain_id=%q", route.SelectedChainID)
	}
	if route.TargetAddr != "91.108.4.10:443" {
		t.Fatalf("target_addr=%q", route.TargetAddr)
	}
	if route.GroupRuntime == nil {
		t.Fatal("group_runtime should not be nil")
	}
}

func TestDecideProbeLocalRouteForTargetDirectForIPOutsideCIDRRule(t *testing.T) {
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

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	route, err := decideProbeLocalRouteForTarget("91.108.8.1:443")
	if err != nil {
		t.Fatalf("decideProbeLocalRouteForTarget returned error: %v", err)
	}
	if !route.Direct || route.Reject {
		t.Fatalf("route=%+v", route)
	}
}

func TestShouldUseProbeLocalDNSFakeIPSkipsDirectDecision(t *testing.T) {
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
		{Group: "media", Action: "direct"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	dnsDecision := resolveProbeLocalProxyRouteDecisionByDomain("api.example.com")
	if shouldUseProbeLocalDNSFakeIP("api.example.com", dnsmessage.TypeA, dnsDecision) {
		t.Fatalf("direct decision should not use fake ip: %+v", dnsDecision)
	}
}
