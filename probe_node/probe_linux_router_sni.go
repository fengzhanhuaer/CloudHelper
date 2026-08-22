//go:build linux_router

package main

import (
	"encoding/binary"
	"net"
	"sync"
	"time"

	"github.com/cloudhelper/probe_node/internal/tlssniff"
)

const (
	probeLinuxRouterSNIMaxClientHello = 16 * 1024
	probeLinuxRouterSNIFlowTTL        = 15 * time.Second
	probeLinuxRouterSNIRejectTTL      = 5 * time.Minute
	probeLinuxRouterSNIMaxFlows       = 1024
)

type probeLinuxRouterSNIFlowKey struct {
	sourceIP        uint32
	destinationIP   uint32
	sourcePort      uint16
	destinationPort uint16
}

type probeLinuxRouterSNITCPPacket struct {
	key             probeLinuxRouterSNIFlowKey
	sequence        uint32
	acknowledgement uint32
	flags           byte
	payload         []byte
}

type probeLinuxRouterSNIFlow struct {
	updatedAt      time.Time
	nextSequence   uint32
	preface        []byte
	pending        map[uint32][]byte
	pendingBytes   int
	rejectedDomain string
}

var probeLinuxRouterSNIState = struct {
	mu          sync.Mutex
	flows       map[probeLinuxRouterSNIFlowKey]*probeLinuxRouterSNIFlow
	lastCleanup time.Time
}{
	flows: make(map[probeLinuxRouterSNIFlowKey]*probeLinuxRouterSNIFlow),
}

var probeLinuxRouterSNIResetWriter = writeProbeVirtualRouterLocalTUNPacket

func resetProbeLinuxRouterSNIState() {
	probeLinuxRouterSNIState.mu.Lock()
	probeLinuxRouterSNIState.flows = make(map[probeLinuxRouterSNIFlowKey]*probeLinuxRouterSNIFlow)
	probeLinuxRouterSNIState.lastCleanup = time.Time{}
	probeLinuxRouterSNIState.mu.Unlock()
}

// probeLinuxRouterSNIRejectsPacket observes forwarded TCP streams without
// delaying them. Once a ClientHello SNI matches a reject rule, subsequent
// packets in that client-to-server flow are dropped until it closes or expires.
func probeLinuxRouterSNIRejectsPacket(packet []byte) bool {
	parsed, ok := parseProbeLinuxRouterSNITCPPacket(packet)
	if !ok {
		return false
	}
	now := time.Now()
	probeLinuxRouterSNIState.mu.Lock()
	cleanupProbeLinuxRouterSNIFlowsLocked(now)
	reject, reset := probeLinuxRouterSNIRejectsParsedPacketLocked(parsed, now)
	probeLinuxRouterSNIState.mu.Unlock()
	if reset != "" {
		resetPacket, ok := buildProbeLinuxRouterSNIResetPacket(packet)
		if ok {
			if err := probeLinuxRouterSNIResetWriter(resetPacket); err != nil {
				logProbeWarnf("probe linux router SNI reject reset failed: source=%s domain=%s err=%v", probeLinuxRouterSNIFlowSource(parsed.key), reset, err)
			}
		}
	}
	return reject
}

func probeLinuxRouterSNIRejectsParsedPacketLocked(parsed probeLinuxRouterSNITCPPacket, now time.Time) (bool, string) {
	flow := probeLinuxRouterSNIState.flows[parsed.key]
	if parsed.flags&0x02 != 0 { // SYN starts a new incarnation of the tuple.
		delete(probeLinuxRouterSNIState.flows, parsed.key)
		flow = nil
	}
	if flow != nil && flow.rejectedDomain != "" {
		if parsed.flags&(0x01|0x04) != 0 { // Let FIN/RST close the direct socket.
			delete(probeLinuxRouterSNIState.flows, parsed.key)
			return false, ""
		}
		flow.updatedAt = now
		if probeLinuxRouterDomainRouteAction(flow.rejectedDomain, parsed.key.destinationIP) == "reject" {
			return true, ""
		}
		delete(probeLinuxRouterSNIState.flows, parsed.key)
		flow = nil
	}
	if len(parsed.payload) == 0 {
		return false, ""
	}
	if flow == nil {
		if parsed.payload[0] != 0x16 {
			return false, ""
		}
		makeProbeLinuxRouterSNIFlowRoomLocked(now)
		flow = &probeLinuxRouterSNIFlow{
			updatedAt:    now,
			nextSequence: parsed.sequence,
			pending:      make(map[uint32][]byte),
		}
		probeLinuxRouterSNIState.flows[parsed.key] = flow
	}
	flow.updatedAt = now
	appendProbeLinuxRouterSNIPayloadLocked(flow, parsed.sequence, parsed.payload)
	serverName, complete := tlssniff.ClientHelloServerName(flow.preface)
	if !complete && len(flow.preface) < probeLinuxRouterSNIMaxClientHello {
		return false, ""
	}
	if !tlssniff.IsValidServerName(serverName) {
		delete(probeLinuxRouterSNIState.flows, parsed.key)
		return false, ""
	}
	action := probeLinuxRouterDomainRouteAction(serverName, parsed.key.destinationIP)
	recordProbeDomainObservation(serverName, "sni", probeLinuxRouterSNIFlowSource(parsed.key), action, nil, nil)
	if action != "reject" {
		delete(probeLinuxRouterSNIState.flows, parsed.key)
		return false, ""
	}
	flow.rejectedDomain = serverName
	flow.preface = nil
	flow.pending = nil
	return true, serverName
}

