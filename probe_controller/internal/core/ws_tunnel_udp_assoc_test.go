package core

import (
	"testing"
	"time"
)

func TestTunnelUDPAssociationEffectiveGCInterval(t *testing.T) {
	got := tunnelUDPAssociationEffectiveGCInterval(tunnelUDPAssociationIdleTTL)
	want := tunnelUDPAssociationGCInterval
	if half := tunnelUDPAssociationIdleTTL / 2; half > 0 && half < want {
		want = half
	}
	if want <= 0 {
		want = time.Second
	}
	if got != want {
		t.Fatalf("tunnelUDPAssociationEffectiveGCInterval() = %s, want %s", got, want)
	}
}

func TestTunnelUDPAssociationEffectiveGCIntervalHalfIdle(t *testing.T) {
	idle := 10 * time.Second
	got := tunnelUDPAssociationEffectiveGCInterval(idle)
	if want := idle / 2; got != want {
		t.Fatalf("idle=%s interval=%s, want %s", idle, got, want)
	}
}

func TestTunnelUDPAssociationEffectiveGCIntervalFallback(t *testing.T) {
	got := tunnelUDPAssociationEffectiveGCInterval(0)
	if got != tunnelUDPAssociationGCInterval {
		t.Fatalf("idle=0 should keep configured gc interval, got=%s want=%s", got, tunnelUDPAssociationGCInterval)
	}
}
