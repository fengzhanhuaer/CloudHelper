package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
)

const (
	probeVirtualRouterFakeIPRecoveryMaxTargets        = 16
	probeVirtualRouterFakeIPRecoveryMaxFlowsPerTarget = 8
)

type probeVirtualRouterFakeIPRecoveryPacket struct {
	runtime *probeVirtualRouterRuntime
	packet  []byte
	path    []string
}

type probeVirtualRouterFakeIPSourceRecoveryPacket struct {
	packet []byte
}

var probeVirtualRouterFakeIPRecoveryState = struct {
	mu      sync.Mutex
	pending map[string]map[string]probeVirtualRouterFakeIPRecoveryPacket
}{
	pending: make(map[string]map[string]probeVirtualRouterFakeIPRecoveryPacket),
}

var probeVirtualRouterFakeIPSourceRecoveryState = struct {
	mu      sync.Mutex
	pending map[string]map[string]probeVirtualRouterFakeIPSourceRecoveryPacket
}{
	pending: make(map[string]map[string]probeVirtualRouterFakeIPSourceRecoveryPacket),
}

var probeVirtualRouterRecoverFakeIPExitPacketHook func(*probeVirtualRouterRuntime, *probeVirtualRouterFrameLink, []byte, []string) bool
var probeVirtualRouterRecoverFakeIPSourcePacketHook func([]byte) bool

func recoverProbeVirtualRouterFakeIPExitPacket(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, packet []byte, path []string) bool {
	if probeVirtualRouterRecoverFakeIPExitPacketHook != nil {
		return probeVirtualRouterRecoverFakeIPExitPacketHook(runtime, link, packet, path)
	}
	return handleProbeVirtualRouterFakeIPExitPacket(runtime, link, packet, path)
}

func recoverProbeVirtualRouterFakeIPSourcePacket(packet []byte) bool {
	if probeVirtualRouterRecoverFakeIPSourcePacketHook != nil {
		return probeVirtualRouterRecoverFakeIPSourcePacketHook(packet)
	}
	return handleProbeVirtualRouterTUNPacket(packet)
}

func probeVirtualRouterFakeIPRecoveryFlow(packet []byte) (probeVirtualRouterTransportLogInfo, string, bool) {
	info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet)
	if !ok || !strings.EqualFold(info.Protocol, "tcp") || info.DestinationPort == 0 {
		return probeVirtualRouterTransportLogInfo{}, "", false
	}
	flags := strings.ToUpper(strings.TrimSpace(info.TCPFlags))
	if !strings.Contains(flags, "SYN") || strings.Contains(flags, "ACK") {
		return probeVirtualRouterTransportLogInfo{}, "", false
	}
	fakeIP := strings.TrimSpace(info.DestinationIP)
	if !probeVirtualRouterIPCanBeFakeIP(fakeIP) {
		return probeVirtualRouterTransportLogInfo{}, "", false
	}
	flowKey := strings.Join([]string{
		strings.TrimSpace(info.SourceIP),
		fmt.Sprintf("%d", info.SourcePort),
		fakeIP,
		fmt.Sprintf("%d", info.DestinationPort),
	}, "|")
	return info, flowKey, true
}

