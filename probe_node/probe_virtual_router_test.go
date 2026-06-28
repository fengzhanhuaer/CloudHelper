package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"net"
	"os"
	"reflect"
	"testing"
	"time"
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
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	config := probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "node-1", IP: "198.18.0.1"},
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
	if err := persistProbeVirtualRouterCache(config); err != nil {
		t.Fatalf("persist cache failed: %v", err)
	}
	path, err := resolveProbeVirtualRouterCachePath()
	if err != nil {
		t.Fatalf("resolve cache path failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
	loaded, err := loadProbeVirtualRouterCache()
	if err != nil {
		t.Fatalf("load cache failed: %v", err)
	}
	if len(loaded.ProbeIPs) != 1 || loaded.ProbeIPs[0].NodeID != "1" {
		t.Fatalf("loaded probe ips=%+v", loaded.ProbeIPs)
	}
	if len(loaded.TopologyRules) != 3 || loaded.TopologyRules[0].Direction != probeVirtualRouterDirectionForward {
		t.Fatalf("loaded topology=%+v", loaded.TopologyRules)
	}
	if loaded.TopologyRules[0].FromServiceDomain != "edge-a.example.com" || loaded.TopologyRules[0].FromServicePort != 443 || loaded.TopologyRules[0].ToServiceDomain != "edge-b.internal.lan" || loaded.TopologyRules[0].ToServicePort != 443 {
		t.Fatalf("loaded service config=%+v", loaded.TopologyRules[0])
	}
	if loaded.TopologyRules[1].FromServicePort != 443 || loaded.TopologyRules[1].ToServicePort != 443 {
		t.Fatalf("service port reuse should be preserved: %+v", loaded.TopologyRules)
	}
	if loaded.TopologyRules[2].FromServicePort != probeVirtualRouterDefaultServicePort || loaded.TopologyRules[2].ToServicePort != probeVirtualRouterDefaultServicePort {
		t.Fatalf("default service ports=%d/%d, want %d", loaded.TopologyRules[2].FromServicePort, loaded.TopologyRules[2].ToServicePort, probeVirtualRouterDefaultServicePort)
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
	rule.AuthTicket = buildProbeChainUserAuthTicketForTest(t, priv, probeVirtualRouterRuntimeChainID(rule), rawPublicKey)
	return rule
}

func resetProbeVirtualRouterStateForTest() {
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{}
	probeVirtualRouterState.localNodeID = ""
	probeVirtualRouterState.localIP = ""
	probeVirtualRouterState.nodeToIP = nil
	probeVirtualRouterState.ipToNode = nil
	probeVirtualRouterState.neighbors = nil
	probeVirtualRouterState.rulesByID = nil
	probeVirtualRouterState.mu.Unlock()
	clearProbeVirtualRouterRouteCache("test reset")
	probeVirtualRouterPathRTTState.mu.Lock()
	probeVirtualRouterPathRTTState.items = make(map[string]probeVirtualRouterPathRTTRecord)
	probeVirtualRouterPathRTTState.mu.Unlock()
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
	if _, ok := buildProbeVirtualRouterRuntimeConfigForRule(rule, nodeIdentity{NodeID: "1"}, ""); ok {
		t.Fatalf("runtime config should require link auth fields")
	}

	rule = withProbeVirtualRouterRuleAuthForTest(t, rule)
	cfg, ok := buildProbeVirtualRouterRuntimeConfigForRule(rule, nodeIdentity{NodeID: "1"}, "")
	if !ok {
		t.Fatalf("runtime config should be built with link auth fields")
	}
	if !cfg.requireUserAuth {
		t.Fatalf("virtual router should require user auth")
	}
	if cfg.secret != "shared-link-secret" || cfg.authTicket == "" || len(cfg.userPublicKey) != ed25519.PublicKeySize {
		t.Fatalf("runtime auth fields not applied: secret=%q ticket=%t pub=%d", cfg.secret, cfg.authTicket != "", len(cfg.userPublicKey))
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

	rt := &probeChainRuntime{cfg: probeChainRuntimeConfig{chainID: "vrouter-frame-stats"}}
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
	resetProbeChainAuthTicketStoreForTest()
	defer resetProbeChainAuthTicketStoreForTest()

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

	chainID := probeVirtualRouterRuntimeChainID(rule)
	if got := lookupProbeChainAuthTicket(chainID); got != rule.AuthTicket {
		t.Fatalf("cached virtual router ticket=%q want %q", got, rule.AuthTicket)
	}
}

func TestEnsureProbeChainRuntimeAuthTicketUsesVirtualRouterConfig(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeChainAuthTicketStoreForTest()
	defer resetProbeChainAuthTicketStoreForTest()
	origRequestConfig := probeRequestLinkChainConfig
	defer func() { probeRequestLinkChainConfig = origRequestConfig }()

	rule := withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
		ID:              "rule-ticket-refresh",
		FromNodeID:      "1",
		ToNodeID:        "2",
		Direction:       probeVirtualRouterDirectionForward,
		FromServicePort: 12040,
		ToServicePort:   12041,
		Enabled:         true,
	})
	chainID := probeVirtualRouterRuntimeChainID(rule)
	probeRequestLinkChainConfig = func(ctx context.Context, controllerBaseURL string, identity nodeIdentity) (probeLinkChainConfigFetchResult, error) {
		return probeLinkChainConfigFetchResult{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:       true,
				TopologyRules: []probeVirtualRouterTopologyRule{rule},
			},
		}, nil
	}

	cfg := probeChainRuntimeConfig{
		chainID:         chainID,
		requireUserAuth: true,
		controllerURL:   "http://controller.example.invalid",
		identity:        nodeIdentity{NodeID: "1", Secret: "node-secret"},
	}
	if err := ensureProbeChainRuntimeAuthTicket(&cfg); err != nil {
		t.Fatalf("ensure auth ticket failed: %v", err)
	}
	if cfg.authTicket != rule.AuthTicket {
		t.Fatalf("refreshed virtual router ticket=%q want %q", cfg.authTicket, rule.AuthTicket)
	}
}

