package backend

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const probeNodesStoreFile = "probe_nodes.json"

type ProbeNode struct {
	NodeNo        int    `json:"node_no"`
	NodeName      string `json:"node_name"`
	Remark        string `json:"remark"`
	NodeSecret    string `json:"node_secret"`
	TargetSystem  string `json:"target_system"`
	DirectConnect bool   `json:"direct_connect"`
	PaymentCycle  string `json:"payment_cycle"`
	Cost          string `json:"cost"`
	ExpireAt      string `json:"expire_at"`
	VendorName    string `json:"vendor_name"`
	VendorURL     string `json:"vendor_url"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func (a *App) GetProbeNodes() ([]ProbeNode, error) {
	nodes, _, err := loadProbeNodes()
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

func (a *App) CreateProbeNode(nodeName string) (ProbeNode, error) {
	name := strings.TrimSpace(nodeName)

	nodes, storePath, err := loadProbeNodes()
	if err != nil {
		return ProbeNode{}, err
	}

	for _, item := range nodes {
		if strings.EqualFold(strings.TrimSpace(item.NodeName), name) {
			return ProbeNode{}, fmt.Errorf("node name already exists")
		}
	}

	nextNo := 1
	for _, item := range nodes {
		if item.NodeNo >= nextNo {
			nextNo = item.NodeNo + 1
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	node := ProbeNode{
		NodeNo:        nextNo,
		NodeName:      name,
		Remark:        "",
		NodeSecret:    randomSecret(32),
		TargetSystem:  "linux",
		DirectConnect: true,
		PaymentCycle:  "",
		Cost:          "",
		ExpireAt:      "",
		VendorName:    "",
		VendorURL:     "",
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	nodes = append(nodes, node)
	if err := writeProbeNodes(storePath, nodes); err != nil {
		return ProbeNode{}, err
	}
	return node, nil
}

func (a *App) UpdateProbeNode(nodeNo int, targetSystem string, directConnect bool) (ProbeNode, error) {
	return a.UpdateProbeNodeSettings(
		nodeNo,
		"",
		"",
		targetSystem,
		directConnect,
		"",
		"",
		"",
		"",
		"",
	)
}

func (a *App) UpdateProbeNodeSettings(
	nodeNo int,
	nodeName string,
	remark string,
	targetSystem string,
	directConnect bool,
	paymentCycle string,
	cost string,
	expireAt string,
	vendorName string,
	vendorURL string,
) (ProbeNode, error) {
	if nodeNo <= 0 {
		return ProbeNode{}, fmt.Errorf("invalid node number")
	}

	name := strings.TrimSpace(nodeName)
	if name == "" {
		return ProbeNode{}, fmt.Errorf("node name is required")
	}

	system := strings.ToLower(strings.TrimSpace(targetSystem))
	if system != "linux" && system != "windows" {
		return ProbeNode{}, fmt.Errorf("target system must be linux or windows")
	}

	nodes, storePath, err := loadProbeNodes()
	if err != nil {
		return ProbeNode{}, err
	}

	for _, item := range nodes {
		if name == "" && item.NodeNo == nodeNo {
			name = strings.TrimSpace(item.NodeName)
		}
		if item.NodeNo == nodeNo {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.NodeName), name) {
			return ProbeNode{}, fmt.Errorf("node name already exists")
		}
	}
	if name == "" {
		return ProbeNode{}, fmt.Errorf("node name is required")
	}

	for idx := range nodes {
		if nodes[idx].NodeNo != nodeNo {
			continue
		}

		nodes[idx].NodeName = name
		nodes[idx].Remark = strings.TrimSpace(remark)
		nodes[idx].TargetSystem = system
		nodes[idx].DirectConnect = directConnect
		nodes[idx].PaymentCycle = strings.TrimSpace(paymentCycle)
		nodes[idx].Cost = strings.TrimSpace(cost)
		nodes[idx].ExpireAt = strings.TrimSpace(expireAt)
		nodes[idx].VendorName = strings.TrimSpace(vendorName)
		nodes[idx].VendorURL = strings.TrimSpace(vendorURL)
		nodes[idx].UpdatedAt = time.Now().UTC().Format(time.RFC3339)

		if err := writeProbeNodes(storePath, nodes); err != nil {
			return ProbeNode{}, err
		}
		return nodes[idx], nil
	}

	return ProbeNode{}, fmt.Errorf("node %d not found", nodeNo)
}

func (a *App) ReplaceProbeNodes(nodes []ProbeNode) ([]ProbeNode, error) {
	_, storePath, err := loadProbeNodes()
	if err != nil {
		return nil, err
	}

	normalized := normalizeProbeNodes(nodes)
	if err := writeProbeNodes(storePath, normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func loadProbeNodes() ([]ProbeNode, string, error) {
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return nil, "", err
	}
	storePath := filepath.Join(dataDir, probeNodesStoreFile)

	raw, err := os.ReadFile(storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []ProbeNode{}, storePath, nil
		}
		return nil, storePath, err
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return []ProbeNode{}, storePath, nil
	}

	var nodes []ProbeNode
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return nil, storePath, fmt.Errorf("failed to parse probe nodes: %w", err)
	}

	return normalizeProbeNodes(nodes), storePath, nil
}

func writeProbeNodes(storePath string, nodes []ProbeNode) error {
	raw, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(storePath, raw, 0o644); err != nil {
		return err
	}
	if err := autoBackupManagerData(); err != nil {
		return err
	}
	return nil
}

func randomSecret(length int) string {
	if length <= 0 {
		return ""
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("node-secret-%d", time.Now().UnixNano())
	}

	out := make([]byte, length)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}

func normalizeProbeNodes(nodes []ProbeNode) []ProbeNode {
	if len(nodes) == 0 {
		return []ProbeNode{}
	}

	seenNo := map[int]struct{}{}
	seenName := map[string]struct{}{}
	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]ProbeNode, 0, len(nodes))

	for _, item := range nodes {
		if item.NodeNo <= 0 {
			continue
		}
		name := strings.TrimSpace(item.NodeName)
		if name == "" {
			continue
		}
		nameKey := strings.ToLower(name)
		if _, ok := seenNo[item.NodeNo]; ok {
			continue
		}
		if _, ok := seenName[nameKey]; ok {
			continue
		}
		seenNo[item.NodeNo] = struct{}{}
		seenName[nameKey] = struct{}{}

		node := item
		node.NodeName = name
		node.Remark = strings.TrimSpace(node.Remark)
		node.NodeSecret = strings.TrimSpace(node.NodeSecret)
		if node.NodeSecret == "" {
			node.NodeSecret = randomSecret(32)
		}
		node.TargetSystem = strings.ToLower(strings.TrimSpace(node.TargetSystem))
		if node.TargetSystem != "windows" {
			node.TargetSystem = "linux"
		}
		node.PaymentCycle = strings.TrimSpace(node.PaymentCycle)
		node.Cost = strings.TrimSpace(node.Cost)
		node.ExpireAt = strings.TrimSpace(node.ExpireAt)
		node.VendorName = strings.TrimSpace(node.VendorName)
		node.VendorURL = strings.TrimSpace(node.VendorURL)
		if strings.TrimSpace(node.CreatedAt) == "" {
			node.CreatedAt = now
		}
		if strings.TrimSpace(node.UpdatedAt) == "" {
			node.UpdatedAt = node.CreatedAt
		}
		out = append(out, node)
	}

	return out
}
