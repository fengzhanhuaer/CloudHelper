package main

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

type probeVirtualRouterCancelTrackingH3Stream struct {
	cancelReadCalls  int
	cancelWriteCalls int
	closeCalls       int
}

func (s *probeVirtualRouterCancelTrackingH3Stream) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (s *probeVirtualRouterCancelTrackingH3Stream) Write(payload []byte) (int, error) {
	return len(payload), nil
}

func (s *probeVirtualRouterCancelTrackingH3Stream) Close() error {
	s.closeCalls++
	return nil
}

func (s *probeVirtualRouterCancelTrackingH3Stream) CancelRead(quic.StreamErrorCode) {
	s.cancelReadCalls++
}

func (s *probeVirtualRouterCancelTrackingH3Stream) CancelWrite(quic.StreamErrorCode) {
	s.cancelWriteCalls++
}

func (s *probeVirtualRouterCancelTrackingH3Stream) SetReadDeadline(time.Time) error {
	return nil
}

func (s *probeVirtualRouterCancelTrackingH3Stream) SetWriteDeadline(time.Time) error {
	return nil
}

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

func TestProbeVirtualRouterH3ConnCloseCancelsBothDirectionsOnce(t *testing.T) {
	stream := &probeVirtualRouterCancelTrackingH3Stream{}
	conn := &probeVirtualRouterH3Conn{stream: stream}

	if err := conn.Close(); err != nil {
		t.Fatalf("first close failed: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
	if stream.cancelReadCalls != 1 || stream.cancelWriteCalls != 1 || stream.closeCalls != 1 {
		t.Fatalf("close calls read=%d write=%d close=%d, want 1/1/1", stream.cancelReadCalls, stream.cancelWriteCalls, stream.closeCalls)
	}
}
