package core

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxProbeLinkChainCount          = 500
	maxProbeLinkChainNameLen        = 128
	maxProbeLinkChainUserIDLen      = 128
	maxProbeLinkChainPublicKeyLen   = 8192
	maxProbeLinkChainSecretLen      = 256
	maxProbeLinkChainHopCount       = 64
	defaultProbeLinkChainListenHost = "0.0.0.0"
	defaultProbeLinkChainLinkLayer  = "http"
	defaultProbeLinkChainSecretLen  = 48
)

type probeLinkChainHopConfig struct {
	NodeNo     int    `json:"node_no"`
	ListenPort int    `json:"listen_port"`
	LinkLayer  string `json:"link_layer"`
}

type probeLinkChainRecord struct {
	ChainID        string                    `json:"chain_id"`
	Name           string                    `json:"name"`
	UserID         string                    `json:"user_id"`
	UserPublicKey  string                    `json:"user_public_key"`
	Secret         string                    `json:"secret"`
	EntryNodeID    string                    `json:"entry_node_id"`
	ExitNodeID     string                    `json:"exit_node_id"`
	CascadeNodeIDs []string                  `json:"cascade_node_ids"`
	ListenHost     string                    `json:"listen_host"`
	ListenPort     int                       `json:"listen_port"`
	LinkLayer      string                    `json:"link_layer"`
	HopConfigs     []probeLinkChainHopConfig `json:"hop_configs"`
	EgressHost     string                    `json:"egress_host"`
	EgressPort     int                       `json:"egress_port"`
	CreatedAt      string                    `json:"created_at"`
	UpdatedAt      string                    `json:"updated_at"`
}

func loadProbeLinkChainsLocked() []probeLinkChainRecord {
	if ProbeLinkChainStore == nil {
		return []probeLinkChainRecord{}
	}
	raw := ProbeLinkChainStore.data.Chains
	if len(raw) == 0 {
		return []probeLinkChainRecord{}
	}
	out := make([]probeLinkChainRecord, 0, len(raw))
	out = append(out, raw...)
	return normalizeProbeLinkChains(out)
}

func findProbeLinkChainByIDLocked(chainID string) (probeLinkChainRecord, bool) {
	target := strings.TrimSpace(chainID)
	if target == "" {
		return probeLinkChainRecord{}, false
	}
	for _, item := range loadProbeLinkChainsLocked() {
		if strings.TrimSpace(item.ChainID) == target {
			return item, true
		}
	}
	return probeLinkChainRecord{}, false
}

