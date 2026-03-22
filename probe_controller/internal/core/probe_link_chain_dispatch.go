package core

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func applyProbeLinkChainRecord(item probeLinkChainRecord, controllerBaseURL string) error {
	route := buildProbeChainRouteNodes(item)
	if len(route) == 0 {
		return fmt.Errorf("chain route is empty")
	}

	var failures []string
	for i, nodeID := range route {
		hopPortOverride, nodeLinkLayer := resolveProbeLinkChainHopSettings(item, nodeID)
		role := "relay"
		if len(route) == 1 {
			role = "entry_exit"
		} else if i == 0 {
			role = "entry"
		} else if i == len(route)-1 {
			role = "exit"
		}

		nextHost := strings.TrimSpace(item.EgressHost)
		nextPort := item.EgressPort
		nextAuthMode := "proxy"
		if i < len(route)-1 {
			nextNodeID := route[i+1]
			resolvedHost, err := resolveProbeLinkChainNodeDialHost(nextNodeID)
			if err != nil {
				failures = append(failures, fmt.Sprintf("node=%s resolve next host failed: %v", nodeID, err))
				continue
			}
			nextHost = resolvedHost
			if hopPortOverride > 0 {
				nextPort = hopPortOverride
			} else {
				relayPort, relayErr := resolveProbeLinkChainNodeRelayPort(nextNodeID)
				if relayErr != nil {
					failures = append(failures, fmt.Sprintf("node=%s resolve next relay port failed: %v", nodeID, relayErr))
					continue
				}
				nextPort = relayPort
			}
			nextAuthMode = "secret"
		}

		_, err := dispatchProbeChainLinkControl(nodeID, probeChainLinkControlCommand{
			Action:            "apply",
			ChainID:           strings.TrimSpace(item.ChainID),
			Name:              strings.TrimSpace(item.Name),
			UserID:            strings.TrimSpace(item.UserID),
			UserPublicKey:     strings.TrimSpace(item.UserPublicKey),
			LinkSecret:        strings.TrimSpace(item.Secret),
			Role:              role,
			ListenHost:        normalizeProbeLinkChainListenHost(item.ListenHost),
			ListenPort:        item.ListenPort,
			LinkLayer:         nodeLinkLayer,
			NextHost:          strings.TrimSpace(nextHost),
			NextPort:          nextPort,
			RequireUserAuth:   i == 0,
			NextAuthMode:      nextAuthMode,
			ControllerBaseURL: strings.TrimSpace(controllerBaseURL),
		})
		if err != nil {
			failures = append(failures, fmt.Sprintf("node=%s apply failed: %v", nodeID, err))
		}
	}

	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func removeProbeLinkChainRecord(item probeLinkChainRecord) error {
	route := buildProbeChainRouteNodes(item)
	if len(route) == 0 {
		return nil
	}
	var failures []string
	for _, nodeID := range route {
		_, err := dispatchProbeChainLinkControl(nodeID, probeChainLinkControlCommand{
			Action:  "remove",
			ChainID: strings.TrimSpace(item.ChainID),
		})
		if err != nil {
			failures = append(failures, fmt.Sprintf("node=%s remove failed: %v", nodeID, err))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func resolveProbeLinkChainHopSettings(item probeLinkChainRecord, nodeID string) (int, string) {
	defaultLayer := normalizeProbeLinkChainLinkLayer(item.LinkLayer)
	targetNodeID := normalizeProbeNodeID(nodeID)
	for _, hop := range item.HopConfigs {
		if hop.NodeNo <= 0 {
			continue
		}
		hopID := normalizeProbeNodeID(strconv.Itoa(hop.NodeNo))
		if hopID == "" || hopID != targetNodeID {
			continue
		}
		port := hop.ListenPort
		if port < 0 || port > 65535 {
			port = 0
		}
		layer := normalizeProbeLinkChainLinkLayer(hop.LinkLayer)
		return port, layer
	}
	return 0, defaultLayer
}

func resolveProbeLinkChainNodeDialHost(nodeID string) (string, error) {
	node, ok := getProbeNodeByID(normalizeProbeNodeID(nodeID))
	if !ok {
		return "", fmt.Errorf("probe node not found: %s", nodeID)
	}
	candidates := []string{
		node.PublicHost,
		node.DDNS,
		node.ServiceHost,
	}
	for _, raw := range candidates {
		if host := normalizeProbeLinkChainDialHost(raw); host != "" {
			return host, nil
		}
	}
	if runtime, ok := getProbeRuntime(nodeID); ok {
		for _, ip := range runtime.IPv4 {
			if host := normalizeProbeLinkChainDialHost(ip); host != "" {
				return host, nil
			}
		}
		for _, ip := range runtime.IPv6 {
			if host := normalizeProbeLinkChainDialHost(ip); host != "" {
				return host, nil
			}
		}
	}
	return "", fmt.Errorf("no dial host configured for node %s", nodeID)
}

func resolveProbeLinkChainNodeRelayPort(nodeID string) (int, error) {
	node, ok := getProbeNodeByID(normalizeProbeNodeID(nodeID))
	if !ok {
		return 0, fmt.Errorf("probe node not found: %s", nodeID)
	}
	if node.PublicPort > 0 && node.PublicPort <= 65535 {
		return node.PublicPort, nil
	}
	if node.ServicePort > 0 && node.ServicePort <= 65535 {
		return node.ServicePort, nil
	}
	return 0, fmt.Errorf("no relay port configured for node %s", nodeID)
}

func normalizeProbeLinkChainDialHost(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil {
			value = strings.TrimSpace(parsed.Host)
		}
	}
	value = strings.TrimSpace(strings.Split(value, "/")[0])
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return strings.TrimSpace(strings.Trim(host, "[]"))
	}
	return strings.TrimSpace(strings.Trim(value, "[]"))
}
