package mobilecore

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestMobileVRouteHopFirstPathSelection(t *testing.T) {
	resetMobileVRouteVPNStateForTest(t, t.TempDir())
	mobileVRoutePathSamples.Lock()
	old := mobileVRoutePathSamples.items
	mobileVRoutePathSamples.items = map[string]mobileVRoutePathSample{
		"1>4": {RTT: 200, At: time.Now()}, "1>2>4": {RTT: 80, At: time.Now()}, "1>3>4": {RTT: 20, At: time.Now()},
	}
	mobileVRoutePathSamples.Unlock()
	t.Cleanup(func() {
		mobileVRoutePathSamples.Lock()
		mobileVRoutePathSamples.items = old
		mobileVRoutePathSamples.Unlock()
	})
	config := mobileVRouteConfig{LocalNodeID: "1", TopologyRules: []mobileVRouteTopology{
		{ID: "a", FromNodeID: "1", ToNodeID: "2", Enabled: true}, {ID: "b", FromNodeID: "2", ToNodeID: "4", Enabled: true},
		{ID: "c", FromNodeID: "1", ToNodeID: "3", Enabled: true}, {ID: "d", FromNodeID: "3", ToNodeID: "4", Enabled: true},
		{ID: "direct", FromNodeID: "1", ToNodeID: "4", Enabled: true},
	}}
	path, err := mobileVRouteShortestPath(config, "1", "4")
	if err != nil || !reflect.DeepEqual(path, []string{"1", "4"}) {
		t.Fatalf("hops must beat RTT: %v %v", path, err)
	}
	config.TopologyRules = config.TopologyRules[:4]
	paths := mobileVRouteShortestCandidates(config, "1", "4")
	if len(paths) != 2 {
		t.Fatalf("equal-hop candidates: %v", paths)
	}
	if !mobileVRoutePathLess(config, []string{"1", "3", "4"}, []string{"1", "2", "4"}) {
		t.Fatal("equal hops must prefer 20ms")
	}
	mobileVRoutePathSamples.Lock()
	delete(mobileVRoutePathSamples.items, "1>2>4")
	mobileVRoutePathSamples.Unlock()
	if !mobileVRoutePathLess(config, []string{"1", "3", "4"}, []string{"1", "2", "4"}) {
		t.Fatal("unknown RTT must not win as 0ms")
	}
	mobileVRoutePathSamples.Lock()
	mobileVRoutePathSamples.items = map[string]mobileVRoutePathSample{}
	mobileVRoutePathSamples.Unlock()
	if !mobileVRoutePathLess(config, []string{"1", "2", "4"}, []string{"1", "3", "4"}) {
		t.Fatal("unknown ties must be deterministic")
	}
	config.TopologyRules[2].Enabled = false
	if got := mobileVRouteShortestCandidates(config, "1", "4"); len(got) != 1 || got[0][1] != "2" {
		t.Fatalf("disabled edge used: %v", got)
	}
}

func TestMobileVRouteAdjacentRouteRTT(t *testing.T) {
	resetMobileVRouteVPNStateForTest(t, t.TempDir())
	slow := mobileVRouteTopology{ID: "ipv4", FromNodeID: "1", ToNodeID: "2", Enabled: true}
	fast := slow
	fast.ID = "ipv6"
	slowCarrier := &mobileVRouteCarrier{plan: mobileVRouteForwardPlan{LocalNode: "1", NextNode: "2", RouteID: mobileVRouteRuntimeRouteID(slow)}}
	fastCarrier := &mobileVRouteCarrier{plan: mobileVRouteForwardPlan{LocalNode: "1", NextNode: "2", RouteID: mobileVRouteRuntimeRouteID(fast)}}
	slowCarrier.rttMS.Store(200)
	fastCarrier.rttMS.Store(20)
	slowCarrier.rttAt.Store(time.Now().UnixNano())
	fastCarrier.rttAt.Store(time.Now().UnixNano())
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items = map[string]*mobileVRouteCarrier{"slow": slowCarrier, "fast": fastCarrier}
	mobileVRouteCarrierState.mu.Unlock()
	// Avoid closing these socket-free selection fixtures in the generic reset.
	defer func() {
		mobileVRouteCarrierState.mu.Lock()
		mobileVRouteCarrierState.items = map[string]*mobileVRouteCarrier{}
		mobileVRouteCarrierState.mu.Unlock()
	}()
	config := mobileVRouteConfig{TopologyRules: []mobileVRouteTopology{slow, fast}}
	rule, _, ok := mobileVRouteFindTopologyRule(config, "1", "2")
	if !ok || rule.ID != "ipv6" {
		t.Fatalf("faster equal-hop endpoint: %+v", rule)
	}
	fastCarrier.rttAt.Store(time.Now().Add(-2 * time.Minute).UnixNano())
	rule, _, _ = mobileVRouteFindTopologyRule(config, "1", "2")
	if rule.ID != "ipv4" {
		t.Fatal("stale RTT must be unknown")
	}
}

