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
	NodeID        string                            `json:"node_id"`
	Item          probeVirtualRouterFakeIPEntry     `json:"item"`
	FakeIPLibrary probeVirtualRouterFakeIPLibrary   `json:"fake_ip_library"`
	Sync          probeLinkConfigSyncDispatchResult `json:"sync"`
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
		RuleID     string `json:"rule_id,omitempty"`
		Action     string `json:"action,omitempty"`
		ExitNodeID string `json:"exit_node_id,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	item, library, err := allocateProbeVirtualRouterFakeIPForDomain(req.Domain, probeVirtualRouterRouteRule{
		ID:         strings.TrimSpace(req.RuleID),
		Name:       strings.TrimSpace(req.RuleID),
		Action:     strings.TrimSpace(req.Action),
		ExitNodeID: strings.TrimSpace(req.ExitNodeID),
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := ProbeRouteConfigStore.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	syncResult := dispatchProbeRouteConfigSyncToKnownNodes(controllerBaseURLFromRequest(r))
	writeJSON(w, http.StatusOK, probeRouteFakeIPResolveResponse{
		NodeID:        nodeID,
		Item:          item,
		FakeIPLibrary: library,
		Sync:          syncResult,
	})
}
