package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
)

type probeGatewayProxyConfig struct {
	Enabled        bool     `json:"enabled"`
	ProxyCIDRs     []string `json:"proxy_cidrs"`
	AllowedNodeIDs []string `json:"allowed_node_ids"`
}

func normalizeProbeGatewayConfig(c probeGatewayProxyConfig) (probeGatewayProxyConfig, error) {
	if len(c.ProxyCIDRs) > 32 || len(c.AllowedNodeIDs) > 256 {
		return c, errors.New("最多32个网段、256个允许节点")
	}
	prefixes := []string{}
	for _, raw := range c.ProxyCIDRs {
		p, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || !p.Addr().Is4() {
			return c, fmt.Errorf("无效IPv4网段：%s", raw)
		}
		p = p.Masked()
		private := false
		for _, root := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
			r := netip.MustParsePrefix(root)
			private = private || (p.Bits() >= r.Bits() && r.Contains(p.Addr()))
		}
		if !private {
			return c, fmt.Errorf("必须是完整包含在私有地址范围的网段：%s", raw)
		}
		prefixes = append(prefixes, p.String())
	}
	ids := []string{}
	for _, raw := range c.AllowedNodeIDs {
		id := normalizeProbeNodeID(raw)
		if id == "" || len(id) > 128 || strings.ContainsAny(id, " \t\r\n") {
			return c, errors.New("无效节点ID")
		}
		ids = append(ids, id)
	}
	slices.Sort(prefixes)
	slices.Sort(ids)
	c.ProxyCIDRs = slices.Compact(prefixes)
	c.AllowedNodeIDs = slices.Compact(ids)
	if c.Enabled && (len(c.ProxyCIDRs) == 0 || len(c.AllowedNodeIDs) == 0) {
		return c, errors.New("启用时必须填写代理网段和允许节点")
	}
	return c, nil
}

func updateProbeRuntimeGatewayProxy(nodeID string, report *probeGatewayProxyConfig) bool {
	if report == nil {
		return false
	} // Older nodes do not report this capability.
	c, err := normalizeProbeGatewayConfig(*report)
	if err != nil {
		c = probeGatewayProxyConfig{}
	}
	probeRuntimeStore.mu.Lock()
	defer probeRuntimeStore.mu.Unlock()
	current := probeRuntimeStore.data[nodeID]
	if reflect.DeepEqual(current.GatewayProxy, &c) {
		return false
	}
	current.GatewayProxy = &c
	probeRuntimeStore.data[nodeID] = current
	return true
}
func appendProbeGatewayPublishedRules(rules []probeVirtualRouterRouteRule, runtimes []probeRuntimeStatus, nodeID string) []probeVirtualRouterRouteRule {
	out := make([]probeVirtualRouterRouteRule, 0, len(rules))
	for _, r := range rules {
		if !strings.HasPrefix(strings.ToLower(r.ID), "gateway-proxy-") {
			out = append(out, r)
		}
	}
	for _, r := range runtimes {
		// A control-stream outage does not revoke the last authenticated LAN
		// grant. The packet carrier may remain usable, and consumers must keep
		// routing the subnet into the overlay rather than their WAN default.
		if r.GatewayProxy == nil || r.NodeID == nodeID {
			continue
		}
		c, err := normalizeProbeGatewayConfig(*r.GatewayProxy)
		if err != nil || !c.Enabled || !slices.Contains(c.AllowedNodeIDs, nodeID) {
			continue
		}
		for _, cidr := range c.ProxyCIDRs {
			sum := sha256.Sum256([]byte(r.NodeID + "|" + cidr))
			out = append(out, probeVirtualRouterRouteRule{
				ID: "gateway-proxy-" + r.NodeID + "-" + hex.EncodeToString(sum[:6]), Name: "网关代理 " + r.NodeID + " / " + cidr, Action: probeVirtualRouterRouteRuleActionExit, ExitNodeID: r.NodeID, Entries: []string{"cidr:" + cidr}, Note: "节点授权发布的LAN网段",
			})
		}
	}
	out = normalizeProbeVirtualRouterRouteRules(out)
	for i := range out {
		if strings.HasPrefix(out[i].ID, "gateway-proxy-") {
			// Derived rules have no independent edit time. Normalization fills
			// missing timestamps with now; never send that artificial churn.
			out[i].UpdatedAt = ""
		}
	}
	return out
}

func updateProbeRuntimePhysicalInterfaces(nodeID, lan, wan string) {
	clean := func(raw string) string {
		a, err := netip.ParseAddr(raw)
		if err != nil || !a.Is4() || a.IsUnspecified() || a.IsMulticast() {
			return ""
		}
		return a.String()
	}
	probeRuntimeStore.mu.Lock()
	defer probeRuntimeStore.mu.Unlock()
	r := probeRuntimeStore.data[nodeID]
	r.LANIP = clean(lan)
	r.WANIP = clean(wan)
	probeRuntimeStore.data[nodeID] = r
}
