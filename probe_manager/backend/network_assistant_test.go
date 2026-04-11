package backend

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestCollectAutoMaintainPolicyTunnelNodeIDs(t *testing.T) {
	_, cidr, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parse cidr: %v", err)
	}

	routing := tunnelRuleRouting{
		RuleSet: tunnelRuleSet{
			Rules: []tunnelRule{
				{Kind: ruleMatcherCIDR, CIDR: cidr, Group: "google"},
				{Kind: ruleMatcherDomainExact, Domain: "github.com", Group: "github"},
				{Kind: ruleMatcherDomainExact, Domain: "example.com", Group: "shared"},
			},
		},
		GroupNodeMap: map[string]string{
			"google":             rulePolicyActionTunnel + ":chain:1",
			"github":             rulePolicyActionTunnel + ":chain:2",
			"shared":             rulePolicyActionTunnel + ":chain:2",
			ruleFallbackGroupKey: rulePolicyActionDirect,
		},
	}

	nodes := collectAutoMaintainPolicyTunnelNodeIDs(routing, []string{"direct", "chain:1", "chain:2"}, defaultNodeID)
	if len(nodes) != 2 {
		t.Fatalf("node count=%d, want 2 (%v)", len(nodes), nodes)
	}
	if nodes[0] != "chain:1" {
		t.Fatalf("node[0]=%s, want chain:1", nodes[0])
	}
	if nodes[1] != "chain:2" {
		t.Fatalf("node[1]=%s, want chain:2", nodes[1])
	}
}

func TestCollectAutoMaintainPolicyTunnelNodeIDsSkipsDirectRejectAndFallbackDirect(t *testing.T) {
	routing := tunnelRuleRouting{
		RuleSet: tunnelRuleSet{
			Rules: []tunnelRule{
				{Kind: ruleMatcherDomainExact, Domain: "direct.example.com", Group: "direct_group"},
				{Kind: ruleMatcherDomainExact, Domain: "reject.example.com", Group: "reject_group"},
			},
		},
		GroupNodeMap: map[string]string{
			"direct_group":       rulePolicyActionDirect,
			"reject_group":       rulePolicyActionReject,
			ruleFallbackGroupKey: rulePolicyActionDirect,
		},
	}

	nodes := collectAutoMaintainPolicyTunnelNodeIDs(routing, []string{"direct", "chain:1"}, defaultNodeID)
	if len(nodes) != 0 {
		t.Fatalf("node count=%d, want 0 (%v)", len(nodes), nodes)
	}
}

func TestBuildTunnelWSURL(t *testing.T) {
	u, err := buildTunnelWSURL("https://controller.example.com", "cloudserver", "tok-1")
	if err != nil {
		t.Fatalf("buildTunnelWSURL returned error: %v", err)
	}
	if u != "wss://controller.example.com/api/ws/tunnel/cloudserver?token=tok-1" {
		t.Fatalf("unexpected tunnel ws url: %s", u)
	}
}

func TestResolveControllerHostForProtection(t *testing.T) {
	host := resolveControllerHostForProtection("https://controller.example.com:443/path")
	if host != "controller.example.com" {
		t.Fatalf("unexpected host: %s", host)
	}

	ipHost := resolveControllerHostForProtection("https://203.0.113.10:8443")
	if ipHost != "203.0.113.10" {
		t.Fatalf("unexpected ip host: %s", ipHost)
	}
}

func TestResolveControllerPreferredDialTargetForConfig(t *testing.T) {
	target, err := resolveControllerPreferredDialTargetForConfig("https://controller.example.com:8443/path", "203.0.113.10")
	if err != nil {
		t.Fatalf("resolveControllerPreferredDialTargetForConfig returned error: %v", err)
	}
	if !target.Enabled {
		t.Fatal("expected preferred dial target to be enabled")
	}
	if target.PreferredIP != "203.0.113.10" {
		t.Fatalf("preferred ip=%s, want 203.0.113.10", target.PreferredIP)
	}
	if target.Address != "203.0.113.10:8443" {
		t.Fatalf("address=%s, want 203.0.113.10:8443", target.Address)
	}
	if target.TLSServerName != "controller.example.com" {
		t.Fatalf("tls server name=%s, want controller.example.com", target.TLSServerName)
	}
}

func TestResolveControllerPreferredDialTargetForConfigSkipsIPHost(t *testing.T) {
	target, err := resolveControllerPreferredDialTargetForConfig("https://203.0.113.20:9443", "203.0.113.10")
	if err != nil {
		t.Fatalf("resolveControllerPreferredDialTargetForConfig returned error: %v", err)
	}
	if target.Enabled {
		t.Fatalf("expected preferred dial target to be disabled for ip host: %#v", target)
	}
}

func TestParseTunnelRuleLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantKind  ruleMatcherKind
		wantGroup string
	}{
		{name: "domain exact", line: "example.com,group_a", wantKind: ruleMatcherDomainExact, wantGroup: "group_a"},
		{name: "domain suffix", line: "*example.com,group_a", wantKind: ruleMatcherDomainSuffix, wantGroup: "group_a"},
		{name: "domain prefix", line: "api.*,group_a", wantKind: ruleMatcherDomainPrefix, wantGroup: "group_a"},
		{name: "domain contains", line: "*ample.co*,group_a", wantKind: ruleMatcherDomainContains, wantGroup: "group_a"},
		{name: "ip", line: "1.2.3.4,group_b", wantKind: ruleMatcherIP, wantGroup: "group_b"},
		{name: "cidr", line: "10.0.0.0/8,group_c", wantKind: ruleMatcherCIDR, wantGroup: "group_c"},
	}

	for _, tt := range tests {
		got, err := parseTunnelRuleLine(tt.line)
		if err != nil {
			t.Fatalf("%s parseTunnelRuleLine returned error: %v", tt.name, err)
		}
		if got.Kind != tt.wantKind {
			t.Fatalf("%s kind=%s, want %s", tt.name, got.Kind, tt.wantKind)
		}
		if got.Group != tt.wantGroup {
			t.Fatalf("%s group=%s, want %s", tt.name, got.Group, tt.wantGroup)
		}
	}
}