func upsertProbeLinkChainLocked(input probeLinkChainRecord) (probeLinkChainRecord, []probeLinkChainRecord, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return probeLinkChainRecord{}, nil, fmt.Errorf("chain name is required")
	}
	if len([]rune(name)) > maxProbeLinkChainNameLen {
		return probeLinkChainRecord{}, nil, fmt.Errorf("chain name must be <= %d characters", maxProbeLinkChainNameLen)
	}

	userID := strings.TrimSpace(input.UserID)
	if userID == "" {
		return probeLinkChainRecord{}, nil, fmt.Errorf("user_id is required")
	}
	if len([]rune(userID)) > maxProbeLinkChainUserIDLen {
		return probeLinkChainRecord{}, nil, fmt.Errorf("user_id must be <= %d characters", maxProbeLinkChainUserIDLen)
	}

	userPublicKey := strings.TrimSpace(input.UserPublicKey)
	if userPublicKey == "" {
		return probeLinkChainRecord{}, nil, fmt.Errorf("user_public_key is required")
	}
	if len(userPublicKey) > maxProbeLinkChainPublicKeyLen {
		return probeLinkChainRecord{}, nil, fmt.Errorf("user_public_key must be <= %d bytes", maxProbeLinkChainPublicKeyLen)
	}

	listenPort := input.ListenPort
	if listenPort <= 0 || listenPort > 65535 {
		return probeLinkChainRecord{}, nil, fmt.Errorf("listen_port must be between 1 and 65535")
	}
	linkLayer, ok := parseProbeLinkChainLinkLayer(input.LinkLayer)
	if !ok {
		return probeLinkChainRecord{}, nil, fmt.Errorf("link_layer must be http/http2/http3")
	}
	if strings.TrimSpace(linkLayer) == "" {
		linkLayer = defaultProbeLinkChainLinkLayer
	}
	egressHost := strings.TrimSpace(input.EgressHost)
	if egressHost == "" {
		return probeLinkChainRecord{}, nil, fmt.Errorf("egress_host is required")
	}
	egressPort := input.EgressPort
	if egressPort <= 0 || egressPort > 65535 {
		return probeLinkChainRecord{}, nil, fmt.Errorf("egress_port must be between 1 and 65535")
	}

	entryNodeID := normalizeProbeNodeID(input.EntryNodeID)
	exitNodeID := normalizeProbeNodeID(input.ExitNodeID)
	if exitNodeID == "" {
		return probeLinkChainRecord{}, nil, fmt.Errorf("exit_node_id is required")
	}
	cascades := normalizeProbeNodeIDList(input.CascadeNodeIDs)
	cascades = removeNodeIDs(cascades, entryNodeID, exitNodeID)
	routeNodes := buildProbeChainRouteNodes(probeLinkChainRecord{
		EntryNodeID:    entryNodeID,
		ExitNodeID:     exitNodeID,
		CascadeNodeIDs: cascades,
	})
	hopConfigs, hopErr := normalizeProbeLinkChainHopConfigsForUpsert(input.HopConfigs, routeNodes)
	if hopErr != nil {
		return probeLinkChainRecord{}, nil, hopErr
	}

	secret := strings.TrimSpace(input.Secret)
	if secret == "" {
		secret = randomProbeNodeSecret(defaultProbeLinkChainSecretLen)
	}
	if len(secret) > maxProbeLinkChainSecretLen {
		return probeLinkChainRecord{}, nil, fmt.Errorf("secret must be <= %d bytes", maxProbeLinkChainSecretLen)
	}

	chainID := strings.TrimSpace(input.ChainID)
	items := loadProbeLinkChainsLocked()
	now := time.Now().UTC().Format(time.RFC3339)
	found := -1
	for i := range items {
		if chainID != "" && strings.TrimSpace(items[i].ChainID) == chainID {
			found = i
			break
		}
	}

	if found < 0 && chainID == "" {
		chainID = "chain-" + randomProbeNodeSecret(16)
	}
	if found < 0 && chainID != "" {
		for i := range items {
			if strings.TrimSpace(items[i].ChainID) == chainID {
				found = i
				break
			}
		}
	}

	record := probeLinkChainRecord{
		ChainID:        chainID,
		Name:           name,
		UserID:         userID,
		UserPublicKey:  userPublicKey,
		Secret:         secret,
		EntryNodeID:    entryNodeID,
		ExitNodeID:     exitNodeID,
		CascadeNodeIDs: cascades,
		ListenHost:     normalizeProbeLinkChainListenHost(input.ListenHost),
		ListenPort:     listenPort,
		LinkLayer:      linkLayer,
		HopConfigs:     hopConfigs,
		EgressHost:     egressHost,
		EgressPort:     egressPort,
		UpdatedAt:      now,
	}
	if found >= 0 {
		record.CreatedAt = strings.TrimSpace(items[found].CreatedAt)
		if record.CreatedAt == "" {
			record.CreatedAt = now
		}
		items[found] = record
	} else {
		if len(items) >= maxProbeLinkChainCount {
			return probeLinkChainRecord{}, nil, fmt.Errorf("chain count exceeded limit (%d)", maxProbeLinkChainCount)
		}
		record.CreatedAt = now
		items = append(items, record)
	}

	normalized := normalizeProbeLinkChains(items)
	ProbeLinkChainStore.data.Chains = normalized

	for _, item := range normalized {
		if strings.TrimSpace(item.ChainID) == chainID {
			return item, normalized, nil
		}
	}
	return probeLinkChainRecord{}, normalized, fmt.Errorf("failed to load saved chain: %s", chainID)
}

func removeProbeLinkChainLocked(chainID string) (probeLinkChainRecord, []probeLinkChainRecord, error) {
	target := strings.TrimSpace(chainID)
	if target == "" {
		return probeLinkChainRecord{}, nil, fmt.Errorf("chain_id is required")
	}
	items := loadProbeLinkChainsLocked()
	next := make([]probeLinkChainRecord, 0, len(items))
	removed := probeLinkChainRecord{}
	found := false
	for _, item := range items {
		if strings.TrimSpace(item.ChainID) == target {
			removed = item
			found = true
			continue
		}
		next = append(next, item)
	}
	if !found {
		return probeLinkChainRecord{}, nil, fmt.Errorf("chain not found")
	}
	normalized := normalizeProbeLinkChains(next)
	ProbeLinkChainStore.data.Chains = normalized
	return removed, normalized, nil
}