func scheduleProbeVirtualRouterFakeIPFirstPacketRecovery(runtime *probeVirtualRouterRuntime, packet []byte, path []string) bool {
	info, flowKey, ok := probeVirtualRouterFakeIPRecoveryFlow(packet)
	if !ok {
		return false
	}
	fakeIP := strings.TrimSpace(info.DestinationIP)
	cleanPath := cleanProbeVirtualRouterPath(path)
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if !probeVirtualRouterFrameTargetsLocalPathEnd(cleanPath, localNodeID) {
		return false
	}

	probeVirtualRouterFakeIPRecoveryState.mu.Lock()
	flows, running := probeVirtualRouterFakeIPRecoveryState.pending[fakeIP]
	if !running {
		if len(probeVirtualRouterFakeIPRecoveryState.pending) >= probeVirtualRouterFakeIPRecoveryMaxTargets {
			probeVirtualRouterFakeIPRecoveryState.mu.Unlock()
			return false
		}
		flows = make(map[string]probeVirtualRouterFakeIPRecoveryPacket)
		probeVirtualRouterFakeIPRecoveryState.pending[fakeIP] = flows
	}
	if _, exists := flows[flowKey]; !exists {
		if len(flows) >= probeVirtualRouterFakeIPRecoveryMaxFlowsPerTarget {
			probeVirtualRouterFakeIPRecoveryState.mu.Unlock()
			return false
		}
		flows[flowKey] = probeVirtualRouterFakeIPRecoveryPacket{
			runtime: runtime,
			packet:  append([]byte(nil), packet...),
			path:    append([]string(nil), cleanPath...),
		}
	}
	probeVirtualRouterFakeIPRecoveryState.mu.Unlock()

	if running {
		return true
	}
	go recoverProbeVirtualRouterFakeIPFirstPackets(fakeIP)
	return true
}

func scheduleProbeVirtualRouterFakeIPSourceFirstPacketRecovery(packet []byte) bool {
	info, flowKey, ok := probeVirtualRouterFakeIPRecoveryFlow(packet)
	if !ok {
		return false
	}
	fakeIP := strings.TrimSpace(info.DestinationIP)

	probeVirtualRouterFakeIPSourceRecoveryState.mu.Lock()
	flows, running := probeVirtualRouterFakeIPSourceRecoveryState.pending[fakeIP]
	if !running {
		if len(probeVirtualRouterFakeIPSourceRecoveryState.pending) >= probeVirtualRouterFakeIPRecoveryMaxTargets {
			probeVirtualRouterFakeIPSourceRecoveryState.mu.Unlock()
			return false
		}
		flows = make(map[string]probeVirtualRouterFakeIPSourceRecoveryPacket)
		probeVirtualRouterFakeIPSourceRecoveryState.pending[fakeIP] = flows
	}
	if _, exists := flows[flowKey]; !exists {
		if len(flows) >= probeVirtualRouterFakeIPRecoveryMaxFlowsPerTarget {
			probeVirtualRouterFakeIPSourceRecoveryState.mu.Unlock()
			return false
		}
		flows[flowKey] = probeVirtualRouterFakeIPSourceRecoveryPacket{packet: append([]byte(nil), packet...)}
	}
	probeVirtualRouterFakeIPSourceRecoveryState.mu.Unlock()

	if running {
		return true
	}
	go recoverProbeVirtualRouterFakeIPSourceFirstPackets(fakeIP)
	return true
}

