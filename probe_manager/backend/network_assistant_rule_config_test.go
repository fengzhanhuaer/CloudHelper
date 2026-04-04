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

	config := buildRuleConfigFromRouting(routing, []string{"cloudserver"}, "cloudserver", nil)
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
	if !containsNodeID(group.TunnelOptions, "cloudserver") {
		t.Fatalf("tunnel options missing cloudserver: %#v", group.TunnelOptions)
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
		[]string{"cloudserver", "chain:1"},
		"cloudserver",
		map[string]probeChainEndpoint{
			"chain:1": {
				ChainID:   "1",
				ChainName: "香港入口",
			},
		},
	)

	if len(config.Groups) != 1 {
		t.Fatalf("group count=%d, want 1", len(config.Groups))
	}

	group := config.Groups[0]
	if got := group.TunnelOptionLabels["chain:1"]; got != "香港入口" {
		t.Fatalf("chain label=%q, want 香港入口", got)
	}
	if got := group.TunnelOptionLabels["cloudserver"]; got != "主控" {
		t.Fatalf("cloudserver label=%q, want 主控", got)
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

	config := buildRuleConfigFromRouting(routing, []string{defaultNodeID}, defaultNodeID, nil)
	if len(config.Groups) != 3 {
		t.Fatalf("group count=%d, want 3", len(config.Groups))
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
