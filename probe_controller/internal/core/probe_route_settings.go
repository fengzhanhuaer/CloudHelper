package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type probeRouteSettingsGroup struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ExitNodeID string `json:"exit_node_id"`
}

type probeRouteSettingsNode struct {
	NodeID      string `json:"node_id"`
	DisplayName string `json:"display_name"`
}

type probeRouteSettingsResponse struct {
	OK        bool                      `json:"ok"`
	Groups    []probeRouteSettingsGroup `json:"groups"`
	Nodes     []probeRouteSettingsNode  `json:"nodes"`
	UpdatedAt string                    `json:"updated_at,omitempty"`
	Message   string                    `json:"message,omitempty"`
	Sync      any                       `json:"sync,omitempty"`
}

var saveProbeRouteSettings = func() error {
	return ProbeRouteConfigStore.Save()
}

func ProbeRouteSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}
	_, err := authenticateProbeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if ProbeRouteConfigStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "route config store not initialized"})
		return
	}

	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, buildProbeRouteSettingsResponse())
		return
	}

	var req struct {
		ExitNodes map[string]string `json:"exit_nodes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if req.ExitNodes == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "exit_nodes is required"})
		return
	}
	if err := updateProbeRouteSettingsExitNodes(req.ExitNodes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := saveProbeRouteSettings(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	response := buildProbeRouteSettingsResponse()
	response.Message = "路由设置已保存到主控"
	response.Sync = dispatchProbeRouteConfigSyncToKnownNodes(controllerBaseURLFromRequest(r))
	writeJSON(w, http.StatusOK, response)
}

func updateProbeRouteSettingsExitNodes(exitNodes map[string]string) error {
	now := time.Now().UTC()

	ProbeRouteConfigStore.mu.Lock()
	defer ProbeRouteConfigStore.mu.Unlock()

	config := ensureProbeVirtualRouterProbeIPsForKnownNodes(normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter))
	knownNodes := make(map[string]struct{}, len(config.ProbeIPs))
	for _, node := range config.ProbeIPs {
		nodeID := normalizeProbeNodeID(node.NodeID)
		if nodeID != "" {
			knownNodes[nodeID] = struct{}{}
		}
	}
	ruleIndex := make(map[string]int, len(config.RouteRules))
	for index, rule := range config.RouteRules {
		if normalizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) != probeVirtualRouterRouteRuleActionExit {
			continue
		}
		if id := strings.TrimSpace(rule.ID); id != "" {
			ruleIndex[id] = index
		}
	}
	for rawRuleID, rawNodeID := range exitNodes {
		ruleID := strings.TrimSpace(rawRuleID)
		index, ok := ruleIndex[ruleID]
		if !ok {
			return fmt.Errorf("unknown route group: %s", ruleID)
		}
		exitNodeID := normalizeProbeNodeID(rawNodeID)
		if _, ok := knownNodes[exitNodeID]; !ok {
			return fmt.Errorf("unknown exit node for route group %s", ruleID)
		}
		config.RouteRules[index].ExitNodeID = exitNodeID
		config.RouteRules[index].Action = probeVirtualRouterRouteRuleActionExit
		config.RouteRules[index].UpdatedAt = now.Format(time.RFC3339)
	}
	config.RouteRules = normalizeProbeVirtualRouterRouteRules(config.RouteRules)
	config.UpdatedAt = now.Format(time.RFC3339)
	ProbeRouteConfigStore.data.VirtualRouter = config
	reconcileProbeVirtualRouterFakeIPLibraryWithRouteRulesLocked(config.RouteRules, now)
	return nil
}

func buildProbeRouteSettingsResponse() probeRouteSettingsResponse {
	ProbeRouteConfigStore.mu.RLock()
	config := ensureProbeVirtualRouterProbeIPsForKnownNodes(normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter))
	ProbeRouteConfigStore.mu.RUnlock()
	config = enrichProbeVirtualRouterProbeIPDisplayNames(config)

	groups := make([]probeRouteSettingsGroup, 0, len(config.RouteRules))
	for _, rule := range config.RouteRules {
		if normalizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) != probeVirtualRouterRouteRuleActionExit {
			continue
		}
		id := strings.TrimSpace(rule.ID)
		if id == "" {
			continue
		}
		groups = append(groups, probeRouteSettingsGroup{
			ID:         id,
			Name:       strings.TrimSpace(rule.Name),
			ExitNodeID: normalizeProbeNodeID(rule.ExitNodeID),
		})
	}

	nodes := make([]probeRouteSettingsNode, 0, len(config.ProbeIPs))
	for _, item := range config.ProbeIPs {
		nodeID := normalizeProbeNodeID(item.NodeID)
		if nodeID == "" {
			continue
		}
		displayName := strings.TrimSpace(item.DisplayName)
		if displayName == "" {
			displayName = "节点 " + nodeID
		}
		nodes = append(nodes, probeRouteSettingsNode{NodeID: nodeID, DisplayName: displayName})
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		left := strings.ToLower(nodes[i].DisplayName)
		right := strings.ToLower(nodes[j].DisplayName)
		if left == right {
			return nodes[i].NodeID < nodes[j].NodeID
		}
		return left < right
	})
	return probeRouteSettingsResponse{
		OK:        true,
		Groups:    groups,
		Nodes:     nodes,
		UpdatedAt: strings.TrimSpace(config.UpdatedAt),
	}
}
