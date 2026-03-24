package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// probeLinkChainsSyncAPIPath is the controller endpoint that returns all chains
// where this probe node appears (entry / cascade / exit).
const (
	probeLinkChainsSyncAPIPath      = "/api/probe/link/chains"
	probeLinkChainsSyncPollInterval = 60 * time.Minute
	probeLinkChainsSyncFetchTimeout = 15 * time.Second
)

// probeLinkChainsResponse mirrors the JSON returned by ProbeLinkChainsHandler.
type probeLinkChainsResponse struct {
	Chains []probeLinkChainServerItem `json:"chains"`
}

// probeLinkChainServerItem is a single chain record returned by the controller.
// Fields map 1-to-1 with probeLinkChainRecord / probeChainRuntimeCacheItem.
type probeLinkChainServerItem struct {
	ChainID        string                         `json:"chain_id"`
	Name           string                         `json:"name"`
	UserID         string                         `json:"user_id"`
	UserPublicKey  string                         `json:"user_public_key"`
	Secret         string                         `json:"secret"`
	EntryNodeID    string                         `json:"entry_node_id"`
	ExitNodeID     string                         `json:"exit_node_id"`
	CascadeNodeIDs []string                       `json:"cascade_node_ids"`
	LinkLayer      string                         `json:"link_layer"`
	HopConfigs     []probeLinkChainHopServerItem  `json:"hop_configs"`
	EgressHost     string                         `json:"egress_host"`
	EgressPort     int                            `json:"egress_port"`
}

// probeLinkChainHopServerItem maps one entry in hop_configs.
// relay_host is filled by the controller from Cloudflare DDNS.
type probeLinkChainHopServerItem struct {
	NodeNo       int    `json:"node_no"`
	ListenHost   string `json:"listen_host"`
	ListenPort   int    `json:"listen_port"`
	ExternalPort int    `json:"external_port"`
	LinkLayer    string `json:"link_layer"`
	RelayHost    string `json:"relay_host"`
}

// startProbeLinkChainsSyncLoop pulls chain configs from the controller and
// reconciles running runtimes. Falls back to the existing cache if controller
// is unconfigured or unreachable.
func startProbeLinkChainsSyncLoop(identity nodeIdentity, controllerBaseURL string) {
	go func() {
		// If controller is not configured, there is nothing to pull.
		// Cache restore (restoreProbeChainRuntimesFromCache) already handles
		// the offline case, so we simply skip polling.
		base := strings.TrimSpace(controllerBaseURL)
		if base == "" {
			log.Printf("probe chain sync disabled: controller base url not configured")
			return
		}

		// Initial sync immediately on startup.
		syncProbeChainRuntimes(identity, base)

		ticker := time.NewTicker(probeLinkChainsSyncPollInterval)
		defer ticker.Stop()
		for range ticker.C {
			syncProbeChainRuntimes(identity, base)
		}
	}()
}

// syncProbeChainRuntimes fetches the latest chains from the controller and
// diffing them against currently running runtimes:
//   - New / changed chains → apply (start / restart).
//   - Chains that were removed from the server → stop.
//
// On fetch failure the running runtimes are left untouched (best-effort).
func syncProbeChainRuntimes(identity nodeIdentity, controllerBaseURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), probeLinkChainsSyncFetchTimeout)
	items, err := fetchProbeLinkChains(ctx, controllerBaseURL, identity)
	cancel()
	if err != nil {
		log.Printf("warning: probe chain sync fetch failed: %v (runtimes unchanged)", err)
		return
	}

	applyProbeLinkChainServerItems(identity, items)
}

// fetchProbeLinkChains calls GET /api/probe/link/chains and returns the list.
func fetchProbeLinkChains(ctx context.Context, controllerBaseURL string, identity nodeIdentity) ([]probeLinkChainServerItem, error) {
	requestURL := strings.TrimRight(strings.TrimSpace(controllerBaseURL), "/") + probeLinkChainsSyncAPIPath
	body, err := probeAuthedGet(ctx, requestURL, identity)
	if err != nil {
		return nil, err
	}
	var resp probeLinkChainsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Chains, nil
}

// applyProbeLinkChainServerItems diffs server items against running runtimes.
func applyProbeLinkChainServerItems(identity nodeIdentity, items []probeLinkChainServerItem) {
	// Build a set of chain IDs from the server response.
	serverChainIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ChainID)
		if id != "" {
			serverChainIDs[id] = struct{}{}
		}
	}

	// Stop runtimes that are no longer present on the server.
	probeChainRuntimeState.mu.Lock()
	var toStop []string
	for id := range probeChainRuntimeState.runtimes {
		if _, ok := serverChainIDs[id]; !ok {
			toStop = append(toStop, id)
		}
	}
	probeChainRuntimeState.mu.Unlock()

	for _, id := range toStop {
		stopProbeChainRuntime(id, "chain removed from server")
	}

	// Apply / update chains from the server.
	for _, item := range items {
		applyProbeLinkChainServerItem(identity, item)
	}
}

