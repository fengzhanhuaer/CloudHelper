package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"sort"
	"strings"
)

const (
	probeVirtualRouterRuntimeChainIDPrefix = "vrouter-"
	probeVirtualRouterRuntimeLinkLayer     = "websocket"
)

func isProbeVirtualRouterRuntimeChainID(chainID string) bool {
	return strings.HasPrefix(strings.TrimSpace(chainID), probeVirtualRouterRuntimeChainIDPrefix)
}

func applyProbeVirtualRouterRuntimesForNode(identity nodeIdentity, controllerBaseURL string, config probeVirtualRouterConfig) {
	localNodeID := normalizeProbeChainNodeID(identity.NodeID)
	if localNodeID == "" {
		stopProbeVirtualRouterRuntimesExcept(nil, "virtual router local node id empty")
		return
	}
	config = sanitizeProbeVirtualRouterConfigForCache(config)
	if !config.Enabled {
		stopProbeVirtualRouterRuntimesExcept(nil, "virtual router disabled")
		return
	}
	configs := buildProbeVirtualRouterRuntimeConfigsForNode(config, identity, controllerBaseURL)
	desired := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		desired[strings.TrimSpace(cfg.chainID)] = struct{}{}
	}
	stopProbeVirtualRouterRuntimesExcept(desired, "virtual router topology changed")
	for _, cfg := range configs {
		if strings.TrimSpace(cfg.chainID) == "" {
			continue
		}
		if isSameProbeChainRuntimeConfig(cfg.chainID, cfg) {
			updateRunningProbeChainRuntimeAuthTicket(cfg.chainID, cfg.authTicket)
			continue
		}
		if _, err := startProbeChainRuntime(cfg); err != nil {
			log.Printf("warning: probe virtual router runtime start failed: chain=%s local=%s next=%s prev=%s err=%v", cfg.chainID, localNodeID, cfg.nextNodeID, cfg.prevNodeID, err)
		}
	}
}

func stopProbeVirtualRouterRuntimesExcept(desired map[string]struct{}, reason string) {
	probeChainRuntimeState.mu.Lock()
	ids := make([]string, 0)
	for id := range probeChainRuntimeState.runtimes {
		if !isProbeVirtualRouterRuntimeChainID(id) {
			continue
		}
		if desired != nil {
			if _, ok := desired[id]; ok {
				continue
			}
		}
		ids = append(ids, id)
	}
	probeChainRuntimeState.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		stopProbeChainRuntime(id, reason)
	}
}

func buildProbeVirtualRouterRuntimeConfigsForNode(config probeVirtualRouterConfig, identity nodeIdentity, controllerBaseURL string) []probeChainRuntimeConfig {
	localNodeID := normalizeProbeChainNodeID(identity.NodeID)
	if localNodeID == "" {
		return []probeChainRuntimeConfig{}
	}
	config = sanitizeProbeVirtualRouterConfigForCache(config)
	if !config.Enabled {
		return []probeChainRuntimeConfig{}
	}
	out := make([]probeChainRuntimeConfig, 0)
	seen := make(map[string]struct{})
	for _, rule := range config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		fromNodeID := normalizeProbeChainNodeID(rule.FromNodeID)
		toNodeID := normalizeProbeChainNodeID(rule.ToNodeID)
		if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
			continue
		}
		if localNodeID != fromNodeID && localNodeID != toNodeID {
			continue
		}
		cfg, ok := buildProbeVirtualRouterRuntimeConfigForRule(rule, identity, controllerBaseURL)
		if !ok {
			continue
		}
		if _, exists := seen[cfg.chainID]; exists {
			continue
		}
		seen[cfg.chainID] = struct{}{}
		out = append(out, cfg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].chainID < out[j].chainID
	})
	return out
}

