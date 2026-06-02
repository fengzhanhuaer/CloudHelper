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
	for _, nodeID := range route {
		if isDeletedProbeNodeID(nodeID) {
			return fmt.Errorf("chain unavailable: deleted probe node %s", normalizeProbeNodeID(nodeID))
		}
	}

	var failures []string
	adminPriv, adminPrivErr := loadAdminPrivateKeyForSigning()
	if adminPrivErr != nil {
		return fmt.Errorf("probe chain auth ticket signing unavailable: %w", adminPrivErr)
	}
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

		nextHost, nextPort, nextLinkLayer, nextDialMode, nextAuthMode, nextErr := resolveProbeLinkChainDispatchNextHop(item, route, i, nodeSettings)
		if nextErr != nil {
			failures = append(failures, fmt.Sprintf("node=%s %v", nodeID, nextErr))
			continue
		}

		prevHost := ""
		prevPort := 0
		prevLinkLayer := ""
		prevDialMode := "none"
		if i > 0 {
			prevNodeID := route[i-1]
			prevNodeSettings := resolveProbeLinkChainNodeSettings(item, prevNodeID)
			resolvedPrevHost := strings.TrimSpace(prevNodeSettings.RelayHost)
			if resolvedPrevHost == "" {
				var err error
				resolvedPrevHost, err = resolveProbeLinkChainNodeDialHost(prevNodeID)
				if err != nil {
					failures = append(failures, fmt.Sprintf("node=%s resolve prev host failed: %v", nodeID, err))
					continue
				}
			}
			prevHost = resolvedPrevHost
			if prevNodeSettings.ExternalPort > 0 {
				prevPort = prevNodeSettings.ExternalPort
			} else {
				failures = append(failures, fmt.Sprintf("node=%s prev hop %s has no external_port in hop_config", nodeID, prevNodeID))
				continue
			}
			prevLinkLayer = prevNodeSettings.LinkLayer
			prevDialMode = prevNodeSettings.DialMode
		}

		authTicket, ticketErr := buildProbeLinkChainAuthTicket(item, adminPriv)
		if ticketErr != nil {
			failures = append(failures, fmt.Sprintf("node=%s auth ticket failed: %v", nodeID, ticketErr))
			continue
		}

		_, err := dispatchProbeChainLinkControl(nodeID, probeChainLinkControlCommand{
			Action:        "apply",
			ChainID:       strings.TrimSpace(item.ChainID),
			ChainType:     strings.TrimSpace(item.ChainType),
			Name:          strings.TrimSpace(item.Name),
			UserID:        strings.TrimSpace(item.UserID),
			UserPublicKey: strings.TrimSpace(item.UserPublicKey),
			LinkSecret:    strings.TrimSpace(item.Secret),
			AuthTicket:    strings.TrimSpace(authTicket),
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
			RequireUserAuth:   true,
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

func resolveProbeLinkChainDispatchNextHop(item probeLinkChainRecord, route []string, index int, nodeSettings probeLinkChainNodeSettings) (host string, port int, linkLayer string, dialMode string, authMode string, err error) {
	authMode = "proxy"
	dialMode = "none"
	if index < 0 || index >= len(route)-1 {
		return "", 0, "", dialMode, authMode, nil
	}
	nextNodeID := route[index+1]
	nextNodeSettings := resolveProbeLinkChainNodeSettings(item, nextNodeID)
	resolvedHost := strings.TrimSpace(nextNodeSettings.RelayHost)
	if resolvedHost == "" {
		resolvedHost, err = resolveProbeLinkChainNodeDialHost(nextNodeID)
		if err != nil {
			return "", 0, "", dialMode, authMode, fmt.Errorf("resolve next host failed: %w", err)
		}
	}
	if nextNodeSettings.ExternalPort <= 0 {
		return "", 0, "", dialMode, authMode, fmt.Errorf("next hop %s has no external_port in hop_config", nextNodeID)
	}
	return resolvedHost, nextNodeSettings.ExternalPort, nextNodeSettings.LinkLayer, nodeSettings.DialMode, "secret", nil
}

func removeProbeLinkChainRecord(item probeLinkChainRecord) error {
	route := buildProbeChainRouteNodes(item)
	if len(route) == 0 {
		return nil
	}
	var failures []string
	for _, nodeID := range route {
		if isDeletedProbeNodeID(nodeID) {
			continue
		}
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
			EntrySide:  strings.TrimSpace(item.EntrySide),
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
	RelayHost    string
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
			RelayHost:    normalizeProbeLinkChainDialHost(hop.RelayHost),
		}
	}
	return probeLinkChainNodeSettings{
		ListenHost:   defaultHost,
		ListenPort:   0,
		ExternalPort: 0,
		LinkLayer:    defaultLayer,
		DialMode:     defaultProbeLinkChainDialMode,
		RelayHost:    "",
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
