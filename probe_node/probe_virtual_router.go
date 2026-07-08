package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeVirtualRouterDirectionForward   = "forward"
	probeVirtualRouterDefaultServicePort = 12040
	probeVirtualRouterRecentPacketLimit  = 256

	// vRouter frame header is a stable 12-byte wire protocol boundary:
	// magic 2 bytes, maintype 2 bytes, subtype 2 bytes,
	// control_len 2 bytes, data_len 2 bytes, checksum 2 bytes.
	// 未经用户明确许可，不得修改此帧定义、字段顺序、字段宽度或 checksum 范围。
	probeVirtualRouterFrameEnvelopeMagic                   uint16 = 0x5652
	probeVirtualRouterFrameEnvelopeHeaderSize                     = 12
	probeVirtualRouterFrameMaxControlBytes                        = 8096
	probeVirtualRouterFrameMaxDataBytes                           = 65535
	probeVirtualRouterFrameMaxBytes                               = probeVirtualRouterFrameEnvelopeHeaderSize + probeVirtualRouterFrameMaxControlBytes + probeVirtualRouterFrameMaxDataBytes
	probeVirtualRouterFrameReadBufferBytes                        = 256 * 1024
	probeVirtualRouterFrameMainTypeIP                      uint16 = 1
	probeVirtualRouterFrameMainTypePingPong                uint16 = 2
	probeVirtualRouterFrameMainTypePathRTT                 uint16 = 3
	probeVirtualRouterFrameMainTypeSpeed                   uint16 = 4
	probeVirtualRouterFrameMainTypeRouteTest               uint16 = 5
	probeVirtualRouterFrameSubTypeUnknown                  uint16 = 0
	probeVirtualRouterIPSubTypeIPv4                        uint16 = 1
	probeVirtualRouterPingPongSubTypePing                  uint16 = 1
	probeVirtualRouterPingPongSubTypePong                  uint16 = 2
	probeVirtualRouterPathRTTSubTypeQuery                  uint16 = 1
	probeVirtualRouterPathRTTSubTypeResp                   uint16 = 2
	probeVirtualRouterSpeedSubTypeStart                    uint16 = 1
	probeVirtualRouterSpeedSubTypeChunk                    uint16 = 2
	probeVirtualRouterSpeedSubTypeFinish                   uint16 = 3
	probeVirtualRouterSpeedSubTypeResult                   uint16 = 4
	probeVirtualRouterSpeedSubTypeSend                     uint16 = 5
	probeVirtualRouterRouteTestSubTypeProbe                uint16 = 1
	probeVirtualRouterRouteTestSubTypeReport               uint16 = 2
	probeVirtualRouterFrameLinkIdleTTL                            = 45 * time.Second
	probeVirtualRouterPingPongInterval                            = 30 * time.Second
	probeVirtualRouterPingPongTimeout                             = 5 * time.Second
	probeVirtualRouterFrameWriteTimeout                           = 500 * time.Millisecond
	probeVirtualRouterPingPongBytes                               = 64
	probeVirtualRouterSpeedTestMaxBytes                           = 128 * 1024 * 1024
	probeVirtualRouterSpeedTestMaxDuration                        = 10 * time.Second
	probeVirtualRouterSpeedTestChunkBytes                         = 48 * 1024
	probeVirtualRouterCarrierStalePingFailures                    = 4
	probeVirtualRouterCarrierStaleRXGrace                         = 2 * probeVirtualRouterPingPongInterval
	probeVirtualRouterRouteConfigRefreshHotPathMinInterval        = 60 * time.Second
)

var probeVirtualRouterEnsureDirectBypass = ensureProbeRouteDirectBypass

var probeVirtualRouterState = struct {
	mu                sync.RWMutex
	config            probeVirtualRouterConfig
	localNodeID       string
	localIP           string
	nodeToIP          map[string]string
	ipToNode          map[string]string
	neighbors         map[string]map[string]struct{}
	rulesByID         map[string]probeVirtualRouterTopologyRule
	topologySignature string
}{}

var probeVirtualRouterControllerState = struct {
	mu                sync.RWMutex
	identity          nodeIdentity
	controllerBaseURL string
}{}

var probeVirtualRouterRouteConfigRefreshState = struct {
	mu      sync.Mutex
	running map[string]bool
	lastAt  map[string]time.Time
}{
	running: make(map[string]bool),
	lastAt:  make(map[string]time.Time),
}

type probeVirtualRouterTopologyIndex struct {
	nodeToIP  map[string]string
	ipToNode  map[string]string
	neighbors map[string]map[string]struct{}
	rulesByID map[string]probeVirtualRouterTopologyRule
}

var probeVirtualRouterRouteCacheState = struct {
	mu     sync.RWMutex
	routes map[string][]string
}{routes: make(map[string][]string)}

var probeVirtualRouterPathRTTState = struct {
	mu    sync.RWMutex
	items map[string]probeVirtualRouterPathRTTRecord
}{items: make(map[string]probeVirtualRouterPathRTTRecord)}

var errProbeVirtualRouterAdjacentRTTUnavailable = errors.New("adjacent virtual router ping-pong latency is unavailable")

var probeVirtualRouterRuntimeStatsState = struct {
	mu    sync.Mutex
	items map[string]*probeVirtualRouterRuntimeStats
}{items: make(map[string]*probeVirtualRouterRuntimeStats)}

var probeVirtualRouterRecentPacketState = struct {
	mu     sync.Mutex
	nextID uint64
	items  []probeVirtualRouterRecentPacket
}{}

var probeVirtualRouterICMPPingState = struct {
	mu      sync.Mutex
	pending map[string]probeVirtualRouterICMPPingPending
}{pending: make(map[string]probeVirtualRouterICMPPingPending)}

var probeVirtualRouterControlResponseState = struct {
	mu      sync.Mutex
	pending map[string]chan probeVirtualRouterControlProbePayload
}{pending: make(map[string]chan probeVirtualRouterControlProbePayload)}

var probeVirtualRouterSpeedReceiveState = struct {
	mu       sync.Mutex
	sessions map[string]*probeVirtualRouterSpeedReceiveSession
}{sessions: make(map[string]*probeVirtualRouterSpeedReceiveSession)}

var probeVirtualRouterSpeedResponseState = struct {
	mu      sync.Mutex
	pending map[string]chan probeVirtualRouterSpeedTestResultPayload
}{pending: make(map[string]chan probeVirtualRouterSpeedTestResultPayload)}

var probeVirtualRouterLocalInterfaceRetryState = struct {
	mu         sync.Mutex
	running    bool
	generation uint64
	ensuredIP  string
	ensuredAt  time.Time
}{}

var probeVirtualRouterLocalInterfaceEnsureState = struct {
	mu      sync.Mutex
	running bool
}{}

type probeVirtualRouterRuntimeStats struct {
	PacketsForwarded            int64   `json:"packets_forwarded,omitempty"`
	BytesForwarded              int64   `json:"bytes_forwarded,omitempty"`
	PacketsReceived             int64   `json:"packets_received,omitempty"`
	BytesReceived               int64   `json:"bytes_received,omitempty"`
	PacketsDelivered            int64   `json:"packets_delivered,omitempty"`
	BytesDelivered              int64   `json:"bytes_delivered,omitempty"`
	FramesSent                  int64   `json:"frames_sent,omitempty"`
	FrameBytesSent              int64   `json:"frame_bytes_sent,omitempty"`
	FramesReceived              int64   `json:"frames_received,omitempty"`
	FrameBytesReceived          int64   `json:"frame_bytes_received,omitempty"`
	LinkOpenCount               int64   `json:"link_open_count,omitempty"`
	LastOpenLatencyMS           int64   `json:"last_open_latency_ms,omitempty"`
	LastOpenError               string  `json:"last_open_error,omitempty"`
	LastOpenAt                  string  `json:"last_open_at,omitempty"`
	PingCount                   int64   `json:"ping_count,omitempty"`
	LastPingLatencyMS           int64   `json:"last_ping_latency_ms,omitempty"`
	LastPingError               string  `json:"last_ping_error,omitempty"`
	LastPingAt                  string  `json:"last_ping_at,omitempty"`
	LastPingFailureCount        int     `json:"last_ping_failure_count,omitempty"`
	LastPingDirection           string  `json:"last_ping_direction,omitempty"`
	LastPingBridgeConnections   int     `json:"last_ping_bridge_connections,omitempty"`
	LastPingBridgeSessionID     string  `json:"last_ping_bridge_session_id,omitempty"`
	LastPingBridgeRemote        string  `json:"last_ping_bridge_remote,omitempty"`
	LastPingBridgeConnectedAt   string  `json:"last_ping_bridge_connected_at,omitempty"`
	VirtualPingCount            int64   `json:"virtual_ping_count,omitempty"`
	LastVirtualPingLatencyMS    int64   `json:"last_virtual_ping_latency_ms,omitempty"`
	LastVirtualPingAt           string  `json:"last_virtual_ping_at,omitempty"`
	LastVirtualPingSourceIP     string  `json:"last_virtual_ping_source_ip,omitempty"`
	LastVirtualPingDestIP       string  `json:"last_virtual_ping_dest_ip,omitempty"`
	LastVirtualPingID           uint16  `json:"last_virtual_ping_id,omitempty"`
	LastVirtualPingSequence     uint16  `json:"last_virtual_ping_sequence,omitempty"`
	LastVirtualPingPath         string  `json:"last_virtual_ping_path,omitempty"`
	LastRemoteRTTMS             int64   `json:"last_remote_rtt_ms,omitempty"`
	LastRemoteRTTAt             string  `json:"last_remote_rtt_at,omitempty"`
	LastRemoteRTTError          string  `json:"last_remote_rtt_error,omitempty"`
	LastRemoteRTTResponder      string  `json:"last_remote_rtt_responder,omitempty"`
	LastRemotePongsReceived     int64   `json:"last_remote_pongs_received,omitempty"`
	LastSpeedTestAt             string  `json:"last_speed_test_at,omitempty"`
	LastSpeedTestSourceNodeID   string  `json:"last_speed_test_source_node_id,omitempty"`
	LastSpeedTestTargetNodeID   string  `json:"last_speed_test_target_node_id,omitempty"`
	LastSpeedTestPath           string  `json:"last_speed_test_path,omitempty"`
	LastSpeedTestError          string  `json:"last_speed_test_error,omitempty"`
	LastSpeedTestUpBytes        int64   `json:"last_speed_test_up_bytes,omitempty"`
	LastSpeedTestUpFrames       int64   `json:"last_speed_test_up_frames,omitempty"`
	LastSpeedTestUpDurationMS   int64   `json:"last_speed_test_up_duration_ms,omitempty"`
	LastSpeedTestUpMbps         float64 `json:"last_speed_test_up_mbps,omitempty"`
	LastSpeedTestDownBytes      int64   `json:"last_speed_test_down_bytes,omitempty"`
	LastSpeedTestDownFrames     int64   `json:"last_speed_test_down_frames,omitempty"`
	LastSpeedTestDownDurationMS int64   `json:"last_speed_test_down_duration_ms,omitempty"`
	LastSpeedTestDownMbps       float64 `json:"last_speed_test_down_mbps,omitempty"`
	LastPacketAt                string  `json:"last_packet_at,omitempty"`
	LastFrameAt                 string  `json:"last_frame_at,omitempty"`
	LastFrameSourceIP           string  `json:"last_frame_source_ip,omitempty"`
	LastFrameDestinationIP      string  `json:"last_frame_destination_ip,omitempty"`
	LastFrameLocalIP            string  `json:"last_frame_local_ip,omitempty"`
	LastFrameLocalMatch         string  `json:"last_frame_local_match,omitempty"`
	LastFramePath               string  `json:"last_frame_path,omitempty"`
	LastFrameRuntimeNodeID      string  `json:"last_frame_runtime_node_id,omitempty"`
	LastDeliveryError           string  `json:"last_delivery_error,omitempty"`
	TUNDataPlane                bool    `json:"tun_data_plane,omitempty"`
	TUNRXPackets                uint64  `json:"tun_rx_packets,omitempty"`
	TUNRXBytes                  uint64  `json:"tun_rx_bytes,omitempty"`
	TUNTXPackets                uint64  `json:"tun_tx_packets,omitempty"`
	TUNTXBytes                  uint64  `json:"tun_tx_bytes,omitempty"`
}

type probeVirtualRouterRecentPacket struct {
	ID              uint64   `json:"id"`
	CapturedAt      string   `json:"captured_at"`
	Source          string   `json:"source"`
	Action          string   `json:"action"`
	RouteID         string   `json:"route_id,omitempty"`
	LocalNodeID     string   `json:"local_node_id,omitempty"`
	PeerNodeID      string   `json:"peer_node_id,omitempty"`
	Protocol        string   `json:"protocol,omitempty"`
	SourceIP        string   `json:"source_ip,omitempty"`
	DestinationIP   string   `json:"destination_ip,omitempty"`
	SourcePort      uint16   `json:"source_port,omitempty"`
	DestinationPort uint16   `json:"destination_port,omitempty"`
	TCPFlags        string   `json:"tcp_flags,omitempty"`
	Length          int      `json:"length"`
	Path            []string `json:"path,omitempty"`
	PathText        string   `json:"path_text,omitempty"`
	LocalMatch      bool     `json:"local_match,omitempty"`
	FakeIP          bool     `json:"fake_ip,omitempty"`
	FakeIPSide      string   `json:"fake_ip_side,omitempty"`
	FakeIPDomain    string   `json:"fake_ip_domain,omitempty"`
	FakeIPExitNode  string   `json:"fake_ip_exit_node,omitempty"`
	Detail          string   `json:"detail,omitempty"`
	Error           string   `json:"error,omitempty"`
}

type probeVirtualRouterICMPEchoLogInfo struct {
	Kind          string
	SourceIP      string
	DestinationIP string
	ID            uint16
	Sequence      uint16
}

type probeVirtualRouterICMPPingPending struct {
	StartedAt     time.Time
	RouteID       string
	SourceIP      string
	DestinationIP string
	ID            uint16
	Sequence      uint16
	Path          string
}

type probeVirtualRouterICMPPingCompleteSummary struct {
	SourceIP      string
	DestinationIP string
	ID            uint16
	Sequence      uint16
	Path          string
	LatencyMS     int64
}

type probeVirtualRouterSpeedReceiveSession struct {
	RequestID     string
	Direction     string
	SourceNodeID  string
	TargetNodeID  string
	ResultNodeID  string
	Path          []string
	RouteID       string
	MaxDurationMS int64
	LocalNodeID   string
	TimerStarted  bool
	StartedAt     time.Time
	LastAt        time.Time
	Bytes         int64
	Frames        int64
}

type probeVirtualRouterTransportLogInfo struct {
	Protocol        string
	SourceIP        string
	DestinationIP   string
	SourcePort      uint16
	DestinationPort uint16
	TCPFlags        string
}

// probeVirtualRouterFrame is the vRouter wire frame. Its binary header layout
// is fixed by the constants above; control_len and data_len are 2 bytes.
// 未经用户明确许可不得修改。
type probeVirtualRouterFrame struct {
	MainType uint16
	SubType  uint16
	Control  []byte
	Data     []byte
}

type probeVirtualRouterFrameControlEnvelope struct {
	Path  []string                          `json:"path,omitempty"`
	Trace []probeVirtualRouterFrameTraceHop `json:"trace,omitempty"`
}

type probeVirtualRouterFrameTraceHop struct {
	ID         string `json:"id"`
	NodeID     string `json:"node_id"`
	RouteID    string `json:"route_id,omitempty"`
	Event      string `json:"event"`
	Direction  string `json:"direction,omitempty"`
	RemoteNode string `json:"remote_node,omitempty"`
	UnixNano   int64  `json:"unix_nano"`
}

type probeVirtualRouterPathRTTRecord struct {
	RTTMS      int64
	LastAt     time.Time
	LastError  string
	TargetNode string
	Responder  string
}

type probeVirtualRouterPathRTTQueryRequest struct {
	RequestID string `json:"request_id,omitempty"`
}

type probeVirtualRouterPathRTTQueryResponse struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
	Responder string `json:"responder,omitempty"`
}

type probeVirtualRouterControlProbePayload struct {
	RequestID         string   `json:"request_id"`
	SourceNodeID      string   `json:"source_node_id,omitempty"`
	TargetNodeID      string   `json:"target_node_id,omitempty"`
	Path              []string `json:"path,omitempty"`
	CreatedAtUnixNano int64    `json:"created_at_unix_nano,omitempty"`
	LatencyMS         int64    `json:"latency_ms,omitempty"`
	PingBytes         int      `json:"ping_bytes,omitempty"`
	OK                bool     `json:"ok,omitempty"`
	Error             string   `json:"error,omitempty"`
	Responder         string   `json:"responder,omitempty"`
}

type probeVirtualRouterSpeedTestResultPayload struct {
	RequestID         string   `json:"request_id"`
	Direction         string   `json:"direction,omitempty"`
	SourceNodeID      string   `json:"source_node_id,omitempty"`
	TargetNodeID      string   `json:"target_node_id,omitempty"`
	ResultNodeID      string   `json:"result_node_id,omitempty"`
	Path              []string `json:"path,omitempty"`
	MaxBytes          int64    `json:"max_bytes,omitempty"`
	MaxDurationMS     int64    `json:"max_duration_ms,omitempty"`
	CreatedAtUnixNano int64    `json:"created_at_unix_nano,omitempty"`
	Bytes             int64    `json:"bytes,omitempty"`
	Frames            int64    `json:"frames,omitempty"`
	DurationMS        int64    `json:"duration_ms,omitempty"`
	Mbps              float64  `json:"mbps,omitempty"`
	OK                bool     `json:"ok,omitempty"`
	Error             string   `json:"error,omitempty"`
	Responder         string   `json:"responder,omitempty"`
	RuntimeRouteID    string   `json:"-"`
}

type probeVirtualRouterSpeedTestResult struct {
	OK         bool
	Error      string
	Bytes      int64
	Frames     int64
	DurationMS int64
	Mbps       float64
}

func sanitizeProbeVirtualRouterConfigForCache(input probeVirtualRouterConfig) probeVirtualRouterConfig {
	out := probeVirtualRouterConfig{
		Enabled:       input.Enabled,
		FakeIPCIDR:    strings.TrimSpace(input.FakeIPCIDR),
		ProbeIPs:      sanitizeProbeVirtualRouterProbeIPs(input.ProbeIPs),
		TopologyRules: sanitizeProbeVirtualRouterTopologyRules(input.TopologyRules),
		RouteRules:    sanitizeProbeVirtualRouterRouteRules(input.RouteRules),
		FakeIPLibrary: sanitizeProbeVirtualRouterFakeIPLibrary(input.FakeIPLibrary),
		UpdatedAt:     strings.TrimSpace(input.UpdatedAt),
	}
	return out
}

func sanitizeProbeVirtualRouterFakeIPLibrary(input probeVirtualRouterFakeIPLibrary) probeVirtualRouterFakeIPLibrary {
	version := input.Version
	if version < 0 {
		version = 0
	}
	out := probeVirtualRouterFakeIPLibrary{
		Version:   version,
		UpdatedAt: strings.TrimSpace(input.UpdatedAt),
		Items:     []probeVirtualRouterFakeIPEntry{},
	}
	seenDomain := map[string]struct{}{}
	seenIP := map[string]struct{}{}
	for _, item := range input.Items {
		domain := normalizeProbeVirtualRouterDomain(item.Domain)
		ip := strings.TrimSpace(item.FakeIP)
		if domain == "" || net.ParseIP(ip).To4() == nil {
			continue
		}
		if _, ok := seenDomain[domain]; ok {
			continue
		}
		if _, ok := seenIP[ip]; ok {
			continue
		}
		seenDomain[domain] = struct{}{}
		seenIP[ip] = struct{}{}
		out.Items = append(out.Items, probeVirtualRouterFakeIPEntry{
			Domain:     domain,
			FakeIP:     ip,
			RuleID:     strings.TrimSpace(item.RuleID),
			Action:     sanitizeProbeVirtualRouterRouteRuleAction(item.Action, item.ExitNodeID),
			ExitNodeID: normalizeProbeRouteNodeID(item.ExitNodeID),
			ExpiresAt:  strings.TrimSpace(item.ExpiresAt),
			UpdatedAt:  strings.TrimSpace(item.UpdatedAt),
		})
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		return out.Items[i].Domain < out.Items[j].Domain
	})
	return out
}

func normalizeProbeVirtualRouterDomain(raw string) string {
	domain := strings.ToLower(strings.TrimSpace(strings.Trim(raw, ".")))
	if domain == "" || strings.ContainsAny(domain, " \t\r\n:/") {
		return ""
	}
	return domain
}

