package core

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	probeVirtualRouterDefaultCIDR           = "198.18.0.0/15"
	probeVirtualRouterProbeIPPoolSize       = 1024
	probeVirtualRouterFakeIPDefaultTTL      = 30 * 24 * time.Hour
	probeVirtualRouterFakeIPRenewInterval   = 48 * time.Hour
	probeVirtualRouterMaxProbeIPCount       = 1024
	probeVirtualRouterMaxFakeIPCount        = 65536
	probeVirtualRouterMaxTopologyRules      = 2048
	probeVirtualRouterMaxRouteRules         = 2048
	probeVirtualRouterMaxRouteRuleEntries   = 8192
	probeVirtualRouterDefaultServicePort    = 12040
	probeVirtualRouterDefaultSecretLen      = 48
	probeVirtualRouterDirectionForward      = "forward"
	probeVirtualRouterRouteRuleActionExit   = "probe_exit"
	probeVirtualRouterRouteRuleActionDirect = "direct"
	probeVirtualRouterRouteRuleActionReject = "reject"
	probeVirtualRouterReservedGatewayIP     = "198.18.0.1"
	probeVirtualRouterReservedTUNIP         = "198.18.0.2"
	probeVirtualRouterRuntimeChainPrefix    = "vrouter-"
)

type probeVirtualRouterConfig struct {
	Enabled       bool                             `json:"enabled"`
	FakeIPCIDR    string                           `json:"fake_ip_cidr,omitempty"`
	ProbeIPs      []probeVirtualRouterProbeIP      `json:"probe_ips,omitempty"`
	TopologyRules []probeVirtualRouterTopologyRule `json:"topology_rules,omitempty"`
	RouteRules    []probeVirtualRouterRouteRule    `json:"route_rules,omitempty"`
	FakeIPLibrary probeVirtualRouterFakeIPLibrary  `json:"fake_ip_library,omitempty"`
	UpdatedAt     string                           `json:"updated_at,omitempty"`
}

type probeVirtualRouterFakeIPLibrary struct {
	Version   int64                           `json:"version"`
	UpdatedAt string                          `json:"updated_at,omitempty"`
	Items     []probeVirtualRouterFakeIPEntry `json:"items,omitempty"`
}

type probeVirtualRouterFakeIPEntry struct {
	Domain     string `json:"domain"`
	FakeIP     string `json:"fake_ip"`
	RuleID     string `json:"rule_id,omitempty"`
	Action     string `json:"action,omitempty"`
	ExitNodeID string `json:"exit_node_id,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
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

type probeVirtualRouterRouteRule struct {
	ID         string   `json:"id,omitempty"`
	Name       string   `json:"name"`
	Action     string   `json:"action,omitempty"`
	ExitNodeID string   `json:"exit_node_id,omitempty"`
	Entries    []string `json:"entries,omitempty"`
	Note       string   `json:"note,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
}

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
	TUNDataPlane                bool    `json:"tun_data_plane,omitempty"`
	TUNRXPackets                uint64  `json:"tun_rx_packets,omitempty"`
	TUNRXBytes                  uint64  `json:"tun_rx_bytes,omitempty"`
	TUNTXPackets                uint64  `json:"tun_tx_packets,omitempty"`
	TUNTXBytes                  uint64  `json:"tun_tx_bytes,omitempty"`
}

func defaultProbeVirtualRouterConfig() probeVirtualRouterConfig {
	return probeVirtualRouterConfig{
		Enabled:       false,
		FakeIPCIDR:    probeVirtualRouterDefaultCIDR,
		ProbeIPs:      []probeVirtualRouterProbeIP{},
		TopologyRules: []probeVirtualRouterTopologyRule{},
		RouteRules:    []probeVirtualRouterRouteRule{},
		FakeIPLibrary: defaultProbeVirtualRouterFakeIPLibrary(),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
}

func defaultProbeVirtualRouterFakeIPLibrary() probeVirtualRouterFakeIPLibrary {
	return probeVirtualRouterFakeIPLibrary{
		Version:   1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Items:     []probeVirtualRouterFakeIPEntry{},
	}
}

func buildProbeVirtualRouterConfigForNodeLocked(nodeID string) probeVirtualRouterConfig {
	if ProbeRouteConfigStore == nil {
		return defaultProbeVirtualRouterConfig()
	}
	config := enrichProbeVirtualRouterAuthTickets(ensureProbeVirtualRouterAuthFields(ensureProbeVirtualRouterProbeIPsForKnownNodes(normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter))))
	config.FakeIPLibrary = normalizeProbeVirtualRouterFakeIPLibrary(ProbeRouteConfigStore.data.VirtualRouterFakeIP)
	if !config.Enabled {
		config.ProbeIPs = []probeVirtualRouterProbeIP{}
		config.TopologyRules = []probeVirtualRouterTopologyRule{}
		return config
	}
	return config
}

