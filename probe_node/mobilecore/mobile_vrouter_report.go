package mobilecore

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

const mobileProbeVirtualRouterRuntimeRole = "virtual_router"

type mobileProbeRouteRelayReportItem struct {
	RouteID        string                                      `json:"route_id"`
	RouteName      string                                      `json:"route_name,omitempty"`
	RouteType      string                                      `json:"route_type,omitempty"`
	Role           string                                      `json:"role,omitempty"`
	ListenHost     string                                      `json:"listen_host,omitempty"`
	ListenPort     int                                         `json:"listen_port,omitempty"`
	RouteLayer     string                                      `json:"route_layer,omitempty"`
	NextHost       string                                      `json:"next_host,omitempty"`
	NextPort       int                                         `json:"next_port,omitempty"`
	NextNodeID     string                                      `json:"next_node_id,omitempty"`
	NextDialMode   string                                      `json:"next_dial_mode,omitempty"`
	PrevHost       string                                      `json:"prev_host,omitempty"`
	PrevPort       int                                         `json:"prev_port,omitempty"`
	PrevNodeID     string                                      `json:"prev_node_id,omitempty"`
	PrevDialMode   string                                      `json:"prev_dial_mode,omitempty"`
	NextState      *mobileProbeRouteRelayProtocolStateSnapshot `json:"next_state,omitempty"`
	VirtualRouter  *mobileProbeVirtualRouterRuntimeStats       `json:"virtual_router,omitempty"`
	BridgeStatus   *mobileProbeRouteBridgeRuntimeStatus        `json:"bridge_status,omitempty"`
	BridgeSessions []mobileProbeRouteBridgeSessionSnapshot     `json:"bridge_sessions,omitempty"`
	UpdatedAt      string                                      `json:"updated_at,omitempty"`
}

