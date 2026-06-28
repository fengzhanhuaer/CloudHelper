package main

import (
	"bufio"
	"bytes"
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
	probeVirtualRouterDirectionForward         = "forward"
	probeVirtualRouterDefaultServicePort       = 12040
	probeVirtualRouterTunnelOpenType           = "virtual_router_lan_packet"
	probeVirtualRouterRTTQueryOpenType         = "virtual_router_rtt_query"
	probeVirtualRouterTunnelScope              = "virtual_router"
	probeVirtualRouterNetworkIPv4              = "ip4"
	probeVirtualRouterStreamIdleTTL            = 45 * time.Second
	probeVirtualRouterPingPongInterval         = 30 * time.Second
	probeVirtualRouterPingPongTimeout          = 5 * time.Second
	probeVirtualRouterPingPongBytes            = 64
	probeVirtualRouterPacketPrewarmMinInterval = 10 * time.Second
)

var probeVirtualRouterState = struct {
	mu          sync.RWMutex
	config      probeVirtualRouterConfig
	localNodeID string
	localIP     string
	nodeToIP    map[string]string
	ipToNode    map[string]string
	neighbors   map[string]map[string]struct{}
	rulesByID   map[string]probeVirtualRouterTopologyRule
}{}

type probeVirtualRouterTopologyIndex struct {
	nodeToIP  map[string]string
	ipToNode  map[string]string
	neighbors map[string]map[string]struct{}
	rulesByID map[string]probeVirtualRouterTopologyRule
}

var probeVirtualRouterStreamState = struct {
	mu      sync.Mutex
	streams map[string]*probeVirtualRouterPacketStream
}{streams: make(map[string]*probeVirtualRouterPacketStream)}

var probeVirtualRouterRouteCacheState = struct {
	mu     sync.RWMutex
	routes map[string][]string
}{routes: make(map[string][]string)}

var probeVirtualRouterPathRTTState = struct {
	mu    sync.RWMutex
	items map[string]probeVirtualRouterPathRTTRecord
}{items: make(map[string]probeVirtualRouterPathRTTRecord)}

var probeVirtualRouterRuntimeStatsState = struct {
	mu    sync.Mutex
	items map[string]*probeVirtualRouterRuntimeStats
}{items: make(map[string]*probeVirtualRouterRuntimeStats)}

var probeVirtualRouterLocalInterfaceRetryState = struct {
	mu      sync.Mutex
	running bool
}{}

var probeVirtualRouterPacketPrewarmState = struct {
	mu            sync.Mutex
	running       bool
	lastStartedAt time.Time
}{}

type probeVirtualRouterRuntimeStats struct {
	PacketsForwarded          int64  `json:"packets_forwarded,omitempty"`
	BytesForwarded            int64  `json:"bytes_forwarded,omitempty"`
	PacketsReceived           int64  `json:"packets_received,omitempty"`
	BytesReceived             int64  `json:"bytes_received,omitempty"`
	PacketsDelivered          int64  `json:"packets_delivered,omitempty"`
	BytesDelivered            int64  `json:"bytes_delivered,omitempty"`
	FramesSent                int64  `json:"frames_sent,omitempty"`
	FrameBytesSent            int64  `json:"frame_bytes_sent,omitempty"`
	FramesReceived            int64  `json:"frames_received,omitempty"`
	FrameBytesReceived        int64  `json:"frame_bytes_received,omitempty"`
	StreamOpenCount           int64  `json:"stream_open_count,omitempty"`
	LastOpenLatencyMS         int64  `json:"last_open_latency_ms,omitempty"`
	LastOpenError             string `json:"last_open_error,omitempty"`
	LastOpenAt                string `json:"last_open_at,omitempty"`
	PingCount                 int64  `json:"ping_count,omitempty"`
	LastPingLatencyMS         int64  `json:"last_ping_latency_ms,omitempty"`
	LastPingError             string `json:"last_ping_error,omitempty"`
	LastPingAt                string `json:"last_ping_at,omitempty"`
	LastPingDirection         string `json:"last_ping_direction,omitempty"`
	LastPingBridgeConnections int    `json:"last_ping_bridge_connections,omitempty"`
	LastPingBridgeSessionID   string `json:"last_ping_bridge_session_id,omitempty"`
	LastPingBridgeRemote      string `json:"last_ping_bridge_remote,omitempty"`
	LastPingBridgeConnectedAt string `json:"last_ping_bridge_connected_at,omitempty"`
	LastRemoteRTTMS           int64  `json:"last_remote_rtt_ms,omitempty"`
	LastRemoteRTTAt           string `json:"last_remote_rtt_at,omitempty"`
	LastRemoteRTTError        string `json:"last_remote_rtt_error,omitempty"`
	LastRemoteRTTResponder    string `json:"last_remote_rtt_responder,omitempty"`
	LastRemotePongsReceived   int64  `json:"last_remote_pongs_received,omitempty"`
	LastPacketAt              string `json:"last_packet_at,omitempty"`
	LastFrameAt               string `json:"last_frame_at,omitempty"`
	LastFrameSourceIP         string `json:"last_frame_source_ip,omitempty"`
	LastFrameDestinationIP    string `json:"last_frame_destination_ip,omitempty"`
	LastFrameLocalIP          string `json:"last_frame_local_ip,omitempty"`
	LastFrameLocalMatch       string `json:"last_frame_local_match,omitempty"`
	LastFramePath             string `json:"last_frame_path,omitempty"`
	LastFrameRuntimeNodeID    string `json:"last_frame_runtime_node_id,omitempty"`
	LastDeliveryError         string `json:"last_delivery_error,omitempty"`
	TUNDataPlane              bool   `json:"tun_data_plane,omitempty"`
	TUNRXPackets              uint64 `json:"tun_rx_packets,omitempty"`
	TUNRXBytes                uint64 `json:"tun_rx_bytes,omitempty"`
	TUNTXPackets              uint64 `json:"tun_tx_packets,omitempty"`
	TUNTXBytes                uint64 `json:"tun_tx_bytes,omitempty"`
}

type probeVirtualRouterICMPEchoLogInfo struct {
	Kind          string
	SourceIP      string
	DestinationIP string
	ID            uint16
	Sequence      uint16
}

