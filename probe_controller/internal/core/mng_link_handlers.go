package core

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func mngLinkPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/link" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngLinkPageHTML))
}

func mngLinkUsersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := getMngProbeLinkUsers()
	writeMngLinkResult(w, result, err)
}

func mngLinkUserPublicKeyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	payload := json.RawMessage(`{}`)
	if username != "" {
		raw, err := json.Marshal(map[string]string{"username": username})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build request payload"})
			return
		}
		payload = raw
	}
	result, err := getMngProbeLinkUserPublicKey(payload)
	writeMngLinkResult(w, result, err)
}

func mngLinkChainsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := listMngProbeLinkChains()
	writeMngLinkResult(w, result, err)
}

func mngLinkVirtualRouterHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		result, err := getMngProbeVirtualRouterConfig()
		writeMngLinkResult(w, result, err)
	case http.MethodPost:
		payload, err := readMngRawJSONPayload(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
		result, err := upsertMngProbeVirtualRouterConfig(payload, controllerBaseURLFromRequest(r))
		writeMngLinkResult(w, result, err)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func mngLinkVirtualRouterStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": listMngVirtualRouterRouteStatus(),
	})
}

func mngLinkVirtualRouterLatencyProbeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result := dispatchProbeVirtualRouterLatencyProbeToKnownNodes()
	writeJSON(w, http.StatusOK, result)
}

func mngLinkNodeDomainsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodeID := normalizeProbeNodeID(r.URL.Query().Get("node_id"))
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}
	domains := listProbeLinkNodeEditCandidateDomains(nodeID)
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id": nodeID,
		"domains": domains,
	})
}

func mngLinkRelayStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": listMngLinkRelayStatus(),
	})
}

func mngLinkEntryProfilesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := listMngProbeLinkEntryProfiles(strings.TrimSpace(r.URL.Query().Get("chain_id")))
	writeMngLinkResult(w, result, err)
}

func mngLinkEntryProfilesUpsertHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload, err := readMngRawJSONPayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := upsertMngProbeLinkEntryProfile(payload)
	writeMngLinkResult(w, result, err)
}

type mngLinkRelayStatusView struct {
	NodeID         string                            `json:"node_id"`
	Online         bool                              `json:"online"`
	LastSeen       string                            `json:"last_seen,omitempty"`
	ChainID        string                            `json:"chain_id"`
	ChainName      string                            `json:"chain_name,omitempty"`
	ChainType      string                            `json:"chain_type,omitempty"`
	Role           string                            `json:"role,omitempty"`
	ListenHost     string                            `json:"listen_host,omitempty"`
	ListenPort     int                               `json:"listen_port,omitempty"`
	LinkLayer      string                            `json:"link_layer,omitempty"`
	NextHost       string                            `json:"next_host,omitempty"`
	NextPort       int                               `json:"next_port,omitempty"`
	NextNodeID     string                            `json:"next_node_id,omitempty"`
	NextLinkLayer  string                            `json:"next_link_layer,omitempty"`
	NextDialMode   string                            `json:"next_dial_mode,omitempty"`
	PrevHost       string                            `json:"prev_host,omitempty"`
	PrevPort       int                               `json:"prev_port,omitempty"`
	PrevNodeID     string                            `json:"prev_node_id,omitempty"`
	PrevLinkLayer  string                            `json:"prev_link_layer,omitempty"`
	PrevDialMode   string                            `json:"prev_dial_mode,omitempty"`
	ListenState    *probeRelayProtocolStateSnapshot  `json:"listen_state,omitempty"`
	NextState      *probeRelayProtocolStateSnapshot  `json:"next_state,omitempty"`
	PrevState      *probeRelayProtocolStateSnapshot  `json:"prev_state,omitempty"`
	VirtualRouter  *probeVirtualRouterRuntimeStats   `json:"virtual_router,omitempty"`
	BridgeStatus   *probeChainBridgeRuntimeStatus    `json:"bridge_status,omitempty"`
	BridgeSessions []probeChainBridgeSessionSnapshot `json:"bridge_sessions,omitempty"`
	UpdatedAt      string                            `json:"updated_at,omitempty"`
}

