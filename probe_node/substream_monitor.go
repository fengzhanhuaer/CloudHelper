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
	PairCount        int                               `json:"pair_count"`
	Active           []probeSubstreamMonitorItem       `json:"active"`
	Completed        []probeSubstreamMonitorItem       `json:"completed"`
	Pairs            []probeSubstreamMonitorPair       `json:"pairs"`
	Failures         []probeTCPDebugFailureItemPayload `json:"failures"`
	FrameWindowBytes int                               `json:"frame_window_bytes"`
	FetchedAt        string                            `json:"fetched_at,omitempty"`
	Error            string                            `json:"error,omitempty"`
	Timestamp        string                            `json:"timestamp,omitempty"`
}

type probeSubstreamMonitorItem struct {
	ID                    string                          `json:"id"`
	Status                string                          `json:"status,omitempty"`
	TrackingID            string                          `json:"tracking_id,omitempty"`
	Kind                  string                          `json:"kind,omitempty"`
	Side                  string                          `json:"side,omitempty"`
	Scope                 string                          `json:"scope,omitempty"`
	FlowID                string                          `json:"flow_id,omitempty"`
	Target                string                          `json:"target,omitempty"`
	RouteTarget           string                          `json:"route_target,omitempty"`
	Domain                string                          `json:"domain,omitempty"`
	DomainSource          string                          `json:"domain_source,omitempty"`
	TargetHost            string                          `json:"target_host,omitempty"`
	TargetIP              string                          `json:"target_ip,omitempty"`
	RouteHost             string                          `json:"route_host,omitempty"`
	RouteIP               string                          `json:"route_ip,omitempty"`
	NodeID                string                          `json:"node_id,omitempty"`
	Group                 string                          `json:"group,omitempty"`
	Transport             string                          `json:"transport,omitempty"`
	SessionID             string                          `json:"session_id,omitempty"`
	SessionRole           string                          `json:"session_role,omitempty"`
	SessionStreamsOpen    int                             `json:"session_streams_open,omitempty"`
	SessionStreamsAfter   int                             `json:"session_streams_after,omitempty"`
	SessionStreamsCurrent int                             `json:"session_streams_current,omitempty"`
	SessionRTTMS          int64                           `json:"session_rtt_ms,omitempty"`
	SessionLastPingAt     string                          `json:"session_last_ping_at,omitempty"`
	SessionLastPongAt     string                          `json:"session_last_pong_at,omitempty"`
	SessionPingsSent      int64                           `json:"session_pings_sent,omitempty"`
	SessionPongsReceived  int64                           `json:"session_pongs_received,omitempty"`
	SessionPingTimeouts   int64                           `json:"session_ping_timeouts,omitempty"`
	OpenedAt              string                          `json:"opened_at,omitempty"`
	ClosedAt              string                          `json:"closed_at,omitempty"`
	LastActive            string                          `json:"last_active,omitempty"`
	LastWriteBlockedAt    string                          `json:"last_write_blocked_at,omitempty"`
	CloseReason           string                          `json:"close_reason,omitempty"`
	OpenLatencyMS         int64                           `json:"open_latency_ms,omitempty"`
	AgeMS                 int64                           `json:"age_ms"`
	DurationMS            int64                           `json:"duration_ms,omitempty"`
	IdleMS                int64                           `json:"idle_ms"`
	BytesUp               int64                           `json:"bytes_up,omitempty"`
	BytesDown             int64                           `json:"bytes_down,omitempty"`
	WritesUp              int64                           `json:"writes_up,omitempty"`
	WritesDown            int64                           `json:"writes_down,omitempty"`
	BlockedWritesUp       int64                           `json:"blocked_writes_up,omitempty"`
	BlockedWritesDown     int64                           `json:"blocked_writes_down,omitempty"`
	WriteBlockMSUp        int64                           `json:"write_block_ms_up,omitempty"`
	WriteBlockMSDown      int64                           `json:"write_block_ms_down,omitempty"`
	MaxWriteBlockMSUp     int64                           `json:"max_write_block_ms_up,omitempty"`
	MaxWriteBlockMSDown   int64                           `json:"max_write_block_ms_down,omitempty"`
	LastWriteBlockMSUp    int64                           `json:"last_write_block_ms_up,omitempty"`
	LastWriteBlockMSDown  int64                           `json:"last_write_block_ms_down,omitempty"`
	LastCongestionSide    string                          `json:"last_congestion_side,omitempty"`
	Buffer                probeSubstreamBufferMonitorItem `json:"buffer"`
}