func TestParseTunnelRuleFileBlockFormat(t *testing.T) {
	tempDir := t.TempDir()
	path := tempDir + "/rule_routes.txt"
	content := "# comment\n" +
		"lan\n" +
		"{\n" +
		"10.0.0.0/8\n" +
		"192.168.1.10\n" +
		"*example.com\n" +
		"}\n" +
		"direct_local\n" +
		"{\n" +
		"localhost\n" +
		"localhost,127.0.0.1\n" +
		"}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	ruleSet, err := parseTunnelRuleFile(path)
	if err != nil {
		t.Fatalf("parseTunnelRuleFile returned error: %v", err)
	}
	if len(ruleSet.Rules) != 5 {
		t.Fatalf("rule count=%d, want 5", len(ruleSet.Rules))
	}
	if ruleSet.Rules[0].Group != "lan" || ruleSet.Rules[0].Kind != ruleMatcherCIDR {
		t.Fatalf("rule 0 = %#v", ruleSet.Rules[0])
	}
	if ruleSet.Rules[1].Group != "lan" || ruleSet.Rules[1].Kind != ruleMatcherIP {
		t.Fatalf("rule 1 = %#v", ruleSet.Rules[1])
	}
	if ruleSet.Rules[2].Group != "lan" || ruleSet.Rules[2].Kind != ruleMatcherDomainSuffix {
		t.Fatalf("rule 2 = %#v", ruleSet.Rules[2])
	}
	if ruleSet.Rules[3].Group != "direct_local" || ruleSet.Rules[3].Kind != ruleMatcherDomainExact {
		t.Fatalf("rule 3 = %#v", ruleSet.Rules[3])
	}
	if ruleSet.Rules[4].Group != "direct_local" || ruleSet.Rules[4].Kind != ruleMatcherDomainStaticIP {
		t.Fatalf("rule 4 = %#v", ruleSet.Rules[4])
	}
}

func TestParseTunnelRuleFileRejectsLegacyLineFormat(t *testing.T) {
	tempDir := t.TempDir()
	path := tempDir + "/rule_routes.txt"
	if err := os.WriteFile(path, []byte("example.com,default\n"), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	_, err := parseTunnelRuleFile(path)
	if err == nil {
		t.Fatal("expected parse error for legacy rule format")
	}
}

func TestTunnelRuleSetMatchHost(t *testing.T) {
	_, cidr, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parse cidr: %v", err)
	}
	rules := tunnelRuleSet{
		Rules: []tunnelRule{
			{Kind: ruleMatcherDomainContains, Domain: "ample", Group: "contains"},
			{Kind: ruleMatcherDomainPrefix, Domain: "www.", Group: "prefix"},
			{Kind: ruleMatcherDomainSuffix, Domain: "example.com", Group: "suffix"},
			{Kind: ruleMatcherDomainExact, Domain: "www.example.com", Group: "exact"},
			{Kind: ruleMatcherIP, IP: "1.2.3.4", Group: "ip"},
			{Kind: ruleMatcherCIDR, CIDR: cidr, Group: "cidr"},
		},
	}

	if got, ok := rules.matchHost("www.example.com"); !ok || got.Group != "exact" {
		t.Fatalf("domain priority match failed: ok=%v group=%s", ok, got.Group)
	}
	if got, ok := rules.matchHost("api.example.com"); !ok || got.Group != "suffix" {
		t.Fatalf("suffix match failed: ok=%v group=%s", ok, got.Group)
	}
	if got, ok := rules.matchHost("www.foo.test"); !ok || got.Group != "prefix" {
		t.Fatalf("prefix match failed: ok=%v group=%s", ok, got.Group)
	}
	if got, ok := rules.matchHost("fooamplebar.test"); !ok || got.Group != "contains" {
		t.Fatalf("contains match failed: ok=%v group=%s", ok, got.Group)
	}
	if got, ok := rules.matchHost("1.2.3.4"); !ok || got.Group != "ip" {
		t.Fatalf("ip match failed: ok=%v group=%s", ok, got.Group)
	}
	if got, ok := rules.matchHost("10.2.3.4"); !ok || got.Group != "cidr" {
		t.Fatalf("cidr match failed: ok=%v group=%s", ok, got.Group)
	}
	if _, ok := rules.matchHost("not-match.test"); ok {
		t.Fatal("unexpected match for unknown host")
	}
}

func TestDecideRouteForTargetRuleModeByIP(t *testing.T) {
	_, cidr, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parse cidr: %v", err)
	}
	service := &networkAssistantService{
		mode:           networkModeTUN,
		nodeID:         "cloudserver",
		availableNodes: []string{"cloudserver", "chain:test-chain"},
		ruleRouting: tunnelRuleRouting{
			RuleSet: tunnelRuleSet{
				Rules: []tunnelRule{
					{Kind: ruleMatcherCIDR, CIDR: cidr, Group: "group_cidr"},
				},
			},
			GroupNodeMap: map[string]string{
				"group_cidr": "chain:test-chain",
			},
		},
		ruleDNSCache: make(map[string]dnsCacheEntry),
	}

	decision, err := service.decideRouteForTarget("10.1.2.3:443")
	if err != nil {
		t.Fatalf("decideRouteForTarget returned error: %v", err)
	}
	if decision.Direct {
		t.Fatal("expected tunnel route for matched cidr")
	}
	if decision.NodeID != "chain:test-chain" {
		t.Fatalf("node id=%s, want chain:test-chain", decision.NodeID)
	}
	if decision.TargetAddr != "10.1.2.3:443" {
		t.Fatalf("target addr=%s, want 10.1.2.3:443", decision.TargetAddr)
	}
	if decision.Group != "group_cidr" {
		t.Fatalf("group=%s, want group_cidr", decision.Group)
	}

	directDecision, err := service.decideRouteForTarget("8.8.8.8:53")
	if err != nil {
		t.Fatalf("decideRouteForTarget returned error: %v", err)
	}
	if !directDecision.Direct {
		t.Fatal("expected direct route for unmatched target")
	}
}

func TestDecideRouteForTargetTUNModeUsesRuleRouting(t *testing.T) {
	_, cidr, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parse cidr: %v", err)
	}
	service := &networkAssistantService{
		mode:           networkModeTUN,
		nodeID:         "cloudserver",
		availableNodes: []string{"cloudserver", "chain:test-chain"},
		ruleRouting: tunnelRuleRouting{
			RuleSet: tunnelRuleSet{
				Rules: []tunnelRule{
					{Kind: ruleMatcherCIDR, CIDR: cidr, Group: "group_cidr"},
				},
			},
			GroupNodeMap: map[string]string{
				"group_cidr":         "chain:test-chain",
				ruleFallbackGroupKey: rulePolicyActionDirect,
			},
		},
		ruleDNSCache: make(map[string]dnsCacheEntry),
	}

	matchedDecision, err := service.decideRouteForTarget("10.1.2.3:443")
	if err != nil {
		t.Fatalf("decideRouteForTarget returned error: %v", err)
	}
	if matchedDecision.Direct {
		t.Fatal("expected tunnel route for matched cidr in tun mode")
	}
	if matchedDecision.NodeID != "chain:test-chain" {
		t.Fatalf("node id=%s, want chain:test-chain", matchedDecision.NodeID)
	}
	if matchedDecision.Group != "group_cidr" {
		t.Fatalf("group=%s, want group_cidr", matchedDecision.Group)
	}

	unmatchedDecision, err := service.decideRouteForTarget("8.8.8.8:53")
	if err != nil {
		t.Fatalf("decideRouteForTarget returned error: %v", err)
	}
	if !unmatchedDecision.Direct {
		t.Fatal("expected direct route for unmatched target in tun mode")
	}
	if !unmatchedDecision.BypassTUN {
		t.Fatal("expected fallback direct to bypass tun")
	}
}