type mngVirtualRouterRouteSideStatus struct {
	NodeID         string                            `json:"node_id"`
	Online         bool                              `json:"online"`
	LastSeen       string                            `json:"last_seen,omitempty"`
	RuntimeFound   bool                              `json:"runtime_found"`
	Status         string                            `json:"status"`
	Listen         string                            `json:"listen,omitempty"`
	Next           string                            `json:"next,omitempty"`
	Prev           string                            `json:"prev,omitempty"`
	NextNodeID     string                            `json:"next_node_id,omitempty"`
	PrevNodeID     string                            `json:"prev_node_id,omitempty"`
	NextDialMode   string                            `json:"next_dial_mode,omitempty"`
	PrevDialMode   string                            `json:"prev_dial_mode,omitempty"`
	ListenState    *probeRelayProtocolStateSnapshot  `json:"listen_state,omitempty"`
	NextState      *probeRelayProtocolStateSnapshot  `json:"next_state,omitempty"`
	PrevState      *probeRelayProtocolStateSnapshot  `json:"prev_state,omitempty"`
	VirtualRouter  *probeVirtualRouterRuntimeStats   `json:"virtual_router,omitempty"`
	BridgeStatus   *probeChainBridgeRuntimeStatus    `json:"bridge_status,omitempty"`
	BridgeSessions []probeChainBridgeSessionSnapshot `json:"bridge_sessions,omitempty"`
}

type mngVirtualRouterRouteStatusView struct {
	RuleID             string                          `json:"rule_id,omitempty"`
	RuleName           string                          `json:"rule_name,omitempty"`
	ChainID            string                          `json:"chain_id"`
	Enabled            bool                            `json:"enabled"`
	Direction          string                          `json:"direction"`
	FromNodeID         string                          `json:"from_node_id"`
	ToNodeID           string                          `json:"to_node_id"`
	FromIP             string                          `json:"from_ip,omitempty"`
	ToIP               string                          `json:"to_ip,omitempty"`
	Status             string                          `json:"status"`
	Packets            int64                           `json:"packets,omitempty"`
	Bytes              int64                           `json:"bytes,omitempty"`
	PacketsForwarded   int64                           `json:"packets_forwarded,omitempty"`
	BytesForwarded     int64                           `json:"bytes_forwarded,omitempty"`
	PacketsReceived    int64                           `json:"packets_received,omitempty"`
	BytesReceived      int64                           `json:"bytes_received,omitempty"`
	PacketsDelivered   int64                           `json:"packets_delivered,omitempty"`
	BytesDelivered     int64                           `json:"bytes_delivered,omitempty"`
	FramesSent         int64                           `json:"frames_sent,omitempty"`
	FrameBytesSent     int64                           `json:"frame_bytes_sent,omitempty"`
	FramesReceived     int64                           `json:"frames_received,omitempty"`
	FrameBytesReceived int64                           `json:"frame_bytes_received,omitempty"`
	LastLatencyMS      int64                           `json:"last_latency_ms,omitempty"`
	LastError          string                          `json:"last_error,omitempty"`
	LastPacketAt       string                          `json:"last_packet_at,omitempty"`
	LastFrameAt        string                          `json:"last_frame_at,omitempty"`
	From               mngVirtualRouterRouteSideStatus `json:"from"`
	To                 mngVirtualRouterRouteSideStatus `json:"to"`
	UpdatedAt          string                          `json:"updated_at,omitempty"`
}

