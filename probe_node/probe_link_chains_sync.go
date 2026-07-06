package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// probeLinkChainsSyncAPIPath is the controller endpoint that returns all chains
// where this probe node appears (entry / cascade / exit).
const (
	probeRouteConfigAPIPath         = "/api/probe/route/config"
	probeRouteFakeIPResolveAPIPath  = "/api/probe/route/fake_ip/resolve"
	probeLinkChainsSyncPollInterval = 60 * time.Minute
	probeLinkChainsSyncFetchTimeout = 15 * time.Second
	probeRouteConfigCacheFileName   = "probe_route_config.json"
)

type probeRouteConfigResponse struct {
	NodeID        string                   `json:"node_id"`
	VirtualRouter probeVirtualRouterConfig `json:"virtual_router,omitempty"`
}

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

type probeRouteConfigCacheFile struct {
	UpdatedAt string                   `json:"updated_at"`
	Item      probeVirtualRouterConfig `json:"item"`
}

type probeRouteFakeIPResolveResponse struct {
	NodeID        string                          `json:"node_id"`
	Item          probeVirtualRouterFakeIPEntry   `json:"item"`
	FakeIPLibrary probeVirtualRouterFakeIPLibrary `json:"fake_ip_library"`
}

var (
	probeRequestRouteConfig = requestProbeRouteConfig
	probeRequestRouteFakeIP = requestProbeRouteFakeIP
)

// probeLinkChainServerItem is a single chain record returned by the controller.
// Fields map 1-to-1 with probeLinkChainRecord / probeChainRuntimeCacheItem.
type probeLinkChainServerItem struct {
	ChainID         string                        `json:"chain_id"`
	RelayChainID    string                        `json:"relay_chain_id"`
	ClientEntryID   string                        `json:"client_entry_id"`
	ClientEntryType string                        `json:"client_entry_type"`
	ChainType       string                        `json:"chain_type"`
	Name            string                        `json:"name"`
	UserID          string                        `json:"user_id"`
	UserPublicKey   string                        `json:"user_public_key"`
	Secret          string                        `json:"secret"`
	AuthTicket      string                        `json:"auth_ticket,omitempty"`
	EntryNodeID     string                        `json:"entry_node_id"`
	ExitNodeID      string                        `json:"exit_node_id"`
	CascadeNodeIDs  []string                      `json:"cascade_node_ids"`
	LinkLayer       string                        `json:"link_layer"`
	HopConfigs      []probeLinkChainHopServerItem `json:"hop_configs"`
	EgressHost      string                        `json:"egress_host"`
	EgressPort      int                           `json:"egress_port"`
}

// probeLinkChainHopServerItem maps one entry in hop_configs.
// relay_host is the selected domain for this hop node.
type probeLinkChainHopServerItem struct {
	NodeNo       int    `json:"node_no"`
	ListenHost   string `json:"listen_host"`
	ListenPort   int    `json:"listen_port"`
	ExternalPort int    `json:"external_port"`
	LinkLayer    string `json:"link_layer"`
	DialMode     string `json:"dial_mode"`
	RelayHost    string `json:"relay_host"`
}

// startProbeLinkChainsSyncLoop pulls chain configs from the controller and
// reconciles running runtimes. Falls back to the existing cache if controller
// is unconfigured or unreachable.
func startProbeRouteConfigSyncLoop(identity nodeIdentity, controllerBaseURL string) {
	go func() {
		base := strings.TrimSpace(controllerBaseURL)
		if base == "" {
			log.Printf("probe route config sync disabled: controller base url not configured")
			return
		}

		if err := syncProbeRouteConfig(identity, base); err != nil {
			log.Printf("warning: initial probe route config sync failed: %v", err)
		}

		ticker := time.NewTicker(probeLinkChainsSyncPollInterval)
		defer ticker.Stop()
		for range ticker.C {
			if err := syncProbeRouteConfig(identity, base); err != nil {
				log.Printf("warning: probe route config sync failed: %v", err)
			}
		}
	}()
}

