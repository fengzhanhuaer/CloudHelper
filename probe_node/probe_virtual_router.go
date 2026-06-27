package main

import (
	"bufio"
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
	probeVirtualRouterDirectionTwoWay   = "bidirectional"
	probeVirtualRouterDirectionForward  = "forward"
	probeVirtualRouterDirectionBackward = "backward"
	probeVirtualRouterTunnelOpenType    = "virtual_router_lan_packet"
	probeVirtualRouterTunnelScope       = "virtual_router"
	probeVirtualRouterNetworkIPv4       = "ip4"
	probeVirtualRouterStreamIdleTTL     = 45 * time.Second
)

var probeVirtualRouterState = struct {
	mu          sync.RWMutex
	config      probeVirtualRouterConfig
	localNodeID string
}{}

var probeVirtualRouterStreamState = struct {
	mu      sync.Mutex
	streams map[string]*probeVirtualRouterPacketStream
}{streams: make(map[string]*probeVirtualRouterPacketStream)}

type probeVirtualRouterPacketStream struct {
	key      string
	stream   net.Conn
	openedAt time.Time
	lastUsed time.Time
	mu       sync.Mutex
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
	for index, item := range items {
		fromNodeID := normalizeProbeChainNodeID(item.FromNodeID)
		toNodeID := normalizeProbeChainNodeID(item.ToNodeID)
		direction := normalizeProbeVirtualRouterDirection(item.Direction)
		if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID || direction == "" {
			continue
		}
		fromServiceDomain := strings.TrimSpace(item.FromServiceDomain)
		fromServicePort := normalizeProbeVirtualRouterServicePort(item.FromServicePort)
		toServiceDomain := strings.TrimSpace(item.ToServiceDomain)
		toServicePort := normalizeProbeVirtualRouterServicePort(item.ToServicePort)
		ruleID := strings.TrimSpace(item.ID)
		if ruleID == "" {
			ruleID = fmt.Sprintf("vr-%s-%s-%s-%d", fromNodeID, toNodeID, direction, index+1)
		}
		key := ruleID
		if key == "" {
			key = fmt.Sprintf("%s|%s|%s|%s|%d|%s|%d", fromNodeID, toNodeID, direction, fromServiceDomain, fromServicePort, toServiceDomain, toServicePort)
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
		return out[i].Direction < out[j].Direction
	})
	return out
}

func normalizeProbeVirtualRouterServicePort(port int) int {
	if port <= 0 || port > 65535 {
		return 0
	}
	return port
}

func normalizeProbeVirtualRouterDirection(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "bidirectional", "both", "two_way", "two-way":
		return probeVirtualRouterDirectionTwoWay
	case "forward", "one_way", "one-way", "from_to", "a_to_b":
		return probeVirtualRouterDirectionForward
	case "backward", "reverse", "to_from", "b_to_a":
		return probeVirtualRouterDirectionBackward
	default:
		return ""
	}
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
	return sanitizeProbeVirtualRouterConfigForCache(payload.Item), nil
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
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = sanitized
	if cleanNodeID := normalizeProbeChainNodeID(nodeID); cleanNodeID != "" {
		probeVirtualRouterState.localNodeID = cleanNodeID
	}
	probeVirtualRouterState.mu.Unlock()
	closeProbeVirtualRouterPacketStreams("config updated")
	ensureProbeVirtualRouterLocalInterfaceIP()
}

func currentProbeVirtualRouterConfig() probeVirtualRouterConfig {
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return sanitizeProbeVirtualRouterConfigForCache(probeVirtualRouterState.config)
}

func currentProbeVirtualRouterLocalNodeID() string {
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return strings.TrimSpace(probeVirtualRouterState.localNodeID)
}

func currentProbeVirtualRouterLocalIP() string {
	probeVirtualRouterState.mu.RLock()
	config := sanitizeProbeVirtualRouterConfigForCache(probeVirtualRouterState.config)
	nodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	probeVirtualRouterState.mu.RUnlock()
	return probeVirtualRouterIPForNode(config, nodeID)
}