func sanitizeProbeVirtualRouterProbeIPs(items []probeVirtualRouterProbeIP) []probeVirtualRouterProbeIP {
	if len(items) == 0 {
		return []probeVirtualRouterProbeIP{}
	}
	out := make([]probeVirtualRouterProbeIP, 0, len(items))
	seenNode := map[string]struct{}{}
	seenIP := map[string]struct{}{}
	for _, item := range items {
		nodeID := normalizeProbeRouteNodeID(item.NodeID)
		ip := net.ParseIP(strings.TrimSpace(item.IP)).To4()
		if nodeID == "" || ip == nil {
			continue
		}
		ipText := ip.String()
		if _, exists := seenNode[nodeID]; exists {
			continue
		}
		if _, exists := seenIP[ipText]; exists {
			continue
		}
		seenNode[nodeID] = struct{}{}
		seenIP[ipText] = struct{}{}
		out = append(out, probeVirtualRouterProbeIP{
			NodeID:      nodeID,
			IP:          ipText,
			ServicePort: normalizeProbeVirtualRouterServicePort(item.ServicePort),
			Note:        strings.TrimSpace(item.Note),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

func sanitizeProbeVirtualRouterTopologyRules(items []probeVirtualRouterTopologyRule) []probeVirtualRouterTopologyRule {
	if len(items) == 0 {
		return []probeVirtualRouterTopologyRule{}
	}
	out := make([]probeVirtualRouterTopologyRule, 0, len(items))
	seen := map[string]struct{}{}
	reserved := collectProbeVirtualRouterReservedRuleIDs(items)
	nextRuleSeq := 1
	for _, item := range items {
		fromNodeID := normalizeProbeRouteNodeID(item.FromNodeID)
		toNodeID := normalizeProbeRouteNodeID(item.ToNodeID)
		direction := normalizeProbeVirtualRouterDirection(item.Direction)
		if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
			continue
		}
		fromServiceDomain := ""
		fromServicePort := 0
		toServiceDomain := strings.TrimSpace(item.ToServiceDomain)
		toServicePort := sanitizeProbeVirtualRouterOptionalServicePort(item.ToServicePort)
		ruleID := strings.TrimSpace(item.ID)
		if ruleID == "" {
			ruleID, nextRuleSeq = allocateProbeVirtualRouterRuleID(seen, reserved, nextRuleSeq)
		}
		key := ruleID
		if key == "" {
			key = fmt.Sprintf("%s|%s|%s", fromNodeID, toNodeID, strings.ToLower(toServiceDomain))
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, probeVirtualRouterTopologyRule{
			ID:                ruleID,
			Name:              strings.TrimSpace(item.Name),
			FromNodeID:        fromNodeID,
			ToNodeID:          toNodeID,
			Direction:         direction,
			FromServiceDomain: fromServiceDomain,
			FromServicePort:   fromServicePort,
			ToServiceDomain:   toServiceDomain,
			ToServicePort:     toServicePort,
			UserID:            strings.TrimSpace(item.UserID),
			UserPublicKey:     strings.TrimSpace(item.UserPublicKey),
			Secret:            strings.TrimSpace(item.Secret),
			AuthTicket:        strings.TrimSpace(item.AuthTicket),
			Enabled:           item.Enabled,
			Note:              strings.TrimSpace(item.Note),
			UpdatedAt:         strings.TrimSpace(item.UpdatedAt),
		})
	}
	return out
}

func sanitizeProbeVirtualRouterRouteRules(items []probeVirtualRouterRouteRule) []probeVirtualRouterRouteRule {
	if len(items) == 0 {
		return []probeVirtualRouterRouteRule{}
	}
	out := make([]probeVirtualRouterRouteRule, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		ruleID := strings.TrimSpace(item.ID)
		key := firstNonEmpty(ruleID, strings.ToLower(name))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entries := make([]string, 0, len(item.Entries))
		entrySeen := map[string]struct{}{}
		for _, raw := range item.Entries {
			entry := strings.TrimSpace(raw)
			if entry == "" {
				continue
			}
			if _, exists := entrySeen[entry]; exists {
				continue
			}
			entrySeen[entry] = struct{}{}
			entries = append(entries, entry)
		}
		sort.Strings(entries)
		action := sanitizeProbeVirtualRouterRouteRuleAction(item.Action, item.ExitNodeID)
		exitNodeID := ""
		if action == "probe_exit" {
			exitNodeID = normalizeProbeRouteNodeID(item.ExitNodeID)
		}
		out = append(out, probeVirtualRouterRouteRule{
			ID:         ruleID,
			Name:       name,
			Action:     action,
			ExitNodeID: exitNodeID,
			Entries:    entries,
			Note:       strings.TrimSpace(item.Note),
			UpdatedAt:  strings.TrimSpace(item.UpdatedAt),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := strings.ToLower(firstNonEmpty(out[i].Name, out[i].ID))
		right := strings.ToLower(firstNonEmpty(out[j].Name, out[j].ID))
		if left != right {
			return left < right
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func sanitizeProbeVirtualRouterRouteRuleAction(raw string, exitNodeID string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "direct":
		if normalizeProbeRouteNodeID(exitNodeID) != "" && value == "" {
			return "probe_exit"
		}
		return "direct"
	case "probe_exit", "exit", "probe":
		return "probe_exit"
	case "reject", "block", "deny":
		return "reject"
	default:
		return "direct"
	}
}

func collectProbeVirtualRouterReservedRuleIDs(items []probeVirtualRouterTopologyRule) map[string]struct{} {
	reserved := map[string]struct{}{}
	for _, item := range items {
		ruleID := strings.TrimSpace(item.ID)
		if ruleID != "" {
			reserved[ruleID] = struct{}{}
		}
	}
	return reserved
}

func allocateProbeVirtualRouterRuleID(seen map[string]struct{}, reserved map[string]struct{}, nextSeq int) (string, int) {
	if nextSeq <= 0 {
		nextSeq = 1
	}
	for {
		ruleID := fmt.Sprintf("vr-%d", nextSeq)
		nextSeq++
		if _, exists := seen[ruleID]; !exists {
			if _, exists := reserved[ruleID]; !exists {
				return ruleID, nextSeq
			}
		}
	}
}

func normalizeProbeVirtualRouterServicePort(port int) int {
	if port <= 0 || port > 65535 {
		return probeVirtualRouterDefaultServicePort
	}
	return port
}

func sanitizeProbeVirtualRouterOptionalServicePort(port int) int {
	if port < 0 || port > 65535 {
		return 0
	}
	return port
}

func normalizeProbeVirtualRouterDirection(raw string) string {
	return probeVirtualRouterDirectionForward
}

func persistProbeRouteConfigCache(config probeVirtualRouterConfig) error {
	cachePath, err := resolveProbeRouteConfigCachePath()
	if err != nil {
		return err
	}
	payload := probeRouteConfigCacheFile{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Item:      sanitizeProbeVirtualRouterConfigForCache(config),
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cachePath, append(encoded, '\n'), 0o644)
}

func loadProbeRouteConfigCache() (probeVirtualRouterConfig, error) {
	cachePath, err := resolveProbeRouteConfigCachePath()
	if err != nil {
		return probeVirtualRouterConfig{}, err
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return probeVirtualRouterConfig{}, nil
		}
		return probeVirtualRouterConfig{}, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return probeVirtualRouterConfig{}, nil
	}
	var payload probeRouteConfigCacheFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return probeVirtualRouterConfig{}, err
	}
	config := sanitizeProbeVirtualRouterConfigForCache(payload.Item)
	rememberProbeVirtualRouterAuthTickets(config)
	return config, nil
}

func resolveProbeRouteConfigCachePath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, probeRouteConfigCacheFileName), nil
}

func applyProbeVirtualRouterConfig(config probeVirtualRouterConfig) {
	applyProbeVirtualRouterConfigForNode(config, "")
}

func applyProbeVirtualRouterConfigForNode(config probeVirtualRouterConfig, nodeID string) {
	sanitized := sanitizeProbeVirtualRouterConfigForCache(config)
	index := buildProbeVirtualRouterTopologyIndex(sanitized)
	probeVirtualRouterState.mu.Lock()
	effectiveNodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	if cleanNodeID := normalizeProbeRouteNodeID(nodeID); cleanNodeID != "" {
		effectiveNodeID = cleanNodeID
	}
	signature := probeVirtualRouterTopologySignature(sanitized, index, effectiveNodeID)
	topologyChanged := probeVirtualRouterState.topologySignature != signature
	probeVirtualRouterState.config = sanitized
	probeVirtualRouterState.localNodeID = effectiveNodeID
	probeVirtualRouterState.nodeToIP = index.nodeToIP
	probeVirtualRouterState.ipToNode = index.ipToNode
	probeVirtualRouterState.neighbors = index.neighbors
	probeVirtualRouterState.rulesByID = index.rulesByID
	probeVirtualRouterState.localIP = index.nodeToIP[effectiveNodeID]
	probeVirtualRouterState.topologySignature = signature
	ensureLocalInterface := sanitized.Enabled && strings.TrimSpace(probeVirtualRouterState.localIP) != ""
	probeVirtualRouterState.mu.Unlock()
	if topologyChanged {
		clearProbeVirtualRouterRouteCache("config updated")
	}
	if ensureLocalInterface {
		scheduleProbeVirtualRouterLocalInterfaceIPEnsure("config_updated")
	} else if err := cleanupProbeVirtualRouterPlatformRoutes(); err != nil {
		log.Printf("warning: cleanup probe virtual router platform routes failed: %v", err)
	}
}

func buildProbeVirtualRouterTopologyIndex(config probeVirtualRouterConfig) probeVirtualRouterTopologyIndex {
	index := probeVirtualRouterTopologyIndex{
		nodeToIP:  make(map[string]string),
		ipToNode:  make(map[string]string),
		neighbors: make(map[string]map[string]struct{}),
		rulesByID: make(map[string]probeVirtualRouterTopologyRule),
	}
	for _, item := range config.ProbeIPs {
		nodeID := normalizeProbeRouteNodeID(item.NodeID)
		ip := net.ParseIP(strings.TrimSpace(item.IP)).To4()
		if nodeID == "" || ip == nil {
			continue
		}
		ipText := ip.String()
		index.nodeToIP[nodeID] = ipText
		index.ipToNode[ipText] = nodeID
	}
	addNeighbor := func(a string, b string) {
		a = normalizeProbeRouteNodeID(a)
		b = normalizeProbeRouteNodeID(b)
		if a == "" || b == "" {
			return
		}
		if index.neighbors[a] == nil {
			index.neighbors[a] = map[string]struct{}{}
		}
		index.neighbors[a][b] = struct{}{}
	}
	for _, rule := range config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		if ruleID := strings.TrimSpace(rule.ID); ruleID != "" {
			index.rulesByID[ruleID] = rule
		}
		addNeighbor(rule.FromNodeID, rule.ToNodeID)
		addNeighbor(rule.ToNodeID, rule.FromNodeID)
	}
	return index
}

func probeVirtualRouterTopologySignature(config probeVirtualRouterConfig, index probeVirtualRouterTopologyIndex, localNodeID string) string {
	var b strings.Builder
	localNodeID = normalizeProbeRouteNodeID(localNodeID)
	fmt.Fprintf(&b, "enabled=%t|cidr=%s|local=%s|local_ip=%s\n", config.Enabled, strings.TrimSpace(config.FakeIPCIDR), localNodeID, strings.TrimSpace(index.nodeToIP[localNodeID]))
	nodeIDs := make([]string, 0, len(index.nodeToIP))
	for nodeID := range index.nodeToIP {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	for _, nodeID := range nodeIDs {
		fmt.Fprintf(&b, "ip|%s|%s|%d\n", nodeID, strings.TrimSpace(index.nodeToIP[nodeID]), probeVirtualRouterServicePortForNode(config, nodeID, probeVirtualRouterDefaultServicePort))
	}
	neighborIDs := make([]string, 0, len(index.neighbors))
	for nodeID := range index.neighbors {
		neighborIDs = append(neighborIDs, nodeID)
	}
	sort.Strings(neighborIDs)
	for _, nodeID := range neighborIDs {
		peers := make([]string, 0, len(index.neighbors[nodeID]))
		for peerID := range index.neighbors[nodeID] {
			peers = append(peers, peerID)
		}
		sort.Strings(peers)
		fmt.Fprintf(&b, "adj|%s|%s\n", nodeID, strings.Join(peers, ","))
	}
	ruleIDs := make([]string, 0, len(index.rulesByID))
	for ruleID := range index.rulesByID {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	for _, ruleID := range ruleIDs {
		rule := index.rulesByID[ruleID]
		fmt.Fprintf(&b, "rule|%s|%s|%s|%s|%s|%d|%s|%d\n",
			strings.TrimSpace(rule.ID),
			normalizeProbeRouteNodeID(rule.FromNodeID),
			normalizeProbeRouteNodeID(rule.ToNodeID),
			normalizeProbeVirtualRouterDirection(rule.Direction),
			strings.TrimSpace(rule.FromServiceDomain),
			sanitizeProbeVirtualRouterOptionalServicePort(rule.FromServicePort),
			strings.TrimSpace(rule.ToServiceDomain),
			sanitizeProbeVirtualRouterOptionalServicePort(rule.ToServicePort),
		)
	}
	for _, rule := range config.RouteRules {
		fmt.Fprintf(&b, "route_rule|%s|%s|%s|%s\n",
			strings.TrimSpace(rule.ID),
			strings.TrimSpace(rule.Name),
			strings.TrimSpace(rule.Action),
			normalizeProbeRouteNodeID(rule.ExitNodeID),
		)
		for _, entry := range rule.Entries {
			fmt.Fprintf(&b, "route_rule_entry|%s|%s\n", strings.TrimSpace(rule.ID), strings.TrimSpace(entry))
		}
	}
	return b.String()
}

func currentProbeVirtualRouterConfig() probeVirtualRouterConfig {
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return sanitizeProbeVirtualRouterConfigForCache(probeVirtualRouterState.config)
}

func probeVirtualRouterServicePortForNode(config probeVirtualRouterConfig, nodeID string, fallback int) int {
	nodeID = normalizeProbeRouteNodeID(nodeID)
	if nodeID != "" {
		for _, item := range config.ProbeIPs {
			if normalizeProbeRouteNodeID(item.NodeID) == nodeID {
				return normalizeProbeVirtualRouterServicePort(item.ServicePort)
			}
		}
	}
	return normalizeProbeVirtualRouterServicePort(fallback)
}

func currentProbeVirtualRouterFakeIPCIDR() string {
	config := currentProbeVirtualRouterConfig()
	if cidr := strings.TrimSpace(config.FakeIPCIDR); cidr != "" {
		return cidr
	}
	return probeLocalFakeIPDefaultCIDR
}

func currentProbeVirtualRouterFakeIPLibrary() probeVirtualRouterFakeIPLibrary {
	config := currentProbeVirtualRouterConfig()
	return sanitizeProbeVirtualRouterFakeIPLibrary(config.FakeIPLibrary)
}

func currentProbeVirtualRouterFakeIPEntryByDomain(domain string) (probeVirtualRouterFakeIPEntry, bool) {
	cleanDomain := normalizeProbeVirtualRouterDomain(domain)
	if cleanDomain == "" {
		return probeVirtualRouterFakeIPEntry{}, false
	}
	now := time.Now().UTC()
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	for _, item := range probeVirtualRouterState.config.FakeIPLibrary.Items {
		if item.Domain != cleanDomain {
			continue
		}
		if probeVirtualRouterFakeIPEntryExpired(item, now) {
			return probeVirtualRouterFakeIPEntry{}, false
		}
		return item, true
	}
	return probeVirtualRouterFakeIPEntry{}, false
}

func currentProbeVirtualRouterRouteRuleForDomain(domain string) (probeVirtualRouterRouteRule, bool) {
	cleanDomain := normalizeProbeVirtualRouterDomain(domain)
	if cleanDomain == "" {
		return probeVirtualRouterRouteRule{}, false
	}
	config := currentProbeVirtualRouterConfig()
	for _, rule := range config.RouteRules {
		action := sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID)
		if action == "" {
			continue
		}
		for _, entry := range rule.Entries {
			if probeVirtualRouterRouteRuleEntryMatchesDomain(cleanDomain, entry) {
				rule.Action = action
				rule.ExitNodeID = normalizeProbeRouteNodeID(rule.ExitNodeID)
				return rule, true
			}
		}
	}
	return probeVirtualRouterRouteRule{}, false
}

func resolveProbeVirtualRouterFakeIPForDNS(domain string, rule probeVirtualRouterRouteRule) (probeVirtualRouterFakeIPEntry, error) {
	cleanDomain := normalizeProbeVirtualRouterDomain(domain)
	if cleanDomain == "" {
		return probeVirtualRouterFakeIPEntry{}, errors.New("virtual router dns domain is empty")
	}
	if item, exists := currentProbeVirtualRouterFakeIPEntryByDomain(cleanDomain); exists {
		return item, nil
	}
	identity, controllerBaseURL, ok := currentProbeVirtualRouterController()
	if ok {
		ctx, cancel := context.WithTimeout(context.Background(), probeRouteConfigSyncFetchTimeout)
		item, library, err := probeRequestRouteFakeIP(ctx, controllerBaseURL, identity, cleanDomain, rule)
		cancel()
		if err == nil && strings.TrimSpace(item.FakeIP) != "" {
			applyProbeVirtualRouterFakeIPLibrary(library)
			return item, nil
		}
		if err != nil {
			logProbeWarnf("probe virtual router fake ip allocate failed: domain=%s exit_node=%s err=%v", cleanDomain, strings.TrimSpace(rule.ExitNodeID), err)
		}
	}
	if item, exists := currentProbeVirtualRouterFakeIPEntryByDomain(cleanDomain); exists {
		return item, nil
	}
	if ok {
		return probeVirtualRouterFakeIPEntry{}, errors.New("virtual router fake ip is unavailable after controller sync")
	}
	return probeVirtualRouterFakeIPEntry{}, errors.New("virtual router controller is unavailable")
}

func probeVirtualRouterRouteRuleEntryMatchesDomain(domain string, entry string) bool {
	key, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
	if !ok {
		return false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	switch key {
	case "domain_suffix":
		return domain == value || strings.HasSuffix(domain, "."+value)
	case "domain_prefix":
		return strings.HasPrefix(domain, value)
	case "domain_keyword":
		return strings.Contains(domain, value)
	default:
		return false
	}
}

func currentProbeVirtualRouterFakeIPEntryByIP(ip string) (probeVirtualRouterFakeIPEntry, bool) {
	target := net.ParseIP(strings.TrimSpace(ip)).To4()
	if target == nil {
		return probeVirtualRouterFakeIPEntry{}, false
	}
	now := time.Now().UTC()
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return probeVirtualRouterFakeIPEntryByIPLocked(target.String(), now)
}

func probeVirtualRouterFakeIPEntryByIPLocked(ip string, now time.Time) (probeVirtualRouterFakeIPEntry, bool) {
	target := net.ParseIP(strings.TrimSpace(ip)).To4()
	if target == nil {
		return probeVirtualRouterFakeIPEntry{}, false
	}
	targetText := target.String()
	for _, item := range probeVirtualRouterState.config.FakeIPLibrary.Items {
		if strings.TrimSpace(item.FakeIP) != targetText {
			continue
		}
		if probeVirtualRouterFakeIPEntryExpired(item, now) {
			return probeVirtualRouterFakeIPEntry{}, false
		}
		return item, true
	}
	return probeVirtualRouterFakeIPEntry{}, false
}

func probeVirtualRouterFakeIPEntryExpired(item probeVirtualRouterFakeIPEntry, now time.Time) bool {
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(item.ExpiresAt))
	return err == nil && !expiresAt.IsZero() && !now.Before(expiresAt)
}

func recordProbeVirtualRouterRecentPacket(source string, action string, runtime *probeVirtualRouterRuntime, packet []byte, path []string, localMatch bool, err error) {
	item := buildProbeVirtualRouterRecentPacket(source, action, runtime, packet, path, localMatch, err)
	if strings.TrimSpace(item.SourceIP) == "" && strings.TrimSpace(item.DestinationIP) == "" {
		return
	}
	probeVirtualRouterRecentPacketState.mu.Lock()
	probeVirtualRouterRecentPacketState.nextID++
	item.ID = probeVirtualRouterRecentPacketState.nextID
	item.CapturedAt = time.Now().UTC().Format(time.RFC3339Nano)
	probeVirtualRouterRecentPacketState.items = append(probeVirtualRouterRecentPacketState.items, item)
	if len(probeVirtualRouterRecentPacketState.items) > probeVirtualRouterRecentPacketLimit {
		excess := len(probeVirtualRouterRecentPacketState.items) - probeVirtualRouterRecentPacketLimit
		copy(probeVirtualRouterRecentPacketState.items, probeVirtualRouterRecentPacketState.items[excess:])
		probeVirtualRouterRecentPacketState.items = probeVirtualRouterRecentPacketState.items[:probeVirtualRouterRecentPacketLimit]
	}
	probeVirtualRouterRecentPacketState.mu.Unlock()
}

func buildProbeVirtualRouterRecentPacket(source string, action string, runtime *probeVirtualRouterRuntime, packet []byte, path []string, localMatch bool, err error) probeVirtualRouterRecentPacket {
	cleanPath := cleanProbeVirtualRouterPath(path)
	item := probeVirtualRouterRecentPacket{
		Source:     strings.TrimSpace(source),
		Action:     strings.TrimSpace(action),
		RouteID:    probeVirtualRouterRuntimeLogRouteID(runtime),
		LocalMatch: localMatch,
		Length:     len(packet),
		Path:       cleanPath,
		PathText:   strings.Join(cleanPath, ">"),
	}
	if runtime != nil {
		item.LocalNodeID = currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
		item.PeerNodeID = normalizeProbeRouteNodeID(runtime.cfg.peerNodeID)
	}
	if item.LocalNodeID == "" {
		item.LocalNodeID = currentProbeVirtualRouterLocalNodeID()
	}
	if err != nil {
		item.Error = strings.TrimSpace(err.Error())
	}
	if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
		item.Protocol = strings.ToUpper(strings.TrimSpace(info.Protocol))
		item.SourceIP = info.SourceIP
		item.DestinationIP = info.DestinationIP
		item.SourcePort = info.SourcePort
		item.DestinationPort = info.DestinationPort
		item.TCPFlags = info.TCPFlags
		item.Detail = probeVirtualRouterPacketChecksumSummary(packet)
	} else if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		item.Protocol = "ICMP"
		item.SourceIP = info.SourceIP
		item.DestinationIP = info.DestinationIP
		item.Detail = fmt.Sprintf("%s id=%d seq=%d", strings.TrimSpace(info.Kind), info.ID, info.Sequence)
	} else {
		item.Protocol = probeVirtualRouterIPv4ProtocolName(packet)
		item.SourceIP = probeVirtualRouterIPv4Source(packet)
		item.DestinationIP = probeVirtualRouterIPv4Destination(packet)
	}
	applyProbeVirtualRouterRecentPacketFakeIP(&item, item.DestinationIP, "dst")
	if !item.FakeIP {
		applyProbeVirtualRouterRecentPacketFakeIP(&item, item.SourceIP, "src")
	}
	return item
}

func applyProbeVirtualRouterRecentPacketFakeIP(item *probeVirtualRouterRecentPacket, ip string, side string) {
	if item == nil {
		return
	}
	entry, ok := currentProbeVirtualRouterFakeIPEntryByIP(ip)
	if !ok {
		return
	}
	item.FakeIP = true
	item.FakeIPSide = strings.TrimSpace(side)
	item.FakeIPDomain = strings.TrimSpace(entry.Domain)
	item.FakeIPExitNode = normalizeProbeRouteNodeID(entry.ExitNodeID)
}

func probeVirtualRouterIPv4ProtocolName(packet []byte) string {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return ""
	}
	switch packet[9] {
	case 1:
		return "ICMP"
	case 6:
		return "TCP"
	case 17:
		return "UDP"
	default:
		return fmt.Sprintf("IP-%d", packet[9])
	}
}

func snapshotProbeVirtualRouterRecentPackets() []probeVirtualRouterRecentPacket {
	probeVirtualRouterRecentPacketState.mu.Lock()
	out := append([]probeVirtualRouterRecentPacket(nil), probeVirtualRouterRecentPacketState.items...)
	probeVirtualRouterRecentPacketState.mu.Unlock()
	for i := 0; i < len(out)/2; i++ {
		j := len(out) - 1 - i
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func applyProbeVirtualRouterFakeIPLibrary(library probeVirtualRouterFakeIPLibrary) {
	nextLibrary := sanitizeProbeVirtualRouterFakeIPLibrary(library)
	probeVirtualRouterState.mu.Lock()
	currentVersion := probeVirtualRouterState.config.FakeIPLibrary.Version
	if nextLibrary.Version >= currentVersion {
		probeVirtualRouterState.config.FakeIPLibrary = nextLibrary
	}
	nextConfig := sanitizeProbeVirtualRouterConfigForCache(probeVirtualRouterState.config)
	probeVirtualRouterState.mu.Unlock()
	if nextLibrary.Version >= currentVersion {
		_ = persistProbeRouteConfigCache(nextConfig)
	}
}

func probeVirtualRouterCloneNodeToIPLocked() map[string]string {
	out := make(map[string]string, len(probeVirtualRouterState.nodeToIP))
	for key, value := range probeVirtualRouterState.nodeToIP {
		out[key] = value
	}
	return out
}

func probeVirtualRouterCloneNeighborsLocked() map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{}, len(probeVirtualRouterState.neighbors))
	for nodeID, peers := range probeVirtualRouterState.neighbors {
		nextPeers := make(map[string]struct{}, len(peers))
		for peerID := range peers {
			nextPeers[peerID] = struct{}{}
		}
		out[nodeID] = nextPeers
	}
	return out
}

func currentProbeVirtualRouterLocalNodeID() string {
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return strings.TrimSpace(probeVirtualRouterState.localNodeID)
}

func currentProbeVirtualRouterLocalNodeIDForRuntime(runtime *probeVirtualRouterRuntime) string {
	if runtime != nil {
		if nodeID := normalizeProbeRouteNodeID(runtime.cfg.identity.NodeID); nodeID != "" {
			return nodeID
		}
	}
	return currentProbeVirtualRouterLocalNodeID()
}

func currentProbeVirtualRouterLocalIP() string {
	probeVirtualRouterState.mu.RLock()
	localIP := strings.TrimSpace(probeVirtualRouterState.localIP)
	nodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	nodeToIP := probeVirtualRouterCloneNodeToIPLocked()
	probeVirtualRouterState.mu.RUnlock()
	if localIP != "" {
		return localIP
	}
	return nodeToIP[nodeID]
}

func currentProbeVirtualRouterLocalIPForRuntime(runtime *probeVirtualRouterRuntime) string {
	nodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if nodeID != "" {
		if ip := currentProbeVirtualRouterIPForNode(nodeID); ip != "" {
			return ip
		}
	}
	return currentProbeVirtualRouterLocalIP()
}

func ensureProbeVirtualRouterLocalInterfaceIP() {
	localIP, err := ensureProbeVirtualRouterLocalInterfaceIPOnce()
	if localIP == "" {
		return
	}
	if err != nil {
		log.Printf("warning: ensure probe virtual router local ip failed: ip=%s err=%v", localIP, err)
		scheduleProbeVirtualRouterLocalInterfaceIPRetry(localIP, err)
		return
	}
	log.Printf("probe virtual router local ip ensured: ip=%s", localIP)
	markProbeVirtualRouterLocalInterfaceEnsured(localIP)
	markProbeLocalTUNInterfaceReady()
}

func scheduleProbeVirtualRouterLocalInterfaceIPEnsure(reason string) {
	probeVirtualRouterLocalInterfaceEnsureState.mu.Lock()
	if probeVirtualRouterLocalInterfaceEnsureState.running {
		probeVirtualRouterLocalInterfaceEnsureState.mu.Unlock()
		return
	}
	probeVirtualRouterLocalInterfaceEnsureState.running = true
	probeVirtualRouterLocalInterfaceEnsureState.mu.Unlock()
	go func() {
		defer func() {
			probeVirtualRouterLocalInterfaceEnsureState.mu.Lock()
			probeVirtualRouterLocalInterfaceEnsureState.running = false
			probeVirtualRouterLocalInterfaceEnsureState.mu.Unlock()
		}()
		if cleanReason := strings.TrimSpace(reason); cleanReason != "" {
			log.Printf("probe virtual router local ip ensure scheduled: reason=%s", cleanReason)
		}
		ensureProbeVirtualRouterLocalInterfaceIP()
	}()
}

func ensureProbeVirtualRouterLocalInterfaceIPOnce() (string, error) {
	localIP := currentProbeVirtualRouterLocalIP()
	if localIP == "" {
		return "", nil
	}
	if err := ensureProbeVirtualRouterPlatformInterfaceIP(localIP); err != nil {
		return localIP, err
	}
	return localIP, nil
}

func scheduleProbeVirtualRouterLocalInterfaceIPRetry(localIP string, cause error) {
	if strings.TrimSpace(localIP) == "" || cause == nil {
		return
	}
	cleanIP := strings.TrimSpace(localIP)
	probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
	if probeVirtualRouterLocalInterfaceRetryState.running {
		probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
		return
	}
	probeVirtualRouterLocalInterfaceRetryState.running = true
	probeVirtualRouterLocalInterfaceRetryState.generation++
	retryGeneration := probeVirtualRouterLocalInterfaceRetryState.generation
	probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()

	go func() {
		defer func() {
			probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
			probeVirtualRouterLocalInterfaceRetryState.running = false
			probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
		}()
		delays := []time.Duration{
			1 * time.Second,
			2 * time.Second,
			5 * time.Second,
			10 * time.Second,
			20 * time.Second,
			30 * time.Second,
		}
		log.Printf("probe virtual router local ip retry scheduled: ip=%s reason=%v", cleanIP, cause)
		for attempt, delay := range delays {
			time.Sleep(delay)
			if probeVirtualRouterLocalInterfaceRetryObsolete(cleanIP, retryGeneration) {
				log.Printf("probe virtual router local ip retry stopped: ip=%s reason=already_ensured attempt=%d", cleanIP, attempt+1)
				return
			}
			nextIP, err := ensureProbeVirtualRouterLocalInterfaceIPOnce()
			if nextIP == "" {
				log.Printf("probe virtual router local ip retry stopped: reason=local_ip_empty attempt=%d", attempt+1)
				return
			}
			if err != nil {
				log.Printf("warning: probe virtual router local ip retry failed: ip=%s attempt=%d err=%v", nextIP, attempt+1, err)
				continue
			}
			if probeVirtualRouterLocalInterfaceRetryObsolete(cleanIP, retryGeneration) {
				log.Printf("probe virtual router local ip retry stopped: ip=%s reason=already_ensured attempt=%d", cleanIP, attempt+1)
				return
			}
			log.Printf("probe virtual router local ip retry succeeded: ip=%s attempt=%d", nextIP, attempt+1)
			markProbeVirtualRouterLocalInterfaceEnsured(nextIP)
			markProbeLocalTUNInterfaceReady()
			return
		}
		log.Printf("warning: probe virtual router local ip retry exhausted: ip=%s attempts=%d", cleanIP, len(delays))
	}()
}

func markProbeVirtualRouterLocalInterfaceEnsured(localIP string) {
	cleanIP := strings.TrimSpace(localIP)
	if cleanIP == "" {
		return
	}
	probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
	probeVirtualRouterLocalInterfaceRetryState.ensuredIP = cleanIP
	probeVirtualRouterLocalInterfaceRetryState.ensuredAt = time.Now()
	probeVirtualRouterLocalInterfaceRetryState.generation++
	probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
}

func probeVirtualRouterLocalInterfaceRetryObsolete(localIP string, generation uint64) bool {
	cleanIP := strings.TrimSpace(localIP)
	if cleanIP == "" {
		return true
	}
	probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
	defer probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
	if probeVirtualRouterLocalInterfaceRetryState.generation != generation {
		return true
	}
	return probeVirtualRouterLocalInterfaceRetryState.ensuredIP == cleanIP && !probeVirtualRouterLocalInterfaceRetryState.ensuredAt.IsZero()
}

func probeVirtualRouterIPForNode(config probeVirtualRouterConfig, nodeID string) string {
	target := normalizeProbeRouteNodeID(nodeID)
	if target == "" {
		return ""
	}
	for _, item := range config.ProbeIPs {
		if normalizeProbeRouteNodeID(item.NodeID) == target {
			return strings.TrimSpace(item.IP)
		}
	}
	return ""
}

func probeVirtualRouterReachable(config probeVirtualRouterConfig, fromNodeID string, toNodeID string) bool {
	return len(probeVirtualRouterPath(config, fromNodeID, toNodeID)) > 0
}

func probeVirtualRouterPath(config probeVirtualRouterConfig, fromNodeID string, toNodeID string) []string {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if !config.Enabled || from == "" || to == "" {
		return nil
	}
	if from == to {
		return []string{from}
	}
	graph := map[string]map[string]struct{}{}
	addEdge := func(a string, b string) {
		a = normalizeProbeRouteNodeID(a)
		b = normalizeProbeRouteNodeID(b)
		if a == "" || b == "" {
			return
		}
		if graph[a] == nil {
			graph[a] = map[string]struct{}{}
		}
		graph[a][b] = struct{}{}
	}
	for _, rule := range config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		addEdge(rule.FromNodeID, rule.ToNodeID)
		addEdge(rule.ToNodeID, rule.FromNodeID)
	}
	return selectProbeVirtualRouterBestPath(probeVirtualRouterShortestPathsFromNeighbors(graph, from, to), false)
}

func probeVirtualRouterPathFromNeighbors(neighbors map[string]map[string]struct{}, fromNodeID string, toNodeID string) []string {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if from == "" || to == "" {
		return nil
	}
	if from == to {
		return []string{from}
	}
	return selectProbeVirtualRouterBestPath(probeVirtualRouterShortestPathsFromNeighbors(neighbors, from, to), true)
}

