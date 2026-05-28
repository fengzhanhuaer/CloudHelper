package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

type probeChainRelayResolveCacheEntry struct {
	DialHost   string
	HostHeader string
	ExpiresAt  time.Time
	StaleUntil time.Time
}

type probeChainRelayProtocolQuality struct {
	Protocol      string    `json:"protocol"`
	Available     bool      `json:"available"`
	LatencyMS     int64     `json:"latency_ms,omitempty"`
	LossPermille  int       `json:"loss_permille,omitempty"`
	RateBPS       int64     `json:"rate_bps,omitempty"`
	Score         int64     `json:"score,omitempty"`
	FailureCount  int       `json:"failure_count,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	LastTestedAt  time.Time `json:"last_tested_at,omitempty"`
	NegativeUntil time.Time `json:"negative_until,omitempty"`
}

type probeChainRelayListenerStatus struct {
	Protocol  string `json:"protocol"`
	Status    string `json:"status"`
	Listen    string `json:"listen,omitempty"`
	LastError string `json:"last_error,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type probeChainRelayProtocolStateSnapshot struct {
	Endpoint          string                           `json:"endpoint"`
	SelectedProtocol  string                           `json:"selected_protocol,omitempty"`
	SelectionReason   string                           `json:"selection_reason,omitempty"`
	UpdatedAt         string                           `json:"updated_at,omitempty"`
	NextProbeAt       string                           `json:"next_probe_at,omitempty"`
	ProtocolQualities []probeChainRelayProtocolQuality `json:"protocol_qualities,omitempty"`
	ListenerStatuses  []probeChainRelayListenerStatus  `json:"listener_statuses,omitempty"`
}

type probeChainRelayReportItem struct {
	ChainID       string                                `json:"chain_id"`
	ChainName     string                                `json:"chain_name,omitempty"`
	ChainType     string                                `json:"chain_type,omitempty"`
	Role          string                                `json:"role,omitempty"`
	ListenHost    string                                `json:"listen_host,omitempty"`
	ListenPort    int                                   `json:"listen_port,omitempty"`
	LinkLayer     string                                `json:"link_layer,omitempty"`
	NextHost      string                                `json:"next_host,omitempty"`
	NextPort      int                                   `json:"next_port,omitempty"`
	NextLinkLayer string                                `json:"next_link_layer,omitempty"`
	PrevHost      string                                `json:"prev_host,omitempty"`
	PrevPort      int                                   `json:"prev_port,omitempty"`
	PrevLinkLayer string                                `json:"prev_link_layer,omitempty"`
	ListenState   *probeChainRelayProtocolStateSnapshot `json:"listen_state,omitempty"`
	NextState     *probeChainRelayProtocolStateSnapshot `json:"next_state,omitempty"`
	PrevState     *probeChainRelayProtocolStateSnapshot `json:"prev_state,omitempty"`
	UpdatedAt     string                                `json:"updated_at,omitempty"`
}

type probeChainRelayProtocolState struct {
	SelectedProtocol string
	SelectionReason  string
	SelectedAt       time.Time
	UpdatedAt        time.Time
	Qualities        map[string]probeChainRelayProtocolQuality
}

type probeChainRelayProtocolDialResult struct {
	Protocol  string
	Conn      net.Conn
	Latency   time.Duration
	Err       error
	StartedAt time.Time
	EndedAt   time.Time
}

type probeChainRelaySpeedTestResult struct {
	Protocol   string `json:"protocol"`
	OK         bool   `json:"ok"`
	LatencyMS  int64  `json:"latency_ms,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	RateBPS    int64  `json:"rate_bps,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	EndedAt    string `json:"ended_at,omitempty"`
}

type probeChainRelayNetConn struct {
	reader       io.ReadCloser
	writer       io.WriteCloser
	closeFn      func() error
	closeOnce    sync.Once
	metricsMu    sync.Mutex
	endpointKey  string
	protocol     string
	openedAt     time.Time
	bytesRead    int64
	bytesWritten int64
	ioErrors     int
}

type probeChainRelayNetAddr struct {
	label string
}

var (
	probeChainRelayResolveNow      = time.Now
	probeChainRelayLookupIP        = defaultProbeChainRelayLookupIP
	probeChainRelayResolveCacheTTL = 24 * time.Hour
	probeChainRelayResolveMaxStale = probeChainRelayResolveCacheTTL + 15*time.Minute
	probeChainRelayResolveCache    = struct {
		mu    sync.Mutex
		items map[string]probeChainRelayResolveCacheEntry
	}{items: make(map[string]probeChainRelayResolveCacheEntry)}
)

func defaultProbeChainRelayLookupIP(_ context.Context, _ string, host string) ([]net.IP, error) {
	ips, err := resolveProbeLocalDNSIPv4s(host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(ips))
	for _, rawIP := range ips {
		parsed := net.ParseIP(strings.TrimSpace(rawIP))
		if parsed == nil {
			continue
		}
		out = append(out, parsed)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("resolve relay host failed: no ip")
	}
	return out, nil
}

var probeChainRelayProtocolStateStore = struct {
	mu    sync.Mutex
	items map[string]*probeChainRelayProtocolState
}{
	items: make(map[string]*probeChainRelayProtocolState),
}

var probeChainRelayListenerStateStore = struct {
	mu    sync.Mutex
	items map[string]map[string]probeChainRelayListenerStatus
}{
	items: make(map[string]map[string]probeChainRelayListenerStatus),
}

var probeChainRelayOpenLayer = openProbeChainRelayNetConnWithLayer
var probeChainRelayMeasurePingPongLatency = measureProbeChainRelayPingPongLatency

func probeChainRelayJoinProtocols(protocols []string) string {
	cleaned := make([]string, 0, len(protocols))
	for _, protocol := range protocols {
		value := normalizeProbeChainLinkLayer(protocol)
		if value == "" {
			continue
		}
		cleaned = append(cleaned, value)
	}
	if len(cleaned) == 0 {
		return "-"
	}
	return strings.Join(cleaned, ",")
}

func logProbeChainRelayDialAttempt(stage string, chainID string, protocol string, relayHost string, relayPort int, dialHost string, hostHeader string, bridgeRole string, openTimeout time.Duration) {
	log.Printf(
		"probe chain relay dial attempt: stage=%s chain=%s protocol=%s relay=%s:%d dial_host=%s host_header=%s bridge_role=%s timeout=%s",
		strings.TrimSpace(stage),
		strings.TrimSpace(chainID),
		normalizeProbeChainLinkLayer(protocol),
		strings.TrimSpace(relayHost),
		relayPort,
		strings.TrimSpace(dialHost),
		strings.TrimSpace(hostHeader),
		normalizeProbeChainBridgeRole(bridgeRole),
		openTimeout,
	)
}

func logProbeChainRelayDialOutcome(stage string, chainID string, protocol string, relayHost string, relayPort int, dialHost string, hostHeader string, bridgeRole string, elapsed time.Duration, err error) {
	if err != nil {
		log.Printf(
			"probe chain relay dial failed: stage=%s chain=%s protocol=%s relay=%s:%d dial_host=%s host_header=%s bridge_role=%s latency_ms=%d err=%v",
			strings.TrimSpace(stage),
			strings.TrimSpace(chainID),
			normalizeProbeChainLinkLayer(protocol),
			strings.TrimSpace(relayHost),
			relayPort,
			strings.TrimSpace(dialHost),
			strings.TrimSpace(hostHeader),
			normalizeProbeChainBridgeRole(bridgeRole),
			probeDurationMilliseconds(elapsed),
			err,
		)
		return
	}
	log.Printf(
		"probe chain relay dial connected: stage=%s chain=%s protocol=%s relay=%s:%d dial_host=%s host_header=%s bridge_role=%s latency_ms=%d",
		strings.TrimSpace(stage),
		strings.TrimSpace(chainID),
		normalizeProbeChainLinkLayer(protocol),
		strings.TrimSpace(relayHost),
		relayPort,
		strings.TrimSpace(dialHost),
		strings.TrimSpace(hostHeader),
		normalizeProbeChainBridgeRole(bridgeRole),
		probeDurationMilliseconds(elapsed),
	)
}

func openProbeChainRelayNetConn(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
	return openProbeChainRelayNetConnAuto(chainID, secret, relayHost, relayPort, layer, bridgeRole)
}

func openProbeChainRelayNetConnAuto(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
	endpointKey := probeChainRelayProtocolEndpointKey(relayHost, relayPort)
	if endpointKey == "" {
		return nil, errors.New("relay endpoint is required")
	}
	candidates := probeChainRelayProtocolCandidates(layer)
	log.Printf(
		"probe chain relay auto dial start: chain=%s relay=%s layer=%s bridge_role=%s endpoint=%s candidates=%s",
		strings.TrimSpace(chainID),
		strings.TrimSpace(relayHost),
		normalizeProbeChainLinkLayer(layer),
		normalizeProbeChainBridgeRole(bridgeRole),
		endpointKey,
		probeChainRelayJoinProtocols(candidates),
	)
	for len(candidates) > 0 && isProbeChainWebSocketRelayProtocol(candidates[0]) {
		protocol := candidates[0]
		openTimeout := probeChainPortForwardDialTimeout + probeChainPortForwardResponseReadDeadline
		if protocol == "websocket-h3" {
			openTimeout = probeChainRelayProtocolProbeTimeout
		}
		result := probeChainRelayOpenLayer(chainID, secret, relayHost, relayPort, protocol, bridgeRole, openTimeout)
		if result.Err == nil {
			recordProbeChainRelayProtocolSuccess(endpointKey, result, "websocket_primary")
			recordProbeChainRelayProtocolSelected(endpointKey, protocol, "websocket_primary")
			return result.Conn, nil
		}
		log.Printf("probe chain relay auto dial websocket primary failed: chain=%s endpoint=%s protocol=%s err=%v", strings.TrimSpace(chainID), endpointKey, protocol, result.Err)
		if !isProbeChainRelayProtocolSwitchableError(result.Err) || len(candidates) == 1 {
			return nil, result.Err
		}
		recordProbeChainRelayProtocolFailure(endpointKey, result, result.Err)
		candidates = candidates[1:]
	}
	if len(candidates) == 1 && candidates[0] == "http" {
		result := probeChainRelayOpenLayer(chainID, secret, relayHost, relayPort, "http", bridgeRole, probeChainPortForwardDialTimeout+probeChainPortForwardResponseReadDeadline)
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Conn, nil
	}

	now := time.Now()
	if preferred := getProbeChainRelayProtocolPreferred(endpointKey, candidates, now); preferred != "" {
		log.Printf("probe chain relay auto dial preferred protocol: chain=%s endpoint=%s protocol=%s candidates=%s", strings.TrimSpace(chainID), endpointKey, preferred, probeChainRelayJoinProtocols(candidates))
		result := probeChainRelayOpenLayer(chainID, secret, relayHost, relayPort, preferred, bridgeRole, probeChainPortForwardDialTimeout+probeChainPortForwardResponseReadDeadline)
		if result.Err == nil {
			recordProbeChainRelayProtocolSuccess(endpointKey, result, "cached_preferred")
			return result.Conn, nil
		}
		log.Printf("probe chain relay auto dial preferred failed: chain=%s endpoint=%s protocol=%s err=%v", strings.TrimSpace(chainID), endpointKey, preferred, result.Err)
		if !isProbeChainRelayProtocolSwitchableError(result.Err) {
			return nil, result.Err
		}
		recordProbeChainRelayProtocolFailure(endpointKey, result, result.Err)
	}

	result, err := probeChainRelayProtocolProbeAndChoose(chainID, secret, relayHost, relayPort, bridgeRole, endpointKey, candidates)
	if err != nil {
		return nil, err
	}
	return result.Conn, nil
}

