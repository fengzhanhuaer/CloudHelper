package main

import (
	"bufio"
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
	"strings"
	"sync"
	"time"
)

const (
	probeVirtualRouterDirectionForward           = "forward"
	probeVirtualRouterDefaultServicePort         = 12040
	probeVirtualRouterFrameEnvelopeMagic         = 0x56524632
	probeVirtualRouterFrameEnvelopeHeaderSize    = 12
	probeVirtualRouterFrameEnvelopeFlagTrace     = 1
	probeVirtualRouterFrameMaxBytes              = 65536
	probeVirtualRouterFrameTypeData              = "data"
	probeVirtualRouterFrameTypeControl           = "control"
	probeVirtualRouterControlTypeIPv4            = "ip4"
	probeVirtualRouterControlTypePing            = "ping"
	probeVirtualRouterControlTypePong            = "pong"
	probeVirtualRouterControlTypePathRTTQuery    = "path_rtt_query"
	probeVirtualRouterControlTypePathRTTResponse = "path_rtt_response"
	probeVirtualRouterControlTypeSpeedStart      = "speed_start"
	probeVirtualRouterControlTypeSpeedChunk      = "speed_chunk"
	probeVirtualRouterControlTypeSpeedFinish     = "speed_finish"
	probeVirtualRouterControlTypeSpeedResult     = "speed_result"
	probeVirtualRouterControlTypeSpeedSend       = "speed_send"
	probeVirtualRouterTunnelScope                = "virtual_router"
	probeVirtualRouterNetworkIPv4                = "ip4"
	probeVirtualRouterFrameLinkIdleTTL           = 45 * time.Second
	probeVirtualRouterPingPongInterval           = 30 * time.Second
	probeVirtualRouterPingPongTimeout            = 5 * time.Second
	probeVirtualRouterPingPongBytes              = 64
	probeVirtualRouterSpeedTestMaxBytes          = 128 * 1024 * 1024
	probeVirtualRouterSpeedTestMaxDuration       = 10 * time.Second
	probeVirtualRouterSpeedTestChunkBytes        = 48 * 1024
	probeVirtualRouterCarrierStalePingFailures   = 4
	probeVirtualRouterCarrierStaleRXGrace        = 2 * probeVirtualRouterPingPongInterval
)

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

type probeVirtualRouterICMPEchoLogInfo struct {
	Kind          string
	SourceIP      string
	DestinationIP string
	ID            uint16
	Sequence      uint16
}

type probeVirtualRouterICMPPingPending struct {
	StartedAt     time.Time
	ChainID       string
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
	ChainID       string
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
}

type probeVirtualRouterFrameMessage struct {
	FrameType   string
	ControlType string
	Payload     []byte
	Path        []string
	Trace       []probeVirtualRouterFrameTraceHop
}

type probeVirtualRouterFrameTraceHop struct {
	ID         string `json:"id"`
	NodeID     string `json:"node_id"`
	ChainID    string `json:"chain_id,omitempty"`
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
	RuntimeChainID    string   `json:"-"`
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
		UpdatedAt:     strings.TrimSpace(input.UpdatedAt),
	}
	return out
}