func probeVirtualRouterShortestPathsFromNeighbors(neighbors map[string]map[string]struct{}, from string, to string) [][]string {
	if from == "" || to == "" {
		return nil
	}
	if from == to {
		return [][]string{{from}}
	}
	distance := map[string]int{from: 0}
	parents := map[string][]string{}
	queue := []string{from}
	foundDistance := -1
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		currentDistance := distance[current]
		if foundDistance >= 0 && currentDistance >= foundDistance {
			continue
		}
		for _, next := range sortedProbeVirtualRouterNeighborIDs(neighbors, current) {
			nextDistance := currentDistance + 1
			if foundDistance >= 0 && nextDistance > foundDistance {
				continue
			}
			oldDistance, seen := distance[next]
			if !seen {
				distance[next] = nextDistance
				parents[next] = []string{current}
				if next == to {
					foundDistance = nextDistance
				} else {
					queue = append(queue, next)
				}
				continue
			}
			if oldDistance == nextDistance {
				parents[next] = append(parents[next], current)
			}
		}
	}
	if foundDistance < 0 {
		return nil
	}
	return buildProbeVirtualRouterShortestPaths(parents, from, to)
}

func sortedProbeVirtualRouterNeighborIDs(neighbors map[string]map[string]struct{}, nodeID string) []string {
	items := make([]string, 0, len(neighbors[nodeID]))
	for item := range neighbors[nodeID] {
		if clean := normalizeProbeRouteNodeID(item); clean != "" {
			items = append(items, clean)
		}
	}
	sort.Strings(items)
	return items
}

func buildProbeVirtualRouterPath(parent map[string]string, from string, to string) []string {
	if from == "" || to == "" {
		return nil
	}
	path := []string{to}
	for current := to; current != from; {
		prev := parent[current]
		if prev == "" {
			return nil
		}
		path = append(path, prev)
		current = prev
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

func buildProbeVirtualRouterShortestPaths(parents map[string][]string, from string, to string) [][]string {
	var out [][]string
	var walk func(current string, suffix []string)
	walk = func(current string, suffix []string) {
		if current == "" {
			return
		}
		nextSuffix := append([]string{current}, suffix...)
		if current == from {
			out = append(out, nextSuffix)
			return
		}
		parentItems := append([]string(nil), parents[current]...)
		sort.Strings(parentItems)
		for _, parent := range parentItems {
			walk(parent, nextSuffix)
		}
	}
	walk(to, nil)
	sort.SliceStable(out, func(i, j int) bool {
		return compareProbeVirtualRouterPathLexicographic(out[i], out[j]) < 0
	})
	return out
}

func selectProbeVirtualRouterBestPath(paths [][]string, useRTT bool) []string {
	if len(paths) == 0 {
		return nil
	}
	best := append([]string(nil), paths[0]...)
	for _, path := range paths[1:] {
		if probeVirtualRouterPathLess(path, best, useRTT) {
			best = append([]string(nil), path...)
		}
	}
	return best
}

func probeVirtualRouterPathLess(left []string, right []string, useRTT bool) bool {
	if len(left) != len(right) {
		return len(left) < len(right)
	}
	if useRTT {
		leftRTT, leftMissing := probeVirtualRouterPathRTTScore(left)
		rightRTT, rightMissing := probeVirtualRouterPathRTTScore(right)
		if leftMissing != rightMissing {
			return leftMissing < rightMissing
		}
		if leftRTT != rightRTT {
			return leftRTT < rightRTT
		}
	}
	return compareProbeVirtualRouterPathLexicographic(left, right) < 0
}

func probeVirtualRouterPathRTTScore(path []string) (int64, int) {
	if latencyMS, ok := currentProbeVirtualRouterPathLatencyMS(path); ok {
		return latencyMS, 0
	}
	var total int64
	missing := 0
	for i := 0; i+1 < len(path); i++ {
		latencyMS, ok := currentProbeVirtualRouterAdjacentLatencyMS(path[i], path[i+1])
		if !ok {
			missing++
			continue
		}
		total += latencyMS
	}
	return total, missing
}

func currentProbeVirtualRouterPathLatencyMS(path []string) (int64, bool) {
	key := probeVirtualRouterPathKey(path)
	if key == "" {
		return 0, false
	}
	probeVirtualRouterPathRTTState.mu.RLock()
	item, ok := probeVirtualRouterPathRTTState.items[key]
	probeVirtualRouterPathRTTState.mu.RUnlock()
	if !ok || item.RTTMS <= 0 || strings.TrimSpace(item.LastError) != "" {
		return 0, false
	}
	return item.RTTMS, true
}

func recordProbeVirtualRouterPathRTTSuccess(path []string, latency time.Duration, responder string) {
	key := probeVirtualRouterPathKey(path)
	if key == "" {
		return
	}
	nextRTTMS := probeDurationMilliseconds(latency)
	target := ""
	if len(path) > 0 {
		target = normalizeProbeRouteNodeID(path[len(path)-1])
	}
	shouldClearRouteCache := false
	probeVirtualRouterPathRTTState.mu.Lock()
	if probeVirtualRouterPathRTTState.items == nil {
		probeVirtualRouterPathRTTState.items = make(map[string]probeVirtualRouterPathRTTRecord)
	}
	previous := probeVirtualRouterPathRTTState.items[key]
	shouldClearRouteCache = previous.RTTMS != nextRTTMS || strings.TrimSpace(previous.LastError) != ""
	probeVirtualRouterPathRTTState.items[key] = probeVirtualRouterPathRTTRecord{
		RTTMS:      nextRTTMS,
		LastAt:     time.Now().UTC(),
		TargetNode: target,
		Responder:  strings.TrimSpace(responder),
	}
	probeVirtualRouterPathRTTState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCache("path rtt query success")
	}
}

func recordProbeVirtualRouterPathRTTError(path []string, err error) {
	if err == nil {
		return
	}
	key := probeVirtualRouterPathKey(path)
	if key == "" {
		return
	}
	target := ""
	if len(path) > 0 {
		target = normalizeProbeRouteNodeID(path[len(path)-1])
	}
	nextError := strings.TrimSpace(err.Error())
	shouldClearRouteCache := false
	probeVirtualRouterPathRTTState.mu.Lock()
	if probeVirtualRouterPathRTTState.items == nil {
		probeVirtualRouterPathRTTState.items = make(map[string]probeVirtualRouterPathRTTRecord)
	}
	item := probeVirtualRouterPathRTTState.items[key]
	shouldClearRouteCache = strings.TrimSpace(item.LastError) != nextError
	item.LastAt = time.Now().UTC()
	item.LastError = nextError
	item.TargetNode = target
	probeVirtualRouterPathRTTState.items[key] = item
	probeVirtualRouterPathRTTState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCache("path rtt query error")
	}
}

func probeVirtualRouterPathKey(path []string) string {
	clean := make([]string, 0, len(path))
	for _, item := range path {
		if nodeID := normalizeProbeRouteNodeID(item); nodeID != "" {
			clean = append(clean, nodeID)
		}
	}
	if len(clean) < 2 {
		return ""
	}
	return strings.Join(clean, ">")
}

func currentProbeVirtualRouterAdjacentLatencyMS(fromNodeID string, toNodeID string) (int64, bool) {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if localNodeID == "" || from == "" || to == "" {
		return 0, false
	}
	target := ""
	switch localNodeID {
	case from:
		target = to
	case to:
		target = from
	default:
		return 0, false
	}
	rt, _ := probeVirtualRouterRuntimeForAdjacentNode(target)
	if rt == nil {
		return 0, false
	}
	stats := snapshotProbeVirtualRouterRuntimeStats(rt.cfg.routeID)
	if stats == nil {
		return 0, false
	}
	if stats.LastPingLatencyMS > 0 && strings.TrimSpace(stats.LastPingError) == "" {
		return stats.LastPingLatencyMS, true
	}
	if stats.LastRemoteRTTMS > 0 && strings.TrimSpace(stats.LastRemoteRTTError) == "" {
		return stats.LastRemoteRTTMS, true
	}
	return 0, false
}

func compareProbeVirtualRouterPathLexicographic(left []string, right []string) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for i := 0; i < limit; i++ {
		l := normalizeProbeRouteNodeID(left[i])
		r := normalizeProbeRouteNodeID(right[i])
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

func probeVirtualRouterNodeIDForIP(config probeVirtualRouterConfig, ip string) string {
	target := net.ParseIP(strings.TrimSpace(ip)).To4()
	if target == nil {
		return ""
	}
	targetText := target.String()
	for _, item := range config.ProbeIPs {
		itemIP := net.ParseIP(strings.TrimSpace(item.IP)).To4()
		if itemIP != nil && itemIP.String() == targetText {
			return normalizeProbeRouteNodeID(item.NodeID)
		}
	}
	return ""
}

func currentProbeVirtualRouterIPForNode(nodeID string) string {
	target := normalizeProbeRouteNodeID(nodeID)
	if target == "" {
		return ""
	}
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return probeVirtualRouterState.nodeToIP[target]
}

func currentProbeVirtualRouterNodeIDForIP(ip string) string {
	target := net.ParseIP(strings.TrimSpace(ip)).To4()
	if target == nil {
		return ""
	}
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return probeVirtualRouterState.ipToNode[target.String()]
}

func currentProbeVirtualRouterPathBetweenNodes(fromNodeID string, toNodeID string) []string {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if from == "" || to == "" {
		return nil
	}
	if from == to {
		return []string{from}
	}
	if path := cachedProbeVirtualRouterRoutePath(from, to); len(path) > 0 {
		return path
	}
	probeVirtualRouterState.mu.RLock()
	neighbors := probeVirtualRouterCloneNeighborsLocked()
	probeVirtualRouterState.mu.RUnlock()
	path := probeVirtualRouterPathFromNeighbors(neighbors, from, to)
	if len(path) > 0 {
		storeProbeVirtualRouterRoutePath(from, to, path)
	}
	return path
}

func probeVirtualRouterRouteCacheKey(fromNodeID string, toNodeID string) string {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if from == "" || to == "" {
		return ""
	}
	return from + ">" + to
}

func cachedProbeVirtualRouterRoutePath(fromNodeID string, toNodeID string) []string {
	key := probeVirtualRouterRouteCacheKey(fromNodeID, toNodeID)
	if key == "" {
		return nil
	}
	probeVirtualRouterRouteCacheState.mu.RLock()
	path := append([]string(nil), probeVirtualRouterRouteCacheState.routes[key]...)
	probeVirtualRouterRouteCacheState.mu.RUnlock()
	return path
}

func storeProbeVirtualRouterRoutePath(fromNodeID string, toNodeID string, path []string) {
	key := probeVirtualRouterRouteCacheKey(fromNodeID, toNodeID)
	if key == "" || len(path) == 0 {
		return
	}
	probeVirtualRouterRouteCacheState.mu.Lock()
	if probeVirtualRouterRouteCacheState.routes == nil {
		probeVirtualRouterRouteCacheState.routes = make(map[string][]string)
	}
	probeVirtualRouterRouteCacheState.routes[key] = append([]string(nil), path...)
	probeVirtualRouterRouteCacheState.mu.Unlock()
}

func clearProbeVirtualRouterRouteCache(reason string) {
	probeVirtualRouterRouteCacheState.mu.Lock()
	if len(probeVirtualRouterRouteCacheState.routes) == 0 {
		probeVirtualRouterRouteCacheState.mu.Unlock()
		return
	}
	probeVirtualRouterRouteCacheState.routes = make(map[string][]string)
	probeVirtualRouterRouteCacheState.mu.Unlock()
	if strings.TrimSpace(reason) != "" {
		log.Printf("probe virtual router route cache cleared: reason=%s", strings.TrimSpace(reason))
	}
}

func clearProbeVirtualRouterRouteCacheForRuntime(rt *probeVirtualRouterRuntime, reason string) {
	if rt == nil {
		clearProbeVirtualRouterRouteCache(reason)
		return
	}
	fromNodeID := normalizeProbeRouteNodeID(rt.cfg.fromNodeID)
	toNodeID := normalizeProbeRouteNodeID(rt.cfg.toNodeID)
	if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
		clearProbeVirtualRouterRouteCache(reason)
		return
	}
	clearProbeVirtualRouterRouteCacheForEdge(fromNodeID, toNodeID, reason)
}

func clearProbeVirtualRouterRouteCacheForEdge(fromNodeID string, toNodeID string, reason string) {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if from == "" || to == "" || from == to {
		clearProbeVirtualRouterRouteCache(reason)
		return
	}
	removed := 0
	probeVirtualRouterRouteCacheState.mu.Lock()
	for key, path := range probeVirtualRouterRouteCacheState.routes {
		if probeVirtualRouterPathContainsAdjacentEdge(path, from, to) {
			delete(probeVirtualRouterRouteCacheState.routes, key)
			removed++
		}
	}
	probeVirtualRouterRouteCacheState.mu.Unlock()
	if removed > 0 && strings.TrimSpace(reason) != "" {
		log.Printf("probe virtual router route cache entries cleared: reason=%s edge=%s>%s count=%d", strings.TrimSpace(reason), from, to, removed)
	}
}

func probeVirtualRouterPathContainsAdjacentEdge(path []string, fromNodeID string, toNodeID string) bool {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if from == "" || to == "" || from == to || len(path) < 2 {
		return false
	}
	for index := 0; index+1 < len(path); index++ {
		left := normalizeProbeRouteNodeID(path[index])
		right := normalizeProbeRouteNodeID(path[index+1])
		if (left == from && right == to) || (left == to && right == from) {
			return true
		}
	}
	return false
}

func currentProbeVirtualRouterPathToIP(ip string) []string {
	targetIP := net.ParseIP(strings.TrimSpace(ip)).To4()
	if targetIP == nil {
		return nil
	}
	probeVirtualRouterState.mu.RLock()
	nodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	targetNodeID := probeVirtualRouterState.ipToNode[targetIP.String()]
	if targetNodeID == "" {
		if entry, ok := probeVirtualRouterFakeIPEntryByIPLocked(targetIP.String(), time.Now().UTC()); ok {
			targetNodeID = normalizeProbeRouteNodeID(entry.ExitNodeID)
		}
	}
	probeVirtualRouterState.mu.RUnlock()
	return currentProbeVirtualRouterPathBetweenNodes(nodeID, targetNodeID)
}

func currentProbeVirtualRouterPathForPacket(packet []byte, dstIP string) []string {
	sourceIP := net.ParseIP(probeVirtualRouterIPv4Source(packet)).To4()
	targetIP := net.ParseIP(strings.TrimSpace(dstIP)).To4()
	if sourceIP == nil || targetIP == nil {
		return nil
	}
	probeVirtualRouterState.mu.RLock()
	nodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	sourceNodeID := probeVirtualRouterState.ipToNode[sourceIP.String()]
	targetNodeID := probeVirtualRouterState.ipToNode[targetIP.String()]
	if targetNodeID == "" {
		if entry, ok := probeVirtualRouterFakeIPEntryByIPLocked(targetIP.String(), time.Now().UTC()); ok {
			targetNodeID = normalizeProbeRouteNodeID(entry.ExitNodeID)
		}
	}
	probeVirtualRouterState.mu.RUnlock()
	if nodeID == "" {
		nodeID = sourceNodeID
	}
	return currentProbeVirtualRouterPathBetweenNodes(nodeID, targetNodeID)
}

func probeVirtualRouterPacketTargetsLocalIP(runtime *probeVirtualRouterRuntime, dstIP string) bool {
	return probeVirtualRouterIPMatches(dstIP, currentProbeVirtualRouterLocalIPForRuntime(runtime))
}

func probeVirtualRouterPacketTargetsLocalDelivery(runtime *probeVirtualRouterRuntime, dstIP string, path []string) bool {
	if probeVirtualRouterPacketTargetsLocalIP(runtime, dstIP) {
		return true
	}
	entry, ok := currentProbeVirtualRouterFakeIPEntryByIP(dstIP)
	if !ok {
		return false
	}
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if normalizeProbeRouteNodeID(entry.ExitNodeID) != localNodeID || localNodeID == "" {
		return false
	}
	return len(path) == 0 || normalizeProbeRouteNodeID(path[len(path)-1]) == localNodeID
}

func probeVirtualRouterIPMatches(target string, local string) bool {
	localIP := net.ParseIP(strings.TrimSpace(local)).To4()
	targetIP := net.ParseIP(strings.TrimSpace(target)).To4()
	if localIP == nil || targetIP == nil {
		return false
	}
	return targetIP.Equal(localIP)
}

func marshalProbeVirtualRouterFrameEnvelope(frame probeVirtualRouterFrame) ([]byte, error) {
	return encodeProbeVirtualRouterFrame(frame)
}

func buildProbeVirtualRouterBusinessFrame(mainType uint16, subType uint16, payload []byte, path []string, trace []probeVirtualRouterFrameTraceHop) (probeVirtualRouterFrame, error) {
	controlPayload, err := marshalProbeVirtualRouterFrameControl(path, trace)
	if err != nil {
		return probeVirtualRouterFrame{}, err
	}
	return probeVirtualRouterFrame{
		MainType: mainType,
		SubType:  subType,
		Control:  controlPayload,
		Data:     append([]byte(nil), payload...),
	}, nil
}

func buildProbeVirtualRouterIPFrame(packet []byte, path []string, trace []probeVirtualRouterFrameTraceHop) (probeVirtualRouterFrame, error) {
	if len(packet) == 0 {
		return probeVirtualRouterFrame{}, errors.New("virtual router ip frame payload is empty")
	}
	return buildProbeVirtualRouterBusinessFrame(probeVirtualRouterFrameMainTypeIP, probeVirtualRouterIPSubTypeIPv4, packet, path, trace)
}

func marshalProbeVirtualRouterFrameControl(path []string, trace []probeVirtualRouterFrameTraceHop) ([]byte, error) {
	cleanPath := cleanProbeVirtualRouterPath(path)
	cleanTrace := make([]probeVirtualRouterFrameTraceHop, 0, len(trace))
	for _, hop := range trace {
		clean := probeVirtualRouterCleanFrameTraceHop(hop)
		if clean.ID == "" || clean.NodeID == "" || clean.Event == "" || clean.UnixNano <= 0 {
			continue
		}
		cleanTrace = append(cleanTrace, clean)
	}
	payload, err := json.Marshal(probeVirtualRouterFrameControlEnvelope{
		Path:  cleanPath,
		Trace: cleanTrace,
	})
	if err != nil {
		return nil, err
	}
	if len(payload) > probeVirtualRouterFrameMaxControlBytes {
		return nil, fmt.Errorf("virtual router frame control is too large: %d", len(payload))
	}
	return payload, nil
}

func encodeProbeVirtualRouterFrame(frame probeVirtualRouterFrame) ([]byte, error) {
	controlLen := len(frame.Control)
	dataLen := len(frame.Data)
	if controlLen > probeVirtualRouterFrameMaxControlBytes {
		return nil, fmt.Errorf("virtual router frame control is too large: %d", controlLen)
	}
	if dataLen > probeVirtualRouterFrameMaxDataBytes {
		return nil, fmt.Errorf("virtual router frame data is too large: %d", dataLen)
	}
	frameLen := probeVirtualRouterFrameEnvelopeHeaderSize + controlLen + dataLen

	out := make([]byte, frameLen)
	binary.BigEndian.PutUint16(out[0:2], probeVirtualRouterFrameEnvelopeMagic)
	binary.BigEndian.PutUint16(out[2:4], frame.MainType)
	binary.BigEndian.PutUint16(out[4:6], frame.SubType)
	binary.BigEndian.PutUint16(out[6:8], uint16(controlLen))
	binary.BigEndian.PutUint16(out[8:10], uint16(dataLen))
	offset := probeVirtualRouterFrameEnvelopeHeaderSize
	copy(out[offset:offset+controlLen], frame.Control)
	copy(out[offset+controlLen:], frame.Data)
	checksum := probeVirtualRouterFrameChecksum(out[:10], frame.Control, frame.Data)
	binary.BigEndian.PutUint16(out[10:12], checksum)
	return out, nil
}

func unmarshalProbeVirtualRouterFrameEnvelope(payload []byte) (probeVirtualRouterFrame, error) {
	return decodeProbeVirtualRouterFrame(payload)
}

func decodeProbeVirtualRouterFrame(payload []byte) (probeVirtualRouterFrame, error) {
	if len(payload) < probeVirtualRouterFrameEnvelopeHeaderSize {
		return probeVirtualRouterFrame{}, errors.New("invalid virtual router frame envelope")
	}
	frame, controlLen, dataLen, payloadLen, err := decodeProbeVirtualRouterFrameHeader(payload[:probeVirtualRouterFrameEnvelopeHeaderSize])
	if err != nil {
		return probeVirtualRouterFrame{}, err
	}
	if len(payload) != probeVirtualRouterFrameEnvelopeHeaderSize+payloadLen {
		return probeVirtualRouterFrame{}, errors.New("invalid virtual router frame envelope")
	}
	controlPayload := payload[probeVirtualRouterFrameEnvelopeHeaderSize : probeVirtualRouterFrameEnvelopeHeaderSize+controlLen]
	dataPayload := payload[probeVirtualRouterFrameEnvelopeHeaderSize+controlLen:]
	if err := verifyProbeVirtualRouterFrameChecksum(payload[:probeVirtualRouterFrameEnvelopeHeaderSize], controlPayload, dataPayload); err != nil {
		return probeVirtualRouterFrame{}, err
	}
	frame.Control = controlPayload
	frame.Data = dataPayload
	if len(dataPayload) != dataLen {
		return probeVirtualRouterFrame{}, errors.New("invalid virtual router frame envelope")
	}
	return frame, nil
}

func decodeProbeVirtualRouterFrameHeader(header []byte) (probeVirtualRouterFrame, int, int, int, error) {
	if len(header) != probeVirtualRouterFrameEnvelopeHeaderSize {
		return probeVirtualRouterFrame{}, 0, 0, 0, errors.New("invalid virtual router frame header")
	}
	if binary.BigEndian.Uint16(header[0:2]) != probeVirtualRouterFrameEnvelopeMagic {
		return probeVirtualRouterFrame{}, 0, 0, 0, errors.New("invalid virtual router frame magic")
	}
	controlLen := int(binary.BigEndian.Uint16(header[6:8]))
	dataLen := int(binary.BigEndian.Uint16(header[8:10]))
	if controlLen > probeVirtualRouterFrameMaxControlBytes {
		return probeVirtualRouterFrame{}, 0, 0, 0, fmt.Errorf("virtual router frame control is too large: %d", controlLen)
	}
	if dataLen > probeVirtualRouterFrameMaxDataBytes {
		return probeVirtualRouterFrame{}, 0, 0, 0, fmt.Errorf("virtual router frame data is too large: %d", dataLen)
	}
	if probeVirtualRouterFrameEnvelopeHeaderSize+controlLen+dataLen > probeVirtualRouterFrameMaxBytes {
		return probeVirtualRouterFrame{}, 0, 0, 0, errors.New("invalid virtual router frame envelope")
	}
	return probeVirtualRouterFrame{
		MainType: binary.BigEndian.Uint16(header[2:4]),
		SubType:  binary.BigEndian.Uint16(header[4:6]),
	}, controlLen, dataLen, controlLen + dataLen, nil
}

func verifyProbeVirtualRouterFrameChecksum(header []byte, controlPayload []byte, dataPayload []byte) error {
	if len(header) != probeVirtualRouterFrameEnvelopeHeaderSize {
		return errors.New("invalid virtual router frame header")
	}
	checksum := binary.BigEndian.Uint16(header[10:12])
	expected := probeVirtualRouterFrameChecksum(header[:10], controlPayload, dataPayload)
	if checksum != expected {
		return errors.New("virtual router frame checksum mismatch")
	}
	return nil
}

func probeVirtualRouterFrameChecksum(headerPrefix []byte, controlPayload []byte, dataPayload []byte) uint16 {
	var sum uint32
	var pending byte
	hasPending := false
	add := func(payload []byte) {
		for _, item := range payload {
			if hasPending {
				sum += uint32(pending)<<8 | uint32(item)
				sum = (sum & 0xffff) + (sum >> 16)
				hasPending = false
				continue
			}
			pending = item
			hasPending = true
		}
	}
	add(headerPrefix)
	add(controlPayload)
	add(dataPayload)
	if hasPending {
		sum += uint32(pending) << 8
	}
	for sum > 0xffff {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func probeVirtualRouterFrameControl(frame probeVirtualRouterFrame, fallbackPath []string) (probeVirtualRouterFrameControlEnvelope, error) {
	control := probeVirtualRouterFrameControlEnvelope{}
	if len(frame.Control) > 0 {
		if err := json.Unmarshal(frame.Control, &control); err != nil {
			return probeVirtualRouterFrameControlEnvelope{}, fmt.Errorf("invalid virtual router frame control: %w", err)
		}
	}
	if len(control.Path) == 0 {
		control.Path = append([]string(nil), fallbackPath...)
	}
	return control, nil
}

func setProbeVirtualRouterFrameControl(frame *probeVirtualRouterFrame, control probeVirtualRouterFrameControlEnvelope) error {
	if frame == nil {
		return errors.New("virtual router frame is nil")
	}
	payload, err := json.Marshal(control)
	if err != nil {
		return err
	}
	if len(payload) > probeVirtualRouterFrameMaxControlBytes {
		return fmt.Errorf("virtual router frame control is too large: %d", len(payload))
	}
	frame.Control = payload
	return nil
}

func appendProbeVirtualRouterWireFrameICMPTrace(frame probeVirtualRouterFrame, runtime *probeVirtualRouterRuntime, fallbackPath []string, event string) probeVirtualRouterFrame {
	if _, ok := probeVirtualRouterParseICMPEchoLogInfo(frame.Data); !ok {
		return frame
	}
	control, err := probeVirtualRouterFrameControl(frame, fallbackPath)
	if err != nil {
		return frame
	}
	control.Trace = appendProbeVirtualRouterICMPTrace(control.Trace, runtime, event, "", "")
	if err := setProbeVirtualRouterFrameControl(&frame, control); err != nil {
		return frame
	}
	return frame
}

func probeVirtualRouterWireFramePathString(frame probeVirtualRouterFrame, fallbackPath []string) string {
	control, err := probeVirtualRouterFrameControl(frame, fallbackPath)
	if err != nil {
		return ""
	}
	return strings.Join(control.Path, ">")
}

func marshalProbeVirtualRouterFrameTrace(trace []probeVirtualRouterFrameTraceHop) ([]byte, error) {
	if len(trace) == 0 {
		return nil, nil
	}
	out := make([]probeVirtualRouterFrameTraceHop, 0, len(trace))
	for _, hop := range trace {
		clean := probeVirtualRouterCleanFrameTraceHop(hop)
		if clean.ID == "" || clean.NodeID == "" || clean.Event == "" || clean.UnixNano <= 0 {
			continue
		}
		out = append(out, clean)
	}
	if len(out) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(raw) > 0xffff {
		return nil, errors.New("virtual router frame trace is too large")
	}
	return raw, nil
}

func unmarshalProbeVirtualRouterFrameTrace(raw []byte) ([]probeVirtualRouterFrameTraceHop, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var items []probeVirtualRouterFrameTraceHop
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("invalid virtual router frame trace: %w", err)
	}
	out := make([]probeVirtualRouterFrameTraceHop, 0, len(items))
	for _, item := range items {
		clean := probeVirtualRouterCleanFrameTraceHop(item)
		if clean.ID == "" || clean.NodeID == "" || clean.Event == "" || clean.UnixNano <= 0 {
			continue
		}
		out = append(out, clean)
	}
	return out, nil
}

func probeVirtualRouterCleanFrameTraceHop(input probeVirtualRouterFrameTraceHop) probeVirtualRouterFrameTraceHop {
	return probeVirtualRouterFrameTraceHop{
		ID:         strings.TrimSpace(input.ID),
		NodeID:     normalizeProbeRouteNodeID(input.NodeID),
		RouteID:    strings.TrimSpace(input.RouteID),
		Event:      strings.TrimSpace(input.Event),
		Direction:  strings.TrimSpace(input.Direction),
		RemoteNode: normalizeProbeRouteNodeID(input.RemoteNode),
		UnixNano:   input.UnixNano,
	}
}

func appendProbeVirtualRouterICMPTrace(trace []probeVirtualRouterFrameTraceHop, runtime *probeVirtualRouterRuntime, event string, direction string, remoteNodeID string) []probeVirtualRouterFrameTraceHop {
	cleanEvent := strings.TrimSpace(event)
	nodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if cleanEvent == "" || nodeID == "" {
		return trace
	}
	routeID := ""
	if runtime != nil {
		routeID = strings.TrimSpace(runtime.cfg.routeID)
	}
	item := probeVirtualRouterFrameTraceHop{
		ID:         newProbeTCPDebugFlowID("vrouter_icmp_trace", nodeID),
		NodeID:     nodeID,
		RouteID:    routeID,
		Event:      cleanEvent,
		Direction:  strings.TrimSpace(direction),
		RemoteNode: normalizeProbeRouteNodeID(remoteNodeID),
		UnixNano:   time.Now().UnixNano(),
	}
	return append(append([]probeVirtualRouterFrameTraceHop(nil), trace...), item)
}

func probeVirtualRouterICMPTraceString(trace []probeVirtualRouterFrameTraceHop) string {
	if len(trace) == 0 {
		return ""
	}
	parts := make([]string, 0, len(trace))
	var firstUnixNano int64
	for i, hop := range trace {
		clean := probeVirtualRouterCleanFrameTraceHop(hop)
		if clean.ID == "" || clean.NodeID == "" || clean.Event == "" || clean.UnixNano <= 0 {
			continue
		}
		at := time.Unix(0, clean.UnixNano).UTC().Format(time.RFC3339Nano)
		if firstUnixNano == 0 {
			firstUnixNano = clean.UnixNano
		}
		sinceStart := "0"
		if clean.UnixNano >= firstUnixNano {
			sinceStart = fmt.Sprintf("%d", (clean.UnixNano-firstUnixNano)/int64(time.Millisecond))
		} else {
			sinceStart = "clock_skew"
		}
		parts = append(parts, fmt.Sprintf("%02d node=%s event=%s direction=%s remote=%s route=%s at=%s since_start_ref_ms=%s id=%s", i, clean.NodeID, clean.Event, clean.Direction, clean.RemoteNode, clean.RouteID, at, sinceStart, clean.ID))
	}
	return strings.Join(parts, " | ")
}

func parseProbeVirtualRouterPathText(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ">")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		nodeID := normalizeProbeRouteNodeID(part)
		if nodeID != "" {
			out = append(out, nodeID)
		}
	}
	return out
}