func ensureProbeVirtualRouterStoredAuthFields() {
	if ProbeRouteConfigStore == nil {
		return
	}
	changed := false
	ProbeRouteConfigStore.mu.Lock()
	current := ProbeRouteConfigStore.data.VirtualRouter
	next := ensureProbeVirtualRouterAuthFields(normalizeProbeVirtualRouterConfig(current))
	if !reflect.DeepEqual(current, next) {
		ProbeRouteConfigStore.data.VirtualRouter = next
		changed = true
	}
	ProbeRouteConfigStore.mu.Unlock()
	if changed {
		if err := ProbeRouteConfigStore.Save(); err != nil {
			logControllerWarnf("failed to persist virtual router auth fields: %v", err)
		}
	}
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
		RouteRules:    normalizeProbeVirtualRouterRouteRules(input.RouteRules),
		FakeIPLibrary: normalizeProbeVirtualRouterFakeIPLibrary(input.FakeIPLibrary),
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
	if len(input.RouteRules) > probeVirtualRouterMaxRouteRules {
		return probeVirtualRouterConfig{}, fmt.Errorf("route_rules exceeded limit (%d)", probeVirtualRouterMaxRouteRules)
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
		if item.ToServicePort < 0 || item.ToServicePort > 65535 {
			return probeVirtualRouterConfig{}, fmt.Errorf("topology_rules[%d].to_service_port must be empty(default %d) or between 1 and 65535", index, probeVirtualRouterDefaultServicePort)
		}
	}
	for index, item := range input.RouteRules {
		if strings.TrimSpace(item.Name) == "" {
			return probeVirtualRouterConfig{}, fmt.Errorf("route_rules[%d].name is required", index)
		}
		action := normalizeProbeVirtualRouterRouteRuleAction(item.Action, item.ExitNodeID)
		if action == "" {
			return probeVirtualRouterConfig{}, fmt.Errorf("route_rules[%d].action is invalid", index)
		}
		if action == probeVirtualRouterRouteRuleActionExit {
			exitNodeID := normalizeProbeNodeID(item.ExitNodeID)
			if exitNodeID == "" {
				return probeVirtualRouterConfig{}, fmt.Errorf("route_rules[%d].exit_node_id is required", index)
			}
			if !isProbeVirtualRouterKnownNodeID(exitNodeID) {
				return probeVirtualRouterConfig{}, fmt.Errorf("route_rules[%d].exit_node_id is unknown", index)
			}
		}
		if len(item.Entries) > probeVirtualRouterMaxRouteRuleEntries {
			return probeVirtualRouterConfig{}, fmt.Errorf("route_rules[%d].entries exceeded limit (%d)", index, probeVirtualRouterMaxRouteRuleEntries)
		}
		for entryIndex, entry := range item.Entries {
			if _, ok := normalizeProbeVirtualRouterRouteRuleEntry(entry); !ok {
				return probeVirtualRouterConfig{}, fmt.Errorf("route_rules[%d].entries[%d] is invalid", index, entryIndex)
			}
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
	reserved := collectProbeVirtualRouterReservedRuleIDs(items)
	nextRuleSeq := 1
	for _, item := range items {
		fromNodeID := normalizeProbeNodeID(item.FromNodeID)
		toNodeID := normalizeProbeNodeID(item.ToNodeID)
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
			Secret:            firstNonEmptyProbeVirtualRouter(strings.TrimSpace(item.Secret), randomProbeNodeSecret(probeVirtualRouterDefaultSecretLen)),
			AuthTicket:        strings.TrimSpace(item.AuthTicket),
			Enabled:           item.Enabled,
			Note:              strings.TrimSpace(item.Note),
			UpdatedAt:         updatedAt,
		})
		if len(out) >= probeVirtualRouterMaxTopologyRules {
			break
		}
	}
	return out
}

