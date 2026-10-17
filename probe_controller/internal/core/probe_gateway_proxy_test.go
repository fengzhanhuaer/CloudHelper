package core

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestGatewayProxyReconnectUsesLatestAuthorization(t *testing.T) {
	const id = "gateway-reconnect-test"
	probeRuntimeStore.mu.Lock()
	old, exists := probeRuntimeStore.data[id]
	probeRuntimeStore.data[id] = probeRuntimeStatus{NodeID: id, Online: true, BuildKind: probeNodeKindNormal,
		GatewayProxy: &probeGatewayProxyConfig{true, []string{"192.168.50.0/24"}, []string{"9"}}}
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
	if !setProbeRuntimeOnline(id, false) {
		t.Fatal("ordinary gateway disconnect did not invalidate routes")
	}
	// A grant/revocation received while a consumer is offline must not wait for it.
	updateProbeRuntimeGatewayProxy(id, &probeGatewayProxyConfig{true, []string{"192.168.50.0/24"}, []string{"14"}})
	if !setProbeRuntimeOnline(id, true) {
		t.Fatal("ordinary gateway reconnect did not restore routes")
	}
	r, _ := getProbeRuntime(id)
	if got := appendProbeGatewayPublishedRules(nil, []probeRuntimeStatus{r}, "14"); len(got) != 1 {
		t.Fatal("returning authorized node did not receive latest grant", got)
	}
	if got := appendProbeGatewayPublishedRules(nil, []probeRuntimeStatus{r}, "9"); len(got) != 0 {
		t.Fatal("returning revoked node retained old grant", got)
	}
}

func TestOrdinaryProbeSessionReconnectRequestsRouteConfig(t *testing.T) {
	const id = "ordinary-gateway-consumer-test"
	// Register twice: a first connection and a later reconnection must both pull
	// missed changes, even though this ordinary node publishes no LAN of its own.
	for i := 0; i < 2; i++ {
		server, client := net.Pipe()
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		session := registerProbeSession(id, server)
		decoder := json.NewDecoder(client)
		found := false
		for j := 0; j < 8; j++ {
			var message struct {
				Type string `json:"type"`
			}
			if err := decoder.Decode(&message); err != nil {
				t.Error("session did not request refresh", err)
				break
			}
			if message.Type == "route_config_sync" {
				found = true
				break
			}
		}
		unregisterProbeSession(id, session)
		_ = client.Close()
		if !found {
			t.Fatal("ordinary reconnect missed config refresh")
		}
	}
	probeRuntimeStore.mu.Lock()
	delete(probeRuntimeStore.data, id)
	probeRuntimeStore.mu.Unlock()
}

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
	if got := appendProbeGatewayPublishedRules(routes, nodes, "9"); len(got) != 2 {
		t.Fatal("control disconnect withdrew an authenticated grant", got)
	}
	nodes[1].GatewayProxy = &probeGatewayProxyConfig{true, c.ProxyCIDRs, []string{"14"}}
	if got := appendProbeGatewayPublishedRules(routes, nodes, "9"); len(got) != 0 {
		t.Fatal("explicit revocation retained derived route", got)
	}
	nodes[1].GatewayProxy = &probeGatewayProxyConfig{}
	if got := appendProbeGatewayPublishedRules(routes, nodes, "14"); len(got) != 0 {
		t.Fatal("explicit disable retained derived route", got)
	}
}

func TestGatewayProxyDerivedRuleHasNoSyntheticEditTime(t *testing.T) {
	nodes := []probeRuntimeStatus{{NodeID: "25", Online: true, GatewayProxy: &probeGatewayProxyConfig{true, []string{"192.168.52.0/24"}, []string{"26"}}}}
	ordinary := probeVirtualRouterRouteRule{ID: "normal", Name: "normal", Action: probeVirtualRouterRouteRuleActionDirect, Entries: []string{"domain_suffix:example.test"}, UpdatedAt: "2026-10-17T00:00:00Z"}
	first := appendProbeGatewayPublishedRules([]probeVirtualRouterRouteRule{ordinary}, nodes, "26")
	nodes[0].Online = false
	second := appendProbeGatewayPublishedRules(first, nodes, "26")
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) || len(second) != 2 {
		t.Fatal("online transition changed published authorization")
	}
	for _, r := range second {
		if r.ID == "normal" && r.UpdatedAt != ordinary.UpdatedAt {
			t.Fatal("ordinary edit time changed")
		}
		if r.ExitNodeID == "25" && r.UpdatedAt != "" {
			t.Fatal("derived rule received an unstable timestamp")
		}
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
