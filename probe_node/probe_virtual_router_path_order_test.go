package main

import (
	"net"
	"reflect"
	"testing"
	"time"
)

func TestProbeVirtualRouterHopsBeforeMeasuredRTT(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	direct := []string{"1", "4"}
	relay := []string{"1", "2", "4"}
	fast := []string{"1", "3", "4"}
	recordProbeVirtualRouterPathRTTSuccess(direct, 200*time.Millisecond, "4")
	recordProbeVirtualRouterPathRTTSuccess(relay, 80*time.Millisecond, "4")
	recordProbeVirtualRouterPathRTTSuccess(fast, 20*time.Millisecond, "4")
	if got := selectProbeVirtualRouterBestPath([][]string{fast, relay, direct}, true); !reflect.DeepEqual(got, direct) {
		t.Fatalf("hops first: %v", got)
	}
	if got := selectProbeVirtualRouterBestPath([][]string{relay, fast}, true); !reflect.DeepEqual(got, fast) {
		t.Fatalf("equal-hop RTT: %v", got)
	}
}

func TestProbeVirtualRouterAdjacentEndpointRTTOrder(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	slow := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "slow", peerNodeID: "2", dialer: true}}
	fast := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "fast", peerNodeID: "2", dialer: true}}
	probeVirtualRouterRuntimeState.mu.Lock()
	old := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{"slow": slow, "fast": fast}
	probeVirtualRouterRuntimeState.mu.Unlock()
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{"slow": {LastPingLatencyMS: 200}, "fast": {LastPingLatencyMS: 20}}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = old
		probeVirtualRouterRuntimeState.mu.Unlock()
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldStats
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})
	for i := 0; i < 20; i++ {
		rt, _ := probeVirtualRouterRuntimeForAdjacentNode("2")
		if rt != fast {
			t.Fatal("map iteration changed best RTT selection")
		}
	}
	left, right := net.Pipe()
	defer right.Close()
	link := newProbeVirtualRouterFrameLink("packet|slow", slow, nil, nil)
	token := link.AttachCarrier(left, "live", "pipe")
	if token == nil {
		t.Fatal("attach")
	}
	defer stopProbeVirtualRouterFrameLink(link)
	probeVirtualRouterFrameLinkState.mu.Lock()
	probeVirtualRouterFrameLinkState.links["packet|slow"] = link
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if rt, _ := probeVirtualRouterRuntimeForAdjacentNode("2"); rt != slow {
		t.Fatal("live endpoint must beat disconnected fast endpoint")
	}
}
