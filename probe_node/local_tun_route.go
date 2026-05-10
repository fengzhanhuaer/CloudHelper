package main

import (
	"errors"
	"net"
	"strings"
	"time"
)

type probeLocalTunnelRouteDecision struct {
	Direct          bool
	Reject          bool
	TargetAddr      string
	Group           string
	SelectedChainID string
	TunnelNodeID    string
	GroupRuntime    *probeLocalTUNGroupRuntime
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

	rewrittenTarget, domainForPolicy, fakeMatched := rewriteProbeLocalRouteTargetForFakeIP(host, port)
	if rewrittenTarget != "" {
		decision.TargetAddr = rewrittenTarget
	}
	if domainForPolicy == "" {
		domainForPolicy = host
	}
	if parsed := net.ParseIP(domainForPolicy); parsed != nil && !fakeMatched {
		return decision, nil
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
		decision.SelectedChainID = firstNonEmpty(strings.TrimSpace(dnsDecision.SelectedChainID), mustProbeLocalSelectedChainIDFromLegacy(dnsDecision.TunnelNodeID))
		if decision.SelectedChainID == "" {
			return probeLocalTunnelRouteDecision{}, errors.New("tunnel route missing selected_chain_id")
		}
		decision.TunnelNodeID = formatProbeLocalLegacyTunnelNodeID(decision.SelectedChainID)
		groupRuntime, runtimeErr := ensureProbeLocalTUNGroupRuntime(decision.Group, decision.SelectedChainID)
		if runtimeErr != nil {
			return probeLocalTunnelRouteDecision{}, runtimeErr
		}
		decision.GroupRuntime = groupRuntime
		return decision, nil
	default:
		decision.Direct = true
		decision.Reject = false
		decision.SelectedChainID = ""
		decision.TunnelNodeID = ""
		decision.GroupRuntime = nil
		return decision, nil
	}
}

func rewriteProbeLocalRouteTargetForFakeIP(host string, port string) (rewrittenTarget string, policyDomain string, fakeMatched bool) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	cleanPort := strings.TrimSpace(port)
	if cleanHost == "" || cleanPort == "" {
		return "", "", false
	}
	if parsed := net.ParseIP(cleanHost); parsed != nil {
		entry, ok := lookupProbeLocalDNSFakeIPEntry(parsed.String())
		if !ok {
			return net.JoinHostPort(cleanHost, cleanPort), cleanHost, false
		}
		domain := strings.TrimSpace(strings.ToLower(strings.Trim(entry.Domain, ".")))
		if domain == "" {
			return net.JoinHostPort(cleanHost, cleanPort), cleanHost, false
		}
		return net.JoinHostPort(domain, cleanPort), domain, true
	}
	return net.JoinHostPort(cleanHost, cleanPort), cleanHost, false
}

func openProbeLocalTunnelConn(network, targetAddr, selectedChainID string) (net.Conn, error) {
	return openProbeLocalTunnelConnWithAssociation(network, targetAddr, selectedChainID, nil)
}

func openProbeLocalTunnelConnWithAssociation(network, targetAddr, selectedChainID string, associationV2 *probeChainAssociationV2Meta) (net.Conn, error) {
	chainID, err := normalizeProbeLocalSelectedChainID(selectedChainID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(chainID) == "" {
		return nil, errors.New("empty selected chain id")
	}
	groupRuntime, err := ensureProbeLocalTUNGroupRuntime("@selected_chain:"+chainID, chainID)
	if err != nil {
		return nil, err
	}
	return openProbeLocalTunnelConnWithGroupRuntime(network, targetAddr, groupRuntime, associationV2)
}

func openProbeLocalTunnelConnWithGroupRuntime(network, targetAddr string, groupRuntime *probeLocalTUNGroupRuntime, associationV2 *probeChainAssociationV2Meta) (net.Conn, error) {
	cleanNetwork := strings.ToLower(strings.TrimSpace(network))
	if cleanNetwork == "" {
		cleanNetwork = "tcp"
	}
	if groupRuntime == nil {
		return nil, errors.New("group runtime is nil")
	}
	return groupRuntime.openStream(cleanNetwork, strings.TrimSpace(targetAddr), associationV2)
}

func dialProbeLocalRoutedTCP(route probeLocalTunnelRouteDecision) (net.Conn, error) {
	if route.Reject {
		return nil, &probeLocalRouteRejectError{Group: route.Group}
	}
	if route.Direct {
		return net.DialTimeout("tcp", strings.TrimSpace(route.TargetAddr), 10*time.Second)
	}
	return openProbeLocalTunnelConnWithGroupRuntime("tcp", route.TargetAddr, route.GroupRuntime, nil)
}
