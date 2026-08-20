package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type probeRouteConfigResponse struct {
	NodeID           string                    `json:"node_id"`
	ExpectedNodeKind string                    `json:"expected_node_kind,omitempty"`
	VirtualRouter    probeVirtualRouterConfig  `json:"virtual_router,omitempty"`
	SpecialExit      *probeSpecialExitSnapshot `json:"special_exit,omitempty"`
}

type probeRouteFakeIPResolveResponse struct {
	NodeID string                        `json:"node_id"`
	Item   probeVirtualRouterFakeIPEntry `json:"item"`
}

type probeRouteFakeIPRenewResponse struct {
	NodeID string                          `json:"node_id"`
	Items  []probeVirtualRouterFakeIPEntry `json:"items"`
}

func ProbeRouteConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}

	nodeID, err := authenticateProbeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if ProbeRouteConfigStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "route config store not initialized"})
		return
	}
	ensureProbeVirtualRouterStoredAuthFields()
	reconcileProbeVirtualRouterFakeIPLibraryBestEffort()
	ProbeRouteConfigStore.mu.RLock()
	virtualRouter := buildProbeVirtualRouterConfigForNodeLocked(nodeID)
	specialExit, hasSpecialExit := findProbeSpecialExitByNodeID(ProbeRouteConfigStore.data.SpecialExits, nodeID)
	ProbeRouteConfigStore.mu.RUnlock()
	expectedNodeKind := probeNodeKindNormal
	if node, ok := getProbeNodeByID(nodeID); ok {
		expectedNodeKind = normalizeProbeNodeKind(node.NodeKind)
	}
	var snapshot *probeSpecialExitSnapshot
	if hasSpecialExit && probeNodeSupportsSpecialExit(expectedNodeKind) {
		value := probeSpecialExitSnapshotForConfig(specialExit)
		snapshot = &value
	}
	writeJSON(w, http.StatusOK, probeRouteConfigResponse{
		NodeID:           nodeID,
		ExpectedNodeKind: expectedNodeKind,
		VirtualRouter:    virtualRouter,
		SpecialExit:      snapshot,
	})
}

func ProbeRouteFakeIPResolveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}
	nodeID, err := authenticateProbeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	var req struct {
		Domain     string `json:"domain"`
		FakeIP     string `json:"fake_ip,omitempty"`
		RuleID     string `json:"rule_id,omitempty"`
		Action     string `json:"action,omitempty"`
		ExitNodeID string `json:"exit_node_id,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if strings.TrimSpace(req.FakeIP) != "" {
		item, _, changed, ok := lookupProbeVirtualRouterFakeIPByIP(req.FakeIP)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "fake ip mapping not found"})
			return
		}
		if _, err := authorizedProbeVirtualRouterFakeIPRule(item.Domain, item.RuleID, item.Action, item.ExitNodeID); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		if changed {
			if err := ProbeRouteConfigStore.Save(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, probeRouteFakeIPResolveResponse{
			NodeID: nodeID,
			Item:   item,
		})
		return
	}
	rule, err := authorizedProbeVirtualRouterFakeIPRule(req.Domain, req.RuleID, req.Action, req.ExitNodeID)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	item, _, changed, err := allocateProbeVirtualRouterFakeIPForDomain(req.Domain, rule)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if changed {
		if err := ProbeRouteConfigStore.Save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, probeRouteFakeIPResolveResponse{
		NodeID: nodeID,
		Item:   item,
	})
}

func ProbeRouteFakeIPRenewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}
	nodeID, err := authenticateProbeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	var req struct {
		Domains []string `json:"domains"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	for _, domain := range req.Domains {
		if _, err := authorizedProbeVirtualRouterFakeIPRule(domain, "", "", ""); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
	}
	items, _, changed, err := renewProbeVirtualRouterFakeIPDomains(req.Domains)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if changed {
		if err := ProbeRouteConfigStore.Save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, probeRouteFakeIPRenewResponse{
		NodeID: nodeID,
		Items:  items,
	})
}

func authorizedProbeVirtualRouterFakeIPRule(domain string, requestedRuleID string, requestedAction string, requestedExitNodeID string) (probeVirtualRouterRouteRule, error) {
	if ProbeRouteConfigStore == nil {
		return probeVirtualRouterRouteRule{}, fmt.Errorf("route config store not initialized")
	}
	ProbeRouteConfigStore.mu.RLock()
	config := normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter)
	ProbeRouteConfigStore.mu.RUnlock()
	if !config.Enabled {
		return probeVirtualRouterRouteRule{}, fmt.Errorf("virtual router is disabled")
	}
	rule, ok := probeVirtualRouterRouteRuleForFakeIPDomain(config.RouteRules, domain)
	if !ok || normalizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) != probeVirtualRouterRouteRuleActionExit {
		return probeVirtualRouterRouteRule{}, fmt.Errorf("domain is not assigned to a probe exit rule")
	}
	exitNodeID := normalizeProbeNodeID(rule.ExitNodeID)
	if exitNodeID == "" {
		return probeVirtualRouterRouteRule{}, fmt.Errorf("probe exit is not configured")
	}
	if value := strings.TrimSpace(requestedRuleID); value != "" && value != strings.TrimSpace(rule.ID) {
		return probeVirtualRouterRouteRule{}, fmt.Errorf("route rule does not match controller configuration")
	}
	if value := normalizeProbeVirtualRouterRouteRuleAction(requestedAction, requestedExitNodeID); strings.TrimSpace(requestedAction) != "" && value != probeVirtualRouterRouteRuleActionExit {
		return probeVirtualRouterRouteRule{}, fmt.Errorf("route action does not match controller configuration")
	}
	if value := normalizeProbeNodeID(requestedExitNodeID); value != "" && value != exitNodeID {
		return probeVirtualRouterRouteRule{}, fmt.Errorf("exit node does not match controller configuration")
	}
	return rule, nil
}