func TestProbeVirtualRouterCurrentLocalPathToIP(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "node-1", IP: "198.18.0.1"},
			{NodeID: "node-2", IP: "198.18.0.2"},
			{NodeID: "node-3", IP: "198.18.0.3"},
		},
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Direction: "bidirectional", Enabled: true},
			{FromNodeID: "2", ToNodeID: "3", Direction: "bidirectional", Enabled: true},
		},
	}
	applyProbeVirtualRouterConfigForNode(config, "node-1")
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
}

func TestBuildProbeVirtualRouterRuntimeConfigsForNode(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
				ID:                "edge-a-b",
				FromNodeID:        "1",
				ToNodeID:          "2",
				Direction:         "bidirectional",
				FromServiceDomain: "a.internal",
				FromServicePort:   12040,
				ToServiceDomain:   "b.internal",
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
	if left[0].chainID != right[0].chainID || !isProbeVirtualRouterRuntimeChainID(left[0].chainID) {
		t.Fatalf("unexpected chain ids left=%q right=%q", left[0].chainID, right[0].chainID)
	}
	if left[0].nextNodeID != "2" || left[0].nextHost != "b.internal" || left[0].nextPort != 12040 || left[0].nextAuthMode != "secret" {
		t.Fatalf("left runtime should dial node 2: %+v", left[0])
	}
	if right[0].prevNodeID != "1" || right[0].nextAuthMode != "proxy" {
		t.Fatalf("right runtime should wait for node 1: %+v", right[0])
	}
	if left[0].listenPort != 12040 || right[0].listenPort != 12040 {
		t.Fatalf("listen ports left=%d right=%d", left[0].listenPort, right[0].listenPort)
	}
}

