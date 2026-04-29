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
	ExpireAt string             `json:"expire_at"`
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

type probeNodeDeleteRequest struct {
	NodeNo int `json:"node_no"`
}

type probeNodeRestoreRequest struct {
	NodeNo int `json:"node_no"`
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

	includeDeleted := false
	if raw := strings.TrimSpace(r.URL.Query().Get("include_deleted")); raw != "" {
		lower := strings.ToLower(raw)
		includeDeleted = lower == "1" || lower == "true" || lower == "yes"
	}

	ProbeStore.mu.RLock()
	nodes := loadProbeNodesLocked()
	resp := map[string]interface{}{
		"nodes": attachProbeRuntimeToNodes(nodes),
	}
	if includeDeleted {
		resp["deleted_nodes"] = loadDeletedProbeNodesLocked()
	}
	ProbeStore.mu.RUnlock()

	writeJSON(w, http.StatusOK, resp)
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

func AdminDeleteProbeNodeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req probeNodeDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	ProbeStore.mu.Lock()
	node, nodes, deletedNodes, err := deleteProbeNodeLocked(req.NodeNo)
	ProbeStore.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := ProbeStore.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist deleted probe node"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":            true,
		"node":          node,
		"nodes":         nodes,
		"deleted_nodes": deletedNodes,
	})
}

func AdminRestoreProbeNodeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req probeNodeRestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	ProbeStore.mu.Lock()
	node, nodes, deletedNodes, err := restoreDeletedProbeNodeLocked(req.NodeNo)
	ProbeStore.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := ProbeStore.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist restored probe node"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":            true,
		"node":          node,
		"nodes":         nodes,
		"deleted_nodes": deletedNodes,
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

	ProbeStore.mu.Lock()
	nodes, err := syncProbeNodesLocked(req.Nodes)
	ProbeStore.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

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

func loadDeletedProbeNodesLocked() []probeNodeRecord {
	result := make([]probeNodeRecord, 0)
	rawAny := ProbeStore.data.DeletedProbeNodes
	if len(rawAny) == 0 {
		return result
	}
	result = append(result, rawAny...)

	normalized, _ := normalizeProbeNodes(result)
	activeNodeNos := make(map[int]struct{})
	for _, node := range loadProbeNodesLocked() {
		if node.NodeNo > 0 {
			activeNodeNos[node.NodeNo] = struct{}{}
		}
	}
	filtered := make([]probeNodeRecord, 0, len(normalized))
	for _, node := range normalized {
		if node.NodeNo <= 0 {
			continue
		}
		if _, ok := activeNodeNos[node.NodeNo]; ok {
			continue
		}
		filtered = append(filtered, node)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].NodeNo < filtered[j].NodeNo
	})
	return filtered
}

