package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type probeVRouteProxyTargetDecision struct {
	OriginalTarget string
	TargetAddr     string
	Host           string
	Port           string
	Domain         string
	FakeIP         string
	Action         string
	RuleID         string
	ExitNodeID     string
	Path           []string
}

func (d probeVRouteProxyTargetDecision) Direct() bool {
	return d.Action == "direct" || (d.Action == "probe_exit" && len(d.Path) == 1)
}

func decideProbeVRouteProxyTarget(targetAddr string) (probeVRouteProxyTargetDecision, error) {
	original := strings.TrimSpace(targetAddr)
	host, port, err := net.SplitHostPort(original)
	if err != nil {
		return probeVRouteProxyTargetDecision{}, fmt.Errorf("invalid proxy target: %w", err)
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	port = strings.TrimSpace(port)
	if host == "" || port == "" {
		return probeVRouteProxyTargetDecision{}, errors.New("proxy target host and port are required")
	}
	decision := probeVRouteProxyTargetDecision{
		OriginalTarget: original,
		Host:           host,
		Port:           port,
		Action:         "direct",
	}

	var fakeEntry probeVirtualRouterFakeIPEntry
	if parsed := net.ParseIP(host); parsed != nil && probeVirtualRouterIPInCurrentFakeCIDR(parsed.String()) {
		decision.FakeIP = parsed.String()
		fakeEntry, err = resolveProbeVRouteProxyFakeIPEntry(decision.FakeIP)
		if err != nil {
			return probeVRouteProxyTargetDecision{}, err
		}
		decision.Domain = normalizeProbeVirtualRouterDomain(fakeEntry.Domain)
		if decision.Domain == "" {
			return probeVRouteProxyTargetDecision{}, fmt.Errorf("fake ip mapping has no domain: %s", decision.FakeIP)
		}
		decision.Host = decision.Domain
	}
	if decision.Domain == "" && net.ParseIP(decision.Host) == nil {
		decision.Domain = normalizeProbeVirtualRouterDomain(decision.Host)
		if decision.Domain != "" {
			decision.Host = decision.Domain
		}
	}
	decision.TargetAddr = net.JoinHostPort(decision.Host, decision.Port)

	var rule probeVirtualRouterRouteRule
	var matched bool
	if decision.Domain != "" {
		rule, matched = currentProbeVirtualRouterRouteRuleForDomain(decision.Domain)
	} else {
		rule, matched = currentProbeVirtualRouterRouteRuleForIP(decision.Host)
	}
	if matched {
		decision.Action = sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID)
		decision.RuleID = strings.TrimSpace(rule.ID)
		decision.ExitNodeID = normalizeProbeRouteNodeID(rule.ExitNodeID)
	} else if decision.FakeIP != "" {
		decision.Action = sanitizeProbeVirtualRouterRouteRuleAction(fakeEntry.Action, fakeEntry.ExitNodeID)
		decision.RuleID = strings.TrimSpace(fakeEntry.RuleID)
		decision.ExitNodeID = normalizeProbeRouteNodeID(fakeEntry.ExitNodeID)
	}
	if decision.Action == "" {
		decision.Action = "direct"
	}
	switch decision.Action {
	case "reject":
		return decision, &probeLocalRouteRejectError{Group: firstNonEmpty(decision.RuleID, "virtual-router")}
	case "direct":
		return decision, nil
	case "probe_exit":
		if decision.ExitNodeID == "" {
			return decision, errors.New("proxy exit node is missing")
		}
		localNodeID := currentProbeVirtualRouterLocalNodeID()
		if localNodeID == "" {
			return decision, errors.New("local virtual router node id is empty")
		}
		decision.Path = currentProbeVirtualRouterPathBetweenNodes(localNodeID, decision.ExitNodeID)
		if len(decision.Path) == 0 {
			return decision, fmt.Errorf("virtual router proxy path is unavailable: %s>%s", localNodeID, decision.ExitNodeID)
		}
		return decision, nil
	default:
		return decision, fmt.Errorf("unsupported proxy route action: %s", decision.Action)
	}
}