func normalizeProbeVirtualRouterRouteRules(items []probeVirtualRouterRouteRule) []probeVirtualRouterRouteRule {
	if len(items) == 0 {
		return []probeVirtualRouterRouteRule{}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]probeVirtualRouterRouteRule, 0, len(items))
	seen := map[string]struct{}{}
	reserved := collectProbeVirtualRouterReservedRouteRuleIDs(items)
	nextSeq := 1
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		ruleID := strings.TrimSpace(item.ID)
		if ruleID == "" {
			ruleID, nextSeq = allocateProbeVirtualRouterRouteRuleID(seen, reserved, nextSeq)
		}
		key := ruleID
		if key == "" {
			key = strings.ToLower(name)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entries := normalizeProbeVirtualRouterRouteRuleEntries(item.Entries)
		action := normalizeProbeVirtualRouterRouteRuleAction(item.Action, item.ExitNodeID)
		exitNodeID := ""
		if action == probeVirtualRouterRouteRuleActionExit {
			exitNodeID = normalizeProbeNodeID(item.ExitNodeID)
		}
		updatedAt := strings.TrimSpace(item.UpdatedAt)
		if updatedAt == "" {
			updatedAt = now
		}
		out = append(out, probeVirtualRouterRouteRule{
			ID:         ruleID,
			Name:       name,
			Action:     action,
			ExitNodeID: exitNodeID,
			Entries:    entries,
			Note:       strings.TrimSpace(item.Note),
			UpdatedAt:  updatedAt,
		})
		if len(out) >= probeVirtualRouterMaxRouteRules {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := strings.ToLower(firstNonEmptyProbeVirtualRouter(out[i].Name, out[i].ID))
		right := strings.ToLower(firstNonEmptyProbeVirtualRouter(out[j].Name, out[j].ID))
		if left != right {
			return left < right
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func normalizeProbeVirtualRouterFakeIPLibrary(input probeVirtualRouterFakeIPLibrary) probeVirtualRouterFakeIPLibrary {
	now := time.Now().UTC()
	updatedAt := strings.TrimSpace(input.UpdatedAt)
	if updatedAt == "" {
		updatedAt = now.Format(time.RFC3339)
	}
	version := input.Version
	if version <= 0 {
		version = 1
	}
	out := probeVirtualRouterFakeIPLibrary{
		Version:   version,
		UpdatedAt: updatedAt,
		Items:     []probeVirtualRouterFakeIPEntry{},
	}
	seenDomain := map[string]struct{}{}
	seenIP := map[string]struct{}{}
	for _, item := range input.Items {
		domain := normalizeProbeVirtualRouterFakeIPDomain(item.Domain)
		ip := normalizeProbeVirtualRouterIP(item.FakeIP)
		if domain == "" || ip == "" {
			continue
		}
		if _, exists := seenDomain[domain]; exists {
			continue
		}
		if _, exists := seenIP[ip]; exists {
			continue
		}
		expiresAt := strings.TrimSpace(item.ExpiresAt)
		if expiresAt == "" {
			expiresAt = now.Add(probeVirtualRouterFakeIPDefaultTTL).Format(time.RFC3339)
		}
		itemUpdatedAt := strings.TrimSpace(item.UpdatedAt)
		if itemUpdatedAt == "" {
			itemUpdatedAt = updatedAt
		}
		seenDomain[domain] = struct{}{}
		seenIP[ip] = struct{}{}
		out.Items = append(out.Items, probeVirtualRouterFakeIPEntry{
			Domain:     domain,
			FakeIP:     ip,
			RuleID:     strings.TrimSpace(item.RuleID),
			Action:     normalizeProbeVirtualRouterRouteRuleAction(item.Action, item.ExitNodeID),
			ExitNodeID: normalizeProbeNodeID(item.ExitNodeID),
			ExpiresAt:  expiresAt,
			UpdatedAt:  itemUpdatedAt,
		})
		if len(out.Items) >= probeVirtualRouterMaxFakeIPCount {
			break
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		return out.Items[i].Domain < out.Items[j].Domain
	})
	return out
}

func normalizeProbeVirtualRouterFakeIPDomain(raw string) string {
	domain := strings.ToLower(strings.TrimSpace(strings.Trim(raw, ".")))
	if domain == "" || strings.ContainsAny(domain, " \t\r\n:/") {
		return ""
	}
	return domain
}

func pruneProbeVirtualRouterFakeIPLibraryLocked(now time.Time) bool {
	if ProbeRouteConfigStore == nil {
		return false
	}
	library := normalizeProbeVirtualRouterFakeIPLibrary(ProbeRouteConfigStore.data.VirtualRouterFakeIP)
	kept := library.Items[:0]
	changed := false
	for _, item := range library.Items {
		expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(item.ExpiresAt))
		if err == nil && !expiresAt.IsZero() && !now.Before(expiresAt) {
			changed = true
			continue
		}
		kept = append(kept, item)
	}
	library.Items = kept
	if changed {
		bumpProbeVirtualRouterFakeIPLibraryVersion(&library, now)
		ProbeRouteConfigStore.data.VirtualRouterFakeIP = library
		return true
	}
	ProbeRouteConfigStore.data.VirtualRouterFakeIP = library
	return false
}

func bumpProbeVirtualRouterFakeIPLibraryVersion(library *probeVirtualRouterFakeIPLibrary, now time.Time) {
	if library.Version <= 0 {
		library.Version = 1
	} else {
		library.Version++
	}
	library.UpdatedAt = now.UTC().Format(time.RFC3339)
}

func allocateProbeVirtualRouterFakeIPForDomain(domain string, rule probeVirtualRouterRouteRule) (probeVirtualRouterFakeIPEntry, probeVirtualRouterFakeIPLibrary, bool, error) {
	if ProbeRouteConfigStore == nil {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, false, fmt.Errorf("route config store not initialized")
	}
	cleanDomain := normalizeProbeVirtualRouterFakeIPDomain(domain)
	if cleanDomain == "" {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, false, fmt.Errorf("domain is invalid")
	}
	action := normalizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID)
	if action != probeVirtualRouterRouteRuleActionExit {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, false, fmt.Errorf("fake ip requires probe exit action")
	}
	exitNodeID := normalizeProbeNodeID(rule.ExitNodeID)
	if exitNodeID == "" || !isProbeVirtualRouterKnownNodeID(exitNodeID) {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, false, fmt.Errorf("exit node is unavailable")
	}

	now := time.Now().UTC()
	ProbeRouteConfigStore.mu.Lock()
	defer ProbeRouteConfigStore.mu.Unlock()
	changed := pruneProbeVirtualRouterFakeIPLibraryLocked(now)
	currentConfig := ProbeRouteConfigStore.data.VirtualRouter
	config := ensureProbeVirtualRouterProbeIPsForKnownNodes(normalizeProbeVirtualRouterConfig(currentConfig))
	if !reflect.DeepEqual(currentConfig, config) {
		changed = true
	}
	library := normalizeProbeVirtualRouterFakeIPLibrary(ProbeRouteConfigStore.data.VirtualRouterFakeIP)
	expiresAt := now.Add(probeVirtualRouterFakeIPDefaultTTL).Format(time.RFC3339)
	for index := range library.Items {
		if library.Items[index].Domain != cleanDomain {
			continue
		}
		if strings.TrimSpace(library.Items[index].RuleID) == strings.TrimSpace(rule.ID) &&
			normalizeProbeVirtualRouterRouteRuleAction(library.Items[index].Action, library.Items[index].ExitNodeID) == action &&
			normalizeProbeNodeID(library.Items[index].ExitNodeID) == exitNodeID {
			if changed {
				ProbeRouteConfigStore.data.VirtualRouter = config
				ProbeRouteConfigStore.data.VirtualRouterFakeIP = library
			}
			return library.Items[index], library, changed, nil
		}
		library.Items[index].RuleID = strings.TrimSpace(rule.ID)
		library.Items[index].Action = action
		library.Items[index].ExitNodeID = exitNodeID
		library.Items[index].ExpiresAt = expiresAt
		library.Items[index].UpdatedAt = now.Format(time.RFC3339)
		bumpProbeVirtualRouterFakeIPLibraryVersion(&library, now)
		ProbeRouteConfigStore.data.VirtualRouter = config
		ProbeRouteConfigStore.data.VirtualRouterFakeIP = library
		return library.Items[index], library, true, nil
	}

	ip := allocateProbeVirtualRouterFreeDomainFakeIP(config, library)
	if ip == "" {
		return probeVirtualRouterFakeIPEntry{}, library, changed, fmt.Errorf("virtual router fake ip pool exhausted")
	}
	entry := probeVirtualRouterFakeIPEntry{
		Domain:     cleanDomain,
		FakeIP:     ip,
		RuleID:     strings.TrimSpace(rule.ID),
		Action:     action,
		ExitNodeID: exitNodeID,
		ExpiresAt:  expiresAt,
		UpdatedAt:  now.Format(time.RFC3339),
	}
	library.Items = append(library.Items, entry)
	sort.SliceStable(library.Items, func(i, j int) bool {
		return library.Items[i].Domain < library.Items[j].Domain
	})
	bumpProbeVirtualRouterFakeIPLibraryVersion(&library, now)
	ProbeRouteConfigStore.data.VirtualRouter = config
	ProbeRouteConfigStore.data.VirtualRouterFakeIP = library
	return entry, library, true, nil
}

func allocateProbeVirtualRouterFreeDomainFakeIP(config probeVirtualRouterConfig, library probeVirtualRouterFakeIPLibrary) string {
	_, network, err := net.ParseCIDR(strings.TrimSpace(firstNonEmptyProbeVirtualRouter(config.FakeIPCIDR, probeVirtualRouterDefaultCIDR)))
	if err != nil || network == nil || network.IP.To4() == nil {
		return ""
	}
	used := map[string]struct{}{
		probeVirtualRouterReservedGatewayIP: {},
		probeVirtualRouterReservedTUNIP:     {},
	}
	for _, item := range config.ProbeIPs {
		if ip := normalizeProbeVirtualRouterIP(item.IP); ip != "" {
			used[ip] = struct{}{}
		}
	}
	for _, item := range library.Items {
		if ip := normalizeProbeVirtualRouterIP(item.FakeIP); ip != "" {
			used[ip] = struct{}{}
		}
	}
	base := binary.BigEndian.Uint32(network.IP.To4())
	ones, bits := network.Mask.Size()
	if bits != 32 || ones < 0 {
		return ""
	}
	size := uint64(1) << uint(32-ones)
	start := uint64(probeVirtualRouterProbeIPPoolSize + 1)
	for offset := start; offset < size; offset++ {
		raw := make([]byte, 4)
		binary.BigEndian.PutUint32(raw, base+uint32(offset))
		ip := net.IP(raw)
		if !network.Contains(ip) {
			break
		}
		ipText := ip.String()
		if _, exists := used[ipText]; exists {
			continue
		}
		return ipText
	}
	return ""
}

func resetProbeVirtualRouterFakeIPLibrary() (probeVirtualRouterFakeIPLibrary, error) {
	if ProbeRouteConfigStore == nil {
		return probeVirtualRouterFakeIPLibrary{}, fmt.Errorf("route config store not initialized")
	}
	now := time.Now().UTC()
	ProbeRouteConfigStore.mu.Lock()
	library := normalizeProbeVirtualRouterFakeIPLibrary(ProbeRouteConfigStore.data.VirtualRouterFakeIP)
	library.Items = []probeVirtualRouterFakeIPEntry{}
	bumpProbeVirtualRouterFakeIPLibraryVersion(&library, now)
	ProbeRouteConfigStore.data.VirtualRouterFakeIP = library
	ProbeRouteConfigStore.mu.Unlock()
	if err := ProbeRouteConfigStore.Save(); err != nil {
		return library, err
	}
	return library, nil
}

func reconcileProbeVirtualRouterFakeIPLibraryBestEffort() bool {
	if ProbeRouteConfigStore == nil {
		return false
	}
	ProbeRouteConfigStore.mu.Lock()
	now := time.Now().UTC()
	changed := pruneProbeVirtualRouterFakeIPLibraryLocked(now)
	if renewProbeVirtualRouterFakeIPLibraryLocked(now) {
		changed = true
	}
	ProbeRouteConfigStore.mu.Unlock()
	if !changed {
		return false
	}
	if err := ProbeRouteConfigStore.Save(); err != nil {
		logControllerWarnf("failed to persist virtual router fake ip maintenance: %v", err)
		return false
	}
	return true
}

func renewProbeVirtualRouterFakeIPLibraryLocked(now time.Time) bool {
	if ProbeRouteConfigStore == nil {
		return false
	}
	library := normalizeProbeVirtualRouterFakeIPLibrary(ProbeRouteConfigStore.data.VirtualRouterFakeIP)
	if len(library.Items) == 0 {
		ProbeRouteConfigStore.data.VirtualRouterFakeIP = library
		return false
	}
	if updatedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(library.UpdatedAt)); err == nil && !updatedAt.IsZero() {
		elapsed := now.Sub(updatedAt)
		if elapsed >= 0 && elapsed < probeVirtualRouterFakeIPRenewInterval {
			ProbeRouteConfigStore.data.VirtualRouterFakeIP = library
			return false
		}
	}
	nextExpiresAt := now.Add(probeVirtualRouterFakeIPDefaultTTL).Format(time.RFC3339)
	nextUpdatedAt := now.Format(time.RFC3339)
	for index := range library.Items {
		library.Items[index].ExpiresAt = nextExpiresAt
		library.Items[index].UpdatedAt = nextUpdatedAt
	}
	bumpProbeVirtualRouterFakeIPLibraryVersion(&library, now)
	ProbeRouteConfigStore.data.VirtualRouterFakeIP = library
	return true
}

