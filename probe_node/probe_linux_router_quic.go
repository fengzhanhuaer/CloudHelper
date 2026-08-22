//go:build linux_router

package main

import (
	"bytes"
	"encoding/binary"
	"net"
	"sync"
	"time"

	"github.com/cloudhelper/probe_node/internal/quicsniff"
	"github.com/cloudhelper/probe_node/internal/tlssniff"
)

const (
	probeLinuxRouterQUICMaxClientHello = 16 * 1024
	probeLinuxRouterQUICFlowTTL        = 15 * time.Second
	probeLinuxRouterQUICMaxFlows       = 1024
)

type probeLinuxRouterQUICFlowKey struct {
	sourceIP        uint32
	destinationIP   uint32
	sourcePort      uint16
	destinationPort uint16
}

type probeLinuxRouterQUICPacket struct {
	key     probeLinuxRouterQUICFlowKey
	payload []byte
}

type probeLinuxRouterQUICFlow struct {
	updatedAt               time.Time
	destinationConnectionID []byte
	largestPacket           uint64
	hasLargestPacket        bool
	crypto                  []byte
	received                []bool
	observed                bool
	observedDomain          string
	observedAction          string
}

var probeLinuxRouterQUICState = struct {
	mu          sync.Mutex
	flows       map[probeLinuxRouterQUICFlowKey]*probeLinuxRouterQUICFlow
	lastCleanup time.Time
}{
	flows: make(map[probeLinuxRouterQUICFlowKey]*probeLinuxRouterQUICFlow),
}

func resetProbeLinuxRouterQUICState() {
	probeLinuxRouterQUICState.mu.Lock()
	probeLinuxRouterQUICState.flows = make(map[probeLinuxRouterQUICFlowKey]*probeLinuxRouterQUICFlow)
	probeLinuxRouterQUICState.lastCleanup = time.Time{}
	probeLinuxRouterQUICState.mu.Unlock()
}

// probeLinuxRouterQUICRejectsPacket passively decrypts client Initial packets.
// Unsupported versions, ECH-only handshakes, and malformed packets fail open.
func probeLinuxRouterQUICRejectsPacket(packet []byte) bool {
	parsed, ok := parseProbeLinuxRouterQUICPacket(packet)
	if !ok {
		return false
	}
	now := time.Now()
	probeLinuxRouterQUICState.mu.Lock()
	defer probeLinuxRouterQUICState.mu.Unlock()
	cleanupProbeLinuxRouterQUICFlowsLocked(now)
	flow := probeLinuxRouterQUICState.flows[parsed.key]
	largestPacket, hasLargestPacket := uint64(0), false
	if flow != nil {
		largestPacket, hasLargestPacket = flow.largestPacket, flow.hasLargestPacket
	}
	initial, ok := quicsniff.ParseClientInitial(parsed.payload, largestPacket, hasLargestPacket)
	freshConnection := false
	if !ok && flow != nil {
		initial, ok = quicsniff.ParseClientInitial(parsed.payload, 0, false)
		freshConnection = ok
	}
	if !ok {
		if flow != nil && flow.observed {
			flow.updatedAt = now
			return flow.observedAction == "reject"
		}
		return false
	}
	if flow == nil || freshConnection || !bytes.Equal(flow.destinationConnectionID, initial.DestinationConnectionID) {
		makeProbeLinuxRouterQUICFlowRoomLocked(now)
		flow = &probeLinuxRouterQUICFlow{
			updatedAt:               now,
			destinationConnectionID: append([]byte(nil), initial.DestinationConnectionID...),
		}
		probeLinuxRouterQUICState.flows[parsed.key] = flow
	} else if flow.observed {
		flow.updatedAt = now
		return flow.observedAction == "reject"
	}
	flow.updatedAt = now
	if !flow.hasLargestPacket || initial.PacketNumber > flow.largestPacket {
		flow.largestPacket = initial.PacketNumber
		flow.hasLargestPacket = true
	}
	for _, fragment := range initial.Fragments {
		appendProbeLinuxRouterQUICCryptoFragment(flow, fragment.Offset, fragment.Data)
	}
	contiguous := probeLinuxRouterQUICContiguousCrypto(flow)
	serverName, complete := tlssniff.ClientHelloHandshakeServerName(contiguous)
	if !complete {
		return false
	}
	flow.observed = true
	if !tlssniff.IsValidServerName(serverName) {
		return false
	}
	flow.observedDomain = serverName
	flow.observedAction = probeLinuxRouterDomainRouteAction(serverName, parsed.key.destinationIP)
	recordProbeDomainObservation(serverName, "quic", probeLinuxRouterQUICFlowSource(parsed.key), flow.observedAction, nil, nil)
	return flow.observedAction == "reject"
}

