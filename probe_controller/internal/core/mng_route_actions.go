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

func validateMngProbeVirtualRouterTopologyRule(item probeVirtualRouterTopologyRule, index int) error {
	fromNodeID := normalizeProbeNodeID(item.FromNodeID)
	toNodeID := normalizeProbeNodeID(item.ToNodeID)
	if fromNodeID == "" {
		return fmt.Errorf("topology_rules[%d].from_node_id is required", index)
	}
	if toNodeID == "" {
		return fmt.Errorf("topology_rules[%d].to_node_id is required", index)
	}
	if fromNodeID == toNodeID {
		return fmt.Errorf("topology_rules[%d] endpoints must be different", index)
	}
	return nil
}

func preserveMngProbeVirtualRouterTopologyRulePrivateFields(item probeVirtualRouterTopologyRule, existing probeVirtualRouterTopologyRule) probeVirtualRouterTopologyRule {
	if strings.TrimSpace(item.Name) == "" {
		item.Name = existing.Name
	}
	if strings.TrimSpace(item.FromTLSSPKISHA256) == "" {
		item.FromTLSSPKISHA256 = existing.FromTLSSPKISHA256
	}
	if strings.TrimSpace(item.ToTLSSPKISHA256) == "" {
		item.ToTLSSPKISHA256 = existing.ToTLSSPKISHA256
	}
	if strings.TrimSpace(item.UserID) == "" {
		item.UserID = existing.UserID
	}
	if strings.TrimSpace(item.UserPublicKey) == "" {
		item.UserPublicKey = existing.UserPublicKey
	}
	if strings.TrimSpace(item.Secret) == "" {
		item.Secret = existing.Secret
	}
	if strings.TrimSpace(item.AuthTicket) == "" {
		item.AuthTicket = existing.AuthTicket
	}
	return item
}

