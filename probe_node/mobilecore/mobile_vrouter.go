package mobilecore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	mobileVRouteConfigAPIPath       = "/api/probe/route/config"
	mobileVRouteConfigFileName      = "probe_route_config.json"
	mobileVRouteConfigFetchTimeout  = 15 * time.Second
	mobileVRouteProbeExitRouteIDPre = "vroute:"
)

type mobileVRouteConfigResponse struct {
	NodeID        string             `json:"node_id"`
	VirtualRouter mobileVRouteConfig `json:"virtual_router,omitempty"`
}

type mobileVRouteConfigCacheFile struct {
	UpdatedAt string             `json:"updated_at"`
	Item      mobileVRouteConfig `json:"item"`
}

type mobileVRouteConfig struct {
	LocalNodeID   string                  `json:"local_node_id,omitempty"`
	Enabled       bool                    `json:"enabled"`
	FakeIPCIDR    string                  `json:"fake_ip_cidr,omitempty"`
	ProbeIPs      []mobileVRouteProbeIP   `json:"probe_ips,omitempty"`
	TopologyRules []mobileVRouteTopology  `json:"topology_rules,omitempty"`
	RouteRules    []mobileVRouteRouteRule `json:"route_rules,omitempty"`
	UpdatedAt     string                  `json:"updated_at,omitempty"`
}

type mobileVRouteProbeIP struct {
	NodeID      string `json:"node_id"`
	IP          string `json:"ip"`
	ServicePort int    `json:"service_port,omitempty"`
	Note        string `json:"note,omitempty"`
}

type mobileVRouteTopology struct {
	ID                string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	FromNodeID        string `json:"from_node_id"`
	ToNodeID          string `json:"to_node_id"`
	Direction         string `json:"direction"`
	FromServiceDomain string `json:"from_service_domain,omitempty"`
	FromServicePort   int    `json:"from_service_port,omitempty"`
	ToServiceDomain   string `json:"to_service_domain,omitempty"`
	ToServicePort     int    `json:"to_service_port,omitempty"`
	RouteLayer        string `json:"route_layer,omitempty"`
	UserID            string `json:"user_id,omitempty"`
	UserPublicKey     string `json:"user_public_key,omitempty"`
	Secret            string `json:"secret,omitempty"`
	AuthTicket        string `json:"auth_ticket,omitempty"`
	Enabled           bool   `json:"enabled"`
	Note              string `json:"note,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

type mobileVRouteRouteRule struct {
	ID         string   `json:"id,omitempty"`
	Name       string   `json:"name"`
	Action     string   `json:"action,omitempty"`
	ExitNodeID string   `json:"exit_node_id,omitempty"`
	Entries    []string `json:"entries,omitempty"`
	Note       string   `json:"note,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
}

type mobileVRouteDecision struct {
	Matched    bool
	Direct     bool
	Reject     bool
	TargetAddr string
	Group      string
	RuleID     string
	RuleName   string
	ExitNodeID string
}

