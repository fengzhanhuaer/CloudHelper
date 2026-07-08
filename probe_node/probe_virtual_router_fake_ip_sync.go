package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	probeVirtualRouterFakeIPSyncTimeout  = 1500 * time.Millisecond
	probeVirtualRouterFakeIPSyncCooldown = time.Second
)

var probeVirtualRouterFakeIPSyncState = struct {
	mu      sync.Mutex
	pending map[string]chan probeVirtualRouterFakeIPSyncPayload
	running map[string]bool
	lastAt  map[string]time.Time
}{
	pending: make(map[string]chan probeVirtualRouterFakeIPSyncPayload),
	running: make(map[string]bool),
	lastAt:  make(map[string]time.Time),
}

type probeVirtualRouterFakeIPSyncPayload struct {
	RequestID         string                        `json:"request_id"`
	SourceNodeID      string                        `json:"source_node_id,omitempty"`
	TargetNodeID      string                        `json:"target_node_id,omitempty"`
	Path              []string                      `json:"path,omitempty"`
	FakeIP            string                        `json:"fake_ip,omitempty"`
	Domain            string                        `json:"domain,omitempty"`
	RuleID            string                        `json:"rule_id,omitempty"`
	Action            string                        `json:"action,omitempty"`
	ExitNodeID        string                        `json:"exit_node_id,omitempty"`
	Entry             probeVirtualRouterFakeIPEntry `json:"entry,omitempty"`
	Reason            string                        `json:"reason,omitempty"`
	CreatedAtUnixNano int64                         `json:"created_at_unix_nano,omitempty"`
	OK                bool                          `json:"ok,omitempty"`
	Error             string                        `json:"error,omitempty"`
	Responder         string                        `json:"responder,omitempty"`
}

func scheduleProbeVirtualRouterFakeIPNegotiationForPacket(runtime *probeVirtualRouterRuntime, fakeIP string, path []string, reason string) bool {
	cleanIP := ""
	if ip := net.ParseIP(strings.TrimSpace(fakeIP)).To4(); ip != nil {
		cleanIP = ip.String()
	}
	cleanPath := cleanProbeVirtualRouterPath(path)
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if localNodeID == "" {
		localNodeID = currentProbeVirtualRouterLocalNodeID()
	}
	localNodeID = normalizeProbeRouteNodeID(localNodeID)
	if cleanIP == "" || len(cleanPath) < 2 || localNodeID == "" {
		return false
	}
	sourceNodeID := normalizeProbeRouteNodeID(cleanPath[0])
	if sourceNodeID == "" || sourceNodeID == localNodeID {
		return false
	}
	localIndex := probeVirtualRouterPathIndex(cleanPath, localNodeID)
	if localIndex <= 0 {
		return false
	}
	requestPath := probeVirtualRouterReversePath(cleanPath[:localIndex+1])
	if len(requestPath) < 2 {
		return false
	}

	key := strings.Join([]string{cleanIP, sourceNodeID, localNodeID}, "|")
	now := time.Now()
	probeVirtualRouterFakeIPSyncState.mu.Lock()
	if probeVirtualRouterFakeIPSyncState.running[key] {
		probeVirtualRouterFakeIPSyncState.mu.Unlock()
		return false
	}
	if lastAt := probeVirtualRouterFakeIPSyncState.lastAt[key]; !lastAt.IsZero() && now.Sub(lastAt) < probeVirtualRouterFakeIPSyncCooldown {
		probeVirtualRouterFakeIPSyncState.mu.Unlock()
		return false
	}
	probeVirtualRouterFakeIPSyncState.running[key] = true
	probeVirtualRouterFakeIPSyncState.lastAt[key] = now
	probeVirtualRouterFakeIPSyncState.mu.Unlock()

	go func() {
		defer func() {
			probeVirtualRouterFakeIPSyncState.mu.Lock()
			delete(probeVirtualRouterFakeIPSyncState.running, key)
			probeVirtualRouterFakeIPSyncState.mu.Unlock()
		}()
		entry, err := negotiateProbeVirtualRouterFakeIPMapping(cleanIP, requestPath, strings.TrimSpace(reason), probeVirtualRouterFakeIPSyncTimeout)
		if err != nil {
			log.Printf("probe virtual router fake ip sync failed: fake_ip=%s path=%s reason=%s err=%v", cleanIP, strings.Join(requestPath, ">"), strings.TrimSpace(reason), err)
			return
		}
		log.Printf("probe virtual router fake ip sync applied: fake_ip=%s domain=%s exit=%s path=%s reason=%s", strings.TrimSpace(entry.FakeIP), strings.TrimSpace(entry.Domain), normalizeProbeRouteNodeID(entry.ExitNodeID), strings.Join(requestPath, ">"), strings.TrimSpace(reason))
	}()
	return true
}

