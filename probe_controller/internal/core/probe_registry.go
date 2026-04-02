package core

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
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
	NodeNo                int                    `json:"node_no"`
	NodeName              string                 `json:"node_name"`
	Remark                string                 `json:"remark"`
	DDNS                  string                 `json:"ddns"`
	CloudflareDDNSRecords []cloudflareDDNSRecord `json:"cloudflare_ddns_records,omitempty"`
	NodeSecret            string                 `json:"node_secret"`
	TargetSystem          string                 `json:"target_system"`
	DirectConnect         bool                   `json:"direct_connect"`
	ServiceScheme         string                 `json:"service_scheme"`
	ServiceHost           string                 `json:"service_host"`
	PaymentCycle          string                 `json:"payment_cycle"`
	Cost                  string                 `json:"cost"`
	ExpireAt              string                 `json:"expire_at"`
	VendorName            string                 `json:"vendor_name"`
	VendorURL             string                 `json:"vendor_url"`
	CreatedAt             string                 `json:"created_at"`
	UpdatedAt             string                 `json:"updated_at"`
}

type probeNodeStatusRecord struct {
	NodeNo   int                `json:"node_no"`
	NodeName string             `json:"node_name"`
	Runtime  probeRuntimeStatus `json:"runtime"`
}

type probeNodesSyncRequest struct {
	Nodes []probeNodeRecord `json:"nodes"`
}

type probeNodeCreateRequest struct {
	NodeName string `json:"node_name"`
}

type probeNodeUpdateRequest struct {
	NodeNo        int    `json:"node_no"`
	NodeName      string `json:"node_name"`
	Remark        string `json:"remark"`
	DDNS          string `json:"ddns"`
	TargetSystem  string `json:"target_system"`
	DirectConnect bool   `json:"direct_connect"`
	PaymentCycle  string `json:"payment_cycle"`
	Cost          string `json:"cost"`
	ExpireAt      string `json:"expire_at"`
	VendorName    string `json:"vendor_name"`
	VendorURL     string `json:"vendor_url"`
}

type probeNodeLinkUpdateRequest struct {
	NodeNo        int    `json:"node_no"`
	ServiceScheme string `json:"service_scheme"`
	ServiceHost   string `json:"service_host"`
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

	ProbeStore.mu.Lock()
	secrets := loadProbeSecretsLocked()
	secrets[nodeID] = secret
	ProbeStore.data.ProbeSecrets = secrets
	ProbeStore.mu.Unlock()

	if err := ProbeStore.Save(); err != nil {
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

	ProbeStore.mu.RLock()
	nodes := loadProbeNodesLocked()
	ProbeStore.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"nodes": nodes,
	})
}

func AdminGetProbeNodeStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ProbeStore.mu.RLock()
	items := loadProbeNodeStatusLocked()
	ProbeStore.mu.RUnlock()

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

	ProbeStore.mu.Lock()
	ProbeStore.data.ProbeNodes = nodes
	ProbeStore.data.ProbeSecrets = secrets
	ProbeStore.mu.Unlock()

	if err := ProbeStore.Save(); err != nil {
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

	rawAny := ProbeStore.data.ProbeSecrets
	if len(rawAny) == 0 {
		return out
	}

	for k, v := range rawAny {
		key := normalizeProbeNodeID(k)
		value := strings.TrimSpace(v)
		if key != "" && value != "" {
			out[key] = value
		}
	}

	return out
}

func loadProbeNodesLocked() []probeNodeRecord {
	result := make([]probeNodeRecord, 0)
	rawAny := ProbeStore.data.ProbeNodes
	if len(rawAny) == 0 {
		return result
	}
	result = append(result, rawAny...)

	normalized, _ := normalizeProbeNodes(result)
	return normalized
}

