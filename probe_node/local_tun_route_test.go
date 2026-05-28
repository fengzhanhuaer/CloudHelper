package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"golang.org/x/net/dns/dnsmessage"
)

func TestDecideProbeLocalRouteForTargetDirectWhenProxyDirectMode(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = false
	probeLocalControl.proxy.Mode = probeLocalProxyModeDirect
	probeLocalControl.mu.Unlock()

	route, err := decideProbeLocalRouteForTarget("api.example.com:443")
	if err != nil {
		t.Fatalf("decideProbeLocalRouteForTarget returned error: %v", err)
	}
	if !route.Direct || route.Reject {
		t.Fatalf("route=%+v", route)
	}
}

func TestDecideProbeLocalRouteForTargetTunnelByDomainRule(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "media", Rules: []string{"domain_suffix:example.com"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "media", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	route, err := decideProbeLocalRouteForTarget("api.example.com:443")
	if err != nil {
		t.Fatalf("decideProbeLocalRouteForTarget returned error: %v", err)
	}
	if route.Direct || route.Reject {
		t.Fatalf("route=%+v", route)
	}
	if route.Group != "media" {
		t.Fatalf("group=%q", route.Group)
	}
	if route.SelectedChainID != "chain-proxy-1" {
		t.Fatalf("selected_chain_id=%q", route.SelectedChainID)
	}
	if route.TunnelNodeID != "chain:chain-proxy-1" {
		t.Fatalf("tunnel_node_id=%q", route.TunnelNodeID)
	}
	if route.GroupRuntime == nil {
		t.Fatal("group_runtime should not be nil")
	}
}

func TestDecideProbeLocalRouteForTargetRejectByRule(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "blocked", Rules: []string{"domain_suffix:blocked.example"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "blocked", Action: "reject"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	route, err := decideProbeLocalRouteForTarget("x.blocked.example:443")
	if err == nil {
		t.Fatal("expected reject error")
	}
	var rejectErr *probeLocalRouteRejectError
	if !errors.As(err, &rejectErr) {
		t.Fatalf("unexpected err type: %T err=%v", err, err)
	}
	if !route.Reject {
		t.Fatalf("route=%+v", route)
	}
}

func TestDecideProbeLocalRouteForTargetTunnelByFakeIP(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "media", Rules: []string{"domain_suffix:example.com"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "media", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
	})
	storeProbeLocalDNSCacheRecords("api.example.com", []string{"203.0.113.44", "203.0.113.45"})

	dnsDecision := resolveProbeLocalProxyRouteDecisionByDomain("api.example.com")
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", dnsDecision)
	if !ok {
		t.Fatal("allocate fake ip failed")
	}
	if net.ParseIP(fakeIP) == nil {
		t.Fatalf("fake ip=%q", fakeIP)
	}

	route, err := decideProbeLocalRouteForTarget(net.JoinHostPort(fakeIP, "443"))
	if err != nil {
		t.Fatalf("decideProbeLocalRouteForTarget returned error: %v", err)
	}
	if route.Direct || route.Reject {
		t.Fatalf("route=%+v", route)
	}
	if route.SelectedChainID != "chain-proxy-1" {
		t.Fatalf("selected_chain_id=%q", route.SelectedChainID)
	}
	if route.TunnelNodeID != "chain:chain-proxy-1" {
		t.Fatalf("tunnel_node_id=%q", route.TunnelNodeID)
	}
	if route.GroupRuntime == nil {
		t.Fatal("group_runtime should not be nil")
	}
	if route.TargetAddr != "203.0.113.44:443" {
		t.Fatalf("target_addr=%q", route.TargetAddr)
	}
	if got := strings.Join(route.TargetAddrs, ","); got != "203.0.113.44:443,203.0.113.45:443" {
		t.Fatalf("target_addrs=%q", got)
	}
	records := queryProbeLocalDNSUnifiedRecords()
	if len(records) != 1 {
		t.Fatalf("unified records len=%d records=%+v", len(records), records)
	}
	record := records[0]
	if strings.TrimSpace(record.FakeIP) != fakeIP {
		t.Fatalf("record fake ip=%q want=%q record=%+v", record.FakeIP, fakeIP, record)
	}
	if got := strings.Join(record.RealIPs, ","); got != "203.0.113.44,203.0.113.45" {
		t.Fatalf("record real ips=%q record=%+v", got, record)
	}
}

func TestProbeLocalTunnelRouteTargetCandidatesDeduplicatesAndKeepsPrimary(t *testing.T) {
	route := probeLocalTunnelRouteDecision{
		TargetAddr:  "203.0.113.45:443",
		TargetAddrs: []string{"203.0.113.44:443", "203.0.113.45:443", "203.0.113.46:443"},
	}
	got := strings.Join(probeLocalTunnelRouteTargetCandidates(route), ",")
	if got != "203.0.113.45:443,203.0.113.44:443,203.0.113.46:443" {
		t.Fatalf("candidates=%q", got)
	}
}

func TestDecideProbeLocalRouteForTargetTunnelByCIDRRule(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "telegram", Rules: []string{"cidr:91.108.4.0/22"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "telegram", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	route, err := decideProbeLocalRouteForTarget("91.108.4.10:443")
	if err != nil {
		t.Fatalf("decideProbeLocalRouteForTarget returned error: %v", err)
	}
	if route.Direct || route.Reject {
		t.Fatalf("route=%+v", route)
	}
	if route.Group != "telegram" {
		t.Fatalf("group=%q", route.Group)
	}
	if route.SelectedChainID != "chain-proxy-1" {
		t.Fatalf("selected_chain_id=%q", route.SelectedChainID)
	}
	if route.TargetAddr != "91.108.4.10:443" {
		t.Fatalf("target_addr=%q", route.TargetAddr)
	}
	if route.GroupRuntime == nil {
		t.Fatal("group_runtime should not be nil")
	}
}

func TestDecideProbeLocalRouteForTargetDirectForIPOutsideCIDRRule(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "telegram", Rules: []string{"cidr:91.108.4.0/22"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "telegram", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	route, err := decideProbeLocalRouteForTarget("91.108.8.1:443")
	if err != nil {
		t.Fatalf("decideProbeLocalRouteForTarget returned error: %v", err)
	}
	if !route.Direct || route.Reject {
		t.Fatalf("route=%+v", route)
	}
}

func TestShouldUseProbeLocalDNSFakeIPSkipsDirectDecision(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "media", Rules: []string{"domain_suffix:example.com"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "media", Action: "direct"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	dnsDecision := resolveProbeLocalProxyRouteDecisionByDomain("api.example.com")
	if shouldUseProbeLocalDNSFakeIP("api.example.com", dnsmessage.TypeA, dnsDecision) {
		t.Fatalf("direct decision should not use fake ip: %+v", dnsDecision)
	}
}

func TestPreconnectProbeLocalTUNGroupRuntimesFromStateConnectsTunnelGroups(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalTUNGroupRuntimeRegistryForTest()
	t.Cleanup(resetProbeLocalTUNGroupRuntimeRegistryForTest)

	if err := persistProbeProxyChainCache([]probeLinkChainServerItem{{
		ChainID:     "chain-preconnect-1",
		ChainType:   "proxy_chain",
		Name:        "preconnect",
		Secret:      "secret",
		EntryNodeID: "12",
		ExitNodeID:  "12",
		LinkLayer:   "http",
		HopConfigs: []probeLinkChainHopServerItem{{
			NodeNo:       12,
			ListenHost:   "0.0.0.0",
			ListenPort:   16030,
			ExternalPort: 16030,
			LinkLayer:    "http",
			RelayHost:    "127.0.0.1",
		}},
	}}); err != nil {
		t.Fatalf("persist proxy chain cache failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Proxy.Enabled = true
	state.Proxy.Mode = probeLocalProxyModeTUN
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "media", Action: "tunnel", SelectedChainID: "chain-preconnect-1", TunnelNodeID: "chain:chain-preconnect-1"},
		{Group: "fallback", Action: "direct", SelectedChainID: "chain-preconnect-1", TunnelNodeID: "chain:chain-preconnect-1"},
	}

	var peers []net.Conn
	probeLocalTUNOpenChainRelayNetConn = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
		if chainID != "chain-preconnect-1" {
			t.Fatalf("chainID=%q", chainID)
		}
		client, server := net.Pipe()
		peers = append(peers, server)
		return client, nil
	}
	probeLocalProxyLinkOpenRelayConn = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) (net.Conn, error) {
		if chainID != "chain-preconnect-1" {
			t.Fatalf("chainID=%q", chainID)
		}
		client, server := net.Pipe()
		go serveProbeLocalTUNPingPongProbeConn(server)
		return client, nil
	}
	t.Cleanup(func() {
		resetProbeLocalProxyHooksForTest()
		for _, peer := range peers {
			_ = peer.Close()
		}
	})

	preconnectProbeLocalTUNGroupRuntimes(state, "test")

	rt := currentProbeLocalTUNGroupRuntime("media")
	if rt == nil {
		t.Fatal("media runtime should be created")
	}
	snapshot := rt.snapshot()
	if !snapshot.Connected || snapshot.RuntimeStatus != "connected" {
		t.Fatalf("media runtime snapshot=%+v", snapshot)
	}
	viewSnapshot, ok := currentProbeLocalProxyViewGroupRuntimeSnapshot("media")
	if !ok {
		t.Fatal("media view snapshot should be updated")
	}
	if viewSnapshot.SelectedChainLatencyStatus != "reachable" {
		t.Fatalf("media view snapshot=%+v", viewSnapshot)
	}
	if viewSnapshot.SelectedChainLatencyMS == nil || *viewSnapshot.SelectedChainLatencyMS <= 0 {
		t.Fatalf("media view snapshot latency=%+v", viewSnapshot)
	}
	if currentProbeLocalTUNGroupRuntime("fallback") != nil {
		t.Fatal("direct fallback group should not be preconnected")
	}
}

func TestProbeLocalTUNGroupRuntimeLatencyUsesPingPongOnly(t *testing.T) {
	resetProbeLocalTUNGroupRuntimeRegistryForTest()
	t.Cleanup(resetProbeLocalTUNGroupRuntimeRegistryForTest)

	if err := persistProbeProxyChainCache([]probeLinkChainServerItem{{
		ChainID:     "chain-latency-only",
		ChainType:   "proxy_chain",
		Name:        "latency-only",
		Secret:      "secret",
		EntryNodeID: "12",
		ExitNodeID:  "12",
		LinkLayer:   "http3",
		HopConfigs: []probeLinkChainHopServerItem{{
			NodeNo:       12,
			ListenHost:   "0.0.0.0",
			ListenPort:   16030,
			ExternalPort: 16030,
			LinkLayer:    "http3",
			RelayHost:    "127.0.0.1",
		}},
	}}); err != nil {
		t.Fatalf("persist proxy chain cache failed: %v", err)
	}

	client, server := net.Pipe()
	session, err := yamux.Client(client, newProbeChainYamuxConfig())
	if err != nil {
		t.Fatalf("create yamux client failed: %v", err)
	}
	serverSession, err := yamux.Server(server, newProbeChainYamuxConfig())
	if err != nil {
		t.Fatalf("create yamux server failed: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Close()
	})

	rt := &probeLocalTUNGroupRuntime{
		Group:           "media",
		SelectedChainID: "chain-latency-only",
		RuntimeStatus:   "connected",
		session:         session,
		relayConn:       client,
	}
	probeLocalTUNGroupRuntimeRegistry.mu.Lock()
	probeLocalTUNGroupRuntimeRegistry.items[normalizeProbeLocalGroupKey("media")] = rt
	probeLocalTUNGroupRuntimeRegistry.mu.Unlock()

	probeLocalProxyLinkOpenRelayConn = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) (net.Conn, error) {
		time.Sleep(25 * time.Millisecond)
		probeClient, probeServer := net.Pipe()
		go serveProbeLocalTUNPingPongProbeRelayConn(probeServer)
		return probeClient, nil
	}
	t.Cleanup(resetProbeLocalProxyHooksForTest)

	keepalive, latencyMS, _, latencyErr := resolveProbeLocalTUNGroupRuntimeKeepaliveAndLatency(rt)
	if keepalive != "connected" || latencyErr != "" {
		t.Fatalf("keepalive=%q latencyErr=%q", keepalive, latencyErr)
	}
	if latencyMS == nil {
		t.Fatal("latency should be reported")
	}
	if *latencyMS >= 25 {
		t.Fatalf("latency=%dms, want data ping-pong only without relay open delay", *latencyMS)
	}
}

func TestProbeLocalTUNGroupRuntimeReconnectsWhenOpenFailureAndRelayProbeUnavailable(t *testing.T) {
	resetProbeLocalTUNGroupRuntimeRegistryForTest()
	t.Cleanup(resetProbeLocalTUNGroupRuntimeRegistryForTest)

	rt := &probeLocalTUNGroupRuntime{
		Group:           "google",
		SelectedChainID: "chain-stale",
		RuntimeStatus:   "connected",
	}
	probeLocalTUNGroupRuntimeRegistry.mu.Lock()
	probeLocalTUNGroupRuntimeRegistry.items[normalizeProbeLocalGroupKey("google")] = rt
	probeLocalTUNGroupRuntimeRegistry.mu.Unlock()

	client1, server1 := net.Pipe()
	staleServer, err := yamux.Server(server1, newProbeChainYamuxConfig())
	if err != nil {
		t.Fatalf("create stale yamux server failed: %v", err)
	}
	staleClient, err := yamux.Client(client1, newProbeChainYamuxConfig())
	if err != nil {
		t.Fatalf("create stale yamux client failed: %v", err)
	}
	rt.session = staleClient
	rt.relayConn = client1
	t.Cleanup(func() {
		_ = staleServer.Close()
		_ = staleClient.Close()
	})

	if err := persistProbeProxyChainCache([]probeLinkChainServerItem{{
		ChainID:     "chain-stale",
		ChainType:   "proxy_chain",
		Name:        "stale",
		Secret:      "secret",
		EntryNodeID: "12",
		ExitNodeID:  "12",
		LinkLayer:   "http",
		HopConfigs: []probeLinkChainHopServerItem{{
			NodeNo:       12,
			ListenHost:   "0.0.0.0",
			ListenPort:   16030,
			ExternalPort: 16030,
			LinkLayer:    "http",
			RelayHost:    "127.0.0.1",
		}},
	}}); err != nil {
		t.Fatalf("persist proxy chain cache failed: %v", err)
	}

	probeCalls := 0
	probeLocalTUNOpenChainRelayNetConn = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
		probeCalls++
		return nil, errors.New(`Post "https://69.63.223.88:16030/api/node/chain/relay?chain_id=5": context canceled`)
	}
	t.Cleanup(func() {
		probeLocalTUNOpenChainRelayNetConn = openProbeLocalTUNChainRelayNetConn
	})

	rt.mu.Lock()
	reconnect := shouldReconnectProbeLocalTUNGroupRuntimeSessionLocked(rt, staleClient)
	rt.mu.Unlock()
	if !reconnect {
		t.Fatal("expected reconnect decision when relay probe is unavailable")
	}
	if probeCalls != 1 {
		t.Fatalf("probeCalls=%d, want 1", probeCalls)
	}
}

