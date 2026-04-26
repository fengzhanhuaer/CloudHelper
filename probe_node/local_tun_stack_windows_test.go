//go:build windows

package main

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"
)

func TestParseProbeLocalTUNIPv4TargetTCP(t *testing.T) {
	packet := make([]byte, 40)
	packet[0] = 0x45
	packet[9] = 6
	copy(packet[16:20], []byte{1, 2, 3, 4})
	binary.BigEndian.PutUint16(packet[22:24], uint16(443))

	network, target, err := parseProbeLocalTUNPacketTarget(packet)
	if err != nil {
		t.Fatalf("parse target failed: %v", err)
	}
	if network != "tcp" {
		t.Fatalf("network=%q", network)
	}
	if target != "1.2.3.4:443" {
		t.Fatalf("target=%q", target)
	}
}

func TestParseProbeLocalTUNIPv4TargetUDP(t *testing.T) {
	packet := make([]byte, 40)
	packet[0] = 0x45
	packet[9] = 17
	copy(packet[16:20], []byte{8, 8, 8, 8})
	binary.BigEndian.PutUint16(packet[22:24], uint16(53))

	network, target, err := parseProbeLocalTUNPacketTarget(packet)
	if err != nil {
		t.Fatalf("parse target failed: %v", err)
	}
	if network != "udp" {
		t.Fatalf("network=%q", network)
	}
	if target != "8.8.8.8:53" {
		t.Fatalf("target=%q", target)
	}
}

func TestParseProbeLocalTUNIPv6TargetTCP(t *testing.T) {
	packet := make([]byte, 60)
	packet[0] = 0x60
	packet[6] = 6
	dst := net.ParseIP("2001:db8::1").To16()
	copy(packet[24:40], dst)
	binary.BigEndian.PutUint16(packet[42:44], uint16(443))

	network, target, err := parseProbeLocalTUNPacketTarget(packet)
	if err != nil {
		t.Fatalf("parse target failed: %v", err)
	}
	if network != "tcp" {
		t.Fatalf("network=%q", network)
	}
	if !strings.Contains(target, "2001:db8::1") || !strings.HasSuffix(target, ":443") {
		t.Fatalf("target=%q", target)
	}
}

func TestProbeLocalTUNSimplePacketStackWriteTunnelValidatesNode(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{{Group: "media", Rules: []string{"domain_suffix:example.com"}}}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{{Group: "media", Action: "tunnel", TunnelNodeID: "chain:chain-proxy-1"}}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	dnsDecision := resolveProbeLocalProxyRouteDecisionByDomain("api.example.com")
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", dnsDecision)
	if !ok {
		t.Fatal("allocate fake ip failed")
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	packet := make([]byte, 40)
	packet[0] = 0x45
	packet[9] = 6
	ip := net.ParseIP(fakeIP).To4()
	copy(packet[16:20], ip)
	binary.BigEndian.PutUint16(packet[22:24], uint16(443))

	stack := &probeLocalTUNSimplePacketStack{}
	n, err := stack.Write(packet)
	if err != nil {
		t.Fatalf("write packet failed: %v", err)
	}
	if n != len(packet) {
		t.Fatalf("n=%d len=%d", n, len(packet))
	}
}

func TestProbeLocalTUNSimplePacketStackWriteRejectRoute(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{{Group: "blocked", Rules: []string{"domain_suffix:blocked.example"}}}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{{Group: "blocked", Action: "reject"}}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	dnsDecision := resolveProbeLocalProxyRouteDecisionByDomain("x.blocked.example")
	fakeIP, ok := allocateProbeLocalDNSFakeIP("x.blocked.example", dnsDecision)
	if !ok {
		t.Fatal("allocate fake ip failed")
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	packet := make([]byte, 40)
	packet[0] = 0x45
	packet[9] = 6
	ip := net.ParseIP(fakeIP).To4()
	copy(packet[16:20], ip)
	binary.BigEndian.PutUint16(packet[22:24], uint16(443))

	stack := &probeLocalTUNSimplePacketStack{}
	n, err := stack.Write(packet)
	if err != nil {
		t.Fatalf("write packet failed: %v", err)
	}
	if n != len(packet) {
		t.Fatalf("n=%d len=%d", n, len(packet))
	}
}

func TestProbeLocalTUNSimplePacketStackWriteClosed(t *testing.T) {
	stack := &probeLocalTUNSimplePacketStack{closed: true}
	_, err := stack.Write([]byte{0x45})
	if err == nil {
		t.Fatal("expected closed error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "closed") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseProbeLocalTUNPacketTargetRejectUnsupportedVersion(t *testing.T) {
	_, _, err := parseProbeLocalTUNPacketTarget([]byte{0x10})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatalf("unexpected err=%v", err)
	}
}

func TestEnsureProbeLocalDirectBypassForTargetRefCountAndRelease(t *testing.T) {
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(resetProbeLocalDirectBypassStateForTest)

	acquireCalls := 0
	releaseCalls := 0
	probeLocalAcquireDirectBypassRoute = func(host string) (func(), error) {
		acquireCalls++
		if host != "1.2.3.4" {
			t.Fatalf("host=%q", host)
		}
		return func() {}, nil
	}
	probeLocalReleaseDirectBypassRoute = func(host string) {
		releaseCalls++
		if host != "1.2.3.4" {
			t.Fatalf("host=%q", host)
		}
	}

	if err := ensureProbeLocalDirectBypassForTarget("1.2.3.4:443"); err != nil {
		t.Fatalf("ensure bypass #1 failed: %v", err)
	}
	if err := ensureProbeLocalDirectBypassForTarget("1.2.3.4:8443"); err != nil {
		t.Fatalf("ensure bypass #2 failed: %v", err)
	}
	if acquireCalls != 1 {
		t.Fatalf("acquireCalls=%d", acquireCalls)
	}

	releaseProbeLocalDirectBypassForHost("1.2.3.4")
	if releaseCalls != 0 {
		t.Fatalf("releaseCalls=%d", releaseCalls)
	}
	releaseProbeLocalDirectBypassForHost("1.2.3.4")
	if releaseCalls != 1 {
		t.Fatalf("releaseCalls=%d", releaseCalls)
	}
}

func TestProbeLocalTUNSimplePacketStackCloseReleasesBypass(t *testing.T) {
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(resetProbeLocalDirectBypassStateForTest)

	releaseCalls := 0
	probeLocalAcquireDirectBypassRoute = func(host string) (func(), error) { return func() {}, nil }
	probeLocalReleaseDirectBypassRoute = func(host string) {
		releaseCalls++
		if host != "1.2.3.4" {
			t.Fatalf("host=%q", host)
		}
	}

	if err := ensureProbeLocalDirectBypassForTarget("1.2.3.4:443"); err != nil {
		t.Fatalf("ensure bypass failed: %v", err)
	}
	stack := &probeLocalTUNSimplePacketStack{}
	if err := stack.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if releaseCalls != 1 {
		t.Fatalf("releaseCalls=%d", releaseCalls)
	}
}
