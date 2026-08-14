package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	yaml "gopkg.in/yaml.v2"
)

const (
	probeSpecialExitSubscriptionMaxBytes = 8 << 20
	probeSpecialExitSubscriptionTimeout  = 20 * time.Second
	probeSpecialExitMaxRedirects         = 5
)

type probeSpecialExitManagedView struct {
	NodeID                    string                             `json:"node_id"`
	SubscriptionConfigured    bool                               `json:"subscription_configured"`
	SubscriptionHeadersSet    bool                               `json:"subscription_headers_configured"`
	Subscriptions             []probeSpecialExitSubscriptionView `json:"subscriptions"`
	Rules                     []probeSpecialExitRuleView         `json:"rules"`
	ProxyNames                []string                           `json:"proxy_names"`
	Revision                  int64                              `json:"desired_revision"`
	SHA256                    string                             `json:"desired_sha256"`
	ManagedRule               *probeVirtualRouterRouteRule       `json:"managed_rule,omitempty"`
	LastSubscriptionRefreshAt string                             `json:"last_subscription_refresh_at,omitempty"`
	LastSubscriptionError     string                             `json:"last_subscription_refresh_error,omitempty"`
	UpdatedAt                 string                             `json:"updated_at,omitempty"`
}

type probeSpecialExitRuleView struct {
	ID      string   `json:"id"`
	Target  string   `json:"target"`
	Domains []string `json:"domains"`
}

type probeSpecialExitManagedInput struct {
	NodeID        string                         `json:"node_id"`
	Subscriptions []probeSpecialExitSubscription `json:"subscriptions"`
	Rules         []probeSpecialExitRuleView     `json:"rules"`
}

type probeSpecialExitSubscriptionView struct {
	ID                         string `json:"id"`
	Name                       string `json:"name"`
	Enabled                    bool   `json:"enabled"`
	Configured                 bool   `json:"configured"`
	HeadersConfigured          bool   `json:"headers_configured"`
	LastSubscriptionRefreshAt  string `json:"last_subscription_refresh_at,omitempty"`
	LastSubscriptionRefreshErr string `json:"last_subscription_refresh_error,omitempty"`
}