func TestDecideRouteForTargetDirectGroupAlwaysBypassTUN(t *testing.T) {
	_, cidr, err := net.ParseCIDR("0.0.0.0/0")
	if err != nil {
		t.Fatalf("parse cidr: %v", err)
	}
	service := &networkAssistantService{
		mode:           networkModeTUN,
		nodeID:         "chain:test-chain",
		availableNodes: []string{"cloudserver", "chain:test-chain"},
		ruleRouting: tunnelRuleRouting{
			RuleSet: tunnelRuleSet{
				Rules: []tunnelRule{
					{Kind: ruleMatcherCIDR, CIDR: cidr, Group: "direct"},
				},
			},
			GroupNodeMap: map[string]string{
				"direct":             rulePolicyActionTunnel + ":chain:test-chain",
				ruleFallbackGroupKey: rulePolicyActionTunnel + ":chain:test-chain",
			},
		},
		ruleDNSCache: make(map[string]dnsCacheEntry),
	}

	decision, err := service.decideRouteForTarget("203.0.113.10:443")
	if err != nil {
		t.Fatalf("decideRouteForTarget returned error: %v", err)
	}
	if !decision.Direct {
		t.Fatal("expected direct group to always route direct")
	}
	if !decision.BypassTUN {
		t.Fatal("expected direct group to bypass tun")
	}
	if decision.Group != "direct" {
		t.Fatalf("group=%s, want direct", decision.Group)
	}
}

func TestDecideRouteForTargetNormalGroupDirectBypassesTUN(t *testing.T) {
	_, cidr, err := net.ParseCIDR("0.0.0.0/0")
	if err != nil {
		t.Fatalf("parse cidr: %v", err)
	}
	service := &networkAssistantService{
		mode:           networkModeTUN,
		nodeID:         "chain:test-chain",
		availableNodes: []string{"cloudserver", "chain:test-chain"},
		ruleRouting: tunnelRuleRouting{
			RuleSet: tunnelRuleSet{
				Rules: []tunnelRule{
					{Kind: ruleMatcherCIDR, CIDR: cidr, Group: "group_normal"},
				},
			},
			GroupNodeMap: map[string]string{
				"group_normal":       rulePolicyActionDirect,
				ruleFallbackGroupKey: rulePolicyActionDirect,
			},
		},
		ruleDNSCache: make(map[string]dnsCacheEntry),
	}

	decision, err := service.decideRouteForTarget("203.0.113.10:443")
	if err != nil {
		t.Fatalf("decideRouteForTarget returned error: %v", err)
	}
	if !decision.Direct {
		t.Fatal("expected normal group direct policy to stay direct")
	}
	if !decision.BypassTUN {
		t.Fatal("expected normal group direct policy to bypass tun")
	}
	if decision.Group != "group_normal" {
		t.Fatalf("group=%s, want group_normal", decision.Group)
	}
}

func TestDecideRouteForTargetRuleModeRejectMatchedGroup(t *testing.T) {
	_, cidr, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parse cidr: %v", err)
	}
	service := &networkAssistantService{
		mode:   networkModeTUN,
		nodeID: "cloudserver",
		ruleRouting: tunnelRuleRouting{
			RuleSet: tunnelRuleSet{
				Rules: []tunnelRule{
					{Kind: ruleMatcherCIDR, CIDR: cidr, Group: "group_reject"},
				},
			},
			GroupNodeMap: map[string]string{
				"group_reject": rulePolicyActionReject,
			},
		},
		ruleDNSCache: make(map[string]dnsCacheEntry),
	}

	_, routeErr := service.decideRouteForTarget("10.1.2.3:443")
	if !isRuleRouteRejectErr(routeErr) {
		t.Fatalf("expected reject route error, got: %v", routeErr)
	}
}

func TestDecideRouteForTargetRuleModeRejectFallback(t *testing.T) {
	service := &networkAssistantService{
		mode:   networkModeTUN,
		nodeID: "cloudserver",
		ruleRouting: tunnelRuleRouting{
			RuleSet: tunnelRuleSet{
				Rules: []tunnelRule{},
			},
			GroupNodeMap: map[string]string{
				ruleFallbackGroupKey: rulePolicyActionReject,
			},
		},
		ruleDNSCache: make(map[string]dnsCacheEntry),
	}

	_, routeErr := service.decideRouteForTarget("8.8.8.8:53")
	if !isRuleRouteRejectErr(routeErr) {
		t.Fatalf("expected reject route error, got: %v", routeErr)
	}
}

func TestDecideRouteForTargetRuleModeTunnelDomainKeepsDomainTarget(t *testing.T) {
	service := &networkAssistantService{
		mode:           networkModeTUN,
		nodeID:         "cloudserver",
		availableNodes: []string{"cloudserver", "chain:edge-a"},
		ruleRouting: tunnelRuleRouting{
			RuleSet: tunnelRuleSet{
				Rules: []tunnelRule{
					{Kind: ruleMatcherDomainSuffix, Domain: "example.com", Group: "group_example"},
				},
			},
			GroupNodeMap: map[string]string{
				"group_example":      rulePolicyActionTunnel + ":chain:edge-a",
				ruleFallbackGroupKey: rulePolicyActionDirect,
			},
		},
		ruleDNSCache: make(map[string]dnsCacheEntry),
	}

	decision, err := service.decideRouteForTarget("api.example.com:443")
	if err != nil {
		t.Fatalf("decideRouteForTarget returned error: %v", err)
	}
	if decision.Direct {
		t.Fatal("expected tunnel route for matched domain")
	}
	if decision.NodeID != "chain:edge-a" {
		t.Fatalf("node id=%s, want chain:edge-a", decision.NodeID)
	}
	if decision.TargetAddr != "api.example.com:443" {
		t.Fatalf("target addr=%s, want api.example.com:443", decision.TargetAddr)
	}
	if decision.Group != "group_example" {
		t.Fatalf("group=%s, want group_example", decision.Group)
	}
}

