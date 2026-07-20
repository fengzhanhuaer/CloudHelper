package main

import (
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
	"time"

	"github.com/gorilla/websocket"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// dialProbeRouteBoundQUIC 用 route-based bypass 准备好的 UDP socket 建立 QUIC 连接，
// 替代 quic.DialAddr（后者自建 socket、无法提前确保直连 host route）。
// QUIC 连接结束后异步关闭底层 UDP socket，避免 fd 泄漏。
func dialProbeRouteBoundQUIC(ctx context.Context, dialHostPort string, tlsConf *tls.Config, quicConf *quic.Config) (*quic.Conn, error) {
	remoteAddr, err := net.ResolveUDPAddr(probeRouteEgressDialNetwork("udp", dialHostPort), dialHostPort)
	if err != nil {
		return nil, err
	}
	if err := ensureProbeRouteDirectBypass(dialHostPort); err != nil {
		log.Printf("probe route relay quic direct bypass failed: target=%s err=%v", strings.TrimSpace(dialHostPort), err)
	}
	listenNetwork := "udp"
	if remoteAddr.IP != nil {
		if remoteAddr.IP.To4() != nil {
			listenNetwork = "udp4"
		} else {
			listenNetwork = "udp6"
		}
	}
	packetConn, err := probeRouteEgressListenConfig().ListenPacket(ctx, listenNetwork, ":0")
	if err != nil {
		return nil, err
	}
	quicConn, err := quic.Dial(ctx, packetConn, remoteAddr, tlsConf, quicConf)
	if err != nil {
		_ = packetConn.Close()
		return nil, err
	}
	go func() {
		<-quicConn.Context().Done()
		_ = packetConn.Close()
	}()
	return quicConn, nil
}

type probeRouteRelayResolveCacheEntry struct {
	DialHost   string
	HostHeader string
	ExpiresAt  time.Time
	StaleUntil time.Time
}

type probeRouteRelayProtocolQuality struct {
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

type probeRouteRelayListenerStatus struct {
	Protocol  string `json:"protocol"`
	Status    string `json:"status"`
	Listen    string `json:"listen,omitempty"`
	LastError string `json:"last_error,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type probeRouteRelayProtocolStateSnapshot struct {
	Endpoint          string                           `json:"endpoint"`
	SelectedProtocol  string                           `json:"selected_protocol,omitempty"`
	SelectionReason   string                           `json:"selection_reason,omitempty"`
	UpdatedAt         string                           `json:"updated_at,omitempty"`
	NextProbeAt       string                           `json:"next_probe_at,omitempty"`
	ProtocolQualities []probeRouteRelayProtocolQuality `json:"protocol_qualities,omitempty"`
	ListenerStatuses  []probeRouteRelayListenerStatus  `json:"listener_statuses,omitempty"`
}

type probeRouteRelayReportItem struct {
	RouteID        string                                `json:"route_id"`
	RouteName      string                                `json:"route_name,omitempty"`
	RouteType      string                                `json:"route_type,omitempty"`
	Role           string                                `json:"role,omitempty"`
	ListenHost     string                                `json:"listen_host,omitempty"`
	ListenPort     int                                   `json:"listen_port,omitempty"`
	RouteLayer     string                                `json:"route_layer,omitempty"`
	NextHost       string                                `json:"next_host,omitempty"`
	NextPort       int                                   `json:"next_port,omitempty"`
	NextNodeID     string                                `json:"next_node_id,omitempty"`
	NextRouteLayer string                                `json:"next_route_layer,omitempty"`
	NextDialMode   string                                `json:"next_dial_mode,omitempty"`
	PrevHost       string                                `json:"prev_host,omitempty"`
	PrevPort       int                                   `json:"prev_port,omitempty"`
	PrevNodeID     string                                `json:"prev_node_id,omitempty"`
	PrevRouteLayer string                                `json:"prev_route_layer,omitempty"`
	PrevDialMode   string                                `json:"prev_dial_mode,omitempty"`
	ListenState    *probeRouteRelayProtocolStateSnapshot `json:"listen_state,omitempty"`
	NextState      *probeRouteRelayProtocolStateSnapshot `json:"next_state,omitempty"`
	PrevState      *probeRouteRelayProtocolStateSnapshot `json:"prev_state,omitempty"`
	VirtualRouter  *probeVirtualRouterRuntimeStats       `json:"virtual_router,omitempty"`
	BridgeStatus   *probeRouteBridgeRuntimeStatus        `json:"bridge_status,omitempty"`
	BridgeSessions []probeRouteBridgeSessionSnapshot     `json:"bridge_sessions,omitempty"`
	UpdatedAt      string                                `json:"updated_at,omitempty"`
}

type probeRouteRelayProtocolState struct {
	SelectedProtocol string
	SelectionReason  string
	SelectedAt       time.Time
	UpdatedAt        time.Time
	Qualities        map[string]probeRouteRelayProtocolQuality
}

type probeRouteRelayProtocolRefreshTarget struct {
	RouteID    string
	Secret     string
	RelayHost  string
	RelayPort  int
	Layer      string
	BridgeRole string
	Endpoint   string
	Candidates []string
}

type probeRouteRelayProtocolDialResult struct {
	Protocol  string
	Conn      net.Conn
	Latency   time.Duration
	Err       error
	StartedAt time.Time
	EndedAt   time.Time
}

type probeRouteRelayNetAddr struct {
	label string
}

var (
	probeRouteRelayResolveNow      = time.Now
	probeRouteRelayLookupIP        = defaultProbeRouteRelayLookupIP
	probeRouteRelayResolveCacheTTL = 24 * time.Hour
	probeRouteRelayResolveMaxStale = probeRouteRelayResolveCacheTTL + 15*time.Minute
	probeRouteRelayResolveCache    = struct {
		mu    sync.Mutex
		items map[string]probeRouteRelayResolveCacheEntry
	}{items: make(map[string]probeRouteRelayResolveCacheEntry)}
)

func defaultProbeRouteRelayLookupIP(_ context.Context, _ string, host string) ([]net.IP, error) {
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

var probeRouteRelayProtocolStateStore = struct {
	mu    sync.Mutex
	items map[string]*probeRouteRelayProtocolState
}{
	items: make(map[string]*probeRouteRelayProtocolState),
}

var probeRouteRelayListenerStateStore = struct {
	mu    sync.Mutex
	items map[string]map[string]probeRouteRelayListenerStatus
}{
	items: make(map[string]map[string]probeRouteRelayListenerStatus),
}

var probeRouteRelayOpenLayer = openProbeRouteRelayNetConnWithLayer
var probeRouteRelayProtocolRefreshStartedForTest func(string)
var probeRouteRelayProtocolRefreshState = struct {
	mu       sync.Mutex
	inflight map[string]struct{}
}{
	inflight: make(map[string]struct{}),
}

func probeRouteRelayJoinProtocols(protocols []string) string {
	cleaned := make([]string, 0, len(protocols))
	for _, protocol := range protocols {
		value := normalizeProbeRouteRouteLayer(protocol)
		if value == "" || value == "auto" {
			continue
		}
		cleaned = append(cleaned, value)
	}
	if len(cleaned) == 0 {
		return "-"
	}
	return strings.Join(cleaned, ",")
}

func logProbeRouteRelayDialAttempt(stage string, routeID string, protocol string, relayHost string, relayPort int, dialHost string, hostHeader string, bridgeRole string, openTimeout time.Duration) {
}

func logProbeRouteRelayDialOutcome(stage string, routeID string, protocol string, relayHost string, relayPort int, dialHost string, hostHeader string, bridgeRole string, elapsed time.Duration, err error) {
	if err != nil {
		log.Printf(
			"probe route relay dial failed: stage=%s route=%s protocol=%s relay=%s:%d dial_host=%s host_header=%s bridge_role=%s latency_ms=%d err=%v",
			strings.TrimSpace(stage),
			strings.TrimSpace(routeID),
			normalizeProbeRouteRouteLayer(protocol),
			strings.TrimSpace(relayHost),
			relayPort,
			strings.TrimSpace(dialHost),
			strings.TrimSpace(hostHeader),
			normalizeProbeRouteBridgeRole(bridgeRole),
			probeDurationMilliseconds(elapsed),
			err,
		)
		return
	}
}

func openProbeRouteRelayNetConn(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
	return openProbeRouteRelayNetConnDefault(routeID, secret, relayHost, relayPort, layer, bridgeRole)
}

func openProbeRouteRelayNetConnDefault(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
	endpointKey := probeRouteRelayProtocolEndpointKey(relayHost, relayPort)
	if endpointKey == "" {
		return nil, errors.New("relay endpoint is required")
	}
	candidates := probeRouteRelayProtocolCandidates(layer)
	now := time.Now()
	if preferred := getProbeRouteRelayProtocolPreferred(endpointKey, candidates, now); preferred != "" {
		candidates = probeRouteRelayProtocolCandidatesPrefer(candidates, preferred)
	} else {
		candidates = probeRouteRelayProtocolCandidatesAllowed(endpointKey, candidates, now)
	}
	log.Printf(
		"probe route relay protocol dial start: route=%s relay=%s layer=%s bridge_role=%s endpoint=%s candidates=%s",
		strings.TrimSpace(routeID),
		strings.TrimSpace(relayHost),
		normalizeProbeRouteRouteLayer(layer),
		normalizeProbeRouteBridgeRole(bridgeRole),
		endpointKey,
		probeRouteRelayJoinProtocols(candidates),
	)
	for len(candidates) > 0 && isProbeRouteWebSocketRelayProtocol(candidates[0]) {
		protocol := candidates[0]
		openTimeout := probeRouteRelayDialTimeout + probeRouteRelayResponseReadDeadline
		if protocol == "websocket-h3" {
			openTimeout = probeRouteRelayProtocolProbeTimeout
		}
		result := probeRouteRelayOpenLayer(routeID, secret, relayHost, relayPort, protocol, bridgeRole, openTimeout)
		if result.Err == nil {
			recordProbeRouteRelayProtocolSuccess(endpointKey, result, "websocket_primary")
			recordProbeRouteRelayProtocolSelected(endpointKey, protocol, "websocket_primary")
			return result.Conn, nil
		}
		log.Printf("probe route relay protocol dial primary failed: route=%s endpoint=%s protocol=%s err=%v", strings.TrimSpace(routeID), endpointKey, protocol, result.Err)
		if !isProbeRouteRelayProtocolSwitchableError(result.Err) || len(candidates) == 1 {
			return nil, result.Err
		}
		recordProbeRouteRelayProtocolFailure(endpointKey, result, result.Err)
		candidates = candidates[1:]
	}

	result, err := probeRouteRelayProtocolProbeAndChoose(routeID, secret, relayHost, relayPort, bridgeRole, endpointKey, candidates)
	if err != nil {
		return nil, err
	}
	return result.Conn, nil
}

func openProbeVirtualRouterBridgeRelayNetConn(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
	return openProbeVirtualRouterBridgeRelayNetConnDefault(routeID, secret, relayHost, relayPort, layer, bridgeRole, true)
}

func openProbeVirtualRouterBridgeRelayNetConnWithDomainPolicy(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration, preserveDomain bool) (net.Conn, error) {
	relayDialHost, relayHostHeader, err := resolveProbeRouteDialIPHostWithPolicy(relayHost, preserveDomain)
	if err != nil {
		return nil, err
	}
	return openProbeVirtualRouterBridgeRelayNetConnWithResolvedHost(routeID, secret, relayHost, relayPort, layer, bridgeRole, relayDialHost, relayHostHeader, openTimeout, true)
}

func openProbeVirtualRouterBridgeRelayNetConnDefault(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, cacheOnSuccess bool) (net.Conn, error) {
	relayDialHost, relayHostHeader, err := resolveProbeRouteDialIPHost(relayHost)
	if err != nil {
		return nil, err
	}
	openTimeout := probeRouteRelayDialTimeout + probeRouteRelayResponseReadDeadline
	return openProbeVirtualRouterBridgeRelayNetConnWithResolvedHost(routeID, secret, relayHost, relayPort, layer, bridgeRole, relayDialHost, relayHostHeader, openTimeout, cacheOnSuccess)
}

func openProbeVirtualRouterBridgeRelayNetConnWithResolvedHost(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	endpointKey := probeRouteRelayProtocolEndpointKey(relayHost, relayPort)
	if endpointKey == "" {
		return nil, errors.New("relay endpoint is required")
	}
	candidates := probeRouteRelayProtocolCandidates(layer)
	now := time.Now()
	if preferred := getProbeRouteRelayProtocolPreferred(endpointKey, candidates, now); preferred != "" {
		candidates = probeRouteRelayProtocolCandidatesPrefer(candidates, preferred)
	} else {
		candidates = probeRouteRelayProtocolCandidatesAllowed(endpointKey, candidates, now)
	}
	log.Printf(
		"probe virtual router relay protocol dial start: route=%s relay=%s layer=%s bridge_role=%s endpoint=%s candidates=%s",
		strings.TrimSpace(routeID),
		strings.TrimSpace(relayHost),
		normalizeProbeRouteRouteLayer(layer),
		normalizeProbeRouteBridgeRole(bridgeRole),
		endpointKey,
		probeRouteRelayJoinProtocols(candidates),
	)
	var lastErr error
	for _, protocol := range candidates {
		cleanProtocol := normalizeProbeRouteRouteLayer(protocol)
		if cleanProtocol == "" {
			continue
		}
		result := probeRouteRelayProtocolDialResult{Protocol: cleanProtocol, StartedAt: time.Now()}
		conn, err := openProbeRouteRelayNetConnWithResolvedHostModeAndToken(routeID, secret, relayHost, relayPort, cleanProtocol, bridgeRole, probeRouteRelayModeBridge, "", relayDialHost, relayHostHeader, openTimeout, cacheOnSuccess)
		result.Latency = time.Since(result.StartedAt)
		if err == nil {
			result.Conn = conn
			recordProbeRouteRelayProtocolSuccess(endpointKey, result, "vrouter_direct")
			recordProbeRouteRelayProtocolSelected(endpointKey, cleanProtocol, "vrouter_direct")
			log.Printf("probe virtual router relay protocol selected: route=%s endpoint=%s protocol=%s reason=direct latency_ms=%d", strings.TrimSpace(routeID), endpointKey, cleanProtocol, probeDurationMilliseconds(result.Latency))
			return conn, nil
		}
		result.Err = err
		lastErr = err
		log.Printf("probe virtual router relay protocol dial failed: route=%s endpoint=%s protocol=%s err=%v", strings.TrimSpace(routeID), endpointKey, cleanProtocol, err)
		recordProbeRouteRelayProtocolFailure(endpointKey, result, err)
		if !isProbeRouteRelayProtocolSwitchableError(err) {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no relay protocol candidate")
	}
	return nil, lastErr
}

func probeRouteRelayProtocolCandidates(layer string) []string {
	switch normalizeProbeRouteRouteLayer(layer) {
	case "websocket":
		return []string{"websocket"}
	case "websocket-h3":
		return []string{"websocket-h3"}
	case "auto":
		return []string{"websocket-h3", "websocket"}
	default:
		return []string{"websocket-h3", "websocket"}
	}
}

func isProbeRouteRelaySupportedProtocol(protocol string) bool {
	switch normalizeProbeRouteRouteLayer(protocol) {
	case "websocket", "websocket-h3":
		return true
	default:
		return false
	}
}

func isProbeRouteWebSocketRelayProtocol(protocol string) bool {
	switch normalizeProbeRouteRouteLayer(protocol) {
	case "websocket", "websocket-h3":
		return true
	default:
		return false
	}
}

func probeRouteRelayProtocolEndpointKey(relayHost string, relayPort int) string {
	host := strings.ToLower(strings.TrimSpace(relayHost))
	if host == "" || relayPort <= 0 {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(relayPort))
}

func getProbeRouteRelayProtocolPreferred(endpointKey string, candidates []string, now time.Time) string {
	probeRouteRelayProtocolStateStore.mu.Lock()
	defer probeRouteRelayProtocolStateStore.mu.Unlock()
	state := probeRouteRelayProtocolStateStore.items[endpointKey]
	if state == nil {
		return ""
	}
	if state.SelectedProtocol != "" && now.Sub(state.SelectedAt) <= probeRouteRelayProtocolQualityTTL {
		if probeRouteRelayProtocolCandidateAllowedLocked(state, state.SelectedProtocol, candidates, now) {
			return state.SelectedProtocol
		}
	}
	best := ""
	var bestScore int64
	for _, candidate := range candidates {
		if !probeRouteRelayProtocolCandidateAllowedLocked(state, candidate, candidates, now) {
			continue
		}
		quality := state.Qualities[candidate]
		if !quality.Available || quality.LastTestedAt.IsZero() || now.Sub(quality.LastTestedAt) > probeRouteRelayProtocolQualityTTL {
			continue
		}
		if best == "" || quality.Score < bestScore {
			best = candidate
			bestScore = quality.Score
		}
	}
	return best
}

func probeRouteRelayProtocolCandidateAllowedLocked(state *probeRouteRelayProtocolState, candidate string, candidates []string, now time.Time) bool {
	if !probeRouteRelayProtocolInCandidates(candidate, candidates) {
		return false
	}
	if state == nil || state.Qualities == nil {
		return true
	}
	quality := state.Qualities[candidate]
	return quality.NegativeUntil.IsZero() || !now.Before(quality.NegativeUntil)
}

func probeRouteRelayProtocolCandidatesAllowed(endpointKey string, candidates []string, now time.Time) []string {
	probeRouteRelayProtocolStateStore.mu.Lock()
	state := probeRouteRelayProtocolStateStore.items[endpointKey]
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		clean := normalizeProbeRouteRouteLayer(candidate)
		if clean == "" {
			continue
		}
		if probeRouteRelayProtocolCandidateAllowedLocked(state, clean, candidates, now) {
			out = append(out, clean)
		}
	}
	probeRouteRelayProtocolStateStore.mu.Unlock()
	if len(out) == 0 {
		return candidates
	}
	return out
}

func probeRouteRelayProtocolInCandidates(protocol string, candidates []string) bool {
	clean := normalizeProbeRouteRouteLayer(protocol)
	for _, candidate := range candidates {
		if normalizeProbeRouteRouteLayer(candidate) == clean {
			return true
		}
	}
	return false
}

func probeRouteRelayProtocolProbeAndChoose(routeID string, secret string, relayHost string, relayPort int, bridgeRole string, endpointKey string, candidates []string) (probeRouteRelayProtocolDialResult, error) {
	now := time.Now()
	active := make([]string, 0, len(candidates))
	probeRouteRelayProtocolStateStore.mu.Lock()
	state := probeRouteRelayProtocolStateStore.items[endpointKey]
	for _, candidate := range candidates {
		if probeRouteRelayProtocolCandidateAllowedLocked(state, candidate, candidates, now) {
			active = append(active, candidate)
		}
	}
	probeRouteRelayProtocolStateStore.mu.Unlock()
	if len(active) == 0 {
		active = append(active, candidates...)
	}
	log.Printf("probe route relay protocol probe start: route=%s endpoint=%s bridge_role=%s candidates=%s", strings.TrimSpace(routeID), endpointKey, normalizeProbeRouteBridgeRole(bridgeRole), probeRouteRelayJoinProtocols(active))

	resultCh := make(chan probeRouteRelayProtocolDialResult, len(active))
	for _, protocol := range active {
		candidate := protocol
		go func() {
			resultCh <- probeRouteRelayOpenLayer(routeID, secret, relayHost, relayPort, candidate, bridgeRole, probeRouteRelayProtocolProbeTimeout)
		}()
	}

	results := make([]probeRouteRelayProtocolDialResult, 0, len(active))
	var nonSwitchableErr error
	for len(results) < len(active) {
		result := <-resultCh
		results = append(results, result)
		if result.Err != nil {
			log.Printf("probe route relay protocol probe result: route=%s endpoint=%s protocol=%s ok=false latency_ms=%d err=%v", strings.TrimSpace(routeID), endpointKey, result.Protocol, probeDurationMilliseconds(result.Latency), result.Err)
			if !isProbeRouteRelayProtocolSwitchableError(result.Err) {
				nonSwitchableErr = result.Err
				continue
			}
			recordProbeRouteRelayProtocolFailure(endpointKey, result, result.Err)
			continue
		}
		log.Printf("probe route relay protocol probe result: route=%s endpoint=%s protocol=%s ok=true latency_ms=%d", strings.TrimSpace(routeID), endpointKey, result.Protocol, probeDurationMilliseconds(result.Latency))
		recordProbeRouteRelayProtocolSuccess(endpointKey, result, "quality")
	}
	if nonSwitchableErr != nil {
		for _, result := range results {
			if result.Err == nil && result.Conn != nil {
				_ = result.Conn.Close()
			}
		}
		return probeRouteRelayProtocolDialResult{}, nonSwitchableErr
	}

	bestIndex := -1
	var bestScore int64
	for i, result := range results {
		if result.Err != nil || result.Conn == nil {
			continue
		}
		score := probeRouteRelayProtocolScore(result.Latency, 0, 0, 0)
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
		log.Printf("probe route relay protocol selected: route=%s endpoint=%s protocol=%s reason=quality latency_ms=%d", strings.TrimSpace(routeID), endpointKey, best.Protocol, probeDurationMilliseconds(best.Latency))
		recordProbeRouteRelayProtocolSelected(endpointKey, best.Protocol, "quality")
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
	log.Printf("probe route relay protocol probe failed: route=%s endpoint=%s errs=%s", strings.TrimSpace(routeID), endpointKey, strings.Join(errs, "; "))
	return probeRouteRelayProtocolDialResult{}, fmt.Errorf("probe relay protocol selection failed: relay=%s %s", endpointKey, strings.Join(errs, "; "))
}

func probeRouteRelayProtocolRefreshNeeded(endpointKey string, candidates []string, now time.Time) bool {
	if endpointKey == "" || len(candidates) == 0 {
		return false
	}
	probeRouteRelayProtocolStateStore.mu.Lock()
	defer probeRouteRelayProtocolStateStore.mu.Unlock()
	state := probeRouteRelayProtocolStateStore.items[endpointKey]
	if state == nil {
		return true
	}
	for _, candidate := range candidates {
		clean := normalizeProbeRouteRouteLayer(candidate)
		if clean == "" {
			continue
		}
		quality := state.Qualities[clean]
		if quality.LastTestedAt.IsZero() {
			return true
		}
		if !quality.Available {
			if quality.NegativeUntil.IsZero() || !now.Before(quality.NegativeUntil) {
				return true
			}
			continue
		}
		if now.Sub(quality.LastTestedAt) > probeRouteRelayProtocolQualityTTL {
			return true
		}
	}
	return false
}

func scheduleProbeRouteRelayProtocolRefreshTarget(target probeRouteRelayProtocolRefreshTarget) {
	endpointKey := strings.TrimSpace(target.Endpoint)
	if endpointKey == "" || strings.TrimSpace(target.RelayHost) == "" || target.RelayPort <= 0 || len(target.Candidates) == 0 {
		return
	}
	probeRouteRelayProtocolRefreshState.mu.Lock()
	if _, ok := probeRouteRelayProtocolRefreshState.inflight[endpointKey]; ok {
		probeRouteRelayProtocolRefreshState.mu.Unlock()
		return
	}
	probeRouteRelayProtocolRefreshState.inflight[endpointKey] = struct{}{}
	probeRouteRelayProtocolRefreshState.mu.Unlock()

	go func() {
		if hook := probeRouteRelayProtocolRefreshStartedForTest; hook != nil {
			hook(endpointKey)
		}
		defer func() {
			probeRouteRelayProtocolRefreshState.mu.Lock()
			delete(probeRouteRelayProtocolRefreshState.inflight, endpointKey)
			probeRouteRelayProtocolRefreshState.mu.Unlock()
		}()
		result, err := probeRouteRelayProtocolProbeAndChoose(target.RouteID, target.Secret, target.RelayHost, target.RelayPort, target.BridgeRole, endpointKey, target.Candidates)
		if result.Conn != nil {
			_ = result.Conn.Close()
		}
		if err != nil {
			log.Printf("probe route relay protocol report refresh failed: route=%s endpoint=%s bridge_role=%s candidates=%s err=%v", strings.TrimSpace(target.RouteID), endpointKey, normalizeProbeRouteBridgeRole(target.BridgeRole), probeRouteRelayJoinProtocols(target.Candidates), err)
			return
		}
		log.Printf("probe route relay protocol report refresh done: route=%s endpoint=%s protocol=%s latency_ms=%d", strings.TrimSpace(target.RouteID), endpointKey, normalizeProbeRouteRouteLayer(result.Protocol), probeDurationMilliseconds(result.Latency))
	}()
}

func isProbeRouteRelayProtocolSwitchableError(err error) bool {
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
		strings.Contains(text, "route runtime not found") ||
		strings.Contains(text, "method not allowed") ||
		strings.Contains(text, "route_id is required") {
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
		strings.Contains(text, "websocket-h3 udp socket unavailable") ||
		strings.Contains(text, "http3 udp socket unavailable") ||
		strings.Contains(text, "eof")
}

func recordProbeRouteRelayProtocolSuccess(endpointKey string, result probeRouteRelayProtocolDialResult, reason string) {
	if endpointKey == "" || result.Protocol == "" {
		return
	}
	now := time.Now()
	latencyMS := int64(result.Latency / time.Millisecond)
	if latencyMS <= 0 {
		latencyMS = 1
	}
	score := probeRouteRelayProtocolScore(result.Latency, 0, 0, 0)
	probeRouteRelayProtocolStateStore.mu.Lock()
	defer probeRouteRelayProtocolStateStore.mu.Unlock()
	state := ensureProbeRouteRelayProtocolStateLocked(endpointKey)
	state.Qualities[result.Protocol] = probeRouteRelayProtocolQuality{
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

func recordProbeRouteRelayProtocolFailure(endpointKey string, result probeRouteRelayProtocolDialResult, err error) {
	if endpointKey == "" || result.Protocol == "" {
		return
	}
	now := time.Now()
	probeRouteRelayProtocolStateStore.mu.Lock()
	defer probeRouteRelayProtocolStateStore.mu.Unlock()
	state := ensureProbeRouteRelayProtocolStateLocked(endpointKey)
	quality := state.Qualities[result.Protocol]
	quality.Protocol = result.Protocol
	quality.Available = false
	quality.FailureCount++
	quality.LastError = strings.TrimSpace(err.Error())
	quality.LastTestedAt = now
	quality.NegativeUntil = now.Add(probeRouteRelayProtocolNegativeTTL)
	quality.LossPermille = 1000
	if result.Latency > 0 {
		latencyMS := int64(result.Latency / time.Millisecond)
		if latencyMS <= 0 {
			latencyMS = 1
		}
		quality.LatencyMS = latencyMS
	}
	quality.Score = probeRouteRelayProtocolScore(0, 1000, 0, quality.FailureCount)
	state.Qualities[result.Protocol] = quality
	state.UpdatedAt = now
}

func recordProbeRouteRelayProtocolObservedTraffic(endpointKey string, protocol string, rateBPS int64, lossPermille int) {
	if endpointKey == "" || protocol == "" {
		return
	}
	now := time.Now()
	probeRouteRelayProtocolStateStore.mu.Lock()
	defer probeRouteRelayProtocolStateStore.mu.Unlock()
	state := ensureProbeRouteRelayProtocolStateLocked(endpointKey)
	quality := state.Qualities[protocol]
	quality.Protocol = protocol
	if rateBPS > 0 {
		quality.RateBPS = rateBPS
	}
	if lossPermille > 0 {
		quality.LossPermille = lossPermille
	}
	if quality.LatencyMS > 0 {
		quality.Score = probeRouteRelayProtocolScore(time.Duration(quality.LatencyMS)*time.Millisecond, quality.LossPermille, quality.RateBPS, quality.FailureCount)
	}
	quality.LastTestedAt = now
	state.Qualities[protocol] = quality
	state.UpdatedAt = now
}

func recordProbeRouteRelayProtocolSelected(endpointKey string, protocol string, reason string) {
	if endpointKey == "" || protocol == "" {
		return
	}
	now := time.Now()
	probeRouteRelayProtocolStateStore.mu.Lock()
	defer probeRouteRelayProtocolStateStore.mu.Unlock()
	state := ensureProbeRouteRelayProtocolStateLocked(endpointKey)
	if state.SelectedProtocol != "" && state.SelectedProtocol != protocol && now.Sub(state.SelectedAt) < probeRouteRelayProtocolSwitchMinHold {
		old := state.Qualities[state.SelectedProtocol]
		next := state.Qualities[protocol]
		if old.Available && old.Score > 0 && next.Score > 0 && next.Score > old.Score/2 {
			return
		}
	}
	state.SelectedProtocol = protocol
	state.SelectionReason = firstNonEmpty(strings.TrimSpace(reason), "quality")
	state.SelectedAt = now
	state.UpdatedAt = now
}

func ensureProbeRouteRelayProtocolStateLocked(endpointKey string) *probeRouteRelayProtocolState {
	state := probeRouteRelayProtocolStateStore.items[endpointKey]
	if state == nil {
		state = &probeRouteRelayProtocolState{Qualities: make(map[string]probeRouteRelayProtocolQuality)}
		probeRouteRelayProtocolStateStore.items[endpointKey] = state
	}
	if state.Qualities == nil {
		state.Qualities = make(map[string]probeRouteRelayProtocolQuality)
	}
	return state
}

func markProbeRouteRelayListenerStatus(listenAddr string, protocol string, status string, errText string) {
	cleanProtocol := normalizeProbeRouteRouteLayer(protocol)
	cleanStatus := strings.TrimSpace(status)
	if cleanProtocol == "" || cleanStatus == "" {
		return
	}
	keys := probeRouteRelayListenerKeys(listenAddr)
	if len(keys) == 0 {
		return
	}
	item := probeRouteRelayListenerStatus{
		Protocol:  cleanProtocol,
		Status:    cleanStatus,
		Listen:    strings.TrimSpace(listenAddr),
		LastError: strings.TrimSpace(errText),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	probeRouteRelayListenerStateStore.mu.Lock()
	defer probeRouteRelayListenerStateStore.mu.Unlock()
	for _, key := range keys {
		protocols := probeRouteRelayListenerStateStore.items[key]
		if protocols == nil {
			protocols = make(map[string]probeRouteRelayListenerStatus)
			probeRouteRelayListenerStateStore.items[key] = protocols
		}
		protocols[cleanProtocol] = item
	}
}

func probeRouteRelayListenerKeys(listenAddr string) []string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return nil
	}
	cleanPort, err := strconv.Atoi(port)
	if err != nil || cleanPort <= 0 {
		return nil
	}
	keys := []string{probeRouteRelayProtocolEndpointKey(host, cleanPort)}
	keys = append(keys, probeRouteRelayProtocolEndpointKey("*", cleanPort))
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		keys = append(keys, probeRouteRelayProtocolEndpointKey("127.0.0.1", cleanPort))
		keys = append(keys, probeRouteRelayProtocolEndpointKey("localhost", cleanPort))
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

func snapshotProbeRouteRelayListenerStatuses(endpointKey string, relayPort int) []probeRouteRelayListenerStatus {
	keys := []string{strings.TrimSpace(endpointKey)}
	if relayPort > 0 {
		keys = append(keys, probeRouteRelayProtocolEndpointKey("*", relayPort))
	}
	probeRouteRelayListenerStateStore.mu.Lock()
	defer probeRouteRelayListenerStateStore.mu.Unlock()
	out := make([]probeRouteRelayListenerStatus, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, key := range keys {
		if key == "" {
			continue
		}
		for protocol, item := range probeRouteRelayListenerStateStore.items[key] {
			if !isProbeRouteRelaySupportedProtocol(protocol) {
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

func probeRouteRelayProtocolScore(latency time.Duration, lossPermille int, rateBPS int64, failures int) int64 {
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

func snapshotProbeRouteProtocolState(relayHost string, relayPort int) probeRouteRelayProtocolStateSnapshot {
	endpointKey := probeRouteRelayProtocolEndpointKey(relayHost, relayPort)
	if endpointKey == "" {
		return probeRouteRelayProtocolStateSnapshot{}
	}
	probeRouteRelayProtocolStateStore.mu.Lock()
	state := probeRouteRelayProtocolStateStore.items[endpointKey]
	snapshot := probeRouteRelayProtocolStateSnapshot{Endpoint: endpointKey}
	if state == nil {
		probeRouteRelayProtocolStateStore.mu.Unlock()
		snapshot.ListenerStatuses = snapshotProbeRouteRelayListenerStatuses(endpointKey, relayPort)
		return snapshot
	}
	snapshot.SelectedProtocol = strings.TrimSpace(state.SelectedProtocol)
	if !isProbeRouteRelaySupportedProtocol(snapshot.SelectedProtocol) {
		snapshot.SelectedProtocol = ""
	}
	snapshot.SelectionReason = strings.TrimSpace(state.SelectionReason)
	if !state.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = state.UpdatedAt.UTC().Format(time.RFC3339)
	}
	nextProbeAt := time.Time{}
	for _, quality := range state.Qualities {
		if !isProbeRouteRelaySupportedProtocol(quality.Protocol) {
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
	probeRouteRelayProtocolStateStore.mu.Unlock()
	snapshot.ListenerStatuses = snapshotProbeRouteRelayListenerStatuses(endpointKey, relayPort)
	return snapshot
}

func snapshotProbeRouteRelayReports() []probeRouteRelayReportItem {
	now := time.Now().UTC().Format(time.RFC3339)
	return snapshotProbeVirtualRouterRelayReports(now)
}

func snapshotProbeVirtualRouterRelayReports(now string) []probeRouteRelayReportItem {
	probeVirtualRouterRuntimeState.mu.RLock()
	runtimes := make([]*probeVirtualRouterRuntime, 0, len(probeVirtualRouterRuntimeState.runtimes))
	for _, rt := range probeVirtualRouterRuntimeState.runtimes {
		if rt != nil {
			runtimes = append(runtimes, rt)
		}
	}
	probeVirtualRouterRuntimeState.mu.RUnlock()
	sort.Slice(runtimes, func(i, j int) bool {
		return strings.TrimSpace(runtimes[i].cfg.routeID) < strings.TrimSpace(runtimes[j].cfg.routeID)
	})
	out := make([]probeRouteRelayReportItem, 0, len(runtimes))
	for _, rt := range runtimes {
		cfg := rt.cfg
		nextHost := ""
		nextPort := 0
		nextNodeID := ""
		nextDialMode := probeRouteDialModeNone
		prevNodeID := ""
		prevDialMode := probeRouteDialModeNone
		if cfg.dialer {
			nextHost = strings.TrimSpace(cfg.peerHost)
			nextPort = cfg.peerPort
			nextNodeID = normalizeProbeRouteNodeID(cfg.peerNodeID)
			if nextHost != "" && nextPort > 0 {
				nextDialMode = probeRouteDialModeForward
			}
		} else {
			prevNodeID = normalizeProbeRouteNodeID(cfg.peerNodeID)
		}
		item := probeRouteRelayReportItem{
			RouteID:      strings.TrimSpace(cfg.routeID),
			RouteName:    strings.TrimSpace(cfg.name),
			RouteType:    probeVirtualRouterRuntimeRole,
			Role:         probeVirtualRouterRuntimeRole,
			ListenHost:   strings.TrimSpace(cfg.listenHost),
			ListenPort:   cfg.listenPort,
			RouteLayer:   normalizeProbeRouteRouteLayer(cfg.routeLayer),
			NextHost:     nextHost,
			NextPort:     nextPort,
			NextNodeID:   nextNodeID,
			NextDialMode: nextDialMode,
			PrevNodeID:   prevNodeID,
			PrevDialMode: prevDialMode,
			UpdatedAt:    now,
		}
		if snapshot := snapshotProbeRouteProtocolState(cfg.listenHost, cfg.listenPort); probeRouteRelaySnapshotHasData(snapshot) {
			item.ListenState = &snapshot
		}
		if nextPort > 0 && nextHost != "" {
			if snapshot := snapshotProbeRouteProtocolState(nextHost, nextPort); probeRouteRelaySnapshotHasData(snapshot) {
				item.NextState = &snapshot
			}
		}
		if stats := snapshotProbeVirtualRouterRuntimeStats(cfg.routeID); stats != nil {
			item.VirtualRouter = stats
		}
		_, bridgeStatus, _ := snapshotProbeVirtualRouterPingContext(rt, "")
		item.BridgeStatus = &bridgeStatus
		item.BridgeSessions = bridgeStatus.Sessions
		out = append(out, item)
	}
	return out
}

func probeRouteRelaySnapshotHasData(snapshot probeRouteRelayProtocolStateSnapshot) bool {
	return strings.TrimSpace(snapshot.Endpoint) != "" ||
		strings.TrimSpace(snapshot.SelectedProtocol) != "" ||
		len(snapshot.ProtocolQualities) > 0 ||
		len(snapshot.ListenerStatuses) > 0
}

func openProbeRouteRelayNetConnWithLayer(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) probeRouteRelayProtocolDialResult {
	startedAt := time.Now()
	conn, err := openProbeRouteRelayNetConnWithLayerConn(routeID, secret, relayHost, relayPort, layer, bridgeRole, openTimeout)
	endedAt := time.Now()
	return probeRouteRelayProtocolDialResult{
		Protocol:  normalizeProbeRouteRouteLayer(layer),
		Conn:      conn,
		Latency:   endedAt.Sub(startedAt),
		Err:       err,
		StartedAt: startedAt,
		EndedAt:   endedAt,
	}
}

func openProbeRouteRelayNetConnWithLayerConn(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) (net.Conn, error) {
	relayDialHost, relayHostHeader, err := resolveProbeRouteDialIPHost(relayHost)
	if err != nil {
		return nil, err
	}
	return openProbeRouteRelayNetConnWithResolvedHostAndMode(routeID, secret, relayHost, relayPort, layer, bridgeRole, probeRouteRelayModeBridge, relayDialHost, relayHostHeader, openTimeout, true)
}

func openProbeRouteRelayNetConnWithLayerConnAndDomainPolicy(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration, preserveDomain bool) (net.Conn, error) {
	relayDialHost, relayHostHeader, err := resolveProbeRouteDialIPHostWithPolicy(relayHost, preserveDomain)
	if err != nil {
		return nil, err
	}
	return openProbeRouteRelayNetConnWithResolvedHostAndMode(routeID, secret, relayHost, relayPort, layer, bridgeRole, probeRouteRelayModeBridge, relayDialHost, relayHostHeader, openTimeout, true)
}

func openProbeRouteRelayNetConnWithResolvedHost(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	return openProbeRouteRelayNetConnWithResolvedHostAndMode(routeID, secret, relayHost, relayPort, layer, bridgeRole, probeRouteRelayModeBridge, relayDialHost, relayHostHeader, openTimeout, cacheOnSuccess)
}

func probeRouteRelayProtocolCandidatesPrefer(candidates []string, preferred string) []string {
	preferred = normalizeProbeRouteRouteLayer(preferred)
	if preferred == "" {
		return candidates
	}
	ordered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if normalizeProbeRouteRouteLayer(candidate) == preferred {
			ordered = append(ordered, preferred)
			break
		}
	}
	for _, candidate := range candidates {
		clean := normalizeProbeRouteRouteLayer(candidate)
		if clean == "" || clean == preferred {
			continue
		}
		ordered = append(ordered, clean)
	}
	if len(ordered) == 0 {
		return candidates
	}
	return ordered
}

func openProbeRouteRelayNetConnWithResolvedHostAndMode(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, relayMode string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	return openProbeRouteRelayNetConnWithResolvedHostModeAndToken(routeID, secret, relayHost, relayPort, layer, bridgeRole, relayMode, "", relayDialHost, relayHostHeader, openTimeout, cacheOnSuccess)
}

func openProbeRouteRelayNetConnWithResolvedHostModeAndToken(routeID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, relayMode string, connToken string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	relayDialHost = strings.TrimSpace(strings.Trim(relayDialHost, "[]"))
	relayHostHeader = strings.TrimSpace(strings.Trim(relayHostHeader, "[]"))
	if relayDialHost == "" {
		return nil, errors.New("relay dial host is required")
	}
	if relayHostHeader == "" {
		relayHostHeader = strings.TrimSpace(strings.Trim(relayHost, "[]"))
	}
	layer = normalizeProbeRouteRouteLayer(layer)
	var (
		conn net.Conn
		err  error
	)
	if layer == "websocket" {
		conn, err = openProbeRouteRelayWebSocketNetConn(routeID, secret, relayHost, relayPort, bridgeRole, relayMode, connToken, relayDialHost, relayHostHeader, openTimeout, cacheOnSuccess)
	} else if layer == "websocket-h3" {
		conn, err = openProbeRouteRelayHTTP3WebSocketNetConn(routeID, secret, relayHost, relayPort, bridgeRole, relayMode, connToken, relayDialHost, relayHostHeader, openTimeout, cacheOnSuccess)
	} else {
		return nil, fmt.Errorf("unsupported relay protocol: %s", layer)
	}
	if err != nil {
		invalidateProbeRouteRelayResolveCacheAfterFailedDial(relayHost, relayDialHost)
	}
	return conn, err
}

func openProbeRouteRelayWebSocketNetConn(routeID string, secret string, relayHost string, relayPort int, bridgeRole string, relayMode string, connToken string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	startedAt := time.Now()
	if openTimeout <= 0 {
		openTimeout = probeRouteRelayDialTimeout + probeRouteRelayResponseReadDeadline
	}
	relayURL, err := buildProbeRouteRelayWebSocketURL(relayHostHeader, relayPort, routeID)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set(probeRouteLegacyRouteIDHeader, strings.TrimSpace(routeID))
	header.Set(probeRouteCodexRouteIDHeader, strings.TrimSpace(routeID))
	header.Set(probeRouteCodexVersionHeader, probeRouteAuthPacketVersion)
	header.Set(probeRouteCodexRelayModeHeader, firstNonEmpty(strings.TrimSpace(relayMode), probeRouteRelayModeBridge))
	header.Set(probeRouteCodexRelayRoleHeader, normalizeProbeRouteBridgeRole(bridgeRole))
	if err := applyProbeRouteSecretAuthHeaders(header, routeID, secret, currentProbeVirtualRouterLocalNodeID(), http.MethodGet, probeRouteRelayAPIPath, bridgeRole); err != nil {
		return nil, err
	}
	if strings.TrimSpace(connToken) != "" {
		header.Set(probeRouteCodexConnIDHeader, strings.TrimSpace(connToken))
	}

	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(relayPort))
	dialer := websocket.Dialer{
		HandshakeTimeout:  openTimeout,
		Proxy:             nil,
		ReadBufferSize:    probeRouteRelayWebSocketBufferBytes,
		WriteBufferSize:   probeRouteRelayWebSocketBufferBytes,
		EnableCompression: false,
		NetDialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			if err := ensureProbeRouteDirectBypass(dialHostPort); err != nil {
				log.Printf("probe route relay websocket direct bypass failed: target=%s err=%v", strings.TrimSpace(dialHostPort), err)
			}
			netDialer := applyProbeRouteEgressDialer(&net.Dialer{Timeout: probeRouteRelayDialTimeout})
			conn, err := netDialer.DialContext(ctx, probeRouteEgressDialNetwork(network, dialHostPort), dialHostPort)
			if err == nil {
				tuneProbeRouteNetConn(conn)
			}
			return conn, err
		},
	}
	tlsConfig, err := newProbeRouteRelayTLSConfig(routeID, relayHostHeader, tls.VersionTLS12, nil)
	if err != nil {
		return nil, err
	}
	dialer.TLSClientConfig = tlsConfig
	logProbeRouteRelayDialAttempt("websocket", routeID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, openTimeout)
	ws, response, err := dialer.Dial(relayURL, header)
	if err != nil {
		if response != nil && response.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			statusErr := fmt.Errorf("probe relay websocket failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
			logProbeRouteRelayDialOutcome("websocket", routeID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), statusErr)
			return nil, statusErr
		}
		wrappedErr := wrapProbeRouteRelayDialError("websocket", relayDialHost, relayPort, err)
		logProbeRouteRelayDialOutcome("websocket", routeID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	if cacheOnSuccess {
		refreshProbeRouteRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	}
	logProbeRouteRelayDialOutcome("websocket", routeID, "websocket", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), nil)
	return newWebSocketNetConn(ws), nil
}

func openProbeRouteRelayHTTP3WebSocketNetConn(routeID string, secret string, relayHost string, relayPort int, bridgeRole string, relayMode string, connToken string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	startedAt := time.Now()
	if openTimeout <= 0 {
		openTimeout = probeRouteRelayProtocolProbeTimeout
	}
	relayURL, err := buildProbeRouteRelayURL(relayHostHeader, relayPort, routeID)
	if err != nil {
		return nil, err
	}
	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(relayPort))
	tlsConf, err := newProbeRouteRelayTLSConfig(routeID, relayHostHeader, tls.VersionTLS13, []string{http3.NextProtoH3})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	openTimer := time.AfterFunc(openTimeout, cancel)
	stopOpenTimer := func() {
		if openTimer != nil {
			openTimer.Stop()
		}
	}
	logProbeRouteRelayDialAttempt("websocket-h3", routeID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, openTimeout)
	quicConn, err := dialProbeRouteBoundQUIC(ctx, dialHostPort, tlsConf, newProbeRouteQUICConfig(0))
	if err != nil {
		stopOpenTimer()
		cancel()
		wrappedErr := wrapProbeRouteRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeRouteRelayDialOutcome("websocket-h3", routeID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
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
		stopOpenTimer()
		cancel()
		timeoutErr := fmt.Errorf("probe relay h3 websocket open timeout: relay=%s:%d", relayDialHost, relayPort)
		logProbeRouteRelayDialOutcome("websocket-h3", routeID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), timeoutErr)
		return nil, timeoutErr
	case <-clientConn.Context().Done():
		stopOpenTimer()
		cancel()
		stateErr := fmt.Errorf("probe relay h3 websocket failed: %w", context.Cause(clientConn.Context()))
		logProbeRouteRelayDialOutcome("websocket-h3", routeID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), stateErr)
		return nil, stateErr
	}
	if settings := clientConn.Settings(); settings == nil || !settings.EnableExtendedConnect {
		_ = quicConn.CloseWithError(0, "h3 websocket extended connect disabled")
		stopOpenTimer()
		cancel()
		extendedErr := errors.New("probe relay h3 websocket failed: server did not enable extended connect")
		logProbeRouteRelayDialOutcome("websocket-h3", routeID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), extendedErr)
		return nil, extendedErr
	}
	streamTimeout := probeRouteHTTP3StreamOpenTimeout(openTimeout)
	stream, err := clientConn.OpenRequestStream(ctx)
	if err != nil {
		_ = quicConn.CloseWithError(0, "h3 websocket stream open failed")
		stopOpenTimer()
		cancel()
		wrappedErr := wrapProbeRouteRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeRouteRelayDialOutcome("websocket-h3", routeID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	_ = stream.SetDeadline(time.Now().Add(streamTimeout))
	request, err := http.NewRequestWithContext(ctx, http.MethodConnect, relayURL, nil)
	if err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 websocket request build failed")
		stopOpenTimer()
		cancel()
		return nil, err
	}
	request.Proto = "websocket"
	request.ProtoMajor = 3
	request.ProtoMinor = 0
	request.Header.Set(probeRouteLegacyRouteIDHeader, strings.TrimSpace(routeID))
	request.Header.Set(probeRouteCodexRouteIDHeader, strings.TrimSpace(routeID))
	request.Header.Set(probeRouteCodexVersionHeader, probeRouteAuthPacketVersion)
	if err := applyProbeRouteSecretAuthHeaders(request.Header, routeID, secret, currentProbeVirtualRouterLocalNodeID(), http.MethodConnect, probeRouteRelayAPIPath, bridgeRole); err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 websocket auth failed")
		stopOpenTimer()
		cancel()
		return nil, err
	}
	request.Header.Set(probeRouteCodexRelayModeHeader, firstNonEmpty(strings.TrimSpace(relayMode), probeRouteRelayModeBridge))
	request.Header.Set(probeRouteCodexRelayRoleHeader, normalizeProbeRouteBridgeRole(bridgeRole))
	if strings.TrimSpace(connToken) != "" {
		request.Header.Set(probeRouteCodexConnIDHeader, strings.TrimSpace(connToken))
	}
	if strings.TrimSpace(relayHostHeader) != "" {
		request.Host = strings.TrimSpace(relayHostHeader)
	}
	if err := stream.SendRequestHeader(request); err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 websocket header send failed")
		stopOpenTimer()
		cancel()
		wrappedErr := wrapProbeRouteRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeRouteRelayDialOutcome("websocket-h3", routeID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	response, err := stream.ReadResponse()
	if err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 websocket response failed")
		stopOpenTimer()
		cancel()
		wrappedErr := wrapProbeRouteRelayDialError("websocket-h3", relayDialHost, relayPort, err)
		logProbeRouteRelayDialOutcome("websocket-h3", routeID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "h3 websocket status failed")
		stopOpenTimer()
		cancel()
		statusErr := fmt.Errorf("probe relay h3 websocket failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
		logProbeRouteRelayDialOutcome("websocket-h3", routeID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), statusErr)
		return nil, statusErr
	}
	_ = stream.SetDeadline(time.Time{})

	if cacheOnSuccess {
		refreshProbeRouteRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	}
	stopOpenTimer()
	logProbeRouteRelayDialOutcome("websocket-h3", routeID, "websocket-h3", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), nil)
	cancelOnce := sync.Once{}
	return &probeRouteHTTP3StreamNetConn{
		stream: stream,
		local:  probeRouteRelayNetAddr{label: "probe-route-h3-websocket-local"},
		remote: probeRouteRelayNetAddr{label: dialHostPort},
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

func probeRouteHTTP3StreamOpenTimeout(openTimeout time.Duration) time.Duration {
	if openTimeout <= 0 {
		return probeRouteRelayProtocolProbeTimeout
	}
	if openTimeout > probeRouteRelayProtocolProbeTimeout {
		return probeRouteRelayProtocolProbeTimeout
	}
	return openTimeout
}

func probeRouteRelayFetchSpeedDebugDefault(routeID string, secret string, relayHost string, relayPort int, layer string, protocol string, openTimeout time.Duration) (probeSpeedDebugResultPayload, error) {
	candidates := probeRouteRelayDebugProtocolCandidates(layer, protocol)
	if len(candidates) == 0 {
		candidates = probeRouteRelayProtocolCandidates(layer)
	}
	var errs []string
	for _, candidate := range candidates {
		payload, err := probeRouteRelayFetchSpeedDebugWithLayer(routeID, secret, relayHost, relayPort, candidate, openTimeout)
		if err == nil {
			return payload, nil
		}
		errs = append(errs, fmt.Sprintf("%s=%v", normalizeProbeRouteRouteLayer(candidate), err))
	}
	if len(errs) == 0 {
		return probeSpeedDebugResultPayload{}, errors.New("no relay speed debug protocol candidate")
	}
	return probeSpeedDebugResultPayload{}, fmt.Errorf("relay speed debug fetch failed: %s", strings.Join(errs, "; "))
}

func probeRouteRelayFetchSpeedDebugWithLayer(routeID string, secret string, relayHost string, relayPort int, layer string, openTimeout time.Duration) (probeSpeedDebugResultPayload, error) {
	cleanLayer := normalizeProbeRouteRouteLayer(layer)
	if cleanLayer != "websocket" && cleanLayer != "websocket-h3" {
		return probeSpeedDebugResultPayload{}, fmt.Errorf("unsupported speed debug protocol: %s", layer)
	}
	if openTimeout <= 0 {
		openTimeout = probeRouteRelayProtocolProbeTimeout
	}
	relayDialHost, relayHostHeader, err := resolveProbeRouteDialIPHost(relayHost)
	if err != nil {
		return probeSpeedDebugResultPayload{}, err
	}
	conn, err := openProbeRouteRelayNetConnWithResolvedHostModeAndToken(routeID, secret, relayHost, relayPort, cleanLayer, probeRouteBridgeRoleToNext, probeRouteRelayModeSpeedDebug, "", relayDialHost, relayHostHeader, openTimeout, true)
	if err != nil {
		return probeSpeedDebugResultPayload{}, err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(openTimeout))
	var payload probeSpeedDebugResultPayload
	if err := json.NewDecoder(conn).Decode(&payload); err != nil {
		return probeSpeedDebugResultPayload{}, err
	}
	_ = conn.SetReadDeadline(time.Time{})
	if strings.TrimSpace(payload.Scope) == "" {
		payload.Scope = "route_relay"
	}
	return payload, nil
}

func probeRouteRelayDebugProtocolCandidates(layer string, protocol string) []string {
	cleanProtocol := normalizeProbeRouteRouteLayer(protocol)
	switch cleanProtocol {
	case "websocket", "websocket-h3":
		return []string{cleanProtocol}
	}
	return probeRouteRelayProtocolCandidates(layer)
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

func wrapProbeRouteRelayDialError(layer string, relayDialHost string, relayPort int, err error) error {
	if err == nil {
		return nil
	}
	if normalizeProbeRouteRouteLayer(layer) != "websocket-h3" || !isProbeRouteRelayUDPSocketResourceError(err) {
		return err
	}
	return fmt.Errorf(
		"probe relay websocket-h3 udp socket unavailable: relay=%s:%d note=each_route_uses_independent_quic_connection err=%w",
		strings.TrimSpace(relayDialHost),
		relayPort,
		err,
	)
}

func isProbeRouteRelayUDPSocketResourceError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "listen udp") &&
		(strings.Contains(text, "buffer space") || strings.Contains(text, "queue was full"))
}

func (a probeRouteRelayNetAddr) Network() string {
	return "probe-route-relay"
}

func (a probeRouteRelayNetAddr) String() string {
	value := strings.TrimSpace(a.label)
	if value == "" {
		return "probe-route-relay"
	}
	return value
}

type probeRouteHTTP3StreamNetConn struct {
	stream  probeRouteHTTP3Stream
	local   net.Addr
	remote  net.Addr
	closeFn func() error
}

type probeRouteHTTP3Stream interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

func (c *probeRouteHTTP3StreamNetConn) Read(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, io.EOF
	}
	return c.stream.Read(payload)
}

func (c *probeRouteHTTP3StreamNetConn) Write(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, io.ErrClosedPipe
	}
	return c.stream.Write(payload)
}

func (c *probeRouteHTTP3StreamNetConn) Close() error {
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

func (c *probeRouteHTTP3StreamNetConn) LocalAddr() net.Addr {
	if c != nil && c.local != nil {
		return c.local
	}
	return probeRouteRelayNetAddr{label: "probe-route-h3-websocket-local"}
}

func (c *probeRouteHTTP3StreamNetConn) RemoteAddr() net.Addr {
	if c != nil && c.remote != nil {
		return c.remote
	}
	return probeRouteRelayNetAddr{label: "probe-route-h3-websocket-remote"}
}

func (c *probeRouteHTTP3StreamNetConn) SetDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetDeadline(t)
}

func (c *probeRouteHTTP3StreamNetConn) SetReadDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetReadDeadline(t)
}

func (c *probeRouteHTTP3StreamNetConn) SetWriteDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetWriteDeadline(t)
}

func applyProbeRouteSecretAuthHeaders(headers http.Header, routeID string, secret string, sourceNodeID string, method string, requestPath string, relayRole string) error {
	cleanRouteID := strings.TrimSpace(routeID)
	cleanSecret := strings.TrimSpace(secret)
	if cleanRouteID == "" {
		return errors.New("route_id is required")
	}
	if cleanSecret == "" {
		return errors.New("route_secret is required")
	}
	cleanSourceNodeID := normalizeProbeRouteNodeID(sourceNodeID)
	if cleanSourceNodeID == "" {
		return errors.New("source_node_id is required")
	}
	nonce := randomHexToken(16)
	headers.Set("Authorization", "Bearer "+nonce)
	headers.Set(probeRouteCodexAuthModeHeader, "secret_hmac")
	headers.Set(probeRouteCodexSourceNodeHeader, cleanSourceNodeID)
	headers.Set(probeRouteCodexMACHeader, buildProbeRouteHMAC(cleanSecret, cleanRouteID, nonce, method, requestPath, cleanSourceNodeID, relayRole))
	applyProbeRouteAuthTicketHeader(headers, cleanRouteID)
	return nil
}

func buildProbeRouteRelayURL(host string, port int, routeID string) (string, error) {
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
		Path:   probeRouteRelayAPIPath,
	}
	query := u.Query()
	query.Set("route_id", strings.TrimSpace(routeID))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func buildProbeRouteRelayWebSocketURL(host string, port int, routeID string) (string, error) {
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
		Path:   probeRouteRelayAPIPath,
	}
	query := u.Query()
	query.Set("route_id", strings.TrimSpace(routeID))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func resolveProbeRouteDialIPHost(rawHost string) (dialHost string, hostHeader string, err error) {
	return resolveProbeRouteDialIPHostWithPolicy(rawHost, false)
}

func resolveProbeVirtualRouterBridgeDialIPHost(rawHost string) (dialHost string, hostHeader string, err error) {
	return resolveProbeRouteDialIPHost(rawHost)
}

func resolveProbeRouteDialIPHostWithPolicy(rawHost string, preserveDomain bool) (dialHost string, hostHeader string, err error) {
	cleanHost := strings.TrimSpace(strings.Trim(rawHost, "[]"))
	if cleanHost == "" {
		return "", "", fmt.Errorf("empty relay host")
	}
	if parsed := net.ParseIP(cleanHost); parsed != nil {
		ipText := parsed.String()
		return ipText, ipText, nil
	}
	if preserveDomain {
		return cleanHost, cleanHost, nil
	}
	if cachedDialHost, cachedHostHeader, ok := loadProbeRouteRelayResolveCache(cleanHost, false); ok {
		_ = cachedHostHeader
		return cachedDialHost, cachedDialHost, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, resolveErr := probeRouteRelayLookupIP(ctx, "ip", cleanHost)
	if resolveErr != nil {
		if cachedDialHost, cachedHostHeader, ok := loadProbeRouteRelayResolveCache(cleanHost, true); ok {
			_ = cachedHostHeader
			return cachedDialHost, cachedDialHost, nil
		}
		return "", "", fmt.Errorf("resolve relay host failed: %w", resolveErr)
	}
	ip := selectProbeRoutePreferredDialIP(ips)
	if ip == nil {
		return "", "", fmt.Errorf("resolve relay host failed: no ip")
	}
	dialHost = ip.String()
	hostHeader = dialHost
	return dialHost, hostHeader, nil
}

func loadProbeRouteRelayResolveCache(host string, allowStale bool) (dialHost string, hostHeader string, ok bool) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return "", "", false
	}
	now := probeRouteRelayResolveNow()
	probeRouteRelayResolveCache.mu.Lock()
	defer probeRouteRelayResolveCache.mu.Unlock()
	entry, exists := probeRouteRelayResolveCache.items[cleanHost]
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
	delete(probeRouteRelayResolveCache.items, cleanHost)
	return "", "", false
}

func storeProbeRouteRelayResolveCache(host string, dialHost string, hostHeader string) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	cleanDialHost := strings.TrimSpace(strings.Trim(dialHost, "[]"))
	cleanHostHeader := strings.TrimSpace(strings.Trim(hostHeader, "[]"))
	if cleanHost == "" || cleanDialHost == "" {
		return
	}
	now := probeRouteRelayResolveNow()
	probeRouteRelayResolveCache.mu.Lock()
	probeRouteRelayResolveCache.items[cleanHost] = probeRouteRelayResolveCacheEntry{
		DialHost:   cleanDialHost,
		HostHeader: firstNonEmpty(cleanHostHeader, cleanHost),
		ExpiresAt:  now.Add(probeRouteRelayResolveCacheTTL),
		StaleUntil: now.Add(probeRouteRelayResolveMaxStale),
	}
	probeRouteRelayResolveCache.mu.Unlock()
}

func refreshProbeRouteRelayResolveCacheOnConnectSuccess(host string, dialHost string, hostHeader string) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return
	}
	if parsed := net.ParseIP(cleanHost); parsed != nil {
		return
	}
	storeProbeRouteRelayResolveCache(cleanHost, dialHost, hostHeader)
}

// invalidateProbeRouteRelayResolveCacheAfterFailedDial makes the next relay
// reconnect resolve the configured domain again instead of retrying a known
// failed cached IP. Domain-preserving relay paths have no entry to remove.
func invalidateProbeRouteRelayResolveCacheAfterFailedDial(host string, dialHost string) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	cleanDialHost := strings.TrimSpace(strings.Trim(dialHost, "[]"))
	if cleanHost == "" || cleanDialHost == "" || net.ParseIP(cleanHost) != nil {
		return
	}
	probeRouteRelayResolveCache.mu.Lock()
	defer probeRouteRelayResolveCache.mu.Unlock()
	entry, ok := probeRouteRelayResolveCache.items[cleanHost]
	if !ok || !strings.EqualFold(strings.TrimSpace(entry.DialHost), cleanDialHost) {
		return
	}
	delete(probeRouteRelayResolveCache.items, cleanHost)
	log.Printf("probe route relay cached dns ip invalidated after dial failure: host=%s ip=%s", cleanHost, cleanDialHost)
}

func resetProbeRouteRelayResolveCacheForTest() {
	probeRouteRelayResolveCache.mu.Lock()
	probeRouteRelayResolveCache.items = make(map[string]probeRouteRelayResolveCacheEntry)
	probeRouteRelayResolveCache.mu.Unlock()
}

func clearProbeRouteRelayResolveCache() {
	probeRouteRelayResolveCache.mu.Lock()
	probeRouteRelayResolveCache.items = make(map[string]probeRouteRelayResolveCacheEntry)
	probeRouteRelayResolveCache.mu.Unlock()
}

func resolveProbeRouteTLSServerName(layer string, dialHost string, hostHeader string) string {
	cleanDialHost := strings.TrimSpace(strings.Trim(dialHost, "[]"))
	cleanHostHeader := strings.TrimSpace(strings.Trim(hostHeader, "[]"))

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

func selectProbeRoutePreferredDialIP(ips []net.IP) net.IP {
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

func resolveProbeRouteClientTLSServerName(layer string, dialHost string, hostHeader string) string {
	return resolveProbeRouteTLSServerName(layer, dialHost, hostHeader)
}

func newProbeRouteRelayTLSConfig(_ string, hostHeader string, minVersion uint16, nextProtos []string) (*tls.Config, error) {
	cleanHost := strings.TrimSpace(strings.Trim(hostHeader, "[]"))
	if cleanHost != "" && net.ParseIP(cleanHost) == nil && isProbeVirtualRouterCloudflareCopilotDomain(cleanHost) {
		return &tls.Config{
			MinVersion: minVersion,
			NextProtos: append([]string(nil), nextProtos...),
			ServerName: cleanHost,
		}, nil
	}
	return &tls.Config{
		MinVersion: minVersion,
		NextProtos: append([]string(nil), nextProtos...),
		// Ordinary relays intentionally omit SNI. Route HMAC and auth tickets
		// authenticate the peer above TLS because the service certificate is not
		// the controller-issued node certificate.
		InsecureSkipVerify: true,
	}, nil
}