type probeVirtualRouterTransportLogInfo struct {
	Protocol        string
	SourceIP        string
	DestinationIP   string
	SourcePort      uint16
	DestinationPort uint16
}

type probeVirtualRouterPacketStream struct {
	key      string
	stream   net.Conn
	openedAt time.Time
	lastUsed time.Time
	mu       sync.Mutex
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
		fromServiceDomain := strings.TrimSpace(item.FromServiceDomain)
		fromServicePort := normalizeProbeVirtualRouterServicePort(item.FromServicePort)
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
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FromNodeID != out[j].FromNodeID {
			return out[i].FromNodeID < out[j].FromNodeID
		}
		if out[i].ToNodeID != out[j].ToNodeID {
			return out[i].ToNodeID < out[j].ToNodeID
		}
		return strings.TrimSpace(out[i].ID) < strings.TrimSpace(out[j].ID)
	})
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
	probeVirtualRouterState.config = sanitized
	effectiveNodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	if cleanNodeID := normalizeProbeChainNodeID(nodeID); cleanNodeID != "" {
		probeVirtualRouterState.localNodeID = cleanNodeID
		effectiveNodeID = cleanNodeID
	}
	probeVirtualRouterState.nodeToIP = index.nodeToIP
	probeVirtualRouterState.ipToNode = index.ipToNode
	probeVirtualRouterState.neighbors = index.neighbors
	probeVirtualRouterState.rulesByID = index.rulesByID
	probeVirtualRouterState.localIP = index.nodeToIP[effectiveNodeID]
	probeVirtualRouterState.mu.Unlock()
	clearProbeVirtualRouterRouteCache("config updated")
	closeProbeVirtualRouterPacketStreams("config updated")
	ensureProbeVirtualRouterLocalInterfaceIP()
	scheduleProbeVirtualRouterPacketStreamPrewarm("config_updated")
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