func TestProbeLocalTUNGroupRuntimeKeepsSessionWhenOpenFailureButRelayProbeSucceeds(t *testing.T) {
	resetProbeLocalTUNGroupRuntimeRegistryForTest()
	t.Cleanup(resetProbeLocalTUNGroupRuntimeRegistryForTest)

	rt := &probeLocalTUNGroupRuntime{
		Group:           "google",
		SelectedChainID: "chain-stale",
		RuntimeStatus:   "connected",
	}
	probeLocalTUNGroupRuntimeRegistry.mu.Lock()
	probeLocalTUNGroupRuntimeRegistry.items[normalizeProbeLocalGroupKey("google")] = rt
	probeLocalTUNGroupRuntimeRegistry.mu.Unlock()

	client1, server1 := net.Pipe()
	staleServer, err := yamux.Server(server1, newProbeChainYamuxConfig())
	if err != nil {
		t.Fatalf("create stale yamux server failed: %v", err)
	}
	staleClient, err := yamux.Client(client1, newProbeChainYamuxConfig())
	if err != nil {
		t.Fatalf("create stale yamux client failed: %v", err)
	}
	rt.session = staleClient
	rt.relayConn = client1
	t.Cleanup(func() {
		_ = staleServer.Close()
		_ = staleClient.Close()
	})

	if err := persistProbeProxyChainCache([]probeLinkChainServerItem{{
		ChainID:     "chain-stale",
		ChainType:   "proxy_chain",
		Name:        "stale",
		Secret:      "secret",
		EntryNodeID: "12",
		ExitNodeID:  "12",
		LinkLayer:   "http",
		HopConfigs: []probeLinkChainHopServerItem{{
			NodeNo:       12,
			ListenHost:   "0.0.0.0",
			ListenPort:   16030,
			ExternalPort: 16030,
			LinkLayer:    "http",
			RelayHost:    "127.0.0.1",
		}},
	}}); err != nil {
		t.Fatalf("persist proxy chain cache failed: %v", err)
	}

	var peers []net.Conn
	probeCalls := 0
	done := make(chan struct{})
	probeLocalTUNOpenChainRelayNetConn = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
		probeCalls++
		client, server := net.Pipe()
		peers = append(peers, server)
		go serveProbeLocalTUNRelayHealthProbe(server, done)
		return client, nil
	}
	t.Cleanup(func() {
		close(done)
		probeLocalTUNOpenChainRelayNetConn = openProbeLocalTUNChainRelayNetConn
		for _, peer := range peers {
			_ = peer.Close()
		}
	})

	rt.mu.Lock()
	reconnect := shouldReconnectProbeLocalTUNGroupRuntimeSessionLocked(rt, staleClient)
	rt.mu.Unlock()
	if reconnect {
		t.Fatal("did not expect reconnect decision when relay probe succeeds")
	}
	if probeCalls != 1 {
		t.Fatalf("probeCalls=%d, want 1", probeCalls)
	}
}

