package main

import "testing"

func TestBuildProbeSubstreamMonitorItemIncludesExplicitProxyTunnel(t *testing.T) {
	item := probeTCPDebugConnectionItemPayload{
		ID:          "probe-tcp-1",
		Status:      "active",
		Scope:       "explicit",
		Side:        "socks5",
		FlowID:      "explicit-flow",
		Target:      "example.com:443",
		RouteTarget: "203.0.113.10:443",
		Group:       "default",
		NodeID:      "chain:1",
		Transport:   "tunnel",
	}

	sub, ok := buildProbeSubstreamMonitorItem(item)
	if !ok {
		t.Fatal("explicit proxy tunnel item was filtered out")
	}
	if sub.Kind != "explicit_proxy" {
		t.Fatalf("kind=%q, want explicit_proxy", sub.Kind)
	}
	if sub.FlowID != "explicit-flow" {
		t.Fatalf("flow_id=%q, want explicit-flow", sub.FlowID)
	}
}

func TestBuildProbeSubstreamMonitorItemFiltersDirectConnections(t *testing.T) {
	item := probeTCPDebugConnectionItemPayload{
		ID:        "probe-tcp-1",
		Status:    "active",
		Scope:     "explicit",
		Target:    "example.com:443",
		Direct:    true,
		Transport: "direct",
	}

	if _, ok := buildProbeSubstreamMonitorItem(item); ok {
		t.Fatal("direct TCP connection should not be rendered as a substream")
	}
}
