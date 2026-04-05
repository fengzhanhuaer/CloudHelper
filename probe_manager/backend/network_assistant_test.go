package backend

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
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
			"google":               rulePolicyActionTunnel + ":chain:1",
			"github":               rulePolicyActionTunnel + ":chain:2",
			"shared":               rulePolicyActionTunnel + ":chain:2",
			ruleFallbackGroupKey:    rulePolicyActionDirect,
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
			"direct_group":         rulePolicyActionDirect,
			"reject_group":         rulePolicyActionReject,
			ruleFallbackGroupKey:    rulePolicyActionDirect,
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

func TestRealDataCollectAutoMaintainTargetsAndResolveRelayHost(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dataDir := filepath.Clean(filepath.Join(cwd, "..", "data"))
	if _, err := os.Stat(filepath.Join(dataDir, "rule_routes.txt")); err != nil {
		t.Fatalf("real data rule_routes missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "probe_chain.json")); err != nil {
		t.Fatalf("real data probe_chain missing: %v", err)
	}

	routing, err := loadOrCreateTunnelRuleRouting()
	if err != nil {
		t.Fatalf("loadOrCreateTunnelRuleRouting: %v", err)
	}
	nodes, chainTargets, err := loadChainCacheFromFile()
	if err != nil {
		t.Fatalf("loadChainCacheFromFile: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("real data available nodes is empty")
	}
	if len(chainTargets) == 0 {
		t.Fatal("real data chain targets is empty")
	}

	targets := collectAutoMaintainPolicyTunnelNodeIDs(routing, nodes, defaultNodeID)
	if len(targets) == 0 {
		t.Fatal("real data auto maintain targets is empty")
	}
	t.Logf("real data auto maintain targets: %v", targets)

	firstTarget := strings.TrimSpace(targets[0])
	endpoint, hasChainTarget, resolvedNodeID, err := resolveProbeChainTargetFromSnapshot(firstTarget, chainTargets)
	if err != nil {
		t.Fatalf("resolveProbeChainTargetFromSnapshot: %v", err)
	}
	if !hasChainTarget {
		t.Fatalf("resolved target %s is not a chain target", firstTarget)
	}
	if strings.TrimSpace(endpoint.EntryHost) == "" {
		t.Fatalf("resolved endpoint entry host is empty: %+v", endpoint)
	}
	if strings.TrimSpace(resolvedNodeID) == "" {
		t.Fatalf("resolved node id is empty: %+v", endpoint)
	}

	resolvedIP, hostHeader, err := resolveProbeChainDialIPHostWithCache(endpoint.EntryHost, true)
	if err != nil {
		t.Fatalf("resolveProbeChainDialIPHostWithCache: host=%s err=%v", endpoint.EntryHost, err)
	}
	if net.ParseIP(strings.TrimSpace(resolvedIP)) == nil {
		t.Fatalf("resolved relay ip is invalid: %s", resolvedIP)
	}
	if strings.TrimSpace(hostHeader) == "" {
		t.Fatalf("resolved relay host header is empty: host=%s", endpoint.EntryHost)
	}
	t.Logf("resolved relay host: target=%s resolved=%s chain=%s entry_host=%s host_header=%s ip=%s", firstTarget, resolvedNodeID, endpoint.ChainID, endpoint.EntryHost, hostHeader, resolvedIP)
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
	if unmatchedDecision.BypassTUN {
		t.Fatal("expected fallback direct to stay in tun path")
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
				"direct":            rulePolicyActionTunnel + ":chain:test-chain",
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

func TestDecideRouteForTargetNormalGroupDirectStaysInTUNPath(t *testing.T) {
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
				"group_normal":      rulePolicyActionDirect,
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
	if decision.BypassTUN {
		t.Fatal("expected normal group direct policy to stay in tun path")
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
			route: tunnelRouteDecision{Direct: true, BypassTUN: false, Group: ruleFallbackGroupKey},
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
			route: tunnelRouteDecision{Direct: true, BypassTUN: false, Group: ruleFallbackGroupKey},
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
	t.Cleanup(func() {
		directDNSCache.mu.Lock()
		directDNSCache.entries = make(map[string]dnsCacheDirectEntry)
		directDNSCache.mu.Unlock()
	})

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
	if len(entries) < 2 {
		t.Fatalf("entries len=%d, want >=2", len(entries))
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