func probeChainRelayProtocolCandidates(layer string) []string {
	switch normalizeProbeChainLinkLayer(layer) {
	case "websocket":
		return []string{"websocket"}
	case "websocket-h3":
		return []string{"websocket-h3"}
	default:
		return []string{"websocket-h3", "websocket"}
	}
}

func isProbeChainRelaySupportedProtocol(protocol string) bool {
	switch normalizeProbeChainLinkLayer(protocol) {
	case "websocket", "websocket-h3":
		return true
	default:
		return false
	}
}

func isProbeChainWebSocketRelayProtocol(protocol string) bool {
	switch normalizeProbeChainLinkLayer(protocol) {
	case "websocket", "websocket-h3":
		return true
	default:
		return false
	}
}

func probeChainRelayProtocolEndpointKey(relayHost string, relayPort int) string {
	host := strings.ToLower(strings.TrimSpace(relayHost))
	if host == "" || relayPort <= 0 {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(relayPort))
}

func getProbeChainRelayProtocolPreferred(endpointKey string, candidates []string, now time.Time) string {
	probeChainRelayProtocolStateStore.mu.Lock()
	defer probeChainRelayProtocolStateStore.mu.Unlock()
	state := probeChainRelayProtocolStateStore.items[endpointKey]
	if state == nil {
		return ""
	}
	if state.SelectedProtocol != "" && now.Sub(state.SelectedAt) <= probeChainRelayProtocolQualityTTL {
		if probeChainRelayProtocolCandidateAllowedLocked(state, state.SelectedProtocol, candidates, now) {
			return state.SelectedProtocol
		}
	}
	best := ""
	var bestScore int64
	for _, candidate := range candidates {
		if !probeChainRelayProtocolCandidateAllowedLocked(state, candidate, candidates, now) {
			continue
		}
		quality := state.Qualities[candidate]
		if !quality.Available || quality.LastTestedAt.IsZero() || now.Sub(quality.LastTestedAt) > probeChainRelayProtocolQualityTTL {
			continue
		}
		if best == "" || quality.Score < bestScore {
			best = candidate
			bestScore = quality.Score
		}
	}
	return best
}

func probeChainRelayProtocolCandidateAllowedLocked(state *probeChainRelayProtocolState, candidate string, candidates []string, now time.Time) bool {
	if !probeChainRelayProtocolInCandidates(candidate, candidates) {
		return false
	}
	if state == nil || state.Qualities == nil {
		return true
	}
	quality := state.Qualities[candidate]
	return quality.NegativeUntil.IsZero() || !now.Before(quality.NegativeUntil)
}

func probeChainRelayProtocolInCandidates(protocol string, candidates []string) bool {
	clean := normalizeProbeChainLinkLayer(protocol)
	for _, candidate := range candidates {
		if normalizeProbeChainLinkLayer(candidate) == clean {
			return true
		}
	}
	return false
}

func probeChainRelayProtocolProbeAndChoose(chainID string, secret string, relayHost string, relayPort int, bridgeRole string, endpointKey string, candidates []string) (probeChainRelayProtocolDialResult, error) {
	now := time.Now()
	active := make([]string, 0, len(candidates))
	probeChainRelayProtocolStateStore.mu.Lock()
	state := probeChainRelayProtocolStateStore.items[endpointKey]
	for _, candidate := range candidates {
		if probeChainRelayProtocolCandidateAllowedLocked(state, candidate, candidates, now) {
			active = append(active, candidate)
		}
	}
	probeChainRelayProtocolStateStore.mu.Unlock()
	if len(active) == 0 {
		active = append(active, candidates...)
	}
	log.Printf("probe chain relay protocol probe start: chain=%s endpoint=%s bridge_role=%s candidates=%s", strings.TrimSpace(chainID), endpointKey, normalizeProbeChainBridgeRole(bridgeRole), probeChainRelayJoinProtocols(active))

	resultCh := make(chan probeChainRelayProtocolDialResult, len(active))
	for _, protocol := range active {
		candidate := protocol
		go func() {
			resultCh <- probeChainRelayOpenLayer(chainID, secret, relayHost, relayPort, candidate, bridgeRole, probeChainRelayProtocolProbeTimeout)
		}()
	}

	results := make([]probeChainRelayProtocolDialResult, 0, len(active))
	var nonSwitchableErr error
	for len(results) < len(active) {
		result := <-resultCh
		if result.Err == nil && result.Conn != nil {
			latency, pingErr := probeChainRelayMeasurePingPongLatency(result.Conn)
			if pingErr != nil {
				_ = result.Conn.Close()
				result.Conn = nil
				result.Err = pingErr
			} else {
				result.Latency = latency
			}
		}
		results = append(results, result)
		if result.Err != nil {
			log.Printf("probe chain relay protocol probe result: chain=%s endpoint=%s protocol=%s ok=false latency_ms=%d err=%v", strings.TrimSpace(chainID), endpointKey, result.Protocol, probeDurationMilliseconds(result.Latency), result.Err)
			if !isProbeChainRelayProtocolSwitchableError(result.Err) {
				nonSwitchableErr = result.Err
				continue
			}
			recordProbeChainRelayProtocolFailure(endpointKey, result, result.Err)
			continue
		}
		log.Printf("probe chain relay protocol probe result: chain=%s endpoint=%s protocol=%s ok=true latency_ms=%d", strings.TrimSpace(chainID), endpointKey, result.Protocol, probeDurationMilliseconds(result.Latency))
		recordProbeChainRelayProtocolSuccess(endpointKey, result, "auto_quality")
	}
	if nonSwitchableErr != nil {
		for _, result := range results {
			if result.Err == nil && result.Conn != nil {
				_ = result.Conn.Close()
			}
		}
		return probeChainRelayProtocolDialResult{}, nonSwitchableErr
	}

	bestIndex := -1
	var bestScore int64
	for i, result := range results {
		if result.Err != nil || result.Conn == nil {
			continue
		}
		score := probeChainRelayProtocolScore(result.Latency, 0, 0, 0)
		if bestIndex < 0 || score < bestScore {
			bestIndex = i
			bestScore = score
		}
	}
	if bestIndex >= 0 {
		for i, result := range results {
			if i != bestIndex && result.Conn != nil {
				_ = result.Conn.Close()
			}
		}
		best := results[bestIndex]
		log.Printf("probe chain relay protocol selected: chain=%s endpoint=%s protocol=%s reason=auto_quality latency_ms=%d", strings.TrimSpace(chainID), endpointKey, best.Protocol, probeDurationMilliseconds(best.Latency))
		recordProbeChainRelayProtocolSelected(endpointKey, best.Protocol, "auto_quality")
		return best, nil
	}

	errs := make([]string, 0, len(results))
	for _, result := range results {
		if result.Err != nil {
			errs = append(errs, fmt.Sprintf("%s=%v", strings.TrimSpace(result.Protocol), result.Err))
		}
	}
	if len(errs) == 0 {
		errs = append(errs, "no protocol result")
	}
	log.Printf("probe chain relay protocol probe failed: chain=%s endpoint=%s errs=%s", strings.TrimSpace(chainID), endpointKey, strings.Join(errs, "; "))
	return probeChainRelayProtocolDialResult{}, fmt.Errorf("probe relay protocol auto failed: relay=%s %s", endpointKey, strings.Join(errs, "; "))
}

func measureProbeChainRelayPingPongLatency(conn net.Conn) (time.Duration, error) {
	const payloadBytes = 64
	if conn == nil {
		return 0, errors.New("relay connection is nil")
	}
	stream, err := openProbeChainRelayPingPongStream(conn, payloadBytes)
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	payload := make([]byte, payloadBytes)
	for i := range payload {
		payload[i] = byte((i * 31) % 251)
	}
	echo := make([]byte, payloadBytes)
	startedAt := time.Now()
	_ = stream.SetDeadline(time.Now().Add(probeChainRelayProtocolProbeTimeout))
	if _, err := stream.Write(payload); err != nil {
		_ = stream.SetDeadline(time.Time{})
		return 0, err
	}
	if _, err := io.ReadFull(stream, echo); err != nil {
		_ = stream.SetDeadline(time.Time{})
		return 0, err
	}
	_ = stream.SetDeadline(time.Time{})
	if !bytes.Equal(payload, echo) {
		return 0, errors.New("ping-pong echo mismatch")
	}
	elapsed := time.Since(startedAt)
	if elapsed <= 0 {
		return time.Millisecond, nil
	}
	return elapsed, nil
}