var probeSpecialExitLookupIP = net.DefaultResolver.LookupIPAddr
var probeSpecialExitDialContext = (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext
var probeSpecialExitFetchSubscription = fetchProbeSpecialExitSubscription

func listMngProbeSpecialExits() (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	ProbeRouteConfigStore.mu.RLock()
	items := append([]probeSpecialExitConfig(nil), ProbeRouteConfigStore.data.SpecialExits...)
	manual := append([]probeVirtualRouterRouteRule(nil), ProbeRouteConfigStore.data.VirtualRouter.RouteRules...)
	ProbeRouteConfigStore.mu.RUnlock()
	views := make([]probeSpecialExitManagedView, 0, len(items))
	for _, item := range items {
		views = append(views, mngProbeSpecialExitView(item))
	}
	return map[string]interface{}{
		"items":           views,
		"managed_rules":   buildProbeSpecialExitManagedRules(items),
		"effective_rules": buildEffectiveProbeVirtualRouterRouteRules(manual, items),
		"nodes":           listMngProbeSpecialExitCandidateNodes(),
	}, nil
}

func mngProbeSpecialExitView(item probeSpecialExitConfig) probeSpecialExitManagedView {
	proxyNames := make([]string, 0, len(item.Proxies))
	for _, proxy := range item.Proxies {
		if name := strings.TrimSpace(fmt.Sprint(proxy["name"])); name != "" {
			proxyNames = append(proxyNames, name)
		}
	}
	sort.Strings(proxyNames)
	subscriptions := make([]probeSpecialExitSubscriptionView, 0, len(item.Subscriptions))
	for _, source := range item.Subscriptions {
		subscriptions = append(subscriptions, probeSpecialExitSubscriptionView{
			ID: source.ID, Name: source.Name, Enabled: source.Enabled,
			Configured: strings.TrimSpace(source.URL) != "", HeadersConfigured: len(source.Headers) > 0,
			LastSubscriptionRefreshAt: source.LastSubscriptionRefreshAt, LastSubscriptionRefreshErr: source.LastSubscriptionRefreshErr,
		})
	}
	rules := make([]probeSpecialExitRuleView, 0, len(item.Rules))
	for _, rule := range item.Rules {
		rules = append(rules, probeSpecialExitRuleView{ID: rule.ID, Target: rule.Target, Domains: append([]string(nil), rule.Domains...)})
	}
	view := probeSpecialExitManagedView{
		NodeID:                 item.NodeID,
		SubscriptionConfigured: probeSpecialExitHasConfiguredSubscription(item), SubscriptionHeadersSet: probeSpecialExitHasSubscriptionHeaders(item), Subscriptions: subscriptions,
		Rules: rules, ProxyNames: proxyNames,
		Revision: item.Revision, SHA256: item.SHA256, LastSubscriptionRefreshAt: item.LastSubscriptionRefreshAt,
		LastSubscriptionError: item.LastSubscriptionRefreshErr, UpdatedAt: item.UpdatedAt,
	}
	managed := buildProbeSpecialExitManagedRules([]probeSpecialExitConfig{item})
	if len(managed) == 1 {
		value := managed[0]
		view.ManagedRule = &value
	}
	return view
}

func listMngProbeSpecialExitCandidateNodes() []probeNodeRecord {
	if ProbeStore == nil {
		return []probeNodeRecord{}
	}
	ProbeStore.mu.RLock()
	defer ProbeStore.mu.RUnlock()
	out := make([]probeNodeRecord, 0)
	for _, node := range loadProbeNodesLocked() {
		if normalizeProbeNodeKind(node.NodeKind) == probeNodeKindMihomoExit {
			node.NodeSecret = ""
			out = append(out, node)
		}
	}
	return out
}

func upsertMngProbeSpecialExit(payload json.RawMessage, controllerBaseURL string) (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	var req struct {
		Item probeSpecialExitManagedInput `json:"item"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return nil, fmt.Errorf("invalid payload")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid payload")
	}
	nodeID := normalizeProbeNodeID(req.Item.NodeID)
	node, ok := getProbeNodeByID(nodeID)
	if !ok {
		return nil, fmt.Errorf("node %q not found", nodeID)
	}
	if normalizeProbeNodeKind(node.NodeKind) != probeNodeKindMihomoExit {
		return nil, fmt.Errorf("node %q must use node_kind mihomo_exit", nodeID)
	}

	var item probeSpecialExitConfig
	err := ProbeRouteConfigStore.update(func(data *probeRouteConfigStoreData) error {
		currentItems := append([]probeSpecialExitConfig(nil), data.SpecialExits...)
		manual := append([]probeVirtualRouterRouteRule(nil), data.VirtualRouter.RouteRules...)
		var previous *probeSpecialExitConfig
		for index := range currentItems {
			if currentItems[index].NodeID == nodeID {
				value := currentItems[index]
				previous = &value
				break
			}
		}
		rules := make([]probeSpecialExitRule, 0, len(req.Item.Rules))
		for _, rule := range req.Item.Rules {
			rules = append(rules, probeSpecialExitRule{ID: rule.ID, Target: rule.Target, Domains: append([]string(nil), rule.Domains...)})
		}
		raw := probeSpecialExitConfig{
			NodeID: nodeID, Subscriptions: req.Item.Subscriptions, Rules: rules,
		}
		var normalizeErr error
		item, normalizeErr = normalizeProbeSpecialExitConfig(raw, previous)
		if normalizeErr != nil {
			return normalizeErr
		}
		item.Proxies = []map[string]interface{}{}
		if previous != nil {
			item.LastSubscriptionRefreshAt = previous.LastSubscriptionRefreshAt
			item.LastSubscriptionRefreshErr = previous.LastSubscriptionRefreshErr
			item.Proxies = previous.Proxies
		}
		if validateErr := validateProbeSpecialExitResolvedPolicies(item); validateErr != nil {
			return validateErr
		}
		changed := previous == nil || probeSpecialExitSemanticHash(*previous) != probeSpecialExitSemanticHash(item)
		if previous == nil {
			item.Revision = 1
		} else if changed {
			item.Revision = previous.Revision + 1
		} else {
			item.Revision = previous.Revision
		}
		now := time.Now().UTC()
		item.UpdatedAt = now.Format(time.RFC3339)
		item.SHA256 = probeSpecialExitSnapshotHash(item)
		next := make([]probeSpecialExitConfig, 0, len(currentItems)+1)
		replaced := false
		for _, current := range currentItems {
			if current.NodeID == nodeID {
				next = append(next, item)
				replaced = true
			} else {
				next = append(next, current)
			}
		}
		if !replaced {
			next = append(next, item)
		}
		next = normalizeProbeSpecialExitConfigs(next)
		if validateErr := validateProbeSpecialExitConflicts(manual, next); validateErr != nil {
			return validateErr
		}
		data.SpecialExits = next
		data.VirtualRouterFakeIP, _ = reconcileProbeVirtualRouterFakeIPLibraryWithRouteRules(data.VirtualRouterFakeIP, buildEffectiveProbeVirtualRouterRouteRules(manual, next), now)
		return nil
	})
	if err != nil {
		return nil, err
	}
	syncResult := dispatchProbeRouteConfigSyncToKnownNodes(controllerBaseURL)
	return map[string]interface{}{"ok": true, "item": mngProbeSpecialExitView(item), "sync": syncResult}, nil
}

func deleteMngProbeSpecialExit(nodeID string, controllerBaseURL string) (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	nodeID = normalizeProbeNodeID(nodeID)
	err := ProbeRouteConfigStore.update(func(data *probeRouteConfigStoreData) error {
		next := make([]probeSpecialExitConfig, 0, len(data.SpecialExits))
		found := false
		for _, item := range data.SpecialExits {
			if item.NodeID == nodeID {
				found = true
				continue
			}
			next = append(next, item)
		}
		if !found {
			return fmt.Errorf("special exit %q not found", nodeID)
		}
		data.SpecialExits = next
		data.VirtualRouterFakeIP, _ = reconcileProbeVirtualRouterFakeIPLibraryWithRouteRules(data.VirtualRouterFakeIP, buildEffectiveProbeVirtualRouterRouteRules(data.VirtualRouter.RouteRules, next), time.Now().UTC())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"ok": true, "node_id": nodeID, "sync": dispatchProbeRouteConfigSyncToKnownNodes(controllerBaseURL)}, nil
}

func refreshMngProbeSpecialExitSubscription(ctx context.Context, nodeID string, controllerBaseURL string) (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	nodeID = normalizeProbeNodeID(nodeID)
	ProbeRouteConfigStore.mu.RLock()
	item, ok := findProbeSpecialExitByNodeID(ProbeRouteConfigStore.data.SpecialExits, nodeID)
	ProbeRouteConfigStore.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("special exit %q not found", nodeID)
	}
	sourceHash := probeSpecialExitSubscriptionSourceHash(item)
	enabled := make([]probeSpecialExitSubscription, 0, len(item.Subscriptions))
	for _, source := range item.Subscriptions {
		if source.Enabled && strings.TrimSpace(source.URL) != "" {
			enabled = append(enabled, source)
		}
	}
	if len(enabled) == 0 {
		return nil, fmt.Errorf("no enabled subscription sources are configured")
	}
	type fetchResult struct {
		source  probeSpecialExitSubscription
		proxies []map[string]interface{}
		err     error
	}
	results := make(chan fetchResult, len(enabled))
	for _, source := range enabled {
		source := source
		go func() {
			content, fetchErr := probeSpecialExitFetchSubscription(ctx, source.URL, source.Headers)
			if fetchErr != nil {
				results <- fetchResult{source: source, err: fetchErr}
				return
			}
			proxies, parseErr := parseProbeSpecialExitSubscription(content)
			results <- fetchResult{source: source, proxies: proxies, err: parseErr}
		}()
	}
	resultsByID := make(map[string]fetchResult, len(enabled))
	for range enabled {
		result := <-results
		resultsByID[result.source.ID] = result
	}
	merged := make([]map[string]interface{}, 0)
	refreshedSourceIDs := make([]string, 0, len(enabled))
	var firstFailure *fetchResult
	for _, source := range enabled {
		result := resultsByID[source.ID]
		if result.err != nil {
			recordProbeSpecialExitRefreshError(nodeID, sourceHash, result.source.ID, result.err)
			if firstFailure == nil {
				failure := result
				firstFailure = &failure
			}
			continue
		}
		refreshedSourceIDs = append(refreshedSourceIDs, result.source.ID)
		merged = append(merged, result.proxies...)
	}
	if firstFailure != nil {
		return nil, fmt.Errorf("subscription source %q refresh failed: %w", firstFailure.source.Name, firstFailure.err)
	}
	proxies, err := normalizeProbeSpecialExitProxies(merged)
	if err != nil {
		recordProbeSpecialExitRefreshError(nodeID, sourceHash, "", err)
		return nil, fmt.Errorf("merged subscriptions are invalid: %w", err)
	}
	item, err = applyProbeSpecialExitSubscriptionRefreshSources(nodeID, sourceHash, proxies, refreshedSourceIDs, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"ok": true, "item": mngProbeSpecialExitView(item), "proxy_count": len(proxies), "subscription_count": len(enabled), "sync": dispatchProbeRouteConfigSyncToKnownNodes(controllerBaseURL)}, nil
}

func applyProbeSpecialExitSubscriptionRefresh(nodeID, sourceHash string, proxies []map[string]interface{}, refreshedAt time.Time) (probeSpecialExitConfig, error) {
	return applyProbeSpecialExitSubscriptionRefreshSources(nodeID, sourceHash, proxies, nil, refreshedAt)
}

func applyProbeSpecialExitSubscriptionRefreshSources(nodeID, sourceHash string, proxies []map[string]interface{}, refreshedSourceIDs []string, refreshedAt time.Time) (probeSpecialExitConfig, error) {
	nodeID = normalizeProbeNodeID(nodeID)
	var item probeSpecialExitConfig
	err := ProbeRouteConfigStore.update(func(data *probeRouteConfigStoreData) error {
		currentItems := append([]probeSpecialExitConfig(nil), data.SpecialExits...)
		manual := append([]probeVirtualRouterRouteRule(nil), data.VirtualRouter.RouteRules...)
		var ok bool
		item, ok = findProbeSpecialExitByNodeID(currentItems, nodeID)
		if !ok {
			return fmt.Errorf("special exit %q was removed while refreshing", nodeID)
		}
		if probeSpecialExitSubscriptionSourceHash(item) != strings.ToLower(strings.TrimSpace(sourceHash)) {
			return fmt.Errorf("special exit %q subscription changed while refreshing", nodeID)
		}
		item.Proxies = proxies
		if validateErr := validateProbeSpecialExitResolvedPolicies(item); validateErr != nil {
			return validateErr
		}
		item.Revision++
		if item.Revision <= 0 {
			item.Revision = 1
		}
		item.LastSubscriptionRefreshAt = refreshedAt.Format(time.RFC3339)
		item.LastSubscriptionRefreshErr = ""
		refreshedSet := make(map[string]struct{}, len(refreshedSourceIDs))
		for _, id := range refreshedSourceIDs {
			refreshedSet[strings.TrimSpace(id)] = struct{}{}
		}
		for index := range item.Subscriptions {
			if len(refreshedSet) == 0 || containsProbeSpecialExitSubscriptionID(refreshedSet, item.Subscriptions[index].ID) {
				item.Subscriptions[index].LastSubscriptionRefreshAt = item.LastSubscriptionRefreshAt
				item.Subscriptions[index].LastSubscriptionRefreshErr = ""
			}
		}
		item.UpdatedAt = item.LastSubscriptionRefreshAt
		item.SHA256 = probeSpecialExitSnapshotHash(item)
		for index := range currentItems {
			if currentItems[index].NodeID == nodeID {
				currentItems[index] = item
				break
			}
		}
		if validateErr := validateProbeSpecialExitConflicts(manual, currentItems); validateErr != nil {
			return validateErr
		}
		data.SpecialExits = currentItems
		data.VirtualRouterFakeIP, _ = reconcileProbeVirtualRouterFakeIPLibraryWithRouteRules(data.VirtualRouterFakeIP, buildEffectiveProbeVirtualRouterRouteRules(manual, currentItems), refreshedAt)
		return nil
	})
	if err != nil {
		return probeSpecialExitConfig{}, err
	}
	return item, nil
}

func recordProbeSpecialExitRefreshError(nodeID, sourceHash, sourceID string, refreshErr error) {
	if ProbeRouteConfigStore == nil {
		return
	}
	_ = ProbeRouteConfigStore.update(func(data *probeRouteConfigStoreData) error {
		for index := range data.SpecialExits {
			if data.SpecialExits[index].NodeID == nodeID && probeSpecialExitSubscriptionSourceHash(data.SpecialExits[index]) == strings.ToLower(strings.TrimSpace(sourceHash)) {
				data.SpecialExits[index].LastSubscriptionRefreshErr = strings.TrimSpace(refreshErr.Error())
				for sourceIndex := range data.SpecialExits[index].Subscriptions {
					if strings.TrimSpace(sourceID) == "" || data.SpecialExits[index].Subscriptions[sourceIndex].ID == strings.TrimSpace(sourceID) {
						data.SpecialExits[index].Subscriptions[sourceIndex].LastSubscriptionRefreshErr = strings.TrimSpace(refreshErr.Error())
					}
				}
				break
			}
		}
		return nil
	})
}

func containsProbeSpecialExitSubscriptionID(items map[string]struct{}, id string) bool {
	_, ok := items[strings.TrimSpace(id)]
	return ok
}

func probeSpecialExitHasConfiguredSubscription(item probeSpecialExitConfig) bool {
	for _, source := range item.Subscriptions {
		if strings.TrimSpace(source.URL) != "" {
			return true
		}
	}
	return false
}

func probeSpecialExitHasSubscriptionHeaders(item probeSpecialExitConfig) bool {
	for _, source := range item.Subscriptions {
		if len(source.Headers) > 0 {
			return true
		}
	}
	return false
}

func parseProbeSpecialExitSubscription(content []byte) ([]map[string]interface{}, error) {
	var raw struct {
		Proxies        []map[interface{}]interface{} `yaml:"proxies"`
		ProxyProviders map[string]interface{}        `yaml:"proxy-providers"`
	}
	if err := yaml.Unmarshal(content, &raw); err != nil {
		return nil, fmt.Errorf("subscription YAML is invalid: %w", err)
	}
	if len(raw.ProxyProviders) > 0 {
		return nil, fmt.Errorf("remote proxy-providers are not supported")
	}
	converted := make([]map[string]interface{}, 0, len(raw.Proxies))
	for index, item := range raw.Proxies {
		value, err := probeSpecialExitStringMap(item)
		if err != nil {
			return nil, fmt.Errorf("proxies[%d]: %w", index, err)
		}
		converted = append(converted, value)
	}
	return normalizeProbeSpecialExitProxies(converted)
}

func probeSpecialExitStringMap(input map[interface{}]interface{}) (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(input))
	for rawKey, rawValue := range input {
		key, ok := rawKey.(string)
		if !ok {
			return nil, fmt.Errorf("object key must be a string")
		}
		value, err := probeSpecialExitYAMLValue(rawValue)
		if err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, nil
}

func probeSpecialExitYAMLValue(input interface{}) (interface{}, error) {
	switch value := input.(type) {
	case map[interface{}]interface{}:
		return probeSpecialExitStringMap(value)
	case []interface{}:
		out := make([]interface{}, len(value))
		for index := range value {
			converted, err := probeSpecialExitYAMLValue(value[index])
			if err != nil {
				return nil, err
			}
			out[index] = converted
		}
		return out, nil
	case nil, bool, int, int64, uint64, float64, string:
		return value, nil
	default:
		return fmt.Sprint(value), nil
	}
}

func fetchProbeSpecialExitSubscription(parent context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, probeSpecialExitSubscriptionTimeout)
	defer cancel()
	target, ips, err := validateProbeSpecialExitSubscriptionURL(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: target.Hostname()}, Proxy: nil}
	transport.DialContext = func(dialCtx context.Context, network, _ string) (net.Conn, error) {
		return probeSpecialExitDialContext(dialCtx, network, net.JoinHostPort(ips[0].String(), "443"))
	}
	client := &http.Client{Transport: transport, Timeout: probeSpecialExitSubscriptionTimeout}
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= probeSpecialExitMaxRedirects {
			return fmt.Errorf("too many subscription redirects")
		}
		return fmt.Errorf("subscription redirects are disabled")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("subscription fetch timed out or was canceled")
		}
		return nil, fmt.Errorf("subscription fetch failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription returned HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, probeSpecialExitSubscriptionMaxBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(content) > probeSpecialExitSubscriptionMaxBytes {
		return nil, fmt.Errorf("subscription exceeded %d bytes", probeSpecialExitSubscriptionMaxBytes)
	}
	return content, nil
}

func validateProbeSpecialExitSubscriptionURL(ctx context.Context, raw string) (*url.URL, []netip.Addr, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target == nil || !strings.EqualFold(target.Scheme, "https") || target.User != nil || strings.TrimSpace(target.Hostname()) == "" {
		return nil, nil, fmt.Errorf("subscription_url must be an HTTPS URL without credentials")
	}
	if port := strings.TrimSpace(target.Port()); port != "" && port != "443" {
		return nil, nil, fmt.Errorf("subscription_url port must be 443")
	}
	resolved, err := probeSpecialExitLookupIP(ctx, target.Hostname())
	if err != nil || len(resolved) == 0 {
		return nil, nil, fmt.Errorf("subscription host resolution failed")
	}
	ips := make([]netip.Addr, 0, len(resolved))
	for _, item := range resolved {
		addr, ok := netip.AddrFromSlice(item.IP)
		if !ok {
			return nil, nil, fmt.Errorf("subscription host returned an invalid address")
		}
		addr = addr.Unmap()
		if !probeSpecialExitPublicAddr(addr) {
			return nil, nil, fmt.Errorf("subscription host resolves to a non-public address")
		}
		ips = append(ips, addr)
	}
	return target, ips, nil
}

func probeSpecialExitPublicAddr(addr netip.Addr) bool {
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
		return false
	}
	blocked := []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "::/128", "::1/128", "fc00::/7", "fe80::/10", "2001:db8::/32"}
	for _, raw := range blocked {
		prefix := netip.MustParsePrefix(raw)
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func listMngProbeSpecialExitStatuses() (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	ProbeRouteConfigStore.mu.RLock()
	items := append([]probeSpecialExitConfig(nil), ProbeRouteConfigStore.data.SpecialExits...)
	ProbeRouteConfigStore.mu.RUnlock()
	statuses := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		runtime, found := getProbeRuntime(item.NodeID)
		status := map[string]interface{}{
			"node_id": item.NodeID, "desired_revision": item.Revision, "desired_sha256": item.SHA256,
			"build_kind": runtime.BuildKind, "probe_version": runtime.Version, "online": found && runtime.Online,
			"applied_revision": runtime.SpecialExit.AppliedRevision, "applied_sha256": runtime.SpecialExit.AppliedSHA256,
			"exit_ready": runtime.SpecialExit.ExitReady, "healthy": runtime.SpecialExit.Healthy, "mihomo_version": runtime.SpecialExit.MihomoVersion,
			"active_sessions": runtime.SpecialExit.ActiveSessions, "bytes_up": runtime.SpecialExit.BytesUp, "bytes_down": runtime.SpecialExit.BytesDown,
			"last_apply_error": runtime.SpecialExit.LastApplyError, "last_seen": runtime.LastSeen,
		}
		statuses = append(statuses, status)
	}
	return map[string]interface{}{"items": statuses}, nil
}

func buildMngProbeSpecialExitInstallInfo(nodeID string, mode string, controllerBaseURL string) (map[string]interface{}, error) {
	nodeID = normalizeProbeNodeID(nodeID)
	node, ok := getProbeNodeByID(nodeID)
	if !ok {
		return nil, fmt.Errorf("node %q not found", nodeID)
	}
	if normalizeProbeNodeKind(node.NodeKind) != probeNodeKindMihomoExit {
		return nil, fmt.Errorf("node %q must use node_kind mihomo_exit", nodeID)
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "native"
	}
	if mode != "native" && mode != "docker" {
		return nil, fmt.Errorf("mode must be native or docker")
	}
	base := strings.TrimRight(strings.TrimSpace(controllerBaseURL), "/")
	env := map[string]string{"PROBE_NODE_ID": nodeID, "PROBE_NODE_SECRET": node.NodeSecret, "PROBE_CONTROLLER_URL": base}
	result := map[string]interface{}{"node_id": nodeID, "mode": mode, "environment": env, "platform": "linux", "architecture": "amd64"}
	if mode == "native" {
		scriptURL := base + "/api/probe/proxy/probe-exit-node/install-script?node_id=" + url.QueryEscape(nodeID) + "&secret=" + url.QueryEscape(node.NodeSecret)
		result["script_url"] = scriptURL
		result["command"] = "curl -fsSL " + shellQuoteProbeSpecialExit(scriptURL) + " | sudo env PROBE_NODE_ID=" + shellQuoteProbeSpecialExit(nodeID) + " PROBE_NODE_SECRET=" + shellQuoteProbeSpecialExit(node.NodeSecret) + " PROBE_CONTROLLER_URL=" + shellQuoteProbeSpecialExit(base) + " bash"
	} else {
		result["compose_directory"] = "docker/probe_exit_node"
	}
	return result, nil
}

func shellQuoteProbeSpecialExit(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
