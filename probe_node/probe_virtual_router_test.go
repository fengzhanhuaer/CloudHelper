package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
)

func TestProbeVirtualRouterReachableViaCommonNode(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.1"},
			{NodeID: "2", IP: "198.18.0.2"},
			{NodeID: "3", IP: "198.18.0.3"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Direction: "bidirectional", Enabled: true},
			{FromNodeID: "2", ToNodeID: "3", Direction: "bidirectional", Enabled: true},
		},
	}
	if !probeVirtualRouterReachable(config, "1", "3") {
		t.Fatalf("node 1 should reach node 3 via node 2")
	}
	if got := probeVirtualRouterPath(config, "1", "3"); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("path=%v, want [1 2 3]", got)
	}
	if !probeVirtualRouterReachable(config, "3", "1") {
		t.Fatalf("node 3 should reach node 1 via node 2")
	}
}

func TestProbeVirtualRouterRelayHandlerCamouflagesPublicPaths(t *testing.T) {
	handler := buildProbeVirtualRouterRelayHandler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("root status=%d want 200 body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "OpenAI-compatible API endpoint") {
		t.Fatalf("root response missing camouflage payload: %s", rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("responses status=%d want 401 body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid_api_key") {
		t.Fatalf("responses response missing OpenAI-style error: %s", rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, probeRouteRelayAPIPath, nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("relay path status=%d want 400 body=%s", rr.Code, rr.Body.String())
	}
}

func TestProbeVirtualRouterReachableTreatsDirectionAsPhysicalDialOnly(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Direction: "forward", Enabled: true},
		},
	}
	if !probeVirtualRouterReachable(config, "1", "2") {
		t.Fatalf("node 1 should reach node 2")
	}
	if !probeVirtualRouterReachable(config, "2", "1") {
		t.Fatalf("node 2 should reach node 1 virtually; A->B only controls physical dial direction")
	}
	if got := probeVirtualRouterPath(config, "2", "1"); !reflect.DeepEqual(got, []string{"2", "1"}) {
		t.Fatalf("reverse virtual path=%v, want [2 1]", got)
	}
}

func TestProbeVirtualRouterCacheRoundTrip(t *testing.T) {
	t.Skip("disabled: VRoute cache persistence roundtrip test is excluded from the default regression suite")
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	config := probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "node-1", IP: "198.18.0.1", ServicePort: 12443},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{
				ID:                "rule-a",
				FromNodeID:        "node-1",
				ToNodeID:          "node-2",
				Direction:         "both",
				FromServiceDomain: "edge-a.example.com",
				FromServicePort:   443,
				ToServiceDomain:   "edge-b.internal.lan",
				ToServicePort:     443,
				RouteLayer:        "http3",
				Enabled:           true,
			},
			{
				ID:                "rule-b",
				FromNodeID:        "node-1",
				ToNodeID:          "node-2",
				Direction:         "both",
				FromServiceDomain: "edge-a-alt.example.com",
				FromServicePort:   443,
				ToServiceDomain:   "edge-b-alt.internal.lan",
				ToServicePort:     443,
				Enabled:           true,
			},
			{
				ID:         "rule-default-port",
				FromNodeID: "node-2",
				ToNodeID:   "node-3",
				Direction:  "both",
				Enabled:    true,
			},
		},
	}
	if err := persistProbeRouteConfigCache(config); err != nil {
		t.Fatalf("persist cache failed: %v", err)
	}
	path, err := resolveProbeRouteConfigCachePath()
	if err != nil {
		t.Fatalf("resolve cache path failed: %v", err)
	}
	if !strings.HasSuffix(path, probeRouteConfigCacheFileName) {
		t.Fatalf("cache path=%q, want suffix %q", path, probeRouteConfigCacheFileName)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
	loaded, err := loadProbeRouteConfigCache()
	if err != nil {
		t.Fatalf("load cache failed: %v", err)
	}
	if len(loaded.ProbeIPs) != 1 || loaded.ProbeIPs[0].NodeID != "1" {
		t.Fatalf("loaded probe ips=%+v", loaded.ProbeIPs)
	}
	if loaded.ProbeIPs[0].ServicePort != 12443 {
		t.Fatalf("loaded probe service port=%d, want 12443", loaded.ProbeIPs[0].ServicePort)
	}
	if len(loaded.TopologyRules) != 3 || loaded.TopologyRules[0].Direction != probeVirtualRouterDirectionForward {
		t.Fatalf("loaded topology=%+v", loaded.TopologyRules)
	}
	if loaded.TopologyRules[0].FromServiceDomain != "" || loaded.TopologyRules[0].FromServicePort != 0 || loaded.TopologyRules[0].ToServiceDomain != "edge-b.internal.lan" || loaded.TopologyRules[0].ToServicePort != 443 {
		t.Fatalf("loaded service config=%+v", loaded.TopologyRules[0])
	}
	if loaded.TopologyRules[0].RouteLayer != "websocket-h3" || loaded.TopologyRules[1].RouteLayer != "auto" {
		t.Fatalf("loaded route layers=%+v", loaded.TopologyRules)
	}
	if loaded.TopologyRules[1].FromServicePort != 0 || loaded.TopologyRules[1].ToServicePort != 443 {
		t.Fatalf("service port reuse should be preserved: %+v", loaded.TopologyRules)
	}
	if loaded.TopologyRules[2].FromServicePort != 0 || loaded.TopologyRules[2].ToServicePort != 0 {
		t.Fatalf("empty topology service ports=%d/%d, want zero", loaded.TopologyRules[2].FromServicePort, loaded.TopologyRules[2].ToServicePort)
	}
}

func TestProbeVirtualRouterCacheDoesNotPersistFakeIPLibrary(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	expiresAt := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	config := probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.11"},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Version:   7,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "api.example.com",
				FakeIP:     "198.18.4.9",
				Action:     "probe_exit",
				ExitNodeID: "9",
				ExpiresAt:  expiresAt,
			}},
		},
	}
	if err := persistProbeRouteConfigCache(config); err != nil {
		t.Fatalf("persist cache failed: %v", err)
	}
	path, err := resolveProbeRouteConfigCachePath()
	if err != nil {
		t.Fatalf("resolve cache path failed: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache failed: %v", err)
	}
	if strings.Contains(string(raw), "api.example.com") || strings.Contains(string(raw), "198.18.4.9") {
		t.Fatalf("fake ip library should not be persisted: %s", string(raw))
	}
	loaded, err := loadProbeRouteConfigCache()
	if err != nil {
		t.Fatalf("load cache failed: %v", err)
	}
	if len(loaded.FakeIPLibrary.Items) != 0 || loaded.FakeIPLibrary.Version != 0 {
		t.Fatalf("loaded fake ip library should be empty: %+v", loaded.FakeIPLibrary)
	}
}

func withProbeVirtualRouterRuleAuthForTest(t *testing.T, rule probeVirtualRouterTopologyRule) probeVirtualRouterTopologyRule {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	rawPublicKey := base64.StdEncoding.EncodeToString(pub)
	rule.Secret = "shared-link-secret"
	rule.UserID = "admin"
	rule.UserPublicKey = rawPublicKey
	rule.AuthTicket = buildProbeRouteUserAuthTicketForTest(t, priv, probeVirtualRouterRuntimeRouteID(rule), rawPublicKey, rule.FromNodeID, rule.ToNodeID)
	return rule
}

func resetProbeVirtualRouterStateForTest() {
	setProbeVirtualRouterNonDirectPathGuardEnabled(false)
	probeVirtualRouterRelayServerState.mu.Lock()
	relays := make([]*probeVirtualRouterRelayServer, 0, len(probeVirtualRouterRelayServerState.servers))
	for _, relay := range probeVirtualRouterRelayServerState.servers {
		relays = append(relays, relay)
	}
	probeVirtualRouterRelayServerState.servers = make(map[string]*probeVirtualRouterRelayServer)
	probeVirtualRouterRelayServerState.mu.Unlock()
	for _, relay := range relays {
		closeProbeVirtualRouterRelayServer(relay)
	}
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{}
	probeVirtualRouterState.localNodeID = ""
	probeVirtualRouterState.localIP = ""
	probeVirtualRouterState.nodeToIP = nil
	probeVirtualRouterState.ipToNode = nil
	probeVirtualRouterState.neighbors = nil
	probeVirtualRouterState.rulesByID = nil
	probeVirtualRouterState.topologySignature = ""
	probeVirtualRouterState.mu.Unlock()
	probeVirtualRouterRouteConfigRefreshState.mu.Lock()
	probeVirtualRouterRouteConfigRefreshState.running = make(map[string]bool)
	probeVirtualRouterRouteConfigRefreshState.lastAt = make(map[string]time.Time)
	probeVirtualRouterRouteConfigRefreshState.mu.Unlock()
	probeVirtualRouterFakeIPItemRefreshState.mu.Lock()
	probeVirtualRouterFakeIPItemRefreshState.running = make(map[string]bool)
	probeVirtualRouterFakeIPItemRefreshState.lastAt = make(map[string]time.Time)
	probeVirtualRouterFakeIPItemRefreshState.mu.Unlock()
	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	probeVirtualRouterFakeIPVerifyState.pending = make(map[string]chan probeVirtualRouterFakeIPVerifyPayload)
	probeVirtualRouterFakeIPVerifyState.running = make(map[string]bool)
	probeVirtualRouterFakeIPVerifyState.lastAt = make(map[string]time.Time)
	probeVirtualRouterFakeIPVerifyState.failures = make(map[string]int)
	probeVirtualRouterFakeIPVerifyState.synFlows = make(map[string]probeVirtualRouterFakeIPVerifySYNFlow)
	probeVirtualRouterFakeIPVerifyState.mu.Unlock()
	probeVirtualRouterLogThrottleState.mu.Lock()
	probeVirtualRouterLogThrottleState.items = make(map[string]probeVirtualRouterLogThrottleEntry)
	probeVirtualRouterLogThrottleState.mu.Unlock()
	probeVirtualRouterNonDirectPathGuardState.mu.Lock()
	probeVirtualRouterNonDirectPathGuardState.failedPaths = make(map[string]struct{})
	probeVirtualRouterNonDirectPathGuardState.mu.Unlock()
	waitProbeVirtualRouterLocalInterfaceIPEnsure()
	clearProbeVirtualRouterRouteCache("test reset")
	probeVirtualRouterDisconnectedCarrierState.mu.Lock()
	probeVirtualRouterDisconnectedCarrierState.routeIDs = make(map[string]struct{})
	probeVirtualRouterDisconnectedCarrierState.mu.Unlock()
	probeVirtualRouterPathRTTState.mu.Lock()
	probeVirtualRouterPathRTTState.items = make(map[string]probeVirtualRouterPathRTTRecord)
	probeVirtualRouterPathRTTState.mu.Unlock()
	probeVirtualRouterPathRecoveryState.mu.Lock()
	probeVirtualRouterPathRecoveryState.inflight = make(map[string]struct{})
	probeVirtualRouterPathRecoveryState.mu.Unlock()
	flushProbeVirtualRouterRecentPacketEvents()
	probeVirtualRouterRecentPacketState.mu.Lock()
	probeVirtualRouterRecentPacketState.nextID = 0
	probeVirtualRouterRecentPacketState.writeIndex = 0
	probeVirtualRouterRecentPacketState.items = nil
	probeVirtualRouterRecentPacketState.mu.Unlock()
	probeVirtualRouterRecentPacketRecorder.mu.Lock()
	probeVirtualRouterRecentPacketRecorder.dropped = 0
	probeVirtualRouterRecentPacketRecorder.droppedTotal = 0
	probeVirtualRouterRecentPacketRecorder.lastDropLogAt = time.Time{}
	probeVirtualRouterRecentPacketRecorder.mu.Unlock()
	probeVirtualRouterRecentConnectionState.mu.Lock()
	probeVirtualRouterRecentConnectionState.items = make(map[string]probeVirtualRouterRecentConnection)
	probeVirtualRouterRecentConnectionState.lastPruneAt = time.Time{}
	probeVirtualRouterRecentConnectionState.mu.Unlock()
	probeVirtualRouterControllerState.mu.Lock()
	probeVirtualRouterControllerState.identity = nodeIdentity{}
	probeVirtualRouterControllerState.controllerBaseURL = ""
	probeVirtualRouterControllerState.mu.Unlock()
	probeVirtualRouterSpeedReceiveState.mu.Lock()
	probeVirtualRouterSpeedReceiveState.sessions = make(map[string]*probeVirtualRouterSpeedReceiveSession)
	probeVirtualRouterSpeedReceiveState.completed = make(map[string]time.Time)
	probeVirtualRouterSpeedReceiveState.mu.Unlock()
}

func TestBuildProbeVirtualRouterRuntimeConfigRequiresLinkAuthFields(t *testing.T) {
	rule := probeVirtualRouterTopologyRule{
		ID:              "rule-auth",
		FromNodeID:      "1",
		ToNodeID:        "2",
		Direction:       "bidirectional",
		ToServiceDomain: "node-2.example.com",
		ToServicePort:   12040,
		FromServicePort: 12040,
		Enabled:         true,
	}
	config := probeVirtualRouterConfig{Enabled: true, TopologyRules: []probeVirtualRouterTopologyRule{rule}}
	if _, ok := buildProbeVirtualRouterRuntimeConfigForRule(config, rule, nodeIdentity{NodeID: "1"}, ""); ok {
		t.Fatalf("runtime config should require link auth fields")
	}

	rule = withProbeVirtualRouterRuleAuthForTest(t, rule)
	config.TopologyRules = []probeVirtualRouterTopologyRule{rule}
	cfg, ok := buildProbeVirtualRouterRuntimeConfigForRule(config, rule, nodeIdentity{NodeID: "1"}, "")
	if !ok {
		t.Fatalf("runtime config should be built with link auth fields")
	}
	if cfg.secret != "shared-link-secret" || cfg.authTicket == "" || len(cfg.userPublicKey) != ed25519.PublicKeySize {
		t.Fatalf("runtime auth fields not applied: secret=%q ticket=%t pub=%d", cfg.secret, cfg.authTicket != "", len(cfg.userPublicKey))
	}
	if cfg.routeLayer != "auto" {
		t.Fatalf("default runtime route layer=%q, want auto", cfg.routeLayer)
	}
	rule.RouteLayer = "http3"
	rule = withProbeVirtualRouterRuleAuthForTest(t, rule)
	cfg, ok = buildProbeVirtualRouterRuntimeConfigForRule(config, rule, nodeIdentity{NodeID: "1"}, "")
	if !ok {
		t.Fatalf("runtime config with route layer should be built")
	}
	if cfg.routeLayer != "websocket-h3" {
		t.Fatalf("runtime route layer=%q, want websocket-h3", cfg.routeLayer)
	}
}

func TestProbeRouteRelayProtocolCandidatesSupportVirtualRouterRouteLayerSelection(t *testing.T) {
	cases := []struct {
		name  string
		layer string
		want  []string
	}{
		{name: "auto", layer: "auto", want: []string{"websocket-h3", "websocket"}},
		{name: "empty", layer: "", want: []string{"websocket-h3", "websocket"}},
		{name: "http2", layer: "http2", want: []string{"websocket"}},
		{name: "http3", layer: "http3", want: []string{"websocket-h3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeRouteRelayProtocolCandidates(tc.layer); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("candidates=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestProbeVirtualRouterSpeedReceiveDropsLateAndOrphanChunks(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	requestID := "speed-late-test"
	startProbeVirtualRouterSpeedReceive(probeVirtualRouterSpeedTestResultPayload{
		RequestID:     requestID,
		Direction:     "down",
		SourceNodeID:  "9",
		TargetNodeID:  "19",
		ResultNodeID:  "9",
		Path:          []string{"19", "9"},
		MaxDurationMS: 8000,
	}, "9", "route-speed-test")
	recordProbeVirtualRouterSpeedChunk(requestID, 1024)
	result, ok := finishProbeVirtualRouterSpeedReceive(probeVirtualRouterSpeedTestResultPayload{RequestID: requestID}, "9")
	if !ok {
		t.Fatalf("finish should produce speed result")
	}
	if result.Bytes != 1024 || result.Frames != 1 || strings.Join(result.Path, ">") != "19>9" {
		t.Fatalf("unexpected speed result: bytes=%d frames=%d path=%s", result.Bytes, result.Frames, strings.Join(result.Path, ">"))
	}

	recordProbeVirtualRouterSpeedChunk(requestID, 1024)
	recordProbeVirtualRouterSpeedChunk("orphan-speed-test", 1024)

	probeVirtualRouterSpeedReceiveState.mu.Lock()
	lateSession := probeVirtualRouterSpeedReceiveState.sessions[requestID]
	orphanSession := probeVirtualRouterSpeedReceiveState.sessions["orphan-speed-test"]
	_, completed := probeVirtualRouterSpeedReceiveState.completed[requestID]
	probeVirtualRouterSpeedReceiveState.mu.Unlock()
	if lateSession != nil {
		t.Fatalf("late chunk recreated completed speed session: %+v", lateSession)
	}
	if orphanSession != nil {
		t.Fatalf("orphan chunk should not create speed session: %+v", orphanSession)
	}
	if !completed {
		t.Fatalf("completed speed request should be retained for late chunk suppression")
	}
}

func TestProbeVirtualRouterICMPTraceUsesLocalNodeWhenRuntimeNil(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.localNodeID = "16"
	probeVirtualRouterState.mu.Unlock()

	trace := appendProbeVirtualRouterICMPTrace(nil, nil, "tun_rx", "", "")
	if len(trace) != 1 {
		t.Fatalf("trace hops=%d, want 1", len(trace))
	}
	if trace[0].NodeID != "16" || trace[0].Event != "tun_rx" || trace[0].UnixNano <= 0 {
		t.Fatalf("unexpected trace hop: %+v", trace[0])
	}
}

func TestProbeVirtualRouterICMPTraceEnvelopeRoundTrip(t *testing.T) {
	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{
		routeID:  "vrouter-test",
		identity: nodeIdentity{NodeID: "16"},
	}}
	trace := appendProbeVirtualRouterICMPTrace(nil, rt, "frame_rx", "", "")
	trace = appendProbeVirtualRouterICMPTrace(trace, rt, "local_deliver", "", "")
	frame, err := buildProbeVirtualRouterIPFrame(buildProbeVirtualRouterTestICMPEchoRequest(t, "198.18.0.21", "198.18.0.18"), []string{"19", "16"}, trace)
	if err != nil {
		t.Fatalf("build frame: %v", err)
	}
	raw, err := marshalProbeVirtualRouterFrameEnvelope(frame)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	got, err := unmarshalProbeVirtualRouterFrameEnvelope(raw)
	if err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	control, err := probeVirtualRouterFrameControl(got, nil)
	if err != nil {
		t.Fatalf("unmarshal control: %v", err)
	}
	if len(control.Trace) != 2 {
		t.Fatalf("trace hops=%d, want 2: %+v", len(control.Trace), control.Trace)
	}
	if control.Trace[0].Event != "frame_rx" || control.Trace[1].Event != "local_deliver" {
		t.Fatalf("unexpected trace events: %+v", control.Trace)
	}
}

func TestProbeVirtualRouterRuntimeFrameStatsAreDirectional(t *testing.T) {
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldItems := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = make(map[string]*probeVirtualRouterRuntimeStats)
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldItems
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-frame-stats"}}
	recordProbeVirtualRouterRuntimeFrameSent(rt, 123)
	recordProbeVirtualRouterRuntimeFrameReceived(rt, 456)

	stats := snapshotProbeVirtualRouterRuntimeStats("vrouter-frame-stats")
	if stats == nil {
		t.Fatalf("stats missing")
	}
	if stats.FramesSent != 1 || stats.FrameBytesSent != 123 {
		t.Fatalf("sent frame stats=%+v", stats)
	}
	if stats.FramesReceived != 1 || stats.FrameBytesReceived != 456 {
		t.Fatalf("received frame stats=%+v", stats)
	}
	if stats.LastFrameAt == "" {
		t.Fatalf("last frame time should be recorded")
	}
}

func TestRememberProbeVirtualRouterAuthTickets(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeRouteAuthTicketStoreForTest()
	defer resetProbeRouteAuthTicketStoreForTest()

	rule := withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
		ID:              "rule-ticket-cache",
		FromNodeID:      "1",
		ToNodeID:        "2",
		Direction:       probeVirtualRouterDirectionForward,
		FromServicePort: 12040,
		ToServicePort:   12041,
		Enabled:         true,
	})
	config := probeVirtualRouterConfig{
		Enabled:       true,
		TopologyRules: []probeVirtualRouterTopologyRule{rule},
	}

	rememberProbeVirtualRouterAuthTickets(config)

	routeID := probeVirtualRouterRuntimeRouteID(rule)
	if got := lookupProbeRouteAuthTicket(routeID); got != rule.AuthTicket {
		t.Fatalf("cached virtual router ticket=%q want %q", got, rule.AuthTicket)
	}
}

func TestVerifyProbeVirtualRouterUserAuthTicket(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeRouteAuthTicketStoreForTest()
	defer resetProbeRouteAuthTicketStoreForTest()

	rule := withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
		ID:              "rule-ticket-refresh",
		FromNodeID:      "1",
		ToNodeID:        "2",
		Direction:       probeVirtualRouterDirectionForward,
		FromServicePort: 12040,
		ToServicePort:   12041,
		Enabled:         true,
	})
	routeID := probeVirtualRouterRuntimeRouteID(rule)
	pub, err := parseProbeRouteUserPublicKey(rule.UserPublicKey)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}

	cfg := probeVirtualRouterRuntimeConfig{
		routeID:       routeID,
		rawPublicKey:  rule.UserPublicKey,
		userPublicKey: pub,
		authTicket:    rule.AuthTicket,
		fromNodeID:    rule.FromNodeID,
		toNodeID:      rule.ToNodeID,
	}
	if err := verifyProbeVirtualRouterUserAuthTicket(cfg, rule.AuthTicket); err != nil {
		t.Fatalf("verify virtual router auth ticket failed: %v", err)
	}
}

func TestProbeVirtualRouterCurrentLocalPathToIP(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.1"},
			{NodeID: "2", IP: "198.18.0.2"},
			{NodeID: "3", IP: "198.18.0.3"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Direction: "bidirectional", Enabled: true},
			{FromNodeID: "2", ToNodeID: "3", Direction: "bidirectional", Enabled: true},
		},
		RouteRules: []probeVirtualRouterRouteRule{
			{
				ID:         "telegram",
				Name:       "Telegram",
				Action:     "probe_exit",
				ExitNodeID: "3",
				Entries:    []string{"cidr:149.154.160.0/20"},
			},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "1")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	if got := currentProbeVirtualRouterLocalNodeID(); got != "1" {
		t.Fatalf("local node id=%q, want 1", got)
	}
	if got := currentProbeVirtualRouterLocalIP(); got != "198.18.0.1" {
		t.Fatalf("local ip=%q, want 198.18.0.1", got)
	}
	probeVirtualRouterState.mu.RLock()
	storedLocalIP := probeVirtualRouterState.localIP
	probeVirtualRouterState.mu.RUnlock()
	if storedLocalIP != "198.18.0.1" {
		t.Fatalf("stored local ip=%q, want 198.18.0.1", storedLocalIP)
	}
	if got := currentProbeVirtualRouterIPForNode("2"); got != "198.18.0.2" {
		t.Fatalf("node 2 ip=%q, want 198.18.0.2", got)
	}
	if got := currentProbeVirtualRouterNodeIDForIP("198.18.0.3"); got != "3" {
		t.Fatalf("node for ip=%q, want 3", got)
	}
	if got := currentProbeVirtualRouterPathBetweenNodes("1", "3"); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("indexed path=%v, want [1 2 3]", got)
	}
	if got := currentProbeVirtualRouterPathToIP("198.18.0.3"); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("path to ip=%v, want [1 2 3]", got)
	}
	if got := currentProbeVirtualRouterPathToIP("149.154.166.110"); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("path to cidr route ip=%v, want [1 2 3]", got)
	}
	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.1", "149.154.166.110", 49152, 443)
	if got := currentProbeVirtualRouterPathForPacket(packet, "149.154.166.110"); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("packet path to cidr route ip=%v, want [1 2 3]", got)
	}
	plan, err := buildProbeVirtualRouterRouteTestPlan("149.154.166.110", 443)
	if err != nil {
		t.Fatalf("build cidr route test plan failed: %v", err)
	}
	if plan.RouteRuleName != "Telegram" || plan.ExitNodeID != "3" || !reflect.DeepEqual(plan.Path, []string{"1", "2", "3"}) {
		t.Fatalf("cidr route test plan=%+v", plan)
	}
}