func TestMobileVRoutePathSampleWireAndCancellation(t *testing.T) {
	carrier := &mobileVRouteCarrier{tx: make(chan mobileVRouteFrame, 1), txControl: make(chan mobileVRouteFrame, 1), done: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := measureMobileVRoutePath(carrier, []string{"1", "2", "4"}, nil); done <- err }()
	frame := <-carrier.txControl
	if frame.MainType != mobileVRouteFrameMainTypePathRTT {
		t.Fatal("wrong sample frame")
	}
	var request mobileVRouteControlProbePayload
	if err := json.Unmarshal(frame.Data, &request); err != nil {
		t.Fatal(err)
	}
	request.OK = true
	request.Responder = "4"
	payload, _ := json.Marshal(request)
	if err := completeMobileVRouteRTTResponse(mobileVRouteFrame{Data: payload}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cancel := make(chan struct{})
	close(cancel)
	start := time.Now()
	if _, err := measureMobileVRoutePath(carrier, []string{"1", "2"}, cancel); err == nil || time.Since(start) > time.Second {
		t.Fatal("cancel must stop a sample without closing the carrier")
	}
}

func TestMobileVRouteRTTUpdateKeepsEstablishedFlowPath(t *testing.T) {
	resetMobileVRouteVPNStateForTest(t, t.TempDir())
	cfg := mobileVRouteConfig{TopologyRules: []mobileVRouteTopology{
		{ID: "12", FromNodeID: "1", ToNodeID: "2", Enabled: true}, {ID: "24", FromNodeID: "2", ToNodeID: "4", Enabled: true},
		{ID: "13", FromNodeID: "1", ToNodeID: "3", Enabled: true}, {ID: "34", FromNodeID: "3", ToNodeID: "4", Enabled: true},
	}}
	oldPlan := mobileVRouteForwardPlan{LocalNode: "1", ExitNode: "4", NextNode: "2", RouteID: mobileVRouteRuntimeRouteID(cfg.TopologyRules[0]), Path: []string{"1", "2", "4"}, Config: cfg}
	newPlan := mobileVRouteForwardPlan{LocalNode: "1", ExitNode: "4", NextNode: "3", RouteID: mobileVRouteRuntimeRouteID(cfg.TopologyRules[2]), Path: []string{"1", "3", "4"}, Config: cfg}
	packet := []byte{0x45, 0, 0, 28, 0, 0, 0, 0, 64, 17, 0, 0, 10, 0, 0, 1, 10, 0, 0, 2, 3, 232, 1, 187, 0, 8, 0, 0}
	carrier := &mobileVRouteCarrier{plan: oldPlan, key: "old"}
	key := mobileVRouteBindingKey(oldPlan, mobileVRoutePacketFlowHash(packet, 2166136261))
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items["old"] = carrier
	mobileVRouteCarrierState.flowBindings[key] = mobileVRouteCarrierFlowBinding{CarrierKey: "old", Path: oldPlan.Path, LastSeen: time.Now()}
	mobileVRouteCarrierState.mu.Unlock()
	defer func() {
		mobileVRouteCarrierState.mu.Lock()
		delete(mobileVRouteCarrierState.items, "old")
		mobileVRouteCarrierState.mu.Unlock()
	}()
	if got := pinMobileVRouteFlowPlan(newPlan, packet); !reflect.DeepEqual(got.Path, oldPlan.Path) {
		t.Fatalf("existing flow moved: %v", got.Path)
	}
	packet[21]++
	if got := pinMobileVRouteFlowPlan(newPlan, packet); !reflect.DeepEqual(got.Path, newPlan.Path) {
		t.Fatalf("new flow did not use new path: %v", got.Path)
	}
	packet[21]--
	newPlan.Config.TopologyRules[1].Enabled = false
	if got := pinMobileVRouteFlowPlan(newPlan, packet); !reflect.DeepEqual(got.Path, newPlan.Path) {
		t.Fatal("removed edge retained")
	}
}
