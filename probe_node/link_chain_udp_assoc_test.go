package main

import (
	"testing"
	"time"
)

func TestProbeChainUDPAssociationEffectiveGCInterval(t *testing.T) {
	got := probeChainUDPAssociationEffectiveGCInterval(probeChainPortForwardSessionIdleTTL)
	want := probeChainPortForwardSessionGCInterval
	if half := probeChainPortForwardSessionIdleTTL / 2; half > 0 && (want <= 0 || half < want) {
		want = half
	}
	if want <= 0 {
		want = time.Second
	}
	if got != want {
		t.Fatalf("probeChainUDPAssociationEffectiveGCInterval() = %s, want %s", got, want)
	}
}

func TestSnapshotProbeUDPAssociationsIncludesSourceFields(t *testing.T) {
	old := globalProbeChainUDPAssociationPool
	pool := &probeChainUDPAssociationPool{items: map[string]*probeChainUDPAssociation{}}
	globalProbeChainUDPAssociationPool = pool
	t.Cleanup(func() { globalProbeChainUDPAssociationPool = old })

	assoc := &probeChainUDPAssociation{
		key:              "k1",
		target:           "8.8.8.8:53",
		assocKeyV2:       "assoc-k1",
		flowID:           "flow-k1",
		sourceKey:        "10.0.0.8:53000",
		sourceRefs:       2,
		routeTarget:      "dns.google:53",
		routeFingerprint: "dns.google:53",
		routeNodeID:      "chain:node-1",
		routeGroup:       "dns",
		natMode:          probeChainUDPAssociationNATModeDefault,
		ttlProfile:       probeChainUDPAssociationTTLProfileDNSFast,
		idleTimeout:      30 * time.Second,
		gcInterval:       10 * time.Second,
		createdAtUnixMS:  time.Now().UnixMilli(),
	}
	assoc.refs.Store(1)
	assoc.lastActiveUnix.Store(time.Now().Unix())
	pool.items[assoc.key] = assoc

	items := snapshotProbeUDPAssociations()
	if len(items) != 1 {
		t.Fatalf("len(items)=%d", len(items))
	}
	item := items[0]
	if item.SourceKey != assoc.sourceKey {
		t.Fatalf("source_key=%q", item.SourceKey)
	}
	if item.SourceRefs != assoc.sourceRefs {
		t.Fatalf("source_refs=%d", item.SourceRefs)
	}
	if item.FlowID != assoc.flowID {
		t.Fatalf("flow_id=%q", item.FlowID)
	}
	if item.Refs != assoc.refs.Load() {
		t.Fatalf("refs=%d", item.Refs)
	}
}

func TestProbeChainUDPAssociationEffectiveGCIntervalHalfIdle(t *testing.T) {
	idle := 10 * time.Second
	got := probeChainUDPAssociationEffectiveGCInterval(idle)
	if want := idle / 2; got != want {
		t.Fatalf("idle=%s interval=%s, want %s", idle, got, want)
	}
}

func TestProbeChainUDPAssociationEffectiveGCIntervalFallback(t *testing.T) {
	got := probeChainUDPAssociationEffectiveGCInterval(0)
	if got != probeChainPortForwardSessionGCInterval {
		t.Fatalf("idle=0 interval=%s, want %s", got, probeChainPortForwardSessionGCInterval)
	}
}