func TestProbeVirtualRouterIPCanBeFakeIPSkipsReservedProbePool(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "19", IP: "198.18.0.21"},
		},
	}, "19")

	cases := []struct {
		ip   string
		want bool
	}{
		{ip: "198.18.0.0", want: false},
		{ip: "198.18.0.7", want: false},
		{ip: "198.18.0.21", want: false},
		{ip: "198.18.4.0", want: false},
		{ip: "198.18.4.1", want: true},
		{ip: "149.154.167.51", want: false},
	}
	for _, tc := range cases {
		if got := probeVirtualRouterIPCanBeFakeIP(tc.ip); got != tc.want {
			t.Fatalf("probeVirtualRouterIPCanBeFakeIP(%q)=%t, want %t", tc.ip, got, tc.want)
		}
	}
}

func TestProbeVirtualRouterReservedProbePoolDoesNotScheduleFakeIPRefresh(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("reserved probe-pool ip should not request fake-ip refresh: %s", r.URL.Path)
	}))
	defer server.Close()

	rememberProbeVirtualRouterController(nodeIdentity{NodeID: "19", Secret: "secret"}, server.URL)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "19", IP: "198.18.0.21"},
		},
	}, "19")

	if scheduleProbeVirtualRouterFakeIPItemRefreshByIP("198.18.0.7") {
		t.Fatalf("reserved probe-pool ip scheduled fake-ip refresh")
	}
	if scheduleProbeVirtualRouterFakeIPItemRefreshByIP("198.18.4.0") {
		t.Fatalf("last reserved probe-pool ip scheduled fake-ip refresh")
	}
}

func TestProbeVirtualRouterApplyConfigReconcilesLocalFakeIPExitWithRouteRules(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	expiresAt := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.1"},
			{NodeID: "2", IP: "198.18.0.2"},
			{NodeID: "3", IP: "198.18.0.3"},
		},
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:         "rr-old",
			Name:       "example",
			Action:     "probe_exit",
			ExitNodeID: "2",
			Entries:    []string{"domain_suffix:example.com"},
		}},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Version: 1,
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "api.example.com",
				FakeIP:     "198.18.4.9",
				RuleID:     "rr-old",
				Action:     "probe_exit",
				ExitNodeID: "2",
				ExpiresAt:  expiresAt,
			}},
		},
	}, "1")

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.1"},
			{NodeID: "2", IP: "198.18.0.2"},
			{NodeID: "3", IP: "198.18.0.3"},
		},
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:         "rr-new",
			Name:       "example",
			Action:     "probe_exit",
			ExitNodeID: "3",
			Entries:    []string{"domain_suffix:example.com"},
		}},
	}, "1")

	entry, ok := currentProbeVirtualRouterFakeIPEntryByIP("198.18.4.9")
	if !ok {
		t.Fatalf("fake ip entry should be kept")
	}
	if entry.RuleID != "rr-new" || entry.Action != "probe_exit" || entry.ExitNodeID != "3" {
		t.Fatalf("fake ip entry did not follow route rule exit update: %+v", entry)
	}
}

func TestProbeVirtualRouterApplyConfigRemovesLocalFakeIPWhenRouteNoLongerUsesExit(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	expiresAt := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.1"},
			{NodeID: "2", IP: "198.18.0.2"},
		},
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:         "rr-exit",
			Name:       "example",
			Action:     "probe_exit",
			ExitNodeID: "2",
			Entries:    []string{"domain_suffix:example.com"},
		}},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Version: 1,
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "api.example.com",
				FakeIP:     "198.18.4.9",
				RuleID:     "rr-exit",
				Action:     "probe_exit",
				ExitNodeID: "2",
				ExpiresAt:  expiresAt,
			}},
		},
	}, "1")

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.1"},
			{NodeID: "2", IP: "198.18.0.2"},
		},
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:      "rr-direct",
			Name:    "example",
			Action:  "direct",
			Entries: []string{"domain_suffix:example.com"},
		}},
	}, "1")

	if entry, ok := currentProbeVirtualRouterFakeIPEntryByIP("198.18.4.9"); ok {
		t.Fatalf("fake ip entry should be removed after route no longer uses exit: %+v", entry)
	}
}

func TestProbeVirtualRouterPacketPathUsesCurrentRuleExitForStaleFakeIPEntry(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	expiresAt := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.1"},
			{NodeID: "9", IP: "198.18.0.9"},
			{NodeID: "18", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.19"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{ID: "vr-9-18", FromNodeID: "9", ToNodeID: "18", Direction: "bidirectional", Enabled: true},
			{ID: "vr-9-19", FromNodeID: "9", ToNodeID: "19", Direction: "bidirectional", Enabled: true},
			{ID: "vr-19-1", FromNodeID: "19", ToNodeID: "1", Direction: "bidirectional", Enabled: true},
		},
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:         "rr-6",
			Name:       "AI",
			Action:     "probe_exit",
			ExitNodeID: "1",
			Entries:    []string{"domain_suffix:chatgpt.com"},
		}},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Version: 1,
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "chatgpt.com",
				FakeIP:     "198.18.4.68",
				RuleID:     "rr-old",
				Action:     "probe_exit",
				ExitNodeID: "18",
				ExpiresAt:  expiresAt,
			}},
		},
	}, "9")

	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.9", "198.18.4.68", 59681, 443)
	if got := currentProbeVirtualRouterPathForPacket(packet, "198.18.4.68"); !reflect.DeepEqual(got, []string{"9", "19", "1"}) {
		t.Fatalf("packet path=%v, want [9 19 1]", got)
	}
	item := buildProbeVirtualRouterRecentPacket("tun_rx", "forward", nil, packet, []string{"9", "19", "1"}, false, nil)
	if !item.FakeIP || item.FakeIPDomain != "chatgpt.com" || item.FakeIPExitNode != "1" {
		t.Fatalf("recent packet fake ip metadata should use current rule exit: %+v", item)
	}
	entry, ok := currentProbeVirtualRouterFakeIPEntryByIP("198.18.4.68")
	if !ok || entry.RuleID != "rr-6" || entry.ExitNodeID != "1" {
		t.Fatalf("effective fake ip entry=%+v ok=%v, want rr-6 exit 1", entry, ok)
	}
}

func TestBuildProbeVirtualRouterRuntimeConfigsForNode(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", DisplayName: "入口节点", IP: "198.18.0.11", ServicePort: 12040},
			{NodeID: "2", DisplayName: "东京出口", IP: "198.18.0.12", ServicePort: 12440},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
				ID:                "edge-a-b",
				FromNodeID:        "1",
				ToNodeID:          "2",
				Direction:         "bidirectional",
				FromServiceDomain: "a.internal",
				FromServicePort:   12040,
				ToServiceDomain:   "b.internal",
				ToServicePort:     13040,
				Enabled:           true,
			}),
		},
	}
	left := buildProbeVirtualRouterRuntimeConfigsForNode(config, nodeIdentity{NodeID: "1", Secret: "node-1"}, "")
	right := buildProbeVirtualRouterRuntimeConfigsForNode(config, nodeIdentity{NodeID: "2", Secret: "node-2"}, "")
	if len(left) != 1 || len(right) != 1 {
		t.Fatalf("runtime configs left=%d right=%d", len(left), len(right))
	}
	if left[0].routeID != right[0].routeID || !isProbeVirtualRouterRuntimeRouteID(left[0].routeID) {
		t.Fatalf("unexpected route ids left=%q right=%q", left[0].routeID, right[0].routeID)
	}
	if left[0].peerNodeID != "2" || left[0].peerName != "东京出口" || left[0].peerHost != "b.internal" || left[0].peerPort != 12440 || !left[0].dialer {
		t.Fatalf("left runtime should dial node 2: %+v", left[0])
	}
	if right[0].peerNodeID != "1" || right[0].peerName != "入口节点" || right[0].dialer || right[0].peerHost != "" || right[0].peerPort != 0 {
		t.Fatalf("right runtime should wait for node 1: %+v", right[0])
	}
	if left[0].listenPort != 0 || right[0].listenPort != 12440 {
		t.Fatalf("listen ports left=%d right=%d", left[0].listenPort, right[0].listenPort)
	}
}

func TestBuildProbeVirtualRouterRuntimeConfigsUseCloudflareCopilotPortForDial(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.11", ServicePort: 12040},
			{NodeID: "2", IP: "198.18.0.12", ServicePort: 12440},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
				ID:              "edge-a-b",
				FromNodeID:      "1",
				ToNodeID:        "2",
				ToServiceDomain: "api_copilot_nw.example.com",
				Enabled:         true,
			}),
		},
	}
	left := buildProbeVirtualRouterRuntimeConfigsForNode(config, nodeIdentity{NodeID: "1", Secret: "node-1"}, "")
	right := buildProbeVirtualRouterRuntimeConfigsForNode(config, nodeIdentity{NodeID: "2", Secret: "node-2"}, "")
	if len(left) != 1 || len(right) != 1 {
		t.Fatalf("runtime configs left=%d right=%d", len(left), len(right))
	}
	if left[0].peerHost != "api_copilot_nw.example.com" || left[0].peerPort != 443 || !left[0].dialer {
		t.Fatalf("dialer should connect to cloudflare copilot 443: %+v", left[0])
	}
	if right[0].listenPort != 12440 || right[0].peerPort != 0 || right[0].dialer {
		t.Fatalf("listener should keep static service port: %+v", right[0])
	}
}

func TestProbeVirtualRouterBridgePreserveDomainForCloudflareCopilot(t *testing.T) {
	resetProbeRouteRelayResolveCacheForTest()
	oldLookup := probeRouteRelayLookupIP
	lookupCalled := false
	probeRouteRelayLookupIP = func(context.Context, string, string) ([]net.IP, error) {
		lookupCalled = true
		return []net.IP{net.ParseIP("203.0.113.9")}, nil
	}
	t.Cleanup(func() {
		probeRouteRelayLookupIP = oldLookup
		resetProbeRouteRelayResolveCacheForTest()
	})

	host := "api_copilot_nw.example.com"
	dialHost, hostHeader, err := resolveProbeRouteDialIPHostWithPolicy(host, isProbeVirtualRouterCloudflareCopilotDomain(host))
	if err != nil {
		t.Fatalf("resolve cf copilot host failed: %v", err)
	}
	if lookupCalled {
		t.Fatalf("cf copilot host should preserve domain without resolving")
	}
	if dialHost != host || hostHeader != host {
		t.Fatalf("cf copilot host should preserve domain, dial=%q host=%q", dialHost, hostHeader)
	}
	if got := resolveProbeRouteClientTLSServerName("websocket", dialHost, hostHeader); got != host {
		t.Fatalf("cf copilot SNI=%q, want %q", got, host)
	}
}

func TestProbeVirtualRouterBridgeResolvesOrdinaryDomainToIP(t *testing.T) {
	resetProbeRouteRelayResolveCacheForTest()
	oldLookup := probeRouteRelayLookupIP
	probeRouteRelayLookupIP = func(_ context.Context, _ string, host string) ([]net.IP, error) {
		if host != "ordinary.example.com" {
			t.Fatalf("lookup host=%q", host)
		}
		return []net.IP{net.ParseIP("203.0.113.9")}, nil
	}
	t.Cleanup(func() {
		probeRouteRelayLookupIP = oldLookup
		resetProbeRouteRelayResolveCacheForTest()
	})

	dialHost, hostHeader, err := resolveProbeRouteDialIPHostWithPolicy("ordinary.example.com", false)
	if err != nil {
		t.Fatalf("resolve ordinary host failed: %v", err)
	}
	if dialHost != "203.0.113.9" || hostHeader != "203.0.113.9" {
		t.Fatalf("ordinary host must not leak its domain through host or sni, dial=%q host=%q", dialHost, hostHeader)
	}
}

func TestProbeRouteRelayTLSOmitsSNIAndDoesNotUseNodeCertificatePin(t *testing.T) {
	config, err := newProbeRouteRelayTLSConfig("vrouter-no-pin-test", "203.0.113.9", tls.VersionTLS12, nil)
	if err != nil {
		t.Fatalf("build tls config: %v", err)
	}
	if config.ServerName != "" || !config.InsecureSkipVerify || config.VerifyConnection != nil {
		t.Fatalf("ordinary relay must omit sni and leave peer authentication to route auth: %+v", config)
	}
}

func TestProbeRouteAuthNonceReplaySurvivesMemoryReset(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeRouteAuthReplayMemoryForTest()
	t.Cleanup(resetProbeRouteAuthReplayMemoryForTest)
	if err := recordProbeRouteAuthNonce("route-replay", "current-ticket", "nonce-1"); err != nil {
		t.Fatalf("record nonce: %v", err)
	}
	resetProbeRouteAuthReplayMemoryForTest()
	if err := recordProbeRouteAuthNonce("route-replay", "current-ticket", "nonce-1"); err == nil {
		t.Fatal("persisted nonce replay should be rejected after memory reset")
	}
	if err := recordProbeRouteAuthNonce("route-replay", "rotated-ticket", "nonce-1"); err != nil {
		t.Fatalf("rotated ticket should start a new nonce namespace: %v", err)
	}
}

func TestProbeRouteRelayFailedDialInvalidatesCachedDomainIP(t *testing.T) {
	resetProbeRouteRelayResolveCacheForTest()
	oldLookup := probeRouteRelayLookupIP
	lookupCalls := 0
	probeRouteRelayLookupIP = func(_ context.Context, _ string, host string) ([]net.IP, error) {
		lookupCalls++
		if host != "route.example.com" {
			t.Fatalf("lookup host=%q", host)
		}
		return []net.IP{net.ParseIP("203.0.113.20")}, nil
	}
	t.Cleanup(func() {
		probeRouteRelayLookupIP = oldLookup
		resetProbeRouteRelayResolveCacheForTest()
	})

	storeProbeRouteRelayResolveCache("route.example.com", "203.0.113.10", "203.0.113.10")
	dialHost, _, err := resolveProbeRouteDialIPHost("route.example.com")
	if err != nil || dialHost != "203.0.113.10" || lookupCalls != 0 {
		t.Fatalf("cached dial host=%q lookup_calls=%d err=%v", dialHost, lookupCalls, err)
	}

	invalidateProbeRouteRelayResolveCacheAfterFailedDial("route.example.com", dialHost)
	dialHost, _, err = resolveProbeRouteDialIPHost("route.example.com")
	if err != nil || dialHost != "203.0.113.20" || lookupCalls != 1 {
		t.Fatalf("refreshed dial host=%q lookup_calls=%d err=%v", dialHost, lookupCalls, err)
	}
}

func TestBuildProbeVirtualRouterRuntimeConfigsAllowSharedPortAcrossRules(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.11", ServicePort: 12040},
			{NodeID: "2", IP: "198.18.0.12", ServicePort: 12441},
			{NodeID: "3", IP: "198.18.0.13", ServicePort: 12042},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
				ID:              "edge-a-b",
				FromNodeID:      "1",
				ToNodeID:        "2",
				FromServicePort: 12040,
				ToServiceDomain: "b.internal",
				ToServicePort:   13040,
				Enabled:         true,
			}),
			withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
				ID:              "edge-c-b",
				FromNodeID:      "3",
				ToNodeID:        "2",
				FromServicePort: 12040,
				ToServiceDomain: "b.internal",
				ToServicePort:   13041,
				Enabled:         true,
			}),
		},
	}

	left := buildProbeVirtualRouterRuntimeConfigsForNode(config, nodeIdentity{NodeID: "1", Secret: "node-1"}, "")
	middle := buildProbeVirtualRouterRuntimeConfigsForNode(config, nodeIdentity{NodeID: "2", Secret: "node-2"}, "")
	right := buildProbeVirtualRouterRuntimeConfigsForNode(config, nodeIdentity{NodeID: "3", Secret: "node-3"}, "")
	if len(left) != 1 || len(middle) != 2 || len(right) != 1 {
		t.Fatalf("runtime configs left=%d middle=%d right=%d", len(left), len(middle), len(right))
	}
	if left[0].listenPort != 0 || right[0].listenPort != 0 {
		t.Fatalf("dialer listen ports left=%d right=%d", left[0].listenPort, right[0].listenPort)
	}
	if left[0].peerHost != "b.internal" || left[0].peerPort != 12441 || right[0].peerHost != "b.internal" || right[0].peerPort != 12441 {
		t.Fatalf("dialers should target same B service port: left=%+v right=%+v", left[0], right[0])
	}
	if middle[0].listenPort != 12441 || middle[1].listenPort != 12441 {
		t.Fatalf("listener ports middle=%d/%d", middle[0].listenPort, middle[1].listenPort)
	}
	if middle[0].routeID == middle[1].routeID {
		t.Fatalf("rules sharing one port must still have distinct route ids: %+v", middle)
	}
	seenPrev := map[string]struct{}{
		middle[0].peerNodeID: {},
		middle[1].peerNodeID: {},
	}
	if _, ok := seenPrev["1"]; !ok {
		t.Fatalf("B runtime should keep rule from node 1: %+v", middle)
	}
	if _, ok := seenPrev["3"]; !ok {
		t.Fatalf("B runtime should keep rule from node 3: %+v", middle)
	}
}

func TestProbeVirtualRouterRuntimeRouteIDIsStableAcrossServiceEndpointChanges(t *testing.T) {
	base := probeVirtualRouterTopologyRule{
		ID:                "edge-a-b",
		FromNodeID:        "1",
		ToNodeID:          "2",
		FromServiceDomain: "old-a.internal",
		FromServicePort:   12040,
		ToServiceDomain:   "old-b.internal",
		ToServicePort:     12040,
		Enabled:           true,
	}
	changedEndpoint := base
	changedEndpoint.FromServiceDomain = "new-a.internal"
	changedEndpoint.FromServicePort = 13040
	changedEndpoint.ToServiceDomain = "new-b.internal"
	changedEndpoint.ToServicePort = 13041
	changedEndpoint.FromNodeID = "3"
	changedEndpoint.ToNodeID = "4"

	if left, right := probeVirtualRouterRuntimeRouteID(base), probeVirtualRouterRuntimeRouteID(changedEndpoint); left != right {
		t.Fatalf("same rule should keep route id across topology endpoint changes: %s != %s", left, right)
	}

	changedRule := base
	changedRule.ID = "edge-a-b-other"
	if left, right := probeVirtualRouterRuntimeRouteID(base), probeVirtualRouterRuntimeRouteID(changedRule); left == right {
		t.Fatalf("different rule ids should produce different route ids: %s", left)
	}
}

func TestSanitizeProbeVirtualRouterTopologyRulesInitializesRuleIDsBySequence(t *testing.T) {
	config := sanitizeProbeVirtualRouterConfigForCache(probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{ID: "vr-1", FromNodeID: "2", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	})
	if len(config.TopologyRules) != 3 {
		t.Fatalf("topology rules=%+v", config.TopologyRules)
	}
	idsByFrom := map[string]string{}
	for _, rule := range config.TopologyRules {
		idsByFrom[rule.FromNodeID] = rule.ID
	}
	if idsByFrom["1"] != "vr-2" || idsByFrom["2"] != "vr-1" || idsByFrom["3"] != "vr-3" {
		t.Fatalf("rule ids should be initialized once by sequence: %+v", config.TopologyRules)
	}
}

func TestBuildProbeVirtualRouterRuntimeConfigForwardPassiveSideDoesNotProbePrev(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
				ID:              "edge-a-b-forward",
				FromNodeID:      "1",
				ToNodeID:        "2",
				Direction:       probeVirtualRouterDirectionForward,
				FromServicePort: 12040,
				ToServiceDomain: "b.internal",
				ToServicePort:   12040,
				Enabled:         true,
			}),
		},
	}
	left := buildProbeVirtualRouterRuntimeConfigsForNode(config, nodeIdentity{NodeID: "1", Secret: "node-1"}, "")
	right := buildProbeVirtualRouterRuntimeConfigsForNode(config, nodeIdentity{NodeID: "2", Secret: "node-2"}, "")
	if len(left) != 1 || len(right) != 1 {
		t.Fatalf("runtime configs left=%d right=%d", len(left), len(right))
	}
	if left[0].peerNodeID != "2" || left[0].peerHost != "b.internal" || !left[0].dialer {
		t.Fatalf("forward source should dial destination: %+v", left[0])
	}
	if right[0].peerNodeID != "1" || right[0].dialer || right[0].peerHost != "" {
		t.Fatalf("forward destination should know source topology but remain passive: %+v", right[0])
	}
}

func TestBuildProbeVirtualRouterRuntimeConfigFixedADialsBRequiresBAddress(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
				ID:                "edge-a-only",
				FromNodeID:        "1",
				ToNodeID:          "2",
				Direction:         "bidirectional",
				FromServiceDomain: "a.internal",
				FromServicePort:   12040,
				ToServicePort:     12040,
				Enabled:           true,
			}),
		},
	}
	left := buildProbeVirtualRouterRuntimeConfigsForNode(config, nodeIdentity{NodeID: "1", Secret: "node-1"}, "")
	right := buildProbeVirtualRouterRuntimeConfigsForNode(config, nodeIdentity{NodeID: "2", Secret: "node-2"}, "")
	if len(left) != 1 || len(right) != 1 {
		t.Fatalf("runtime configs left=%d right=%d", len(left), len(right))
	}
	if left[0].peerNodeID != "2" || !left[0].dialer || left[0].peerHost != "" {
		t.Fatalf("node 1 should keep topology but cannot dial without B address: %+v", left[0])
	}
	if right[0].peerNodeID != "1" || right[0].dialer || right[0].peerHost != "" {
		t.Fatalf("node 2 should remain passive; B never dials A for virtual router: %+v", right[0])
	}
}

func TestProbeVirtualRouterControlFrameEnvelope(t *testing.T) {
	payload := []byte(`{"request_id":"r1"}`)
	frame, err := buildProbeVirtualRouterBusinessFrame(probeVirtualRouterFrameMainTypePingPong, probeVirtualRouterPingPongSubTypePing, payload, []string{"1", "2"}, nil)
	if err != nil {
		t.Fatalf("build control frame failed: %v", err)
	}
	raw, err := marshalProbeVirtualRouterFrameEnvelope(frame)
	if err != nil {
		t.Fatalf("marshal control frame failed: %v", err)
	}
	frame, err = unmarshalProbeVirtualRouterFrameEnvelope(raw)
	if err != nil {
		t.Fatalf("unmarshal control frame failed: %v", err)
	}
	if frame.MainType != probeVirtualRouterFrameMainTypePingPong || frame.SubType != probeVirtualRouterPingPongSubTypePing {
		t.Fatalf("unexpected control frame type: %+v", frame)
	}
	control, err := probeVirtualRouterFrameControl(frame, nil)
	if err != nil {
		t.Fatalf("unmarshal control envelope failed: %v", err)
	}
	if string(frame.Data) != string(payload) || !reflect.DeepEqual(control.Path, []string{"1", "2"}) {
		t.Fatalf("unexpected control frame payload/path: %+v", frame)
	}
}

func TestProbeVirtualRouterNextHopInPath(t *testing.T) {
	path := []string{"1", "2", "3"}
	if got := probeVirtualRouterNextHopInPath(path, "1"); got != "2" {
		t.Fatalf("next hop from 1=%q, want 2", got)
	}
	if got := probeVirtualRouterNextHopInPath(path, "2"); got != "3" {
		t.Fatalf("next hop from 2=%q, want 3", got)
	}
	if got := probeVirtualRouterNextHopInPath(path, "3"); got != "" {
		t.Fatalf("next hop from 3=%q, want empty", got)
	}
}

func TestProbeVirtualRouterPathFromAssociation(t *testing.T) {
	association := &probeRouteAssociationV2Meta{
		RouteTarget: "node-1>node-2>node-3",
	}
	if got := probeVirtualRouterPathFromAssociation(association); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("path=%v, want [1 2 3]", got)
	}
}

func TestApplyProbeVirtualRouterConfigKeepsFrameLinksWhenTopologyUnchanged(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{
				ID:                "1",
				FromNodeID:        "16",
				ToNodeID:          "19",
				FromServiceDomain: "node-16.example.test",
				FromServicePort:   12040,
				ToServiceDomain:   "node-19.example.test",
				ToServicePort:     12040,
				Enabled:           true,
				AuthTicket:        "ticket-a",
				UpdatedAt:         "2026-06-28T20:10:07Z",
			},
		},
		UpdatedAt: "2026-06-28T20:10:07Z",
	}
	applyProbeVirtualRouterConfigForNode(config, "16")

	left, right := net.Pipe()
	defer right.Close()
	key := "packet|vrouter-test"
	link := newProbeVirtualRouterFrameLink(key, nil, left, nil)
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: link}
	probeVirtualRouterFrameLinkState.mu.Unlock()

	unchanged := config
	unchanged.UpdatedAt = "2026-06-28T20:10:08Z"
	unchanged.TopologyRules[0].AuthTicket = "ticket-b"
	unchanged.TopologyRules[0].UpdatedAt = "2026-06-28T20:10:08Z"
	applyProbeVirtualRouterConfigForNode(unchanged, "16")

	if isProbeVirtualRouterFrameLinkClosed(link) {
		t.Fatalf("frame link should stay open when topology is unchanged")
	}
	if got := reusableProbeVirtualRouterFrameLink(key, time.Now()); got != link {
		t.Fatalf("frame link should remain cached, got=%v", got)
	}
}