func currentProbeVirtualRouterLocalNodeIDForRuntime(runtime *probeChainRuntime) string {
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

func currentProbeVirtualRouterLocalIPForRuntime(runtime *probeChainRuntime) string {
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
	markProbeLocalTUNInterfaceReady()
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
	probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
	if probeVirtualRouterLocalInterfaceRetryState.running {
		probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
		return
	}
	probeVirtualRouterLocalInterfaceRetryState.running = true
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
		log.Printf("probe virtual router local ip retry scheduled: ip=%s reason=%v", strings.TrimSpace(localIP), cause)
		for attempt, delay := range delays {
			time.Sleep(delay)
			nextIP, err := ensureProbeVirtualRouterLocalInterfaceIPOnce()
			if nextIP == "" {
				log.Printf("probe virtual router local ip retry stopped: reason=local_ip_empty attempt=%d", attempt+1)
				return
			}
			if err != nil {
				log.Printf("warning: probe virtual router local ip retry failed: ip=%s attempt=%d err=%v", nextIP, attempt+1, err)
				continue
			}
			log.Printf("probe virtual router local ip retry succeeded: ip=%s attempt=%d", nextIP, attempt+1)
			markProbeLocalTUNInterfaceReady()
			return
		}
		log.Printf("warning: probe virtual router local ip retry exhausted: ip=%s attempts=%d", strings.TrimSpace(localIP), len(delays))
	}()
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
	target := ""
	if len(path) > 0 {
		target = normalizeProbeChainNodeID(path[len(path)-1])
	}
	probeVirtualRouterPathRTTState.mu.Lock()
	if probeVirtualRouterPathRTTState.items == nil {
		probeVirtualRouterPathRTTState.items = make(map[string]probeVirtualRouterPathRTTRecord)
	}
	probeVirtualRouterPathRTTState.items[key] = probeVirtualRouterPathRTTRecord{
		RTTMS:      probeDurationMilliseconds(latency),
		LastAt:     time.Now().UTC(),
		TargetNode: target,
		Responder:  strings.TrimSpace(responder),
	}
	probeVirtualRouterPathRTTState.mu.Unlock()
	clearProbeVirtualRouterRouteCache("path rtt query success")
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
	probeVirtualRouterPathRTTState.mu.Lock()
	if probeVirtualRouterPathRTTState.items == nil {
		probeVirtualRouterPathRTTState.items = make(map[string]probeVirtualRouterPathRTTRecord)
	}
	item := probeVirtualRouterPathRTTState.items[key]
	item.LastAt = time.Now().UTC()
	item.LastError = strings.TrimSpace(err.Error())
	item.TargetNode = target
	probeVirtualRouterPathRTTState.items[key] = item
	probeVirtualRouterPathRTTState.mu.Unlock()
	clearProbeVirtualRouterRouteCache("path rtt query error")
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

func probeVirtualRouterPacketTargetsLocalIP(runtime *probeChainRuntime, dstIP string) bool {
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

func buildProbeVirtualRouterTunnelOpenRequest(dstIP string, path []string) probeChainTunnelOpenRequest {
	cleanPath := make([]string, 0, len(path))
	for _, item := range path {
		if nodeID := normalizeProbeChainNodeID(item); nodeID != "" {
			cleanPath = append(cleanPath, nodeID)
		}
	}
	targetNodeID := ""
	if len(cleanPath) > 0 {
		targetNodeID = cleanPath[len(cleanPath)-1]
	}
	flowID := newProbeTCPDebugFlowID("vrouter", strings.TrimSpace(dstIP))
	return probeChainTunnelOpenRequest{
		Type:      probeVirtualRouterTunnelOpenType,
		Scope:     probeVirtualRouterTunnelScope,
		Network:   probeVirtualRouterNetworkIPv4,
		Address:   strings.TrimSpace(dstIP),
		FlowID:    flowID,
		Priority:  "realtime",
		RequestID: flowID,
		AssociationV2: &probeChainAssociationV2Meta{
			Version:     2,
			FlowID:      flowID,
			Transport:   probeVirtualRouterNetworkIPv4,
			RouteNodeID: targetNodeID,
			RouteTarget: strings.Join(cleanPath, ">"),
		},
	}
}

func buildProbeVirtualRouterRTTQueryOpenRequest(path []string) probeChainTunnelOpenRequest {
	cleanPath := make([]string, 0, len(path))
	for _, item := range path {
		if nodeID := normalizeProbeChainNodeID(item); nodeID != "" {
			cleanPath = append(cleanPath, nodeID)
		}
	}
	targetNodeID := ""
	if len(cleanPath) > 0 {
		targetNodeID = cleanPath[len(cleanPath)-1]
	}
	flowID := newProbeTCPDebugFlowID("vrouter_rtt", strings.Join(cleanPath, ">"))
	return probeChainTunnelOpenRequest{
		Type:      probeVirtualRouterRTTQueryOpenType,
		Scope:     probeVirtualRouterTunnelScope,
		Network:   "rtt",
		Address:   targetNodeID,
		FlowID:    flowID,
		Priority:  "realtime",
		RequestID: flowID,
		PingBytes: probeVirtualRouterPingPongBytes,
		AssociationV2: &probeChainAssociationV2Meta{
			Version:     2,
			FlowID:      flowID,
			Transport:   "rtt",
			RouteNodeID: targetNodeID,
			RouteTarget: strings.Join(cleanPath, ">"),
		},
	}
}

func handleProbeVirtualRouterTUNPacket(packet []byte) bool {
	dstIP := probeVirtualRouterIPv4Destination(packet)
	if dstIP == "" {
		return false
	}
	path := currentProbeVirtualRouterPathForPacket(packet, dstIP)
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp tun rx: kind=%s src=%s dst=%s id=%d seq=%d local_node=%s path=%s bytes=%d", info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, currentProbeVirtualRouterLocalNodeID(), strings.Join(path, ">"), len(packet))
	}
	if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
		log.Printf("probe virtual router transport tun rx: proto=%s src=%s:%d dst=%s:%d local_node=%s path=%s bytes=%d", info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, currentProbeVirtualRouterLocalNodeID(), strings.Join(path, ">"), len(packet))
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
	if err := forwardProbeVirtualRouterPacketAlongPath(packet, dstIP, path); err != nil {
		log.Printf("probe virtual router packet forward failed: dst=%s path=%s err=%v", dstIP, strings.Join(path, ">"), err)
		return true
	}
	return true
}

func startProbeVirtualRouterPingPongWorker(rt *probeChainRuntime) {
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

func probeVirtualRouterPingPongRuntime(rt *probeChainRuntime) {
	if rt == nil {
		return
	}
	probed := false
	if normalizeProbeChainNodeID(rt.cfg.nextNodeID) != "" && normalizeProbeChainDialMode(rt.cfg.nextDialMode) == probeChainDialModeForward {
		probed = true
		probeVirtualRouterPingPongDirection(rt, probeChainBridgeRoleToNext)
	}
	if normalizeProbeChainNodeID(rt.cfg.prevNodeID) != "" && shouldProbeVirtualRouterPrevDirection(rt) {
		probed = true
		probeVirtualRouterPingPongDirection(rt, probeChainBridgeRoleToPrev)
	}
	if !probed {
		clearProbeVirtualRouterRuntimePingError(rt.cfg.chainID)
	}
	probeVirtualRouterQueryAdjacentRTTRuntime(rt)
}

func probeVirtualRouterPingPongAllRuntimes() int {
	probeChainRuntimeState.mu.Lock()
	runtimes := make([]*probeChainRuntime, 0, len(probeChainRuntimeState.runtimes))
	for _, rt := range probeChainRuntimeState.runtimes {
		if rt == nil || !isProbeVirtualRouterRuntimeChainID(rt.cfg.chainID) {
			continue
		}
		runtimes = append(runtimes, rt)
	}
	probeChainRuntimeState.mu.Unlock()

	var wg sync.WaitGroup
	for _, rt := range runtimes {
		wg.Add(1)
		go func(runtime *probeChainRuntime) {
			defer wg.Done()
			probeVirtualRouterPingPongRuntime(runtime)
		}(rt)
	}
	wg.Wait()
	probeVirtualRouterQueryAllPathRTTs()
	return len(runtimes)
}

func probeVirtualRouterQueryAdjacentRTTRuntime(rt *probeChainRuntime) {
	if rt == nil {
		return
	}
	sessionItem := rt.latestPhysicalBridgeSession()
	if sessionItem == nil || sessionItem.Session == nil || sessionItem.Session.IsClosed() {
		recordProbeVirtualRouterRuntimeRemoteRTTError(rt.cfg.chainID, errors.New("physical bridge session is unavailable"))
		return
	}
	result, err := sessionItem.Session.QueryRemoteRTT(probeVirtualRouterPingPongTimeout)
	if err != nil {
		recordProbeVirtualRouterRuntimeRemoteRTTError(rt.cfg.chainID, err)
		return
	}
	recordProbeVirtualRouterRuntimeRemoteRTTSuccess(rt.cfg.chainID, result)
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
		err := errors.New("adjacent virtual router ping-pong latency is unavailable")
		recordProbeVirtualRouterPathRTTError(path, err)
		return 0, err
	}
	if len(path) == 2 {
		latency := time.Duration(localHopLatencyMS) * time.Millisecond
		recordProbeVirtualRouterPathRTTSuccess(path, latency, nextNodeID)
		return latency, nil
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return 0, errors.New("adjacent virtual router rtt runtime is unavailable")
	}
	stream, err := openProbeVirtualRouterRTTQueryStream(rt, direction, path)
	if err != nil {
		recordProbeVirtualRouterPathRTTError(path, err)
		return 0, err
	}
	response, err := queryProbeVirtualRouterPathRTTSum(stream)
	if err != nil {
		dropProbeVirtualRouterPacketStream(stream)
		recordProbeVirtualRouterPathRTTError(path, err)
		return 0, err
	}
	if !response.OK {
		err = errors.New(strings.TrimSpace(response.Error))
		if err.Error() == "" {
			err = errors.New("virtual router rtt query failed")
		}
		dropProbeVirtualRouterPacketStream(stream)
		recordProbeVirtualRouterPathRTTError(path, err)
		return 0, err
	}
	latency := time.Duration(localHopLatencyMS+response.LatencyMS) * time.Millisecond
	recordProbeVirtualRouterPathRTTSuccess(path, latency, response.Responder)
	return latency, nil
}

func openProbeVirtualRouterRTTQueryStream(rt *probeChainRuntime, direction string, path []string) (net.Conn, error) {
	if rt == nil {
		return nil, errors.New("runtime is nil")
	}
	key := probeVirtualRouterRTTQueryStreamKey(rt, direction, path)
	if key == "" {
		return nil, errors.New("rtt query stream key is empty")
	}
	now := time.Now()
	if stream := reusableProbeVirtualRouterPacketStream(key, now); stream != nil {
		return stream, nil
	}
	req := buildProbeVirtualRouterRTTQueryOpenRequest(path)
	stream, _, err := openProbeVirtualRouterPhysicalBridgeStream(rt, req)
	if err != nil {
		return nil, err
	}
	item := &probeVirtualRouterPacketStream{
		key:      key,
		stream:   stream,
		openedAt: now,
		lastUsed: now,
	}
	probeVirtualRouterStreamState.mu.Lock()
	if old := probeVirtualRouterStreamState.streams[key]; old != nil {
		_ = old.stream.Close()
	}
	probeVirtualRouterStreamState.streams[key] = item
	probeVirtualRouterStreamState.mu.Unlock()
	return item, nil
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
				log.Printf("probe virtual router path rtt query failed: path=%s err=%v", strings.Join(pathCopy, ">"), err)
			}
		}()
	}
	wg.Wait()
	return len(paths)
}