func resolveProbeVRouteProxyFakeIPEntry(fakeIP string) (probeVirtualRouterFakeIPEntry, error) {
	cleanIP := ""
	if parsed := net.ParseIP(strings.TrimSpace(fakeIP)).To4(); parsed != nil {
		cleanIP = parsed.String()
	}
	if cleanIP == "" || !probeVirtualRouterIPInCurrentFakeCIDR(cleanIP) {
		return probeVirtualRouterFakeIPEntry{}, fmt.Errorf("invalid virtual router fake ip: %s", fakeIP)
	}
	if item, ok := currentProbeVirtualRouterFakeIPEntryByIP(cleanIP); ok {
		return item, nil
	}
	identity, controllerBaseURL, ok := currentProbeVirtualRouterController()
	if !ok {
		return probeVirtualRouterFakeIPEntry{}, fmt.Errorf("fake ip mapping unavailable and controller is not configured: %s", cleanIP)
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeRouteConfigSyncFetchTimeout)
	defer cancel()
	item, err := probeRequestRouteFakeIPByIP(ctx, controllerBaseURL, identity, cleanIP)
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, fmt.Errorf("refresh fake ip mapping failed: %w", err)
	}
	if !applyProbeVirtualRouterFakeIPEntry(item) {
		return probeVirtualRouterFakeIPEntry{}, fmt.Errorf("controller returned invalid fake ip mapping: %s", cleanIP)
	}
	if item, ok = currentProbeVirtualRouterFakeIPEntryByIP(cleanIP); !ok {
		return probeVirtualRouterFakeIPEntry{}, fmt.Errorf("fake ip mapping remained unavailable after refresh: %s", cleanIP)
	}
	return item, nil
}

func dialProbeVRouteProxyDirectTCP(targetAddr string) (net.Conn, error) {
	route := probeLocalTunnelRouteDecision{
		Direct:     true,
		TargetAddr: strings.TrimSpace(targetAddr),
		Group:      "virtual-router-proxy",
	}
	conn, _, err := dialProbeLocalRoutedTCP(route)
	return conn, err
}

func dialProbeVRouteProxyDirectUDP(targetAddr string) (net.Conn, error) {
	target := strings.TrimSpace(targetAddr)
	if target == "" {
		return nil, errors.New("udp target is required")
	}
	if err := ensureProbeRouteDirectBypass(target); err != nil {
		logProbeWarnf("probe vroute proxy udp direct bypass failed: target=%s err=%v", target, err)
	}
	dialer := applyProbeRouteEgressDialer(&net.Dialer{Timeout: 10 * time.Second})
	conn, err := dialer.DialContext(context.Background(), probeRouteEgressDialNetwork("udp", target), target)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func dialProbeVRouteProxyExitTCP(targetAddr string) (net.Conn, error) {
	resolved, err := resolveProbeVRouteProxyExitTarget(targetAddr)
	if err != nil {
		return nil, err
	}
	return dialProbeVRouteProxyDirectTCP(resolved)
}

func dialProbeVRouteProxyExitUDP(targetAddr string) (net.Conn, error) {
	resolved, err := resolveProbeVRouteProxyExitTarget(targetAddr)
	if err != nil {
		return nil, err
	}
	return dialProbeVRouteProxyDirectUDP(resolved)
}

func authorizeProbeVRouteProxyExitTarget(targetAddr string, path []string) error {
	cleanPath := cleanProbeVirtualRouterPath(path)
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if len(cleanPath) < 2 || localNodeID == "" || cleanPath[len(cleanPath)-1] != localNodeID {
		return errors.New("remote proxy target path is unauthorized")
	}
	decision, err := decideProbeVRouteProxyTarget(targetAddr)
	if err != nil {
		return err
	}
	if decision.Action != "probe_exit" || normalizeProbeRouteNodeID(decision.ExitNodeID) != localNodeID {
		return errors.New("remote proxy target is not assigned to this exit node")
	}
	_, err = resolveProbeVRouteProxyExitTarget(decision.TargetAddr)
	return err
}

func resolveProbeVRouteProxyExitTarget(targetAddr string) (string, error) {
	target := strings.TrimSpace(targetAddr)
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", fmt.Errorf("invalid proxy exit target: %w", err)
	}
	portNumber, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || portNumber <= 0 || portNumber > 65535 {
		return "", errors.New("invalid proxy exit target port")
	}
	if allowProbeVRouteProxyPrivateTargets() {
		return target, nil
	}
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return "", errors.New("proxy exit target host is required")
	}
	if ip := net.ParseIP(cleanHost); ip != nil {
		if probeVRouteProxyExitIPDenied(ip) {
			return "", errors.New("proxy exit target address is not allowed")
		}
		return net.JoinHostPort(ip.String(), port), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, cleanHost)
	if err != nil {
		return "", fmt.Errorf("resolve proxy exit target failed: %w", err)
	}
	if len(addresses) == 0 {
		return "", errors.New("resolve proxy exit target failed: no addresses")
	}
	for _, address := range addresses {
		if address.IP == nil || probeVRouteProxyExitIPDenied(address.IP) {
			continue
		}
		return net.JoinHostPort(address.IP.String(), port), nil
	}
	return "", errors.New("proxy exit target resolved only to disallowed addresses")
}

func probeVRouteProxyExitIPDenied(ip net.IP) bool {
	return ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast()
}

func allowProbeVRouteProxyPrivateTargets() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("PROBE_VROUTE_PROXY_ALLOW_PRIVATE_TARGETS")))
	return value == "1" || value == "true" || value == "yes"
}
