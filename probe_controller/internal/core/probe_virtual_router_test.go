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

func TestProbeVirtualRouterProbeIPPoolUsesFirst1024FakeIPs(t *testing.T) {
	pool := probeVirtualRouterProbeIPPool("198.18.0.0/15")
	if len(pool) != 1022 {
		t.Fatalf("pool size=%d, want 1022", len(pool))
	}
	if pool[0] != "198.18.0.3" {
		t.Fatalf("first pool ip=%q", pool[0])
	}
	if pool[len(pool)-1] != "198.18.4.0" {
		t.Fatalf("last pool ip=%q", pool[len(pool)-1])
	}
	for _, ip := range pool {
		if ip == probeVirtualRouterReservedGatewayIP || ip == probeVirtualRouterReservedTUNIP {
			t.Fatalf("pool contains reserved ip %s", ip)
		}
	}
}

func TestEnsureProbeVirtualRouterProbeIPsAllocatesFreedAddress(t *testing.T) {
	setProbeVirtualRouterTestProbeStore(t, probeConfigData{
		ProbeNodes: []probeNodeRecord{
			{NodeNo: 1, NodeName: "node-1"},
			{NodeNo: 3, NodeName: "node-3"},
			{NodeNo: 4, NodeName: "node-4"},
		},
		DeletedProbeNodes:   []probeNodeRecord{{NodeNo: 2, NodeName: "node-2"}},
		DeletedProbeNodeNos: []int{2},
		ProbeSecrets:        map[string]string{},
	})
	clearProbeVirtualRouterTestRuntimes(t)

	config := ensureProbeVirtualRouterProbeIPsForKnownNodes(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.3"},
			{NodeID: "2", IP: "198.18.0.4"},
			{NodeID: "3", IP: "198.18.0.5"},
		},
	})
	ipByNode := probeVirtualRouterTestIPByNode(config.ProbeIPs)
	if _, ok := ipByNode["2"]; ok {
		t.Fatalf("deleted node should be released: %+v", config.ProbeIPs)
	}
	if ipByNode["1"] != "198.18.0.3" || ipByNode["3"] != "198.18.0.5" {
		t.Fatalf("existing active ips should be preserved: %+v", config.ProbeIPs)
	}
	if ipByNode["4"] != "198.18.0.4" {
		t.Fatalf("node 4 ip=%q, want freed 198.18.0.4; ips=%+v", ipByNode["4"], config.ProbeIPs)
	}
}

func TestEnsureProbeVirtualRouterProbeIPsAllocatesHighNodeIDFromFreePool(t *testing.T) {
	setProbeVirtualRouterTestProbeStore(t, probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 2000, NodeName: "node-2000"}},
		ProbeSecrets: map[string]string{},
	})
	clearProbeVirtualRouterTestRuntimes(t)

	config := ensureProbeVirtualRouterProbeIPsForKnownNodes(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
	})
	ipByNode := probeVirtualRouterTestIPByNode(config.ProbeIPs)
	if ipByNode["2000"] != "198.18.0.3" {
		t.Fatalf("node 2000 ip=%q, ips=%+v", ipByNode["2000"], config.ProbeIPs)
	}
}

func setProbeVirtualRouterTestProbeStore(t *testing.T, data probeConfigData) {
	t.Helper()
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: data}
	t.Cleanup(func() { ProbeStore = oldStore })
}

func clearProbeVirtualRouterTestRuntimes(t *testing.T) {
	t.Helper()
	probeRuntimeStore.mu.Lock()
	oldRuntimeData := probeRuntimeStore.data
	probeRuntimeStore.data = make(map[string]probeRuntimeStatus)
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = oldRuntimeData
		probeRuntimeStore.mu.Unlock()
	})
}

func probeVirtualRouterTestIPByNode(items []probeVirtualRouterProbeIP) map[string]string {
	out := make(map[string]string, len(items))
	for _, item := range items {
		out[item.NodeID] = item.IP
	}
	return out
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
