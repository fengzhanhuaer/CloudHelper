package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type udpAssociationDebugItem struct {
	Key              string `json:"key"`
	AssocKeyV2       string `json:"assoc_key_v2,omitempty"`
	FlowID           string `json:"flow_id,omitempty"`
	Target           string `json:"target,omitempty"`
	RouteTarget      string `json:"route_target,omitempty"`
	RouteFingerprint string `json:"route_fingerprint,omitempty"`
	NodeID           string `json:"node_id,omitempty"`
	Group            string `json:"group,omitempty"`
	Direct           bool   `json:"direct"`
	Transport        string `json:"transport,omitempty"`
	Refs             int32  `json:"refs,omitempty"`
	Active           bool   `json:"active"`
	LastActive       string `json:"last_active,omitempty"`
	IdleMS           int64  `json:"idle_ms"`
}

type udpAssociationsDebugPayload struct {
	Kind      string                    `json:"kind"`
	Scope     string                    `json:"scope"`
	NodeID    string                    `json:"node_id,omitempty"`
	Count     int                       `json:"count"`
	Items     []udpAssociationDebugItem `json:"items"`
	FetchedAt string                    `json:"fetched_at"`
}

type probeUDPAssociationsCommand struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Timestamp string `json:"timestamp"`
}

type probeUDPAssociationsResultMessage struct {
	Type      string                    `json:"type"`
	RequestID string                    `json:"request_id"`
	NodeID    string                    `json:"node_id"`
	OK        bool                      `json:"ok"`
	Scope     string                    `json:"scope,omitempty"`
	Count     int                       `json:"count"`
	Items     []udpAssociationDebugItem `json:"items"`
	FetchedAt string                    `json:"fetched_at,omitempty"`
	Error     string                    `json:"error,omitempty"`
	Timestamp string                    `json:"timestamp,omitempty"`
}

var probeUDPAssociationsRequestSeq atomic.Uint64

var probeUDPAssociationsWaiters = struct {
	mu   sync.Mutex
	data map[string]chan probeUDPAssociationsResultMessage
}{data: make(map[string]chan probeUDPAssociationsResultMessage)}

func buildControllerUDPAssociationsPayload() udpAssociationsDebugPayload {
	items := snapshotTunnelUDPAssociations()
	return udpAssociationsDebugPayload{
		Kind:      "network_udp_associations",
		Scope:     "controller",
		Count:     len(items),
		Items:     items,
		FetchedAt: time.Now().Format(time.RFC3339),
	}
}

func buildProbeUDPAssociationsPayloadFromResult(result probeUDPAssociationsResultMessage) udpAssociationsDebugPayload {
	items := append([]udpAssociationDebugItem(nil), result.Items...)
	return udpAssociationsDebugPayload{
		Kind:      "network_udp_associations",
		Scope:     firstNonEmptyControllerString(strings.TrimSpace(result.Scope), "probe"),
		NodeID:    strings.TrimSpace(result.NodeID),
		Count:     len(items),
		Items:     items,
		FetchedAt: firstNonEmptyControllerString(strings.TrimSpace(result.FetchedAt), strings.TrimSpace(result.Timestamp), time.Now().Format(time.RFC3339)),
	}
}

func snapshotTunnelUDPAssociations() []udpAssociationDebugItem {
	pool := globalTunnelUDPAssociationPool
	if pool == nil {
		return []udpAssociationDebugItem{}
	}
	now := time.Now()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	items := make([]udpAssociationDebugItem, 0, len(pool.items))
	for key, assoc := range pool.items {
		if assoc == nil {
			continue
		}
		item := udpAssociationDebugItem{
			Key:              strings.TrimSpace(key),
			AssocKeyV2:       strings.TrimSpace(assoc.assocKeyV2),
			FlowID:           strings.TrimSpace(assoc.flowID),
			Target:           strings.TrimSpace(assoc.target),
			RouteTarget:      strings.TrimSpace(assoc.routeTarget),
			RouteFingerprint: strings.TrimSpace(assoc.routeFingerprint),
			NodeID:           strings.TrimSpace(assoc.routeNodeID),
			Group:            strings.TrimSpace(assoc.routeGroup),
			Transport:        "udp",
			Refs:             assoc.refs.Load(),
			Active:           assoc.conn != nil,
		}
		if lastActive := assoc.lastActiveUnix.Load(); lastActive > 0 {
			lastActiveAt := time.Unix(lastActive, 0).UTC()
			item.LastActive = lastActiveAt.Format(time.RFC3339)
			item.IdleMS = now.Sub(lastActiveAt).Milliseconds()
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Target == items[j].Target {
			return items[i].Key < items[j].Key
		}
		return items[i].Target < items[j].Target
	})
	return items
}

func fetchProbeUDPAssociationsFromNode(nodeID string) (probeUDPAssociationsResultMessage, error) {
	normalizedID := normalizeProbeNodeID(nodeID)
	if normalizedID == "" {
		return probeUDPAssociationsResultMessage{}, fmt.Errorf("node_id is required")
	}
	session, ok := getProbeSession(normalizedID)
	if !ok {
		return probeUDPAssociationsResultMessage{}, fmt.Errorf("probe is offline")
	}
	requestID := newProbeUDPAssociationsRequestID(normalizedID)
	waiter := make(chan probeUDPAssociationsResultMessage, 1)

	probeUDPAssociationsWaiters.mu.Lock()
	probeUDPAssociationsWaiters.data[requestID] = waiter
	probeUDPAssociationsWaiters.mu.Unlock()
	defer func() {
		probeUDPAssociationsWaiters.mu.Lock()
		delete(probeUDPAssociationsWaiters.data, requestID)
		probeUDPAssociationsWaiters.mu.Unlock()
	}()

	cmd := probeUDPAssociationsCommand{
		Type:      "udp_associations_get",
		RequestID: requestID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if err := session.writeJSON(cmd); err != nil {
		unregisterProbeSession(normalizedID, session)
		return probeUDPAssociationsResultMessage{}, err
	}

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

	select {
	case result := <-waiter:
		if strings.TrimSpace(result.NodeID) == "" {
			result.NodeID = normalizedID
		}
		result.Scope = firstNonEmptyControllerString(strings.TrimSpace(result.Scope), "probe")
		result.Count = len(result.Items)
		if !result.OK {
			errMsg := strings.TrimSpace(result.Error)
			if errMsg == "" {
				errMsg = "probe udp associations fetch failed"
			}
			return result, errors.New(errMsg)
		}
		return result, nil
	case <-timer.C:
		return probeUDPAssociationsResultMessage{}, fmt.Errorf("probe udp associations fetch timeout")
	}
}

func consumeProbeUDPAssociationsResult(result probeUDPAssociationsResultMessage) {
	requestID := strings.TrimSpace(result.RequestID)
	if requestID == "" {
		return
	}
	probeUDPAssociationsWaiters.mu.Lock()
	waiter, ok := probeUDPAssociationsWaiters.data[requestID]
	if ok {
		delete(probeUDPAssociationsWaiters.data, requestID)
	}
	probeUDPAssociationsWaiters.mu.Unlock()
	if !ok {
		return
	}
	select {
	case waiter <- result:
	default:
	}
}

func newProbeUDPAssociationsRequestID(nodeID string) string {
	seq := probeUDPAssociationsRequestSeq.Add(1)
	return fmt.Sprintf("probe-udp-assoc-%s-%d-%d", normalizeProbeNodeID(nodeID), time.Now().UnixNano(), seq)
}

func firstNonEmptyControllerString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
