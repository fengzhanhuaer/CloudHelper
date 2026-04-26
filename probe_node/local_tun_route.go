package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

type probeLocalTunnelRouteDecision struct {
	Direct       bool
	Reject       bool
	TargetAddr   string
	Group        string
	TunnelNodeID string
}

type probeLocalRouteRejectError struct {
	Group string
}

func (e *probeLocalRouteRejectError) Error() string {
	if e == nil {
		return "route rejected"
	}
	group := strings.TrimSpace(e.Group)
	if group == "" {
		return "route rejected"
	}
	return "route rejected by group: " + group
}

func isProbeLocalProxyTunnelModeEnabled() bool {
	status := probeLocalControl.proxyStatus()
	return status.Enabled && strings.EqualFold(strings.TrimSpace(status.Mode), probeLocalProxyModeTUN)
}

func decideProbeLocalRouteForTarget(targetAddr string) (probeLocalTunnelRouteDecision, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return probeLocalTunnelRouteDecision{}, err
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	port = strings.TrimSpace(port)
	if host == "" || port == "" {
		return probeLocalTunnelRouteDecision{}, errors.New("invalid target address")
	}
	decision := probeLocalTunnelRouteDecision{
		Direct:     true,
		Reject:     false,
		TargetAddr: net.JoinHostPort(host, port),
		Group:      "fallback",
	}
	if !isProbeLocalProxyTunnelModeEnabled() {
		return decision, nil
	}

	domainForPolicy := host
	if parsed := net.ParseIP(host); parsed != nil {
		entry, ok := lookupProbeLocalDNSFakeIPEntry(parsed.String())
		if !ok {
			return decision, nil
		}
		domainForPolicy = strings.TrimSpace(entry.Domain)
	}

	dnsDecision := resolveProbeLocalProxyRouteDecisionByDomain(domainForPolicy)
	decision.Group = strings.TrimSpace(dnsDecision.Group)
	switch strings.ToLower(strings.TrimSpace(dnsDecision.Action)) {
	case "reject":
		decision.Direct = false
		decision.Reject = true
		return decision, &probeLocalRouteRejectError{Group: decision.Group}
	case "tunnel":
		decision.Direct = false
		decision.Reject = false
		decision.TunnelNodeID = strings.TrimSpace(dnsDecision.TunnelNodeID)
		if decision.TunnelNodeID == "" {
			return probeLocalTunnelRouteDecision{}, errors.New("tunnel route missing tunnel_node_id")
		}
		return decision, nil
	default:
		decision.Direct = true
		decision.Reject = false
		decision.TunnelNodeID = ""
		return decision, nil
	}
}

func openProbeLocalTunnelConn(network, targetAddr, tunnelNodeID string) (net.Conn, error) {
	cleanNetwork := strings.ToLower(strings.TrimSpace(network))
	if cleanNetwork == "" {
		cleanNetwork = "tcp"
	}
	normalizedNodeID, chainID, err := normalizeProbeLocalTunnelNodeID(tunnelNodeID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(chainID) == "" {
		return nil, errors.New("empty tunnel chain id")
	}
	runtime := getProbeChainRuntime(chainID)
	if runtime == nil {
		return nil, fmt.Errorf("tunnel chain runtime not found: %s", normalizedNodeID)
	}
	return openProbeChainPortForwardStream(runtime, "", cleanNetwork, strings.TrimSpace(targetAddr))
}

func dialProbeLocalRoutedTCP(route probeLocalTunnelRouteDecision) (net.Conn, error) {
	if route.Reject {
		return nil, &probeLocalRouteRejectError{Group: route.Group}
	}
	if route.Direct {
		return net.DialTimeout("tcp", strings.TrimSpace(route.TargetAddr), 10*time.Second)
	}
	return openProbeLocalTunnelConn("tcp", route.TargetAddr, route.TunnelNodeID)
}