func normalizeProbeVirtualRouterRouteRuleAction(raw string, exitNodeID string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", probeVirtualRouterRouteRuleActionDirect:
		if normalizeProbeNodeID(exitNodeID) != "" && value == "" {
			return probeVirtualRouterRouteRuleActionExit
		}
		return probeVirtualRouterRouteRuleActionDirect
	case probeVirtualRouterRouteRuleActionExit, "exit", "probe":
		return probeVirtualRouterRouteRuleActionExit
	case probeVirtualRouterRouteRuleActionReject, "block", "deny":
		return probeVirtualRouterRouteRuleActionReject
	default:
		return ""
	}
}

func normalizeProbeVirtualRouterRouteRuleEntries(items []string) []string {
	if len(items) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		entry, ok := normalizeProbeVirtualRouterRouteRuleEntry(item)
		if !ok {
			continue
		}
		if _, exists := seen[entry]; exists {
			continue
		}
		seen[entry] = struct{}{}
		out = append(out, entry)
		if len(out) >= probeVirtualRouterMaxRouteRuleEntries {
			break
		}
	}
	sort.Strings(out)
	return out
}

func normalizeProbeVirtualRouterRouteRuleEntry(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	key, value, ok := strings.Cut(trimmed, ":")
	if !ok {
		return "", false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return "", false
	}
	switch key {
	case "domain_suffix", "domain_prefix", "domain_keyword":
		normalized := normalizeProbeVirtualRouterRouteRuleDomainValue(key, value)
		if normalized == "" {
			return "", false
		}
		return key + ":" + normalized, true
	case "cidr":
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return "", false
		}
		return key + ":" + prefix.Masked().String(), true
	default:
		return "", false
	}
}

