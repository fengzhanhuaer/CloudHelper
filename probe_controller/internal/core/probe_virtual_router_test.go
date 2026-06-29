package core

import "testing"

func TestBuildProbeVirtualRouterConfigForNodeReturnsFullVirtualTopology(t *testing.T) {
	oldStore := ProbeLinkChainStore
	t.Cleanup(func() { ProbeLinkChainStore = oldStore })

	ProbeLinkChainStore = &probeLinkChainStore{
		data: probeLinkChainStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: "198.18.0.0/15",
				ProbeIPs: []probeVirtualRouterProbeIP{
					{NodeID: "1", IP: "198.18.0.11"},
					{NodeID: "2", IP: "198.18.0.12"},
					{NodeID: "3", IP: "198.18.0.13"},
					{NodeID: "4", IP: "198.18.0.14"},
				},
				TopologyRules: []probeVirtualRouterTopologyRule{
					{FromNodeID: "1", ToNodeID: "2", Direction: "bidirectional", Enabled: true},
					{FromNodeID: "2", ToNodeID: "3", Direction: "bidirectional", Enabled: true},
					{FromNodeID: "3", ToNodeID: "4", Direction: "bidirectional", Enabled: true},
				},
			},
		},
	}

	config := buildProbeVirtualRouterConfigForNodeLocked("1")
	if len(config.ProbeIPs) != 4 {
		t.Fatalf("probe ips=%+v, want full virtual ip map", config.ProbeIPs)
	}
	if len(config.TopologyRules) != 3 {
		t.Fatalf("topology rules=%+v, want full virtual topology", config.TopologyRules)
	}
	if config.TopologyRules[0].FromServicePort != 0 || config.TopologyRules[0].ToServicePort != probeVirtualRouterDefaultServicePort {
		t.Fatalf("default service ports=%d/%d, want from=0 to=%d", config.TopologyRules[0].FromServicePort, config.TopologyRules[0].ToServicePort, probeVirtualRouterDefaultServicePort)
	}
}

func TestProbeVirtualRouterIPForNodeUsesFirst1024FakeIPs(t *testing.T) {
	if got := probeVirtualRouterIPForNode("198.18.0.0/15", "1"); got != "198.18.0.3" {
		t.Fatalf("node 1 ip=%q", got)
	}
	if got := probeVirtualRouterIPForNode("198.18.0.0/15", "1022"); got != "198.18.4.0" {
		t.Fatalf("node 1022 ip=%q", got)
	}
	if got := probeVirtualRouterIPForNode("198.18.0.0/15", "1023"); got != "" {
		t.Fatalf("node 1023 ip=%q, want empty", got)
	}
}

func TestProbeVirtualRouterRuntimeChainIDIsStableAcrossServiceEndpointChanges(t *testing.T) {
	base := probeVirtualRouterTopologyRule{
		ID:                "edge-a-b",
		FromNodeID:        "1",
		ToNodeID:          "2",
		FromServiceDomain: "old-a.internal",
		FromServicePort:   12040,
		ToServiceDomain:   "old-b.internal",
		ToServicePort:     12040,
		Enabled:           true,
	}
	changedEndpoint := base
	changedEndpoint.FromServiceDomain = "new-a.internal"
	changedEndpoint.FromServicePort = 13040
	changedEndpoint.ToServiceDomain = "new-b.internal"
	changedEndpoint.ToServicePort = 13041
	changedEndpoint.FromNodeID = "3"
	changedEndpoint.ToNodeID = "4"

	if left, right := probeVirtualRouterRuntimeChainID(base), probeVirtualRouterRuntimeChainID(changedEndpoint); left != right {
		t.Fatalf("same rule should keep chain id across topology endpoint changes: %s != %s", left, right)
	}

	changedRule := base
	changedRule.ID = "edge-a-b-other"
	if left, right := probeVirtualRouterRuntimeChainID(base), probeVirtualRouterRuntimeChainID(changedRule); left == right {
		t.Fatalf("different rule ids should produce different chain ids: %s", left)
	}
}

func TestNormalizeProbeVirtualRouterTopologyRulesInitializesRuleIDsBySequence(t *testing.T) {
	config := normalizeProbeVirtualRouterConfig(probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{ID: "vr-1", FromNodeID: "2", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	})
	if len(config.TopologyRules) != 3 {
		t.Fatalf("topology rules=%+v", config.TopologyRules)
	}
	idsByFrom := map[string]string{}
	for _, rule := range config.TopologyRules {
		idsByFrom[rule.FromNodeID] = rule.ID
	}
	if idsByFrom["1"] != "vr-2" || idsByFrom["2"] != "vr-1" || idsByFrom["3"] != "vr-3" {
		t.Fatalf("rule ids should be initialized once by sequence: %+v", config.TopologyRules)
	}
}
