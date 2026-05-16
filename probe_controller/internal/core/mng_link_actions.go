package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

func getMngProbeLinkUsers() (map[string]interface{}, error) {
	return map[string]interface{}{
		"users": listProbeLinkUserIdentities(),
	}, nil
}

func getMngProbeLinkUserPublicKey(payload json.RawMessage) (map[string]interface{}, error) {
	var req struct {
		Username string `json:"username"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
	}
	user, publicKey, err := resolveProbeLinkUserIdentityAndPublicKey(req.Username)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"username":   strings.TrimSpace(user.Username),
		"user_role":  strings.TrimSpace(user.UserRole),
		"cert_type":  strings.TrimSpace(user.CertType),
		"public_key": strings.TrimSpace(publicKey),
	}, nil
}

func listMngProbeLinkChains() (map[string]interface{}, error) {
	if ProbeLinkChainStore == nil {
		return map[string]interface{}{"items": []probeLinkChainRecord{}}, nil
	}
	ProbeLinkChainStore.mu.RLock()
	items := loadProbeLinkChainsLocked()
	ProbeLinkChainStore.mu.RUnlock()
	items = annotateProbeLinkChainAvailability(fillChainRelayHosts(items))
	return map[string]interface{}{
		"items": items,
	}, nil
}

func upsertMngProbeLinkChain(payload json.RawMessage, controllerBaseURL string) (map[string]interface{}, error) {
	var req struct {
		ChainID        string   `json:"chain_id"`
		Name           string   `json:"name"`
		ChainType      string   `json:"chain_type"`
		UserID         string   `json:"user_id"`
		UserPublicKey  string   `json:"user_public_key"`
		Secret         string   `json:"secret"`
		EntryNodeID    string   `json:"entry_node_id"`
		ExitNodeID     string   `json:"exit_node_id"`
		CascadeNodeIDs []string `json:"cascade_node_ids"`
		ListenHost     string   `json:"listen_host"`
		ListenPort     int      `json:"listen_port"`
		LinkLayer      string   `json:"link_layer"`
		HopConfigs     []struct {
			NodeNo       int    `json:"node_no"`
			ListenHost   string `json:"listen_host"`
			ListenPort   int    `json:"listen_port"`
			ServicePort  int    `json:"service_port"`
			ExternalPort int    `json:"external_port"`
			LinkLayer    string `json:"link_layer"`
			DialMode     string `json:"dial_mode"`
			RelayHost    string `json:"relay_host"`
		} `json:"hop_configs"`
		PortForwards []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			EntrySide  string `json:"entry_side"`
			ListenHost string `json:"listen_host"`
			ListenPort int    `json:"listen_port"`
			TargetHost string `json:"target_host"`
			TargetPort int    `json:"target_port"`
			Network    string `json:"network"`
			Enabled    bool   `json:"enabled"`
		} `json:"port_forwards"`
		EgressHost string `json:"egress_host"`
		EgressPort int    `json:"egress_port"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload")
	}
	if ProbeLinkChainStore == nil {
		return nil, fmt.Errorf("probe link chain store is not initialized")
	}

	var previous probeLinkChainRecord
	var hadPrevious bool
	ProbeLinkChainStore.mu.Lock()
	if strings.TrimSpace(req.ChainID) != "" {
		if item, ok := findProbeLinkChainByIDLocked(req.ChainID); ok {
			previous = item
			hadPrevious = true
		}
	}
	item, items, err := upsertProbeLinkChainLocked(probeLinkChainRecord{
		ChainID:        strings.TrimSpace(req.ChainID),
		Name:           strings.TrimSpace(req.Name),
		ChainType:      strings.TrimSpace(req.ChainType),
		UserID:         strings.TrimSpace(req.UserID),
		UserPublicKey:  strings.TrimSpace(req.UserPublicKey),
		Secret:         strings.TrimSpace(req.Secret),
		EntryNodeID:    strings.TrimSpace(req.EntryNodeID),
		ExitNodeID:     strings.TrimSpace(req.ExitNodeID),
		CascadeNodeIDs: req.CascadeNodeIDs,
		ListenHost:     strings.TrimSpace(req.ListenHost),
		ListenPort:     req.ListenPort,
		LinkLayer:      strings.TrimSpace(req.LinkLayer),
		HopConfigs:     buildMngProbeLinkHopConfigs(req.HopConfigs),
		PortForwards:   buildMngProbeLinkPortForwards(req.PortForwards),
		EgressHost:     strings.TrimSpace(req.EgressHost),
		EgressPort:     req.EgressPort,
	})
	ProbeLinkChainStore.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := ProbeLinkChainStore.Save(); err != nil {
		return nil, err
	}

	applyErrorText := ""
	if hadPrevious && strings.TrimSpace(previous.ChainID) != "" {
		if err := removeProbeLinkChainRecord(previous); err != nil {
			applyErrorText = err.Error()
		}
	}
	if err := applyProbeLinkChainRecord(item, controllerBaseURL); err != nil {
		if applyErrorText == "" {
			applyErrorText = err.Error()
		} else {
			applyErrorText = applyErrorText + "; " + err.Error()
		}
	}
	return map[string]interface{}{
		"item":        item,
		"items":       items,
		"apply_ok":    strings.TrimSpace(applyErrorText) == "",
		"apply_error": strings.TrimSpace(applyErrorText),
	}, nil
}