func writeProbeVirtualRouterIPFrame(link *probeVirtualRouterFrameLink, packet []byte, path []string, trace []probeVirtualRouterFrameTraceHop) error {
	if link == nil {
		return errors.New("virtual router frame link is nil")
	}
	frame, err := buildProbeVirtualRouterIPFrame(packet, path, trace)
	if err != nil {
		return err
	}
	return link.EnqueueProbeVirtualRouterFrame(frame)
}

func writeProbeVirtualRouterWireFrameRaw(writer io.Writer, frame probeVirtualRouterFrame) error {
	payload, err := encodeProbeVirtualRouterFrame(frame)
	if err != nil {
		return err
	}
	if deadlineWriter, ok := writer.(interface{ SetWriteDeadline(time.Time) error }); ok && probeVirtualRouterFrameWriteTimeout > 0 {
		if err := deadlineWriter.SetWriteDeadline(time.Now().Add(probeVirtualRouterFrameWriteTimeout)); err != nil {
			return err
		}
		defer func() {
			_ = deadlineWriter.SetWriteDeadline(time.Time{})
		}()
	}
	return writeProbeVirtualRouterAll(writer, payload)
}

func readProbeVirtualRouterWireFrame(reader *bufio.Reader) (probeVirtualRouterFrame, error) {
	header := make([]byte, probeVirtualRouterFrameEnvelopeHeaderSize)
	if _, err := io.ReadFull(reader, header); err != nil {
		return probeVirtualRouterFrame{}, err
	}
	frame, controlLen, dataLen, payloadLen, err := decodeProbeVirtualRouterFrameHeader(header)
	if err != nil {
		return probeVirtualRouterFrame{}, err
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return probeVirtualRouterFrame{}, err
	}
	controlPayload := payload[:controlLen]
	dataPayload := payload[controlLen : controlLen+dataLen]
	if err := verifyProbeVirtualRouterFrameChecksum(header, controlPayload, dataPayload); err != nil {
		return probeVirtualRouterFrame{}, err
	}
	frame.Control = controlPayload
	frame.Data = dataPayload
	return frame, nil
}

func writeProbeVirtualRouterAll(writer io.Writer, payload []byte) error {
	written := 0
	for written < len(payload) {
		n, err := writer.Write(payload[written:])
		if n > 0 {
			written += n
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func handleProbeVirtualRouterTUNPacket(packet []byte) bool {
	dstIP := probeVirtualRouterIPv4Destination(packet)
	if dstIP == "" {
		return false
	}
	if probeVirtualRouterShouldDropNonUnicastDestination(dstIP) {
		return false
	}
	if handleProbeVirtualRouterLocalSelfTUNPacket(packet, dstIP) {
		return true
	}
	path := currentProbeVirtualRouterPathForPacket(packet, dstIP)
	if !probeVirtualRouterLocalEntryEnabled() && !probeVirtualRouterTUNPacketAllowedWhenEntryDisabled(dstIP, path) {
		return false
	}
	if len(path) < 2 && probeVirtualRouterIPInCurrentFakeCIDR(dstIP) {
		scheduleProbeVirtualRouterRouteConfigRefreshFromController("fake_ip_path_miss", probeVirtualRouterRouteConfigRefreshHotPathMinInterval)
	}
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp tun rx: trace_code=icmp-trace-v2 kind=%s src=%s dst=%s id=%d seq=%d local_node=%s path=%s bytes=%d", info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, currentProbeVirtualRouterLocalNodeID(), strings.Join(path, ">"), len(packet))
	}
	if len(path) < 2 {
		if probeVirtualRouterEnsureDirectBypassForOrdinaryTarget(packet, dstIP) {
			recordProbeVirtualRouterRecentPacket("tun_rx", "bypass", nil, packet, path, false, nil)
			return false
		}
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp tun drop: kind=%s src=%s dst=%s id=%d seq=%d reason=path_unavailable local_node=%s", info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, currentProbeVirtualRouterLocalNodeID())
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport tun drop: proto=%s src=%s:%d dst=%s:%d reason=path_unavailable local_node=%s", info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, currentProbeVirtualRouterLocalNodeID())
		}
		recordProbeVirtualRouterRecentPacket("tun_rx", "drop", nil, packet, path, false, errors.New("path unavailable"))
		return false
	}
	if err := probeVirtualRouterFakeIPForwardUnavailableError(dstIP, path, currentProbeVirtualRouterLocalNodeID()); err != nil {
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp tun drop: kind=%s src=%s dst=%s id=%d seq=%d reason=fake_ip_exit_unreachable local_node=%s path=%s err=%v", info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, currentProbeVirtualRouterLocalNodeID(), strings.Join(path, ">"), err)
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport tun drop: proto=%s src=%s:%d dst=%s:%d reason=fake_ip_exit_unreachable local_node=%s path=%s err=%v", info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, currentProbeVirtualRouterLocalNodeID(), strings.Join(path, ">"), err)
		}
		recordProbeVirtualRouterRecentPacket("tun_rx", "drop", nil, packet, path, false, err)
		return false
	}
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok && info.Kind == "echo_request" && probeVirtualRouterIPMatches(info.SourceIP, currentProbeVirtualRouterLocalIP()) {
		recordProbeVirtualRouterICMPPingStart(info, path)
	}
	trace := []probeVirtualRouterFrameTraceHop(nil)
	if _, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		trace = appendProbeVirtualRouterICMPTrace(trace, nil, "tun_rx", "", "")
	}
	if err := forwardProbeVirtualRouterPacketAlongPath(packet, dstIP, path, trace); err != nil {
		log.Printf("probe virtual router frame forward failed: dst=%s path=%s err=%v", dstIP, strings.Join(path, ">"), err)
		recordProbeVirtualRouterRecentPacket("tun_rx", "forward_error", nil, packet, path, false, err)
		return true
	}
	recordProbeVirtualRouterRecentPacket("tun_rx", "forward", nil, packet, path, false, nil)
	return true
}

func probeVirtualRouterEnsureDirectBypassForOrdinaryTarget(packet []byte, dstIP string) bool {
	if !probeVirtualRouterLocalEntryEnabled() || probeVirtualRouterIPInCurrentFakeCIDR(dstIP) {
		return false
	}
	targetIP := net.ParseIP(strings.TrimSpace(dstIP)).To4()
	if targetIP == nil {
		return false
	}
	probeVirtualRouterState.mu.RLock()
	_, isVirtualNodeIP := probeVirtualRouterState.ipToNode[targetIP.String()]
	probeVirtualRouterState.mu.RUnlock()
	if isVirtualNodeIP {
		return false
	}
	targetAddr := probeVirtualRouterDirectBypassTargetAddr(packet, targetIP.String())
	if targetAddr == "" {
		return false
	}
	if err := probeVirtualRouterEnsureDirectBypass(targetAddr); err != nil {
		log.Printf("probe virtual router direct bypass route failed: dst=%s target=%s err=%v", targetIP.String(), targetAddr, err)
	}
	return true
}

func probeVirtualRouterDirectBypassTargetAddr(packet []byte, dstIP string) string {
	if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok && info.DestinationPort > 0 {
		return net.JoinHostPort(strings.TrimSpace(dstIP), strconv.Itoa(int(info.DestinationPort)))
	}
	return net.JoinHostPort(strings.TrimSpace(dstIP), "0")
}

func probeVirtualRouterFakeIPForwardUnavailableError(dstIP string, path []string, localNodeID string) error {
	if len(path) < 2 {
		return nil
	}
	if _, ok := currentProbeVirtualRouterFakeIPEntryByIP(dstIP); !ok {
		return nil
	}
	localNodeID = normalizeProbeRouteNodeID(localNodeID)
	if localNodeID == "" {
		localNodeID = currentProbeVirtualRouterLocalNodeID()
	}
	nextNodeID := probeVirtualRouterNextHopInPath(path, localNodeID)
	if nextNodeID == "" {
		return fmt.Errorf("fake ip exit unreachable: next hop is unavailable path=%s", strings.Join(path, ">"))
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return fmt.Errorf("fake ip exit unreachable: adjacent runtime is unavailable next=%s path=%s", nextNodeID, strings.Join(path, ">"))
	}
	if !probeVirtualRouterRuntimeHasBridgeSession(rt, direction) {
		return fmt.Errorf("fake ip exit unreachable: physical carrier unavailable route=%s next=%s direction=%s path=%s", probeVirtualRouterRuntimeLogRouteID(rt), nextNodeID, normalizeProbeRouteBridgeRole(direction), strings.Join(path, ">"))
	}
	return nil
}

func probeVirtualRouterTUNPacketAllowedWhenEntryDisabled(dstIP string, path []string) bool {
	if probeVirtualRouterIPInCurrentFakeCIDR(dstIP) {
		return true
	}
	if len(path) >= 2 {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(dstIP)).To4()
	if ip == nil {
		return false
	}
	probeVirtualRouterState.mu.RLock()
	_, ok := probeVirtualRouterState.ipToNode[ip.String()]
	probeVirtualRouterState.mu.RUnlock()
	return ok
}

func handleProbeVirtualRouterLocalSelfTUNPacket(packet []byte, dstIP string) bool {
	localIP := currentProbeVirtualRouterLocalIP()
	if !probeVirtualRouterIPMatches(dstIP, localIP) {
		return false
	}
	reply, _, ok := buildProbeVirtualRouterICMPEchoReply(packet, localIP)
	if !ok {
		return false
	}
	writer := probeVirtualRouterLocalTUNPacketWriter
	if writer == nil {
		writer = writeProbeVirtualRouterLocalTUNPacket
	}
	if err := writer(reply); err != nil {
		log.Printf("probe virtual router local self icmp reply failed: local_ip=%s err=%v", localIP, err)
		return true
	}
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(reply); ok {
		log.Printf("probe virtual router local self icmp reply ok: kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s bytes=%d", info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, len(reply))
	}
	return true
}

func probeVirtualRouterIPInCurrentFakeCIDR(ipText string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip == nil {
		return false
	}
	_, network, err := net.ParseCIDR(currentProbeVirtualRouterFakeIPCIDR())
	return err == nil && network != nil && network.Contains(ip)
}

func refreshProbeVirtualRouterRouteConfigFromController(reason string) bool {
	identity, controllerBaseURL, ok := currentProbeVirtualRouterController()
	if !ok {
		return false
	}
	if cleanReason := strings.TrimSpace(reason); cleanReason != "" {
		log.Printf("probe virtual router route config sync start: reason=%s", cleanReason)
	}
	if err := syncProbeRouteConfig(identity, controllerBaseURL); err != nil {
		log.Printf("probe virtual router route config sync failed: reason=%s err=%v", strings.TrimSpace(reason), err)
		return false
	}
	return true
}

func scheduleProbeVirtualRouterRouteConfigRefreshFromController(reason string, minInterval time.Duration) bool {
	cleanReason := strings.TrimSpace(reason)
	if cleanReason == "" {
		cleanReason = "scheduled"
	}
	if minInterval <= 0 {
		minInterval = probeVirtualRouterRouteConfigRefreshHotPathMinInterval
	}
	now := time.Now()
	probeVirtualRouterRouteConfigRefreshState.mu.Lock()
	if probeVirtualRouterRouteConfigRefreshState.running[cleanReason] {
		probeVirtualRouterRouteConfigRefreshState.mu.Unlock()
		return false
	}
	if lastAt := probeVirtualRouterRouteConfigRefreshState.lastAt[cleanReason]; !lastAt.IsZero() && now.Sub(lastAt) < minInterval {
		probeVirtualRouterRouteConfigRefreshState.mu.Unlock()
		return false
	}
	probeVirtualRouterRouteConfigRefreshState.running[cleanReason] = true
	probeVirtualRouterRouteConfigRefreshState.lastAt[cleanReason] = now
	probeVirtualRouterRouteConfigRefreshState.mu.Unlock()

	go func() {
		defer func() {
			probeVirtualRouterRouteConfigRefreshState.mu.Lock()
			delete(probeVirtualRouterRouteConfigRefreshState.running, cleanReason)
			probeVirtualRouterRouteConfigRefreshState.mu.Unlock()
		}()
		refreshProbeVirtualRouterRouteConfigFromController(cleanReason)
	}()
	return true
}

func probeVirtualRouterShouldDropNonUnicastDestination(dstIP string) bool {
	ip := net.ParseIP(strings.TrimSpace(dstIP)).To4()
	if ip == nil {
		return false
	}
	if ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
		return true
	}
	// vRouter uses 198.18.0.0/15 for virtual addresses; 198.19.255.255 is the
	// subnet broadcast and must not enter point-to-point route selection.
	return ip[0] == 198 && ip[1] == 19 && ip[2] == 255 && ip[3] == 255
}

func startProbeVirtualRouterKeepAliveWorker(rt *probeVirtualRouterRuntime) {
	if rt == nil || !isProbeVirtualRouterRuntimeRouteID(rt.cfg.routeID) {
		return
	}
	go func() {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-rt.stopCh:
				return
			case <-timer.C:
				probeVirtualRouterKeepAliveRuntime(rt)
				timer.Reset(probeVirtualRouterPingPongInterval)
			}
		}
	}()
}

func probeVirtualRouterKeepAliveRuntime(rt *probeVirtualRouterRuntime) {
	if rt == nil {
		return
	}
	if normalizeProbeRouteNodeID(rt.cfg.peerNodeID) == "" {
		clearProbeVirtualRouterRuntimePingError(rt.cfg.routeID)
		return
	}
	if !probeVirtualRouterRuntimeHasPhysicalBridgeSession(rt) {
		if rt.cfg.dialer {
			signalProbeVirtualRouterBridgeDialer(rt)
		}
		clearProbeVirtualRouterRuntimePingError(rt.cfg.routeID)
		return
	}
	if !rt.cfg.dialer {
		clearProbeVirtualRouterRuntimePingError(rt.cfg.routeID)
		return
	}
	probeVirtualRouterPingPongDirection(rt, probeRouteBridgeRoleToNext)
}

func probeVirtualRouterPingPongAllRuntimes() int {
	probeVirtualRouterRuntimeState.mu.RLock()
	runtimes := make([]*probeVirtualRouterRuntime, 0, len(probeVirtualRouterRuntimeState.runtimes))
	for _, rt := range probeVirtualRouterRuntimeState.runtimes {
		if rt == nil {
			continue
		}
		runtimes = append(runtimes, rt)
	}
	probeVirtualRouterRuntimeState.mu.RUnlock()

	var wg sync.WaitGroup
	for _, rt := range runtimes {
		wg.Add(1)
		go func(runtime *probeVirtualRouterRuntime) {
			defer wg.Done()
			probeVirtualRouterKeepAliveRuntime(runtime)
		}(rt)
	}
	wg.Wait()
	probeVirtualRouterQueryAllPathRTTs()
	return len(runtimes)
}

func probeVirtualRouterQueryAdjacentRTTRuntime(rt *probeVirtualRouterRuntime) {
	if rt == nil {
		return
	}
	targetNodeID := ""
	direction := ""
	if normalizeProbeRouteNodeID(rt.cfg.peerNodeID) != "" && rt.cfg.dialer {
		targetNodeID = normalizeProbeRouteNodeID(rt.cfg.peerNodeID)
		direction = probeRouteBridgeRoleToNext
	}
	if targetNodeID == "" {
		recordProbeVirtualRouterRuntimeRemoteRTTError(rt.cfg.routeID, errors.New("adjacent virtual router node is unavailable"))
		return
	}
	result, err := queryProbeVirtualRouterAdjacentPing(rt, direction, targetNodeID)
	if err != nil {
		recordProbeVirtualRouterRuntimeRemoteRTTError(rt.cfg.routeID, err)
		return
	}
	recordProbeVirtualRouterRuntimeRemoteRTTControlSuccess(rt.cfg.routeID, result.LatencyMS, result.Responder)
}

func probeVirtualRouterQueryPathRTT(path []string) (time.Duration, error) {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" {
		return 0, errors.New("local virtual router node id is empty")
	}
	if len(path) < 2 || normalizeProbeRouteNodeID(path[0]) != localNodeID {
		return 0, errors.New("virtual router rtt query path must start at local node")
	}
	nextNodeID := probeVirtualRouterNextHopInPath(path, localNodeID)
	if nextNodeID == "" {
		return 0, errors.New("next virtual router rtt hop is unavailable")
	}
	localHopLatencyMS, ok := currentProbeVirtualRouterAdjacentLatencyMS(localNodeID, nextNodeID)
	if !ok {
		return 0, errProbeVirtualRouterAdjacentRTTUnavailable
	}
	if len(path) == 2 {
		latency := time.Duration(localHopLatencyMS) * time.Millisecond
		recordProbeVirtualRouterPathRTTSuccess(path, latency, nextNodeID)
		return latency, nil
	}
	response, err := queryProbeVirtualRouterPathRTTControl(path)
	if err != nil {
		recordProbeVirtualRouterPathRTTError(path, err)
		return 0, err
	}
	if !response.OK {
		err = errors.New(strings.TrimSpace(response.Error))
		if err.Error() == "" {
			err = errors.New("virtual router rtt query failed")
		}
		recordProbeVirtualRouterPathRTTError(path, err)
		return 0, err
	}
	latency := time.Duration(response.LatencyMS) * time.Millisecond
	recordProbeVirtualRouterPathRTTSuccess(path, latency, response.Responder)
	return latency, nil
}

func probeVirtualRouterQueryAllPathRTTs() int {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" {
		return 0
	}
	probeVirtualRouterState.mu.RLock()
	neighbors := probeVirtualRouterCloneNeighborsLocked()
	nodeToIP := probeVirtualRouterCloneNodeToIPLocked()
	probeVirtualRouterState.mu.RUnlock()
	nodeIDs := make([]string, 0, len(nodeToIP))
	for nodeID := range nodeToIP {
		clean := normalizeProbeRouteNodeID(nodeID)
		if clean != "" && clean != localNodeID {
			nodeIDs = append(nodeIDs, clean)
		}
	}
	sort.Strings(nodeIDs)
	var paths [][]string
	for _, nodeID := range nodeIDs {
		paths = append(paths, probeVirtualRouterShortestPathsFromNeighbors(neighbors, localNodeID, nodeID)...)
	}
	var wg sync.WaitGroup
	for _, path := range paths {
		pathCopy := append([]string(nil), path...)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := probeVirtualRouterQueryPathRTT(pathCopy); err != nil {
				if errors.Is(err, errProbeVirtualRouterAdjacentRTTUnavailable) {
					return
				}
				log.Printf("probe virtual router path rtt query failed: path=%s err=%v", strings.Join(pathCopy, ">"), err)
			}
		}()
	}
	wg.Wait()
	return len(paths)
}

func probeVirtualRouterPingPongDirection(rt *probeVirtualRouterRuntime, direction string) {
	if rt == nil {
		return
	}
	targetNodeID := ""
	switch normalizeProbeRouteBridgeRole(direction) {
	case probeRouteBridgeRoleToPrev:
		targetNodeID = normalizeProbeRouteNodeID(rt.cfg.peerNodeID)
	default:
		targetNodeID = normalizeProbeRouteNodeID(rt.cfg.peerNodeID)
	}
	result, err := queryProbeVirtualRouterAdjacentPing(rt, direction, targetNodeID)
	if err != nil {
		recordProbeVirtualRouterRuntimePingError(rt, direction, err)
		return
	}
	recordProbeVirtualRouterRuntimePingSuccess(rt, direction, time.Duration(result.LatencyMS)*time.Millisecond)
}

func makeProbeVirtualRouterFrameLinkRXDispatchShards() []chan probeVirtualRouterFrame {
	shards := make([]chan probeVirtualRouterFrame, 0, probeVirtualRouterFrameLinkRXDispatchShards)
	for i := 0; i < probeVirtualRouterFrameLinkRXDispatchShards; i++ {
		shards = append(shards, make(chan probeVirtualRouterFrame, probeVirtualRouterFrameLinkRXDispatchShardBufferFrames))
	}
	return shards
}

func newProbeVirtualRouterFrameLink(key string, runtime *probeVirtualRouterRuntime, carrier net.Conn, requestPath []string) *probeVirtualRouterFrameLink {
	now := time.Now()
	link := &probeVirtualRouterFrameLink{
		key:              strings.TrimSpace(key),
		runtime:          runtime,
		requestPath:      append([]string(nil), requestPath...),
		openedAt:         now,
		lastUsed:         now,
		tx:               make(chan probeVirtualRouterFrame, probeVirtualRouterFrameLinkTXBufferFrames),
		rx:               make(chan probeVirtualRouterFrame, probeVirtualRouterFrameLinkRXBufferFrames),
		rxDispatchShards: makeProbeVirtualRouterFrameLinkRXDispatchShards(),
		done:             make(chan struct{}),
		carrierNotify:    make(chan struct{}, 1),
	}
	if carrier != nil {
		link.carrier = newProbeVirtualRouterPhysicalCarrier(carrier, "", "")
	}
	return link
}

func newProbeVirtualRouterPhysicalCarrier(conn net.Conn, sessionID string, remoteAddr string) *probeVirtualRouterPhysicalCarrier {
	now := time.Now()
	return &probeVirtualRouterPhysicalCarrier{
		conn:        conn,
		sessionID:   strings.TrimSpace(sessionID),
		remoteAddr:  strings.TrimSpace(remoteAddr),
		connectedAt: now,
		lastReadAt:  now,
		lastWriteAt: now,
		done:        make(chan struct{}),
	}
}

func (s *probeVirtualRouterFrameLink) AttachCarrier(conn net.Conn, sessionID string, remoteAddr string) *probeVirtualRouterPhysicalCarrier {
	if s == nil || conn == nil {
		return nil
	}
	token := newProbeVirtualRouterPhysicalCarrier(conn, sessionID, remoteAddr)
	var old *probeVirtualRouterPhysicalCarrier
	rejectDuplicate := false
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		token.close()
		return nil
	default:
	}
	old = s.carrier
	if s.runtime != nil && old != nil && len(s.tx) == 0 && probeVirtualRouterPhysicalCarrierRecentlyActive(old, probeVirtualRouterCarrierStaleRXGrace) {
		rejectDuplicate = true
	}
	if rejectDuplicate {
		s.mu.Unlock()
		token.close()
		log.Printf("probe virtual router physical carrier duplicate rejected: route=%s key=%s current_session=%s current_remote=%s new_session=%s new_remote=%s", probeVirtualRouterRuntimeLogRouteID(s.runtime), strings.TrimSpace(s.key), strings.TrimSpace(old.sessionID), strings.TrimSpace(old.remoteAddr), strings.TrimSpace(sessionID), strings.TrimSpace(remoteAddr))
		return nil
	}
	s.carrier = token
	s.openedAt = token.connectedAt
	s.lastUsed = token.connectedAt
	droppedTX, droppedRX := s.clearBuffersLocked()
	s.signalCarrierChangedLocked()
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	if droppedTX > 0 || droppedRX > 0 {
		log.Printf("probe virtual router frame buffers cleared: reason=carrier_attached route=%s key=%s tx=%d rx=%d session_id=%s remote=%s", probeVirtualRouterRuntimeLogRouteID(s.runtime), strings.TrimSpace(s.key), droppedTX, droppedRX, strings.TrimSpace(sessionID), strings.TrimSpace(remoteAddr))
	}
	return token
}

func probeVirtualRouterPhysicalCarrierRecentlyActive(token *probeVirtualRouterPhysicalCarrier, maxIdle time.Duration) bool {
	if token == nil {
		return false
	}
	if maxIdle <= 0 {
		maxIdle = probeVirtualRouterCarrierStaleRXGrace
	}
	token.mu.Lock()
	lastReadAt := token.lastReadAt
	lastWriteAt := token.lastWriteAt
	connectedAt := token.connectedAt
	token.mu.Unlock()
	lastActiveAt := lastReadAt
	if lastWriteAt.After(lastActiveAt) {
		lastActiveAt = lastWriteAt
	}
	if lastActiveAt.IsZero() {
		lastActiveAt = connectedAt
	}
	return !lastActiveAt.IsZero() && time.Since(lastActiveAt) < maxIdle
}

