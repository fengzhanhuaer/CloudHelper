package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProbeVirtualRouterBusinessPayloadCannotReplaceAuthenticatedPath(t *testing.T) {
	probeVirtualRouterState.mu.Lock()
	previous := probeVirtualRouterState.config
	probeVirtualRouterState.config.TopologyRules = []probeVirtualRouterTopologyRule{
		{Enabled: true, FromNodeID: "1", ToNodeID: "2", UserID: "owner-a"},
		{Enabled: true, FromNodeID: "2", ToNodeID: "3", UserID: "owner-b"},
	}
	probeVirtualRouterState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = previous
		probeVirtualRouterState.mu.Unlock()
	})
	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{localNodeID: "2", peerNodeID: "1", userID: "owner-a"}}
	for _, family := range []struct{ main, sub uint16 }{
		{probeVirtualRouterFrameMainTypePingPong, probeVirtualRouterPingPongSubTypePing},
		{probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterPathRTTSubTypeQuery},
		{probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterSpeedSubTypeStart},
		{probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterSpeedSubTypeSend},
		{probeVirtualRouterFrameMainTypeRouteTest, probeVirtualRouterRouteTestSubTypeProbe},
		{probeVirtualRouterFrameMainTypeFakeIPVerify, probeVirtualRouterFakeIPVerifySubTypeQuery},
		{probeVirtualRouterFrameMainTypeDebugLog, probeVirtualRouterDebugLogSubTypeQuery},
	} {
		for _, substituted := range [][]string{{"2", "3"}, {"1", "2", "3"}, {"2", "1"}} {
			payload, err := json.Marshal(map[string]any{"request_id": "boundary-test", "path": substituted})
			if err != nil {
				t.Fatal(err)
			}
			frame, err := buildProbeVirtualRouterBusinessFrame(family.main, family.sub, payload, []string{"1", "2"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := handleProbeVirtualRouterFrame(rt, nil, frame, nil); err == nil || !strings.Contains(err.Error(), "payload path") {
				t.Fatalf("family=%d/%d path=%v reached dispatch: %v", family.main, family.sub, substituted, err)
			}
		}
	}
	// Exercise the same ingress and dispatch with a legitimate ping and inspect
	// its queued reply, without opening sockets or invoking host commands.
	link := newProbeVirtualRouterFrameLink("security-ping", rt, nil, nil)
	t.Cleanup(func() { stopProbeVirtualRouterFrameLink(link) })
	for _, path := range [][]string{nil, {"1", "2"}} {
		payload, _ := json.Marshal(map[string]any{"request_id": "legitimate-ping", "path": path})
		frame, err := buildProbeVirtualRouterBusinessFrame(probeVirtualRouterFrameMainTypePingPong, probeVirtualRouterPingPongSubTypePing, payload, []string{"1", "2"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := handleProbeVirtualRouterFrame(rt, link, frame, nil); err != nil {
			t.Fatalf("normal ping rejected: %v", err)
		}
		reply, ok := link.txControl.TryPop()
		if !ok {
			t.Fatal("normal ping did not queue a response")
		}
		control, err := probeVirtualRouterFrameControl(reply, nil)
		if err != nil || strings.Join(control.Path, ">") != "2>1" {
			t.Fatalf("invalid response envelope: %+v %v", control, err)
		}
		var body probeVirtualRouterControlProbePayload
		if err := json.Unmarshal(reply.Data, &body); err != nil {
			t.Fatal(err)
		}
		if err := validateProbeVirtualRouterBusinessPayloadPath(reply.MainType, reply.SubType, body.Path, control.Path); err != nil {
			t.Fatalf("generated legacy pong rejected: %v", err)
		}
	}
}

func TestProbeVirtualRouterBusinessPayloadResponseCompatibility(t *testing.T) {
	for _, family := range []struct{ main, sub uint16 }{
		{probeVirtualRouterFrameMainTypePingPong, probeVirtualRouterPingPongSubTypePong},
		{probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterPathRTTSubTypeResp},
	} {
		if err := validateProbeVirtualRouterBusinessPayloadPath(family.main, family.sub, []string{"1", "2", "3"}, []string{"3", "2", "1"}); err != nil {
			t.Fatalf("legacy response path rejected: %v", err)
		}
		if err := validateProbeVirtualRouterBusinessPayloadPath(family.main, family.sub, []string{"1", "9", "3"}, []string{"3", "2", "1"}); err == nil {
			t.Fatal("response introduced an unauthorized middle hop")
		}
	}
	// Hop reports carry only the already-traversed return prefix in both fields.
	if err := validateProbeVirtualRouterBusinessPayloadPath(probeVirtualRouterFrameMainTypeRouteTest, probeVirtualRouterRouteTestSubTypeReport, []string{"2", "1"}, []string{"2", "1"}); err != nil {
		t.Fatalf("hop report rejected: %v", err)
	}
}