func negotiateProbeVirtualRouterFakeIPMapping(fakeIP string, requestPath []string, reason string, timeout time.Duration) (probeVirtualRouterFakeIPEntry, error) {
	cleanIP := ""
	if ip := net.ParseIP(strings.TrimSpace(fakeIP)).To4(); ip != nil {
		cleanIP = ip.String()
	}
	cleanPath := cleanProbeVirtualRouterPath(requestPath)
	if cleanIP == "" {
		return probeVirtualRouterFakeIPEntry{}, errors.New("fake ip is invalid")
	}
	if len(cleanPath) < 2 {
		return probeVirtualRouterFakeIPEntry{}, errors.New("fake ip sync path is incomplete")
	}
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" {
		return probeVirtualRouterFakeIPEntry{}, errors.New("local virtual router node id is empty")
	}
	localNodeID = normalizeProbeRouteNodeID(localNodeID)
	if cleanPath[0] != localNodeID {
		return probeVirtualRouterFakeIPEntry{}, fmt.Errorf("fake ip sync path must start from local node: local=%s path=%s", localNodeID, strings.Join(cleanPath, ">"))
	}
	targetNodeID := cleanPath[len(cleanPath)-1]
	requestID := newProbeTCPDebugFlowID("vrouter_fake_ip_sync", cleanIP)
	waiter := registerProbeVirtualRouterFakeIPSyncResponse(requestID)
	defer unregisterProbeVirtualRouterFakeIPSyncResponse(requestID)

	msg := probeVirtualRouterFakeIPSyncPayload{
		RequestID:         requestID,
		SourceNodeID:      localNodeID,
		TargetNodeID:      targetNodeID,
		Path:              cleanPath,
		FakeIP:            cleanIP,
		ExitNodeID:        localNodeID,
		Reason:            strings.TrimSpace(reason),
		CreatedAtUnixNano: time.Now().UnixNano(),
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, err
	}
	if err := forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeFakeIPSync, probeVirtualRouterFakeIPSyncSubTypeQuery, payload, cleanPath); err != nil {
		return probeVirtualRouterFakeIPEntry{}, err
	}
	if timeout <= 0 {
		timeout = probeVirtualRouterFakeIPSyncTimeout
	}
	response, err := waitProbeVirtualRouterFakeIPSyncResponse(waiter, timeout)
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, fmt.Errorf("%w: request_id=%s target=%s path=%s", err, requestID, targetNodeID, strings.Join(cleanPath, ">"))
	}
	return applyProbeVirtualRouterFakeIPSyncResponse(response, cleanIP, localNodeID)
}

