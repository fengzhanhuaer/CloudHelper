package main

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestDefaultProbeLocalDNSUpstreamsBootstrapWithoutSystemDNS(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	probeLocalDNSBootstrapLookupIPv4 = func(string) ([]string, error) {
		t.Fatal("default encrypted DNS upstreams must not require system or plain DNS bootstrap")
		return nil, nil
	}
	for _, host := range []string{"dns.alidns.com", "doh.pub", "dot.pub"} {
		ips, err := resolveProbeLocalDNSIPv4s(host)
		if err != nil || len(ips) == 0 || net.ParseIP(ips[0]) == nil {
			t.Fatalf("bootstrap host=%s ips=%v err=%v", host, ips, err)
		}
	}
}

func TestProbeLocalDNSBootstrapPrefersSystemServers(t *testing.T) {
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)
	probeLocalDNSSystemServers = func() []string {
		return []string{"172.18.52.205", "223.5.5.5"}
	}

	got := currentProbeLocalDNSBootstrapServerTargets()
	want := []string{"172.18.52.205:53", "223.5.5.5:53", "119.29.29.29:53"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("bootstrap servers=%v want=%v", got, want)
	}
}

func TestProbeLocalDNSBootstrapFallsBackAfterSystemServerFailure(t *testing.T) {
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)
	probeLocalDNSSystemServers = func() []string { return []string{"172.18.52.205"} }

	var queried []string
	probeLocalDNSBootstrapQuery = func(server string, packet []byte) ([]byte, error) {
		queried = append(queried, server)
		if server == "172.18.52.205:53" {
			return nil, errors.New("system dns unavailable")
		}
		var message dnsmessage.Message
		if err := message.Unpack(packet); err != nil {
			return nil, err
		}
		message.Header.Response = true
		message.Answers = []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: message.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
			Body:   &dnsmessage.AResource{A: [4]byte{203, 0, 113, 41}},
		}}
		return message.Pack()
	}

	ips, err := bootstrapProbeLocalDNSResolveIPv4s("controller.example")
	if err != nil {
		t.Fatalf("bootstrap fallback failed: %v", err)
	}
	if strings.Join(ips, ",") != "203.0.113.41" {
		t.Fatalf("bootstrap ips=%v", ips)
	}
	if strings.Join(queried, ",") != "172.18.52.205:53,223.5.5.5:53" {
		t.Fatalf("queried servers=%v", queried)
	}
}

func TestProbeLocalDNSExternalAndRelayResolutionUseFullUpstreamChain(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	queries := make([]string, 0, 2)
	probeLocalDNSQueryUpstream = func(candidate probeLocalDNSUpstreamCandidate, packet []byte) ([]byte, error) {
		if candidate.Kind != "doh" {
			t.Fatalf("first built-in upstream kind=%q want doh", candidate.Kind)
		}
		var message dnsmessage.Message
		if err := message.Unpack(packet); err != nil {
			t.Fatal(err)
		}
		if len(message.Questions) != 1 {
			t.Fatalf("questions=%d want 1", len(message.Questions))
		}
		domain := strings.TrimSuffix(message.Questions[0].Name.String(), ".")
		queries = append(queries, domain)
		message.Header.Response = true
		message.Answers = []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: message.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
			Body:   &dnsmessage.AResource{A: [4]byte{203, 0, 113, 40}},
		}}
		return message.Pack()
	}

	externalQuery, err := buildProbeLocalDNSQueryA("www.external.example")
	if err != nil {
		t.Fatal(err)
	}
	response, externalIPs, err := resolveProbeVirtualRouterDNSUpstreamResponse(externalQuery, "www.external.example", probeLocalDNSRouteDecision{Action: "direct"})
	if err != nil || len(response) == 0 || len(externalIPs) != 1 || externalIPs[0] != "203.0.113.40" {
		t.Fatalf("external resolution response=%d ips=%v err=%v", len(response), externalIPs, err)
	}

	relayIPs, err := defaultProbeRouteRelayLookupIP(context.Background(), "ip", "relay.external.example")
	if err != nil || len(relayIPs) != 1 || relayIPs[0].String() != "203.0.113.40" {
		t.Fatalf("relay resolution ips=%v err=%v", relayIPs, err)
	}
	if got := strings.Join(queries, ","); got != "www.external.example,relay.external.example" {
		t.Fatalf("resolved domains=%q", got)
	}
}

