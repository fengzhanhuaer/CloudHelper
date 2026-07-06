package main

import (
	"errors"
	"net"
	"strings"
	"time"
)

var errProbeLocalLegacyTunnelRouteRemoved = errors.New("legacy probe link tunnel route has been removed")

type probeLocalTunnelRouteDecision struct {
	Direct          bool
	Reject          bool
	TargetAddr      string
	TargetAddrs     []string
	Group           string
	SelectedChainID string
	TunnelNodeID    string
	FlowID          string
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
	return status.Enabled && strings.EqualFold(strings.TrimSpace(status.Mode), probeLocalProxyModeLegacyTunnel)
}

func decideProbeLocalRouteForTarget(targetAddr string) (probeLocalTunnelRouteDecision, error) {
	return decideProbeLocalRouteForTargetWithTunnelPolicy(targetAddr, isProbeLocalProxyTunnelModeEnabled())
}

func decideProbeLocalRouteForTargetWithTunnelPolicy(targetAddr string, allowTunnel bool) (probeLocalTunnelRouteDecision, error) {
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
	if !allowTunnel {
		return decision, nil
	}

	rewrittenTarget, domainForPolicy, fakeMatched := rewriteProbeLocalRouteTargetForFakeIP(host, port)
	if rewrittenTarget != "" {
		decision.TargetAddr = rewrittenTarget
	}
	if domainForPolicy == "" {
		domainForPolicy = host
	}
	var routeDecision probeLocalDNSRouteDecision
	if parsed := net.ParseIP(domainForPolicy); parsed != nil && !fakeMatched {
		routeDecision = resolveProbeLocalProxyRouteDecisionByIP(parsed.String())
	} else {
		routeDecision = resolveProbeLocalProxyRouteDecisionByDomain(domainForPolicy)
	}

	decision.Group = strings.TrimSpace(routeDecision.Group)
	switch strings.ToLower(strings.TrimSpace(routeDecision.Action)) {
	case "reject":
		decision.Direct = false
		decision.Reject = true
		return decision, &probeLocalRouteRejectError{Group: decision.Group}
	case "tunnel":
		decision.Direct = false
		decision.Reject = false
		decision.SelectedChainID = firstNonEmpty(strings.TrimSpace(routeDecision.SelectedChainID), mustProbeLocalSelectedChainIDFromLegacy(routeDecision.TunnelNodeID))
		decision.TunnelNodeID = formatProbeLocalLegacyTunnelNodeID(decision.SelectedChainID)
		if fakeMatched {
			realIPs := resolveProbeLocalDNSRealIPsForRouteDomain(domainForPolicy, routeDecision)
			if len(realIPs) > 0 {
				decision.TargetAddrs = buildProbeLocalTunnelRouteTargetCandidates(realIPs, port)
				if len(decision.TargetAddrs) > 0 {
					decision.TargetAddr = decision.TargetAddrs[0]
				}
			}
		}
		return decision, errProbeLocalLegacyTunnelRouteRemoved
	default:
		decision.Direct = true
		decision.Reject = false
		decision.SelectedChainID = ""
		decision.TunnelNodeID = ""
		return decision, nil
	}
}

func buildProbeLocalTunnelRouteTargetCandidates(ips []string, port string) []string {
	cleanPort := strings.TrimSpace(port)
	if cleanPort == "" {
		return nil
	}
	seen := map[string]struct{}{}
	targets := make([]string, 0, len(ips))
	for _, ip := range ips {
		cleanIP := strings.TrimSpace(strings.Trim(ip, "[]"))
		if cleanIP == "" || net.ParseIP(cleanIP) == nil {
			continue
		}
		target := net.JoinHostPort(cleanIP, cleanPort)
		key := strings.ToLower(target)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, target)
	}
	return targets
}

func probeLocalTunnelRouteTargetCandidates(route probeLocalTunnelRouteDecision) []string {
	seen := map[string]struct{}{}
	candidates := make([]string, 0, len(route.TargetAddrs)+1)
	for _, target := range append([]string{route.TargetAddr}, route.TargetAddrs...) {
		cleanTarget := strings.TrimSpace(target)
		if cleanTarget == "" {
			continue
		}
		key := strings.ToLower(cleanTarget)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, cleanTarget)
	}
	return candidates
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

func dialProbeLocalRoutedTCP(route probeLocalTunnelRouteDecision) (net.Conn, probeLocalTunnelRouteDecision, error) {
	if route.Reject {
		return nil, route, &probeLocalRouteRejectError{Group: route.Group}
	}
	if !route.Direct {
		return nil, route, errProbeLocalLegacyTunnelRouteRemoved
	}
	if err := ensureProbeLocalDirectBypass(route.TargetAddr); err != nil {
		logProbeWarnf("probe local routed tcp direct bypass failed: target=%s err=%v", route.TargetAddr, err)
	}
	dialer := applyProbeLocalEgressDialer(&net.Dialer{Timeout: 10 * time.Second})
	conn, err := dialer.Dial(probeLocalEgressDialNetwork("tcp", route.TargetAddr), strings.TrimSpace(route.TargetAddr))
	if err != nil {
		return nil, route, err
	}
	tuneProbeChainNetConn(conn)
	return conn, route, nil
}