func syncProbeRouteConfig(identity nodeIdentity, controllerBaseURL string) error {
	rememberProbeVirtualRouterController(identity, controllerBaseURL)
	ctx, cancel := context.WithTimeout(context.Background(), probeLinkChainsSyncFetchTimeout)
	config, err := fetchProbeRouteConfig(ctx, controllerBaseURL, identity)
	cancel()
	if err != nil {
		if cached, cacheErr := loadProbeRouteConfigCache(); cacheErr == nil {
			applyProbeVirtualRouterConfigForNode(cached, identity.NodeID)
			applyProbeVirtualRouterRuntimesForNode(identity, controllerBaseURL, cached)
		} else {
			log.Printf("warning: load probe route config cache failed: %v", cacheErr)
		}
		return err
	}
	if err := persistProbeRouteConfigCache(config); err != nil {
		log.Printf("warning: persist probe route config cache failed: %v", err)
	}
	applyProbeVirtualRouterConfigForNode(config, identity.NodeID)
	applyProbeVirtualRouterRuntimesForNode(identity, controllerBaseURL, config)
	return nil
}

func rememberProbeVirtualRouterController(identity nodeIdentity, controllerBaseURL string) {
	probeVirtualRouterControllerState.mu.Lock()
	probeVirtualRouterControllerState.identity = identity
	probeVirtualRouterControllerState.controllerBaseURL = strings.TrimSpace(controllerBaseURL)
	probeVirtualRouterControllerState.mu.Unlock()
}

func currentProbeVirtualRouterController() (nodeIdentity, string, bool) {
	probeVirtualRouterControllerState.mu.RLock()
	defer probeVirtualRouterControllerState.mu.RUnlock()
	identity := probeVirtualRouterControllerState.identity
	baseURL := strings.TrimSpace(probeVirtualRouterControllerState.controllerBaseURL)
	return identity, baseURL, strings.TrimSpace(identity.NodeID) != "" && strings.TrimSpace(identity.Secret) != "" && baseURL != ""
}

func fetchProbeRouteConfig(ctx context.Context, controllerBaseURL string, identity nodeIdentity) (probeVirtualRouterConfig, error) {
	base := strings.TrimSpace(controllerBaseURL)
	if base == "" {
		return probeVirtualRouterConfig{}, errors.New("controller base url is empty")
	}
	config, err := probeRequestRouteConfig(ctx, base, identity)
	if err != nil {
		return probeVirtualRouterConfig{}, err
	}
	config = sanitizeProbeVirtualRouterConfigForCache(config)
	rememberProbeVirtualRouterAuthTickets(config)
	return config, nil
}