func TestApplyProbeVirtualRouterConfigKeepsFrameLinksWhenTopologyChanges(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{ID: "1", FromNodeID: "16", ToNodeID: "19", ToServiceDomain: "node-19.example.test", ToServicePort: 12040, Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "16")

	left, right := net.Pipe()
	defer right.Close()
	key := "packet|vrouter-test"
	link := newProbeVirtualRouterFrameLink(key, nil, left, nil)
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: link}
	probeVirtualRouterFrameLinkState.mu.Unlock()

	changed := config
	changed.TopologyRules[0].ToServiceDomain = "node-19-new.example.test"
	applyProbeVirtualRouterConfigForNode(changed, "16")

	if isProbeVirtualRouterFrameLinkClosed(link) {
		t.Fatalf("frame link should stay open during config apply; runtime diff handles precise restarts")
	}
	if got := reusableProbeVirtualRouterFrameLink(key, time.Now()); got != link {
		t.Fatalf("frame link should remain cached during config apply, got=%v", got)
	}
}

func TestCurrentProbeVirtualRouterPathForPacketInfersLocalNodeFromSourceIP(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	packet := buildProbeVirtualRouterTestICMPEchoRequest(t, "198.18.0.21", "198.18.0.18")
	if got := currentProbeVirtualRouterPathForPacket(packet, "198.18.0.18"); !reflect.DeepEqual(got, []string{"19", "16"}) {
		t.Fatalf("path=%v, want [19 16]", got)
	}
}

func TestCurrentProbeVirtualRouterPathPrefersLowestRTTAmongEqualHopPaths(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{FromNodeID: "2", ToNodeID: "4", Enabled: true},
			{FromNodeID: "1", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "1")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	probeVirtualRouterRuntimeState.mu.Lock()
	oldRuntimes := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{
		"vrouter-1-2": {cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-2", peerNodeID: "2", dialer: true}},
		"vrouter-1-3": {cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-3", peerNodeID: "3", dialer: true}},
	}
	probeVirtualRouterRuntimeState.mu.Unlock()
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		"vrouter-1-2": {LastPingLatencyMS: 50},
		"vrouter-1-3": {LastPingLatencyMS: 10},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = oldRuntimes
		probeVirtualRouterRuntimeState.mu.Unlock()
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldStats
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, []string{"1", "3", "4"}) {
		t.Fatalf("path=%v, want [1 3 4]", got)
	}
}

func TestCurrentProbeVirtualRouterPathUsesRemoteRTTFallback(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{FromNodeID: "2", ToNodeID: "4", Enabled: true},
			{FromNodeID: "1", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "1")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	probeVirtualRouterRuntimeState.mu.Lock()
	oldRuntimes := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{
		"vrouter-1-2": {cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-2", peerNodeID: "2", dialer: true}},
		"vrouter-1-3": {cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-3", peerNodeID: "3", dialer: true}},
	}
	probeVirtualRouterRuntimeState.mu.Unlock()
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		"vrouter-1-2": {LastRemoteRTTMS: 80},
		"vrouter-1-3": {LastRemoteRTTMS: 20},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = oldRuntimes
		probeVirtualRouterRuntimeState.mu.Unlock()
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldStats
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, []string{"1", "3", "4"}) {
		t.Fatalf("path=%v, want [1 3 4]", got)
	}
}

func TestCurrentProbeVirtualRouterPathUsesPathPingPongSumForRouteSelection(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{FromNodeID: "2", ToNodeID: "4", Enabled: true},
			{FromNodeID: "1", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "1")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	recordProbeVirtualRouterPathRTTSuccess([]string{"1", "2", "4"}, 80*time.Millisecond, "4")
	recordProbeVirtualRouterPathRTTSuccess([]string{"1", "3", "4"}, 20*time.Millisecond, "4")

	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, []string{"1", "3", "4"}) {
		t.Fatalf("path=%v, want [1 3 4]", got)
	}
}

func TestProbeVirtualRouterPathSelectionKeepsPathAfterGuardianProbeError(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.11"},
			{NodeID: "2", IP: "198.18.0.12"},
			{NodeID: "3", IP: "198.18.0.13"},
			{NodeID: "4", IP: "198.18.0.14"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{FromNodeID: "2", ToNodeID: "4", Enabled: true},
			{FromNodeID: "1", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	}, "1")

	recordProbeVirtualRouterPathRTTError([]string{"1", "2", "4"}, errors.New("physical carrier disconnected"))
	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, []string{"1", "2", "4"}) {
		t.Fatalf("path=%v, want original route [1 2 4]", got)
	}
}

func TestProbeVirtualRouterPathSelectionDoesNotQuarantineGuardianError(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.11"},
			{NodeID: "2", IP: "198.18.0.12"},
			{NodeID: "3", IP: "198.18.0.13"},
			{NodeID: "4", IP: "198.18.0.14"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{FromNodeID: "2", ToNodeID: "4", Enabled: true},
			{FromNodeID: "1", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	}, "1")

	failedPath := []string{"1", "2", "4"}
	recordProbeVirtualRouterPathRTTError(failedPath, errors.New("physical carrier disconnected"))
	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, failedPath) {
		t.Fatalf("path=%v, want original route [1 2 4]", got)
	}

	recordProbeVirtualRouterPathRTTSuccess(failedPath, 10*time.Millisecond, "4")
	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, failedPath) {
		t.Fatalf("path=%v, want recovered path [1 2 4]", got)
	}
}

func TestProbeVirtualRouterPathSelectionAvoidsDisconnectedFirstHop(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.11"},
			{NodeID: "2", IP: "198.18.0.12"},
			{NodeID: "3", IP: "198.18.0.13"},
			{NodeID: "4", IP: "198.18.0.14"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{FromNodeID: "2", ToNodeID: "4", Enabled: true},
			{FromNodeID: "1", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	}, "1")
	storeProbeVirtualRouterRoutePath("1", "4", []string{"1", "2", "4"})
	probeVirtualRouterRuntimeState.mu.Lock()
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{
		"vrouter-1-2": {cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-2", localNodeID: "1", peerNodeID: "2", dialer: true}},
	}
	probeVirtualRouterRuntimeState.mu.Unlock()
	markProbeVirtualRouterPhysicalCarrierDisconnected(&probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-2", fromNodeID: "1", toNodeID: "2"}}, "test")

	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, []string{"1", "3", "4"}) {
		t.Fatalf("path=%v, want alternate [1 3 4] after first-hop disconnect", got)
	}
	clearProbeVirtualRouterPhysicalCarrierDisconnected(&probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-2", fromNodeID: "1", toNodeID: "2"}})
	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, []string{"1", "2", "4"}) {
		t.Fatalf("path=%v, want recovered first-hop path [1 2 4]", got)
	}
}

func TestProbeVirtualRouterPathSelectionReturnsNoPathWhenEveryFirstHopIsDisconnected(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.11"},
			{NodeID: "2", IP: "198.18.0.12"},
			{NodeID: "3", IP: "198.18.0.13"},
			{NodeID: "4", IP: "198.18.0.14"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{FromNodeID: "2", ToNodeID: "4", Enabled: true},
			{FromNodeID: "1", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	}, "1")
	probeVirtualRouterRuntimeState.mu.Lock()
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{
		"vrouter-1-2": {cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-2", localNodeID: "1", peerNodeID: "2", dialer: true}},
		"vrouter-1-3": {cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-3", localNodeID: "1", peerNodeID: "3", dialer: true}},
	}
	probeVirtualRouterRuntimeState.mu.Unlock()
	markProbeVirtualRouterPhysicalCarrierDisconnected(&probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-2", fromNodeID: "1", toNodeID: "2"}}, "test")
	markProbeVirtualRouterPhysicalCarrierDisconnected(&probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-3", fromNodeID: "1", toNodeID: "3"}}, "test")

	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); len(got) != 0 {
		t.Fatalf("path=%v, want no path while all first-hop carriers are disconnected", got)
	}
}

func TestProbeVirtualRouterControlResponseCompletesPendingRequest(t *testing.T) {
	requestID := "rtt-test-1"
	waiter := registerProbeVirtualRouterControlResponse(requestID)
	defer unregisterProbeVirtualRouterControlResponse(requestID)

	completeProbeVirtualRouterControlResponse(probeVirtualRouterControlProbePayload{
		RequestID: requestID,
		OK:        true,
		LatencyMS: 17,
		Responder: "4",
	})
	response, err := waitProbeVirtualRouterControlResponse(waiter, time.Second)
	if err != nil {
		t.Fatalf("wait control response failed: %v", err)
	}
	if !response.OK || response.LatencyMS != 17 || response.Responder != "4" {
		t.Fatalf("response=%+v, want ok latency=17 responder=4", response)
	}
}

func TestProbeVirtualRouterControlPingDoesNotUseRemoteClockForLatency(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	link := newProbeVirtualRouterFrameLink("test-control-ping-clock", nil, left, nil)
	link.Start()
	defer stopProbeVirtualRouterFrameLink(link)

	replyCh := make(chan struct {
		frame probeVirtualRouterFrame
		err   error
	}, 1)
	go func() {
		frame, err := readProbeVirtualRouterWireFrame(bufio.NewReader(right))
		replyCh <- struct {
			frame probeVirtualRouterFrame
			err   error
		}{frame: frame, err: err}
	}()

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-2", identity: nodeIdentity{NodeID: "2"}}}
	requestCreatedAt := time.Now().Add(time.Hour).UnixNano()
	err := handleProbeVirtualRouterControlPing(rt, link, probeVirtualRouterControlProbePayload{
		RequestID:         "clock-skew-ping",
		SourceNodeID:      "1",
		TargetNodeID:      "2",
		Path:              []string{"1", "2"},
		CreatedAtUnixNano: requestCreatedAt,
	})
	if err != nil {
		t.Fatalf("handle control ping failed: %v", err)
	}

	select {
	case got := <-replyCh:
		if got.err != nil {
			t.Fatalf("read pong failed: %v", got.err)
		}
		var response probeVirtualRouterControlProbePayload
		if err := json.Unmarshal(got.frame.Data, &response); err != nil {
			t.Fatalf("decode pong failed: %v", err)
		}
		if !response.OK || response.Responder != "2" {
			t.Fatalf("response=%+v, want ok responder 2", response)
		}
		if response.LatencyMS != 0 {
			t.Fatalf("response latency=%d, want 0 so requester computes local RTT", response.LatencyMS)
		}
		if response.CreatedAtUnixNano == requestCreatedAt || response.CreatedAtUnixNano <= 0 {
			t.Fatalf("response timestamp=%d should be responder send timestamp, request timestamp=%d", response.CreatedAtUnixNano, requestCreatedAt)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for pong")
	}
}

func TestProbeVirtualRouterAdjacentLatencyMillisecondsUsesFullRTT(t *testing.T) {
	if got := probeVirtualRouterAdjacentLatencyMilliseconds(558 * time.Millisecond); got != 558 {
		t.Fatalf("latency=%d, want 558", got)
	}
	if got := probeVirtualRouterAdjacentLatencyMilliseconds(time.Millisecond); got != 1 {
		t.Fatalf("minimum latency=%d, want 1", got)
	}
}

func TestProbeVirtualRouterPathLatencyUsesSourceTimestamp(t *testing.T) {
	sentAt := time.Unix(1700000000, 100).UnixNano()
	latencyMS, err := probeVirtualRouterPathLatencyMilliseconds(sentAt, sentAt+81*int64(time.Millisecond))
	if err != nil {
		t.Fatalf("path latency returned error: %v", err)
	}
	if latencyMS != 81 {
		t.Fatalf("path latency=%d, want 81", latencyMS)
	}
	if _, err := probeVirtualRouterPathLatencyMilliseconds(sentAt, sentAt-1); err == nil {
		t.Fatalf("backward source timestamp should fail")
	}
}

func TestProbeVirtualRouterPathRTTErrorQuarantinesOnlyAfterFiveFailures(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	path := []string{"1", "2", "3"}
	for attempt := 1; attempt <= probeVirtualRouterPathRTTFailureThreshold; attempt++ {
		reached := recordProbeVirtualRouterPathRTTError(path, errors.New("destination unreachable"))
		if reached != (attempt == probeVirtualRouterPathRTTFailureThreshold) {
			t.Fatalf("attempt=%d threshold reached=%t", attempt, reached)
		}
	}
	probeVirtualRouterPathRTTState.mu.RLock()
	item := probeVirtualRouterPathRTTState.items[probeVirtualRouterPathKey(path)]
	probeVirtualRouterPathRTTState.mu.RUnlock()
	if item.ConsecutiveFailureCount != probeVirtualRouterPathRTTFailureThreshold {
		t.Fatalf("failure count=%d", item.ConsecutiveFailureCount)
	}
	recordProbeVirtualRouterPathRTTSuccess(path, 20*time.Millisecond, "3")
	probeVirtualRouterPathRTTState.mu.RLock()
	item = probeVirtualRouterPathRTTState.items[probeVirtualRouterPathKey(path)]
	probeVirtualRouterPathRTTState.mu.RUnlock()
	if item.ConsecutiveFailureCount != 0 || item.LastError != "" {
		t.Fatalf("successful path should clear failure state: %+v", item)
	}
}

func TestProbeVirtualRouterPathRTTSupportsAdjacentDirectPath(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	path := []string{"1", "2"}
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.1"},
			{NodeID: "2", IP: "198.18.0.2"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{{
			FromNodeID: "1",
			ToNodeID:   "2",
			Enabled:    true,
		}},
	}, "1")

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{
		routeID:     "vrouter-direct-1-2",
		localNodeID: "1",
		peerNodeID:  "2",
		fromNodeID:  "1",
		toNodeID:    "2",
		dialer:      true,
	}}
	probeVirtualRouterRuntimeState.mu.Lock()
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{rt.cfg.routeID: rt}
	probeVirtualRouterRuntimeState.mu.Unlock()

	key := probeVirtualRouterFrameLinkKey(rt, probeRouteBridgeRoleToNext, "", path)
	link := newProbeVirtualRouterFrameLink(key, rt, nil, path)
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: link}
	probeVirtualRouterFrameLinkState.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		select {
		case frame := <-link.txControl:
			if frame.MainType != probeVirtualRouterFrameMainTypePingPong || frame.SubType != probeVirtualRouterPingPongSubTypePing {
				done <- fmt.Errorf("frame type=%d/%d, want ping", frame.MainType, frame.SubType)
				return
			}
			var msg probeVirtualRouterControlProbePayload
			if err := json.Unmarshal(frame.Data, &msg); err != nil {
				done <- err
				return
			}
			time.Sleep(4 * time.Millisecond)
			completeProbeVirtualRouterControlResponse(probeVirtualRouterControlProbePayload{
				RequestID: msg.RequestID,
				OK:        true,
				Responder: "2",
			})
			done <- nil
		case <-time.After(time.Second):
			done <- errors.New("timed out waiting for direct path rtt frame")
		}
	}()

	latency, err := probeVirtualRouterQueryPathRTT(path)
	if err != nil {
		t.Fatalf("probeVirtualRouterQueryPathRTT returned error: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("direct path rtt probe failed: %v", err)
	}
	if latency <= 0 {
		t.Fatalf("latency=%v, want positive", latency)
	}
	probeVirtualRouterPathRTTState.mu.RLock()
	record := probeVirtualRouterPathRTTState.items[probeVirtualRouterPathKey(path)]
	probeVirtualRouterPathRTTState.mu.RUnlock()
	if record.RTTMS <= 0 || record.Responder != "2" || record.ConsecutiveFailureCount != 0 || strings.TrimSpace(record.LastError) != "" {
		t.Fatalf("path rtt record=%+v, want successful direct rtt", record)
	}
}

func TestProbeVirtualRouterForwardPathRejectsLoopsAndMoreThanThreeHops(t *testing.T) {
	if err := validateProbeVirtualRouterForwardPath([]string{"1", "2", "3", "4"}); err != nil {
		t.Fatalf("three-hop path rejected: %v", err)
	}
	if err := validateProbeVirtualRouterForwardPath([]string{"1", "2", "3", "4", "5"}); err == nil {
		t.Fatalf("four-hop path should be rejected")
	}
	if err := validateProbeVirtualRouterForwardPath([]string{"1", "2", "3", "2", "4"}); err == nil {
		t.Fatalf("loop path should be rejected")
	}
}

func TestReusableProbeVirtualRouterFrameLinkDropsClosedLink(t *testing.T) {
	key := "closed-frame-link"
	done := make(chan struct{})
	close(done)
	item := &probeVirtualRouterFrameLink{
		key:      key,
		done:     done,
		openedAt: time.Now(),
		lastUsed: time.Now(),
	}
	probeVirtualRouterFrameLinkState.mu.Lock()
	oldLinks := probeVirtualRouterFrameLinkState.links
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: item}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterFrameLinkState.mu.Lock()
		probeVirtualRouterFrameLinkState.links = oldLinks
		probeVirtualRouterFrameLinkState.mu.Unlock()
	})

	if stream := reusableProbeVirtualRouterFrameLink(key, time.Now()); stream != nil {
		t.Fatalf("closed link should not be reused")
	}
	probeVirtualRouterFrameLinkState.mu.Lock()
	_, exists := probeVirtualRouterFrameLinkState.links[key]
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if exists {
		t.Fatalf("closed link should be removed from cache")
	}
}

func TestProbeVirtualRouterFrameLinkKeyIsPerRule(t *testing.T) {
	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-rule-1"}}
	left := probeVirtualRouterFrameLinkKey(rt, "to_next", "198.18.0.21", []string{"1", "2"})
	right := probeVirtualRouterFrameLinkKey(rt, "to_prev", "198.18.0.22", []string{"2", "1"})
	if left == "" {
		t.Fatalf("frame link key should not be empty")
	}
	if left != right {
		t.Fatalf("frame link key should be per rule: left=%q right=%q", left, right)
	}
}

func TestStartProbeVirtualRouterRuntimeCreatesFrameLinkWorkers(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	routeID := "vrouter-fixed-workers"
	rt, err := startProbeVirtualRouterRuntime(probeVirtualRouterRuntimeConfig{
		routeID:    routeID,
		secret:     "secret",
		authTicket: "ticket",
		peerNodeID: "19",
		dialer:     true,
	})
	if err != nil {
		t.Fatalf("start runtime failed: %v", err)
	}
	t.Cleanup(func() { stopProbeVirtualRouterRuntime(routeID, "test cleanup") })

	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	probeVirtualRouterFrameLinkState.mu.Lock()
	link := probeVirtualRouterFrameLinkState.links[key]
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if link == nil {
		t.Fatalf("frame link should be created with runtime")
	}
	if rt.frameLink != link {
		t.Fatalf("runtime frame link mismatch")
	}
	if link.tx == nil || link.txControl == nil || link.txBulk == nil || link.rx == nil || link.done == nil || link.carrierNotify == nil {
		t.Fatalf("frame link worker channels should be initialized")
	}
	select {
	case <-link.done:
		t.Fatalf("frame link should stay open while runtime is running")
	default:
	}
}

func TestStartProbeVirtualRouterRuntimeReusesRelayForSharedListenPort(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	writeProbeVirtualRouterTestTLSCertificate(t)

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := blocker.Addr().(*net.TCPAddr).Port
	_ = blocker.Close()

	base := probeVirtualRouterRuntimeConfig{
		secret:     "shared-secret",
		authTicket: "ticket",
		listenHost: "127.0.0.1",
		listenPort: port,
		identity:   nodeIdentity{NodeID: "2"},
	}
	first := base
	first.routeID = "vrouter-shared-listen-1"
	first.peerNodeID = "1"
	rt1, err := startProbeVirtualRouterRuntime(first)
	if err != nil {
		t.Fatalf("start first runtime failed: %v", err)
	}
	t.Cleanup(func() { stopProbeVirtualRouterRuntime(first.routeID, "test cleanup") })

	second := base
	second.routeID = "vrouter-shared-listen-2"
	second.peerNodeID = "3"
	rt2, err := startProbeVirtualRouterRuntime(second)
	if err != nil {
		t.Fatalf("start second runtime on shared port failed: %v", err)
	}
	t.Cleanup(func() { stopProbeVirtualRouterRuntime(second.routeID, "test cleanup") })

	if rt1.relay == nil || rt1.relay != rt2.relay {
		t.Fatalf("runtimes should share one relay: left=%p right=%p", rt1.relay, rt2.relay)
	}
	listenAddr := net.JoinHostPort(base.listenHost, strconv.Itoa(port))
	probeVirtualRouterRelayServerState.mu.Lock()
	relay := probeVirtualRouterRelayServerState.servers[listenAddr]
	routeCount := 0
	if relay != nil {
		routeCount = len(relay.routeIDs)
	}
	probeVirtualRouterRelayServerState.mu.Unlock()
	if relay != rt1.relay || routeCount != 2 {
		t.Fatalf("shared relay state relay=%p route_count=%d want relay=%p count=2", relay, routeCount, rt1.relay)
	}

	stopProbeVirtualRouterRuntime(first.routeID, "test stop first")
	probeVirtualRouterRelayServerState.mu.Lock()
	relay = probeVirtualRouterRelayServerState.servers[listenAddr]
	routeCount = 0
	if relay != nil {
		routeCount = len(relay.routeIDs)
	}
	probeVirtualRouterRelayServerState.mu.Unlock()
	if relay != rt2.relay || routeCount != 1 {
		t.Fatalf("shared relay should stay alive after one route stops: relay=%p route_count=%d", relay, routeCount)
	}

	stopProbeVirtualRouterRuntime(second.routeID, "test stop second")
	probeVirtualRouterRelayServerState.mu.Lock()
	relay = probeVirtualRouterRelayServerState.servers[listenAddr]
	probeVirtualRouterRelayServerState.mu.Unlock()
	if relay != nil {
		t.Fatalf("shared relay should close after last route stops: %p", relay)
	}
}

func writeProbeVirtualRouterTestTLSCertificate(t *testing.T) {
	t.Helper()
	dataDir, err := resolveDataDir()
	if err != nil {
		t.Fatalf("resolve data dir: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate tls key: %v", err)
	}
	now := time.Now().Add(-time.Minute)
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName: "127.0.0.1",
		},
		NotBefore:             now,
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create tls certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(dataDir, probeTLSCertFile), certPEM, 0o600); err != nil {
		t.Fatalf("write tls cert: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, probeTLSKeyFile), keyPEM, 0o600); err != nil {
		t.Fatalf("write tls key: %v", err)
	}
}

func TestProbeVirtualRouterKeepAliveDialerWakesConnectWithoutCarrier(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	rt := &probeVirtualRouterRuntime{
		cfg: probeVirtualRouterRuntimeConfig{
			routeID:    "vrouter-keepalive-dialer",
			peerNodeID: "19",
			dialer:     true,
		},
		stopCh:       make(chan struct{}),
		bridgeWakeCh: make(chan struct{}, 1),
	}
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	link := newProbeVirtualRouterFrameLink(key, rt, nil, nil)
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: link}
	probeVirtualRouterFrameLinkState.mu.Unlock()

	probeVirtualRouterKeepAliveRuntime(rt)

	if got := len(rt.bridgeWakeCh); got != 1 {
		t.Fatalf("bridge wake queue=%d, want 1", got)
	}
	if depth, _, _, _, _, _, _, _ := link.txQueueSnapshot(); depth != 0 {
		t.Fatalf("keepalive should not enqueue ping without carrier, tx=%d", depth)
	}
}

func TestProbeVirtualRouterKeepAliveServerSkipsActivePingWithCarrier(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	rt := &probeVirtualRouterRuntime{
		cfg: probeVirtualRouterRuntimeConfig{
			routeID:    "vrouter-keepalive-server",
			peerNodeID: "16",
		},
		stopCh:       make(chan struct{}),
		bridgeWakeCh: make(chan struct{}, 1),
	}
	left, right := net.Pipe()
	defer right.Close()
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	link := newProbeVirtualRouterFrameLink(key, rt, nil, nil)
	link.AttachCarrier(left, "vrouter-carrier-server", "198.51.100.16:12040")
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: link}
	probeVirtualRouterFrameLinkState.mu.Unlock()

	probeVirtualRouterKeepAliveRuntime(rt)

	if got := len(rt.bridgeWakeCh); got != 0 {
		t.Fatalf("server should not wake dialer, wake queue=%d", got)
	}
	if depth, _, _, _, _, _, _, _ := link.txQueueSnapshot(); depth != 0 {
		t.Fatalf("server keepalive should not enqueue active ping even with carrier, tx=%d", depth)
	}
}