func refreshMobileVRouteConfig(controllerURL string, nodeID string, nodeSecret string, configDir string) (mobileVRouteConfig, error) {
	baseURL, err := normalizeControllerBaseURL(controllerURL)
	if err != nil {
		return mobileVRouteConfig{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mobileVRouteConfigFetchTimeout)
	defer cancel()
	config, err := fetchMobileVRouteConfig(ctx, baseURL, nodeID, nodeSecret)
	if err != nil {
		return mobileVRouteConfig{}, err
	}
	if err := persistMobileVRouteConfig(configDir, config); err != nil {
		return mobileVRouteConfig{}, err
	}
	return config, nil
}

func fetchMobileVRouteConfig(ctx context.Context, controllerURL string, nodeID string, nodeSecret string) (mobileVRouteConfig, error) {
	baseURL, err := normalizeControllerBaseURL(controllerURL)
	if err != nil {
		return mobileVRouteConfig{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+mobileVRouteConfigAPIPath, nil)
	if err != nil {
		return mobileVRouteConfig{}, err
	}
	applyAuthHeaders(req, nodeID, nodeSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return mobileVRouteConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return mobileVRouteConfig{}, fmt.Errorf("request vroute config failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload mobileVRouteConfigResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return mobileVRouteConfig{}, err
	}
	config := sanitizeMobileVRouteConfig(payload.VirtualRouter)
	if config.LocalNodeID == "" {
		config.LocalNodeID = normalizeMobileRouteNodeID(payload.NodeID)
	}
	return config, nil
}

func persistMobileVRouteConfig(configDir string, config mobileVRouteConfig) error {
	path, ok := mobileVRouteConfigPath(configDir)
	if !ok {
		return errors.New("config dir is required")
	}
	payload := mobileVRouteConfigCacheFile{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Item:      sanitizeMobileVRouteConfig(config),
	}
	return writeJSONFile(path, payload)
}

func loadMobileVRouteConfig(configDir string) (mobileVRouteConfig, error) {
	path, ok := mobileVRouteConfigPath(configDir)
	if !ok {
		return mobileVRouteConfig{}, errors.New("config dir is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return mobileVRouteConfig{}, err
	}
	var payload mobileVRouteConfigCacheFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return mobileVRouteConfig{}, err
	}
	return sanitizeMobileVRouteConfig(payload.Item), nil
}

func mobileVRouteConfigPath(configDir string) (string, bool) {
	dir := strings.TrimSpace(configDir)
	if dir == "" {
		return "", false
	}
	return filepath.Join(dir, mobileVRouteConfigFileName), true
}

func sanitizeMobileVRouteConfig(input mobileVRouteConfig) mobileVRouteConfig {
	out := mobileVRouteConfig{
		LocalNodeID:   normalizeMobileRouteNodeID(input.LocalNodeID),
		Enabled:       input.Enabled,
		FakeIPCIDR:    strings.TrimSpace(input.FakeIPCIDR),
		ProbeIPs:      []mobileVRouteProbeIP{},
		TopologyRules: []mobileVRouteTopology{},
		RouteRules:    []mobileVRouteRouteRule{},
		UpdatedAt:     strings.TrimSpace(input.UpdatedAt),
	}
	for _, item := range input.ProbeIPs {
		nodeID := normalizeMobileRouteNodeID(item.NodeID)
		ip := net.ParseIP(strings.TrimSpace(item.IP)).To4()
		if nodeID == "" || ip == nil {
			continue
		}
		out.ProbeIPs = append(out.ProbeIPs, mobileVRouteProbeIP{
			NodeID:      nodeID,
			IP:          ip.String(),
			ServicePort: item.ServicePort,
			Note:        strings.TrimSpace(item.Note),
		})
	}
	for _, item := range input.TopologyRules {
		if !item.Enabled {
			continue
		}
		from := normalizeMobileRouteNodeID(item.FromNodeID)
		to := normalizeMobileRouteNodeID(item.ToNodeID)
		if from == "" || to == "" {
			continue
		}
		item.ID = strings.TrimSpace(item.ID)
		item.Name = strings.TrimSpace(item.Name)
		item.FromNodeID = from
		item.ToNodeID = to
		item.Direction = strings.TrimSpace(item.Direction)
		item.FromServiceDomain = strings.TrimSpace(item.FromServiceDomain)
		item.ToServiceDomain = strings.TrimSpace(item.ToServiceDomain)
		item.RouteLayer = strings.TrimSpace(item.RouteLayer)
		item.UserID = strings.TrimSpace(item.UserID)
		item.UserPublicKey = strings.TrimSpace(item.UserPublicKey)
		item.Secret = strings.TrimSpace(item.Secret)
		item.AuthTicket = strings.TrimSpace(item.AuthTicket)
		item.Note = strings.TrimSpace(item.Note)
		item.UpdatedAt = strings.TrimSpace(item.UpdatedAt)
		out.TopologyRules = append(out.TopologyRules, item)
	}
	for _, item := range input.RouteRules {
		item.Name = strings.TrimSpace(item.Name)
		action := normalizeMobileVRouteRuleAction(item.Action, item.ExitNodeID)
		if item.Name == "" || action == "" {
			continue
		}
		item.ID = strings.TrimSpace(item.ID)
		item.Action = action
		item.ExitNodeID = normalizeMobileRouteNodeID(item.ExitNodeID)
		item.Note = strings.TrimSpace(item.Note)
		item.UpdatedAt = strings.TrimSpace(item.UpdatedAt)
		entries := make([]string, 0, len(item.Entries))
		for _, entry := range item.Entries {
			if clean := strings.TrimSpace(strings.Trim(entry, `'",`)); clean != "" {
				entries = append(entries, clean)
			}
		}
		item.Entries = entries
		out.RouteRules = append(out.RouteRules, item)
	}
	return out
}

func decideMobileVRouteForTarget(configDir string, targetAddr string) (mobileVRouteDecision, bool, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return mobileVRouteDecision{}, false, err
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" || strings.TrimSpace(port) == "" {
		return mobileVRouteDecision{}, false, errors.New("invalid target address")
	}
	config, err := loadMobileVRouteConfig(configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return mobileVRouteDecision{}, false, nil
		}
		return mobileVRouteDecision{}, false, err
	}
	if !config.Enabled {
		return mobileVRouteDecision{}, false, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if decision, ok := decideMobileVRouteForIP(config, ip, port); ok {
			return decision, true, nil
		}
		return mobileVRouteDecision{}, false, nil
	}
	if decision, ok := decideMobileVRouteForDomain(config, host, port); ok {
		return decision, true, nil
	}
	return mobileVRouteDecision{}, false, nil
}

func decideMobileVRouteForDomain(config mobileVRouteConfig, domain string, port string) (mobileVRouteDecision, bool) {
	cleanDomain := normalizeMobileVRouteDomain(domain)
	if cleanDomain == "" {
		return mobileVRouteDecision{}, false
	}
	for _, rule := range config.RouteRules {
		for _, entry := range rule.Entries {
			if !mobileVRouteRuleEntryMatchesDomain(cleanDomain, entry) {
				continue
			}
			return mobileVRouteDecisionFromRule(config, rule, net.JoinHostPort(cleanDomain, port)), true
		}
	}
	return mobileVRouteDecision{}, false
}

func decideMobileVRouteForIP(config mobileVRouteConfig, ip net.IP, port string) (mobileVRouteDecision, bool) {
	target := ip
	if target.To4() != nil {
		target = target.To4()
	}
	for _, rule := range config.RouteRules {
		for _, entry := range rule.Entries {
			if !mobileVRouteRuleEntryMatchesIP(target, entry) {
				continue
			}
			return mobileVRouteDecisionFromRule(config, rule, net.JoinHostPort(target.String(), port)), true
		}
	}
	return mobileVRouteDecision{}, false
}

func mobileVRouteDecisionFromRule(config mobileVRouteConfig, rule mobileVRouteRouteRule, targetAddr string) mobileVRouteDecision {
	action := normalizeMobileVRouteRuleAction(rule.Action, rule.ExitNodeID)
	exitNodeID := normalizeMobileRouteNodeID(rule.ExitNodeID)
	decision := mobileVRouteDecision{
		Matched:    true,
		TargetAddr: strings.TrimSpace(targetAddr),
		Group:      firstNonEmptyString(strings.TrimSpace(rule.Name), "vroute"),
		RuleID:     strings.TrimSpace(rule.ID),
		RuleName:   strings.TrimSpace(rule.Name),
		ExitNodeID: exitNodeID,
	}
	switch action {
	case "direct":
		decision.Direct = true
	case "reject":
		decision.Reject = true
	case "probe_exit":
		if exitNodeID != "" && exitNodeID == normalizeMobileRouteNodeID(config.LocalNodeID) {
			decision.Direct = true
		}
	default:
		decision.Direct = true
	}
	return decision
}

func normalizeMobileVRouteRuleAction(action string, exitNodeID string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "probe_exit", "exit", "probe":
		if normalizeMobileRouteNodeID(exitNodeID) == "" {
			return ""
		}
		return "probe_exit"
	case "reject", "block", "deny":
		return "reject"
	case "direct":
		return "direct"
	default:
		if normalizeMobileRouteNodeID(exitNodeID) != "" {
			return "probe_exit"
		}
		return "direct"
	}
}

