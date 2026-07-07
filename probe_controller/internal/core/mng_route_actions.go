package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type mngProbeVirtualRouterCFDomain struct {
	NodeID      string `json:"node_id"`
	Domain      string `json:"domain"`
	Source      string `json:"source,omitempty"`
	ServicePort int    `json:"service_port,omitempty"`
}

func getMngProbeVirtualRouterConfig() (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	ensureProbeVirtualRouterStoredAuthFields()
	ProbeRouteConfigStore.mu.RLock()
	config := normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter)
	config.FakeIPLibrary = normalizeProbeVirtualRouterFakeIPLibrary(ProbeRouteConfigStore.data.VirtualRouterFakeIP)
	ProbeRouteConfigStore.mu.RUnlock()
	config = enrichProbeVirtualRouterAuthTickets(ensureProbeVirtualRouterAuthFields(ensureProbeVirtualRouterProbeIPsForKnownNodes(config)))
	cfDomains := listMngProbeVirtualRouterCFDomains()
	return map[string]interface{}{
		"item":       config,
		"node_ids":   listProbeVirtualRouterKnownNodeIDs(),
		"pool_size":  probeVirtualRouterProbeIPPoolSize,
		"cf_domains": cfDomains,
	}, nil
}

func upsertMngProbeVirtualRouterConfig(payload json.RawMessage, controllerBaseURL string) (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	var req probeVirtualRouterConfig
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload")
	}
	req.RouteRules = nil
	ProbeRouteConfigStore.mu.RLock()
	current := normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter)
	ProbeRouteConfigStore.mu.RUnlock()
	req.TopologyRules = preserveProbeVirtualRouterTopologyRuleIdentity(req.TopologyRules, current.TopologyRules)
	config, err := validateAndNormalizeProbeVirtualRouterConfig(req)
	if err != nil {
		return nil, err
	}
	config.RouteRules = current.RouteRules
	cfDomains := listMngProbeVirtualRouterCFDomains()
	config = enrichProbeVirtualRouterAuthTickets(ensureProbeVirtualRouterAuthFields(config))
	ProbeRouteConfigStore.mu.Lock()
	ProbeRouteConfigStore.data.VirtualRouter = config
	ProbeRouteConfigStore.mu.Unlock()
	if err := ProbeRouteConfigStore.Save(); err != nil {
		return nil, err
	}
	syncResult := dispatchProbeRouteConfigSyncToKnownNodes(controllerBaseURL)
	return map[string]interface{}{
		"ok":         true,
		"item":       config,
		"sync":       syncResult,
		"cf_domains": cfDomains,
	}, nil
}

func listMngProbeVirtualRouterCFDomains() []mngProbeVirtualRouterCFDomain {
	zoneName := normalizeCloudflareZoneName(getCloudflareZone().ZoneName)
	if zoneName == "" {
		return []mngProbeVirtualRouterCFDomain{}
	}
	out := make([]mngProbeVirtualRouterCFDomain, 0)
	for _, nodeID := range listProbeVirtualRouterKnownNodeIDs() {
		node, ok := getProbeNodeByID(nodeID)
		if !ok || node.NodeNo <= 0 {
			continue
		}
		domain := strings.TrimSpace(strings.ToLower(buildCloudflareCopilotCandidateDomain(node.NodeNo, zoneName)))
		if domain == "" {
			continue
		}
		out = append(out, mngProbeVirtualRouterCFDomain{
			NodeID:      normalizeProbeNodeID(nodeID),
			Domain:      domain,
			Source:      "cloudflare",
			ServicePort: 443,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		return out[i].Domain < out[j].Domain
	})
	return out
}

func getMngProbeVirtualRouterRouteRules() (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	ProbeRouteConfigStore.mu.RLock()
	config := normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter)
	config.FakeIPLibrary = normalizeProbeVirtualRouterFakeIPLibrary(ProbeRouteConfigStore.data.VirtualRouterFakeIP)
	ProbeRouteConfigStore.mu.RUnlock()
	return map[string]interface{}{
		"items": config.RouteRules,
	}, nil
}

func upsertMngProbeVirtualRouterRouteRules(payload json.RawMessage, controllerBaseURL string) (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	var req struct {
		Items []probeVirtualRouterRouteRule `json:"items"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload")
	}
	for index, item := range req.Items {
		if strings.TrimSpace(item.Name) == "" {
			return nil, fmt.Errorf("route_rules[%d].name is required", index)
		}
		action := normalizeProbeVirtualRouterRouteRuleAction(item.Action, item.ExitNodeID)
		if action == "" {
			return nil, fmt.Errorf("route_rules[%d].action is invalid", index)
		}
		if action == probeVirtualRouterRouteRuleActionExit {
			exitNodeID := normalizeProbeNodeID(item.ExitNodeID)
			if exitNodeID == "" {
				return nil, fmt.Errorf("route_rules[%d].exit_node_id is required", index)
			}
			if !isProbeVirtualRouterKnownNodeID(exitNodeID) {
				return nil, fmt.Errorf("route_rules[%d].exit_node_id is unknown", index)
			}
		}
		if len(item.Entries) > probeVirtualRouterMaxRouteRuleEntries {
			return nil, fmt.Errorf("route_rules[%d].entries exceeded limit (%d)", index, probeVirtualRouterMaxRouteRuleEntries)
		}
		for entryIndex, entry := range item.Entries {
			if _, ok := normalizeProbeVirtualRouterRouteRuleEntry(entry); !ok {
				return nil, fmt.Errorf("route_rules[%d].entries[%d] is invalid", index, entryIndex)
			}
		}
	}
	if len(req.Items) > probeVirtualRouterMaxRouteRules {
		return nil, fmt.Errorf("route_rules exceeded limit (%d)", probeVirtualRouterMaxRouteRules)
	}
	rules := normalizeProbeVirtualRouterRouteRules(req.Items)
	now := time.Now().UTC()
	ProbeRouteConfigStore.mu.Lock()
	config := normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter)
	config.RouteRules = rules
	config.UpdatedAt = now.Format(time.RFC3339)
	ProbeRouteConfigStore.data.VirtualRouter = config
	reconcileProbeVirtualRouterFakeIPLibraryWithRouteRulesLocked(rules, now)
	ProbeRouteConfigStore.mu.Unlock()
	if err := ProbeRouteConfigStore.Save(); err != nil {
		return nil, err
	}
	syncResult := dispatchProbeRouteConfigSyncToKnownNodes(controllerBaseURL)
	return map[string]interface{}{
		"ok":    true,
		"items": rules,
		"sync":  syncResult,
	}, nil
}
