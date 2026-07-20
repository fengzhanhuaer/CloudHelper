package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	probeRouteConfigAPIPath          = "/api/probe/route/config"
	probeRouteFakeIPResolveAPIPath   = "/api/probe/route/fake_ip/resolve"
	probeRouteConfigSyncPollInterval = 60 * time.Minute
	probeRouteConfigSyncFetchTimeout = 15 * time.Second
	probeRouteConfigCacheFileName    = "probe_route_config.json"
)

type probeRouteConfigResponse struct {
	NodeID        string                   `json:"node_id"`
	VirtualRouter probeVirtualRouterConfig `json:"virtual_router,omitempty"`
}

type probeVirtualRouterConfig struct {
	Enabled       bool                             `json:"enabled"`
	FakeIPCIDR    string                           `json:"fake_ip_cidr,omitempty"`
	ProbeIPs      []probeVirtualRouterProbeIP      `json:"probe_ips,omitempty"`
	TopologyRules []probeVirtualRouterTopologyRule `json:"topology_rules,omitempty"`
	RouteRules    []probeVirtualRouterRouteRule    `json:"route_rules,omitempty"`
	FakeIPLibrary probeVirtualRouterFakeIPLibrary  `json:"fake_ip_library,omitempty"`
	UpdatedAt     string                           `json:"updated_at,omitempty"`
}

type probeVirtualRouterFakeIPLibrary struct {
	Version   int64                           `json:"version"`
	UpdatedAt string                          `json:"updated_at,omitempty"`
	Items     []probeVirtualRouterFakeIPEntry `json:"items,omitempty"`
}

