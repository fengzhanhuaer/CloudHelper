package main

import "testing"

func TestProbeTCPDebugCompletedConnectionKeepsDomain(t *testing.T) {
	state := newProbeTCPDebugState()
	relay := state.beginRelayWithOptions(probeTCPDebugRelayOptions{
		Scope:  "explicit",
		Side:   "socks5",
		Target: "example.com:443",
		Route: probeLocalTunnelRouteDecision{
			TargetAddr:   "example.com:443",
			Group:        "fallback",
			TunnelNodeID: "chain:1",
		},
	})
	if relay == nil {
		t.Fatal("relay is nil")
	}
	relay.touch("up", 128)
	relay.releaseSide()
	relay.releaseSide()

	payload := state.snapshotPayload("node-1", "req-1")
	if payload.ActiveCount != 0 {
		t.Fatalf("active_count=%d, want 0", payload.ActiveCount)
	}
	if payload.CompletedCount != 1 {
		t.Fatalf("completed_count=%d, want 1", payload.CompletedCount)
	}
	item := payload.Completed[0]
	if item.Domain != "example.com" {
		t.Fatalf("domain=%q, want example.com", item.Domain)
	}
	if item.DomainSource != "target" {
		t.Fatalf("domain_source=%q, want target", item.DomainSource)
	}
	if item.Group != "fallback" {
		t.Fatalf("group=%q, want fallback", item.Group)
	}
	if item.BytesUp != 128 {
		t.Fatalf("bytes_up=%d, want 128", item.BytesUp)
	}
}
