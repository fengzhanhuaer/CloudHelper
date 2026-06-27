package core

import "testing"

func TestBuildProbeVirtualRouterConfigForNodeFiltersDisabledRules(t *testing.T) {
	oldStore := ProbeLinkChainStore
	t.Cleanup(func() { ProbeLinkChainStore = oldStore })

	ProbeLinkChainStore = &probeLinkChainStore{
		data: probeLinkChainStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: "198.18.0.0/15",
				ProbeIPs: []probeVirtualRouterProbeIP{
					{NodeID: "1", IP: "198.18.0.1"},
					{NodeID: "2", IP: "198.18.0.2"},
				},
				TopologyRules: []probeVirtualRouterTopologyRule{
					{FromNodeID: "1", ToNodeID: "2", Direction: "bidirectional", Enabled: true},
					{FromNodeID: "2", ToNodeID: "3", Direction: "bidirectional", Enabled: false},
				},
			},
		},
	}

	config := buildProbeVirtualRouterConfigForNodeLocked("1")
	if len(config.TopologyRules) != 1 {
		t.Fatalf("topology rules=%+v, want enabled related rule only", config.TopologyRules)
	}
	if config.TopologyRules[0].FromNodeID != "1" || config.TopologyRules[0].ToNodeID != "2" {
		t.Fatalf("unexpected topology rule=%+v", config.TopologyRules[0])
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
