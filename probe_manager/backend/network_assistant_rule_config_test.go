package backend

import "testing"

func TestBuildRuleConfigFromRoutingIncludesSelectedTunnelOption(t *testing.T) {
	routing := tunnelRuleRouting{
		RuleSet: tunnelRuleSet{
			Rules: []tunnelRule{
				{Kind: ruleMatcherDomainSuffix, Suffix: "example.com", Group: "group_a"},
			},
		},
		GroupNodeMap: map[string]string{
			"group_a":            "tunnel:chain:legacy-path",
			ruleFallbackGroupKey: rulePolicyActionDirect,
		},
		RuleFilePath: "data/rule_routes.txt",
	}

	config := buildRuleConfigFromRouting(routing, []string{"cloudserver", "1", "2"}, defaultNodeID, nil, map[string]NetworkAssistantGroupKeepaliveItem{})
	if len(config.Groups) != 1 {
		t.Fatalf("group count=%d, want 1", len(config.Groups))
	}

	group := config.Groups[0]
	if group.Action != rulePolicyActionTunnel {
		t.Fatalf("group action=%s, want %s", group.Action, rulePolicyActionTunnel)
	}
	if group.TunnelNodeID != "chain:legacy-path" {
		t.Fatalf("group tunnel node=%s, want chain:legacy-path", group.TunnelNodeID)
	}
	if containsNodeID(group.TunnelOptions, "1") || containsNodeID(group.TunnelOptions, "2") {
		t.Fatalf("tunnel options should exclude probe nodes: %#v", group.TunnelOptions)
	}
	if containsNodeID(group.TunnelOptions, "cloudserver") {
		t.Fatalf("tunnel options should exclude legacy non-chain option: %#v", group.TunnelOptions)
	}
	if containsNodeID(group.TunnelOptions, defaultNodeID) {
		t.Fatalf("tunnel options should exclude default node option: %#v", group.TunnelOptions)
	}
	if !containsNodeID(group.TunnelOptions, "chain:legacy-path") {
		t.Fatalf("tunnel options missing selected tunnel: %#v", group.TunnelOptions)
	}
}

func TestBuildRuleConfigFromRoutingUsesChainNameLabels(t *testing.T) {
	routing := tunnelRuleRouting{
		RuleSet: tunnelRuleSet{
			Rules: []tunnelRule{
				{Kind: ruleMatcherDomainSuffix, Suffix: "example.com", Group: "group_a"},
			},
		},
		GroupNodeMap: map[string]string{
			"group_a":            "tunnel:chain:1",
			ruleFallbackGroupKey: rulePolicyActionDirect,
		},
		RuleFilePath: "data/rule_routes.txt",
	}

	config := buildRuleConfigFromRouting(
		routing,
		[]string{defaultNodeID, "cloudserver", "chain:1", "3"},
		defaultNodeID,
		map[string]probeChainEndpoint{
			"chain:1": {
				ChainID:   "1",
				ChainName: "香港入口",
			},
		},
		map[string]NetworkAssistantGroupKeepaliveItem{},
	)

	if len(config.Groups) != 1 {
		t.Fatalf("group count=%d, want 1", len(config.Groups))
	}

	group := config.Groups[0]
	if got := group.TunnelOptionLabels["chain:1"]; got != "香港入口" {
		t.Fatalf("chain label=%q, want 香港入口", got)
	}
	if _, ok := group.TunnelOptionLabels["cloudserver"]; ok {
		t.Fatalf("legacy non-chain label should not appear in tunnel options: %#v", group.TunnelOptionLabels)
	}
	if _, ok := group.TunnelOptionLabels[defaultNodeID]; ok {
		t.Fatalf("default node label should not appear in tunnel options: %#v", group.TunnelOptionLabels)
	}
	if _, ok := group.TunnelOptionLabels["3"]; ok {
		t.Fatalf("probe node label should not appear in tunnel options: %#v", group.TunnelOptionLabels)
	}
}

func TestBuildRuleConfigFromRoutingIncludesRuntimeSnapshot(t *testing.T) {
	routing := tunnelRuleRouting{
		RuleSet: tunnelRuleSet{
			Rules: []tunnelRule{{Kind: ruleMatcherDomainSuffix, Suffix: "example.com", Group: "group_a"}},
		},
		GroupNodeMap: map[string]string{
			"group_a":            "tunnel:chain:1",
			ruleFallbackGroupKey: rulePolicyActionDirect,
		},
	}

	config := buildRuleConfigFromRouting(
		routing,
		[]string{"chain:1"},
		defaultNodeID,
		map[string]probeChainEndpoint{
			"chain:1": {ChainID: "1", ChainName: "香港入口"},
		},
		map[string]NetworkAssistantGroupKeepaliveItem{
			"group_a": {
				Group:         "group_a",
				Action:        rulePolicyActionTunnel,
				TunnelNodeID:  "chain:1",
				TunnelLabel:   "香港入口",
				Connected:     true,
				ActiveStreams: 2,
				LastRecv:      "recv-at",
				LastPong:      "pong-at",
				Status:        "在线",
			},
		},
	)

	group := config.Groups[0]
	if group.SelectedLabel != "香港入口" {
		t.Fatalf("selected label=%q, want 香港入口", group.SelectedLabel)
	}
	if group.RuntimeAction != rulePolicyActionTunnel {
		t.Fatalf("runtime action=%q, want %q", group.RuntimeAction, rulePolicyActionTunnel)
	}
	if group.RuntimeTunnelNodeID != "chain:1" {
		t.Fatalf("runtime tunnel node=%q, want chain:1", group.RuntimeTunnelNodeID)
	}
	if group.RuntimeTunnelLabel != "香港入口" {
		t.Fatalf("runtime tunnel label=%q, want 香港入口", group.RuntimeTunnelLabel)
	}
	if !group.RuntimeConnected {
		t.Fatalf("runtime connected=false, want true")
	}
	if group.RuntimeStatus != "在线" {
		t.Fatalf("runtime status=%q, want 在线", group.RuntimeStatus)
	}
	if group.RuntimeLastRecv != "recv-at" || group.RuntimeLastPong != "pong-at" {
		t.Fatalf("runtime recv/pong=(%q,%q), want (recv-at,pong-at)", group.RuntimeLastRecv, group.RuntimeLastPong)
	}
	if group.RuntimeActiveStreams != 2 {
		t.Fatalf("runtime active streams=%d, want 2", group.RuntimeActiveStreams)
	}
}