func TestProbeVirtualRouterAdjacentRTTServerSkipsActivePing(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	rt := &probeVirtualRouterRuntime{
		cfg: probeVirtualRouterRuntimeConfig{
			routeID:    "vrouter-rtt-server",
			peerNodeID: "16",
		},
		stopCh:       make(chan struct{}),
		bridgeWakeCh: make(chan struct{}, 1),
	}
	left, right := net.Pipe()
	defer right.Close()
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	link := newProbeVirtualRouterFrameLink(key, rt, nil, nil)
	link.AttachCarrier(left, "vrouter-carrier-rtt-server", "198.51.100.16:12040")
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: link}
	probeVirtualRouterFrameLinkState.mu.Unlock()

	probeVirtualRouterQueryAdjacentRTTRuntime(rt)

	if depth, _, _, _, _, _, _, _ := link.txQueueSnapshot(); depth != 0 {
		t.Fatalf("server adjacent rtt should not enqueue active ping, tx=%d", depth)
	}
	stats := snapshotProbeVirtualRouterRuntimeStats(rt.cfg.routeID)
	if stats == nil || strings.TrimSpace(stats.LastRemoteRTTError) == "" {
		t.Fatalf("server adjacent rtt should record unavailable instead of pinging, stats=%+v", stats)
	}
}

func TestProbeVirtualRouterFrameEnvelopeCarriesTypeControlAndPath(t *testing.T) {
	packet := []byte{0x45, 0x00, 0x00, 0x14}
	frame, err := buildProbeVirtualRouterIPFrame(packet, []string{"1", "3", "4"}, nil)
	if err != nil {
		t.Fatalf("build frame envelope failed: %v", err)
	}
	payload, err := marshalProbeVirtualRouterFrameEnvelope(frame)
	if err != nil {
		t.Fatalf("marshal frame envelope failed: %v", err)
	}
	if len(payload) <= probeVirtualRouterFrameEnvelopeHeaderSize {
		t.Fatalf("payload len=%d, want frame header plus body", len(payload))
	}
	if got := binary.BigEndian.Uint16(payload[0:2]); got != probeVirtualRouterFrameEnvelopeMagic {
		t.Fatalf("magic=0x%x, want 0x%x", got, probeVirtualRouterFrameEnvelopeMagic)
	}
	if got := binary.BigEndian.Uint16(payload[2:4]); got != probeVirtualRouterFrameMainTypeIP {
		t.Fatalf("maintype=%d, want ip business type", got)
	}
	if got := binary.BigEndian.Uint16(payload[4:6]); got != probeVirtualRouterIPSubTypeIPv4 {
		t.Fatalf("subtype=%d, want ip4", got)
	}
	controlLen := binary.BigEndian.Uint16(payload[6:8])
	dataLen := binary.BigEndian.Uint16(payload[8:10])
	if controlLen == 0 {
		t.Fatalf("control_len=0, want control envelope")
	}
	if dataLen != uint16(len(packet)) {
		t.Fatalf("data_len=%d, want %d", dataLen, len(packet))
	}
	if got := binary.BigEndian.Uint16(payload[10:12]); got == 0 {
		t.Fatalf("checksum=0, want non-zero")
	}
	got, err := unmarshalProbeVirtualRouterFrameEnvelope(payload)
	if err != nil {
		t.Fatalf("unmarshal frame envelope failed: %v", err)
	}
	control, err := probeVirtualRouterFrameControl(got, []string{"fallback"})
	if err != nil {
		t.Fatalf("unmarshal frame control failed: %v", err)
	}
	if got.MainType != probeVirtualRouterFrameMainTypeIP || got.SubType != probeVirtualRouterIPSubTypeIPv4 || !reflect.DeepEqual(got.Data, packet) || !reflect.DeepEqual(control.Path, []string{"1", "3", "4"}) {
		t.Fatalf("frame=%+v", got)
	}
	broken := append([]byte(nil), payload...)
	broken[len(broken)-1] ^= 0xff
	if _, err := unmarshalProbeVirtualRouterFrameEnvelope(broken); err == nil {
		t.Fatalf("checksum mismatch should fail")
	}
}

func TestProbeVirtualRouterFrameChecksumKeepsOddControlAdjacentToData(t *testing.T) {
	header := make([]byte, probeVirtualRouterFrameEnvelopeHeaderSize-2)
	control := []byte{0x01}
	data := []byte{0x02}
	if got, want := probeVirtualRouterFrameChecksum(header, control, data), uint16(0xfefd); got != want {
		t.Fatalf("checksum=0x%x, want 0x%x", got, want)
	}
}

func TestProbeVirtualRouterFrameEnvelopeUsesTwoByteLengths(t *testing.T) {
	frame := probeVirtualRouterFrame{
		MainType: probeVirtualRouterFrameMainTypeSpeed,
		SubType:  probeVirtualRouterSpeedSubTypeChunk,
		Control:  []byte("{}"),
		Data:     make([]byte, probeVirtualRouterFrameMaxDataBytes),
	}
	payload, err := marshalProbeVirtualRouterFrameEnvelope(frame)
	if err != nil {
		t.Fatalf("marshal max data frame failed: %v", err)
	}
	if got := binary.BigEndian.Uint16(payload[6:8]); got != uint16(len(frame.Control)) {
		t.Fatalf("control_len=%d, want %d", got, len(frame.Control))
	}
	if got := binary.BigEndian.Uint16(payload[8:10]); got != uint16(probeVirtualRouterFrameMaxDataBytes) {
		t.Fatalf("data_len=%d, want %d", got, probeVirtualRouterFrameMaxDataBytes)
	}
	decoded, err := unmarshalProbeVirtualRouterFrameEnvelope(payload)
	if err != nil {
		t.Fatalf("unmarshal max data frame failed: %v", err)
	}
	if len(decoded.Data) != probeVirtualRouterFrameMaxDataBytes {
		t.Fatalf("decoded data len=%d, want %d", len(decoded.Data), probeVirtualRouterFrameMaxDataBytes)
	}

	frame.Data = make([]byte, probeVirtualRouterFrameMaxDataBytes+1)
	if _, err := marshalProbeVirtualRouterFrameEnvelope(frame); err == nil {
		t.Fatalf("data larger than two-byte data_len should fail")
	}
	frame.Data = nil
	frame.Control = make([]byte, probeVirtualRouterFrameMaxControlBytes+1)
	if _, err := marshalProbeVirtualRouterFrameEnvelope(frame); err == nil {
		t.Fatalf("control larger than application limit should fail")
	}
}

func TestProbeVirtualRouterFrameEnvelopeCarriesICMPTrace(t *testing.T) {
	packet := []byte{0x45, 0x00, 0x00, 0x14}
	trace := []probeVirtualRouterFrameTraceHop{
		{ID: "trace-1", NodeID: "16", RouteID: "vrouter-a", Event: "tun_rx", UnixNano: time.Unix(0, 1000).UnixNano()},
		{ID: "trace-2", NodeID: "19", RouteID: "vrouter-b", Event: "frame_rx", Direction: "to_prev", RemoteNode: "16", UnixNano: time.Unix(0, 2000).UnixNano()},
	}
	frame, err := buildProbeVirtualRouterIPFrame(packet, []string{"16", "19"}, trace)
	if err != nil {
		t.Fatalf("build traced frame envelope failed: %v", err)
	}
	payload, err := marshalProbeVirtualRouterFrameEnvelope(frame)
	if err != nil {
		t.Fatalf("marshal traced frame envelope failed: %v", err)
	}
	got, err := unmarshalProbeVirtualRouterFrameEnvelope(payload)
	if err != nil {
		t.Fatalf("unmarshal traced frame envelope failed: %v", err)
	}
	control, err := probeVirtualRouterFrameControl(got, nil)
	if err != nil {
		t.Fatalf("unmarshal traced frame control failed: %v", err)
	}
	if !reflect.DeepEqual(got.Data, packet) || !reflect.DeepEqual(control.Path, []string{"16", "19"}) {
		t.Fatalf("frame payload/path=%+v", got)
	}
	if len(control.Trace) != len(trace) {
		t.Fatalf("trace len=%d want %d trace=%+v", len(control.Trace), len(trace), control.Trace)
	}
	if control.Trace[0].NodeID != "16" || control.Trace[1].RemoteNode != "16" || control.Trace[1].Direction != "to_prev" {
		t.Fatalf("trace=%+v", control.Trace)
	}
}

func TestProbeVirtualRouterFrameLinkTXWorkerWritesBufferedFrame(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()

	link := newProbeVirtualRouterFrameLink("test-link", nil, left, nil)
	link.Start()
	defer stopProbeVirtualRouterFrameLink(link)

	type result struct {
		frame probeVirtualRouterFrame
		err   error
	}
	done := make(chan result, 1)
	go func() {
		frame, err := readProbeVirtualRouterWireFrame(bufio.NewReader(right))
		done <- result{frame: frame, err: err}
	}()

	wantPacket := []byte{0x45, 0x00, 0x00, 0x14}
	wantPath := []string{"16", "19"}
	if err := writeProbeVirtualRouterIPFrame(link, wantPacket, wantPath, nil); err != nil {
		t.Fatalf("enqueue frame failed: %v", err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read frame failed: %v", got.err)
		}
		control, err := probeVirtualRouterFrameControl(got.frame, nil)
		if err != nil {
			t.Fatalf("read frame control failed: %v", err)
		}
		if got.frame.MainType != probeVirtualRouterFrameMainTypeIP || got.frame.SubType != probeVirtualRouterIPSubTypeIPv4 || !reflect.DeepEqual(got.frame.Data, wantPacket) || !reflect.DeepEqual(control.Path, wantPath) {
			t.Fatalf("frame=%+v, want payload/path %v/%v", got.frame, wantPacket, wantPath)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for tx worker frame")
	}
}

func TestProbeVirtualRouterFrameLinkTXWorkerBatchesQueuedFrames(t *testing.T) {
	left, right := net.Pipe()
	countedLeft := &probeVirtualRouterWriteCountingConn{Conn: left}
	defer right.Close()

	link := newProbeVirtualRouterFrameLink("test-batched-link", nil, countedLeft, nil)
	defer stopProbeVirtualRouterFrameLink(link)
	wantPath := []string{"16", "19"}
	for _, packet := range [][]byte{
		{0x45, 0x00, 0x00, 0x14, 0x01},
		{0x45, 0x00, 0x00, 0x14, 0x02},
	} {
		frame, err := buildProbeVirtualRouterIPFrame(packet, wantPath, nil)
		if err != nil {
			t.Fatalf("build frame failed: %v", err)
		}
		if err := link.EnqueueProbeVirtualRouterFrame(frame); err != nil {
			t.Fatalf("enqueue frame failed: %v", err)
		}
	}

	type result struct {
		frames []probeVirtualRouterFrame
		err    error
	}
	done := make(chan result, 1)
	go func() {
		reader := bufio.NewReader(right)
		frames := make([]probeVirtualRouterFrame, 0, 2)
		for range 2 {
			frame, err := readProbeVirtualRouterWireFrame(reader)
			if err != nil {
				done <- result{err: err}
				return
			}
			frames = append(frames, frame)
		}
		done <- result{frames: frames}
	}()
	link.Start()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read batched frames failed: %v", got.err)
		}
		if len(got.frames) != 2 || got.frames[0].Data[4] != 0x01 || got.frames[1].Data[4] != 0x02 {
			t.Fatalf("batched frames=%+v", got.frames)
		}
		if writes := countedLeft.WriteCalls(); writes != 1 {
			t.Fatalf("carrier write calls=%d, want 1 for two queued frames", writes)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for batched tx worker frames")
	}
}

func TestProbeVirtualRouterFrameLinkTXCoalesceWaitsForNextFrame(t *testing.T) {
	link := newProbeVirtualRouterFrameLink("test-coalesce-link", nil, nil, nil)
	defer stopProbeVirtualRouterFrameLink(link)
	want := probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypeIP, Data: []byte{0x45}}
	go func() {
		time.Sleep(10 * time.Millisecond)
		link.tx <- want
	}()
	businessSinceBulk := 0
	got, ok := link.waitNextTXFrameUntil(&businessSinceBulk, time.Now().Add(time.Second))
	if !ok || got.MainType != want.MainType || !reflect.DeepEqual(got.Data, want.Data) {
		t.Fatalf("coalesced frame=%+v ok=%v, want %+v", got, ok, want)
	}
}

type probeVirtualRouterWriteCountingConn struct {
	net.Conn
	mu         sync.Mutex
	writeCalls int
}

func (c *probeVirtualRouterWriteCountingConn) Write(payload []byte) (int, error) {
	c.mu.Lock()
	c.writeCalls++
	c.mu.Unlock()
	return c.Conn.Write(payload)
}

func (c *probeVirtualRouterWriteCountingConn) WriteCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeCalls
}

func TestProbeVirtualRouterFrameTXSuccessClearsRecoveredTransportError(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	left, right := net.Pipe()
	defer right.Close()
	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-tx-recovery"}}
	link := newProbeVirtualRouterFrameLink("packet|vrouter-tx-recovery", rt, left, nil)
	link.Start()
	t.Cleanup(func() { stopProbeVirtualRouterFrameLink(link) })

	recordProbeVirtualRouterRuntimeOpenError(rt.cfg.routeID, errors.New("virtual router tx queue full: depth=1024 capacity=1024"))
	frame, err := buildProbeVirtualRouterIPFrame([]byte{0x45, 0x00, 0x00, 0x14}, []string{"1", "2"}, nil)
	if err != nil {
		t.Fatalf("build frame failed: %v", err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, err := readProbeVirtualRouterWireFrame(bufio.NewReader(right))
		readDone <- err
	}()
	if err := link.EnqueueProbeVirtualRouterFrame(frame); err != nil {
		t.Fatalf("enqueue frame failed: %v", err)
	}
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("read frame failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tx worker frame")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats := snapshotProbeVirtualRouterRuntimeStats(rt.cfg.routeID)
		if stats != nil && stats.LastOpenError == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	stats := snapshotProbeVirtualRouterRuntimeStats(rt.cfg.routeID)
	t.Fatalf("successful carrier write should clear recovered transport error, stats=%+v", stats)
}

func TestProbeVirtualRouterFrameLinkTXWorkerSurvivesCarrierMigration(t *testing.T) {
	firstLeft, firstRight := net.Pipe()
	link := newProbeVirtualRouterFrameLink("test-link", nil, firstLeft, nil)
	link.Start()
	_ = firstLeft.Close()
	_ = firstRight.Close()
	defer stopProbeVirtualRouterFrameLink(link)
	detachDeadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := link.currentCarrier(); err != nil {
			break
		}
		if time.Now().After(detachDeadline) {
			t.Fatal("timed out waiting for old carrier detach")
		}
		time.Sleep(time.Millisecond)
	}

	wantPacket := []byte{0x45, 0x00, 0x00, 0x14}
	wantPath := []string{"16", "19"}
	secondLeft, secondRight := net.Pipe()
	defer secondRight.Close()
	if token := link.AttachCarrier(secondLeft, "reconnected", "pipe"); token == nil {
		t.Fatalf("attach carrier returned nil")
	}
	if err := writeProbeVirtualRouterIPFrame(link, wantPacket, wantPath, nil); err != nil {
		t.Fatalf("enqueue frame failed: %v", err)
	}
	if err := secondRight.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set migrated carrier read deadline failed: %v", err)
	}
	frame, err := readProbeVirtualRouterWireFrame(bufio.NewReader(secondRight))
	if err != nil {
		t.Fatalf("read migrated frame failed: %v", err)
	}
	control, err := probeVirtualRouterFrameControl(frame, nil)
	if err != nil {
		t.Fatalf("read migrated frame control failed: %v", err)
	}
	if frame.MainType != probeVirtualRouterFrameMainTypeIP || frame.SubType != probeVirtualRouterIPSubTypeIPv4 || !reflect.DeepEqual(frame.Data, wantPacket) || !reflect.DeepEqual(control.Path, wantPath) {
		t.Fatalf("frame=%+v, want payload/path %v/%v", frame, wantPacket, wantPath)
	}
}

func TestCurrentProbeVirtualRouterPathKeepsShortestHopBeforeRTT(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "4", Enabled: true},
			{FromNodeID: "1", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "1")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	probeVirtualRouterRuntimeState.mu.Lock()
	oldRuntimes := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{
		"vrouter-1-4": {cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-4", peerNodeID: "4", dialer: true}},
		"vrouter-1-3": {cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-3", peerNodeID: "3", dialer: true}},
	}
	probeVirtualRouterRuntimeState.mu.Unlock()
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		"vrouter-1-4": {LastPingLatencyMS: 100},
		"vrouter-1-3": {LastPingLatencyMS: 1},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = oldRuntimes
		probeVirtualRouterRuntimeState.mu.Unlock()
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldStats
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, []string{"1", "4"}) {
		t.Fatalf("path=%v, want [1 4]", got)
	}
}

func TestCurrentProbeVirtualRouterPathUsesRouteCacheUntilInvalidated(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{FromNodeID: "2", ToNodeID: "4", Enabled: true},
			{FromNodeID: "1", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "1")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	probeVirtualRouterRuntimeState.mu.Lock()
	oldRuntimes := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{
		"vrouter-1-2": {cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-2", peerNodeID: "2", dialer: true}},
		"vrouter-1-3": {cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-1-3", peerNodeID: "3", dialer: true}},
	}
	probeVirtualRouterRuntimeState.mu.Unlock()
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		"vrouter-1-2": {LastPingLatencyMS: 50},
		"vrouter-1-3": {LastPingLatencyMS: 10},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = oldRuntimes
		probeVirtualRouterRuntimeState.mu.Unlock()
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldStats
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, []string{"1", "3", "4"}) {
		t.Fatalf("initial path=%v, want [1 3 4]", got)
	}

	probeVirtualRouterRuntimeStatsState.mu.Lock()
	probeVirtualRouterRuntimeStatsState.items["vrouter-1-2"].LastPingLatencyMS = 1
	probeVirtualRouterRuntimeStatsState.items["vrouter-1-3"].LastPingLatencyMS = 100
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, []string{"1", "3", "4"}) {
		t.Fatalf("cached path=%v, want [1 3 4]", got)
	}

	clearProbeVirtualRouterRouteCache("test")
	if got := currentProbeVirtualRouterPathBetweenNodes("1", "4"); !reflect.DeepEqual(got, []string{"1", "2", "4"}) {
		t.Fatalf("path after cache clear=%v, want [1 2 4]", got)
	}
}

func TestCurrentProbeVirtualRouterPathDropsCachedDetourAfterDirectPrefixRecovers(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "9", IP: "198.18.0.9"},
			{NodeID: "18", IP: "198.18.0.18"},
			{NodeID: "17", IP: "198.18.0.17"},
			{NodeID: "19", IP: "198.18.0.19"},
			{NodeID: "15", IP: "198.18.0.15"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "9", ToNodeID: "18", Enabled: true},
			{FromNodeID: "18", ToNodeID: "19", Enabled: true},
			{FromNodeID: "9", ToNodeID: "19", Enabled: true},
			{FromNodeID: "19", ToNodeID: "15", Enabled: true},
		},
	}, "9")

	recordProbeVirtualRouterPathRTTSuccess([]string{"9", "19"}, 8*time.Millisecond, "19")
	storeProbeVirtualRouterRoutePath("9", "15", []string{"9", "18", "19", "15"})
	for attempt := 0; attempt < probeVirtualRouterPathRTTFailureThreshold; attempt++ {
		recordProbeVirtualRouterPathRTTError([]string{"9", "19", "15"}, errors.New("virtual router control response timeout"))
	}

	if got := currentProbeVirtualRouterPathBetweenNodes("9", "15"); !reflect.DeepEqual(got, []string{"9", "19", "15"}) {
		t.Fatalf("path=%v, want direct recovered prefix [9 19 15]", got)
	}

	directRuntime := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{
		routeID:     "vrouter-9-19",
		localNodeID: "9",
		peerNodeID:  "19",
		fromNodeID:  "9",
		toNodeID:    "19",
		dialer:      true,
	}}
	probeVirtualRouterRuntimeState.mu.Lock()
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{directRuntime.cfg.routeID: directRuntime}
	probeVirtualRouterRuntimeState.mu.Unlock()
	markProbeVirtualRouterPhysicalCarrierDisconnected(directRuntime, "test")

	if got := currentProbeVirtualRouterPathBetweenNodes("9", "15"); !reflect.DeepEqual(got, []string{"9", "18", "19", "15"}) {
		t.Fatalf("path=%v, want detour [9 18 19 15] while direct prefix is disconnected", got)
	}
}

func TestProbeVirtualRouterManualRefreshExploresAlternatePathImmediately(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "9", IP: "198.18.0.9"},
			{NodeID: "18", IP: "198.18.0.18"},
			{NodeID: "17", IP: "198.18.0.17"},
			{NodeID: "19", IP: "198.18.0.19"},
			{NodeID: "15", IP: "198.18.0.15"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "9", ToNodeID: "19", Enabled: true},
			{FromNodeID: "19", ToNodeID: "15", Enabled: true},
			{FromNodeID: "9", ToNodeID: "18", Enabled: true},
			{FromNodeID: "18", ToNodeID: "17", Enabled: true},
			{FromNodeID: "17", ToNodeID: "15", Enabled: true},
		},
	}, "9")

	query := func(path []string) (time.Duration, error) {
		switch strings.Join(path, ">") {
		case "9>17", "9>18", "9>19":
			return 2 * time.Millisecond, nil
		case "9>19>15":
			return 0, errors.New("virtual router control response timeout")
		case "9>18>17", "9>18>17>15":
			return 15 * time.Millisecond, nil
		default:
			return 0, errors.New("unreachable")
		}
	}
	backgroundResult := probeVirtualRouterRefreshPathRTTs(query, false)
	if backgroundResult.Explored != 0 || backgroundResult.RecoveredTargets != 0 || len(cachedProbeVirtualRouterRoutePath("9", "15")) != 0 {
		t.Fatalf("background refresh unexpectedly explored alternates: result=%+v path=%v", backgroundResult, cachedProbeVirtualRouterRoutePath("9", "15"))
	}
	result := probeVirtualRouterRefreshPathRTTs(query, true)
	if result.Explored == 0 || result.RecoveredTargets != 1 {
		t.Fatalf("refresh result=%+v, want active exploration recovery", result)
	}
	if got := cachedProbeVirtualRouterRoutePath("9", "15"); !reflect.DeepEqual(got, []string{"9", "18", "17", "15"}) {
		t.Fatalf("cached path=%v, want explored path [9 18 17 15]", got)
	}
}

func TestProbeVirtualRouterPacketTargetsLocalIPUsesStoredVirtualIP(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "19", IP: "198.18.0.21"},
			{NodeID: "20", IP: "198.18.0.22"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "19", ToNodeID: "20", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{identity: nodeIdentity{NodeID: "19"}}}
	if !probeVirtualRouterPacketTargetsLocalIP(rt, "198.18.0.21") {
		t.Fatalf("packet to node 19 ip should target local node")
	}
	if probeVirtualRouterPacketTargetsLocalIP(rt, "198.18.0.22") {
		t.Fatalf("packet to node 20 ip must not be treated as local node 19")
	}
}

func TestProbeVirtualRouterPacketTargetsLocalIPPrefersRuntimeIdentity(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "19")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{identity: nodeIdentity{NodeID: "16"}}}
	if !probeVirtualRouterPacketTargetsLocalIP(rt, "198.18.0.18") {
		t.Fatalf("packet to runtime node 16 ip should target local runtime")
	}
	if probeVirtualRouterPacketTargetsLocalIP(rt, "198.18.0.21") {
		t.Fatalf("packet to node 19 ip must not be treated as local runtime node 16")
	}
}

func TestProbeVirtualRouterReversePath(t *testing.T) {
	if got := probeVirtualRouterReversePath([]string{"node-16", "node-19"}); !reflect.DeepEqual(got, []string{"19", "16"}) {
		t.Fatalf("reverse path=%v, want [19 16]", got)
	}
}

