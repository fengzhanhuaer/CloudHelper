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
	chainCacheFileName = "probe_chain.json"
)

// chainCachePayload 是链路缓存的落盘格式。
type chainCachePayload struct {
	UpdatedAt    string                        `json:"updated_at"`
	Nodes        []string                      `json:"nodes"`
	ChainTargets map[string]chainCacheEndpoint `json:"chain_targets,omitempty"`
}

// chainCacheEndpoint 是 probeChainEndpoint 的可序列化镜像（字段全部可导出）。
type chainCacheEndpoint struct {
	TargetID       string                `json:"target_id"`
	ChainName      string                `json:"chain_name"`
	ChainID        string                `json:"chain_id"`
	UserID         string                `json:"user_id"`
	UserPublicKey  string                `json:"user_public_key"`
	EntryNode      string                `json:"entry_node"`
	ExitNode       string                `json:"exit_node"`
	CascadeNodeIDs []string              `json:"cascade_node_ids,omitempty"`
	ListenHost     string                `json:"listen_host"`
	ListenPort     int                   `json:"listen_port"`
	EgressHost     string                `json:"egress_host"`
	EgressPort     int                   `json:"egress_port"`
	CreatedAt      string                `json:"created_at"`
	UpdatedAt      string                `json:"updated_at"`
	EntryHost      string                `json:"entry_host"`
	EntryPort      int                   `json:"entry_port"`
	LinkLayer      string                `json:"link_layer"`
	ChainSecret    string                `json:"chain_secret"`
	HopConfigs     []chainCacheHopConfig `json:"hop_configs,omitempty"`
	PortForwards   []chainCachePortForward `json:"port_forwards,omitempty"`
}

type chainCacheHopConfig struct {
	NodeNo       int    `json:"node_no"`
	ListenHost   string `json:"listen_host,omitempty"`
	ListenPort   int    `json:"listen_port,omitempty"`
	ExternalPort int    `json:"external_port,omitempty"`
	LinkLayer    string `json:"link_layer"`
	DialMode     string `json:"dial_mode,omitempty"`
	RelayHost    string `json:"relay_host,omitempty"`
}