func buildProbeLinuxRouterSNIResetPacket(packet []byte) ([]byte, bool) {
	parsed, ok := parseProbeLinuxRouterSNITCPPacket(packet)
	if !ok || parsed.flags&0x04 != 0 {
		return nil, false
	}
	reset := make([]byte, 40)
	reset[0] = 0x45
	reset[1] = packet[1]
	binary.BigEndian.PutUint16(reset[2:4], uint16(len(reset)))
	binary.BigEndian.PutUint16(reset[6:8], 0x4000)
	reset[8] = 64
	reset[9] = 6
	copy(reset[12:16], packet[16:20])
	copy(reset[16:20], packet[12:16])
	tcp := reset[20:]
	binary.BigEndian.PutUint16(tcp[0:2], parsed.key.destinationPort)
	binary.BigEndian.PutUint16(tcp[2:4], parsed.key.sourcePort)
	tcp[12] = 5 << 4
	if parsed.flags&0x10 != 0 {
		binary.BigEndian.PutUint32(tcp[4:8], parsed.acknowledgement)
		tcp[13] = 0x04
	} else {
		segmentLength := uint32(len(parsed.payload))
		if parsed.flags&0x02 != 0 {
			segmentLength++
		}
		if parsed.flags&0x01 != 0 {
			segmentLength++
		}
		binary.BigEndian.PutUint32(tcp[8:12], parsed.sequence+segmentLength)
		tcp[13] = 0x14
	}
	normalizeProbeVirtualRouterLocalTUNPacketChecksums(reset)
	return reset, true
}

func probeLinuxRouterSNIFlowSource(key probeLinuxRouterSNIFlowKey) string {
	return net.IPv4(byte(key.sourceIP>>24), byte(key.sourceIP>>16), byte(key.sourceIP>>8), byte(key.sourceIP)).String()
}

func probeLinuxRouterDomainRouteAction(domain string, destinationIP uint32) string {
	rule, ok := currentProbeVirtualRouterRouteRuleForDomain(domain)
	if ok {
		return sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID)
	}
	ipText := net.IPv4(byte(destinationIP>>24), byte(destinationIP>>16), byte(destinationIP>>8), byte(destinationIP)).String()
	if rule, ok = currentProbeVirtualRouterRouteRuleForIP(ipText); ok {
		return sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID)
	}
	return "direct"
}