func ensureProbeVirtualRouterLocalInterfaceIP() {
	localIP := currentProbeVirtualRouterLocalIP()
	if localIP == "" {
		return
	}
	if err := ensureProbeVirtualRouterPlatformInterfaceIP(localIP); err != nil {
		log.Printf("warning: ensure probe virtual router local ip failed: ip=%s err=%v", localIP, err)
	}
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
		switch normalizeProbeVirtualRouterDirection(rule.Direction) {
		case probeVirtualRouterDirectionTwoWay:
			addEdge(rule.FromNodeID, rule.ToNodeID)
			addEdge(rule.ToNodeID, rule.FromNodeID)
		case probeVirtualRouterDirectionForward:
			addEdge(rule.FromNodeID, rule.ToNodeID)
		case probeVirtualRouterDirectionBackward:
			addEdge(rule.ToNodeID, rule.FromNodeID)
		}
	}
	seen := map[string]struct{}{from: {}}
	parent := map[string]string{}
	queue := []string{from}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for next := range graph[current] {
			if next == to {
				parent[next] = current
				return buildProbeVirtualRouterPath(parent, from, to)
			}
			if _, ok := seen[next]; ok {
				continue
			}
			seen[next] = struct{}{}
			parent[next] = current
			queue = append(queue, next)
		}
	}
	return nil
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

func currentProbeVirtualRouterPathToIP(ip string) []string {
	probeVirtualRouterState.mu.RLock()
	config := sanitizeProbeVirtualRouterConfigForCache(probeVirtualRouterState.config)
	nodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	probeVirtualRouterState.mu.RUnlock()
	targetNodeID := probeVirtualRouterNodeIDForIP(config, ip)
	return probeVirtualRouterPath(config, nodeID, targetNodeID)
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

func handleProbeVirtualRouterTUNPacket(packet []byte) bool {
	dstIP := probeVirtualRouterIPv4Destination(packet)
	if dstIP == "" {
		return false
	}
	path := currentProbeVirtualRouterPathToIP(dstIP)
	if len(path) < 2 {
		return false
	}
	if err := forwardProbeVirtualRouterPacketAlongPath(packet, dstIP, path); err != nil {
		log.Printf("probe virtual router packet forward failed: dst=%s path=%s err=%v", dstIP, strings.Join(path, ">"), err)
		return true
	}
	return true
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
		dstIP := probeVirtualRouterIPv4Destination(packet)
		if dstIP == "" {
			return errors.New("virtual router packet destination is invalid")
		}
		path := probeVirtualRouterPathFromRequest(req)
		if len(path) == 0 {
			path = currentProbeVirtualRouterPathToIP(dstIP)
		}
		localNodeID := currentProbeVirtualRouterLocalNodeID()
		if len(path) > 0 && normalizeProbeChainNodeID(path[len(path)-1]) == localNodeID {
			if err := writeProbeLocalTUNPacket(packet); err != nil {
				return err
			}
			continue
		}
		if len(path) < 2 {
			return errors.New("virtual router path is incomplete")
		}
		if err := forwardProbeVirtualRouterPacketAlongPath(packet, dstIP, path); err != nil {
			return err
		}
	}
}

func forwardProbeVirtualRouterPacketAlongPath(packet []byte, dstIP string, path []string) error {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
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
	stream, err := openProbeVirtualRouterPacketStream(rt, direction, dstIP, path)
	if err != nil {
		return err
	}
	if err := writeProbeChainFramedPacket(stream, packet); err != nil {
		dropProbeVirtualRouterPacketStream(stream)
		return err
	}
	return nil
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
	conn, _, err := openProbeChainPortForwardDataStreamByDialMode(rt, direction, req)
	if err != nil {
		return nil, err
	}
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

func probeVirtualRouterRuntimeForAdjacentNode(nodeID string) (*probeChainRuntime, string) {
	target := normalizeProbeChainNodeID(nodeID)
	if target == "" {
		return nil, ""
	}
	probeChainRuntimeState.mu.Lock()
	defer probeChainRuntimeState.mu.Unlock()
	for _, rt := range probeChainRuntimeState.runtimes {
		if rt == nil {
			continue
		}
		if normalizeProbeChainNodeID(rt.cfg.nextNodeID) == target && rt.cfg.nextAuthMode != "proxy" {
			return rt, probeChainBridgeRoleToNext
		}
		if normalizeProbeChainNodeID(rt.cfg.prevNodeID) == target {
			return rt, probeChainBridgeRoleToPrev
		}
	}
	return nil, ""
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
