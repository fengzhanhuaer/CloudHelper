package main

import (
	"net"
	"strings"
	"time"
)

type probeSubstreamMonitorPayload struct {
	Type             string                            `json:"type"`
	RequestID        string                            `json:"request_id"`
	NodeID           string                            `json:"node_id,omitempty"`
	OK               bool                              `json:"ok"`
	Scope            string                            `json:"scope,omitempty"`
	ActiveCount      int                               `json:"active_count"`
	CompletedCount   int                               `json:"completed_count"`
	FailureCount     int                               `json:"failure_count"`
	Active           []probeSubstreamMonitorItem       `json:"active"`
	Completed        []probeSubstreamMonitorItem       `json:"completed"`
	Failures         []probeTCPDebugFailureItemPayload `json:"failures"`
	YamuxWindowBytes int                               `json:"yamux_window_bytes"`
	FetchedAt        string                            `json:"fetched_at,omitempty"`
	Error            string                            `json:"error,omitempty"`
	Timestamp        string                            `json:"timestamp,omitempty"`
}

type probeSubstreamMonitorItem struct {
	ID                   string                          `json:"id"`
	Status               string                          `json:"status,omitempty"`
	Kind                 string                          `json:"kind,omitempty"`
	Side                 string                          `json:"side,omitempty"`
	Scope                string                          `json:"scope,omitempty"`
	FlowID               string                          `json:"flow_id,omitempty"`
	Target               string                          `json:"target,omitempty"`
	RouteTarget          string                          `json:"route_target,omitempty"`
	Domain               string                          `json:"domain,omitempty"`
	DomainSource         string                          `json:"domain_source,omitempty"`
	TargetHost           string                          `json:"target_host,omitempty"`
	TargetIP             string                          `json:"target_ip,omitempty"`
	RouteHost            string                          `json:"route_host,omitempty"`
	RouteIP              string                          `json:"route_ip,omitempty"`
	NodeID               string                          `json:"node_id,omitempty"`
	Group                string                          `json:"group,omitempty"`
	Transport            string                          `json:"transport,omitempty"`
	OpenedAt             string                          `json:"opened_at,omitempty"`
	ClosedAt             string                          `json:"closed_at,omitempty"`
	LastActive           string                          `json:"last_active,omitempty"`
	LastWriteBlockedAt   string                          `json:"last_write_blocked_at,omitempty"`
	CloseReason          string                          `json:"close_reason,omitempty"`
	AgeMS                int64                           `json:"age_ms"`
	DurationMS           int64                           `json:"duration_ms,omitempty"`
	IdleMS               int64                           `json:"idle_ms"`
	BytesUp              int64                           `json:"bytes_up,omitempty"`
	BytesDown            int64                           `json:"bytes_down,omitempty"`
	WritesUp             int64                           `json:"writes_up,omitempty"`
	WritesDown           int64                           `json:"writes_down,omitempty"`
	BlockedWritesUp      int64                           `json:"blocked_writes_up,omitempty"`
	BlockedWritesDown    int64                           `json:"blocked_writes_down,omitempty"`
	WriteBlockMSUp       int64                           `json:"write_block_ms_up,omitempty"`
	WriteBlockMSDown     int64                           `json:"write_block_ms_down,omitempty"`
	MaxWriteBlockMSUp    int64                           `json:"max_write_block_ms_up,omitempty"`
	MaxWriteBlockMSDown  int64                           `json:"max_write_block_ms_down,omitempty"`
	LastWriteBlockMSUp   int64                           `json:"last_write_block_ms_up,omitempty"`
	LastWriteBlockMSDown int64                           `json:"last_write_block_ms_down,omitempty"`
	LastCongestionSide   string                          `json:"last_congestion_side,omitempty"`
	Buffer               probeSubstreamBufferMonitorItem `json:"buffer"`
}

type probeSubstreamBufferMonitorItem struct {
	Status               string `json:"status"`
	YamuxWindowBytes     int    `json:"yamux_window_bytes"`
	BlockedWritesUp      int64  `json:"blocked_writes_up,omitempty"`
	BlockedWritesDown    int64  `json:"blocked_writes_down,omitempty"`
	WriteBlockMSUp       int64  `json:"write_block_ms_up,omitempty"`
	WriteBlockMSDown     int64  `json:"write_block_ms_down,omitempty"`
	MaxWriteBlockMSUp    int64  `json:"max_write_block_ms_up,omitempty"`
	MaxWriteBlockMSDown  int64  `json:"max_write_block_ms_down,omitempty"`
	LastWriteBlockMSUp   int64  `json:"last_write_block_ms_up,omitempty"`
	LastWriteBlockMSDown int64  `json:"last_write_block_ms_down,omitempty"`
	LastCongestionSide   string `json:"last_congestion_side,omitempty"`
	LastWriteBlockedAt   string `json:"last_write_blocked_at,omitempty"`
}

