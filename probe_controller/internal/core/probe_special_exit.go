package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	probeNodeKindNormal                = "normal"
	probeNodeKindMihomoExit            = "mihomo_exit"
	probeSpecialExitDirectTarget       = "DIRECT"
	probeSpecialExitMaxCount           = 128
	probeSpecialExitMaxRules           = 2048
	probeSpecialExitMaxProxies         = 4096
	probeSpecialExitMaxSubscriptions   = 32
	probeSpecialExitMaxSubscriptionURL = 4096
)

type probeSpecialExitConfig struct {
	NodeID    string                   `json:"node_id"`
	Rules     []probeSpecialExitRule   `json:"rules,omitempty"`
	Proxies   []map[string]interface{} `json:"proxies,omitempty"`
	Revision  int64                    `json:"revision"`
	SHA256    string                   `json:"sha256"`
	UpdatedAt string                   `json:"updated_at,omitempty"`
}

type probeSpecialExitLibrary struct {
	Subscriptions              []probeSpecialExitSubscription `json:"subscriptions,omitempty"`
	Proxies                    []map[string]interface{}       `json:"proxies,omitempty"`
	ProxySourceIDs             map[string]string              `json:"proxy_source_ids,omitempty"`
	LastSubscriptionRefreshAt  string                         `json:"last_subscription_refresh_at,omitempty"`
	LastSubscriptionRefreshErr string                         `json:"last_subscription_refresh_error,omitempty"`
	UpdatedAt                  string                         `json:"updated_at,omitempty"`
}

type probeSpecialExitSubscription struct {
	ID                         string `json:"id"`
	Name                       string `json:"name"`
	Enabled                    bool   `json:"enabled"`
	URL                        string `json:"url,omitempty"`
	LastSubscriptionRefreshAt  string `json:"last_subscription_refresh_at,omitempty"`
	LastSubscriptionRefreshErr string `json:"last_subscription_refresh_error,omitempty"`
}

type probeSpecialExitRule struct {
	RouteRuleID string   `json:"route_rule_id"`
	Target      string   `json:"target"`
	Entries     []string `json:"entries"`
}

type probeSpecialExitSnapshot struct {
	Version  int                      `json:"version"`
	NodeID   string                   `json:"node_id"`
	Revision int64                    `json:"revision"`
	SHA256   string                   `json:"sha256"`
	Rules    []probeSpecialExitRule   `json:"rules"`
	Proxies  []map[string]interface{} `json:"proxies"`
}

type probeSpecialExitRuntimeReport struct {
	AppliedRevision int64                                `json:"applied_revision,omitempty"`
	AppliedSHA256   string                               `json:"applied_sha256,omitempty"`
	ExitReady       bool                                 `json:"exit_ready"`
	Healthy         bool                                 `json:"healthy"`
	MihomoVersion   string                               `json:"mihomo_version,omitempty"`
	ActiveSessions  int64                                `json:"active_sessions,omitempty"`
	BytesUp         int64                                `json:"bytes_up,omitempty"`
	BytesDown       int64                                `json:"bytes_down,omitempty"`
	Connectivity    []probeSpecialExitConnectivityReport `json:"connectivity,omitempty"`
	LastApplyError  string                               `json:"last_apply_error,omitempty"`
	UpdatedAt       string                               `json:"updated_at,omitempty"`
}

