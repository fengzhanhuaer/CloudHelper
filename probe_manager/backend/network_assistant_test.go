package backend

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

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

func TestDefaultDirectWhitelistIsEmpty(t *testing.T) {
	whitelist, err := parseDirectWhitelistRules(defaultDirectWhitelistRules)
	if err != nil {
		t.Fatalf("parseDirectWhitelistRules returned error: %v", err)
	}

	tests := []struct {
		addr string
		want bool
	}{
		{addr: "10.20.30.40:443", want: false},
		{addr: "172.20.10.10:8080", want: false},
		{addr: "192.168.1.10:80", want: false},
		{addr: "127.0.0.1:3000", want: false},
		{addr: "localhost:15030", want: false},
		{addr: "8.8.8.8:53", want: false},
	}

	for _, tt := range tests {
		got := whitelist.matchesTarget(tt.addr)
		if got != tt.want {
			t.Fatalf("matchesTarget(%q)=%v, want %v", tt.addr, got, tt.want)
		}
	}
}

func TestDirectWhitelistIPv6HostPortFormat(t *testing.T) {
	whitelist, err := parseDirectWhitelistRules([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("parseDirectWhitelistRules returned error: %v", err)
	}

	ipv6 := netip.MustParseAddr("2001:db8::1")
	if whitelist.matchesTarget(net.JoinHostPort(ipv6.String(), "443")) {
		t.Fatal("unexpected whitelist match for IPv6 target")
	}
}

func TestDirectWhitelistHostSuffixMatch(t *testing.T) {
	whitelist, err := parseDirectWhitelistRules([]string{"example.com"})
	if err != nil {
		t.Fatalf("parseDirectWhitelistRules returned error: %v", err)
	}

	tests := []struct {
		addr string
		want bool
	}{
		{addr: "example.com:443", want: true},
		{addr: "api.example.com:443", want: true},
		{addr: "deep.api.example.com:443", want: true},
		{addr: "API.EXAMPLE.COM:443", want: true},
		{addr: "badexample.com:443", want: false},
		{addr: "example.com.cn:443", want: false},
	}

	for _, tt := range tests {
		got := whitelist.matchesTarget(tt.addr)
		if got != tt.want {
			t.Fatalf("matchesTarget(%q)=%v, want %v", tt.addr, got, tt.want)
		}
	}
}

func TestParseTunnelRuleLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantKind  ruleMatcherKind
		wantGroup string
	}{
		{name: "domain suffix", line: "example.com,group_a", wantKind: ruleMatcherDomainSuffix, wantGroup: "group_a"},
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
		"example.com\n" +
		"}\n" +
		"direct_local\n" +
		"{\n" +
		"localhost\n" +
		"}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	ruleSet, err := parseTunnelRuleFile(path)
	if err != nil {
		t.Fatalf("parseTunnelRuleFile returned error: %v", err)
	}
	if len(ruleSet.Rules) != 4 {
		t.Fatalf("rule count=%d, want 4", len(ruleSet.Rules))
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
	if ruleSet.Rules[3].Group != "direct_local" || ruleSet.Rules[3].Kind != ruleMatcherDomainSuffix {
		t.Fatalf("rule 3 = %#v", ruleSet.Rules[3])
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
			{Kind: ruleMatcherDomainSuffix, Suffix: "example.com", Group: "domain"},
			{Kind: ruleMatcherIP, IP: "1.2.3.4", Group: "ip"},
			{Kind: ruleMatcherCIDR, CIDR: cidr, Group: "cidr"},
		},
	}

	if got, ok := rules.matchHost("www.example.com"); !ok || got.Group != "domain" {
		t.Fatalf("domain match failed: ok=%v group=%s", ok, got.Group)
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
		directWhitelist: &socksDirectWhitelist{
			hosts: map[string]struct{}{},
			ips:   map[string]struct{}{},
			cidrs: []*net.IPNet{},
		},
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
		directWhitelist: &socksDirectWhitelist{
			hosts: map[string]struct{}{},
			ips:   map[string]struct{}{},
			cidrs: []*net.IPNet{},
		},
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
}

func TestDecideRouteForTargetControlPlaneAlwaysDirect(t *testing.T) {
	_, cidr, err := net.ParseCIDR("0.0.0.0/0")
	if err != nil {
		t.Fatalf("parse cidr: %v", err)
	}
	service := &networkAssistantService{
		mode:           networkModeTUN,
		nodeID:         "chain:test-chain",
		availableNodes: []string{"cloudserver", "chain:test-chain"},
		directWhitelist: &socksDirectWhitelist{
			hosts: map[string]struct{}{},
			ips:   map[string]struct{}{},
			cidrs: []*net.IPNet{},
		},
		ruleRouting: tunnelRuleRouting{
			RuleSet: tunnelRuleSet{
				Rules: []tunnelRule{
					{Kind: ruleMatcherCIDR, CIDR: cidr, Group: "group_all"},
				},
			},
			GroupNodeMap: map[string]string{
				"group_all": rulePolicyActionTunnel + ":chain:test-chain",
			},
		},
		ruleDNSCache: make(map[string]dnsCacheEntry),
		controlPlaneHosts: map[string]struct{}{
			"controller.example.com": {},
		},
		controlPlaneIPs: map[string]struct{}{
			"203.0.113.10": {},
		},
	}

	hostDecision, err := service.decideRouteForTarget("controller.example.com:443")
	if err != nil {
		t.Fatalf("decideRouteForTarget by host returned error: %v", err)
	}
	if !hostDecision.Direct {
		t.Fatal("expected controller host to be forced direct")
	}

	ipDecision, err := service.decideRouteForTarget("203.0.113.10:443")
	if err != nil {
		t.Fatalf("decideRouteForTarget by ip returned error: %v", err)
	}
	if !ipDecision.Direct {
		t.Fatal("expected controller ip to be forced direct")
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
		directWhitelist: &socksDirectWhitelist{
			hosts: map[string]struct{}{},
			ips:   map[string]struct{}{},
			cidrs: []*net.IPNet{},
		},
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
		directWhitelist: &socksDirectWhitelist{
			hosts: map[string]struct{}{},
			ips:   map[string]struct{}{},
			cidrs: []*net.IPNet{},
		},
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
		directWhitelist: &socksDirectWhitelist{
			hosts: map[string]struct{}{},
			ips:   map[string]struct{}{},
			cidrs: []*net.IPNet{},
		},
		ruleRouting: tunnelRuleRouting{
			RuleSet: tunnelRuleSet{
				Rules: []tunnelRule{
					{Kind: ruleMatcherDomainSuffix, Suffix: "example.com", Group: "group_example"},
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

func TestDecideRouteForTargetUsesDNSRouteHintForIP(t *testing.T) {
	service := &networkAssistantService{
		mode:           networkModeTUN,
		nodeID:         "cloudserver",
		availableNodes: []string{"cloudserver", "chain:edge-a"},
		directWhitelist: &socksDirectWhitelist{
			hosts: map[string]struct{}{},
			ips:   map[string]struct{}{},
			cidrs: []*net.IPNet{},
		},
		ruleRouting: tunnelRuleRouting{
			RuleSet:      tunnelRuleSet{Rules: []tunnelRule{}},
			GroupNodeMap: map[string]string{ruleFallbackGroupKey: rulePolicyActionDirect},
		},
		ruleDNSCache: make(map[string]dnsCacheEntry),
		dnsRouteHints: map[string]dnsRouteHintEntry{
			"203.0.113.7": {
				Direct:  false,
				NodeID:  "chain:edge-a",
				Group:   "group_example",
				Expires: time.Now().Add(30 * time.Second),
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
		{NodeNo: 1, DDNS: "", ServiceHost: "", BusinessDDNS: "", BusinessDDNSFullDomain: ""},
		{NodeNo: 2, DDNS: "node2.ddns.example.com", ServiceHost: "", BusinessDDNS: "", BusinessDDNSFullDomain: ""},
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
	if out[0].BusinessDDNSFullDomain != "cf-biz.example.com" {
		t.Fatalf("node1 business_ddns_full_domain=%q, want cf-biz.example.com", out[0].BusinessDDNSFullDomain)
	}
	if out[0].DDNS != "cf-biz.example.com" {
		t.Fatalf("node1 ddns=%q, want cf-biz.example.com", out[0].DDNS)
	}

	if out[1].BusinessDDNS != "cf-biz2.example.com" {
		t.Fatalf("node2 business_ddns=%q, want cf-biz2.example.com", out[1].BusinessDDNS)
	}
	if out[1].BusinessDDNSFullDomain != "cf-biz2.example.com" {
		t.Fatalf("node2 business_ddns_full_domain=%q, want cf-biz2.example.com", out[1].BusinessDDNSFullDomain)
	}
	if out[1].DDNS != "node2.ddns.example.com" {
		t.Fatalf("node2 ddns=%q, want keep original node2.ddns.example.com", out[1].DDNS)
	}
}