func TestResolveDomainForInternalDNSStaticIP(t *testing.T) {
	service := &networkAssistantService{
		mode:   networkModeTUN,
		nodeID: "cloudserver",
		ruleRouting: tunnelRuleRouting{
			RuleSet: tunnelRuleSet{
				Rules: []tunnelRule{
					{Kind: ruleMatcherDomainStaticIP, Domain: "localhost", IP: "127.0.0.1", Group: "direct_local"},
				},
			},
			GroupNodeMap: map[string]string{ruleFallbackGroupKey: rulePolicyActionDirect},
		},
		ruleDNSCache: make(map[string]dnsCacheEntry),
	}

	addrs, ttl, _, err := service.resolveDomainForInternalDNS("localhost", 1)
	if err != nil {
		t.Fatalf("resolveDomainForInternalDNS returned error: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != "127.0.0.1" {
		t.Fatalf("unexpected static dns addrs: %#v", addrs)
	}
	if ttl <= 0 {
		t.Fatalf("unexpected static dns ttl=%d", ttl)
	}

	addrs6, _, _, err := service.resolveDomainForInternalDNS("localhost", 28)
	if err != nil {
		t.Fatalf("expected AAAA query fallback to succeed, got err=%v", err)
	}
	if len(addrs6) == 0 {
		t.Fatalf("expected AAAA query fallback to return at least one address")
	}
}

func TestShouldUseTunnelDNSForRoute(t *testing.T) {
	tests := []struct {
		name  string
		route tunnelRouteDecision
		want  bool
	}{
		{
			name:  "fallback direct should use system dns",
			route: tunnelRouteDecision{Direct: true, BypassTUN: true, Group: ruleFallbackGroupKey},
			want:  false,
		},
		{
			name:  "direct bypass should use system dns",
			route: tunnelRouteDecision{Direct: true, BypassTUN: true, Group: "direct"},
			want:  false,
		},
		{
			name:  "tunnel route should use tunnel dns",
			route: tunnelRouteDecision{Direct: false, BypassTUN: false, NodeID: "chain:edge-a", Group: "group_example"},
			want:  true,
		},
		{
			name:  "bypassed non-direct should still use system dns",
			route: tunnelRouteDecision{Direct: false, BypassTUN: true, Group: "group_example"},
			want:  false,
		},
	}

	for _, tt := range tests {
		if got := shouldUseTunnelDNSForRoute(tt.route); got != tt.want {
			t.Fatalf("%s: got=%v, want=%v, route=%#v", tt.name, got, tt.want, tt.route)
		}
	}
}

func TestBuildInternalDNSCacheKeyUsesDirectBucketForDirectRoutes(t *testing.T) {
	tests := []struct {
		name  string
		route tunnelRouteDecision
		want  string
	}{
		{
			name:  "fallback direct should not fallback to cloudserver bucket",
			route: tunnelRouteDecision{Direct: true, BypassTUN: true, Group: ruleFallbackGroupKey},
			want:  "direct|1|example.com",
		},
		{
			name:  "tunnel route without node id should use default node bucket",
			route: tunnelRouteDecision{Direct: false, BypassTUN: false, Group: "group_example"},
			want:  defaultNodeID + "|1|example.com",
		},
		{
			name:  "tunnel route with explicit node id should use that bucket",
			route: tunnelRouteDecision{Direct: false, BypassTUN: false, NodeID: "chain:edge-a", Group: "group_example"},
			want:  "chain:edge-a|1|example.com",
		},
	}

	for _, tt := range tests {
		got := buildInternalDNSCacheKey(tt.route, "example.com", 1)
		if got != tt.want {
			t.Fatalf("%s: key=%s, want=%s, route=%#v", tt.name, got, tt.want, tt.route)
		}
	}
}

func TestParseTunnelRuleFileRejectsWildcardStaticIP(t *testing.T) {
	tempDir := t.TempDir()
	path := tempDir + "/rule_routes.txt"
	content := "direct_local\n" +
		"{\n" +
		"*baidu.com,127.0.0.1\n" +
		"}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	_, err := parseTunnelRuleFile(path)
	if err == nil {
		t.Fatal("expected parse error for wildcard static dns rule")
	}
}

func TestDecideRouteForTargetUsesDNSRouteHintForIP(t *testing.T) {
	service := &networkAssistantService{
		mode:           networkModeTUN,
		nodeID:         "cloudserver",
		availableNodes: []string{"cloudserver", "chain:edge-a"},
		ruleRouting: tunnelRuleRouting{
			RuleSet:      tunnelRuleSet{Rules: []tunnelRule{}},
			GroupNodeMap: map[string]string{ruleFallbackGroupKey: rulePolicyActionDirect},
		},
		ruleDNSCache: make(map[string]dnsCacheEntry),
		dnsRouteHints: map[string]dnsRouteHintEntry{
			"203.0.113.7": {
				Direct:    false,
				BypassTUN: false,
				NodeID:    "chain:edge-a",
				Group:     "group_example",
				Expires:   time.Now().Add(30 * time.Second),
			},
		},
	}

	decision, err := service.decideRouteForTarget("203.0.113.7:443")
	if err != nil {
		t.Fatalf("decideRouteForTarget returned error: %v", err)
	}
	if decision.Direct {
		t.Fatal("expected tunnel route by dns hint")
	}
	if decision.NodeID != "chain:edge-a" {
		t.Fatalf("node id=%s, want chain:edge-a", decision.NodeID)
	}
	if decision.Group != "group_example" {
		t.Fatalf("group=%s, want group_example", decision.Group)
	}
}

func TestCollectDNSCacheTrafficStats(t *testing.T) {
	events := []NetworkProcessEvent{
		{
			Kind:        NetworkProcessEventDNS,
			Domain:      "Example.COM",
			ResolvedIPs: []string{"198.51.100.7", "198.51.100.7", "2001:db8::7"},
			Direct:      false,
			NodeID:      "chain:edge-a",
			Group:       "group_example",
			Count:       2,
			Timestamp:   100,
		},
		{
			Kind:      NetworkProcessEventTCP,
			TargetIP:  "198.51.100.7",
			Direct:    false,
			NodeID:    "chain:edge-a",
			Group:     "group_example",
			Count:     3,
			Timestamp: 101,
		},
		{
			Kind:      NetworkProcessEventUDP,
			TargetIP:  "203.0.113.9",
			Direct:    true,
			Count:     4,
			Timestamp: 102,
		},
	}

	domainStats, ipStats, ipRoute := collectDNSCacheTrafficStats(events)

	if got := domainStats["example.com"].DNSCount; got != 2 {
		t.Fatalf("domain dns count=%d, want 2", got)
	}
	if got := ipStats["198.51.100.7"].DNSCount; got != 2 {
		t.Fatalf("ip dns count=%d, want 2", got)
	}
	if got := ipStats["198.51.100.7"].IPConnectCount; got != 3 {
		t.Fatalf("ip connect count=%d, want 3", got)
	}
	if got := ipStats["2001:db8::7"].DNSCount; got != 2 {
		t.Fatalf("ipv6 dns count=%d, want 2", got)
	}
	if got := ipStats["203.0.113.9"].IPConnectCount; got != 4 {
		t.Fatalf("ip-only connect count=%d, want 4", got)
	}

	route, ok := ipRoute["198.51.100.7"]
	if !ok {
		t.Fatal("route candidate for 198.51.100.7 not found")
	}
	if route.Route.Direct {
		t.Fatal("route candidate should be non-direct")
	}
	if route.Route.NodeID != "chain:edge-a" || route.Route.Group != "group_example" {
		t.Fatalf("unexpected route candidate: %#v", route.Route)
	}
}

func TestQuerySplitDNSCacheEntriesIncludesIPOnlyTrafficAndCounters(t *testing.T) {
	directDNSCache.mu.Lock()
	directDNSCache.entries = make(map[string]dnsCacheDirectEntry)
	directDNSCache.mu.Unlock()
	dnsBiMapCache.mu.Lock()
	dnsBiMapCache.loaded = true
	dnsBiMapCache.path = ""
	dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
	dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
	dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
	dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
	dnsBiMapCache.mu.Unlock()
	t.Cleanup(func() {
		directDNSCache.mu.Lock()
		directDNSCache.entries = make(map[string]dnsCacheDirectEntry)
		directDNSCache.mu.Unlock()
		dnsBiMapCache.mu.Lock()
		dnsBiMapCache.loaded = true
		dnsBiMapCache.path = ""
		dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
		dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
		dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
		dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
		dnsBiMapCache.mu.Unlock()
	})

	now := time.Now()
	dnsBiMapCache.mu.Lock()
	bimapEntry := dnsBiMapEntry{
		Domain:    "bimap.example.com",
		IP:        "198.51.100.9",
		Group:     "group_example",
		NodeID:    "chain:edge-a",
		ExpiresAt: now.Add(30 * time.Minute),
		UpdatedAt: now,
	}
	dnsBiMapCache.entries[dnsBiMapKey(bimapEntry.Domain, bimapEntry.IP)] = bimapEntry
	addDNSBiMapIndexLocked(bimapEntry.Domain, bimapEntry.IP)
	dnsBiMapCache.mu.Unlock()

	monitor := newProcessMonitor()
	monitor.Start()
	monitor.appendEvent(NetworkProcessEvent{
		Kind:        NetworkProcessEventDNS,
		Domain:      "example.com",
		ResolvedIPs: []string{"198.51.100.7"},
		Direct:      false,
		NodeID:      "chain:edge-a",
		Group:       "group_example",
		Count:       2,
		Timestamp:   100,
	})
	monitor.appendEvent(NetworkProcessEvent{
		Kind:      NetworkProcessEventTCP,
		TargetIP:  "198.51.100.7",
		Direct:    false,
		NodeID:    "chain:edge-a",
		Group:     "group_example",
		Count:     3,
		Timestamp: 101,
	})
	monitor.appendEvent(NetworkProcessEvent{
		Kind:      NetworkProcessEventUDP,
		TargetIP:  "203.0.113.9",
		Direct:    true,
		Count:     4,
		Timestamp: 102,
	})

	svc := &networkAssistantService{
		ruleDNSCache: make(map[string]dnsCacheEntry),
		dnsRouteHints: map[string]dnsRouteHintEntry{
			"198.51.100.7": {
				Domain:    "example.com",
				Direct:    false,
				BypassTUN: false,
				NodeID:    "chain:edge-a",
				Group:     "group_example",
				Expires:   time.Now().Add(2 * time.Minute),
			},
		},
		processMonitor: monitor,
	}

	entries := querySplitDNSCacheEntries(svc, "")
	if len(entries) < 3 {
		t.Fatalf("entries len=%d, want >=3", len(entries))
	}

	byIP := make(map[string]NetworkAssistantDNSCacheEntry, len(entries))
	for _, entry := range entries {
		byIP[entry.IP] = entry
	}

	entry, ok := byIP["198.51.100.7"]
	if !ok {
		t.Fatal("missing merged record for 198.51.100.7")
	}
	if entry.Domain != "example.com" {
		t.Fatalf("domain=%q, want example.com", entry.Domain)
	}
	if entry.DNSCount != 2 || entry.IPConnectCount != 3 || entry.TotalCount != 5 {
		t.Fatalf("unexpected counters: dns=%d ip=%d total=%d", entry.DNSCount, entry.IPConnectCount, entry.TotalCount)
	}

	biMapEntry, ok := byIP["198.51.100.9"]
	if !ok {
		t.Fatal("missing bi-map record for 198.51.100.9")
	}
	if biMapEntry.Domain != "bimap.example.com" {
		t.Fatalf("bimap domain=%q, want bimap.example.com", biMapEntry.Domain)
	}
	if biMapEntry.Kind != dnsCacheKindBiMap || biMapEntry.Source != dnsCacheSourceBiMap {
		t.Fatalf("bimap record kind/source=%s/%s, want %s/%s", biMapEntry.Kind, biMapEntry.Source, dnsCacheKindBiMap, dnsCacheSourceBiMap)
	}
	if biMapEntry.Direct {
		t.Fatal("bimap record should be non-direct")
	}

	ipOnly, ok := byIP["203.0.113.9"]
	if !ok {
		t.Fatal("missing ip-only traffic record")
	}
	if ipOnly.Domain != "" {
		t.Fatalf("ip-only domain=%q, want empty", ipOnly.Domain)
	}
	if ipOnly.DNSCount != 0 || ipOnly.IPConnectCount != 4 || ipOnly.TotalCount != 4 {
		t.Fatalf("unexpected ip-only counters: dns=%d ip=%d total=%d", ipOnly.DNSCount, ipOnly.IPConnectCount, ipOnly.TotalCount)
	}
	if !ipOnly.Direct {
		t.Fatal("ip-only record should inherit direct route")
	}
}

func TestRecordDNSBiMapConnectResultEvictsAfterThreeFailuresInWindow(t *testing.T) {
	dnsBiMapCache.mu.Lock()
	dnsBiMapCache.loaded = true
	dnsBiMapCache.path = ""
	dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
	dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
	dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
	dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
	dnsBiMapCache.mu.Unlock()
	t.Cleanup(func() {
		dnsBiMapCache.mu.Lock()
		dnsBiMapCache.loaded = true
		dnsBiMapCache.path = ""
		dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
		dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
		dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
		dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
		dnsBiMapCache.mu.Unlock()
	})

	now := time.Now()
	dnsBiMapCache.mu.Lock()
	entry := dnsBiMapEntry{
		Domain:    "proxy.example.com",
		IP:        "198.51.100.77",
		Group:     "group_example",
		NodeID:    "chain:edge-a",
		ExpiresAt: now.Add(time.Hour),
		UpdatedAt: now,
	}
	key := dnsBiMapKey(entry.Domain, entry.IP)
	dnsBiMapCache.entries[key] = entry
	addDNSBiMapIndexLocked(entry.Domain, entry.IP)
	dnsBiMapCache.mu.Unlock()

	svc := &networkAssistantService{mode: networkModeTUN, tunEnabled: true}
	for i := 0; i < dnsBidirectionalFailureThreshold; i++ {
		svc.recordDNSBiMapConnectResult(net.JoinHostPort(entry.IP, "443"), "group_example", false)
	}

	dnsBiMapCache.mu.Lock()
	_, exists := dnsBiMapCache.entries[key]
	dnsBiMapCache.mu.Unlock()
	if exists {
		t.Fatal("entry should be evicted after three failures within window")
	}
}

func TestStoreDNSBiMapSkipsDirectAndStoresProxyGroup(t *testing.T) {
	dnsBiMapCache.mu.Lock()
	dnsBiMapCache.loaded = true
	dnsBiMapCache.path = ""
	dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
	dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
	dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
	dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
	dnsBiMapCache.mu.Unlock()
	t.Cleanup(func() {
		dnsBiMapCache.mu.Lock()
		dnsBiMapCache.loaded = true
		dnsBiMapCache.path = ""
		dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
		dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
		dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
		dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
		dnsBiMapCache.mu.Unlock()
	})

	svc := &networkAssistantService{}
	svc.storeDNSBiMap([]string{"198.51.100.10"}, "direct.example.com", tunnelRouteDecision{Direct: true, Group: "direct"})

	dnsBiMapCache.mu.Lock()
	if len(dnsBiMapCache.entries) != 0 {
		dnsBiMapCache.mu.Unlock()
		t.Fatalf("direct route should not be stored, got entries=%d", len(dnsBiMapCache.entries))
	}
	dnsBiMapCache.mu.Unlock()

	svc.storeDNSBiMap([]string{"198.51.100.11"}, "proxy.example.com", tunnelRouteDecision{Direct: false, Group: "group_example", NodeID: "chain:edge-a"})

	dnsBiMapCache.mu.Lock()
	defer dnsBiMapCache.mu.Unlock()
	if len(dnsBiMapCache.entries) != 1 {
		t.Fatalf("proxy route should be stored once, got entries=%d", len(dnsBiMapCache.entries))
	}
	key := dnsBiMapKey("proxy.example.com", "198.51.100.11")
	entry, ok := dnsBiMapCache.entries[key]
	if !ok {
		t.Fatalf("missing key=%s", key)
	}
	if entry.Group != "group_example" || entry.NodeID != "chain:edge-a" {
		t.Fatalf("unexpected stored entry=%#v", entry)
	}
}

func TestLookupDNSBiMapDomainByIPPrefersNewestEntry(t *testing.T) {
	dnsBiMapCache.mu.Lock()
	dnsBiMapCache.loaded = true
	dnsBiMapCache.path = ""
	dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
	dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
	dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
	dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
	now := time.Now()
	oldEntry := dnsBiMapEntry{
		Domain:    "old.example.com",
		IP:        "198.51.100.21",
		Group:     "group_a",
		NodeID:    "chain:old",
		ExpiresAt: now.Add(30 * time.Minute),
		UpdatedAt: now.Add(-10 * time.Minute),
	}
	newEntry := dnsBiMapEntry{
		Domain:    "new.example.com",
		IP:        "198.51.100.21",
		Group:     "group_b",
		NodeID:    "chain:new",
		ExpiresAt: now.Add(30 * time.Minute),
		UpdatedAt: now,
	}
	dnsBiMapCache.entries[dnsBiMapKey(oldEntry.Domain, oldEntry.IP)] = oldEntry
	dnsBiMapCache.entries[dnsBiMapKey(newEntry.Domain, newEntry.IP)] = newEntry
	addDNSBiMapIndexLocked(oldEntry.Domain, oldEntry.IP)
	addDNSBiMapIndexLocked(newEntry.Domain, newEntry.IP)
	dnsBiMapCache.mu.Unlock()
	t.Cleanup(func() {
		dnsBiMapCache.mu.Lock()
		dnsBiMapCache.loaded = true
		dnsBiMapCache.path = ""
		dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
		dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
		dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
		dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
		dnsBiMapCache.mu.Unlock()
	})

	domain, ok := lookupDNSBiMapDomainByIP("198.51.100.21")
	if !ok {
		t.Fatal("lookup should return a domain")
	}
	if domain != "new.example.com" {
		t.Fatalf("domain=%q, want new.example.com", domain)
	}
}

func TestQuerySplitDNSCacheEntriesBackfillsDomainForIPOnlyTraffic(t *testing.T) {
	directDNSCache.mu.Lock()
	directDNSCache.entries = make(map[string]dnsCacheDirectEntry)
	directDNSCache.mu.Unlock()
	dnsBiMapCache.mu.Lock()
	dnsBiMapCache.loaded = true
	dnsBiMapCache.path = ""
	dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
	dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
	dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
	dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
	now := time.Now()
	entry := dnsBiMapEntry{
		Domain:    "proxy.example.com",
		IP:        "198.51.100.50",
		Group:     "group_example",
		NodeID:    "chain:edge-a",
		ExpiresAt: now.Add(30 * time.Minute),
		UpdatedAt: now,
	}
	dnsBiMapCache.entries[dnsBiMapKey(entry.Domain, entry.IP)] = entry
	addDNSBiMapIndexLocked(entry.Domain, entry.IP)
	dnsBiMapCache.mu.Unlock()

	t.Cleanup(func() {
		directDNSCache.mu.Lock()
		directDNSCache.entries = make(map[string]dnsCacheDirectEntry)
		directDNSCache.mu.Unlock()
		dnsBiMapCache.mu.Lock()
		dnsBiMapCache.loaded = true
		dnsBiMapCache.path = ""
		dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
		dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
		dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
		dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
		dnsBiMapCache.mu.Unlock()
	})

	monitor := newProcessMonitor()
	monitor.Start()
	monitor.appendEvent(NetworkProcessEvent{
		Kind:      NetworkProcessEventTCP,
		TargetIP:  "198.51.100.50",
		Direct:    false,
		NodeID:    "chain:edge-a",
		Group:     "group_example",
		Count:     3,
		Timestamp: 100,
	})

	svc := &networkAssistantService{
		ruleDNSCache:   make(map[string]dnsCacheEntry),
		dnsRouteHints:  make(map[string]dnsRouteHintEntry),
		processMonitor: monitor,
	}

	entries := querySplitDNSCacheEntries(svc, "proxy.example.com")
	if len(entries) == 0 {
		t.Fatal("expected filtered result for proxy.example.com")
	}
	if entries[0].Domain != "proxy.example.com" {
		t.Fatalf("domain=%q, want proxy.example.com", entries[0].Domain)
	}
	if entries[0].IP != "198.51.100.50" {
		t.Fatalf("ip=%q, want 198.51.100.50", entries[0].IP)
	}
}

func TestDNSBiMapForceRefreshOnModeSwitchDoesNotClear(t *testing.T) {
	dnsBiMapCache.mu.Lock()
	dnsBiMapCache.loaded = true
	dnsBiMapCache.path = ""
	dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
	dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
	dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
	dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
	now := time.Now()
	entry := dnsBiMapEntry{
		Domain:    "keep.example.com",
		IP:        "198.51.100.88",
		Group:     "group_example",
		NodeID:    "chain:edge-a",
		ExpiresAt: now.Add(time.Hour),
		UpdatedAt: now,
	}
	key := dnsBiMapKey(entry.Domain, entry.IP)
	dnsBiMapCache.entries[key] = entry
	addDNSBiMapIndexLocked(entry.Domain, entry.IP)
	dnsBiMapCache.mu.Unlock()
	t.Cleanup(func() {
		dnsBiMapCache.mu.Lock()
		dnsBiMapCache.loaded = true
		dnsBiMapCache.path = ""
		dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
		dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
		dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
		dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
		dnsBiMapCache.mu.Unlock()
	})

	svc := &networkAssistantService{
		ruleDNSCache:  map[string]dnsCacheEntry{"k": {Addrs: []string{"1.1.1.1"}, Expires: time.Now().Add(time.Minute)}},
		dnsRouteHints: map[string]dnsRouteHintEntry{"1.1.1.1": {Domain: "example.com", Expires: time.Now().Add(time.Minute)}},
	}
	svc.forceRefreshDNSOnModeSwitch("test")

	dnsBiMapCache.mu.Lock()
	_, ok := dnsBiMapCache.entries[key]
	dnsBiMapCache.mu.Unlock()
	if !ok {
		t.Fatal("bimap entry should survive forceRefreshDNSOnModeSwitch")
	}
}

func TestBuildAndParseLocalTUNUDPPacket(t *testing.T) {
	srcIP := net.ParseIP("10.10.0.2").To4()
	dstIP := net.ParseIP("8.8.8.8").To4()
	payload := []byte("hello-udp-over-tun")
	packet, err := buildLocalTUNUDPPacket(srcIP, dstIP, 53000, 53, payload, 123)
	if err != nil {
		t.Fatalf("buildLocalTUNUDPPacket returned error: %v", err)
	}

	frame, err := parseLocalTUNUDPPacket(packet)
	if err != nil {
		t.Fatalf("parseLocalTUNUDPPacket returned error: %v", err)
	}
	if frame.SrcIP.String() != "10.10.0.2" || frame.DstIP.String() != "8.8.8.8" {
		t.Fatalf("unexpected ips: src=%s dst=%s", frame.SrcIP.String(), frame.DstIP.String())
	}
	if frame.SrcPort != 53000 || frame.DstPort != 53 {
		t.Fatalf("unexpected ports: src=%d dst=%d", frame.SrcPort, frame.DstPort)
	}
	if !bytes.Equal(frame.Payload, payload) {
		t.Fatalf("unexpected payload: %q", string(frame.Payload))
	}
}

func TestParseLocalTUNUDPPacketRejectsNonUDP(t *testing.T) {
	packet := make([]byte, 20)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], 20)
	packet[9] = 6 // TCP
	copy(packet[12:16], net.ParseIP("10.0.0.1").To4())
	copy(packet[16:20], net.ParseIP("1.1.1.1").To4())

	if _, err := parseLocalTUNUDPPacket(packet); err == nil {
		t.Fatal("expected parse error for non-udp packet")
	}
}

