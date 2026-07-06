package main

import "testing"

func TestBuildProbeSubstreamMonitorItemFiltersLegacyExplicitProxy(t *testing.T) {
	item := probeTCPDebugConnectionItemPayload{
		ID:          "probe-tcp-1",
		Status:      "active",
		TrackingID:  "track-explicit-1",
		Scope:       "explicit",
		Side:        "local",
		FlowID:      "explicit-flow",
		Target:      "example.com:443",
		RouteTarget: "203.0.113.10:443",
		Group:       "default",
		NodeID:      "chain:1",
		Transport:   "tunnel",
	}

	if _, ok := buildProbeSubstreamMonitorItem(item); ok {
		t.Fatal("legacy explicit route item should not be rendered as a substream")
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

func TestMergeProbeSubstreamMonitorPairsCombinesTUNEntryAndExit(t *testing.T) {
	local := probeSubstreamMonitorPayload{
		Active: []probeSubstreamMonitorItem{{
			ID:         "entry-1",
			Status:     "active",
			TrackingID: "track-1",
			FlowID:     "flow-1",
			Scope:      "tun",
			Side:       "local",
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
