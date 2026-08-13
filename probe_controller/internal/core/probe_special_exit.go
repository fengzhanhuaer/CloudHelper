package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	probeNodeKindNormal                  = "normal"
	probeNodeKindMihomoExit              = "mihomo_exit"
	probeSpecialExitRuleIDPrefix         = "special-exit:"
	probeSpecialExitMaxCount             = 128
	probeSpecialExitMaxRules             = 2048
	probeSpecialExitMaxProxies           = 4096
	probeSpecialExitMaxSubscriptionURL   = 4096
	probeSpecialExitMaxSubscriptionHeads = 32
)

type probeSpecialExitConfig struct {
	NodeID                     string                   `json:"node_id"`
	Name                       string                   `json:"name"`
	Enabled                    bool                     `json:"enabled"`
	SubscriptionURL            string                   `json:"subscription_url,omitempty"`
	SubscriptionHeaders        map[string]string        `json:"subscription_headers,omitempty"`
	DefaultAction              string                   `json:"default_action"`
	DefaultTarget              string                   `json:"default_target,omitempty"`
	Rules                      []probeSpecialExitRule   `json:"rules,omitempty"`
	Proxies                    []map[string]interface{} `json:"proxies,omitempty"`
	Revision                   int64                    `json:"revision"`
	SHA256                     string                   `json:"sha256"`
	LastSubscriptionRefreshAt  string                   `json:"last_subscription_refresh_at,omitempty"`
	LastSubscriptionRefreshErr string                   `json:"last_subscription_refresh_error,omitempty"`
	UpdatedAt                  string                   `json:"updated_at,omitempty"`
}

type probeSpecialExitRule struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Action  string   `json:"action"`
	Target  string   `json:"target,omitempty"`
	Entries []string `json:"entries"`
	Ports   []string `json:"ports,omitempty"`
	Network string   `json:"network,omitempty"`
}

type probeSpecialExitSnapshot struct {
	Version       int                      `json:"version"`
	NodeID        string                   `json:"node_id"`
	Enabled       bool                     `json:"enabled"`
	Revision      int64                    `json:"revision"`
	SHA256        string                   `json:"sha256"`
	DefaultAction string                   `json:"default_action"`
	DefaultTarget string                   `json:"default_target,omitempty"`
	Rules         []probeSpecialExitRule   `json:"rules"`
	Proxies       []map[string]interface{} `json:"proxies"`
}

