package main

import (
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

func TestProbeVirtualRouterReachableHonorsDirection(t *testing.T) {
	config := probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Direction: "forward", Enabled: true},
		},
	}
	if !probeVirtualRouterReachable(config, "1", "2") {
		t.Fatalf("node 1 should reach node 2")
	}
	if probeVirtualRouterReachable(config, "2", "1") {
		t.Fatalf("node 2 should not reach node 1 through forward-only rule")
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
	if len(loaded.TopologyRules) != 2 || loaded.TopologyRules[0].Direction != probeVirtualRouterDirectionTwoWay {
		t.Fatalf("loaded topology=%+v", loaded.TopologyRules)
	}
	if loaded.TopologyRules[0].FromServiceDomain != "edge-a.example.com" || loaded.TopologyRules[0].FromServicePort != 443 || loaded.TopologyRules[0].ToServiceDomain != "edge-b.internal.lan" || loaded.TopologyRules[0].ToServicePort != 443 {
		t.Fatalf("loaded service config=%+v", loaded.TopologyRules[0])
	}
	if loaded.TopologyRules[1].FromServicePort != 443 || loaded.TopologyRules[1].ToServicePort != 443 {
		t.Fatalf("service port reuse should be preserved: %+v", loaded.TopologyRules)
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
	t.Cleanup(func() {
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = probeVirtualRouterConfig{}
		probeVirtualRouterState.localNodeID = ""
		probeVirtualRouterState.mu.Unlock()
	})

	if got := currentProbeVirtualRouterLocalNodeID(); got != "1" {
		t.Fatalf("local node id=%q, want 1", got)
	}
	if got := currentProbeVirtualRouterLocalIP(); got != "198.18.0.1" {
		t.Fatalf("local ip=%q, want 198.18.0.1", got)
	}
	if got := currentProbeVirtualRouterPathToIP("198.18.0.3"); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("path to ip=%v, want [1 2 3]", got)
	}
}

func TestBuildProbeVirtualRouterTunnelOpenRequest(t *testing.T) {
	req := buildProbeVirtualRouterTunnelOpenRequest("198.18.0.3", []string{"1", "2", "3"})
	if req.Type != probeVirtualRouterTunnelOpenType || req.Scope != probeVirtualRouterTunnelScope {
		t.Fatalf("unexpected request type/scope: %+v", req)
	}
	if req.Network != probeVirtualRouterNetworkIPv4 || req.Address != "198.18.0.3" {
		t.Fatalf("unexpected request target: %+v", req)
	}
	if req.FlowID == "" || req.RequestID != req.FlowID {
		t.Fatalf("unexpected flow/request id: %+v", req)
	}
	if req.AssociationV2 == nil || req.AssociationV2.RouteNodeID != "3" || req.AssociationV2.RouteTarget != "1>2>3" {
		t.Fatalf("unexpected association: %+v", req.AssociationV2)
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

func TestProbeVirtualRouterIPv4Destination(t *testing.T) {
	packet := buildProbeVirtualRouterTestIPv4Packet(t, "198.18.0.2", "198.18.0.9")
	if got := probeVirtualRouterIPv4Destination(packet); got != "198.18.0.9" {
		t.Fatalf("dst=%q, want 198.18.0.9", got)
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

func TestProbeVirtualRouterPacketStreamKey(t *testing.T) {
	rt := &probeChainRuntime{cfg: probeChainRuntimeConfig{chainID: "chain-a"}}
	got := probeVirtualRouterPacketStreamKey(rt, probeChainBridgeRoleToNext, "198.18.0.9", []string{"node-1", "node-2"})
	if got != "chain-a|to_next|198.18.0.9|1>2" {
		t.Fatalf("key=%q", got)
	}
}

func TestProbeVirtualRouterPacketStreamCacheReuseAndDrop(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	t.Cleanup(func() { closeProbeVirtualRouterPacketStreams("test cleanup") })

	key := "chain-a|to_next|198.18.0.9|1>2"
	item := &probeVirtualRouterPacketStream{key: key, stream: left, openedAt: time.Now(), lastUsed: time.Now()}
	probeVirtualRouterStreamState.mu.Lock()
	probeVirtualRouterStreamState.streams = map[string]*probeVirtualRouterPacketStream{key: item}
	probeVirtualRouterStreamState.mu.Unlock()

	if got := reusableProbeVirtualRouterPacketStream(key, time.Now()); got != item {
		t.Fatalf("expected cached stream")
	}
	dropProbeVirtualRouterPacketStream(item)
	if got := reusableProbeVirtualRouterPacketStream(key, time.Now()); got != nil {
		t.Fatalf("expected dropped stream, got=%v", got)
	}
}

func TestProbeVirtualRouterPacketStreamCacheExpires(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	t.Cleanup(func() { closeProbeVirtualRouterPacketStreams("test cleanup") })

	key := "chain-a|to_next|198.18.0.9|1>2"
	item := &probeVirtualRouterPacketStream{
		key:      key,
		stream:   left,
		openedAt: time.Now().Add(-2 * probeVirtualRouterStreamIdleTTL),
		lastUsed: time.Now().Add(-2 * probeVirtualRouterStreamIdleTTL),
	}
	probeVirtualRouterStreamState.mu.Lock()
	probeVirtualRouterStreamState.streams = map[string]*probeVirtualRouterPacketStream{key: item}
	probeVirtualRouterStreamState.mu.Unlock()

	if got := reusableProbeVirtualRouterPacketStream(key, time.Now()); got != nil {
		t.Fatalf("expected expired stream to be removed, got=%v", got)
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