func TestBuildProbeChainPortForwardsForManager(t *testing.T) {
	chain := probeLinkChainAdminItem{
		PortForwards: []struct {
			ID         string `json:"id,omitempty"`
			Name       string `json:"name,omitempty"`
			EntrySide  string `json:"entry_side,omitempty"`
			ListenHost string `json:"listen_host"`
			ListenPort int    `json:"listen_port"`
			TargetHost string `json:"target_host"`
			TargetPort int    `json:"target_port"`
			Network    string `json:"network,omitempty"`
			Enabled    bool   `json:"enabled"`
		}{
			{
				ID:         " pf-1 ",
				Name:       " edge ",
				EntrySide:  " chain_exit ",
				ListenHost: " 0.0.0.0 ",
				ListenPort: 18080,
				TargetHost: " 10.0.0.8 ",
				TargetPort: 8080,
				Network:    " tcp ",
				Enabled:    true,
			},
		},
	}

	out := buildProbeChainPortForwardsForManager(chain)
	if len(out) != 1 {
		t.Fatalf("port forwards len=%d, want 1", len(out))
	}
	if out[0].ID != "pf-1" {
		t.Fatalf("id=%q, want pf-1", out[0].ID)
	}
	if out[0].Name != "edge" {
		t.Fatalf("name=%q, want edge", out[0].Name)
	}
	if out[0].ListenHost != "0.0.0.0" {
		t.Fatalf("listen_host=%q, want 0.0.0.0", out[0].ListenHost)
	}
	if out[0].TargetHost != "10.0.0.8" {
		t.Fatalf("target_host=%q, want 10.0.0.8", out[0].TargetHost)
	}
	if out[0].Network != "tcp" {
		t.Fatalf("network=%q, want tcp", out[0].Network)
	}
	if !out[0].Enabled {
		t.Fatal("enabled=false, want true")
	}
}

