package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	yaml "gopkg.in/yaml.v2"
)

const (
	probeSpecialExitSubscriptionMaxBytes = 8 << 20
	probeSpecialExitSubscriptionTimeout  = 20 * time.Second
)

type probeSpecialExitManagedView struct {
	NodeID    string                     `json:"node_id"`
	Rules     []probeSpecialExitRuleView `json:"rules"`
	Revision  int64                      `json:"desired_revision"`
	SHA256    string                     `json:"desired_sha256"`
	UpdatedAt string                     `json:"updated_at,omitempty"`
}

type probeSpecialExitProxyOption struct {
	Name             string `json:"name"`
	SubscriptionID   string `json:"subscription_id,omitempty"`
	SubscriptionName string `json:"subscription_name,omitempty"`
}

type probeSpecialExitRuleView struct {
	RouteRuleID string   `json:"route_rule_id"`
	Name        string   `json:"name"`
	Entries     []string `json:"entries"`
	Target      string   `json:"target"`
}

type probeSpecialExitRuleInput struct {
	RouteRuleID string `json:"route_rule_id"`
	Target      string `json:"target"`
}

type probeSpecialExitManagedInput struct {
	NodeID string                      `json:"node_id"`
	Rules  []probeSpecialExitRuleInput `json:"rules"`
}

type probeSpecialExitLibraryInput struct {
	Subscriptions []probeSpecialExitSubscriptionInput `json:"subscriptions"`
}

type probeSpecialExitSubscriptionInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	URL         string `json:"url"`
	FetchNodeID string `json:"fetch_node_id"`
}

type probeSpecialExitSubscriptionView struct {
	ID                         string `json:"id"`
	Name                       string `json:"name"`
	Enabled                    bool   `json:"enabled"`
	Configured                 bool   `json:"configured"`
	FetchNodeID                string `json:"fetch_node_id,omitempty"`
	LastSubscriptionRefreshAt  string `json:"last_subscription_refresh_at,omitempty"`
	LastSubscriptionRefreshErr string `json:"last_subscription_refresh_error,omitempty"`
}

type probeSpecialExitSubscriptionParseResult struct {
	Proxies           []map[string]interface{}
	SkippedProxyCount int
}

var errProbeSpecialExitAnyTLSRealityUnsupported = errors.New("AnyTLS+Reality is not supported by Mihomo")

var probeSpecialExitFetchSubscriptionFromNode = fetchProbeSpecialExitSubscriptionFromNode

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
		views = append(views, mngProbeSpecialExitView(item, manual))
	}
	return map[string]interface{}{
		"items":       views,
		"route_rules": manual,
		"nodes":       listMngProbeSpecialExitCandidateNodes(),
	}, nil
}

func mngProbeSpecialExitView(item probeSpecialExitConfig, routeRules []probeVirtualRouterRouteRule) probeSpecialExitManagedView {
	selected := make(map[string]string, len(item.Rules))
	for _, rule := range item.Rules {
		selected[strings.TrimSpace(rule.RouteRuleID)] = normalizeProbeSpecialExitTarget(rule.Target)
	}
	rules := make([]probeSpecialExitRuleView, 0, len(selected))
	for _, routeRule := range normalizeProbeVirtualRouterRouteRules(routeRules) {
		if !probeSpecialExitRouteRuleAssignedToNode(routeRule, item.NodeID) {
			continue
		}
		target := probeSpecialExitDirectTarget
		if value, ok := selected[routeRule.ID]; ok {
			target = value
		}
		rules = append(rules, probeSpecialExitRuleView{
			RouteRuleID: routeRule.ID,
			Name:        routeRule.Name,
			Entries:     append([]string(nil), routeRule.Entries...),
			Target:      target,
		})
	}
	view := probeSpecialExitManagedView{NodeID: item.NodeID, Rules: rules, Revision: item.Revision, SHA256: item.SHA256, UpdatedAt: item.UpdatedAt}
	return view
}

