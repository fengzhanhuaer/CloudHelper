package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// HTTP/3 relay connection pool.
//
// HTTP/3 natively multiplexes request streams over a single QUIC connection, so
// every relay data stream toward a given endpoint can share one QUIC connection
// instead of performing a fresh QUIC+TLS handshake per stream. That handshake is
// the dominant relay-server CPU cost when many streams are opened (e.g. a TUN
// proxy fanning out to dozens of upstream connections), so pooling removes the
// "CPU rises as connections accumulate" behavior.

const (
	probeRouteHTTP3PoolIdleTTL  = 60 * time.Second
	probeRouteHTTP3PoolSweepGap = 20 * time.Second
)

type probeRouteHTTP3PooledConn struct {
	key        string
	quicConn   probeRouteHTTP3QUICConn
	clientConn *http3.ClientConn

	mu       sync.Mutex
	streams  int
	lastUsed time.Time
	retired  bool
}

// probeRouteHTTP3QUICConn is the subset of *quic.Conn the pool needs (kept as an
// interface so the pool logic stays testable).
type probeRouteHTTP3QUICConn interface {
	CloseWithError(code quic.ApplicationErrorCode, msg string) error
	Context() context.Context
}

func (p *probeRouteHTTP3PooledConn) addStream() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.streams++
	p.lastUsed = time.Now()
	p.mu.Unlock()
}

func (p *probeRouteHTTP3PooledConn) removeStream() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.streams > 0 {
		p.streams--
	}
	p.lastUsed = time.Now()
	p.mu.Unlock()
}

func (p *probeRouteHTTP3PooledConn) idleSince(now time.Time) (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streams > 0 {
		return 0, false
	}
	return now.Sub(p.lastUsed), true
}

var probeRouteHTTP3Pool = struct {
	mu        sync.Mutex
	conns     map[string]*probeRouteHTTP3PooledConn
	sweeperOn bool
}{conns: map[string]*probeRouteHTTP3PooledConn{}}

func probeRouteHTTP3PoolKey(relayHost string, relayPort int, relayDialHost string, relayHostHeader string) string {
	return strings.Join([]string{
		strings.TrimSpace(relayHost),
		strconv.Itoa(relayPort),
		strings.TrimSpace(relayDialHost),
		strings.TrimSpace(relayHostHeader),
	}, "|")
}

// acquireProbeRouteHTTP3PooledConn returns a healthy pooled QUIC/H3 connection for
// the endpoint, creating one if necessary. reused reports whether an existing
// connection was returned (vs freshly dialed).
func acquireProbeRouteHTTP3PooledConn(routeID, relayHost string, relayPort int, relayDialHost, relayHostHeader string, openTimeout time.Duration) (pooled *probeRouteHTTP3PooledConn, reused bool, err error) {
	key := probeRouteHTTP3PoolKey(relayHost, relayPort, relayDialHost, relayHostHeader)

	probeRouteHTTP3Pool.mu.Lock()
	if existing, ok := probeRouteHTTP3Pool.conns[key]; ok {
		if probeRouteHTTP3PooledConnHealthy(existing) {
			existing.mu.Lock()
			existing.lastUsed = time.Now()
			existing.mu.Unlock()
			probeRouteHTTP3Pool.mu.Unlock()
			return existing, true, nil
		}
		// Dead connection: drop it and fall through to redial.
		delete(probeRouteHTTP3Pool.conns, key)
		go func(c *probeRouteHTTP3PooledConn) { _ = closeProbeRouteHTTP3PooledConn(c) }(existing)
	}
	probeRouteHTTP3Pool.mu.Unlock()

	created, err := dialProbeRouteHTTP3PooledConn(routeID, relayHost, relayPort, relayDialHost, relayHostHeader, openTimeout)
	if err != nil {
		return nil, false, err
	}
	created.key = key

	probeRouteHTTP3Pool.mu.Lock()
	// Another goroutine may have created one concurrently; prefer the existing
	// healthy one and discard ours.
	if existing, ok := probeRouteHTTP3Pool.conns[key]; ok && probeRouteHTTP3PooledConnHealthy(existing) {
		probeRouteHTTP3Pool.mu.Unlock()
		go func(c *probeRouteHTTP3PooledConn) { _ = closeProbeRouteHTTP3PooledConn(c) }(created)
		existing.mu.Lock()
		existing.lastUsed = time.Now()
		existing.mu.Unlock()
		return existing, true, nil
	}
	probeRouteHTTP3Pool.conns[key] = created
	ensureProbeRouteHTTP3PoolSweeperLocked()
	probeRouteHTTP3Pool.mu.Unlock()
	return created, false, nil
}

func probeRouteHTTP3PooledConnHealthy(p *probeRouteHTTP3PooledConn) bool {
	if p == nil || p.clientConn == nil || p.quicConn == nil {
		return false
	}
	p.mu.Lock()
	retired := p.retired
	p.mu.Unlock()
	if retired {
		return false
	}
	select {
	case <-p.quicConn.Context().Done():
		return false
	default:
		return true
	}
}