func sanitizeProbeVirtualRouterProbeIPs(items []probeVirtualRouterProbeIP) []probeVirtualRouterProbeIP {
	if len(items) == 0 {
		return []probeVirtualRouterProbeIP{}
	}
	out := make([]probeVirtualRouterProbeIP, 0, len(items))
	seenNode := map[string]struct{}{}
	seenIP := map[string]struct{}{}
	for _, item := range items {
		nodeID := normalizeProbeChainNodeID(item.NodeID)
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
			NodeID: nodeID,
			IP:     ipText,
			Note:   strings.TrimSpace(item.Note),
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
		fromNodeID := normalizeProbeChainNodeID(item.FromNodeID)
		toNodeID := normalizeProbeChainNodeID(item.ToNodeID)
		direction := normalizeProbeVirtualRouterDirection(item.Direction)
		if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
			continue
		}
		fromServiceDomain := ""
		fromServicePort := 0
		toServiceDomain := strings.TrimSpace(item.ToServiceDomain)
		toServicePort := normalizeProbeVirtualRouterServicePort(item.ToServicePort)
		ruleID := strings.TrimSpace(item.ID)
		if ruleID == "" {
			ruleID, nextRuleSeq = allocateProbeVirtualRouterRuleID(seen, reserved, nextRuleSeq)
		}
		key := ruleID
		if key == "" {
			key = fmt.Sprintf("%s|%s|%s|%d|%s|%d", fromNodeID, toNodeID, fromServiceDomain, fromServicePort, toServiceDomain, toServicePort)
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

func normalizeProbeVirtualRouterDirection(raw string) string {
	return probeVirtualRouterDirectionForward
}

func persistProbeVirtualRouterCache(config probeVirtualRouterConfig) error {
	cachePath, err := resolveProbeVirtualRouterCachePath()
	if err != nil {
		return err
	}
	payload := probeVirtualRouterCacheFile{
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

func loadProbeVirtualRouterCache() (probeVirtualRouterConfig, error) {
	cachePath, err := resolveProbeVirtualRouterCachePath()
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
	var payload probeVirtualRouterCacheFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return probeVirtualRouterConfig{}, err
	}
	config := sanitizeProbeVirtualRouterConfigForCache(payload.Item)
	rememberProbeVirtualRouterAuthTickets(config)
	return config, nil
}

func resolveProbeVirtualRouterCachePath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, probeVirtualRouterCacheFileName), nil
}

func applyProbeVirtualRouterConfig(config probeVirtualRouterConfig) {
	applyProbeVirtualRouterConfigForNode(config, "")
}

func applyProbeVirtualRouterConfigForNode(config probeVirtualRouterConfig, nodeID string) {
	sanitized := sanitizeProbeVirtualRouterConfigForCache(config)
	index := buildProbeVirtualRouterTopologyIndex(sanitized)
	probeVirtualRouterState.mu.Lock()
	effectiveNodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	if cleanNodeID := normalizeProbeChainNodeID(nodeID); cleanNodeID != "" {
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
		closeProbeVirtualRouterFrameLinks("config updated")
	}
	if ensureLocalInterface {
		scheduleProbeVirtualRouterLocalInterfaceIPEnsure("config_updated")
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
		nodeID := normalizeProbeChainNodeID(item.NodeID)
		ip := net.ParseIP(strings.TrimSpace(item.IP)).To4()
		if nodeID == "" || ip == nil {
			continue
		}
		ipText := ip.String()
		index.nodeToIP[nodeID] = ipText
		index.ipToNode[ipText] = nodeID
	}
	addNeighbor := func(a string, b string) {
		a = normalizeProbeChainNodeID(a)
		b = normalizeProbeChainNodeID(b)
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
	localNodeID = normalizeProbeChainNodeID(localNodeID)
	fmt.Fprintf(&b, "enabled=%t|cidr=%s|local=%s|local_ip=%s\n", config.Enabled, strings.TrimSpace(config.FakeIPCIDR), localNodeID, strings.TrimSpace(index.nodeToIP[localNodeID]))
	nodeIDs := make([]string, 0, len(index.nodeToIP))
	for nodeID := range index.nodeToIP {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	for _, nodeID := range nodeIDs {
		fmt.Fprintf(&b, "ip|%s|%s\n", nodeID, strings.TrimSpace(index.nodeToIP[nodeID]))
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
			normalizeProbeChainNodeID(rule.FromNodeID),
			normalizeProbeChainNodeID(rule.ToNodeID),
			normalizeProbeVirtualRouterDirection(rule.Direction),
			strings.TrimSpace(rule.FromServiceDomain),
			normalizeProbeVirtualRouterServicePort(rule.FromServicePort),
			strings.TrimSpace(rule.ToServiceDomain),
			normalizeProbeVirtualRouterServicePort(rule.ToServicePort),
		)
	}
	return b.String()
}

func currentProbeVirtualRouterConfig() probeVirtualRouterConfig {
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return sanitizeProbeVirtualRouterConfigForCache(probeVirtualRouterState.config)
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
		if nodeID := normalizeProbeChainNodeID(runtime.cfg.identity.NodeID); nodeID != "" {
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
	target := normalizeProbeChainNodeID(nodeID)
	if target == "" {
		return ""
	}
	for _, item := range config.ProbeIPs {
		if normalizeProbeChainNodeID(item.NodeID) == target {
			return strings.TrimSpace(item.IP)
		}
	}
	return ""
}

func probeVirtualRouterReachable(config probeVirtualRouterConfig, fromNodeID string, toNodeID string) bool {
	return len(probeVirtualRouterPath(config, fromNodeID, toNodeID)) > 0
}

func probeVirtualRouterPath(config probeVirtualRouterConfig, fromNodeID string, toNodeID string) []string {
	from := normalizeProbeChainNodeID(fromNodeID)
	to := normalizeProbeChainNodeID(toNodeID)
	if !config.Enabled || from == "" || to == "" {
		return nil
	}
	if from == to {
		return []string{from}
	}
	graph := map[string]map[string]struct{}{}
	addEdge := func(a string, b string) {
		a = normalizeProbeChainNodeID(a)
		b = normalizeProbeChainNodeID(b)
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
	from := normalizeProbeChainNodeID(fromNodeID)
	to := normalizeProbeChainNodeID(toNodeID)
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
		if clean := normalizeProbeChainNodeID(item); clean != "" {
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
		target = normalizeProbeChainNodeID(path[len(path)-1])
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
		target = normalizeProbeChainNodeID(path[len(path)-1])
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
		if nodeID := normalizeProbeChainNodeID(item); nodeID != "" {
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
	from := normalizeProbeChainNodeID(fromNodeID)
	to := normalizeProbeChainNodeID(toNodeID)
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
	stats := snapshotProbeVirtualRouterRuntimeStats(rt.cfg.chainID)
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
		l := normalizeProbeChainNodeID(left[i])
		r := normalizeProbeChainNodeID(right[i])
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
			return normalizeProbeChainNodeID(item.NodeID)
		}
	}
	return ""
}

func currentProbeVirtualRouterIPForNode(nodeID string) string {
	target := normalizeProbeChainNodeID(nodeID)
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
	from := normalizeProbeChainNodeID(fromNodeID)
	to := normalizeProbeChainNodeID(toNodeID)
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
	from := normalizeProbeChainNodeID(fromNodeID)
	to := normalizeProbeChainNodeID(toNodeID)
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

func currentProbeVirtualRouterPathToIP(ip string) []string {
	targetIP := net.ParseIP(strings.TrimSpace(ip)).To4()
	if targetIP == nil {
		return nil
	}
	probeVirtualRouterState.mu.RLock()
	nodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	targetNodeID := probeVirtualRouterState.ipToNode[targetIP.String()]
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
	probeVirtualRouterState.mu.RUnlock()
	if nodeID == "" {
		nodeID = sourceNodeID
	}
	return currentProbeVirtualRouterPathBetweenNodes(nodeID, targetNodeID)
}

func probeVirtualRouterPacketTargetsLocalIP(runtime *probeVirtualRouterRuntime, dstIP string) bool {
	return probeVirtualRouterIPMatches(dstIP, currentProbeVirtualRouterLocalIPForRuntime(runtime))
}

func probeVirtualRouterIPMatches(target string, local string) bool {
	localIP := net.ParseIP(strings.TrimSpace(local)).To4()
	targetIP := net.ParseIP(strings.TrimSpace(target)).To4()
	if localIP == nil || targetIP == nil {
		return false
	}
	return targetIP.Equal(localIP)
}

func marshalProbeVirtualRouterFrameEnvelope(frame probeVirtualRouterFrameMessage) ([]byte, error) {
	if len(frame.Payload) == 0 {
		return nil, errors.New("virtual router frame payload is empty")
	}
	frameType := strings.TrimSpace(frame.FrameType)
	if frameType == "" {
		frameType = probeVirtualRouterFrameTypeData
	}
	controlType := strings.TrimSpace(frame.ControlType)
	if frameType == probeVirtualRouterFrameTypeData && controlType == "" {
		controlType = probeVirtualRouterControlTypeIPv4
	}
	if len(frameType) > 0xff || len(controlType) > 0xff {
		return nil, errors.New("virtual router frame type is too large")
	}
	cleanPath := make([]string, 0, len(frame.Path))
	for _, item := range frame.Path {
		if nodeID := normalizeProbeChainNodeID(item); nodeID != "" {
			cleanPath = append(cleanPath, nodeID)
		}
	}
	pathText := strings.Join(cleanPath, ">")
	if len(pathText) > 0xffff {
		return nil, errors.New("virtual router frame path is too large")
	}
	tracePayload, err := marshalProbeVirtualRouterFrameTrace(frame.Trace)
	if err != nil {
		return nil, err
	}
	traceLenSize := 0
	if len(tracePayload) > 0 {
		traceLenSize = 2
		if len(frameType) > 0x0f {
			return nil, errors.New("virtual router traced frame type is too large")
		}
	}
	total := probeVirtualRouterFrameEnvelopeHeaderSize + len(frameType) + len(controlType) + len(pathText) + traceLenSize + len(tracePayload) + len(frame.Payload)
	if total > probeVirtualRouterFrameMaxBytes {
		return nil, errors.New("virtual router frame envelope is too large")
	}
	out := make([]byte, total)
	binary.BigEndian.PutUint32(out[0:4], probeVirtualRouterFrameEnvelopeMagic)
	out[4] = byte(len(frameType))
	out[5] = byte(len(controlType))
	binary.BigEndian.PutUint16(out[6:8], uint16(len(pathText)))
	binary.BigEndian.PutUint32(out[8:12], uint32(len(frame.Payload)))
	if len(tracePayload) > 0 {
		out[4] = byte(len(frameType)<<4) | probeVirtualRouterFrameEnvelopeFlagTrace
	}
	offset := probeVirtualRouterFrameEnvelopeHeaderSize
	copy(out[offset:offset+len(frameType)], frameType)
	offset += len(frameType)
	copy(out[offset:offset+len(controlType)], controlType)
	offset += len(controlType)
	copy(out[offset:offset+len(pathText)], pathText)
	offset += len(pathText)
	if len(tracePayload) > 0 {
		binary.BigEndian.PutUint16(out[offset:offset+2], uint16(len(tracePayload)))
		offset += 2
		copy(out[offset:offset+len(tracePayload)], tracePayload)
		offset += len(tracePayload)
	}
	copy(out[offset:], frame.Payload)
	return out, nil
}

func unmarshalProbeVirtualRouterFrameEnvelope(payload []byte, fallbackPath []string) (probeVirtualRouterFrameMessage, error) {
	if len(payload) < 4 {
		return probeVirtualRouterFrameMessage{FrameType: probeVirtualRouterFrameTypeData, ControlType: probeVirtualRouterControlTypeIPv4, Payload: append([]byte(nil), payload...), Path: append([]string(nil), fallbackPath...)}, nil
	}
	magic := binary.BigEndian.Uint32(payload[0:4])
	if magic != probeVirtualRouterFrameEnvelopeMagic {
		return probeVirtualRouterFrameMessage{FrameType: probeVirtualRouterFrameTypeData, ControlType: probeVirtualRouterControlTypeIPv4, Payload: append([]byte(nil), payload...), Path: append([]string(nil), fallbackPath...)}, nil
	}
	if len(payload) < probeVirtualRouterFrameEnvelopeHeaderSize {
		return probeVirtualRouterFrameMessage{}, errors.New("invalid virtual router frame envelope")
	}
	flags := payload[4] & 0x0f
	frameTypeLen := int((payload[4] & 0xf0) >> 4)
	if frameTypeLen == 0 {
		frameTypeLen = int(payload[4])
		flags = 0
	}
	controlTypeLen := int(payload[5])
	pathLen := int(binary.BigEndian.Uint16(payload[6:8]))
	payloadLen := int(binary.BigEndian.Uint32(payload[8:12]))
	offset := probeVirtualRouterFrameEnvelopeHeaderSize
	baseTotal := probeVirtualRouterFrameEnvelopeHeaderSize + frameTypeLen + controlTypeLen + pathLen + payloadLen
	if flags&probeVirtualRouterFrameEnvelopeFlagTrace != 0 {
		baseTotal += 2
	}
	if payloadLen <= 0 || baseTotal > len(payload) {
		return probeVirtualRouterFrameMessage{}, errors.New("invalid virtual router frame envelope")
	}
	frameType := strings.TrimSpace(string(payload[offset : offset+frameTypeLen]))
	offset += frameTypeLen
	controlType := strings.TrimSpace(string(payload[offset : offset+controlTypeLen]))
	offset += controlTypeLen
	path := parseProbeVirtualRouterPathText(string(payload[offset : offset+pathLen]))
	offset += pathLen
	var trace []probeVirtualRouterFrameTraceHop
	if flags&probeVirtualRouterFrameEnvelopeFlagTrace != 0 {
		if offset+2 > len(payload) {
			return probeVirtualRouterFrameMessage{}, errors.New("invalid virtual router frame trace")
		}
		traceLen := int(binary.BigEndian.Uint16(payload[offset : offset+2]))
		offset += 2
		if traceLen <= 0 || offset+traceLen+payloadLen != len(payload) {
			return probeVirtualRouterFrameMessage{}, errors.New("invalid virtual router frame trace size")
		}
		var err error
		trace, err = unmarshalProbeVirtualRouterFrameTrace(payload[offset : offset+traceLen])
		if err != nil {
			return probeVirtualRouterFrameMessage{}, err
		}
		offset += traceLen
	} else if offset+payloadLen != len(payload) {
		return probeVirtualRouterFrameMessage{}, errors.New("invalid virtual router frame envelope")
	}
	if len(path) == 0 {
		path = append([]string(nil), fallbackPath...)
	}
	return probeVirtualRouterFrameMessage{
		FrameType:   frameType,
		ControlType: controlType,
		Payload:     append([]byte(nil), payload[offset:]...),
		Path:        path,
		Trace:       trace,
	}, nil
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
		NodeID:     normalizeProbeChainNodeID(input.NodeID),
		ChainID:    strings.TrimSpace(input.ChainID),
		Event:      strings.TrimSpace(input.Event),
		Direction:  strings.TrimSpace(input.Direction),
		RemoteNode: normalizeProbeChainNodeID(input.RemoteNode),
		UnixNano:   input.UnixNano,
	}
}

func appendProbeVirtualRouterICMPTrace(trace []probeVirtualRouterFrameTraceHop, runtime *probeVirtualRouterRuntime, event string, direction string, remoteNodeID string) []probeVirtualRouterFrameTraceHop {
	cleanEvent := strings.TrimSpace(event)
	nodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if cleanEvent == "" || nodeID == "" {
		return trace
	}
	chainID := ""
	if runtime != nil {
		chainID = strings.TrimSpace(runtime.cfg.chainID)
	}
	item := probeVirtualRouterFrameTraceHop{
		ID:         newProbeTCPDebugFlowID("vrouter_icmp_trace", nodeID),
		NodeID:     nodeID,
		ChainID:    chainID,
		Event:      cleanEvent,
		Direction:  strings.TrimSpace(direction),
		RemoteNode: normalizeProbeChainNodeID(remoteNodeID),
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
		parts = append(parts, fmt.Sprintf("%02d node=%s event=%s direction=%s remote=%s chain=%s at=%s since_start_ref_ms=%s id=%s", i, clean.NodeID, clean.Event, clean.Direction, clean.RemoteNode, clean.ChainID, at, sinceStart, clean.ID))
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
		nodeID := normalizeProbeChainNodeID(part)
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
	return link.EnqueueProbeVirtualRouterFrame(probeVirtualRouterFrameMessage{
		FrameType:   probeVirtualRouterFrameTypeData,
		ControlType: probeVirtualRouterControlTypeIPv4,
		Payload:     packet,
		Path:        path,
		Trace:       trace,
	})
}

func writeProbeVirtualRouterFrameRaw(writer io.Writer, frame probeVirtualRouterFrameMessage) error {
	payload, err := marshalProbeVirtualRouterFrameEnvelope(frame)
	if err != nil {
		return err
	}
	return writeProbeVirtualRouterAll(writer, payload)
}

func readProbeVirtualRouterFrame(reader *bufio.Reader, fallbackPath []string) (probeVirtualRouterFrameMessage, error) {
	header := make([]byte, probeVirtualRouterFrameEnvelopeHeaderSize)
	if _, err := io.ReadFull(reader, header); err != nil {
		return probeVirtualRouterFrameMessage{}, err
	}
	magic := binary.BigEndian.Uint32(header[0:4])
	if magic != probeVirtualRouterFrameEnvelopeMagic {
		return probeVirtualRouterFrameMessage{}, errors.New("invalid virtual router physical frame magic")
	}
	flags := byte(0)
	frameTypeLen := int(header[4])
	if header[4]&probeVirtualRouterFrameEnvelopeFlagTrace != 0 && header[4]&0xf0 != 0 {
		flags = header[4] & 0x0f
		frameTypeLen = int((header[4] & 0xf0) >> 4)
	}
	controlTypeLen := int(header[5])
	pathLen := int(binary.BigEndian.Uint16(header[6:8]))
	payloadLen := int(binary.BigEndian.Uint32(header[8:12]))
	prefixLen := probeVirtualRouterFrameEnvelopeHeaderSize + frameTypeLen + controlTypeLen + pathLen
	if flags&probeVirtualRouterFrameEnvelopeFlagTrace != 0 {
		prefixLen += 2
	}
	if payloadLen <= 0 || prefixLen < len(header) || prefixLen > probeVirtualRouterFrameMaxBytes {
		return probeVirtualRouterFrameMessage{}, errors.New("invalid virtual router physical frame size")
	}
	payload := make([]byte, prefixLen)
	copy(payload, header)
	if _, err := io.ReadFull(reader, payload[len(header):]); err != nil {
		return probeVirtualRouterFrameMessage{}, err
	}
	traceLen := 0
	if flags&probeVirtualRouterFrameEnvelopeFlagTrace != 0 {
		traceLen = int(binary.BigEndian.Uint16(payload[prefixLen-2 : prefixLen]))
	}
	total := prefixLen + traceLen + payloadLen
	if total > probeVirtualRouterFrameMaxBytes {
		return probeVirtualRouterFrameMessage{}, errors.New("invalid virtual router physical frame size")
	}
	tail := make([]byte, traceLen+payloadLen)
	if _, err := io.ReadFull(reader, tail); err != nil {
		return probeVirtualRouterFrameMessage{}, err
	}
	payload = append(payload, tail...)
	return unmarshalProbeVirtualRouterFrameEnvelope(payload, fallbackPath)
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
	path := currentProbeVirtualRouterPathForPacket(packet, dstIP)
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp tun rx: trace_code=icmp-trace-v2 kind=%s src=%s dst=%s id=%d seq=%d local_node=%s path=%s bytes=%d", info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, currentProbeVirtualRouterLocalNodeID(), strings.Join(path, ">"), len(packet))
	}
	if len(path) < 2 {
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp tun drop: kind=%s src=%s dst=%s id=%d seq=%d reason=path_unavailable local_node=%s", info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, currentProbeVirtualRouterLocalNodeID())
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport tun drop: proto=%s src=%s:%d dst=%s:%d reason=path_unavailable local_node=%s", info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, currentProbeVirtualRouterLocalNodeID())
		}
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
		return true
	}
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

func startProbeVirtualRouterPingPongWorker(rt *probeVirtualRouterRuntime) {
	if rt == nil || !isProbeVirtualRouterRuntimeChainID(rt.cfg.chainID) {
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
				probeVirtualRouterPingPongRuntime(rt)
				timer.Reset(probeVirtualRouterPingPongInterval)
			}
		}
	}()
}

func probeVirtualRouterPingPongRuntime(rt *probeVirtualRouterRuntime) {
	if rt == nil {
		return
	}
	probed := false
	if normalizeProbeChainNodeID(rt.cfg.peerNodeID) != "" && rt.cfg.dialer {
		probed = true
		probeVirtualRouterPingPongDirection(rt, probeChainBridgeRoleToNext)
	}
	if normalizeProbeChainNodeID(rt.cfg.peerNodeID) != "" && !rt.cfg.dialer && shouldProbeVirtualRouterPrevDirection(rt) {
		probed = true
		probeVirtualRouterPingPongDirection(rt, probeChainBridgeRoleToPrev)
	}
	if !probed {
		clearProbeVirtualRouterRuntimePingError(rt.cfg.chainID)
	}
	probeVirtualRouterQueryAdjacentRTTRuntime(rt)
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
			probeVirtualRouterPingPongRuntime(runtime)
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
	if normalizeProbeChainNodeID(rt.cfg.peerNodeID) != "" && rt.cfg.dialer {
		targetNodeID = normalizeProbeChainNodeID(rt.cfg.peerNodeID)
		direction = probeChainBridgeRoleToNext
	} else if normalizeProbeChainNodeID(rt.cfg.peerNodeID) != "" && shouldProbeVirtualRouterPrevDirection(rt) {
		targetNodeID = normalizeProbeChainNodeID(rt.cfg.peerNodeID)
		direction = probeChainBridgeRoleToPrev
	}
	if targetNodeID == "" {
		recordProbeVirtualRouterRuntimeRemoteRTTError(rt.cfg.chainID, errors.New("adjacent virtual router node is unavailable"))
		return
	}
	result, err := queryProbeVirtualRouterAdjacentPing(rt, direction, targetNodeID)
	if err != nil {
		recordProbeVirtualRouterRuntimeRemoteRTTError(rt.cfg.chainID, err)
		return
	}
	recordProbeVirtualRouterRuntimeRemoteRTTControlSuccess(rt.cfg.chainID, result.LatencyMS, result.Responder)
}

func probeVirtualRouterQueryPathRTT(path []string) (time.Duration, error) {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" {
		return 0, errors.New("local virtual router node id is empty")
	}
	if len(path) < 2 || normalizeProbeChainNodeID(path[0]) != localNodeID {
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
		clean := normalizeProbeChainNodeID(nodeID)
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

func shouldProbeVirtualRouterPrevDirection(rt *probeVirtualRouterRuntime) bool {
	if rt == nil {
		return false
	}
	return probeVirtualRouterRuntimeHasPhysicalBridgeSession(rt)
}

func probeVirtualRouterPingPongDirection(rt *probeVirtualRouterRuntime, direction string) {
	if rt == nil {
		return
	}
	targetNodeID := ""
	switch normalizeProbeChainBridgeRole(direction) {
	case probeChainBridgeRoleToPrev:
		targetNodeID = normalizeProbeChainNodeID(rt.cfg.peerNodeID)
	default:
		targetNodeID = normalizeProbeChainNodeID(rt.cfg.peerNodeID)
	}
	result, err := queryProbeVirtualRouterAdjacentPing(rt, direction, targetNodeID)
	if err != nil {
		recordProbeVirtualRouterRuntimePingError(rt, direction, err)
		return
	}
	recordProbeVirtualRouterRuntimePingSuccess(rt, direction, time.Duration(result.LatencyMS)*time.Millisecond)
}

func newProbeVirtualRouterFrameLink(key string, runtime *probeVirtualRouterRuntime, carrier net.Conn, requestPath []string) *probeVirtualRouterFrameLink {
	now := time.Now()
	link := &probeVirtualRouterFrameLink{
		key:           strings.TrimSpace(key),
		runtime:       runtime,
		requestPath:   append([]string(nil), requestPath...),
		openedAt:      now,
		lastUsed:      now,
		tx:            make(chan probeVirtualRouterFrameMessage, probeVirtualRouterFrameLinkTXBufferFrames),
		rx:            make(chan probeVirtualRouterFrameMessage, probeVirtualRouterFrameLinkRXBufferFrames),
		done:          make(chan struct{}),
		carrierNotify: make(chan struct{}, 1),
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
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		token.close()
		return nil
	default:
	}
	old = s.carrier
	s.carrier = token
	s.openedAt = token.connectedAt
	s.lastUsed = token.connectedAt
	s.signalCarrierChangedLocked()
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	return token
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
	recordProbeVirtualRouterRuntimeOpenSuccess(runtime.cfg.chainID, 0)
	log.Printf("probe virtual router physical carrier connected: chain=%s role=%s session_id=%s remote=%s", strings.TrimSpace(runtime.cfg.chainID), probeVirtualRouterRuntimeRole, strings.TrimSpace(sessionID), strings.TrimSpace(remoteAddr))
	<-token.done
	log.Printf("probe virtual router physical carrier disconnected: chain=%s role=%s session_id=%s remote=%s", strings.TrimSpace(runtime.cfg.chainID), probeVirtualRouterRuntimeRole, strings.TrimSpace(sessionID), strings.TrimSpace(remoteAddr))
}

func (s *probeVirtualRouterFrameLink) Start() {
	if s == nil || s.done == nil || s.tx == nil || s.rx == nil {
		return
	}
	s.startOnce.Do(func() {
		go s.runTXWorker()
		go s.runRXWorker()
		go s.runRXDispatchWorker()
	})
}

func (s *probeVirtualRouterFrameLink) Wait() {
	if s == nil || s.done == nil {
		return
	}
	<-s.done
}

func (s *probeVirtualRouterFrameLink) EnqueueProbeVirtualRouterFrame(input probeVirtualRouterFrameMessage) error {
	if s == nil {
		return io.ErrClosedPipe
	}
	frame := probeVirtualRouterFrameMessage{
		FrameType:   strings.TrimSpace(input.FrameType),
		ControlType: strings.TrimSpace(input.ControlType),
		Payload:     append([]byte(nil), input.Payload...),
		Path:        append([]string(nil), input.Path...),
		Trace:       append([]probeVirtualRouterFrameTraceHop(nil), input.Trace...),
	}
	if frame.FrameType == "" {
		frame.FrameType = probeVirtualRouterFrameTypeData
	}
	if frame.FrameType == probeVirtualRouterFrameTypeData && frame.ControlType == "" {
		frame.ControlType = probeVirtualRouterControlTypeIPv4
	}
	if s.tx == nil || s.done == nil {
		token, err := s.waitCarrier()
		if err != nil {
			return err
		}
		if _, ok := probeVirtualRouterParseICMPEchoLogInfo(frame.Payload); ok {
			frame.Trace = appendProbeVirtualRouterICMPTrace(frame.Trace, s.runtime, "carrier_tx", "", "")
		}
		if err := writeProbeVirtualRouterFrameRaw(token.conn, frame); err != nil {
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
	}
}

func (s *probeVirtualRouterFrameLink) runTXWorker() {
	for {
		select {
		case frame := <-s.tx:
			if len(frame.Payload) == 0 {
				continue
			}
			for {
				token, err := s.waitCarrier()
				if err != nil {
					return
				}
				if _, ok := probeVirtualRouterParseICMPEchoLogInfo(frame.Payload); ok {
					frame.Trace = appendProbeVirtualRouterICMPTrace(frame.Trace, s.runtime, "carrier_tx", "", "")
				}
				err = writeProbeVirtualRouterFrameRaw(token.conn, frame)
				if err == nil {
					token.markWrite()
					s.touch()
					break
				}
				log.Printf("probe virtual router frame tx carrier failed: chain=%s key=%s path=%s err=%v", probeVirtualRouterRuntimeLogChainID(s.runtime), s.key, strings.Join(frame.Path, ">"), err)
				s.detachCarrier(token)
			}
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
		reader := bufio.NewReader(token.conn)
		for {
			frame, err := readProbeVirtualRouterFrame(reader, s.requestPath)
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !isProbeVirtualRouterClosedLinkError(err) {
					log.Printf("probe virtual router frame rx carrier failed: chain=%s key=%s err=%v", probeVirtualRouterRuntimeLogChainID(s.runtime), s.key, err)
				}
				s.detachCarrier(token)
				break
			}
			if _, ok := probeVirtualRouterParseICMPEchoLogInfo(frame.Payload); ok {
				frame.Trace = appendProbeVirtualRouterICMPTrace(frame.Trace, s.runtime, "carrier_rx", "", "")
			}
			token.markRead()
			select {
			case s.rx <- frame:
				s.touch()
			case <-s.done:
				return
			}
		}
	}
}

func (s *probeVirtualRouterFrameLink) runRXDispatchWorker() {
	for {
		select {
		case frame := <-s.rx:
			if len(frame.Payload) == 0 {
				continue
			}
			if err := handleProbeVirtualRouterFrameMessage(s.runtime, s, frame); err != nil {
				log.Printf("probe virtual router frame rx dispatch failed: chain=%s key=%s path=%s err=%v", probeVirtualRouterRuntimeLogChainID(s.runtime), s.key, strings.Join(frame.Path, ">"), err)
				continue
			}
		case <-s.done:
			return
		}
	}
}

func (s *probeVirtualRouterFrameLink) touch() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()
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

func (s *probeVirtualRouterFrameLink) detachCarrier(token *probeVirtualRouterPhysicalCarrier) {
	if s == nil || token == nil {
		return
	}
	s.mu.Lock()
	if s.carrier == token {
		s.carrier = nil
		s.signalCarrierChangedLocked()
	}
	s.mu.Unlock()
	token.close()
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
		s.signalCarrierChangedLocked()
		s.mu.Unlock()
		if token != nil {
			token.close()
		}
	})
}

func handleProbeVirtualRouterFrameMessage(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, frame probeVirtualRouterFrameMessage) error {
	frameType := strings.TrimSpace(frame.FrameType)
	if frameType == "" {
		frameType = probeVirtualRouterFrameTypeData
	}
	controlType := strings.TrimSpace(frame.ControlType)
	if frameType == probeVirtualRouterFrameTypeData && controlType == "" {
		controlType = probeVirtualRouterControlTypeIPv4
	}
	switch {
	case frameType == probeVirtualRouterFrameTypeData && controlType == probeVirtualRouterControlTypeIPv4:
		return handleProbeVirtualRouterIPFrame(runtime, link, frame.Payload, frame.Path, frame.Trace)
	case frameType == probeVirtualRouterFrameTypeControl:
		return handleProbeVirtualRouterControlFrame(runtime, link, controlType, frame.Payload, frame.Path)
	default:
		return fmt.Errorf("unsupported virtual router frame type=%s control=%s", frameType, controlType)
	}
}

func handleProbeVirtualRouterControlFrame(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, controlType string, payload []byte, framePath []string) error {
	if strings.TrimSpace(controlType) == probeVirtualRouterControlTypeSpeedChunk {
		return handleProbeVirtualRouterControlSpeedChunk(runtime, controlType, payload, framePath)
	}
	msg := probeVirtualRouterControlProbePayload{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}
	if len(msg.Path) == 0 {
		msg.Path = append([]string(nil), framePath...)
	}
	switch strings.TrimSpace(controlType) {
	case probeVirtualRouterControlTypePing:
		return handleProbeVirtualRouterControlPing(runtime, link, msg)
	case probeVirtualRouterControlTypePong:
		completeProbeVirtualRouterControlResponse(msg)
		return nil
	case probeVirtualRouterControlTypePathRTTQuery:
		return handleProbeVirtualRouterControlPathRTTQuery(runtime, msg)
	case probeVirtualRouterControlTypePathRTTResponse:
		return handleProbeVirtualRouterControlPathRTTResponse(runtime, msg)
	case probeVirtualRouterControlTypeSpeedStart, probeVirtualRouterControlTypeSpeedFinish, probeVirtualRouterControlTypeSpeedResult, probeVirtualRouterControlTypeSpeedSend:
		speedMsg := probeVirtualRouterSpeedTestResultPayload{}
		if err := json.Unmarshal(payload, &speedMsg); err != nil {
			return err
		}
		if len(speedMsg.Path) == 0 {
			speedMsg.Path = append([]string(nil), framePath...)
		}
		return handleProbeVirtualRouterControlSpeedFrame(runtime, strings.TrimSpace(controlType), speedMsg)
	default:
		return fmt.Errorf("unsupported virtual router control type=%s", controlType)
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
	response.LatencyMS = probeDurationMilliseconds(time.Since(time.Unix(0, msg.CreatedAtUnixNano)))
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if link == nil {
		return errors.New("virtual router control ping carrier is unavailable")
	}
	return link.EnqueueProbeVirtualRouterFrame(probeVirtualRouterFrameMessage{
		FrameType:   probeVirtualRouterFrameTypeControl,
		ControlType: probeVirtualRouterControlTypePong,
		Payload:     payload,
		Path:        probeVirtualRouterReversePath(msg.Path),
	})
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
	if normalizeProbeChainNodeID(msg.TargetNodeID) == localNodeID || probeVirtualRouterNextHopInPath(msg.Path, localNodeID) == "" {
		return sendProbeVirtualRouterPathRTTResponse(msg, true, msg.LatencyMS, localNodeID, "")
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterControlAlongPath(probeVirtualRouterControlTypePathRTTQuery, payload, msg.Path)
}

func handleProbeVirtualRouterControlPathRTTResponse(runtime *probeVirtualRouterRuntime, msg probeVirtualRouterControlProbePayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if normalizeProbeChainNodeID(msg.SourceNodeID) == localNodeID {
		completeProbeVirtualRouterControlResponse(msg)
		return nil
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterControlAlongPath(probeVirtualRouterControlTypePathRTTResponse, payload, probeVirtualRouterReversePath(msg.Path))
}

func sendProbeVirtualRouterPathRTTResponse(msg probeVirtualRouterControlProbePayload, ok bool, latencyMS int64, responder string, message string) error {
	msg.OK = ok
	msg.LatencyMS = latencyMS
	msg.Responder = normalizeProbeChainNodeID(responder)
	msg.Error = strings.TrimSpace(message)
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterControlAlongPath(probeVirtualRouterControlTypePathRTTResponse, payload, probeVirtualRouterReversePath(msg.Path))
}

func handleProbeVirtualRouterControlSpeedFrame(runtime *probeVirtualRouterRuntime, controlType string, msg probeVirtualRouterSpeedTestResultPayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	if localNodeID == "" || msg.RequestID == "" || len(msg.Path) < 2 {
		return errors.New("virtual router speed control frame is incomplete")
	}
	switch controlType {
	case probeVirtualRouterControlTypeSpeedStart:
		if localNodeID != msg.Path[len(msg.Path)-1] {
			return forwardProbeVirtualRouterSpeedControlAlongPath(controlType, msg, msg.Path)
		}
		startProbeVirtualRouterSpeedReceive(msg, localNodeID, probeVirtualRouterRuntimeLogChainID(runtime))
		return nil
	case probeVirtualRouterControlTypeSpeedFinish:
		if localNodeID != msg.Path[len(msg.Path)-1] {
			return forwardProbeVirtualRouterSpeedControlAlongPath(controlType, msg, msg.Path)
		}
		result, ok := finishProbeVirtualRouterSpeedReceive(msg, localNodeID)
		if !ok {
			return nil
		}
		if runtime != nil {
			recordProbeVirtualRouterRuntimeSpeedTestReceive(strings.TrimSpace(runtime.cfg.chainID), result)
		}
		if normalizeProbeChainNodeID(result.ResultNodeID) == localNodeID {
			completeProbeVirtualRouterSpeedResponse(result)
			return nil
		}
		result.Path = probeVirtualRouterReversePath(msg.Path)
		return forwardProbeVirtualRouterSpeedControlAlongPath(probeVirtualRouterControlTypeSpeedResult, result, result.Path)
	case probeVirtualRouterControlTypeSpeedResult:
		if normalizeProbeChainNodeID(msg.ResultNodeID) == localNodeID {
			completeProbeVirtualRouterSpeedResponse(msg)
			return nil
		}
		return forwardProbeVirtualRouterSpeedControlAlongPath(controlType, msg, msg.Path)
	case probeVirtualRouterControlTypeSpeedSend:
		if localNodeID != msg.Path[len(msg.Path)-1] {
			return forwardProbeVirtualRouterSpeedControlAlongPath(controlType, msg, msg.Path)
		}
		go func() {
			if err := runProbeVirtualRouterOneWaySpeedSender(probeVirtualRouterReversePath(msg.Path), msg, probeVirtualRouterSpeedTestMaxDuration); err != nil {
				response := msg
				response.OK = false
				response.Error = strings.TrimSpace(err.Error())
				response.Responder = localNodeID
				response.Path = probeVirtualRouterReversePath(msg.Path)
				_ = forwardProbeVirtualRouterSpeedControlAlongPath(probeVirtualRouterControlTypeSpeedResult, response, response.Path)
			}
		}()
		return nil
	default:
		return fmt.Errorf("unsupported virtual router speed control type=%s", controlType)
	}
}

func handleProbeVirtualRouterControlSpeedChunk(runtime *probeVirtualRouterRuntime, controlType string, payload []byte, framePath []string) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	path := cleanProbeVirtualRouterPath(framePath)
	if localNodeID == "" || len(path) < 2 {
		return errors.New("virtual router speed chunk path is incomplete")
	}
	if localNodeID != path[len(path)-1] {
		return forwardProbeVirtualRouterRawControlAlongPath(controlType, payload, path)
	}
	requestID, ok := parseProbeVirtualRouterSpeedChunkRequestID(payload)
	if !ok {
		return errors.New("invalid virtual router speed chunk payload")
	}
	recordProbeVirtualRouterSpeedChunk(requestID, int64(len(payload)))
	return nil
}

func startProbeVirtualRouterSpeedReceive(msg probeVirtualRouterSpeedTestResultPayload, localNodeID string, chainID string) {
	now := time.Now()
	session := &probeVirtualRouterSpeedReceiveSession{
		RequestID:     strings.TrimSpace(msg.RequestID),
		Direction:     strings.TrimSpace(msg.Direction),
		SourceNodeID:  normalizeProbeChainNodeID(msg.SourceNodeID),
		TargetNodeID:  normalizeProbeChainNodeID(msg.TargetNodeID),
		ResultNodeID:  normalizeProbeChainNodeID(firstNonEmpty(msg.ResultNodeID, msg.SourceNodeID)),
		Path:          append([]string(nil), msg.Path...),
		ChainID:       strings.TrimSpace(chainID),
		MaxDurationMS: msg.MaxDurationMS,
		LocalNodeID:   normalizeProbeChainNodeID(localNodeID),
		LastAt:        now,
	}
	if session.ResultNodeID == "" {
		session.ResultNodeID = normalizeProbeChainNodeID(localNodeID)
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
	result.Responder = normalizeProbeChainNodeID(localNodeID)
	result.RuntimeChainID = strings.TrimSpace(session.ChainID)
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
		result.ResultNodeID = normalizeProbeChainNodeID(firstNonEmpty(fallback.ResultNodeID, fallback.SourceNodeID))
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
	if normalizeProbeChainNodeID(result.ResultNodeID) == normalizeProbeChainNodeID(localNodeID) {
		recordProbeVirtualRouterRuntimeSpeedTestReceive(result.RuntimeChainID, result)
		completeProbeVirtualRouterSpeedResponse(result)
		return
	}
	recordProbeVirtualRouterRuntimeSpeedTestReceive(result.RuntimeChainID, result)
	result.Path = probeVirtualRouterReversePath(result.Path)
	if err := forwardProbeVirtualRouterSpeedControlAlongPath(probeVirtualRouterControlTypeSpeedResult, result, result.Path); err != nil {
		log.Printf("probe virtual router speed timed result forward failed: request_id=%s path=%s err=%v", strings.TrimSpace(result.RequestID), strings.Join(result.Path, ">"), err)
	}
}

func forwardProbeVirtualRouterSpeedControlAlongPath(controlType string, msg probeVirtualRouterSpeedTestResultPayload, path []string) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterRawControlAlongPath(controlType, payload, path)
}

func forwardProbeVirtualRouterRawControlAlongPath(controlType string, payload []byte, path []string) error {
	return forwardProbeVirtualRouterControlAlongPath(controlType, payload, path)
}

func queryProbeVirtualRouterAdjacentPing(rt *probeVirtualRouterRuntime, direction string, targetNodeID string) (probeVirtualRouterControlProbePayload, error) {
	if rt == nil {
		return probeVirtualRouterControlProbePayload{}, errors.New("runtime is nil")
	}
	targetNodeID = normalizeProbeChainNodeID(targetNodeID)
	if targetNodeID == "" {
		return probeVirtualRouterControlProbePayload{}, errors.New("adjacent virtual router node is unavailable")
	}
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(rt)
	path := []string{localNodeID, targetNodeID}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, "", path)
	if err != nil {
		return probeVirtualRouterControlProbePayload{}, err
	}
	requestID := newProbeTCPDebugFlowID("vrouter_control_ping", rt.cfg.chainID)
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
	if err := enqueueProbeVirtualRouterControlFrame(link, probeVirtualRouterControlTypePing, payload, path); err != nil {
		return probeVirtualRouterControlProbePayload{}, err
	}
	response, err := waitProbeVirtualRouterControlResponse(waiter, probeVirtualRouterPingPongTimeout)
	if err != nil {
		return probeVirtualRouterControlProbePayload{}, err
	}
	if response.LatencyMS <= 0 {
		response.LatencyMS = probeDurationMilliseconds(time.Since(startedAt))
	}
	return response, nil
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
	if err := enqueueProbeVirtualRouterControlFrame(link, probeVirtualRouterControlTypePathRTTQuery, payload, cleanPath); err != nil {
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
	sourceNodeID = normalizeProbeChainNodeID(sourceNodeID)
	targetNodeID = normalizeProbeChainNodeID(targetNodeID)
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
	chainID := ""
	if nextNodeID := probeVirtualRouterNextHopInPath(path, localNodeID); nextNodeID != "" {
		if rt, _ := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID); rt != nil {
			chainID = strings.TrimSpace(rt.cfg.chainID)
		}
	}
	recordProbeVirtualRouterRuntimeSpeedTest(chainID, sourceNodeID, targetNodeID, pathText, up, down, err)
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
	if err := forwardProbeVirtualRouterSpeedControlAlongPath(probeVirtualRouterControlTypeSpeedSend, msg, path); err != nil {
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
	if err := enqueueProbeVirtualRouterControlFrameUntil(link, probeVirtualRouterControlTypeSpeedStart, startPayload, cleanPath, time.Now().Add(2*time.Second)); err != nil {
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
		if err := enqueueProbeVirtualRouterControlFrameUntil(link, probeVirtualRouterControlTypeSpeedChunk, payload, cleanPath, deadline); err != nil {
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
	return enqueueProbeVirtualRouterControlFrameUntil(link, probeVirtualRouterControlTypeSpeedFinish, finishPayload, cleanPath, time.Now().Add(2*time.Second))
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

func forwardProbeVirtualRouterControlAlongPath(controlType string, payload []byte, path []string) error {
	cleanPath := cleanProbeVirtualRouterPath(path)
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" || len(cleanPath) < 2 {
		return errors.New("virtual router control path is incomplete")
	}
	nextNodeID := probeVirtualRouterNextHopInPath(cleanPath, localNodeID)
	if nextNodeID == "" {
		return errors.New("next virtual router control hop is unavailable")
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return errors.New("adjacent virtual router control runtime is unavailable")
	}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, "", cleanPath)
	if err != nil {
		return err
	}
	return enqueueProbeVirtualRouterControlFrame(link, controlType, payload, cleanPath)
}

func enqueueProbeVirtualRouterControlFrame(link *probeVirtualRouterFrameLink, controlType string, payload []byte, path []string) error {
	if link == nil {
		return errors.New("virtual router physical carrier is unavailable")
	}
	return link.EnqueueProbeVirtualRouterFrame(probeVirtualRouterFrameMessage{
		FrameType:   probeVirtualRouterFrameTypeControl,
		ControlType: strings.TrimSpace(controlType),
		Payload:     append([]byte(nil), payload...),
		Path:        cleanProbeVirtualRouterPath(path),
	})
}

func enqueueProbeVirtualRouterControlFrameUntil(link *probeVirtualRouterFrameLink, controlType string, payload []byte, path []string, deadline time.Time) error {
	if link == nil {
		return errors.New("virtual router physical carrier is unavailable")
	}
	if link.tx == nil || link.done == nil {
		return enqueueProbeVirtualRouterControlFrame(link, controlType, payload, path)
	}
	frame := probeVirtualRouterFrameMessage{
		FrameType:   probeVirtualRouterFrameTypeControl,
		ControlType: strings.TrimSpace(controlType),
		Payload:     append([]byte(nil), payload...),
		Path:        cleanProbeVirtualRouterPath(path),
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
		if nodeID := normalizeProbeChainNodeID(item); nodeID != "" {
			out = append(out, nodeID)
		}
	}
	return out
}

func probeVirtualRouterPreviousHopInPath(path []string, localNodeID string) string {
	local := normalizeProbeChainNodeID(localNodeID)
	if local == "" || len(path) < 2 {
		return ""
	}
	for i, item := range path {
		if normalizeProbeChainNodeID(item) != local {
			continue
		}
		if i > 0 {
			return normalizeProbeChainNodeID(path[i-1])
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
	localMatch := probeVirtualRouterIPMatches(dstIP, localIP)
	recordProbeVirtualRouterRuntimeFrameDecision(runtime, srcIP, dstIP, localIP, path, localMatch)
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "frame_rx", "", "")
		log.Printf("probe virtual router icmp frame rx: trace_code=icmp-trace-v2 chain=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s local_match=%v path=%s bytes=%d trace_hops=%d", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, localMatch, strings.Join(path, ">"), len(packet), len(trace))
	}
	if localMatch {
		if handleProbeVirtualRouterLocalICMPEchoRequest(runtime, link, packet, path, trace) {
			recordProbeVirtualRouterRuntimePacketDelivered(runtime, len(packet))
			return nil
		}
		deliverStartedAt := time.Now()
		if err := writeProbeVirtualRouterLocalTUNPacket(packet); err != nil {
			recordProbeVirtualRouterRuntimeDeliveryError(runtime, err)
			if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
				log.Printf("probe virtual router icmp local deliver failed: chain=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s err=%v", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, err)
			}
			if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
				log.Printf("probe virtual router transport local deliver failed: chain=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d local_ip=%s err=%v", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, localIP, err)
			}
			return err
		}
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			if info.Kind == "echo_reply" {
				trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "local_deliver", "", "")
				log.Printf("probe virtual router icmp local deliver ok: trace_code=icmp-trace-v2 chain=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s write_ms=%d bytes=%d trace_hops=%d", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, probeDurationMilliseconds(time.Since(deliverStartedAt)), len(packet), len(trace))
				summary, completed := recordProbeVirtualRouterICMPPingReply(runtime, info)
				if completed {
					logProbeVirtualRouterICMPPingSummary(runtime, info, trace, summary)
				} else {
					logProbeVirtualRouterICMPTraceComplete(runtime, info, trace)
				}
			} else {
				log.Printf("probe virtual router icmp local deliver ok: chain=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s write_ms=%d bytes=%d", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, probeDurationMilliseconds(time.Since(deliverStartedAt)), len(packet))
			}
		}
		recordProbeVirtualRouterRuntimePacketDelivered(runtime, len(packet))
		return nil
	}
	if len(path) < 2 {
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp frame drop: chain=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d reason=path_incomplete local_ip=%s local_match=%v path=%s", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, localMatch, strings.Join(path, ">"))
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport frame drop: chain=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d reason=path_incomplete local_ip=%s local_match=%v path=%s", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, localIP, localMatch, strings.Join(path, ">"))
		}
		return errors.New("virtual router path is incomplete")
	}
	if err := forwardProbeVirtualRouterPacketAlongPath(packet, dstIP, path, trace); err != nil {
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp frame forward failed: chain=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s err=%v", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), err)
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport frame forward failed: chain=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d path=%s err=%v", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, strings.Join(path, ">"), err)
		}
		return err
	}
	return nil
}