func shouldProbeVirtualRouterPrevDirection(rt *probeChainRuntime) bool {
	if rt == nil {
		return false
	}
	if normalizeProbeChainDialMode(rt.cfg.prevDialMode) == probeChainDialModeReverse {
		return true
	}
	return probeVirtualRouterRuntimeHasPhysicalBridgeSession(rt)
}

func probeVirtualRouterPingPongDirection(rt *probeChainRuntime, direction string) {
	if rt == nil {
		return
	}
	req := probeChainTunnelOpenRequest{
		Type:      probeChainRelayModePingPong,
		PingBytes: probeVirtualRouterPingPongBytes,
		Priority:  "realtime",
		RequestID: newProbeTCPDebugFlowID("vrouter_ping", rt.cfg.chainID),
	}
	conn, _, err := openProbeVirtualRouterPhysicalBridgeStream(rt, req)
	if err != nil {
		recordProbeVirtualRouterRuntimePingError(rt, direction, err)
		return
	}
	defer conn.Close()

	payload := make([]byte, probeVirtualRouterPingPongBytes)
	for i := range payload {
		payload[i] = byte((i*29 + 7) % 251)
	}
	echo := make([]byte, len(payload))
	startedAt := time.Now()
	_ = conn.SetDeadline(time.Now().Add(probeVirtualRouterPingPongTimeout))
	if _, err := conn.Write(payload); err != nil {
		_ = conn.SetDeadline(time.Time{})
		recordProbeVirtualRouterRuntimePingError(rt, direction, err)
		return
	}
	if _, err := io.ReadFull(conn, echo); err != nil {
		_ = conn.SetDeadline(time.Time{})
		recordProbeVirtualRouterRuntimePingError(rt, direction, err)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if !bytes.Equal(payload, echo) {
		recordProbeVirtualRouterRuntimePingError(rt, direction, errors.New("virtual router ping-pong echo mismatch"))
		return
	}
	recordProbeVirtualRouterRuntimePingSuccess(rt, direction, time.Since(startedAt))
}

func isProbeVirtualRouterStreamOpenType(openType string) bool {
	clean := strings.TrimSpace(openType)
	return strings.EqualFold(clean, probeVirtualRouterTunnelOpenType) || strings.EqualFold(clean, probeVirtualRouterRTTQueryOpenType)
}

func handleProbeVirtualRouterOpenStream(runtime *probeChainRuntime, stream net.Conn, req probeChainTunnelOpenRequest, responder func(probeChainTunnelOpenResponse) error) error {
	if strings.EqualFold(strings.TrimSpace(req.Type), probeVirtualRouterRTTQueryOpenType) {
		return handleProbeVirtualRouterRTTQueryStream(runtime, stream, req, responder)
	}
	return handleProbeVirtualRouterFrameStream(runtime, stream, req, responder)
}

func handleProbeVirtualRouterFrameStream(runtime *probeChainRuntime, stream net.Conn, req probeChainTunnelOpenRequest, responder func(probeChainTunnelOpenResponse) error) error {
	if responder == nil {
		return errors.New("missing frame open responder")
	}
	if stream == nil {
		_ = responder(probeChainTunnelOpenResponse{OK: false, Error: "stream is nil"})
		return errors.New("stream is nil")
	}
	if err := responder(probeChainTunnelOpenResponse{OK: true}); err != nil {
		return err
	}
	reader := bufio.NewReader(stream)
	for {
		packet, err := readProbeChainFramedPacket(reader)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		recordProbeVirtualRouterRuntimeFrameReceived(runtime, len(packet))
		dstIP := probeVirtualRouterIPv4Destination(packet)
		if dstIP == "" {
			return errors.New("virtual router packet destination is invalid")
		}
		recordProbeVirtualRouterRuntimePacketReceived(runtime, len(packet))
		path := probeVirtualRouterPathFromRequest(req)
		if len(path) == 0 {
			path = currentProbeVirtualRouterPathToIP(dstIP)
		}
		srcIP := probeVirtualRouterIPv4Source(packet)
		localIP := currentProbeVirtualRouterLocalIPForRuntime(runtime)
		localMatch := probeVirtualRouterIPMatches(dstIP, localIP)
		recordProbeVirtualRouterRuntimeFrameDecision(runtime, srcIP, dstIP, localIP, path, localMatch)
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp frame rx: chain=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s local_match=%v path=%s bytes=%d", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, localMatch, strings.Join(path, ">"), len(packet))
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport frame rx: chain=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d local_ip=%s local_match=%v path=%s bytes=%d", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, localIP, localMatch, strings.Join(path, ">"), len(packet))
		}
		if localMatch {
			if handleProbeVirtualRouterLocalICMPEchoRequest(runtime, packet, path) {
				recordProbeVirtualRouterRuntimePacketDelivered(runtime, len(packet))
				continue
			}
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
				log.Printf("probe virtual router icmp local deliver ok: chain=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s bytes=%d", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, len(packet))
			}
			if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
				log.Printf("probe virtual router transport local deliver ok: chain=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d local_ip=%s bytes=%d", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, localIP, len(packet))
			}
			recordProbeVirtualRouterRuntimePacketDelivered(runtime, len(packet))
			continue
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
		if err := forwardProbeVirtualRouterPacketAlongPath(packet, dstIP, path); err != nil {
			if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
				log.Printf("probe virtual router icmp frame forward failed: chain=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s err=%v", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), err)
			}
			if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
				log.Printf("probe virtual router transport frame forward failed: chain=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d path=%s err=%v", probeVirtualRouterRuntimeLogChainID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, strings.Join(path, ">"), err)
			}
			return err
		}
	}
}

