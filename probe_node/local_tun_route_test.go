package main

import (
	"errors"
	"net"
	"testing"
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
		{Group: "media", Action: "tunnel", TunnelNodeID: "chain:chain-proxy-1"},
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
	if route.TunnelNodeID != "chain:chain-proxy-1" {
		t.Fatalf("tunnel_node_id=%q", route.TunnelNodeID)
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
	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "media", Rules: []string{"domain_suffix:example.com"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "media", Action: "tunnel", TunnelNodeID: "chain:chain-proxy-1"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

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
	if route.TunnelNodeID != "chain:chain-proxy-1" {
		t.Fatalf("tunnel_node_id=%q", route.TunnelNodeID)
	}
}