func writeProbeVirtualRouterLocalTUNPacket(packet []byte) error {
	if err := writeProbeLocalTUNPacket(packet); err == nil {
		return nil
	} else {
		firstErr := err
		if startErr := startProbeLocalTUNDataPlane(); startErr != nil {
			return fmt.Errorf("write local tun packet failed: %w; restart data plane failed: %v", firstErr, startErr)
		}
		if retryErr := writeProbeLocalTUNPacket(packet); retryErr != nil {
			return fmt.Errorf("write local tun packet failed after data plane restart: %w (initial: %v)", retryErr, firstErr)
		}
		return nil
	}
}

func forwardProbeVirtualRouterPacketAlongPath(packet []byte, dstIP string, path []string, trace []probeVirtualRouterFrameTraceHop) error {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" && len(path) > 0 {
		localNodeID = normalizeProbeChainNodeID(path[0])
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
		return errors.New("adjacent probe chain runtime is unavailable")
	}
	if _, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		trace = appendProbeVirtualRouterICMPTrace(trace, rt, "forward_tx", direction, nextNodeID)
	}
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp forward enqueue: trace_code=icmp-trace-v2 chain=%s local_node=%s next_node=%s direction=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s bytes=%d", probeVirtualRouterRuntimeLogChainID(rt), localNodeID, nextNodeID, direction, info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), len(packet))
	}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, dstIP, path)
	if err != nil {
		return err
	}
	if err := writeProbeVirtualRouterIPFrame(link, packet, path, trace); err != nil {
		recordProbeVirtualRouterRuntimeOpenError(rt.cfg.chainID, err)
		if !isProbeVirtualRouterClosedLinkError(err) {
			return err
		}
		link, err = ensureProbeVirtualRouterFrameLink(rt, direction, dstIP, path)
		if err != nil {
			recordProbeVirtualRouterRuntimeOpenError(rt.cfg.chainID, err)
			return err
		}
		if err := writeProbeVirtualRouterIPFrame(link, packet, path, trace); err != nil {
			recordProbeVirtualRouterRuntimeOpenError(rt.cfg.chainID, err)
			return err
		}
	}
	recordProbeVirtualRouterRuntimeFrameSent(rt, len(packet))
	recordProbeVirtualRouterRuntimePacketForwarded(rt, len(packet))
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp forward queued: trace_code=icmp-trace-v2 chain=%s local_node=%s next_node=%s direction=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s bytes=%d", probeVirtualRouterRuntimeLogChainID(rt), localNodeID, nextNodeID, direction, info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), len(packet))
	}
	return nil
}