func TestProbeLocalDNSFakeIPPersistsOnFlushAndReloads(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	decision := probeLocalDNSRouteDecision{Group: "media", Action: "tunnel", SelectedRouteID: "route-1", TunnelNodeID: "route:route-1"}
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision)
	if !ok || strings.TrimSpace(fakeIP) == "" {
		t.Fatalf("allocate fake ip failed: ip=%q ok=%v", fakeIP, ok)
	}
	cachePath, err := resolveProbeLocalDNSCachePath()
	if err != nil {
		t.Fatalf("resolve dns cache path failed: %v", err)
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fake ip allocation should wait for batched persist, stat err=%v", err)
	}
	flushProbeLocalDNSCacheToDisk()
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("fake ip allocation should persist cache after flush: %v", err)
	}

	resetProbeLocalDNSServiceForTest()

	entry, ok := lookupProbeLocalDNSFakeIPEntry(fakeIP)
	if !ok {
		t.Fatalf("reloaded fake ip %s not found", fakeIP)
	}
	if entry.Domain != "api.example.com" || entry.FakeIP != fakeIP {
		t.Fatalf("reloaded fake ip entry=%+v", entry)
	}
	reusedIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision)
	if !ok || reusedIP != fakeIP {
		t.Fatalf("reused fake ip=%q ok=%v want=%q", reusedIP, ok, fakeIP)
	}
}

func TestProbeLocalDNSFakeIPSkipsVirtualRouterProbeReserve(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	decision := probeLocalDNSRouteDecision{Group: "media", Action: "tunnel", SelectedRouteID: "route-1", TunnelNodeID: "route:route-1"}
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision)
	if !ok {
		t.Fatalf("allocate fake ip failed")
	}
	if fakeIP != "198.18.4.1" {
		t.Fatalf("fake ip=%q, want first ordinary fake ip after reserved 1024 probe IPs", fakeIP)
	}
}

func TestNormalizeProbeLocalDNSResponseTTL(t *testing.T) {
	packet := buildDNSResponseWithTTLForTest(t, 37)
	normalized := normalizeProbeLocalDNSResponseTTL(packet)
	if ttl := dnsResponseFirstAnswerTTLForTest(t, normalized); ttl != 600 {
		t.Fatalf("normalized desktop dns ttl=%d, want 600", ttl)
	}
}

func TestProbeVirtualRouterLocalSettingsMissingFieldKeepsDefaultDisabled(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)
	path, err := resolveProbeVirtualRouterLocalSettingsPath()
	if err != nil {
		t.Fatalf("resolve settings path failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"virtual_router_enabled":false}`), 0o644); err != nil {
		t.Fatalf("write settings failed: %v", err)
	}
	settings := loadProbeVirtualRouterLocalSettings()
	if settings.VirtualRouterEnabled || settings.VirtualDNSEnabled {
		t.Fatalf("settings=%+v, want router=false dns default false", settings)
	}
}

func TestResolveProbeVirtualRouterDNSResponseUsesControllerFakeIPForExitRule(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(true, true)
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:         "rr-reddit",
			Name:       "reddit",
			Action:     "probe_exit",
			ExitNodeID: "9",
			Entries:    []string{"domain_suffix:reddit.com"},
		}},
	})
	defer restore()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	oldRequest := probeRequestRouteFakeIP
	probeRequestRouteFakeIP = func(ctx context.Context, controllerBaseURL string, identity nodeIdentity, domain string, rule probeVirtualRouterRouteRule) (probeVirtualRouterFakeIPEntry, probeVirtualRouterFakeIPLibrary, error) {
		entry := probeVirtualRouterFakeIPEntry{
			Domain:     domain,
			FakeIP:     "198.18.4.9",
			RuleID:     rule.ID,
			Action:     rule.Action,
			ExitNodeID: rule.ExitNodeID,
			ExpiresAt:  time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339),
			UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		}
		return entry, probeVirtualRouterFakeIPLibrary{
			Version:   2,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Items:     []probeVirtualRouterFakeIPEntry{entry},
		}, nil
	}
	rememberProbeVirtualRouterController(nodeIdentity{NodeID: "1", Secret: "secret-1"}, "https://controller.example")
	t.Cleanup(func() {
		probeRequestRouteFakeIP = oldRequest
		rememberProbeVirtualRouterController(nodeIdentity{}, "")
	})

	packet, err := buildProbeLocalDNSQueryA("www.reddit.com")
	if err != nil {
		t.Fatalf("build dns query failed: %v", err)
	}
	result, err := resolveProbeVirtualRouterDNSPacket(packet)
	if err != nil {
		t.Fatalf("first resolve virtual router dns failed: %v", err)
	}
	if got := strings.Join(extractProbeLocalDNSResponseIPsBestEffort(result.Response), ","); got != "198.18.4.9" {
		t.Fatalf("response fake ip=%q", got)
	}
	if ttl := dnsResponseFirstAnswerTTLForTest(t, result.Response); ttl != uint32((10*time.Minute)/time.Second) {
		t.Fatalf("response ttl=%d, want 600", ttl)
	}
	if item, ok := currentProbeVirtualRouterFakeIPEntryByDomain("www.reddit.com"); !ok || item.FakeIP != "198.18.4.9" {
		t.Fatalf("cached fake ip entry=%+v ok=%v", item, ok)
	} else if expiresAt, err := time.Parse(time.RFC3339, item.ExpiresAt); err != nil || time.Until(expiresAt) > 49*time.Hour || time.Until(expiresAt) < 47*time.Hour {
		t.Fatalf("local fake ip ttl should be about 48h, expires_at=%q err=%v", item.ExpiresAt, err)
	}
}