func parseProbeLinuxRouterQUICPacket(packet []byte) (probeLinuxRouterQUICPacket, bool) {
	if len(packet) < 28 || packet[0]>>4 != 4 || packet[9] != 17 {
		return probeLinuxRouterQUICPacket{}, false
	}
	ihl := int(packet[0]&0x0f) * 4
	if ihl < 20 || len(packet) < ihl+8 || binary.BigEndian.Uint16(packet[6:8])&0x3fff != 0 {
		return probeLinuxRouterQUICPacket{}, false
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen < ihl+8 || totalLen > len(packet) {
		return probeLinuxRouterQUICPacket{}, false
	}
	udp := packet[ihl:totalLen]
	udpLen := int(binary.BigEndian.Uint16(udp[4:6]))
	if udpLen < 8 || udpLen > len(udp) {
		return probeLinuxRouterQUICPacket{}, false
	}
	return probeLinuxRouterQUICPacket{
		key: probeLinuxRouterQUICFlowKey{
			sourceIP:        binary.BigEndian.Uint32(packet[12:16]),
			destinationIP:   binary.BigEndian.Uint32(packet[16:20]),
			sourcePort:      binary.BigEndian.Uint16(udp[0:2]),
			destinationPort: binary.BigEndian.Uint16(udp[2:4]),
		},
		payload: udp[8:udpLen],
	}, true
}

func appendProbeLinuxRouterQUICCryptoFragment(flow *probeLinuxRouterQUICFlow, offset uint64, data []byte) {
	if flow == nil || len(data) == 0 || offset >= probeLinuxRouterQUICMaxClientHello || uint64(len(data)) > probeLinuxRouterQUICMaxClientHello-offset {
		return
	}
	if flow.crypto == nil {
		flow.crypto = make([]byte, probeLinuxRouterQUICMaxClientHello)
		flow.received = make([]bool, probeLinuxRouterQUICMaxClientHello)
	}
	start := int(offset)
	copy(flow.crypto[start:start+len(data)], data)
	for i := start; i < start+len(data); i++ {
		flow.received[i] = true
	}
}

func probeLinuxRouterQUICContiguousCrypto(flow *probeLinuxRouterQUICFlow) []byte {
	if flow == nil || len(flow.received) == 0 {
		return nil
	}
	end := 0
	for end < len(flow.received) && flow.received[end] {
		end++
	}
	return flow.crypto[:end]
}

func probeLinuxRouterQUICFlowSource(key probeLinuxRouterQUICFlowKey) string {
	return net.IPv4(byte(key.sourceIP>>24), byte(key.sourceIP>>16), byte(key.sourceIP>>8), byte(key.sourceIP)).String()
}

func cleanupProbeLinuxRouterQUICFlowsLocked(now time.Time) {
	if !probeLinuxRouterQUICState.lastCleanup.IsZero() && now.Sub(probeLinuxRouterQUICState.lastCleanup) < time.Second && len(probeLinuxRouterQUICState.flows) < probeLinuxRouterQUICMaxFlows {
		return
	}
	for key, flow := range probeLinuxRouterQUICState.flows {
		if flow == nil || now.Sub(flow.updatedAt) > probeLinuxRouterQUICFlowTTL {
			delete(probeLinuxRouterQUICState.flows, key)
		}
	}
	probeLinuxRouterQUICState.lastCleanup = now
}

func makeProbeLinuxRouterQUICFlowRoomLocked(now time.Time) {
	cleanupProbeLinuxRouterQUICFlowsLocked(now)
	if len(probeLinuxRouterQUICState.flows) < probeLinuxRouterQUICMaxFlows {
		return
	}
	var oldestKey probeLinuxRouterQUICFlowKey
	var oldest time.Time
	for key, flow := range probeLinuxRouterQUICState.flows {
		if flow != nil && (oldest.IsZero() || flow.updatedAt.Before(oldest)) {
			oldestKey, oldest = key, flow.updatedAt
		}
	}
	delete(probeLinuxRouterQUICState.flows, oldestKey)
}