func handleProbeVirtualRouterLocalICMPEchoRequest(runtime *probeVirtualRouterRuntime, stream *probeVirtualRouterFrameLink, packet []byte, ingressPath []string, trace []probeVirtualRouterFrameTraceHop) bool {
	localIP := currentProbeVirtualRouterLocalIPForRuntime(runtime)
	reply, dstIP, ok := buildProbeVirtualRouterICMPEchoReply(packet, localIP)
	if !ok {
		return false
	}
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
			log.Printf("probe virtual router icmp echo reply build: chain=%s runtime_node=%s request_src=%s request_dst=%s reply_src=%s reply_dst=%s id=%d seq=%d local_ip=%s path=%s", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), reqInfo.SourceIP, reqInfo.DestinationIP, replyInfo.SourceIP, replyInfo.DestinationIP, reqInfo.ID, reqInfo.Sequence, localIP, strings.Join(path, ">"))
		}
	}
	if err := writeProbeVirtualRouterIPFrame(stream, reply, path, trace); err != nil {
		if runtime != nil {
			recordProbeVirtualRouterRuntimeOpenError(runtime.cfg.chainID, err)
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
		if nodeID := normalizeProbeChainNodeID(path[i]); nodeID != "" {
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
		strings.Contains(text, "broken pipe")
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
		strings.TrimSpace(rt.cfg.chainID),
	}, "|")
}