func loadProbeNodeStatusLocked() []probeNodeStatusRecord {
	nodes := loadProbeNodesLocked()
	runtimes := listProbeRuntimes()
	runtimeMap := make(map[string]probeRuntimeStatus, len(runtimes))
	for _, rt := range runtimes {
		runtimeMap[normalizeProbeNodeID(rt.NodeID)] = rt
	}

	out := make([]probeNodeStatusRecord, 0, len(nodes))
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		nodeID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
		seen[nodeID] = struct{}{}
		runtime := probeRuntimeStatus{NodeID: nodeID, Online: false, System: probeSystemMetrics{}}
		if rt, ok := runtimeMap[nodeID]; ok {
			runtime = rt
		}
		out = append(out, probeNodeStatusRecord{NodeNo: node.NodeNo, NodeName: node.NodeName, Runtime: runtime})
	}

	for nodeID, rt := range runtimeMap {
		if _, ok := seen[nodeID]; ok {
			continue
		}
		nodeNo := 0
		if n, err := strconv.Atoi(nodeID); err == nil && n > 0 {
			nodeNo = n
		}
		name := "未注册节点"
		if nodeID != "" {
			name = name + "(" + nodeID + ")"
		}
		out = append(out, probeNodeStatusRecord{NodeNo: nodeNo, NodeName: name, Runtime: rt})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeNo == out[j].NodeNo {
			return out[i].NodeName < out[j].NodeName
		}
		if out[i].NodeNo == 0 {
			return false
		}
		if out[j].NodeNo == 0 {
			return true
		}
		return out[i].NodeNo < out[j].NodeNo
	})
	return out
}