func requestProbeRouteConfig(ctx context.Context, controllerBaseURL string, identity nodeIdentity) (probeVirtualRouterConfig, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(controllerBaseURL), "/")
	if baseURL == "" {
		return probeVirtualRouterConfig{}, errors.New("controller base url is required")
	}
	nodeID := strings.TrimSpace(identity.NodeID)
	secret := strings.TrimSpace(identity.Secret)
	if nodeID == "" || secret == "" {
		return probeVirtualRouterConfig{}, errors.New("node identity is missing node id or secret")
	}

	query := url.Values{}
	query.Set("node_id", nodeID)
	query.Set("secret", secret)
	configURL := baseURL + probeRouteConfigAPIPath + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, configURL, nil)
	if err != nil {
		return probeVirtualRouterConfig{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")

	client, closeClient, err := newProbeResolvedHTTPClientForURL(configURL, probeLinkChainsSyncFetchTimeout)
	if err != nil {
		return probeVirtualRouterConfig{}, err
	}
	defer closeClient()
	resp, err := client.Do(req)
	if err != nil {
		return probeVirtualRouterConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return probeVirtualRouterConfig{}, fmt.Errorf("request route config failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload probeRouteConfigResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return probeVirtualRouterConfig{}, err
	}
	return sanitizeProbeVirtualRouterConfigForCache(payload.VirtualRouter), nil
}

func requestProbeRouteFakeIP(ctx context.Context, controllerBaseURL string, identity nodeIdentity, domain string, rule probeVirtualRouterRouteRule) (probeVirtualRouterFakeIPEntry, probeVirtualRouterFakeIPLibrary, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(controllerBaseURL), "/")
	if baseURL == "" {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, errors.New("controller base url is required")
	}
	nodeID := strings.TrimSpace(identity.NodeID)
	secret := strings.TrimSpace(identity.Secret)
	if nodeID == "" || secret == "" {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, errors.New("node identity is missing node id or secret")
	}
	query := url.Values{}
	query.Set("node_id", nodeID)
	query.Set("secret", secret)
	endpoint := baseURL + probeRouteFakeIPResolveAPIPath + "?" + query.Encode()
	body, err := json.Marshal(map[string]string{
		"domain":       strings.TrimSpace(domain),
		"rule_id":      strings.TrimSpace(rule.ID),
		"action":       strings.TrimSpace(rule.Action),
		"exit_node_id": strings.TrimSpace(rule.ExitNodeID),
	})
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	client, closeClient, err := newProbeResolvedHTTPClientForURL(endpoint, probeLinkChainsSyncFetchTimeout)
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, err
	}
	defer closeClient()
	resp, err := client.Do(req)
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, fmt.Errorf("request route fake ip failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload probeRouteFakeIPResolveResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, err
	}
	return payload.Item, payload.FakeIPLibrary, nil
}

// applyProbeLinkChainServerItems diffs server items against running runtimes.
func applyProbeLinkChainServerItems(identity nodeIdentity, controllerBaseURL string, items []probeLinkChainServerItem) {
	_ = identity
	_ = controllerBaseURL
	probeChainRuntimeState.mu.Lock()
	toStop := collectProbeLinkChainLegacyRuntimeIDsLocked()
	probeChainRuntimeState.mu.Unlock()
	for _, id := range toStop {
		stopProbeChainRuntime(id, "legacy probe chain runtime removed")
	}
	if len(items) > 0 {
		log.Printf("probe chain sync skipped legacy runtimes: reason=feature_removed count=%d", len(items))
	}
}

func collectProbeLinkChainLegacyRuntimeIDsLocked() []string {
	toStop := make([]string, 0)
	for id := range probeChainRuntimeState.runtimes {
		if isProbeVirtualRouterRuntimeChainID(id) {
			continue
		}
		toStop = append(toStop, id)
	}
	sort.Strings(toStop)
	return toStop
}

