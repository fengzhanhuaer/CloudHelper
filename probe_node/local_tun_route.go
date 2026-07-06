package main

import (
	"errors"
	"net"
	"strings"
	"time"
)

var errProbeLocalTunnelRouteUnavailable = errors.New("probe local tunnel route action is unavailable; use virtual router route rules")

type probeLocalTunnelRouteDecision struct {
	Direct          bool
	Reject          bool
	TargetAddr      string
	TargetAddrs     []string
	Group           string
	SelectedRouteID string
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

func isprobeLocalRouteTunnelModeEnabled() bool {
	return false
}

func decideProbeLocalRouteForTarget(targetAddr string) (probeLocalTunnelRouteDecision, error) {
	return decideProbeLocalRouteForTargetWithTunnelPolicy(targetAddr, isprobeLocalRouteTunnelModeEnabled())
}

func decideProbeLocalRouteForTargetWithTunnelPolicy(targetAddr string, allowTunnel bool) (probeLocalTunnelRouteDecision, error) {
	_ = allowTunnel
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
	rewrittenTarget, domainForPolicy, fakeMatched := rewriteProbeLocalRouteTargetForFakeIP(host, port)
	if rewrittenTarget != "" {
		decision.TargetAddr = rewrittenTarget
	}
	if domainForPolicy == "" {
		domainForPolicy = host
	}
	if fakeMatched {
		applyProbeLocalFakeIPDirectTargetCandidates(&decision, domainForPolicy, port)
	}
	return decision, nil
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

func applyProbeLocalFakeIPDirectTargetCandidates(decision *probeLocalTunnelRouteDecision, domain string, port string) {
	if decision == nil {
		return
	}
	routeDecision := probeLocalDNSRouteDecision{Group: firstNonEmpty(strings.TrimSpace(decision.Group), "virtual-router"), Action: "direct"}
	realIPs := resolveProbeLocalDNSRealIPsForRouteDomain(domain, routeDecision)
	if len(realIPs) == 0 {
		return
	}
	targets := buildProbeLocalTunnelRouteTargetCandidates(realIPs, port)
	if len(targets) == 0 {
		return
	}
	decision.TargetAddrs = targets
	decision.TargetAddr = targets[0]
}

func rewriteProbeLocalRouteTargetForFakeIP(host string, port string) (rewrittenTarget string, policyDomain string, fakeMatched bool) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	cleanPort := strings.TrimSpace(port)
	if cleanHost == "" || cleanPort == "" {
		return "", "", false
	}
	if parsed := net.ParseIP(cleanHost); parsed != nil {
		if entry, ok := lookupProbeLocalDNSFakeIPEntry(parsed.String()); ok {
			domain := strings.TrimSpace(strings.ToLower(strings.Trim(entry.Domain, ".")))
			if domain != "" {
				return net.JoinHostPort(domain, cleanPort), domain, true
			}
		}
		if entry, ok := currentProbeVirtualRouterFakeIPEntryByIP(parsed.String()); ok {
			domain := normalizeProbeVirtualRouterDomain(entry.Domain)
			if domain != "" {
				return net.JoinHostPort(domain, cleanPort), domain, true
			}
		}
		return net.JoinHostPort(cleanHost, cleanPort), cleanHost, false
	}
	return net.JoinHostPort(cleanHost, cleanPort), cleanHost, false
}

func dialProbeLocalRoutedTCP(route probeLocalTunnelRouteDecision) (net.Conn, probeLocalTunnelRouteDecision, error) {
	if route.Reject {
		return nil, route, &probeLocalRouteRejectError{Group: route.Group}
	}
	if !route.Direct {
		return nil, route, errProbeLocalTunnelRouteUnavailable
	}
	if err := ensureProbeRouteDirectBypass(route.TargetAddr); err != nil {
		logProbeWarnf("probe local routed tcp direct bypass failed: target=%s err=%v", route.TargetAddr, err)
	}
	dialer := applyProbeRouteEgressDialer(&net.Dialer{Timeout: 10 * time.Second})
	conn, err := dialer.Dial(probeRouteEgressDialNetwork("tcp", route.TargetAddr), strings.TrimSpace(route.TargetAddr))
	if err != nil {
		return nil, route, err
	}
	tuneProbeRouteNetConn(conn)
	return conn, route, nil
}