func handleProbeVirtualRouterRTTQueryStream(runtime *probeChainRuntime, stream net.Conn, req probeChainTunnelOpenRequest, responder func(probeChainTunnelOpenResponse) error) error {
	if responder == nil {
		return errors.New("missing rtt query open responder")
	}
	if stream == nil {
		_ = responder(probeChainTunnelOpenResponse{OK: false, Error: "stream is nil"})
		return errors.New("stream is nil")
	}
	path := probeVirtualRouterPathFromRequest(req)
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if localNodeID == "" && len(path) > 0 {
		localNodeID = normalizeProbeChainNodeID(path[0])
	}
	if localNodeID == "" || len(path) < 2 {
		_ = responder(probeChainTunnelOpenResponse{OK: false, Error: "virtual router rtt path is incomplete"})
		return errors.New("virtual router rtt path is incomplete")
	}
	targetNodeID := normalizeProbeChainNodeID(path[len(path)-1])
	if targetNodeID == "" {
		_ = responder(probeChainTunnelOpenResponse{OK: false, Error: "virtual router rtt target is empty"})
		return errors.New("virtual router rtt target is empty")
	}
	if localNodeID == targetNodeID {
		if err := responder(probeChainTunnelOpenResponse{OK: true}); err != nil {
			return err
		}
		return serveProbeVirtualRouterPathRTTSumTarget(stream, localNodeID)
	}
	nextNodeID := probeVirtualRouterNextHopInPath(path, localNodeID)
	if nextNodeID == "" {
		_ = responder(probeChainTunnelOpenResponse{OK: false, Error: "next virtual router rtt hop is unavailable"})
		return errors.New("next virtual router rtt hop is unavailable")
	}
	localHopLatencyMS, ok := currentProbeVirtualRouterAdjacentLatencyMS(localNodeID, nextNodeID)
	if !ok {
		_ = responder(probeChainTunnelOpenResponse{OK: false, Error: "adjacent virtual router ping-pong latency is unavailable"})
		return errors.New("adjacent virtual router ping-pong latency is unavailable")
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		_ = responder(probeChainTunnelOpenResponse{OK: false, Error: "adjacent virtual router rtt runtime is unavailable"})
		return errors.New("adjacent virtual router rtt runtime is unavailable")
	}
	nextStream, err := openProbeVirtualRouterRTTQueryStream(rt, direction, path)
	if err != nil {
		_ = responder(probeChainTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	if err := responder(probeChainTunnelOpenResponse{OK: true}); err != nil {
		return err
	}
	return relayProbeVirtualRouterPathRTTSum(stream, nextStream, localHopLatencyMS)
}

func serveProbeVirtualRouterPathRTTSumTarget(stream net.Conn, localNodeID string) error {
	for {
		_, err := readProbeVirtualRouterRTTQueryRequest(stream)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		response := probeVirtualRouterPathRTTQueryResponse{
			OK:        true,
			LatencyMS: 0,
			Responder: normalizeProbeChainNodeID(localNodeID),
		}
		if err := writeProbeVirtualRouterRTTQueryResponse(stream, response); err != nil {
			return err
		}
	}
}

func relayProbeVirtualRouterPathRTTSum(left net.Conn, right net.Conn, localHopLatencyMS int64) error {
	for {
		_, err := readProbeVirtualRouterRTTQueryRequest(left)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		response, err := queryProbeVirtualRouterPathRTTSum(right)
		if err != nil {
			dropProbeVirtualRouterPacketStream(right)
			return err
		}
		if response.OK {
			response.LatencyMS += localHopLatencyMS
		}
		if err := writeProbeVirtualRouterRTTQueryResponse(left, response); err != nil {
			return err
		}
	}
}

func queryProbeVirtualRouterPathRTTSum(stream net.Conn) (probeVirtualRouterPathRTTQueryResponse, error) {
	request := probeVirtualRouterPathRTTQueryRequest{RequestID: newProbeTCPDebugFlowID("vrouter_rtt_sum", "")}
	payload, err := json.Marshal(request)
	if err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	if wrapped, ok := stream.(*probeVirtualRouterPacketStream); ok {
		wrapped.mu.Lock()
		defer wrapped.mu.Unlock()
		if err := writeProbeVirtualRouterRTTQueryPayload(wrapped.stream, payload); err != nil {
			return probeVirtualRouterPathRTTQueryResponse{}, err
		}
		response, err := readProbeVirtualRouterRTTQueryResponse(wrapped.stream)
		if err == nil {
			wrapped.lastUsed = time.Now()
		}
		return response, err
	}
	if err := writeProbeVirtualRouterRTTQueryPayload(stream, payload); err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	return readProbeVirtualRouterRTTQueryResponse(stream)
}

func readProbeVirtualRouterRTTQueryRequest(stream net.Conn) (probeVirtualRouterPathRTTQueryRequest, error) {
	payload, err := readProbeVirtualRouterRTTQueryPayload(stream)
	if err != nil {
		return probeVirtualRouterPathRTTQueryRequest{}, err
	}
	request := probeVirtualRouterPathRTTQueryRequest{}
	if err := json.Unmarshal(payload, &request); err != nil {
		return probeVirtualRouterPathRTTQueryRequest{}, err
	}
	return request, nil
}

func writeProbeVirtualRouterRTTQueryResponse(stream net.Conn, response probeVirtualRouterPathRTTQueryResponse) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return writeProbeVirtualRouterRTTQueryPayload(stream, payload)
}

func readProbeVirtualRouterRTTQueryResponse(stream net.Conn) (probeVirtualRouterPathRTTQueryResponse, error) {
	payload, err := readProbeVirtualRouterRTTQueryPayload(stream)
	if err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	response := probeVirtualRouterPathRTTQueryResponse{}
	if err := json.Unmarshal(payload, &response); err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	return response, nil
}

func writeProbeVirtualRouterRTTQueryPayload(stream net.Conn, payload []byte) error {
	_ = stream.SetDeadline(time.Now().Add(probeVirtualRouterPingPongTimeout))
	err := writeProbeChainFramedPacket(stream, payload)
	_ = stream.SetDeadline(time.Time{})
	return err
}

func readProbeVirtualRouterRTTQueryPayload(stream net.Conn) ([]byte, error) {
	_ = stream.SetDeadline(time.Now().Add(probeVirtualRouterPingPongTimeout))
	frame, err := readProbeChainFrame(stream)
	_ = stream.SetDeadline(time.Time{})
	if err != nil {
		return nil, err
	}
	if frame.Kind != probeChainFrameKindData {
		return nil, errors.New("invalid virtual router rtt frame kind")
	}
	if len(frame.Control) != 0 {
		return nil, errors.New("invalid virtual router rtt frame control")
	}
	if len(frame.Data) == 0 {
		return nil, errors.New("empty virtual router rtt frame data")
	}
	return frame.Data, nil
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

func forwardProbeVirtualRouterPacketAlongPath(packet []byte, dstIP string, path []string) error {
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
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp forward open: chain=%s local_node=%s next_node=%s direction=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s bytes=%d", probeVirtualRouterRuntimeLogChainID(rt), localNodeID, nextNodeID, direction, info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), len(packet))
	}
	if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
		log.Printf("probe virtual router transport forward open: chain=%s local_node=%s next_node=%s direction=%s proto=%s src=%s:%d dst=%s:%d path=%s bytes=%d", probeVirtualRouterRuntimeLogChainID(rt), localNodeID, nextNodeID, direction, info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, strings.Join(path, ">"), len(packet))
	}
	stream, err := openProbeVirtualRouterPacketStream(rt, direction, dstIP, path)
	if err != nil {
		return err
	}
	if err := writeProbeChainFramedPacket(stream, packet); err != nil {
		dropProbeVirtualRouterPacketStream(stream)
		recordProbeVirtualRouterRuntimeOpenError(rt.cfg.chainID, err)
		return err
	}
	recordProbeVirtualRouterRuntimeFrameSent(rt, len(packet))
	recordProbeVirtualRouterRuntimePacketForwarded(rt, len(packet))
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp forward sent: chain=%s local_node=%s next_node=%s direction=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s bytes=%d", probeVirtualRouterRuntimeLogChainID(rt), localNodeID, nextNodeID, direction, info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), len(packet))
	}
	if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
		log.Printf("probe virtual router transport forward sent: chain=%s local_node=%s next_node=%s direction=%s proto=%s src=%s:%d dst=%s:%d path=%s bytes=%d", probeVirtualRouterRuntimeLogChainID(rt), localNodeID, nextNodeID, direction, info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, strings.Join(path, ">"), len(packet))
	}
	return nil
}

