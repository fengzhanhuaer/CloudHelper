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
		nodeSettings := resolveProbeLinkChainNodeSettings(item, nodeID)
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
		nextLinkLayer := ""
		nextDialMode := "none"
		if i < len(route)-1 {
			nextNodeID := route[i+1]
			resolvedHost, err := resolveProbeLinkChainNodeDialHost(nextNodeID)
			if err != nil {
				failures = append(failures, fmt.Sprintf("node=%s resolve next host failed: %v", nodeID, err))
				continue
			}
			nextHost = resolvedHost
			nextNodeSettings := resolveProbeLinkChainNodeSettings(item, nextNodeID)
			if nextNodeSettings.ExternalPort > 0 {
				nextPort = nextNodeSettings.ExternalPort
			} else {
				failures = append(failures, fmt.Sprintf("node=%s next hop %s has no external_port in hop_config", nodeID, nextNodeID))
				continue
			}
			nextLinkLayer = nextNodeSettings.LinkLayer
			nextDialMode = nodeSettings.DialMode
			nextAuthMode = "secret"
		}

		prevHost := ""
		prevPort := 0
		prevLinkLayer := ""
		prevDialMode := "none"
		if i > 0 {
			prevNodeID := route[i-1]
			resolvedPrevHost, err := resolveProbeLinkChainNodeDialHost(prevNodeID)
			if err != nil {
				failures = append(failures, fmt.Sprintf("node=%s resolve prev host failed: %v", nodeID, err))
				continue
			}
			prevHost = resolvedPrevHost
			prevNodeSettings := resolveProbeLinkChainNodeSettings(item, prevNodeID)
			if prevNodeSettings.ExternalPort > 0 {
				prevPort = prevNodeSettings.ExternalPort
			} else {
				failures = append(failures, fmt.Sprintf("node=%s prev hop %s has no external_port in hop_config", nodeID, prevNodeID))
				continue
			}
			prevLinkLayer = prevNodeSettings.LinkLayer
			prevDialMode = prevNodeSettings.DialMode
		}

		_, err := dispatchProbeChainLinkControl(nodeID, probeChainLinkControlCommand{
			Action:        "apply",
			ChainID:       strings.TrimSpace(item.ChainID),
			Name:          strings.TrimSpace(item.Name),
			UserID:        strings.TrimSpace(item.UserID),
			UserPublicKey: strings.TrimSpace(item.UserPublicKey),
			LinkSecret:    strings.TrimSpace(item.Secret),
			Role:          role,
			ListenHost:    nodeSettings.ListenHost,
			ListenPort: func() int {
				if nodeSettings.ListenPort > 0 {
					return nodeSettings.ListenPort
				}
				return item.ListenPort
			}(),
			LinkLayer:         nodeSettings.LinkLayer,
			NextLinkLayer:     strings.TrimSpace(nextLinkLayer),
			NextDialMode:      strings.TrimSpace(nextDialMode),
			NextHost:          strings.TrimSpace(nextHost),
			NextPort:          nextPort,
			PrevHost:          strings.TrimSpace(prevHost),
			PrevPort:          prevPort,
			PrevLinkLayer:     strings.TrimSpace(prevLinkLayer),
			PrevDialMode:      strings.TrimSpace(prevDialMode),
			PortForwards:      buildProbeChainPortForwardCommands(item.PortForwards),
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

func buildProbeChainPortForwardCommands(values []probeLinkChainPortForwardConfig) []probeChainPortForwardCommand {
	if len(values) == 0 {
		return []probeChainPortForwardCommand{}
	}
	out := make([]probeChainPortForwardCommand, 0, len(values))
	for _, item := range values {
		out = append(out, probeChainPortForwardCommand{
			ID:         strings.TrimSpace(item.ID),
			Name:       strings.TrimSpace(item.Name),
			ListenHost: strings.TrimSpace(item.ListenHost),
			ListenPort: item.ListenPort,
			TargetHost: strings.TrimSpace(item.TargetHost),
			TargetPort: item.TargetPort,
			Network:    strings.TrimSpace(item.Network),
			Enabled:    item.Enabled,
		})
	}
	return out
}

type probeLinkChainNodeSettings struct {
	ListenHost   string
	ListenPort   int
	ExternalPort int
	LinkLayer    string
	DialMode     string
}

func resolveProbeLinkChainNodeSettings(item probeLinkChainRecord, nodeID string) probeLinkChainNodeSettings {
	defaultHost := normalizeProbeLinkChainListenHost(item.ListenHost)
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
		listenPort := hop.ListenPort
		if listenPort < 0 || listenPort > 65535 {
			listenPort = 0
		}
		externalPort := hop.ExternalPort
		if externalPort < 0 || externalPort > 65535 {
			externalPort = 0
		}
		listenHost := strings.TrimSpace(hop.ListenHost)
		if listenHost == "" {
			listenHost = defaultHost
		}
		layer := normalizeProbeLinkChainLinkLayer(hop.LinkLayer)
		dialMode := normalizeProbeLinkChainDialMode(hop.DialMode)
		return probeLinkChainNodeSettings{
			ListenHost:   normalizeProbeLinkChainListenHost(listenHost),
			ListenPort:   listenPort,
			ExternalPort: externalPort,
			LinkLayer:    layer,
			DialMode:     dialMode,
		}
	}
	return probeLinkChainNodeSettings{
		ListenHost:   defaultHost,
		ListenPort:   0,
		ExternalPort: 0,
		LinkLayer:    defaultLayer,
		DialMode:     defaultProbeLinkChainDialMode,
	}
}

func resolveProbeLinkChainNodeDialHost(nodeID string) (string, error) {
	relayHosts := buildNodeRelayHostMap()
	if host, exists := relayHosts[normalizeProbeNodeID(nodeID)]; exists {
		if h := normalizeProbeLinkChainDialHost(host); h != "" {
			return h, nil
		}
	}

	node, ok := getProbeNodeByID(normalizeProbeNodeID(nodeID))
	if !ok {
		return "", fmt.Errorf("probe node not found: %s", nodeID)
	}
	candidates := []string{
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
