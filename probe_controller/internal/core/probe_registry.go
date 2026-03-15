package core

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

const (
	probeSecretsStoreField = "probe_secrets"
	probeNodesStoreField   = "probe_nodes"
)

type probeSecretUpsertRequest struct {
	NodeID string `json:"node_id"`
	Secret string `json:"secret"`
}

type probeNodeRecord struct {
	NodeNo        int    `json:"node_no"`
	NodeName      string `json:"node_name"`
	NodeSecret    string `json:"node_secret"`
	TargetSystem  string `json:"target_system"`
	DirectConnect bool   `json:"direct_connect"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type probeNodeStatusRecord struct {
	NodeNo   int                `json:"node_no"`
	NodeName string             `json:"node_name"`
	Runtime  probeRuntimeStatus `json:"runtime"`
}

type probeNodesSyncRequest struct {
	Nodes []probeNodeRecord `json:"nodes"`
}

func AdminUpsertProbeSecretHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req probeSecretUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	nodeID := normalizeProbeNodeID(req.NodeID)
	secret := strings.TrimSpace(req.Secret)
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}
	if secret == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "secret is required"})
		return
	}

	Store.mu.Lock()
	secrets := loadProbeSecretsLocked()
	secrets[nodeID] = secret
	Store.Data[probeSecretsStoreField] = secrets
	Store.mu.Unlock()

	if err := Store.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist probe secret"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"node_id": nodeID,
	})
}

func AdminGetProbeNodesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	Store.mu.RLock()
	nodes := loadProbeNodesLocked()
	Store.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"nodes": nodes,
	})
}

func AdminGetProbeNodeStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	Store.mu.RLock()
	items := loadProbeNodeStatusLocked()
	Store.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": items,
	})
}

func AdminSyncProbeNodesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req probeNodesSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	nodes, secrets := normalizeProbeNodes(req.Nodes)

	Store.mu.Lock()
	Store.Data[probeNodesStoreField] = nodes
	Store.Data[probeSecretsStoreField] = secrets
	Store.mu.Unlock()

	if err := Store.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist probe nodes"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":    true,
		"count": len(nodes),
		"nodes": nodes,
	})
}

func loadProbeSecretsLocked() map[string]string {
	out := make(map[string]string)
	for _, item := range loadProbeNodesLocked() {
		nodeID := normalizeProbeNodeID(strconv.Itoa(item.NodeNo))
		secret := strings.TrimSpace(item.NodeSecret)
		if nodeID != "" && secret != "" {
			out[nodeID] = secret
		}
	}
	if len(out) > 0 {
		return out
	}

	rawAny, ok := Store.Data[probeSecretsStoreField]
	if !ok {
		return out
	}

	switch raw := rawAny.(type) {
	case map[string]string:
		for k, v := range raw {
			key := normalizeProbeNodeID(k)
			value := strings.TrimSpace(v)
			if key != "" && value != "" {
				out[key] = value
			}
		}
	case map[string]interface{}:
		for k, v := range raw {
			value, ok := v.(string)
			if !ok {
				continue
			}
			key := normalizeProbeNodeID(k)
			trimmed := strings.TrimSpace(value)
			if key != "" && trimmed != "" {
				out[key] = trimmed
			}
		}
	}

	return out
}

func loadProbeNodesLocked() []probeNodeRecord {
	result := make([]probeNodeRecord, 0)
	rawAny, ok := Store.Data[probeNodesStoreField]
	if !ok {
		return result
	}

	rawJSON, err := json.Marshal(rawAny)
	if err != nil {
		return result
	}
	if err := json.Unmarshal(rawJSON, &result); err != nil {
		return []probeNodeRecord{}
	}

	normalized, _ := normalizeProbeNodes(result)
	return normalized
}

func loadProbeNodeStatusLocked() []probeNodeStatusRecord {
	nodes := loadProbeNodesLocked()
	out := make([]probeNodeStatusRecord, 0, len(nodes))
	for _, node := range nodes {
		nodeID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
		runtime := probeRuntimeStatus{NodeID: nodeID, Online: false, System: probeSystemMetrics{}}
		if runtime, ok := getProbeRuntime(nodeID); ok {
			runtime = runtime
		}
		out = append(out, probeNodeStatusRecord{NodeNo: node.NodeNo, NodeName: node.NodeName, Runtime: runtime})
	}
	return out
}

func normalizeProbeNodes(items []probeNodeRecord) ([]probeNodeRecord, map[string]string) {
	nodes := make([]probeNodeRecord, 0, len(items))
	secrets := make(map[string]string)
	seenNo := make(map[int]struct{})

	for _, item := range items {
		if item.NodeNo <= 0 {
			continue
		}
		if _, ok := seenNo[item.NodeNo]; ok {
			continue
		}
		seenNo[item.NodeNo] = struct{}{}

		node := item
		node.NodeName = strings.TrimSpace(node.NodeName)
		node.NodeSecret = strings.TrimSpace(node.NodeSecret)
		node.TargetSystem = strings.ToLower(strings.TrimSpace(node.TargetSystem))
		if node.TargetSystem != "windows" {
			node.TargetSystem = "linux"
		}
		nodes = append(nodes, node)

		nodeID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
		if nodeID != "" && node.NodeSecret != "" {
			secrets[nodeID] = node.NodeSecret
		}
	}

	return nodes, secrets
}

func normalizeProbeNodeID(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}

	lower := strings.ToLower(v)
	if strings.HasPrefix(lower, "node-") || strings.HasPrefix(lower, "node_") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(lower, "node-"), "node_")
		suffix = strings.TrimSpace(suffix)
		if suffix != "" {
			if n, err := strconv.Atoi(suffix); err == nil && n > 0 {
				return strconv.Itoa(n)
			}
		}
	}

	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return strconv.Itoa(n)
	}
	return v
}
