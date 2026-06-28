package core

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	probeVirtualRouterDefaultCIDR        = "198.18.0.0/15"
	probeVirtualRouterProbeIPPoolSize    = 1024
	probeVirtualRouterMaxProbeIPCount    = 1024
	probeVirtualRouterMaxTopologyRules   = 2048
	probeVirtualRouterDefaultServicePort = 12040
	probeVirtualRouterDirectionTwoWay    = "bidirectional"
	probeVirtualRouterDirectionForward   = "forward"
	probeVirtualRouterDirectionBackward  = "backward"
	probeVirtualRouterReservedGatewayIP  = "198.18.0.1"
	probeVirtualRouterReservedTUNIP      = "198.18.0.2"
	probeVirtualRouterRuntimeChainPrefix = "vrouter-"
)

type probeVirtualRouterConfig struct {
	Enabled       bool                             `json:"enabled"`
	FakeIPCIDR    string                           `json:"fake_ip_cidr,omitempty"`
	ProbeIPs      []probeVirtualRouterProbeIP      `json:"probe_ips,omitempty"`
	TopologyRules []probeVirtualRouterTopologyRule `json:"topology_rules,omitempty"`
	UpdatedAt     string                           `json:"updated_at,omitempty"`
}

type probeVirtualRouterProbeIP struct {
	NodeID string `json:"node_id"`
	IP     string `json:"ip"`
	Note   string `json:"note,omitempty"`
}

