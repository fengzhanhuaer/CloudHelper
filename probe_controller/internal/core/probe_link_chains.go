package core

import (
	"fmt"
	"net/http"
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
	maxProbeLinkChainPortForwardCnt = 256
	defaultProbeLinkChainListenHost = "0.0.0.0"
	defaultProbeLinkChainLinkLayer  = "http"
	defaultProbeLinkChainDialMode   = "forward"
	defaultProbeLinkChainPFNetwork  = "tcp"
	defaultProbeLinkChainPFEntrySide = "chain_entry"
	defaultProbeLinkChainPFHost     = "0.0.0.0"
	defaultProbeLinkChainSecretLen  = 48
)

type probeLinkChainHopConfig struct {
	NodeNo       int    `json:"node_no"`
	ListenHost   string `json:"listen_host,omitempty"`
	ListenPort   int    `json:"listen_port,omitempty"`   // internal port the relay service listens on
	ExternalPort int    `json:"external_port,omitempty"` // public-facing port used for connections
	LinkLayer    string `json:"link_layer"`
	DialMode     string `json:"dial_mode,omitempty"`
	// RelayHost is dynamically filled from the Cloudflare DDNS store (business record).
	// It is NOT persisted; omitempty ensures it does not appear in stored JSON.
	RelayHost string `json:"relay_host,omitempty"`
}

type probeLinkChainPortForwardConfig struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	EntrySide  string `json:"entry_side,omitempty"`
	ListenHost string `json:"listen_host,omitempty"`
	ListenPort int    `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Network    string `json:"network,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type probeLinkChainRecord struct {
	ChainID        string                            `json:"chain_id"`
	Name           string                            `json:"name"`
	UserID         string                            `json:"user_id"`
	UserPublicKey  string                            `json:"user_public_key"`
	Secret         string                            `json:"secret"`
	EntryNodeID    string                            `json:"entry_node_id"`
	ExitNodeID     string                            `json:"exit_node_id"`
	CascadeNodeIDs []string                          `json:"cascade_node_ids"`
	ListenHost     string                            `json:"listen_host"`
	ListenPort     int                               `json:"listen_port"`
	LinkLayer      string                            `json:"link_layer"`
	HopConfigs     []probeLinkChainHopConfig         `json:"hop_configs"`
	PortForwards   []probeLinkChainPortForwardConfig `json:"port_forwards,omitempty"`
	EgressHost     string                            `json:"egress_host"`
	EgressPort     int                               `json:"egress_port"`
	CreatedAt      string                            `json:"created_at"`
	UpdatedAt      string                            `json:"updated_at"`
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