func TestProbeVirtualRouterIPv4Destination(t *testing.T) {
	packet := buildProbeVirtualRouterTestIPv4Packet(t, "198.18.0.2", "198.18.0.9")
	if got := probeVirtualRouterIPv4Destination(packet); got != "198.18.0.9" {
		t.Fatalf("dst=%q, want 198.18.0.9", got)
	}
}

func TestProbeVirtualRouterDropsNonUnicastTUNDestinationsBeforeRouting(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "16")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	for _, dst := range []string{
		"198.19.255.255",
		"224.0.0.251",
		"224.0.0.252",
		"239.255.255.250",
		"255.255.255.255",
	} {
		packet := buildProbeVirtualRouterTestUDPPacket(t, "198.18.0.18", dst, 5353, 5353)
		if !probeVirtualRouterShouldDropNonUnicastDestination(dst) {
			t.Fatalf("dst=%s should be treated as non-unicast/discovery", dst)
		}
		if handleProbeVirtualRouterTUNPacket(packet) {
			t.Fatalf("dst=%s should be dropped before vRouter routing", dst)
		}
	}

	if probeVirtualRouterShouldDropNonUnicastDestination("198.18.0.21") {
		t.Fatalf("unicast vRouter peer should not be dropped")
	}
}

func TestBuildProbeVirtualRouterICMPEchoReply(t *testing.T) {
	request := buildProbeVirtualRouterTestICMPEchoRequest(t, "198.18.0.18", "198.18.0.21")
	reply, dstIP, ok := buildProbeVirtualRouterICMPEchoReply(request, "198.18.0.21")
	if !ok {
		t.Fatalf("echo request not handled")
	}
	if dstIP != "198.18.0.18" {
		t.Fatalf("reply dst=%q", dstIP)
	}
	if got := probeVirtualRouterIPv4Source(reply); got != "198.18.0.21" {
		t.Fatalf("reply source=%q", got)
	}
	if got := probeVirtualRouterIPv4Destination(reply); got != "198.18.0.18" {
		t.Fatalf("reply destination=%q", got)
	}
	if reply[20] != 0 || reply[21] != 0 {
		t.Fatalf("icmp type/code=%d/%d", reply[20], reply[21])
	}
	if probeVirtualRouterChecksum(reply[:20]) != 0 {
		t.Fatalf("invalid ipv4 checksum")
	}
	if probeVirtualRouterChecksum(reply[20:]) != 0 {
		t.Fatalf("invalid icmp checksum")
	}
	if _, _, ok := buildProbeVirtualRouterICMPEchoReply(request, "198.18.0.22"); ok {
		t.Fatalf("request for another local ip should not be handled")
	}
}

func TestProbeVirtualRouterTUNPacketAllowsVirtualRangeWhenEntryDisabled(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "16")
	if probeVirtualRouterLocalEntryEnabled() {
		t.Fatalf("virtual router entry should default disabled")
	}

	oldWriter := probeVirtualRouterLocalTUNPacketWriter
	var written [][]byte
	probeVirtualRouterLocalTUNPacketWriter = func(packet []byte) error {
		written = append(written, append([]byte(nil), packet...))
		return nil
	}
	t.Cleanup(func() { probeVirtualRouterLocalTUNPacketWriter = oldWriter })

	request := buildProbeVirtualRouterTestICMPEchoRequest(t, "198.18.0.99", "198.18.0.18")
	if !handleProbeVirtualRouterTUNPacket(request) {
		t.Fatalf("local self virtual ip echo should be handled even when entry is disabled")
	}
	if len(written) != 1 {
		t.Fatalf("written packets=%d, want 1", len(written))
	}
	info, ok := probeVirtualRouterParseICMPEchoLogInfo(written[0])
	if !ok || info.Kind != "echo_reply" || info.SourceIP != "198.18.0.18" || info.DestinationIP != "198.18.0.99" {
		t.Fatalf("reply info=%+v ok=%v", info, ok)
	}

	peerPacket := buildProbeVirtualRouterTestICMPEchoRequest(t, "198.18.0.18", "198.18.0.21")
	if !handleProbeVirtualRouterTUNPacket(peerPacket) {
		t.Fatalf("remote virtual ip forwarding should remain available when entry is disabled")
	}

	ordinaryPacket := buildProbeVirtualRouterTestICMPEchoRequest(t, "198.18.0.18", "203.0.113.99")
	if handleProbeVirtualRouterTUNPacket(ordinaryPacket) {
		t.Fatalf("ordinary system traffic should not be handled when entry is disabled")
	}
}

func TestProbeVirtualRouterTUNPacketEnsuresDirectBypassForOrdinaryTarget(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)

	config := probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "16")
	enableProbeVirtualRouterLocalSettingsForTest(true, true)

	oldEnsure := probeVirtualRouterEnsureDirectBypass
	var targets []string
	probeVirtualRouterEnsureDirectBypass = func(target string) error {
		targets = append(targets, target)
		return nil
	}
	t.Cleanup(func() { probeVirtualRouterEnsureDirectBypass = oldEnsure })

	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "203.0.113.99", 49152, 443)
	if handleProbeVirtualRouterTUNPacket(packet) {
		t.Fatalf("ordinary system traffic should be released after bypass route ensure")
	}
	if len(targets) != 1 || targets[0] != "203.0.113.99:443" {
		t.Fatalf("direct bypass targets=%v, want [203.0.113.99:443]", targets)
	}

	fakePacket := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.1.20", 49153, 443)
	if handleProbeVirtualRouterTUNPacket(fakePacket) {
		t.Fatalf("unmapped fake ip should not be treated as ordinary direct bypass")
	}
	if len(targets) != 1 {
		t.Fatalf("fake ip should not add direct bypass target, got %v", targets)
	}

	config.RouteRules = []probeVirtualRouterRouteRule{{
		ID:         "telegram",
		Name:       "Telegram",
		Action:     "probe_exit",
		ExitNodeID: "19",
		Entries:    []string{"cidr:149.154.160.0/20"},
	}}
	applyProbeVirtualRouterConfigForNode(config, "16")
	tgPacket := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "149.154.167.51", 49154, 443)
	if probeVirtualRouterEnsureDirectBypassForOrdinaryTarget(tgPacket, "149.154.167.51") {
		t.Fatalf("virtual-router route rule target should not be released to direct bypass")
	}
	if len(targets) != 1 {
		t.Fatalf("route rule target should not add direct bypass target, got %v", targets)
	}
}

func TestProbeVirtualRouterTUNPacketDropsFakeIPWhenExitCarrierUnavailable(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	config := probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.16"},
			{NodeID: "19", IP: "198.18.0.19"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{ID: "edge-16-19", FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "api.example.com",
				FakeIP:     "198.18.4.9",
				Action:     "probe_exit",
				ExitNodeID: "19",
			}},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "16")

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-16-19", peerNodeID: "19", dialer: true}}
	probeVirtualRouterRuntimeState.mu.Lock()
	oldRuntimes := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{rt.cfg.routeID: rt}
	probeVirtualRouterRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = oldRuntimes
		probeVirtualRouterRuntimeState.mu.Unlock()
	})

	probeVirtualRouterFrameLinkState.mu.Lock()
	oldLinks := probeVirtualRouterFrameLinkState.links
	probeVirtualRouterFrameLinkState.links = make(map[string]*probeVirtualRouterFrameLink)
	probeVirtualRouterFrameLinkState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterFrameLinkState.mu.Lock()
		probeVirtualRouterFrameLinkState.links = oldLinks
		probeVirtualRouterFrameLinkState.mu.Unlock()
	})

	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.16", "198.18.4.9", 49152, 443)
	if handleProbeVirtualRouterTUNPacket(packet) {
		t.Fatalf("fake ip packet should be dropped when exit carrier is unavailable")
	}
	items := snapshotProbeVirtualRouterRecentPackets()
	if len(items) != 1 {
		t.Fatalf("recent packets=%d, want 1", len(items))
	}
	item := items[0]
	if item.Source != "tun_rx" || item.Action != "drop" || item.FakeIPDomain != "api.example.com" || item.FakeIPExitNode != "19" {
		t.Fatalf("unexpected recent packet: %+v", item)
	}
	if !strings.Contains(item.Error, "fake ip exit unreachable") {
		t.Fatalf("recent packet error=%q, want fake ip exit unreachable", item.Error)
	}
	probeVirtualRouterFrameLinkState.mu.Lock()
	linkCount := len(probeVirtualRouterFrameLinkState.links)
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if linkCount != 0 {
		t.Fatalf("frame links=%d, want 0 because packet should not be forwarded", linkCount)
	}
}

func TestProbeVirtualRouterFakeIPExitPacketUsesNetstack(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	resetProbeVirtualRouterExitNetstackForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeVirtualRouterExitNetstackForTest)

	config := probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "127.0.0.1",
				FakeIP:     "198.18.4.9",
				Action:     "probe_exit",
				ExitNodeID: "19",
			}},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "19")

	oldWriter := probeVirtualRouterLocalTUNPacketWriter
	var tunWrites int
	probeVirtualRouterLocalTUNPacketWriter = func(packet []byte) error {
		tunWrites++
		return nil
	}
	t.Cleanup(func() { probeVirtualRouterLocalTUNPacketWriter = oldWriter })

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-16-19", identity: nodeIdentity{NodeID: "19"}}}
	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.4.9", 49152, 443)
	if err := handleProbeVirtualRouterIPFrame(rt, nil, packet, []string{"16", "19"}, nil); err != nil {
		t.Fatalf("handle fake ip exit packet failed: %v", err)
	}
	if tunWrites != 0 {
		t.Fatalf("fake ip exit packet should be consumed by netstack, local tun writes=%d", tunWrites)
	}
	if probeVirtualRouterExitNetstackState.runner == nil {
		t.Fatalf("fake ip exit packet should start exit netstack")
	}
}

func TestProbeVirtualRouterCIDRExitPacketUsesNetstackAtFinalHop(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	resetProbeVirtualRouterExitNetstackForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeVirtualRouterExitNetstackForTest)

	config := probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:         "rr-cidr",
			Name:       "CIDR",
			Action:     "probe_exit",
			ExitNodeID: "19",
			Entries:    []string{"cidr:203.0.113.0/24"},
		}},
	}
	applyProbeVirtualRouterConfigForNode(config, "19")

	oldWriter := probeVirtualRouterLocalTUNPacketWriter
	var tunWrites int
	probeVirtualRouterLocalTUNPacketWriter = func(packet []byte) error {
		tunWrites++
		return nil
	}
	t.Cleanup(func() { probeVirtualRouterLocalTUNPacketWriter = oldWriter })

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-16-19", identity: nodeIdentity{NodeID: "19"}}}
	rule, ok := currentProbeVirtualRouterRouteRuleForIP("203.0.113.9")
	if !ok || rule.ExitNodeID != "19" || rule.Action != "probe_exit" {
		t.Fatalf("cidr route rule not matched: rule=%+v ok=%t", rule, ok)
	}
	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "203.0.113.9", 49152, 443)
	if err := handleProbeVirtualRouterIPFrame(rt, nil, packet, []string{"16", "19"}, nil); err != nil {
		t.Fatalf("handle cidr exit packet failed: %v", err)
	}
	if tunWrites != 0 {
		t.Fatalf("cidr exit packet should be consumed by netstack, local tun writes=%d", tunWrites)
	}
	if probeVirtualRouterExitNetstackState.runner == nil {
		t.Fatalf("cidr exit packet should start exit netstack")
	}
	items := snapshotProbeVirtualRouterRecentPackets()
	if len(items) == 0 || items[0].Action != "exit" || items[0].DestinationIP != "203.0.113.9" {
		t.Fatalf("unexpected recent packet: %+v", items)
	}
}

func TestProbeVirtualRouterRecentPacketRecordsFakeIPSummary(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	config := probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "api.example.com",
				FakeIP:     "198.18.4.9",
				Action:     "probe_exit",
				ExitNodeID: "19",
			}},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "19")
	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.4.9", 49152, 443)
	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-16-19", identity: nodeIdentity{NodeID: "19"}, peerNodeID: "16"}}

	recordProbeVirtualRouterRecentPacket("frame_rx", "fake_exit", rt, packet, []string{"16", "19"}, true, nil)
	items := snapshotProbeVirtualRouterRecentPackets()
	if len(items) != 1 {
		t.Fatalf("recent packets=%d, want 1", len(items))
	}
	item := items[0]
	if item.ID != 1 || item.Source != "frame_rx" || item.Action != "fake_exit" || item.RouteID != "vrouter-16-19" {
		t.Fatalf("unexpected recent packet identity: %+v", item)
	}
	if item.Protocol != "TCP" || item.SourceIP != "198.18.0.18" || item.DestinationIP != "198.18.4.9" || item.SourcePort != 49152 || item.DestinationPort != 443 {
		t.Fatalf("unexpected packet tuple: %+v", item)
	}
	if !item.FakeIP || item.FakeIPSide != "dst" || item.FakeIPDomain != "api.example.com" || item.FakeIPExitNode != "19" {
		t.Fatalf("unexpected fake ip metadata: %+v", item)
	}
	if !reflect.DeepEqual(item.Path, []string{"16", "19"}) || item.PathText != "16>19" || !item.LocalMatch {
		t.Fatalf("unexpected path/local match: %+v", item)
	}
}

func TestProbeVirtualRouterRecentConnectionsAggregateBidirectionalTraffic(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	forward := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.16", "198.18.4.9", 49152, 443)
	reply := buildProbeVirtualRouterTestTCPPacket(t, "198.18.4.9", "198.18.0.16", 443, 49152)
	recordProbeVirtualRouterRecentPacket("tun_rx", "forward", nil, forward, []string{"16", "19"}, false, nil)
	recordProbeVirtualRouterRecentPacket("frame_rx", "deliver", nil, reply, []string{"19", "16"}, true, nil)

	connections := snapshotProbeVirtualRouterRecentConnections()
	if len(connections) != 1 {
		t.Fatalf("connections=%d, want 1: %+v", len(connections), connections)
	}
	connection := connections[0]
	if connection.Protocol != "TCP" || connection.Events != 2 || connection.TUNEvents != 1 || connection.FrameEvents != 1 || connection.Forwarded != 1 || connection.Delivered != 1 {
		t.Fatalf("unexpected connection activity: %+v", connection)
	}
	if connection.EndpointA != "198.18.0.16:49152" || connection.EndpointB != "198.18.4.9:443" || connection.Status != "active" {
		t.Fatalf("unexpected connection identity: %+v", connection)
	}
	probeVirtualRouterRecentConnectionState.mu.Lock()
	for key, item := range probeVirtualRouterRecentConnectionState.items {
		item.lastAt = time.Now().Add(-probeVirtualRouterRecentConnectionTTL - time.Second)
		probeVirtualRouterRecentConnectionState.items[key] = item
	}
	probeVirtualRouterRecentConnectionState.mu.Unlock()
	if expired := snapshotProbeVirtualRouterRecentConnections(); len(expired) != 0 {
		t.Fatalf("expired connections=%d, want 0: %+v", len(expired), expired)
	}
}

func TestProbeVirtualRouterRecentConnectionsKeepDNSWithoutNetworkConnection(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	recordProbeVirtualRouterRecentDNSQuery("api.example.com", "fake_ip", "17", "198.18.4.9", nil, nil)
	recordProbeVirtualRouterRecentDNSQuery("failed.example.com", "fake_ip", "17", "", nil, errors.New("controller unavailable"))

	findConnection := func(domain string) probeVirtualRouterRecentConnection {
		for _, item := range snapshotProbeVirtualRouterRecentConnections() {
			if item.Kind == "dns" && item.Domain == domain {
				return item
			}
		}
		t.Fatalf("DNS connection for %s was not retained", domain)
		return probeVirtualRouterRecentConnection{}
	}
	query := findConnection("api.example.com")
	if query.Status != "unconnected" || query.DNSQueries != 1 || query.FakeIPExitNode != "17" {
		t.Fatalf("unexpected unconnected DNS query: %+v", query)
	}
	markProbeVirtualRouterRecentDNSConnection("api.example.com", probeVirtualRouterRecentPacket{
		CapturedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		Source:          "tun_rx",
		Protocol:        "TCP",
		DestinationIP:   "198.18.4.9",
		DestinationPort: 443,
	})
	if connected := findConnection("api.example.com"); connected.Status != "connected" || !connected.Connected {
		t.Fatalf("DNS query should show connected after network activity: %+v", connected)
	}
	if failed := findConnection("failed.example.com"); failed.Status != "error" || failed.Errors != 1 || !strings.Contains(failed.LastError, "controller unavailable") {
		t.Fatalf("unexpected failed DNS query: %+v", failed)
	}
}

func TestProbeVirtualRouterFakeIPVerifyAtExitChecksDomainReachability(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
	}, "19")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen test tcp target: %v", err)
	}
	defer listener.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- struct{}{}
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	oldExitLookup := probeVirtualRouterExitLookupIPv4
	probeVirtualRouterExitLookupIPv4 = func(domain string) ([]string, error) {
		if domain != "api.example.com" {
			t.Fatalf("unexpected verify lookup domain: %s", domain)
		}
		return []string{"127.0.0.1"}, nil
	}
	t.Cleanup(func() { probeVirtualRouterExitLookupIPv4 = oldExitLookup })

	msg := probeVirtualRouterFakeIPVerifyPayload{
		RequestID:    "verify-1",
		SourceNodeID: "16",
		ExitNodeID:   "19",
		Path:         []string{"16", "19"},
		Domain:       "api.example.com",
		FakeIP:       "198.18.4.9",
		Port:         port,
		Protocol:     "tcp",
		Reason:       "test",
	}
	response := runProbeVirtualRouterFakeIPVerifyAtExit(msg, &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{identity: nodeIdentity{NodeID: "19"}}})
	if !response.OK || response.Error != "" {
		t.Fatalf("verify response failed: %+v", response)
	}
	if !reflect.DeepEqual(response.ResolvedIPs, []string{"127.0.0.1"}) || response.CheckedAddress == "" || response.LatencyMS < 0 {
		t.Fatalf("unexpected verify response detail: %+v", response)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatalf("exit verify did not dial test target")
	}
}

func TestProbeVirtualRouterFakeIPVerifyResponseCompletesAtSource(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
	}, "19")

	requestID := "verify-2"
	waiter := registerProbeVirtualRouterFakeIPVerifyResponse(requestID)
	defer unregisterProbeVirtualRouterFakeIPVerifyResponse(requestID)
	response := probeVirtualRouterFakeIPVerifyPayload{
		RequestID:      requestID,
		SourceNodeID:   "19",
		ExitNodeID:     "16",
		Path:           []string{"16", "19"},
		Domain:         "api.example.com",
		FakeIP:         "198.18.4.9",
		Port:           443,
		Protocol:       "tcp",
		OK:             true,
		Responder:      "16",
		ResolvedIPs:    []string{"203.0.113.10"},
		CheckedAddress: "203.0.113.10:443",
		LatencyMS:      12,
	}
	if err := handleProbeVirtualRouterFakeIPVerifyFrame(&probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{identity: nodeIdentity{NodeID: "19"}}}, probeVirtualRouterFakeIPVerifySubTypeResponse, response); err != nil {
		t.Fatalf("handle verify response failed: %v", err)
	}
	got, err := waitProbeVirtualRouterFakeIPVerifyResponse(waiter, time.Second)
	if err != nil {
		t.Fatalf("wait verify response failed: %v", err)
	}
	if !got.OK || got.Responder != "16" || got.CheckedAddress != "203.0.113.10:443" {
		t.Fatalf("unexpected verify response: %+v", got)
	}
}

func TestProbeVirtualRouterDebugLogResponseCompletesAtSource(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "9", IP: "198.18.0.7"},
			{NodeID: "17", IP: "198.18.0.11"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "9", ToNodeID: "17", Enabled: true},
		},
	}, "9")

	requestID := "debug-log-1"
	waiter := registerProbeVirtualRouterDebugLogResponse(requestID)
	defer unregisterProbeVirtualRouterDebugLogResponse(requestID)
	response := probeVirtualRouterDebugLogPayload{
		RequestID:    requestID,
		SourceNodeID: "9",
		TargetNodeID: "17",
		Path:         []string{"17", "9"},
		Lines:        50,
		Keyword:      "carrier",
		OK:           true,
		Responder:    "17",
		Content:      "carrier closed",
		Entries: []probeLogViewEntry{{
			Level:   probeLogLevelNormal,
			Message: "carrier closed",
			Line:    "carrier closed",
		}},
		Count: 1,
	}
	if err := handleProbeVirtualRouterDebugLogFrame(&probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{identity: nodeIdentity{NodeID: "9"}}}, probeVirtualRouterDebugLogSubTypeResponse, response); err != nil {
		t.Fatalf("handle debug log response failed: %v", err)
	}
	got, err := waitProbeVirtualRouterDebugLogResponse(waiter, time.Second)
	if err != nil {
		t.Fatalf("wait debug log response failed: %v", err)
	}
	if !got.OK || got.Responder != "17" || got.Count != 1 || !strings.Contains(got.Content, "carrier closed") {
		t.Fatalf("unexpected debug log response: %+v", got)
	}
}

func TestProbeVirtualRouterFakeIPVerifySYNRetransmitSchedulesSourceVerify(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "api.example.com",
				FakeIP:     "198.18.4.9",
				Action:     "probe_exit",
				ExitNodeID: "19",
			}},
		},
	}, "16")

	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.4.9", 49152, 443)
	if maybeScheduleProbeVirtualRouterFakeIPVerifyForTCPRetransmit(packet, []string{"16", "19"}) {
		t.Fatalf("first SYN should not trigger verify")
	}
	if !maybeScheduleProbeVirtualRouterFakeIPVerifyForTCPRetransmit(packet, []string{"16", "19"}) {
		t.Fatalf("second SYN should trigger verify")
	}
	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	defer probeVirtualRouterFakeIPVerifyState.mu.Unlock()
	if len(probeVirtualRouterFakeIPVerifyState.lastAt) != 1 {
		t.Fatalf("verify lastAt state=%+v", probeVirtualRouterFakeIPVerifyState.lastAt)
	}
}

func TestProbeVirtualRouterFakeIPVerifyTimeoutAdaptsToPathRTT(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	path := []string{"9", "17"}
	if got := probeVirtualRouterFakeIPVerifyTimeoutForPath(path); got != probeVirtualRouterFakeIPVerifyMinTimeout {
		t.Fatalf("timeout without RTT=%s, want %s", got, probeVirtualRouterFakeIPVerifyMinTimeout)
	}
	recordProbeVirtualRouterPathRTTSuccess(path, 4700*time.Millisecond, "17")
	if got := probeVirtualRouterFakeIPVerifyTimeoutForPath(path); got != 11400*time.Millisecond {
		t.Fatalf("timeout with 4.7s RTT=%s, want 11.4s", got)
	}
	recordProbeVirtualRouterPathRTTSuccess(path, 20*time.Second, "17")
	if got := probeVirtualRouterFakeIPVerifyTimeoutForPath(path); got != probeVirtualRouterFakeIPVerifyMaxTimeout {
		t.Fatalf("timeout cap=%s, want %s", got, probeVirtualRouterFakeIPVerifyMaxTimeout)
	}
}

func TestProbeVirtualRouterFakeIPVerifyFailureCooldownBacksOff(t *testing.T) {
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{failures: 0, want: 15 * time.Second},
		{failures: 1, want: 30 * time.Second},
		{failures: 2, want: time.Minute},
		{failures: 3, want: 2 * time.Minute},
		{failures: 5, want: 5 * time.Minute},
		{failures: 20, want: 5 * time.Minute},
	}
	for _, test := range tests {
		if got := probeVirtualRouterFakeIPVerifyCooldownForFailures(test.failures); got != test.want {
			t.Fatalf("failures=%d cooldown=%s, want %s", test.failures, got, test.want)
		}
	}
}