func buildProbeVirtualRouterRuntimeConfigForRule(rule probeVirtualRouterTopologyRule, identity nodeIdentity, controllerBaseURL string) (probeChainRuntimeConfig, bool) {
	localNodeID := normalizeProbeChainNodeID(identity.NodeID)
	fromNodeID := normalizeProbeChainNodeID(rule.FromNodeID)
	toNodeID := normalizeProbeChainNodeID(rule.ToNodeID)
	if localNodeID == "" || fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
		return probeChainRuntimeConfig{}, false
	}
	localIsFrom := localNodeID == fromNodeID
	localIsTo := localNodeID == toNodeID
	if !localIsFrom && !localIsTo {
		return probeChainRuntimeConfig{}, false
	}
	peerNodeID := toNodeID
	localPort := normalizeProbeVirtualRouterServicePort(rule.FromServicePort)
	peerDomain := strings.TrimSpace(rule.ToServiceDomain)
	peerPort := normalizeProbeVirtualRouterServicePort(rule.ToServicePort)
	if localIsTo {
		peerNodeID = fromNodeID
		localPort = normalizeProbeVirtualRouterServicePort(rule.ToServicePort)
		peerDomain = strings.TrimSpace(rule.FromServiceDomain)
		peerPort = normalizeProbeVirtualRouterServicePort(rule.FromServicePort)
	}
	if localPort <= 0 {
		return probeChainRuntimeConfig{}, false
	}

	chainID := probeVirtualRouterRuntimeChainID(rule)
	dialerNodeID := probeVirtualRouterRuleDialerNodeID(rule)
	if dialerNodeID == "" {
		return probeChainRuntimeConfig{}, false
	}
	secret := strings.TrimSpace(rule.Secret)
	authTicket := strings.TrimSpace(rule.AuthTicket)
	rawPublicKey := strings.TrimSpace(rule.UserPublicKey)
	if secret == "" || authTicket == "" || rawPublicKey == "" {
		log.Printf("warning: probe virtual router rule skipped: chain=%s missing link auth fields", chainID)
		return probeChainRuntimeConfig{}, false
	}
	userPublicKey, err := parseProbeChainUserPublicKey(rawPublicKey)
	if err != nil {
		log.Printf("warning: probe virtual router rule skipped: chain=%s invalid user_public_key: %v", chainID, err)
		return probeChainRuntimeConfig{}, false
	}
	cfg := probeChainRuntimeConfig{
		chainID:         chainID,
		chainType:       "virtual_router",
		name:            "Virtual Router " + firstNonEmpty(strings.TrimSpace(rule.Name), strings.TrimSpace(rule.ID), fromNodeID+"-"+toNodeID),
		userID:          strings.TrimSpace(rule.UserID),
		rawPublicKey:    rawPublicKey,
		userPublicKey:   userPublicKey,
		secret:          secret,
		authTicket:      authTicket,
		role:            "relay",
		listenHost:      "0.0.0.0",
		listenPort:      localPort,
		linkLayer:       probeVirtualRouterRuntimeLinkLayer,
		nextAuthMode:    "proxy",
		nextDialMode:    probeChainDialModeNone,
		prevDialMode:    probeChainDialModeNone,
		requireUserAuth: true,
		identity:        identity,
		controllerURL:   resolveProbeControllerBaseURL(strings.TrimSpace(controllerBaseURL), ""),
	}
	if localNodeID == dialerNodeID {
		if strings.TrimSpace(peerDomain) == "" || peerPort <= 0 {
			cfg.prevNodeID = peerNodeID
			return cfg, true
		}
		cfg.nextAuthMode = "secret"
		cfg.nextDialMode = probeChainDialModeForward
		cfg.nextLinkLayer = probeVirtualRouterRuntimeLinkLayer
		cfg.nextHost = peerDomain
		cfg.nextPort = peerPort
		cfg.nextNodeID = peerNodeID
		cfg.nextPreserveRelayDomain = true
		return cfg, true
	}
	cfg.prevNodeID = peerNodeID
	return cfg, true
}

func probeVirtualRouterRuntimeChainID(rule probeVirtualRouterTopologyRule) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"chain",
		strings.TrimSpace(rule.ID),
	}, "|")))
	return probeVirtualRouterRuntimeChainIDPrefix + hex.EncodeToString(sum[:])[:24]
}

func probeVirtualRouterRuleDialerNodeID(rule probeVirtualRouterTopologyRule) string {
	fromNodeID := normalizeProbeChainNodeID(rule.FromNodeID)
	toNodeID := normalizeProbeChainNodeID(rule.ToNodeID)
	if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
		return ""
	}
	return fromNodeID
}