func TestBackfillProbeNodeDomainsFromChains(t *testing.T) {
	nodes := []probeNodeAdminItem{
		{NodeNo: 1, DDNS: "", ServiceHost: "", BusinessDDNS: ""},
		{NodeNo: 2, DDNS: "node2.ddns.example.com", ServiceHost: "", BusinessDDNS: ""},
	}
	businessDomainByNodeID := map[string]string{
		"1": "cf-biz.example.com",
		"2": "cf-biz2.example.com",
	}
	chains := []probeLinkChainAdminItem{
		{
			ChainID: "chain-a",
			HopConfigs: []struct {
				NodeNo       int    `json:"node_no"`
				ListenHost   string `json:"listen_host,omitempty"`
				ListenPort   int    `json:"listen_port,omitempty"`
				ExternalPort int    `json:"external_port,omitempty"`
				LinkLayer    string `json:"link_layer"`
				DialMode     string `json:"dial_mode,omitempty"`
				RelayHost    string `json:"relay_host,omitempty"`
			}{
				{NodeNo: 1, RelayHost: "api.biz.example.com"},
				{NodeNo: 2, RelayHost: "api.biz2.example.com"},
			},
		},
	}

	out := backfillProbeNodeDomainsFromChains(nodes, businessDomainByNodeID, chains)
	if len(out) != 2 {
		t.Fatalf("nodes len=%d, want 2", len(out))
	}

	if out[0].BusinessDDNS != "cf-biz.example.com" {
		t.Fatalf("node1 business_ddns=%q, want cf-biz.example.com", out[0].BusinessDDNS)
	}
	if out[0].DDNS != "cf-biz.example.com" {
		t.Fatalf("node1 ddns=%q, want cf-biz.example.com", out[0].DDNS)
	}

	if out[1].BusinessDDNS != "cf-biz2.example.com" {
		t.Fatalf("node2 business_ddns=%q, want cf-biz2.example.com", out[1].BusinessDDNS)
	}
	if out[1].DDNS != "node2.ddns.example.com" {
		t.Fatalf("node2 ddns=%q, want keep original node2.ddns.example.com", out[1].DDNS)
	}
}

