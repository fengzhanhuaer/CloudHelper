//go:build linux_router

package main

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/cloudhelper/probe_node/internal/tlssniff"
)

func TestProbeLinuxRouterSNIRejectsSplitClientHello(t *testing.T) {
	useProbeLinuxRouterSNITestState(t, []probeVirtualRouterRouteRule{{
		ID:      "reject-ads",
		Name:    "Reject ads",
		Action:  "reject",
		Entries: []string{"domain_suffix:example.com"},
	}})
	var resets [][]byte
	probeLinuxRouterSNIResetWriter = func(packet []byte) error {
		resets = append(resets, append([]byte(nil), packet...))
		return nil
	}
	hello := buildProbeLinuxRouterTestClientHello("ads.example.com")
	assertProbeLinuxRouterTestClientHello(t, hello, "ads.example.com")
	firstLen := 11
	if probeLinuxRouterSNIRejectsPacket(buildProbeLinuxRouterTestTCPPacket(1000, 0x18, hello[:firstLen])) {
		t.Fatal("incomplete ClientHello was rejected before SNI was available")
	}
	second := buildProbeLinuxRouterTestTCPPacket(1000+uint32(firstLen), 0x18, hello[firstLen:])
	if !probeLinuxRouterSNIRejectsPacket(second) {
		t.Fatal("completed ClientHello did not match the reject rule")
	}
	if len(resets) != 1 {
		t.Fatalf("reset count=%d, want 1", len(resets))
	}
	reset := resets[0]
	if len(reset) != 40 || !net.IP(reset[12:16]).Equal(net.IPv4(203, 0, 113, 8)) || !net.IP(reset[16:20]).Equal(net.IPv4(192, 168, 51, 20)) {
		t.Fatalf("unexpected reset IP packet: %v", reset)
	}
	if binary.BigEndian.Uint16(reset[20:22]) != 443 || binary.BigEndian.Uint16(reset[22:24]) != 43210 || binary.BigEndian.Uint32(reset[24:28]) != 9000 || reset[33] != 0x04 {
		t.Fatalf("unexpected reset TCP header: %v", reset[20:])
	}
	if summary := probeVirtualRouterPacketChecksumSummary(reset); summary != "ip_checksum=ok tcp_checksum=ok" {
		t.Fatalf("reset checksums=%q", summary)
	}
	observations, _, err := snapshotProbeDomainObservations()
	if err != nil || len(observations) != 1 || observations[0].Domain != "ads.example.com" || observations[0].SNIObservations != 1 || observations[0].LastSource != "192.168.51.20" {
		t.Fatalf("SNI observation=%+v err=%v", observations, err)
	}
	if !probeLinuxRouterSNIRejectsPacket(second) {
		t.Fatal("retransmitted packet from a rejected flow was allowed")
	}
	if len(resets) != 1 {
		t.Fatalf("retransmission emitted %d resets, want one per rejected flow", len(resets))
	}
	if probeLinuxRouterSNIRejectsPacket(buildProbeLinuxRouterTestTCPPacket(1000+uint32(len(hello)), 0x04, nil)) {
		t.Fatal("RST should be allowed to close the direct socket")
	}
}

func TestBuildProbeLinuxRouterSNIResetPacketAcknowledgesUnackedSegment(t *testing.T) {
	packet := buildProbeLinuxRouterTestTCPPacket(1200, 0x08, []byte("hello"))
	reset, ok := buildProbeLinuxRouterSNIResetPacket(packet)
	if !ok {
		t.Fatal("reset packet was not built")
	}
	if reset[33] != 0x14 || binary.BigEndian.Uint32(reset[24:28]) != 0 || binary.BigEndian.Uint32(reset[28:32]) != 1205 {
		t.Fatalf("unexpected RST+ACK header: %v", reset[20:])
	}
	if summary := probeVirtualRouterPacketChecksumSummary(reset); summary != "ip_checksum=ok tcp_checksum=ok" {
		t.Fatalf("reset checksums=%q", summary)
	}
}

