//go:build linux_router

package main

import (
	"encoding/binary"
	"testing"
)

func testProbeLinuxRouterIPv4Packet(src [4]byte, dst [4]byte) []byte {
	packet := make([]byte, 20)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[9] = 17
	copy(packet[12:16], src[:])
	copy(packet[16:20], dst[:])
	return packet
}

func TestValidateProbeLinuxRouterSnapshotSHA(t *testing.T) {
	snapshot := probeLinuxRouterSnapshot{
		Version: 1, NodeID: "21", Revision: 2,
		GatewayProxy: probeLinuxRouterGatewayConfig{Interface: "auto", GatewayAddress: "192.168.1.150/24", UpstreamGateway: "192.168.1.1", LANCIDRs: []string{"192.168.1.0/24"}, DNSEnabled: true},
		LocalIPProxy: probeLinuxRouterLocalIPConfig{PublishedCIDRs: []string{"192.168.1.0/24"}, AllowedNodeIDs: []string{"1"}},
	}
	snapshot.SHA256 = probeLinuxRouterSnapshotSHA256(snapshot)
	if err := validateProbeLinuxRouterSnapshot(&snapshot, "21"); err != nil {
		t.Fatal(err)
	}
	snapshot.GatewayProxy.UpstreamGateway = "192.168.1.2"
	if err := validateProbeLinuxRouterSnapshot(&snapshot, "21"); err == nil {
		t.Fatal("tampered snapshot unexpectedly accepted")
	}
}

func TestProbeLinuxRouterOnlyAllowsConfiguredLANSource(t *testing.T) {
	oldDesired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	t.Cleanup(func() {
		probeLinuxRouterRuntimeState.mu.Lock()
		probeLinuxRouterRuntimeState.desired = oldDesired
		probeLinuxRouterRuntimeState.mu.Unlock()
	})
	probeLinuxRouterRuntimeState.mu.Lock()
	probeLinuxRouterRuntimeState.desired = &probeLinuxRouterSnapshot{GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: true, LANCIDRs: []string{"192.168.1.0/24"}}}
	probeLinuxRouterRuntimeState.mu.Unlock()
	if !probeLinuxRouterAllowsForwardedTUNPacket(testProbeLinuxRouterIPv4Packet([4]byte{192, 168, 1, 20}, [4]byte{1, 1, 1, 1}), "1.1.1.1", nil) {
		t.Fatal("configured LAN source was rejected")
	}
	if probeLinuxRouterAllowsForwardedTUNPacket(testProbeLinuxRouterIPv4Packet([4]byte{10, 0, 0, 20}, [4]byte{1, 1, 1, 1}), "1.1.1.1", nil) {
		t.Fatal("host/non-LAN source was accepted")
	}
}

func TestProbeLinuxRouterAllowsPublishedSubnetReturnWithGatewayDisabled(t *testing.T) {
	oldDesired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	t.Cleanup(func() {
		probeLinuxRouterRuntimeState.mu.Lock()
		probeLinuxRouterRuntimeState.desired = oldDesired
		probeLinuxRouterRuntimeState.mu.Unlock()
	})
	probeLinuxRouterRuntimeState.mu.Lock()
	probeLinuxRouterRuntimeState.desired = &probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: false, LANCIDRs: []string{"192.168.1.0/24"}},
		LocalIPProxy: probeLinuxRouterLocalIPConfig{Enabled: true, PublishedCIDRs: []string{"192.168.50.0/24"}},
	}
	probeLinuxRouterRuntimeState.mu.Unlock()
	if !probeLinuxRouterAllowsForwardedTUNPacket(testProbeLinuxRouterIPv4Packet([4]byte{192, 168, 50, 20}, [4]byte{198, 18, 0, 3}), "198.18.0.3", nil) {
		t.Fatal("published subnet return packet was rejected when local IP proxy was enabled independently")
	}
	if probeLinuxRouterAllowsForwardedTUNPacket(testProbeLinuxRouterIPv4Packet([4]byte{192, 168, 1, 20}, [4]byte{198, 18, 0, 3}), "198.18.0.3", nil) {
		t.Fatal("gateway LAN source was accepted while gateway proxy was disabled")
	}
}

func TestCurrentProbeLinuxRouterReportUsesBestHealthyAdjacentLatency(t *testing.T) {
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = map[string]*probeVirtualRouterRuntimeStats{
		"vrouter-21-1": {LastPingLatencyMS: 42},
		"vrouter-21-2": {LastPingLatencyMS: 17},
		"vrouter-21-3": {LastPingLatencyMS: 3, LastPingError: "timeout"},
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldStats
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	if got := currentProbeLinuxRouterReport().LatencyMS; got != 17 {
		t.Fatalf("latency_ms=%d, want best healthy adjacent RTT 17", got)
	}
}

func TestProbeLinuxRouterIgnoresControllerSnapshot(t *testing.T) {
	probeLinuxRouterRuntimeState.mu.Lock()
	oldNodeID := probeLinuxRouterRuntimeState.nodeID
	oldDesired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	local := &probeLinuxRouterSnapshot{Version: 1, NodeID: "21", Revision: 3, GatewayProxy: probeLinuxRouterGatewayConfig{GatewayAddress: "192.168.1.150/24"}}
	probeLinuxRouterRuntimeState.nodeID = "21"
	probeLinuxRouterRuntimeState.desired = cloneProbeLinuxRouterSnapshot(local)
	probeLinuxRouterRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		probeLinuxRouterRuntimeState.mu.Lock()
		probeLinuxRouterRuntimeState.nodeID = oldNodeID
		probeLinuxRouterRuntimeState.desired = oldDesired
		probeLinuxRouterRuntimeState.mu.Unlock()
	})

	controller := &probeLinuxRouterSnapshot{Version: 1, NodeID: "21", Revision: 99, GatewayProxy: probeLinuxRouterGatewayConfig{GatewayAddress: "192.168.99.1/24"}}
	if err := applyProbeLinuxRouterSnapshot(controller, "21"); err != nil {
		t.Fatal(err)
	}
	desired, _, _ := currentProbeLinuxRouterLocalState()
	if desired == nil || desired.Revision != 3 || desired.GatewayProxy.GatewayAddress != "192.168.1.150/24" {
		t.Fatalf("controller snapshot changed local config: %+v", desired)
	}
	if err := applyProbeLinuxRouterSnapshot(nil, "21"); err != nil {
		t.Fatal(err)
	}
	desired, _, _ = currentProbeLinuxRouterLocalState()
	if desired == nil || desired.Revision != 3 {
		t.Fatalf("nil controller snapshot cleared local config: %+v", desired)
	}
}
