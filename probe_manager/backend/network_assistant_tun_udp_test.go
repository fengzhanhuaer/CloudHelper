//go:build windows

package backend

import (
	"testing"
	"time"
)

func TestLocalTUNUDPEffectiveGCInterval(t *testing.T) {
	got := localTUNUDPEffectiveGCInterval(localTUNUDPAssociationTimeout)
	want := localTUNUDPAssociationGCInterval
	if half := localTUNUDPAssociationTimeout / 2; half > 0 && half < want {
		want = half
	}
	if want <= 0 {
		want = time.Second
	}
	if got != want {
		t.Fatalf("localTUNUDPEffectiveGCInterval() = %s, want %s", got, want)
	}
}

func TestLocalTUNUDPEffectiveGCIntervalHalfIdle(t *testing.T) {
	idle := 10 * time.Second
	got := localTUNUDPEffectiveGCInterval(idle)
	if want := idle / 2; got != want {
		t.Fatalf("idle=%s interval=%s, want %s", idle, got, want)
	}
}

func TestLocalTUNUDPEffectiveGCIntervalFallback(t *testing.T) {
	if got := localTUNUDPEffectiveGCInterval(0); got != localTUNUDPAssociationGCInterval {
		t.Fatalf("idle=0 interval=%s, want %s", got, localTUNUDPAssociationGCInterval)
	}
}