func openProbeChainRelayPingPongStream(conn net.Conn, payloadBytes int64) (net.Conn, error) {
	if conn == nil {
		return nil, errors.New("relay connection is nil")
	}
	if quicControl, ok := conn.(*probeChainQUICDataPlaneControlNetConn); ok {
		if quicControl.session == nil {
			return nil, errors.New("quic dataplane session is nil")
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeChainRelayProtocolProbeTimeout)
		defer cancel()
		stream, err := quicControl.session.OpenStream(ctx)
		if err != nil {
			return nil, err
		}
		if err := writeProbeChainRelayPingPongRequest(stream, payloadBytes); err != nil {
			_ = stream.Close()
			return nil, err
		}
		return stream, nil
	}
	session, err := yamux.Client(conn, newProbeChainYamuxConfig())
	if err != nil {
		return nil, err
	}
	stream, err := session.Open()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if err := writeProbeChainRelayPingPongRequest(stream, payloadBytes); err != nil {
		_ = stream.Close()
		_ = session.Close()
		return nil, err
	}
	return &probeChainRelayPingPongStreamConn{Conn: stream, session: session}, nil
}

type probeChainRelayPingPongStreamConn struct {
	net.Conn
	session *yamux.Session
}

func (c *probeChainRelayPingPongStreamConn) Close() error {
	var firstErr error
	if c != nil && c.Conn != nil {
		firstErr = c.Conn.Close()
	}
	if c != nil && c.session != nil {
		if err := c.session.Close(); firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func writeProbeChainRelayPingPongRequest(stream net.Conn, payloadBytes int64) error {
	_ = stream.SetWriteDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	if err := json.NewEncoder(stream).Encode(probeChainTunnelOpenRequest{Type: probeChainRelayModePingPong, PingBytes: payloadBytes}); err != nil {
		_ = stream.SetWriteDeadline(time.Time{})
		return err
	}
	_ = stream.SetWriteDeadline(time.Time{})
	_ = stream.SetReadDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	var response probeChainTunnelOpenResponse
	if err := json.NewDecoder(stream).Decode(&response); err != nil {
		_ = stream.SetReadDeadline(time.Time{})
		return err
	}
	_ = stream.SetReadDeadline(time.Time{})
	if !response.OK {
		return errors.New(firstNonEmpty(strings.TrimSpace(response.Error), "ping-pong open failed"))
	}
	return nil
}

func isProbeChainRelayProtocolSwitchableError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return false
	}
	if strings.Contains(text, "probe relay failed: status=") ||
		strings.Contains(text, "probe relay websocket failed: status=") ||
		strings.Contains(text, "probe relay h3 websocket failed: status=") ||
		strings.Contains(text, "unauthorized") ||
		strings.Contains(text, "authentication failed") ||
		strings.Contains(text, "chain runtime not found") ||
		strings.Contains(text, "method not allowed") ||
		strings.Contains(text, "chain_id is required") {
		return false
	}
	return strings.Contains(text, "timeout") ||
		strings.Contains(text, "context canceled") ||
		strings.Contains(text, "deadline") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "connection reset") ||
		strings.Contains(text, "connection aborted") ||
		strings.Contains(text, "no route to host") ||
		strings.Contains(text, "network is unreachable") ||
		strings.Contains(text, "tls") ||
		strings.Contains(text, "handshake") ||
		strings.Contains(text, "quic") ||
		strings.Contains(text, "extended connect") ||
		strings.Contains(text, "http3 udp socket unavailable") ||
		strings.Contains(text, "eof")
}

func recordProbeChainRelayProtocolSuccess(endpointKey string, result probeChainRelayProtocolDialResult, reason string) {
	if endpointKey == "" || result.Protocol == "" {
		return
	}
	now := time.Now()
	latencyMS := int64(result.Latency / time.Millisecond)
	if latencyMS <= 0 {
		latencyMS = 1
	}
	score := probeChainRelayProtocolScore(result.Latency, 0, 0, 0)
	probeChainRelayProtocolStateStore.mu.Lock()
	defer probeChainRelayProtocolStateStore.mu.Unlock()
	state := ensureProbeChainRelayProtocolStateLocked(endpointKey)
	state.Qualities[result.Protocol] = probeChainRelayProtocolQuality{
		Protocol:     result.Protocol,
		Available:    true,
		LatencyMS:    latencyMS,
		LossPermille: 0,
		RateBPS:      0,
		Score:        score,
		LastTestedAt: now,
	}
	state.SelectedProtocol = result.Protocol
	state.SelectionReason = firstNonEmpty(strings.TrimSpace(reason), "success")
	state.SelectedAt = now
	state.UpdatedAt = now
}

func recordProbeChainRelayProtocolFailure(endpointKey string, result probeChainRelayProtocolDialResult, err error) {
	if endpointKey == "" || result.Protocol == "" {
		return
	}
	now := time.Now()
	probeChainRelayProtocolStateStore.mu.Lock()
	defer probeChainRelayProtocolStateStore.mu.Unlock()
	state := ensureProbeChainRelayProtocolStateLocked(endpointKey)
	quality := state.Qualities[result.Protocol]
	quality.Protocol = result.Protocol
	quality.Available = false
	quality.FailureCount++
	quality.LastError = strings.TrimSpace(err.Error())
	quality.LastTestedAt = now
	quality.NegativeUntil = now.Add(probeChainRelayProtocolNegativeTTL)
	quality.LossPermille = 1000
	if result.Latency > 0 {
		latencyMS := int64(result.Latency / time.Millisecond)
		if latencyMS <= 0 {
			latencyMS = 1
		}
		quality.LatencyMS = latencyMS
	}
	quality.Score = probeChainRelayProtocolScore(0, 1000, 0, quality.FailureCount)
	state.Qualities[result.Protocol] = quality
	state.UpdatedAt = now
}

func recordProbeChainRelayProtocolObservedTraffic(endpointKey string, protocol string, rateBPS int64, lossPermille int) {
	if endpointKey == "" || protocol == "" {
		return
	}
	now := time.Now()
	probeChainRelayProtocolStateStore.mu.Lock()
	defer probeChainRelayProtocolStateStore.mu.Unlock()
	state := ensureProbeChainRelayProtocolStateLocked(endpointKey)
	quality := state.Qualities[protocol]
	quality.Protocol = protocol
	if rateBPS > 0 {
		quality.RateBPS = rateBPS
	}
	if lossPermille > 0 {
		quality.LossPermille = lossPermille
	}
	if quality.LatencyMS > 0 {
		quality.Score = probeChainRelayProtocolScore(time.Duration(quality.LatencyMS)*time.Millisecond, quality.LossPermille, quality.RateBPS, quality.FailureCount)
	}
	quality.LastTestedAt = now
	state.Qualities[protocol] = quality
	state.UpdatedAt = now
}

func recordProbeChainRelayProtocolSelected(endpointKey string, protocol string, reason string) {
	if endpointKey == "" || protocol == "" {
		return
	}
	now := time.Now()
	probeChainRelayProtocolStateStore.mu.Lock()
	defer probeChainRelayProtocolStateStore.mu.Unlock()
	state := ensureProbeChainRelayProtocolStateLocked(endpointKey)
	if state.SelectedProtocol != "" && state.SelectedProtocol != protocol && now.Sub(state.SelectedAt) < probeChainRelayProtocolSwitchMinHold {
		old := state.Qualities[state.SelectedProtocol]
		next := state.Qualities[protocol]
		if old.Available && old.Score > 0 && next.Score > 0 && next.Score > old.Score/2 {
			return
		}
	}
	state.SelectedProtocol = protocol
	state.SelectionReason = firstNonEmpty(strings.TrimSpace(reason), "auto_quality")
	state.SelectedAt = now
	state.UpdatedAt = now
}

func ensureProbeChainRelayProtocolStateLocked(endpointKey string) *probeChainRelayProtocolState {
	state := probeChainRelayProtocolStateStore.items[endpointKey]
	if state == nil {
		state = &probeChainRelayProtocolState{Qualities: make(map[string]probeChainRelayProtocolQuality)}
		probeChainRelayProtocolStateStore.items[endpointKey] = state
	}
	if state.Qualities == nil {
		state.Qualities = make(map[string]probeChainRelayProtocolQuality)
	}
	return state
}