func TestProbeLinuxRouterSNIReassemblesOutOfOrderClientHello(t *testing.T) {
	useProbeLinuxRouterSNITestState(t, []probeVirtualRouterRouteRule{{
		Name:    "Reject blocked",
		Action:  "reject",
		Entries: []string{"domain_suffix:blocked.example"},
	}})
	hello := buildProbeLinuxRouterTestClientHello("blocked.example")
	assertProbeLinuxRouterTestClientHello(t, hello, "blocked.example")
	firstEnd := 8
	middleEnd := 29
	if probeLinuxRouterSNIRejectsPacket(buildProbeLinuxRouterTestTCPPacket(5000, 0x18, hello[:firstEnd])) {
		t.Fatal("first fragment was rejected")
	}
	if probeLinuxRouterSNIRejectsPacket(buildProbeLinuxRouterTestTCPPacket(5000+uint32(middleEnd), 0x18, hello[middleEnd:])) {
		t.Fatal("out-of-order tail was rejected before reassembly")
	}
	if !probeLinuxRouterSNIRejectsPacket(buildProbeLinuxRouterTestTCPPacket(5000+uint32(firstEnd), 0x18, hello[firstEnd:middleEnd])) {
		t.Fatal("ClientHello was not rejected after filling the sequence gap")
	}
}

func TestProbeLinuxRouterSNIFallsThroughAndObservesRuleRemoval(t *testing.T) {
	useProbeLinuxRouterSNITestState(t, []probeVirtualRouterRouteRule{{
		Name:    "Reject blocked",
		Action:  "reject",
		Entries: []string{"domain_suffix:blocked.example"},
	}})
	allowed := buildProbeLinuxRouterTestClientHello("allowed.example")
	if probeLinuxRouterSNIRejectsPacket(buildProbeLinuxRouterTestTCPPacket(7000, 0x18, allowed)) {
		t.Fatal("non-matching SNI was rejected")
	}

	blocked := buildProbeLinuxRouterTestClientHello("blocked.example")
	packet := buildProbeLinuxRouterTestTCPPacket(8000, 0x18, blocked)
	if !probeLinuxRouterSNIRejectsPacket(packet) {
		t.Fatal("matching SNI was not rejected")
	}
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config.RouteRules = nil
	probeVirtualRouterState.mu.Unlock()
	if probeLinuxRouterSNIRejectsPacket(packet) {
		t.Fatal("flow remained rejected after its rule was removed")
	}
}

func assertProbeLinuxRouterTestClientHello(t *testing.T, hello []byte, want string) {
	t.Helper()
	host, complete := tlssniff.ClientHelloServerName(hello)
	if !complete || host != want {
		t.Fatalf("test ClientHello host=%q complete=%t, want %q", host, complete, want)
	}
	if action := probeLinuxRouterDomainAction(host); action != "reject" {
		t.Fatalf("test ClientHello action=%q, want reject", action)
	}
}

func TestProbeLinuxRouterSNIFlowCleanupIsBounded(t *testing.T) {
	useProbeLinuxRouterSNITestState(t, nil)
	probeLinuxRouterSNIState.mu.Lock()
	now := time.Now()
	for i := 0; i < probeLinuxRouterSNIMaxFlows; i++ {
		key := probeLinuxRouterSNIFlowKey{sourceIP: uint32(i + 1)}
		probeLinuxRouterSNIState.flows[key] = &probeLinuxRouterSNIFlow{updatedAt: now.Add(time.Duration(i) * time.Nanosecond)}
	}
	makeProbeLinuxRouterSNIFlowRoomLocked(now.Add(time.Second))
	got := len(probeLinuxRouterSNIState.flows)
	probeLinuxRouterSNIState.mu.Unlock()
	if got >= probeLinuxRouterSNIMaxFlows {
		t.Fatalf("flow count=%d, want room below %d", got, probeLinuxRouterSNIMaxFlows)
	}
}

