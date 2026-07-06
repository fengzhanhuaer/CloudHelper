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

func mngRoutePageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/route" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngRoutePageHTML))
}

func mngRouteVirtualRouterHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		result, err := getMngProbeVirtualRouterConfig()
		writeMngRouteResult(w, result, err)
	case http.MethodPost:
		payload, err := readMngRawJSONPayload(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
		result, err := upsertMngProbeVirtualRouterConfig(payload, controllerBaseURLFromRequest(r))
		writeMngRouteResult(w, result, err)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func mngRouteVirtualRouterRouteRulesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		result, err := getMngProbeVirtualRouterRouteRules()
		writeMngRouteResult(w, result, err)
	case http.MethodPost:
		payload, err := readMngRawJSONPayload(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
		result, err := upsertMngProbeVirtualRouterRouteRules(payload, controllerBaseURLFromRequest(r))
		writeMngRouteResult(w, result, err)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func mngRouteVirtualRouterFakeIPResetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	library, err := resetProbeVirtualRouterFakeIPLibrary()
	result := map[string]any{"fake_ip_library": library}
	if err == nil {
		result["sync"] = dispatchProbeRouteConfigSyncToKnownNodes(controllerBaseURLFromRequest(r))
	}
	writeMngRouteResult(w, result, err)
}

func mngRouteVirtualRouterStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": listMngVirtualRouterRouteStatus(),
	})
}

func mngRouteVirtualRouterLatencyProbeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result := dispatchProbeVirtualRouterLatencyProbeToKnownNodes()
	writeJSON(w, http.StatusOK, result)
}

func mngRouteVirtualRouterSpeedTestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		SourceNodeID string `json:"source_node_id"`
		TargetNodeID string `json:"target_node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result := dispatchProbeVirtualRouterSpeedTestToNode(req.SourceNodeID, req.TargetNodeID)
	writeJSON(w, http.StatusOK, result)
}

type mngRouteRelayStatusView struct {
	NodeID         string                            `json:"node_id"`
	Online         bool                              `json:"online"`
	LastSeen       string                            `json:"last_seen,omitempty"`
	RouteID        string                            `json:"route_id"`
	RouteName      string                            `json:"route_name,omitempty"`
	RouteType      string                            `json:"route_type,omitempty"`
	Role           string                            `json:"role,omitempty"`
	ListenHost     string                            `json:"listen_host,omitempty"`
	ListenPort     int                               `json:"listen_port,omitempty"`
	RouteLayer     string                            `json:"route_layer,omitempty"`
	NextHost       string                            `json:"next_host,omitempty"`
	NextPort       int                               `json:"next_port,omitempty"`
	NextNodeID     string                            `json:"next_node_id,omitempty"`
	NextRouteLayer string                            `json:"next_route_layer,omitempty"`
	NextDialMode   string                            `json:"next_dial_mode,omitempty"`
	PrevHost       string                            `json:"prev_host,omitempty"`
	PrevPort       int                               `json:"prev_port,omitempty"`
	PrevNodeID     string                            `json:"prev_node_id,omitempty"`
	PrevRouteLayer string                            `json:"prev_route_layer,omitempty"`
	PrevDialMode   string                            `json:"prev_dial_mode,omitempty"`
	ListenState    *probeRelayProtocolStateSnapshot  `json:"listen_state,omitempty"`
	NextState      *probeRelayProtocolStateSnapshot  `json:"next_state,omitempty"`
	PrevState      *probeRelayProtocolStateSnapshot  `json:"prev_state,omitempty"`
	VirtualRouter  *probeVirtualRouterRuntimeStats   `json:"virtual_router,omitempty"`
	BridgeStatus   *probeRouteBridgeRuntimeStatus    `json:"bridge_status,omitempty"`
	BridgeSessions []probeRouteBridgeSessionSnapshot `json:"bridge_sessions,omitempty"`
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
	BridgeStatus   *probeRouteBridgeRuntimeStatus    `json:"bridge_status,omitempty"`
	BridgeSessions []probeRouteBridgeSessionSnapshot `json:"bridge_sessions,omitempty"`
}

type mngVirtualRouterRouteStatusView struct {
	RuleID             string                          `json:"rule_id,omitempty"`
	RuleName           string                          `json:"rule_name,omitempty"`
	RouteID            string                          `json:"route_id"`
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

func listMngVirtualRouterRouteStatus() []mngVirtualRouterRouteStatusView {
	if ProbeRouteConfigStore == nil {
		return []mngVirtualRouterRouteStatusView{}
	}
	ProbeRouteConfigStore.mu.RLock()
	config := ensureProbeVirtualRouterProbeIPsForKnownNodes(normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter))
	ProbeRouteConfigStore.mu.RUnlock()

	runtimes := listProbeRuntimes()
	runtimeByNode := make(map[string]probeRuntimeStatus, len(runtimes))
	statusByNodeRoute := make(map[string]mngRouteRelayStatusView)
	for _, runtime := range runtimes {
		nodeID := normalizeProbeNodeID(runtime.NodeID)
		if nodeID == "" {
			continue
		}
		runtimeByNode[nodeID] = runtime
		for _, status := range runtime.RelayStatus {
			routeID := strings.TrimSpace(status.RouteID)
			if routeID == "" {
				continue
			}
			statusByNodeRoute[nodeID+"|"+routeID] = mngRouteRelayStatusView{
				NodeID:         nodeID,
				Online:         runtime.Online,
				LastSeen:       strings.TrimSpace(runtime.LastSeen),
				RouteID:        routeID,
				RouteName:      strings.TrimSpace(status.RouteName),
				RouteType:      strings.TrimSpace(status.RouteType),
				Role:           strings.TrimSpace(status.Role),
				ListenHost:     strings.TrimSpace(status.ListenHost),
				ListenPort:     status.ListenPort,
				RouteLayer:     strings.TrimSpace(status.RouteLayer),
				NextHost:       strings.TrimSpace(status.NextHost),
				NextPort:       status.NextPort,
				NextNodeID:     strings.TrimSpace(status.NextNodeID),
				NextRouteLayer: strings.TrimSpace(status.NextRouteLayer),
				NextDialMode:   strings.TrimSpace(status.NextDialMode),
				PrevHost:       strings.TrimSpace(status.PrevHost),
				PrevPort:       status.PrevPort,
				PrevNodeID:     strings.TrimSpace(status.PrevNodeID),
				PrevRouteLayer: strings.TrimSpace(status.PrevRouteLayer),
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
		routeID := probeVirtualRouterRuntimeRouteID(rule)
		from := buildMngVirtualRouterRouteSideStatus(fromNodeID, routeID, runtimeByNode, statusByNodeRoute)
		to := buildMngVirtualRouterRouteSideStatus(toNodeID, routeID, runtimeByNode, statusByNodeRoute)
		view := mngVirtualRouterRouteStatusView{
			RuleID:     strings.TrimSpace(rule.ID),
			RuleName:   strings.TrimSpace(rule.Name),
			RouteID:    routeID,
			Enabled:    rule.Enabled,
			Direction:  "A->B",
			FromNodeID: fromNodeID,
			ToNodeID:   toNodeID,
			FromIP:     ipByNode[fromNodeID],
			ToIP:       ipByNode[toNodeID],
			From:       from,
			To:         to,
		}
		view.Status = summarizeMngVirtualRouterRouteStatus(rule.Enabled, from, to)
		// Compatibility fields: these are lifecycle event sums, not unique IP
		// packet totals. The UI renders the explicit lifecycle counters instead.
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
		left := firstNonEmptyString(items[i].RuleName, items[i].RuleID, items[i].RouteID)
		right := firstNonEmptyString(items[j].RuleName, items[j].RuleID, items[j].RouteID)
		return left < right
	})
	return items
}

func buildMngVirtualRouterRouteSideStatus(nodeID string, routeID string, runtimes map[string]probeRuntimeStatus, statuses map[string]mngRouteRelayStatusView) mngVirtualRouterRouteSideStatus {
	runtime, onlineKnown := runtimes[nodeID]
	status, found := statuses[nodeID+"|"+routeID]
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
	var latestAt time.Time
	var latestLatency int64
	var fallback int64
	for _, item := range values {
		if item == nil || item.LastPingLatencyMS <= 0 {
			continue
		}
		if item.LastPingLatencyMS > fallback {
			fallback = item.LastPingLatencyMS
		}
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(item.LastPingAt))
		if err != nil || at.IsZero() {
			continue
		}
		if latestAt.IsZero() || at.After(latestAt) {
			latestAt = at
			latestLatency = item.LastPingLatencyMS
		}
	}
	if latestLatency > 0 {
		return latestLatency
	}
	return fallback
}

func mngVirtualRouterStatsError(item *probeVirtualRouterRuntimeStats) string {
	if item == nil {
		return ""
	}
	return normalizeMngVirtualRouterBridgeError(firstNonEmptyString(strings.TrimSpace(item.LastPingError), strings.TrimSpace(item.LastOpenError)))
}

func mngVirtualRouterSideStatsError(side mngVirtualRouterRouteSideStatus) string {
	stats := side.VirtualRouter
	if stats == nil {
		return ""
	}
	if errText := strings.TrimSpace(stats.LastPingError); errText != "" && !isMngVirtualRouterSideErrorStale(side, stats.LastPingAt) {
		return normalizeMngVirtualRouterBridgeError(errText)
	}
	if errText := strings.TrimSpace(stats.LastOpenError); errText != "" && !isMngVirtualRouterSideErrorStale(side, stats.LastOpenAt) {
		return normalizeMngVirtualRouterBridgeError(errText)
	}
	return ""
}

func normalizeMngVirtualRouterBridgeError(value string) string {
	text := strings.TrimSpace(value)
	text = strings.ReplaceAll(text, "upstream bridge", "bridge")
	text = strings.ReplaceAll(text, "downstream bridge", "bridge")
	return text
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

func mngVirtualRouterSideBridgeSessions(side mngVirtualRouterRouteSideStatus) []probeRouteBridgeSessionSnapshot {
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

func writeMngRouteError(w http.ResponseWriter, err error) {
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

func writeMngRouteResult(w http.ResponseWriter, result map[string]interface{}, err error) {
	if err != nil {
		writeMngRouteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
