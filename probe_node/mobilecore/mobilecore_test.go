package mobilecore

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func TestResolveWebSocketURL(t *testing.T) {
	got, err := resolveWebSocketURL("https://controller.example.com:15030/admin")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://controller.example.com:15030/api/probe" {
		t.Fatalf("ws url=%q", got)
	}
}

func TestSignConnect(t *testing.T) {
	got := signConnect("secret-1", "node-1", "100", "abc")
	mac := hmac.New(sha256.New, []byte("secret-1"))
	_, _ = mac.Write([]byte("node-1\n100\nabc"))
	want := hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("signature=%q want %q", got, want)
	}
}

func TestResolveLinkDialHostUsesIPForDirectAndDomainForCF(t *testing.T) {
	dialHost, hostHeader, err := resolveLinkDialHost("localhost")
	if err != nil {
		t.Fatalf("resolveLinkDialHost returned error: %v", err)
	}
	if net.ParseIP(dialHost) == nil || hostHeader != dialHost {
		t.Fatalf("direct relay should use ip for dial and host: dialHost=%s hostHeader=%s", dialHost, hostHeader)
	}

	dialHost, hostHeader, err = resolveLinkDialHostWithPolicy("relay.example.com", true)
	if err != nil {
		t.Fatalf("resolveLinkDialHostWithPolicy returned error: %v", err)
	}
	if dialHost != "relay.example.com" || hostHeader != "relay.example.com" {
		t.Fatalf("cf relay should preserve domain: dialHost=%s hostHeader=%s", dialHost, hostHeader)
	}
}

func TestMobileChainControlRestartAndStop(t *testing.T) {
	listenAddr := reserveMobileChainTestAddr(t)
	host, portText, err := net.SplitHostPort(listenAddr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	encoder := json.NewEncoder(server)
	writeMu := &sync.Mutex{}
	decoder := json.NewDecoder(client)

	cmd := chainLinkControlMessage{
		RequestID:    "apply-1",
		Action:       "start",
		ChainID:      "android-chain-control",
		Role:         "entry",
		ListenHost:   host,
		ListenPort:   port,
		LinkSecret:   "secret-control",
		NextAuthMode: "proxy",
	}
	t.Cleanup(func() { stopMobileChainRuntime(cmd.ChainID, "test cleanup") })
	go runMobileChainLinkControl(cmd, mobileNodeIdentity{NodeID: "android-1", Secret: "node-secret"}, server, encoder, writeMu)
	var result chainLinkControlResult
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode start result: %v", err)
	}
	if !result.OK || result.Action != "apply" {
		t.Fatalf("start result=%+v", result)
	}
	if getMobileChainRuntime(cmd.ChainID) == nil {
		t.Fatal("runtime was not started")
	}

	cmd.RequestID = "restart-1"
	cmd.Action = "restart"
	go runMobileChainLinkControl(cmd, mobileNodeIdentity{NodeID: "android-1", Secret: "node-secret"}, server, encoder, writeMu)
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode restart result: %v", err)
	}
	if !result.OK || result.Action != "restart" {
		t.Fatalf("restart result=%+v", result)
	}

	cmd.RequestID = "stop-1"
	cmd.Action = "stop"
	go runMobileChainLinkControl(cmd, mobileNodeIdentity{NodeID: "android-1", Secret: "node-secret"}, server, encoder, writeMu)
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode stop result: %v", err)
	}
	if !result.OK || result.Action != "remove" {
		t.Fatalf("stop result=%+v", result)
	}
	if getMobileChainRuntime(cmd.ChainID) != nil {
		t.Fatal("runtime was not stopped")
	}
}

func TestMobileChainRelayAcceptsMismatchedHostWithValidAuth(t *testing.T) {
	rt := &mobileChainRuntime{cfg: mobileChainRuntimeConfig{ChainID: "android-chain-host", Secret: "secret-host"}}
	req := httptest.NewRequest(http.MethodGet, "https://wrong.host.invalid"+mobileChainRelayPath+"?chain_id="+rt.cfg.ChainID, nil)
	req.Host = "wrong.host.invalid"
	nonce := "nonce-host"
	req.Header.Set("Authorization", "Bearer "+nonce)
	req.Header.Set(mobileChainHeaderChainID, rt.cfg.ChainID)
	req.Header.Set(mobileChainHeaderMAC, mobileChainHMAC(rt.cfg.Secret, rt.cfg.ChainID, nonce))
	req.Header.Set(mobileChainHeaderAuthMode, "secret_hmac")
	if err := verifyMobileChainRelayRequestAuth(rt, req); err != nil {
		t.Fatalf("valid auth should not depend on Host: %v", err)
	}
}