func mobileVRouteRuleEntryMatchesDomain(domain string, entry string) bool {
	key, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
	if !ok {
		return false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	value = normalizeMobileVRouteDomain(value)
	if value == "" {
		return false
	}
	switch key {
	case "domain", "host":
		return domain == value
	case "domain_suffix", "domain-suffix", "suffix":
		return domain == value || strings.HasSuffix(domain, "."+value)
	case "domain_keyword", "domain-keyword", "keyword":
		return strings.Contains(domain, value)
	default:
		return false
	}
}

func mobileVRouteRuleEntryMatchesIP(ip net.IP, entry string) bool {
	key, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
	if !ok {
		return false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch key {
	case "cidr", "ip_cidr", "ip-cidr":
		_, network, err := net.ParseCIDR(value)
		return err == nil && network != nil && network.Contains(ip)
	case "ip":
		parsed := net.ParseIP(value)
		return parsed != nil && parsed.Equal(ip)
	default:
		return false
	}
}

func normalizeMobileVRouteDomain(domain string) string {
	return strings.TrimSpace(strings.ToLower(strings.Trim(strings.TrimSpace(domain), ".")))
}

func mobileVRouteStatusPayload(configDir string) map[string]any {
	config, err := loadMobileVRouteConfig(configDir)
	if err != nil {
		return map[string]any{
			"enabled": false,
			"error":   err.Error(),
		}
	}
	exitNodes := map[string]struct{}{}
	for _, rule := range config.RouteRules {
		if normalizeMobileVRouteRuleAction(rule.Action, rule.ExitNodeID) == "probe_exit" {
			exitNodes[normalizeMobileRouteNodeID(rule.ExitNodeID)] = struct{}{}
		}
	}
	nodes := make([]string, 0, len(exitNodes))
	for id := range exitNodes {
		if id != "" {
			nodes = append(nodes, id)
		}
	}
	sort.Strings(nodes)
	exitNodeItems := make([]map[string]any, 0, len(nodes))
	for _, id := range nodes {
		exitNodeItems = append(exitNodeItems, map[string]any{
			"node_id":      id,
			"ip":           mobileVRouteProbeIPForNode(config, id),
			"service_port": mobileVRouteServicePortForNode(config, id, 0),
		})
	}
	routeRuleItems := make([]map[string]any, 0, len(config.RouteRules))
	for _, rule := range config.RouteRules {
		routeRuleItems = append(routeRuleItems, map[string]any{
			"id":           strings.TrimSpace(rule.ID),
			"name":         strings.TrimSpace(rule.Name),
			"action":       normalizeMobileVRouteRuleAction(rule.Action, rule.ExitNodeID),
			"exit_node_id": normalizeMobileRouteNodeID(rule.ExitNodeID),
			"entries":      append([]string(nil), rule.Entries...),
			"updated_at":   strings.TrimSpace(rule.UpdatedAt),
		})
	}
	return map[string]any{
		"local_node_id":    strings.TrimSpace(config.LocalNodeID),
		"local_ip":         mobileVRouteProbeIPForNode(config, config.LocalNodeID),
		"enabled":          config.Enabled,
		"fake_ip_cidr":     strings.TrimSpace(config.FakeIPCIDR),
		"probe_ips":        len(config.ProbeIPs),
		"topology_rules":   len(config.TopologyRules),
		"route_rules":      len(config.RouteRules),
		"route_rule_items": routeRuleItems,
		"exit_nodes":       nodes,
		"exit_node_items":  exitNodeItems,
		"updated_at":       strings.TrimSpace(config.UpdatedAt),
	}
}

func mobileVRouteProbeExitRouteID(exitNodeID string) string {
	exitNodeID = normalizeMobileRouteNodeID(exitNodeID)
	if exitNodeID == "" {
		return ""
	}
	return mobileVRouteProbeExitRouteIDPre + exitNodeID
}

func isMobileVRouteProbeExitRouteID(routeID string) bool {
	return strings.HasPrefix(strings.TrimSpace(routeID), mobileVRouteProbeExitRouteIDPre)
}

func mobileVRouteProbeExitNodeFromRouteID(routeID string) string {
	return normalizeMobileRouteNodeID(strings.TrimPrefix(strings.TrimSpace(routeID), mobileVRouteProbeExitRouteIDPre))
}

func mobileVRouteSummary(config mobileVRouteConfig) string {
	return fmt.Sprintf("vroute=%t rules=%d topology=%d probes=%d", config.Enabled, len(config.RouteRules), len(config.TopologyRules), len(config.ProbeIPs))
}

func mobileVRouteRouteIDForDecision(decision mobileVRouteDecision) string {
	if decision.Direct || decision.Reject || strings.TrimSpace(decision.ExitNodeID) == "" {
		return ""
	}
	return mobileVRouteProbeExitRouteID(decision.ExitNodeID)
}

func mobileVRouteConfigRuleCount(config mobileVRouteConfig) int {
	return len(config.RouteRules)
}

func mobileVRouteConfigProbeCount(config mobileVRouteConfig) int {
	return len(config.ProbeIPs)
}

func mobileVRouteConfigTopologyCount(config mobileVRouteConfig) int {
	return len(config.TopologyRules)
}

func mobileVRouteConfigUpdatedAt(config mobileVRouteConfig) string {
	return strings.TrimSpace(config.UpdatedAt)
}

func mobileVRouteConfigEnabled(config mobileVRouteConfig) bool {
	return config.Enabled
}

func mobileVRouteProbeIPMap(config mobileVRouteConfig) map[string]string {
	out := make(map[string]string, len(config.ProbeIPs))
	for _, item := range config.ProbeIPs {
		if nodeID := normalizeMobileRouteNodeID(item.NodeID); nodeID != "" {
			out[nodeID] = strings.TrimSpace(item.IP)
		}
	}
	return out
}

func mobileVRouteProbeIPForNode(config mobileVRouteConfig, nodeID string) string {
	return mobileVRouteProbeIPMap(config)[normalizeMobileRouteNodeID(nodeID)]
}

func mobileVRouteServicePortForNode(config mobileVRouteConfig, nodeID string, fallback int) int {
	nodeID = normalizeMobileRouteNodeID(nodeID)
	for _, item := range config.ProbeIPs {
		if normalizeMobileRouteNodeID(item.NodeID) != nodeID {
			continue
		}
		if item.ServicePort > 0 && item.ServicePort <= 65535 {
			return item.ServicePort
		}
	}
	if fallback > 0 && fallback <= 65535 {
		return fallback
	}
	return 12040
}

func mobileVRouteNodeIDs(config mobileVRouteConfig) []string {
	set := map[string]struct{}{}
	for _, item := range config.ProbeIPs {
		if nodeID := normalizeMobileRouteNodeID(item.NodeID); nodeID != "" {
			set[nodeID] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		left, lerr := strconv.Atoi(out[i])
		right, rerr := strconv.Atoi(out[j])
		if lerr == nil && rerr == nil {
			return left < right
		}
		return out[i] < out[j]
	})
	return out
}