func normalizeProbeVirtualRouterRouteRuleDomainValue(kind string, raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if kind == "domain_suffix" {
		value = strings.TrimLeft(value, ".")
	}
	if value == "" || strings.ContainsAny(value, " \t\r\n") || strings.Contains(value, ":") {
		return ""
	}
	return value
}

func collectProbeVirtualRouterReservedRouteRuleIDs(items []probeVirtualRouterRouteRule) map[string]struct{} {
	reserved := map[string]struct{}{}
	for _, item := range items {
		ruleID := strings.TrimSpace(item.ID)
		if ruleID != "" {
			reserved[ruleID] = struct{}{}
		}
	}
	return reserved
}

func allocateProbeVirtualRouterRouteRuleID(seen map[string]struct{}, reserved map[string]struct{}, nextSeq int) (string, int) {
	if nextSeq <= 0 {
		nextSeq = 1
	}
	for {
		ruleID := fmt.Sprintf("rr-%d", nextSeq)
		nextSeq++
		if _, exists := seen[ruleID]; !exists {
			if _, exists := reserved[ruleID]; !exists {
				return ruleID, nextSeq
			}
		}
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

func ensureProbeVirtualRouterAuthFields(config probeVirtualRouterConfig) probeVirtualRouterConfig {
	user, publicKey, userErr := resolveProbeVirtualRouterUserIdentityAndPublicKey("")
	for index := range config.TopologyRules {
		rule := &config.TopologyRules[index]
		if strings.TrimSpace(rule.Secret) == "" {
			rule.Secret = randomProbeNodeSecret(probeVirtualRouterDefaultSecretLen)
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
		ticket, ticketErr := buildProbeVirtualRouterAuthTicket(*rule, priv)
		if ticketErr != nil {
			continue
		}
		rule.AuthTicket = ticket
	}
	return config
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
	}, "|")))
	return probeVirtualRouterRuntimeChainPrefix + hex.EncodeToString(sum[:])[:24]
}