func parseProbeLinkChainNumericID(raw string) (int64, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func maxProbeLinkChainNumericID(items []probeLinkChainRecord) int64 {
	var maxID int64
	for _, item := range items {
		if id, ok := parseProbeLinkChainNumericID(item.ChainID); ok && id > maxID {
			maxID = id
		}
	}
	return maxID
}

func normalizeProbeLinkChainNextID(storedNextID int64, items []probeLinkChainRecord) int64 {
	maxID := maxProbeLinkChainNumericID(items)
	nextID := storedNextID
	if nextID <= 0 {
		nextID = 1
	}
	if nextID <= maxID {
		nextID = maxID + 1
	}
	return nextID
}

func allocateNextProbeLinkChainIDLocked(items []probeLinkChainRecord) string {
	nextID := int64(1)
	if ProbeLinkChainStore != nil {
		ProbeLinkChainStore.data.NextChainID = normalizeProbeLinkChainNextID(
			ProbeLinkChainStore.data.NextChainID,
			items,
		)
		nextID = ProbeLinkChainStore.data.NextChainID
	}
	if nextID <= 0 {
		nextID = normalizeProbeLinkChainNextID(1, items)
	}

	used := make(map[string]struct{}, len(items))
	for _, item := range items {
		key := strings.TrimSpace(item.ChainID)
		if key == "" {
			continue
		}
		used[key] = struct{}{}
	}

	for {
		candidate := strconv.FormatInt(nextID, 10)
		nextID++
		if _, exists := used[candidate]; exists {
			continue
		}
		if ProbeLinkChainStore != nil {
			ProbeLinkChainStore.data.NextChainID = nextID
		}
		return candidate
	}
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
	if listenPort < 0 || listenPort > 65535 {
		return probeLinkChainRecord{}, nil, fmt.Errorf("listen_port must be between 1 and 65535")
	}
	linkLayer, ok := parseProbeLinkChainLinkLayer(input.LinkLayer)
	if !ok {
		return probeLinkChainRecord{}, nil, fmt.Errorf("link_layer must be http/http2/http3")
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
	portForwards, forwardErr := normalizeProbeLinkChainPortForwardsForUpsert(input.PortForwards)
	if forwardErr != nil {
		return probeLinkChainRecord{}, nil, forwardErr
	}
	if listenPort <= 0 && len(hopConfigs) > 0 {
		listenPort = hopConfigs[0].ExternalPort
	}
	if listenPort <= 0 && len(hopConfigs) > 0 {
		listenPort = hopConfigs[0].ListenPort
	}
	if listenPort <= 0 || listenPort > 65535 {
		return probeLinkChainRecord{}, nil, fmt.Errorf("listen_port must be between 1 and 65535")
	}
	if strings.TrimSpace(linkLayer) == "" && len(hopConfigs) > 0 {
		linkLayer = normalizeProbeLinkChainLinkLayer(hopConfigs[0].LinkLayer)
	}
	if strings.TrimSpace(linkLayer) == "" {
		linkLayer = defaultProbeLinkChainLinkLayer
	}
	listenHost := normalizeProbeLinkChainListenHost(input.ListenHost)
	if strings.TrimSpace(input.ListenHost) == "" && len(hopConfigs) > 0 {
		listenHost = normalizeProbeLinkChainListenHost(hopConfigs[0].ListenHost)
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
		chainID = allocateNextProbeLinkChainIDLocked(items)
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
		ListenHost:     listenHost,
		ListenPort:     listenPort,
		LinkLayer:      linkLayer,
		HopConfigs:     hopConfigs,
		PortForwards:   portForwards,
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
			PortForwards:   normalizeProbeLinkChainPortForwardsForStore(item.PortForwards),
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

func normalizeProbeLinkChainDialMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "reverse", "rev":
		return "reverse"
	default:
		return defaultProbeLinkChainDialMode
	}
}

func parseProbeLinkChainDialMode(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", true
	}
	switch strings.ToLower(trimmed) {
	case "forward", "fwd":
		return "forward", true
	case "reverse", "rev":
		return "reverse", true
	default:
		return "", false
	}
}

func normalizeProbeLinkChainPFNetwork(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "udp":
		return "udp"
	case "both", "tcp+udp", "udp+tcp":
		return "both"
	default:
		return defaultProbeLinkChainPFNetwork
	}
}

func parseProbeLinkChainPFNetwork(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", true
	}
	switch strings.ToLower(trimmed) {
	case "tcp":
		return "tcp", true
	case "udp":
		return "udp", true
	case "both", "tcp+udp", "udp+tcp":
		return "both", true
	default:
		return "", false
	}
}

func normalizeProbeLinkChainPFEntrySide(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "chain_exit", "exit", "egress":
		return "chain_exit"
	default:
		return defaultProbeLinkChainPFEntrySide
	}
}