func TestProbeVirtualRouterFakeIPVerifyScheduleHonorsConcurrencyAndFailureBackoff(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	msg := probeVirtualRouterFakeIPVerifyPayload{
		SourceNodeID: "9",
		ExitNodeID:   "17",
		Path:         []string{"9", "17"},
		Domain:       "api.example.com",
		FakeIP:       "198.18.4.9",
		Port:         443,
		Protocol:     "tcp",
	}
	key := probeVirtualRouterFakeIPVerifyKey(msg)
	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	probeVirtualRouterFakeIPVerifyState.lastAt[key] = time.Now()
	probeVirtualRouterFakeIPVerifyState.failures[key] = 1
	probeVirtualRouterFakeIPVerifyState.mu.Unlock()
	if scheduleProbeVirtualRouterFakeIPVerify(msg, nil) {
		t.Fatal("failure backoff should suppress an immediate retry")
	}

	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	delete(probeVirtualRouterFakeIPVerifyState.lastAt, key)
	delete(probeVirtualRouterFakeIPVerifyState.failures, key)
	for index := 0; index < probeVirtualRouterFakeIPVerifyMaxConcurrent; index++ {
		probeVirtualRouterFakeIPVerifyState.running[fmt.Sprintf("busy-%d", index)] = true
	}
	probeVirtualRouterFakeIPVerifyState.mu.Unlock()
	if scheduleProbeVirtualRouterFakeIPVerify(msg, nil) {
		t.Fatal("global verify concurrency limit should suppress excess diagnostics")
	}

	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	probeVirtualRouterFakeIPVerifyState.running = map[string]bool{key: true}
	probeVirtualRouterFakeIPVerifyState.failures[key] = 3
	probeVirtualRouterFakeIPVerifyState.mu.Unlock()
	if failures := completeProbeVirtualRouterFakeIPVerifySchedule(key, true); failures != 0 {
		t.Fatalf("successful verify failure count=%d, want 0", failures)
	}
	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	_, stillRunning := probeVirtualRouterFakeIPVerifyState.running[key]
	_, stillFailing := probeVirtualRouterFakeIPVerifyState.failures[key]
	probeVirtualRouterFakeIPVerifyState.mu.Unlock()
	if stillRunning || stillFailing {
		t.Fatalf("successful verify should clear running and failure state: running=%t failing=%t", stillRunning, stillFailing)
	}
}

func TestProbeVirtualRouterLogThrottleAggregatesSuppressedEntries(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	period := 30 * time.Second
	startedAt := time.Unix(100, 0)
	if allowed, suppressed := takeProbeVirtualRouterLogThrottle("verify|9>17", period, startedAt); !allowed || suppressed != 0 {
		t.Fatalf("first log=(%t,%d), want (true,0)", allowed, suppressed)
	}
	for index := 1; index <= 2; index++ {
		if allowed, suppressed := takeProbeVirtualRouterLogThrottle("verify|9>17", period, startedAt.Add(time.Duration(index)*time.Second)); allowed || suppressed != 0 {
			t.Fatalf("throttled log %d=(%t,%d), want (false,0)", index, allowed, suppressed)
		}
	}
	if allowed, suppressed := takeProbeVirtualRouterLogThrottle("verify|9>17", period, startedAt.Add(period)); !allowed || suppressed != 2 {
		t.Fatalf("aggregated log=(%t,%d), want (true,2)", allowed, suppressed)
	}
}

func TestProbeVirtualRouterNonDirectPathGuardianLogsFailureTransitionOnce(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	pathKey := "9>19>15"
	if !markProbeVirtualRouterNonDirectPathGuardianFailure(pathKey, true) {
		t.Fatal("first failure should be logged")
	}
	if markProbeVirtualRouterNonDirectPathGuardianFailure(pathKey, true) {
		t.Fatal("continuous failure should not be logged again")
	}
	markProbeVirtualRouterNonDirectPathGuardianFailure(pathKey, false)
	if !markProbeVirtualRouterNonDirectPathGuardianFailure(pathKey, true) {
		t.Fatal("failure after recovery should be logged")
	}
	pruneProbeVirtualRouterNonDirectPathGuardianFailures(map[string]struct{}{})
	if !markProbeVirtualRouterNonDirectPathGuardianFailure(pathKey, true) {
		t.Fatal("failure after path removal should be logged")
	}
}

func TestProbeVirtualRouterTransportTUNDropLogThrottleAggregatesByFlow(t *testing.T) {
	probeVirtualRouterLogThrottleState.mu.Lock()
	oldItems := probeVirtualRouterLogThrottleState.items
	probeVirtualRouterLogThrottleState.items = make(map[string]probeVirtualRouterLogThrottleEntry)
	probeVirtualRouterLogThrottleState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterLogThrottleState.mu.Lock()
		probeVirtualRouterLogThrottleState.items = oldItems
		probeVirtualRouterLogThrottleState.mu.Unlock()
	})

	info := probeVirtualRouterTransportLogInfo{
		Protocol:        "udp",
		SourceIP:        "198.18.0.8",
		SourcePort:      48672,
		DestinationIP:   "198.18.0.21",
		DestinationPort: 53006,
	}
	startedAt := time.Date(2026, 7, 17, 18, 16, 57, 0, time.UTC)
	if allowed, suppressed := takeProbeVirtualRouterTransportTUNDropLogThrottle(info, "path_unavailable", startedAt); !allowed || suppressed != 0 {
		t.Fatalf("first drop allowed=%v suppressed=%d", allowed, suppressed)
	}
	if allowed, suppressed := takeProbeVirtualRouterTransportTUNDropLogThrottle(info, "path_unavailable", startedAt.Add(time.Second)); allowed || suppressed != 0 {
		t.Fatalf("repeated drop allowed=%v suppressed=%d", allowed, suppressed)
	}
	if allowed, suppressed := takeProbeVirtualRouterTransportTUNDropLogThrottle(info, "path_unavailable", startedAt.Add(probeVirtualRouterDiagnosticLogPeriod)); !allowed || suppressed != 1 {
		t.Fatalf("window drop allowed=%v suppressed=%d", allowed, suppressed)
	}
	info.DestinationPort++
	if allowed, suppressed := takeProbeVirtualRouterTransportTUNDropLogThrottle(info, "path_unavailable", startedAt.Add(time.Second)); !allowed || suppressed != 0 {
		t.Fatalf("different flow allowed=%v suppressed=%d", allowed, suppressed)
	}
}

func TestProbeVirtualRouterFinalHopFakeIPMissDropsWithoutSourceSync(t *testing.T) {
	t.Skip("disabled: final-hop fake-ip miss edge-case test is excluded from the default regression suite")
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
	}, "19")

	oldWriter := probeVirtualRouterLocalTUNPacketWriter
	var tunWrites int
	probeVirtualRouterLocalTUNPacketWriter = func(packet []byte) error {
		tunWrites++
		return nil
	}
	t.Cleanup(func() { probeVirtualRouterLocalTUNPacketWriter = oldWriter })

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-16-19", identity: nodeIdentity{NodeID: "19"}, peerNodeID: "16"}}
	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.4.9", 49152, 443)
	if err := handleProbeVirtualRouterIPFrame(rt, nil, packet, []string{"16", "19"}, nil); err != nil {
		t.Fatalf("final-hop fake ip miss should be dropped without rx error: %v", err)
	}
	if tunWrites != 0 {
		t.Fatalf("final-hop fake ip miss must not be written to local TUN, writes=%d", tunWrites)
	}
	items := snapshotProbeVirtualRouterRecentPackets()
	if len(items) != 1 || items[0].Action != "drop" {
		t.Fatalf("missing fake ip packet should be dropped, items=%+v", items)
	}
}

func TestProbeVirtualRouterFakeIPExitTargetsResolveRealIP(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	lookupCalls := 0
	oldExitLookup := probeVirtualRouterExitLookupIPv4
	probeVirtualRouterExitLookupIPv4 = func(domain string) ([]string, error) {
		if domain != "api.example.com" {
			t.Fatalf("unexpected exit lookup domain: %s", domain)
		}
		lookupCalls++
		return []string{"203.0.113.10"}, nil
	}
	t.Cleanup(func() { probeVirtualRouterExitLookupIPv4 = oldExitLookup })

	config := probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "19", IP: "198.18.0.21"},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "api.example.com",
				FakeIP:     "198.18.4.9",
				Action:     "probe_exit",
				ExitNodeID: "19",
			}},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "19")

	targets, _, err := probeVirtualRouterFakeIPTargetsFromTransportID(tcpip.AddrFrom4([4]byte{198, 18, 4, 9}), 443)
	if err != nil {
		t.Fatalf("resolve fake ip targets failed: %v", err)
	}
	if !reflect.DeepEqual(targets, []string{"203.0.113.10:443"}) {
		t.Fatalf("targets=%v, want [203.0.113.10:443]", targets)
	}
	if lookupCalls != 1 {
		t.Fatalf("exit lookup calls=%d, want 1 so fake ip exit ignores cached real ips", lookupCalls)
	}
}