func markProbeChainRelayListenerStatus(listenAddr string, protocol string, status string, errText string) {
	cleanProtocol := normalizeProbeChainLinkLayer(protocol)
	cleanStatus := strings.TrimSpace(status)
	if cleanProtocol == "" || cleanStatus == "" {
		return
	}
	keys := probeChainRelayListenerKeys(listenAddr)
	if len(keys) == 0 {
		return
	}
	item := probeChainRelayListenerStatus{
		Protocol:  cleanProtocol,
		Status:    cleanStatus,
		Listen:    strings.TrimSpace(listenAddr),
		LastError: strings.TrimSpace(errText),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	probeChainRelayListenerStateStore.mu.Lock()
	defer probeChainRelayListenerStateStore.mu.Unlock()
	for _, key := range keys {
		protocols := probeChainRelayListenerStateStore.items[key]
		if protocols == nil {
			protocols = make(map[string]probeChainRelayListenerStatus)
			probeChainRelayListenerStateStore.items[key] = protocols
		}
		protocols[cleanProtocol] = item
	}
}

func probeChainRelayListenerKeys(listenAddr string) []string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return nil
	}
	cleanPort, err := strconv.Atoi(port)
	if err != nil || cleanPort <= 0 {
		return nil
	}
	keys := []string{probeChainRelayProtocolEndpointKey(host, cleanPort)}
	keys = append(keys, probeChainRelayProtocolEndpointKey("*", cleanPort))
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		keys = append(keys, probeChainRelayProtocolEndpointKey("127.0.0.1", cleanPort))
		keys = append(keys, probeChainRelayProtocolEndpointKey("localhost", cleanPort))
	}
	out := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func snapshotProbeChainRelayListenerStatuses(endpointKey string, relayPort int) []probeChainRelayListenerStatus {
	keys := []string{strings.TrimSpace(endpointKey)}
	if relayPort > 0 {
		keys = append(keys, probeChainRelayProtocolEndpointKey("*", relayPort))
	}
	probeChainRelayListenerStateStore.mu.Lock()
	defer probeChainRelayListenerStateStore.mu.Unlock()
	out := make([]probeChainRelayListenerStatus, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, key := range keys {
		if key == "" {
			continue
		}
		for protocol, item := range probeChainRelayListenerStateStore.items[key] {
			if !isProbeChainRelaySupportedProtocol(protocol) {
				continue
			}
			if _, exists := seen[protocol]; exists {
				continue
			}
			seen[protocol] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

func probeChainRelayProtocolScore(latency time.Duration, lossPermille int, rateBPS int64, failures int) int64 {
	score := int64(latency / time.Millisecond)
	if score <= 0 {
		score = 1
	}
	score += int64(lossPermille) * 10
	if rateBPS > 0 {
		score -= rateBPS / 1024 / 1024
	}
	score += int64(failures) * 10000
	if score <= 0 {
		return 1
	}
	return score
}

func snapshotProbeChainProtocolState(relayHost string, relayPort int) probeChainRelayProtocolStateSnapshot {
	endpointKey := probeChainRelayProtocolEndpointKey(relayHost, relayPort)
	if endpointKey == "" {
		return probeChainRelayProtocolStateSnapshot{}
	}
	probeChainRelayProtocolStateStore.mu.Lock()
	state := probeChainRelayProtocolStateStore.items[endpointKey]
	snapshot := probeChainRelayProtocolStateSnapshot{Endpoint: endpointKey}
	if state == nil {
		probeChainRelayProtocolStateStore.mu.Unlock()
		snapshot.ListenerStatuses = snapshotProbeChainRelayListenerStatuses(endpointKey, relayPort)
		return snapshot
	}
	snapshot.SelectedProtocol = strings.TrimSpace(state.SelectedProtocol)
	if !isProbeChainRelaySupportedProtocol(snapshot.SelectedProtocol) {
		snapshot.SelectedProtocol = ""
	}
	snapshot.SelectionReason = strings.TrimSpace(state.SelectionReason)
	if !state.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = state.UpdatedAt.UTC().Format(time.RFC3339)
	}
	nextProbeAt := time.Time{}
	for _, quality := range state.Qualities {
		if !isProbeChainRelaySupportedProtocol(quality.Protocol) {
			continue
		}
		snapshot.ProtocolQualities = append(snapshot.ProtocolQualities, quality)
		if !quality.NegativeUntil.IsZero() && (nextProbeAt.IsZero() || quality.NegativeUntil.Before(nextProbeAt)) {
			nextProbeAt = quality.NegativeUntil
		}
	}
	if !nextProbeAt.IsZero() {
		snapshot.NextProbeAt = nextProbeAt.UTC().Format(time.RFC3339)
	}
	probeChainRelayProtocolStateStore.mu.Unlock()
	snapshot.ListenerStatuses = snapshotProbeChainRelayListenerStatuses(endpointKey, relayPort)
	return snapshot
}

func snapshotProbeChainRelayReports() []probeChainRelayReportItem {
	probeChainRuntimeState.mu.Lock()
	configs := make([]probeChainRuntimeConfig, 0, len(probeChainRuntimeState.runtimes))
	for _, runtime := range probeChainRuntimeState.runtimes {
		if runtime == nil {
			continue
		}
		configs = append(configs, runtime.cfg)
	}
	probeChainRuntimeState.mu.Unlock()

	if len(configs) == 0 {
		return nil
	}
	sort.Slice(configs, func(i, j int) bool {
		return strings.TrimSpace(configs[i].chainID) < strings.TrimSpace(configs[j].chainID)
	})

	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]probeChainRelayReportItem, 0, len(configs))
	for _, cfg := range configs {
		item := probeChainRelayReportItem{
			ChainID:       strings.TrimSpace(cfg.chainID),
			ChainName:     strings.TrimSpace(cfg.name),
			ChainType:     strings.TrimSpace(cfg.chainType),
			Role:          normalizeProbeChainRole(cfg.role),
			ListenHost:    strings.TrimSpace(cfg.listenHost),
			ListenPort:    cfg.listenPort,
			LinkLayer:     normalizeProbeChainLinkLayer(cfg.linkLayer),
			NextHost:      strings.TrimSpace(cfg.nextHost),
			NextPort:      cfg.nextPort,
			NextLinkLayer: normalizeProbeChainLinkLayer(cfg.nextLinkLayer),
			PrevHost:      strings.TrimSpace(cfg.prevHost),
			PrevPort:      cfg.prevPort,
			PrevLinkLayer: normalizeProbeChainLinkLayer(cfg.prevLinkLayer),
			UpdatedAt:     now,
		}
		if snapshot := snapshotProbeChainProtocolState(cfg.listenHost, cfg.listenPort); probeChainRelaySnapshotHasData(snapshot) {
			item.ListenState = &snapshot
		}
		if cfg.nextPort > 0 && strings.TrimSpace(cfg.nextHost) != "" {
			if snapshot := snapshotProbeChainProtocolState(cfg.nextHost, cfg.nextPort); probeChainRelaySnapshotHasData(snapshot) {
				item.NextState = &snapshot
			}
		}
		if cfg.prevPort > 0 && strings.TrimSpace(cfg.prevHost) != "" {
			if snapshot := snapshotProbeChainProtocolState(cfg.prevHost, cfg.prevPort); probeChainRelaySnapshotHasData(snapshot) {
				item.PrevState = &snapshot
			}
		}
		out = append(out, item)
	}
	return out
}

func probeChainRelaySnapshotHasData(snapshot probeChainRelayProtocolStateSnapshot) bool {
	return strings.TrimSpace(snapshot.Endpoint) != "" ||
		strings.TrimSpace(snapshot.SelectedProtocol) != "" ||
		len(snapshot.ProtocolQualities) > 0 ||
		len(snapshot.ListenerStatuses) > 0
}

func openProbeChainRelayNetConnWithLayer(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) probeChainRelayProtocolDialResult {
	startedAt := time.Now()
	conn, err := openProbeChainRelayNetConnWithLayerConn(chainID, secret, relayHost, relayPort, layer, bridgeRole, openTimeout)
	endedAt := time.Now()
	return probeChainRelayProtocolDialResult{
		Protocol:  normalizeProbeChainLinkLayer(layer),
		Conn:      conn,
		Latency:   endedAt.Sub(startedAt),
		Err:       err,
		StartedAt: startedAt,
		EndedAt:   endedAt,
	}
}

func openProbeChainRelayNetConnWithLayerConn(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) (net.Conn, error) {
	relayDialHost, relayHostHeader, err := resolveProbeChainDialIPHost(relayHost)
	if err != nil {
		return nil, err
	}
	return openProbeChainRelayNetConnWithResolvedHost(chainID, secret, relayHost, relayPort, layer, bridgeRole, relayDialHost, relayHostHeader, openTimeout, true)
}

func openProbeChainRelayNetConnWithResolvedHost(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	relayDialHost = strings.TrimSpace(strings.Trim(relayDialHost, "[]"))
	relayHostHeader = strings.TrimSpace(strings.Trim(relayHostHeader, "[]"))
	if relayDialHost == "" {
		return nil, errors.New("relay dial host is required")
	}
	if relayHostHeader == "" {
		relayHostHeader = strings.TrimSpace(strings.Trim(relayHost, "[]"))
	}
	layer = normalizeProbeChainLinkLayer(layer)
	if layer == "websocket" {
		return openProbeChainRelayWebSocketNetConn(chainID, secret, relayHost, relayPort, bridgeRole, relayDialHost, relayHostHeader, openTimeout, cacheOnSuccess)
	}
	if layer == "websocket-h3" {
		return openProbeChainRelayHTTP3WebSocketNetConn(chainID, secret, relayHost, relayPort, bridgeRole, relayDialHost, relayHostHeader, openTimeout, cacheOnSuccess)
	}
	return nil, fmt.Errorf("unsupported relay protocol: %s", layer)
}

func openProbeChainRelayNetConnHTTPPostLegacy(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	startedAt := time.Now()
	relayDialHost = strings.TrimSpace(strings.Trim(relayDialHost, "[]"))
	relayHostHeader = strings.TrimSpace(strings.Trim(relayHostHeader, "[]"))
	if relayDialHost == "" {
		return nil, errors.New("relay dial host is required")
	}
	if relayHostHeader == "" {
		relayHostHeader = strings.TrimSpace(strings.Trim(relayHost, "[]"))
	}
	layer = normalizeProbeChainLinkLayer(layer)
	relayURL, err := buildProbeChainRelayURL(relayDialHost, relayPort, chainID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	bodyReader, bodyWriter := io.Pipe()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL, bodyReader)
	if err != nil {
		cancel()
		_ = bodyReader.Close()
		_ = bodyWriter.Close()
		return nil, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set(probeChainLegacyChainIDHeader, strings.TrimSpace(chainID))
	request.Header.Set(probeChainCodexChainIDHeader, strings.TrimSpace(chainID))
	request.Header.Set(probeChainCodexVersionHeader, probeChainAuthPacketVersion)
	if err := applyProbeChainSecretAuthHeaders(request.Header, chainID, secret); err != nil {
		cancel()
		_ = bodyReader.Close()
		_ = bodyWriter.Close()
		return nil, err
	}
	request.Header.Set(probeChainCodexRelayModeHeader, probeChainRelayModeBridge)
	request.Header.Set(probeChainCodexRelayRoleHeader, normalizeProbeChainBridgeRole(bridgeRole))
	if strings.TrimSpace(relayHostHeader) != "" {
		request.Host = strings.TrimSpace(relayHostHeader)
	}

	tlsServerName := resolveProbeChainClientTLSServerName(layer, relayDialHost, relayHostHeader)
	var closeTransport func() error
	var client *http.Client
	switch layer {
	case "http3":
		transport := &http3.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS13,
				NextProtos:         []string{"h3"},
				ServerName:         tlsServerName,
				InsecureSkipVerify: true,
			},
		}
		client = &http.Client{Transport: transport}
		closeTransport = func() error { return transport.Close() }
	case "http2":
		dialer := &net.Dialer{Timeout: probeChainPortForwardDialTimeout}
		transport := &http.Transport{
			Proxy:                 nil,
			ForceAttemptHTTP2:     true,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   probeChainPortForwardDialTimeout,
			ResponseHeaderTimeout: probeChainPortForwardResponseReadDeadline,
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				ServerName:         tlsServerName,
				InsecureSkipVerify: true,
			},
		}
		client = &http.Client{Transport: transport}
		closeTransport = func() error {
			transport.CloseIdleConnections()
			return nil
		}
	default:
		dialer := &net.Dialer{Timeout: probeChainPortForwardDialTimeout}
		transport := &http.Transport{
			Proxy:                 nil,
			ForceAttemptHTTP2:     false,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   probeChainPortForwardDialTimeout,
			ResponseHeaderTimeout: probeChainPortForwardResponseReadDeadline,
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				ServerName:         tlsServerName,
				InsecureSkipVerify: true,
			},
			TLSNextProto: make(map[string]func(string, *tls.Conn) http.RoundTripper),
		}
		client = &http.Client{Transport: transport}
		closeTransport = func() error {
			transport.CloseIdleConnections()
			return nil
		}
	}

	if openTimeout <= 0 {
		openTimeout = probeChainPortForwardDialTimeout + probeChainPortForwardResponseReadDeadline
	}
	logProbeChainRelayDialAttempt("http", chainID, layer, relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, openTimeout)
	var openTimedOut atomic.Bool
	openTimer := time.AfterFunc(openTimeout, func() {
		openTimedOut.Store(true)
		cancel()
	})
	response, err := client.Do(request)
	if err != nil {
		openTimer.Stop()
		cancel()
		_ = bodyWriter.Close()
		_ = closeTransport()
		if openTimedOut.Load() {
			timeoutErr := fmt.Errorf("probe relay open timeout: relay=%s:%d", relayDialHost, relayPort)
			logProbeChainRelayDialOutcome("http", chainID, layer, relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), timeoutErr)
			return nil, timeoutErr
		}
		wrappedErr := wrapProbeChainRelayDialError(layer, relayDialHost, relayPort, err)
		logProbeChainRelayDialOutcome("http", chainID, layer, relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	if !openTimer.Stop() {
		_ = response.Body.Close()
		cancel()
		_ = bodyWriter.Close()
		_ = closeTransport()
		timeoutErr := fmt.Errorf("probe relay open timeout: relay=%s:%d", relayDialHost, relayPort)
		logProbeChainRelayDialOutcome("http", chainID, layer, relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), timeoutErr)
		return nil, timeoutErr
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		cancel()
		_ = bodyWriter.Close()
		_ = closeTransport()
		statusErr := fmt.Errorf("probe relay failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
		logProbeChainRelayDialOutcome("http", chainID, layer, relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), statusErr)
		return nil, statusErr
	}
	if cacheOnSuccess {
		refreshProbeChainRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	}
	logProbeChainRelayDialOutcome("http", chainID, layer, relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), nil)

	return &probeChainRelayNetConn{
		reader:      response.Body,
		writer:      bodyWriter,
		endpointKey: probeChainRelayProtocolEndpointKey(relayHost, relayPort),
		protocol:    normalizeProbeChainLinkLayer(layer),
		openedAt:    time.Now(),
		closeFn: func() error {
			cancel()
			_ = bodyWriter.Close()
			_ = response.Body.Close()
			_ = closeTransport()
			return nil
		},
	}, nil
}