type probeVirtualRouterFakeIPEntry struct {
	Domain     string `json:"domain"`
	FakeIP     string `json:"fake_ip"`
	RuleID     string `json:"rule_id,omitempty"`
	Action     string `json:"action,omitempty"`
	ExitNodeID string `json:"exit_node_id,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type probeVirtualRouterProbeIP struct {
	NodeID      string `json:"node_id"`
	DisplayName string `json:"display_name,omitempty"`
	IP          string `json:"ip"`
	ServicePort int    `json:"service_port,omitempty"`
	Note        string `json:"note,omitempty"`
}

type probeVirtualRouterTopologyRule struct {
	ID                string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	FromNodeID        string `json:"from_node_id"`
	ToNodeID          string `json:"to_node_id"`
	Direction         string `json:"direction"`
	FromServiceDomain string `json:"from_service_domain,omitempty"`
	FromServicePort   int    `json:"from_service_port,omitempty"`
	FromTLSSPKISHA256 string `json:"from_tls_spki_sha256,omitempty"`
	ToServiceDomain   string `json:"to_service_domain,omitempty"`
	ToServicePort     int    `json:"to_service_port,omitempty"`
	ToTLSSPKISHA256   string `json:"to_tls_spki_sha256,omitempty"`
	RouteLayer        string `json:"route_layer,omitempty"`
	UserID            string `json:"user_id,omitempty"`
	UserPublicKey     string `json:"user_public_key,omitempty"`
	Secret            string `json:"secret,omitempty"`
	AuthTicket        string `json:"auth_ticket,omitempty"`
	Enabled           bool   `json:"enabled"`
	Note              string `json:"note,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

type probeVirtualRouterRouteRule struct {
	ID         string   `json:"id,omitempty"`
	Name       string   `json:"name"`
	Action     string   `json:"action,omitempty"`
	ExitNodeID string   `json:"exit_node_id,omitempty"`
	Entries    []string `json:"entries,omitempty"`
	Note       string   `json:"note,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
}

type probeRouteConfigCacheFile struct {
	UpdatedAt string                   `json:"updated_at"`
	Item      probeVirtualRouterConfig `json:"item"`
}

type probeRouteFakeIPResolveResponse struct {
	NodeID        string                          `json:"node_id"`
	Item          probeVirtualRouterFakeIPEntry   `json:"item"`
	FakeIPLibrary probeVirtualRouterFakeIPLibrary `json:"fake_ip_library"`
}

var (
	probeRequestRouteConfig     = requestProbeRouteConfig
	probeRequestRouteFakeIP     = requestProbeRouteFakeIP
	probeRequestRouteFakeIPByIP = requestProbeRouteFakeIPByIP
)

func startProbeRouteConfigSyncLoop(identity nodeIdentity, controllerBaseURL string) {
	go func() {
		base := strings.TrimSpace(controllerBaseURL)
		if base == "" {
			log.Printf("probe route config sync disabled: controller base url not configured")
			return
		}

		if err := syncProbeRouteConfig(identity, base); err != nil {
			log.Printf("warning: initial probe route config sync failed: %v", err)
		}

		ticker := time.NewTicker(probeRouteConfigSyncPollInterval)
		defer ticker.Stop()
		for range ticker.C {
			if err := syncProbeRouteConfig(identity, base); err != nil {
				log.Printf("warning: probe route config sync failed: %v", err)
			}
		}
	}()
}

func syncProbeRouteConfig(identity nodeIdentity, controllerBaseURL string) error {
	rememberProbeVirtualRouterController(identity, controllerBaseURL)
	ctx, cancel := context.WithTimeout(context.Background(), probeRouteConfigSyncFetchTimeout)
	config, err := fetchProbeRouteConfig(ctx, controllerBaseURL, identity)
	cancel()
	if err != nil {
		if cached, cacheErr := loadProbeRouteConfigCache(); cacheErr == nil {
			applyProbeVirtualRouterConfigForNode(cached, identity.NodeID)
			applyProbeVirtualRouterRuntimesForNode(identity, controllerBaseURL, cached)
		} else {
			log.Printf("warning: load probe route config cache failed: %v", cacheErr)
		}
		return err
	}
	if err := persistProbeRouteConfigCache(config); err != nil {
		log.Printf("warning: persist probe route config cache failed: %v", err)
	}
	applyProbeVirtualRouterConfigForNode(config, identity.NodeID)
	applyProbeVirtualRouterRuntimesForNode(identity, controllerBaseURL, config)
	return nil
}

func rememberProbeVirtualRouterController(identity nodeIdentity, controllerBaseURL string) {
	probeVirtualRouterControllerState.mu.Lock()
	probeVirtualRouterControllerState.identity = identity
	probeVirtualRouterControllerState.controllerBaseURL = strings.TrimSpace(controllerBaseURL)
	probeVirtualRouterControllerState.mu.Unlock()
}

func currentProbeVirtualRouterController() (nodeIdentity, string, bool) {
	probeVirtualRouterControllerState.mu.RLock()
	defer probeVirtualRouterControllerState.mu.RUnlock()
	identity := probeVirtualRouterControllerState.identity
	baseURL := strings.TrimSpace(probeVirtualRouterControllerState.controllerBaseURL)
	return identity, baseURL, strings.TrimSpace(identity.NodeID) != "" && strings.TrimSpace(identity.Secret) != "" && baseURL != ""
}

func fetchProbeRouteConfig(ctx context.Context, controllerBaseURL string, identity nodeIdentity) (probeVirtualRouterConfig, error) {
	base := strings.TrimSpace(controllerBaseURL)
	if base == "" {
		return probeVirtualRouterConfig{}, errors.New("controller base url is empty")
	}
	config, err := probeRequestRouteConfig(ctx, base, identity)
	if err != nil {
		return probeVirtualRouterConfig{}, err
	}
	config = sanitizeProbeVirtualRouterConfigForCache(config)
	rememberProbeVirtualRouterAuthTickets(config)
	return config, nil
}

func requestProbeRouteConfig(ctx context.Context, controllerBaseURL string, identity nodeIdentity) (probeVirtualRouterConfig, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(controllerBaseURL), "/")
	if baseURL == "" {
		return probeVirtualRouterConfig{}, errors.New("controller base url is required")
	}
	nodeID := strings.TrimSpace(identity.NodeID)
	secret := strings.TrimSpace(identity.Secret)
	if nodeID == "" || secret == "" {
		return probeVirtualRouterConfig{}, errors.New("node identity is missing node id or secret")
	}

	configURL := baseURL + probeRouteConfigAPIPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, configURL, nil)
	if err != nil {
		return probeVirtualRouterConfig{}, err
	}
	req.Header.Set("Accept", "application/json")
	if err := applyProbeAuthHeaders(req, identity); err != nil {
		return probeVirtualRouterConfig{}, err
	}

	client, closeClient, err := newProbeResolvedHTTPClientForURL(configURL, probeRouteConfigSyncFetchTimeout)
	if err != nil {
		return probeVirtualRouterConfig{}, err
	}
	defer closeClient()
	resp, err := client.Do(req)
	if err != nil {
		return probeVirtualRouterConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return probeVirtualRouterConfig{}, fmt.Errorf("request route config failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload probeRouteConfigResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return probeVirtualRouterConfig{}, err
	}
	return sanitizeProbeVirtualRouterConfigForCache(payload.VirtualRouter), nil
}

func requestProbeRouteFakeIP(ctx context.Context, controllerBaseURL string, identity nodeIdentity, domain string, rule probeVirtualRouterRouteRule) (probeVirtualRouterFakeIPEntry, probeVirtualRouterFakeIPLibrary, error) {
	body, err := json.Marshal(map[string]string{
		"domain":       strings.TrimSpace(domain),
		"rule_id":      strings.TrimSpace(rule.ID),
		"action":       strings.TrimSpace(rule.Action),
		"exit_node_id": strings.TrimSpace(rule.ExitNodeID),
	})
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, err
	}
	item, err := requestProbeRouteFakeIPItem(ctx, controllerBaseURL, identity, body)
	return item, probeVirtualRouterFakeIPLibrary{}, err
}

func requestProbeRouteFakeIPByIP(ctx context.Context, controllerBaseURL string, identity nodeIdentity, fakeIP string) (probeVirtualRouterFakeIPEntry, error) {
	body, err := json.Marshal(map[string]string{
		"fake_ip": strings.TrimSpace(fakeIP),
	})
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, err
	}
	return requestProbeRouteFakeIPItem(ctx, controllerBaseURL, identity, body)
}

func requestProbeRouteFakeIPItem(ctx context.Context, controllerBaseURL string, identity nodeIdentity, body []byte) (probeVirtualRouterFakeIPEntry, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(controllerBaseURL), "/")
	if baseURL == "" {
		return probeVirtualRouterFakeIPEntry{}, errors.New("controller base url is required")
	}
	nodeID := strings.TrimSpace(identity.NodeID)
	secret := strings.TrimSpace(identity.Secret)
	if nodeID == "" || secret == "" {
		return probeVirtualRouterFakeIPEntry{}, errors.New("node identity is missing node id or secret")
	}
	endpoint := baseURL + probeRouteFakeIPResolveAPIPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if err := applyProbeAuthHeaders(req, identity); err != nil {
		return probeVirtualRouterFakeIPEntry{}, err
	}
	client, closeClient, err := newProbeResolvedHTTPClientForURL(endpoint, probeRouteConfigSyncFetchTimeout)
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, err
	}
	defer closeClient()
	resp, err := client.Do(req)
	if err != nil {
		return probeVirtualRouterFakeIPEntry{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return probeVirtualRouterFakeIPEntry{}, fmt.Errorf("request route fake ip failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload probeRouteFakeIPResolveResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return probeVirtualRouterFakeIPEntry{}, err
	}
	return payload.Item, nil
}

func rememberProbeVirtualRouterAuthTickets(config probeVirtualRouterConfig) {
	config = sanitizeProbeVirtualRouterConfigForCache(config)
	for _, rule := range config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		routeID := strings.TrimSpace(probeVirtualRouterRuntimeRouteID(rule))
		if routeID == "" {
			continue
		}
		rememberProbeRouteAuthTicket(routeID, rule.AuthTicket)
	}
}

func normalizeProbeRouteNodeID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "node-") || strings.HasPrefix(lower, "node_") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(lower, "node-"), "node_")
		suffix = strings.TrimSpace(suffix)
		if suffix != "" {
			if n, err := strconv.Atoi(suffix); err == nil && n > 0 {
				return strconv.Itoa(n)
			}
			return suffix
		}
	}
	if n, err := strconv.Atoi(value); err == nil && n > 0 {
		return strconv.Itoa(n)
	}
	return value
}
