package main

import "testing"

func TestBuildProbeSubstreamMonitorItemIncludesExplicitProxyTunnel(t *testing.T) {
	item := probeTCPDebugConnectionItemPayload{
		ID:          "probe-tcp-1",
		Status:      "active",
		TrackingID:  "track-explicit-1",
		Scope:       "explicit",
		Side:        "socks5",
		FlowID:      "explicit-flow",
		Target:      "example.com:443",
		RouteTarget: "203.0.113.10:443",
		Group:       "default",
		NodeID:      "chain:1",
		Transport:   "tunnel",
	}

	sub, ok := buildProbeSubstreamMonitorItem(item)
	if !ok {
		t.Fatal("explicit proxy tunnel item was filtered out")
	}
	if sub.Kind != "explicit_proxy" {
		t.Fatalf("kind=%q, want explicit_proxy", sub.Kind)
	}
	if sub.FlowID != "explicit-flow" {
		t.Fatalf("flow_id=%q, want explicit-flow", sub.FlowID)
	}
	if sub.TrackingID != "track-explicit-1" {
		t.Fatalf("tracking_id=%q, want track-explicit-1", sub.TrackingID)
	}
}

func TestBuildProbeSubstreamMonitorItemFiltersDirectConnections(t *testing.T) {
	item := probeTCPDebugConnectionItemPayload{
		ID:        "probe-tcp-1",
		Status:    "active",
		Scope:     "explicit",
		Target:    "example.com:443",
		Direct:    true,
		Transport: "direct",
	}

	if _, ok := buildProbeSubstreamMonitorItem(item); ok {
		t.Fatal("direct TCP connection should not be rendered as a substream")
	}
}

func TestMergeProbeSubstreamMonitorPairsCombinesEntryAndExit(t *testing.T) {
	local := probeSubstreamMonitorPayload{
		Active: []probeSubstreamMonitorItem{{
			ID:         "entry-1",
			Status:     "active",
			TrackingID: "track-1",
			FlowID:     "flow-1",
			Scope:      "explicit",
			Side:       "socks5",
			Group:      "google",
			Target:     "mail.google.com:443",
		}},
	}
	peer := probeLocalPeerStatusMonitorSnapshot{
		Groups: []probePeerStatusGroupSnapshot{{
			Group: "google",
			Exit: probePeerStatusSidePayload{
				Substreams: probeSubstreamMonitorPayload{
					Active: []probeSubstreamMonitorItem{{
						ID:         "exit-1",
						Status:     "active",
						TrackingID: "track-1",
						FlowID:     "flow-1",
						Scope:      "chain_exit",
						Side:       "remote",
						Group:      "google",
						Target:     "mail.google.com:443",
					}},
				},
			},
		}},
	}

	pairs := mergeProbeSubstreamMonitorPairs(local, peer)
	if len(pairs) != 1 {
		t.Fatalf("pairs=%d, want 1", len(pairs))
	}
	if pairs[0].Status != "complete" {
		t.Fatalf("status=%q, want complete", pairs[0].Status)
	}
	if pairs[0].Entry == nil || pairs[0].Entry.ID != "entry-1" {
		t.Fatalf("entry=%+v, want entry-1", pairs[0].Entry)
	}
	if pairs[0].Exit == nil || pairs[0].Exit.ID != "exit-1" {
		t.Fatalf("exit=%+v, want exit-1", pairs[0].Exit)
	}
}

func TestBeginProbeLocalExplicitProxyTCPDebugReusesRouteFlowID(t *testing.T) {
	state := globalProbeTCPDebugState
	globalProbeTCPDebugState = newProbeTCPDebugState()
	t.Cleanup(func() { globalProbeTCPDebugState = state })

	relay := beginProbeLocalExplicitProxyTCPDebug("socks5", "example.com:443", probeLocalTunnelRouteDecision{
		FlowID:     "explicit-flow-1",
		TargetAddr: "example.com:443",
		Group:      "default",
	})
	if relay == nil {
		t.Fatal("relay is nil")
	}
	defer relay.releaseSide()
	defer relay.releaseSide()

	payload := globalProbeTCPDebugState.snapshotPayload("node-1", "req-1")
	if payload.ActiveCount != 1 {
		t.Fatalf("active_count=%d, want 1", payload.ActiveCount)
	}
	item := payload.Active[0]
	if item.FlowID != "explicit-flow-1" {
		t.Fatalf("flow_id=%q, want explicit-flow-1", item.FlowID)
	}
	if item.TrackingID != "explicit-flow-1" {
		t.Fatalf("tracking_id=%q, want explicit-flow-1", item.TrackingID)
	}
}
