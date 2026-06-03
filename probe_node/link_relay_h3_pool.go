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
	probeChainHTTP3PoolIdleTTL  = 60 * time.Second
	probeChainHTTP3PoolSweepGap = 20 * time.Second
)

type probeChainHTTP3PooledConn struct {
	key        string
	quicConn   probeChainHTTP3QUICConn
	clientConn *http3.ClientConn

	mu       sync.Mutex
	streams  int
	lastUsed time.Time
	retired  bool
}

// probeChainHTTP3QUICConn is the subset of *quic.Conn the pool needs (kept as an
// interface so the pool logic stays testable).
type probeChainHTTP3QUICConn interface {
	CloseWithError(code quic.ApplicationErrorCode, msg string) error
	Context() context.Context
}

func (p *probeChainHTTP3PooledConn) addStream() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.streams++
	p.lastUsed = time.Now()
	p.mu.Unlock()
}

func (p *probeChainHTTP3PooledConn) removeStream() {
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

func (p *probeChainHTTP3PooledConn) idleSince(now time.Time) (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streams > 0 {
		return 0, false
	}
	return now.Sub(p.lastUsed), true
}

var probeChainHTTP3Pool = struct {
	mu        sync.Mutex
	conns     map[string]*probeChainHTTP3PooledConn
	sweeperOn bool
}{conns: map[string]*probeChainHTTP3PooledConn{}}

func probeChainHTTP3PoolKey(relayHost string, relayPort int, relayDialHost string, relayHostHeader string) string {
	return strings.Join([]string{
		strings.TrimSpace(relayHost),
		strconv.Itoa(relayPort),
		strings.TrimSpace(relayDialHost),
		strings.TrimSpace(relayHostHeader),
	}, "|")
}

// acquireProbeChainHTTP3PooledConn returns a healthy pooled QUIC/H3 connection for
// the endpoint, creating one if necessary. reused reports whether an existing
// connection was returned (vs freshly dialed).
func acquireProbeChainHTTP3PooledConn(chainID, relayHost string, relayPort int, relayDialHost, relayHostHeader string, openTimeout time.Duration) (pooled *probeChainHTTP3PooledConn, reused bool, err error) {
	key := probeChainHTTP3PoolKey(relayHost, relayPort, relayDialHost, relayHostHeader)

	probeChainHTTP3Pool.mu.Lock()
	if existing, ok := probeChainHTTP3Pool.conns[key]; ok {
		if probeChainHTTP3PooledConnHealthy(existing) {
			existing.mu.Lock()
			existing.lastUsed = time.Now()
			existing.mu.Unlock()
			probeChainHTTP3Pool.mu.Unlock()
			return existing, true, nil
		}
		// Dead connection: drop it and fall through to redial.
		delete(probeChainHTTP3Pool.conns, key)
		go func(c *probeChainHTTP3PooledConn) { _ = closeProbeChainHTTP3PooledConn(c) }(existing)
	}
	probeChainHTTP3Pool.mu.Unlock()

	created, err := dialProbeChainHTTP3PooledConn(chainID, relayHost, relayPort, relayDialHost, relayHostHeader, openTimeout)
	if err != nil {
		return nil, false, err
	}
	created.key = key

	probeChainHTTP3Pool.mu.Lock()
	// Another goroutine may have created one concurrently; prefer the existing
	// healthy one and discard ours.
	if existing, ok := probeChainHTTP3Pool.conns[key]; ok && probeChainHTTP3PooledConnHealthy(existing) {
		probeChainHTTP3Pool.mu.Unlock()
		go func(c *probeChainHTTP3PooledConn) { _ = closeProbeChainHTTP3PooledConn(c) }(created)
		existing.mu.Lock()
		existing.lastUsed = time.Now()
		existing.mu.Unlock()
		return existing, true, nil
	}
	probeChainHTTP3Pool.conns[key] = created
	ensureProbeChainHTTP3PoolSweeperLocked()
	probeChainHTTP3Pool.mu.Unlock()
	return created, false, nil
}

func probeChainHTTP3PooledConnHealthy(p *probeChainHTTP3PooledConn) bool {
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

func dialProbeChainHTTP3PooledConn(chainID, relayHost string, relayPort int, relayDialHost, relayHostHeader string, openTimeout time.Duration) (*probeChainHTTP3PooledConn, error) {
	if openTimeout <= 0 {
		openTimeout = probeChainRelayProtocolProbeTimeout
	}
	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(relayPort))
	tlsConf := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{http3.NextProtoH3},
		ServerName:         resolveProbeChainClientTLSServerName("websocket-h3", relayDialHost, relayHostHeader),
		InsecureSkipVerify: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()

	quicConn, err := dialProbeChainBoundQUIC(ctx, dialHostPort, tlsConf, newProbeChainQUICConfig(0))
	if err != nil {
		return nil, err
	}
	transport := &http3.Transport{}
	clientConn := transport.NewClientConn(quicConn)
	select {
	case <-clientConn.ReceivedSettings():
		settings := clientConn.Settings()
		enableExtendedConnect := settings != nil && settings.EnableExtendedConnect
		log.Printf("probe chain relay h3 websocket settings: chain=%s relay=%s:%d dial_host=%s host_header=%s extended_connect=%t", strings.TrimSpace(chainID), strings.TrimSpace(relayHost), relayPort, strings.TrimSpace(relayDialHost), strings.TrimSpace(relayHostHeader), enableExtendedConnect)
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
	return &probeChainHTTP3PooledConn{
		quicConn:   quicConn,
		clientConn: clientConn,
		lastUsed:   time.Now(),
	}, nil
}

// releaseProbeChainHTTP3PooledConn is called after a stream-open failure. When
// drop is true the connection is retired and removed from the pool (it is likely
// dead); the underlying QUIC conn is only closed once no streams remain.
func releaseProbeChainHTTP3PooledConn(pooled *probeChainHTTP3PooledConn, drop bool) {
	if pooled == nil {
		return
	}
	if !drop {
		return
	}
	probeChainHTTP3Pool.mu.Lock()
	if cur, ok := probeChainHTTP3Pool.conns[pooled.key]; ok && cur == pooled {
		delete(probeChainHTTP3Pool.conns, pooled.key)
	}
	probeChainHTTP3Pool.mu.Unlock()

	pooled.mu.Lock()
	pooled.retired = true
	noStreams := pooled.streams == 0
	pooled.mu.Unlock()
	if noStreams {
		_ = closeProbeChainHTTP3PooledConn(pooled)
	}
}

func closeProbeChainHTTP3PooledConn(pooled *probeChainHTTP3PooledConn) error {
	if pooled == nil || pooled.quicConn == nil {
		return nil
	}
	return pooled.quicConn.CloseWithError(0, "h3 websocket pool closed")
}

func ensureProbeChainHTTP3PoolSweeperLocked() {
	if probeChainHTTP3Pool.sweeperOn {
		return
	}
	probeChainHTTP3Pool.sweeperOn = true
	go runProbeChainHTTP3PoolSweeper()
}

func runProbeChainHTTP3PoolSweeper() {
	ticker := time.NewTicker(probeChainHTTP3PoolSweepGap)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		var toClose []*probeChainHTTP3PooledConn

		probeChainHTTP3Pool.mu.Lock()
		for key, conn := range probeChainHTTP3Pool.conns {
			if !probeChainHTTP3PooledConnHealthy(conn) {
				delete(probeChainHTTP3Pool.conns, key)
				toClose = append(toClose, conn)
				continue
			}
			if idle, ok := conn.idleSince(now); ok && idle >= probeChainHTTP3PoolIdleTTL {
				delete(probeChainHTTP3Pool.conns, key)
				conn.mu.Lock()
				conn.retired = true
				conn.mu.Unlock()
				toClose = append(toClose, conn)
			}
		}
		if len(probeChainHTTP3Pool.conns) == 0 {
			probeChainHTTP3Pool.sweeperOn = false
			probeChainHTTP3Pool.mu.Unlock()
			for _, conn := range toClose {
				_ = closeProbeChainHTTP3PooledConn(conn)
			}
			return
		}
		probeChainHTTP3Pool.mu.Unlock()

		for _, conn := range toClose {
			_ = closeProbeChainHTTP3PooledConn(conn)
		}
	}
}