func serveProbeLocalTUNGroupRuntimeOpenError(session *yamux.Session, errText string) {
	if session == nil {
		return
	}
	defer session.Close()
	stream, err := session.Accept()
	if err != nil {
		return
	}
	defer stream.Close()
	var req probeChainTunnelOpenRequest
	if err := json.NewDecoder(stream).Decode(&req); err != nil {
		return
	}
	_ = json.NewEncoder(stream).Encode(probeChainTunnelOpenResponse{OK: false, Error: errText})
}

func serveProbeLocalTUNGroupRuntimeOpenOK(conn net.Conn, done <-chan struct{}) {
	defer conn.Close()
	session, err := yamux.Server(conn, newProbeChainYamuxConfig())
	if err != nil {
		return
	}
	defer session.Close()
	stream, err := session.Accept()
	if err != nil {
		return
	}
	defer stream.Close()
	var req probeChainTunnelOpenRequest
	if err := json.NewDecoder(stream).Decode(&req); err != nil {
		return
	}
	_ = json.NewEncoder(stream).Encode(probeChainTunnelOpenResponse{OK: true})
	<-done
}

func serveProbeLocalTUNPingPongProbeConn(conn net.Conn) {
	defer conn.Close()
	session, err := yamux.Server(conn, newProbeChainYamuxConfig())
	if err != nil {
		return
	}
	defer session.Close()
	stream, err := session.Accept()
	if err != nil {
		return
	}
	defer stream.Close()
	serveProbeLocalTUNPingPongProbeStream(stream)
}

func serveProbeLocalTUNPingPongProbeStream(stream net.Conn) {
	var req probeChainTunnelOpenRequest
	if err := json.NewDecoder(stream).Decode(&req); err != nil {
		return
	}
	if req.Type != probeChainRelayModePingPong {
		return
	}
	if err := json.NewEncoder(stream).Encode(probeChainTunnelOpenResponse{OK: true}); err != nil {
		return
	}
	if req.PingBytes <= 0 {
		return
	}
	buf := make([]byte, req.PingBytes)
	if _, err := io.ReadFull(stream, buf); err != nil {
		return
	}
	_, _ = stream.Write(buf)
}

func serveProbeLocalTUNPingPongProbeRelayConn(conn net.Conn) {
	defer conn.Close()
	relaySession, err := yamux.Server(conn, newProbeChainYamuxConfig())
	if err != nil {
		return
	}
	defer relaySession.Close()
	stream, err := relaySession.Accept()
	if err != nil {
		return
	}
	defer stream.Close()
	serveProbeLocalTUNPingPongProbeStream(stream)
}

func serveProbeLocalTUNRelayHealthProbe(conn net.Conn, done <-chan struct{}) {
	defer conn.Close()
	<-done
}