func deleteMngProbeLinkChain(payload json.RawMessage) (map[string]interface{}, error) {
	var req struct {
		ChainID string `json:"chain_id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload")
	}
	if ProbeLinkChainStore == nil {
		return nil, fmt.Errorf("probe link chain store is not initialized")
	}
	ProbeLinkChainStore.mu.Lock()
	removed, items, err := removeProbeLinkChainLocked(req.ChainID)
	ProbeLinkChainStore.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := ProbeLinkChainStore.Save(); err != nil {
		return nil, err
	}
	applyErrorText := ""
	if err := removeProbeLinkChainRecord(removed); err != nil {
		applyErrorText = err.Error()
	}
	return map[string]interface{}{
		"removed":     removed,
		"items":       items,
		"apply_ok":    strings.TrimSpace(applyErrorText) == "",
		"apply_error": strings.TrimSpace(applyErrorText),
	}, nil
}

func buildMngProbeLinkHopConfigs(items []struct {
	NodeNo       int    `json:"node_no"`
	ListenHost   string `json:"listen_host"`
	ListenPort   int    `json:"listen_port"`
	ServicePort  int    `json:"service_port"`
	ExternalPort int    `json:"external_port"`
	LinkLayer    string `json:"link_layer"`
	DialMode     string `json:"dial_mode"`
	RelayHost    string `json:"relay_host"`
}) []probeLinkChainHopConfig {
	out := make([]probeLinkChainHopConfig, 0, len(items))
	for _, cfg := range items {
		listenPort := cfg.ListenPort
		if listenPort <= 0 {
			listenPort = cfg.ServicePort
		}
		out = append(out, probeLinkChainHopConfig{
			NodeNo:       cfg.NodeNo,
			ListenHost:   strings.TrimSpace(cfg.ListenHost),
			ListenPort:   listenPort,
			ExternalPort: cfg.ExternalPort,
			LinkLayer:    strings.TrimSpace(cfg.LinkLayer),
			DialMode:     strings.TrimSpace(cfg.DialMode),
			RelayHost:    strings.TrimSpace(cfg.RelayHost),
		})
	}
	return out
}

func buildMngProbeLinkPortForwards(items []struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	EntrySide  string `json:"entry_side"`
	ListenHost string `json:"listen_host"`
	ListenPort int    `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Network    string `json:"network"`
	Enabled    bool   `json:"enabled"`
}) []probeLinkChainPortForwardConfig {
	out := make([]probeLinkChainPortForwardConfig, 0, len(items))
	for _, item := range items {
		out = append(out, probeLinkChainPortForwardConfig{
			ID:         strings.TrimSpace(item.ID),
			Name:       strings.TrimSpace(item.Name),
			EntrySide:  strings.TrimSpace(item.EntrySide),
			ListenHost: strings.TrimSpace(item.ListenHost),
			ListenPort: item.ListenPort,
			TargetHost: strings.TrimSpace(item.TargetHost),
			TargetPort: item.TargetPort,
			Network:    strings.TrimSpace(item.Network),
			Enabled:    item.Enabled,
		})
	}
	return out
}
