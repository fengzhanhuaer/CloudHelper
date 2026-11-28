package main

import (
	"errors"
	"testing"
	"time"

	"github.com/cloudhelper/probe_node/internal/tcpwindow"
	"github.com/quic-go/quic-go"
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

func TestProbeQUICConnectionsShareReceiveWindow(t *testing.T) {
	var pool probeQUICWindowPool
	one, two, three := new(quic.Conn), new(quic.Conn), new(quic.Conn)
	initial := uint64(probeRouteRelayQUICInitialConnectionWindow)
	if !pool.reserve(one, initial, true) || !pool.reserve(two, initial, true) {
		t.Fatal("connections could not use available initial budget")
	}
	if !pool.grow(one, tcpwindow.Payload-2*initial) || pool.grow(two, 1) || pool.reserve(three, initial, true) {
		t.Fatal("QUIC allowed independent allocation beyond the shared budget")
	}
	pool.release(one)
	if !pool.reserve(three, initial, true) || pool.grow(one, 1) {
		t.Fatal("budget was not correctly recovered when the connection closed")
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