func TestBuildProbeVirtualRouterRuntimeConfigsAllowSharedPortAcrossRules(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
				ID:              "edge-a-b",
				FromNodeID:      "1",
				ToNodeID:        "2",
				FromServicePort: 12040,
				ToServiceDomain: "b.internal",
				ToServicePort:   12040,
				Enabled:         true,
			}),
			withProbeVirtualRouterRuleAuthForTest(t, probeVirtualRouterTopologyRule{
				ID:              "edge-c-b",
				FromNodeID:      "3",
				ToNodeID:        "2",
				FromServicePort: 12040,
				ToServiceDomain: "b.internal",
				ToServicePort:   12040,
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
	if left[0].listenPort != 12040 || right[0].listenPort != 12040 {
		t.Fatalf("dialer listen ports left=%d right=%d", left[0].listenPort, right[0].listenPort)
	}
	if left[0].nextHost != "b.internal" || left[0].nextPort != 12040 || right[0].nextHost != "b.internal" || right[0].nextPort != 12040 {
		t.Fatalf("dialers should target same B service port: left=%+v right=%+v", left[0], right[0])
	}
	if middle[0].listenPort != 12040 || middle[1].listenPort != 12040 {
		t.Fatalf("listener ports middle=%d/%d", middle[0].listenPort, middle[1].listenPort)
	}
	if middle[0].chainID == middle[1].chainID {
		t.Fatalf("rules sharing one port must still have distinct chain ids: %+v", middle)
	}
	seenPrev := map[string]struct{}{
		middle[0].prevNodeID: {},
		middle[1].prevNodeID: {},
	}
	if _, ok := seenPrev["1"]; !ok {
		t.Fatalf("B runtime should keep rule from node 1: %+v", middle)
	}
	if _, ok := seenPrev["3"]; !ok {
		t.Fatalf("B runtime should keep rule from node 3: %+v", middle)
	}
}

func TestProbeVirtualRouterRuntimeChainIDIsStableAcrossServiceEndpointChanges(t *testing.T) {
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

	if left, right := probeVirtualRouterRuntimeChainID(base), probeVirtualRouterRuntimeChainID(changedEndpoint); left != right {
		t.Fatalf("same rule should keep chain id across topology endpoint changes: %s != %s", left, right)
	}

	changedRule := base
	changedRule.ID = "edge-a-b-other"
	if left, right := probeVirtualRouterRuntimeChainID(base), probeVirtualRouterRuntimeChainID(changedRule); left == right {
		t.Fatalf("different rule ids should produce different chain ids: %s", left)
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
	if left[0].nextNodeID != "2" || left[0].nextHost != "b.internal" || left[0].nextAuthMode != "secret" {
		t.Fatalf("forward source should dial destination: %+v", left[0])
	}
	if right[0].prevNodeID != "1" || right[0].nextNodeID != "" || right[0].nextAuthMode != "proxy" || right[0].prevDialMode != probeChainDialModeNone {
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
	if left[0].prevNodeID != "2" || left[0].nextAuthMode != "proxy" || left[0].nextNodeID != "" {
		t.Fatalf("node 1 should keep topology but cannot dial without B address: %+v", left[0])
	}
	if right[0].prevNodeID != "1" || right[0].nextNodeID != "" || right[0].nextAuthMode != "proxy" {
		t.Fatalf("node 2 should remain passive; B never dials A for virtual router: %+v", right[0])
	}
}

func TestCollectProbeLinkChainRuntimeIDsToStopKeepsVirtualRouterRuntime(t *testing.T) {
	probeChainRuntimeState.mu.Lock()
	oldRuntimes := probeChainRuntimeState.runtimes
	probeChainRuntimeState.runtimes = map[string]*probeChainRuntime{
		"ordinary": {
			cfg:                probeChainRuntimeConfig{chainID: "ordinary"},
			downstreamSessions: make(map[string]*probeChainBridgeSession),
			upstreamSessions:   make(map[string]*probeChainBridgeSession),
			stopCh:             make(chan struct{}),
		},
		"vrouter-abc": {
			cfg:                probeChainRuntimeConfig{chainID: "vrouter-abc"},
			downstreamSessions: make(map[string]*probeChainBridgeSession),
			upstreamSessions:   make(map[string]*probeChainBridgeSession),
			stopCh:             make(chan struct{}),
		},
	}
	t.Cleanup(func() {
		probeChainRuntimeState.mu.Lock()
		probeChainRuntimeState.runtimes = oldRuntimes
		probeChainRuntimeState.mu.Unlock()
	})

	toStop := collectProbeLinkChainRuntimeIDsToStopLocked(map[string]struct{}{})
	probeChainRuntimeState.mu.Unlock()

	if !reflect.DeepEqual(toStop, []string{"ordinary"}) {
		t.Fatalf("toStop=%v, want [ordinary]", toStop)
	}
}

func TestProbeVirtualRouterControlFrameEnvelope(t *testing.T) {
	payload := []byte(`{"request_id":"r1"}`)
	raw, err := marshalProbeVirtualRouterFrameEnvelope(probeVirtualRouterFrameMessage{
		FrameType:   probeVirtualRouterFrameTypeControl,
		ControlType: probeVirtualRouterControlTypePing,
		Payload:     payload,
		Path:        []string{"1", "2"},
	})
	if err != nil {
		t.Fatalf("marshal control frame failed: %v", err)
	}
	frame, err := unmarshalProbeVirtualRouterFrameEnvelope(raw, nil)
	if err != nil {
		t.Fatalf("unmarshal control frame failed: %v", err)
	}
	if frame.FrameType != probeVirtualRouterFrameTypeControl || frame.ControlType != probeVirtualRouterControlTypePing {
		t.Fatalf("unexpected control frame type: %+v", frame)
	}
	if string(frame.Payload) != string(payload) || !reflect.DeepEqual(frame.Path, []string{"1", "2"}) {
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

func TestProbeVirtualRouterPathFromRequest(t *testing.T) {
	req := probeChainTunnelOpenRequest{
		AssociationV2: &probeChainAssociationV2Meta{
			RouteTarget: "node-1>node-2>node-3",
		},
	}
	if got := probeVirtualRouterPathFromRequest(req); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("path=%v, want [1 2 3]", got)
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

	probeChainRuntimeState.mu.Lock()
	oldRuntimes := probeChainRuntimeState.runtimes
	probeChainRuntimeState.runtimes = map[string]*probeChainRuntime{
		"vrouter-1-2": {cfg: probeChainRuntimeConfig{chainID: "vrouter-1-2", nextNodeID: "2", nextAuthMode: "secret"}},
		"vrouter-1-3": {cfg: probeChainRuntimeConfig{chainID: "vrouter-1-3", nextNodeID: "3", nextAuthMode: "secret"}},
	}
	probeChainRuntimeState.mu.Unlock()
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		"vrouter-1-2": {LastPingLatencyMS: 50},
		"vrouter-1-3": {LastPingLatencyMS: 10},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeChainRuntimeState.mu.Lock()
		probeChainRuntimeState.runtimes = oldRuntimes
		probeChainRuntimeState.mu.Unlock()
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

	probeChainRuntimeState.mu.Lock()
	oldRuntimes := probeChainRuntimeState.runtimes
	probeChainRuntimeState.runtimes = map[string]*probeChainRuntime{
		"vrouter-1-2": {cfg: probeChainRuntimeConfig{chainID: "vrouter-1-2", nextNodeID: "2", nextAuthMode: "secret"}},
		"vrouter-1-3": {cfg: probeChainRuntimeConfig{chainID: "vrouter-1-3", nextNodeID: "3", nextAuthMode: "secret"}},
	}
	probeChainRuntimeState.mu.Unlock()
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		"vrouter-1-2": {LastRemoteRTTMS: 80},
		"vrouter-1-3": {LastRemoteRTTMS: 20},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeChainRuntimeState.mu.Lock()
		probeChainRuntimeState.runtimes = oldRuntimes
		probeChainRuntimeState.mu.Unlock()
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
	rt := &probeChainRuntime{cfg: probeChainRuntimeConfig{chainID: "vrouter-rule-1"}}
	left := probeVirtualRouterFrameLinkKey(rt, "to_next", "198.18.0.21", []string{"1", "2"})
	right := probeVirtualRouterFrameLinkKey(rt, "to_prev", "198.18.0.22", []string{"2", "1"})
	if left == "" {
		t.Fatalf("frame link key should not be empty")
	}
	if left != right {
		t.Fatalf("frame link key should be per rule: left=%q right=%q", left, right)
	}
}

func TestProbeVirtualRouterFrameEnvelopeCarriesTypeControlAndPath(t *testing.T) {
	packet := []byte{0x45, 0x00, 0x00, 0x14}
	payload, err := marshalProbeVirtualRouterFrameEnvelope(probeVirtualRouterFrameMessage{
		FrameType:   probeVirtualRouterFrameTypeData,
		ControlType: probeVirtualRouterControlTypeIPv4,
		Payload:     packet,
		Path:        []string{"1", "3", "4"},
	})
	if err != nil {
		t.Fatalf("marshal frame envelope failed: %v", err)
	}
	got, err := unmarshalProbeVirtualRouterFrameEnvelope(payload, []string{"fallback"})
	if err != nil {
		t.Fatalf("unmarshal frame envelope failed: %v", err)
	}
	if got.FrameType != probeVirtualRouterFrameTypeData || got.ControlType != probeVirtualRouterControlTypeIPv4 || !reflect.DeepEqual(got.Payload, packet) || !reflect.DeepEqual(got.Path, []string{"1", "3", "4"}) {
		t.Fatalf("frame=%+v", got)
	}
	legacy, err := unmarshalProbeVirtualRouterFrameEnvelope(packet, []string{"fallback"})
	if err != nil {
		t.Fatalf("legacy frame fallback failed: %v", err)
	}
	if legacy.FrameType != probeVirtualRouterFrameTypeData || legacy.ControlType != probeVirtualRouterControlTypeIPv4 || !reflect.DeepEqual(legacy.Payload, packet) || !reflect.DeepEqual(legacy.Path, []string{"fallback"}) {
		t.Fatalf("legacy frame=%+v", legacy)
	}
}

func TestProbeVirtualRouterFrameLinkTXWorkerWritesBufferedFrame(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()

	link := newProbeVirtualRouterFrameLink("test-link", nil, left, nil)
	link.Start()
	defer stopProbeVirtualRouterFrameLink(link)

	type result struct {
		frame probeVirtualRouterFrameMessage
		err   error
	}
	done := make(chan result, 1)
	go func() {
		frame, err := readProbeVirtualRouterFrame(bufio.NewReader(right), nil)
		done <- result{frame: frame, err: err}
	}()

	wantPacket := []byte{0x45, 0x00, 0x00, 0x14}
	wantPath := []string{"16", "19"}
	if err := writeProbeVirtualRouterIPFrame(link, wantPacket, wantPath); err != nil {
		t.Fatalf("enqueue frame failed: %v", err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read frame failed: %v", got.err)
		}
		if got.frame.FrameType != probeVirtualRouterFrameTypeData || got.frame.ControlType != probeVirtualRouterControlTypeIPv4 || !reflect.DeepEqual(got.frame.Payload, wantPacket) || !reflect.DeepEqual(got.frame.Path, wantPath) {
			t.Fatalf("frame=%+v, want payload/path %v/%v", got.frame, wantPacket, wantPath)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for tx worker frame")
	}
}

func TestProbeVirtualRouterFrameLinkTXWorkerSurvivesCarrierMigration(t *testing.T) {
	firstLeft, firstRight := net.Pipe()
	link := newProbeVirtualRouterFrameLink("test-link", nil, firstLeft, nil)
	link.Start()
	_ = firstLeft.Close()
	_ = firstRight.Close()
	defer stopProbeVirtualRouterFrameLink(link)

	wantPacket := []byte{0x45, 0x00, 0x00, 0x14}
	wantPath := []string{"16", "19"}
	if err := writeProbeVirtualRouterIPFrame(link, wantPacket, wantPath); err != nil {
		t.Fatalf("enqueue frame failed: %v", err)
	}

	secondLeft, secondRight := net.Pipe()
	defer secondRight.Close()
	if token := link.AttachCarrier(secondLeft, "reconnected", "pipe"); token == nil {
		t.Fatalf("attach carrier returned nil")
	}
	frame, err := readProbeVirtualRouterFrame(bufio.NewReader(secondRight), nil)
	if err != nil {
		t.Fatalf("read migrated frame failed: %v", err)
	}
	if frame.FrameType != probeVirtualRouterFrameTypeData || frame.ControlType != probeVirtualRouterControlTypeIPv4 || !reflect.DeepEqual(frame.Payload, wantPacket) || !reflect.DeepEqual(frame.Path, wantPath) {
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

	probeChainRuntimeState.mu.Lock()
	oldRuntimes := probeChainRuntimeState.runtimes
	probeChainRuntimeState.runtimes = map[string]*probeChainRuntime{
		"vrouter-1-4": {cfg: probeChainRuntimeConfig{chainID: "vrouter-1-4", nextNodeID: "4", nextAuthMode: "secret"}},
		"vrouter-1-3": {cfg: probeChainRuntimeConfig{chainID: "vrouter-1-3", nextNodeID: "3", nextAuthMode: "secret"}},
	}
	probeChainRuntimeState.mu.Unlock()
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		"vrouter-1-4": {LastPingLatencyMS: 100},
		"vrouter-1-3": {LastPingLatencyMS: 1},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeChainRuntimeState.mu.Lock()
		probeChainRuntimeState.runtimes = oldRuntimes
		probeChainRuntimeState.mu.Unlock()
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

	probeChainRuntimeState.mu.Lock()
	oldRuntimes := probeChainRuntimeState.runtimes
	probeChainRuntimeState.runtimes = map[string]*probeChainRuntime{
		"vrouter-1-2": {cfg: probeChainRuntimeConfig{chainID: "vrouter-1-2", nextNodeID: "2", nextAuthMode: "secret"}},
		"vrouter-1-3": {cfg: probeChainRuntimeConfig{chainID: "vrouter-1-3", nextNodeID: "3", nextAuthMode: "secret"}},
	}
	probeChainRuntimeState.mu.Unlock()
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		"vrouter-1-2": {LastPingLatencyMS: 50},
		"vrouter-1-3": {LastPingLatencyMS: 10},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeChainRuntimeState.mu.Lock()
		probeChainRuntimeState.runtimes = oldRuntimes
		probeChainRuntimeState.mu.Unlock()
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

	rt := &probeChainRuntime{cfg: probeChainRuntimeConfig{identity: nodeIdentity{NodeID: "19"}}}
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

	rt := &probeChainRuntime{cfg: probeChainRuntimeConfig{identity: nodeIdentity{NodeID: "16"}}}
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
		frame probeVirtualRouterFrameMessage
		err   error
	}, 1)
	go func() {
		frame, err := readProbeVirtualRouterFrame(bufio.NewReader(right), nil)
		replyCh <- struct {
			frame probeVirtualRouterFrameMessage
			err   error
		}{frame: frame, err: err}
	}()

	rt := &probeChainRuntime{cfg: probeChainRuntimeConfig{chainID: "vrouter-16-19", identity: nodeIdentity{NodeID: "19"}}}
	request := buildProbeVirtualRouterTestICMPEchoRequest(t, "198.18.0.18", "198.18.0.21")
	if !handleProbeVirtualRouterLocalICMPEchoRequest(rt, link, request, []string{"16", "19"}) {
		t.Fatalf("echo request should be handled")
	}
	select {
	case got := <-replyCh:
		if got.err != nil {
			t.Fatalf("read reply failed: %v", got.err)
		}
		info, ok := probeVirtualRouterParseICMPEchoLogInfo(got.frame.Payload)
		if !ok || info.Kind != "echo_reply" || info.SourceIP != "198.18.0.21" || info.DestinationIP != "198.18.0.18" {
			t.Fatalf("reply info=%+v ok=%v", info, ok)
		}
		if got.frame.FrameType != probeVirtualRouterFrameTypeData || got.frame.ControlType != probeVirtualRouterControlTypeIPv4 || !reflect.DeepEqual(got.frame.Path, []string{"19", "16"}) {
			t.Fatalf("reply frame=%+v, want data/ip4 path [19 16]", got.frame)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for echo reply")
	}
}

func TestProbeVirtualRouterParseTCPUDPLogInfo(t *testing.T) {
	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.0.21", 49152, 8080)
	info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet)
	if !ok {
		t.Fatalf("tcp packet not parsed")
	}
	if info.Protocol != "tcp" || info.SourceIP != "198.18.0.18" || info.DestinationIP != "198.18.0.21" || info.SourcePort != 49152 || info.DestinationPort != 8080 {
		t.Fatalf("unexpected tcp info: %+v", info)
	}
}

func TestProbeVirtualRouterRuntimeForAdjacentNode(t *testing.T) {
	probeChainRuntimeState.mu.Lock()
	oldRuntimes := probeChainRuntimeState.runtimes
	probeChainRuntimeState.runtimes = map[string]*probeChainRuntime{
		"chain-a": {cfg: probeChainRuntimeConfig{chainID: "chain-a", nextNodeID: "2", nextAuthMode: "secret"}},
		"chain-b": {cfg: probeChainRuntimeConfig{chainID: "chain-b", prevNodeID: "3"}},
	}
	probeChainRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		probeChainRuntimeState.mu.Lock()
		probeChainRuntimeState.runtimes = oldRuntimes
		probeChainRuntimeState.mu.Unlock()
	})

	rt, direction := probeVirtualRouterRuntimeForAdjacentNode("2")
	if rt == nil || rt.cfg.chainID != "chain-a" || direction != probeChainBridgeRoleToNext {
		t.Fatalf("next runtime=%v direction=%q", rt, direction)
	}
	rt, direction = probeVirtualRouterRuntimeForAdjacentNode("3")
	if rt == nil || rt.cfg.chainID != "chain-b" || direction != probeChainBridgeRoleToPrev {
		t.Fatalf("prev runtime=%v direction=%q", rt, direction)
	}
}

func TestProbeVirtualRouterRuntimeForAdjacentNodePrefersAvailableBridgeSession(t *testing.T) {
	nextClient, nextServer := newProbeChainFrameSessionPairForTest(t)
	defer nextClient.Close()
	defer nextServer.Close()
	prevClient, prevServer := newProbeChainFrameSessionPairForTest(t)
	defer prevClient.Close()
	defer prevServer.Close()

	nextRT := &probeChainRuntime{cfg: probeChainRuntimeConfig{chainID: "vrouter-next", nextNodeID: "2", nextAuthMode: "secret"}}
	nextRT.setUpstreamSession("upstream-test", nextServer, probeChainBridgeRoleToPrev, "pipe")
	prevRT := &probeChainRuntime{cfg: probeChainRuntimeConfig{chainID: "vrouter-prev", prevNodeID: "3"}}
	prevRT.setDownstreamSession("downstream-test", prevServer, probeChainBridgeRoleToNext, "pipe")

	probeChainRuntimeState.mu.Lock()
	oldRuntimes := probeChainRuntimeState.runtimes
	probeChainRuntimeState.runtimes = map[string]*probeChainRuntime{
		"vrouter-next": nextRT,
		"vrouter-prev": prevRT,
	}
	probeChainRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		probeChainRuntimeState.mu.Lock()
		probeChainRuntimeState.runtimes = oldRuntimes
		probeChainRuntimeState.mu.Unlock()
	})

	rt, direction := probeVirtualRouterRuntimeForAdjacentNode("2")
	if rt == nil || rt.cfg.chainID != "vrouter-next" || direction != probeChainBridgeRoleToNext {
		t.Fatalf("next runtime=%v direction=%q", rt, direction)
	}
	rt, direction = probeVirtualRouterRuntimeForAdjacentNode("3")
	if rt == nil || rt.cfg.chainID != "vrouter-prev" || direction != probeChainBridgeRoleToPrev {
		t.Fatalf("prev runtime=%v direction=%q", rt, direction)
	}
}

func TestProbeVirtualRouterFrameLinkKey(t *testing.T) {
	rt := &probeChainRuntime{cfg: probeChainRuntimeConfig{chainID: "chain-a"}}
	got := probeVirtualRouterFrameLinkKey(rt, probeChainBridgeRoleToNext, "198.18.0.9", []string{"node-1", "node-2"})
	if got != "packet|chain-a" {
		t.Fatalf("key=%q", got)
	}
	other := probeVirtualRouterFrameLinkKey(rt, probeChainBridgeRoleToPrev, "198.18.0.10", []string{"node-2", "node-1"})
	if other != got {
		t.Fatalf("frame link key should be per rule: got=%q other=%q", got, other)
	}
}

func TestProbeVirtualRouterFrameLinkCacheReuseAndDrop(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })

	key := "chain-a|to_next|198.18.0.9|1>2"
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

	key := "chain-a|to_next|198.18.0.9|1>2"
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