type probeSpecialExitConnectivityReport struct {
	Target    string `json:"target"`
	Reachable bool   `json:"reachable"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
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

func normalizeProbeSpecialExitConfig(raw probeSpecialExitConfig, _ *probeSpecialExitConfig) (probeSpecialExitConfig, error) {
	nodeID := normalizeProbeNodeID(raw.NodeID)
	if nodeID == "" {
		return probeSpecialExitConfig{}, fmt.Errorf("node_id is required")
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
	item := probeSpecialExitConfig{NodeID: nodeID, Rules: rules, Proxies: proxies, Revision: raw.Revision, UpdatedAt: updatedAt}
	if item.Revision <= 0 {
		item.Revision = 1
	}
	item.SHA256 = probeSpecialExitSnapshotHash(item)
	return item, nil
}

func normalizeProbeSpecialExitProxySourceIDs(input map[string]string, proxies []map[string]interface{}) map[string]string {
	if len(input) == 0 || len(proxies) == 0 {
		return map[string]string{}
	}
	validNames := make(map[string]struct{}, len(proxies))
	for _, proxy := range proxies {
		if name := strings.TrimSpace(fmt.Sprint(proxy["name"])); name != "" {
			validNames[name] = struct{}{}
		}
	}
	out := make(map[string]string, len(input))
	for rawName, rawSourceID := range input {
		name := strings.TrimSpace(rawName)
		sourceID := strings.TrimSpace(rawSourceID)
		if _, ok := validNames[name]; !ok || sourceID == "" || len(sourceID) > 128 || strings.ContainsAny(sourceID, "\r\n") {
			continue
		}
		out[name] = sourceID
	}
	return out
}

func normalizeProbeSpecialExitLibrary(raw probeSpecialExitLibrary, previous *probeSpecialExitLibrary) (probeSpecialExitLibrary, error) {
	previousSubscriptions := []probeSpecialExitSubscription(nil)
	if previous != nil {
		previousSubscriptions = previous.Subscriptions
	}
	rawSubscriptions := raw.Subscriptions
	if rawSubscriptions == nil {
		if previous != nil {
			rawSubscriptions = append([]probeSpecialExitSubscription(nil), previousSubscriptions...)
		} else {
			rawSubscriptions = []probeSpecialExitSubscription{}
		}
	}
	subscriptions, err := normalizeProbeSpecialExitSubscriptions(rawSubscriptions, previousSubscriptions)
	if err != nil {
		return probeSpecialExitLibrary{}, err
	}
	proxies, err := normalizeProbeSpecialExitProxies(raw.Proxies)
	if err != nil {
		return probeSpecialExitLibrary{}, err
	}
	proxySourceIDs := raw.ProxySourceIDs
	if proxySourceIDs == nil && previous != nil {
		proxySourceIDs = previous.ProxySourceIDs
	}
	updatedAt := strings.TrimSpace(raw.UpdatedAt)
	if updatedAt == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return probeSpecialExitLibrary{
		Subscriptions:              subscriptions,
		Proxies:                    proxies,
		ProxySourceIDs:             normalizeProbeSpecialExitProxySourceIDs(proxySourceIDs, proxies),
		LastSubscriptionRefreshAt:  strings.TrimSpace(raw.LastSubscriptionRefreshAt),
		LastSubscriptionRefreshErr: strings.TrimSpace(raw.LastSubscriptionRefreshErr),
		UpdatedAt:                  updatedAt,
	}, nil
}

func resolveProbeSpecialExitProxies(rules []probeSpecialExitRule, library []map[string]interface{}) ([]map[string]interface{}, error) {
	wanted := make(map[string]struct{})
	for _, rule := range rules {
		target := normalizeProbeSpecialExitTarget(rule.Target)
		if target != probeSpecialExitDirectTarget {
			wanted[target] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return []map[string]interface{}{}, nil
	}
	resolved := make([]map[string]interface{}, 0, len(wanted))
	for _, proxy := range library {
		name := strings.TrimSpace(fmt.Sprint(proxy["name"]))
		if _, ok := wanted[name]; ok {
			resolved = append(resolved, proxy)
			delete(wanted, name)
		}
	}
	if len(wanted) > 0 {
		missing := make([]string, 0, len(wanted))
		for name := range wanted {
			missing = append(missing, name)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("rule target %q is not present in the global Clash node library", missing[0])
	}
	return normalizeProbeSpecialExitProxies(resolved)
}

func normalizeProbeSpecialExitSubscriptions(input, previous []probeSpecialExitSubscription) ([]probeSpecialExitSubscription, error) {
	if len(input) > probeSpecialExitMaxSubscriptions {
		return nil, fmt.Errorf("subscriptions exceeded limit (%d)", probeSpecialExitMaxSubscriptions)
	}
	previousByID := make(map[string]probeSpecialExitSubscription, len(previous))
	for _, item := range previous {
		previousByID[strings.TrimSpace(item.ID)] = item
	}
	out := make([]probeSpecialExitSubscription, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for index, raw := range input {
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			id = "subscription-" + strconv.Itoa(index+1)
		}
		if len(id) > 128 || strings.ContainsAny(id, "\r\n") {
			return nil, fmt.Errorf("subscriptions[%d].id is invalid", index)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("subscriptions[%d].id is duplicated", index)
		}
		seen[id] = struct{}{}
		prior, hadPrior := previousByID[id]
		name := strings.TrimSpace(raw.Name)
		if name == "" && hadPrior {
			name = strings.TrimSpace(prior.Name)
		}
		if name == "" {
			name = "订阅 " + strconv.Itoa(index+1)
		}
		if len(name) > 160 {
			return nil, fmt.Errorf("subscriptions[%d].name exceeds 160 characters", index)
		}
		url := strings.TrimSpace(raw.URL)
		if url == "" && hadPrior {
			url = strings.TrimSpace(prior.URL)
		}
		if len(url) > probeSpecialExitMaxSubscriptionURL {
			return nil, fmt.Errorf("subscriptions[%d].url is too long", index)
		}
		lastRefreshAt := strings.TrimSpace(raw.LastSubscriptionRefreshAt)
		lastRefreshErr := strings.TrimSpace(raw.LastSubscriptionRefreshErr)
		if hadPrior {
			lastRefreshAt = prior.LastSubscriptionRefreshAt
			lastRefreshErr = prior.LastSubscriptionRefreshErr
		}
		out = append(out, probeSpecialExitSubscription{
			ID: id, Name: name, Enabled: raw.Enabled, URL: url,
			LastSubscriptionRefreshAt: lastRefreshAt, LastSubscriptionRefreshErr: lastRefreshErr,
		})
	}
	return out, nil
}

func normalizeProbeSpecialExitRules(input []probeSpecialExitRule) ([]probeSpecialExitRule, error) {
	if len(input) > probeSpecialExitMaxRules {
		return nil, fmt.Errorf("rules exceeded limit (%d)", probeSpecialExitMaxRules)
	}
	out := make([]probeSpecialExitRule, 0, len(input))
	seen := make(map[string]struct{})
	for index, raw := range input {
		routeRuleID := strings.TrimSpace(raw.RouteRuleID)
		if routeRuleID == "" {
			continue
		}
		if len(routeRuleID) > 128 || strings.ContainsAny(routeRuleID, "\r\n") {
			return nil, fmt.Errorf("rules[%d].route_rule_id is invalid", index)
		}
		if _, exists := seen[routeRuleID]; exists {
			return nil, fmt.Errorf("rules[%d].route_rule_id is duplicated", index)
		}
		seen[routeRuleID] = struct{}{}
		target := normalizeProbeSpecialExitTarget(raw.Target)
		if target == "" {
			return nil, fmt.Errorf("rules[%d].target is invalid", index)
		}
		if len(target) > 256 || strings.ContainsAny(target, ",\r\n") {
			return nil, fmt.Errorf("rules[%d].target contains an unsupported delimiter", index)
		}
		entries := normalizeProbeVirtualRouterRouteRuleEntries(raw.Entries)
		out = append(out, probeSpecialExitRule{RouteRuleID: routeRuleID, Target: target, Entries: entries})
	}
	return out, nil
}

func normalizeProbeSpecialExitTarget(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || strings.EqualFold(value, probeSpecialExitDirectTarget) {
		return probeSpecialExitDirectTarget
	}
	return value
}

func probeSpecialExitRouteRuleAssignedToNode(rule probeVirtualRouterRouteRule, nodeID string) bool {
	return normalizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) == probeVirtualRouterRouteRuleActionExit &&
		normalizeProbeNodeID(rule.ExitNodeID) == normalizeProbeNodeID(nodeID)
}

func compileProbeSpecialExitRules(nodeID string, selections []probeSpecialExitRule, routeRules []probeVirtualRouterRouteRule, strict bool) ([]probeSpecialExitRule, error) {
	nodeID = normalizeProbeNodeID(nodeID)
	selectionByID := make(map[string]string, len(selections))
	for index, selection := range selections {
		routeRuleID := strings.TrimSpace(selection.RouteRuleID)
		if routeRuleID == "" || len(routeRuleID) > 128 || strings.ContainsAny(routeRuleID, "\r\n") {
			return nil, fmt.Errorf("rules[%d].route_rule_id is invalid", index)
		}
		if _, exists := selectionByID[routeRuleID]; exists {
			return nil, fmt.Errorf("rules[%d].route_rule_id is duplicated", index)
		}
		target := normalizeProbeSpecialExitTarget(selection.Target)
		if len(target) > 256 || strings.ContainsAny(target, ",\r\n") {
			return nil, fmt.Errorf("rules[%d].target is invalid", index)
		}
		selectionByID[routeRuleID] = target
	}

	matched := make(map[string]struct{}, len(selectionByID))
	out := make([]probeSpecialExitRule, 0)
	for _, routeRule := range normalizeProbeVirtualRouterRouteRules(routeRules) {
		if !probeSpecialExitRouteRuleAssignedToNode(routeRule, nodeID) {
			continue
		}
		routeRuleID := strings.TrimSpace(routeRule.ID)
		target := probeSpecialExitDirectTarget
		if selected, ok := selectionByID[routeRuleID]; ok {
			target = selected
			matched[routeRuleID] = struct{}{}
		}
		out = append(out, probeSpecialExitRule{
			RouteRuleID: routeRuleID,
			Target:      target,
			Entries:     append([]string(nil), routeRule.Entries...),
		})
	}
	if strict {
		for routeRuleID := range selectionByID {
			if _, ok := matched[routeRuleID]; !ok {
				return nil, fmt.Errorf("route rule %q is not assigned to special exit %q", routeRuleID, nodeID)
			}
		}
	}
	return out, nil
}

func reconcileProbeSpecialExitConfigsWithRouteRules(items []probeSpecialExitConfig, routeRules []probeVirtualRouterRouteRule, now time.Time) ([]probeSpecialExitConfig, bool) {
	normalized := normalizeProbeSpecialExitConfigs(items)
	changed := false
	for index := range normalized {
		item := &normalized[index]
		previousHash := probeSpecialExitSemanticHash(*item)
		compiled, err := compileProbeSpecialExitRules(item.NodeID, item.Rules, routeRules, false)
		if err != nil {
			continue
		}
		item.Rules = compiled
		nextHash := probeSpecialExitSemanticHash(*item)
		if nextHash != previousHash {
			item.Revision++
			item.UpdatedAt = now.UTC().Format(time.RFC3339)
			changed = true
		}
		item.SHA256 = probeSpecialExitSnapshotHash(*item)
	}
	return normalized, changed
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
	validate := func(target string) error {
		target = strings.TrimSpace(target)
		if _, exists := proxyNames[target]; !exists {
			return fmt.Errorf("proxy node %q does not exist", target)
		}
		return nil
	}
	for index, rule := range item.Rules {
		if normalizeProbeSpecialExitTarget(rule.Target) == probeSpecialExitDirectTarget {
			continue
		}
		if err := validate(rule.Target); err != nil {
			return fmt.Errorf("rules[%d].target: %w", index, err)
		}
	}
	return nil
}

func probeSpecialExitSnapshotForConfig(item probeSpecialExitConfig) probeSpecialExitSnapshot {
	return probeSpecialExitSnapshot{Version: 3, NodeID: item.NodeID, Revision: item.Revision, SHA256: item.SHA256, Rules: append([]probeSpecialExitRule(nil), item.Rules...), Proxies: append([]map[string]interface{}(nil), item.Proxies...)}
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

func probeSpecialExitLibrarySourceHash(library probeSpecialExitLibrary) string {
	value := make([]probeSpecialExitSubscription, 0, len(library.Subscriptions))
	for _, source := range library.Subscriptions {
		value = append(value, probeSpecialExitSubscription{
			ID: source.ID, Name: source.Name, Enabled: source.Enabled, URL: source.URL,
		})
	}
	content, _ := json.Marshal(value)
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
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