// applyProbeLinkChainServerItem converts one server chain record into a
// probeControlMessage and delegates to the existing start logic.
// It figures out this node's role and hop config from the chain topology.
func applyProbeLinkChainServerItem(identity nodeIdentity, item probeLinkChainServerItem) {
	chainID := strings.TrimSpace(item.ChainID)
	if chainID == "" {
		return
	}

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
	nextHost, nextPort, nextAuthMode := resolveProbeChainNextHopFromItem(item, nodeID, role)

	// Require next_host+port unless this is the exit node (next_auth_mode=proxy).
	if nextAuthMode != "proxy" && (strings.TrimSpace(nextHost) == "" || nextPort <= 0) {
		log.Printf("warning: probe chain sync skip chain=%s role=%s: next hop not resolved", chainID, role)
		return
	}

	listenHost := strings.TrimSpace(hop.ListenHost)
	if listenHost == "" {
		listenHost = "0.0.0.0"
	}

	msg := probeControlMessage{
		ChainID:         chainID,
		Name:            strings.TrimSpace(item.Name),
		UserID:          strings.TrimSpace(item.UserID),
		UserPublicKey:   strings.TrimSpace(item.UserPublicKey),
		LinkSecret:      strings.TrimSpace(item.Secret),
		Role:            role,
		ListenHost:      listenHost,
		ListenPort:      hop.ListenPort,
		LinkLayer:       firstNonEmpty(strings.TrimSpace(hop.LinkLayer), strings.TrimSpace(item.LinkLayer), "http"),
		NextHost:        nextHost,
		NextPort:        nextPort,
		RequireUserAuth: strings.TrimSpace(item.UserPublicKey) != "",
		NextAuthMode:    nextAuthMode,
	}

	cfg, err := buildProbeChainRuntimeConfigFromControl(msg)
	if err != nil {
		log.Printf("warning: probe chain sync build config failed: chain=%s err=%v", chainID, err)
		return
	}

	// Skip restart if config has not changed (compare fields that affect behaviour).
	if isSameProbeChainRuntimeConfig(chainID, cfg) {
		return
	}

	if _, err := startProbeChainRuntime(cfg); err != nil {
		log.Printf("warning: probe chain sync start failed: chain=%s err=%v", chainID, err)
	}
}

// resolveProbeNodeChainRole returns the role of nodeID in the chain.
func resolveProbeNodeChainRole(item probeLinkChainServerItem, nodeID string) string {
	isEntry := strings.TrimSpace(item.EntryNodeID) == nodeID
	isExit := strings.TrimSpace(item.ExitNodeID) == nodeID
	if isEntry && isExit {
		return "entry_exit"
	}
	if isEntry {
		return "entry"
	}
	if isExit {
		return "exit"
	}
	for _, id := range item.CascadeNodeIDs {
		if strings.TrimSpace(id) == nodeID {
			return "relay"
		}
	}
	return ""
}

// findHopConfigForNode returns the hop_config for nodeID by matching node_no
// against the chain's node_no ordering. Node numbering follows the route order:
// EntryNodeID=1, CascadeNodeIDs... , ExitNodeID=last.
func findHopConfigForNode(item probeLinkChainServerItem, nodeID string) probeLinkChainHopServerItem {
	// Build a node_no → node_id mapping from the route.
	route := buildChainRoute(item)
	for no, id := range route {
		if id == nodeID {
			nodeNo := no + 1 // 1-indexed
			for _, hop := range item.HopConfigs {
				if hop.NodeNo == nodeNo {
					return hop
				}
			}
		}
	}
	return probeLinkChainHopServerItem{}
}

// resolveProbeChainNextHopFromItem determines next_host, next_port, next_auth_mode
// based on the current node's position in the chain.
//   - Entry/Relay:  next hop = following node in route (use relay_host + external_port)
//   - Exit:         next_auth_mode = "proxy" (connects to actual destination)
func resolveProbeChainNextHopFromItem(item probeLinkChainServerItem, nodeID, role string) (host string, port int, authMode string) {
	if role == "exit" || role == "entry_exit" {
		// Exit node proxies to the end target, no next relay needed.
		return "", 0, "proxy"
	}

	route := buildChainRoute(item)
	for i, id := range route {
		if id != nodeID {
			continue
		}
		if i+1 >= len(route) {
			break
		}
		nextNodeID := route[i+1]
		nextNo := i + 2 // 1-indexed
		for _, hop := range item.HopConfigs {
			if hop.NodeNo != nextNo {
				continue
			}
			// Prefer relay_host (DDNS filled by server); fall back to empty (will fail).
			relayHost := strings.TrimSpace(hop.RelayHost)
			// external_port is auto-filled to listen_port by the server.
			externalPort := hop.ExternalPort
			if externalPort <= 0 {
				externalPort = hop.ListenPort
			}
			_ = nextNodeID
			return relayHost, externalPort, "secret"
		}
	}
	return "", 0, "none"
}

// buildChainRoute returns the ordered node ID list: [entry, cascade..., exit].
func buildChainRoute(item probeLinkChainServerItem) []string {
	route := make([]string, 0, 2+len(item.CascadeNodeIDs))
	route = append(route, strings.TrimSpace(item.EntryNodeID))
	for _, id := range item.CascadeNodeIDs {
		route = append(route, strings.TrimSpace(id))
	}
	route = append(route, strings.TrimSpace(item.ExitNodeID))
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
	return c.role == cfg.role &&
		c.listenHost == cfg.listenHost &&
		c.listenPort == cfg.listenPort &&
		c.linkLayer == cfg.linkLayer &&
		c.nextHost == cfg.nextHost &&
		c.nextPort == cfg.nextPort &&
		c.nextAuthMode == cfg.nextAuthMode &&
		c.secret == cfg.secret &&
		c.rawPublicKey == cfg.rawPublicKey
}