func normalizeProbeLinkChains(items []probeLinkChainRecord) []probeLinkChainRecord {
	if len(items) == 0 {
		return []probeLinkChainRecord{}
	}
	out := make([]probeLinkChainRecord, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		chainID := strings.TrimSpace(item.ChainID)
		if chainID == "" {
			continue
		}
		if _, ok := seen[chainID]; ok {
			continue
		}
		seen[chainID] = struct{}{}
		name := strings.TrimSpace(item.Name)
		userID := strings.TrimSpace(item.UserID)
		pubKey := strings.TrimSpace(item.UserPublicKey)
		secret := strings.TrimSpace(item.Secret)
		entryNodeID := normalizeProbeNodeID(item.EntryNodeID)
		exitNodeID := normalizeProbeNodeID(item.ExitNodeID)
		listenPort := item.ListenPort
		linkLayer := normalizeProbeLinkChainLinkLayer(item.LinkLayer)
		egressHost := strings.TrimSpace(item.EgressHost)
		egressPort := item.EgressPort
		if name == "" || userID == "" || pubKey == "" || secret == "" || exitNodeID == "" || listenPort <= 0 || listenPort > 65535 || egressHost == "" || egressPort <= 0 || egressPort > 65535 {
			continue
		}
		if len([]rune(name)) > maxProbeLinkChainNameLen || len([]rune(userID)) > maxProbeLinkChainUserIDLen || len(pubKey) > maxProbeLinkChainPublicKeyLen || len(secret) > maxProbeLinkChainSecretLen {
			continue
		}
		cascades := normalizeProbeNodeIDList(item.CascadeNodeIDs)
		cascades = removeNodeIDs(cascades, entryNodeID, exitNodeID)
		routeNodes := buildProbeChainRouteNodes(probeLinkChainRecord{
			EntryNodeID:    entryNodeID,
			ExitNodeID:     exitNodeID,
			CascadeNodeIDs: cascades,
		})
		out = append(out, probeLinkChainRecord{
			ChainID:        chainID,
			Name:           name,
			UserID:         userID,
			UserPublicKey:  pubKey,
			Secret:         secret,
			EntryNodeID:    entryNodeID,
			ExitNodeID:     exitNodeID,
			CascadeNodeIDs: cascades,
			ListenHost:     normalizeProbeLinkChainListenHost(item.ListenHost),
			ListenPort:     listenPort,
			LinkLayer:      linkLayer,
			HopConfigs:     normalizeProbeLinkChainHopConfigsForStore(item.HopConfigs, routeNodes),
			EgressHost:     egressHost,
			EgressPort:     egressPort,
			CreatedAt:      strings.TrimSpace(item.CreatedAt),
			UpdatedAt:      strings.TrimSpace(item.UpdatedAt),
		})
		if len(out) >= maxProbeLinkChainCount {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		left := strings.TrimSpace(out[i].UpdatedAt)
		right := strings.TrimSpace(out[j].UpdatedAt)
		if left == right {
			return out[i].ChainID < out[j].ChainID
		}
		return left > right
	})
	return out
}

func normalizeProbeNodeIDList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		nodeID := normalizeProbeNodeID(raw)
		if nodeID == "" {
			continue
		}
		if _, ok := seen[nodeID]; ok {
			continue
		}
		seen[nodeID] = struct{}{}
		out = append(out, nodeID)
	}
	return out
}

func removeNodeIDs(values []string, excludes ...string) []string {
	if len(values) == 0 {
		return []string{}
	}
	excludeSet := make(map[string]struct{}, len(excludes))
	for _, item := range excludes {
		key := normalizeProbeNodeID(item)
		if key == "" {
			continue
		}
		excludeSet[key] = struct{}{}
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		key := normalizeProbeNodeID(value)
		if key == "" {
			continue
		}
		if _, ok := excludeSet[key]; ok {
			continue
		}
		out = append(out, key)
	}
	return out
}

func normalizeProbeLinkChainListenHost(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return defaultProbeLinkChainListenHost
	}
	return value
}

