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

	config := buildRuleConfigFromRouting(routing, []string{"cloudserver"}, "cloudserver")
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