func mngProbeSpecialExitLibraryView(library probeSpecialExitLibrary) map[string]interface{} {
	proxyNames := make([]string, 0, len(library.Proxies))
	proxyOptions := make([]probeSpecialExitProxyOption, 0, len(library.Proxies))
	subscriptionNames := make(map[string]string, len(library.Subscriptions))
	subscriptions := make([]probeSpecialExitSubscriptionView, 0, len(library.Subscriptions))
	for _, source := range library.Subscriptions {
		subscriptionNames[strings.TrimSpace(source.ID)] = strings.TrimSpace(source.Name)
		subscriptions = append(subscriptions, probeSpecialExitSubscriptionView{
			ID: source.ID, Name: source.Name, Enabled: source.Enabled,
			Configured: strings.TrimSpace(source.URL) != "", FetchNodeID: source.FetchNodeID, LastSubscriptionRefreshAt: source.LastSubscriptionRefreshAt,
			LastSubscriptionRefreshErr: source.LastSubscriptionRefreshErr,
		})
	}
	for _, proxy := range library.Proxies {
		name := strings.TrimSpace(fmt.Sprint(proxy["name"]))
		if name == "" {
			continue
		}
		proxyNames = append(proxyNames, name)
		sourceID := strings.TrimSpace(library.ProxySourceIDs[name])
		proxyOptions = append(proxyOptions, probeSpecialExitProxyOption{Name: name, SubscriptionID: sourceID, SubscriptionName: subscriptionNames[sourceID]})
	}
	sort.Strings(proxyNames)
	sort.SliceStable(proxyOptions, func(i, j int) bool {
		if proxyOptions[i].SubscriptionName != proxyOptions[j].SubscriptionName {
			return proxyOptions[i].SubscriptionName < proxyOptions[j].SubscriptionName
		}
		return proxyOptions[i].Name < proxyOptions[j].Name
	})
	return map[string]interface{}{
		"subscriptions": subscriptions, "proxy_names": proxyNames, "proxy_options": proxyOptions,
		"last_subscription_refresh_at":    library.LastSubscriptionRefreshAt,
		"last_subscription_refresh_error": library.LastSubscriptionRefreshErr, "updated_at": library.UpdatedAt,
	}
}