type mobileProbeRouteRelayProtocolStateSnapshot struct {
	Endpoint         string `json:"endpoint"`
	SelectedProtocol string `json:"selected_protocol,omitempty"`
	SelectionReason  string `json:"selection_reason,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

type mobileProbeVirtualRouterRuntimeStats struct {
	PacketsForwarded   int64  `json:"packets_forwarded,omitempty"`
	BytesForwarded     int64  `json:"bytes_forwarded,omitempty"`
	PacketsReceived    int64  `json:"packets_received,omitempty"`
	BytesReceived      int64  `json:"bytes_received,omitempty"`
	PacketsDelivered   int64  `json:"packets_delivered,omitempty"`
	BytesDelivered     int64  `json:"bytes_delivered,omitempty"`
	FramesSent         int64  `json:"frames_sent,omitempty"`
	FrameBytesSent     int64  `json:"frame_bytes_sent,omitempty"`
	FramesReceived     int64  `json:"frames_received,omitempty"`
	FrameBytesReceived int64  `json:"frame_bytes_received,omitempty"`
	LinkOpenCount      int64  `json:"link_open_count,omitempty"`
	LastOpenError      string `json:"last_open_error,omitempty"`
	LastOpenAt         string `json:"last_open_at,omitempty"`
	LastPacketAt       string `json:"last_packet_at,omitempty"`
	LastFrameAt        string `json:"last_frame_at,omitempty"`
	TUNDataPlane       bool   `json:"tun_data_plane,omitempty"`
}

type mobileProbeRouteBridgeRuntimeStatus struct {
	DownstreamActive int                                     `json:"downstream_active"`
	UpstreamActive   int                                     `json:"upstream_active"`
	Sessions         []mobileProbeRouteBridgeSessionSnapshot `json:"sessions,omitempty"`
	UpdatedAt        string                                  `json:"updated_at,omitempty"`
}

type mobileProbeRouteBridgeSessionSnapshot struct {
	RouteID             string `json:"route_id,omitempty"`
	RuntimeRole         string `json:"runtime_role,omitempty"`
	Direction           string `json:"direction,omitempty"`
	SessionID           string `json:"session_id,omitempty"`
	BridgeRole          string `json:"bridge_role,omitempty"`
	RemoteAddr          string `json:"remote_addr,omitempty"`
	ConnectedAt         string `json:"connected_at,omitempty"`
	ConnectedMS         int64  `json:"connected_ms,omitempty"`
	FramesSent          int64  `json:"frames_sent,omitempty"`
	FrameBytesSent      int64  `json:"frame_bytes_sent,omitempty"`
	FramesReceived      int64  `json:"frames_received,omitempty"`
	FrameBytesReceived  int64  `json:"frame_bytes_received,omitempty"`
	LastFrameSentAt     string `json:"last_frame_sent_at,omitempty"`
	LastFrameReceivedAt string `json:"last_frame_received_at,omitempty"`
	Closed              bool   `json:"closed,omitempty"`
}

func snapshotMobileVRouteRelayReports(configDir string) []mobileProbeRouteRelayReportItem {
	config, err := loadMobileVRouteConfig(configDir)
	if err != nil || !config.Enabled {
		return nil
	}
	localNode := normalizeMobileRouteNodeID(config.LocalNodeID)
	if localNode == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	carriers := snapshotMobileVRouteCarriersByRouteID()
	out := make([]mobileProbeRouteRelayReportItem, 0, len(config.TopologyRules))
	for _, rule := range config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		fromNode := normalizeMobileRouteNodeID(rule.FromNodeID)
		toNode := normalizeMobileRouteNodeID(rule.ToNodeID)
		if fromNode == "" || toNode == "" || (fromNode != localNode && toNode != localNode) {
			continue
		}
		routeID := mobileVRouteRuntimeRouteID(rule)
		item := mobileProbeRouteRelayReportItem{
			RouteID:    routeID,
			RouteName:  strings.TrimSpace(rule.Name),
			RouteType:  mobileProbeVirtualRouterRuntimeRole,
			Role:       mobileProbeVirtualRouterRuntimeRole,
			RouteLayer: strings.TrimSpace(rule.RouteLayer),
			UpdatedAt:  now,
			VirtualRouter: &mobileProbeVirtualRouterRuntimeStats{
				TUNDataPlane: mobileVRouteVPNRuntimeRunning(),
			},
		}
		if fromNode == localNode {
			item.NextHost = strings.TrimSpace(rule.ToServiceDomain)
			item.NextPort = mobileVRouteServicePortForNode(config, toNode, rule.ToServicePort)
			if mobileVRouteIsCloudflareCopilotDomain(item.NextHost) {
				item.NextPort = 443
			}
			item.NextNodeID = toNode
			if item.NextHost != "" && item.NextPort > 0 {
				item.NextDialMode = "forward"
			}
		} else {
			item.ListenHost = strings.TrimSpace(rule.ToServiceDomain)
			item.ListenPort = mobileVRouteServicePortForNode(config, localNode, rule.ToServicePort)
			item.PrevHost = strings.TrimSpace(rule.FromServiceDomain)
			item.PrevPort = mobileVRouteServicePortForNode(config, fromNode, rule.FromServicePort)
			item.PrevNodeID = fromNode
			item.PrevDialMode = "none"
			item.VirtualRouter.LastOpenError = "mobilecore inbound relay listener is not supported"
			item.VirtualRouter.LastOpenAt = now
		}
		if carrier := carriers[routeID]; carrier != nil {
			applyMobileVRouteCarrierReport(&item, carrier, now)
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.TrimSpace(out[i].RouteID) < strings.TrimSpace(out[j].RouteID)
	})
	return out
}

func snapshotMobileVRouteCarriersByRouteID() map[string]*mobileVRouteCarrier {
	mobileVRouteCarrierState.mu.Lock()
	defer mobileVRouteCarrierState.mu.Unlock()
	out := make(map[string]*mobileVRouteCarrier, len(mobileVRouteCarrierState.items))
	for _, carrier := range mobileVRouteCarrierState.items {
		if carrier == nil {
			continue
		}
		routeID := strings.TrimSpace(carrier.plan.RouteID)
		if routeID != "" {
			out[routeID] = carrier
		}
	}
	return out
}

func applyMobileVRouteCarrierReport(item *mobileProbeRouteRelayReportItem, carrier *mobileVRouteCarrier, now string) {
	if item == nil || carrier == nil {
		return
	}
	txFrames := carrier.txFrames.Load()
	txBytes := carrier.txBytes.Load()
	txIPFrames := carrier.txIPFrames.Load()
	txIPBytes := carrier.txIPBytes.Load()
	rxFrames := carrier.rxFrames.Load()
	rxBytes := carrier.rxBytes.Load()
	rxIPFrames := carrier.rxIPFrames.Load()
	rxIPBytes := carrier.rxIPBytes.Load()
	tunWriteFrames := carrier.tunWriteFrames.Load()
	tunWriteBytes := carrier.tunWriteBytes.Load()
	createdAt := mobileVRouteUnixNanoRFC3339(carrier.createdUnixNS)
	lastActivityAt := mobileVRouteUnixNanoRFC3339(carrier.lastActivityNS.Load())
	carrier.lastErrorMu.Lock()
	lastError := strings.TrimSpace(carrier.lastError)
	lastErrorAt := mobileVRouteUnixNanoRFC3339(carrier.lastErrorUnixNS)
	carrier.lastErrorMu.Unlock()
	if item.NextHost == "" {
		item.NextHost = strings.TrimSpace(carrier.plan.RelayHost)
	}
	if item.NextPort <= 0 {
		item.NextPort = carrier.plan.RelayPort
	}
	if item.NextNodeID == "" {
		item.NextNodeID = normalizeMobileRouteNodeID(carrier.plan.NextNode)
	}
	if item.NextDialMode == "" && item.NextHost != "" && item.NextPort > 0 {
		item.NextDialMode = "forward"
	}
	item.NextState = &mobileProbeRouteRelayProtocolStateSnapshot{
		Endpoint:         net.JoinHostPort(strings.TrimSpace(carrier.plan.RelayHost), strconv.Itoa(carrier.plan.RelayPort)),
		SelectedProtocol: normalizeMobileVRouteRelayLayer(carrier.plan.Layer),
		SelectionReason:  "mobile_vroute_connected",
		UpdatedAt:        firstNonEmptyString(lastActivityAt, now),
	}
	stats := item.VirtualRouter
	if stats == nil {
		stats = &mobileProbeVirtualRouterRuntimeStats{}
		item.VirtualRouter = stats
	}
	stats.PacketsForwarded = txIPFrames
	stats.BytesForwarded = txIPBytes
	stats.PacketsReceived = rxIPFrames
	stats.BytesReceived = rxIPBytes
	stats.PacketsDelivered = tunWriteFrames
	stats.BytesDelivered = tunWriteBytes
	stats.FramesSent = txFrames
	stats.FrameBytesSent = txBytes
	stats.FramesReceived = rxFrames
	stats.FrameBytesReceived = rxBytes
	stats.LinkOpenCount = 1
	stats.LastOpenAt = createdAt
	stats.LastOpenError = lastError
	stats.LastPacketAt = firstNonEmptyString(lastActivityAt, lastErrorAt)
	stats.LastFrameAt = firstNonEmptyString(lastActivityAt, lastErrorAt)
	stats.TUNDataPlane = mobileVRouteVPNRuntimeRunning()
	session := mobileProbeRouteBridgeSessionSnapshot{
		RouteID:             strings.TrimSpace(carrier.plan.RouteID),
		RuntimeRole:         mobileProbeVirtualRouterRuntimeRole,
		Direction:           strings.TrimSpace(carrier.plan.BridgeRole),
		SessionID:           "mobile-vroute-" + strings.TrimSpace(carrier.key),
		BridgeRole:          strings.TrimSpace(carrier.plan.BridgeRole),
		RemoteAddr:          net.JoinHostPort(strings.TrimSpace(carrier.plan.RelayHost), strconv.Itoa(carrier.plan.RelayPort)),
		ConnectedAt:         createdAt,
		ConnectedMS:         mobileVRouteConnectedMS(carrier.createdUnixNS),
		FramesSent:          txFrames,
		FrameBytesSent:      txBytes,
		FramesReceived:      rxFrames,
		FrameBytesReceived:  rxBytes,
		LastFrameSentAt:     lastActivityAt,
		LastFrameReceivedAt: lastActivityAt,
	}
	item.BridgeSessions = []mobileProbeRouteBridgeSessionSnapshot{session}
	item.BridgeStatus = &mobileProbeRouteBridgeRuntimeStatus{
		UpstreamActive: 1,
		Sessions:       item.BridgeSessions,
		UpdatedAt:      firstNonEmptyString(lastActivityAt, now),
	}
}

func mobileVRouteVPNRuntimeRunning() bool {
	vpnRuntime.mu.Lock()
	defer vpnRuntime.mu.Unlock()
	return vpnRuntime.stack != nil
}

func mobileVRouteConnectedMS(createdUnixNS int64) int64 {
	if createdUnixNS <= 0 {
		return 0
	}
	return time.Since(time.Unix(0, createdUnixNS)).Milliseconds()
}
