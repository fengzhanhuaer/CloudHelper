package core

import (
	"encoding/json"
	"testing"
)

func TestGatewayProxyRoutesScopedToAuthorizedSources(t *testing.T) {
	c := &probeGatewayProxyConfig{true, []string{"192.168.50.0/24", "10.20.0.0/16"}, []string{"9", "14"}}
	nodes := []probeRuntimeStatus{{NodeID: "23", Online: true, BuildKind: "normal", GatewayProxy: c}, {NodeID: "24", Online: true, BuildKind: "asus_56uv2", GatewayProxy: c}}
	routes := appendProbeGatewayPublishedRules(nil, nodes, "9")
	if len(routes) != 4 {
		t.Fatalf("ordinary/ASUS multiple subnet routes: %+v", routes)
	}
	if got := appendProbeGatewayPublishedRules(routes, nodes, "8"); len(got) != 0 {
		t.Fatal("unauthorized source received routes", got)
	}
	if got := appendProbeGatewayPublishedRules(nil, nodes, "23"); len(got) != 0 {
		t.Fatal("owner not in allow list must receive no other owner routes", got)
	}
	nodes[0].GatewayProxy = &probeGatewayProxyConfig{}
	nodes[1].Online = false
	if got := appendProbeGatewayPublishedRules(routes, nodes, "9"); len(got) != 0 {
		t.Fatal("revocation/offline retained derived route", got)
	}
}

func TestGatewayProxyRuntimeReportAndWANLAN(t *testing.T) {
	const id = "gateway-report-test"
	probeRuntimeStore.mu.Lock()
	old, exists := probeRuntimeStore.data[id]
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		probeRuntimeStore.mu.Lock()
		defer probeRuntimeStore.mu.Unlock()
		if exists {
			probeRuntimeStore.data[id] = old
		} else {
			delete(probeRuntimeStore.data, id)
		}
	})
	raw := `{"gateway_proxy":{"enabled":true,"proxy_cidrs":["192.168.52.1/24"],"allowed_node_ids":["9"]},"lan_ip":"192.168.52.1","wan_ip":"192.168.205.2","system":{"network_traffic_available":true,"network_rx_bytes":123456,"network_tx_bytes":789012}}`
	var msg probeReportMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	if !msg.System.NetworkAvailable || msg.System.NetworkRXBytes != 123456 || msg.System.NetworkTXBytes != 789012 {
		t.Fatalf("WAN traffic wire contract: %+v", msg.System)
	}
	if !updateProbeRuntimeGatewayProxy(id, msg.GatewayProxy) {
		t.Fatal("first report not propagated")
	}
	if updateProbeRuntimeGatewayProxy(id, msg.GatewayProxy) {
		t.Fatal("unchanged report repeatedly syncs")
	}
	updateProbeRuntimePhysicalInterfaces(id, msg.LANIP, msg.WANIP)
	probeRuntimeStore.mu.Lock()
	r := probeRuntimeStore.data[id]
	probeRuntimeStore.mu.Unlock()
	if r.LANIP != msg.LANIP || r.WANIP != msg.WANIP || r.GatewayProxy.ProxyCIDRs[0] != "192.168.52.0/24" {
		t.Fatal(r)
	}
	updateProbeRuntimePhysicalInterfaces(id, msg.LANIP, "")
	probeRuntimeStore.mu.Lock()
	r = probeRuntimeStore.data[id]
	probeRuntimeStore.mu.Unlock()
	if r.WANIP != "" {
		t.Fatal("stale WAN retained")
	}
	bad := *msg.GatewayProxy
	bad.ProxyCIDRs = []string{"172.16.0.0/8"}
	if !updateProbeRuntimeGatewayProxy(id, &bad) {
		t.Fatal("invalid report not withdrawn")
	}
	probeRuntimeStore.mu.Lock()
	r = probeRuntimeStore.data[id]
	probeRuntimeStore.mu.Unlock()
	if r.GatewayProxy.Enabled {
		t.Fatal("invalid private supernet accepted")
	}
}
