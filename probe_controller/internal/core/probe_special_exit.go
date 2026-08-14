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
	probeSpecialExitMaxSubscriptions     = 32
	probeSpecialExitMaxSubscriptionURL   = 4096
	probeSpecialExitMaxSubscriptionHeads = 32
)

type probeSpecialExitConfig struct {
	NodeID                     string                         `json:"node_id"`
	Subscriptions              []probeSpecialExitSubscription `json:"subscriptions,omitempty"`
	Rules                      []probeSpecialExitRule         `json:"rules,omitempty"`
	Proxies                    []map[string]interface{}       `json:"proxies,omitempty"`
	Revision                   int64                          `json:"revision"`
	SHA256                     string                         `json:"sha256"`
	LastSubscriptionRefreshAt  string                         `json:"last_subscription_refresh_at,omitempty"`
	LastSubscriptionRefreshErr string                         `json:"last_subscription_refresh_error,omitempty"`
	UpdatedAt                  string                         `json:"updated_at,omitempty"`
}

type probeSpecialExitSubscription struct {
	ID                         string            `json:"id"`
	Name                       string            `json:"name"`
	Enabled                    bool              `json:"enabled"`
	URL                        string            `json:"url,omitempty"`
	Headers                    map[string]string `json:"headers,omitempty"`
	ClearHeaders               bool              `json:"clear_headers,omitempty"`
	LastSubscriptionRefreshAt  string            `json:"last_subscription_refresh_at,omitempty"`
	LastSubscriptionRefreshErr string            `json:"last_subscription_refresh_error,omitempty"`
}

type probeSpecialExitRule struct {
	ID      string   `json:"id"`
	Target  string   `json:"target"`
	Domains []string `json:"domains"`
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
		return probeSpecialExitConfig{}, err
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
		Subscriptions:              subscriptions,
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
		headers := normalizeProbeSpecialExitHeaders(raw.Headers)
		if raw.ClearHeaders {
			headers = map[string]string{}
		} else if len(headers) == 0 && hadPrior {
			headers = normalizeProbeSpecialExitHeaders(prior.Headers)
		}
		lastRefreshAt := strings.TrimSpace(raw.LastSubscriptionRefreshAt)
		lastRefreshErr := strings.TrimSpace(raw.LastSubscriptionRefreshErr)
		if hadPrior {
			lastRefreshAt = prior.LastSubscriptionRefreshAt
			lastRefreshErr = prior.LastSubscriptionRefreshErr
		}
		out = append(out, probeSpecialExitSubscription{
			ID: id, Name: name, Enabled: raw.Enabled, URL: url, Headers: headers,
			LastSubscriptionRefreshAt: lastRefreshAt, LastSubscriptionRefreshErr: lastRefreshErr,
		})
	}
	return out, nil
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
		target := strings.TrimSpace(raw.Target)
		if target == "" {
			return nil, fmt.Errorf("rules[%d] requires an exit node", index)
		}
		if len(target) > 256 || strings.ContainsAny(target, ",\r\n") {
			return nil, fmt.Errorf("rules[%d].target contains an unsupported delimiter", index)
		}
		for _, domain := range raw.Domains {
			if strings.ContainsAny(strings.TrimSpace(domain), ",:\r\n") {
				return nil, fmt.Errorf("rules[%d] contains an invalid domain", index)
			}
		}
		entries := normalizeProbeVirtualRouterRouteRuleEntries(raw.Domains)
		if len(entries) == 0 {
			return nil, fmt.Errorf("rules[%d] requires at least one domain", index)
		}
		domains := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !strings.HasPrefix(entry, "domain_suffix:") {
				return nil, fmt.Errorf("rules[%d] only supports domain names", index)
			}
			domains = append(domains, strings.TrimPrefix(entry, "domain_suffix:"))
		}
		out = append(out, probeSpecialExitRule{ID: id, Target: target, Domains: domains})
	}
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
	validate := func(target string) error {
		target = strings.TrimSpace(target)
		if _, exists := proxyNames[target]; !exists {
			return fmt.Errorf("proxy node %q does not exist", target)
		}
		return nil
	}
	for index, rule := range item.Rules {
		if err := validate(rule.Target); err != nil {
			return fmt.Errorf("rules[%d].target: %w", index, err)
		}
	}
	return nil
}

func probeSpecialExitSnapshotForConfig(item probeSpecialExitConfig) probeSpecialExitSnapshot {
	return probeSpecialExitSnapshot{Version: 2, NodeID: item.NodeID, Revision: item.Revision, SHA256: item.SHA256, Rules: append([]probeSpecialExitRule(nil), item.Rules...), Proxies: append([]map[string]interface{}(nil), item.Proxies...)}
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
	value := make([]probeSpecialExitSubscription, 0, len(item.Subscriptions))
	for _, source := range item.Subscriptions {
		value = append(value, probeSpecialExitSubscription{
			ID: strings.TrimSpace(source.ID), Name: strings.TrimSpace(source.Name), Enabled: source.Enabled,
			URL: strings.TrimSpace(source.URL), Headers: normalizeProbeSpecialExitHeaders(source.Headers),
		})
	}
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func buildProbeSpecialExitManagedRules(items []probeSpecialExitConfig) []probeVirtualRouterRouteRule {
	out := make([]probeVirtualRouterRouteRule, 0, len(items))
	for _, item := range normalizeProbeSpecialExitConfigs(items) {
		entries := make([]string, 0)
		for _, rule := range item.Rules {
			for _, domain := range rule.Domains {
				entries = append(entries, "domain_suffix:"+domain)
			}
		}
		entries = normalizeProbeVirtualRouterRouteRuleEntries(entries)
		if len(entries) == 0 {
			continue
		}
		out = append(out, probeVirtualRouterRouteRule{ID: probeSpecialExitRuleIDPrefix + item.NodeID, Name: "Mihomo exit " + item.NodeID, Action: probeVirtualRouterRouteRuleActionExit, ExitNodeID: item.NodeID, Entries: entries, Note: "managed special exit", UpdatedAt: item.UpdatedAt})
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
		for _, rule := range item.Rules {
			for _, domain := range rule.Domains {
				owned = append(owned, ownedEntry{owner: "special:" + item.NodeID, entry: "domain_suffix:" + domain})
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