func listMngLinkRelayStatus() []mngLinkRelayStatusView {
	runtimes := listProbeRuntimes()
	items := make([]mngLinkRelayStatusView, 0)
	for _, runtime := range runtimes {
		for _, status := range runtime.RelayStatus {
			chainID := strings.TrimSpace(status.ChainID)
			if chainID == "" {
				continue
			}
			items = append(items, mngLinkRelayStatusView{
				NodeID:         strings.TrimSpace(runtime.NodeID),
				Online:         runtime.Online,
				LastSeen:       strings.TrimSpace(runtime.LastSeen),
				ChainID:        chainID,
				ChainName:      strings.TrimSpace(status.ChainName),
				ChainType:      strings.TrimSpace(status.ChainType),
				Role:           strings.TrimSpace(status.Role),
				ListenHost:     strings.TrimSpace(status.ListenHost),
				ListenPort:     status.ListenPort,
				LinkLayer:      strings.TrimSpace(status.LinkLayer),
				NextHost:       strings.TrimSpace(status.NextHost),
				NextPort:       status.NextPort,
				NextNodeID:     strings.TrimSpace(status.NextNodeID),
				NextLinkLayer:  strings.TrimSpace(status.NextLinkLayer),
				NextDialMode:   strings.TrimSpace(status.NextDialMode),
				PrevHost:       strings.TrimSpace(status.PrevHost),
				PrevPort:       status.PrevPort,
				PrevNodeID:     strings.TrimSpace(status.PrevNodeID),
				PrevLinkLayer:  strings.TrimSpace(status.PrevLinkLayer),
				PrevDialMode:   strings.TrimSpace(status.PrevDialMode),
				ListenState:    status.ListenState,
				NextState:      status.NextState,
				PrevState:      status.PrevState,
				VirtualRouter:  status.VirtualRouter,
				BridgeStatus:   status.BridgeStatus,
				BridgeSessions: status.BridgeSessions,
				UpdatedAt:      strings.TrimSpace(status.UpdatedAt),
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].NodeID != items[j].NodeID {
			return items[i].NodeID < items[j].NodeID
		}
		if items[i].ChainID != items[j].ChainID {
			return items[i].ChainID < items[j].ChainID
		}
		return items[i].Role < items[j].Role
	})
	return items
}

func listMngVirtualRouterRouteStatus() []mngVirtualRouterRouteStatusView {
	if ProbeLinkChainStore == nil {
		return []mngVirtualRouterRouteStatusView{}
	}
	ProbeLinkChainStore.mu.RLock()
	config := ensureProbeVirtualRouterProbeIPsForKnownNodes(normalizeProbeVirtualRouterConfig(ProbeLinkChainStore.data.VirtualRouter))
	ProbeLinkChainStore.mu.RUnlock()

	runtimes := listProbeRuntimes()
	runtimeByNode := make(map[string]probeRuntimeStatus, len(runtimes))
	statusByNodeChain := make(map[string]mngLinkRelayStatusView)
	for _, runtime := range runtimes {
		nodeID := normalizeProbeNodeID(runtime.NodeID)
		if nodeID == "" {
			continue
		}
		runtimeByNode[nodeID] = runtime
		for _, status := range runtime.RelayStatus {
			chainID := strings.TrimSpace(status.ChainID)
			if chainID == "" {
				continue
			}
			statusByNodeChain[nodeID+"|"+chainID] = mngLinkRelayStatusView{
				NodeID:         nodeID,
				Online:         runtime.Online,
				LastSeen:       strings.TrimSpace(runtime.LastSeen),
				ChainID:        chainID,
				ChainName:      strings.TrimSpace(status.ChainName),
				ChainType:      strings.TrimSpace(status.ChainType),
				Role:           strings.TrimSpace(status.Role),
				ListenHost:     strings.TrimSpace(status.ListenHost),
				ListenPort:     status.ListenPort,
				LinkLayer:      strings.TrimSpace(status.LinkLayer),
				NextHost:       strings.TrimSpace(status.NextHost),
				NextPort:       status.NextPort,
				NextNodeID:     strings.TrimSpace(status.NextNodeID),
				NextLinkLayer:  strings.TrimSpace(status.NextLinkLayer),
				NextDialMode:   strings.TrimSpace(status.NextDialMode),
				PrevHost:       strings.TrimSpace(status.PrevHost),
				PrevPort:       status.PrevPort,
				PrevNodeID:     strings.TrimSpace(status.PrevNodeID),
				PrevLinkLayer:  strings.TrimSpace(status.PrevLinkLayer),
				PrevDialMode:   strings.TrimSpace(status.PrevDialMode),
				ListenState:    status.ListenState,
				NextState:      status.NextState,
				PrevState:      status.PrevState,
				VirtualRouter:  status.VirtualRouter,
				BridgeStatus:   status.BridgeStatus,
				BridgeSessions: status.BridgeSessions,
				UpdatedAt:      strings.TrimSpace(status.UpdatedAt),
			}
		}
	}

	ipByNode := make(map[string]string, len(config.ProbeIPs))
	for _, item := range config.ProbeIPs {
		if nodeID := normalizeProbeNodeID(item.NodeID); nodeID != "" {
			ipByNode[nodeID] = strings.TrimSpace(item.IP)
		}
	}
	items := make([]mngVirtualRouterRouteStatusView, 0, len(config.TopologyRules))
	for _, rule := range config.TopologyRules {
		fromNodeID := normalizeProbeNodeID(rule.FromNodeID)
		toNodeID := normalizeProbeNodeID(rule.ToNodeID)
		if fromNodeID == "" || toNodeID == "" {
			continue
		}
		chainID := probeVirtualRouterRuntimeChainID(rule)
		from := buildMngVirtualRouterRouteSideStatus(fromNodeID, chainID, runtimeByNode, statusByNodeChain)
		to := buildMngVirtualRouterRouteSideStatus(toNodeID, chainID, runtimeByNode, statusByNodeChain)
		view := mngVirtualRouterRouteStatusView{
			RuleID:     strings.TrimSpace(rule.ID),
			RuleName:   strings.TrimSpace(rule.Name),
			ChainID:    chainID,
			Enabled:    rule.Enabled,
			Direction:  normalizeProbeVirtualRouterDirection(rule.Direction),
			FromNodeID: fromNodeID,
			ToNodeID:   toNodeID,
			FromIP:     ipByNode[fromNodeID],
			ToIP:       ipByNode[toNodeID],
			From:       from,
			To:         to,
		}
		view.Status = summarizeMngVirtualRouterRouteStatus(rule.Enabled, from, to)
		view.Packets, view.Bytes = sumMngVirtualRouterRouteTraffic(from.VirtualRouter, to.VirtualRouter)
		view.PacketsForwarded, view.BytesForwarded, view.PacketsReceived, view.BytesReceived, view.PacketsDelivered, view.BytesDelivered = sumMngVirtualRouterRoutePacketLifecycle(from.VirtualRouter, to.VirtualRouter)
		view.FramesSent, view.FrameBytesSent, view.FramesReceived, view.FrameBytesReceived = sumMngVirtualRouterRouteFrames(from.VirtualRouter, to.VirtualRouter)
		view.LastLatencyMS = lastMngVirtualRouterRouteLatency(from.VirtualRouter, to.VirtualRouter)
		view.LastError = firstNonEmptyString(mngVirtualRouterSideStatsError(from), mngVirtualRouterSideStatsError(to))
		view.LastPacketAt = maxRFC3339String(mngVirtualRouterStatsPacketAt(from.VirtualRouter), mngVirtualRouterStatsPacketAt(to.VirtualRouter))
		view.LastFrameAt = maxRFC3339String(mngVirtualRouterStatsFrameAt(from.VirtualRouter), mngVirtualRouterStatsFrameAt(to.VirtualRouter))
		view.UpdatedAt = maxRFC3339String(from.LastSeen, to.LastSeen)
		items = append(items, view)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Enabled != items[j].Enabled {
			return items[i].Enabled
		}
		left := firstNonEmptyString(items[i].RuleName, items[i].RuleID, items[i].ChainID)
		right := firstNonEmptyString(items[j].RuleName, items[j].RuleID, items[j].ChainID)
		return left < right
	})
	return items
}