func probeVirtualRouterRTTQueryLinkKey(rt *probeVirtualRouterRuntime, direction string, path []string) string {
	if rt == nil {
		return ""
	}
	cleanPath := make([]string, 0, len(path))
	for _, item := range path {
		if nodeID := normalizeProbeChainNodeID(item); nodeID != "" {
			cleanPath = append(cleanPath, nodeID)
		}
	}
	if len(cleanPath) < 2 {
		return ""
	}
	return strings.Join([]string{
		"rtt",
		strings.TrimSpace(rt.cfg.chainID),
		strings.TrimSpace(direction),
		strings.Join(cleanPath, ">"),
	}, "|")
}

func probeVirtualRouterRuntimeStatsForUpdateLocked(chainID string) *probeVirtualRouterRuntimeStats {
	chainID = strings.TrimSpace(chainID)
	if chainID == "" {
		return nil
	}
	item := probeVirtualRouterRuntimeStatsState.items[chainID]
	if item == nil {
		item = &probeVirtualRouterRuntimeStats{}
		probeVirtualRouterRuntimeStatsState.items[chainID] = item
	}
	return item
}

func recordProbeVirtualRouterRuntimePacketForwarded(rt *probeVirtualRouterRuntime, packetBytes int) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.chainID)
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
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.chainID)
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
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.chainID)
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
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.chainID)
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
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.chainID)
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
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.chainID)
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
	chainID := ""
	if rt != nil {
		chainID = strings.TrimSpace(rt.cfg.chainID)
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
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
	log.Printf("probe virtual router icmp echo complete: trace_code=icmp-trace-v2 complete_site=record_reply chain=%s src=%s dst=%s id=%d seq=%d latency_ms=%d path=%s", chainID, pending.SourceIP, pending.DestinationIP, pending.ID, pending.Sequence, latencyMS, pending.Path)
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
	chainID := ""
	if rt != nil {
		chainID = strings.TrimSpace(rt.cfg.chainID)
	}
	log.Printf("probe virtual router icmp trace complete: trace_code=icmp-trace-v2 chain=%s kind=%s src=%s dst=%s id=%d seq=%d hops=%d trace_clock=node_local_absolute trace=%s", chainID, info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, len(trace), probeVirtualRouterICMPTraceString(trace))
}