func getMngProbeSpecialExitLibrary() (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	ProbeRouteConfigStore.mu.RLock()
	library := ProbeRouteConfigStore.data.SpecialExitLibrary
	ProbeRouteConfigStore.mu.RUnlock()
	return mngProbeSpecialExitLibraryView(library), nil
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

func upsertMngProbeSpecialExitLibrary(payload json.RawMessage, controllerBaseURL string) (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	var req struct {
		Item probeSpecialExitLibraryInput `json:"item"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return nil, fmt.Errorf("invalid payload")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid payload")
	}
	var library probeSpecialExitLibrary
	affected := []string{}
	err := ProbeRouteConfigStore.update(func(data *probeRouteConfigStoreData) error {
		previous := data.SpecialExitLibrary
		subscriptions := make([]probeSpecialExitSubscription, 0, len(req.Item.Subscriptions))
		for _, source := range req.Item.Subscriptions {
			subscriptions = append(subscriptions, probeSpecialExitSubscription{ID: source.ID, Name: source.Name, Enabled: source.Enabled, URL: source.URL, FetchNodeID: source.FetchNodeID})
		}
		raw := probeSpecialExitLibrary{
			Subscriptions: subscriptions, Proxies: previous.Proxies, ProxySourceIDs: previous.ProxySourceIDs,
			LastSubscriptionRefreshAt:  previous.LastSubscriptionRefreshAt,
			LastSubscriptionRefreshErr: previous.LastSubscriptionRefreshErr,
		}
		var err error
		library, err = normalizeProbeSpecialExitLibrary(raw, &previous)
		if err != nil {
			return err
		}
		if err := validateMngProbeSpecialExitSubscriptionFetchNodes(library.Subscriptions); err != nil {
			return err
		}
		validSources := make(map[string]struct{}, len(library.Subscriptions))
		for _, source := range library.Subscriptions {
			validSources[source.ID] = struct{}{}
		}
		kept := make([]map[string]interface{}, 0, len(library.Proxies))
		keptSources := make(map[string]string, len(library.ProxySourceIDs))
		for _, proxy := range library.Proxies {
			name := strings.TrimSpace(fmt.Sprint(proxy["name"]))
			sourceID := strings.TrimSpace(library.ProxySourceIDs[name])
			if _, ok := validSources[sourceID]; !ok {
				continue
			}
			kept = append(kept, proxy)
			keptSources[name] = sourceID
		}
		library.Proxies = kept
		library.ProxySourceIDs = keptSources
		library.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		var rebuildErr error
		data.SpecialExits, affected, rebuildErr = rebuildProbeSpecialExitsFromLibrary(data.SpecialExits, library, time.Now().UTC())
		if rebuildErr != nil {
			return rebuildErr
		}
		data.SpecialExitLibrary = library
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"ok": true, "library": mngProbeSpecialExitLibraryView(library),
		"sync": dispatchProbeRouteConfigSyncToNodes(affected, controllerBaseURL),
	}, nil
}

func validateMngProbeSpecialExitSubscriptionFetchNodes(subscriptions []probeSpecialExitSubscription) error {
	for index, source := range subscriptions {
		fetchNodeID := normalizeProbeNodeID(source.FetchNodeID)
		if source.Enabled && strings.TrimSpace(source.URL) != "" && fetchNodeID == "" {
			return fmt.Errorf("subscriptions[%d].fetch_node_id is required", index)
		}
		if fetchNodeID == "" {
			continue
		}
		node, ok := getProbeNodeByID(fetchNodeID)
		if !ok {
			return fmt.Errorf("subscriptions[%d].fetch_node_id probe not found", index)
		}
		if strings.EqualFold(strings.TrimSpace(node.TargetSystem), "android") {
			return fmt.Errorf("subscriptions[%d].fetch_node_id uses an unsupported Android probe", index)
		}
	}
	return nil
}

func rebuildProbeSpecialExitsFromLibrary(items []probeSpecialExitConfig, library probeSpecialExitLibrary, now time.Time) ([]probeSpecialExitConfig, []string, error) {
	next := append([]probeSpecialExitConfig(nil), items...)
	affected := make([]string, 0)
	for index := range next {
		previous := next[index]
		proxies, err := resolveProbeSpecialExitProxies(previous.Rules, library.Proxies)
		if err != nil {
			return nil, nil, fmt.Errorf("special exit %q: %w", previous.NodeID, err)
		}
		next[index].Proxies = proxies
		if err := validateProbeSpecialExitResolvedPolicies(next[index]); err != nil {
			return nil, nil, fmt.Errorf("special exit %q: %w", previous.NodeID, err)
		}
		if probeSpecialExitSemanticHash(previous) != probeSpecialExitSemanticHash(next[index]) {
			next[index].Revision = previous.Revision + 1
			if next[index].Revision <= 0 {
				next[index].Revision = 1
			}
			next[index].UpdatedAt = now.Format(time.RFC3339)
			next[index].SHA256 = probeSpecialExitSnapshotHash(next[index])
			affected = append(affected, next[index].NodeID)
		}
	}
	return next, affected, nil
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
		selections := make([]probeSpecialExitRule, 0, len(req.Item.Rules))
		for _, rule := range req.Item.Rules {
			selections = append(selections, probeSpecialExitRule{RouteRuleID: rule.RouteRuleID, Target: rule.Target})
		}
		rules, compileErr := compileProbeSpecialExitRules(nodeID, selections, manual, true)
		if compileErr != nil {
			return compileErr
		}
		raw := probeSpecialExitConfig{NodeID: nodeID, Rules: rules}
		var normalizeErr error
		item, normalizeErr = normalizeProbeSpecialExitConfig(raw, previous)
		if normalizeErr != nil {
			return normalizeErr
		}
		item.Proxies, normalizeErr = resolveProbeSpecialExitProxies(item.Rules, data.SpecialExitLibrary.Proxies)
		if normalizeErr != nil {
			return normalizeErr
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
		data.SpecialExits = next
		data.VirtualRouterFakeIP, _ = reconcileProbeVirtualRouterFakeIPLibraryWithRouteRules(data.VirtualRouterFakeIP, manual, now)
		return nil
	})
	if err != nil {
		return nil, err
	}
	syncResult := dispatchProbeRouteConfigSyncToNodes([]string{nodeID}, controllerBaseURL)
	ProbeRouteConfigStore.mu.RLock()
	routeRules := append([]probeVirtualRouterRouteRule(nil), ProbeRouteConfigStore.data.VirtualRouter.RouteRules...)
	ProbeRouteConfigStore.mu.RUnlock()
	return map[string]interface{}{"ok": true, "item": mngProbeSpecialExitView(item, routeRules), "sync": syncResult}, nil
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
		data.VirtualRouterFakeIP, _ = reconcileProbeVirtualRouterFakeIPLibraryWithRouteRules(data.VirtualRouterFakeIP, data.VirtualRouter.RouteRules, time.Now().UTC())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"ok": true, "node_id": nodeID, "sync": dispatchProbeRouteConfigSyncToNodes([]string{nodeID}, controllerBaseURL)}, nil
}

func refreshMngProbeSpecialExitSubscription(ctx context.Context, subscriptionID, controllerBaseURL string) (map[string]interface{}, error) {
	source, sourceHash, err := loadMngProbeSpecialExitSubscriptionSource(subscriptionID)
	if err != nil {
		return nil, err
	}
	content, err := probeSpecialExitFetchSubscriptionFromNode(ctx, source.FetchNodeID, source.URL)
	if err != nil {
		recordProbeSpecialExitRefreshError(sourceHash, source.ID, err)
		return nil, fmt.Errorf("subscription source %q refresh failed: %w", source.Name, err)
	}
	return applyMngProbeSpecialExitSubscriptionContent(source, sourceHash, content, controllerBaseURL)
}

func loadMngProbeSpecialExitSubscriptionSource(subscriptionID string) (probeSpecialExitSubscription, string, error) {
	if ProbeRouteConfigStore == nil {
		return probeSpecialExitSubscription{}, "", fmt.Errorf("probe route config store is not initialized")
	}
	subscriptionID = strings.TrimSpace(subscriptionID)
	if subscriptionID == "" {
		return probeSpecialExitSubscription{}, "", fmt.Errorf("subscription_id is required")
	}
	ProbeRouteConfigStore.mu.RLock()
	library := ProbeRouteConfigStore.data.SpecialExitLibrary
	ProbeRouteConfigStore.mu.RUnlock()
	sourceHash := probeSpecialExitLibrarySourceHash(library)
	var source probeSpecialExitSubscription
	foundSource := false
	for _, candidate := range library.Subscriptions {
		if strings.TrimSpace(candidate.ID) == subscriptionID {
			source = candidate
			foundSource = true
			break
		}
	}
	if !foundSource {
		return probeSpecialExitSubscription{}, "", fmt.Errorf("subscription source %q not found", subscriptionID)
	}
	if !source.Enabled {
		return probeSpecialExitSubscription{}, "", fmt.Errorf("subscription source %q is disabled", source.Name)
	}
	if strings.TrimSpace(source.URL) == "" {
		return probeSpecialExitSubscription{}, "", fmt.Errorf("subscription source %q URL is not configured", source.Name)
	}
	if normalizeProbeNodeID(source.FetchNodeID) == "" {
		return probeSpecialExitSubscription{}, "", fmt.Errorf("subscription source %q fetch probe is not configured", source.Name)
	}
	return source, sourceHash, nil
}

func applyMngProbeSpecialExitSubscriptionContent(source probeSpecialExitSubscription, sourceHash string, content []byte, controllerBaseURL string) (map[string]interface{}, error) {
	parsed, err := parseProbeSpecialExitSubscriptionWithResult(content)
	if err != nil {
		recordProbeSpecialExitRefreshError(sourceHash, source.ID, err)
		return nil, fmt.Errorf("subscription source %q refresh failed: %w", source.Name, err)
	}
	proxies, err := normalizeProbeSpecialExitProxies(parsed.Proxies)
	if err != nil {
		recordProbeSpecialExitRefreshError(sourceHash, source.ID, err)
		return nil, fmt.Errorf("subscription source %q is invalid: %w", source.Name, err)
	}
	library, affected, err := applyProbeSpecialExitLibrarySubscriptionRefresh(sourceHash, source.ID, proxies, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"ok": true, "library": mngProbeSpecialExitLibraryView(library), "proxy_count": len(proxies),
		"skipped_proxy_count": parsed.SkippedProxyCount, "subscription_id": source.ID,
		"sync": dispatchProbeRouteConfigSyncToNodes(affected, controllerBaseURL),
	}, nil
}

func applyProbeSpecialExitLibrarySubscriptionRefresh(sourceHash, sourceID string, proxies []map[string]interface{}, refreshedAt time.Time) (probeSpecialExitLibrary, []string, error) {
	sourceID = strings.TrimSpace(sourceID)
	var library probeSpecialExitLibrary
	affected := []string{}
	err := ProbeRouteConfigStore.update(func(data *probeRouteConfigStoreData) error {
		library = data.SpecialExitLibrary
		if probeSpecialExitLibrarySourceHash(library) != strings.ToLower(strings.TrimSpace(sourceHash)) {
			return fmt.Errorf("Clash subscription library changed while refreshing")
		}
		foundSource := false
		for _, source := range library.Subscriptions {
			if strings.TrimSpace(source.ID) == sourceID {
				foundSource = true
				break
			}
		}
		if !foundSource {
			return fmt.Errorf("subscription %q was removed while refreshing", sourceID)
		}
		newNames := make(map[string]struct{}, len(proxies))
		for _, proxy := range proxies {
			newNames[strings.TrimSpace(fmt.Sprint(proxy["name"]))] = struct{}{}
		}
		merged := make([]map[string]interface{}, 0, len(library.Proxies)+len(proxies))
		sourceIDs := make(map[string]string, len(library.ProxySourceIDs)+len(proxies))
		for _, proxy := range library.Proxies {
			name := strings.TrimSpace(fmt.Sprint(proxy["name"]))
			oldSourceID := strings.TrimSpace(library.ProxySourceIDs[name])
			_, replacedByName := newNames[name]
			if oldSourceID == sourceID || (oldSourceID == "" && replacedByName) {
				continue
			}
			merged = append(merged, proxy)
			if oldSourceID != "" {
				sourceIDs[name] = oldSourceID
			}
		}
		merged = append(merged, proxies...)
		var normalizeErr error
		library.Proxies, normalizeErr = normalizeProbeSpecialExitProxies(merged)
		if normalizeErr != nil {
			return normalizeErr
		}
		for name := range newNames {
			sourceIDs[name] = sourceID
		}
		library.ProxySourceIDs = normalizeProbeSpecialExitProxySourceIDs(sourceIDs, library.Proxies)
		library.LastSubscriptionRefreshAt = refreshedAt.Format(time.RFC3339)
		library.LastSubscriptionRefreshErr = ""
		for index := range library.Subscriptions {
			if strings.TrimSpace(library.Subscriptions[index].ID) == sourceID {
				library.Subscriptions[index].LastSubscriptionRefreshAt = library.LastSubscriptionRefreshAt
				library.Subscriptions[index].LastSubscriptionRefreshErr = ""
			}
		}
		library.UpdatedAt = library.LastSubscriptionRefreshAt
		var rebuildErr error
		data.SpecialExits, affected, rebuildErr = rebuildProbeSpecialExitsFromLibrary(data.SpecialExits, library, refreshedAt)
		if rebuildErr != nil {
			return rebuildErr
		}
		data.SpecialExitLibrary = library
		return nil
	})
	if err != nil {
		return probeSpecialExitLibrary{}, nil, err
	}
	return library, affected, nil
}

func recordProbeSpecialExitRefreshError(sourceHash, sourceID string, refreshErr error) {
	if ProbeRouteConfigStore == nil {
		return
	}
	_ = ProbeRouteConfigStore.update(func(data *probeRouteConfigStoreData) error {
		if probeSpecialExitLibrarySourceHash(data.SpecialExitLibrary) != strings.ToLower(strings.TrimSpace(sourceHash)) {
			return nil
		}
		data.SpecialExitLibrary.LastSubscriptionRefreshErr = strings.TrimSpace(refreshErr.Error())
		for sourceIndex := range data.SpecialExitLibrary.Subscriptions {
			if strings.TrimSpace(sourceID) == "" || data.SpecialExitLibrary.Subscriptions[sourceIndex].ID == strings.TrimSpace(sourceID) {
				data.SpecialExitLibrary.Subscriptions[sourceIndex].LastSubscriptionRefreshErr = strings.TrimSpace(refreshErr.Error())
			}
		}
		return nil
	})
}

func parseProbeSpecialExitSubscription(content []byte) ([]map[string]interface{}, error) {
	result, err := parseProbeSpecialExitSubscriptionWithResult(content)
	return result.Proxies, err
}

func parseProbeSpecialExitSubscriptionWithResult(content []byte) (probeSpecialExitSubscriptionParseResult, error) {
	if proxies, recognized, err := parseProbeSpecialExitYAML(content); recognized {
		return probeSpecialExitSubscriptionParseResult{Proxies: proxies}, err
	}
	result, err := parseProbeSpecialExitURIList(content)
	if err != nil {
		return probeSpecialExitSubscriptionParseResult{}, err
	}
	proxies, err := normalizeProbeSpecialExitProxies(result.Proxies)
	result.Proxies = proxies
	return result, err
}

func parseProbeSpecialExitYAML(content []byte) ([]map[string]interface{}, bool, error) {
	var raw struct {
		Proxies        []map[interface{}]interface{} `yaml:"proxies"`
		Payload        []map[interface{}]interface{} `yaml:"payload"`
		ProxyProviders map[string]interface{}        `yaml:"proxy-providers"`
	}
	if err := yaml.Unmarshal(content, &raw); err != nil {
		return nil, false, nil
	}
	if len(raw.ProxyProviders) > 0 {
		return nil, true, fmt.Errorf("remote proxy-providers are not supported")
	}
	recognized := raw.Proxies != nil || raw.Payload != nil || raw.ProxyProviders != nil
	if !recognized {
		return nil, false, nil
	}
	items := raw.Proxies
	if items == nil {
		items = raw.Payload
	}
	if len(items) == 0 {
		return nil, true, fmt.Errorf("subscription contains no proxy nodes")
	}
	converted := make([]map[string]interface{}, 0, len(items))
	for index, item := range items {
		value, err := probeSpecialExitStringMap(item)
		if err != nil {
			return nil, true, fmt.Errorf("proxies[%d]: %w", index, err)
		}
		converted = append(converted, value)
	}
	proxies, err := normalizeProbeSpecialExitProxies(converted)
	return proxies, true, err
}

func parseProbeSpecialExitURIList(content []byte) (probeSpecialExitSubscriptionParseResult, error) {
	text := strings.TrimSpace(strings.TrimPrefix(string(content), "\ufeff"))
	if !strings.Contains(text, "://") {
		decoded, ok := decodeProbeSpecialExitBase64Subscription(text)
		if !ok {
			return probeSpecialExitSubscriptionParseResult{}, fmt.Errorf("subscription is not valid Clash YAML or a supported proxy URI list")
		}
		text = strings.TrimSpace(strings.TrimPrefix(string(decoded), "\ufeff"))
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	result := probeSpecialExitSubscriptionParseResult{Proxies: make([]map[string]interface{}, 0, len(lines))}
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parsed, err := url.Parse(line)
		if err != nil || strings.TrimSpace(parsed.Scheme) == "" {
			return probeSpecialExitSubscriptionParseResult{}, fmt.Errorf("subscription URI at line %d is invalid", index+1)
		}
		var proxy map[string]interface{}
		switch strings.ToLower(strings.TrimSpace(parsed.Scheme)) {
		case "anytls":
			proxy, err = parseProbeSpecialExitAnyTLSURI(parsed, index+1)
		default:
			err = fmt.Errorf("subscription URI scheme %q is not supported", strings.ToLower(strings.TrimSpace(parsed.Scheme)))
		}
		if err != nil {
			if errors.Is(err, errProbeSpecialExitAnyTLSRealityUnsupported) {
				result.SkippedProxyCount++
				continue
			}
			return probeSpecialExitSubscriptionParseResult{}, err
		}
		result.Proxies = append(result.Proxies, proxy)
	}
	if len(result.Proxies) == 0 {
		if result.SkippedProxyCount > 0 {
			return probeSpecialExitSubscriptionParseResult{}, fmt.Errorf("subscription contains no Mihomo-compatible proxy nodes; skipped %d AnyTLS+Reality nodes", result.SkippedProxyCount)
		}
		return probeSpecialExitSubscriptionParseResult{}, fmt.Errorf("subscription contains no proxy nodes")
	}
	return result, nil
}

func decodeProbeSpecialExitBase64Subscription(text string) ([]byte, bool) {
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
	if compact == "" {
		return nil, false
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err := encoding.DecodeString(compact)
		if err == nil && len(decoded) > 0 && strings.Contains(string(decoded), "://") {
			return decoded, true
		}
	}
	return nil, false
}

func parseProbeSpecialExitAnyTLSURI(parsed *url.URL, line int) (map[string]interface{}, error) {
	if parsed == nil || parsed.User == nil {
		return nil, fmt.Errorf("anytls URI at line %d is missing its password", line)
	}
	password := parsed.User.Username()
	if password == "" {
		return nil, fmt.Errorf("anytls URI at line %d is missing its password", line)
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		return nil, fmt.Errorf("anytls URI at line %d must use password@host userinfo", line)
	}
	server := strings.TrimSpace(parsed.Hostname())
	if server == "" {
		return nil, fmt.Errorf("anytls URI at line %d is missing its server", line)
	}
	port := 443
	if rawPort := strings.TrimSpace(parsed.Port()); rawPort != "" {
		value, err := strconv.Atoi(rawPort)
		if err != nil || value < 1 || value > 65535 {
			return nil, fmt.Errorf("anytls URI at line %d has an invalid port", line)
		}
		port = value
	}
	query := parsed.Query()
	if strings.EqualFold(strings.TrimSpace(query.Get("security")), "reality") || firstProbeSpecialExitQueryValue(query, "pbk", "public-key", "sid", "short-id") != "" {
		return nil, fmt.Errorf("anytls URI at line %d: %w", line, errProbeSpecialExitAnyTLSRealityUnsupported)
	}
	name := strings.TrimSpace(parsed.Fragment)
	if name == "" {
		name = net.JoinHostPort(server, strconv.Itoa(port))
	}
	proxy := map[string]interface{}{
		"name": name, "type": "anytls", "server": server, "port": port,
		"password": password, "udp": true,
	}
	if sni := firstProbeSpecialExitQueryValue(query, "sni", "peer", "serverName"); sni != "" {
		proxy["sni"] = sni
	}
	if fingerprint := firstProbeSpecialExitQueryValue(query, "client-fingerprint", "fp"); fingerprint != "" {
		proxy["client-fingerprint"] = fingerprint
	}
	if raw := firstProbeSpecialExitQueryValue(query, "insecure", "allowInsecure", "skip-cert-verify"); raw != "" {
		value, err := parseProbeSpecialExitURIFlag(raw)
		if err != nil {
			return nil, fmt.Errorf("anytls URI at line %d has an invalid insecure flag", line)
		}
		proxy["skip-cert-verify"] = value
	}
	if raw := strings.TrimSpace(query.Get("udp")); raw != "" {
		value, err := parseProbeSpecialExitURIFlag(raw)
		if err != nil {
			return nil, fmt.Errorf("anytls URI at line %d has an invalid udp flag", line)
		}
		proxy["udp"] = value
	}
	if raw := strings.TrimSpace(query.Get("alpn")); raw != "" {
		values := make([]string, 0)
		for _, item := range strings.Split(raw, ",") {
			if item = strings.TrimSpace(item); item != "" {
				values = append(values, item)
			}
		}
		if len(values) > 0 {
			proxy["alpn"] = values
		}
	}
	for _, key := range []string{"idle-session-check-interval", "idle-session-timeout", "min-idle-session"} {
		raw := strings.TrimSpace(query.Get(key))
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return nil, fmt.Errorf("anytls URI at line %d has an invalid %s", line, key)
		}
		proxy[key] = value
	}
	return proxy, nil
}

func firstProbeSpecialExitQueryValue(query url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(query.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func parseProbeSpecialExitURIFlag(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value")
	}
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

func listMngProbeSpecialExitStatuses() (map[string]interface{}, error) {
	if ProbeRouteConfigStore == nil {
		return nil, fmt.Errorf("probe route config store is not initialized")
	}
	ProbeRouteConfigStore.mu.RLock()
	items := append([]probeSpecialExitConfig(nil), ProbeRouteConfigStore.data.SpecialExits...)
	library := ProbeRouteConfigStore.data.SpecialExitLibrary
	ProbeRouteConfigStore.mu.RUnlock()
	subscriptionNames := make(map[string]string, len(library.Subscriptions))
	for _, source := range library.Subscriptions {
		subscriptionNames[source.ID] = source.Name
	}
	statuses := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		runtime, found := getProbeRuntime(item.NodeID)
		connectivity := make([]map[string]interface{}, 0, len(runtime.SpecialExit.Connectivity))
		for _, probeResult := range runtime.SpecialExit.Connectivity {
			sourceID := strings.TrimSpace(library.ProxySourceIDs[probeResult.Target])
			connectivity = append(connectivity, map[string]interface{}{
				"target": probeResult.Target, "subscription_id": sourceID, "subscription_name": subscriptionNames[sourceID],
				"reachable": probeResult.Reachable, "latency_ms": probeResult.LatencyMS,
				"error": probeResult.Error, "checked_at": probeResult.CheckedAt,
			})
		}
		status := map[string]interface{}{
			"node_id": item.NodeID, "desired_revision": item.Revision, "desired_sha256": item.SHA256,
			"build_kind": runtime.BuildKind, "probe_version": runtime.Version, "online": found && runtime.Online,
			"applied_revision": runtime.SpecialExit.AppliedRevision, "applied_sha256": runtime.SpecialExit.AppliedSHA256,
			"exit_ready": runtime.SpecialExit.ExitReady, "healthy": runtime.SpecialExit.Healthy, "mihomo_version": runtime.SpecialExit.MihomoVersion,
			"active_sessions": runtime.SpecialExit.ActiveSessions, "bytes_up": runtime.SpecialExit.BytesUp, "bytes_down": runtime.SpecialExit.BytesDown,
			"connectivity":     connectivity,
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