func buildMngVirtualRouterRouteSideStatus(nodeID string, chainID string, runtimes map[string]probeRuntimeStatus, statuses map[string]mngLinkRelayStatusView) mngVirtualRouterRouteSideStatus {
	runtime, onlineKnown := runtimes[nodeID]
	status, found := statuses[nodeID+"|"+chainID]
	side := mngVirtualRouterRouteSideStatus{
		NodeID:       nodeID,
		Online:       onlineKnown && runtime.Online,
		LastSeen:     strings.TrimSpace(runtime.LastSeen),
		RuntimeFound: found,
		Status:       "missing_runtime",
	}
	if !side.Online {
		side.Status = "offline"
	}
	if !found {
		return side
	}
	side.Online = status.Online
	side.LastSeen = strings.TrimSpace(status.LastSeen)
	side.RuntimeFound = true
	side.Listen = formatMngVirtualRouterEndpoint(status.ListenHost, status.ListenPort)
	side.Next = formatMngVirtualRouterEndpoint(status.NextHost, status.NextPort)
	side.Prev = formatMngVirtualRouterEndpoint(status.PrevHost, status.PrevPort)
	side.NextNodeID = strings.TrimSpace(status.NextNodeID)
	side.PrevNodeID = strings.TrimSpace(status.PrevNodeID)
	side.NextDialMode = strings.TrimSpace(status.NextDialMode)
	side.PrevDialMode = strings.TrimSpace(status.PrevDialMode)
	side.ListenState = status.ListenState
	side.NextState = status.NextState
	side.PrevState = status.PrevState
	side.VirtualRouter = status.VirtualRouter
	side.BridgeStatus = status.BridgeStatus
	side.BridgeSessions = status.BridgeSessions
	side.Status = "configured"
	if !side.Online {
		side.Status = "offline"
	} else if mngRelayStateHasFailure(status.ListenState) || mngRelayStateHasFailure(status.NextState) || mngRelayStateHasFailure(status.PrevState) || mngVirtualRouterSideStatsError(side) != "" {
		side.Status = "failed"
	} else if mngRelayStateHasSelectedProtocol(status.NextState) || mngRelayStateHasSelectedProtocol(status.PrevState) {
		side.Status = "connected"
	} else if mngRelayStateHasListening(status.ListenState) {
		side.Status = "listening"
	}
	return side
}