func parseProbeLinkChainPFEntrySide(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", true
	}
	switch strings.ToLower(trimmed) {
	case "chain_entry", "entry", "ingress":
		return "chain_entry", true
	case "chain_exit", "exit", "egress":
		return "chain_exit", true
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

		listenHost := normalizeProbeLinkChainListenHost(item.ListenHost)
		listenPort, listenPortErr := normalizeOptionalProbeLinkChainPort(item.ListenPort)
		if listenPortErr != nil {
			return nil, fmt.Errorf("hop listen_port must be between 1 and 65535")
		}
		externalPort, externalPortErr := normalizeOptionalProbeLinkChainPort(item.ExternalPort)
		if externalPortErr != nil {
			return nil, fmt.Errorf("hop external_port must be between 1 and 65535")
		}
		linkLayer, ok := parseProbeLinkChainLinkLayer(item.LinkLayer)
		if !ok {
			return nil, fmt.Errorf("hop link_layer must be http/http2/http3")
		}
		dialMode, dialModeOK := parseProbeLinkChainDialMode(item.DialMode)
		if !dialModeOK {
			return nil, fmt.Errorf("hop dial_mode must be forward/reverse")
		}
		if listenPort <= 0 {
			return nil, fmt.Errorf("hop listen_port must be between 1 and 65535")
		}
		// If external_port is not configured, default to listen_port.
		if externalPort <= 0 {
			externalPort = listenPort
		}
		if strings.TrimSpace(linkLayer) == "" {
			linkLayer = defaultProbeLinkChainLinkLayer
		}
		if strings.TrimSpace(dialMode) == "" {
			dialMode = defaultProbeLinkChainDialMode
		}
		seen[nodeID] = struct{}{}
		nodeNo, _ := strconv.Atoi(nodeID)
		out = append(out, probeLinkChainHopConfig{
			NodeNo:       nodeNo,
			ListenHost:   listenHost,
			ListenPort:   listenPort,
			ExternalPort: externalPort,
			LinkLayer:    linkLayer,
			DialMode:     dialMode,
		})
		if len(out) >= maxProbeLinkChainHopCount {
			break
		}
	}
	for nodeID := range filter {
		if _, ok := seen[nodeID]; !ok {
			return nil, fmt.Errorf("hop config is required for node %s", nodeID)
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
		listenPort, listenPortErr := normalizeOptionalProbeLinkChainPort(item.ListenPort)
		if listenPortErr != nil {
			continue
		}
		externalPort, externalPortErr := normalizeOptionalProbeLinkChainPort(item.ExternalPort)
		if externalPortErr != nil {
			continue
		}
		linkLayer := ""
		if normalized, ok := parseProbeLinkChainLinkLayer(item.LinkLayer); ok {
			linkLayer = normalized
		}
		dialMode := ""
		if normalized, ok := parseProbeLinkChainDialMode(item.DialMode); ok {
			dialMode = normalized
		}
		if listenPort <= 0 && externalPort <= 0 && strings.TrimSpace(linkLayer) == "" && strings.TrimSpace(dialMode) == "" {
			continue
		}
		// If external_port is not configured, default to listen_port.
		if externalPort <= 0 {
			externalPort = listenPort
		}
		if strings.TrimSpace(dialMode) == "" {
			dialMode = defaultProbeLinkChainDialMode
		}
		seen[nodeID] = struct{}{}
		nodeNo, _ := strconv.Atoi(nodeID)
		out = append(out, probeLinkChainHopConfig{
			NodeNo:       nodeNo,
			ListenHost:   normalizeProbeLinkChainListenHost(item.ListenHost),
			ListenPort:   listenPort,
			ExternalPort: externalPort,
			LinkLayer:    linkLayer,
			DialMode:     dialMode,
		})
		if len(out) >= maxProbeLinkChainHopCount {
			break
		}
	}
	return out
}

func normalizeOptionalProbeLinkChainPort(raw int) (int, error) {
	if raw < 0 || raw > 65535 {
		return 0, fmt.Errorf("port must be between 1 and 65535")
	}
	if raw == 0 {
		return 0, nil
	}
	return raw, nil
}

func normalizeProbeLinkChainPortForwardID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = "pf-" + strings.ToLower(strings.TrimSpace(randomProbeNodeSecret(8)))
	}
	if len(value) > 96 {
		value = value[:96]
	}
	return value
}

