package main

import (
	"net"
	"strings"
)

func resolveProbeLocalProxyRouteDecisionByDomain(domain string) probeLocalDNSRouteDecision {
	decision := probeLocalDNSRouteDecision{Group: "fallback", Action: "direct"}
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" {
		return decision
	}
	groupFile, err := loadProbeLocalProxyGroupFile()
	if err != nil {
		return decision
	}
	stateFile, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return decision
	}

	matchGroup := "fallback"
	for _, item := range groupFile.Groups {
		groupName := strings.TrimSpace(item.Group)
		if groupName == "" {
			continue
		}
		if probeLocalDNSDomainMatchesRules(cleanDomain, item.Rules) {
			matchGroup = groupName
			break
		}
	}
	decision.Group = matchGroup

	applyProbeLocalProxyStateDecision(&decision, stateFile.Groups, matchGroup)
	return decision
}

func resolveProbeLocalProxyRouteDecisionByIP(ipText string) probeLocalDNSRouteDecision {
	decision := probeLocalDNSRouteDecision{Group: "fallback", Action: "direct"}
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(ipText, "[]")))
	if ip == nil {
		return decision
	}
	groupFile, err := loadProbeLocalProxyGroupFile()
	if err != nil {
		return decision
	}
	stateFile, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return decision
	}

	matchGroup := "fallback"
	for _, item := range groupFile.Groups {
		groupName := strings.TrimSpace(item.Group)
		if groupName == "" {
			continue
		}
		if probeLocalIPMatchesCIDRRules(ip, item.Rules) {
			matchGroup = groupName
			break
		}
	}
	decision.Group = matchGroup

	applyProbeLocalProxyStateDecision(&decision, stateFile.Groups, matchGroup)
	return decision
}

func probeLocalTunnelCIDRRules() []string {
	groupFile, err := loadProbeLocalProxyGroupFile()
	if err != nil {
		return nil
	}
	stateFile, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return nil
	}
	tunnelGroups := make(map[string]struct{}, len(stateFile.Groups))
	for _, entry := range stateFile.Groups {
		if !strings.EqualFold(strings.TrimSpace(entry.Action), "tunnel") {
			continue
		}
		if strings.TrimSpace(firstNonEmpty(entry.SelectedChainID, entry.TunnelNodeID)) == "" {
			continue
		}
		group := strings.TrimSpace(entry.Group)
		if group == "" {
			continue
		}
		tunnelGroups[strings.ToLower(group)] = struct{}{}
	}
	if len(tunnelGroups) == 0 {
		return nil
	}
	out := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, item := range groupFile.Groups {
		groupName := strings.TrimSpace(item.Group)
		if _, ok := tunnelGroups[strings.ToLower(groupName)]; !ok {
			continue
		}
		for _, rule := range item.Rules {
			key, value, ok := splitProbeLocalProxyRule(rule)
			if !ok || key != "cidr" {
				continue
			}
			ip, network, err := net.ParseCIDR(value)
			if err != nil || network == nil || ip == nil || ip.To4() == nil || network.IP.To4() == nil {
				continue
			}
			cidr := network.String()
			if _, ok := seen[cidr]; ok {
				continue
			}
			seen[cidr] = struct{}{}
			out = append(out, cidr)
		}
	}
	return out
}

func probeLocalIPMatchesCIDRRules(ip net.IP, rules []string) bool {
	if ip == nil {
		return false
	}
	for _, rule := range rules {
		key, value, ok := splitProbeLocalProxyRule(rule)
		if !ok || key != "cidr" {
			continue
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil || network == nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func splitProbeLocalProxyRule(rule string) (key string, value string, ok bool) {
	trimmed := strings.TrimSpace(rule)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(parts[0]))
	value = strings.TrimSpace(parts[1])
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func applyProbeLocalProxyStateDecision(decision *probeLocalDNSRouteDecision, entries []probeLocalProxyStateGroupEntry, matchGroup string) {
	if decision == nil {
		return
	}
	for _, entry := range entries {
		if !strings.EqualFold(strings.TrimSpace(entry.Group), matchGroup) {
			continue
		}
		action := strings.ToLower(strings.TrimSpace(entry.Action))
		switch action {
		case "reject":
			decision.Action = "reject"
			decision.Reject = true
		case "tunnel":
			decision.Action = "tunnel"
			decision.SelectedChainID = firstNonEmpty(strings.TrimSpace(entry.SelectedChainID), mustProbeLocalSelectedChainIDFromLegacy(entry.TunnelNodeID))
			decision.TunnelNodeID = formatProbeLocalLegacyTunnelNodeID(decision.SelectedChainID)
		default:
			decision.Action = "direct"
		}
		break
	}
}