func TestLocalTUNUDPSourceAcquireAndReleaseLifecycle(t *testing.T) {
	svc := &networkAssistantService{
		tunUDPSources: make(map[string]*localTUNUDPSource),
	}
	srcIP := net.ParseIP("10.10.0.2").To4()
	if srcIP == nil {
		t.Fatal("src ip parse failed")
	}

	source, release := svc.acquireLocalTUNUDPSource(srcIP, 53000)
	if source == nil {
		t.Fatal("source should not be nil")
	}
	if source.refs.Load() != 1 {
		t.Fatalf("source refs=%d, want 1", source.refs.Load())
	}
	if got := buildLocalTUNUDPSourceKey(srcIP, 53000); got != source.key {
		t.Fatalf("source key=%q, want %q", source.key, got)
	}

	source2, release2 := svc.acquireLocalTUNUDPSource(srcIP, 53000)
	if source2 != source {
		t.Fatal("same source tuple should reuse source object")
	}
	if source.refs.Load() != 2 {
		t.Fatalf("source refs=%d, want 2", source.refs.Load())
	}

	release()
	if source.refs.Load() != 1 {
		t.Fatalf("source refs=%d, want 1 after first release", source.refs.Load())
	}

	release2()
	svc.mu.RLock()
	_, ok := svc.tunUDPSources[source.key]
	svc.mu.RUnlock()
	if ok {
		t.Fatal("source should be removed when refs reaches zero")
	}
}