func loadProbeNodeStatusLocked() []probeNodeStatusRecord {
	nodes := loadProbeNodesLocked()
	runtimes := listProbeRuntimes()
	runtimeMap := make(map[string]probeRuntimeStatus, len(runtimes))
	for _, rt := range runtimes {
		runtimeMap[normalizeProbeNodeID(rt.NodeID)] = rt
	}
	deletedNodeIDs := loadDeletedProbeNodeIDSetLocked()

	out := make([]probeNodeStatusRecord, 0, len(nodes))
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		nodeID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
		seen[nodeID] = struct{}{}
		runtime := probeRuntimeStatus{NodeID: nodeID, Online: false, System: probeSystemMetrics{}}
		if rt, ok := runtimeMap[nodeID]; ok {
			runtime = rt
		}
		out = append(out, probeNodeStatusRecord{NodeNo: node.NodeNo, NodeName: node.NodeName, ExpireAt: node.ExpireAt, Runtime: runtime})
	}

	for nodeID, rt := range runtimeMap {
		if _, ok := seen[nodeID]; ok {
			continue
		}
		if _, deleted := deletedNodeIDs[nodeID]; deleted {
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
		out = append(out, probeNodeStatusRecord{NodeNo: nodeNo, NodeName: name, ExpireAt: "", Runtime: rt})
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
		return probeNodeStatusRecord{NodeNo: node.NodeNo, NodeName: node.NodeName, ExpireAt: node.ExpireAt, Runtime: runtime}, true
	}
	if isDeletedProbeNodeIDLocked(normalizedID) {
		return probeNodeStatusRecord{}, false
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
		return probeNodeStatusRecord{NodeNo: nodeNo, NodeName: name, ExpireAt: "", Runtime: rt}, true
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

type probeNodeWithRuntime struct {
	probeNodeRecord
	Runtime *probeRuntimeStatus `json:"runtime,omitempty"`
}

func attachProbeRuntimeToNodes(nodes []probeNodeRecord) []probeNodeWithRuntime {
	if len(nodes) == 0 {
		return []probeNodeWithRuntime{}
	}
	runtimes := listProbeRuntimes()
	runtimeByNodeID := make(map[string]probeRuntimeStatus, len(runtimes))
	for _, rt := range runtimes {
		runtimeByNodeID[normalizeProbeNodeID(rt.NodeID)] = rt
	}
	out := make([]probeNodeWithRuntime, 0, len(nodes))
	for _, node := range nodes {
		item := probeNodeWithRuntime{probeNodeRecord: node}
		nodeID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
		if rt, ok := runtimeByNodeID[nodeID]; ok {
			runtime := rt
			item.Runtime = &runtime
		}
		out = append(out, item)
	}
	return out
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
	for _, item := range loadDeletedProbeNodesLocked() {
		if strings.EqualFold(strings.TrimSpace(item.NodeName), name) {
			return probeNodeRecord{}, fmt.Errorf("node name already exists in deleted probe list")
		}
	}

	deletedSet := loadDeletedProbeNodeNosLocked()
	nextNo := nextProbeNodeNo(nodes, deletedSet)

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
	delete(deletedSet, nextNo)
	saveDeletedProbeNodeNosLocked(deletedSet)

	for _, item := range normalized {
		if item.NodeNo == nextNo {
			return item, nil
		}
	}
	return probeNodeRecord{}, fmt.Errorf("failed to create node")
}

func deleteProbeNodeLocked(nodeNo int) (probeNodeRecord, []probeNodeRecord, []probeNodeRecord, error) {
	if nodeNo <= 0 {
		return probeNodeRecord{}, nil, nil, fmt.Errorf("invalid node number")
	}

	nodes := loadProbeNodesLocked()
	deletedNodes := loadDeletedProbeNodesLocked()
	for _, node := range deletedNodes {
		if node.NodeNo == nodeNo {
			return probeNodeRecord{}, nil, nil, fmt.Errorf("node %d is already deleted", nodeNo)
		}
	}

	found := -1
	for i := range nodes {
		if nodes[i].NodeNo == nodeNo {
			found = i
			break
		}
	}
	if found < 0 {
		return probeNodeRecord{}, nil, nil, fmt.Errorf("node %d not found", nodeNo)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	target := nodes[found]
	target.UpdatedAt = now
	activeNodes := append([]probeNodeRecord{}, nodes[:found]...)
	activeNodes = append(activeNodes, nodes[found+1:]...)
	deletedNodes = append(deletedNodes, target)
	sort.Slice(deletedNodes, func(i, j int) bool {
		return deletedNodes[i].NodeNo < deletedNodes[j].NodeNo
	})

	normalizedActive, secrets := normalizeProbeNodes(activeNodes)
	normalizedDeleted, _ := normalizeProbeNodes(deletedNodes)
	deletedSet := loadDeletedProbeNodeNosLocked()
	deletedSet[nodeNo] = struct{}{}
	ProbeStore.data.ProbeNodes = normalizedActive
	ProbeStore.data.DeletedProbeNodes = normalizedDeleted
	ProbeStore.data.ProbeSecrets = secrets
	saveDeletedProbeNodeNosLocked(deletedSet)
	return target, normalizedActive, normalizedDeleted, nil
}

func restoreDeletedProbeNodeLocked(nodeNo int) (probeNodeRecord, []probeNodeRecord, []probeNodeRecord, error) {
	if nodeNo <= 0 {
		return probeNodeRecord{}, nil, nil, fmt.Errorf("invalid node number")
	}

	nodes := loadProbeNodesLocked()
	for _, node := range nodes {
		if node.NodeNo == nodeNo {
			return probeNodeRecord{}, nil, nil, fmt.Errorf("node %d is already active", nodeNo)
		}
	}
	deletedNodes := loadDeletedProbeNodesLocked()
	found := -1
	for i := range deletedNodes {
		if deletedNodes[i].NodeNo == nodeNo {
			found = i
			break
		}
	}
	if found < 0 {
		return probeNodeRecord{}, nil, nil, fmt.Errorf("deleted node %d not found", nodeNo)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	target := deletedNodes[found]
	target.UpdatedAt = now
	activeNodes := append(nodes, target)
	sort.Slice(activeNodes, func(i, j int) bool {
		return activeNodes[i].NodeNo < activeNodes[j].NodeNo
	})
	nextDeletedNodes := append([]probeNodeRecord{}, deletedNodes[:found]...)
	nextDeletedNodes = append(nextDeletedNodes, deletedNodes[found+1:]...)

	normalizedActive, secrets := normalizeProbeNodes(activeNodes)
	normalizedDeleted, _ := normalizeProbeNodes(nextDeletedNodes)
	deletedSet := loadDeletedProbeNodeNosLocked()
	delete(deletedSet, nodeNo)
	ProbeStore.data.ProbeNodes = normalizedActive
	ProbeStore.data.DeletedProbeNodes = normalizedDeleted
	ProbeStore.data.ProbeSecrets = secrets
	saveDeletedProbeNodeNosLocked(deletedSet)
	return target, normalizedActive, normalizedDeleted, nil
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
	for _, item := range loadDeletedProbeNodesLocked() {
		if item.NodeNo == req.NodeNo {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.NodeName), name) {
			return probeNodeRecord{}, fmt.Errorf("node name already exists in deleted probe list")
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

func syncProbeNodesLocked(items []probeNodeRecord) ([]probeNodeRecord, error) {
	incomingNodes, _ := normalizeProbeNodes(items)
	existingNodes := loadProbeNodesLocked()
	deletedNodes := loadDeletedProbeNodesLocked()
	existingByNo := make(map[int]probeNodeRecord, len(existingNodes))
	for _, node := range existingNodes {
		if node.NodeNo > 0 {
			existingByNo[node.NodeNo] = node
		}
	}
	deletedByNo := make(map[int]probeNodeRecord, len(deletedNodes))
	for _, node := range deletedNodes {
		if node.NodeNo > 0 {
			deletedByNo[node.NodeNo] = node
		}
	}

	for _, node := range incomingNodes {
		if node.NodeNo <= 0 {
			continue
		}
		if _, ok := deletedByNo[node.NodeNo]; ok {
			return nil, fmt.Errorf("node_no %d is deleted; use restore action", node.NodeNo)
		}
		if _, ok := existingByNo[node.NodeNo]; !ok {
			return nil, fmt.Errorf("node_no %d is not assigned by controller; use create action", node.NodeNo)
		}
	}

	nodes := make([]probeNodeRecord, 0, len(incomingNodes))
	for _, node := range incomingNodes {
		existing := existingByNo[node.NodeNo]
		if strings.TrimSpace(node.CreatedAt) == "" {
			node.CreatedAt = strings.TrimSpace(existing.CreatedAt)
		}
		if strings.TrimSpace(node.NodeSecret) == "" {
			node.NodeSecret = strings.TrimSpace(existing.NodeSecret)
		}
		if strings.TrimSpace(node.UpdatedAt) == "" {
			node.UpdatedAt = strings.TrimSpace(existing.UpdatedAt)
		}
		nodes = append(nodes, node)
	}
	if len(nodes) == 0 {
		nodes = []probeNodeRecord{}
	}
	finalNodes, secrets := normalizeProbeNodes(nodes)

	deletedSet := loadDeletedProbeNodeNosLocked()
	incomingSet := make(map[int]struct{}, len(finalNodes))
	for _, node := range finalNodes {
		if node.NodeNo > 0 {
			incomingSet[node.NodeNo] = struct{}{}
		}
	}
	mergedDeletedNodes := append([]probeNodeRecord{}, deletedNodes...)
	deletedSeen := make(map[int]struct{}, len(mergedDeletedNodes))
	for _, node := range mergedDeletedNodes {
		if node.NodeNo > 0 {
			deletedSeen[node.NodeNo] = struct{}{}
			deletedSet[node.NodeNo] = struct{}{}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, oldNode := range existingNodes {
		if oldNode.NodeNo <= 0 {
			continue
		}
		if _, ok := incomingSet[oldNode.NodeNo]; ok {
			continue
		}
		deletedSet[oldNode.NodeNo] = struct{}{}
		if _, ok := deletedSeen[oldNode.NodeNo]; ok {
			continue
		}
		oldNode.UpdatedAt = now
		mergedDeletedNodes = append(mergedDeletedNodes, oldNode)
		deletedSeen[oldNode.NodeNo] = struct{}{}
	}
	for no := range incomingSet {
		delete(deletedSet, no)
	}
	normalizedDeletedNodes, _ := normalizeProbeNodes(mergedDeletedNodes)
	sort.Slice(normalizedDeletedNodes, func(i, j int) bool {
		return normalizedDeletedNodes[i].NodeNo < normalizedDeletedNodes[j].NodeNo
	})

	ProbeStore.data.ProbeNodes = finalNodes
	ProbeStore.data.DeletedProbeNodes = normalizedDeletedNodes
	ProbeStore.data.ProbeSecrets = secrets
	saveDeletedProbeNodeNosLocked(deletedSet)
	return finalNodes, nil
}

func loadDeletedProbeNodeNosLocked() map[int]struct{} {
	set := make(map[int]struct{})
	for _, no := range ProbeStore.data.DeletedProbeNodeNos {
		if no > 0 {
			set[no] = struct{}{}
		}
	}
	for _, node := range ProbeStore.data.DeletedProbeNodes {
		if node.NodeNo > 0 {
			set[node.NodeNo] = struct{}{}
		}
	}
	return set
}

func loadDeletedProbeNodeIDSetLocked() map[string]struct{} {
	set := make(map[string]struct{})
	for _, node := range loadDeletedProbeNodesLocked() {
		nodeID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
		if nodeID != "" {
			set[nodeID] = struct{}{}
		}
	}
	return set
}

func isDeletedProbeNodeIDLocked(nodeID string) bool {
	_, ok := loadDeletedProbeNodeIDSetLocked()[normalizeProbeNodeID(nodeID)]
	return ok
}

func saveDeletedProbeNodeNosLocked(set map[int]struct{}) {
	nos := make([]int, 0, len(set))
	for no := range set {
		if no > 0 {
			nos = append(nos, no)
		}
	}
	sort.Ints(nos)
	ProbeStore.data.DeletedProbeNodeNos = nos
}

func isDeletedProbeNodeID(nodeID string) bool {
	if ProbeStore == nil {
		return false
	}
	ProbeStore.mu.RLock()
	defer ProbeStore.mu.RUnlock()
	return isDeletedProbeNodeIDLocked(nodeID)
}

func nextProbeNodeNo(nodes []probeNodeRecord, deletedSet map[int]struct{}) int {
	nextNo := 1
	for _, item := range nodes {
		if item.NodeNo >= nextNo {
			nextNo = item.NodeNo + 1
		}
	}
	for no := range deletedSet {
		if no >= nextNo {
			nextNo = no + 1
		}
	}
	return nextNo
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