func dialProbeRouteHTTP3PooledConn(routeID, relayHost string, relayPort int, relayDialHost, relayHostHeader string, openTimeout time.Duration) (*probeRouteHTTP3PooledConn, error) {
	if openTimeout <= 0 {
		openTimeout = probeRouteRelayProtocolProbeTimeout
	}
	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(relayPort))
	tlsConf := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{http3.NextProtoH3},
		ServerName:         resolveProbeRouteClientTLSServerName("websocket-h3", relayDialHost, relayHostHeader),
		InsecureSkipVerify: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()

	quicConn, err := dialProbeRouteBoundQUIC(ctx, dialHostPort, tlsConf, newProbeRouteQUICConfig(0))
	if err != nil {
		return nil, err
	}
	transport := &http3.Transport{}
	clientConn := transport.NewClientConn(quicConn)
	select {
	case <-clientConn.ReceivedSettings():
		settings := clientConn.Settings()
		enableExtendedConnect := settings != nil && settings.EnableExtendedConnect
		log.Printf("probe route relay h3 websocket settings: route=%s relay=%s:%d dial_host=%s host_header=%s extended_connect=%t", strings.TrimSpace(routeID), strings.TrimSpace(relayHost), relayPort, strings.TrimSpace(relayDialHost), strings.TrimSpace(relayHostHeader), enableExtendedConnect)
	case <-ctx.Done():
		_ = quicConn.CloseWithError(0, "h3 websocket settings timeout")
		return nil, fmt.Errorf("probe relay h3 websocket open timeout: relay=%s:%d", relayDialHost, relayPort)
	case <-clientConn.Context().Done():
		return nil, fmt.Errorf("probe relay h3 websocket failed: %w", context.Cause(clientConn.Context()))
	}
	if settings := clientConn.Settings(); settings == nil || !settings.EnableExtendedConnect {
		_ = quicConn.CloseWithError(0, "h3 websocket extended connect disabled")
		return nil, fmt.Errorf("probe relay h3 websocket failed: server did not enable extended connect")
	}
	return &probeRouteHTTP3PooledConn{
		quicConn:   quicConn,
		clientConn: clientConn,
		lastUsed:   time.Now(),
	}, nil
}

// releaseProbeRouteHTTP3PooledConn is called after a stream-open failure. When
// drop is true the connection is retired and removed from the pool (it is likely
// dead); the underlying QUIC conn is only closed once no streams remain.
func releaseProbeRouteHTTP3PooledConn(pooled *probeRouteHTTP3PooledConn, drop bool) {
	if pooled == nil {
		return
	}
	if !drop {
		return
	}
	probeRouteHTTP3Pool.mu.Lock()
	if cur, ok := probeRouteHTTP3Pool.conns[pooled.key]; ok && cur == pooled {
		delete(probeRouteHTTP3Pool.conns, pooled.key)
	}
	probeRouteHTTP3Pool.mu.Unlock()

	pooled.mu.Lock()
	pooled.retired = true
	noStreams := pooled.streams == 0
	pooled.mu.Unlock()
	if noStreams {
		_ = closeProbeRouteHTTP3PooledConn(pooled)
	}
}

func closeProbeRouteHTTP3PooledConn(pooled *probeRouteHTTP3PooledConn) error {
	if pooled == nil || pooled.quicConn == nil {
		return nil
	}
	return pooled.quicConn.CloseWithError(0, "h3 websocket pool closed")
}

func ensureProbeRouteHTTP3PoolSweeperLocked() {
	if probeRouteHTTP3Pool.sweeperOn {
		return
	}
	probeRouteHTTP3Pool.sweeperOn = true
	go runProbeRouteHTTP3PoolSweeper()
}

func runProbeRouteHTTP3PoolSweeper() {
	ticker := time.NewTicker(probeRouteHTTP3PoolSweepGap)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		var toClose []*probeRouteHTTP3PooledConn

		probeRouteHTTP3Pool.mu.Lock()
		for key, conn := range probeRouteHTTP3Pool.conns {
			if !probeRouteHTTP3PooledConnHealthy(conn) {
				delete(probeRouteHTTP3Pool.conns, key)
				toClose = append(toClose, conn)
				continue
			}
			if idle, ok := conn.idleSince(now); ok && idle >= probeRouteHTTP3PoolIdleTTL {
				delete(probeRouteHTTP3Pool.conns, key)
				conn.mu.Lock()
				conn.retired = true
				conn.mu.Unlock()
				toClose = append(toClose, conn)
			}
		}
		if len(probeRouteHTTP3Pool.conns) == 0 {
			probeRouteHTTP3Pool.sweeperOn = false
			probeRouteHTTP3Pool.mu.Unlock()
			for _, conn := range toClose {
				_ = closeProbeRouteHTTP3PooledConn(conn)
			}
			return
		}
		probeRouteHTTP3Pool.mu.Unlock()

		for _, conn := range toClose {
			_ = closeProbeRouteHTTP3PooledConn(conn)
		}
	}
}