func logProbeVirtualRouterICMPPingSummary(rt *probeVirtualRouterRuntime, info probeVirtualRouterICMPEchoLogInfo, trace []probeVirtualRouterFrameTraceHop, summary probeVirtualRouterICMPPingCompleteSummary) {
	chainID := ""
	if rt != nil {
		chainID = strings.TrimSpace(rt.cfg.chainID)
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
	log.Printf("probe virtual router icmp echo summary: trace_code=icmp-trace-v2 chain=%s kind=%s src=%s dst=%s id=%d seq=%d latency_ms=%d path=%s trace_hops=%d trace_clock=node_local_absolute trace=%s", chainID, info.Kind, sourceIP, destinationIP, summary.ID, summary.Sequence, summary.LatencyMS, path, len(trace), traceText)
}

func recordProbeVirtualRouterRuntimeDeliveryError(rt *probeVirtualRouterRuntime, err error) {
	if rt == nil || err == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.chainID)
	if item != nil {
		item.LastDeliveryError = strings.TrimSpace(err.Error())
		item.LastPacketAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeOpenSuccess(chainID string, latency time.Duration) {
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
	if item != nil {
		item.LinkOpenCount++
		item.LastOpenLatencyMS = probeDurationMilliseconds(latency)
		item.LastOpenError = ""
		item.LastOpenAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeOpenError(chainID string, err error) {
	if err == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
	if item != nil {
		item.LastOpenError = strings.TrimSpace(err.Error())
		item.LastOpenAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimePingSuccess(rt *probeVirtualRouterRuntime, direction string, latency time.Duration) {
	chainID, bridgeStatus, bridgeSession := snapshotProbeVirtualRouterPingContext(rt, direction)
	shouldClearRouteCache := false
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
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
		clearProbeVirtualRouterRouteCache("bridge ping recovered")
	}
}

func recordProbeVirtualRouterRuntimePingError(rt *probeVirtualRouterRuntime, direction string, err error) {
	if rt == nil || err == nil {
		return
	}
	chainID, bridgeStatus, bridgeSession := snapshotProbeVirtualRouterPingContext(rt, direction)
	normalizedErr := normalizeProbeVirtualRouterBridgeError(err.Error())
	failureCount := 0
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
	if item != nil {
		item.LastPingError = normalizedErr
		item.LastPingFailureCount++
		failureCount = item.LastPingFailureCount
		item.LastPingAt = time.Now().UTC().Format(time.RFC3339)
		applyProbeVirtualRouterPingContext(item, direction, bridgeStatus, bridgeSession)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	clearProbeVirtualRouterRouteCache("bridge ping error")
	log.Printf("probe virtual router bridge ping error retained carrier: chain=%s direction=%s failures=%d err=%s", chainID, normalizeProbeChainBridgeRole(direction), failureCount, normalizedErr)
	detachProbeVirtualRouterStalePhysicalCarrier(rt, failureCount, normalizedErr)
}

func recordProbeVirtualRouterRuntimeRemoteRTTSuccess(chainID string, result probeChainFrameRTTQueryResult) {
	chainID = strings.TrimSpace(chainID)
	if chainID == "" {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
	if item != nil {
		item.LastRemoteRTTMS = result.RTTMS
		item.LastRemoteRTTAt = time.Now().UTC().Format(time.RFC3339)
		item.LastRemoteRTTError = ""
		item.LastRemoteRTTResponder = strings.TrimSpace(result.Responder)
		item.LastRemotePongsReceived = result.PongsReceived
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeRemoteRTTControlSuccess(chainID string, latencyMS int64, responder string) {
	chainID = strings.TrimSpace(chainID)
	if chainID == "" {
		return
	}
	shouldClearRouteCache := false
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
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

func recordProbeVirtualRouterRuntimeRemoteRTTError(chainID string, err error) {
	if err == nil {
		return
	}
	chainID = strings.TrimSpace(chainID)
	if chainID == "" {
		return
	}
	shouldClearRouteCache := false
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
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

func recordProbeVirtualRouterRuntimeSpeedTest(chainID string, sourceNodeID string, targetNodeID string, pathText string, up probeVirtualRouterSpeedTestResult, down probeVirtualRouterSpeedTestResult, resultErr error) {
	chainID = strings.TrimSpace(chainID)
	if chainID == "" {
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
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
	if item != nil {
		item.LastSpeedTestAt = time.Now().UTC().Format(time.RFC3339)
		item.LastSpeedTestSourceNodeID = normalizeProbeChainNodeID(sourceNodeID)
		item.LastSpeedTestTargetNodeID = normalizeProbeChainNodeID(targetNodeID)
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

func recordProbeVirtualRouterRuntimeSpeedTestReceive(chainID string, result probeVirtualRouterSpeedTestResultPayload) {
	chainID = strings.TrimSpace(chainID)
	if chainID == "" {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
	if item != nil {
		item.LastSpeedTestAt = time.Now().UTC().Format(time.RFC3339)
		item.LastSpeedTestSourceNodeID = normalizeProbeChainNodeID(result.SourceNodeID)
		item.LastSpeedTestTargetNodeID = normalizeProbeChainNodeID(result.TargetNodeID)
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

func snapshotProbeVirtualRouterPingContext(rt *probeVirtualRouterRuntime, direction string) (string, probeChainBridgeRuntimeStatus, probeChainBridgeSessionSnapshot) {
	if rt == nil {
		return "", probeChainBridgeRuntimeStatus{}, probeChainBridgeSessionSnapshot{}
	}
	snapshot, ok := snapshotProbeVirtualRouterPhysicalCarrier(rt)
	status := probeChainBridgeRuntimeStatus{UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	if ok {
		status.DownstreamActive = 1
		status.Sessions = []probeChainBridgeSessionSnapshot{snapshot}
		return strings.TrimSpace(rt.cfg.chainID), status, snapshot
	}
	return strings.TrimSpace(rt.cfg.chainID), status, probeChainBridgeSessionSnapshot{}
}

func applyProbeVirtualRouterPingContext(item *probeVirtualRouterRuntimeStats, direction string, bridgeStatus probeChainBridgeRuntimeStatus, bridgeSession probeChainBridgeSessionSnapshot) {
	if item == nil {
		return
	}
	item.LastPingDirection = normalizeProbeChainBridgeRole(direction)
	item.LastPingBridgeConnections = probeVirtualRouterBridgeConnectionCount(bridgeStatus)
	item.LastPingBridgeSessionID = strings.TrimSpace(bridgeSession.SessionID)
	item.LastPingBridgeRemote = strings.TrimSpace(bridgeSession.RemoteAddr)
	item.LastPingBridgeConnectedAt = strings.TrimSpace(bridgeSession.ConnectedAt)
}

func probeVirtualRouterBridgeConnectionCount(bridgeStatus probeChainBridgeRuntimeStatus) int {
	count := 0
	for _, session := range bridgeStatus.Sessions {
		if !session.Closed {
			count++
		}
	}
	return count
}

func snapshotProbeVirtualRouterPhysicalCarrier(rt *probeVirtualRouterRuntime) (probeChainBridgeSessionSnapshot, bool) {
	link := currentProbeVirtualRouterPhysicalCarrier(rt)
	if link == nil {
		return probeChainBridgeSessionSnapshot{}, false
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	carrier := link.carrier
	if carrier == nil {
		return probeChainBridgeSessionSnapshot{}, false
	}
	connectedAt := ""
	connectedMS := int64(0)
	if !carrier.connectedAt.IsZero() {
		connectedAt = carrier.connectedAt.UTC().Format(time.RFC3339)
		connectedMS = time.Since(carrier.connectedAt).Milliseconds()
	}
	return probeChainBridgeSessionSnapshot{
		ChainID:        strings.TrimSpace(rt.cfg.chainID),
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

func clearProbeVirtualRouterRuntimePingError(chainID string) {
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
	if item != nil {
		item.LastPingError = ""
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func snapshotProbeVirtualRouterRuntimeStats(chainID string) *probeVirtualRouterRuntimeStats {
	chainID = strings.TrimSpace(chainID)
	if chainID == "" {
		return nil
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsState.items[chainID]
	if item == nil {
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
		return nil
	}
	out := *item
	tunStats := probeLocalTUNDataPlaneStatsSnapshot()
	out.TUNDataPlane = tunStats.Running
	out.TUNRXPackets = tunStats.RXPackets
	out.TUNRXBytes = tunStats.RXBytes
	out.TUNTXPackets = tunStats.TXPackets
	out.TUNTXBytes = tunStats.TXBytes
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	return &out
}

func probeVirtualRouterRuntimeForAdjacentNode(nodeID string) (*probeVirtualRouterRuntime, string) {
	target := normalizeProbeChainNodeID(nodeID)
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
		if normalizeProbeChainNodeID(rt.cfg.peerNodeID) != target {
			continue
		}
		direction := probeChainBridgeRoleToPrev
		if rt.cfg.dialer {
			direction = probeChainBridgeRoleToNext
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
	return normalizeProbeChainBridgeRole(preferred)
}

func probeVirtualRouterRuntimeLogChainID(runtime *probeVirtualRouterRuntime) string {
	if runtime == nil {
		return ""
	}
	return strings.TrimSpace(runtime.cfg.chainID)
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
	log.Printf("probe virtual router physical carrier stale, detach for reconnect: chain=%s role=%s session_id=%s remote=%s failures=%d rx_idle_ms=%d reason=%s", strings.TrimSpace(rt.cfg.chainID), probeVirtualRouterRuntimeRole, strings.TrimSpace(token.sessionID), strings.TrimSpace(token.remoteAddr), failureCount, probeDurationMilliseconds(idleFor), strings.TrimSpace(reason))
	item.detachCarrier(token)
}

func probeVirtualRouterNextHopInPath(path []string, localNodeID string) string {
	local := normalizeProbeChainNodeID(localNodeID)
	if local == "" || len(path) < 2 {
		return ""
	}
	for i, item := range path {
		if normalizeProbeChainNodeID(item) != local {
			continue
		}
		if i+1 < len(path) {
			return normalizeProbeChainNodeID(path[i+1])
		}
		return ""
	}
	return ""
}

func probeVirtualRouterPathFromRequest(req probeChainTunnelOpenRequest) []string {
	if req.AssociationV2 == nil {
		return nil
	}
	return parseProbeVirtualRouterPathText(req.AssociationV2.RouteTarget)
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
	return probeVirtualRouterTransportLogInfo{
		Protocol:        protocol,
		SourceIP:        net.IPv4(packet[12], packet[13], packet[14], packet[15]).String(),
		DestinationIP:   net.IPv4(packet[16], packet[17], packet[18], packet[19]).String(),
		SourcePort:      binary.BigEndian.Uint16(transport[0:2]),
		DestinationPort: binary.BigEndian.Uint16(transport[2:4]),
	}, true
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