func TestBuildAIDebugManagerUDPAssociationsPayloadIncludesSourceInfo(t *testing.T) {
	sourceKey := "10.10.0.2:53000"
	source := &localTUNUDPSource{key: sourceKey}
	source.refs.Store(3)
	source.lastActiveUnix.Store(time.Now().Unix())

	relay := &localTUNUDPRelay{
		key:              "10.10.0.2:53000->8.8.8.8:53",
		sourceKey:        sourceKey,
		srcIP:            net.ParseIP("10.10.0.2").To4(),
		dstIP:            net.ParseIP("8.8.8.8").To4(),
		srcPort:          53000,
		dstPort:          53,
		routeTarget:      "8.8.8.8:53",
		routeNodeID:      "chain:edge-a",
		routeGroup:       "group_a",
		routeDirect:      false,
		assocKeyV2:       "assoc-v2-key",
		flowID:           "flow-v2-key",
		routeFingerprint: "group_a|chain:edge-a|8.8.8.8:53",
	}
	relay.lastActiveUnix.Store(time.Now().Unix())

	svc := &networkAssistantService{
		tunUDPRelays: map[string]*localTUNUDPRelay{
			relay.key: relay,
		},
		tunUDPSources: map[string]*localTUNUDPSource{
			sourceKey: source,
		},
	}

	payload, err := buildAIDebugManagerUDPAssociationsPayload(svc)
	if err != nil {
		t.Fatalf("buildAIDebugManagerUDPAssociationsPayload returned error: %v", err)
	}
	if payload.Count != 1 || len(payload.Items) != 1 {
		t.Fatalf("payload count/items=(%d,%d), want (1,1)", payload.Count, len(payload.Items))
	}

	item := payload.Items[0]
	if item.SourceKey != sourceKey {
		t.Fatalf("source_key=%q, want %q", item.SourceKey, sourceKey)
	}
	if item.SourceRefs != 3 {
		t.Fatalf("source_refs=%d, want 3", item.SourceRefs)
	}
	if item.AssocKeyV2 != "assoc-v2-key" || item.FlowID != "flow-v2-key" {
		t.Fatalf("assoc/flow=(%q,%q), want (assoc-v2-key,flow-v2-key)", item.AssocKeyV2, item.FlowID)
	}
	if item.RouteTarget != "8.8.8.8:53" || item.NodeID != "chain:edge-a" || item.Group != "group_a" {
		t.Fatalf("route fields unexpected: route=%q node=%q group=%q", item.RouteTarget, item.NodeID, item.Group)
	}
}

func TestNextUDPDialRetryBackoff(t *testing.T) {
	if got := nextUDPDialRetryBackoff(0); got != 2*udpDialRetryInitialBackoff {
		t.Fatalf("backoff(0)=%s, want %s", got, 2*udpDialRetryInitialBackoff)
	}

	if got := nextUDPDialRetryBackoff(udpDialRetryInitialBackoff); got != 2*udpDialRetryInitialBackoff {
		t.Fatalf("backoff(initial)=%s, want %s", got, 2*udpDialRetryInitialBackoff)
	}

	if got := nextUDPDialRetryBackoff(udpDialRetryMaxBackoff); got != udpDialRetryMaxBackoff {
		t.Fatalf("backoff(max)=%s, want %s", got, udpDialRetryMaxBackoff)
	}
}

func TestIsRetryableUDPSocketErr(t *testing.T) {
	retryableErr := &net.OpError{
		Op:  "dial",
		Net: "udp",
		Err: os.NewSyscallError("sendto", errors.New("WSAENOBUFS")),
	}
	if !isRetryableUDPSocketErr(retryableErr) {
		t.Fatal("expected wrapped WSAENOBUFS to be retryable")
	}

	nonRetryableErr := &net.OpError{
		Op:  "dial",
		Net: "udp",
		Err: os.NewSyscallError("sendto", errors.New("connection refused")),
	}
	if isRetryableUDPSocketErr(nonRetryableErr) {
		t.Fatal("expected non-retryable udp dial error")
	}
}

func TestShouldFallbackLocalTUNUDPBind(t *testing.T) {
	if shouldFallbackLocalTUNUDPBind(nil) {
		t.Fatal("nil error should not trigger fallback")
	}
	if !shouldFallbackLocalTUNUDPBind(errors.New("bind: address already in use")) {
		t.Fatal("address already in use should trigger fallback")
	}
	if !shouldFallbackLocalTUNUDPBind(errors.New("connectex: The requested address is not valid in its context")) {
		t.Fatal("requested address invalid should trigger fallback")
	}
	if shouldFallbackLocalTUNUDPBind(errors.New("connection refused")) {
		t.Fatal("connection refused should not trigger fallback")
	}
}
