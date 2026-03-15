package core

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

const probeSecretsStoreField = "probe_secrets"

type probeSecretUpsertRequest struct {
	NodeID string `json:"node_id"`
	Secret string `json:"secret"`
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

func loadProbeSecretsLocked() map[string]string {
	out := make(map[string]string)
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

func normalizeProbeNodeID(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return strconv.Itoa(n)
	}
	return v
}