func summarizeMngVirtualRouterRouteStatus(enabled bool, from mngVirtualRouterRouteSideStatus, to mngVirtualRouterRouteSideStatus) string {
	if !enabled {
		return "disabled"
	}
	if from.Status == "failed" || to.Status == "failed" {
		return "failed"
	}
	if from.Status == "offline" || to.Status == "offline" {
		return "offline"
	}
	if !from.RuntimeFound || !to.RuntimeFound {
		return "missing_runtime"
	}
	if (from.Status == "connected" || from.Status == "listening" || from.Status == "configured") &&
		(to.Status == "connected" || to.Status == "listening" || to.Status == "configured") {
		return "ready"
	}
	return "partial"
}

func formatMngVirtualRouterEndpoint(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" || port <= 0 {
		return ""
	}
	return host + ":" + strconv.Itoa(port)
}

func mngRelayStateHasFailure(snapshot *probeRelayProtocolStateSnapshot) bool {
	if snapshot == nil {
		return false
	}
	for _, item := range snapshot.ListenerStatuses {
		status := strings.ToLower(strings.TrimSpace(item.Status))
		if status == "failed" {
			return true
		}
	}
	for _, item := range snapshot.ProtocolQualities {
		if strings.TrimSpace(item.LastError) != "" && !item.Available {
			return true
		}
	}
	return false
}

func mngRelayStateHasSelectedProtocol(snapshot *probeRelayProtocolStateSnapshot) bool {
	return snapshot != nil && strings.TrimSpace(snapshot.SelectedProtocol) != ""
}

func mngRelayStateHasListening(snapshot *probeRelayProtocolStateSnapshot) bool {
	if snapshot == nil {
		return false
	}
	for _, item := range snapshot.ListenerStatuses {
		if strings.EqualFold(strings.TrimSpace(item.Status), "listening") {
			return true
		}
	}
	return false
}

func sumMngVirtualRouterRouteTraffic(values ...*probeVirtualRouterRuntimeStats) (int64, int64) {
	var packets int64
	var bytesValue int64
	for _, item := range values {
		if item == nil {
			continue
		}
		packets += item.PacketsForwarded + item.PacketsReceived + item.PacketsDelivered
		bytesValue += item.BytesForwarded + item.BytesReceived + item.BytesDelivered
	}
	return packets, bytesValue
}

func sumMngVirtualRouterRoutePacketLifecycle(values ...*probeVirtualRouterRuntimeStats) (int64, int64, int64, int64, int64, int64) {
	var packetsForwarded int64
	var bytesForwarded int64
	var packetsReceived int64
	var bytesReceived int64
	var packetsDelivered int64
	var bytesDelivered int64
	for _, item := range values {
		if item == nil {
			continue
		}
		packetsForwarded += item.PacketsForwarded
		bytesForwarded += item.BytesForwarded
		packetsReceived += item.PacketsReceived
		bytesReceived += item.BytesReceived
		packetsDelivered += item.PacketsDelivered
		bytesDelivered += item.BytesDelivered
	}
	return packetsForwarded, bytesForwarded, packetsReceived, bytesReceived, packetsDelivered, bytesDelivered
}

func sumMngVirtualRouterRouteFrames(values ...*probeVirtualRouterRuntimeStats) (int64, int64, int64, int64) {
	var framesSent int64
	var frameBytesSent int64
	var framesReceived int64
	var frameBytesReceived int64
	for _, item := range values {
		if item == nil {
			continue
		}
		framesSent += item.FramesSent
		frameBytesSent += item.FrameBytesSent
		framesReceived += item.FramesReceived
		frameBytesReceived += item.FrameBytesReceived
	}
	return framesSent, frameBytesSent, framesReceived, frameBytesReceived
}