type probeSpecialExitRuntimeReport struct {
	AppliedRevision int64  `json:"applied_revision,omitempty"`
	AppliedSHA256   string `json:"applied_sha256,omitempty"`
	ExitReady       bool   `json:"exit_ready"`
	Healthy         bool   `json:"healthy"`
	MihomoVersion   string `json:"mihomo_version,omitempty"`
	ActiveSessions  int64  `json:"active_sessions,omitempty"`
	BytesUp         int64  `json:"bytes_up,omitempty"`
	BytesDown       int64  `json:"bytes_down,omitempty"`
	LastApplyError  string `json:"last_apply_error,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

func normalizeProbeNodeKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case probeNodeKindMihomoExit:
		return probeNodeKindMihomoExit
	default:
		return probeNodeKindNormal
	}
}

func normalizeProbeSpecialExitConfigs(items []probeSpecialExitConfig) []probeSpecialExitConfig {
	if len(items) == 0 {
		return []probeSpecialExitConfig{}
	}
	out := make([]probeSpecialExitConfig, 0, len(items))
	seen := make(map[string]struct{})
	for _, raw := range items {
		item, err := normalizeProbeSpecialExitConfig(raw, nil)
		if err != nil || item.NodeID == "" {
			continue
		}
		if _, exists := seen[item.NodeID]; exists {
			continue
		}
		seen[item.NodeID] = struct{}{}
		out = append(out, item)
		if len(out) >= probeSpecialExitMaxCount {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func normalizeProbeSpecialExitConfig(raw probeSpecialExitConfig, previous *probeSpecialExitConfig) (probeSpecialExitConfig, error) {
	nodeID := normalizeProbeNodeID(raw.NodeID)
	if nodeID == "" {
		return probeSpecialExitConfig{}, fmt.Errorf("node_id is required")
	}
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		name = "Special exit " + nodeID
	}
	if len(name) > 160 {
		return probeSpecialExitConfig{}, fmt.Errorf("name exceeds 160 characters")
	}
	url := strings.TrimSpace(raw.SubscriptionURL)
	if url == "" && previous != nil {
		url = previous.SubscriptionURL
	}
	if len(url) > probeSpecialExitMaxSubscriptionURL {
		return probeSpecialExitConfig{}, fmt.Errorf("subscription_url is too long")
	}
	headers := normalizeProbeSpecialExitHeaders(raw.SubscriptionHeaders)
	if len(headers) == 0 && previous != nil {
		headers = normalizeProbeSpecialExitHeaders(previous.SubscriptionHeaders)
	}
	defaultAction, err := normalizeProbeSpecialExitAction(raw.DefaultAction, raw.DefaultTarget)
	if err != nil {
		return probeSpecialExitConfig{}, fmt.Errorf("default_action: %w", err)
	}
	defaultTarget := strings.TrimSpace(raw.DefaultTarget)
	if defaultAction == "direct" || defaultAction == "reject" {
		defaultTarget = ""
	}
	rules, err := normalizeProbeSpecialExitRules(raw.Rules)
	if err != nil {
		return probeSpecialExitConfig{}, err
	}
	proxies, err := normalizeProbeSpecialExitProxies(raw.Proxies)
	if err != nil {
		return probeSpecialExitConfig{}, err
	}
	updatedAt := strings.TrimSpace(raw.UpdatedAt)
	if updatedAt == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	item := probeSpecialExitConfig{
		NodeID:                     nodeID,
		Name:                       name,
		Enabled:                    raw.Enabled,
		SubscriptionURL:            url,
		SubscriptionHeaders:        headers,
		DefaultAction:              defaultAction,
		DefaultTarget:              defaultTarget,
		Rules:                      rules,
		Proxies:                    proxies,
		Revision:                   raw.Revision,
		LastSubscriptionRefreshAt:  strings.TrimSpace(raw.LastSubscriptionRefreshAt),
		LastSubscriptionRefreshErr: strings.TrimSpace(raw.LastSubscriptionRefreshErr),
		UpdatedAt:                  updatedAt,
	}
	if item.Revision <= 0 {
		item.Revision = 1
	}
	item.SHA256 = probeSpecialExitSnapshotHash(item)
	return item, nil
}

func normalizeProbeSpecialExitHeaders(input map[string]string) map[string]string {
	if len(input) == 0 {
		return map[string]string{}
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]string)
	for _, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		value := strings.TrimSpace(input[rawKey])
		if key == "" || value == "" || len(key) > 128 || len(value) > 4096 {
			continue
		}
		out[key] = value
		if len(out) >= probeSpecialExitMaxSubscriptionHeads {
			break
		}
	}
	return out
}

func normalizeProbeSpecialExitRules(input []probeSpecialExitRule) ([]probeSpecialExitRule, error) {
	if len(input) > probeSpecialExitMaxRules {
		return nil, fmt.Errorf("rules exceeded limit (%d)", probeSpecialExitMaxRules)
	}
	out := make([]probeSpecialExitRule, 0, len(input))
	seen := make(map[string]struct{})
	for index, raw := range input {
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			id = "rule-" + strconv.Itoa(index+1)
		}
		if len(id) > 128 {
			return nil, fmt.Errorf("rules[%d].id is too long", index)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("rules[%d].id is duplicated", index)
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			name = id
		}
		action, err := normalizeProbeSpecialExitAction(raw.Action, raw.Target)
		if err != nil {
			return nil, fmt.Errorf("rules[%d].action: %w", index, err)
		}
		entries := normalizeProbeVirtualRouterRouteRuleEntries(raw.Entries)
		if raw.Enabled && len(entries) == 0 {
			return nil, fmt.Errorf("rules[%d] requires an aggregatable entry", index)
		}
		ports, err := normalizeProbeSpecialExitPorts(raw.Ports)
		if err != nil {
			return nil, fmt.Errorf("rules[%d].ports: %w", index, err)
		}
		network := strings.ToLower(strings.TrimSpace(raw.Network))
		if network != "" && network != "tcp" && network != "udp" {
			return nil, fmt.Errorf("rules[%d].network must be tcp or udp", index)
		}
		target := strings.TrimSpace(raw.Target)
		if action == "direct" || action == "reject" {
			target = ""
		}
		out = append(out, probeSpecialExitRule{ID: id, Name: name, Enabled: raw.Enabled, Action: action, Target: target, Entries: entries, Ports: ports, Network: network})
	}
	return out, nil
}

func normalizeProbeSpecialExitAction(raw string, target string) (string, error) {
	action := strings.ToLower(strings.TrimSpace(raw))
	switch action {
	case "", "direct":
		return "direct", nil
	case "reject":
		return "reject", nil
	case "proxy", "group", "node":
		target = strings.TrimSpace(target)
		if target == "" {
			return "", fmt.Errorf("%s action requires target", action)
		}
		if len(target) > 256 || strings.ContainsAny(target, ",\r\n") {
			return "", fmt.Errorf("%s target contains an unsupported delimiter", action)
		}
		return action, nil
	default:
		return "", fmt.Errorf("unsupported action %q", raw)
	}
}

func normalizeProbeSpecialExitPorts(input []string) ([]string, error) {
	out := make([]string, 0, len(input))
	seen := make(map[string]struct{})
	for _, raw := range input {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		parts := strings.Split(value, "-")
		if len(parts) > 2 {
			return nil, fmt.Errorf("invalid port %q", value)
		}
		for _, part := range parts {
			port, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("invalid port %q", value)
			}
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func normalizeProbeSpecialExitProxies(input []map[string]interface{}) ([]map[string]interface{}, error) {
	if len(input) > probeSpecialExitMaxProxies {
		return nil, fmt.Errorf("proxies exceeded limit (%d)", probeSpecialExitMaxProxies)
	}
	out := make([]map[string]interface{}, 0, len(input))
	seen := make(map[string]struct{})
	for index, raw := range input {
		name := strings.TrimSpace(fmt.Sprint(raw["name"]))
		kind := strings.TrimSpace(fmt.Sprint(raw["type"]))
		if name == "" || kind == "" {
			return nil, fmt.Errorf("proxies[%d] requires name and type", index)
		}
		if len(name) > 256 || strings.ContainsAny(name, ",\r\n") {
			return nil, fmt.Errorf("proxies[%d].name contains an unsupported delimiter", index)
		}
		if probeSpecialExitReservedPolicyName(name) {
			return nil, fmt.Errorf("proxies[%d].name %q is reserved", index, name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("proxy name %q is duplicated", name)
		}
		encoded, err := json.Marshal(raw)
		if err != nil || len(encoded) > 64*1024 {
			return nil, fmt.Errorf("proxies[%d] is invalid or too large", index)
		}
		var cloned map[string]interface{}
		if err := json.Unmarshal(encoded, &cloned); err != nil {
			return nil, fmt.Errorf("proxies[%d] is invalid", index)
		}
		seen[name] = struct{}{}
		out = append(out, cloned)
	}
	sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i]["name"]) < fmt.Sprint(out[j]["name"]) })
	return out, nil
}

func probeSpecialExitReservedPolicyName(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL", "MATCH":
		return true
	default:
		return false
	}
}

func validateProbeSpecialExitResolvedPolicies(item probeSpecialExitConfig) error {
	proxyNames := make(map[string]struct{}, len(item.Proxies))
	for _, proxy := range item.Proxies {
		proxyNames[strings.TrimSpace(fmt.Sprint(proxy["name"]))] = struct{}{}
	}
	validate := func(action, target string) error {
		action = strings.ToLower(strings.TrimSpace(action))
		target = strings.TrimSpace(target)
		switch action {
		case "proxy", "group":
			if len(proxyNames) == 0 {
				return fmt.Errorf("%s action requires at least one subscription proxy", action)
			}
			if probeSpecialExitReservedPolicyName(target) {
				return fmt.Errorf("policy target %q is reserved", target)
			}
			if _, exists := proxyNames[target]; exists {
				return fmt.Errorf("policy group %q conflicts with a proxy name", target)
			}
		case "node":
			if _, exists := proxyNames[target]; !exists {
				return fmt.Errorf("proxy node %q does not exist", target)
			}
		}
		return nil
	}
	if err := validate(item.DefaultAction, item.DefaultTarget); err != nil {
		return fmt.Errorf("default_action: %w", err)
	}
	for index, rule := range item.Rules {
		if !rule.Enabled {
			continue
		}
		if err := validate(rule.Action, rule.Target); err != nil {
			return fmt.Errorf("rules[%d].action: %w", index, err)
		}
	}
	return nil
}

func probeSpecialExitSnapshotForConfig(item probeSpecialExitConfig) probeSpecialExitSnapshot {
	return probeSpecialExitSnapshot{Version: 1, NodeID: item.NodeID, Enabled: item.Enabled, Revision: item.Revision, SHA256: item.SHA256, DefaultAction: item.DefaultAction, DefaultTarget: item.DefaultTarget, Rules: append([]probeSpecialExitRule(nil), item.Rules...), Proxies: append([]map[string]interface{}(nil), item.Proxies...)}
}

func probeSpecialExitSnapshotHash(item probeSpecialExitConfig) string {
	snapshot := probeSpecialExitSnapshotForConfig(item)
	snapshot.SHA256 = ""
	encoded, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func probeSpecialExitSemanticHash(item probeSpecialExitConfig) string {
	item.Revision = 0
	return probeSpecialExitSnapshotHash(item)
}

func probeSpecialExitSubscriptionSourceHash(item probeSpecialExitConfig) string {
	value := struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}{URL: strings.TrimSpace(item.SubscriptionURL), Headers: normalizeProbeSpecialExitHeaders(item.SubscriptionHeaders)}
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func buildProbeSpecialExitManagedRules(items []probeSpecialExitConfig) []probeVirtualRouterRouteRule {
	out := make([]probeVirtualRouterRouteRule, 0, len(items))
	for _, item := range normalizeProbeSpecialExitConfigs(items) {
		if !item.Enabled {
			continue
		}
		entries := make([]string, 0)
		for _, rule := range item.Rules {
			if rule.Enabled {
				entries = append(entries, rule.Entries...)
			}
		}
		entries = normalizeProbeVirtualRouterRouteRuleEntries(entries)
		if len(entries) == 0 {
			continue
		}
		out = append(out, probeVirtualRouterRouteRule{ID: probeSpecialExitRuleIDPrefix + item.NodeID, Name: item.Name, Action: probeVirtualRouterRouteRuleActionExit, ExitNodeID: item.NodeID, Entries: entries, Note: "managed special exit", UpdatedAt: item.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func buildEffectiveProbeVirtualRouterRouteRules(manual []probeVirtualRouterRouteRule, special []probeSpecialExitConfig) []probeVirtualRouterRouteRule {
	out := append([]probeVirtualRouterRouteRule(nil), normalizeProbeVirtualRouterRouteRules(manual)...)
	out = append(out, buildProbeSpecialExitManagedRules(special)...)
	return out
}

func validateProbeSpecialExitConflicts(manual []probeVirtualRouterRouteRule, special []probeSpecialExitConfig) error {
	type ownedEntry struct{ owner, entry string }
	owned := make([]ownedEntry, 0)
	for _, rule := range normalizeProbeVirtualRouterRouteRules(manual) {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(rule.ID)), probeSpecialExitRuleIDPrefix) {
			return fmt.Errorf("manual route rule %q uses reserved special-exit ID", rule.ID)
		}
		for _, entry := range rule.Entries {
			owned = append(owned, ownedEntry{owner: "manual", entry: entry})
		}
	}
	for _, item := range normalizeProbeSpecialExitConfigs(special) {
		if !item.Enabled {
			continue
		}
		for _, rule := range item.Rules {
			if !rule.Enabled {
				continue
			}
			for _, entry := range rule.Entries {
				owned = append(owned, ownedEntry{owner: "special:" + item.NodeID, entry: entry})
			}
		}
	}
	for i := 0; i < len(owned); i++ {
		for j := i + 1; j < len(owned); j++ {
			if owned[i].owner == owned[j].owner || !probeSpecialExitEntriesOverlap(owned[i].entry, owned[j].entry) {
				continue
			}
			return fmt.Errorf("route entry conflict: %s %q overlaps %s %q", owned[i].owner, owned[i].entry, owned[j].owner, owned[j].entry)
		}
	}
	return nil
}

func probeSpecialExitEntriesOverlap(left string, right string) bool {
	if left == right {
		return true
	}
	lk, lv, lok := strings.Cut(left, ":")
	rk, rv, rok := strings.Cut(right, ":")
	if !lok || !rok {
		return false
	}
	leftCIDR := lk == "cidr"
	rightCIDR := rk == "cidr"
	if leftCIDR != rightCIDR {
		return false
	}
	if lk == "cidr" && rk == "cidr" {
		lp, le := netip.ParsePrefix(lv)
		rp, re := netip.ParsePrefix(rv)
		return le == nil && re == nil && (lp.Contains(rp.Addr()) || rp.Contains(lp.Addr()))
	}
	if lk == "domain_keyword" || rk == "domain_keyword" {
		return true
	}
	if lk == "domain_suffix" && rk == "domain_suffix" {
		return lv == rv || strings.HasSuffix(lv, "."+rv) || strings.HasSuffix(rv, "."+lv)
	}
	if lk == "domain_prefix" && rk == "domain_prefix" {
		return strings.HasPrefix(lv, rv) || strings.HasPrefix(rv, lv)
	}
	if lk == "domain_prefix" && rk == "domain_suffix" {
		return true
	}
	if lk == "domain_suffix" && rk == "domain_prefix" {
		return true
	}
	return false
}

func findProbeSpecialExitByNodeID(items []probeSpecialExitConfig, nodeID string) (probeSpecialExitConfig, bool) {
	nodeID = normalizeProbeNodeID(nodeID)
	for _, item := range items {
		if normalizeProbeNodeID(item.NodeID) == nodeID {
			return item, true
		}
	}
	return probeSpecialExitConfig{}, false
}
