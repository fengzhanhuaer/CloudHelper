package main

import "strings"

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

	for _, entry := range stateFile.Groups {
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
			decision.TunnelNodeID = strings.TrimSpace(entry.TunnelNodeID)
		default:
			decision.Action = "direct"
		}
		break
	}
	return decision
}