// applyProbeLinkChainServerItem converts one server chain record into a
// probeControlMessage and delegates to the existing start logic.
// It figures out this node's role and hop config from the chain topology.
func applyProbeLinkChainServerItem(identity nodeIdentity, controllerBaseURL string, item probeLinkChainServerItem) {
	chainID := strings.TrimSpace(item.ChainID)
	if chainID == "" {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(item.ChainType), "virtual_router") && !isProbeVirtualRouterRuntimeChainID(chainID) {
		stopProbeChainRuntime(chainID, "legacy probe chain runtime removed")
		log.Printf("probe chain sync skip legacy chain: chain=%s reason=feature_removed", chainID)
		return
	}
	rememberProbeChainAuthTicket(effectiveProbeLinkRelayChainID(item), item.AuthTicket)

	nodeID := strings.TrimSpace(identity.NodeID)
	role := resolveProbeNodeChainRole(item, nodeID)
	if role == "" {
		// This node is not in the chain's route – skip.
		return
	}

	// Locate this node's hop config to get listen_port, link_layer, listen_host.
	hop := findHopConfigForNode(item, nodeID)
	if hop.ListenPort <= 0 {
		log.Printf("warning: probe chain sync skip chain=%s role=%s: hop listen_port not configured", chainID, role)
		return
	}

	// Determine the next hop (relay_host:external_port) based on role.
	nextHost, nextPort, nextLinkLayer, nextDialMode, nextAuthMode := resolveProbeChainNextHopFromItem(item, nodeID, role)
	prevHost, prevPort, prevLinkLayer, prevDialMode := resolveProbeChainPrevHopFromItem(item, nodeID, role)
	nextNodeID, prevNodeID := resolveProbeChainAdjacentNodeIDsFromItem(item, nodeID)

	// Require next_host+port unless this is the exit node (next_auth_mode=proxy).
	if nextAuthMode != "proxy" && (strings.TrimSpace(nextHost) == "" || nextPort <= 0) {
		log.Printf("warning: probe chain sync skip chain=%s role=%s: next hop not resolved", chainID, role)
		return
	}
	if strings.EqualFold(strings.TrimSpace(prevDialMode), "reverse") && (strings.TrimSpace(prevHost) == "" || prevPort <= 0) {
		log.Printf("warning: probe chain sync skip chain=%s role=%s: prev reverse hop not resolved", chainID, role)
		return
	}

	listenHost := strings.TrimSpace(hop.ListenHost)
	if listenHost == "" {
		listenHost = "0.0.0.0"
	}

	msg := probeControlMessage{
		ChainID:         chainID,
		ChainType:       strings.TrimSpace(item.ChainType),
		Name:            strings.TrimSpace(item.Name),
		UserID:          strings.TrimSpace(item.UserID),
		UserPublicKey:   strings.TrimSpace(item.UserPublicKey),
		LinkSecret:      strings.TrimSpace(item.Secret),
		AuthTicket:      strings.TrimSpace(item.AuthTicket),
		Role:            role,
		ListenHost:      listenHost,
		ListenPort:      hop.ListenPort,
		LinkLayer:       normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(hop.LinkLayer), strings.TrimSpace(item.LinkLayer))),
		NextLinkLayer:   strings.TrimSpace(nextLinkLayer),
		NextDialMode:    strings.TrimSpace(nextDialMode),
		NextHost:        nextHost,
		NextPort:        nextPort,
		PrevHost:        prevHost,
		PrevPort:        prevPort,
		PrevLinkLayer:   strings.TrimSpace(prevLinkLayer),
		PrevDialMode:    strings.TrimSpace(prevDialMode),
		RequireUserAuth: true,
		NextAuthMode:    nextAuthMode,
	}

	cfg, err := buildProbeChainRuntimeConfigFromControl(msg)
	if err != nil {
		log.Printf("warning: probe chain sync build config failed: chain=%s err=%v", chainID, err)
		return
	}
	cfg.identity = identity
	cfg.controllerURL = resolveProbeControllerBaseURL(strings.TrimSpace(controllerBaseURL), "")
	cfg.nextNodeID = nextNodeID
	cfg.prevNodeID = prevNodeID

	// Skip restart if config has not changed (compare fields that affect behaviour).
	if isSameProbeChainRuntimeConfig(chainID, cfg) {
		updateRunningProbeChainRuntimeAuthTicket(chainID, cfg.authTicket)
		return
	}

	if _, err := startProbeChainRuntime(cfg); err != nil {
		log.Printf("warning: probe chain sync start failed: chain=%s err=%v", chainID, err)
	}
}

func rememberProbeChainAuthTicketsForItems(items []probeLinkChainServerItem) {
	for _, item := range items {
		rememberProbeChainAuthTicket(effectiveProbeLinkRelayChainID(item), item.AuthTicket)
	}
}

func rememberProbeVirtualRouterAuthTickets(config probeVirtualRouterConfig) {
	for _, item := range probeVirtualRouterAuthTicketItems(config) {
		rememberProbeChainAuthTicket(effectiveProbeLinkRelayChainID(item), item.AuthTicket)
	}
}