type probeVirtualRouterTopologyRule struct {
	ID                string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	FromNodeID        string `json:"from_node_id"`
	ToNodeID          string `json:"to_node_id"`
	Direction         string `json:"direction"`
	FromServiceDomain string `json:"from_service_domain,omitempty"`
	FromServicePort   int    `json:"from_service_port,omitempty"`
	ToServiceDomain   string `json:"to_service_domain,omitempty"`
	ToServicePort     int    `json:"to_service_port,omitempty"`
	UserID            string `json:"user_id,omitempty"`
	UserPublicKey     string `json:"user_public_key,omitempty"`
	Secret            string `json:"secret,omitempty"`
	AuthTicket        string `json:"auth_ticket,omitempty"`
	Enabled           bool   `json:"enabled"`
	Note              string `json:"note,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

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
	LastPingBridgeDownstream  int    `json:"last_ping_bridge_downstream,omitempty"`
	LastPingBridgeUpstream    int    `json:"last_ping_bridge_upstream,omitempty"`
	LastPingBridgeSessionID   string `json:"last_ping_bridge_session_id,omitempty"`
	LastPingBridgeRemote      string `json:"last_ping_bridge_remote,omitempty"`
	LastPingBridgeConnectedAt string `json:"last_ping_bridge_connected_at,omitempty"`
	LastPacketAt              string `json:"last_packet_at,omitempty"`
	LastFrameAt               string `json:"last_frame_at,omitempty"`
	TUNDataPlane              bool   `json:"tun_data_plane,omitempty"`
	TUNRXPackets              uint64 `json:"tun_rx_packets,omitempty"`
	TUNRXBytes                uint64 `json:"tun_rx_bytes,omitempty"`
	TUNTXPackets              uint64 `json:"tun_tx_packets,omitempty"`
	TUNTXBytes                uint64 `json:"tun_tx_bytes,omitempty"`
}

func defaultProbeVirtualRouterConfig() probeVirtualRouterConfig {
	return probeVirtualRouterConfig{
		Enabled:       true,
		FakeIPCIDR:    probeVirtualRouterDefaultCIDR,
		ProbeIPs:      []probeVirtualRouterProbeIP{},
		TopologyRules: []probeVirtualRouterTopologyRule{},
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
}

func buildProbeVirtualRouterConfigForNodeLocked(nodeID string) probeVirtualRouterConfig {
	if ProbeLinkChainStore == nil {
		return defaultProbeVirtualRouterConfig()
	}
	config := enrichProbeVirtualRouterAuthTickets(ensureProbeVirtualRouterAuthFields(ensureProbeVirtualRouterProbeIPsForKnownNodes(normalizeProbeVirtualRouterConfig(ProbeLinkChainStore.data.VirtualRouter))))
	if !config.Enabled {
		config.ProbeIPs = []probeVirtualRouterProbeIP{}
		config.TopologyRules = []probeVirtualRouterTopologyRule{}
		return config
	}
	target := normalizeProbeNodeID(nodeID)
	rules := make([]probeVirtualRouterTopologyRule, 0, len(config.TopologyRules))
	for _, rule := range config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		if target == "" || rule.FromNodeID == target || rule.ToNodeID == target || probeVirtualRouterRuleMayAffectNode(config.TopologyRules, target, rule) {
			rules = append(rules, rule)
		}
	}
	config.TopologyRules = rules
	return config
}

func ensureProbeVirtualRouterStoredAuthFields() {
	if ProbeLinkChainStore == nil {
		return
	}
	changed := false
	ProbeLinkChainStore.mu.Lock()
	current := ProbeLinkChainStore.data.VirtualRouter
	next := ensureProbeVirtualRouterAuthFields(normalizeProbeVirtualRouterConfig(current))
	if !reflect.DeepEqual(current, next) {
		ProbeLinkChainStore.data.VirtualRouter = next
		changed = true
	}
	ProbeLinkChainStore.mu.Unlock()
	if changed {
		if err := ProbeLinkChainStore.Save(); err != nil {
			logControllerWarnf("failed to persist virtual router auth fields: %v", err)
		}
	}
}

func probeVirtualRouterRuleMayAffectNode(rules []probeVirtualRouterTopologyRule, nodeID string, candidate probeVirtualRouterTopologyRule) bool {
	target := normalizeProbeNodeID(nodeID)
	if target == "" {
		return false
	}
	graph := map[string]map[string]struct{}{}
	addEdge := func(from string, to string) {
		from = normalizeProbeNodeID(from)
		to = normalizeProbeNodeID(to)
		if from == "" || to == "" {
			return
		}
		if graph[from] == nil {
			graph[from] = map[string]struct{}{}
		}
		graph[from][to] = struct{}{}
	}
	for _, rule := range rules {
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
	affected := map[string]struct{}{}
	queue := []string{target}
	affected[target] = struct{}{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for next := range graph[current] {
			if _, seen := affected[next]; seen {
				continue
			}
			affected[next] = struct{}{}
			queue = append(queue, next)
		}
	}
	_, fromAffected := affected[normalizeProbeNodeID(candidate.FromNodeID)]
	_, toAffected := affected[normalizeProbeNodeID(candidate.ToNodeID)]
	return fromAffected || toAffected
}

func normalizeProbeVirtualRouterConfig(input probeVirtualRouterConfig) probeVirtualRouterConfig {
	now := time.Now().UTC().Format(time.RFC3339)
	cidr := strings.TrimSpace(input.FakeIPCIDR)
	if cidr == "" {
		cidr = probeVirtualRouterDefaultCIDR
	}
	out := probeVirtualRouterConfig{
		Enabled:       input.Enabled,
		FakeIPCIDR:    cidr,
		ProbeIPs:      normalizeProbeVirtualRouterProbeIPs(cidr, input.ProbeIPs),
		TopologyRules: normalizeProbeVirtualRouterTopologyRules(input.TopologyRules),
		UpdatedAt:     strings.TrimSpace(input.UpdatedAt),
	}
	if out.UpdatedAt == "" {
		out.UpdatedAt = now
	}
	return out
}

func validateAndNormalizeProbeVirtualRouterConfig(input probeVirtualRouterConfig) (probeVirtualRouterConfig, error) {
	cidr := strings.TrimSpace(input.FakeIPCIDR)
	if cidr == "" {
		cidr = probeVirtualRouterDefaultCIDR
	}
	_, network, err := net.ParseCIDR(cidr)
	if err != nil || network == nil || network.IP.To4() == nil {
		return probeVirtualRouterConfig{}, fmt.Errorf("fake_ip_cidr is invalid")
	}

	seenNode := map[string]struct{}{}
	seenIP := map[string]struct{}{}
	for index, item := range input.ProbeIPs {
		nodeID := normalizeProbeNodeID(item.NodeID)
		if nodeID == "" {
			return probeVirtualRouterConfig{}, fmt.Errorf("probe_ips[%d].node_id is required", index)
		}
		ip := normalizeProbeVirtualRouterIP(item.IP)
		if ip == "" {
			return probeVirtualRouterConfig{}, fmt.Errorf("probe_ips[%d].ip is invalid", index)
		}
		if !isProbeVirtualRouterProbeIPInPool(cidr, ip) {
			return probeVirtualRouterConfig{}, fmt.Errorf("probe_ips[%d].ip must be in first %d fake ip addresses", index, probeVirtualRouterProbeIPPoolSize)
		}
		if _, exists := seenNode[nodeID]; exists {
			return probeVirtualRouterConfig{}, fmt.Errorf("probe_ips[%d].node_id is duplicated", index)
		}
		if _, exists := seenIP[ip]; exists {
			return probeVirtualRouterConfig{}, fmt.Errorf("probe_ips[%d].ip is duplicated", index)
		}
		seenNode[nodeID] = struct{}{}
		seenIP[ip] = struct{}{}
	}
	if len(input.ProbeIPs) > probeVirtualRouterMaxProbeIPCount {
		return probeVirtualRouterConfig{}, fmt.Errorf("probe_ips exceeded limit (%d)", probeVirtualRouterMaxProbeIPCount)
	}
	if len(input.TopologyRules) > probeVirtualRouterMaxTopologyRules {
		return probeVirtualRouterConfig{}, fmt.Errorf("topology_rules exceeded limit (%d)", probeVirtualRouterMaxTopologyRules)
	}
	for index, item := range input.TopologyRules {
		fromNodeID := normalizeProbeNodeID(item.FromNodeID)
		toNodeID := normalizeProbeNodeID(item.ToNodeID)
		if fromNodeID == "" {
			return probeVirtualRouterConfig{}, fmt.Errorf("topology_rules[%d].from_node_id is required", index)
		}
		if toNodeID == "" {
			return probeVirtualRouterConfig{}, fmt.Errorf("topology_rules[%d].to_node_id is required", index)
		}
		if fromNodeID == toNodeID {
			return probeVirtualRouterConfig{}, fmt.Errorf("topology_rules[%d] endpoints must be different", index)
		}
		if normalizeProbeVirtualRouterDirection(item.Direction) == "" {
			return probeVirtualRouterConfig{}, fmt.Errorf("topology_rules[%d].direction is invalid", index)
		}
		if item.FromServicePort < 0 || item.FromServicePort > 65535 {
			return probeVirtualRouterConfig{}, fmt.Errorf("topology_rules[%d].from_service_port must be empty(default %d) or between 1 and 65535", index, probeVirtualRouterDefaultServicePort)
		}
		if item.ToServicePort < 0 || item.ToServicePort > 65535 {
			return probeVirtualRouterConfig{}, fmt.Errorf("topology_rules[%d].to_service_port must be empty(default %d) or between 1 and 65535", index, probeVirtualRouterDefaultServicePort)
		}
	}
	config := normalizeProbeVirtualRouterConfig(input)
	config.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return ensureProbeVirtualRouterProbeIPsForKnownNodes(config), nil
}

func normalizeProbeVirtualRouterProbeIPs(cidr string, items []probeVirtualRouterProbeIP) []probeVirtualRouterProbeIP {
	if len(items) == 0 {
		return []probeVirtualRouterProbeIP{}
	}
	out := make([]probeVirtualRouterProbeIP, 0, len(items))
	seenNode := map[string]struct{}{}
	seenIP := map[string]struct{}{}
	for _, item := range items {
		nodeID := normalizeProbeNodeID(item.NodeID)
		ip := normalizeProbeVirtualRouterIP(item.IP)
		if nodeID == "" || ip == "" {
			continue
		}
		if !isProbeVirtualRouterProbeIPInPool(cidr, ip) {
			continue
		}
		if _, exists := seenNode[nodeID]; exists {
			continue
		}
		if _, exists := seenIP[ip]; exists {
			continue
		}
		seenNode[nodeID] = struct{}{}
		seenIP[ip] = struct{}{}
		out = append(out, probeVirtualRouterProbeIP{
			NodeID: nodeID,
			IP:     ip,
			Note:   strings.TrimSpace(item.Note),
		})
		if len(out) >= probeVirtualRouterMaxProbeIPCount {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, _ := strconv.Atoi(out[i].NodeID)
		right, _ := strconv.Atoi(out[j].NodeID)
		if left > 0 && right > 0 && left != right {
			return left < right
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

func normalizeProbeVirtualRouterTopologyRules(items []probeVirtualRouterTopologyRule) []probeVirtualRouterTopologyRule {
	if len(items) == 0 {
		return []probeVirtualRouterTopologyRule{}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]probeVirtualRouterTopologyRule, 0, len(items))
	seen := map[string]struct{}{}
	for index, item := range items {
		fromNodeID := normalizeProbeNodeID(item.FromNodeID)
		toNodeID := normalizeProbeNodeID(item.ToNodeID)
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
		updatedAt := strings.TrimSpace(item.UpdatedAt)
		if updatedAt == "" {
			updatedAt = now
		}
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
			Secret:            firstNonEmptyProbeVirtualRouter(strings.TrimSpace(item.Secret), randomProbeNodeSecret(defaultProbeLinkChainSecretLen)),
			AuthTicket:        strings.TrimSpace(item.AuthTicket),
			Enabled:           item.Enabled,
			Note:              strings.TrimSpace(item.Note),
			UpdatedAt:         updatedAt,
		})
		if len(out) >= probeVirtualRouterMaxTopologyRules {
			break
		}
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

func ensureProbeVirtualRouterAuthFields(config probeVirtualRouterConfig) probeVirtualRouterConfig {
	user, publicKey, userErr := resolveProbeLinkUserIdentityAndPublicKey("")
	for index := range config.TopologyRules {
		rule := &config.TopologyRules[index]
		if strings.TrimSpace(rule.Secret) == "" {
			rule.Secret = randomProbeNodeSecret(defaultProbeLinkChainSecretLen)
		}
		if userErr == nil {
			if strings.TrimSpace(rule.UserID) == "" {
				rule.UserID = strings.TrimSpace(user.Username)
			}
			if strings.TrimSpace(rule.UserPublicKey) == "" {
				rule.UserPublicKey = strings.TrimSpace(publicKey)
			}
		}
	}
	return config
}

func enrichProbeVirtualRouterAuthTickets(config probeVirtualRouterConfig) probeVirtualRouterConfig {
	priv, err := loadAdminPrivateKeyForSigning()
	if err != nil {
		return config
	}
	for index := range config.TopologyRules {
		rule := &config.TopologyRules[index]
		record := probeVirtualRouterRuleAsLinkChainRecord(*rule)
		ticket, ticketErr := buildProbeLinkChainAuthTicket(record, priv)
		if ticketErr != nil {
			continue
		}
		rule.AuthTicket = ticket
	}
	return config
}

func probeVirtualRouterRuleAsLinkChainRecord(rule probeVirtualRouterTopologyRule) probeLinkChainRecord {
	return probeLinkChainRecord{
		ChainID:       probeVirtualRouterRuntimeChainID(rule),
		ClientEntryID: strings.TrimSpace(rule.ID),
		Name:          strings.TrimSpace(rule.Name),
		ChainType:     "virtual_router",
		UserID:        strings.TrimSpace(rule.UserID),
		UserPublicKey: strings.TrimSpace(rule.UserPublicKey),
		Secret:        strings.TrimSpace(rule.Secret),
		EntryNodeID:   normalizeProbeNodeID(rule.FromNodeID),
		ExitNodeID:    normalizeProbeNodeID(rule.ToNodeID),
	}
}

func normalizeProbeVirtualRouterServicePort(port int) int {
	if port <= 0 || port > 65535 {
		return probeVirtualRouterDefaultServicePort
	}
	return port
}

func probeVirtualRouterRuntimeChainID(rule probeVirtualRouterTopologyRule) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"chain",
		strings.TrimSpace(rule.ID),
		normalizeProbeNodeID(rule.FromNodeID),
		normalizeProbeNodeID(rule.ToNodeID),
		normalizeProbeVirtualRouterDirection(rule.Direction),
		strings.TrimSpace(rule.FromServiceDomain),
		strconv.Itoa(normalizeProbeVirtualRouterServicePort(rule.FromServicePort)),
		strings.TrimSpace(rule.ToServiceDomain),
		strconv.Itoa(normalizeProbeVirtualRouterServicePort(rule.ToServicePort)),
	}, "|")))
	return probeVirtualRouterRuntimeChainPrefix + hex.EncodeToString(sum[:])[:24]
}

func normalizeProbeVirtualRouterDirection(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "bidirectional", "both", "two_way", "two-way", "双向":
		return probeVirtualRouterDirectionTwoWay
	case "forward", "one_way", "one-way", "from_to", "a_to_b", "单向":
		return probeVirtualRouterDirectionForward
	case "backward", "reverse", "to_from", "b_to_a":
		return probeVirtualRouterDirectionBackward
	default:
		return ""
	}
}

func normalizeProbeVirtualRouterIP(raw string) string {
	ip := net.ParseIP(strings.TrimSpace(raw)).To4()
	if ip == nil {
		return ""
	}
	return ip.String()
}

func isProbeVirtualRouterProbeIPInPool(cidr string, ipText string) bool {
	_, network, err := net.ParseCIDR(strings.TrimSpace(firstNonEmptyProbeVirtualRouter(cidr, probeVirtualRouterDefaultCIDR)))
	if err != nil || network == nil || network.IP.To4() == nil {
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip == nil || !network.Contains(ip) {
		return false
	}
	base := binary.BigEndian.Uint32(network.IP.To4())
	value := binary.BigEndian.Uint32(ip)
	if value <= base {
		return false
	}
	if ip.String() == probeVirtualRouterReservedGatewayIP || ip.String() == probeVirtualRouterReservedTUNIP {
		return false
	}
	offset := value - base
	return offset >= 1 && offset <= uint32(probeVirtualRouterProbeIPPoolSize)
}

func probeVirtualRouterIPForNode(cidr string, nodeID string) string {
	normalized := normalizeProbeNodeID(nodeID)
	if normalized == "" {
		return ""
	}
	n, err := strconv.Atoi(normalized)
	if err != nil || n <= 0 || n > probeVirtualRouterProbeIPPoolSize {
		return ""
	}
	_, network, err := net.ParseCIDR(strings.TrimSpace(firstNonEmptyProbeVirtualRouter(cidr, probeVirtualRouterDefaultCIDR)))
	if err != nil || network == nil || network.IP.To4() == nil {
		return ""
	}
	base := binary.BigEndian.Uint32(network.IP.To4())
	for offset := uint32(1); offset <= uint32(probeVirtualRouterProbeIPPoolSize); offset++ {
		candidate := base + offset
		raw := make([]byte, 4)
		binary.BigEndian.PutUint32(raw, candidate)
		ip := net.IP(raw).String()
		if ip == probeVirtualRouterReservedGatewayIP || ip == probeVirtualRouterReservedTUNIP {
			continue
		}
		n--
		if n == 0 {
			return ip
		}
	}
	return ""
}

func firstNonEmptyProbeVirtualRouter(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func ensureProbeVirtualRouterProbeIPsForKnownNodes(config probeVirtualRouterConfig) probeVirtualRouterConfig {
	known := listProbeVirtualRouterKnownNodeIDs()
	existing := map[string]probeVirtualRouterProbeIP{}
	for _, item := range config.ProbeIPs {
		existing[normalizeProbeNodeID(item.NodeID)] = item
	}
	for _, nodeID := range known {
		if nodeID == "" {
			continue
		}
		if _, ok := existing[nodeID]; ok {
			continue
		}
		ip := probeVirtualRouterIPForNode(config.FakeIPCIDR, nodeID)
		if ip == "" {
			continue
		}
		config.ProbeIPs = append(config.ProbeIPs, probeVirtualRouterProbeIP{NodeID: nodeID, IP: ip})
	}
	config.ProbeIPs = normalizeProbeVirtualRouterProbeIPs(config.FakeIPCIDR, config.ProbeIPs)
	return config
}

func listProbeVirtualRouterKnownNodeIDs() []string {
	seen := map[string]struct{}{}
	var ids []string
	add := func(raw string) {
		nodeID := normalizeProbeNodeID(raw)
		if nodeID == "" {
			return
		}
		if _, exists := seen[nodeID]; exists {
			return
		}
		seen[nodeID] = struct{}{}
		ids = append(ids, nodeID)
	}
	if ProbeStore != nil {
		ProbeStore.mu.RLock()
		for _, node := range ProbeStore.data.ProbeNodes {
			if node.NodeNo > 0 {
				add(strconv.Itoa(node.NodeNo))
			}
		}
		ProbeStore.mu.RUnlock()
	}
	for _, rt := range listProbeRuntimes() {
		add(rt.NodeID)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		left, _ := strconv.Atoi(ids[i])
		right, _ := strconv.Atoi(ids[j])
		if left > 0 && right > 0 && left != right {
			return left < right
		}
		return ids[i] < ids[j]
	})
	return ids
}
