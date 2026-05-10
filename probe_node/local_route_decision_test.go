package main

import "testing"

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