func normalizeProbeVirtualRouterDirection(raw string) string {
	return probeVirtualRouterDirectionForward
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

func probeVirtualRouterProbeIPPool(cidr string) []string {
	_, network, err := net.ParseCIDR(strings.TrimSpace(firstNonEmptyProbeVirtualRouter(cidr, probeVirtualRouterDefaultCIDR)))
	if err != nil || network == nil || network.IP.To4() == nil {
		return []string{}
	}
	base := binary.BigEndian.Uint32(network.IP.To4())
	out := make([]string, 0, probeVirtualRouterProbeIPPoolSize)
	for offset := uint32(1); offset <= uint32(probeVirtualRouterProbeIPPoolSize); offset++ {
		candidate := base + offset
		raw := make([]byte, 4)
		binary.BigEndian.PutUint32(raw, candidate)
		ipValue := net.IP(raw)
		if !network.Contains(ipValue) {
			break
		}
		ip := ipValue.String()
		if ip == probeVirtualRouterReservedGatewayIP || ip == probeVirtualRouterReservedTUNIP {
			continue
		}
		out = append(out, ip)
	}
	return out
}

func allocateProbeVirtualRouterFreeProbeIP(cidr string, used map[string]struct{}) string {
	for _, ip := range probeVirtualRouterProbeIPPool(cidr) {
		if _, exists := used[ip]; !exists {
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
	existingNode := map[string]struct{}{}
	usedIP := map[string]struct{}{}
	probeIPs := make([]probeVirtualRouterProbeIP, 0, len(config.ProbeIPs)+len(known))
	for _, item := range config.ProbeIPs {
		nodeID := normalizeProbeNodeID(item.NodeID)
		ip := normalizeProbeVirtualRouterIP(item.IP)
		if nodeID == "" || ip == "" {
			continue
		}
		if isDeletedProbeNodeID(nodeID) {
			continue
		}
		if !isProbeVirtualRouterProbeIPInPool(config.FakeIPCIDR, ip) {
			continue
		}
		if _, ok := existingNode[nodeID]; ok {
			continue
		}
		if _, ok := usedIP[ip]; ok {
			continue
		}
		existingNode[nodeID] = struct{}{}
		usedIP[ip] = struct{}{}
		probeIPs = append(probeIPs, probeVirtualRouterProbeIP{
			NodeID: nodeID,
			IP:     ip,
			Note:   strings.TrimSpace(item.Note),
		})
	}
	for _, nodeID := range known {
		if nodeID == "" {
			continue
		}
		if _, ok := existingNode[nodeID]; ok {
			continue
		}
		ip := allocateProbeVirtualRouterFreeProbeIP(config.FakeIPCIDR, usedIP)
		if ip == "" {
			continue
		}
		existingNode[nodeID] = struct{}{}
		usedIP[ip] = struct{}{}
		probeIPs = append(probeIPs, probeVirtualRouterProbeIP{NodeID: nodeID, IP: ip})
	}
	config.ProbeIPs = normalizeProbeVirtualRouterProbeIPs(config.FakeIPCIDR, probeIPs)
	return config
}

func reconcileProbeVirtualRouterStoredProbeIPsBestEffort() {
	if ProbeRouteConfigStore == nil {
		return
	}
	changed := false
	ProbeRouteConfigStore.mu.Lock()
	current := ProbeRouteConfigStore.data.VirtualRouter
	next := ensureProbeVirtualRouterProbeIPsForKnownNodes(normalizeProbeVirtualRouterConfig(current))
	if !reflect.DeepEqual(current, next) {
		ProbeRouteConfigStore.data.VirtualRouter = next
		changed = true
	}
	ProbeRouteConfigStore.mu.Unlock()
	if !changed || strings.TrimSpace(ProbeRouteConfigStore.path) == "" {
		return
	}
	if err := ProbeRouteConfigStore.Save(); err != nil {
		logControllerWarnf("failed to persist virtual router probe ip reconciliation: %v", err)
	}
}

func isProbeVirtualRouterKnownNodeID(nodeID string) bool {
	target := normalizeProbeNodeID(nodeID)
	if target == "" {
		return false
	}
	for _, item := range listProbeVirtualRouterKnownNodeIDs() {
		if normalizeProbeNodeID(item) == target {
			return true
		}
	}
	return false
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
		if isDeletedProbeNodeID(rt.NodeID) {
			continue
		}
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