func openProbeChainRelayWebSocketNetConn(chainID string, secret string, relayHost string, relayPort int, bridgeRole string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	startedAt := time.Now()
	if openTimeout <= 0 {
		openTimeout = probeChainPortForwardDialTimeout + probeChainPortForwardResponseReadDeadline
	}
	relayURL, err := buildProbeChainRelayWebSocketURL(relayHostHeader, relayPort, chainID)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set(probeChainLegacyChainIDHeader, strings.TrimSpace(chainID))
	header.Set(probeChainCodexChainIDHeader, strings.TrimSpace(chainID))
	header.Set(probeChainCodexVersionHeader, probeChainAuthPacketVersion)
	if err := applyProbeChainSecretAuthHeaders(header, chainID, secret); err != nil {
		return nil, err
	}
	header.Set(probeChainCodexRelayModeHeader, probeChainRelayModeBridge)
	header.Set(probeChainCodexRelayRoleHeader, normalizeProbeChainBridgeRole(bridgeRole))

	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(relayPort))
	dialer := websocket.Dialer{
		HandshakeTimeout:  openTimeout,
		Proxy:             nil,
		ReadBufferSize:    probeChainRelayWebSocketBufferBytes,
		WriteBufferSize:   probeChainRelayWebSocketBufferBytes,
		EnableCompression: false,
		NetDialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			netDialer := net.Dialer{Timeout: probeChainPortForwardDialTimeout}
			conn, err := netDialer.DialContext(ctx, network, dialHostPort)
			if err == nil {
				tuneProbeChainNetConn(conn)
			}
			return conn, err
		},
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         resolveProbeChainClientTLSServerName("websocket", relayDialHost, relayHostHeader),
			InsecureSkipVerify: true,
		},
	}
	logProbeChainRelayDialAttempt("websocket", chainID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, openTimeout)
	ws, response, err := dialer.Dial(relayURL, header)
	if err != nil {
		if response != nil && response.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			statusErr := fmt.Errorf("probe relay websocket failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
			logProbeChainRelayDialOutcome("websocket", chainID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), statusErr)
			return nil, statusErr
		}
		wrappedErr := wrapProbeChainRelayDialError("websocket", relayDialHost, relayPort, err)
		logProbeChainRelayDialOutcome("websocket", chainID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	if cacheOnSuccess {
		refreshProbeChainRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	}
	logProbeChainRelayDialOutcome("websocket", chainID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), nil)
	return newWebSocketNetConn(ws), nil
}

