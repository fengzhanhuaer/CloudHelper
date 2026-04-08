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