func TestMobileChainRelayWebSocketPingPongEndToEnd(t *testing.T) {
	listenAddr := reserveMobileChainTestAddr(t)
	host, portText, err := net.SplitHostPort(listenAddr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	cfg := mobileChainRuntimeConfig{
		ChainID:      "android-chain-e2e",
		Secret:       "secret-e2e",
		Role:         mobileChainRoleEntry,
		ListenHost:   host,
		ListenPort:   port,
		NextAuthMode: "proxy",
	}
	rt, err := startMobileChainRuntime(cfg)
	if err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	t.Cleanup(func() { stopMobileChainRuntime(rt.cfg.ChainID, "test cleanup") })

	endpoint := linkEndpoint{
		ChainID:             cfg.ChainID,
		EntryHost:           host,
		EntryPort:           port,
		ChainSecret:         cfg.Secret,
		PreserveRelayDomain: false,
	}
	conn, err := openLinkRelayConn(endpoint, "websocket", 5*time.Second)
	if err != nil {
		t.Fatalf("open relay conn: %v", err)
	}
	defer conn.Close()
	stream, err := openLinkPingPongStream(conn, 32)
	if err != nil {
		t.Fatalf("open ping stream: %v", err)
	}
	defer stream.Close()
	payload := []byte("android-chain-relay-e2e-ping-123")
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(payload, echo) {
		t.Fatalf("echo=%q want %q", string(echo), string(payload))
	}
}

func TestMobileChainPortForwardStreamUsesExistingYamuxOnly(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	serverSession, err := yamux.Server(serverConn, newMobileChainYamuxConfig())
	if err != nil {
		t.Fatalf("server yamux: %v", err)
	}
	defer serverSession.Close()
	clientSession, err := yamux.Client(clientConn, newMobileChainYamuxConfig())
	if err != nil {
		t.Fatalf("client yamux: %v", err)
	}
	defer clientSession.Close()

	rt := &mobileChainRuntime{
		cfg:                mobileChainRuntimeConfig{ChainID: "android-chain-yamux", Role: "entry"},
		downstreamSessions: map[string]*mobileChainBridgeSession{"s1": {ID: "s1", Session: clientSession}},
		upstreamSessions:   map[string]*mobileChainBridgeSession{},
		stopCh:             make(chan struct{}),
	}
	defer close(rt.stopCh)

	reqCh := make(chan mobileChainTunnelOpenRequest, 1)
	go func() {
		stream, acceptErr := serverSession.Accept()
		if acceptErr != nil {
			return
		}
		defer stream.Close()
		var req mobileChainTunnelOpenRequest
		_ = json.NewDecoder(stream).Decode(&req)
		reqCh <- req
		_ = json.NewEncoder(stream).Encode(mobileChainTunnelOpenResponse{OK: true})
	}()

	stream, err := openMobileChainPortForwardStream(rt, mobileChainEntrySideEntry, mobileChainNetworkTCP, "127.0.0.1:3389", "flow-test")
	if err != nil {
		t.Fatalf("open port forward stream: %v", err)
	}
	defer stream.Close()
	select {
	case req := <-reqCh:
		if req.Network != mobileChainNetworkTCP || req.Address != "127.0.0.1:3389" || req.FlowID != "flow-test" {
			t.Fatalf("unexpected request: %+v", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("yamux stream was not opened")
	}
}

func TestMobileChainPortForwardStreamFailsWithoutBridge(t *testing.T) {
	oldTimeout := mobileChainOpenBridgeStreamTimeout
	mobileChainOpenBridgeStreamTimeout = 50 * time.Millisecond
	t.Cleanup(func() { mobileChainOpenBridgeStreamTimeout = oldTimeout })
	rt := &mobileChainRuntime{
		cfg:                mobileChainRuntimeConfig{ChainID: "android-chain-no-bridge", Role: "entry"},
		downstreamSessions: map[string]*mobileChainBridgeSession{},
		upstreamSessions:   map[string]*mobileChainBridgeSession{},
		stopCh:             make(chan struct{}),
	}
	defer close(rt.stopCh)
	_, err := openMobileChainPortForwardStream(rt, mobileChainEntrySideEntry, mobileChainNetworkTCP, "127.0.0.1:3389", "")
	if err == nil || !strings.Contains(err.Error(), "downstream bridge is unavailable") {
		t.Fatalf("err=%v, want downstream bridge unavailable", err)
	}
}

func TestMobileChainDialHostPolicy(t *testing.T) {
	dialHost, hostHeader, err := resolveMobileChainDialHost("localhost", false)
	if err != nil {
		t.Fatalf("resolve direct: %v", err)
	}
	if net.ParseIP(dialHost) == nil || hostHeader != dialHost {
		t.Fatalf("direct should use ip for dial and host: dialHost=%s hostHeader=%s", dialHost, hostHeader)
	}
	dialHost, hostHeader, err = resolveMobileChainDialHost("relay.example.com", true)
	if err != nil {
		t.Fatalf("resolve cf: %v", err)
	}
	if dialHost != "relay.example.com" || hostHeader != "relay.example.com" {
		t.Fatalf("cf should preserve domain: dialHost=%s hostHeader=%s", dialHost, hostHeader)
	}
}

func TestMobileChainUserAuthTicketVerification(t *testing.T) {
	oldNow := mobileChainAuthTicketNow
	mobileChainAuthTicketNow = func() time.Time {
		return time.Date(2026, time.June, 8, 0, 0, 0, 0, time.UTC)
	}
	defer func() {
		mobileChainAuthTicketNow = oldNow
	}()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	rawPub := base64.StdEncoding.EncodeToString(pub)
	cfg, err := buildMobileChainRuntimeConfig(chainLinkControlMessage{
		ChainID:         "android-chain-user-auth",
		Role:            "entry",
		ListenPort:      12345,
		LinkSecret:      "secret-user-auth",
		NextAuthMode:    "proxy",
		RequireUserAuth: true,
		UserPublicKey:   rawPub,
	})
	if err != nil {
		t.Fatalf("build config: %v", err)
	}
	payload := mobileChainUserAuthTicketPayload{
		Version:       "chain-auth-v1",
		ChainID:       cfg.ChainID,
		UserPublicKey: rawPub,
		IssuedAt:      time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal ticket: %v", err)
	}
	sig := ed25519.Sign(priv, payloadBytes)
	ticket := base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(sig)
	if err := verifyMobileChainUserAuthTicket(cfg, ticket); err != nil {
		t.Fatalf("verify ticket: %v", err)
	}
	if err := verifyMobileChainUserAuthTicket(cfg, "bad.ticket"); err == nil {
		t.Fatal("bad ticket unexpectedly accepted")
	}
	mobileChainAuthTicketNow = func() time.Time {
		return time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	}
	if err := verifyMobileChainUserAuthTicket(cfg, ticket); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired ticket error, got: %v", err)
	}
}

func TestApplyMobileChainRuntimesFromConfigDirRestoresSelfAndPortForwardChains(t *testing.T) {
	dir := t.TempDir()
	listenA := reserveMobileChainTestAddr(t)
	hostA, portAText, _ := net.SplitHostPort(listenA)
	portA, _ := strconv.Atoi(portAText)
	listenB := reserveMobileChainTestAddr(t)
	hostB, portBText, _ := net.SplitHostPort(listenB)
	portB, _ := strconv.Atoi(portBText)
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	itemA := linkChainServerItem{
		ChainID:       "android-restore-self",
		Name:          "restore-self",
		UserPublicKey: base64.StdEncoding.EncodeToString(pub),
		Secret:        "secret-restore-self",
		EntryNodeID:   "7",
		ExitNodeID:    "7",
		HopConfigs: []linkChainHopItem{{
			NodeNo:     7,
			ListenHost: hostA,
			ListenPort: portA,
		}},
	}
	itemB := linkChainServerItem{
		ChainID:       "android-restore-pf",
		Name:          "restore-pf",
		UserPublicKey: base64.StdEncoding.EncodeToString(pub),
		Secret:        "secret-restore-pf",
		EntryNodeID:   "7",
		ExitNodeID:    "7",
		HopConfigs: []linkChainHopItem{{
			NodeNo:     7,
			ListenHost: hostB,
			ListenPort: portB,
		}},
		PortForwards: []json.RawMessage{json.RawMessage(`{"id":"pf-1","entry_side":"chain_entry","listen_host":"127.0.0.1","listen_port":1,"target_host":"127.0.0.1","target_port":9,"network":"tcp","enabled":false}`)},
	}
	writeTestJSON(t, filepath.Join(dir, "probe_link_chain_config.json"), chainCacheFile{Items: mustMarshalRawItems(t, itemA)})
	writeTestJSON(t, filepath.Join(dir, "probe_link_port_forward_chain_config.json"), chainCacheFile{Items: mustMarshalRawItems(t, itemB)})
	t.Cleanup(func() {
		stopMobileChainRuntime(itemA.ChainID, "test cleanup")
		stopMobileChainRuntime(itemB.ChainID, "test cleanup")
	})
	applied, err := applyMobileChainRuntimesFromConfigDir(dir, mobileNodeIdentity{NodeID: "7", Secret: "node-secret"})
	if err != nil {
		t.Fatalf("apply from config: %v", err)
	}
	if applied != 2 {
		t.Fatalf("applied=%d want 2", applied)
	}
	if getMobileChainRuntime(itemA.ChainID) == nil || getMobileChainRuntime(itemB.ChainID) == nil {
		t.Fatal("expected both runtimes to be restored")
	}
}

func TestRefreshConfigFilesWritesProxyAndChainCaches(t *testing.T) {
	proxyGroup := `{"groups":[{"group":"default","rules":[],"fallback":"direct"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Probe-Node-Id") != "7" {
			t.Fatalf("missing auth node id for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/probe/proxy_group/backup":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":             true,
				"node_id":        "7",
				"file_name":      "proxy_group.json",
				"content_base64": base64.StdEncoding.EncodeToString([]byte(proxyGroup)),
			})
		case "/api/probe/link/config/grouped":
			if r.URL.Query().Get("node_id") != "7" || r.URL.Query().Get("secret") != "secret-7" {
				t.Fatalf("missing query identity: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id": "7",
				"self_chains": []map[string]any{
					{"chain_id": "self-1", "chain_type": "port_forward"},
				},
				"global_proxy_forward_chains": []map[string]any{
					{"chain_id": "proxy-1", "chain_type": "proxy_chain"},
					{"chain_id": "proxy-2", "chain_type": "proxy_chain"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	summary, err := refreshConfigFiles(server.URL, "7", "secret-7", dir)
	if err != nil {
		t.Fatalf("refreshConfigFiles returned error: %v", err)
	}
	if !summary.ProxyGroupUpdated || summary.SelfChains != 1 || summary.ProxyEntries != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if got := readTestFile(t, filepath.Join(dir, "proxy_group.json")); !strings.Contains(got, `"groups"`) {
		t.Fatalf("proxy_group.json not written: %s", got)
	}
	if got := readTestFile(t, filepath.Join(dir, "probe_link_chain_config.json")); !strings.Contains(got, `"self-1"`) {
		t.Fatalf("probe_link_chain_config.json not written: %s", got)
	}
	if got := readTestFile(t, filepath.Join(dir, "proxy_chain.json")); !strings.Contains(got, `"proxy-2"`) {
		t.Fatalf("proxy_chain.json not written: %s", got)
	}
}

func TestParseLinuxMemInfo(t *testing.T) {
	total, used, swapTotal, swapUsed := parseLinuxMemInfo(strings.NewReader(strings.Join([]string{
		"MemTotal:        1000 kB",
		"MemAvailable:     250 kB",
		"SwapTotal:        400 kB",
		"SwapFree:         100 kB",
	}, "\n")))
	if total != 1000*1024 || used != 750*1024 || swapTotal != 400*1024 || swapUsed != 300*1024 {
		t.Fatalf("meminfo total=%d used=%d swapTotal=%d swapUsed=%d", total, used, swapTotal, swapUsed)
	}
}

func TestParseCPUSnapshot(t *testing.T) {
	got, ok := parseCPUSnapshot(strings.NewReader("cpu  10 20 30 40 50 60 70\n"))
	if !ok {
		t.Fatal("expected cpu snapshot")
	}
	if got.total != 280 || got.idle != 90 {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestSetVersion(t *testing.T) {
	manager.mu.Lock()
	old := manager.version
	manager.version = ""
	manager.mu.Unlock()
	defer func() {
		manager.mu.Lock()
		manager.version = old
		manager.mu.Unlock()
	}()

	if got := currentVersion(); got != "android" {
		t.Fatalf("default version=%q", got)
	}
	if got := SetVersion("1.2.3 (12)"); got != "1.2.3 (12)" {
		t.Fatalf("SetVersion returned %q", got)
	}
	if got := currentVersion(); got != "1.2.3 (12)" {
		t.Fatalf("currentVersion=%q", got)
	}
}

func TestSetNativeIPsParsesAndFilters(t *testing.T) {
	manager.mu.Lock()
	old4 := append([]string{}, manager.injectedIPv4...)
	old6 := append([]string{}, manager.injectedIPv6...)
	manager.injectedIPv4 = nil
	manager.injectedIPv6 = nil
	manager.mu.Unlock()
	defer func() {
		manager.mu.Lock()
		manager.injectedIPv4 = old4
		manager.injectedIPv6 = old6
		manager.mu.Unlock()
	}()

	got := SetNativeIPs(`["192.168.1.10","127.0.0.1","2409:8a00::1"]`, `["2409:8a00::1","fe80::1","192.168.1.10"]`)
	if !strings.Contains(got, "ipv4=1") || !strings.Contains(got, "ipv6=2") {
		t.Fatalf("SetNativeIPs=%q", got)
	}
	ipv4, ipv6 := currentInjectedIPs()
	if strings.Join(ipv4, ",") != "192.168.1.10" {
		t.Fatalf("ipv4=%v", ipv4)
	}
	if strings.Join(ipv6, ",") != "2409:8a00::1,fe80::1" {
		t.Fatalf("ipv6=%v", ipv6)
	}
}

func TestCollectIPsIncludesInjectedNativeIPs(t *testing.T) {
	t.Setenv("PROBE_PUBLIC_IP_SNIFF", "0")
	manager.mu.Lock()
	old4 := append([]string{}, manager.injectedIPv4...)
	old6 := append([]string{}, manager.injectedIPv6...)
	manager.injectedIPv4 = nil
	manager.injectedIPv6 = nil
	manager.mu.Unlock()
	defer func() {
		manager.mu.Lock()
		manager.injectedIPv4 = old4
		manager.injectedIPv6 = old6
		manager.mu.Unlock()
	}()

	got := SetNativeIPs(`["198.51.100.44"]`, `["2001:db8::44"]`)
	if !strings.Contains(got, "ipv4=1") || !strings.Contains(got, "ipv6=1") {
		t.Fatalf("SetNativeIPs=%q", got)
	}
	ipv4, ipv6 := collectIPs()
	if !containsString(ipv4, "198.51.100.44") {
		t.Fatalf("collectIPs ipv4=%v, want injected IPv4", ipv4)
	}
	if !containsString(ipv6, "2001:db8::44") {
		t.Fatalf("collectIPs ipv6=%v, want injected IPv6", ipv6)
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func TestParseIPAddrOutput(t *testing.T) {
	ipv4, ipv6 := parseIPAddrOutput(strings.Join([]string{
		"2: wlan0    inet 192.168.31.10/24 brd 192.168.31.255 scope global wlan0",
		"3: rmnet_data0    inet 10.22.1.5/30 scope global rmnet_data0",
		"4: wlan0    inet6 2409:8a00::123/64 scope global dynamic",
		"5: lo    inet 127.0.0.1/8 scope host lo",
	}, "\n"))
	if strings.Join(ipv4, ",") != "10.22.1.5,192.168.31.10" {
		t.Fatalf("ipv4=%v", ipv4)
	}
	if strings.Join(ipv6, ",") != "2409:8a00::123" {
		t.Fatalf("ipv6=%v", ipv6)
	}
}

func TestResolveLinkEndpointUsesProjectedRelayChain(t *testing.T) {
	item := linkChainServerItem{
		ChainID:        "client-chain",
		RelayChainID:   "relay-chain",
		Name:           "Android Link",
		Secret:         "link-secret",
		EntryNodeID:    "node-1",
		ExitNodeID:     "node-2",
		LinkLayer:      "ws",
		CascadeNodeIDs: []string{"node-9"},
		HopConfigs: []linkChainHopItem{
			{NodeNo: 1, RelayHost: "relay.example.com", ExternalPort: 443, ListenPort: 8443, LinkLayer: "websocket"},
		},
	}
	endpoint, err := resolveLinkEndpoint(item)
	if err != nil {
		t.Fatalf("resolveLinkEndpoint returned error: %v", err)
	}
	if endpoint.ChainID != "relay-chain" || endpoint.EntryHost != "relay.example.com" || endpoint.EntryPort != 443 || endpoint.LinkLayer != "websocket" {
		t.Fatalf("unexpected endpoint: %+v", endpoint)
	}
}

func TestLinkStatusReadsProxyChainCache(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_chain.json"), map[string]any{
		"items": []map[string]any{
			{
				"chain_id":       "client-chain",
				"relay_chain_id": "relay-chain",
				"name":           "Android Link",
				"secret":         "link-secret",
				"entry_node_id":  "1",
				"exit_node_id":   "2",
				"link_layer":     "websocket",
				"hop_configs": []map[string]any{
					{"node_no": 1, "relay_host": "127.0.0.1", "external_port": 443},
				},
			},
		},
	})
	var payload struct {
		OK     bool             `json:"ok"`
		Chains []linkStatusItem `json:"chains"`
	}
	if err := json.Unmarshal([]byte(LinkStatus(dir)), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || len(payload.Chains) != 1 {
		t.Fatalf("unexpected status payload: %+v", payload)
	}
	if payload.Chains[0].Status != "configured" || payload.Chains[0].RelayChainID != "relay-chain" {
		t.Fatalf("unexpected chain status: %+v", payload.Chains[0])
	}
}

func TestLinkLatencyAndSpeedUseRelayProtocol(t *testing.T) {
	const chainID = "relay-chain"
	const secret = "link-secret"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != linkRelayAPIPath {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("chain_id") != chainID {
			t.Fatalf("chain_id query=%q", r.URL.Query().Get("chain_id"))
		}
		assertLinkAuth(t, r, chainID, secret)
		mode := r.Header.Get(linkCodexRelayModeHeader)
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade failed: %v", err)
		}
		conn := newWebSocketNetConn(ws)
		switch mode {
		case linkRelayModeBridge:
			serveTestPingPongRelay(t, conn)
		case linkRelayModeSpeedTest:
			byteCount, _ := strconv.ParseInt(r.Header.Get(linkCodexSpeedBytesHeader), 10, 64)
			if byteCount <= 0 {
				t.Fatalf("missing speed byte count")
			}
			writeTestSpeedBytes(t, conn, byteCount)
		case linkRelayModeSpeedDebug:
			payload := linkSpeedDebugResultPayload{
				Type:      "speed_debug_result",
				NodeID:    "10",
				OK:        true,
				Scope:     "chain_relay",
				FetchedAt: time.Now().UTC().Format(time.RFC3339),
				Recent: []linkSpeedDebugItemPayload{{
					ChainID:   chainID,
					Transport: "websocket",
					Status:    "completed",
					Bytes:     4096,
					RateBPS:   4096,
				}},
			}
			if err := json.NewEncoder(conn).Encode(payload); err != nil {
				t.Fatalf("write speed debug failed: %v", err)
			}
		default:
			t.Fatalf("unexpected relay mode %q", mode)
		}
	}))
	defer server.Close()

	host, port := testServerHostPort(t, server)
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_chain.json"), map[string]any{
		"items": []map[string]any{
			{
				"chain_id":       "client-chain",
				"relay_chain_id": chainID,
				"name":           "Android Link",
				"secret":         secret,
				"entry_node_id":  "1",
				"exit_node_id":   "2",
				"link_layer":     "websocket",
				"hop_configs": []map[string]any{
					{"node_no": 1, "relay_host": host, "external_port": port},
				},
			},
		},
	})

	var latency struct {
		OK           bool   `json:"ok"`
		Status       string `json:"status"`
		BestProtocol string `json:"best_protocol"`
	}
	if err := json.Unmarshal([]byte(LinkLatency(dir, "client-chain")), &latency); err != nil {
		t.Fatal(err)
	}
	if !latency.OK || latency.Status != "reachable" || latency.BestProtocol != "websocket" {
		t.Fatalf("unexpected latency result: %+v", latency)
	}

	_, endpoint, err := loadLinkEndpointByID(dir, "client-chain")
	if err != nil {
		t.Fatal(err)
	}
	speed := linkRelaySpeedTestWithLayer(endpoint, "websocket", 4096, 5*time.Second)
	if !speed.OK || speed.Bytes != 4096 || speed.RateBPS <= 0 {
		t.Fatalf("unexpected speed result: %+v", speed)
	}
	debug, err := linkRelayFetchSpeedDebugWithLayer(endpoint, "websocket", 5*time.Second)
	if err != nil {
		t.Fatalf("speed debug failed: %v", err)
	}
	if !debug.OK || debug.NodeID != "10" || !linkSpeedDebugPayloadHasChain(debug, linkChainServerItem{RelayChainID: chainID}) {
		t.Fatalf("unexpected speed debug result: %+v", debug)
	}
}

func TestAndroidProxyChainSessionDefaultProtocolFallsBackToWebSocket(t *testing.T) {
	const chainID = "relay-chain"
	const secret = "link-secret"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != linkRelayAPIPath {
			http.NotFound(w, r)
			return
		}
		assertLinkAuth(t, r, chainID, secret)
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade failed: %v", err)
		}
		conn := newWebSocketNetConn(ws)
		defer conn.Close()
		session, err := yamux.Server(conn, newLinkYamuxConfig())
		if err != nil {
			t.Fatalf("yamux server: %v", err)
		}
		defer session.Close()
	}))
	defer server.Close()

	host, port := testServerHostPort(t, server)
	proxyRuntime.mu.Lock()
	oldSessions := proxyRuntime.sessions
	proxyRuntime.sessions = map[string]*proxyChainSession{}
	proxyRuntime.mu.Unlock()
	defer func() {
		proxyRuntime.mu.Lock()
		for _, session := range proxyRuntime.sessions {
			closeProxyChainSession(session)
		}
		proxyRuntime.sessions = oldSessions
		proxyRuntime.mu.Unlock()
	}()

	item := linkChainServerItem{
		ChainID:      "client-chain",
		RelayChainID: chainID,
		Secret:       secret,
		EntryNodeID:  "1",
		ExitNodeID:   "2",
		LinkLayer:    "",
		HopConfigs: []linkChainHopItem{
			{NodeNo: 1, RelayHost: host, ExternalPort: port},
		},
	}
	endpoint, err := resolveLinkEndpoint(item)
	if err != nil {
		t.Fatal(err)
	}
	session, err := ensureProxyChainSession(item, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if session == nil || session.IsClosed() {
		t.Fatalf("session not established: %v", session)
	}
}

func TestOpenAndroidProxyIndependentStreamUsesBridgeSession(t *testing.T) {
	proxyRuntime.mu.Lock()
	oldSessions := proxyRuntime.sessions
	proxyRuntime.sessions = map[string]*proxyChainSession{}
	proxyRuntime.mu.Unlock()
	defer func() {
		proxyRuntime.mu.Lock()
		for _, session := range proxyRuntime.sessions {
			closeProxyChainSession(session)
		}
		proxyRuntime.sessions = oldSessions
		proxyRuntime.mu.Unlock()
	}()

	clientConn, serverConn := net.Pipe()
	serverSession, err := yamux.Server(serverConn, newLinkYamuxConfig())
	if err != nil {
		t.Fatalf("yamux server failed: %v", err)
	}
	defer serverSession.Close()
	clientSession, err := yamux.Client(clientConn, newLinkYamuxConfig())
	if err != nil {
		t.Fatalf("yamux client failed: %v", err)
	}

	endpoint := linkEndpoint{
		ChainID:     "chain-bridge",
		EntryHost:   "invalid.invalid",
		EntryPort:   1,
		LinkLayer:   "websocket-h3",
		ChainSecret: "secret-a",
	}
	proxyRuntime.mu.Lock()
	proxyRuntime.sessions[endpoint.ChainID] = &proxyChainSession{chainID: endpoint.ChainID, conn: clientConn, session: clientSession}
	proxyRuntime.mu.Unlock()

	serverErr := make(chan error, 1)
	go func() {
		stream, err := serverSession.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer stream.Close()
		var req linkTunnelOpenRequest
		if err := json.NewDecoder(stream).Decode(&req); err != nil {
			serverErr <- err
			return
		}
		if req.Type != "open" || req.Network != "tcp" || req.Address != "example.com:443" || req.FlowID != "flow-a" {
			serverErr <- errors.New("unexpected open request")
			return
		}
		if err := json.NewEncoder(stream).Encode(linkTunnelOpenResponse{OK: true}); err != nil {
			serverErr <- err
			return
		}
		_, _ = io.Copy(io.Discard, stream)
		serverErr <- nil
	}()

	stream, err := openAndroidProxyIndependentStream(linkChainServerItem{ChainID: endpoint.ChainID}, endpoint, linkTunnelOpenRequest{
		Type:    "open",
		Network: "tcp",
		Address: "example.com:443",
		FlowID:  "flow-a",
	})
	if err != nil {
		t.Fatalf("open independent stream failed: %v", err)
	}
	_ = stream.Close()

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server side failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bridge stream")
	}
}

func TestProxyFramedPacketRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("udp payload")
	if err := writeProxyFramedPacket(&buf, payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 64)
	n, err := readProxyFramedPacket(bufio.NewReader(&buf), got)
	if err != nil {
		t.Fatal(err)
	}
	if string(got[:n]) != string(payload) {
		t.Fatalf("payload=%q", string(got[:n]))
	}
}

func TestDecideVPNRouteForTargetUsesProxyState(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_group.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "media", "rules": []string{"domain_suffix:example.com"}},
		},
	})
	writeTestJSON(t, filepath.Join(dir, "proxy_state.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "media", "action": "tunnel", "selected_chain_id": "chain-1"},
		},
	})
	vpnRuntime.mu.Lock()
	old := vpnRuntime.configDir
	vpnRuntime.configDir = dir
	vpnRuntime.mu.Unlock()
	defer func() {
		vpnRuntime.mu.Lock()
		vpnRuntime.configDir = old
		vpnRuntime.mu.Unlock()
	}()

	route, err := decideVPNRouteForTarget("www.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	if route.Direct || route.SelectedChainID != "chain-1" || route.Group != "media" {
		t.Fatalf("unexpected route: %+v", route)
	}
}

func TestProxyRouteForcesControllerDirect(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_state.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "fallback", "action": "tunnel", "selected_chain_id": "chain-1"},
		},
	})
	oldHost, oldPort := currentControllerDirectTarget()
	defer func() {
		manager.mu.Lock()
		manager.controllerHost = oldHost
		manager.controllerPort = oldPort
		manager.mu.Unlock()
	}()
	SetControllerURL("https://controller.example.com/admin")

	route, err := decideAndroidProxyRouteForTarget(dir, "controller.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	if !route.Direct || route.Group != "controller" {
		t.Fatalf("controller route should be forced direct: %+v", route)
	}

	route, err = decideAndroidProxyRouteForTarget(dir, "other.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	if route.Direct || route.SelectedChainID != "chain-1" {
		t.Fatalf("non-controller fallback should still use selected route: %+v", route)
	}
}

func TestProxyRouteForcesLinkEntryDirect(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_state.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "fallback", "action": "tunnel", "selected_chain_id": "chain-1"},
		},
	})
	writeTestJSON(t, filepath.Join(dir, "proxy_chain.json"), map[string]any{
		"items": []map[string]any{
			{
				"chain_id":      "chain-1",
				"name":          "Link Entry",
				"entry_node_id": "1",
				"exit_node_id":  "2",
				"hop_configs": []map[string]any{
					{"node_no": 1, "relay_host": "entry.example.com", "external_port": 8443},
				},
			},
		},
	})

	route, err := decideAndroidProxyRouteForTarget(dir, "entry.example.com:8443")
	if err != nil {
		t.Fatal(err)
	}
	if !route.Direct || route.Group != "link_entry" {
		t.Fatalf("link entry route should be forced direct: %+v", route)
	}
}

func TestBuildLogsControlResultUsesAndroidLogBuffer(t *testing.T) {
	oldStore := androidLogStore
	androidLogStore = &androidLogBuffer{}
	defer func() {
		androidLogStore = oldStore
	}()

	AppendAppLog("vpn", "info", "vpn started")
	AppendAppLog("proxy", "error", "proxy failed")

	result := buildLogsControlResult(logsControlMessage{
		RequestID: "log-1",
		Lines:     10,
		MinLevel:  "warning",
	}, "7")
	if !result.OK || result.Type != "logs_result" || result.RequestID != "log-1" || result.NodeID != "7" {
		t.Fatalf("unexpected logs result header: %+v", result)
	}
	if len(result.Entries) != 1 || result.Entries[0].Level != "error" || !strings.Contains(result.Content, "proxy failed") {
		t.Fatalf("unexpected filtered logs: %+v content=%q", result.Entries, result.Content)
	}
}

func TestVPNUDPAssociationMetadata(t *testing.T) {
	id := stack.TransportEndpointID{
		LocalAddress:  tcpip.AddrFrom4([4]byte{8, 8, 8, 8}),
		LocalPort:     53,
		RemoteAddress: tcpip.AddrFrom4([4]byte{10, 111, 0, 2}),
		RemotePort:    53000,
	}
	assocKey := strings.ToLower("8.8.8.8:53") + "|" + id.RemoteAddress.String() + ":53000->" + id.LocalAddress.String() + ":53"
	association := &linkAssociationV2Meta{
		Version:          2,
		Transport:        "udp",
		RouteGroup:       "fallback",
		RouteNodeID:      formatProxyLegacyTunnelNodeID("chain-1"),
		RouteTarget:      "8.8.8.8:53",
		RouteFingerprint: "8.8.8.8:53",
		NATMode:          "default",
		TTLProfile:       "default",
		IdleTimeoutMS:    vpnUDPRelayTimeout.Milliseconds(),
		GCIntervalMS:     (vpnUDPRelayTimeout / 2).Milliseconds(),
		CreatedAtUnixMS:  1,
		AssocKeyV2:       assocKey,
		FlowID:           assocKey,
		SrcIP:            id.RemoteAddress.String(),
		SrcPort:          uint16(id.RemotePort),
		DstIP:            id.LocalAddress.String(),
		DstPort:          uint16(id.LocalPort),
		IPFamily:         4,
		SourceKey:        id.RemoteAddress.String() + ":53000",
		SourceRefs:       1,
	}
	if association.RouteNodeID != "chain:chain-1" || association.AssocKeyV2 == "" || association.SrcPort != 53000 || association.DstPort != 53 {
		t.Fatalf("unexpected association: %+v", association)
	}
}

func TestAndroidVPNDNSFakeIPPreservesDomainRoute(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_group.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "media", "rules": []string{"domain_suffix:example.com"}},
		},
	})
	writeTestJSON(t, filepath.Join(dir, "proxy_state.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "media", "action": "tunnel", "selected_chain_id": "chain-1"},
			{"group": "fallback", "action": "direct"},
		},
	})
	oldDNSState := vpnDNSState
	vpnDNSState = &androidVPNDNSState{
		nextFakeOffset: 2,
		fakeDomainToIP: map[string]string{},
		fakeIPToEntry:  map[string]androidVPNDNSFakeEntry{},
		routeIPHints:   map[string]androidVPNDNSRouteHintEntry{},
	}
	defer func() {
		vpnDNSState = oldDNSState
	}()
	vpnRuntime.mu.Lock()
	oldConfigDir := vpnRuntime.configDir
	vpnRuntime.configDir = dir
	vpnRuntime.mu.Unlock()
	defer func() {
		vpnRuntime.mu.Lock()
		vpnRuntime.configDir = oldConfigDir
		vpnRuntime.mu.Unlock()
	}()

	query := buildTestDNSQuery(t, "video.example.com", dnsmessage.TypeA)
	response, err := resolveAndroidVPNDNSPacket(query)
	if err != nil {
		t.Fatal(err)
	}
	ips := extractTestDNSARecords(t, response)
	if len(ips) != 1 || !strings.HasPrefix(ips[0], "198.18.") {
		t.Fatalf("dns fake ips=%v", ips)
	}
	route, err := decideVPNRouteForTarget(net.JoinHostPort(ips[0], "443"))
	if err != nil {
		t.Fatal(err)
	}
	if route.Direct || route.Group != "media" || route.SelectedChainID != "chain-1" || route.TargetAddr != "video.example.com:443" {
		t.Fatalf("fake ip route not preserved: %+v", route)
	}
}

func TestAndroidVPNDNSRouteHintPreservesDirectDomainRule(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_group.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "direct-site", "rules": []string{"domain_suffix:example.com"}},
		},
	})
	writeTestJSON(t, filepath.Join(dir, "proxy_state.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "direct-site", "action": "direct"},
			{"group": "fallback", "action": "tunnel", "selected_chain_id": "chain-1"},
		},
	})
	oldDNSState := vpnDNSState
	vpnDNSState = &androidVPNDNSState{
		nextFakeOffset: 2,
		fakeDomainToIP: map[string]string{},
		fakeIPToEntry:  map[string]androidVPNDNSFakeEntry{},
		routeIPHints:   map[string]androidVPNDNSRouteHintEntry{},
	}
	defer func() {
		vpnDNSState = oldDNSState
	}()

	query := buildTestDNSQuery(t, "direct.example.com", dnsmessage.TypeA)
	response := buildAndroidVPNDNSSuccess(query, []net.IP{net.ParseIP("203.0.113.10")}, dnsmessage.TypeA)
	storeAndroidVPNDNSRouteHints("direct.example.com", response, proxyRouteDecision{Direct: true, Group: "direct-site"})

	route, err := decideAndroidProxyRouteForTarget(dir, "203.0.113.10:443")
	if err != nil {
		t.Fatal(err)
	}
	if !route.Direct || route.Group != "direct-site" || route.SelectedChainID != "" {
		t.Fatalf("direct route hint not preserved: %+v", route)
	}
}

func TestAndroidVPNDNSRouteHintPreservesTunnelDomainTarget(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_group.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "google", "rules": []string{"domain_suffix:google.com"}},
		},
	})
	writeTestJSON(t, filepath.Join(dir, "proxy_state.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "google", "action": "tunnel", "selected_chain_id": "chain-1"},
			{"group": "fallback", "action": "direct"},
		},
	})
	oldDNSState := vpnDNSState
	vpnDNSState = &androidVPNDNSState{
		nextFakeOffset: 2,
		fakeDomainToIP: map[string]string{},
		fakeIPToEntry:  map[string]androidVPNDNSFakeEntry{},
		routeIPHints:   map[string]androidVPNDNSRouteHintEntry{},
	}
	defer func() {
		vpnDNSState = oldDNSState
	}()

	query := buildTestDNSQuery(t, "www.google.com", dnsmessage.TypeA)
	response := buildAndroidVPNDNSSuccess(query, []net.IP{net.ParseIP("142.250.190.68")}, dnsmessage.TypeA)
	storeAndroidVPNDNSRouteHints("www.google.com", response, proxyRouteDecision{Direct: false, Group: "google", SelectedChainID: "chain-1"})

	route, err := decideAndroidProxyRouteForTarget(dir, "142.250.190.68:443")
	if err != nil {
		t.Fatal(err)
	}
	if route.Direct || route.Group != "google" || route.SelectedChainID != "chain-1" || route.TargetAddr != "www.google.com:443" {
		t.Fatalf("tunnel route hint did not preserve domain target: %+v", route)
	}
}

func TestAndroidVPNDNSRouteHintUsesCurrentRulesAfterConfigRefresh(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_group.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "google", "rules": []string{"domain_suffix:google.com"}},
		},
	})
	writeTestJSON(t, filepath.Join(dir, "proxy_state.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "google", "action": "tunnel", "selected_chain_id": "chain-1"},
			{"group": "fallback", "action": "direct"},
		},
	})
	oldDNSState := vpnDNSState
	vpnDNSState = &androidVPNDNSState{
		nextFakeOffset: 2,
		fakeDomainToIP: map[string]string{},
		fakeIPToEntry:  map[string]androidVPNDNSFakeEntry{},
		routeIPHints:   map[string]androidVPNDNSRouteHintEntry{},
	}
	defer func() {
		vpnDNSState = oldDNSState
	}()

	query := buildTestDNSQuery(t, "www.google.com", dnsmessage.TypeA)
	response := buildAndroidVPNDNSSuccess(query, []net.IP{net.ParseIP("142.250.190.68")}, dnsmessage.TypeA)
	storeAndroidVPNDNSRouteHints("www.google.com", response, proxyRouteDecision{Direct: true, Group: "fallback"})

	route, err := decideAndroidProxyRouteForTarget(dir, "142.250.190.68:443")
	if err != nil {
		t.Fatal(err)
	}
	if route.Direct || route.Group != "google" || route.SelectedChainID != "chain-1" || route.TargetAddr != "www.google.com:443" {
		t.Fatalf("stale direct hint should use current google tunnel rules: %+v", route)
	}
}

func TestAndroidVPNIPv6FallbackRouteUsesHintIPv4(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_group.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "google", "rules": []string{"domain_suffix:google.com"}},
		},
	})
	writeTestJSON(t, filepath.Join(dir, "proxy_state.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "google", "action": "tunnel", "selected_chain_id": "chain-1"},
			{"group": "fallback", "action": "direct"},
		},
	})
	oldDNSState := vpnDNSState
	vpnDNSState = &androidVPNDNSState{
		nextFakeOffset: 2,
		fakeDomainToIP: map[string]string{},
		fakeIPToEntry:  map[string]androidVPNDNSFakeEntry{},
		routeIPHints:   map[string]androidVPNDNSRouteHintEntry{},
	}
	defer func() {
		vpnDNSState = oldDNSState
	}()
	vpnRuntime.mu.Lock()
	oldConfigDir := vpnRuntime.configDir
	vpnRuntime.configDir = dir
	vpnRuntime.mu.Unlock()
	defer func() {
		vpnRuntime.mu.Lock()
		vpnRuntime.configDir = oldConfigDir
		vpnRuntime.mu.Unlock()
	}()

	query := buildTestDNSQuery(t, "dl.google.com", dnsmessage.TypeAAAA)
	response := buildAndroidVPNDNSSuccess(query, []net.IP{net.ParseIP("2001:4860:4802:32::223")}, dnsmessage.TypeAAAA)
	storeAndroidVPNDNSRouteHints("dl.google.com", response, proxyRouteDecision{Direct: false, Group: "google", SelectedChainID: "chain-1"})
	rememberAndroidVPNDNSRouteHintIPv4s("2001:4860:4802:32::223", []string{"142.250.190.78"})

	route, ok := buildAndroidVPNIPv4FallbackRoute(vpnRouteDecision{
		Direct:          false,
		TargetAddr:      "[2001:4860:4802:32::223]:443",
		Group:           "google",
		SelectedChainID: "chain-1",
	}, timeoutTestError{})
	if !ok {
		t.Fatal("expected ipv4 fallback route")
	}
	if route.Direct || route.Group != "google" || route.SelectedChainID != "chain-1" || route.TargetAddr != "142.250.190.78:443" {
		t.Fatalf("unexpected fallback route: %+v", route)
	}
}

func TestAndroidVPNIPv6FallbackRouteAcceptsStringTimeout(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_group.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "google", "rules": []string{"domain_suffix:google.com"}},
		},
	})
	writeTestJSON(t, filepath.Join(dir, "proxy_state.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "google", "action": "tunnel", "selected_chain_id": "chain-1"},
		},
	})
	oldDNSState := vpnDNSState
	vpnDNSState = &androidVPNDNSState{
		nextFakeOffset: 2,
		fakeDomainToIP: map[string]string{},
		fakeIPToEntry:  map[string]androidVPNDNSFakeEntry{},
		routeIPHints:   map[string]androidVPNDNSRouteHintEntry{},
	}
	defer func() {
		vpnDNSState = oldDNSState
	}()
	vpnRuntime.mu.Lock()
	oldConfigDir := vpnRuntime.configDir
	vpnRuntime.configDir = dir
	vpnRuntime.mu.Unlock()
	defer func() {
		vpnRuntime.mu.Lock()
		vpnRuntime.configDir = oldConfigDir
		vpnRuntime.mu.Unlock()
	}()

	query := buildTestDNSQuery(t, "dl.google.com", dnsmessage.TypeAAAA)
	response := buildAndroidVPNDNSSuccess(query, []net.IP{net.ParseIP("2001:4860:4802:36::223")}, dnsmessage.TypeAAAA)
	storeAndroidVPNDNSRouteHints("dl.google.com", response, proxyRouteDecision{Direct: false, Group: "google", SelectedChainID: "chain-1"})
	rememberAndroidVPNDNSRouteHintIPv4s("2001:4860:4802:36::223", []string{"142.251.188.95"})

	route, ok := buildAndroidVPNIPv4FallbackRoute(vpnRouteDecision{
		Direct:          false,
		TargetAddr:      "[2001:4860:4802:36::223]:443",
		Group:           "google",
		SelectedChainID: "chain-1",
	}, errors.New("dial tcp [2001:4860:4802:36::223]:443: i/o timeout"))
	if !ok {
		t.Fatal("expected ipv4 fallback route for string timeout")
	}
	if route.TargetAddr != "142.251.188.95:443" || route.SelectedChainID != "chain-1" {
		t.Fatalf("unexpected fallback route: %+v", route)
	}
}

func TestAndroidVPNDNSUsesFakeIPForFallbackTunnel(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_state.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "fallback", "action": "tunnel", "selected_chain_id": "chain-1"},
		},
	})
	oldDNSState := vpnDNSState
	vpnDNSState = &androidVPNDNSState{
		nextFakeOffset: 2,
		fakeDomainToIP: map[string]string{},
		fakeIPToEntry:  map[string]androidVPNDNSFakeEntry{},
		routeIPHints:   map[string]androidVPNDNSRouteHintEntry{},
	}
	defer func() {
		vpnDNSState = oldDNSState
	}()
	vpnRuntime.mu.Lock()
	oldConfigDir := vpnRuntime.configDir
	vpnRuntime.configDir = dir
	vpnRuntime.mu.Unlock()
	defer func() {
		vpnRuntime.mu.Lock()
		vpnRuntime.configDir = oldConfigDir
		vpnRuntime.mu.Unlock()
	}()

	query := buildTestDNSQuery(t, "www.google.com", dnsmessage.TypeA)
	response, err := resolveAndroidVPNDNSPacket(query)
	if err != nil {
		t.Fatal(err)
	}
	ips := extractTestDNSARecords(t, response)
	if len(ips) != 1 || !strings.HasPrefix(ips[0], "198.18.") {
		t.Fatalf("fallback tunnel fake ips=%v", ips)
	}
	route, err := decideVPNRouteForTarget(net.JoinHostPort(ips[0], "443"))
	if err != nil {
		t.Fatal(err)
	}
	if route.Direct || route.Group != "fallback" || route.SelectedChainID != "chain-1" || route.TargetAddr != "www.google.com:443" {
		t.Fatalf("fallback fake route not preserved: %+v", route)
	}
}

func TestAndroidVPNSelfCheckRoutesFakeIPThroughVPNDecision(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_state.json"), map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "fallback", "action": "tunnel", "selected_chain_id": "chain-1"},
		},
	})
	oldDNSState := vpnDNSState
	vpnDNSState = &androidVPNDNSState{
		nextFakeOffset: 2,
		fakeDomainToIP: map[string]string{},
		fakeIPToEntry:  map[string]androidVPNDNSFakeEntry{},
		routeIPHints:   map[string]androidVPNDNSRouteHintEntry{},
	}
	defer func() {
		vpnDNSState = oldDNSState
	}()
	vpnRuntime.mu.Lock()
	oldConfigDir := vpnRuntime.configDir
	vpnRuntime.configDir = dir
	vpnRuntime.mu.Unlock()
	defer func() {
		vpnRuntime.mu.Lock()
		vpnRuntime.configDir = oldConfigDir
		vpnRuntime.mu.Unlock()
	}()

	query := buildTestDNSQuery(t, "www.google.com", dnsmessage.TypeA)
	response, err := resolveAndroidVPNDNSPacket(query)
	if err != nil {
		t.Fatal(err)
	}
	ips := extractTestDNSARecords(t, response)
	if len(ips) != 1 {
		t.Fatalf("fake ips=%v", ips)
	}
	route, err := decideVPNRouteForTarget(net.JoinHostPort(ips[0], "443"))
	if err != nil {
		t.Fatal(err)
	}
	if route.Direct || route.Group != "fallback" || route.SelectedChainID != "chain-1" {
		t.Fatalf("self-check fake route would be wrong: %+v", route)
	}
}

func TestExtractVPNTLSClientHelloSNI(t *testing.T) {
	hello := buildTestTLSClientHello(t, "www.google.com")
	if got := extractVPNTLSClientHelloSNI(hello); got != "www.google.com" {
		t.Fatalf("sni=%q", got)
	}
}

func TestPrepareVPNTCPDialTargetUsesSNIForIP443(t *testing.T) {
	hello := buildTestTLSClientHello(t, "www.google.com")
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	go func() {
		_, _ = clientConn.Write(hello)
	}()
	preface, target, sni := prepareVPNTCPDialTarget(serverConn, "173.194.202.138:443")
	if sni != "www.google.com" || target != "www.google.com:443" {
		t.Fatalf("target=%q sni=%q", target, sni)
	}
	if !bytes.Equal(preface, hello) {
		t.Fatalf("preface was not preserved")
	}
}

func TestPrepareVPNTCPDialTargetReadsSplitTLSClientHello(t *testing.T) {
	hello := buildTestTLSClientHello(t, "www.google.com")
	if len(hello) < 12 {
		t.Fatalf("test hello too small: %d", len(hello))
	}
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	go func() {
		_, _ = clientConn.Write(hello[:7])
		time.Sleep(20 * time.Millisecond)
		_, _ = clientConn.Write(hello[7:])
	}()
	preface, target, sni := prepareVPNTCPDialTarget(serverConn, "173.194.202.138:443")
	if sni != "www.google.com" || target != "www.google.com:443" {
		t.Fatalf("target=%q sni=%q", target, sni)
	}
	if !bytes.Equal(preface, hello) {
		t.Fatalf("split preface was not preserved: got=%d want=%d", len(preface), len(hello))
	}
}

func buildTestTLSClientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	errCh := make(chan error, 1)
	go func() {
		tlsConn := tls.Client(clientConn, &tls.Config{ServerName: serverName, InsecureSkipVerify: true})
		errCh <- tlsConn.Handshake()
	}()
	_ = serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16*1024)
	n, err := serverConn.Read(buf)
	if err != nil {
		t.Fatalf("read client hello: %v", err)
	}
	_ = clientConn.Close()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
	}
	return append([]byte(nil), buf[:n]...)
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func buildTestDNSQuery(t *testing.T, domain string, qType dnsmessage.Type) []byte {
	t.Helper()
	name, err := dnsmessage.NewName(strings.TrimSuffix(domain, ".") + ".")
	if err != nil {
		t.Fatal(err)
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 100, RecursionDesired: true})
	if err := builder.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := builder.Question(dnsmessage.Question{Name: name, Type: qType, Class: dnsmessage.ClassINET}); err != nil {
		t.Fatal(err)
	}
	message, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func extractTestDNSARecords(t *testing.T, packet []byte) []string {
	t.Helper()
	parser := dnsmessage.Parser{}
	if _, err := parser.Start(packet); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := parser.Question(); err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			t.Fatal(err)
		}
	}
	var out []string
	for {
		header, err := parser.AnswerHeader()
		if err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			t.Fatal(err)
		}
		if header.Type != dnsmessage.TypeA {
			if err := parser.SkipAnswer(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		answer, err := parser.AResource()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, net.IP(answer.A[:]).String())
	}
	return out
}

type timeoutTestError struct{}

func (timeoutTestError) Error() string   { return "i/o timeout" }
func (timeoutTestError) Timeout() bool   { return true }
func (timeoutTestError) Temporary() bool { return true }

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func mustMarshalRawItems(t *testing.T, values ...any) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, json.RawMessage(raw))
	}
	return out
}

func testServerHostPort(t *testing.T, server *httptest.Server) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func reserveMobileChainTestAddr(t *testing.T) string {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		udp, udpErr := net.ListenPacket("udp", addr)
		if udpErr == nil {
			_ = udp.Close()
			if err := ln.Close(); err != nil {
				t.Fatal(err)
			}
			return addr
		}
		_ = ln.Close()
	}
	t.Fatal("failed to reserve tcp+udp test address")
	return ""
}

func assertLinkAuth(t *testing.T, r *http.Request, chainID string, secret string) {
	t.Helper()
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	nonce := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if nonce == "" || nonce == auth {
		t.Fatalf("missing bearer nonce: %q", auth)
	}
	want := buildLinkHMAC(secret, chainID, nonce)
	if got := r.Header.Get(linkCodexMACHeader); got != want {
		t.Fatalf("mac=%q want %q", got, want)
	}
	if r.Header.Get(linkCodexAuthModeHeader) != "secret_hmac" {
		t.Fatalf("auth mode=%q", r.Header.Get(linkCodexAuthModeHeader))
	}
}

func serveTestPingPongRelay(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	session, err := yamux.Server(conn, newLinkYamuxConfig())
	if err != nil {
		t.Fatalf("yamux server: %v", err)
	}
	defer session.Close()
	stream, err := session.Accept()
	if err != nil {
		t.Fatalf("yamux accept: %v", err)
	}
	defer stream.Close()
	var req linkTunnelOpenRequest
	if err := json.NewDecoder(stream).Decode(&req); err != nil {
		t.Fatalf("decode ping request: %v", err)
	}
	if req.Type != linkRelayModePingPong || req.PingBytes != 64 {
		t.Fatalf("unexpected ping request: %+v", req)
	}
	if err := json.NewEncoder(stream).Encode(linkTunnelOpenResponse{OK: true}); err != nil {
		t.Fatalf("encode ping response: %v", err)
	}
	buf := make([]byte, req.PingBytes)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read ping payload: %v", err)
	}
	if _, err := stream.Write(buf); err != nil {
		t.Fatalf("write ping echo: %v", err)
	}
}

func writeTestSpeedBytes(t *testing.T, conn net.Conn, byteCount int64) {
	t.Helper()
	defer conn.Close()
	chunk := []byte(strings.Repeat("a", 1024))
	remaining := byteCount
	for remaining > 0 {
		n := int64(len(chunk))
		if remaining < n {
			n = remaining
		}
		if _, err := conn.Write(chunk[:n]); err != nil {
			t.Fatalf("write speed bytes: %v", err)
		}
		remaining -= n
	}
}