type probeSubstreamMonitorPair struct {
	Key        string                     `json:"key"`
	Status     string                     `json:"status"`
	TrackingID string                     `json:"tracking_id,omitempty"`
	FlowID     string                     `json:"flow_id,omitempty"`
	Group      string                     `json:"group,omitempty"`
	Entry      *probeSubstreamMonitorItem `json:"entry,omitempty"`
	Exit       *probeSubstreamMonitorItem `json:"exit,omitempty"`
}

type probeSubstreamBufferMonitorItem struct {
	Status               string `json:"status"`
	FrameWindowBytes     int    `json:"frame_window_bytes"`
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
		Pairs:            []probeSubstreamMonitorPair{},
		Failures:         tcp.Failures,
		FrameWindowBytes: probeChainFrameMaxDataBytes * probeChainFrameSessionInboundBuffer,
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
	payload.Pairs = buildProbeSubstreamMonitorPairsFromRows(collectProbeSubstreamMonitorRows(payload, "local", ""))
	payload.PairCount = len(payload.Pairs)
	return payload
}

func buildProbeSubstreamMonitorItem(item probeTCPDebugConnectionItemPayload) (probeSubstreamMonitorItem, bool) {
	kind := probeSubstreamKindFromTCPDebugItem(item)
	if kind == "" {
		return probeSubstreamMonitorItem{}, false
	}
	buffer := probeSubstreamBufferMonitorItem{
		Status:               "clear",
		FrameWindowBytes:     probeChainFrameMaxDataBytes * probeChainFrameSessionInboundBuffer,
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
		ID:                    strings.TrimSpace(item.ID),
		Status:                firstNonEmptyProbeTCPDebugString(strings.TrimSpace(item.Status), "active"),
		TrackingID:            firstNonEmptyProbeTCPDebugString(strings.TrimSpace(item.TrackingID), strings.TrimSpace(item.FlowID)),
		Kind:                  kind,
		Side:                  strings.TrimSpace(item.Side),
		Scope:                 strings.TrimSpace(item.Scope),
		FlowID:                strings.TrimSpace(item.FlowID),
		Target:                target,
		RouteTarget:           routeTarget,
		Domain:                strings.TrimSpace(item.Domain),
		DomainSource:          strings.TrimSpace(item.DomainSource),
		TargetHost:            targetHost,
		TargetIP:              targetIP,
		RouteHost:             routeHost,
		RouteIP:               routeIP,
		NodeID:                strings.TrimSpace(item.NodeID),
		Group:                 strings.TrimSpace(item.Group),
		Transport:             firstNonEmptyProbeTCPDebugString(strings.TrimSpace(item.Transport), "frame"),
		SessionID:             strings.TrimSpace(item.SessionID),
		SessionRole:           strings.TrimSpace(item.SessionRole),
		SessionStreamsOpen:    item.SessionStreamsOpen,
		SessionStreamsAfter:   item.SessionStreamsAfter,
		SessionStreamsCurrent: item.SessionStreamsCurrent,
		SessionRTTMS:          item.SessionRTTMS,
		SessionLastPingAt:     strings.TrimSpace(item.SessionLastPingAt),
		SessionLastPongAt:     strings.TrimSpace(item.SessionLastPongAt),
		SessionPingsSent:      item.SessionPingsSent,
		SessionPongsReceived:  item.SessionPongsReceived,
		SessionPingTimeouts:   item.SessionPingTimeouts,
		OpenedAt:              strings.TrimSpace(item.OpenedAt),
		ClosedAt:              strings.TrimSpace(item.ClosedAt),
		LastActive:            strings.TrimSpace(item.LastActive),
		LastWriteBlockedAt:    strings.TrimSpace(item.LastWriteBlockedAt),
		CloseReason:           strings.TrimSpace(item.CloseReason),
		OpenLatencyMS:         item.OpenLatencyMS,
		AgeMS:                 item.AgeMS,
		DurationMS:            item.DurationMS,
		IdleMS:                item.IdleMS,
		BytesUp:               item.BytesUp,
		BytesDown:             item.BytesDown,
		WritesUp:              item.WritesUp,
		WritesDown:            item.WritesDown,
		BlockedWritesUp:       item.BlockedWritesUp,
		BlockedWritesDown:     item.BlockedWritesDown,
		WriteBlockMSUp:        item.WriteBlockMSUp,
		WriteBlockMSDown:      item.WriteBlockMSDown,
		MaxWriteBlockMSUp:     item.MaxWriteBlockMSUp,
		MaxWriteBlockMSDown:   item.MaxWriteBlockMSDown,
		LastWriteBlockMSUp:    item.LastWriteBlockMSUp,
		LastWriteBlockMSDown:  item.LastWriteBlockMSDown,
		LastCongestionSide:    strings.TrimSpace(item.LastCongestionSide),
		Buffer:                buffer,
	}, true
}