func handleProbeVirtualRouterLocalICMPEchoRequest(runtime *probeChainRuntime, packet []byte, ingressPath []string) bool {
	localIP := currentProbeVirtualRouterLocalIPForRuntime(runtime)
	reply, dstIP, ok := buildProbeVirtualRouterICMPEchoReply(packet, localIP)
	if !ok {
		return false
	}
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
	if err := forwardProbeVirtualRouterPacketAlongPath(reply, dstIP, path); err != nil {
		if runtime != nil {
			recordProbeVirtualRouterRuntimeOpenError(runtime.cfg.chainID, err)
		}
		log.Printf("probe virtual router icmp echo reply forward failed: dst=%s path=%s err=%v", dstIP, strings.Join(path, ">"), err)
		return false
	}
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

func openProbeVirtualRouterPacketStream(rt *probeChainRuntime, direction string, dstIP string, path []string) (net.Conn, error) {
	if rt == nil {
		return nil, errors.New("runtime is nil")
	}
	key := probeVirtualRouterPacketStreamKey(rt, direction, dstIP, path)
	if key == "" {
		return nil, errors.New("packet stream key is empty")
	}
	now := time.Now()
	if stream := reusableProbeVirtualRouterPacketStream(key, now); stream != nil {
		return stream, nil
	}
	req := buildProbeVirtualRouterTunnelOpenRequest(dstIP, path)
	startedAt := time.Now()
	conn, _, err := openProbeVirtualRouterPhysicalBridgeStream(rt, req)
	if err != nil {
		recordProbeVirtualRouterRuntimeOpenError(rt.cfg.chainID, err)
		return nil, err
	}
	recordProbeVirtualRouterRuntimeOpenSuccess(rt.cfg.chainID, time.Since(startedAt))
	item := &probeVirtualRouterPacketStream{
		key:      key,
		stream:   conn,
		openedAt: now,
		lastUsed: now,
	}
	probeVirtualRouterStreamState.mu.Lock()
	if old := probeVirtualRouterStreamState.streams[key]; old != nil {
		_ = old.stream.Close()
	}
	probeVirtualRouterStreamState.streams[key] = item
	probeVirtualRouterStreamState.mu.Unlock()
	return item, nil
}

func scheduleProbeVirtualRouterPacketStreamPrewarm(reason string) {
	now := time.Now()
	probeVirtualRouterPacketPrewarmState.mu.Lock()
	if probeVirtualRouterPacketPrewarmState.running || now.Sub(probeVirtualRouterPacketPrewarmState.lastStartedAt) < probeVirtualRouterPacketPrewarmMinInterval {
		probeVirtualRouterPacketPrewarmState.mu.Unlock()
		return
	}
	probeVirtualRouterPacketPrewarmState.running = true
	probeVirtualRouterPacketPrewarmState.lastStartedAt = now
	probeVirtualRouterPacketPrewarmState.mu.Unlock()

	go func() {
		defer func() {
			probeVirtualRouterPacketPrewarmState.mu.Lock()
			probeVirtualRouterPacketPrewarmState.running = false
			probeVirtualRouterPacketPrewarmState.mu.Unlock()
		}()
		prewarmProbeVirtualRouterPacketStreams(reason)
	}()
}

func prewarmProbeVirtualRouterPacketStreams(reason string) {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" {
		return
	}
	probeVirtualRouterState.mu.RLock()
	nodeToIP := probeVirtualRouterCloneNodeToIPLocked()
	probeVirtualRouterState.mu.RUnlock()
	nodeIDs := make([]string, 0, len(nodeToIP))
	for nodeID := range nodeToIP {
		if normalizeProbeChainNodeID(nodeID) != localNodeID {
			nodeIDs = append(nodeIDs, normalizeProbeChainNodeID(nodeID))
		}
	}
	sort.Strings(nodeIDs)
	opened := 0
	failed := 0
	for _, nodeID := range nodeIDs {
		dstIP := strings.TrimSpace(nodeToIP[nodeID])
		if dstIP == "" {
			continue
		}
		path := currentProbeVirtualRouterPathBetweenNodes(localNodeID, nodeID)
		if len(path) < 2 {
			continue
		}
		nextNodeID := probeVirtualRouterNextHopInPath(path, localNodeID)
		if nextNodeID == "" {
			continue
		}
		rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
		if rt == nil {
			failed++
			continue
		}
		if _, err := openProbeVirtualRouterPacketStream(rt, direction, dstIP, path); err != nil {
			failed++
			log.Printf("probe virtual router packet stream prewarm failed: reason=%s dst_node=%s dst_ip=%s path=%s err=%v", strings.TrimSpace(reason), nodeID, dstIP, strings.Join(path, ">"), err)
			continue
		}
		opened++
	}
	if opened > 0 || failed > 0 {
		log.Printf("probe virtual router packet stream prewarm completed: reason=%s opened=%d failed=%d", strings.TrimSpace(reason), opened, failed)
	}
}