func (s *probeVirtualRouterFrameLink) signalCarrierChangedLocked() {
	if s == nil || s.carrierNotify == nil {
		return
	}
	select {
	case s.carrierNotify <- struct{}{}:
	default:
	}
}

func (s *probeVirtualRouterFrameLink) clearBuffersLocked() (int, int) {
	if s == nil {
		return 0, 0
	}
	txDropped := drainProbeVirtualRouterFrameChannel(s.tx)
	rxDropped := drainProbeVirtualRouterFrameChannel(s.rx)
	for _, shard := range s.rxDispatchShards {
		rxDropped += drainProbeVirtualRouterFrameChannel(shard)
	}
	return txDropped, rxDropped
}

func drainProbeVirtualRouterFrameChannel(ch chan probeVirtualRouterFrame) int {
	if ch == nil {
		return 0
	}
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			return count
		}
	}
}

func runProbeVirtualRouterPhysicalCarrier(runtime *probeVirtualRouterRuntime, carrier net.Conn, sessionID string, remoteAddr string) {
	if runtime == nil || carrier == nil {
		return
	}
	key := probeVirtualRouterFrameLinkKey(runtime, "", "", nil)
	probeVirtualRouterFrameLinkState.mu.Lock()
	link := probeVirtualRouterFrameLinkState.links[key]
	if link == nil {
		link = newProbeVirtualRouterFrameLink(key, runtime, nil, nil)
		probeVirtualRouterFrameLinkState.links[key] = link
	} else {
		link.runtime = runtime
	}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	link.Start()
	token := link.AttachCarrier(carrier, sessionID, remoteAddr)
	if token == nil {
		_ = carrier.Close()
		return
	}
	recordProbeVirtualRouterRuntimeOpenSuccess(runtime.cfg.routeID, 0)
	log.Printf("probe virtual router physical carrier connected: route=%s role=%s session_id=%s remote=%s", strings.TrimSpace(runtime.cfg.routeID), probeVirtualRouterRuntimeRole, strings.TrimSpace(sessionID), strings.TrimSpace(remoteAddr))
	<-token.done
	log.Printf("probe virtual router physical carrier disconnected: route=%s role=%s session_id=%s remote=%s", strings.TrimSpace(runtime.cfg.routeID), probeVirtualRouterRuntimeRole, strings.TrimSpace(sessionID), strings.TrimSpace(remoteAddr))
}

func (s *probeVirtualRouterFrameLink) Start() {
	if s == nil || s.done == nil || s.tx == nil || s.rx == nil {
		return
	}
	s.startOnce.Do(func() {
		shards := s.ensureRXDispatchShards()
		go s.runTXWorker()
		go s.runRXWorker()
		go s.runRXDispatchWorker()
		for shardID, shard := range shards {
			go s.runRXDispatchShardWorker(shardID, shard)
		}
	})
}

func (s *probeVirtualRouterFrameLink) ensureRXDispatchShards() []chan probeVirtualRouterFrame {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if len(s.rxDispatchShards) == 0 {
		s.rxDispatchShards = makeProbeVirtualRouterFrameLinkRXDispatchShards()
	}
	shards := append([]chan probeVirtualRouterFrame(nil), s.rxDispatchShards...)
	s.mu.Unlock()
	return shards
}

func (s *probeVirtualRouterFrameLink) rxQueueSnapshot() (int, int, int, int, int) {
	if s == nil {
		return 0, 0, 0, 0, 0
	}
	entryDepth, entryCap := 0, 0
	if s.rx != nil {
		entryDepth = len(s.rx)
		entryCap = cap(s.rx)
	}
	s.mu.Lock()
	shards := append([]chan probeVirtualRouterFrame(nil), s.rxDispatchShards...)
	s.mu.Unlock()
	dispatchDepth, dispatchCap := 0, 0
	for _, shard := range shards {
		if shard == nil {
			continue
		}
		dispatchDepth += len(shard)
		dispatchCap += cap(shard)
	}
	return entryDepth, entryCap, dispatchDepth, dispatchCap, len(shards)
}

func (s *probeVirtualRouterFrameLink) Wait() {
	if s == nil || s.done == nil {
		return
	}
	<-s.done
}

func (s *probeVirtualRouterFrameLink) EnqueueProbeVirtualRouterFrame(input probeVirtualRouterFrame) error {
	if s == nil {
		return io.ErrClosedPipe
	}
	frame := probeVirtualRouterFrame{
		MainType: input.MainType,
		SubType:  input.SubType,
		Control:  append([]byte(nil), input.Control...),
		Data:     append([]byte(nil), input.Data...),
	}
	if s.tx == nil || s.done == nil {
		token, err := s.currentCarrier()
		if err != nil {
			return err
		}
		frame = appendProbeVirtualRouterWireFrameICMPTrace(frame, s.runtime, s.requestPath, "carrier_tx")
		if err := writeProbeVirtualRouterWireFrameRaw(token.conn, frame); err != nil {
			s.detachCarrier(token)
			return err
		}
		token.markWrite()
		s.touch()
		return nil
	}
	select {
	case <-s.done:
		return io.ErrClosedPipe
	default:
	}
	select {
	case s.tx <- frame:
		s.touch()
		return nil
	case <-s.done:
		return io.ErrClosedPipe
	default:
		return fmt.Errorf("virtual router tx queue full: key=%s depth=%d capacity=%d", strings.TrimSpace(s.key), len(s.tx), cap(s.tx))
	}
}

func (s *probeVirtualRouterFrameLink) runTXWorker() {
	for {
		select {
		case frame := <-s.tx:
			if len(frame.Data) == 0 {
				continue
			}
			token, err := s.currentCarrier()
			if err != nil {
				log.Printf("probe virtual router frame tx drop: route=%s key=%s path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, probeVirtualRouterWireFramePathString(frame, s.requestPath), err)
				continue
			}
			frame = appendProbeVirtualRouterWireFrameICMPTrace(frame, s.runtime, s.requestPath, "carrier_tx")
			err = writeProbeVirtualRouterWireFrameRaw(token.conn, frame)
			if err == nil {
				token.markWrite()
				s.touch()
				continue
			}
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				log.Printf("probe virtual router frame tx carrier failed: route=%s key=%s path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, probeVirtualRouterWireFramePathString(frame, s.requestPath), err)
			}
			s.detachCarrier(token)
		case <-s.done:
			return
		}
	}
}

func (s *probeVirtualRouterFrameLink) runRXWorker() {
	for {
		token, err := s.waitCarrier()
		if err != nil {
			return
		}
		reader := bufio.NewReaderSize(token.conn, probeVirtualRouterFrameReadBufferBytes)
		for {
			frame, err := readProbeVirtualRouterWireFrame(reader)
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !isProbeVirtualRouterClosedLinkError(err) {
					log.Printf("probe virtual router frame rx carrier failed: route=%s key=%s err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, err)
				}
				s.detachCarrier(token)
				break
			}
			frame = appendProbeVirtualRouterWireFrameICMPTrace(frame, s.runtime, s.requestPath, "carrier_rx")
			token.markRead()
			if shouldHandleProbeVirtualRouterFrameInRXWorker(s.runtime, frame, s.requestPath) {
				if err := handleProbeVirtualRouterFrame(s.runtime, s, frame, s.requestPath); err != nil {
					log.Printf("probe virtual router frame rx inline failed: route=%s key=%s path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, probeVirtualRouterWireFramePathString(frame, s.requestPath), err)
				}
				s.touch()
				continue
			}
			if err := s.enqueueRXFrame(frame); err != nil {
				if errors.Is(err, io.ErrClosedPipe) {
					return
				}
				log.Printf("probe virtual router frame rx enqueue failed: route=%s key=%s path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, probeVirtualRouterWireFramePathString(frame, s.requestPath), err)
			}
		}
	}
}

func (s *probeVirtualRouterFrameLink) enqueueRXFrame(frame probeVirtualRouterFrame) error {
	if s == nil {
		return io.ErrClosedPipe
	}
	if s.rx == nil || s.done == nil {
		return io.ErrClosedPipe
	}
	select {
	case s.rx <- frame:
		s.touch()
		return nil
	case <-s.done:
		return io.ErrClosedPipe
	default:
		return fmt.Errorf("virtual router rx queue full: key=%s depth=%d capacity=%d", strings.TrimSpace(s.key), len(s.rx), cap(s.rx))
	}
}

func (s *probeVirtualRouterFrameLink) runRXDispatchWorker() {
	shards := s.ensureRXDispatchShards()
	for {
		select {
		case frame := <-s.rx:
			if len(frame.Data) == 0 {
				continue
			}
			if err := s.enqueueRXDispatchFrame(frame, shards); err != nil {
				if errors.Is(err, io.ErrClosedPipe) {
					return
				}
				log.Printf("probe virtual router frame rx dispatch enqueue failed: route=%s key=%s path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, probeVirtualRouterWireFramePathString(frame, s.requestPath), err)
				if frame.MainType == probeVirtualRouterFrameMainTypeIP {
					recordProbeVirtualRouterRecentPacket("frame_rx", "drop", s.runtime, frame.Data, s.requestPath, false, err)
				}
				continue
			}
		case <-s.done:
			return
		}
	}
}

func (s *probeVirtualRouterFrameLink) enqueueRXDispatchFrame(frame probeVirtualRouterFrame, shards []chan probeVirtualRouterFrame) error {
	if s == nil {
		return io.ErrClosedPipe
	}
	if len(shards) == 0 {
		return s.handleRXDispatchFrame(frame)
	}
	shardID := probeVirtualRouterFrameRXDispatchShard(frame, len(shards))
	shard := shards[shardID]
	select {
	case shard <- frame:
		return nil
	case <-s.done:
		return io.ErrClosedPipe
	default:
		return fmt.Errorf("virtual router rx dispatch queue full: key=%s shard=%d depth=%d capacity=%d", strings.TrimSpace(s.key), shardID, len(shard), cap(shard))
	}
}

func (s *probeVirtualRouterFrameLink) runRXDispatchShardWorker(shardID int, shard chan probeVirtualRouterFrame) {
	if s == nil || shard == nil {
		return
	}
	for {
		select {
		case frame := <-shard:
			if len(frame.Data) == 0 {
				continue
			}
			if err := s.handleRXDispatchFrame(frame); err != nil {
				log.Printf("probe virtual router frame rx dispatch failed: route=%s key=%s shard=%d path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, shardID, probeVirtualRouterWireFramePathString(frame, s.requestPath), err)
				continue
			}
		case <-s.done:
			return
		}
	}
}

func (s *probeVirtualRouterFrameLink) handleRXDispatchFrame(frame probeVirtualRouterFrame) error {
	if s == nil {
		return io.ErrClosedPipe
	}
	if len(frame.Data) == 0 {
		return nil
	}
	return handleProbeVirtualRouterFrame(s.runtime, s, frame, s.requestPath)
}

func probeVirtualRouterFrameRXDispatchShard(frame probeVirtualRouterFrame, shardCount int) int {
	if shardCount <= 1 {
		return 0
	}
	return int(probeVirtualRouterFrameRXDispatchHash(frame) % uint32(shardCount))
}

func probeVirtualRouterFrameRXDispatchHash(frame probeVirtualRouterFrame) uint32 {
	const (
		fnvOffset uint32 = 2166136261
		fnvPrime  uint32 = 16777619
	)
	hashByte := func(h uint32, value byte) uint32 {
		h ^= uint32(value)
		return h * fnvPrime
	}
	hashUint16 := func(h uint32, value uint16) uint32 {
		h = hashByte(h, byte(value>>8))
		return hashByte(h, byte(value))
	}
	h := fnvOffset
	h = hashUint16(h, frame.MainType)
	h = hashUint16(h, frame.SubType)
	if frame.MainType != probeVirtualRouterFrameMainTypeIP {
		return h
	}
	return probeVirtualRouterPacketFlowHash(frame.Data, h)
}

func probeVirtualRouterPacketDispatchShard(packet []byte, shardCount int) int {
	if shardCount <= 1 {
		return 0
	}
	return int(probeVirtualRouterPacketFlowHash(packet, 2166136261) % uint32(shardCount))
}

func probeVirtualRouterPacketFlowHash(packet []byte, seed uint32) uint32 {
	const fnvPrime uint32 = 16777619
	hashByte := func(h uint32, value byte) uint32 {
		h ^= uint32(value)
		return h * fnvPrime
	}
	hashUint16 := func(h uint32, value uint16) uint32 {
		h = hashByte(h, byte(value>>8))
		return hashByte(h, byte(value))
	}
	hashUint32 := func(h uint32, value uint32) uint32 {
		h = hashByte(h, byte(value>>24))
		h = hashByte(h, byte(value>>16))
		h = hashByte(h, byte(value>>8))
		return hashByte(h, byte(value))
	}

	h := seed
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return h
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl {
		return h
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl {
		return h
	}
	proto := packet[9]
	srcIP := binary.BigEndian.Uint32(packet[12:16])
	dstIP := binary.BigEndian.Uint32(packet[16:20])
	srcPort, dstPort := uint16(0), uint16(0)
	if (proto == 6 || proto == 17) && totalLen >= ihl+4 {
		transport := packet[ihl:totalLen]
		srcPort = binary.BigEndian.Uint16(transport[0:2])
		dstPort = binary.BigEndian.Uint16(transport[2:4])
	}
	if probeVirtualRouterEndpointGreater(srcIP, srcPort, dstIP, dstPort) {
		srcIP, dstIP = dstIP, srcIP
		srcPort, dstPort = dstPort, srcPort
	}
	h = hashByte(h, proto)
	h = hashUint32(h, srcIP)
	h = hashUint16(h, srcPort)
	h = hashUint32(h, dstIP)
	h = hashUint16(h, dstPort)
	if proto == 1 && totalLen >= ihl+8 {
		icmp := packet[ihl:totalLen]
		h = hashUint16(h, binary.BigEndian.Uint16(icmp[4:6]))
	}
	return h
}

func probeVirtualRouterEndpointGreater(leftIP uint32, leftPort uint16, rightIP uint32, rightPort uint16) bool {
	if leftIP != rightIP {
		return leftIP > rightIP
	}
	return leftPort > rightPort
}

func (s *probeVirtualRouterFrameLink) touch() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()
}

func shouldHandleProbeVirtualRouterFrameInRXWorker(runtime *probeVirtualRouterRuntime, frame probeVirtualRouterFrame, fallbackPath []string) bool {
	if len(frame.Data) == 0 {
		return false
	}
	control, err := probeVirtualRouterFrameControl(frame, fallbackPath)
	if err != nil {
		return false
	}
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if localNodeID == "" {
		return false
	}
	switch frame.MainType {
	case probeVirtualRouterFrameMainTypeIP:
		return false
	case probeVirtualRouterFrameMainTypePingPong:
		return shouldHandleProbeVirtualRouterPingPongFrameInRXWorker(frame.SubType, frame.Data)
	case probeVirtualRouterFrameMainTypePathRTT:
		return shouldHandleProbeVirtualRouterPathRTTFrameInRXWorker(frame.SubType, frame.Data, localNodeID)
	case probeVirtualRouterFrameMainTypeSpeed:
		return shouldHandleProbeVirtualRouterSpeedFrameInRXWorker(frame.SubType, frame.Data, control.Path, localNodeID)
	case probeVirtualRouterFrameMainTypeRouteTest:
		return false
	default:
		return false
	}
}

func shouldHandleProbeVirtualRouterIPFrameInRXWorker(runtime *probeVirtualRouterRuntime, packet []byte, path []string, localNodeID string) bool {
	dstIP := probeVirtualRouterIPv4Destination(packet)
	if dstIP == "" {
		return false
	}
	if probeVirtualRouterIPMatches(dstIP, currentProbeVirtualRouterLocalIPForRuntime(runtime)) {
		return false
	}
	cleanPath := cleanProbeVirtualRouterPath(path)
	if len(cleanPath) == 0 {
		cleanPath = currentProbeVirtualRouterPathToIP(dstIP)
	}
	return probeVirtualRouterNextHopInPath(cleanPath, localNodeID) != ""
}

func shouldHandleProbeVirtualRouterPingPongFrameInRXWorker(subType uint16, payload []byte) bool {
	switch subType {
	case probeVirtualRouterPingPongSubTypePing, probeVirtualRouterPingPongSubTypePong:
		msg := probeVirtualRouterControlProbePayload{}
		return json.Unmarshal(payload, &msg) == nil
	default:
		return false
	}
}

func shouldHandleProbeVirtualRouterPathRTTFrameInRXWorker(subType uint16, payload []byte, localNodeID string) bool {
	msg := probeVirtualRouterControlProbePayload{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return false
	}
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	switch subType {
	case probeVirtualRouterPathRTTSubTypeQuery:
		if normalizeProbeRouteNodeID(msg.TargetNodeID) == localNodeID {
			return true
		}
		return probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	case probeVirtualRouterPathRTTSubTypeResp:
		if normalizeProbeRouteNodeID(msg.SourceNodeID) == localNodeID {
			return true
		}
		return probeVirtualRouterNextHopInPath(probeVirtualRouterReversePath(msg.Path), localNodeID) != ""
	default:
		return false
	}
}

func shouldHandleProbeVirtualRouterSpeedFrameInRXWorker(subType uint16, payload []byte, framePath []string, localNodeID string) bool {
	if subType == probeVirtualRouterSpeedSubTypeChunk {
		path := cleanProbeVirtualRouterPath(framePath)
		if len(path) < 2 {
			return false
		}
		if localNodeID == path[len(path)-1] {
			return true
		}
		return probeVirtualRouterNextHopInPath(path, localNodeID) != ""
	}
	msg := probeVirtualRouterSpeedTestResultPayload{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return false
	}
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	switch subType {
	case probeVirtualRouterSpeedSubTypeStart, probeVirtualRouterSpeedSubTypeFinish:
		if len(msg.Path) < 2 {
			return false
		}
		if localNodeID == msg.Path[len(msg.Path)-1] {
			return true
		}
		return probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	case probeVirtualRouterSpeedSubTypeSend:
		return len(msg.Path) >= 2 && localNodeID != msg.Path[len(msg.Path)-1] && probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	case probeVirtualRouterSpeedSubTypeResult:
		if normalizeProbeRouteNodeID(msg.ResultNodeID) == localNodeID {
			return true
		}
		return probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	default:
		return false
	}
}

func shouldHandleProbeVirtualRouterRouteTestFrameInRXWorker(subType uint16, payload []byte, framePath []string, localNodeID string) bool {
	msg := probeVirtualRouterRouteTestPayload{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return false
	}
	if len(msg.Path) == 0 {
		msg.Path = append([]string(nil), framePath...)
	}
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	switch subType {
	case probeVirtualRouterRouteTestSubTypeProbe:
		if len(msg.Path) < 1 {
			return false
		}
		if normalizeProbeRouteNodeID(msg.ExitNodeID) == localNodeID || localNodeID == msg.Path[len(msg.Path)-1] {
			return true
		}
		return probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	case probeVirtualRouterRouteTestSubTypeReport:
		if normalizeProbeRouteNodeID(msg.SourceNodeID) == localNodeID {
			return true
		}
		return probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	default:
		return false
	}
}

func (s *probeVirtualRouterFrameLink) currentCarrier() (*probeVirtualRouterPhysicalCarrier, error) {
	if s == nil {
		return nil, io.ErrClosedPipe
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		return nil, io.ErrClosedPipe
	}
	select {
	case <-s.done:
		return nil, io.ErrClosedPipe
	default:
	}
	token := s.carrier
	if token == nil || token.conn == nil {
		return nil, errors.New("virtual router physical carrier is unavailable")
	}
	return token, nil
}

func (s *probeVirtualRouterFrameLink) waitCarrier() (*probeVirtualRouterPhysicalCarrier, error) {
	if s == nil {
		return nil, io.ErrClosedPipe
	}
	for {
		s.mu.Lock()
		if s.done == nil {
			s.mu.Unlock()
			return nil, io.ErrClosedPipe
		}
		select {
		case <-s.done:
			s.mu.Unlock()
			return nil, io.ErrClosedPipe
		default:
		}
		token := s.carrier
		notify := s.carrierNotify
		if token != nil && token.conn != nil {
			s.mu.Unlock()
			return token, nil
		}
		s.mu.Unlock()
		select {
		case <-s.done:
			return nil, io.ErrClosedPipe
		case <-notify:
		}
	}
}

func probeVirtualRouterFrameLinkDebugState(link *probeVirtualRouterFrameLink) string {
	if link == nil {
		return "link=nil"
	}
	now := time.Now()
	txDepth := 0
	if link.tx != nil {
		txDepth = len(link.tx)
	}
	rxEntryDepth, _, rxDispatchDepth, _, _ := link.rxQueueSnapshot()
	rxDepth := rxEntryDepth + rxDispatchDepth
	link.mu.Lock()
	key := strings.TrimSpace(link.key)
	lastUsed := link.lastUsed
	token := link.carrier
	link.mu.Unlock()
	if token == nil {
		return fmt.Sprintf("link_key=%s carrier=none last_used_ms=%d tx_queue=%d rx_queue=%d rx_entry_queue=%d rx_dispatch_queue=%d", key, probeDurationMilliseconds(now.Sub(lastUsed)), txDepth, rxDepth, rxEntryDepth, rxDispatchDepth)
	}
	token.mu.Lock()
	sessionID := strings.TrimSpace(token.sessionID)
	remoteAddr := strings.TrimSpace(token.remoteAddr)
	connectedAt := token.connectedAt
	lastReadAt := token.lastReadAt
	lastWriteAt := token.lastWriteAt
	token.mu.Unlock()
	return fmt.Sprintf(
		"link_key=%s carrier_session=%s remote=%s connected_ms=%d rx_idle_ms=%d tx_idle_ms=%d tx_queue=%d rx_queue=%d rx_entry_queue=%d rx_dispatch_queue=%d",
		key,
		sessionID,
		remoteAddr,
		probeDurationMilliseconds(now.Sub(connectedAt)),
		probeDurationMilliseconds(now.Sub(lastReadAt)),
		probeDurationMilliseconds(now.Sub(lastWriteAt)),
		txDepth,
		rxDepth,
		rxEntryDepth,
		rxDispatchDepth,
	)
}

func (s *probeVirtualRouterFrameLink) detachCarrier(token *probeVirtualRouterPhysicalCarrier) {
	if s == nil || token == nil {
		return
	}
	detached := false
	droppedTX := 0
	droppedRX := 0
	s.mu.Lock()
	if s.carrier == token {
		s.carrier = nil
		droppedTX, droppedRX = s.clearBuffersLocked()
		s.signalCarrierChangedLocked()
		detached = true
	}
	s.mu.Unlock()
	token.close()
	if detached && (droppedTX > 0 || droppedRX > 0) {
		log.Printf("probe virtual router frame buffers cleared: reason=carrier_detached route=%s key=%s tx=%d rx=%d session_id=%s remote=%s", probeVirtualRouterRuntimeLogRouteID(s.runtime), strings.TrimSpace(s.key), droppedTX, droppedRX, strings.TrimSpace(token.sessionID), strings.TrimSpace(token.remoteAddr))
	}
	if detached && s.runtime != nil {
		clearProbeVirtualRouterRuntimePingError(s.runtime.cfg.routeID)
	}
}

func (c *probeVirtualRouterPhysicalCarrier) markRead() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.lastReadAt = time.Now()
	c.mu.Unlock()
}

func (c *probeVirtualRouterPhysicalCarrier) markWrite() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.lastWriteAt = time.Now()
	c.mu.Unlock()
}

func (c *probeVirtualRouterPhysicalCarrier) lastRead() time.Time {
	if c == nil {
		return time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastReadAt
}

func (c *probeVirtualRouterPhysicalCarrier) close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		if c.conn != nil {
			_ = c.conn.Close()
		}
		if c.done != nil {
			close(c.done)
		}
	})
}

func stopProbeVirtualRouterFrameLink(s *probeVirtualRouterFrameLink) {
	if s == nil {
		return
	}
	var token *probeVirtualRouterPhysicalCarrier
	droppedTX := 0
	droppedRX := 0
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.done != nil {
			select {
			case <-s.done:
			default:
				close(s.done)
			}
		}
		token = s.carrier
		s.carrier = nil
		droppedTX, droppedRX = s.clearBuffersLocked()
		s.signalCarrierChangedLocked()
		s.mu.Unlock()
		if droppedTX > 0 || droppedRX > 0 {
			log.Printf("probe virtual router frame buffers cleared: reason=link_stopped route=%s key=%s tx=%d rx=%d", probeVirtualRouterRuntimeLogRouteID(s.runtime), strings.TrimSpace(s.key), droppedTX, droppedRX)
		}
		if token != nil {
			token.close()
		}
	})
}

func handleProbeVirtualRouterFrame(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, frame probeVirtualRouterFrame, fallbackPath []string) error {
	control, err := probeVirtualRouterFrameControl(frame, fallbackPath)
	if err != nil {
		return err
	}
	switch frame.MainType {
	case probeVirtualRouterFrameMainTypeIP:
		if frame.SubType != probeVirtualRouterIPSubTypeIPv4 {
			return fmt.Errorf("unsupported virtual router ip subtype=%d", frame.SubType)
		}
		return handleProbeVirtualRouterIPFrame(runtime, link, frame.Data, control.Path, control.Trace)
	case probeVirtualRouterFrameMainTypePingPong, probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterFrameMainTypeRouteTest:
		return handleProbeVirtualRouterBusinessFrame(runtime, link, frame.MainType, frame.SubType, frame.Data, control.Path)
	default:
		return fmt.Errorf("unsupported virtual router business type=%d subtype=%d", frame.MainType, frame.SubType)
	}
}

func handleProbeVirtualRouterBusinessFrame(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, mainType uint16, subType uint16, payload []byte, framePath []string) error {
	if mainType == probeVirtualRouterFrameMainTypeSpeed && subType == probeVirtualRouterSpeedSubTypeChunk {
		return handleProbeVirtualRouterSpeedChunk(runtime, payload, framePath)
	}
	msg := probeVirtualRouterControlProbePayload{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}
	if len(msg.Path) == 0 {
		msg.Path = append([]string(nil), framePath...)
	}
	switch {
	case mainType == probeVirtualRouterFrameMainTypePingPong && subType == probeVirtualRouterPingPongSubTypePing:
		return handleProbeVirtualRouterControlPing(runtime, link, msg)
	case mainType == probeVirtualRouterFrameMainTypePingPong && subType == probeVirtualRouterPingPongSubTypePong:
		completeProbeVirtualRouterControlResponse(msg)
		return nil
	case mainType == probeVirtualRouterFrameMainTypePathRTT && subType == probeVirtualRouterPathRTTSubTypeQuery:
		return handleProbeVirtualRouterControlPathRTTQuery(runtime, msg)
	case mainType == probeVirtualRouterFrameMainTypePathRTT && subType == probeVirtualRouterPathRTTSubTypeResp:
		return handleProbeVirtualRouterControlPathRTTResponse(runtime, msg)
	case mainType == probeVirtualRouterFrameMainTypeSpeed:
		speedMsg := probeVirtualRouterSpeedTestResultPayload{}
		if err := json.Unmarshal(payload, &speedMsg); err != nil {
			return err
		}
		if len(speedMsg.Path) == 0 {
			speedMsg.Path = append([]string(nil), framePath...)
		}
		return handleProbeVirtualRouterSpeedFrame(runtime, subType, speedMsg)
	case mainType == probeVirtualRouterFrameMainTypeRouteTest:
		routeTestMsg := probeVirtualRouterRouteTestPayload{}
		if err := json.Unmarshal(payload, &routeTestMsg); err != nil {
			return err
		}
		if len(routeTestMsg.Path) == 0 {
			routeTestMsg.Path = append([]string(nil), framePath...)
		}
		return handleProbeVirtualRouterRouteTestFrame(runtime, subType, routeTestMsg)
	default:
		return fmt.Errorf("unsupported virtual router business type=%d subtype=%d", mainType, subType)
	}
}

