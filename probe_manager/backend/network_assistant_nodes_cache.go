package backend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	chainCacheFileName = "probe_chain.json"
)

// chainCachePayload 是链路缓存的落盘格式。
type chainCachePayload struct {
	UpdatedAt    string                        `json:"updated_at"`
	Nodes        []string                      `json:"nodes"`
	ChainTargets map[string]chainCacheEndpoint `json:"chain_targets,omitempty"`
	// ProbeNodes 是从服务器同步的原始探针节点配置（node_no + ddns + service_host），不含实时状态
	ProbeNodes []chainCacheProbeNode `json:"probe_nodes,omitempty"`
}

// chainCacheProbeNode 是 probeNodeAdminItem 的可序列化镜像（仅静态配置字段）。
type chainCacheProbeNode struct {
	NodeNo                 int    `json:"node_no"`
	DDNS                   string `json:"ddns"`
	ServiceHost            string `json:"service_host"`
	BusinessDDNS           string `json:"business_ddns,omitempty"`
	BusinessDDNSFullDomain string `json:"business_ddns_full_domain,omitempty"`
}

// chainCacheEndpoint 是 probeChainEndpoint 的可序列化镜像（字段全部可导出）。
type chainCacheEndpoint struct {
	TargetID     string                  `json:"target_id"`
	ChainName    string                  `json:"chain_name"`
	ChainID      string                  `json:"chain_id"`
	EntryNode    string                  `json:"entry_node"`
	EntryHost    string                  `json:"entry_host"`
	EntryPort    int                     `json:"entry_port"`
	LinkLayer    string                  `json:"link_layer"`
	ChainSecret  string                  `json:"chain_secret"`
	PortForwards []chainCachePortForward `json:"port_forwards,omitempty"`
}

type chainCachePortForward struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ListenHost string `json:"listen_host"`
	ListenPort int    `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Network    string `json:"network"`
	Enabled    bool   `json:"enabled"`
}

func toChainCacheEndpoint(e probeChainEndpoint) chainCacheEndpoint {
	pfs := make([]chainCachePortForward, len(e.PortForwards))
	for i, pf := range e.PortForwards {
		pfs[i] = chainCachePortForward{
			ID:         pf.ID,
			Name:       pf.Name,
			ListenHost: pf.ListenHost,
			ListenPort: pf.ListenPort,
			TargetHost: pf.TargetHost,
			TargetPort: pf.TargetPort,
			Network:    pf.Network,
			Enabled:    pf.Enabled,
		}
	}
	return chainCacheEndpoint{
		TargetID:     e.TargetID,
		ChainName:    e.ChainName,
		ChainID:      e.ChainID,
		EntryNode:    e.EntryNode,
		EntryHost:    e.EntryHost,
		EntryPort:    e.EntryPort,
		LinkLayer:    e.LinkLayer,
		ChainSecret:  e.ChainSecret,
		PortForwards: pfs,
	}
}

func fromChainCacheEndpoint(e chainCacheEndpoint) probeChainEndpoint {
	pfs := make([]probeChainPortForward, len(e.PortForwards))
	for i, pf := range e.PortForwards {
		pfs[i] = probeChainPortForward{
			ID:         pf.ID,
			Name:       pf.Name,
			ListenHost: pf.ListenHost,
			ListenPort: pf.ListenPort,
			TargetHost: pf.TargetHost,
			TargetPort: pf.TargetPort,
			Network:    pf.Network,
			Enabled:    pf.Enabled,
		}
	}
	return probeChainEndpoint{
		TargetID:     e.TargetID,
		ChainName:    e.ChainName,
		ChainID:      e.ChainID,
		EntryNode:    e.EntryNode,
		EntryHost:    e.EntryHost,
		EntryPort:    e.EntryPort,
		LinkLayer:    e.LinkLayer,
		ChainSecret:  e.ChainSecret,
		PortForwards: pfs,
	}
}

func chainCacheFilePath() (string, error) {
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, chainCacheFileName), nil
}

// saveChainCacheToFile 将 refreshAvailableNodes 拉取到的节点列表、链路目标和探针节点列表持久化到本地。
func saveChainCacheToFile(nodes []string, chainTargets map[string]probeChainEndpoint, probeNodes []probeNodeAdminItem) error {
	path, err := chainCacheFilePath()
	if err != nil {
		return err
	}

	cacheEndpoints := make(map[string]chainCacheEndpoint, len(chainTargets))
	for k, v := range chainTargets {
		cacheEndpoints[k] = toChainCacheEndpoint(v)
	}

	cacheProbeNodes := make([]chainCacheProbeNode, 0, len(probeNodes))
	for _, n := range probeNodes {
		if n.NodeNo <= 0 {
			continue
		}
		cacheProbeNodes = append(cacheProbeNodes, chainCacheProbeNode{
			NodeNo:                 n.NodeNo,
			DDNS:                   n.DDNS,
			ServiceHost:            n.ServiceHost,
			BusinessDDNS:           n.BusinessDDNS,
			BusinessDDNSFullDomain: n.BusinessDDNSFullDomain,
		})
	}

	payload := chainCachePayload{
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

// loadChainCacheFromFile 从本地读取链路缓存。若文件不存在或数据为空则返回 nil, nil, nil, nil。
func loadChainCacheFromFile() (nodes []string, chainTargets map[string]probeChainEndpoint, probeNodes []probeNodeAdminItem, err error) {
	path, pathErr := chainCacheFilePath()
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

	var payload chainCachePayload
	if unmarshalErr := json.Unmarshal(raw, &payload); unmarshalErr != nil {
		return nil, nil, nil, unmarshalErr
	}

	targets := make(map[string]probeChainEndpoint, len(payload.ChainTargets))
	for k, v := range payload.ChainTargets {
		targets[k] = fromChainCacheEndpoint(v)
	}

	adminNodes := make([]probeNodeAdminItem, 0, len(payload.ProbeNodes))
	for _, n := range payload.ProbeNodes {
		if n.NodeNo <= 0 {
			continue
		}
		adminNodes = append(adminNodes, probeNodeAdminItem{
			NodeNo:                 n.NodeNo,
			DDNS:                   n.DDNS,
			ServiceHost:            n.ServiceHost,
			BusinessDDNS:           n.BusinessDDNS,
			BusinessDDNSFullDomain: n.BusinessDDNSFullDomain,
		})
	}

	nodeSet := make(map[string]struct{}, len(payload.Nodes)+len(targets)+len(adminNodes))
	outNodes := make([]string, 0, len(payload.Nodes)+len(targets)+len(adminNodes))
	addNode := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, exists := nodeSet[id]; exists {
			return
		}
		nodeSet[id] = struct{}{}
		outNodes = append(outNodes, id)
	}

	for _, id := range payload.Nodes {
		addNode(id)
	}
	// 兼容旧缓存：若 nodes 为空，尝试从 chain_targets 回填可选节点。
	if len(outNodes) == 0 {
		for nodeID := range targets {
			addNode(nodeID)
		}
	}
	// 再次兜底：从 probe_nodes 回填节点编号。
	if len(outNodes) == 0 {
		for _, item := range adminNodes {
			if item.NodeNo <= 0 {
				continue
			}
			addNode(strconv.Itoa(item.NodeNo))
		}
	}

	if len(outNodes) == 0 && len(targets) == 0 && len(adminNodes) == 0 {
		return nil, nil, nil, nil
	}
	return outNodes, targets, adminNodes, nil
}