func loadProbeNodeStatusByIDLocked(nodeID string) (probeNodeStatusRecord, bool) {
	normalizedID := normalizeProbeNodeID(nodeID)
	if normalizedID == "" {
		return probeNodeStatusRecord{}, false
	}

	nodes := loadProbeNodesLocked()
	for _, node := range nodes {
		if normalizeProbeNodeID(strconv.Itoa(node.NodeNo)) != normalizedID {
			continue
		}
		runtime := probeRuntimeStatus{NodeID: normalizedID, Online: false, System: probeSystemMetrics{}}
		if rt, ok := getProbeRuntime(normalizedID); ok {
			runtime = rt
		}
		return probeNodeStatusRecord{NodeNo: node.NodeNo, NodeName: node.NodeName, Runtime: runtime}, true
	}

	if rt, ok := getProbeRuntime(normalizedID); ok {
		nodeNo := 0
		if n, err := strconv.Atoi(normalizedID); err == nil && n > 0 {
			nodeNo = n
		}
		name := "未注册节点"
		if normalizedID != "" {
			name += "(" + normalizedID + ")"
		}
		return probeNodeStatusRecord{NodeNo: nodeNo, NodeName: name, Runtime: rt}, true
	}

	return probeNodeStatusRecord{}, false
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
		node.Remark = strings.TrimSpace(node.Remark)
		node.DDNS = strings.TrimSpace(node.DDNS)
		node.CloudflareDDNSRecords = normalizeCloudflareRecords(node.CloudflareDDNSRecords)
		node.NodeSecret = strings.TrimSpace(node.NodeSecret)
		node.TargetSystem = strings.ToLower(strings.TrimSpace(node.TargetSystem))
		if node.TargetSystem != "windows" {
			node.TargetSystem = "linux"
		}
		node.ServiceScheme = normalizeProbeEndpointScheme(node.ServiceScheme)
		node.ServiceHost = strings.TrimSpace(node.ServiceHost)
		node.PaymentCycle = strings.TrimSpace(node.PaymentCycle)
		node.Cost = strings.TrimSpace(node.Cost)
		node.ExpireAt = strings.TrimSpace(node.ExpireAt)
		node.VendorName = strings.TrimSpace(node.VendorName)
		node.VendorURL = strings.TrimSpace(node.VendorURL)
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

func createProbeNodeLocked(nodeName string) (probeNodeRecord, error) {
	name := strings.TrimSpace(nodeName)
	if name == "" {
		return probeNodeRecord{}, fmt.Errorf("node name is required")
	}

	nodes := loadProbeNodesLocked()
	for _, item := range nodes {
		if strings.EqualFold(strings.TrimSpace(item.NodeName), name) {
			return probeNodeRecord{}, fmt.Errorf("node name already exists")
		}
	}

	nextNo := 1
	for _, item := range nodes {
		if item.NodeNo >= nextNo {
			nextNo = item.NodeNo + 1
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	node := probeNodeRecord{
		NodeNo:        nextNo,
		NodeName:      name,
		Remark:        "",
		DDNS:          "",
		NodeSecret:    randomProbeNodeSecret(32),
		TargetSystem:  "linux",
		DirectConnect: true,
		ServiceScheme: "http",
		ServiceHost:   "",
		PaymentCycle:  "",
		Cost:          "",
		ExpireAt:      "",
		VendorName:    "",
		VendorURL:     "",
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	nodes = append(nodes, node)
	normalized, secrets := normalizeProbeNodes(nodes)
	ProbeStore.data.ProbeNodes = normalized
	ProbeStore.data.ProbeSecrets = secrets

	for _, item := range normalized {
		if item.NodeNo == nextNo {
			return item, nil
		}
	}
	return probeNodeRecord{}, fmt.Errorf("failed to create node")
}

func updateProbeNodeLocked(req probeNodeUpdateRequest) (probeNodeRecord, error) {
	if req.NodeNo <= 0 {
		return probeNodeRecord{}, fmt.Errorf("invalid node number")
	}

	name := strings.TrimSpace(req.NodeName)
	if name == "" {
		return probeNodeRecord{}, fmt.Errorf("node name is required")
	}

	system := strings.ToLower(strings.TrimSpace(req.TargetSystem))
	if system != "linux" && system != "windows" {
		return probeNodeRecord{}, fmt.Errorf("target system must be linux or windows")
	}

	nodes := loadProbeNodesLocked()
	found := -1
	for i := range nodes {
		if nodes[i].NodeNo == req.NodeNo {
			found = i
			continue
		}
		if strings.EqualFold(strings.TrimSpace(nodes[i].NodeName), name) {
			return probeNodeRecord{}, fmt.Errorf("node name already exists")
		}
	}
	if found < 0 {
		return probeNodeRecord{}, fmt.Errorf("node %d not found", req.NodeNo)
	}

	nodes[found].NodeName = name
	nodes[found].Remark = strings.TrimSpace(req.Remark)
	nodes[found].DDNS = strings.TrimSpace(req.DDNS)
	nodes[found].TargetSystem = system
	nodes[found].DirectConnect = req.DirectConnect
	nodes[found].PaymentCycle = strings.TrimSpace(req.PaymentCycle)
	nodes[found].Cost = strings.TrimSpace(req.Cost)
	nodes[found].ExpireAt = strings.TrimSpace(req.ExpireAt)
	nodes[found].VendorName = strings.TrimSpace(req.VendorName)
	nodes[found].VendorURL = strings.TrimSpace(req.VendorURL)
	nodes[found].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if strings.TrimSpace(nodes[found].CreatedAt) == "" {
		nodes[found].CreatedAt = nodes[found].UpdatedAt
	}
	if strings.TrimSpace(nodes[found].NodeSecret) == "" {
		nodes[found].NodeSecret = randomProbeNodeSecret(32)
	}

	normalized, secrets := normalizeProbeNodes(nodes)
	ProbeStore.data.ProbeNodes = normalized
	ProbeStore.data.ProbeSecrets = secrets

	for _, item := range normalized {
		if item.NodeNo == req.NodeNo {
			return item, nil
		}
	}
	return probeNodeRecord{}, fmt.Errorf("node %d not found after update", req.NodeNo)
}

func updateProbeNodeLinkLocked(req probeNodeLinkUpdateRequest) (probeNodeRecord, error) {
	if req.NodeNo <= 0 {
		return probeNodeRecord{}, fmt.Errorf("invalid node number")
	}

	nodes := loadProbeNodesLocked()
	found := -1
	for i := range nodes {
		if nodes[i].NodeNo == req.NodeNo {
			found = i
			break
		}
	}
	if found < 0 {
		return probeNodeRecord{}, fmt.Errorf("node %d not found", req.NodeNo)
	}

	nodes[found].ServiceScheme = normalizeProbeEndpointScheme(req.ServiceScheme)
	nodes[found].ServiceHost = strings.TrimSpace(req.ServiceHost)
	nodes[found].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if strings.TrimSpace(nodes[found].CreatedAt) == "" {
		nodes[found].CreatedAt = nodes[found].UpdatedAt
	}
	if strings.TrimSpace(nodes[found].NodeSecret) == "" {
		nodes[found].NodeSecret = randomProbeNodeSecret(32)
	}

	normalized, secrets := normalizeProbeNodes(nodes)
	ProbeStore.data.ProbeNodes = normalized
	ProbeStore.data.ProbeSecrets = secrets

	for _, item := range normalized {
		if item.NodeNo == req.NodeNo {
			return item, nil
		}
	}
	return probeNodeRecord{}, fmt.Errorf("node %d not found after link update", req.NodeNo)
}

func normalizeProbeEndpointScheme(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "https":
		return "https"
	case "tcp":
		return "tcp"
	case "http3", "h3":
		return "http3"
	case "websocket", "ws", "wss":
		return "websocket"
	case "http":
		return "http"
	}
	return "http"
}


func randomProbeNodeSecret(length int) string {
	if length <= 0 {
		return ""
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("node-secret-%d", time.Now().UnixNano())
	}

	out := make([]byte, length)
	for i := range buf {
		out[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(out)
}