func TestResolveProbeVirtualRouterFakeIPForDNSPrefersLocalCache(t *testing.T) {
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Version:   9,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "www.reddit.com",
				FakeIP:     "198.18.4.9",
				RuleID:     "rr-reddit",
				Action:     "probe_exit",
				ExitNodeID: "9",
				ExpiresAt:  time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339),
				UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
			}},
		},
	})
	defer restore()

	oldRequest := probeRequestRouteFakeIP
	requests := 0
	probeRequestRouteFakeIP = func(ctx context.Context, controllerBaseURL string, identity nodeIdentity, domain string, rule probeVirtualRouterRouteRule) (probeVirtualRouterFakeIPEntry, probeVirtualRouterFakeIPLibrary, error) {
		requests++
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, errors.New("controller should not be called for cached fake ip")
	}
	rememberProbeVirtualRouterController(nodeIdentity{NodeID: "1", Secret: "secret-1"}, "https://controller.example")
	t.Cleanup(func() {
		probeRequestRouteFakeIP = oldRequest
		rememberProbeVirtualRouterController(nodeIdentity{}, "")
	})

	item, err := resolveProbeVirtualRouterFakeIPForDNS("www.reddit.com", probeVirtualRouterRouteRule{
		ID:         "rr-reddit",
		Action:     "probe_exit",
		ExitNodeID: "9",
	})
	if err != nil {
		t.Fatalf("resolve cached fake ip failed: %v", err)
	}
	if item.FakeIP != "198.18.4.9" || requests != 0 {
		t.Fatalf("item=%+v controller_requests=%d", item, requests)
	}
}

func TestResolveProbeVirtualRouterDNSResponseReject(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(true, true)
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:      "rr-reject",
			Name:    "reject",
			Action:  "reject",
			Entries: []string{"domain_keyword:block-me"},
		}},
	})
	defer restore()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	rejectPacket, err := buildProbeLocalDNSQueryA("block-me.example")
	if err != nil {
		t.Fatalf("build reject dns query failed: %v", err)
	}
	rejectResult, err := resolveProbeVirtualRouterDNSPacket(rejectPacket)
	if err != nil {
		t.Fatalf("resolve reject dns failed: %v", err)
	}
	if dnsResponseRCodeForTest(t, rejectResult.Response) != dnsmessage.RCodeRefused {
		t.Fatalf("reject rcode=%v", dnsResponseRCodeForTest(t, rejectResult.Response))
	}
}

func TestResolveProbeVirtualRouterDNSDirectResponsePreparesBypass(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(true, true)
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
	})
	defer restore()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	oldResolve := probeVirtualRouterDNSResolveRealPacket
	oldEnsure := probeVirtualRouterEnsureDirectBypass
	response := []byte{1, 2, 3}
	probeVirtualRouterDNSResolveRealPacket = func([]byte, string) ([]byte, []string, error) {
		return response, []string{"203.0.113.10", "203.0.113.11"}, nil
	}
	var targets []string
	probeVirtualRouterEnsureDirectBypass = func(target string) error {
		targets = append(targets, target)
		return nil
	}
	t.Cleanup(func() {
		probeVirtualRouterDNSResolveRealPacket = oldResolve
		probeVirtualRouterEnsureDirectBypass = oldEnsure
	})

	packet, err := buildProbeLocalDNSQueryA("direct.example.com")
	if err != nil {
		t.Fatalf("build direct dns query failed: %v", err)
	}
	result, err := resolveProbeVirtualRouterDNSPacket(packet)
	if err != nil {
		t.Fatalf("resolve direct dns failed: %v", err)
	}
	if string(result.Response) != string(response) {
		t.Fatalf("response=%v, want %v", result.Response, response)
	}
	if got := strings.Join(targets, ","); got != "203.0.113.10:0,203.0.113.11:0" {
		t.Fatalf("direct bypass targets=%q", got)
	}
}