func handleProbeVirtualRouterControlPing(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, msg probeVirtualRouterControlProbePayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if msg.RequestID == "" {
		return errors.New("virtual router control ping request id is empty")
	}
	response := msg
	response.OK = true
	response.Error = ""
	response.Responder = localNodeID
	response.CreatedAtUnixNano = time.Now().UnixNano()
	response.LatencyMS = 0
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if link == nil {
		return errors.New("virtual router control ping carrier is unavailable")
	}
	frame, err := buildProbeVirtualRouterBusinessFrame(probeVirtualRouterFrameMainTypePingPong, probeVirtualRouterPingPongSubTypePong, payload, probeVirtualRouterReversePath(msg.Path), nil)
	if err != nil {
		return err
	}
	return link.EnqueueProbeVirtualRouterFrame(frame)
}

func handleProbeVirtualRouterControlPathRTTQuery(runtime *probeVirtualRouterRuntime, msg probeVirtualRouterControlProbePayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if localNodeID == "" {
		return errors.New("local virtual router node id is empty")
	}
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	if msg.RequestID == "" || len(msg.Path) < 2 {
		return errors.New("virtual router path rtt query is incomplete")
	}
	prevNodeID := probeVirtualRouterPreviousHopInPath(msg.Path, localNodeID)
	if prevNodeID != "" {
		latencyMS, ok := currentProbeVirtualRouterAdjacentLatencyMS(prevNodeID, localNodeID)
		if !ok {
			return sendProbeVirtualRouterPathRTTResponse(msg, false, msg.LatencyMS, localNodeID, "adjacent virtual router ping-pong latency is unavailable")
		}
		msg.LatencyMS += latencyMS
	}
	if normalizeProbeRouteNodeID(msg.TargetNodeID) == localNodeID || probeVirtualRouterNextHopInPath(msg.Path, localNodeID) == "" {
		return sendProbeVirtualRouterPathRTTResponse(msg, true, msg.LatencyMS, localNodeID, "")
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterPathRTTSubTypeQuery, payload, msg.Path)
}

func handleProbeVirtualRouterControlPathRTTResponse(runtime *probeVirtualRouterRuntime, msg probeVirtualRouterControlProbePayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if normalizeProbeRouteNodeID(msg.SourceNodeID) == localNodeID {
		completeProbeVirtualRouterControlResponse(msg)
		return nil
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterPathRTTSubTypeResp, payload, probeVirtualRouterReversePath(msg.Path))
}

func sendProbeVirtualRouterPathRTTResponse(msg probeVirtualRouterControlProbePayload, ok bool, latencyMS int64, responder string, message string) error {
	msg.OK = ok
	msg.LatencyMS = latencyMS
	msg.Responder = normalizeProbeRouteNodeID(responder)
	msg.Error = strings.TrimSpace(message)
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterPathRTTSubTypeResp, payload, probeVirtualRouterReversePath(msg.Path))
}

func handleProbeVirtualRouterSpeedFrame(runtime *probeVirtualRouterRuntime, subType uint16, msg probeVirtualRouterSpeedTestResultPayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	if localNodeID == "" || msg.RequestID == "" || len(msg.Path) < 2 {
		return errors.New("virtual router speed control frame is incomplete")
	}
	switch subType {
	case probeVirtualRouterSpeedSubTypeStart:
		if localNodeID != msg.Path[len(msg.Path)-1] {
			return forwardProbeVirtualRouterSpeedAlongPath(subType, msg, msg.Path)
		}
		startProbeVirtualRouterSpeedReceive(msg, localNodeID, probeVirtualRouterRuntimeLogRouteID(runtime))
		return nil
	case probeVirtualRouterSpeedSubTypeFinish:
		if localNodeID != msg.Path[len(msg.Path)-1] {
			return forwardProbeVirtualRouterSpeedAlongPath(subType, msg, msg.Path)
		}
		result, ok := finishProbeVirtualRouterSpeedReceive(msg, localNodeID)
		if !ok {
			return nil
		}
		if runtime != nil {
			recordProbeVirtualRouterRuntimeSpeedTestReceive(strings.TrimSpace(runtime.cfg.routeID), result)
		}
		if normalizeProbeRouteNodeID(result.ResultNodeID) == localNodeID {
			completeProbeVirtualRouterSpeedResponse(result)
			return nil
		}
		result.Path = probeVirtualRouterReversePath(msg.Path)
		return forwardProbeVirtualRouterSpeedAlongPath(probeVirtualRouterSpeedSubTypeResult, result, result.Path)
	case probeVirtualRouterSpeedSubTypeResult:
		if normalizeProbeRouteNodeID(msg.ResultNodeID) == localNodeID {
			completeProbeVirtualRouterSpeedResponse(msg)
			return nil
		}
		return forwardProbeVirtualRouterSpeedAlongPath(subType, msg, msg.Path)
	case probeVirtualRouterSpeedSubTypeSend:
		if localNodeID != msg.Path[len(msg.Path)-1] {
			return forwardProbeVirtualRouterSpeedAlongPath(subType, msg, msg.Path)
		}
		go func() {
			if err := runProbeVirtualRouterOneWaySpeedSender(probeVirtualRouterReversePath(msg.Path), msg, probeVirtualRouterSpeedTestMaxDuration); err != nil {
				response := msg
				response.OK = false
				response.Error = strings.TrimSpace(err.Error())
				response.Responder = localNodeID
				response.Path = probeVirtualRouterReversePath(msg.Path)
				_ = forwardProbeVirtualRouterSpeedAlongPath(probeVirtualRouterSpeedSubTypeResult, response, response.Path)
			}
		}()
		return nil
	default:
		return fmt.Errorf("unsupported virtual router speed subtype=%d", subType)
	}
}

func handleProbeVirtualRouterSpeedChunk(runtime *probeVirtualRouterRuntime, payload []byte, framePath []string) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	path := cleanProbeVirtualRouterPath(framePath)
	if localNodeID == "" || len(path) < 2 {
		return errors.New("virtual router speed chunk path is incomplete")
	}
	if localNodeID != path[len(path)-1] {
		return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterSpeedSubTypeChunk, payload, path)
	}
	requestID, ok := parseProbeVirtualRouterSpeedChunkRequestID(payload)
	if !ok {
		return errors.New("invalid virtual router speed chunk payload")
	}
	recordProbeVirtualRouterSpeedChunk(requestID, int64(len(payload)))
	return nil
}

func startProbeVirtualRouterSpeedReceive(msg probeVirtualRouterSpeedTestResultPayload, localNodeID string, routeID string) {
	now := time.Now()
	session := &probeVirtualRouterSpeedReceiveSession{
		RequestID:     strings.TrimSpace(msg.RequestID),
		Direction:     strings.TrimSpace(msg.Direction),
		SourceNodeID:  normalizeProbeRouteNodeID(msg.SourceNodeID),
		TargetNodeID:  normalizeProbeRouteNodeID(msg.TargetNodeID),
		ResultNodeID:  normalizeProbeRouteNodeID(firstNonEmpty(msg.ResultNodeID, msg.SourceNodeID)),
		Path:          append([]string(nil), msg.Path...),
		RouteID:       strings.TrimSpace(routeID),
		MaxDurationMS: msg.MaxDurationMS,
		LocalNodeID:   normalizeProbeRouteNodeID(localNodeID),
		LastAt:        now,
	}
	if session.ResultNodeID == "" {
		session.ResultNodeID = normalizeProbeRouteNodeID(localNodeID)
	}
	probeVirtualRouterSpeedReceiveState.mu.Lock()
	if probeVirtualRouterSpeedReceiveState.sessions == nil {
		probeVirtualRouterSpeedReceiveState.sessions = make(map[string]*probeVirtualRouterSpeedReceiveSession)
	}
	for key, item := range probeVirtualRouterSpeedReceiveState.sessions {
		if item == nil || (!item.LastAt.IsZero() && now.Sub(item.LastAt) > time.Minute) {
			delete(probeVirtualRouterSpeedReceiveState.sessions, key)
		}
	}
	probeVirtualRouterSpeedReceiveState.sessions[session.RequestID] = session
	probeVirtualRouterSpeedReceiveState.mu.Unlock()
}

func recordProbeVirtualRouterSpeedChunk(requestID string, bytes int64) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || bytes <= 0 {
		return
	}
	now := time.Now()
	probeVirtualRouterSpeedReceiveState.mu.Lock()
	session := probeVirtualRouterSpeedReceiveState.sessions[requestID]
	if session == nil {
		session = &probeVirtualRouterSpeedReceiveSession{RequestID: requestID, LastAt: now}
		if probeVirtualRouterSpeedReceiveState.sessions == nil {
			probeVirtualRouterSpeedReceiveState.sessions = make(map[string]*probeVirtualRouterSpeedReceiveSession)
		}
		probeVirtualRouterSpeedReceiveState.sessions[requestID] = session
	}
	shouldStartTimer := false
	if session.Frames == 0 || session.StartedAt.IsZero() {
		session.StartedAt = now
		shouldStartTimer = true
	}
	session.LastAt = now
	session.Bytes += bytes
	session.Frames++
	maxDuration := time.Duration(session.MaxDurationMS) * time.Millisecond
	if maxDuration <= 0 || maxDuration > probeVirtualRouterSpeedTestMaxDuration {
		maxDuration = probeVirtualRouterSpeedTestMaxDuration
	}
	localNodeID := session.LocalNodeID
	if localNodeID == "" {
		localNodeID = currentProbeVirtualRouterLocalNodeID()
	}
	if shouldStartTimer && !session.TimerStarted {
		session.TimerStarted = true
		go completeProbeVirtualRouterSpeedReceiveAfter(requestID, localNodeID, maxDuration+500*time.Millisecond)
	}
	probeVirtualRouterSpeedReceiveState.mu.Unlock()
}

func finishProbeVirtualRouterSpeedReceive(msg probeVirtualRouterSpeedTestResultPayload, localNodeID string) (probeVirtualRouterSpeedTestResultPayload, bool) {
	result, ok := finalizeProbeVirtualRouterSpeedReceive(strings.TrimSpace(msg.RequestID), localNodeID, msg)
	return result, ok
}

func finalizeProbeVirtualRouterSpeedReceive(requestID string, localNodeID string, fallback probeVirtualRouterSpeedTestResultPayload) (probeVirtualRouterSpeedTestResultPayload, bool) {
	now := time.Now()
	requestID = strings.TrimSpace(requestID)
	probeVirtualRouterSpeedReceiveState.mu.Lock()
	session := probeVirtualRouterSpeedReceiveState.sessions[requestID]
	if session != nil {
		delete(probeVirtualRouterSpeedReceiveState.sessions, requestID)
	}
	probeVirtualRouterSpeedReceiveState.mu.Unlock()
	if session == nil {
		return probeVirtualRouterSpeedTestResultPayload{}, false
	}
	result := fallback
	if result.RequestID == "" {
		result.RequestID = session.RequestID
	}
	if result.Direction == "" {
		result.Direction = session.Direction
	}
	if result.SourceNodeID == "" {
		result.SourceNodeID = session.SourceNodeID
	}
	if result.TargetNodeID == "" {
		result.TargetNodeID = session.TargetNodeID
	}
	if result.ResultNodeID == "" {
		result.ResultNodeID = session.ResultNodeID
	}
	if len(result.Path) == 0 {
		result.Path = append([]string(nil), session.Path...)
	}
	result.OK = true
	result.Error = ""
	result.Responder = normalizeProbeRouteNodeID(localNodeID)
	result.RuntimeRouteID = strings.TrimSpace(session.RouteID)
	result.Bytes = session.Bytes
	result.Frames = session.Frames
	if !session.StartedAt.IsZero() && session.Frames > 0 {
		result.DurationMS = probeDurationMilliseconds(now.Sub(session.StartedAt))
		if result.DurationMS <= 0 {
			result.DurationMS = 1
		}
	}
	result.Mbps = probeVirtualRouterSpeedMbps(result.Bytes, result.DurationMS)
	if result.ResultNodeID == "" {
		result.ResultNodeID = normalizeProbeRouteNodeID(firstNonEmpty(fallback.ResultNodeID, fallback.SourceNodeID))
	}
	return result, true
}

func completeProbeVirtualRouterSpeedReceiveAfter(requestID string, localNodeID string, delay time.Duration) {
	if delay <= 0 {
		delay = probeVirtualRouterSpeedTestMaxDuration
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	<-timer.C
	result, ok := finalizeProbeVirtualRouterSpeedReceive(requestID, localNodeID, probeVirtualRouterSpeedTestResultPayload{})
	if !ok {
		return
	}
	if normalizeProbeRouteNodeID(result.ResultNodeID) == normalizeProbeRouteNodeID(localNodeID) {
		recordProbeVirtualRouterRuntimeSpeedTestReceive(result.RuntimeRouteID, result)
		completeProbeVirtualRouterSpeedResponse(result)
		return
	}
	recordProbeVirtualRouterRuntimeSpeedTestReceive(result.RuntimeRouteID, result)
	result.Path = probeVirtualRouterReversePath(result.Path)
	if err := forwardProbeVirtualRouterSpeedAlongPath(probeVirtualRouterSpeedSubTypeResult, result, result.Path); err != nil {
		log.Printf("probe virtual router speed timed result forward failed: request_id=%s path=%s err=%v", strings.TrimSpace(result.RequestID), strings.Join(result.Path, ">"), err)
	}
}

func forwardProbeVirtualRouterSpeedAlongPath(subType uint16, msg probeVirtualRouterSpeedTestResultPayload, path []string) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeSpeed, subType, payload, path)
}

func queryProbeVirtualRouterAdjacentPing(rt *probeVirtualRouterRuntime, direction string, targetNodeID string) (probeVirtualRouterControlProbePayload, error) {
	if rt == nil {
		return probeVirtualRouterControlProbePayload{}, errors.New("runtime is nil")
	}
	targetNodeID = normalizeProbeRouteNodeID(targetNodeID)
	if targetNodeID == "" {
		return probeVirtualRouterControlProbePayload{}, errors.New("adjacent virtual router node is unavailable")
	}
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(rt)
	path := []string{localNodeID, targetNodeID}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, "", path)
	if err != nil {
		return probeVirtualRouterControlProbePayload{}, err
	}
	requestID := newProbeTCPDebugFlowID("vrouter_control_ping", rt.cfg.routeID)
	waiter := registerProbeVirtualRouterControlResponse(requestID)
	defer unregisterProbeVirtualRouterControlResponse(requestID)
	startedAt := time.Now()
	msg := probeVirtualRouterControlProbePayload{
		RequestID:         requestID,
		SourceNodeID:      localNodeID,
		TargetNodeID:      targetNodeID,
		Path:              path,
		CreatedAtUnixNano: startedAt.UnixNano(),
		PingBytes:         probeVirtualRouterPingPongBytes,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return probeVirtualRouterControlProbePayload{}, err
	}
	if err := enqueueProbeVirtualRouterBusinessFrame(link, probeVirtualRouterFrameMainTypePingPong, probeVirtualRouterPingPongSubTypePing, payload, path); err != nil {
		return probeVirtualRouterControlProbePayload{}, err
	}
	response, err := waitProbeVirtualRouterControlResponse(waiter, probeVirtualRouterPingPongTimeout)
	if err != nil {
		return probeVirtualRouterControlProbePayload{}, fmt.Errorf("%w: request_id=%s target=%s direction=%s path=%s %s", err, requestID, targetNodeID, normalizeProbeRouteBridgeRole(direction), strings.Join(path, ">"), probeVirtualRouterFrameLinkDebugState(link))
	}
	response.LatencyMS = probeVirtualRouterAdjacentLatencyMilliseconds(time.Since(startedAt))
	return response, nil
}

func probeVirtualRouterAdjacentLatencyMilliseconds(elapsed time.Duration) int64 {
	return probeDurationMilliseconds(elapsed / 2)
}

func queryProbeVirtualRouterPathRTTControl(path []string) (probeVirtualRouterPathRTTQueryResponse, error) {
	cleanPath := cleanProbeVirtualRouterPath(path)
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if len(cleanPath) < 2 || localNodeID == "" || cleanPath[0] != localNodeID {
		return probeVirtualRouterPathRTTQueryResponse{}, errors.New("virtual router rtt query path must start at local node")
	}
	targetNodeID := cleanPath[len(cleanPath)-1]
	nextNodeID := probeVirtualRouterNextHopInPath(cleanPath, localNodeID)
	if nextNodeID == "" {
		return probeVirtualRouterPathRTTQueryResponse{}, errors.New("next virtual router rtt hop is unavailable")
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return probeVirtualRouterPathRTTQueryResponse{}, errors.New("adjacent virtual router rtt runtime is unavailable")
	}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, "", cleanPath)
	if err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	requestID := newProbeTCPDebugFlowID("vrouter_path_rtt", strings.Join(cleanPath, ">"))
	waiter := registerProbeVirtualRouterControlResponse(requestID)
	defer unregisterProbeVirtualRouterControlResponse(requestID)
	msg := probeVirtualRouterControlProbePayload{
		RequestID:         requestID,
		SourceNodeID:      localNodeID,
		TargetNodeID:      targetNodeID,
		Path:              cleanPath,
		CreatedAtUnixNano: time.Now().UnixNano(),
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	if err := enqueueProbeVirtualRouterBusinessFrame(link, probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterPathRTTSubTypeQuery, payload, cleanPath); err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	response, err := waitProbeVirtualRouterControlResponse(waiter, probeVirtualRouterPingPongTimeout)
	if err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	return probeVirtualRouterPathRTTQueryResponse{
		OK:        response.OK,
		LatencyMS: response.LatencyMS,
		Error:     response.Error,
		Responder: response.Responder,
	}, nil
}

func runProbeVirtualRouterSpeedTest(sourceNodeID string, targetNodeID string, maxBytes int64, maxDuration time.Duration) (probeVirtualRouterSpeedTestResult, probeVirtualRouterSpeedTestResult, string, error) {
	sourceNodeID = normalizeProbeRouteNodeID(sourceNodeID)
	targetNodeID = normalizeProbeRouteNodeID(targetNodeID)
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if sourceNodeID == "" {
		sourceNodeID = localNodeID
	}
	if sourceNodeID == "" || targetNodeID == "" {
		return probeVirtualRouterSpeedTestResult{}, probeVirtualRouterSpeedTestResult{}, "", errors.New("source and target virtual router nodes are required")
	}
	if sourceNodeID != localNodeID {
		return probeVirtualRouterSpeedTestResult{}, probeVirtualRouterSpeedTestResult{}, "", errors.New("virtual router speed test must run on the selected source node")
	}
	if sourceNodeID == targetNodeID {
		return probeVirtualRouterSpeedTestResult{}, probeVirtualRouterSpeedTestResult{}, "", errors.New("source and target virtual router nodes must be different")
	}
	if maxBytes <= 0 || maxBytes > probeVirtualRouterSpeedTestMaxBytes {
		maxBytes = probeVirtualRouterSpeedTestMaxBytes
	}
	if maxDuration <= 0 || maxDuration > probeVirtualRouterSpeedTestMaxDuration {
		maxDuration = probeVirtualRouterSpeedTestMaxDuration
	}
	path := currentProbeVirtualRouterPathBetweenNodes(sourceNodeID, targetNodeID)
	if len(path) < 2 {
		return probeVirtualRouterSpeedTestResult{}, probeVirtualRouterSpeedTestResult{}, "", errors.New("virtual router speed test path is unavailable")
	}
	up, upErr := runProbeVirtualRouterOneWaySpeedTest(path, "up", sourceNodeID, targetNodeID, maxBytes, maxDuration)
	down, downErr := runProbeVirtualRouterReverseSpeedTest(path, sourceNodeID, targetNodeID, maxBytes, maxDuration)
	pathText := strings.Join(path, ">")
	var err error
	if upErr != nil && downErr != nil {
		err = fmt.Errorf("up: %v; down: %v", upErr, downErr)
	} else if upErr != nil {
		err = fmt.Errorf("up: %v", upErr)
	} else if downErr != nil {
		err = fmt.Errorf("down: %v", downErr)
	}
	routeID := ""
	if nextNodeID := probeVirtualRouterNextHopInPath(path, localNodeID); nextNodeID != "" {
		if rt, _ := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID); rt != nil {
			routeID = strings.TrimSpace(rt.cfg.routeID)
		}
	}
	recordProbeVirtualRouterRuntimeSpeedTest(routeID, sourceNodeID, targetNodeID, pathText, up, down, err)
	return up, down, pathText, err
}

func runProbeVirtualRouterOneWaySpeedTest(path []string, directionName string, sourceNodeID string, targetNodeID string, maxBytes int64, maxDuration time.Duration) (probeVirtualRouterSpeedTestResult, error) {
	requestID := newProbeTCPDebugFlowID("vrouter_speed_"+directionName, strings.Join(path, ">"))
	waiter := registerProbeVirtualRouterSpeedResponse(requestID)
	defer unregisterProbeVirtualRouterSpeedResponse(requestID)
	msg := probeVirtualRouterSpeedTestResultPayload{
		RequestID:         requestID,
		Direction:         directionName,
		SourceNodeID:      sourceNodeID,
		TargetNodeID:      targetNodeID,
		ResultNodeID:      sourceNodeID,
		Path:              append([]string(nil), path...),
		MaxBytes:          maxBytes,
		MaxDurationMS:     probeDurationMilliseconds(maxDuration),
		CreatedAtUnixNano: time.Now().UnixNano(),
	}
	if err := runProbeVirtualRouterOneWaySpeedSender(path, msg, maxDuration); err != nil {
		return probeVirtualRouterSpeedTestResult{OK: false, Error: err.Error()}, err
	}
	response, err := waitProbeVirtualRouterSpeedResponse(waiter, maxDuration+10*time.Second)
	if err != nil {
		return probeVirtualRouterSpeedTestResult{OK: false, Error: err.Error()}, err
	}
	result := probeVirtualRouterSpeedTestResult{
		OK:         response.OK,
		Error:      strings.TrimSpace(response.Error),
		Bytes:      response.Bytes,
		Frames:     response.Frames,
		DurationMS: response.DurationMS,
		Mbps:       response.Mbps,
	}
	if !response.OK && result.Error == "" {
		result.Error = "virtual router speed test failed"
	}
	return result, nil
}

func runProbeVirtualRouterReverseSpeedTest(path []string, sourceNodeID string, targetNodeID string, maxBytes int64, maxDuration time.Duration) (probeVirtualRouterSpeedTestResult, error) {
	requestID := newProbeTCPDebugFlowID("vrouter_speed_down", strings.Join(path, ">"))
	waiter := registerProbeVirtualRouterSpeedResponse(requestID)
	defer unregisterProbeVirtualRouterSpeedResponse(requestID)
	msg := probeVirtualRouterSpeedTestResultPayload{
		RequestID:         requestID,
		Direction:         "down",
		SourceNodeID:      sourceNodeID,
		TargetNodeID:      targetNodeID,
		ResultNodeID:      sourceNodeID,
		Path:              append([]string(nil), path...),
		MaxBytes:          maxBytes,
		MaxDurationMS:     probeDurationMilliseconds(maxDuration),
		CreatedAtUnixNano: time.Now().UnixNano(),
	}
	if err := forwardProbeVirtualRouterSpeedAlongPath(probeVirtualRouterSpeedSubTypeSend, msg, path); err != nil {
		return probeVirtualRouterSpeedTestResult{OK: false, Error: err.Error()}, err
	}
	response, err := waitProbeVirtualRouterSpeedResponse(waiter, maxDuration+10*time.Second)
	if err != nil {
		return probeVirtualRouterSpeedTestResult{OK: false, Error: err.Error()}, err
	}
	result := probeVirtualRouterSpeedTestResult{
		OK:         response.OK,
		Error:      strings.TrimSpace(response.Error),
		Bytes:      response.Bytes,
		Frames:     response.Frames,
		DurationMS: response.DurationMS,
		Mbps:       response.Mbps,
	}
	if !response.OK && result.Error == "" {
		result.Error = "virtual router speed test failed"
	}
	return result, nil
}

func runProbeVirtualRouterOneWaySpeedSender(path []string, msg probeVirtualRouterSpeedTestResultPayload, maxDuration time.Duration) error {
	cleanPath := cleanProbeVirtualRouterPath(path)
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" || len(cleanPath) < 2 || cleanPath[0] != localNodeID {
		return errors.New("virtual router speed sender path must start at local node")
	}
	nextNodeID := probeVirtualRouterNextHopInPath(cleanPath, localNodeID)
	if nextNodeID == "" {
		return errors.New("next virtual router speed hop is unavailable")
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return errors.New("adjacent virtual router speed runtime is unavailable")
	}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, "", cleanPath)
	if err != nil {
		return err
	}
	msg.Path = cleanPath
	msg.MaxBytes = normalizeProbeVirtualRouterSpeedMaxBytes(msg.MaxBytes)
	if maxDuration <= 0 || maxDuration > probeVirtualRouterSpeedTestMaxDuration {
		maxDuration = probeVirtualRouterSpeedTestMaxDuration
	}
	if msg.MaxDurationMS <= 0 {
		msg.MaxDurationMS = probeDurationMilliseconds(maxDuration)
	}
	startPayload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := enqueueProbeVirtualRouterBusinessFrameUntil(link, probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterSpeedSubTypeStart, startPayload, cleanPath, time.Now().Add(2*time.Second)); err != nil {
		return err
	}
	deadline := time.Now().Add(maxDuration)
	var sentBytes int64
	var frames int64
	for sentBytes < msg.MaxBytes && time.Now().Before(deadline) {
		size := int64(probeVirtualRouterSpeedTestChunkBytes)
		if remain := msg.MaxBytes - sentBytes; remain < size {
			size = remain
		}
		payload := buildProbeVirtualRouterSpeedChunkPayload(msg.RequestID, int(size))
		if err := enqueueProbeVirtualRouterBusinessFrameUntil(link, probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterSpeedSubTypeChunk, payload, cleanPath, deadline); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}
			return err
		}
		sentBytes += int64(len(payload))
		frames++
	}
	msg.Bytes = sentBytes
	msg.Frames = frames
	finishPayload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return enqueueProbeVirtualRouterBusinessFrameUntil(link, probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterSpeedSubTypeFinish, finishPayload, cleanPath, time.Now().Add(2*time.Second))
}

func normalizeProbeVirtualRouterSpeedMaxBytes(value int64) int64 {
	if value <= 0 || value > probeVirtualRouterSpeedTestMaxBytes {
		return probeVirtualRouterSpeedTestMaxBytes
	}
	return value
}

func buildProbeVirtualRouterSpeedChunkPayload(requestID string, size int) []byte {
	requestID = strings.TrimSpace(requestID)
	headerSize := 6 + len(requestID)
	if size < headerSize {
		size = headerSize
	}
	payload := make([]byte, size)
	copy(payload[0:4], []byte("VRS1"))
	binary.BigEndian.PutUint16(payload[4:6], uint16(len(requestID)))
	copy(payload[6:6+len(requestID)], []byte(requestID))
	return payload
}