func probeVirtualRouterAuthTicketItems(config probeVirtualRouterConfig) []probeLinkChainServerItem {
	config = sanitizeProbeVirtualRouterConfigForCache(config)
	if !config.Enabled || len(config.TopologyRules) == 0 {
		return []probeLinkChainServerItem{}
	}
	out := make([]probeLinkChainServerItem, 0, len(config.TopologyRules))
	seen := map[string]struct{}{}
	for _, rule := range config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		chainID := strings.TrimSpace(probeVirtualRouterRuntimeChainID(rule))
		if chainID == "" {
			continue
		}
		if _, exists := seen[chainID]; exists {
			continue
		}
		seen[chainID] = struct{}{}
		out = append(out, probeLinkChainServerItem{
			ChainID:       chainID,
			ClientEntryID: strings.TrimSpace(rule.ID),
			ChainType:     "virtual_router",
			Name:          strings.TrimSpace(rule.Name),
			UserID:        strings.TrimSpace(rule.UserID),
			UserPublicKey: strings.TrimSpace(rule.UserPublicKey),
			Secret:        strings.TrimSpace(rule.Secret),
			AuthTicket:    strings.TrimSpace(rule.AuthTicket),
			EntryNodeID:   normalizeProbeChainNodeID(rule.FromNodeID),
			ExitNodeID:    normalizeProbeChainNodeID(rule.ToNodeID),
		})
	}
	return out
}

func updateRunningProbeChainRuntimeAuthTicket(chainID string, authTicket string) {
	id := strings.TrimSpace(chainID)
	ticket := strings.TrimSpace(authTicket)
	if id == "" || ticket == "" {
		return
	}
	rememberProbeChainAuthTicket(id, ticket)
	probeChainRuntimeState.mu.Lock()
	if rt, ok := probeChainRuntimeState.runtimes[id]; ok && rt != nil {
		rt.cfg.authTicket = ticket
	}
	probeChainRuntimeState.mu.Unlock()
}

func effectiveProbeLinkRelayChainID(item probeLinkChainServerItem) string {
	if relayID := strings.TrimSpace(item.RelayChainID); relayID != "" {
		return relayID
	}
	return strings.TrimSpace(item.ChainID)
}

func normalizeProbeChainNodeID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "node-") || strings.HasPrefix(lower, "node_") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(lower, "node-"), "node_")
		suffix = strings.TrimSpace(suffix)
		if suffix != "" {
			if n, err := strconv.Atoi(suffix); err == nil && n > 0 {
				return strconv.Itoa(n)
			}
			return suffix
		}
	}
	if n, err := strconv.Atoi(value); err == nil && n > 0 {
		return strconv.Itoa(n)
	}
	return value
}

func findHopConfigForNodeID(item probeLinkChainServerItem, nodeID string) (probeLinkChainHopServerItem, bool) {
	targetNodeID := normalizeProbeChainNodeID(nodeID)
	if targetNodeID == "" {
		return probeLinkChainHopServerItem{}, false
	}
	for _, hop := range item.HopConfigs {
		if hop.NodeNo <= 0 {
			continue
		}
		hopNodeID := normalizeProbeChainNodeID(strconv.Itoa(hop.NodeNo))
		if hopNodeID == "" || hopNodeID != targetNodeID {
			continue
		}
		return hop, true
	}
	return probeLinkChainHopServerItem{}, false
}

