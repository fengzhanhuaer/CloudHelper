package core

import (
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

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
	AllowedNodeIDs      []string `json:"allowed_node_ids,omitempty"`
	TUNRXPackets        uint64   `json:"tun_rx_packets,omitempty"`
	TUNRXBytes          uint64   `json:"tun_rx_bytes,omitempty"`
	TUNTXPackets        uint64   `json:"tun_tx_packets,omitempty"`
	TUNTXBytes          uint64   `json:"tun_tx_bytes,omitempty"`
	LatencyMS           int64    `json:"latency_ms,omitempty"`
	LastApplyError      string   `json:"last_apply_error,omitempty"`
	UpdatedAt           string   `json:"updated_at,omitempty"`
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

func mngProbeLinuxRouterHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": listMngProbeLinuxRouters(r.URL.Query().Get("node_id"))})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "linux router configuration is managed by the router local web console"})
	}
}

func listMngProbeLinuxRouters(onlyNodeID string) []map[string]interface{} {
	onlyNodeID = normalizeProbeNodeID(onlyNodeID)
	ProbeStore.mu.RLock()
	nodes := append([]probeNodeRecord(nil), loadProbeNodesLocked()...)
	ProbeStore.mu.RUnlock()
	out := make([]map[string]interface{}, 0)
	for _, node := range nodes {
		nodeID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
		if normalizeProbeNodeKind(node.NodeKind) != probeNodeKindLinuxRouter || (onlyNodeID != "" && nodeID != onlyNodeID) {
			continue
		}
		view := buildMngProbeLinuxRouterItem(nodeID)
		view["node_name"] = node.NodeName
		out = append(out, view)
	}
	return out
}

func buildMngProbeLinuxRouterItem(nodeID string) map[string]interface{} {
	runtime, found := getProbeRuntime(nodeID)
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
		"node_id": nodeID,
		"online":  online, "version": version, "build_kind": buildKind, "runtime": status,
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
		"command":    "if command -v curl >/dev/null 2>&1; then curl -fsSL " + shellQuoteProbeRouter(scriptURL) + "; else wget -qO- " + shellQuoteProbeRouter(scriptURL) + "; fi | env PROBE_NODE_ID=" + shellQuoteProbeRouter(nodeID) + " PROBE_NODE_SECRET=" + shellQuoteProbeRouter(node.NodeSecret) + " PROBE_CONTROLLER_URL=" + shellQuoteProbeRouter(base) + " sh",
	}, nil
}

func shellQuoteProbeRouter(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