func useProbeLinuxRouterSNITestState(t *testing.T, rules []probeVirtualRouterRouteRule) {
	t.Helper()
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeDomainObservations()
	probeVirtualRouterState.mu.Lock()
	oldConfig := probeVirtualRouterState.config
	probeVirtualRouterState.config.RouteRules = rules
	probeVirtualRouterState.mu.Unlock()
	probeLinuxRouterSNIState.mu.Lock()
	oldFlows := probeLinuxRouterSNIState.flows
	oldCleanup := probeLinuxRouterSNIState.lastCleanup
	probeLinuxRouterSNIState.flows = make(map[probeLinuxRouterSNIFlowKey]*probeLinuxRouterSNIFlow)
	probeLinuxRouterSNIState.lastCleanup = time.Time{}
	probeLinuxRouterSNIState.mu.Unlock()
	oldResetWriter := probeLinuxRouterSNIResetWriter
	probeLinuxRouterSNIResetWriter = func([]byte) error { return nil }
	t.Cleanup(func() {
		resetProbeDomainObservations()
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = oldConfig
		probeVirtualRouterState.mu.Unlock()
		probeLinuxRouterSNIState.mu.Lock()
		probeLinuxRouterSNIState.flows = oldFlows
		probeLinuxRouterSNIState.lastCleanup = oldCleanup
		probeLinuxRouterSNIState.mu.Unlock()
		probeLinuxRouterSNIResetWriter = oldResetWriter
	})
}

func buildProbeLinuxRouterTestTCPPacket(sequence uint32, flags byte, payload []byte) []byte {
	packet := make([]byte, 20+20+len(payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64
	packet[9] = 6
	copy(packet[12:16], []byte{192, 168, 51, 20})
	copy(packet[16:20], []byte{203, 0, 113, 8})
	tcpPacket := packet[20:]
	binary.BigEndian.PutUint16(tcpPacket[0:2], 43210)
	binary.BigEndian.PutUint16(tcpPacket[2:4], 443)
	binary.BigEndian.PutUint32(tcpPacket[4:8], sequence)
	if flags&0x10 != 0 {
		binary.BigEndian.PutUint32(tcpPacket[8:12], 9000)
	}
	tcpPacket[12] = 5 << 4
	tcpPacket[13] = flags
	copy(tcpPacket[20:], payload)
	return packet
}

func buildProbeLinuxRouterTestClientHello(host string) []byte {
	serverName := make([]byte, 5+len(host))
	binary.BigEndian.PutUint16(serverName[0:2], uint16(3+len(host)))
	serverName[2] = 0
	binary.BigEndian.PutUint16(serverName[3:5], uint16(len(host)))
	copy(serverName[5:], host)
	extension := make([]byte, 4+len(serverName))
	binary.BigEndian.PutUint16(extension[2:4], uint16(len(serverName)))
	copy(extension[4:], serverName)
	body := make([]byte, 2+32+1+2+2+1+1+2+len(extension))
	body[0], body[1] = 0x03, 0x03
	offset := 34
	body[offset] = 0
	offset++
	binary.BigEndian.PutUint16(body[offset:offset+2], 2)
	offset += 2
	body[offset], body[offset+1] = 0x13, 0x01
	offset += 2
	body[offset], body[offset+1] = 1, 0
	offset += 2
	binary.BigEndian.PutUint16(body[offset:offset+2], uint16(len(extension)))
	offset += 2
	copy(body[offset:], extension)
	handshake := make([]byte, 4+len(body))
	handshake[0] = 0x01
	handshake[1] = byte(len(body) >> 16)
	handshake[2] = byte(len(body) >> 8)
	handshake[3] = byte(len(body))
	copy(handshake[4:], body)
	record := make([]byte, 5+len(handshake))
	record[0], record[1], record[2] = 0x16, 0x03, 0x01
	binary.BigEndian.PutUint16(record[3:5], uint16(len(handshake)))
	copy(record[5:], handshake)
	return record
}
