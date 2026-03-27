package backend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	nodesCacheFileName = "probe_nodes_cache.json"
)

// nodesCachePayload 是节点缓存的落盘格式。
type nodesCachePayload struct {
	UpdatedAt    string                        `json:"updated_at"`
	Nodes        []string                      `json:"nodes"`
	ChainTargets map[string]nodesCacheEndpoint `json:"chain_targets,omitempty"`
	// ProbeNodes 是从服务器同步的原始探针节点配置（node_no + ddns + service_host），不含实时状态
	ProbeNodes []nodesCacheProbeNode `json:"probe_nodes,omitempty"`
}

// nodesCacheProbeNode 是 probeNodeAdminItem 的可序列化镜像（仅静态配置字段）。
type nodesCacheProbeNode struct {
	NodeNo      int    `json:"node_no"`
	DDNS        string `json:"ddns"`
	ServiceHost string `json:"service_host"`
}

// nodesCacheEndpoint 是 probeChainEndpoint 的可序列化镜像（字段全部可导出）。
type nodesCacheEndpoint struct {
	TargetID    string `json:"target_id"`
	ChainName   string `json:"chain_name"`
	ChainID     string `json:"chain_id"`
	EntryNode   string `json:"entry_node"`
	EntryHost   string `json:"entry_host"`
	EntryPort   int    `json:"entry_port"`
	LinkLayer   string `json:"link_layer"`
	ChainSecret string `json:"chain_secret"`
}

func toNodesCacheEndpoint(e probeChainEndpoint) nodesCacheEndpoint {
	return nodesCacheEndpoint{
		TargetID:    e.TargetID,
		ChainName:   e.ChainName,
		ChainID:     e.ChainID,
		EntryNode:   e.EntryNode,
		EntryHost:   e.EntryHost,
		EntryPort:   e.EntryPort,
		LinkLayer:   e.LinkLayer,
		ChainSecret: e.ChainSecret,
	}
}

func fromNodesCacheEndpoint(e nodesCacheEndpoint) probeChainEndpoint {
	return probeChainEndpoint{
		TargetID:    e.TargetID,
		ChainName:   e.ChainName,
		ChainID:     e.ChainID,
		EntryNode:   e.EntryNode,
		EntryHost:   e.EntryHost,
		EntryPort:   e.EntryPort,
		LinkLayer:   e.LinkLayer,
		ChainSecret: e.ChainSecret,
	}
}

func nodesCacheFilePath() (string, error) {
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, nodesCacheFileName), nil
}

// saveNodesCacheToFile 将 refreshAvailableNodes 拉取到的节点列表、链路目标和探针节点列表持久化到本地。
func saveNodesCacheToFile(nodes []string, chainTargets map[string]probeChainEndpoint, probeNodes []probeNodeAdminItem) error {
	path, err := nodesCacheFilePath()
	if err != nil {
		return err
	}

	cacheEndpoints := make(map[string]nodesCacheEndpoint, len(chainTargets))
	for k, v := range chainTargets {
		cacheEndpoints[k] = toNodesCacheEndpoint(v)
	}

	cacheProbeNodes := make([]nodesCacheProbeNode, 0, len(probeNodes))
	for _, n := range probeNodes {
		if n.NodeNo <= 0 {
			continue
		}
		cacheProbeNodes = append(cacheProbeNodes, nodesCacheProbeNode{
			NodeNo:      n.NodeNo,
			DDNS:        n.DDNS,
			ServiceHost: n.ServiceHost,
		})
	}

	payload := nodesCachePayload{
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
		Nodes:        nodes,
		ChainTargets: cacheEndpoints,
		ProbeNodes:   cacheProbeNodes,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

// loadNodesCacheFromFile 从本地读取节点缓存。若文件不存在或数据为空则返回 nil, nil, nil, nil。
func loadNodesCacheFromFile() (nodes []string, chainTargets map[string]probeChainEndpoint, probeNodes []probeNodeAdminItem, err error) {
	path, pathErr := nodesCacheFilePath()
	if pathErr != nil {
		return nil, nil, nil, pathErr
	}

	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, readErr
	}

	if strings.TrimSpace(string(raw)) == "" {
		return nil, nil, nil, nil
	}

	var payload nodesCachePayload
	if unmarshalErr := json.Unmarshal(raw, &payload); unmarshalErr != nil {
		return nil, nil, nil, unmarshalErr
	}

	if len(payload.Nodes) == 0 {
		return nil, nil, nil, nil
	}

	targets := make(map[string]probeChainEndpoint, len(payload.ChainTargets))
	for k, v := range payload.ChainTargets {
		targets[k] = fromNodesCacheEndpoint(v)
	}

	adminNodes := make([]probeNodeAdminItem, 0, len(payload.ProbeNodes))
	for _, n := range payload.ProbeNodes {
		if n.NodeNo <= 0 {
			continue
		}
		adminNodes = append(adminNodes, probeNodeAdminItem{
			NodeNo:      n.NodeNo,
			DDNS:        n.DDNS,
			ServiceHost: n.ServiceHost,
		})
	}

	return payload.Nodes, targets, adminNodes, nil
}