func parseProbeVirtualRouterSpeedChunkRequestID(payload []byte) (string, bool) {
	if len(payload) < 6 || string(payload[0:4]) != "VRS1" {
		return "", false
	}
	size := int(binary.BigEndian.Uint16(payload[4:6]))
	if size <= 0 || 6+size > len(payload) {
		return "", false
	}
	requestID := strings.TrimSpace(string(payload[6 : 6+size]))
	return requestID, requestID != ""
}

func probeVirtualRouterSpeedMbps(bytes int64, durationMS int64) float64 {
	if bytes <= 0 || durationMS <= 0 {
		return 0
	}
	return float64(bytes*8) / (float64(durationMS) / 1000) / 1000 / 1000
}

func forwardProbeVirtualRouterBusinessAlongPath(mainType uint16, subType uint16, payload []byte, path []string) error {
	cleanPath := cleanProbeVirtualRouterPath(path)
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" || len(cleanPath) < 2 {
		return errors.New("virtual router business frame path is incomplete")
	}
	nextNodeID := probeVirtualRouterNextHopInPath(cleanPath, localNodeID)
	if nextNodeID == "" {
		return errors.New("next virtual router business frame hop is unavailable")
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return errors.New("adjacent virtual router business frame runtime is unavailable")
	}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, "", cleanPath)
	if err != nil {
		return err
	}
	return enqueueProbeVirtualRouterBusinessFrame(link, mainType, subType, payload, cleanPath)
}

func enqueueProbeVirtualRouterBusinessFrame(link *probeVirtualRouterFrameLink, mainType uint16, subType uint16, payload []byte, path []string) error {
	if link == nil {
		return errors.New("virtual router physical carrier is unavailable")
	}
	frame, err := buildProbeVirtualRouterBusinessFrame(mainType, subType, payload, path, nil)
	if err != nil {
		return err
	}
	return link.EnqueueProbeVirtualRouterFrame(frame)
}

func enqueueProbeVirtualRouterBusinessFrameUntil(link *probeVirtualRouterFrameLink, mainType uint16, subType uint16, payload []byte, path []string, deadline time.Time) error {
	if link == nil {
		return errors.New("virtual router physical carrier is unavailable")
	}
	if link.tx == nil || link.done == nil {
		return enqueueProbeVirtualRouterBusinessFrame(link, mainType, subType, payload, path)
	}
	frame, err := buildProbeVirtualRouterBusinessFrame(mainType, subType, payload, path, nil)
	if err != nil {
		return err
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		return os.ErrDeadlineExceeded
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case link.tx <- frame:
		link.touch()
		return nil
	case <-link.done:
		return io.ErrClosedPipe
	case <-timer.C:
		return os.ErrDeadlineExceeded
	}
}

func registerProbeVirtualRouterControlResponse(requestID string) chan probeVirtualRouterControlProbePayload {
	requestID = strings.TrimSpace(requestID)
	ch := make(chan probeVirtualRouterControlProbePayload, 1)
	if requestID == "" {
		return ch
	}
	probeVirtualRouterControlResponseState.mu.Lock()
	if probeVirtualRouterControlResponseState.pending == nil {
		probeVirtualRouterControlResponseState.pending = make(map[string]chan probeVirtualRouterControlProbePayload)
	}
	probeVirtualRouterControlResponseState.pending[requestID] = ch
	probeVirtualRouterControlResponseState.mu.Unlock()
	return ch
}

func unregisterProbeVirtualRouterControlResponse(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterControlResponseState.mu.Lock()
	delete(probeVirtualRouterControlResponseState.pending, requestID)
	probeVirtualRouterControlResponseState.mu.Unlock()
}

func waitProbeVirtualRouterControlResponse(ch chan probeVirtualRouterControlProbePayload, timeout time.Duration) (probeVirtualRouterControlProbePayload, error) {
	if ch == nil {
		return probeVirtualRouterControlProbePayload{}, errors.New("virtual router control response waiter is nil")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-ch:
		return response, nil
	case <-timer.C:
		return probeVirtualRouterControlProbePayload{}, errors.New("virtual router control response timeout")
	}
}

func completeProbeVirtualRouterControlResponse(msg probeVirtualRouterControlProbePayload) {
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterControlResponseState.mu.Lock()
	ch := probeVirtualRouterControlResponseState.pending[requestID]
	probeVirtualRouterControlResponseState.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func registerProbeVirtualRouterSpeedResponse(requestID string) chan probeVirtualRouterSpeedTestResultPayload {
	requestID = strings.TrimSpace(requestID)
	ch := make(chan probeVirtualRouterSpeedTestResultPayload, 1)
	if requestID == "" {
		return ch
	}
	probeVirtualRouterSpeedResponseState.mu.Lock()
	if probeVirtualRouterSpeedResponseState.pending == nil {
		probeVirtualRouterSpeedResponseState.pending = make(map[string]chan probeVirtualRouterSpeedTestResultPayload)
	}
	probeVirtualRouterSpeedResponseState.pending[requestID] = ch
	probeVirtualRouterSpeedResponseState.mu.Unlock()
	return ch
}

func unregisterProbeVirtualRouterSpeedResponse(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterSpeedResponseState.mu.Lock()
	delete(probeVirtualRouterSpeedResponseState.pending, requestID)
	probeVirtualRouterSpeedResponseState.mu.Unlock()
}

func waitProbeVirtualRouterSpeedResponse(ch chan probeVirtualRouterSpeedTestResultPayload, timeout time.Duration) (probeVirtualRouterSpeedTestResultPayload, error) {
	if ch == nil {
		return probeVirtualRouterSpeedTestResultPayload{}, errors.New("virtual router speed response waiter is nil")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-ch:
		return response, nil
	case <-timer.C:
		return probeVirtualRouterSpeedTestResultPayload{}, errors.New("virtual router speed response timeout")
	}
}

func completeProbeVirtualRouterSpeedResponse(msg probeVirtualRouterSpeedTestResultPayload) {
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterSpeedResponseState.mu.Lock()
	ch := probeVirtualRouterSpeedResponseState.pending[requestID]
	probeVirtualRouterSpeedResponseState.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func cleanProbeVirtualRouterPath(path []string) []string {
	out := make([]string, 0, len(path))
	for _, item := range path {
		if nodeID := normalizeProbeRouteNodeID(item); nodeID != "" {
			out = append(out, nodeID)
		}
	}
	return out
}

func probeVirtualRouterPreviousHopInPath(path []string, localNodeID string) string {
	local := normalizeProbeRouteNodeID(localNodeID)
	if local == "" || len(path) < 2 {
		return ""
	}
	for i, item := range path {
		if normalizeProbeRouteNodeID(item) != local {
			continue
		}
		if i > 0 {
			return normalizeProbeRouteNodeID(path[i-1])
		}
		return ""
	}
	return ""
}

func handleProbeVirtualRouterIPFrame(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, packet []byte, path []string, trace []probeVirtualRouterFrameTraceHop) error {
	recordProbeVirtualRouterRuntimeFrameReceived(runtime, len(packet))
	dstIP := probeVirtualRouterIPv4Destination(packet)
	if dstIP == "" {
		return errors.New("virtual router frame destination is invalid")
	}
	recordProbeVirtualRouterRuntimePacketReceived(runtime, len(packet))
	if len(path) == 0 {
		path = currentProbeVirtualRouterPathToIP(dstIP)
	}
	srcIP := probeVirtualRouterIPv4Source(packet)
	localIP := currentProbeVirtualRouterLocalIPForRuntime(runtime)
	localMatch := probeVirtualRouterPacketTargetsLocalDelivery(runtime, dstIP, path)
	if !localMatch && probeVirtualRouterFrameTargetsLocalFakeIP(dstIP, path, currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)) {
		scheduleProbeVirtualRouterRouteConfigRefreshFromController("fake_ip_exit_delivery_miss", probeVirtualRouterRouteConfigRefreshHotPathMinInterval)
	}
	recordProbeVirtualRouterRuntimeFrameDecision(runtime, srcIP, dstIP, localIP, path, localMatch)
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "frame_rx", "", "")
		log.Printf("probe virtual router icmp frame rx: trace_code=icmp-trace-v2 route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s local_match=%v path=%s bytes=%d trace_hops=%d", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, localMatch, strings.Join(path, ">"), len(packet), len(trace))
	}
	if localMatch {
		if handleProbeVirtualRouterLocalICMPEchoRequest(runtime, link, packet, path, trace) {
			recordProbeVirtualRouterRuntimePacketDelivered(runtime, len(packet))
			recordProbeVirtualRouterRecentPacket("frame_rx", "local_icmp", runtime, packet, path, true, nil)
			return nil
		}
		if handleProbeVirtualRouterFakeIPExitPacket(runtime, link, packet, path) {
			recordProbeVirtualRouterRuntimePacketDelivered(runtime, len(packet))
			recordProbeVirtualRouterRecentPacket("frame_rx", "fake_exit", runtime, packet, path, true, nil)
			return nil
		}
		deliverStartedAt := time.Now()
		if err := writeProbeVirtualRouterLocalTUNPacket(packet); err != nil {
			recordProbeVirtualRouterRuntimeDeliveryError(runtime, err)
			recordProbeVirtualRouterRecentPacket("frame_rx", "deliver_error", runtime, packet, path, true, err)
			if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
				log.Printf("probe virtual router icmp local deliver failed: route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, err)
			}
			if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
				log.Printf("probe virtual router transport local deliver failed: route=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d local_ip=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, localIP, err)
			}
			return err
		}
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			if info.Kind == "echo_reply" {
				trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "echo_reply_source", "", "")
				trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "local_deliver", "", "")
				log.Printf("probe virtual router icmp local deliver ok: trace_code=icmp-trace-v2 route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s write_ms=%d bytes=%d trace_hops=%d", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, probeDurationMilliseconds(time.Since(deliverStartedAt)), len(packet), len(trace))
				summary, completed := recordProbeVirtualRouterICMPPingReply(runtime, info)
				if completed {
					logProbeVirtualRouterICMPPingSummary(runtime, info, trace, summary)
				} else {
					logProbeVirtualRouterICMPTraceComplete(runtime, info, trace)
				}
			} else {
				log.Printf("probe virtual router icmp local deliver ok: route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s write_ms=%d bytes=%d", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, probeDurationMilliseconds(time.Since(deliverStartedAt)), len(packet))
			}
		}
		recordProbeVirtualRouterRuntimePacketDelivered(runtime, len(packet))
		recordProbeVirtualRouterRecentPacket("frame_rx", "deliver", runtime, packet, path, true, nil)
		return nil
	}
	if len(path) < 2 {
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp frame drop: route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d reason=path_incomplete local_ip=%s local_match=%v path=%s", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, localMatch, strings.Join(path, ">"))
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport frame drop: route=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d reason=path_incomplete local_ip=%s local_match=%v path=%s", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, localIP, localMatch, strings.Join(path, ">"))
		}
		recordProbeVirtualRouterRecentPacket("frame_rx", "drop", runtime, packet, path, false, errors.New("path incomplete"))
		return errors.New("virtual router path is incomplete")
	}
	if err := probeVirtualRouterFakeIPForwardUnavailableError(dstIP, path, currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)); err != nil {
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp frame drop: route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d reason=fake_ip_exit_unreachable local_ip=%s local_match=%v path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, localMatch, strings.Join(path, ">"), err)
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport frame drop: route=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d reason=fake_ip_exit_unreachable local_ip=%s local_match=%v path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, localIP, localMatch, strings.Join(path, ">"), err)
		}
		recordProbeVirtualRouterRecentPacket("frame_rx", "drop", runtime, packet, path, false, err)
		return err
	}
	if err := forwardProbeVirtualRouterPacketAlongPath(packet, dstIP, path, trace); err != nil {
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp frame forward failed: route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), err)
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport frame forward failed: route=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, strings.Join(path, ">"), err)
		}
		recordProbeVirtualRouterRecentPacket("frame_rx", "forward_error", runtime, packet, path, false, err)
		return err
	}
	recordProbeVirtualRouterRecentPacket("frame_rx", "forward", runtime, packet, path, false, nil)
	return nil
}

func probeVirtualRouterFrameTargetsLocalFakeIP(dstIP string, path []string, localNodeID string) bool {
	if !probeVirtualRouterIPInCurrentFakeCIDR(dstIP) {
		return false
	}
	local := normalizeProbeRouteNodeID(localNodeID)
	if local == "" || len(path) == 0 {
		return false
	}
	return normalizeProbeRouteNodeID(path[len(path)-1]) == local
}

func writeProbeVirtualRouterLocalTUNPacket(packet []byte) error {
	normalizeProbeVirtualRouterLocalTUNPacketChecksums(packet)
	if err := writeProbeVirtualRouterTUNPacket(packet); err == nil {
		return nil
	} else {
		firstErr := err
		if startErr := startProbeVirtualRouterTUNDataPlane(); startErr != nil {
			return fmt.Errorf("write local tun packet failed: %w; restart data plane failed: %v", firstErr, startErr)
		}
		if retryErr := writeProbeVirtualRouterTUNPacket(packet); retryErr != nil {
			return fmt.Errorf("write local tun packet failed after data plane restart: %w (initial: %v)", retryErr, firstErr)
		}
		return nil
	}
}

var probeVirtualRouterLocalTUNPacketWriter func([]byte) error

func normalizeProbeVirtualRouterLocalTUNPacketChecksums(packet []byte) {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl {
		return
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl {
		return
	}
	packet[10], packet[11] = 0, 0
	binary.BigEndian.PutUint16(packet[10:12], probeVirtualRouterChecksum(packet[:ihl]))
	transport := packet[ihl:totalLen]
	switch packet[9] {
	case 6:
		if len(transport) < 20 {
			return
		}
		transport[16], transport[17] = 0, 0
		binary.BigEndian.PutUint16(transport[16:18], probeVirtualRouterTransportChecksum(packet, transport))
	case 17:
		if len(transport) < 8 {
			return
		}
		transport[6], transport[7] = 0, 0
		binary.BigEndian.PutUint16(transport[6:8], probeVirtualRouterTransportChecksum(packet, transport))
	}
}

func forwardProbeVirtualRouterPacketAlongPath(packet []byte, dstIP string, path []string, trace []probeVirtualRouterFrameTraceHop) error {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" && len(path) > 0 {
		localNodeID = normalizeProbeRouteNodeID(path[0])
	}
	if localNodeID == "" {
		return errors.New("local virtual router node id is empty")
	}
	nextNodeID := probeVirtualRouterNextHopInPath(path, localNodeID)
	if nextNodeID == "" {
		return errors.New("next virtual router hop is unavailable")
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return errors.New("adjacent virtual router runtime is unavailable")
	}
	if _, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		trace = appendProbeVirtualRouterICMPTrace(trace, rt, "forward_tx", direction, nextNodeID)
	}
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp forward enqueue: trace_code=icmp-trace-v2 route=%s local_node=%s next_node=%s direction=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s bytes=%d", probeVirtualRouterRuntimeLogRouteID(rt), localNodeID, nextNodeID, direction, info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), len(packet))
	}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, dstIP, path)
	if err != nil {
		return err
	}
	if err := writeProbeVirtualRouterIPFrame(link, packet, path, trace); err != nil {
		recordProbeVirtualRouterRuntimeOpenError(rt.cfg.routeID, err)
		if !isProbeVirtualRouterClosedLinkError(err) {
			return err
		}
		link, err = ensureProbeVirtualRouterFrameLink(rt, direction, dstIP, path)
		if err != nil {
			recordProbeVirtualRouterRuntimeOpenError(rt.cfg.routeID, err)
			return err
		}
		if err := writeProbeVirtualRouterIPFrame(link, packet, path, trace); err != nil {
			recordProbeVirtualRouterRuntimeOpenError(rt.cfg.routeID, err)
			return err
		}
	}
	recordProbeVirtualRouterRuntimeFrameSent(rt, len(packet))
	recordProbeVirtualRouterRuntimePacketForwarded(rt, len(packet))
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp forward queued: trace_code=icmp-trace-v2 route=%s local_node=%s next_node=%s direction=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s bytes=%d", probeVirtualRouterRuntimeLogRouteID(rt), localNodeID, nextNodeID, direction, info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), len(packet))
	}
	return nil
}

func handleProbeVirtualRouterLocalICMPEchoRequest(runtime *probeVirtualRouterRuntime, stream *probeVirtualRouterFrameLink, packet []byte, ingressPath []string, trace []probeVirtualRouterFrameTraceHop) bool {
	localIP := currentProbeVirtualRouterLocalIPForRuntime(runtime)
	reply, dstIP, ok := buildProbeVirtualRouterICMPEchoReply(packet, localIP)
	if !ok {
		return false
	}
	trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "echo_request_final", "", "")
	trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "echo_reply_build", "", "")
	path := probeVirtualRouterReversePath(ingressPath)
	if len(path) < 2 {
		path = currentProbeVirtualRouterPathForPacket(reply, dstIP)
	}
	if len(path) < 2 {
		log.Printf("probe virtual router icmp echo reply path unavailable: dst=%s", dstIP)
		return false
	}
	if reqInfo, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		if replyInfo, replyOK := probeVirtualRouterParseICMPEchoLogInfo(reply); replyOK {
			log.Printf("probe virtual router icmp echo reply build: route=%s runtime_node=%s request_src=%s request_dst=%s reply_src=%s reply_dst=%s id=%d seq=%d local_ip=%s path=%s", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), reqInfo.SourceIP, reqInfo.DestinationIP, replyInfo.SourceIP, replyInfo.DestinationIP, reqInfo.ID, reqInfo.Sequence, localIP, strings.Join(path, ">"))
		}
	}
	if err := writeProbeVirtualRouterIPFrame(stream, reply, path, trace); err != nil {
		if runtime != nil {
			recordProbeVirtualRouterRuntimeOpenError(runtime.cfg.routeID, err)
		}
		log.Printf("probe virtual router icmp echo reply write failed: dst=%s path=%s err=%v", dstIP, strings.Join(path, ">"), err)
		return false
	}
	recordProbeVirtualRouterRuntimeFrameSent(runtime, len(reply))
	recordProbeVirtualRouterRuntimePacketForwarded(runtime, len(reply))
	return true
}

func probeVirtualRouterReversePath(path []string) []string {
	if len(path) == 0 {
		return nil
	}
	out := make([]string, 0, len(path))
	for i := len(path) - 1; i >= 0; i-- {
		if nodeID := normalizeProbeRouteNodeID(path[i]); nodeID != "" {
			out = append(out, nodeID)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

func ensureProbeVirtualRouterFrameLink(rt *probeVirtualRouterRuntime, direction string, dstIP string, path []string) (*probeVirtualRouterFrameLink, error) {
	if rt == nil {
		return nil, errors.New("runtime is nil")
	}
	key := probeVirtualRouterFrameLinkKey(rt, direction, dstIP, path)
	if key == "" {
		return nil, errors.New("frame link key is empty")
	}
	now := time.Now()
	if stream := reusableProbeVirtualRouterFrameLink(key, now); stream != nil {
		return stream, nil
	}
	link := newProbeVirtualRouterFrameLink(key, rt, nil, path)
	link.Start()
	probeVirtualRouterFrameLinkState.mu.Lock()
	if existing := probeVirtualRouterFrameLinkState.links[key]; existing != nil && !isProbeVirtualRouterFrameLinkClosed(existing) {
		probeVirtualRouterFrameLinkState.mu.Unlock()
		stopProbeVirtualRouterFrameLink(link)
		return existing, nil
	}
	probeVirtualRouterFrameLinkState.links[key] = link
	probeVirtualRouterFrameLinkState.mu.Unlock()
	return link, nil
}

func reusableProbeVirtualRouterFrameLink(key string, now time.Time) *probeVirtualRouterFrameLink {
	probeVirtualRouterFrameLinkState.mu.Lock()
	item := probeVirtualRouterFrameLinkState.links[key]
	if item == nil {
		probeVirtualRouterFrameLinkState.mu.Unlock()
		return nil
	}
	if isProbeVirtualRouterFrameLinkClosed(item) {
		delete(probeVirtualRouterFrameLinkState.links, key)
		probeVirtualRouterFrameLinkState.mu.Unlock()
		stopProbeVirtualRouterFrameLink(item)
		return nil
	}
	item.lastUsed = now
	probeVirtualRouterFrameLinkState.mu.Unlock()
	return item
}

func isProbeVirtualRouterFrameLinkClosed(item *probeVirtualRouterFrameLink) bool {
	if item == nil {
		return true
	}
	if item.done != nil {
		select {
		case <-item.done:
			return true
		default:
		}
	}
	return false
}

func isProbeVirtualRouterClosedLinkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.EOF) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "use of closed network connection") ||
		strings.Contains(text, "closed pipe") ||
		strings.Contains(text, "connection reset by peer") ||
		strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "websocket: close") ||
		strings.Contains(text, "abnormal closure") ||
		strings.Contains(text, "unexpected eof")
}

func dropProbeVirtualRouterFrameLink(link *probeVirtualRouterFrameLink) {
	if link == nil {
		return
	}
	dropped := false
	probeVirtualRouterFrameLinkState.mu.Lock()
	for key, item := range probeVirtualRouterFrameLinkState.links {
		if item != nil && item == link {
			delete(probeVirtualRouterFrameLinkState.links, key)
			dropped = true
			break
		}
	}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if dropped {
		stopProbeVirtualRouterFrameLink(link)
	}
}

func closeProbeVirtualRouterFrameLinks(reason string) {
	probeVirtualRouterFrameLinkState.mu.Lock()
	links := probeVirtualRouterFrameLinkState.links
	probeVirtualRouterFrameLinkState.links = make(map[string]*probeVirtualRouterFrameLink)
	probeVirtualRouterFrameLinkState.mu.Unlock()
	for _, item := range links {
		if item != nil {
			stopProbeVirtualRouterFrameLink(item)
		}
	}
	if len(links) > 0 {
		log.Printf("probe virtual router frame links closed: count=%d reason=%s", len(links), strings.TrimSpace(reason))
	}
}

func closeProbeVirtualRouterRuntimeFrameLink(rt *probeVirtualRouterRuntime) {
	if rt == nil {
		return
	}
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	if key == "" {
		return
	}
	probeVirtualRouterFrameLinkState.mu.Lock()
	item := probeVirtualRouterFrameLinkState.links[key]
	if item != nil {
		delete(probeVirtualRouterFrameLinkState.links, key)
	}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if item != nil {
		stopProbeVirtualRouterFrameLink(item)
	}
}

func probeVirtualRouterFrameLinkKey(rt *probeVirtualRouterRuntime, direction string, dstIP string, path []string) string {
	if rt == nil {
		return ""
	}
	return strings.Join([]string{
		"packet",
		strings.TrimSpace(rt.cfg.routeID),
	}, "|")
}

func probeVirtualRouterRTTQueryLinkKey(rt *probeVirtualRouterRuntime, direction string, path []string) string {
	if rt == nil {
		return ""
	}
	cleanPath := make([]string, 0, len(path))
	for _, item := range path {
		if nodeID := normalizeProbeRouteNodeID(item); nodeID != "" {
			cleanPath = append(cleanPath, nodeID)
		}
	}
	if len(cleanPath) < 2 {
		return ""
	}
	return strings.Join([]string{
		"rtt",
		strings.TrimSpace(rt.cfg.routeID),
		strings.TrimSpace(direction),
		strings.Join(cleanPath, ">"),
	}, "|")
}

func probeVirtualRouterRuntimeStatsForUpdateLocked(routeID string) *probeVirtualRouterRuntimeStats {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return nil
	}
	item := probeVirtualRouterRuntimeStatsState.items[routeID]
	if item == nil {
		item = &probeVirtualRouterRuntimeStats{}
		probeVirtualRouterRuntimeStatsState.items[routeID] = item
	}
	return item
}

func recordProbeVirtualRouterRuntimePacketForwarded(rt *probeVirtualRouterRuntime, packetBytes int) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.PacketsForwarded++
		item.BytesForwarded += int64(packetBytes)
		item.LastPacketAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimePacketReceived(rt *probeVirtualRouterRuntime, packetBytes int) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.PacketsReceived++
		item.BytesReceived += int64(packetBytes)
		item.LastPacketAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimePacketDelivered(rt *probeVirtualRouterRuntime, packetBytes int) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.PacketsDelivered++
		item.BytesDelivered += int64(packetBytes)
		item.LastPacketAt = time.Now().UTC().Format(time.RFC3339)
		item.LastDeliveryError = ""
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeFrameSent(rt *probeVirtualRouterRuntime, frameBytes int) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.FramesSent++
		item.FrameBytesSent += int64(frameBytes)
		item.LastFrameAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeFrameReceived(rt *probeVirtualRouterRuntime, frameBytes int) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.FramesReceived++
		item.FrameBytesReceived += int64(frameBytes)
		item.LastFrameAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeFrameDecision(rt *probeVirtualRouterRuntime, srcIP string, dstIP string, localIP string, path []string, localMatch bool) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.LastFrameSourceIP = strings.TrimSpace(srcIP)
		item.LastFrameDestinationIP = strings.TrimSpace(dstIP)
		item.LastFrameLocalIP = strings.TrimSpace(localIP)
		if localMatch {
			item.LastFrameLocalMatch = "yes"
		} else {
			item.LastFrameLocalMatch = "no"
		}
		item.LastFramePath = strings.Join(path, ">")
		item.LastFrameRuntimeNodeID = currentProbeVirtualRouterLocalNodeIDForRuntime(rt)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func probeVirtualRouterICMPPingKey(sourceIP string, destinationIP string, id uint16, sequence uint16) string {
	return strings.Join([]string{
		strings.TrimSpace(sourceIP),
		strings.TrimSpace(destinationIP),
		fmt.Sprintf("%d", id),
		fmt.Sprintf("%d", sequence),
	}, "|")
}

