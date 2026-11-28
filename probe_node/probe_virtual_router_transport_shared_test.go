package main

import (
	"errors"
	"testing"
	"time"
)

func TestProbeRouteTCPConnTuningShouldLog(t *testing.T) {
	if probeRouteTCPConnTuningShouldLog("ok", nil, nil) {
		t.Fatal("successful socket tuning should be silent")
	}
	if !probeRouteTCPConnTuningShouldLog("socket_buffer_below_requested", nil, nil) {
		t.Fatal("non-ok socket tuning hint should be logged")
	}
	if !probeRouteTCPConnTuningShouldLog("ok", errors.New("set buffer failed")) {
		t.Fatal("socket tuning errors should be logged even when hint is ok")
	}
}

func TestProbeRouteHTTP3StreamOpenTimeoutPreservesCallerBudget(t *testing.T) {
	if got := probeRouteHTTP3StreamOpenTimeout(0); got != probeRouteRelayProtocolProbeTimeout {
		t.Fatalf("default timeout=%s, want %s", got, probeRouteRelayProtocolProbeTimeout)
	}
	if got := probeRouteHTTP3StreamOpenTimeout(2 * time.Second); got != 2*time.Second {
		t.Fatalf("short timeout=%s, want 2s", got)
	}
	runtimeBudget := probeRouteRelayDialTimeout + probeRouteRelayResponseReadDeadline
	if got := probeRouteHTTP3StreamOpenTimeout(runtimeBudget); got != runtimeBudget {
		t.Fatalf("runtime timeout=%s, want %s", got, runtimeBudget)
	}
}

func TestNewProbeRouteQUICConfigKeepsIdleCarriersAlive(t *testing.T) {
	config := newProbeRouteQUICConfig(7)
	if config.KeepAlivePeriod != probeRouteRelayQUICKeepAlivePeriod {
		t.Fatalf("keepalive=%s, want %s", config.KeepAlivePeriod, probeRouteRelayQUICKeepAlivePeriod)
	}
	if config.MaxIdleTimeout != probeRouteRelayQUICMaxIdleTimeout {
		t.Fatalf("idle timeout=%s, want %s", config.MaxIdleTimeout, probeRouteRelayQUICMaxIdleTimeout)
	}
	if config.KeepAlivePeriod >= config.MaxIdleTimeout {
		t.Fatalf("keepalive=%s must be shorter than idle timeout=%s", config.KeepAlivePeriod, config.MaxIdleTimeout)
	}
	if config.MaxIncomingStreams != 7 {
		t.Fatalf("max incoming streams=%d, want 7", config.MaxIncomingStreams)
	}
}