func normalizeProbeLinkChainLinkLayer(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "http":
		return "http"
	case "http2", "h2":
		return "http2"
	case "http3", "h3":
		return "http3"
	default:
		return defaultProbeLinkChainLinkLayer
	}
}

func parseProbeLinkChainLinkLayer(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", true
	}
	switch strings.ToLower(trimmed) {
	case "http":
		return "http", true
	case "http2", "h2":
		return "http2", true
	case "http3", "h3":
		return "http3", true
	default:
		return "", false
	}
}

func normalizeProbeLinkChainHopConfigsForUpsert(values []probeLinkChainHopConfig, routeNodeIDs []string) ([]probeLinkChainHopConfig, error) {
	filter := make(map[string]struct{}, len(routeNodeIDs))
	for _, nodeID := range normalizeProbeNodeIDList(routeNodeIDs) {
		filter[nodeID] = struct{}{}
	}
	out := make([]probeLinkChainHopConfig, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, item := range values {
		if item.NodeNo <= 0 {
			continue
		}
		nodeID := normalizeProbeNodeID(strconv.Itoa(item.NodeNo))
		if nodeID == "" {
			continue
		}
		if len(filter) > 0 {
			if _, ok := filter[nodeID]; !ok {
				continue
			}
		}
		if _, ok := seen[nodeID]; ok {
			continue
		}
		seen[nodeID] = struct{}{}

		listenPort := item.ListenPort
		if listenPort < 0 || listenPort > 65535 {
			return nil, fmt.Errorf("hop listen_port must be between 1 and 65535")
		}
		linkLayer, ok := parseProbeLinkChainLinkLayer(item.LinkLayer)
		if !ok {
			return nil, fmt.Errorf("hop link_layer must be http/http2/http3")
		}
		nodeNo, _ := strconv.Atoi(nodeID)
		out = append(out, probeLinkChainHopConfig{
			NodeNo:     nodeNo,
			ListenPort: listenPort,
			LinkLayer:  linkLayer,
		})
		if len(out) >= maxProbeLinkChainHopCount {
			break
		}
	}
	return out, nil
}

func normalizeProbeLinkChainHopConfigsForStore(values []probeLinkChainHopConfig, routeNodeIDs []string) []probeLinkChainHopConfig {
	filter := make(map[string]struct{}, len(routeNodeIDs))
	for _, nodeID := range normalizeProbeNodeIDList(routeNodeIDs) {
		filter[nodeID] = struct{}{}
	}
	out := make([]probeLinkChainHopConfig, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, item := range values {
		if item.NodeNo <= 0 {
			continue
		}
		nodeID := normalizeProbeNodeID(strconv.Itoa(item.NodeNo))
		if nodeID == "" {
			continue
		}
		if len(filter) > 0 {
			if _, ok := filter[nodeID]; !ok {
				continue
			}
		}
		if _, ok := seen[nodeID]; ok {
			continue
		}
		listenPort := item.ListenPort
		if listenPort < 0 || listenPort > 65535 {
			continue
		}
		linkLayer := ""
		if normalized, ok := parseProbeLinkChainLinkLayer(item.LinkLayer); ok {
			linkLayer = normalized
		}
		seen[nodeID] = struct{}{}
		nodeNo, _ := strconv.Atoi(nodeID)
		out = append(out, probeLinkChainHopConfig{
			NodeNo:     nodeNo,
			ListenPort: listenPort,
			LinkLayer:  linkLayer,
		})
		if len(out) >= maxProbeLinkChainHopCount {
			break
		}
	}
	return out
}

func buildProbeChainRouteNodes(item probeLinkChainRecord) []string {
	route := make([]string, 0, 2+len(item.CascadeNodeIDs))
	entry := normalizeProbeNodeID(item.EntryNodeID)
	exitNode := normalizeProbeNodeID(item.ExitNodeID)
	if entry != "" {
		route = append(route, entry)
	}
	for _, cascade := range normalizeProbeNodeIDList(item.CascadeNodeIDs) {
		if cascade == entry || cascade == exitNode {
			continue
		}
		route = append(route, cascade)
	}
	if exitNode != "" {
		if len(route) == 0 || route[len(route)-1] != exitNode {
			route = append(route, exitNode)
		}
	}
	return route
}