func TestBuildCanonicalRulePolicyMapAutoAddsGroupsAndFallback(t *testing.T) {
	ruleSet := tunnelRuleSet{
		Rules: []tunnelRule{
			{Kind: ruleMatcherDomainSuffix, Suffix: "a.example", Group: "group_a"},
			{Kind: ruleMatcherDomainSuffix, Suffix: "b.example", Group: "group_b"},
			{Kind: ruleMatcherIP, IP: "1.1.1.1", Group: "group_a"},
		},
	}

	result := buildCanonicalRulePolicyMap(ruleSet, map[string]string{
		"group_a": "direct",
		"ghost":   "tunnel:chain:ghost",
	}, defaultNodeID)

	if len(result) != 3 {
		t.Fatalf("canonical policy count=%d, want 3", len(result))
	}
	if result["group_a"] != "direct" {
		t.Fatalf("group_a policy=%s, want direct", result["group_a"])
	}
	if result["group_b"] != "tunnel:"+defaultNodeID {
		t.Fatalf("group_b policy=%s, want tunnel:%s", result["group_b"], defaultNodeID)
	}
	if result[ruleFallbackGroupKey] != "direct" {
		t.Fatalf("fallback policy=%s, want direct", result[ruleFallbackGroupKey])
	}
	if _, ok := result["ghost"]; ok {
		t.Fatalf("unexpected stale group in canonical result: ghost")
	}
}

func TestBuildRuleConfigFromRoutingKeepsRuleGroupDefinitionOrder(t *testing.T) {
	routing := tunnelRuleRouting{
		RuleSet: tunnelRuleSet{
			Rules: []tunnelRule{
				{Kind: ruleMatcherDomainSuffix, Suffix: "a.example", Group: "group_z"},
				{Kind: ruleMatcherDomainSuffix, Suffix: "b.example", Group: "group_a"},
				{Kind: ruleMatcherDomainSuffix, Suffix: "c.example", Group: "group_m"},
				{Kind: ruleMatcherDomainSuffix, Suffix: "dup.example", Group: "group_a"},
			},
		},
		GroupNodeMap: map[string]string{
			ruleFallbackGroupKey: rulePolicyActionDirect,
		},
	}

	config := buildRuleConfigFromRouting(routing, []string{defaultNodeID}, defaultNodeID, nil, map[string]NetworkAssistantGroupKeepaliveItem{})
	if len(config.Groups) != 3 {
		t.Fatalf("group count=%d, want 3", len(config.Groups))
	}
	for _, group := range config.Groups {
		if len(group.TunnelOptions) != 0 {
			t.Fatalf("group %s tunnel options=%#v, want empty", group.Group, group.TunnelOptions)
		}
	}

	if got := config.Groups[0].Group; got != "group_z" {
		t.Fatalf("group[0]=%s, want group_z", got)
	}
	if got := config.Groups[1].Group; got != "group_a" {
		t.Fatalf("group[1]=%s, want group_a", got)
	}
	if got := config.Groups[2].Group; got != "group_m" {
		t.Fatalf("group[2]=%s, want group_m", got)
	}
}

func TestShouldClearDynamicBypassForPolicyTransition(t *testing.T) {
	cases := []struct {
		name     string
		previous ruleGroupPolicy
		next     ruleGroupPolicy
		want     bool
	}{
		{
			name:     "direct_to_tunnel",
			previous: ruleGroupPolicy{Action: rulePolicyActionDirect},
			next:     ruleGroupPolicy{Action: rulePolicyActionTunnel, TunnelNodeID: defaultNodeID},
			want:     true,
		},
		{
			name:     "direct_to_reject",
			previous: ruleGroupPolicy{Action: rulePolicyActionDirect},
			next:     ruleGroupPolicy{Action: rulePolicyActionReject},
			want:     true,
		},
		{
			name:     "tunnel_to_direct",
			previous: ruleGroupPolicy{Action: rulePolicyActionTunnel, TunnelNodeID: defaultNodeID},
			next:     ruleGroupPolicy{Action: rulePolicyActionDirect},
			want:     false,
		},
		{
			name:     "tunnel_to_reject",
			previous: ruleGroupPolicy{Action: rulePolicyActionTunnel, TunnelNodeID: defaultNodeID},
			next:     ruleGroupPolicy{Action: rulePolicyActionReject},
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldClearDynamicBypassForPolicyTransition(tc.previous, tc.next)
			if got != tc.want {
				t.Fatalf("shouldClearDynamicBypassForPolicyTransition()=%v, want %v", got, tc.want)
			}
		})
	}
}
