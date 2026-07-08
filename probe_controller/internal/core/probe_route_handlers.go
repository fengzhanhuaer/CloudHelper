package core

import (
	"encoding/json"
	"net/http"
	"strings"
)

type probeRouteConfigResponse struct {
	NodeID        string                   `json:"node_id"`
	VirtualRouter probeVirtualRouterConfig `json:"virtual_router,omitempty"`
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

	nodeID, err := authenticateProbeRequestOrQuerySecret(r)
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
	ProbeRouteConfigStore.mu.RUnlock()
	writeJSON(w, http.StatusOK, probeRouteConfigResponse{
		NodeID:        nodeID,
		VirtualRouter: virtualRouter,
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
	nodeID, err := authenticateProbeRequestOrQuerySecret(r)
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
		item, _, ok := lookupProbeVirtualRouterFakeIPByIP(req.FakeIP)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "fake ip mapping not found"})
			return
		}
		writeJSON(w, http.StatusOK, probeRouteFakeIPResolveResponse{
			NodeID: nodeID,
			Item:   item,
		})
		return
	}
	item, _, changed, err := allocateProbeVirtualRouterFakeIPForDomain(req.Domain, probeVirtualRouterRouteRule{
		ID:         strings.TrimSpace(req.RuleID),
		Name:       strings.TrimSpace(req.RuleID),
		Action:     strings.TrimSpace(req.Action),
		ExitNodeID: strings.TrimSpace(req.ExitNodeID),
	})
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
	nodeID, err := authenticateProbeRequestOrQuerySecret(r)
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