// resolveProbeNodeChainRole returns the role of nodeID in the chain.
func resolveProbeNodeChainRole(item probeLinkChainServerItem, nodeID string) string {
	targetNodeID := normalizeProbeChainNodeID(nodeID)
	if targetNodeID == "" {
		return ""
	}
	entryNodeID := normalizeProbeChainNodeID(item.EntryNodeID)
	exitNodeID := normalizeProbeChainNodeID(item.ExitNodeID)
	isEntry := entryNodeID != "" && targetNodeID == entryNodeID
	isExit := exitNodeID != "" && targetNodeID == exitNodeID
	if isEntry && isExit {
		return "entry_exit"
	}
	if isEntry {
		return "entry"
	}
	if isExit {
		return "exit"
	}

	// Topology fallback: when entry/exit fields are partially missing,
	// infer head/tail roles from computed route [entry, cascade..., exit].
	// This keeps single-cascade chains (e.g. entry missing, cascade has one node)
	// correctly treated as entry instead of relay.
	route := buildChainRoute(item)
	if len(route) > 0 {
		inferredEntry := normalizeProbeChainNodeID(route[0])
		inferredExit := normalizeProbeChainNodeID(route[len(route)-1])
		inferredIsEntry := inferredEntry != "" && targetNodeID == inferredEntry
		inferredIsExit := inferredExit != "" && targetNodeID == inferredExit
		if inferredIsEntry && inferredIsExit {
			return "entry_exit"
		}
		if inferredIsEntry {
			return "entry"
		}
		if inferredIsExit {
			return "exit"
		}
	}

	for _, id := range item.CascadeNodeIDs {
		if normalizeProbeChainNodeID(id) == targetNodeID {
			return "relay"
		}
	}
	return ""
}

// findHopConfigForNode returns the hop_config for nodeID. It first matches hop.node_no
// as node identity (current format), then falls back to legacy positional numbering.
func findHopConfigForNode(item probeLinkChainServerItem, nodeID string) probeLinkChainHopServerItem {
	if hop, ok := findHopConfigForNodeID(item, nodeID); ok {
		return hop
	}

	// Legacy fallback: node_no was stored as route position (1..N).
	targetNodeID := normalizeProbeChainNodeID(nodeID)
	route := buildChainRoute(item)
	for no, id := range route {
		if normalizeProbeChainNodeID(id) != targetNodeID {
			continue
		}
		legacyNodeNo := no + 1 // 1-indexed
		for _, hop := range item.HopConfigs {
			if hop.NodeNo == legacyNodeNo {
				return hop
			}
		}
		break
	}
	return probeLinkChainHopServerItem{}
}

// resolveProbeChainNextHopFromItem determines next_host, next_port, next_auth_mode
// based on the current node's position in the chain.
//   - Entry/Relay:  next hop = following node in route (use relay_host + external_port)
//   - Exit:         next_auth_mode = "proxy" (connects to actual destination)
func resolveProbeChainNextHopFromItem(item probeLinkChainServerItem, nodeID, role string) (host string, port int, nextLayer string, nextDialMode string, authMode string) {
	if role == "exit" || role == "entry_exit" {
		// Exit node proxies to the end target, no next relay needed.
		return "", 0, "", probeChainDialModeNone, "proxy"
	}

	route := buildChainRoute(item)
	targetNodeID := normalizeProbeChainNodeID(nodeID)
	for i, id := range route {
		if normalizeProbeChainNodeID(id) != targetNodeID {
			continue
		}
		if i+1 >= len(route) {
			break
		}
		nextNodeID := route[i+1]
		dialMode := probeChainDialModeForward
		if currentHop, ok := findHopConfigForNodeID(item, id); ok {
			dialMode = normalizeProbeChainDialMode(strings.TrimSpace(currentHop.DialMode))
		}
		nextHop := findHopConfigForNode(item, nextNodeID)
		relayHost := strings.TrimSpace(nextHop.RelayHost)
		externalPort := nextHop.ExternalPort
		if externalPort <= 0 {
			externalPort = nextHop.ListenPort
		}
		return relayHost, externalPort, normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(nextHop.LinkLayer), strings.TrimSpace(item.LinkLayer))), dialMode, "secret"
	}
	return "", 0, "", probeChainDialModeNone, "none"
}