func openProbeChainRelayHTTP3WebSocketNetConn(chainID string, secret string, relayHost string, relayPort int, bridgeRole string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	startedAt := time.Now()
	if openTimeout <= 0 {
		openTimeout = probeChainRelayProtocolProbeTimeout
	}
	relayURL, err := buildProbeChainRelayURL(relayHostHeader, relayPort, chainID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(relayPort))
	tlsConf := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{http3.NextProtoH3},
		ServerName:         resolveProbeChainClientTLSServerName("websocket-h3", relayDialHost, relayHostHeader),
		InsecureSkipVerify: true,
	}
	logProbeChainRelayDialAttempt("websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, openTimeout)
	quicConn, err := quic.DialAddr(ctx, dialHostPort, tlsConf, newProbeChainQUICConfig(0))
	if err != nil {
		cancel()
		wrappedErr := wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeChainRelayDialOutcome("websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
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
		cancel()
		timeoutErr := fmt.Errorf("probe relay h3 websocket open timeout: relay=%s:%d", relayDialHost, relayPort)
		logProbeChainRelayDialOutcome("websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), timeoutErr)
		return nil, timeoutErr
	case <-clientConn.Context().Done():
		cancel()
		stateErr := fmt.Errorf("probe relay h3 websocket failed: %w", context.Cause(clientConn.Context()))
		logProbeChainRelayDialOutcome("websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), stateErr)
		return nil, stateErr
	}
	if settings := clientConn.Settings(); settings == nil || !settings.EnableExtendedConnect {
		_ = quicConn.CloseWithError(0, "h3 websocket extended connect disabled")
		cancel()
		extendedErr := errors.New("probe relay h3 websocket failed: server did not enable extended connect")
		logProbeChainRelayDialOutcome("websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), extendedErr)
		return nil, extendedErr
	}

	stream, err := clientConn.OpenRequestStream(ctx)
	if err != nil {
		_ = quicConn.CloseWithError(0, "h3 websocket stream open failed")
		cancel()
		wrappedErr := wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeChainRelayDialOutcome("websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodConnect, relayURL, nil)
	if err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 websocket request build failed")
		cancel()
		return nil, err
	}
	request.Proto = "websocket"
	request.ProtoMajor = 3
	request.ProtoMinor = 0
	request.Header.Set(probeChainLegacyChainIDHeader, strings.TrimSpace(chainID))
	request.Header.Set(probeChainCodexChainIDHeader, strings.TrimSpace(chainID))
	request.Header.Set(probeChainCodexVersionHeader, probeChainAuthPacketVersion)
	if err := applyProbeChainSecretAuthHeaders(request.Header, chainID, secret); err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 websocket auth failed")
		cancel()
		return nil, err
	}
	request.Header.Set(probeChainCodexRelayModeHeader, probeChainRelayModeBridge)
	request.Header.Set(probeChainCodexRelayRoleHeader, normalizeProbeChainBridgeRole(bridgeRole))
	if strings.TrimSpace(relayHostHeader) != "" {
		request.Host = strings.TrimSpace(relayHostHeader)
	}
	if err := stream.SendRequestHeader(request); err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 websocket header send failed")
		cancel()
		wrappedErr := wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeChainRelayDialOutcome("websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	response, err := stream.ReadResponse()
	if err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 websocket response failed")
		cancel()
		wrappedErr := wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeChainRelayDialOutcome("websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		_ = quicConn.CloseWithError(0, "h3 websocket status failed")
		cancel()
		statusErr := fmt.Errorf("probe relay h3 websocket failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
		logProbeChainRelayDialOutcome("websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), statusErr)
		return nil, statusErr
	}
	if cacheOnSuccess {
		refreshProbeChainRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	}
	logProbeChainRelayDialOutcome("websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), nil)
	cancelOnce := sync.Once{}
	return &probeChainHTTP3StreamNetConn{
		stream: stream,
		local:  probeChainRelayNetAddr{label: "probe-chain-h3-websocket-local"},
		remote: probeChainRelayNetAddr{label: dialHostPort},
		closeFn: func() error {
			var closeErr error
			cancelOnce.Do(func() {
				cancel()
				stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
				stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
				closeErr = quicConn.CloseWithError(0, "h3 websocket closed")
			})
			return closeErr
		},
	}, nil
}

func probeChainRelaySpeedTestAuto(chainID string, secret string, relayHost string, relayPort int, layer string, protocol string, byteCount int64) []probeChainRelaySpeedTestResult {
	endpointKey := probeChainRelayProtocolEndpointKey(relayHost, relayPort)
	candidates := probeChainRelaySpeedTestCandidates(layer, protocol)
	if byteCount <= 0 {
		byteCount = probeChainRelaySpeedTestBytes
	}
	if byteCount > probeChainRelaySpeedTestMaxBytes {
		byteCount = probeChainRelaySpeedTestMaxBytes
	}
	results := make([]probeChainRelaySpeedTestResult, 0, len(candidates))
	for _, candidate := range candidates {
		result := probeChainRelaySpeedTestWithLayer(chainID, secret, relayHost, relayPort, candidate, byteCount, probeChainRelaySpeedTestTimeout)
		results = append(results, result)
		probeResult := probeChainRelayProtocolDialResult{
			Protocol:  normalizeProbeChainLinkLayer(candidate),
			Latency:   time.Duration(result.LatencyMS) * time.Millisecond,
			StartedAt: parseProbeChainRFC3339Time(result.StartedAt),
			EndedAt:   parseProbeChainRFC3339Time(result.EndedAt),
		}
		if result.OK {
			recordProbeChainRelayProtocolSuccess(endpointKey, probeResult, "speed_test")
			recordProbeChainRelayProtocolObservedTraffic(endpointKey, normalizeProbeChainLinkLayer(candidate), result.RateBPS, 0)
			continue
		}
		err := errors.New(firstNonEmpty(strings.TrimSpace(result.Error), "speed test failed"))
		probeResult.Err = err
		recordProbeChainRelayProtocolFailure(endpointKey, probeResult, err)
	}
	bestProtocol := ""
	var bestScore int64
	snapshot := snapshotProbeChainProtocolState(relayHost, relayPort)
	for _, quality := range snapshot.ProtocolQualities {
		if !quality.Available || quality.Score <= 0 {
			continue
		}
		if bestProtocol == "" || quality.Score < bestScore {
			bestProtocol = strings.TrimSpace(quality.Protocol)
			bestScore = quality.Score
		}
	}
	if bestProtocol != "" {
		recordProbeChainRelayProtocolSelected(endpointKey, bestProtocol, "speed_test")
	}
	return results
}

func probeChainRelaySpeedTestCandidates(layer string, protocol string) []string {
	cleanProtocol := normalizeProbeChainLinkLayer(protocol)
	switch cleanProtocol {
	case "websocket", "websocket-h3":
		return []string{cleanProtocol}
	}
	return probeChainRelayProtocolCandidates(layer)
}

func parseProbeChainRFC3339Time(raw string) time.Time {
	value, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return value
}

func probeChainRelaySpeedTestWithLayer(chainID string, secret string, relayHost string, relayPort int, layer string, byteCount int64, timeout time.Duration) probeChainRelaySpeedTestResult {
	startedAt := time.Now()
	result := probeChainRelaySpeedTestResult{
		Protocol:  normalizeProbeChainLinkLayer(layer),
		StartedAt: startedAt.UTC().Format(time.RFC3339),
	}
	log.Printf("probe chain relay speed test start: chain=%s protocol=%s relay=%s:%d bytes=%d timeout=%s", strings.TrimSpace(chainID), normalizeProbeChainLinkLayer(layer), strings.TrimSpace(relayHost), relayPort, byteCount, timeout)
	defer func() {
		if strings.TrimSpace(result.EndedAt) == "" {
			result.EndedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if result.OK {
			log.Printf("probe chain relay speed test done: chain=%s protocol=%s relay=%s:%d ok=true latency_ms=%d bytes=%d duration_ms=%d rate_bps=%d", strings.TrimSpace(chainID), normalizeProbeChainLinkLayer(layer), strings.TrimSpace(relayHost), relayPort, result.LatencyMS, result.Bytes, result.DurationMS, result.RateBPS)
			return
		}
		log.Printf("probe chain relay speed test done: chain=%s protocol=%s relay=%s:%d ok=false latency_ms=%d bytes=%d duration_ms=%d err=%s", strings.TrimSpace(chainID), normalizeProbeChainLinkLayer(layer), strings.TrimSpace(relayHost), relayPort, result.LatencyMS, result.Bytes, result.DurationMS, strings.TrimSpace(result.Error))
	}()
	relayDialHost, relayHostHeader, err := resolveProbeChainDialIPHost(relayHost)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	relayURL, err := buildProbeChainRelayURL(relayDialHost, relayPort, chainID)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if byteCount <= 0 {
		byteCount = probeChainRelaySpeedTestBytes
	}
	if byteCount > probeChainRelaySpeedTestMaxBytes {
		byteCount = probeChainRelaySpeedTestMaxBytes
	}
	if timeout <= 0 {
		timeout = probeChainRelaySpeedTestTimeout
	}
	deadlineAt := startedAt.Add(timeout)
	cleanLayer := normalizeProbeChainLinkLayer(layer)
	if cleanLayer == "websocket" || cleanLayer == "websocket-h3" {
		speedConn, speedErr := openProbeChainRelaySpeedTestConn(chainID, secret, relayHost, relayPort, cleanLayer, byteCount, timeout)
		headerAt := time.Now()
		if speedErr != nil {
			result.LatencyMS = probeDurationMilliseconds(headerAt.Sub(startedAt))
			result.Error = speedErr.Error()
			return result
		}
		defer speedConn.Close()
		_ = headerAt
		consumeProbeChainRelaySpeedTestData(speedConn, byteCount, time.Until(deadlineAt), &result)
		return result
	}
	remainingTimeout := time.Until(deadlineAt)
	if remainingTimeout <= 0 {
		result.Error = context.DeadlineExceeded.Error()
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), remainingTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL, nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set(probeChainLegacyChainIDHeader, strings.TrimSpace(chainID))
	request.Header.Set(probeChainCodexChainIDHeader, strings.TrimSpace(chainID))
	request.Header.Set(probeChainCodexVersionHeader, probeChainAuthPacketVersion)
	request.Header.Set(probeChainCodexRelayModeHeader, probeChainRelayModeSpeedTest)
	request.Header.Set(probeChainCodexSpeedBytesHeader, strconv.FormatInt(byteCount, 10))
	if err := applyProbeChainSecretAuthHeaders(request.Header, chainID, secret); err != nil {
		result.Error = err.Error()
		return result
	}
	if strings.TrimSpace(relayHostHeader) != "" {
		request.Host = strings.TrimSpace(relayHostHeader)
	}

	tlsServerName := resolveProbeChainClientTLSServerName(layer, relayDialHost, relayHostHeader)
	client, closeTransport := newProbeChainRelaySpeedTestHTTPClient(layer, tlsServerName)
	response, err := client.Do(request)
	headerAt := time.Now()
	if err != nil {
		_ = closeTransport()
		result.LatencyMS = probeDurationMilliseconds(headerAt.Sub(startedAt))
		result.Error = wrapProbeChainRelayDialError(layer, relayDialHost, relayPort, err).Error()
		return result
	}
	defer response.Body.Close()
	defer closeTransport()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		result.Error = fmt.Sprintf("speed test failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
		return result
	}
	_ = headerAt
	consumeProbeChainRelaySpeedTestData(response.Body, byteCount, time.Until(deadlineAt), &result)
	refreshProbeChainRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	return result
}

func consumeProbeChainRelaySpeedTestData(reader io.Reader, byteCount int64, maxDuration time.Duration, result *probeChainRelaySpeedTestResult) {
	if result == nil {
		return
	}
	readStartedAt := time.Now()
	if maxDuration <= 0 {
		maxDuration = probeChainRelaySpeedTestTimeout
	}
	if deadliner, ok := reader.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = deadliner.SetReadDeadline(readStartedAt.Add(maxDuration))
		defer deadliner.SetReadDeadline(time.Time{})
	}
	var first [1]byte
	firstN, firstErr := io.ReadFull(reader, first[:])
	firstAt := time.Now()
	if firstN > 0 {
		result.LatencyMS = probeDurationMilliseconds(firstAt.Sub(readStartedAt))
	}
	remaining := byteCount - int64(firstN)
	if remaining < 0 {
		remaining = 0
	}
	n, err := probeChainCopy(io.Discard, io.LimitReader(reader, remaining))
	endedAt := time.Now()
	result.EndedAt = endedAt.UTC().Format(time.RFC3339)
	result.Bytes = int64(firstN) + n
	result.DurationMS = probeDurationMilliseconds(endedAt.Sub(readStartedAt))
	if firstErr != nil {
		if isProbeChainRelaySpeedTestDurationLimitErr(firstErr, result.Bytes, readStartedAt, maxDuration) {
			finalizeProbeChainRelaySpeedTestPartialResult(result, readStartedAt, endedAt)
			return
		}
		result.Error = firstErr.Error()
		return
	}
	if err != nil {
		if isProbeChainRelaySpeedTestDurationLimitErr(err, result.Bytes, readStartedAt, maxDuration) {
			finalizeProbeChainRelaySpeedTestPartialResult(result, readStartedAt, endedAt)
			return
		}
		result.Error = err.Error()
		return
	}
	if result.Bytes <= 0 {
		result.Error = "speed test returned no data"
		return
	}
	if result.Bytes < byteCount {
		result.Error = fmt.Sprintf("speed test returned incomplete data: bytes=%d want=%d", result.Bytes, byteCount)
		return
	}
	finalizeProbeChainRelaySpeedTestPartialResult(result, readStartedAt, endedAt)
}

func finalizeProbeChainRelaySpeedTestPartialResult(result *probeChainRelaySpeedTestResult, startedAt time.Time, endedAt time.Time) {
	if result == nil {
		return
	}
	if result.Bytes <= 0 {
		result.Error = "speed test returned no data"
		return
	}
	elapsed := endedAt.Sub(startedAt)
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	result.RateBPS = int64(float64(result.Bytes) / elapsed.Seconds())
	result.OK = true
	result.Error = ""
}

func isProbeChainRelaySpeedTestDurationLimitErr(err error, bytesRead int64, startedAt time.Time, maxDuration time.Duration) bool {
	if err == nil || bytesRead <= 0 || maxDuration <= 0 {
		return false
	}
	elapsed := time.Since(startedAt)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if !strings.Contains(text, "timeout") && !strings.Contains(text, "deadline") {
		return false
	}
	return elapsed >= maxDuration || maxDuration <= time.Millisecond
}

func openProbeChainRelaySpeedTestConn(chainID string, secret string, relayHost string, relayPort int, layer string, byteCount int64, openTimeout time.Duration) (net.Conn, error) {
	relayDialHost, relayHostHeader, err := resolveProbeChainDialIPHost(relayHost)
	if err != nil {
		return nil, err
	}
	switch normalizeProbeChainLinkLayer(layer) {
	case "websocket":
		return openProbeChainRelayWebSocketSpeedTestNetConn(chainID, secret, relayHost, relayPort, relayDialHost, relayHostHeader, byteCount, openTimeout)
	case "websocket-h3":
		return openProbeChainRelayHTTP3WebSocketSpeedTestNetConn(chainID, secret, relayHost, relayPort, relayDialHost, relayHostHeader, byteCount, openTimeout)
	default:
		return nil, fmt.Errorf("unsupported speed test protocol: %s", layer)
	}
}

func openProbeChainRelayWebSocketSpeedTestNetConn(chainID string, secret string, relayHost string, relayPort int, relayDialHost string, relayHostHeader string, byteCount int64, openTimeout time.Duration) (net.Conn, error) {
	startedAt := time.Now()
	if openTimeout <= 0 {
		openTimeout = probeChainRelaySpeedTestTimeout
	}
	if byteCount <= 0 {
		byteCount = probeChainRelaySpeedTestBytes
	}
	relayURL, err := buildProbeChainRelayWebSocketURL(relayHostHeader, relayPort, chainID)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set(probeChainLegacyChainIDHeader, strings.TrimSpace(chainID))
	header.Set(probeChainCodexChainIDHeader, strings.TrimSpace(chainID))
	header.Set(probeChainCodexVersionHeader, probeChainAuthPacketVersion)
	header.Set(probeChainCodexRelayModeHeader, probeChainRelayModeSpeedTest)
	header.Set(probeChainCodexSpeedBytesHeader, strconv.FormatInt(byteCount, 10))
	if err := applyProbeChainSecretAuthHeaders(header, chainID, secret); err != nil {
		return nil, err
	}
	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(relayPort))
	dialer := websocket.Dialer{
		HandshakeTimeout:  openTimeout,
		Proxy:             nil,
		ReadBufferSize:    probeChainRelayWebSocketBufferBytes,
		WriteBufferSize:   probeChainRelayWebSocketBufferBytes,
		EnableCompression: false,
		NetDialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			netDialer := net.Dialer{Timeout: probeChainPortForwardDialTimeout}
			conn, err := netDialer.DialContext(ctx, network, dialHostPort)
			if err == nil {
				tuneProbeChainNetConn(conn)
			}
			return conn, err
		},
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         resolveProbeChainClientTLSServerName("websocket", relayDialHost, relayHostHeader),
			InsecureSkipVerify: true,
		},
	}
	logProbeChainRelayDialAttempt("speed-websocket", chainID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, "", openTimeout)
	ws, response, err := dialer.Dial(relayURL, header)
	if err != nil {
		if response != nil && response.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			statusErr := fmt.Errorf("probe relay websocket speed test failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
			logProbeChainRelayDialOutcome("speed-websocket", chainID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), statusErr)
			return nil, statusErr
		}
		wrappedErr := wrapProbeChainRelayDialError("websocket", relayDialHost, relayPort, err)
		logProbeChainRelayDialOutcome("speed-websocket", chainID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	refreshProbeChainRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	logProbeChainRelayDialOutcome("speed-websocket", chainID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), nil)
	return newWebSocketNetConn(ws), nil
}

func openProbeChainRelayHTTP3WebSocketSpeedTestNetConn(chainID string, secret string, relayHost string, relayPort int, relayDialHost string, relayHostHeader string, byteCount int64, openTimeout time.Duration) (net.Conn, error) {
	startedAt := time.Now()
	if openTimeout <= 0 {
		openTimeout = probeChainRelaySpeedTestTimeout
	}
	if byteCount <= 0 {
		byteCount = probeChainRelaySpeedTestBytes
	}
	relayURL, err := buildProbeChainRelayURL(relayHostHeader, relayPort, chainID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(relayPort))
	tlsConf := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{http3.NextProtoH3},
		ServerName:         resolveProbeChainClientTLSServerName("websocket-h3", relayDialHost, relayHostHeader),
		InsecureSkipVerify: true,
	}
	logProbeChainRelayDialAttempt("speed-websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, "", openTimeout)
	quicConn, err := quic.DialAddr(ctx, dialHostPort, tlsConf, newProbeChainQUICConfig(0))
	if err != nil {
		cancel()
		wrappedErr := wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeChainRelayDialOutcome("speed-websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	transport := &http3.Transport{}
	clientConn := transport.NewClientConn(quicConn)
	select {
	case <-clientConn.ReceivedSettings():
	case <-ctx.Done():
		_ = quicConn.CloseWithError(0, "h3 speed websocket settings timeout")
		cancel()
		timeoutErr := fmt.Errorf("probe relay h3 websocket open timeout: relay=%s:%d", relayDialHost, relayPort)
		logProbeChainRelayDialOutcome("speed-websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), timeoutErr)
		return nil, timeoutErr
	case <-clientConn.Context().Done():
		cancel()
		stateErr := fmt.Errorf("probe relay h3 websocket failed: %w", context.Cause(clientConn.Context()))
		logProbeChainRelayDialOutcome("speed-websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), stateErr)
		return nil, stateErr
	}
	if settings := clientConn.Settings(); settings == nil || !settings.EnableExtendedConnect {
		_ = quicConn.CloseWithError(0, "h3 websocket extended connect disabled")
		cancel()
		extendedErr := errors.New("probe relay h3 websocket failed: server did not enable extended connect")
		logProbeChainRelayDialOutcome("speed-websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), extendedErr)
		return nil, extendedErr
	}
	stream, err := clientConn.OpenRequestStream(ctx)
	if err != nil {
		_ = quicConn.CloseWithError(0, "h3 speed websocket stream open failed")
		cancel()
		wrappedErr := wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeChainRelayDialOutcome("speed-websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodConnect, relayURL, nil)
	if err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 speed websocket request build failed")
		cancel()
		return nil, err
	}
	request.Proto = "websocket"
	request.ProtoMajor = 3
	request.ProtoMinor = 0
	request.Header.Set(probeChainLegacyChainIDHeader, strings.TrimSpace(chainID))
	request.Header.Set(probeChainCodexChainIDHeader, strings.TrimSpace(chainID))
	request.Header.Set(probeChainCodexVersionHeader, probeChainAuthPacketVersion)
	request.Header.Set(probeChainCodexRelayModeHeader, probeChainRelayModeSpeedTest)
	request.Header.Set(probeChainCodexSpeedBytesHeader, strconv.FormatInt(byteCount, 10))
	if err := applyProbeChainSecretAuthHeaders(request.Header, chainID, secret); err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 speed websocket auth failed")
		cancel()
		return nil, err
	}
	if strings.TrimSpace(relayHostHeader) != "" {
		request.Host = strings.TrimSpace(relayHostHeader)
	}
	if err := stream.SendRequestHeader(request); err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 speed websocket header send failed")
		cancel()
		wrappedErr := wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeChainRelayDialOutcome("speed-websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	response, err := stream.ReadResponse()
	if err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 speed websocket response failed")
		cancel()
		wrappedErr := wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeChainRelayDialOutcome("speed-websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		_ = quicConn.CloseWithError(0, "h3 speed websocket status failed")
		cancel()
		statusErr := fmt.Errorf("probe relay h3 websocket failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
		logProbeChainRelayDialOutcome("speed-websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), statusErr)
		return nil, statusErr
	}
	refreshProbeChainRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	logProbeChainRelayDialOutcome("speed-websocket-h3", chainID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, "", time.Since(startedAt), nil)
	cancelOnce := sync.Once{}
	return &probeChainHTTP3StreamNetConn{
		stream: stream,
		local:  probeChainRelayNetAddr{label: "probe-chain-h3-speed-local"},
		remote: probeChainRelayNetAddr{label: dialHostPort},
		closeFn: func() error {
			var closeErr error
			cancelOnce.Do(func() {
				cancel()
				stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
				stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
				closeErr = quicConn.CloseWithError(0, "h3 speed websocket closed")
			})
			return closeErr
		},
	}, nil
}

func newProbeChainRelaySpeedTestHTTPClient(layer string, tlsServerName string) (*http.Client, func() error) {
	switch normalizeProbeChainLinkLayer(layer) {
	case "http3":
		transport := &http3.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS13,
				NextProtos:         []string{"h3"},
				ServerName:         tlsServerName,
				InsecureSkipVerify: true,
			},
		}
		return &http.Client{Transport: transport}, func() error { return transport.Close() }
	case "http2":
		dialer := &net.Dialer{Timeout: probeChainPortForwardDialTimeout}
		transport := &http.Transport{
			Proxy:                 nil,
			ForceAttemptHTTP2:     true,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   probeChainPortForwardDialTimeout,
			ResponseHeaderTimeout: probeChainPortForwardResponseReadDeadline,
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				ServerName:         tlsServerName,
				InsecureSkipVerify: true,
			},
		}
		return &http.Client{Transport: transport}, func() error {
			transport.CloseIdleConnections()
			return nil
		}
	default:
		dialer := &net.Dialer{Timeout: probeChainPortForwardDialTimeout}
		transport := &http.Transport{
			Proxy:                 nil,
			ForceAttemptHTTP2:     false,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   probeChainPortForwardDialTimeout,
			ResponseHeaderTimeout: probeChainPortForwardResponseReadDeadline,
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				ServerName:         tlsServerName,
				InsecureSkipVerify: true,
			},
			TLSNextProto: make(map[string]func(string, *tls.Conn) http.RoundTripper),
		}
		return &http.Client{Transport: transport}, func() error {
			transport.CloseIdleConnections()
			return nil
		}
	}
}

func probeDurationMilliseconds(elapsed time.Duration) int64 {
	if elapsed <= 0 {
		return 1
	}
	ms := int64(elapsed / time.Millisecond)
	if ms <= 0 {
		return 1
	}
	return ms
}

func wrapProbeChainRelayDialError(layer string, relayDialHost string, relayPort int, err error) error {
	if err == nil {
		return nil
	}
	if normalizeProbeChainLinkLayer(layer) != "http3" || !isProbeChainRelayUDPSocketResourceError(err) {
		return err
	}
	return fmt.Errorf(
		"probe relay http3 udp socket unavailable: relay=%s:%d note=each_proxy_group_uses_independent_quic_connection err=%w",
		strings.TrimSpace(relayDialHost),
		relayPort,
		err,
	)
}

func isProbeChainRelayUDPSocketResourceError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "listen udp") &&
		(strings.Contains(text, "buffer space") || strings.Contains(text, "queue was full"))
}

func (a probeChainRelayNetAddr) Network() string {
	return "probe-chain-relay"
}

func (a probeChainRelayNetAddr) String() string {
	value := strings.TrimSpace(a.label)
	if value == "" {
		return "probe-chain-relay"
	}
	return value
}

func (c *probeChainRelayNetConn) Read(payload []byte) (int, error) {
	if c == nil || c.reader == nil {
		return 0, io.EOF
	}
	n, err := c.reader.Read(payload)
	c.recordIO(n, err)
	return n, err
}

func (c *probeChainRelayNetConn) Write(payload []byte) (int, error) {
	if c == nil || c.writer == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := c.writer.Write(payload)
	c.recordWrite(n, err)
	return n, err
}

func (c *probeChainRelayNetConn) Close() error {
	if c == nil {
		return nil
	}
	var closeErr error
	c.closeOnce.Do(func() {
		c.flushMetrics()
		if c.closeFn != nil {
			closeErr = c.closeFn()
			return
		}
		if c.writer != nil {
			_ = c.writer.Close()
		}
		if c.reader != nil {
			_ = c.reader.Close()
		}
	})
	return closeErr
}

func (c *probeChainRelayNetConn) recordIO(n int, err error) {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	if n > 0 {
		c.bytesRead += int64(n)
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		c.ioErrors++
	}
	c.metricsMu.Unlock()
}

func (c *probeChainRelayNetConn) recordWrite(n int, err error) {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	if n > 0 {
		c.bytesWritten += int64(n)
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		c.ioErrors++
	}
	c.metricsMu.Unlock()
}

func (c *probeChainRelayNetConn) flushMetrics() {
	if c == nil || strings.TrimSpace(c.endpointKey) == "" || strings.TrimSpace(c.protocol) == "" {
		return
	}
	c.metricsMu.Lock()
	bytesTotal := c.bytesRead + c.bytesWritten
	ioErrors := c.ioErrors
	openedAt := c.openedAt
	c.metricsMu.Unlock()
	if openedAt.IsZero() {
		return
	}
	elapsed := time.Since(openedAt)
	if elapsed <= 0 {
		elapsed = time.Second
	}
	rateBPS := int64(0)
	if bytesTotal > 0 {
		rateBPS = int64(float64(bytesTotal) / elapsed.Seconds())
	}
	lossPermille := 0
	if ioErrors > 0 {
		lossPermille = 1000
	}
	recordProbeChainRelayProtocolObservedTraffic(c.endpointKey, c.protocol, rateBPS, lossPermille)
}

func (c *probeChainRelayNetConn) LocalAddr() net.Addr {
	return probeChainRelayNetAddr{label: "local"}
}

func (c *probeChainRelayNetConn) RemoteAddr() net.Addr {
	return probeChainRelayNetAddr{label: "remote"}
}

func (c *probeChainRelayNetConn) SetDeadline(t time.Time) error {
	_ = t
	return nil
}

func (c *probeChainRelayNetConn) SetReadDeadline(t time.Time) error {
	_ = t
	return nil
}

func (c *probeChainRelayNetConn) SetWriteDeadline(t time.Time) error {
	_ = t
	return nil
}

type probeChainHTTP3StreamNetConn struct {
	stream  probeChainHTTP3Stream
	local   net.Addr
	remote  net.Addr
	closeFn func() error
}

type probeChainHTTP3Stream interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

func (c *probeChainHTTP3StreamNetConn) Read(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, io.EOF
	}
	return c.stream.Read(payload)
}

func (c *probeChainHTTP3StreamNetConn) Write(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, io.ErrClosedPipe
	}
	return c.stream.Write(payload)
}

func (c *probeChainHTTP3StreamNetConn) Close() error {
	if c == nil {
		return nil
	}
	if c.closeFn != nil {
		return c.closeFn()
	}
	if c.stream != nil {
		return c.stream.Close()
	}
	return nil
}

func (c *probeChainHTTP3StreamNetConn) LocalAddr() net.Addr {
	if c != nil && c.local != nil {
		return c.local
	}
	return probeChainRelayNetAddr{label: "probe-chain-h3-websocket-local"}
}

func (c *probeChainHTTP3StreamNetConn) RemoteAddr() net.Addr {
	if c != nil && c.remote != nil {
		return c.remote
	}
	return probeChainRelayNetAddr{label: "probe-chain-h3-websocket-remote"}
}

func (c *probeChainHTTP3StreamNetConn) SetDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetDeadline(t)
}

func (c *probeChainHTTP3StreamNetConn) SetReadDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetReadDeadline(t)
}

func (c *probeChainHTTP3StreamNetConn) SetWriteDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetWriteDeadline(t)
}

func applyProbeChainSecretAuthHeaders(headers http.Header, chainID string, secret string) error {
	cleanChainID := strings.TrimSpace(chainID)
	cleanSecret := strings.TrimSpace(secret)
	if cleanChainID == "" {
		return errors.New("chain_id is required")
	}
	if cleanSecret == "" {
		return errors.New("link_secret is required")
	}
	nonce := randomHexToken(16)
	headers.Set("Authorization", "Bearer "+nonce)
	headers.Set(probeChainCodexAuthModeHeader, "secret_hmac")
	headers.Set(probeChainCodexMACHeader, buildProbeChainHMAC(cleanSecret, cleanChainID, nonce))
	return nil
}

func buildProbeChainRelayURL(host string, port int, chainID string) (string, error) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return "", fmt.Errorf("empty relay host")
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("invalid relay port")
	}
	u := &url.URL{
		Scheme: "https",
		Host:   net.JoinHostPort(cleanHost, strconv.Itoa(port)),
		Path:   probeChainRelayAPIPath,
	}
	query := u.Query()
	query.Set("chain_id", strings.TrimSpace(chainID))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func buildProbeChainRelayWebSocketURL(host string, port int, chainID string) (string, error) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return "", fmt.Errorf("empty relay host")
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("invalid relay port")
	}
	u := &url.URL{
		Scheme: "wss",
		Host:   net.JoinHostPort(cleanHost, strconv.Itoa(port)),
		Path:   probeChainRelayAPIPath,
	}
	query := u.Query()
	query.Set("chain_id", strings.TrimSpace(chainID))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func resolveProbeChainDialIPHost(rawHost string) (dialHost string, hostHeader string, err error) {
	cleanHost := strings.TrimSpace(strings.Trim(rawHost, "[]"))
	if cleanHost == "" {
		return "", "", fmt.Errorf("empty relay host")
	}
	if parsed := net.ParseIP(cleanHost); parsed != nil {
		return parsed.String(), cleanHost, nil
	}
	if cachedDialHost, cachedHostHeader, ok := loadProbeChainRelayResolveCache(cleanHost, false); ok {
		return cachedDialHost, cachedHostHeader, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, resolveErr := probeChainRelayLookupIP(ctx, "ip", cleanHost)
	if resolveErr != nil {
		if cachedDialHost, cachedHostHeader, ok := loadProbeChainRelayResolveCache(cleanHost, true); ok {
			return cachedDialHost, cachedHostHeader, nil
		}
		return "", "", fmt.Errorf("resolve relay host failed: %w", resolveErr)
	}
	ip := selectProbeChainPreferredDialIP(ips)
	if ip == nil {
		return "", "", fmt.Errorf("resolve relay host failed: no ip")
	}
	dialHost = ip.String()
	hostHeader = cleanHost
	return dialHost, hostHeader, nil
}

func loadProbeChainRelayResolveCache(host string, allowStale bool) (dialHost string, hostHeader string, ok bool) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return "", "", false
	}
	now := probeChainRelayResolveNow()
	probeChainRelayResolveCache.mu.Lock()
	defer probeChainRelayResolveCache.mu.Unlock()
	entry, exists := probeChainRelayResolveCache.items[cleanHost]
	if !exists {
		return "", "", false
	}
	if entry.ExpiresAt.After(now) {
		return entry.DialHost, entry.HostHeader, true
	}
	if entry.StaleUntil.After(now) {
		if allowStale {
			return entry.DialHost, entry.HostHeader, true
		}
		return "", "", false
	}
	delete(probeChainRelayResolveCache.items, cleanHost)
	return "", "", false
}

