package mobilecore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMobileVRouteEmptyEntriesKeepOtherRules(t *testing.T) {
	for _, entries := range []string{"", `,"entries":null`, `,"entries":[]`} {
		var config mobileVRouteConfig
		if err := json.Unmarshal([]byte(`{"enabled":true,"route_rules":[{"id":"empty","name":"Empty","action":"reject"`+entries+`},{"id":"valid","name":"Valid","action":"reject","entries":["domain:blocked.test"]}]}`), &config); err != nil {
			t.Fatal(err)
		}
		config = sanitizeMobileVRouteConfig(config)
		if len(config.RouteRules) != 2 {
			t.Fatalf("empty rule discarded whole config: %+v", config)
		}
		if _, ok := decideMobileVRouteForDomain(config, "allowed.test", "80"); ok {
			t.Fatal("empty rule matched")
		}
		if d, ok := decideMobileVRouteForDomain(config, "blocked.test", "80"); !ok || !d.Reject || d.RuleID != "valid" {
			t.Fatalf("later rule lost: %+v", d)
		}
	}
}

func nodeIPTestConfig() mobileVRouteConfig {
	return mobileVRouteConfig{
		LocalNodeID: "9", Enabled: true,
		ProbeIPs: []mobileVRouteProbeIP{{NodeID: "9", IP: "198.18.0.9"}, {NodeID: "17", IP: "198.18.0.17", DisplayName: "Remote"}, {NodeID: "19", IP: "198.18.0.19"}},
		TopologyRules: []mobileVRouteTopology{
			{ID: "edge-9-17", FromNodeID: "9", ToNodeID: "17", Enabled: true, ToServiceDomain: "edge.test", ToServicePort: 12040, Secret: "fixture-secret", AuthTicket: "fixture-ticket"},
			{ID: "edge-17-19", FromNodeID: "17", ToNodeID: "19", Enabled: true},
		},
	}
}

func TestMobileVRouteNodeIPPlan(t *testing.T) {
	dir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, dir)
	config := nodeIPTestConfig()
	// A broad internet exit rule must not send a node's own IP to a different exit.
	config.RouteRules = []mobileVRouteRouteRule{{ID: "internet", Name: "Internet", Action: "probe_exit", ExitNodeID: "4", Entries: []string{"cidr:0.0.0.0/0"}}}
	if err := persistMobileVRouteConfig(dir, config); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"17", "19"} {
		d, err := decideVPNRouteForTarget("198.18.0." + node + ":8080")
		if err != nil || d.Direct || d.Reject || d.SelectedRouteID != "vroute:"+node {
			t.Fatalf("node IP route: %+v %v", d, err)
		}
		plan, err := buildMobileVRouteForwardPlan(dir, d.SelectedRouteID)
		if err != nil || plan.ExitNode != node || plan.NextNode != "17" || strings.Join(plan.Path, ",") != map[string]string{"17": "9,17", "19": "9,17,19"}[node] {
			t.Fatalf("authorized path: %+v %v", plan, err)
		}
	}
	if d, err := decideVPNRouteForTarget("203.0.113.8:80"); err != nil || d.SelectedRouteID != "vroute:4" {
		t.Fatalf("ordinary CIDR rule changed: %+v %v", d, err)
	}
	config.RouteRules = nil
	config.TopologyRules[0].AuthTicket = ""
	if err := persistMobileVRouteConfig(dir, config); err != nil {
		t.Fatal(err)
	}
	if _, err := buildMobileVRouteForwardPlan(dir, "vroute:17"); err == nil || !strings.Contains(err.Error(), "auth ticket missing") {
		t.Fatalf("missing auth accepted: %v", err)
	}
	config.TopologyRules[0].Enabled = false
	if err := persistMobileVRouteConfig(dir, config); err != nil {
		t.Fatal(err)
	}
	packet := buildMobileVRouteTestIPv4Packet(6, "198.18.0.2", "198.18.0.17", 12345, 80)
	if handled, err := mobileVRouteHandleVPNPacket(dir, packet, nil); !handled || err == nil {
		t.Fatalf("unreachable node leaked to direct dial: %t %v", handled, err)
	}
}

func TestMobileVRouteNodeIPPacketUsesCarrierWithoutExitRule(t *testing.T) {
	dir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, dir)
	if err := persistMobileVRouteConfig(dir, nodeIPTestConfig()); err != nil {
		t.Fatal(err)
	}
	plan, err := buildMobileVRouteForwardPlan(dir, "vroute:19")
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	carrier := newMobileVRouteCarrier(mobileVRouteCarrierKey(plan), plan, left)
	carrier.markActivity()
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items[carrier.key] = carrier
	mobileVRouteCarrierState.mu.Unlock()
	go carrier.runTXWorker()
	frames := make(chan mobileVRouteFrame, 1)
	errs := make(chan error, 1)
	go func() {
		f, e := readMobileVRouteFrame(bufio.NewReader(right))
		if e != nil {
			errs <- e
		} else {
			frames <- f
		}
	}()
	packet := buildMobileVRouteTestIPv4Packet(6, "198.18.0.2", "198.18.0.19", 12345, 8080)
	if handled, err := mobileVRouteHandleVPNPacket(dir, packet, nil); !handled || err != nil {
		t.Fatalf("node packet takeover: %t %v", handled, err)
	}
	select {
	case f := <-frames:
		expected, err := rewriteAndroidVPNIPv4Packet(packet, "198.18.0.9", "")
		if err != nil || f.MainType != mobileVRouteFrameMainTypeIP || !bytes.Equal(f.Data, expected) {
			t.Fatalf("node frame destination/source: %+v %v", f, err)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("node frame was not sent")
	}
}