func upsertMngProbeVirtualRouterTopologyRule(payload json.RawMessage, controllerBaseURL string) (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	var req struct {
		Item probeVirtualRouterTopologyRule `json:"item"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload")
	}
	if err := validateMngProbeVirtualRouterTopologyRule(req.Item, 0); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	item := req.Item
	item.ID = strings.TrimSpace(item.ID)
	item.UpdatedAt = now

	ProbeRouteConfigStore.mu.Lock()
	config := normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter)
	rules := append([]probeVirtualRouterTopologyRule(nil), config.TopologyRules...)
	if item.ID == "" {
		if len(rules) >= probeVirtualRouterMaxTopologyRules {
			ProbeRouteConfigStore.mu.Unlock()
			return nil, fmt.Errorf("topology_rules exceeded limit (%d)", probeVirtualRouterMaxTopologyRules)
		}
		rules = append(rules, item)
	} else {
		found := false
		for index := range rules {
			if strings.TrimSpace(rules[index].ID) != item.ID {
				continue
			}
			item = preserveMngProbeVirtualRouterTopologyRulePrivateFields(item, rules[index])
			rules[index] = item
			found = true
			break
		}
		if !found {
			ProbeRouteConfigStore.mu.Unlock()
			return nil, fmt.Errorf("topology rule %q not found", item.ID)
		}
	}

	rules = normalizeProbeVirtualRouterTopologyRules(rules)
	var saved probeVirtualRouterTopologyRule
	if item.ID == "" && len(rules) > 0 {
		saved = rules[len(rules)-1]
	} else {
		for _, rule := range rules {
			if strings.TrimSpace(rule.ID) == item.ID {
				saved = rule
				break
			}
		}
	}
	if strings.TrimSpace(saved.ID) == "" {
		ProbeRouteConfigStore.mu.Unlock()
		return nil, fmt.Errorf("topology rule could not be saved")
	}
	config.TopologyRules = rules
	config.UpdatedAt = now
	config = enrichProbeVirtualRouterAuthTickets(ensureProbeVirtualRouterAuthFields(config))
	for _, rule := range config.TopologyRules {
		if rule.ID == saved.ID {
			saved = rule
			break
		}
	}
	ProbeRouteConfigStore.data.VirtualRouter = config
	ProbeRouteConfigStore.mu.Unlock()
	if err := ProbeRouteConfigStore.Save(); err != nil {
		return nil, err
	}
	syncResult := dispatchProbeRouteConfigSyncToKnownNodes(controllerBaseURL)
	return map[string]interface{}{
		"ok":   true,
		"item": saved,
		"sync": syncResult,
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

func validateMngProbeVirtualRouterRouteRule(item probeVirtualRouterRouteRule, index int) error {
	if strings.TrimSpace(item.Name) == "" {
		return fmt.Errorf("route_rules[%d].name is required", index)
	}
	action := normalizeProbeVirtualRouterRouteRuleAction(item.Action, item.ExitNodeID)
	if action == "" {
		return fmt.Errorf("route_rules[%d].action is invalid", index)
	}
	if action == probeVirtualRouterRouteRuleActionExit {
		exitNodeID := normalizeProbeNodeID(item.ExitNodeID)
		if exitNodeID == "" {
			return fmt.Errorf("route_rules[%d].exit_node_id is required", index)
		}
		if !isProbeVirtualRouterKnownNodeID(exitNodeID) {
			return fmt.Errorf("route_rules[%d].exit_node_id is unknown", index)
		}
	}
	if len(item.Entries) > probeVirtualRouterMaxRouteRuleEntries {
		return fmt.Errorf("route_rules[%d].entries exceeded limit (%d)", index, probeVirtualRouterMaxRouteRuleEntries)
	}
	for entryIndex, entry := range item.Entries {
		if _, ok := normalizeProbeVirtualRouterRouteRuleEntry(entry); !ok {
			return fmt.Errorf("route_rules[%d].entries[%d] %q is invalid", index, entryIndex, entry)
		}
	}
	return nil
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
		if err := validateMngProbeVirtualRouterRouteRule(item, index); err != nil {
			return nil, err
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

func upsertMngProbeVirtualRouterRouteRule(payload json.RawMessage, controllerBaseURL string) (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	var req struct {
		Item probeVirtualRouterRouteRule `json:"item"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload")
	}
	if err := validateMngProbeVirtualRouterRouteRule(req.Item, 0); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	item := req.Item
	item.ID = strings.TrimSpace(item.ID)
	item.UpdatedAt = now.Format(time.RFC3339)

	ProbeRouteConfigStore.mu.Lock()
	config := normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter)
	rules := append([]probeVirtualRouterRouteRule(nil), config.RouteRules...)
	if item.ID == "" {
		if len(rules) >= probeVirtualRouterMaxRouteRules {
			ProbeRouteConfigStore.mu.Unlock()
			return nil, fmt.Errorf("route_rules exceeded limit (%d)", probeVirtualRouterMaxRouteRules)
		}
		seen := collectProbeVirtualRouterReservedRouteRuleIDs(rules)
		item.ID, _ = allocateProbeVirtualRouterRouteRuleID(seen, seen, 1)
		rules = append(rules, item)
	} else {
		found := false
		for index := range rules {
			if strings.TrimSpace(rules[index].ID) != item.ID {
				continue
			}
			rules[index] = item
			found = true
			break
		}
		if !found {
			ProbeRouteConfigStore.mu.Unlock()
			return nil, fmt.Errorf("route rule %q not found", item.ID)
		}
	}

	rules = normalizeProbeVirtualRouterRouteRules(rules)
	var saved probeVirtualRouterRouteRule
	for _, rule := range rules {
		if rule.ID == item.ID {
			saved = rule
			break
		}
	}
	if saved.ID == "" {
		ProbeRouteConfigStore.mu.Unlock()
		return nil, fmt.Errorf("route rule could not be saved")
	}
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
		"ok":   true,
		"item": saved,
		"sync": syncResult,
	}, nil
}