func TestResolveProbeVirtualRouterDNSDirectResponseWaitsForBypass(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(true, true)
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
	})
	defer restore()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	oldResolve := probeVirtualRouterDNSResolveRealPacket
	oldEnsure := probeVirtualRouterEnsureDirectBypass
	probeVirtualRouterDNSResolveRealPacket = func([]byte, string) ([]byte, []string, error) {
		return []byte{1, 2, 3}, []string{"203.0.113.10"}, nil
	}
	probeVirtualRouterEnsureDirectBypass = func(string) error {
		return errors.New("route create failed")
	}
	t.Cleanup(func() {
		probeVirtualRouterDNSResolveRealPacket = oldResolve
		probeVirtualRouterEnsureDirectBypass = oldEnsure
	})

	packet, err := buildProbeLocalDNSQueryA("direct.example.com")
	if err != nil {
		t.Fatalf("build direct dns query failed: %v", err)
	}
	result, err := resolveProbeVirtualRouterDNSPacket(packet)
	if err == nil || !strings.Contains(err.Error(), "route create failed") {
		t.Fatalf("resolve direct dns err=%v", err)
	}
	if len(result.Response) != 0 {
		t.Fatalf("dns answer must not be published before bypass is ready: %v", result.Response)
	}
}

func TestResolveProbeVirtualRouterDNSDirectResponsePreservesIPExitRule(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(true, true)
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:         "rr-ip-exit",
			Name:       "IP exit",
			Action:     "probe_exit",
			ExitNodeID: "9",
			Entries:    []string{"cidr:203.0.113.0/24"},
		}},
	})
	defer restore()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	oldResolve := probeVirtualRouterDNSResolveRealPacket
	oldEnsure := probeVirtualRouterEnsureDirectBypass
	probeVirtualRouterDNSResolveRealPacket = func([]byte, string) ([]byte, []string, error) {
		return []byte{1, 2, 3}, []string{"203.0.113.10"}, nil
	}
	probeVirtualRouterEnsureDirectBypass = func(target string) error {
		t.Fatalf("ip-routed exit target must not receive a direct bypass: %s", target)
		return nil
	}
	t.Cleanup(func() {
		probeVirtualRouterDNSResolveRealPacket = oldResolve
		probeVirtualRouterEnsureDirectBypass = oldEnsure
	})

	packet, err := buildProbeLocalDNSQueryA("unmatched.example.com")
	if err != nil {
		t.Fatalf("build dns query failed: %v", err)
	}
	result, err := resolveProbeVirtualRouterDNSPacket(packet)
	if err != nil || len(result.Response) == 0 {
		t.Fatalf("resolve ip-routed dns response failed: response=%v err=%v", result.Response, err)
	}
}

func TestResolveProbeVirtualRouterDNSExplicitDirectRuleOverridesIPExitRule(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(true, true)
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		RouteRules: []probeVirtualRouterRouteRule{
			{ID: "rr-domain-direct", Name: "domain direct", Action: "direct", Entries: []string{"domain_suffix:direct.example"}},
			{ID: "rr-ip-exit", Name: "IP exit", Action: "probe_exit", ExitNodeID: "9", Entries: []string{"cidr:203.0.113.0/24"}},
		},
	})
	defer restore()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
		probeVirtualRouterDNSDirectPriorityState.mu.Lock()
		probeVirtualRouterDNSDirectPriorityState.ips = map[string]time.Time{}
		probeVirtualRouterDNSDirectPriorityState.mu.Unlock()
	})

	oldResolve := probeVirtualRouterDNSResolveRealPacket
	oldEnsure := probeVirtualRouterEnsureDirectBypass
	probeVirtualRouterDNSResolveRealPacket = func([]byte, string) ([]byte, []string, error) {
		return []byte{1, 2, 3}, []string{"203.0.113.10"}, nil
	}
	var targets []string
	probeVirtualRouterEnsureDirectBypass = func(target string) error {
		targets = append(targets, target)
		return nil
	}
	t.Cleanup(func() {
		probeVirtualRouterDNSResolveRealPacket = oldResolve
		probeVirtualRouterEnsureDirectBypass = oldEnsure
	})

	packet, err := buildProbeLocalDNSQueryA("api.direct.example")
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolveProbeVirtualRouterDNSPacket(packet)
	if err != nil || len(result.Response) == 0 {
		t.Fatalf("explicit direct domain lookup failed: response=%v err=%v", result.Response, err)
	}
	if strings.Join(targets, ",") != "203.0.113.10:0" {
		t.Fatalf("explicit direct domain did not prepare bypass: %v", targets)
	}
	if !probeVirtualRouterDNSDirectPriorityIP(net.ParseIP("203.0.113.10")) {
		t.Fatal("explicit direct domain IP was not protected from lower-priority IP routing")
	}
}