type probeSubstreamMonitorPairRow struct {
	source string
	group  string
	item   probeSubstreamMonitorItem
}

func mergeProbeSubstreamMonitorPairs(local probeSubstreamMonitorPayload, peer probeLocalPeerStatusMonitorSnapshot) []probeSubstreamMonitorPair {
	rows := collectProbeSubstreamMonitorRows(local, "local", "")
	for _, group := range peer.Groups {
		groupName := strings.TrimSpace(group.Group)
		rows = append(rows, collectProbeSubstreamMonitorRows(group.Entry.Substreams, "entry", groupName)...)
		rows = append(rows, collectProbeSubstreamMonitorRows(group.Exit.Substreams, "exit", groupName)...)
	}
	return buildProbeSubstreamMonitorPairsFromRows(rows)
}

func collectProbeSubstreamMonitorRows(payload probeSubstreamMonitorPayload, source string, group string) []probeSubstreamMonitorPairRow {
	rows := make([]probeSubstreamMonitorPairRow, 0, len(payload.Active)+len(payload.Completed))
	for _, item := range payload.Active {
		rows = append(rows, probeSubstreamMonitorPairRow{source: strings.TrimSpace(source), group: strings.TrimSpace(group), item: item})
	}
	for _, item := range payload.Completed {
		rows = append(rows, probeSubstreamMonitorPairRow{source: strings.TrimSpace(source), group: strings.TrimSpace(group), item: item})
	}
	return rows
}

func buildProbeSubstreamMonitorPairsFromRows(rows []probeSubstreamMonitorPairRow) []probeSubstreamMonitorPair {
	pairs := []probeSubstreamMonitorPair{}
	byKey := map[string]int{}
	for _, row := range rows {
		item := row.item
		role := resolveProbeSubstreamEndpointRole(item, row.source)
		key := probeSubstreamMonitorPairKey(item, role)
		if key == "" {
			continue
		}
		pairIndex, ok := byKey[key]
		if ok && probeSubstreamMonitorPairHasRole(pairs[pairIndex], role) && !isSameProbeSubstreamEndpoint(probeSubstreamMonitorPairEndpoint(pairs[pairIndex], role), item) {
			key = key + ":" + strings.TrimSpace(item.ID) + ":" + probeSubstreamMonitorPairTargetToken(item)
			pairIndex, ok = byKey[key]
		}
		if !ok {
			pair := probeSubstreamMonitorPair{
				Key:        key,
				Status:     "missing_exit",
				TrackingID: firstNonEmptyProbeTCPDebugString(strings.TrimSpace(item.TrackingID), strings.TrimSpace(item.FlowID)),
				FlowID:     strings.TrimSpace(item.FlowID),
			}
			pairs = append(pairs, pair)
			pairIndex = len(pairs) - 1
			byKey[key] = pairIndex
		}
		if pairs[pairIndex].TrackingID == "" {
			pairs[pairIndex].TrackingID = firstNonEmptyProbeTCPDebugString(strings.TrimSpace(item.TrackingID), strings.TrimSpace(item.FlowID))
		}
		if pairs[pairIndex].FlowID == "" {
			pairs[pairIndex].FlowID = strings.TrimSpace(item.FlowID)
		}
		group := firstNonEmptyProbeTCPDebugString(strings.TrimSpace(row.group), strings.TrimSpace(item.Group))
		if group != "" && (pairs[pairIndex].Group == "" || pairs[pairIndex].Group == "local") {
			pairs[pairIndex].Group = group
		}
		itemCopy := item
		if role == "exit" {
			pairs[pairIndex].Exit = &itemCopy
		} else {
			pairs[pairIndex].Entry = &itemCopy
		}
		pairs[pairIndex].Status = probeSubstreamMonitorPairStatus(pairs[pairIndex])
	}
	return pairs
}