func parseProbeLinuxRouterSNITCPPacket(packet []byte) (probeLinuxRouterSNITCPPacket, bool) {
	if len(packet) < 40 || packet[0]>>4 != 4 || packet[9] != 6 {
		return probeLinuxRouterSNITCPPacket{}, false
	}
	ihl := int(packet[0]&0x0f) * 4
	if ihl < 20 || len(packet) < ihl+20 {
		return probeLinuxRouterSNITCPPacket{}, false
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen < ihl+20 || totalLen > len(packet) {
		return probeLinuxRouterSNITCPPacket{}, false
	}
	if binary.BigEndian.Uint16(packet[6:8])&0x3fff != 0 {
		return probeLinuxRouterSNITCPPacket{}, false
	}
	tcpPacket := packet[ihl:totalLen]
	tcpHeaderLen := int(tcpPacket[12]>>4) * 4
	if tcpHeaderLen < 20 || tcpHeaderLen > len(tcpPacket) {
		return probeLinuxRouterSNITCPPacket{}, false
	}
	return probeLinuxRouterSNITCPPacket{
		key: probeLinuxRouterSNIFlowKey{
			sourceIP:        binary.BigEndian.Uint32(packet[12:16]),
			destinationIP:   binary.BigEndian.Uint32(packet[16:20]),
			sourcePort:      binary.BigEndian.Uint16(tcpPacket[0:2]),
			destinationPort: binary.BigEndian.Uint16(tcpPacket[2:4]),
		},
		sequence:        binary.BigEndian.Uint32(tcpPacket[4:8]),
		acknowledgement: binary.BigEndian.Uint32(tcpPacket[8:12]),
		flags:           tcpPacket[13],
		payload:         tcpPacket[tcpHeaderLen:],
	}, true
}

func appendProbeLinuxRouterSNIPayloadLocked(flow *probeLinuxRouterSNIFlow, sequence uint32, payload []byte) {
	if flow == nil || len(payload) == 0 || len(flow.preface) >= probeLinuxRouterSNIMaxClientHello {
		return
	}
	if sequence != flow.nextSequence {
		if int32(sequence-flow.nextSequence) < 0 {
			overlap := int(flow.nextSequence - sequence)
			if overlap >= len(payload) {
				return
			}
			payload = payload[overlap:]
			sequence = flow.nextSequence
		} else {
			remaining := probeLinuxRouterSNIMaxClientHello - len(flow.preface) - flow.pendingBytes
			if len(flow.pending) < 16 && remaining > 0 {
				if len(payload) > remaining {
					payload = payload[:remaining]
				}
				if previous, exists := flow.pending[sequence]; exists {
					flow.pendingBytes -= len(previous)
				}
				flow.pending[sequence] = append([]byte(nil), payload...)
				flow.pendingBytes += len(payload)
			}
			return
		}
	}
	appendProbeLinuxRouterSNIContiguousPayloadLocked(flow, payload)
	for {
		var nextSequence uint32
		var nextPayload []byte
		for pendingSequence, pendingPayload := range flow.pending {
			if pendingSequence == flow.nextSequence || int32(pendingSequence-flow.nextSequence) < 0 {
				nextSequence = pendingSequence
				nextPayload = pendingPayload
				break
			}
		}
		if nextPayload == nil {
			return
		}
		delete(flow.pending, nextSequence)
		flow.pendingBytes -= len(nextPayload)
		overlap := 0
		if int32(nextSequence-flow.nextSequence) < 0 {
			overlap = int(flow.nextSequence - nextSequence)
			if overlap >= len(nextPayload) {
				continue
			}
		}
		appendProbeLinuxRouterSNIContiguousPayloadLocked(flow, nextPayload[overlap:])
	}
}

func appendProbeLinuxRouterSNIContiguousPayloadLocked(flow *probeLinuxRouterSNIFlow, payload []byte) {
	remaining := probeLinuxRouterSNIMaxClientHello - len(flow.preface)
	if remaining <= 0 || len(payload) == 0 {
		return
	}
	if len(payload) > remaining {
		payload = payload[:remaining]
	}
	flow.preface = append(flow.preface, payload...)
	flow.nextSequence += uint32(len(payload))
}

func cleanupProbeLinuxRouterSNIFlowsLocked(now time.Time) {
	if !probeLinuxRouterSNIState.lastCleanup.IsZero() && now.Sub(probeLinuxRouterSNIState.lastCleanup) < time.Second && len(probeLinuxRouterSNIState.flows) < probeLinuxRouterSNIMaxFlows {
		return
	}
	for key, flow := range probeLinuxRouterSNIState.flows {
		ttl := probeLinuxRouterSNIFlowTTL
		if flow.rejectedDomain != "" {
			ttl = probeLinuxRouterSNIRejectTTL
		}
		if now.Sub(flow.updatedAt) >= ttl {
			delete(probeLinuxRouterSNIState.flows, key)
		}
	}
	probeLinuxRouterSNIState.lastCleanup = now
}

func makeProbeLinuxRouterSNIFlowRoomLocked(now time.Time) {
	cleanupProbeLinuxRouterSNIFlowsLocked(now)
	if len(probeLinuxRouterSNIState.flows) < probeLinuxRouterSNIMaxFlows {
		return
	}
	var oldestKey probeLinuxRouterSNIFlowKey
	var oldestAt time.Time
	for key, flow := range probeLinuxRouterSNIState.flows {
		if oldestAt.IsZero() || flow.updatedAt.Before(oldestAt) {
			oldestKey = key
			oldestAt = flow.updatedAt
		}
	}
	delete(probeLinuxRouterSNIState.flows, oldestKey)
}