func handleProbeVirtualRouterFakeIPSyncFrame(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, subType uint16, msg probeVirtualRouterFakeIPSyncPayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if localNodeID == "" {
		localNodeID = currentProbeVirtualRouterLocalNodeID()
	}
	localNodeID = normalizeProbeRouteNodeID(localNodeID)
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	if localNodeID == "" || msg.RequestID == "" || len(msg.Path) < 2 {
		return errors.New("virtual router fake ip sync frame is incomplete")
	}
	switch subType {
	case probeVirtualRouterFakeIPSyncSubTypeQuery:
		targetNodeID := normalizeProbeRouteNodeID(msg.TargetNodeID)
		if targetNodeID == "" {
			targetNodeID = msg.Path[len(msg.Path)-1]
		}
		if targetNodeID == localNodeID || probeVirtualRouterNextHopInPath(msg.Path, localNodeID) == "" {
			return sendProbeVirtualRouterFakeIPSyncResponse(link, msg, localNodeID)
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeFakeIPSync, probeVirtualRouterFakeIPSyncSubTypeQuery, payload, msg.Path)
	case probeVirtualRouterFakeIPSyncSubTypeResponse:
		if normalizeProbeRouteNodeID(msg.SourceNodeID) == localNodeID || msg.Path[len(msg.Path)-1] == localNodeID {
			completeProbeVirtualRouterFakeIPSyncResponse(msg)
			return nil
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeFakeIPSync, probeVirtualRouterFakeIPSyncSubTypeResponse, payload, msg.Path)
	default:
		return fmt.Errorf("unsupported virtual router fake ip sync subtype=%d", subType)
	}
}