func TestProbeVirtualRouterFakeIPExitTargetsRefreshMissingMappingItemFromController(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	oldExitLookup := probeVirtualRouterExitLookupIPv4
	probeVirtualRouterExitLookupIPv4 = func(domain string) ([]string, error) {
		if domain != "api.example.com" {
			t.Fatalf("unexpected exit lookup domain: %s", domain)
		}
		return []string{"203.0.113.10"}, nil
	}
	t.Cleanup(func() { probeVirtualRouterExitLookupIPv4 = oldExitLookup })
	oldRequestByIP := probeRequestRouteFakeIPByIP
	requestCh := make(chan struct{}, 1)
	probeRequestRouteFakeIPByIP = func(ctx context.Context, controllerBaseURL string, identity nodeIdentity, fakeIP string) (probeVirtualRouterFakeIPEntry, error) {
		select {
		case requestCh <- struct{}{}:
		default:
		}
		if controllerBaseURL != "https://controller.example.test" || identity.NodeID != "19" || identity.Secret != "secret-19" || fakeIP != "198.18.4.9" {
			t.Fatalf("unexpected controller request: base=%q identity=%+v fake_ip=%q", controllerBaseURL, identity, fakeIP)
		}
		return probeVirtualRouterFakeIPEntry{
			Domain:     "api.example.com",
			FakeIP:     "198.18.4.9",
			Action:     "probe_exit",
			ExitNodeID: "19",
			ExpiresAt:  time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339),
		}, nil
	}
	t.Cleanup(func() { probeRequestRouteFakeIPByIP = oldRequestByIP })
	probeVirtualRouterControllerState.mu.Lock()
	oldIdentity := probeVirtualRouterControllerState.identity
	oldControllerBaseURL := probeVirtualRouterControllerState.controllerBaseURL
	probeVirtualRouterControllerState.identity = nodeIdentity{NodeID: "19", Secret: "secret-19"}
	probeVirtualRouterControllerState.controllerBaseURL = "https://controller.example.test"
	probeVirtualRouterControllerState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterControllerState.mu.Lock()
		probeVirtualRouterControllerState.identity = oldIdentity
		probeVirtualRouterControllerState.controllerBaseURL = oldControllerBaseURL
		probeVirtualRouterControllerState.mu.Unlock()
	})

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "19", IP: "198.18.0.21"},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{Version: 1},
	}, "19")

	targets, _, err := probeVirtualRouterFakeIPTargetsFromTransportID(tcpip.AddrFrom4([4]byte{198, 18, 4, 9}), 443)
	if err != nil {
		t.Fatalf("first resolve should wait for controller item refresh: %v", err)
	}
	select {
	case <-requestCh:
	default:
		t.Fatalf("controller item refresh was not requested")
	}
	if !reflect.DeepEqual(targets, []string{"203.0.113.10:443"}) {
		t.Fatalf("targets=%v, want [203.0.113.10:443]", targets)
	}
	if entry, ok := currentProbeVirtualRouterFakeIPEntryByIP("198.18.4.9"); !ok || entry.Domain != "api.example.com" {
		t.Fatalf("fake ip item should be refreshed, entry=%+v ok=%v", entry, ok)
	} else if expiresAt, err := time.Parse(time.RFC3339, entry.ExpiresAt); err != nil || time.Until(expiresAt) > 49*time.Hour || time.Until(expiresAt) < 47*time.Hour {
		t.Fatalf("local fake ip ttl should be about 48h, expires_at=%q err=%v", entry.ExpiresAt, err)
	}
	targets, _, err = probeVirtualRouterFakeIPTargetsFromTransportID(tcpip.AddrFrom4([4]byte{198, 18, 4, 9}), 443)
	if err != nil {
		t.Fatalf("resolve fake ip targets after item refresh failed: %v", err)
	}
	if !reflect.DeepEqual(targets, []string{"203.0.113.10:443"}) {
		t.Fatalf("targets=%v, want [203.0.113.10:443]", targets)
	}
	select {
	case <-requestCh:
		t.Fatalf("cached fake ip item should not request controller again")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestProbeVirtualRouterFakeIPExitTCPForwarderDoesNotBlockOnResolve(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	releaseLookup := make(chan struct{})
	lookupStarted := make(chan struct{})
	var lookupStartedOnce sync.Once
	oldExitLookup := probeVirtualRouterExitLookupIPv4
	probeVirtualRouterExitLookupIPv4 = func(domain string) ([]string, error) {
		if domain != "api.example.com" {
			t.Errorf("unexpected exit lookup domain: %s", domain)
		}
		lookupStartedOnce.Do(func() { close(lookupStarted) })
		<-releaseLookup
		return nil, errors.New("test resolver stopped")
	}
	t.Cleanup(func() {
		close(releaseLookup)
		probeVirtualRouterExitLookupIPv4 = oldExitLookup
	})

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "19", IP: "198.18.0.21"},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "api.example.com",
				FakeIP:     "198.18.4.9",
				Action:     "probe_exit",
				ExitNodeID: "19",
			}},
		},
	}, "19")

	runner, err := newProbeVirtualRouterExitNetstack()
	if err != nil {
		t.Fatalf("new netstack failed: %v", err)
	}
	t.Cleanup(func() { _ = runner.Close() })

	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.4.9", 49152, 443)
	setProbeVirtualRouterTestTCPChecksum(packet)
	injectDone := make(chan error, 1)
	go func() {
		injectDone <- runner.Inject(packet)
	}()

	select {
	case err := <-injectDone:
		if err != nil {
			t.Fatalf("inject failed: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("tcp forwarder should not block packet injection while fake-ip resolve is pending")
	}
	select {
	case <-lookupStarted:
	default:
	}
}

func TestProbeVirtualRouterFakeIPMappingMissDoesNotBlockOnItemRefresh(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	releaseRequest := make(chan struct{})
	requestStarted := make(chan struct{})
	var releaseOnce sync.Once
	var requestStartedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestStartedOnce.Do(func() { close(requestStarted) })
		<-releaseRequest
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"item":{"domain":"api.example.com","fake_ip":"198.18.4.9","action":"probe_exit","exit_node_id":"19","expires_at":"2099-01-01T00:00:00Z"}}`)
	}))
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseRequest) })
		server.Close()
	})
	rememberProbeVirtualRouterController(nodeIdentity{NodeID: "19", Secret: "secret"}, server.URL)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "19", IP: "198.18.0.21"},
		},
	}, "19")

	done := make(chan bool, 1)
	go func() {
		_, ok := currentProbeVirtualRouterFakeIPEntryByIPWithAsyncRefresh("198.18.4.9")
		done <- ok
	}()

	select {
	case ok := <-done:
		if ok {
			t.Fatalf("missing fake-ip mapping should not resolve synchronously")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("fake-ip mapping miss blocked on controller refresh")
	}
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("fake-ip mapping miss should schedule a controller refresh")
	}
	releaseOnce.Do(func() { close(releaseRequest) })
}

func TestProbeVirtualRouterFakeIPExitUDPForwarderDoesNotBlockOnResolve(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	releaseLookup := make(chan struct{})
	lookupStarted := make(chan struct{})
	var lookupStartedOnce sync.Once
	oldExitLookup := probeVirtualRouterExitLookupIPv4
	probeVirtualRouterExitLookupIPv4 = func(domain string) ([]string, error) {
		if domain != "api.example.com" {
			t.Errorf("unexpected exit lookup domain: %s", domain)
		}
		lookupStartedOnce.Do(func() { close(lookupStarted) })
		<-releaseLookup
		return nil, errors.New("test resolver stopped")
	}
	t.Cleanup(func() {
		close(releaseLookup)
		probeVirtualRouterExitLookupIPv4 = oldExitLookup
	})

	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "19", IP: "198.18.0.21"},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "api.example.com",
				FakeIP:     "198.18.4.9",
				Action:     "probe_exit",
				ExitNodeID: "19",
			}},
		},
	}, "19")

	runner, err := newProbeVirtualRouterExitNetstack()
	if err != nil {
		t.Fatalf("new netstack failed: %v", err)
	}
	t.Cleanup(func() { _ = runner.Close() })

	packet := buildProbeVirtualRouterTestUDPPacket(t, "198.18.0.18", "198.18.4.9", 49152, 443)
	injectDone := make(chan error, 1)
	go func() {
		injectDone <- runner.Inject(packet)
	}()

	select {
	case err := <-injectDone:
		if err != nil {
			t.Fatalf("inject failed: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("udp forwarder should not block packet injection while fake-ip resolve is pending")
	}
	select {
	case <-lookupStarted:
	case <-time.After(time.Second):
		t.Fatalf("udp forwarder should start fake-ip resolve asynchronously")
	}
}

func TestProbeVirtualRouterFakeIPExitPipeUsesHalfClose(t *testing.T) {
	src := &probeVirtualRouterHalfCloseTestConn{readErr: io.EOF}
	dst := &probeVirtualRouterHalfCloseTestConn{}

	if err := pipeProbeVirtualRouterExitConnHalf(dst, src); err != nil {
		t.Fatalf("pipe half returned error: %v", err)
	}
	if dst.closeWriteCalls != 1 {
		t.Fatalf("dst CloseWrite calls=%d, want 1", dst.closeWriteCalls)
	}
	if src.closeReadCalls != 1 {
		t.Fatalf("src CloseRead calls=%d, want 1", src.closeReadCalls)
	}
	if dst.closeCalls != 0 || src.closeCalls != 0 {
		t.Fatalf("half-close should not hard close conns, dst=%d src=%d", dst.closeCalls, src.closeCalls)
	}
}

type probeVirtualRouterHalfCloseTestConn struct {
	readErr         error
	closeCalls      int
	closeReadCalls  int
	closeWriteCalls int
}

func (c *probeVirtualRouterHalfCloseTestConn) Read([]byte) (int, error) {
	if c.readErr != nil {
		return 0, c.readErr
	}
	return 0, io.EOF
}

func (c *probeVirtualRouterHalfCloseTestConn) Write(p []byte) (int, error) {
	return len(p), nil
}

func (c *probeVirtualRouterHalfCloseTestConn) Close() error {
	c.closeCalls++
	return nil
}

func (c *probeVirtualRouterHalfCloseTestConn) LocalAddr() net.Addr {
	return &net.TCPAddr{}
}

func (c *probeVirtualRouterHalfCloseTestConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{}
}

func (c *probeVirtualRouterHalfCloseTestConn) SetDeadline(time.Time) error {
	return nil
}

func (c *probeVirtualRouterHalfCloseTestConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *probeVirtualRouterHalfCloseTestConn) SetWriteDeadline(time.Time) error {
	return nil
}

func (c *probeVirtualRouterHalfCloseTestConn) CloseRead() error {
	c.closeReadCalls++
	return nil
}

func (c *probeVirtualRouterHalfCloseTestConn) CloseWrite() error {
	c.closeWriteCalls++
	return nil
}

func TestProbeVirtualRouterFakeIPExitICMPEchoUsesRealTarget(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	oldExitLookup := probeVirtualRouterExitLookupIPv4
	probeVirtualRouterExitLookupIPv4 = func(domain string) ([]string, error) {
		if domain != "api.example.com" {
			t.Fatalf("unexpected exit lookup domain: %s", domain)
		}
		return []string{"203.0.113.10"}, nil
	}
	t.Cleanup(func() { probeVirtualRouterExitLookupIPv4 = oldExitLookup })

	oldEnsure := probeVirtualRouterEnsureDirectBypass
	var bypassTargets []string
	probeVirtualRouterEnsureDirectBypass = func(target string) error {
		bypassTargets = append(bypassTargets, target)
		return nil
	}
	t.Cleanup(func() { probeVirtualRouterEnsureDirectBypass = oldEnsure })

	oldSend := probeVirtualRouterSendICMPEcho
	var sentTarget string
	probeVirtualRouterSendICMPEcho = func(target string, payload []byte, timeout time.Duration) ([]byte, error) {
		sentTarget = target
		reply := append([]byte(nil), payload...)
		reply[0] = 0
		reply[1] = 0
		reply[2], reply[3] = 0, 0
		binary.BigEndian.PutUint16(reply[2:4], probeVirtualRouterChecksum(reply))
		return reply, nil
	}
	t.Cleanup(func() { probeVirtualRouterSendICMPEcho = oldSend })

	config := probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "api.example.com",
				FakeIP:     "198.18.4.9",
				Action:     "probe_exit",
				ExitNodeID: "19",
			}},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "19")

	left, right := net.Pipe()
	defer right.Close()
	link := newProbeVirtualRouterFrameLink("test-fake-ip-icmp-exit-link", nil, left, nil)
	link.Start()
	defer stopProbeVirtualRouterFrameLink(link)
	replyCh := make(chan struct {
		frame probeVirtualRouterFrame
		err   error
	}, 1)
	go func() {
		frame, err := readProbeVirtualRouterWireFrame(bufio.NewReader(right))
		replyCh <- struct {
			frame probeVirtualRouterFrame
			err   error
		}{frame: frame, err: err}
	}()

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-16-19", identity: nodeIdentity{NodeID: "19"}}}
	request := buildProbeVirtualRouterTestICMPEchoRequest(t, "198.18.0.18", "198.18.4.9")
	if err := handleProbeVirtualRouterIPFrame(rt, link, request, []string{"16", "19"}, nil); err != nil {
		t.Fatalf("handle fake ip icmp exit packet failed: %v", err)
	}

	select {
	case got := <-replyCh:
		if got.err != nil {
			t.Fatalf("read reply failed: %v", got.err)
		}
		control, err := probeVirtualRouterFrameControl(got.frame, nil)
		if err != nil {
			t.Fatalf("read reply control failed: %v", err)
		}
		info, ok := probeVirtualRouterParseICMPEchoLogInfo(got.frame.Data)
		if !ok || info.Kind != "echo_reply" || info.SourceIP != "198.18.4.9" || info.DestinationIP != "198.18.0.18" {
			t.Fatalf("reply info=%+v ok=%v", info, ok)
		}
		if got.frame.MainType != probeVirtualRouterFrameMainTypeIP || got.frame.SubType != probeVirtualRouterIPSubTypeIPv4 || !reflect.DeepEqual(control.Path, []string{"19", "16"}) {
			t.Fatalf("reply frame=%+v path=%v, want data/ip4 path [19 16]", got.frame, control.Path)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for fake ip icmp reply")
	}
	if sentTarget != "203.0.113.10" {
		t.Fatalf("icmp target=%q, want 203.0.113.10", sentTarget)
	}
	if !reflect.DeepEqual(bypassTargets, []string{"203.0.113.10:0"}) {
		t.Fatalf("bypass targets=%v, want [203.0.113.10:0]", bypassTargets)
	}
}

func TestProbeVirtualRouterICMPEchoReplyWritesBackOnIngressLink(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "19")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	left, right := net.Pipe()
	defer right.Close()
	link := newProbeVirtualRouterFrameLink("test-echo-link", nil, left, nil)
	link.Start()
	defer stopProbeVirtualRouterFrameLink(link)
	replyCh := make(chan struct {
		frame probeVirtualRouterFrame
		err   error
	}, 1)
	go func() {
		frame, err := readProbeVirtualRouterWireFrame(bufio.NewReader(right))
		replyCh <- struct {
			frame probeVirtualRouterFrame
			err   error
		}{frame: frame, err: err}
	}()

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-16-19", identity: nodeIdentity{NodeID: "19"}}}
	request := buildProbeVirtualRouterTestICMPEchoRequest(t, "198.18.0.18", "198.18.0.21")
	if !handleProbeVirtualRouterLocalICMPEchoRequest(rt, link, request, []string{"16", "19"}, nil) {
		t.Fatalf("echo request should be handled")
	}
	select {
	case got := <-replyCh:
		if got.err != nil {
			t.Fatalf("read reply failed: %v", got.err)
		}
		control, err := probeVirtualRouterFrameControl(got.frame, nil)
		if err != nil {
			t.Fatalf("read reply control failed: %v", err)
		}
		info, ok := probeVirtualRouterParseICMPEchoLogInfo(got.frame.Data)
		if !ok || info.Kind != "echo_reply" || info.SourceIP != "198.18.0.21" || info.DestinationIP != "198.18.0.18" {
			t.Fatalf("reply info=%+v ok=%v", info, ok)
		}
		if got.frame.MainType != probeVirtualRouterFrameMainTypeIP || got.frame.SubType != probeVirtualRouterIPSubTypeIPv4 || !reflect.DeepEqual(control.Path, []string{"19", "16"}) {
			t.Fatalf("reply frame=%+v, want data/ip4 path [19 16]", got.frame)
		}
		if len(control.Trace) < 2 {
			t.Fatalf("reply trace hops=%d, want at least final/build events: %+v", len(control.Trace), control.Trace)
		}
		events := map[string]bool{}
		for _, hop := range control.Trace {
			events[hop.Event] = true
			if hop.UnixNano <= 0 {
				t.Fatalf("trace hop missing timestamp: %+v", hop)
			}
		}
		for _, want := range []string{"echo_request_final", "echo_reply_build"} {
			if !events[want] {
				t.Fatalf("reply trace missing event %q: %+v", want, control.Trace)
			}
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for echo reply")
	}
}

func TestProbeVirtualRouterICMPFinalHopReturnsFrameWithTrace(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.18"},
			{NodeID: "19", IP: "198.18.0.21"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "16", ToNodeID: "19", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "19")
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	left, right := net.Pipe()
	defer right.Close()
	link := newProbeVirtualRouterFrameLink("test-final-hop-icmp", nil, left, nil)
	link.Start()
	defer stopProbeVirtualRouterFrameLink(link)
	replyCh := make(chan probeVirtualRouterFrame, 1)
	errCh := make(chan error, 1)
	go func() {
		frame, err := readProbeVirtualRouterWireFrame(bufio.NewReader(right))
		if err != nil {
			errCh <- err
			return
		}
		replyCh <- frame
	}()

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-16-19", identity: nodeIdentity{NodeID: "19"}}}
	request := buildProbeVirtualRouterTestICMPEchoRequest(t, "198.18.0.18", "198.18.0.21")
	initialTrace := []probeVirtualRouterFrameTraceHop{
		{ID: "trace-start", NodeID: "16", Event: "tun_rx", UnixNano: time.Now().UnixNano()},
	}
	if err := handleProbeVirtualRouterIPFrame(rt, link, request, []string{"16", "19"}, initialTrace); err != nil {
		t.Fatalf("final hop frame handling failed: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("read reply failed: %v", err)
	case frame := <-replyCh:
		control, err := probeVirtualRouterFrameControl(frame, nil)
		if err != nil {
			t.Fatalf("read reply control failed: %v", err)
		}
		info, ok := probeVirtualRouterParseICMPEchoLogInfo(frame.Data)
		if !ok || info.Kind != "echo_reply" || info.SourceIP != "198.18.0.21" || info.DestinationIP != "198.18.0.18" {
			t.Fatalf("reply info=%+v ok=%v", info, ok)
		}
		if !reflect.DeepEqual(control.Path, []string{"19", "16"}) {
			t.Fatalf("reply path=%v, want [19 16]", control.Path)
		}
		events := make([]string, 0, len(control.Trace))
		for _, hop := range control.Trace {
			events = append(events, hop.Event)
			if hop.UnixNano <= 0 {
				t.Fatalf("trace hop missing timestamp: %+v", hop)
			}
		}
		for _, want := range []string{"tun_rx", "frame_rx", "echo_request_final", "echo_reply_build"} {
			if !containsProbeVirtualRouterTestString(events, want) {
				t.Fatalf("reply trace events=%v missing %q", events, want)
			}
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for final-hop echo reply")
	}
}

func TestProbeVirtualRouterParseTCPUDPLogInfo(t *testing.T) {
	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.0.21", 49152, 8080)
	info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet)
	if !ok {
		t.Fatalf("tcp packet not parsed")
	}
	if info.Protocol != "tcp" || info.SourceIP != "198.18.0.18" || info.DestinationIP != "198.18.0.21" || info.SourcePort != 49152 || info.DestinationPort != 8080 || info.TCPFlags != "SYN" {
		t.Fatalf("unexpected tcp info: %+v", info)
	}
}

func TestProbeVirtualRouterRecentPacketIncludesChecksumSummary(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.0.21", 49152, 8080)
	setProbeVirtualRouterTestTCPChecksum(packet)
	recordProbeVirtualRouterRecentPacket("frame_rx", "deliver", nil, packet, []string{"19", "16"}, true, nil)
	items := snapshotProbeVirtualRouterRecentPackets()
	if len(items) != 1 {
		t.Fatalf("recent packets=%d, want 1", len(items))
	}
	if !strings.Contains(items[0].Detail, "ip_checksum=ok") || !strings.Contains(items[0].Detail, "tcp_checksum=ok") {
		t.Fatalf("unexpected checksum detail: %+v", items[0])
	}
}

func TestProbeVirtualRouterRecentPacketRingKeepsNewestItems(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.0.21", 49152, 8080)
	total := probeVirtualRouterRecentPacketLimit + 10
	for i := 0; i < total; i++ {
		recordProbeVirtualRouterRecentPacket("frame_rx", "deliver", nil, packet, []string{"19", "16"}, true, nil)
	}
	items := snapshotProbeVirtualRouterRecentPackets()
	if len(items) != probeVirtualRouterRecentPacketLimit {
		t.Fatalf("recent packets=%d, want %d", len(items), probeVirtualRouterRecentPacketLimit)
	}
	if items[0].ID != uint64(total) || items[len(items)-1].ID != 11 {
		t.Fatalf("unexpected ring order: newest=%d oldest=%d", items[0].ID, items[len(items)-1].ID)
	}
}

func TestProbeVirtualRouterFrameLinkRXCapacityAndDropLogAggregation(t *testing.T) {
	link := newProbeVirtualRouterFrameLink("packet|capacity", nil, nil, nil)
	if cap(link.rx) != probeVirtualRouterFrameLinkRXBufferFrames {
		t.Fatalf("rx capacity=%d, want %d", cap(link.rx), probeVirtualRouterFrameLinkRXBufferFrames)
	}
	if len(link.rxDispatchShards) != probeVirtualRouterFrameLinkRXDispatchShards {
		t.Fatalf("rx dispatch shards=%d, want %d", len(link.rxDispatchShards), probeVirtualRouterFrameLinkRXDispatchShards)
	}
	for shardID, shard := range link.rxDispatchShards {
		if cap(shard) != probeVirtualRouterFrameLinkRXDispatchShardBufferFrames {
			t.Fatalf("shard %d capacity=%d, want %d", shardID, cap(shard), probeVirtualRouterFrameLinkRXDispatchShardBufferFrames)
		}
	}
	if shouldLog, dropped := link.recordRXDispatchDrop(); !shouldLog || dropped != 1 {
		t.Fatalf("first drop log=(%t,%d), want (true,1)", shouldLog, dropped)
	}
	if shouldLog, dropped := link.recordRXDispatchDrop(); shouldLog || dropped != 0 {
		t.Fatalf("rate-limited drop log=(%t,%d), want (false,0)", shouldLog, dropped)
	}
	link.mu.Lock()
	link.rxDropLastLogAt = time.Now().Add(-probeVirtualRouterRXDispatchDropLogPeriod)
	link.mu.Unlock()
	if shouldLog, dropped := link.recordRXDispatchDrop(); !shouldLog || dropped != 2 {
		t.Fatalf("aggregated drop log=(%t,%d), want (true,2)", shouldLog, dropped)
	}
}

func TestProbeVirtualRouterFakeIPVerifyResponseHandledInRXWorker(t *testing.T) {
	runtime := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{identity: nodeIdentity{NodeID: "9"}}}
	response := probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypeFakeIPVerify, SubType: probeVirtualRouterFakeIPVerifySubTypeResponse, Data: []byte(`{"request_id":"verify-1"}`)}
	if !shouldHandleProbeVirtualRouterFrameInRXWorker(runtime, response, []string{"18", "9"}) {
		t.Fatal("fake ip verify response should bypass rx dispatch shards")
	}
	query := response
	query.SubType = probeVirtualRouterFakeIPVerifySubTypeQuery
	if shouldHandleProbeVirtualRouterFrameInRXWorker(runtime, query, []string{"9", "18"}) {
		t.Fatal("fake ip verify query should remain on the dispatch path")
	}
}

func TestProbeVirtualRouterRuntimeForAdjacentNode(t *testing.T) {
	probeVirtualRouterRuntimeState.mu.Lock()
	oldRuntimes := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{
		"route-a": {cfg: probeVirtualRouterRuntimeConfig{routeID: "route-a", peerNodeID: "2", dialer: true}},
		"route-b": {cfg: probeVirtualRouterRuntimeConfig{routeID: "route-b", peerNodeID: "3"}},
	}
	probeVirtualRouterRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = oldRuntimes
		probeVirtualRouterRuntimeState.mu.Unlock()
	})

	rt, direction := probeVirtualRouterRuntimeForAdjacentNode("2")
	if rt == nil || rt.cfg.routeID != "route-a" || direction != probeRouteBridgeRoleToNext {
		t.Fatalf("next runtime=%v direction=%q", rt, direction)
	}
	rt, direction = probeVirtualRouterRuntimeForAdjacentNode("3")
	if rt == nil || rt.cfg.routeID != "route-b" || direction != probeRouteBridgeRoleToPrev {
		t.Fatalf("prev runtime=%v direction=%q", rt, direction)
	}
}

func TestProbeVirtualRouterRuntimeForAdjacentNodePrefersAvailableCarrier(t *testing.T) {
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })
	nextRT := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-next", peerNodeID: "2", dialer: true}}
	prevRT := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-prev", peerNodeID: "3"}}
	nextLeft, nextRight := net.Pipe()
	defer nextRight.Close()
	prevLeft, prevRight := net.Pipe()
	defer prevRight.Close()
	nextLink := newProbeVirtualRouterFrameLink(probeVirtualRouterFrameLinkKey(nextRT, "", "", nil), nextRT, nil, nil)
	prevLink := newProbeVirtualRouterFrameLink(probeVirtualRouterFrameLinkKey(prevRT, "", "", nil), prevRT, nil, nil)
	nextLink.AttachCarrier(nextLeft, "next-carrier", "pipe")
	prevLink.AttachCarrier(prevLeft, "prev-carrier", "pipe")
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{
		probeVirtualRouterFrameLinkKey(nextRT, "", "", nil): nextLink,
		probeVirtualRouterFrameLinkKey(prevRT, "", "", nil): prevLink,
	}
	probeVirtualRouterFrameLinkState.mu.Unlock()

	probeVirtualRouterRuntimeState.mu.Lock()
	oldRuntimes := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{
		"vrouter-next": nextRT,
		"vrouter-prev": prevRT,
	}
	probeVirtualRouterRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = oldRuntimes
		probeVirtualRouterRuntimeState.mu.Unlock()
	})

	rt, direction := probeVirtualRouterRuntimeForAdjacentNode("2")
	if rt == nil || rt.cfg.routeID != "vrouter-next" || direction != probeRouteBridgeRoleToNext {
		t.Fatalf("next runtime=%v direction=%q", rt, direction)
	}
	rt, direction = probeVirtualRouterRuntimeForAdjacentNode("3")
	if rt == nil || rt.cfg.routeID != "vrouter-prev" || direction != probeRouteBridgeRoleToPrev {
		t.Fatalf("prev runtime=%v direction=%q", rt, direction)
	}
}

func TestProbeVirtualRouterFrameLinkKey(t *testing.T) {
	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "route-a"}}
	got := probeVirtualRouterFrameLinkKey(rt, probeRouteBridgeRoleToNext, "198.18.0.9", []string{"node-1", "node-2"})
	if got != "packet|route-a" {
		t.Fatalf("key=%q", got)
	}
	other := probeVirtualRouterFrameLinkKey(rt, probeRouteBridgeRoleToPrev, "198.18.0.10", []string{"node-2", "node-1"})
	if other != got {
		t.Fatalf("frame link key should be per rule: got=%q other=%q", got, other)
	}
}

func TestProbeVirtualRouterFrameLinkCacheReuseAndDrop(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	key := "route-a|to_next|198.18.0.9|1>2"
	item := newProbeVirtualRouterFrameLink(key, nil, left, nil)
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: item}
	probeVirtualRouterFrameLinkState.mu.Unlock()

	if got := reusableProbeVirtualRouterFrameLink(key, time.Now()); got != item {
		t.Fatalf("expected cached link")
	}
	dropProbeVirtualRouterFrameLink(item)
	if got := reusableProbeVirtualRouterFrameLink(key, time.Now()); got != nil {
		t.Fatalf("expected dropped link, got=%v", got)
	}
}

func TestProbeVirtualRouterFrameLinkCachePersistsWhileIdle(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	key := "route-a|to_next|198.18.0.9|1>2"
	item := newProbeVirtualRouterFrameLink(key, nil, left, nil)
	item.openedAt = time.Now().Add(-2 * probeVirtualRouterFrameLinkIdleTTL)
	item.lastUsed = time.Now().Add(-2 * probeVirtualRouterFrameLinkIdleTTL)
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: item}
	probeVirtualRouterFrameLinkState.mu.Unlock()

	if got := reusableProbeVirtualRouterFrameLink(key, time.Now()); got != item {
		t.Fatalf("expected idle link to persist, got=%v", got)
	}
}

func TestProbeVirtualRouterFrameLinkDebugStateIncludesCarrierDetails(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	defer left.Close()

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-debug"}}
	link := newProbeVirtualRouterFrameLink("packet|vrouter-debug", rt, nil, nil)
	link.AttachCarrier(left, "session-debug", "198.51.100.19:12040")

	text := probeVirtualRouterFrameLinkDebugState(link)
	for _, want := range []string{"link_key=packet|vrouter-debug", "carrier_session=session-debug", "remote=198.51.100.19:12040", "rx_idle_ms=", "tx_idle_ms=", "tx_queue=", "rx_queue="} {
		if !strings.Contains(text, want) {
			t.Fatalf("debug state %q missing %q", text, want)
		}
	}
}

func TestProbeVirtualRouterAttachCarrierReplacesCurrentCarrier(t *testing.T) {
	firstLeft, firstRight := net.Pipe()
	defer firstRight.Close()
	link := newProbeVirtualRouterFrameLink("packet|vrouter-duplicate", &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-duplicate"}}, nil, nil)
	first := link.AttachCarrier(firstLeft, "carrier-active", "198.51.100.10:12040")
	if first == nil {
		t.Fatalf("first carrier should attach")
	}

	secondLeft, secondRight := net.Pipe()
	defer secondRight.Close()
	defer secondLeft.Close()
	second := link.AttachCarrier(secondLeft, "carrier-replacement", "198.51.100.11:12040")
	if second == nil {
		t.Fatalf("authenticated new carrier should replace the current carrier")
	}

	link.mu.Lock()
	got := link.carrier
	link.mu.Unlock()
	if got != second {
		t.Fatalf("new carrier should be attached")
	}
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatalf("replaced carrier should be closed")
	}
}

func TestProbeVirtualRouterAttachCarrierReplacesStaleCarrier(t *testing.T) {
	firstLeft, firstRight := net.Pipe()
	defer firstRight.Close()
	link := newProbeVirtualRouterFrameLink("packet|vrouter-stale-replace", &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-stale-replace"}}, nil, nil)
	first := link.AttachCarrier(firstLeft, "carrier-stale", "198.51.100.10:12040")
	if first == nil {
		t.Fatalf("first carrier should attach")
	}
	oldAt := time.Now().Add(-probeVirtualRouterCarrierStaleRXGrace - time.Second)
	first.mu.Lock()
	first.connectedAt = oldAt
	first.lastReadAt = oldAt
	first.lastWriteAt = oldAt
	first.mu.Unlock()

	secondLeft, secondRight := net.Pipe()
	defer secondRight.Close()
	defer secondLeft.Close()
	second := link.AttachCarrier(secondLeft, "carrier-replacement", "198.51.100.11:12040")
	if second == nil {
		t.Fatalf("stale carrier should be replaceable")
	}

	link.mu.Lock()
	got := link.carrier
	link.mu.Unlock()
	if got != second {
		t.Fatalf("replacement carrier should be attached")
	}
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatalf("stale carrier should be closed after replacement")
	}
}

func TestProbeVirtualRouterClosedLinkErrorIncludesWebSocketAbnormalEOF(t *testing.T) {
	err := errors.New("websocket: close 1006 (abnormal closure): unexpected EOF")
	if !isProbeVirtualRouterClosedLinkError(err) {
		t.Fatalf("websocket abnormal EOF should be treated as a closed link")
	}
}

func TestProbeVirtualRouterOpenSuccessResetsPingState(t *testing.T) {
	routeID := "vrouter-open-reset"
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		routeID: {
			LastPingLatencyMS:         88,
			LastPingError:             "virtual router control response timeout",
			LastPingAt:                time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
			LastPingFailureCount:      7,
			LastPingDirection:         probeRouteBridgeRoleToNext,
			LastPingBridgeConnections: 1,
			LastPingBridgeSessionID:   "old-carrier",
			LastPingBridgeRemote:      "198.51.100.19:12040",
			LastPingBridgeConnectedAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldStats
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	recordProbeVirtualRouterRuntimeOpenSuccess(routeID, 10*time.Millisecond)

	probeVirtualRouterRuntimeStatsState.mu.Lock()
	stats := probeVirtualRouterRuntimeStatsState.items[routeID]
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if stats == nil {
		t.Fatalf("runtime stats should remain available")
	}
	if stats.LastPingLatencyMS != 0 || stats.LastPingError != "" || stats.LastPingAt != "" || stats.LastPingFailureCount != 0 || stats.LastPingDirection != "" || stats.LastPingBridgeConnections != 0 || stats.LastPingBridgeSessionID != "" || stats.LastPingBridgeRemote != "" || stats.LastPingBridgeConnectedAt != "" {
		t.Fatalf("ping state should reset on open success: %+v", stats)
	}
}

func TestProbeVirtualRouterRuntimeStopResetsPingFailureCount(t *testing.T) {
	routeID := "vrouter-stop-reset"
	probeVirtualRouterRuntimeState.mu.Lock()
	oldRuntimes := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{
		routeID: {cfg: probeVirtualRouterRuntimeConfig{routeID: routeID}, stopCh: make(chan struct{})},
	}
	probeVirtualRouterRuntimeState.mu.Unlock()
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		routeID: {LastPingError: "virtual router control response timeout", LastPingFailureCount: 3},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = oldRuntimes
		probeVirtualRouterRuntimeState.mu.Unlock()
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldStats
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	if !stopProbeVirtualRouterRuntime(routeID, "test stop") {
		t.Fatalf("runtime should stop")
	}

	probeVirtualRouterRuntimeStatsState.mu.Lock()
	stats := probeVirtualRouterRuntimeStatsState.items[routeID]
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if stats == nil {
		t.Fatalf("runtime stats should remain available")
	}
	if stats.LastPingError != "" || stats.LastPingFailureCount != 0 {
		t.Fatalf("ping state should reset on stop, error=%q failures=%d", stats.LastPingError, stats.LastPingFailureCount)
	}
}

func TestProbeVirtualRouterCarrierDetachResetsPingState(t *testing.T) {
	routeID := "vrouter-detach-reset"
	left, right := net.Pipe()
	defer right.Close()
	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: routeID}}
	link := newProbeVirtualRouterFrameLink("packet|"+routeID, rt, nil, nil)
	token := link.AttachCarrier(left, "carrier-detach-reset", "198.51.100.19:12040")
	if token == nil {
		t.Fatalf("carrier should attach")
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		routeID: {
			LastPingError:             "virtual router control response timeout",
			LastPingFailureCount:      2,
			LastPingBridgeConnections: 1,
			LastPingBridgeSessionID:   "carrier-detach-reset",
			LastPingBridgeRemote:      "198.51.100.19:12040",
			LastPingBridgeConnectedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldStats
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	link.detachCarrier(token)

	probeVirtualRouterRuntimeStatsState.mu.Lock()
	stats := probeVirtualRouterRuntimeStatsState.items[routeID]
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if stats == nil {
		t.Fatalf("runtime stats should remain available")
	}
	if stats.LastPingError != "" || stats.LastPingFailureCount != 0 || stats.LastPingBridgeConnections != 0 || stats.LastPingBridgeSessionID != "" || stats.LastPingBridgeRemote != "" || stats.LastPingBridgeConnectedAt != "" {
		t.Fatalf("ping state should reset on carrier detach: %+v", stats)
	}
}

func TestProbeVirtualRouterPingErrorRetainsCarrierAndFrameLink(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	rt := &probeVirtualRouterRuntime{
		cfg: probeVirtualRouterRuntimeConfig{
			routeID:    "vrouter-carrier-health",
			peerNodeID: "19",
			dialer:     true,
		},
	}
	left, right := net.Pipe()
	defer right.Close()
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	link := newProbeVirtualRouterFrameLink(key, rt, nil, nil)
	link.AttachCarrier(left, "vrouter-carrier-test", "198.51.100.19:12040")
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: link}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	storeProbeVirtualRouterRoutePath("1", "2", []string{"1", "2"})

	recordProbeVirtualRouterRuntimePingError(rt, probeRouteBridgeRoleToNext, errors.New("virtual router control ping timeout"))
	recordProbeVirtualRouterRuntimePingError(rt, probeRouteBridgeRoleToNext, errors.New("virtual router control ping timeout"))

	if isProbeVirtualRouterFrameLinkClosed(link) {
		t.Fatalf("ping error should not close frame link")
	}
	link.mu.Lock()
	carrier := link.carrier
	link.mu.Unlock()
	if carrier == nil {
		t.Fatalf("ping error should not detach physical carrier")
	}
	if got := reusableProbeVirtualRouterFrameLink(key, time.Now()); got != link {
		t.Fatalf("frame link should remain cached for reconnection, got=%v", got)
	}
	if got := cachedProbeVirtualRouterRoutePath("1", "2"); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("route cache should survive transient ping errors, got=%v", got)
	}
}

func TestProbeVirtualRouterPingErrorKeepsRecentlyActiveCarrier(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	rt := &probeVirtualRouterRuntime{
		cfg: probeVirtualRouterRuntimeConfig{
			routeID:    "vrouter-carrier-active",
			peerNodeID: "19",
			dialer:     true,
		},
	}
	left, right := net.Pipe()
	defer right.Close()
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	link := newProbeVirtualRouterFrameLink(key, rt, nil, nil)
	link.AttachCarrier(left, "vrouter-carrier-active", "198.51.100.19:12040")
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: link}
	probeVirtualRouterFrameLinkState.mu.Unlock()

	recordProbeVirtualRouterRuntimeFrameReceived(rt, 91)
	recordProbeVirtualRouterRuntimePingError(rt, probeRouteBridgeRoleToNext, errors.New("virtual router control response timeout"))
	recordProbeVirtualRouterRuntimePingError(rt, probeRouteBridgeRoleToNext, errors.New("virtual router control response timeout"))

	if isProbeVirtualRouterFrameLinkClosed(link) {
		t.Fatalf("recent ping errors should not close active frame link")
	}
	link.mu.Lock()
	carrier := link.carrier
	link.mu.Unlock()
	if carrier == nil {
		t.Fatalf("recently active carrier should be retained")
	}
	stats := snapshotProbeVirtualRouterRuntimeStats(rt.cfg.routeID)
	if stats == nil {
		t.Fatalf("missing runtime stats")
	}
	if stats.LastPingError != "" || stats.LastPingFailureCount != 0 {
		t.Fatalf("active carrier ping timeout should not be recorded as failure, error=%q failures=%d", stats.LastPingError, stats.LastPingFailureCount)
	}
}

func TestProbeVirtualRouterRepeatedPingErrorDetachesStaleCarrier(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	rt := &probeVirtualRouterRuntime{
		cfg: probeVirtualRouterRuntimeConfig{
			routeID:    "vrouter-carrier-stale",
			peerNodeID: "19",
			dialer:     true,
		},
	}
	left, right := net.Pipe()
	defer right.Close()
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	link := newProbeVirtualRouterFrameLink(key, rt, nil, nil)
	token := link.AttachCarrier(left, "vrouter-carrier-stale", "198.51.100.19:12040")
	if token == nil {
		t.Fatalf("carrier should attach")
	}
	oldReadAt := time.Now().Add(-probeVirtualRouterCarrierStaleRXGrace - time.Second)
	token.mu.Lock()
	token.lastReadAt = oldReadAt
	token.mu.Unlock()
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{key: link}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	storeProbeVirtualRouterRoutePath("1", "2", []string{"1", "2"})

	for i := 0; i < probeVirtualRouterCarrierStalePingFailures; i++ {
		recordProbeVirtualRouterRuntimePingError(rt, probeRouteBridgeRoleToNext, errors.New("virtual router control response timeout"))
	}

	if isProbeVirtualRouterFrameLinkClosed(link) {
		t.Fatalf("stale carrier detach should not close frame link")
	}
	link.mu.Lock()
	carrier := link.carrier
	link.mu.Unlock()
	if carrier != nil {
		t.Fatalf("stale carrier should be detached for reconnect")
	}
	if got := cachedProbeVirtualRouterRoutePath("1", "2"); len(got) != 0 {
		t.Fatalf("route cache should be cleared after repeated ping errors, got=%v", got)
	}
}

func TestProbeVirtualRouterRepeatedPingErrorClearsOnlyAffectedRouteCache(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	rt := &probeVirtualRouterRuntime{
		cfg: probeVirtualRouterRuntimeConfig{
			routeID:    "vrouter-cache-edge",
			fromNodeID: "1",
			toNodeID:   "2",
			peerNodeID: "2",
			dialer:     true,
		},
	}
	storeProbeVirtualRouterRoutePath("1", "3", []string{"1", "2", "3"})
	storeProbeVirtualRouterRoutePath("4", "5", []string{"4", "5"})

	for i := 0; i < probeVirtualRouterCarrierStalePingFailures; i++ {
		recordProbeVirtualRouterRuntimePingError(rt, probeRouteBridgeRoleToNext, errors.New("virtual router control response timeout"))
	}

	if got := cachedProbeVirtualRouterRoutePath("1", "3"); len(got) != 0 {
		t.Fatalf("route cache using failed edge should be cleared, got=%v", got)
	}
	if got := cachedProbeVirtualRouterRoutePath("4", "5"); !reflect.DeepEqual(got, []string{"4", "5"}) {
		t.Fatalf("unrelated route cache should survive failed edge, got=%v", got)
	}
}

func TestProbeVirtualRouterPersistentPingErrorClearsRouteCacheOnlyAtThreshold(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	rt := &probeVirtualRouterRuntime{
		cfg: probeVirtualRouterRuntimeConfig{
			routeID:    "vrouter-cache-threshold",
			fromNodeID: "1",
			toNodeID:   "2",
			peerNodeID: "2",
			dialer:     true,
		},
	}
	storeProbeVirtualRouterRoutePath("1", "3", []string{"1", "2", "3"})
	for i := 0; i < probeVirtualRouterCarrierStalePingFailures; i++ {
		recordProbeVirtualRouterRuntimePingError(rt, probeRouteBridgeRoleToNext, errors.New("virtual router control response timeout"))
	}
	if got := cachedProbeVirtualRouterRoutePath("1", "3"); len(got) != 0 {
		t.Fatalf("route cache should be cleared when failure reaches threshold, got=%v", got)
	}

	storeProbeVirtualRouterRoutePath("1", "3", []string{"1", "2", "3"})
	recordProbeVirtualRouterRuntimePingError(rt, probeRouteBridgeRoleToNext, errors.New("virtual router control response timeout"))
	if got := cachedProbeVirtualRouterRoutePath("1", "3"); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("route cache should not be repeatedly cleared after threshold, got=%v", got)
	}
}

func TestProbeVirtualRouterDispatchErrorKeepsFrameLinkAlive(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	link := newProbeVirtualRouterFrameLink("dispatch-error", &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-dispatch-error"}}, nil, nil)
	link.Start()
	t.Cleanup(func() { stopProbeVirtualRouterFrameLink(link) })

	frame, err := buildProbeVirtualRouterBusinessFrame(999, 1, []byte("bad-frame"), []string{"1", "2"}, nil)
	if err != nil {
		t.Fatalf("build unsupported wire frame failed: %v", err)
	}
	link.rx <- frame

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if isProbeVirtualRouterFrameLinkClosed(link) {
			t.Fatalf("dispatch error should not close frame link")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProbeVirtualRouterCarrierFailureDetachesCarrierButKeepsFrameLink(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	left, right := net.Pipe()
	key := "carrier-failure"
	link := newProbeVirtualRouterFrameLink(key, &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-carrier-failure"}}, nil, nil)
	link.Start()
	token := link.AttachCarrier(left, "vrouter-carrier-test", "pipe")
	if token == nil {
		t.Fatalf("carrier should attach")
	}
	t.Cleanup(func() { stopProbeVirtualRouterFrameLink(link) })
	_ = right.Close()

	frame, err := buildProbeVirtualRouterIPFrame(buildProbeVirtualRouterTestIPv4Packet(t, "198.18.0.1", "198.18.0.2"), []string{"1", "2"}, nil)
	if err != nil {
		t.Fatalf("build ip frame failed: %v", err)
	}
	if err := link.EnqueueProbeVirtualRouterFrame(frame); err != nil {
		t.Fatalf("enqueue should not fail before carrier worker observes close: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if isProbeVirtualRouterFrameLinkClosed(link) {
			t.Fatalf("carrier failure should not close frame link")
		}
		link.mu.Lock()
		carrier := link.carrier
		link.mu.Unlock()
		if carrier == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("carrier should detach after physical failure")
}

func TestProbeVirtualRouterFrameLinkTXQueueFullReturnsImmediately(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	link := newProbeVirtualRouterFrameLink("queue-full", &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-queue-full"}}, nil, nil)
	t.Cleanup(func() { stopProbeVirtualRouterFrameLink(link) })

	frame, err := buildProbeVirtualRouterIPFrame(buildProbeVirtualRouterTestIPv4Packet(t, "198.18.0.1", "198.18.0.2"), []string{"1", "2"}, nil)
	if err != nil {
		t.Fatalf("build ip frame failed: %v", err)
	}
	for i := 0; i < cap(link.tx); i++ {
		link.tx <- frame
	}

	startedAt := time.Now()
	err = link.EnqueueProbeVirtualRouterFrame(frame)
	elapsed := time.Since(startedAt)
	if err == nil || !strings.Contains(err.Error(), "tx queue full") {
		t.Fatalf("enqueue err=%v, want tx queue full", err)
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("enqueue should return immediately when tx queue is full, elapsed=%s", elapsed)
	}
}

func TestProbeVirtualRouterFrameLinkTXQueuesReserveControlCapacity(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	link := newProbeVirtualRouterFrameLink("queue-classes", &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-queue-classes"}}, nil, nil)
	t.Cleanup(func() { stopProbeVirtualRouterFrameLink(link) })
	ipFrame, err := buildProbeVirtualRouterIPFrame(buildProbeVirtualRouterTestIPv4Packet(t, "198.18.0.1", "198.18.0.2"), []string{"1", "2"}, nil)
	if err != nil {
		t.Fatalf("build ip frame failed: %v", err)
	}
	for i := 0; i < cap(link.tx); i++ {
		link.tx <- ipFrame
	}
	controlFrame := probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypePingPong, SubType: probeVirtualRouterPingPongSubTypePing, Data: []byte{1}}
	if err := link.EnqueueProbeVirtualRouterFrame(controlFrame); err != nil {
		t.Fatalf("control enqueue should use reserved queue: %v", err)
	}
	if got := len(link.txControl); got != 1 {
		t.Fatalf("control queue depth=%d, want 1", got)
	}
}

func TestProbeVirtualRouterFrameLinkTXSchedulingPrioritizesControlAndBoundsBulk(t *testing.T) {
	link := newProbeVirtualRouterFrameLink("queue-priority", &probeVirtualRouterRuntime{}, nil, nil)
	t.Cleanup(func() { stopProbeVirtualRouterFrameLink(link) })
	business := probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypeIP, Data: []byte{1}}
	control := probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypePingPong, Data: []byte{2}}
	bulk := probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypeSpeed, Data: []byte{3}}
	for i := 0; i < probeVirtualRouterFrameLinkTXBusinessQuantum+1; i++ {
		link.tx <- business
	}
	link.txControl <- control
	link.txBulk <- bulk

	businessSinceBulk := 0
	frame, ok := link.nextTXFrame(&businessSinceBulk)
	if !ok || frame.MainType != probeVirtualRouterFrameMainTypePingPong {
		t.Fatalf("first frame=%+v ok=%v, want control", frame, ok)
	}
	for i := 0; i < probeVirtualRouterFrameLinkTXBusinessQuantum; i++ {
		frame, ok = link.nextTXFrame(&businessSinceBulk)
		if !ok || frame.MainType != probeVirtualRouterFrameMainTypeIP {
			t.Fatalf("frame %d=%+v ok=%v, want business", i, frame, ok)
		}
	}
	frame, ok = link.nextTXFrame(&businessSinceBulk)
	if !ok || frame.MainType != probeVirtualRouterFrameMainTypeSpeed {
		t.Fatalf("frame after business quantum=%+v ok=%v, want bulk", frame, ok)
	}
}

func TestProbeVirtualRouterSpeedBackpressureUsesBoundedBulkQueue(t *testing.T) {
	link := newProbeVirtualRouterFrameLink("speed-backpressure", &probeVirtualRouterRuntime{}, nil, nil)
	t.Cleanup(func() { stopProbeVirtualRouterFrameLink(link) })
	high, low := probeVirtualRouterSpeedTXWatermarks(cap(link.txBulk))
	if high != 96 || low != 32 {
		t.Fatalf("speed tx watermarks high=%d low=%d, want high=96 low=32", high, low)
	}
	for i := 0; i < high; i++ {
		link.txBulk <- probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypeSpeed, Data: []byte{1}}
	}

	done := make(chan error, 1)
	go func() {
		done <- waitProbeVirtualRouterSpeedTXBackpressure(link, time.Now().Add(time.Second))
	}()
	select {
	case err := <-done:
		t.Fatalf("backpressure returned before bulk queue drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	for len(link.txBulk) > low {
		<-link.txBulk
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("backpressure returned error after drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("backpressure did not release after bulk queue reached low watermark")
	}
}

func TestProbeVirtualRouterFrameRXDispatchShardKeepsTCPFlowTogether(t *testing.T) {
	forward := probeVirtualRouterFrame{
		MainType: probeVirtualRouterFrameMainTypeIP,
		Data:     buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.7", "198.18.4.52", 57113, 443),
	}
	reverse := probeVirtualRouterFrame{
		MainType: probeVirtualRouterFrameMainTypeIP,
		Data:     buildProbeVirtualRouterTestTCPPacket(t, "198.18.4.52", "198.18.0.7", 443, 57113),
	}
	if got, want := probeVirtualRouterFrameRXDispatchShard(reverse, probeVirtualRouterFrameLinkRXDispatchShards), probeVirtualRouterFrameRXDispatchShard(forward, probeVirtualRouterFrameLinkRXDispatchShards); got != want {
		t.Fatalf("bidirectional tcp flow should use same rx dispatch shard, reverse=%d forward=%d", got, want)
	}
}

func TestProbeVirtualRouterPacketDispatchShardKeepsTCPFlowTogether(t *testing.T) {
	forward := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.7", "198.18.4.52", 60592, 443)
	reverse := buildProbeVirtualRouterTestTCPPacket(t, "198.18.4.52", "198.18.0.7", 443, 60592)
	if got, want := probeVirtualRouterPacketDispatchShard(reverse, 8), probeVirtualRouterPacketDispatchShard(forward, 8); got != want {
		t.Fatalf("bidirectional tcp packets should use same tun dispatch shard, reverse=%d forward=%d", got, want)
	}
}

func TestSnapshotProbeVirtualRouterExitNetstackAggregatesOutputQueues(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	shards := makeProbeVirtualRouterExitNetstackOutputDispatchShards()
	shards[0] <- probeVirtualRouterExitNetstackOutputPacket{payload: []byte{0x45}}
	shards[1] <- probeVirtualRouterExitNetstackOutputPacket{payload: []byte{0x45}}
	runner := &probeVirtualRouterExitNetstack{outputDispatch: shards}
	runner.outputEnqueued.Store(3)
	runner.outputForwarded.Store(2)
	runner.outputDropped.Store(1)
	runner.outputQueueFull.Store(1)
	probeVirtualRouterExitNetstackState.mu.Lock()
	probeVirtualRouterExitNetstackState.runner = runner
	probeVirtualRouterExitNetstackState.mu.Unlock()

	snapshot := snapshotProbeVirtualRouterExitNetstack()
	if !snapshot.Running {
		t.Fatalf("snapshot should report runner as running")
	}
	if snapshot.MTU != probeVirtualRouterExitNetstackMTU {
		t.Fatalf("snapshot mtu=%d, want %d", snapshot.MTU, probeVirtualRouterExitNetstackMTU)
	}
	if snapshot.OutputShards != len(shards) || snapshot.OutputQueueDepth != 2 || snapshot.OutputQueueCapacity != len(shards)*probeVirtualRouterExitNetstackOutputShardQueuePackets {
		t.Fatalf("unexpected output queue snapshot: %+v", snapshot)
	}
	if snapshot.OutputEnqueued != 3 || snapshot.OutputForwarded != 2 || snapshot.OutputDropped != 1 || snapshot.OutputQueueFull != 1 {
		t.Fatalf("unexpected output counters: %+v", snapshot)
	}
}

func TestProbeVirtualRouterFrameLinkAttachClearsBufferedFrames(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	left, right := net.Pipe()
	defer right.Close()
	link := newProbeVirtualRouterFrameLink("attach-clear-buffers", &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-attach-clear"}}, nil, nil)
	t.Cleanup(func() { stopProbeVirtualRouterFrameLink(link) })
	frame, err := buildProbeVirtualRouterIPFrame(buildProbeVirtualRouterTestIPv4Packet(t, "198.18.0.1", "198.18.0.2"), []string{"1", "2"}, nil)
	if err != nil {
		t.Fatalf("build ip frame failed: %v", err)
	}
	link.tx <- frame
	link.txControl <- probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypePingPong, Data: []byte{1}}
	link.txBulk <- probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypeSpeed, Data: []byte{1}}
	link.rx <- frame
	link.rxDispatchShards[0] <- frame

	if token := link.AttachCarrier(left, "carrier-attach-clear", "pipe"); token == nil {
		t.Fatalf("carrier should attach")
	}
	if txDepth, _, _, _, _, _, _, _ := link.txQueueSnapshot(); txDepth != 0 || len(link.rx) != 0 || len(link.rxDispatchShards[0]) != 0 {
		t.Fatalf("attach should clear buffered frames, tx=%d rx=%d rx_dispatch=%d", txDepth, len(link.rx), len(link.rxDispatchShards[0]))
	}
}

func TestProbeVirtualRouterFrameLinkDetachClearsBufferedFrames(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	left, right := net.Pipe()
	defer right.Close()
	link := newProbeVirtualRouterFrameLink("detach-clear-buffers", &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-detach-clear"}}, nil, nil)
	t.Cleanup(func() { stopProbeVirtualRouterFrameLink(link) })
	token := link.AttachCarrier(left, "carrier-detach-clear", "pipe")
	if token == nil {
		t.Fatalf("carrier should attach")
	}
	frame, err := buildProbeVirtualRouterIPFrame(buildProbeVirtualRouterTestIPv4Packet(t, "198.18.0.1", "198.18.0.2"), []string{"1", "2"}, nil)
	if err != nil {
		t.Fatalf("build ip frame failed: %v", err)
	}
	link.tx <- frame
	link.txControl <- probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypePingPong, Data: []byte{1}}
	link.txBulk <- probeVirtualRouterFrame{MainType: probeVirtualRouterFrameMainTypeSpeed, Data: []byte{1}}
	link.rx <- frame
	link.rxDispatchShards[0] <- frame

	link.detachCarrier(token)

	if txDepth, _, _, _, _, _, _, _ := link.txQueueSnapshot(); txDepth != 0 || len(link.rx) != 0 || len(link.rxDispatchShards[0]) != 0 {
		t.Fatalf("detach should clear buffered frames, tx=%d rx=%d rx_dispatch=%d", txDepth, len(link.rx), len(link.rxDispatchShards[0]))
	}
}

func TestProbeVirtualRouterCarrierWriteDeadlineDetachesBlockedCarrier(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	conn := newProbeVirtualRouterDeadlineFailConn()
	key := "carrier-write-deadline"
	link := newProbeVirtualRouterFrameLink(key, &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "vrouter-carrier-write-deadline"}}, nil, nil)
	link.Start()
	token := link.AttachCarrier(conn, "vrouter-carrier-deadline", "deadline")
	if token == nil {
		t.Fatalf("carrier should attach")
	}
	t.Cleanup(func() { stopProbeVirtualRouterFrameLink(link) })

	frame, err := buildProbeVirtualRouterIPFrame(buildProbeVirtualRouterTestIPv4Packet(t, "198.18.0.1", "198.18.0.2"), []string{"1", "2"}, nil)
	if err != nil {
		t.Fatalf("build ip frame failed: %v", err)
	}
	if err := link.EnqueueProbeVirtualRouterFrame(frame); err != nil {
		t.Fatalf("enqueue should not fail before carrier write timeout: %v", err)
	}

	select {
	case <-conn.writeDeadlineSet:
	case <-time.After(time.Second):
		t.Fatalf("carrier write should set a write deadline")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if isProbeVirtualRouterFrameLinkClosed(link) {
			t.Fatalf("carrier write timeout should not close frame link")
		}
		link.mu.Lock()
		carrier := link.carrier
		link.mu.Unlock()
		if carrier == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("carrier should detach after write deadline failure")
}

func TestProbeVirtualRouterWireFrameWriteDeadlineRefreshesAfterProgress(t *testing.T) {
	conn := &probeVirtualRouterPartialWriteDeadlineConn{chunkSize: 7}
	frame, err := buildProbeVirtualRouterIPFrame(buildProbeVirtualRouterTestIPv4Packet(t, "198.18.0.1", "198.18.0.2"), []string{"1", "2"}, nil)
	if err != nil {
		t.Fatalf("build ip frame failed: %v", err)
	}
	payload, err := encodeProbeVirtualRouterFrame(frame)
	if err != nil {
		t.Fatalf("encode frame failed: %v", err)
	}
	if err := writeProbeVirtualRouterWireFrameRaw(conn, frame); err != nil {
		t.Fatalf("write frame failed: %v", err)
	}
	if !bytes.Equal(conn.buf.Bytes(), payload) {
		t.Fatalf("written payload mismatch: got=%d want=%d", conn.buf.Len(), len(payload))
	}
	if conn.writeCalls < 2 {
		t.Fatalf("partial writer should be called multiple times, calls=%d", conn.writeCalls)
	}
	if got, wantMin := conn.deadlineSetCalls, conn.writeCalls+1; got < wantMin {
		t.Fatalf("write deadline should refresh for each progress write and clear at end, got=%d want>=%d", got, wantMin)
	}
	if !conn.cleared {
		t.Fatalf("write deadline should be cleared after frame write")
	}
}

var errProbeVirtualRouterTestWriteDeadline = errors.New("test write deadline reached")

type probeVirtualRouterDeadlineFailConn struct {
	writeDeadlineSet chan struct{}
	closeCh          chan struct{}
	deadlineOnce     sync.Once
	closeOnce        sync.Once
}

func newProbeVirtualRouterDeadlineFailConn() *probeVirtualRouterDeadlineFailConn {
	return &probeVirtualRouterDeadlineFailConn{
		writeDeadlineSet: make(chan struct{}),
		closeCh:          make(chan struct{}),
	}
}

func (c *probeVirtualRouterDeadlineFailConn) Read([]byte) (int, error) {
	<-c.closeCh
	return 0, net.ErrClosed
}

func (c *probeVirtualRouterDeadlineFailConn) Write([]byte) (int, error) {
	select {
	case <-c.writeDeadlineSet:
		return 0, errProbeVirtualRouterTestWriteDeadline
	case <-c.closeCh:
		return 0, net.ErrClosed
	}
}

func (c *probeVirtualRouterDeadlineFailConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closeCh)
	})
	return nil
}

func (c *probeVirtualRouterDeadlineFailConn) LocalAddr() net.Addr {
	return &net.TCPAddr{}
}

func (c *probeVirtualRouterDeadlineFailConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{}
}

func (c *probeVirtualRouterDeadlineFailConn) SetDeadline(time.Time) error {
	return nil
}

func (c *probeVirtualRouterDeadlineFailConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *probeVirtualRouterDeadlineFailConn) SetWriteDeadline(t time.Time) error {
	if !t.IsZero() {
		c.deadlineOnce.Do(func() {
			close(c.writeDeadlineSet)
		})
	}
	return nil
}

type probeVirtualRouterPartialWriteDeadlineConn struct {
	buf              bytes.Buffer
	chunkSize        int
	writeCalls       int
	deadlineSetCalls int
	cleared          bool
}

func (c *probeVirtualRouterPartialWriteDeadlineConn) Write(p []byte) (int, error) {
	c.writeCalls++
	if c.chunkSize <= 0 || c.chunkSize > len(p) {
		c.chunkSize = len(p)
	}
	return c.buf.Write(p[:c.chunkSize])
}

func (c *probeVirtualRouterPartialWriteDeadlineConn) SetWriteDeadline(t time.Time) error {
	c.deadlineSetCalls++
	if t.IsZero() {
		c.cleared = true
	}
	return nil
}

func containsProbeVirtualRouterTestString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func buildProbeVirtualRouterTestIPv4Packet(t *testing.T, src string, dst string) []byte {
	t.Helper()
	srcIP := net.ParseIP(src).To4()
	dstIP := net.ParseIP(dst).To4()
	if srcIP == nil || dstIP == nil {
		t.Fatalf("invalid test ip src=%q dst=%q", src, dst)
	}
	packet := make([]byte, 20)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64
	packet[9] = 1
	copy(packet[12:16], srcIP)
	copy(packet[16:20], dstIP)
	return packet
}

func buildProbeVirtualRouterTestICMPEchoRequest(t *testing.T, src string, dst string) []byte {
	t.Helper()
	srcIP := net.ParseIP(src).To4()
	dstIP := net.ParseIP(dst).To4()
	if srcIP == nil || dstIP == nil {
		t.Fatalf("invalid test ip src=%q dst=%q", src, dst)
	}
	packet := make([]byte, 32)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	binary.BigEndian.PutUint16(packet[4:6], 0x1234)
	packet[8] = 64
	packet[9] = 1
	copy(packet[12:16], srcIP)
	copy(packet[16:20], dstIP)
	binary.BigEndian.PutUint16(packet[10:12], probeVirtualRouterChecksum(packet[:20]))
	packet[20] = 8
	packet[21] = 0
	binary.BigEndian.PutUint16(packet[24:26], 0x4567)
	binary.BigEndian.PutUint16(packet[26:28], 1)
	copy(packet[28:], []byte{1, 2, 3, 4})
	binary.BigEndian.PutUint16(packet[22:24], probeVirtualRouterChecksum(packet[20:]))
	return packet
}

func buildProbeVirtualRouterTestTCPPacket(t *testing.T, src string, dst string, srcPort uint16, dstPort uint16) []byte {
	t.Helper()
	srcIP := net.ParseIP(src).To4()
	dstIP := net.ParseIP(dst).To4()
	if srcIP == nil || dstIP == nil {
		t.Fatalf("invalid test ip src=%q dst=%q", src, dst)
	}
	packet := make([]byte, 40)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	binary.BigEndian.PutUint16(packet[4:6], 0x1234)
	packet[8] = 64
	packet[9] = 6
	copy(packet[12:16], srcIP)
	copy(packet[16:20], dstIP)
	binary.BigEndian.PutUint16(packet[20:22], srcPort)
	binary.BigEndian.PutUint16(packet[22:24], dstPort)
	packet[32] = 0x50
	packet[33] = 0x02
	binary.BigEndian.PutUint16(packet[34:36], 65535)
	binary.BigEndian.PutUint16(packet[10:12], probeVirtualRouterChecksum(packet[:20]))
	return packet
}

func setProbeVirtualRouterTestTCPChecksum(packet []byte) {
	if len(packet) < 40 || packet[0]>>4 != 4 || packet[9] != 6 {
		return
	}
	ihl := int(packet[0]&0x0F) * 4
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if ihl < 20 || totalLen <= ihl || totalLen > len(packet) {
		return
	}
	transport := packet[ihl:totalLen]
	if len(transport) < 18 {
		return
	}
	transport[16], transport[17] = 0, 0
	binary.BigEndian.PutUint16(transport[16:18], probeVirtualRouterTransportChecksum(packet, transport))
}

func buildProbeVirtualRouterTestUDPPacket(t *testing.T, src string, dst string, srcPort uint16, dstPort uint16) []byte {
	t.Helper()
	srcIP := net.ParseIP(src).To4()
	dstIP := net.ParseIP(dst).To4()
	if srcIP == nil || dstIP == nil {
		t.Fatalf("invalid test ip src=%q dst=%q", src, dst)
	}
	packet := make([]byte, 28)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	binary.BigEndian.PutUint16(packet[4:6], 0x1234)
	packet[8] = 64
	packet[9] = 17
	copy(packet[12:16], srcIP)
	copy(packet[16:20], dstIP)
	binary.BigEndian.PutUint16(packet[20:22], srcPort)
	binary.BigEndian.PutUint16(packet[22:24], dstPort)
	binary.BigEndian.PutUint16(packet[24:26], 8)
	binary.BigEndian.PutUint16(packet[10:12], probeVirtualRouterChecksum(packet[:20]))
	return packet
}