func snapshotProbeSubstreamMonitorPayload(nodeID string, requestID string, scope string) probeSubstreamMonitorPayload {
	tcp := globalProbeTCPDebugState.snapshotPayload(strings.TrimSpace(nodeID), strings.TrimSpace(requestID))
	payload := probeSubstreamMonitorPayload{
		Type:             "substreams_result",
		RequestID:        strings.TrimSpace(requestID),
		NodeID:           strings.TrimSpace(nodeID),
		OK:               true,
		Scope:            firstNonEmptyProbeTCPDebugString(strings.TrimSpace(scope), "local"),
		Active:           []probeSubstreamMonitorItem{},
		Completed:        []probeSubstreamMonitorItem{},
		Failures:         tcp.Failures,
		YamuxWindowBytes: probeChainRelayYamuxMaxStreamWindowBytes,
		FetchedAt:        time.Now().UTC().Format(time.RFC3339),
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
	}
	for _, item := range tcp.Active {
		if sub, ok := buildProbeSubstreamMonitorItem(item); ok {
			payload.Active = append(payload.Active, sub)
		}
	}
	for _, item := range tcp.Completed {
		if sub, ok := buildProbeSubstreamMonitorItem(item); ok {
			payload.Completed = append(payload.Completed, sub)
		}
	}
	payload.ActiveCount = len(payload.Active)
	payload.CompletedCount = len(payload.Completed)
	payload.FailureCount = len(payload.Failures)
	return payload
}

func buildProbeSubstreamMonitorItem(item probeTCPDebugConnectionItemPayload) (probeSubstreamMonitorItem, bool) {
	kind := probeSubstreamKindFromTCPDebugItem(item)
	if kind == "" {
		return probeSubstreamMonitorItem{}, false
	}
	buffer := probeSubstreamBufferMonitorItem{
		Status:               "clear",
		YamuxWindowBytes:     probeChainRelayYamuxMaxStreamWindowBytes,
		BlockedWritesUp:      item.BlockedWritesUp,
		BlockedWritesDown:    item.BlockedWritesDown,
		WriteBlockMSUp:       item.WriteBlockMSUp,
		WriteBlockMSDown:     item.WriteBlockMSDown,
		MaxWriteBlockMSUp:    item.MaxWriteBlockMSUp,
		MaxWriteBlockMSDown:  item.MaxWriteBlockMSDown,
		LastWriteBlockMSUp:   item.LastWriteBlockMSUp,
		LastWriteBlockMSDown: item.LastWriteBlockMSDown,
		LastCongestionSide:   strings.TrimSpace(item.LastCongestionSide),
		LastWriteBlockedAt:   strings.TrimSpace(item.LastWriteBlockedAt),
	}
	if buffer.BlockedWritesUp > 0 || buffer.BlockedWritesDown > 0 || buffer.MaxWriteBlockMSUp > 0 || buffer.MaxWriteBlockMSDown > 0 {
		buffer.Status = "blocked"
	}
	target := strings.TrimSpace(item.Target)
	routeTarget := strings.TrimSpace(item.RouteTarget)
	targetHost, targetIP := splitProbeSubstreamHostAndIP(target)
	routeHost, routeIP := splitProbeSubstreamHostAndIP(routeTarget)
	return probeSubstreamMonitorItem{
		ID:                   strings.TrimSpace(item.ID),
		Status:               firstNonEmptyProbeTCPDebugString(strings.TrimSpace(item.Status), "active"),
		Kind:                 kind,
		Side:                 strings.TrimSpace(item.Side),
		Scope:                strings.TrimSpace(item.Scope),
		FlowID:               strings.TrimSpace(item.FlowID),
		Target:               target,
		RouteTarget:          routeTarget,
		Domain:               strings.TrimSpace(item.Domain),
		DomainSource:         strings.TrimSpace(item.DomainSource),
		TargetHost:           targetHost,
		TargetIP:             targetIP,
		RouteHost:            routeHost,
		RouteIP:              routeIP,
		NodeID:               strings.TrimSpace(item.NodeID),
		Group:                strings.TrimSpace(item.Group),
		Transport:            firstNonEmptyProbeTCPDebugString(strings.TrimSpace(item.Transport), "yamux"),
		OpenedAt:             strings.TrimSpace(item.OpenedAt),
		ClosedAt:             strings.TrimSpace(item.ClosedAt),
		LastActive:           strings.TrimSpace(item.LastActive),
		LastWriteBlockedAt:   strings.TrimSpace(item.LastWriteBlockedAt),
		CloseReason:          strings.TrimSpace(item.CloseReason),
		AgeMS:                item.AgeMS,
		DurationMS:           item.DurationMS,
		IdleMS:               item.IdleMS,
		BytesUp:              item.BytesUp,
		BytesDown:            item.BytesDown,
		WritesUp:             item.WritesUp,
		WritesDown:           item.WritesDown,
		BlockedWritesUp:      item.BlockedWritesUp,
		BlockedWritesDown:    item.BlockedWritesDown,
		WriteBlockMSUp:       item.WriteBlockMSUp,
		WriteBlockMSDown:     item.WriteBlockMSDown,
		MaxWriteBlockMSUp:    item.MaxWriteBlockMSUp,
		MaxWriteBlockMSDown:  item.MaxWriteBlockMSDown,
		LastWriteBlockMSUp:   item.LastWriteBlockMSUp,
		LastWriteBlockMSDown: item.LastWriteBlockMSDown,
		LastCongestionSide:   strings.TrimSpace(item.LastCongestionSide),
		Buffer:               buffer,
	}, true
}

func splitProbeSubstreamHostAndIP(addr string) (string, string) {
	host := probeTCPDebugTargetHost(addr)
	if host == "" {
		return "", ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return host, ip.String()
	}
	return host, ""
}

func probeSubstreamKindFromScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "tun":
		return "tun"
	case "explicit":
		return "explicit_proxy"
	case "port_forward":
		return "port_forward"
	case "chain_exit":
		return "peer_exit"
	default:
		return ""
	}
}

func probeSubstreamKindFromTCPDebugItem(item probeTCPDebugConnectionItemPayload) string {
	if item.Direct {
		return ""
	}
	return probeSubstreamKindFromScope(item.Scope)
}