func lastMngVirtualRouterRouteLatency(values ...*probeVirtualRouterRuntimeStats) int64 {
	var out int64
	for _, item := range values {
		if item != nil && item.LastPingLatencyMS > out {
			out = item.LastPingLatencyMS
		}
	}
	return out
}

func mngVirtualRouterStatsError(item *probeVirtualRouterRuntimeStats) string {
	if item == nil {
		return ""
	}
	return firstNonEmptyString(strings.TrimSpace(item.LastPingError), strings.TrimSpace(item.LastOpenError))
}

func mngVirtualRouterSideStatsError(side mngVirtualRouterRouteSideStatus) string {
	stats := side.VirtualRouter
	if stats == nil {
		return ""
	}
	if errText := strings.TrimSpace(stats.LastPingError); errText != "" && !isMngVirtualRouterSideErrorStale(side, stats.LastPingAt) {
		return errText
	}
	if errText := strings.TrimSpace(stats.LastOpenError); errText != "" && !isMngVirtualRouterSideErrorStale(side, stats.LastOpenAt) {
		return errText
	}
	return ""
}

func isMngVirtualRouterSideErrorStale(side mngVirtualRouterRouteSideStatus, errorAt string) bool {
	errorAt = strings.TrimSpace(errorAt)
	if errorAt == "" {
		return false
	}
	errTime, err := time.Parse(time.RFC3339, errorAt)
	if err != nil {
		return false
	}
	for _, session := range mngVirtualRouterSideBridgeSessions(side) {
		if session.Closed {
			continue
		}
		connectedAt := strings.TrimSpace(session.ConnectedAt)
		if connectedAt == "" {
			continue
		}
		connectedTime, err := time.Parse(time.RFC3339, connectedAt)
		if err == nil && connectedTime.After(errTime) {
			return true
		}
	}
	return false
}

func mngVirtualRouterSideBridgeSessions(side mngVirtualRouterRouteSideStatus) []probeChainBridgeSessionSnapshot {
	if side.BridgeStatus != nil {
		return side.BridgeStatus.Sessions
	}
	return side.BridgeSessions
}

func mngVirtualRouterStatsPacketAt(item *probeVirtualRouterRuntimeStats) string {
	if item == nil {
		return ""
	}
	return strings.TrimSpace(item.LastPacketAt)
}

func mngVirtualRouterStatsFrameAt(item *probeVirtualRouterRuntimeStats) string {
	if item == nil {
		return ""
	}
	return strings.TrimSpace(item.LastFrameAt)
}

func maxRFC3339String(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	leftTime, leftErr := time.Parse(time.RFC3339, left)
	rightTime, rightErr := time.Parse(time.RFC3339, right)
	if leftErr == nil && rightErr == nil {
		if rightTime.After(leftTime) {
			return right
		}
		return left
	}
	if right > left {
		return right
	}
	return left
}

func mngLinkChainUpsertHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload, err := readMngRawJSONPayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := upsertMngProbeLinkChain(payload, controllerBaseURLFromRequest(r))
	writeMngLinkResult(w, result, err)
}

func mngLinkChainDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload, err := readMngRawJSONPayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := deleteMngProbeLinkChain(payload)
	writeMngLinkResult(w, result, err)
}

func readMngRawJSONPayload(r *http.Request) (json.RawMessage, error) {
	if r == nil || r.Body == nil {
		return json.RawMessage(`{}`), nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(trimmed) {
		return nil, io.ErrUnexpectedEOF
	}
	out := make([]byte, len(trimmed))
	copy(out, trimmed)
	return json.RawMessage(out), nil
}

func writeMngLinkError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unknown error"})
		return
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		msg = "unknown error"
	}
	lower := strings.ToLower(msg)
	status := http.StatusInternalServerError
	switch {
	case strings.Contains(lower, "invalid payload"),
		strings.Contains(lower, " invalid"),
		strings.Contains(lower, " is invalid"),
		strings.Contains(lower, " is required"),
		strings.Contains(lower, " must be"),
		strings.Contains(lower, " duplicated"),
		strings.Contains(lower, "endpoints must be different"),
		strings.Contains(lower, "exceeded limit"):
		status = http.StatusBadRequest
	case strings.Contains(lower, "not found"):
		status = http.StatusNotFound
	case strings.Contains(lower, "not initialized"):
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeMngLinkResult(w http.ResponseWriter, result map[string]interface{}, err error) {
	if err != nil {
		writeMngLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