func reusableProbeVirtualRouterPacketStream(key string, now time.Time) net.Conn {
	probeVirtualRouterStreamState.mu.Lock()
	item := probeVirtualRouterStreamState.streams[key]
	if item == nil {
		probeVirtualRouterStreamState.mu.Unlock()
		return nil
	}
	if now.Sub(item.lastUsed) > probeVirtualRouterStreamIdleTTL {
		delete(probeVirtualRouterStreamState.streams, key)
		probeVirtualRouterStreamState.mu.Unlock()
		_ = item.stream.Close()
		return nil
	}
	item.lastUsed = now
	probeVirtualRouterStreamState.mu.Unlock()
	return item
}

func dropProbeVirtualRouterPacketStream(conn net.Conn) {
	if conn == nil {
		return
	}
	dropped := false
	probeVirtualRouterStreamState.mu.Lock()
	for key, item := range probeVirtualRouterStreamState.streams {
		if item != nil && (item == conn || item.stream == conn) {
			delete(probeVirtualRouterStreamState.streams, key)
			dropped = true
			break
		}
	}
	probeVirtualRouterStreamState.mu.Unlock()
	if dropped {
		_ = conn.Close()
	}
}

func closeProbeVirtualRouterPacketStreams(reason string) {
	probeVirtualRouterStreamState.mu.Lock()
	streams := probeVirtualRouterStreamState.streams
	probeVirtualRouterStreamState.streams = make(map[string]*probeVirtualRouterPacketStream)
	probeVirtualRouterStreamState.mu.Unlock()
	for _, item := range streams {
		if item != nil && item.stream != nil {
			_ = item.stream.Close()
		}
	}
	if len(streams) > 0 {
		log.Printf("probe virtual router packet streams closed: count=%d reason=%s", len(streams), strings.TrimSpace(reason))
	}
}

func probeVirtualRouterPacketStreamKey(rt *probeChainRuntime, direction string, dstIP string, path []string) string {
	if rt == nil {
		return ""
	}
	cleanPath := make([]string, 0, len(path))
	for _, item := range path {
		if nodeID := normalizeProbeChainNodeID(item); nodeID != "" {
			cleanPath = append(cleanPath, nodeID)
		}
	}
	return strings.Join([]string{
		strings.TrimSpace(rt.cfg.chainID),
		strings.TrimSpace(direction),
		strings.TrimSpace(dstIP),
		strings.Join(cleanPath, ">"),
	}, "|")
}