func storeProbeChainRelayResolveCache(host string, dialHost string, hostHeader string) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	cleanDialHost := strings.TrimSpace(strings.Trim(dialHost, "[]"))
	cleanHostHeader := strings.TrimSpace(strings.Trim(hostHeader, "[]"))
	if cleanHost == "" || cleanDialHost == "" {
		return
	}
	now := probeChainRelayResolveNow()
	probeChainRelayResolveCache.mu.Lock()
	probeChainRelayResolveCache.items[cleanHost] = probeChainRelayResolveCacheEntry{
		DialHost:   cleanDialHost,
		HostHeader: firstNonEmpty(cleanHostHeader, cleanHost),
		ExpiresAt:  now.Add(probeChainRelayResolveCacheTTL),
		StaleUntil: now.Add(probeChainRelayResolveMaxStale),
	}
	probeChainRelayResolveCache.mu.Unlock()
}

func refreshProbeChainRelayResolveCacheOnConnectSuccess(host string, dialHost string, hostHeader string) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return
	}
	if parsed := net.ParseIP(cleanHost); parsed != nil {
		return
	}
	storeProbeChainRelayResolveCache(cleanHost, dialHost, hostHeader)
}

func resetProbeChainRelayResolveCacheForTest() {
	probeChainRelayResolveCache.mu.Lock()
	probeChainRelayResolveCache.items = make(map[string]probeChainRelayResolveCacheEntry)
	probeChainRelayResolveCache.mu.Unlock()
}

func resolveProbeChainTLSServerName(layer string, dialHost string, hostHeader string) string {
	cleanDialHost := strings.TrimSpace(strings.Trim(dialHost, "[]"))
	cleanHostHeader := strings.TrimSpace(strings.Trim(hostHeader, "[]"))

	if normalizeProbeChainLinkLayer(layer) == "http" {
		return cleanDialHost
	}
	if cleanHostHeader != "" {
		if parsed := net.ParseIP(cleanHostHeader); parsed == nil {
			return cleanHostHeader
		}
	}
	if cleanDialHost != "" {
		return cleanDialHost
	}
	return cleanHostHeader
}

func selectProbeChainPreferredDialIP(ips []net.IP) net.IP {
	for _, candidate := range ips {
		if candidate == nil {
			continue
		}
		if v4 := candidate.To4(); v4 != nil {
			return v4
		}
	}
	for _, candidate := range ips {
		if candidate == nil {
			continue
		}
		if v6 := candidate.To16(); v6 != nil {
			return v6
		}
	}
	return nil
}

func resolveProbeChainClientTLSServerName(layer string, dialHost string, hostHeader string) string {
	return resolveProbeChainTLSServerName(layer, dialHost, hostHeader)
}