func probeSubstreamMonitorPairKey(item probeSubstreamMonitorItem, role string) string {
	if trackingID := firstNonEmptyProbeTCPDebugString(strings.TrimSpace(item.TrackingID), strings.TrimSpace(item.FlowID)); trackingID != "" {
		return "track:" + trackingID
	}
	if id := strings.TrimSpace(item.ID); id != "" {
		return strings.TrimSpace(role) + ":" + id
	}
	if target := firstNonEmptyProbeTCPDebugString(strings.TrimSpace(item.Target), strings.TrimSpace(item.RouteTarget)); target != "" {
		return strings.TrimSpace(role) + ":target:" + target
	}
	return ""
}

func probeSubstreamMonitorPairTargetToken(item probeSubstreamMonitorItem) string {
	token := firstNonEmptyProbeTCPDebugString(strings.TrimSpace(item.Target), strings.TrimSpace(item.RouteTarget), strings.TrimSpace(item.FlowID), strings.TrimSpace(item.TrackingID))
	token = strings.NewReplacer(":", "_", "/", "_", "\\", "_", " ", "_").Replace(token)
	if token == "" {
		return "unknown"
	}
	return token
}

func probeSubstreamMonitorPairEndpoint(pair probeSubstreamMonitorPair, role string) *probeSubstreamMonitorItem {
	if role == "exit" {
		return pair.Exit
	}
	return pair.Entry
}

func probeSubstreamMonitorPairHasRole(pair probeSubstreamMonitorPair, role string) bool {
	return probeSubstreamMonitorPairEndpoint(pair, role) != nil
}

func probeSubstreamMonitorPairStatus(pair probeSubstreamMonitorPair) string {
	if pair.Entry != nil && pair.Exit != nil {
		return "complete"
	}
	if pair.Entry == nil {
		return "missing_entry"
	}
	return "missing_exit"
}

func isSameProbeSubstreamEndpoint(left *probeSubstreamMonitorItem, right probeSubstreamMonitorItem) bool {
	if left == nil {
		return false
	}
	return strings.TrimSpace(left.ID) == strings.TrimSpace(right.ID) &&
		strings.TrimSpace(left.TrackingID) == strings.TrimSpace(right.TrackingID) &&
		strings.TrimSpace(left.FlowID) == strings.TrimSpace(right.FlowID) &&
		strings.TrimSpace(left.Status) == strings.TrimSpace(right.Status) &&
		strings.TrimSpace(left.Scope) == strings.TrimSpace(right.Scope) &&
		strings.TrimSpace(left.Side) == strings.TrimSpace(right.Side) &&
		strings.TrimSpace(left.Target) == strings.TrimSpace(right.Target) &&
		strings.TrimSpace(left.RouteTarget) == strings.TrimSpace(right.RouteTarget)
}

func resolveProbeSubstreamEndpointRole(item probeSubstreamMonitorItem, source string) string {
	scope := strings.ToLower(strings.TrimSpace(item.Scope))
	side := strings.ToLower(strings.TrimSpace(item.Side))
	if scope == "tun" || side == "local" {
		return "entry"
	}
	if scope == "chain_exit" || side == "remote" {
		return "exit"
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(source)), "exit") {
		return "exit"
	}
	return "entry"
}

func cloneProbeSubstreamMonitorPairs(pairs []probeSubstreamMonitorPair) []probeSubstreamMonitorPair {
	if pairs == nil {
		return nil
	}
	out := make([]probeSubstreamMonitorPair, len(pairs))
	for i, pair := range pairs {
		out[i] = pair
		if pair.Entry != nil {
			entry := *pair.Entry
			out[i].Entry = &entry
		}
		if pair.Exit != nil {
			exit := *pair.Exit
			out[i].Exit = &exit
		}
	}
	return out
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