func probeVirtualRouterRTTQueryStreamKey(rt *probeChainRuntime, direction string, path []string) string {
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

func recordProbeVirtualRouterRuntimePacketForwarded(rt *probeChainRuntime, packetBytes int) {
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

func recordProbeVirtualRouterRuntimePacketReceived(rt *probeChainRuntime, packetBytes int) {
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

func recordProbeVirtualRouterRuntimePacketDelivered(rt *probeChainRuntime, packetBytes int) {
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

func recordProbeVirtualRouterRuntimeFrameSent(rt *probeChainRuntime, frameBytes int) {
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

func recordProbeVirtualRouterRuntimeFrameReceived(rt *probeChainRuntime, frameBytes int) {
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

func recordProbeVirtualRouterRuntimeFrameDecision(rt *probeChainRuntime, srcIP string, dstIP string, localIP string, path []string, localMatch bool) {
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

func recordProbeVirtualRouterRuntimeDeliveryError(rt *probeChainRuntime, err error) {
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
		item.StreamOpenCount++
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

func recordProbeVirtualRouterRuntimePingSuccess(rt *probeChainRuntime, direction string, latency time.Duration) {
	chainID, bridgeStatus, bridgeSession := snapshotProbeVirtualRouterPingContext(rt, direction)
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
	if item != nil {
		item.PingCount++
		item.LastPingLatencyMS = probeDurationMilliseconds(latency)
		item.LastPingError = ""
		item.LastPingAt = time.Now().UTC().Format(time.RFC3339)
		applyProbeVirtualRouterPingContext(item, direction, bridgeStatus, bridgeSession)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	clearProbeVirtualRouterRouteCache("bridge ping success")
	scheduleProbeVirtualRouterPacketStreamPrewarm("bridge_ping_success")
}

func recordProbeVirtualRouterRuntimePingError(rt *probeChainRuntime, direction string, err error) {
	if rt == nil || err == nil {
		return
	}
	chainID, bridgeStatus, bridgeSession := snapshotProbeVirtualRouterPingContext(rt, direction)
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
	if item != nil {
		item.LastPingError = normalizeProbeVirtualRouterBridgeError(err.Error())
		item.LastPingAt = time.Now().UTC().Format(time.RFC3339)
		applyProbeVirtualRouterPingContext(item, direction, bridgeStatus, bridgeSession)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	clearProbeVirtualRouterRouteCache("bridge ping error")
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
	clearProbeVirtualRouterRouteCache("remote rtt query success")
}

func recordProbeVirtualRouterRuntimeRemoteRTTError(chainID string, err error) {
	if err == nil {
		return
	}
	chainID = strings.TrimSpace(chainID)
	if chainID == "" {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(chainID)
	if item != nil {
		item.LastRemoteRTTError = strings.TrimSpace(err.Error())
		item.LastRemoteRTTAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	clearProbeVirtualRouterRouteCache("remote rtt query error")
}

func normalizeProbeVirtualRouterBridgeError(value string) string {
	text := strings.TrimSpace(value)
	text = strings.ReplaceAll(text, "upstream bridge", "bridge")
	text = strings.ReplaceAll(text, "downstream bridge", "bridge")
	return text
}

func snapshotProbeVirtualRouterPingContext(rt *probeChainRuntime, direction string) (string, probeChainBridgeRuntimeStatus, probeChainBridgeSessionSnapshot) {
	if rt == nil {
		return "", probeChainBridgeRuntimeStatus{}, probeChainBridgeSessionSnapshot{}
	}
	bridgeStatus := rt.snapshotBridgeStatus()
	var selected probeChainBridgeSessionSnapshot
	for _, session := range bridgeStatus.Sessions {
		if session.Closed {
			continue
		}
		if selected.ConnectedAt == "" || session.ConnectedAt > selected.ConnectedAt {
			selected = session
		}
	}
	return strings.TrimSpace(rt.cfg.chainID), bridgeStatus, selected
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

func probeVirtualRouterRuntimeForAdjacentNode(nodeID string) (*probeChainRuntime, string) {
	target := normalizeProbeChainNodeID(nodeID)
	if target == "" {
		return nil, ""
	}
	probeChainRuntimeState.mu.Lock()
	defer probeChainRuntimeState.mu.Unlock()
	if rt, direction := findProbeVirtualRouterRuntimeForAdjacentNodeLocked(target, true); rt != nil {
		return rt, direction
	}
	return findProbeVirtualRouterRuntimeForAdjacentNodeLocked(target, false)
}

func findProbeVirtualRouterRuntimeForAdjacentNodeLocked(target string, virtualOnly bool) (*probeChainRuntime, string) {
	var fallbackRT *probeChainRuntime
	var fallbackDirection string
	for _, rt := range probeChainRuntimeState.runtimes {
		if rt == nil {
			continue
		}
		if virtualOnly != isProbeVirtualRouterRuntimeChainID(rt.cfg.chainID) {
			continue
		}
		if normalizeProbeChainNodeID(rt.cfg.nextNodeID) == target && rt.cfg.nextAuthMode != "proxy" {
			direction := selectProbeVirtualRouterBridgeDirection(rt, probeChainBridgeRoleToNext)
			if probeVirtualRouterRuntimeHasBridgeSession(rt, direction) {
				return rt, direction
			}
			if fallbackRT == nil {
				fallbackRT = rt
				fallbackDirection = direction
			}
		}
		if normalizeProbeChainNodeID(rt.cfg.prevNodeID) == target {
			direction := selectProbeVirtualRouterBridgeDirection(rt, probeChainBridgeRoleToPrev)
			if probeVirtualRouterRuntimeHasBridgeSession(rt, direction) {
				return rt, direction
			}
			if fallbackRT == nil {
				fallbackRT = rt
				fallbackDirection = direction
			}
		}
	}
	if fallbackRT != nil {
		return fallbackRT, fallbackDirection
	}
	return nil, ""
}

func selectProbeVirtualRouterBridgeDirection(rt *probeChainRuntime, preferred string) string {
	return normalizeProbeChainBridgeRole(preferred)
}

func probeVirtualRouterRuntimeLogChainID(runtime *probeChainRuntime) string {
	if runtime == nil {
		return ""
	}
	return strings.TrimSpace(runtime.cfg.chainID)
}

func probeVirtualRouterRuntimeHasBridgeSession(rt *probeChainRuntime, direction string) bool {
	if rt == nil {
		return false
	}
	if rt.singleBridgeSessionPerRule() {
		return probeVirtualRouterRuntimeHasPhysicalBridgeSession(rt)
	}
	switch normalizeProbeChainBridgeRole(direction) {
	case probeChainBridgeRoleToPrev:
		return rt.getUpstreamSession() != nil
	default:
		return rt.getDownstreamSession() != nil
	}
}

func probeVirtualRouterRuntimeHasPhysicalBridgeSession(rt *probeChainRuntime) bool {
	if rt == nil {
		return false
	}
	return rt.getDownstreamSession() != nil || rt.getUpstreamSession() != nil
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
	raw := strings.TrimSpace(req.AssociationV2.RouteTarget)
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

func (s *probeVirtualRouterPacketStream) Read(p []byte) (int, error) {
	if s == nil || s.stream == nil {
		return 0, io.ErrClosedPipe
	}
	return s.stream.Read(p)
}

func (s *probeVirtualRouterPacketStream) Write(p []byte) (int, error) {
	if s == nil || s.stream == nil {
		return 0, io.ErrClosedPipe
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.stream.Write(p)
	if err == nil {
		s.lastUsed = time.Now()
	}
	return n, err
}

func (s *probeVirtualRouterPacketStream) Close() error {
	if s == nil || s.stream == nil {
		return nil
	}
	return s.stream.Close()
}

func (s *probeVirtualRouterPacketStream) LocalAddr() net.Addr {
	if s == nil || s.stream == nil {
		return nil
	}
	return s.stream.LocalAddr()
}

func (s *probeVirtualRouterPacketStream) RemoteAddr() net.Addr {
	if s == nil || s.stream == nil {
		return nil
	}
	return s.stream.RemoteAddr()
}

func (s *probeVirtualRouterPacketStream) SetDeadline(t time.Time) error {
	if s == nil || s.stream == nil {
		return io.ErrClosedPipe
	}
	return s.stream.SetDeadline(t)
}

func (s *probeVirtualRouterPacketStream) SetReadDeadline(t time.Time) error {
	if s == nil || s.stream == nil {
		return io.ErrClosedPipe
	}
	return s.stream.SetReadDeadline(t)
}

func (s *probeVirtualRouterPacketStream) SetWriteDeadline(t time.Time) error {
	if s == nil || s.stream == nil {
		return io.ErrClosedPipe
	}
	return s.stream.SetWriteDeadline(t)
}
