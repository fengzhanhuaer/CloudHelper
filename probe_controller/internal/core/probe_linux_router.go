package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const probeLinuxRouterConfigVersion = 1

var probeLinuxRouterInterfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,32}$`)

type probeLinuxRouterGatewayConfig struct {
	Enabled         bool     `json:"enabled"`
	Interface       string   `json:"interface"`
	GatewayAddress  string   `json:"gateway_address"`
	UpstreamGateway string   `json:"upstream_gateway"`
	LANCIDRs        []string `json:"lan_cidrs"`
	DNSEnabled      bool     `json:"dns_enabled"`
}

type probeLinuxRouterLocalIPConfig struct {
	Enabled        bool     `json:"enabled"`
	PublishedCIDRs []string `json:"published_cidrs"`
	AllowedNodeIDs []string `json:"allowed_node_ids"`
}

type probeLinuxRouterConfig struct {
	NodeID       string                        `json:"node_id"`
	Revision     int64                         `json:"revision"`
	SHA256       string                        `json:"sha256"`
	GatewayProxy probeLinuxRouterGatewayConfig `json:"gateway_proxy"`
	LocalIPProxy probeLinuxRouterLocalIPConfig `json:"local_ip_proxy"`
	UpdatedAt    string                        `json:"updated_at,omitempty"`
}

type probeLinuxRouterSnapshot struct {
	Version      int                           `json:"version"`
	NodeID       string                        `json:"node_id"`
	Revision     int64                         `json:"revision"`
	SHA256       string                        `json:"sha256"`
	GatewayProxy probeLinuxRouterGatewayConfig `json:"gateway_proxy"`
	LocalIPProxy probeLinuxRouterLocalIPConfig `json:"local_ip_proxy"`
}

type probeLinuxRouterRuntimeReport struct {
	AppliedRevision     int64    `json:"applied_revision,omitempty"`
	AppliedSHA256       string   `json:"applied_sha256,omitempty"`
	GatewayProxyEnabled bool     `json:"gateway_proxy_enabled"`
	LocalIPProxyEnabled bool     `json:"local_ip_proxy_enabled"`
	Healthy             bool     `json:"healthy"`
	FailOpen            bool     `json:"fail_open"`
	Interface           string   `json:"interface,omitempty"`
	GatewayAddress      string   `json:"gateway_address,omitempty"`
	PublishedCIDRs      []string `json:"published_cidrs,omitempty"`
	TUNRXPackets        uint64   `json:"tun_rx_packets,omitempty"`
	TUNRXBytes          uint64   `json:"tun_rx_bytes,omitempty"`
	TUNTXPackets        uint64   `json:"tun_tx_packets,omitempty"`
	TUNTXBytes          uint64   `json:"tun_tx_bytes,omitempty"`
	LatencyMS           int64    `json:"latency_ms,omitempty"`
	LastApplyError      string   `json:"last_apply_error,omitempty"`
	UpdatedAt           string   `json:"updated_at,omitempty"`
}

func defaultProbeLinuxRouterConfig(nodeID string) probeLinuxRouterConfig {
	return probeLinuxRouterConfig{
		NodeID:   normalizeProbeNodeID(nodeID),
		Revision: 1,
		GatewayProxy: probeLinuxRouterGatewayConfig{
			Interface:       "auto",
			GatewayAddress:  "192.168.1.150/24",
			UpstreamGateway: "192.168.1.1",
			LANCIDRs:        []string{"192.168.1.0/24"},
			DNSEnabled:      true,
		},
		LocalIPProxy: probeLinuxRouterLocalIPConfig{
			PublishedCIDRs: []string{"192.168.1.0/24"},
			AllowedNodeIDs: []string{},
		},
	}
}

func normalizeProbeLinuxRouterConfigs(items []probeLinuxRouterConfig) []probeLinuxRouterConfig {
	out := make([]probeLinuxRouterConfig, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		item, err := normalizeProbeLinuxRouterConfig(raw, nil)
		if err != nil || item.NodeID == "" {
			continue
		}
		if _, ok := seen[item.NodeID]; ok {
			continue
		}
		seen[item.NodeID] = struct{}{}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func normalizeProbeLinuxRouterConfig(raw probeLinuxRouterConfig, previous *probeLinuxRouterConfig) (probeLinuxRouterConfig, error) {
	nodeID := normalizeProbeNodeID(raw.NodeID)
	if nodeID == "" {
		return probeLinuxRouterConfig{}, fmt.Errorf("node_id is required")
	}
	node, ok := getProbeNodeByID(nodeID)
	if !ok || normalizeProbeNodeKind(node.NodeKind) != probeNodeKindLinuxRouter {
		return probeLinuxRouterConfig{}, fmt.Errorf("node %q must use node_kind linux_router", nodeID)
	}
	defaults := defaultProbeLinuxRouterConfig(nodeID)
	result := raw
	result.NodeID = nodeID
	result.GatewayProxy.Interface = strings.TrimSpace(result.GatewayProxy.Interface)
	if result.GatewayProxy.Interface == "" {
		result.GatewayProxy.Interface = defaults.GatewayProxy.Interface
	}
	if result.GatewayProxy.Interface != "auto" && !probeLinuxRouterInterfacePattern.MatchString(result.GatewayProxy.Interface) {
		return probeLinuxRouterConfig{}, fmt.Errorf("gateway_proxy.interface is invalid")
	}
	if strings.TrimSpace(result.GatewayProxy.GatewayAddress) == "" {
		result.GatewayProxy.GatewayAddress = defaults.GatewayProxy.GatewayAddress
	}
	gatewayPrefix, err := netip.ParsePrefix(strings.TrimSpace(result.GatewayProxy.GatewayAddress))
	if err != nil || !gatewayPrefix.Addr().Is4() || !gatewayPrefix.Addr().IsPrivate() {
		return probeLinuxRouterConfig{}, fmt.Errorf("gateway_proxy.gateway_address must be a private IPv4 CIDR")
	}
	result.GatewayProxy.GatewayAddress = gatewayPrefix.String()
	if strings.TrimSpace(result.GatewayProxy.UpstreamGateway) == "" {
		result.GatewayProxy.UpstreamGateway = defaults.GatewayProxy.UpstreamGateway
	}
	upstream, err := netip.ParseAddr(strings.TrimSpace(result.GatewayProxy.UpstreamGateway))
	if err != nil || !upstream.Is4() || !gatewayPrefix.Masked().Contains(upstream) || upstream == gatewayPrefix.Addr() {
		return probeLinuxRouterConfig{}, fmt.Errorf("gateway_proxy.upstream_gateway must be another IPv4 address in the gateway subnet")
	}
	result.GatewayProxy.UpstreamGateway = upstream.String()
	if len(result.GatewayProxy.LANCIDRs) == 0 {
		result.GatewayProxy.LANCIDRs = defaults.GatewayProxy.LANCIDRs
	}
	result.GatewayProxy.LANCIDRs, err = normalizeProbeLinuxRouterCIDRs(result.GatewayProxy.LANCIDRs)
	if err != nil {
		return probeLinuxRouterConfig{}, fmt.Errorf("gateway_proxy.lan_cidrs: %w", err)
	}
	if len(result.LocalIPProxy.PublishedCIDRs) == 0 {
		result.LocalIPProxy.PublishedCIDRs = defaults.LocalIPProxy.PublishedCIDRs
	}
	result.LocalIPProxy.PublishedCIDRs, err = normalizeProbeLinuxRouterCIDRs(result.LocalIPProxy.PublishedCIDRs)
	if err != nil {
		return probeLinuxRouterConfig{}, fmt.Errorf("local_ip_proxy.published_cidrs: %w", err)
	}
	result.LocalIPProxy.AllowedNodeIDs = normalizeProbeLinuxRouterAllowedNodes(result.LocalIPProxy.AllowedNodeIDs, nodeID)
	if result.LocalIPProxy.Enabled && len(result.LocalIPProxy.AllowedNodeIDs) == 0 {
		return probeLinuxRouterConfig{}, fmt.Errorf("local_ip_proxy.allowed_node_ids is required when local IP proxy is enabled")
	}
	if previous != nil {
		result.Revision = previous.Revision + 1
	} else if result.Revision < 1 {
		result.Revision = 1
	}
	result.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	result.SHA256 = probeLinuxRouterConfigSHA256(result)
	if previous != nil && strings.EqualFold(previous.SHA256, result.SHA256) {
		result.Revision = previous.Revision
		result.UpdatedAt = previous.UpdatedAt
	}
	return result, nil
}

func normalizeProbeLinuxRouterCIDRs(values []string) ([]string, error) {
	fakePool := netip.MustParsePrefix("198.18.0.0/15")
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Bits() < 8 {
			return nil, fmt.Errorf("%q must be a private IPv4 CIDR with prefix length 8 or greater", raw)
		}
		prefix = prefix.Masked()
		if prefix.Contains(fakePool.Addr()) || fakePool.Contains(prefix.Addr()) {
			return nil, fmt.Errorf("%q overlaps the virtual router fake IP pool", prefix.String())
		}
		value := prefix.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) > 32 {
			return nil, fmt.Errorf("at most 32 CIDRs are allowed")
		}
	}
	sort.Strings(out)
	return out, nil
}

func normalizeProbeLinuxRouterAllowedNodes(values []string, routerNodeID string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		nodeID := normalizeProbeNodeID(raw)
		if nodeID == "" || nodeID == routerNodeID {
			continue
		}
		if _, ok := getProbeNodeByID(nodeID); !ok {
			continue
		}
		if _, ok := seen[nodeID]; ok {
			continue
		}
		seen[nodeID] = struct{}{}
		out = append(out, nodeID)
	}
	sort.Strings(out)
	return out
}

func probeLinuxRouterConfigSHA256(item probeLinuxRouterConfig) string {
	payload := struct {
		NodeID       string                        `json:"node_id"`
		GatewayProxy probeLinuxRouterGatewayConfig `json:"gateway_proxy"`
		LocalIPProxy probeLinuxRouterLocalIPConfig `json:"local_ip_proxy"`
	}{item.NodeID, item.GatewayProxy, item.LocalIPProxy}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func findProbeLinuxRouterConfig(items []probeLinuxRouterConfig, nodeID string) (probeLinuxRouterConfig, bool) {
	nodeID = normalizeProbeNodeID(nodeID)
	for _, item := range items {
		if normalizeProbeNodeID(item.NodeID) == nodeID {
			return item, true
		}
	}
	return probeLinuxRouterConfig{}, false
}

func probeLinuxRouterSnapshotForConfig(item probeLinuxRouterConfig) probeLinuxRouterSnapshot {
	return probeLinuxRouterSnapshot{
		Version: probeLinuxRouterConfigVersion, NodeID: item.NodeID, Revision: item.Revision, SHA256: item.SHA256,
		GatewayProxy: item.GatewayProxy, LocalIPProxy: item.LocalIPProxy,
	}
}

func mngProbeLinuxRouterHandler(w http.ResponseWriter, r *http.Request) {
	if ProbeRouteConfigStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "route config store not initialized"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": listMngProbeLinuxRouters(r.URL.Query().Get("node_id"))})
	case http.MethodPost:
		var req probeLinuxRouterConfig
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
		var saved probeLinuxRouterConfig
		err := ProbeRouteConfigStore.update(func(data *probeRouteConfigStoreData) error {
			previous, found := findProbeLinuxRouterConfig(data.LinuxRouters, req.NodeID)
			var previousPtr *probeLinuxRouterConfig
			if found {
				previousPtr = &previous
			}
			normalized, err := normalizeProbeLinuxRouterConfig(req, previousPtr)
			if err != nil {
				return err
			}
			next := make([]probeLinuxRouterConfig, 0, len(data.LinuxRouters)+1)
			for _, item := range data.LinuxRouters {
				if normalizeProbeNodeID(item.NodeID) != normalized.NodeID {
					next = append(next, item)
				}
			}
			next = append(next, normalized)
			data.LinuxRouters = normalizeProbeLinuxRouterConfigs(next)
			saved = normalized
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		syncResult := dispatchProbeRouteConfigSyncToKnownNodes(controllerBaseURLFromRequest(r))
		writeJSON(w, http.StatusOK, map[string]interface{}{"item": buildMngProbeLinuxRouterItem(saved), "sync": syncResult})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func listMngProbeLinuxRouters(onlyNodeID string) []map[string]interface{} {
	onlyNodeID = normalizeProbeNodeID(onlyNodeID)
	ProbeStore.mu.RLock()
	nodes := append([]probeNodeRecord(nil), loadProbeNodesLocked()...)
	ProbeStore.mu.RUnlock()
	ProbeRouteConfigStore.mu.RLock()
	configs := append([]probeLinuxRouterConfig(nil), ProbeRouteConfigStore.data.LinuxRouters...)
	ProbeRouteConfigStore.mu.RUnlock()
	out := make([]map[string]interface{}, 0)
	for _, node := range nodes {
		nodeID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
		if normalizeProbeNodeKind(node.NodeKind) != probeNodeKindLinuxRouter || (onlyNodeID != "" && nodeID != onlyNodeID) {
			continue
		}
		item, found := findProbeLinuxRouterConfig(configs, nodeID)
		if !found {
			item = defaultProbeLinuxRouterConfig(nodeID)
			item.SHA256 = probeLinuxRouterConfigSHA256(item)
		}
		view := buildMngProbeLinuxRouterItem(item)
		view["node_name"] = node.NodeName
		out = append(out, view)
	}
	return out
}

func buildMngProbeLinuxRouterItem(item probeLinuxRouterConfig) map[string]interface{} {
	runtime, found := getProbeRuntime(item.NodeID)
	status := probeLinuxRouterRuntimeReport{}
	online := false
	version := ""
	buildKind := ""
	if found {
		status = runtime.LinuxRouter
		online = runtime.Online
		version = runtime.Version
		buildKind = runtime.BuildKind
	}
	return map[string]interface{}{
		"node_id": item.NodeID, "revision": item.Revision, "sha256": item.SHA256,
		"gateway_proxy": item.GatewayProxy, "local_ip_proxy": item.LocalIPProxy, "updated_at": item.UpdatedAt,
		"online": online, "version": version, "build_kind": buildKind, "runtime": status,
	}
}

func buildMngProbeLinuxRouterInstallInfo(nodeID string, controllerBaseURL string) (map[string]interface{}, error) {
	nodeID = normalizeProbeNodeID(nodeID)
	node, ok := getProbeNodeByID(nodeID)
	if !ok {
		return nil, fmt.Errorf("node %q not found", nodeID)
	}
	if normalizeProbeNodeKind(node.NodeKind) != probeNodeKindLinuxRouter {
		return nil, fmt.Errorf("node %q must use node_kind linux_router", nodeID)
	}
	base := strings.TrimRight(strings.TrimSpace(controllerBaseURL), "/")
	if !strings.HasPrefix(strings.ToLower(base), "https://") {
		return nil, fmt.Errorf("controller URL must use HTTPS")
	}
	scriptURL := base + "/api/probe/proxy/probe-router/install-script?node_id=" + url.QueryEscape(nodeID) + "&secret=" + url.QueryEscape(node.NodeSecret)
	env := map[string]string{
		"PROBE_NODE_ID":        nodeID,
		"PROBE_NODE_SECRET":    node.NodeSecret,
		"PROBE_CONTROLLER_URL": base,
	}
	return map[string]interface{}{
		"node_id": nodeID, "mode": "native", "environment": env,
		"platform": "linux", "architectures": []string{"amd64", "arm64"},
		"script_url": scriptURL,
		"command":    "curl -fsSL " + shellQuoteProbeSpecialExit(scriptURL) + " | sudo env PROBE_NODE_ID=" + shellQuoteProbeSpecialExit(nodeID) + " PROBE_NODE_SECRET=" + shellQuoteProbeSpecialExit(node.NodeSecret) + " PROBE_CONTROLLER_URL=" + shellQuoteProbeSpecialExit(base) + " sh",
	}, nil
}
