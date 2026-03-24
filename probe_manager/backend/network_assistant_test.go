package backend

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
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

func TestSocks5HandshakeNoAuth(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	respCh := make(chan []byte, 1)
	errCh := make(chan error, 1)

	go func() {
		if _, err := client.Write([]byte{0x05, 0x01, 0x00}); err != nil {
			errCh <- err
			return
		}
		buf := make([]byte, 2)
		if _, err := client.Read(buf); err != nil {
			errCh <- err
			return
		}
		respCh <- buf
	}()

	if err := socks5Handshake(bufio.NewReader(server), server); err != nil {
		t.Fatalf("socks5Handshake returned error: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("client side error: %v", err)
	case resp := <-respCh:
		expect := []byte{0x05, 0x00}
		if !bytes.Equal(resp, expect) {
			t.Fatalf("unexpected handshake response: %v", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for handshake response")
	}
}

func TestSocks5ReadConnectRequestDomain(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	request := []byte{
		0x05, 0x01, 0x00, 0x03,
		0x0b,
		'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm',
		0x00, 0x50,
	}

	req, err := socks5ReadRequest(bufio.NewReader(bytes.NewReader(request)), server)
	if err != nil {
		t.Fatalf("socks5ReadRequest returned error: %v", err)
	}
	if req.Address != "example.com:80" {
		t.Fatalf("unexpected target address: %s", req.Address)
	}
	if req.Cmd != 0x01 {
		t.Fatalf("unexpected socks cmd: %d", req.Cmd)
	}
}

func TestSocks4ReadConnectRequestIPv4(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	request := []byte{
		0x04, 0x01,
		0x00, 0x50,
		0x5d, 0xb8, 0xd8, 0x22,
		'u', 0x00,
	}

	req, err := socks4ReadRequest(bufio.NewReader(bytes.NewReader(request)), server)
	if err != nil {
		t.Fatalf("socks4ReadRequest returned error: %v", err)
	}
	if req.Address != "93.184.216.34:80" {
		t.Fatalf("unexpected target address: %s", req.Address)
	}
	if req.Cmd != 0x01 {
		t.Fatalf("unexpected socks cmd: %d", req.Cmd)
	}
}

func TestSocks4ReadConnectRequestDomain(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	request := []byte{
		0x04, 0x01,
		0x01, 0xbb,
		0x00, 0x00, 0x00, 0x01,
		0x00,
		'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm',
		0x00,
	}

	req, err := socks4ReadRequest(bufio.NewReader(bytes.NewReader(request)), server)
	if err != nil {
		t.Fatalf("socks4ReadRequest returned error: %v", err)
	}
	if req.Address != "example.com:443" {
		t.Fatalf("unexpected target address: %s", req.Address)
	}
	if req.Cmd != 0x01 {
		t.Fatalf("unexpected socks cmd: %d", req.Cmd)
	}
}

func TestDefaultDirectWhitelistMatchesPrivateRanges(t *testing.T) {
	whitelist, err := parseDirectWhitelistRules(defaultDirectWhitelistRules)
	if err != nil {
		t.Fatalf("parseDirectWhitelistRules returned error: %v", err)
	}

	tests := []struct {
		addr string
		want bool
	}{
		{addr: "10.20.30.40:443", want: true},
		{addr: "172.20.10.10:8080", want: true},
		{addr: "192.168.1.10:80", want: true},
		{addr: "127.0.0.1:3000", want: true},
		{addr: "localhost:15030", want: true},
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
		mode:           networkModeRule,
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

func TestDecideRouteForTargetRuleModeRejectMatchedGroup(t *testing.T) {
	_, cidr, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parse cidr: %v", err)
	}
	service := &networkAssistantService{
		mode:   networkModeRule,
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
		mode:   networkModeRule,
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