func recoverProbeVirtualRouterFakeIPFirstPackets(fakeIP string) {
	entry, ok := currentProbeVirtualRouterFakeIPEntryByIPWithControllerRefresh(fakeIP)

	probeVirtualRouterFakeIPRecoveryState.mu.Lock()
	flows := probeVirtualRouterFakeIPRecoveryState.pending[fakeIP]
	delete(probeVirtualRouterFakeIPRecoveryState.pending, fakeIP)
	probeVirtualRouterFakeIPRecoveryState.mu.Unlock()

	if !ok {
		err := fmt.Errorf("fake ip mapping refresh failed: fake_ip=%s", fakeIP)
		for _, item := range flows {
			recordProbeVirtualRouterRuntimeDeliveryError(item.runtime, err)
			recordProbeVirtualRouterRecentPacket("fake_mapping", "recovery_error", item.runtime, item.packet, item.path, false, err)
		}
		return
	}
	for _, item := range flows {
		localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(item.runtime)
		if normalizeProbeRouteNodeID(entry.ExitNodeID) != normalizeProbeRouteNodeID(localNodeID) {
			err := fmt.Errorf("refreshed fake ip mapping targets another exit: fake_ip=%s exit=%s local=%s", fakeIP, entry.ExitNodeID, localNodeID)
			recordProbeVirtualRouterRuntimeDeliveryError(item.runtime, err)
			recordProbeVirtualRouterRecentPacket("fake_mapping", "recovery_error", item.runtime, item.packet, item.path, false, err)
			continue
		}
		if !recoverProbeVirtualRouterFakeIPExitPacket(item.runtime, nil, item.packet, item.path) {
			err := errors.New("recovered fake ip packet was not accepted by exit")
			recordProbeVirtualRouterRuntimeDeliveryError(item.runtime, err)
			recordProbeVirtualRouterRecentPacket("fake_mapping", "recovery_error", item.runtime, item.packet, item.path, false, err)
			continue
		}
		recordProbeVirtualRouterRuntimePacketDelivered(item.runtime, len(item.packet))
		recordProbeVirtualRouterRecentPacket("fake_mapping", "recovered", item.runtime, item.packet, item.path, true, nil)
	}
	if len(flows) > 0 {
		log.Printf("probe virtual router fake ip first packet recovered: fake_ip=%s domain=%s flows=%d", fakeIP, strings.TrimSpace(entry.Domain), len(flows))
	}
}

func recoverProbeVirtualRouterFakeIPSourceFirstPackets(fakeIP string) {
	entry, ok := currentProbeVirtualRouterFakeIPEntryByIPWithControllerRefresh(fakeIP)

	probeVirtualRouterFakeIPSourceRecoveryState.mu.Lock()
	flows := probeVirtualRouterFakeIPSourceRecoveryState.pending[fakeIP]
	delete(probeVirtualRouterFakeIPSourceRecoveryState.pending, fakeIP)
	probeVirtualRouterFakeIPSourceRecoveryState.mu.Unlock()

	if !ok {
		err := fmt.Errorf("fake ip mapping refresh failed: fake_ip=%s", fakeIP)
		for _, item := range flows {
			recordProbeVirtualRouterRecentPacket("fake_mapping", "source_recovery_error", nil, item.packet, nil, false, err)
		}
		return
	}
	for _, item := range flows {
		path := currentProbeVirtualRouterPathForPacket(item.packet, fakeIP)
		if len(path) < 2 {
			err := fmt.Errorf("refreshed fake ip path unavailable: fake_ip=%s exit=%s", fakeIP, entry.ExitNodeID)
			recordProbeVirtualRouterRecentPacket("fake_mapping", "source_recovery_error", nil, item.packet, path, false, err)
			continue
		}
		if !recoverProbeVirtualRouterFakeIPSourcePacket(item.packet) {
			err := errors.New("recovered fake ip packet was not accepted by source")
			recordProbeVirtualRouterRecentPacket("fake_mapping", "source_recovery_error", nil, item.packet, path, false, err)
			continue
		}
		recordProbeVirtualRouterRecentPacket("fake_mapping", "source_recovered", nil, item.packet, path, false, nil)
	}
	if len(flows) > 0 {
		log.Printf("probe virtual router fake ip source first packet recovered: fake_ip=%s domain=%s flows=%d", fakeIP, strings.TrimSpace(entry.Domain), len(flows))
	}
}

func resetProbeVirtualRouterFakeIPRecoveryForTest() {
	probeVirtualRouterFakeIPRecoveryState.mu.Lock()
	probeVirtualRouterFakeIPRecoveryState.pending = make(map[string]map[string]probeVirtualRouterFakeIPRecoveryPacket)
	probeVirtualRouterFakeIPRecoveryState.mu.Unlock()
	probeVirtualRouterFakeIPSourceRecoveryState.mu.Lock()
	probeVirtualRouterFakeIPSourceRecoveryState.pending = make(map[string]map[string]probeVirtualRouterFakeIPSourceRecoveryPacket)
	probeVirtualRouterFakeIPSourceRecoveryState.mu.Unlock()
}