func resolveProbeChainPrevHopFromItem(item probeLinkChainServerItem, nodeID, role string) (host string, port int, prevLayer string, prevDialMode string) {
	if role == "entry" {
		return "", 0, "", probeChainDialModeNone
	}
	route := buildChainRoute(item)
	targetNodeID := normalizeProbeChainNodeID(nodeID)
	for i, id := range route {
		if normalizeProbeChainNodeID(id) != targetNodeID {
			continue
		}
		if i <= 0 {
			return "", 0, "", probeChainDialModeNone
		}
		prevNodeID := route[i-1]
		prevHop := findHopConfigForNode(item, prevNodeID)
		externalPort := prevHop.ExternalPort
		if externalPort <= 0 {
			externalPort = prevHop.ListenPort
		}
		return strings.TrimSpace(prevHop.RelayHost), externalPort, normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(prevHop.LinkLayer), strings.TrimSpace(item.LinkLayer))), normalizeProbeChainDialMode(strings.TrimSpace(prevHop.DialMode))
	}
	return "", 0, "", probeChainDialModeNone
}

func resolveProbeChainAdjacentNodeIDsFromItem(item probeLinkChainServerItem, nodeID string) (nextNodeID string, prevNodeID string) {
	route := buildChainRoute(item)
	targetNodeID := normalizeProbeChainNodeID(nodeID)
	for i, id := range route {
		if normalizeProbeChainNodeID(id) != targetNodeID {
			continue
		}
		if i+1 < len(route) {
			nextNodeID = normalizeProbeChainNodeID(route[i+1])
		}
		if i > 0 {
			prevNodeID = normalizeProbeChainNodeID(route[i-1])
		}
		return nextNodeID, prevNodeID
	}
	return "", ""
}

// buildChainRoute returns the ordered node ID list: [entry, cascade..., exit].
func buildChainRoute(item probeLinkChainServerItem) []string {
	route := make([]string, 0, 2+len(item.CascadeNodeIDs))
	seen := make(map[string]struct{}, 2+len(item.CascadeNodeIDs))
	push := func(raw string) {
		nodeID := normalizeProbeChainNodeID(raw)
		if nodeID == "" {
			return
		}
		if _, exists := seen[nodeID]; exists {
			return
		}
		seen[nodeID] = struct{}{}
		route = append(route, nodeID)
	}
	push(item.EntryNodeID)
	for _, id := range item.CascadeNodeIDs {
		push(id)
	}
	push(item.ExitNodeID)
	return route
}

// isSameProbeChainRuntimeConfig returns true if the currently running runtime
// for chainID has the same effective config as cfg (no restart needed).
func isSameProbeChainRuntimeConfig(chainID string, cfg probeChainRuntimeConfig) bool {
	probeChainRuntimeState.mu.Lock()
	rt, ok := probeChainRuntimeState.runtimes[chainID]
	probeChainRuntimeState.mu.Unlock()
	if !ok || rt == nil {
		return false
	}
	c := rt.cfg
	return c.chainType == cfg.chainType &&
		c.role == cfg.role &&
		c.listenHost == cfg.listenHost &&
		c.listenPort == cfg.listenPort &&
		c.linkLayer == cfg.linkLayer &&
		c.nextLinkLayer == cfg.nextLinkLayer &&
		c.nextDialMode == cfg.nextDialMode &&
		c.nextNodeID == cfg.nextNodeID &&
		c.nextHost == cfg.nextHost &&
		c.nextPort == cfg.nextPort &&
		c.prevHost == cfg.prevHost &&
		c.prevPort == cfg.prevPort &&
		c.prevLinkLayer == cfg.prevLinkLayer &&
		c.prevDialMode == cfg.prevDialMode &&
		c.prevNodeID == cfg.prevNodeID &&
		c.nextAuthMode == cfg.nextAuthMode &&
		c.secret == cfg.secret &&
		c.rawPublicKey == cfg.rawPublicKey
}