func normalizeProbeLinkChainPortForwardsForUpsert(values []probeLinkChainPortForwardConfig) ([]probeLinkChainPortForwardConfig, error) {
	if len(values) == 0 {
		return []probeLinkChainPortForwardConfig{}, nil
	}
	out := make([]probeLinkChainPortForwardConfig, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, item := range values {
		listenPort, listenErr := normalizeOptionalProbeLinkChainPort(item.ListenPort)
		if listenErr != nil || listenPort <= 0 {
			return nil, fmt.Errorf("port_forwards listen_port must be between 1 and 65535")
		}
		targetPort, targetErr := normalizeOptionalProbeLinkChainPort(item.TargetPort)
		if targetErr != nil || targetPort <= 0 {
			return nil, fmt.Errorf("port_forwards target_port must be between 1 and 65535")
		}
		if strings.TrimSpace(item.TargetHost) == "" {
			return nil, fmt.Errorf("port_forwards target_host is required")
		}
		network, ok := parseProbeLinkChainPFNetwork(item.Network)
		if !ok {
			return nil, fmt.Errorf("port_forwards network must be tcp/udp/both")
		}
		if strings.TrimSpace(network) == "" {
			network = defaultProbeLinkChainPFNetwork
		}
		entrySide, entrySideOK := parseProbeLinkChainPFEntrySide(item.EntrySide)
		if !entrySideOK {
			return nil, fmt.Errorf("port_forwards entry_side must be chain_entry/chain_exit")
		}
		if strings.TrimSpace(entrySide) == "" {
			entrySide = defaultProbeLinkChainPFEntrySide
		}
		id := normalizeProbeLinkChainPortForwardID(item.ID)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		listenHost := strings.TrimSpace(item.ListenHost)
		if listenHost == "" {
			listenHost = defaultProbeLinkChainPFHost
		}
		out = append(out, probeLinkChainPortForwardConfig{
			ID:         id,
			Name:       strings.TrimSpace(item.Name),
			EntrySide:  entrySide,
			ListenHost: normalizeProbeLinkChainListenHost(listenHost),
			ListenPort: listenPort,
			TargetHost: strings.TrimSpace(item.TargetHost),
			TargetPort: targetPort,
			Network:    network,
			Enabled:    item.Enabled,
		})
		if len(out) >= maxProbeLinkChainPortForwardCnt {
			break
		}
	}
	return out, nil
}

func normalizeProbeLinkChainPortForwardsForStore(values []probeLinkChainPortForwardConfig) []probeLinkChainPortForwardConfig {
	if len(values) == 0 {
		return []probeLinkChainPortForwardConfig{}
	}
	out := make([]probeLinkChainPortForwardConfig, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, item := range values {
		listenPort, listenErr := normalizeOptionalProbeLinkChainPort(item.ListenPort)
		targetPort, targetErr := normalizeOptionalProbeLinkChainPort(item.TargetPort)
		if listenErr != nil || targetErr != nil || listenPort <= 0 || targetPort <= 0 {
			continue
		}
		targetHost := strings.TrimSpace(item.TargetHost)
		if targetHost == "" {
			continue
		}
		network := defaultProbeLinkChainPFNetwork
		if normalized, ok := parseProbeLinkChainPFNetwork(item.Network); ok && strings.TrimSpace(normalized) != "" {
			network = normalized
		}
		entrySide := defaultProbeLinkChainPFEntrySide
		if normalized, ok := parseProbeLinkChainPFEntrySide(item.EntrySide); ok && strings.TrimSpace(normalized) != "" {
			entrySide = normalized
		}
		id := normalizeProbeLinkChainPortForwardID(item.ID)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		listenHost := strings.TrimSpace(item.ListenHost)
		if listenHost == "" {
			listenHost = defaultProbeLinkChainPFHost
		}
		out = append(out, probeLinkChainPortForwardConfig{
			ID:         id,
			Name:       strings.TrimSpace(item.Name),
			EntrySide:  entrySide,
			ListenHost: normalizeProbeLinkChainListenHost(listenHost),
			ListenPort: listenPort,
			TargetHost: targetHost,
			TargetPort: targetPort,
			Network:    network,
			Enabled:    item.Enabled,
		})
		if len(out) >= maxProbeLinkChainPortForwardCnt {
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

// fillChainRelayHosts looks up the Cloudflare business DDNS record for each hop node
// and fills in RelayHost dynamically. The returned slice is a copy; original records are not modified.
// ExternalPort is already auto-filled to ListenPort during save; no separate relay_port field is needed.
func fillChainRelayHosts(items []probeLinkChainRecord) []probeLinkChainRecord {
	relayHostByNodeID := buildNodeRelayHostMap()

	out := make([]probeLinkChainRecord, len(items))
	for i, chain := range items {
		if len(chain.HopConfigs) == 0 {
			out[i] = chain
			continue
		}
		hops := make([]probeLinkChainHopConfig, len(chain.HopConfigs))
		for j, hop := range chain.HopConfigs {
			h := hop
			nodeID := normalizeProbeNodeID(strconv.Itoa(hop.NodeNo))
			if nodeID != "" {
				if host, ok := relayHostByNodeID[nodeID]; ok && strings.TrimSpace(h.RelayHost) == "" {
					h.RelayHost = host
				}
			}
			hops[j] = h
		}
		chainCopy := chain
		chainCopy.HopConfigs = hops
		out[i] = chainCopy
	}
	return out
}

// buildNodeRelayHostMap returns a map from nodeID to its business-class API DDNS
// (e.g. "api.codex.<tag>.example.com") from the Cloudflare store.
func buildNodeRelayHostMap() map[string]string {
	result := make(map[string]string)
	if CloudflareStore == nil {
		return result
	}
	CloudflareStore.mu.RLock()
	records := make([]cloudflareDDNSRecord, len(CloudflareStore.data.Records))
	copy(records, CloudflareStore.data.Records)
	CloudflareStore.mu.RUnlock()

	for _, rec := range records {
		if !strings.EqualFold(strings.TrimSpace(rec.RecordClass), "business") {
			continue
		}
		recordName := strings.TrimSpace(rec.RecordName)
		if recordName == "" {
			continue
		}
		nodeID := normalizeProbeNodeID(rec.NodeID)
		if nodeID == "" {
			continue
		}
		// Use sequence 1 (primary record) as the relay host.
		if rec.Sequence != 1 {
			continue
		}
		if _, exists := result[nodeID]; !exists {
			result[nodeID] = recordName
		}
	}
	return result
}

// ProbeLinkChainsHandler serves GET /api/probe/link/chains.
// A probe authenticates with its secret and receives all chain configs
// where it appears as entry, cascade, or exit node. The response includes
// dynamically filled relay_host (Cloudflare DDNS) for each hop, so the
// probe can derive its listen configuration entirely from chain hop_configs.
func ProbeLinkChainsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}

	nodeID, err := authenticateProbeRequestOrQuerySecret(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	if ProbeLinkChainStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "chain store not initialized"})
		return
	}

	ProbeLinkChainStore.mu.RLock()
	all := loadProbeLinkChainsLocked()
	ProbeLinkChainStore.mu.RUnlock()

	related := filterProbeLinkChainsByNodeID(all, nodeID)
	enriched := fillChainRelayHosts(related)

	writeJSON(w, http.StatusOK, map[string]any{"chains": enriched})
}

// filterProbeLinkChainsByNodeID returns chains where nodeID appears in the route
// (entry, cascade, or exit node).
func filterProbeLinkChainsByNodeID(chains []probeLinkChainRecord, nodeID string) []probeLinkChainRecord {
	normalized := normalizeProbeNodeID(nodeID)
	if normalized == "" {
		return nil
	}
	var result []probeLinkChainRecord
	for _, chain := range chains {
		if isProbeLinkChainNodeInRoute(chain, normalized) {
			result = append(result, chain)
		}
	}
	return result
}

// isProbeLinkChainNodeInRoute reports whether nodeID is part of this chain's route.
func isProbeLinkChainNodeInRoute(chain probeLinkChainRecord, nodeID string) bool {
	if normalizeProbeNodeID(chain.EntryNodeID) == nodeID {
		return true
	}
	if normalizeProbeNodeID(chain.ExitNodeID) == nodeID {
		return true
	}
	for _, id := range chain.CascadeNodeIDs {
		if normalizeProbeNodeID(id) == nodeID {
			return true
		}
	}
	return false
}
