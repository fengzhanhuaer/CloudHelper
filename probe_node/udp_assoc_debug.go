package main

import (
	"encoding/json"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

type probeUDPAssociationDebugItemPayload struct {
	Key              string `json:"key"`
	AssocKeyV2       string `json:"assoc_key_v2,omitempty"`
	FlowID           string `json:"flow_id,omitempty"`
	Target           string `json:"target,omitempty"`
	RouteTarget      string `json:"route_target,omitempty"`
	RouteFingerprint string `json:"route_fingerprint,omitempty"`
	NodeID           string `json:"node_id,omitempty"`
	Group            string `json:"group,omitempty"`
	NATMode          string `json:"nat_mode,omitempty"`
	TTLProfile       string `json:"ttl_profile,omitempty"`
	IdleTimeoutMS    int64  `json:"idle_timeout_ms,omitempty"`
	GCIntervalMS     int64  `json:"gc_interval_ms,omitempty"`
	CreatedAtUnixMS  int64  `json:"created_at_unix_ms,omitempty"`
	Direct           bool   `json:"direct"`
	Transport        string `json:"transport,omitempty"`
	Refs             int32  `json:"refs,omitempty"`
	Active           bool   `json:"active"`
	LastActive       string `json:"last_active,omitempty"`
	IdleMS           int64  `json:"idle_ms"`
}

type probeUDPAssociationsResultPayload struct {
	Type      string                                `json:"type"`
	RequestID string                                `json:"request_id"`
	NodeID    string                                `json:"node_id"`
	OK        bool                                  `json:"ok"`
	Scope     string                                `json:"scope,omitempty"`
	Count     int                                   `json:"count"`
	Items     []probeUDPAssociationDebugItemPayload `json:"items"`
	FetchedAt string                                `json:"fetched_at,omitempty"`
	Error     string                                `json:"error,omitempty"`
	Timestamp string                                `json:"timestamp,omitempty"`
}

func runProbeUDPAssociationsFetch(cmd probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	requestID := strings.TrimSpace(cmd.RequestID)
	if requestID == "" {
		return
	}
	payload := probeUDPAssociationsResultPayload{
		Type:      "udp_associations_result",
		RequestID: requestID,
		NodeID:    strings.TrimSpace(identity.NodeID),
		OK:        true,
		Scope:     "probe",
		Items:     snapshotProbeUDPAssociations(),
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	payload.Count = len(payload.Items)
	if writeErr := writeProbeStreamJSON(stream, encoder, writeMu, payload); writeErr != nil {
		log.Printf("probe udp associations response send failed: request_id=%s err=%v", requestID, writeErr)
	}
}

func snapshotProbeUDPAssociations() []probeUDPAssociationDebugItemPayload {
	pool := globalProbeChainUDPAssociationPool
	if pool == nil {
		return []probeUDPAssociationDebugItemPayload{}
	}
	now := time.Now()
	items := make([]probeUDPAssociationDebugItemPayload, 0)
	pool.mu.Lock()
	for key, assoc := range pool.items {
		if assoc == nil {
			continue
		}
		item := probeUDPAssociationDebugItemPayload{
			Key:              strings.TrimSpace(key),
			AssocKeyV2:       strings.TrimSpace(assoc.assocKeyV2),
			FlowID:           strings.TrimSpace(assoc.flowID),
			Target:           strings.TrimSpace(assoc.target),
			RouteTarget:      strings.TrimSpace(assoc.routeTarget),
			RouteFingerprint: strings.TrimSpace(assoc.routeFingerprint),
			NodeID:           strings.TrimSpace(assoc.routeNodeID),
			Group:            strings.TrimSpace(assoc.routeGroup),
			NATMode:          strings.TrimSpace(assoc.natMode),
			TTLProfile:       strings.TrimSpace(assoc.ttlProfile),
			IdleTimeoutMS:    assoc.idleTimeout.Milliseconds(),
			GCIntervalMS:     assoc.gcInterval.Milliseconds(),
			CreatedAtUnixMS:  assoc.createdAtUnixMS,
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
	pool.mu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].Target == items[j].Target {
			return items[i].Key < items[j].Key
		}
		return items[i].Target < items[j].Target
	})
	return items
}
