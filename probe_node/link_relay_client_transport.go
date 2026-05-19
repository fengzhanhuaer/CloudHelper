package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
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

func openProbeChainRelayNetConn(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
	return openProbeChainRelayNetConnAuto(chainID, secret, relayHost, relayPort, layer, bridgeRole)
}

func openProbeChainRelayNetConnAuto(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
	endpointKey := probeChainRelayProtocolEndpointKey(relayHost, relayPort)
	if endpointKey == "" {
		return nil, errors.New("relay endpoint is required")
	}
	candidates := probeChainRelayProtocolCandidates(layer)
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
		result := probeChainRelayOpenLayer(chainID, secret, relayHost, relayPort, preferred, bridgeRole, probeChainPortForwardDialTimeout+probeChainPortForwardResponseReadDeadline)
		if result.Err == nil {
			recordProbeChainRelayProtocolSuccess(endpointKey, result, "cached_preferred")
			return result.Conn, nil
		}
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
	case "http":
		return []string{"websocket-h3", "websocket", "http3", "http2"}
	case "http2", "http3":
		return []string{"websocket-h3", "websocket", "http3", "http2"}
	case "websocket":
		return []string{"websocket"}
	case "websocket-h3":
		return []string{"websocket-h3"}
	default:
		return []string{"websocket-h3", "websocket", "http3", "http2"}
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
		results = append(results, result)
		if result.Err != nil {
			if !isProbeChainRelayProtocolSwitchableError(result.Err) {
				nonSwitchableErr = result.Err
				continue
			}
			recordProbeChainRelayProtocolFailure(endpointKey, result, result.Err)
			continue
		}
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
	return probeChainRelayProtocolDialResult{}, fmt.Errorf("probe relay protocol auto failed: relay=%s %s", endpointKey, strings.Join(errs, "; "))
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
	snapshot.SelectionReason = strings.TrimSpace(state.SelectionReason)
	if !state.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = state.UpdatedAt.UTC().Format(time.RFC3339)
	}
	nextProbeAt := time.Time{}
	for _, quality := range state.Qualities {
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
			return nil, fmt.Errorf("probe relay open timeout: relay=%s:%d", relayDialHost, relayPort)
		}
		return nil, wrapProbeChainRelayDialError(layer, relayDialHost, relayPort, err)
	}
	if !openTimer.Stop() {
		_ = response.Body.Close()
		cancel()
		_ = bodyWriter.Close()
		_ = closeTransport()
		return nil, fmt.Errorf("probe relay open timeout: relay=%s:%d", relayDialHost, relayPort)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		cancel()
		_ = bodyWriter.Close()
		_ = closeTransport()
		return nil, fmt.Errorf("probe relay failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if cacheOnSuccess {
		refreshProbeChainRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	}

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
		HandshakeTimeout: openTimeout,
		Proxy:            nil,
		NetDialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			netDialer := net.Dialer{Timeout: probeChainPortForwardDialTimeout}
			return netDialer.DialContext(ctx, network, dialHostPort)
		},
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         resolveProbeChainClientTLSServerName("websocket", relayDialHost, relayHostHeader),
			InsecureSkipVerify: true,
		},
	}
	ws, response, err := dialer.Dial(relayURL, header)
	if err != nil {
		if response != nil && response.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			return nil, fmt.Errorf("probe relay websocket failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, wrapProbeChainRelayDialError("websocket", relayDialHost, relayPort, err)
	}
	if cacheOnSuccess {
		refreshProbeChainRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	}
	return newWebSocketNetConn(ws), nil
}

func openProbeChainRelayHTTP3WebSocketNetConn(chainID string, secret string, relayHost string, relayPort int, bridgeRole string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
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
	quicConn, err := quic.DialAddr(ctx, dialHostPort, tlsConf, &quic.Config{KeepAlivePeriod: 10 * time.Second})
	if err != nil {
		cancel()
		return nil, wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
	}

	transport := &http3.Transport{}
	clientConn := transport.NewClientConn(quicConn)
	select {
	case <-clientConn.ReceivedSettings():
	case <-ctx.Done():
		_ = quicConn.CloseWithError(0, "h3 websocket settings timeout")
		cancel()
		return nil, fmt.Errorf("probe relay h3 websocket open timeout: relay=%s:%d", relayDialHost, relayPort)
	case <-clientConn.Context().Done():
		cancel()
		return nil, fmt.Errorf("probe relay h3 websocket failed: %w", context.Cause(clientConn.Context()))
	}
	if settings := clientConn.Settings(); settings == nil || !settings.EnableExtendedConnect {
		_ = quicConn.CloseWithError(0, "h3 websocket extended connect disabled")
		cancel()
		return nil, errors.New("probe relay h3 websocket failed: server did not enable extended connect")
	}

	stream, err := clientConn.OpenRequestStream(ctx)
	if err != nil {
		_ = quicConn.CloseWithError(0, "h3 websocket stream open failed")
		cancel()
		return nil, wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
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
		return nil, wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
	}
	response, err := stream.ReadResponse()
	if err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 websocket response failed")
		cancel()
		return nil, wrapProbeChainRelayDialError("websocket-h3", relayDialHost, relayPort, err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		_ = quicConn.CloseWithError(0, "h3 websocket status failed")
		cancel()
		return nil, fmt.Errorf("probe relay h3 websocket failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if cacheOnSuccess {
		refreshProbeChainRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	}
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
	if cleanProtocol == "http2" || cleanProtocol == "http3" {
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
	defer func() {
		if strings.TrimSpace(result.EndedAt) == "" {
			result.EndedAt = time.Now().UTC().Format(time.RFC3339)
		}
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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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
	result.LatencyMS = probeDurationMilliseconds(headerAt.Sub(startedAt))
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		result.Error = fmt.Sprintf("speed test failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
		return result
	}
	readStartedAt := time.Now()
	n, err := io.Copy(io.Discard, io.LimitReader(response.Body, byteCount))
	endedAt := time.Now()
	result.EndedAt = endedAt.UTC().Format(time.RFC3339)
	result.Bytes = n
	result.DurationMS = probeDurationMilliseconds(endedAt.Sub(readStartedAt))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if n <= 0 {
		result.Error = "speed test returned no data"
		return result
	}
	if n < byteCount {
		result.Error = fmt.Sprintf("speed test returned incomplete data: bytes=%d want=%d", n, byteCount)
		return result
	}
	elapsed := endedAt.Sub(readStartedAt)
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	result.RateBPS = int64(float64(n) / elapsed.Seconds())
	result.OK = true
	refreshProbeChainRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	return result
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