func sendProbeVirtualRouterFakeIPSyncResponse(link *probeVirtualRouterFrameLink, request probeVirtualRouterFakeIPSyncPayload, responder string) error {
	response := request
	response.Path = probeVirtualRouterReversePath(request.Path)
	response.Responder = normalizeProbeRouteNodeID(responder)
	response.CreatedAtUnixNano = time.Now().UnixNano()
	entry, ok := lookupProbeVirtualRouterFakeIPSyncEntry(request)
	if !ok {
		response.OK = false
		response.Error = fmt.Sprintf("fake ip mapping is unavailable: fake_ip=%s domain=%s", strings.TrimSpace(request.FakeIP), strings.TrimSpace(request.Domain))
	} else {
		response.OK = true
		response.Error = ""
		response.Entry = entry
		response.FakeIP = strings.TrimSpace(entry.FakeIP)
		response.Domain = strings.TrimSpace(entry.Domain)
		response.RuleID = strings.TrimSpace(entry.RuleID)
		response.Action = strings.TrimSpace(entry.Action)
		response.ExitNodeID = normalizeProbeRouteNodeID(entry.ExitNodeID)
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if link != nil {
		if err := enqueueProbeVirtualRouterBusinessFrame(link, probeVirtualRouterFrameMainTypeFakeIPSync, probeVirtualRouterFakeIPSyncSubTypeResponse, payload, response.Path); err == nil {
			return nil
		} else {
			log.Printf("probe virtual router fake ip sync response enqueue on incoming link failed: request_id=%s path=%s err=%v", strings.TrimSpace(request.RequestID), strings.Join(response.Path, ">"), err)
		}
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeFakeIPSync, probeVirtualRouterFakeIPSyncSubTypeResponse, payload, response.Path)
}

func lookupProbeVirtualRouterFakeIPSyncEntry(request probeVirtualRouterFakeIPSyncPayload) (probeVirtualRouterFakeIPEntry, bool) {
	if entry, ok := currentProbeVirtualRouterFakeIPEntryByIP(request.FakeIP); ok {
		return entry, true
	}
	if entry, ok := currentProbeVirtualRouterFakeIPEntryByDomain(request.Domain); ok {
		if strings.TrimSpace(request.FakeIP) == "" || strings.TrimSpace(entry.FakeIP) == strings.TrimSpace(request.FakeIP) {
			return entry, true
		}
	}
	return probeVirtualRouterFakeIPEntry{}, false
}

func applyProbeVirtualRouterFakeIPSyncResponse(msg probeVirtualRouterFakeIPSyncPayload, expectedFakeIP string, expectedExitNodeID string) (probeVirtualRouterFakeIPEntry, error) {
	if !msg.OK {
		if strings.TrimSpace(msg.Error) != "" {
			return probeVirtualRouterFakeIPEntry{}, errors.New(strings.TrimSpace(msg.Error))
		}
		return probeVirtualRouterFakeIPEntry{}, errors.New("fake ip sync response is not ok")
	}
	entry := msg.Entry
	if strings.TrimSpace(entry.FakeIP) == "" {
		entry.FakeIP = strings.TrimSpace(msg.FakeIP)
	}
	if strings.TrimSpace(entry.Domain) == "" {
		entry.Domain = strings.TrimSpace(msg.Domain)
	}
	if strings.TrimSpace(entry.RuleID) == "" {
		entry.RuleID = strings.TrimSpace(msg.RuleID)
	}
	if strings.TrimSpace(entry.Action) == "" {
		entry.Action = strings.TrimSpace(msg.Action)
	}
	if strings.TrimSpace(entry.ExitNodeID) == "" {
		entry.ExitNodeID = normalizeProbeRouteNodeID(msg.ExitNodeID)
	}
	expectedIP := ""
	if ip := net.ParseIP(strings.TrimSpace(expectedFakeIP)).To4(); ip != nil {
		expectedIP = ip.String()
	}
	if expectedIP != "" && strings.TrimSpace(entry.FakeIP) != expectedIP {
		return probeVirtualRouterFakeIPEntry{}, fmt.Errorf("fake ip sync response mismatch: got=%s want=%s", strings.TrimSpace(entry.FakeIP), expectedIP)
	}
	expectedExit := normalizeProbeRouteNodeID(expectedExitNodeID)
	if expectedExit != "" {
		entryExit := normalizeProbeRouteNodeID(entry.ExitNodeID)
		if entryExit != "" && entryExit != expectedExit {
			return probeVirtualRouterFakeIPEntry{}, fmt.Errorf("fake ip sync exit mismatch: got=%s want=%s", entryExit, expectedExit)
		}
		if entryExit == "" {
			entry.ExitNodeID = expectedExit
		}
	}
	if !applyProbeVirtualRouterFakeIPEntry(entry) {
		return probeVirtualRouterFakeIPEntry{}, errors.New("fake ip sync response entry is invalid")
	}
	return sanitizeProbeVirtualRouterFakeIPEntry(entry), nil
}

func registerProbeVirtualRouterFakeIPSyncResponse(requestID string) chan probeVirtualRouterFakeIPSyncPayload {
	requestID = strings.TrimSpace(requestID)
	ch := make(chan probeVirtualRouterFakeIPSyncPayload, 1)
	if requestID == "" {
		return ch
	}
	probeVirtualRouterFakeIPSyncState.mu.Lock()
	if probeVirtualRouterFakeIPSyncState.pending == nil {
		probeVirtualRouterFakeIPSyncState.pending = make(map[string]chan probeVirtualRouterFakeIPSyncPayload)
	}
	probeVirtualRouterFakeIPSyncState.pending[requestID] = ch
	probeVirtualRouterFakeIPSyncState.mu.Unlock()
	return ch
}

func unregisterProbeVirtualRouterFakeIPSyncResponse(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterFakeIPSyncState.mu.Lock()
	delete(probeVirtualRouterFakeIPSyncState.pending, requestID)
	probeVirtualRouterFakeIPSyncState.mu.Unlock()
}

func waitProbeVirtualRouterFakeIPSyncResponse(ch chan probeVirtualRouterFakeIPSyncPayload, timeout time.Duration) (probeVirtualRouterFakeIPSyncPayload, error) {
	if ch == nil {
		return probeVirtualRouterFakeIPSyncPayload{}, errors.New("virtual router fake ip sync response waiter is nil")
	}
	if timeout <= 0 {
		timeout = probeVirtualRouterFakeIPSyncTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-ch:
		return response, nil
	case <-timer.C:
		return probeVirtualRouterFakeIPSyncPayload{}, errors.New("virtual router fake ip sync response timeout")
	}
}

func completeProbeVirtualRouterFakeIPSyncResponse(msg probeVirtualRouterFakeIPSyncPayload) {
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterFakeIPSyncState.mu.Lock()
	ch := probeVirtualRouterFakeIPSyncState.pending[requestID]
	probeVirtualRouterFakeIPSyncState.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func probeVirtualRouterPathIndex(path []string, nodeID string) int {
	target := normalizeProbeRouteNodeID(nodeID)
	if target == "" {
		return -1
	}
	for index, item := range path {
		if normalizeProbeRouteNodeID(item) == target {
			return index
		}
	}
	return -1
}
