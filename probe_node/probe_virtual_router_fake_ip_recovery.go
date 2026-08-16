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

var probeVirtualRouterFakeIPRecoveryState = struct {
	mu      sync.Mutex
	pending map[string]map[string]probeVirtualRouterFakeIPRecoveryPacket
}{
	pending: make(map[string]map[string]probeVirtualRouterFakeIPRecoveryPacket),
}

var probeVirtualRouterRecoverFakeIPExitPacketHook func(*probeVirtualRouterRuntime, *probeVirtualRouterFrameLink, []byte, []string) bool

func recoverProbeVirtualRouterFakeIPExitPacket(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, packet []byte, path []string) bool {
	if probeVirtualRouterRecoverFakeIPExitPacketHook != nil {
		return probeVirtualRouterRecoverFakeIPExitPacketHook(runtime, link, packet, path)
	}
	return handleProbeVirtualRouterFakeIPExitPacket(runtime, link, packet, path)
}

func scheduleProbeVirtualRouterFakeIPFirstPacketRecovery(runtime *probeVirtualRouterRuntime, packet []byte, path []string) bool {
	info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet)
	if !ok || !strings.EqualFold(info.Protocol, "tcp") || info.DestinationPort == 0 {
		return false
	}
	flags := strings.ToUpper(strings.TrimSpace(info.TCPFlags))
	if !strings.Contains(flags, "SYN") || strings.Contains(flags, "ACK") {
		return false
	}
	fakeIP := strings.TrimSpace(info.DestinationIP)
	cleanPath := cleanProbeVirtualRouterPath(path)
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if !probeVirtualRouterIPCanBeFakeIP(fakeIP) || !probeVirtualRouterFrameTargetsLocalPathEnd(cleanPath, localNodeID) {
		return false
	}
	flowKey := strings.Join([]string{
		strings.TrimSpace(info.SourceIP),
		fmt.Sprintf("%d", info.SourcePort),
		fakeIP,
		fmt.Sprintf("%d", info.DestinationPort),
	}, "|")

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

func resetProbeVirtualRouterFakeIPRecoveryForTest() {
	probeVirtualRouterFakeIPRecoveryState.mu.Lock()
	probeVirtualRouterFakeIPRecoveryState.pending = make(map[string]map[string]probeVirtualRouterFakeIPRecoveryPacket)
	probeVirtualRouterFakeIPRecoveryState.mu.Unlock()
}