func recordProbeVirtualRouterICMPPingStart(info probeVirtualRouterICMPEchoLogInfo, path []string) {
	key := probeVirtualRouterICMPPingKey(info.SourceIP, info.DestinationIP, info.ID, info.Sequence)
	if key == "" {
		return
	}
	now := time.Now()
	pending := probeVirtualRouterICMPPingPending{
		StartedAt:     now,
		SourceIP:      strings.TrimSpace(info.SourceIP),
		DestinationIP: strings.TrimSpace(info.DestinationIP),
		ID:            info.ID,
		Sequence:      info.Sequence,
		Path:          strings.Join(path, ">"),
	}
	probeVirtualRouterICMPPingState.mu.Lock()
	if probeVirtualRouterICMPPingState.pending == nil {
		probeVirtualRouterICMPPingState.pending = make(map[string]probeVirtualRouterICMPPingPending)
	}
	for itemKey, item := range probeVirtualRouterICMPPingState.pending {
		if now.Sub(item.StartedAt) > 60*time.Second {
			delete(probeVirtualRouterICMPPingState.pending, itemKey)
		}
	}
	probeVirtualRouterICMPPingState.pending[key] = pending
	probeVirtualRouterICMPPingState.mu.Unlock()
	log.Printf("probe virtual router icmp echo start: trace_code=icmp-trace-v2 src=%s dst=%s id=%d seq=%d path=%s", info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"))
}

func recordProbeVirtualRouterICMPPingReply(rt *probeVirtualRouterRuntime, info probeVirtualRouterICMPEchoLogInfo) (probeVirtualRouterICMPPingCompleteSummary, bool) {
	key := probeVirtualRouterICMPPingKey(info.DestinationIP, info.SourceIP, info.ID, info.Sequence)
	if key == "" {
		return probeVirtualRouterICMPPingCompleteSummary{}, false
	}
	probeVirtualRouterICMPPingState.mu.Lock()
	pending, ok := probeVirtualRouterICMPPingState.pending[key]
	if ok {
		delete(probeVirtualRouterICMPPingState.pending, key)
	}
	probeVirtualRouterICMPPingState.mu.Unlock()
	if !ok || pending.StartedAt.IsZero() {
		return probeVirtualRouterICMPPingCompleteSummary{}, false
	}
	latency := time.Since(pending.StartedAt)
	latencyMS := probeDurationMilliseconds(latency)
	routeID := ""
	if rt != nil {
		routeID = strings.TrimSpace(rt.cfg.routeID)
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.VirtualPingCount++
		item.LastVirtualPingLatencyMS = latencyMS
		item.LastVirtualPingAt = time.Now().UTC().Format(time.RFC3339)
		item.LastVirtualPingSourceIP = pending.SourceIP
		item.LastVirtualPingDestIP = pending.DestinationIP
		item.LastVirtualPingID = pending.ID
		item.LastVirtualPingSequence = pending.Sequence
		item.LastVirtualPingPath = pending.Path
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	log.Printf("probe virtual router icmp echo complete: trace_code=icmp-trace-v2 complete_site=record_reply route=%s src=%s dst=%s id=%d seq=%d latency_ms=%d path=%s", routeID, pending.SourceIP, pending.DestinationIP, pending.ID, pending.Sequence, latencyMS, pending.Path)
	return probeVirtualRouterICMPPingCompleteSummary{
		SourceIP:      pending.SourceIP,
		DestinationIP: pending.DestinationIP,
		ID:            pending.ID,
		Sequence:      pending.Sequence,
		Path:          pending.Path,
		LatencyMS:     latencyMS,
	}, true
}

func logProbeVirtualRouterICMPTraceComplete(rt *probeVirtualRouterRuntime, info probeVirtualRouterICMPEchoLogInfo, trace []probeVirtualRouterFrameTraceHop) {
	if len(trace) == 0 {
		return
	}
	routeID := ""
	if rt != nil {
		routeID = strings.TrimSpace(rt.cfg.routeID)
	}
	log.Printf("probe virtual router icmp trace complete: trace_code=icmp-trace-v2 route=%s kind=%s src=%s dst=%s id=%d seq=%d hops=%d trace_clock=node_local_absolute trace=%s", routeID, info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, len(trace), probeVirtualRouterICMPTraceString(trace))
}

func logProbeVirtualRouterICMPPingSummary(rt *probeVirtualRouterRuntime, info probeVirtualRouterICMPEchoLogInfo, trace []probeVirtualRouterFrameTraceHop, summary probeVirtualRouterICMPPingCompleteSummary) {
	routeID := ""
	if rt != nil {
		routeID = strings.TrimSpace(rt.cfg.routeID)
	}
	traceText := probeVirtualRouterICMPTraceString(trace)
	if strings.TrimSpace(traceText) == "" {
		traceText = "-"
	}
	sourceIP := firstNonEmpty(summary.SourceIP, info.DestinationIP)
	destinationIP := firstNonEmpty(summary.DestinationIP, info.SourceIP)
	path := strings.TrimSpace(summary.Path)
	if path == "" {
		path = "-"
	}
	log.Printf("probe virtual router icmp echo summary: trace_code=icmp-trace-v2 route=%s kind=%s src=%s dst=%s id=%d seq=%d latency_ms=%d path=%s trace_hops=%d trace_clock=node_local_absolute trace=%s", routeID, info.Kind, sourceIP, destinationIP, summary.ID, summary.Sequence, summary.LatencyMS, path, len(trace), traceText)
}

func recordProbeVirtualRouterRuntimeDeliveryError(rt *probeVirtualRouterRuntime, err error) {
	if rt == nil || err == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.LastDeliveryError = strings.TrimSpace(err.Error())
		item.LastPacketAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeOpenSuccess(routeID string, latency time.Duration) {
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.LinkOpenCount++
		item.LastOpenLatencyMS = probeDurationMilliseconds(latency)
		item.LastOpenError = ""
		item.LastOpenAt = time.Now().UTC().Format(time.RFC3339)
		resetProbeVirtualRouterRuntimePingStateLocked(item)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeOpenError(routeID string, err error) {
	if err == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.LastOpenError = strings.TrimSpace(err.Error())
		item.LastOpenAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimePingSuccess(rt *probeVirtualRouterRuntime, direction string, latency time.Duration) {
	routeID, bridgeStatus, bridgeSession := snapshotProbeVirtualRouterPingContext(rt, direction)
	shouldClearRouteCache := false
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		shouldClearRouteCache = strings.TrimSpace(item.LastPingError) != ""
		item.PingCount++
		item.LastPingLatencyMS = probeDurationMilliseconds(latency)
		item.LastPingError = ""
		item.LastPingFailureCount = 0
		item.LastPingAt = time.Now().UTC().Format(time.RFC3339)
		applyProbeVirtualRouterPingContext(item, direction, bridgeStatus, bridgeSession)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCacheForRuntime(rt, "bridge ping recovered")
	}
}

func recordProbeVirtualRouterRuntimePingError(rt *probeVirtualRouterRuntime, direction string, err error) {
	if rt == nil || err == nil {
		return
	}
	routeID, bridgeStatus, bridgeSession := snapshotProbeVirtualRouterPingContext(rt, direction)
	normalizedErr := normalizeProbeVirtualRouterBridgeError(err.Error())
	failureCount := 0
	shouldClearRouteCache := false
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.LastPingError = normalizedErr
		item.LastPingFailureCount++
		failureCount = item.LastPingFailureCount
		shouldClearRouteCache = failureCount == probeVirtualRouterCarrierStalePingFailures
		item.LastPingAt = time.Now().UTC().Format(time.RFC3339)
		applyProbeVirtualRouterPingContext(item, direction, bridgeStatus, bridgeSession)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCacheForRuntime(rt, "bridge ping error threshold")
	}
	log.Printf("probe virtual router bridge ping error retained carrier: route=%s direction=%s failures=%d err=%s", routeID, normalizeProbeRouteBridgeRole(direction), failureCount, normalizedErr)
	detachProbeVirtualRouterStalePhysicalCarrier(rt, failureCount, normalizedErr)
}

func recordProbeVirtualRouterRuntimeRemoteRTTControlSuccess(routeID string, latencyMS int64, responder string) {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return
	}
	shouldClearRouteCache := false
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		shouldClearRouteCache = strings.TrimSpace(item.LastRemoteRTTError) != ""
		item.LastRemoteRTTMS = latencyMS
		item.LastRemoteRTTAt = time.Now().UTC().Format(time.RFC3339)
		item.LastRemoteRTTError = ""
		item.LastRemoteRTTResponder = strings.TrimSpace(responder)
		item.LastRemotePongsReceived++
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCache("remote rtt control query recovered")
	}
}

func recordProbeVirtualRouterRuntimeRemoteRTTError(routeID string, err error) {
	if err == nil {
		return
	}
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return
	}
	shouldClearRouteCache := false
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		shouldClearRouteCache = strings.TrimSpace(item.LastRemoteRTTError) == ""
		item.LastRemoteRTTError = strings.TrimSpace(err.Error())
		item.LastRemoteRTTAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCache("remote rtt query error")
	}
}

func recordProbeVirtualRouterRuntimeSpeedTest(routeID string, sourceNodeID string, targetNodeID string, pathText string, up probeVirtualRouterSpeedTestResult, down probeVirtualRouterSpeedTestResult, resultErr error) {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return
	}
	errText := ""
	if resultErr != nil {
		errText = strings.TrimSpace(resultErr.Error())
	}
	if up.Error != "" {
		errText = firstNonEmpty(errText, "up: "+strings.TrimSpace(up.Error))
	}
	if down.Error != "" {
		errText = firstNonEmpty(errText, "down: "+strings.TrimSpace(down.Error))
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.LastSpeedTestAt = time.Now().UTC().Format(time.RFC3339)
		item.LastSpeedTestSourceNodeID = normalizeProbeRouteNodeID(sourceNodeID)
		item.LastSpeedTestTargetNodeID = normalizeProbeRouteNodeID(targetNodeID)
		item.LastSpeedTestPath = strings.TrimSpace(pathText)
		item.LastSpeedTestError = errText
		item.LastSpeedTestUpBytes = up.Bytes
		item.LastSpeedTestUpFrames = up.Frames
		item.LastSpeedTestUpDurationMS = up.DurationMS
		item.LastSpeedTestUpMbps = up.Mbps
		item.LastSpeedTestDownBytes = down.Bytes
		item.LastSpeedTestDownFrames = down.Frames
		item.LastSpeedTestDownDurationMS = down.DurationMS
		item.LastSpeedTestDownMbps = down.Mbps
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeSpeedTestReceive(routeID string, result probeVirtualRouterSpeedTestResultPayload) {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.LastSpeedTestAt = time.Now().UTC().Format(time.RFC3339)
		item.LastSpeedTestSourceNodeID = normalizeProbeRouteNodeID(result.SourceNodeID)
		item.LastSpeedTestTargetNodeID = normalizeProbeRouteNodeID(result.TargetNodeID)
		item.LastSpeedTestPath = strings.Join(cleanProbeVirtualRouterPath(result.Path), ">")
		item.LastSpeedTestError = strings.TrimSpace(result.Error)
		switch strings.TrimSpace(result.Direction) {
		case "down":
			item.LastSpeedTestDownBytes = result.Bytes
			item.LastSpeedTestDownFrames = result.Frames
			item.LastSpeedTestDownDurationMS = result.DurationMS
			item.LastSpeedTestDownMbps = result.Mbps
		default:
			item.LastSpeedTestUpBytes = result.Bytes
			item.LastSpeedTestUpFrames = result.Frames
			item.LastSpeedTestUpDurationMS = result.DurationMS
			item.LastSpeedTestUpMbps = result.Mbps
		}
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func normalizeProbeVirtualRouterBridgeError(value string) string {
	text := strings.TrimSpace(value)
	text = strings.ReplaceAll(text, "upstream bridge", "bridge")
	text = strings.ReplaceAll(text, "downstream bridge", "bridge")
	return text
}

func snapshotProbeVirtualRouterPingContext(rt *probeVirtualRouterRuntime, direction string) (string, probeRouteBridgeRuntimeStatus, probeRouteBridgeSessionSnapshot) {
	if rt == nil {
		return "", probeRouteBridgeRuntimeStatus{}, probeRouteBridgeSessionSnapshot{}
	}
	snapshot, ok := snapshotProbeVirtualRouterPhysicalCarrier(rt)
	status := probeRouteBridgeRuntimeStatus{UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	if ok {
		status.DownstreamActive = 1
		status.Sessions = []probeRouteBridgeSessionSnapshot{snapshot}
		return strings.TrimSpace(rt.cfg.routeID), status, snapshot
	}
	return strings.TrimSpace(rt.cfg.routeID), status, probeRouteBridgeSessionSnapshot{}
}

func applyProbeVirtualRouterPingContext(item *probeVirtualRouterRuntimeStats, direction string, bridgeStatus probeRouteBridgeRuntimeStatus, bridgeSession probeRouteBridgeSessionSnapshot) {
	if item == nil {
		return
	}
	item.LastPingDirection = normalizeProbeRouteBridgeRole(direction)
	item.LastPingBridgeConnections = probeVirtualRouterBridgeConnectionCount(bridgeStatus)
	item.LastPingBridgeSessionID = strings.TrimSpace(bridgeSession.SessionID)
	item.LastPingBridgeRemote = strings.TrimSpace(bridgeSession.RemoteAddr)
	item.LastPingBridgeConnectedAt = strings.TrimSpace(bridgeSession.ConnectedAt)
}

func probeVirtualRouterBridgeConnectionCount(bridgeStatus probeRouteBridgeRuntimeStatus) int {
	count := 0
	for _, session := range bridgeStatus.Sessions {
		if !session.Closed {
			count++
		}
	}
	return count
}

func snapshotProbeVirtualRouterPhysicalCarrier(rt *probeVirtualRouterRuntime) (probeRouteBridgeSessionSnapshot, bool) {
	link := currentProbeVirtualRouterPhysicalCarrier(rt)
	if link == nil {
		return probeRouteBridgeSessionSnapshot{}, false
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	carrier := link.carrier
	if carrier == nil {
		return probeRouteBridgeSessionSnapshot{}, false
	}
	connectedAt := ""
	connectedMS := int64(0)
	if !carrier.connectedAt.IsZero() {
		connectedAt = carrier.connectedAt.UTC().Format(time.RFC3339)
		connectedMS = time.Since(carrier.connectedAt).Milliseconds()
	}
	return probeRouteBridgeSessionSnapshot{
		RouteID:        strings.TrimSpace(rt.cfg.routeID),
		RuntimeRole:    probeVirtualRouterRuntimeRole,
		Direction:      "physical",
		SessionID:      firstNonEmpty(strings.TrimSpace(carrier.sessionID), "vrouter-carrier"),
		BridgeRole:     "physical",
		RemoteAddr:     strings.TrimSpace(carrier.remoteAddr),
		ConnectedAt:    connectedAt,
		ConnectedMS:    connectedMS,
		StreamsCurrent: 0,
		Closed:         false,
	}, true
}

func clearProbeVirtualRouterRuntimePingError(routeID string) {
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		resetProbeVirtualRouterRuntimePingStateLocked(item)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func resetProbeVirtualRouterRuntimePingStateLocked(item *probeVirtualRouterRuntimeStats) {
	if item == nil {
		return
	}
	item.LastPingLatencyMS = 0
	item.LastPingError = ""
	item.LastPingAt = ""
	item.LastPingFailureCount = 0
	item.LastPingDirection = ""
	item.LastPingBridgeConnections = 0
	item.LastPingBridgeSessionID = ""
	item.LastPingBridgeRemote = ""
	item.LastPingBridgeConnectedAt = ""
}

func snapshotProbeVirtualRouterRuntimeStats(routeID string) *probeVirtualRouterRuntimeStats {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return nil
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsState.items[routeID]
	if item == nil {
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
		return nil
	}
	out := *item
	tunStats := probeVirtualRouterTUNDataPlaneStatsSnapshot()
	out.TUNDataPlane = tunStats.Running
	out.TUNRXPackets = tunStats.RXPackets
	out.TUNRXBytes = tunStats.RXBytes
	out.TUNTXPackets = tunStats.TXPackets
	out.TUNTXBytes = tunStats.TXBytes
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	return &out
}

func probeVirtualRouterRuntimeForAdjacentNode(nodeID string) (*probeVirtualRouterRuntime, string) {
	target := normalizeProbeRouteNodeID(nodeID)
	if target == "" {
		return nil, ""
	}
	probeVirtualRouterRuntimeState.mu.RLock()
	defer probeVirtualRouterRuntimeState.mu.RUnlock()
	return findProbeVirtualRouterRuntimeForAdjacentNodeLocked(target)
}

func findProbeVirtualRouterRuntimeForAdjacentNodeLocked(target string) (*probeVirtualRouterRuntime, string) {
	var fallbackRT *probeVirtualRouterRuntime
	var fallbackDirection string
	for _, rt := range probeVirtualRouterRuntimeState.runtimes {
		if rt == nil {
			continue
		}
		if normalizeProbeRouteNodeID(rt.cfg.peerNodeID) != target {
			continue
		}
		direction := probeRouteBridgeRoleToPrev
		if rt.cfg.dialer {
			direction = probeRouteBridgeRoleToNext
		}
		if probeVirtualRouterRuntimeHasBridgeSession(rt, direction) {
			return rt, direction
		}
		if fallbackRT == nil {
			fallbackRT = rt
			fallbackDirection = direction
		}
	}
	if fallbackRT != nil {
		return fallbackRT, fallbackDirection
	}
	return nil, ""
}

func selectProbeVirtualRouterBridgeDirection(rt *probeVirtualRouterRuntime, preferred string) string {
	return normalizeProbeRouteBridgeRole(preferred)
}

func probeVirtualRouterRuntimeLogRouteID(runtime *probeVirtualRouterRuntime) string {
	if runtime == nil {
		return ""
	}
	return strings.TrimSpace(runtime.cfg.routeID)
}

func probeVirtualRouterRuntimeHasBridgeSession(rt *probeVirtualRouterRuntime, direction string) bool {
	if rt == nil {
		return false
	}
	return probeVirtualRouterRuntimeHasPhysicalBridgeSession(rt)
}

func probeVirtualRouterRuntimeHasPhysicalBridgeSession(rt *probeVirtualRouterRuntime) bool {
	if rt == nil {
		return false
	}
	return currentProbeVirtualRouterPhysicalCarrier(rt) != nil
}

func currentProbeVirtualRouterPhysicalCarrier(rt *probeVirtualRouterRuntime) *probeVirtualRouterFrameLink {
	if rt == nil {
		return nil
	}
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	probeVirtualRouterFrameLinkState.mu.Lock()
	item := probeVirtualRouterFrameLinkState.links[key]
	if item == nil {
		probeVirtualRouterFrameLinkState.mu.Unlock()
		return nil
	}
	if isProbeVirtualRouterFrameLinkClosed(item) {
		delete(probeVirtualRouterFrameLinkState.links, key)
		probeVirtualRouterFrameLinkState.mu.Unlock()
		stopProbeVirtualRouterFrameLink(item)
		return nil
	}
	item.mu.Lock()
	hasCarrier := item.carrier != nil
	item.mu.Unlock()
	if !hasCarrier {
		probeVirtualRouterFrameLinkState.mu.Unlock()
		return nil
	}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	return item
}

func detachProbeVirtualRouterStalePhysicalCarrier(rt *probeVirtualRouterRuntime, failureCount int, reason string) {
	if rt == nil || failureCount < probeVirtualRouterCarrierStalePingFailures {
		return
	}
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	if key == "" {
		return
	}
	probeVirtualRouterFrameLinkState.mu.Lock()
	item := probeVirtualRouterFrameLinkState.links[key]
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if item == nil || isProbeVirtualRouterFrameLinkClosed(item) {
		return
	}
	item.mu.Lock()
	token := item.carrier
	item.mu.Unlock()
	if token == nil {
		return
	}
	lastReadAt := token.lastRead()
	if lastReadAt.IsZero() {
		lastReadAt = token.connectedAt
	}
	idleFor := time.Since(lastReadAt)
	if idleFor < probeVirtualRouterCarrierStaleRXGrace {
		return
	}
	log.Printf("probe virtual router physical carrier stale, detach for reconnect: route=%s role=%s session_id=%s remote=%s failures=%d rx_idle_ms=%d reason=%s", strings.TrimSpace(rt.cfg.routeID), probeVirtualRouterRuntimeRole, strings.TrimSpace(token.sessionID), strings.TrimSpace(token.remoteAddr), failureCount, probeDurationMilliseconds(idleFor), strings.TrimSpace(reason))
	item.detachCarrier(token)
}

func probeVirtualRouterNextHopInPath(path []string, localNodeID string) string {
	local := normalizeProbeRouteNodeID(localNodeID)
	if local == "" || len(path) < 2 {
		return ""
	}
	for i, item := range path {
		if normalizeProbeRouteNodeID(item) != local {
			continue
		}
		if i+1 < len(path) {
			return normalizeProbeRouteNodeID(path[i+1])
		}
		return ""
	}
	return ""
}

func probeVirtualRouterPathFromAssociation(association *probeRouteAssociationV2Meta) []string {
	if association == nil {
		return nil
	}
	return parseProbeVirtualRouterPathText(association.RouteTarget)
}

func probeVirtualRouterIPv4Destination(packet []byte) string {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return ""
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl {
		return ""
	}
	totalLen := int(packet[2])<<8 | int(packet[3])
	if totalLen > 0 && totalLen > len(packet) {
		return ""
	}
	ip := net.IPv4(packet[16], packet[17], packet[18], packet[19]).To4()
	if ip == nil {
		return ""
	}
	return ip.String()
}

func probeVirtualRouterIPv4Source(packet []byte) string {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return ""
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl {
		return ""
	}
	totalLen := int(packet[2])<<8 | int(packet[3])
	if totalLen > 0 && totalLen > len(packet) {
		return ""
	}
	ip := net.IPv4(packet[12], packet[13], packet[14], packet[15]).To4()
	if ip == nil {
		return ""
	}
	return ip.String()
}

func probeVirtualRouterParseICMPEchoLogInfo(packet []byte) (probeVirtualRouterICMPEchoLogInfo, bool) {
	if len(packet) < 28 || packet[0]>>4 != 4 {
		return probeVirtualRouterICMPEchoLogInfo{}, false
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl+8 {
		return probeVirtualRouterICMPEchoLogInfo{}, false
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl+8 {
		return probeVirtualRouterICMPEchoLogInfo{}, false
	}
	if packet[9] != 1 {
		return probeVirtualRouterICMPEchoLogInfo{}, false
	}
	icmp := packet[ihl:totalLen]
	kind := ""
	switch {
	case icmp[0] == 8 && icmp[1] == 0:
		kind = "echo_request"
	case icmp[0] == 0 && icmp[1] == 0:
		kind = "echo_reply"
	default:
		return probeVirtualRouterICMPEchoLogInfo{}, false
	}
	return probeVirtualRouterICMPEchoLogInfo{
		Kind:          kind,
		SourceIP:      net.IPv4(packet[12], packet[13], packet[14], packet[15]).String(),
		DestinationIP: net.IPv4(packet[16], packet[17], packet[18], packet[19]).String(),
		ID:            binary.BigEndian.Uint16(icmp[4:6]),
		Sequence:      binary.BigEndian.Uint16(icmp[6:8]),
	}, true
}

func probeVirtualRouterParseTCPUDPLogInfo(packet []byte) (probeVirtualRouterTransportLogInfo, bool) {
	if len(packet) < 28 || packet[0]>>4 != 4 {
		return probeVirtualRouterTransportLogInfo{}, false
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl+8 {
		return probeVirtualRouterTransportLogInfo{}, false
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl+8 {
		return probeVirtualRouterTransportLogInfo{}, false
	}
	protocol := ""
	switch packet[9] {
	case 6:
		protocol = "tcp"
	case 17:
		protocol = "udp"
	default:
		return probeVirtualRouterTransportLogInfo{}, false
	}
	transport := packet[ihl:totalLen]
	info := probeVirtualRouterTransportLogInfo{
		Protocol:        protocol,
		SourceIP:        net.IPv4(packet[12], packet[13], packet[14], packet[15]).String(),
		DestinationIP:   net.IPv4(packet[16], packet[17], packet[18], packet[19]).String(),
		SourcePort:      binary.BigEndian.Uint16(transport[0:2]),
		DestinationPort: binary.BigEndian.Uint16(transport[2:4]),
	}
	if protocol == "tcp" && len(transport) >= 14 {
		info.TCPFlags = formatProbeVirtualRouterTCPFlags(transport[13])
	}
	return info, true
}

func formatProbeVirtualRouterTCPFlags(flags byte) string {
	if flags == 0 {
		return ""
	}
	names := make([]string, 0, 8)
	if flags&0x80 != 0 {
		names = append(names, "CWR")
	}
	if flags&0x40 != 0 {
		names = append(names, "ECE")
	}
	if flags&0x20 != 0 {
		names = append(names, "URG")
	}
	if flags&0x10 != 0 {
		names = append(names, "ACK")
	}
	if flags&0x08 != 0 {
		names = append(names, "PSH")
	}
	if flags&0x04 != 0 {
		names = append(names, "RST")
	}
	if flags&0x02 != 0 {
		names = append(names, "SYN")
	}
	if flags&0x01 != 0 {
		names = append(names, "FIN")
	}
	return strings.Join(names, ",")
}

func probeVirtualRouterPacketChecksumSummary(packet []byte) string {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return ""
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl {
		return ""
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl {
		return ""
	}
	parts := make([]string, 0, 2)
	ipChecksum := "bad"
	if probeVirtualRouterChecksum(packet[:ihl]) == 0 {
		ipChecksum = "ok"
	}
	parts = append(parts, "ip_checksum="+ipChecksum)
	transport := packet[ihl:totalLen]
	switch packet[9] {
	case 6:
		if len(transport) < 20 {
			parts = append(parts, "tcp_checksum=short")
			break
		}
		checksum := binary.BigEndian.Uint16(transport[16:18])
		tcpChecksum := "bad"
		if checksum == 0 {
			tcpChecksum = "zero"
		} else if probeVirtualRouterTransportChecksum(packet, transport) == 0 {
			tcpChecksum = "ok"
		}
		parts = append(parts, "tcp_checksum="+tcpChecksum)
	case 17:
		if len(transport) < 8 {
			parts = append(parts, "udp_checksum=short")
			break
		}
		checksum := binary.BigEndian.Uint16(transport[6:8])
		udpChecksum := "ok"
		if checksum != 0 && probeVirtualRouterTransportChecksum(packet, transport) != 0 {
			udpChecksum = "bad"
		}
		parts = append(parts, "udp_checksum="+udpChecksum)
	}
	return strings.Join(parts, " ")
}

func probeVirtualRouterTransportChecksum(packet []byte, transport []byte) uint16 {
	pseudo := make([]byte, 12+len(transport))
	copy(pseudo[0:4], packet[12:16])
	copy(pseudo[4:8], packet[16:20])
	pseudo[9] = packet[9]
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(transport)))
	copy(pseudo[12:], transport)
	return probeVirtualRouterChecksum(pseudo)
}

func buildProbeVirtualRouterICMPEchoReply(packet []byte, localIP string) ([]byte, string, bool) {
	local := net.ParseIP(strings.TrimSpace(localIP)).To4()
	if local == nil || len(packet) < 28 || packet[0]>>4 != 4 {
		return nil, "", false
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl+8 {
		return nil, "", false
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl+8 {
		return nil, "", false
	}
	if packet[9] != 1 {
		return nil, "", false
	}
	dst := net.IPv4(packet[16], packet[17], packet[18], packet[19]).To4()
	if dst == nil || !dst.Equal(local) {
		return nil, "", false
	}
	icmp := packet[ihl:totalLen]
	if len(icmp) < 8 || icmp[0] != 8 || icmp[1] != 0 {
		return nil, "", false
	}
	reply := append([]byte(nil), packet[:totalLen]...)
	copy(reply[12:16], packet[16:20])
	copy(reply[16:20], packet[12:16])
	reply[8] = 64
	reply[10], reply[11] = 0, 0
	binary.BigEndian.PutUint16(reply[10:12], probeVirtualRouterChecksum(reply[:ihl]))
	reply[ihl] = 0
	reply[ihl+1] = 0
	reply[ihl+2], reply[ihl+3] = 0, 0
	binary.BigEndian.PutUint16(reply[ihl+2:ihl+4], probeVirtualRouterChecksum(reply[ihl:totalLen]))
	return reply, net.IPv4(packet[12], packet[13], packet[14], packet[15]).String(), true
}

func probeVirtualRouterChecksum(payload []byte) uint16 {
	var sum uint32
	for len(payload) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(payload[:2]))
		payload = payload[2:]
	}
	if len(payload) > 0 {
		sum += uint32(payload[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