func setProbeVirtualRouterDNSConfigForTest(t *testing.T, config probeVirtualRouterConfig) func() {
	t.Helper()
	probeVirtualRouterState.mu.Lock()
	oldConfig := probeVirtualRouterState.config
	oldLocalNodeID := probeVirtualRouterState.localNodeID
	oldLocalIP := probeVirtualRouterState.localIP
	oldNodeToIP := probeVirtualRouterState.nodeToIP
	oldIPToNode := probeVirtualRouterState.ipToNode
	oldNeighbors := probeVirtualRouterState.neighbors
	oldRulesByID := probeVirtualRouterState.rulesByID
	oldSignature := probeVirtualRouterState.topologySignature
	sanitized := sanitizeProbeVirtualRouterConfigForCache(config)
	index := buildProbeVirtualRouterTopologyIndex(sanitized)
	probeVirtualRouterState.config = sanitized
	probeVirtualRouterState.localNodeID = "1"
	probeVirtualRouterState.localIP = index.nodeToIP["1"]
	probeVirtualRouterState.nodeToIP = index.nodeToIP
	probeVirtualRouterState.ipToNode = index.ipToNode
	probeVirtualRouterState.neighbors = index.neighbors
	probeVirtualRouterState.rulesByID = index.rulesByID
	probeVirtualRouterState.topologySignature = "test"
	probeVirtualRouterState.mu.Unlock()
	return func() {
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = oldConfig
		probeVirtualRouterState.localNodeID = oldLocalNodeID
		probeVirtualRouterState.localIP = oldLocalIP
		probeVirtualRouterState.nodeToIP = oldNodeToIP
		probeVirtualRouterState.ipToNode = oldIPToNode
		probeVirtualRouterState.neighbors = oldNeighbors
		probeVirtualRouterState.rulesByID = oldRulesByID
		probeVirtualRouterState.topologySignature = oldSignature
		probeVirtualRouterState.mu.Unlock()
	}
}

func enableProbeVirtualRouterLocalSettingsForTest(routerEnabled bool, dnsEnabled bool) {
	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.loaded = true
	probeVirtualRouterLocalSettingsState.settings = probeVirtualRouterLocalSettings{
		VirtualRouterEnabled: routerEnabled,
		VirtualDNSEnabled:    dnsEnabled,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339),
	}
	probeVirtualRouterLocalSettingsState.mu.Unlock()
}

func dnsResponseRCodeForTest(t *testing.T, packet []byte) dnsmessage.RCode {
	t.Helper()
	parser := dnsmessage.Parser{}
	header, err := parser.Start(packet)
	if err != nil {
		t.Fatalf("parse dns response failed: %v", err)
	}
	return header.RCode
}

func dnsResponseFirstAnswerTTLForTest(t *testing.T, packet []byte) uint32 {
	t.Helper()
	parser := dnsmessage.Parser{}
	if _, err := parser.Start(packet); err != nil {
		t.Fatalf("parse dns response failed: %v", err)
	}
	if err := parser.SkipAllQuestions(); err != nil {
		t.Fatalf("skip dns questions failed: %v", err)
	}
	header, err := parser.AnswerHeader()
	if err != nil {
		t.Fatalf("read dns answer header failed: %v", err)
	}
	return header.TTL
}

func buildDNSResponseWithTTLForTest(t *testing.T, ttl uint32) []byte {
	t.Helper()
	name, err := dnsmessage.NewName("example.com.")
	if err != nil {
		t.Fatalf("build dns name failed: %v", err)
	}
	message := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 7, Response: true, RCode: dnsmessage.RCodeSuccess},
		Questions: []dnsmessage.Question{{
			Name:  name,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: ttl},
			Body:   &dnsmessage.AResource{A: [4]byte{203, 0, 113, 10}},
		}},
	}
	packet, err := message.Pack()
	if err != nil {
		t.Fatalf("pack dns response failed: %v", err)
	}
	return packet
}