type chainCachePortForward struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	EntrySide  string `json:"entry_side,omitempty"`
	ListenHost string `json:"listen_host"`
	ListenPort int    `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Network    string `json:"network"`
	Enabled    bool   `json:"enabled"`
}

func toChainCacheEndpoint(e probeChainEndpoint) chainCacheEndpoint {
	hops := make([]chainCacheHopConfig, len(e.HopConfigs))
	for i, hop := range e.HopConfigs {
		hops[i] = chainCacheHopConfig{
			NodeNo:       hop.NodeNo,
			ListenHost:   hop.ListenHost,
			ListenPort:   hop.ListenPort,
			ExternalPort: hop.ExternalPort,
			LinkLayer:    hop.LinkLayer,
			DialMode:     hop.DialMode,
			RelayHost:    hop.RelayHost,
		}
	}
	pfs := make([]chainCachePortForward, len(e.PortForwards))
	for i, pf := range e.PortForwards {
		pfs[i] = chainCachePortForward{
			ID:         pf.ID,
			Name:       pf.Name,
			EntrySide:  pf.EntrySide,
			ListenHost: pf.ListenHost,
			ListenPort: pf.ListenPort,
			TargetHost: pf.TargetHost,
			TargetPort: pf.TargetPort,
			Network:    pf.Network,
			Enabled:    pf.Enabled,
		}
	}
	return chainCacheEndpoint{
		TargetID:       e.TargetID,
		ChainName:      e.ChainName,
		ChainID:        e.ChainID,
		UserID:         e.UserID,
		UserPublicKey:  e.UserPublicKey,
		EntryNode:      e.EntryNode,
		ExitNode:       e.ExitNode,
		CascadeNodeIDs: append([]string(nil), e.CascadeNodeIDs...),
		ListenHost:     e.ListenHost,
		ListenPort:     e.ListenPort,
		EgressHost:     e.EgressHost,
		EgressPort:     e.EgressPort,
		CreatedAt:      e.CreatedAt,
		UpdatedAt:      e.UpdatedAt,
		EntryHost:      e.EntryHost,
		EntryPort:      e.EntryPort,
		LinkLayer:      e.LinkLayer,
		ChainSecret:    e.ChainSecret,
		HopConfigs:     hops,
		PortForwards:   pfs,
	}
}

func fromChainCacheEndpoint(e chainCacheEndpoint) probeChainEndpoint {
	hops := make([]probeChainHopConfig, len(e.HopConfigs))
	for i, hop := range e.HopConfigs {
		hops[i] = probeChainHopConfig{
			NodeNo:       hop.NodeNo,
			ListenHost:   hop.ListenHost,
			ListenPort:   hop.ListenPort,
			ExternalPort: hop.ExternalPort,
			LinkLayer:    hop.LinkLayer,
			DialMode:     hop.DialMode,
			RelayHost:    hop.RelayHost,
		}
	}
	pfs := make([]probeChainPortForward, len(e.PortForwards))
	for i, pf := range e.PortForwards {
		pfs[i] = probeChainPortForward{
			ID:         pf.ID,
			Name:       pf.Name,
			EntrySide:  pf.EntrySide,
			ListenHost: pf.ListenHost,
			ListenPort: pf.ListenPort,
			TargetHost: pf.TargetHost,
			TargetPort: pf.TargetPort,
			Network:    pf.Network,
			Enabled:    pf.Enabled,
		}
	}
	return probeChainEndpoint{
		TargetID:       e.TargetID,
		ChainName:      e.ChainName,
		ChainID:        e.ChainID,
		UserID:         e.UserID,
		UserPublicKey:  e.UserPublicKey,
		EntryNode:      e.EntryNode,
		ExitNode:       e.ExitNode,
		CascadeNodeIDs: append([]string(nil), e.CascadeNodeIDs...),
		ListenHost:     e.ListenHost,
		ListenPort:     e.ListenPort,
		EgressHost:     e.EgressHost,
		EgressPort:     e.EgressPort,
		CreatedAt:      e.CreatedAt,
		UpdatedAt:      e.UpdatedAt,
		EntryHost:      e.EntryHost,
		EntryPort:      e.EntryPort,
		LinkLayer:      e.LinkLayer,
		ChainSecret:    e.ChainSecret,
		HopConfigs:     hops,
		PortForwards:   pfs,
	}
}

func chainCacheFilePath() (string, error) {
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, chainCacheFileName), nil
}

// saveChainCacheToFile 将 refreshAvailableNodes 拉取到的节点列表和链路目标持久化到本地。
func saveChainCacheToFile(nodes []string, chainTargets map[string]probeChainEndpoint) error {
	path, err := chainCacheFilePath()
	if err != nil {
		return err
	}

	cacheEndpoints := make(map[string]chainCacheEndpoint, len(chainTargets))
	for k, v := range chainTargets {
		cacheEndpoints[k] = toChainCacheEndpoint(v)
	}

	payload := chainCachePayload{
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
		Nodes:        nodes,
		ChainTargets: cacheEndpoints,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

// loadChainCacheFromFile 从本地读取链路缓存。若文件不存在或数据为空则返回 nil, nil, nil。
func loadChainCacheFromFile() (nodes []string, chainTargets map[string]probeChainEndpoint, err error) {
	path, pathErr := chainCacheFilePath()
	if pathErr != nil {
		return nil, nil, pathErr
	}

	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, readErr
	}

	if strings.TrimSpace(string(raw)) == "" {
		return nil, nil, nil
	}

	var payload chainCachePayload
	if unmarshalErr := json.Unmarshal(raw, &payload); unmarshalErr != nil {
		return nil, nil, unmarshalErr
	}

	targets := make(map[string]probeChainEndpoint, len(payload.ChainTargets))
	for k, v := range payload.ChainTargets {
		targets[k] = fromChainCacheEndpoint(v)
	}

	nodeSet := make(map[string]struct{}, len(payload.Nodes)+len(targets))
	outNodes := make([]string, 0, len(payload.Nodes)+len(targets))
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
	// 兼容旧缓存：若 nodes 为空，尝试从 chain_targets 回填可选链路目标。
	if len(outNodes) == 0 {
		for nodeID := range targets {
			addNode(nodeID)
		}
	}

	if len(outNodes) == 0 && len(targets) == 0 {
		return nil, nil, nil
	}
	return outNodes, targets, nil
}
