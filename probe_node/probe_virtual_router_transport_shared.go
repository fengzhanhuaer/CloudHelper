package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

type probeRouteBridgeSessionSnapshot struct {
	RouteID             string `json:"route_id,omitempty"`
	RuntimeRole         string `json:"runtime_role,omitempty"`
	Direction           string `json:"direction,omitempty"`
	SessionID           string `json:"session_id,omitempty"`
	BridgeRole          string `json:"bridge_role,omitempty"`
	RemoteAddr          string `json:"remote_addr,omitempty"`
	ConnectedAt         string `json:"connected_at,omitempty"`
	ConnectedMS         int64  `json:"connected_ms,omitempty"`
	StreamsCurrent      int    `json:"streams_current,omitempty"`
	RTTMS               int64  `json:"rtt_ms,omitempty"`
	LastPingAt          string `json:"last_ping_at,omitempty"`
	LastPongAt          string `json:"last_pong_at,omitempty"`
	PingsSent           int64  `json:"pings_sent,omitempty"`
	PongsReceived       int64  `json:"pongs_received,omitempty"`
	PingTimeouts        int64  `json:"ping_timeouts,omitempty"`
	PendingPings        int    `json:"pending_pings,omitempty"`
	FramesSent          int64  `json:"frames_sent,omitempty"`
	FrameBytesSent      int64  `json:"frame_bytes_sent,omitempty"`
	FramesReceived      int64  `json:"frames_received,omitempty"`
	FrameBytesReceived  int64  `json:"frame_bytes_received,omitempty"`
	LastFrameSentAt     string `json:"last_frame_sent_at,omitempty"`
	LastFrameReceivedAt string `json:"last_frame_received_at,omitempty"`
	Closed              bool   `json:"closed,omitempty"`
}

type probeRouteBridgeRuntimeStatus struct {
	DownstreamActive int                               `json:"downstream_active"`
	UpstreamActive   int                               `json:"upstream_active"`
	Sessions         []probeRouteBridgeSessionSnapshot `json:"sessions,omitempty"`
	UpdatedAt        string                            `json:"updated_at,omitempty"`
}

type probeRouteAuthEnvelope struct {
	Type       string                     `json:"type,omitempty"`
	APIVersion string                     `json:"api_version,omitempty"`
	RequestID  string                     `json:"request_id,omitempty"`
	Timestamp  string                     `json:"timestamp,omitempty"`
	Auth       *probeRouteAuthPayloadBody `json:"auth,omitempty"`
	Mode       string                     `json:"mode,omitempty"`
	RouteID    string                     `json:"route_id,omitempty"`
	Nonce      string                     `json:"nonce,omitempty"`
	Signature  string                     `json:"signature,omitempty"`
	MAC        string                     `json:"mac,omitempty"`
	AuthTicket string                     `json:"auth_ticket,omitempty"`
}

type probeRouteAuthPayloadBody struct {
	Mode       string `json:"mode,omitempty"`
	RouteID    string `json:"route_id,omitempty"`
	Nonce      string `json:"nonce,omitempty"`
	Timestamp  string `json:"timestamp,omitempty"`
	Signature  string `json:"signature,omitempty"`
	MAC        string `json:"mac,omitempty"`
	AuthTicket string `json:"auth_ticket,omitempty"`
}

type probeRouteAuthIPState struct {
	FailedAttempts int
	BlacklistedTil time.Time
	Manual         bool
}

type probeRouteAuthBlacklistEntry struct {
	IP        string `json:"ip"`
	Until     string `json:"until,omitempty"`
	Manual    bool   `json:"manual,omitempty"`
	ExpiresIn string `json:"expires_in,omitempty"`
}

type probeRouteAuthBlacklistFile struct {
	IPs []string `json:"ips"`
}

type probeRouteAssociationV2Meta struct {
	Version          int    `json:"version"`
	AssocKeyV2       string `json:"assoc_key_v2,omitempty"`
	FlowID           string `json:"flow_id,omitempty"`
	SourceKey        string `json:"source_key,omitempty"`
	SourceRefs       int64  `json:"source_refs,omitempty"`
	SrcIP            string `json:"src_ip,omitempty"`
	SrcPort          uint16 `json:"src_port,omitempty"`
	DstIP            string `json:"dst_ip,omitempty"`
	DstPort          uint16 `json:"dst_port,omitempty"`
	IPFamily         uint8  `json:"ip_family,omitempty"`
	Transport        string `json:"transport,omitempty"`
	RouteGroup       string `json:"route_group,omitempty"`
	RouteNodeID      string `json:"route_node_id,omitempty"`
	RouteTarget      string `json:"route_target,omitempty"`
	RouteFingerprint string `json:"route_fingerprint,omitempty"`
	NATMode          string `json:"nat_mode,omitempty"`
	TTLProfile       string `json:"ttl_profile,omitempty"`
	IdleTimeoutMS    int64  `json:"idle_timeout_ms,omitempty"`
	GCIntervalMS     int64  `json:"gc_interval_ms,omitempty"`
	CreatedAtUnixMS  int64  `json:"created_at_unix_ms,omitempty"`
}

const (
	probeRouteRelayAPIPath    = "/api/node/route/relay"
	probeRouteAuthNoncePrefix = "CHNONCE "

	probeRouteLegacyRouteIDHeader   = "X-CH-Route-ID"
	probeRouteCodexRouteIDHeader    = "X-Codex-Route-Id"
	probeRouteCodexAuthModeHeader   = "X-Codex-Auth-Mode"
	probeRouteCodexMACHeader        = "X-Codex-Mac"
	probeRouteCodexAuthTicketHeader = "X-Codex-User-Auth-Ticket"
	probeRouteCodexAuthTimeHeader   = "X-Codex-Auth-Timestamp"
	probeRouteCodexVersionHeader    = "X-Codex-Api-Version"
	probeRouteCodexRelayModeHeader  = "X-Codex-Relay-Mode"
	probeRouteCodexRelayRoleHeader  = "X-Codex-Relay-Role"
	probeRouteCodexConnIDHeader     = "X-Codex-Conn-Id"
	probeRouteCodexSpeedBytesHeader = "X-Codex-Speed-Bytes"

	probeRouteRelayModeBridge     = "bridge"
	probeRouteRelayModeSpeedTest  = "speed_test"
	probeRouteRelayModeSpeedDebug = "speed_debug"
	probeRouteBridgeRoleToNext    = "to_next"
	probeRouteBridgeRoleToPrev    = "to_prev"

	probeRouteDialModeForward = "forward"
	probeRouteDialModeReverse = "reverse"
	probeRouteDialModeNone    = "none"

	probeRouteBridgeRetryMin = 1 * time.Second
	probeRouteBridgeRetryMax = 15 * time.Second

	probeRouteUDPSessionIdleTTL         = 90 * time.Second
	probeRouteUDPSessionGCInterval      = 15 * time.Second
	probeRouteRelayDialTimeout          = 12 * time.Second
	probeRouteRelayResponseReadDeadline = 10 * time.Second

	probeRouteRelayProtocolQualityTTL          = 10 * time.Minute
	probeRouteRelayProtocolNegativeTTL         = 60 * time.Second
	probeRouteRelayProtocolProbeTimeout        = 6 * time.Second
	probeRouteRelayProtocolSwitchMinHold       = 30 * time.Second
	probeRouteRelaySpeedTestBytes              = 128 * 1024 * 1024
	probeRouteRelaySpeedTestMaxBytes           = 256 * 1024 * 1024
	probeRouteRelaySpeedTestTimeout            = 10 * time.Second
	probeRouteRelaySpeedTestChunkBytes         = 1024 * 1024
	probeRouteRelayWebSocketBufferBytes        = 512 * 1024
	probeRouteRelayWebSocketWriteBatchBytes    = 1024 * 1024
	probeRouteRelayWebSocketWriteQueueDepth    = 64
	probeRouteRelayTCPSocketBufferBytes        = 8 * 1024 * 1024
	probeRouteRelayUDPSocketBufferBytes        = 64 * 1024 * 1024
	probeRouteRelayTCPKeepAlivePeriod          = 30 * time.Second
	probeRouteRelayQUICInitialStreamWindow     = 128 * 1024 * 1024
	probeRouteRelayQUICMaxStreamWindow         = 512 * 1024 * 1024
	probeRouteRelayQUICInitialConnectionWindow = 512 * 1024 * 1024
	probeRouteRelayQUICMaxConnectionWindow     = 1024 * 1024 * 1024
	probeRouteRelayQUICMaxIncomingStreams      = 1024
	probeRouteRelayQUICDatagramMaxPayloadBytes = 1200

	probeRouteAuthPacketType        = "github_copilot_auth_request"
	probeRouteAuthPacketVersion     = "2025-03-22"
	probeRouteAuthFailureThreshold  = 5
	probeRouteAuthBlacklistTTL      = 5 * time.Hour
	probeRouteAuthFailureMinDelayMs = 200
	probeRouteAuthFailureMaxDelayMs = 400
	probeRouteAuthReplayTTL         = 10 * time.Minute
)

var probeRouteAuthIPStateMap = struct {
	mu    sync.Mutex
	items map[string]probeRouteAuthIPState
}{items: make(map[string]probeRouteAuthIPState)}

var probeRouteAuthTicketStore = struct {
	mu    sync.RWMutex
	items map[string]string
}{items: make(map[string]string)}

var probeRouteAuthTicketNow = time.Now

var probeRouteAuthReplayStore = struct {
	mu    sync.Mutex
	items map[string]time.Time
}{items: make(map[string]time.Time)}

func normalizeProbeRouteRouteLayer(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "websocket-h3", "h3", "http3", "quic":
		return "websocket-h3"
	default:
		return "websocket"
	}
}

func normalizeProbeRouteDialMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case probeRouteDialModeReverse, "rev":
		return probeRouteDialModeReverse
	case probeRouteDialModeNone:
		return probeRouteDialModeNone
	default:
		return probeRouteDialModeForward
	}
}

func parseProbeRouteUserPublicKey(raw string) (ed25519.PublicKey, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("public key is required")
	}
	if block, _ := pem.Decode([]byte(trimmed)); block != nil {
		pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		pub, ok := pubAny.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("public key is not ed25519")
		}
		return pub, nil
	}
	if rawBytes, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
		if len(rawBytes) == ed25519.PublicKeySize {
			return ed25519.PublicKey(rawBytes), nil
		}
		if pubAny, parseErr := x509.ParsePKIXPublicKey(rawBytes); parseErr == nil {
			if pub, ok := pubAny.(ed25519.PublicKey); ok {
				return pub, nil
			}
		}
	}
	if rawBytes, err := hex.DecodeString(trimmed); err == nil && len(rawBytes) == ed25519.PublicKeySize {
		return ed25519.PublicKey(rawBytes), nil
	}
	return nil, fmt.Errorf("unsupported public key format")
}

func nextProbeRouteBridgeBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return probeRouteBridgeRetryMin
	}
	next := current * 2
	if next > probeRouteBridgeRetryMax {
		return probeRouteBridgeRetryMax
	}
	return next
}

func normalizeProbeRouteBridgeRole(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case probeRouteBridgeRoleToPrev:
		return probeRouteBridgeRoleToPrev
	default:
		return probeRouteBridgeRoleToNext
	}
}

func resolveProbeRouteIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	routeID := strings.TrimSpace(r.URL.Query().Get("route_id"))
	if routeID == "" {
		routeID = strings.TrimSpace(r.Header.Get(probeRouteCodexRouteIDHeader))
	}
	if routeID == "" {
		routeID = strings.TrimSpace(r.Header.Get(probeRouteLegacyRouteIDHeader))
	}
	return routeID
}

func readProbeRouteAuthEnvelopeFromHeaders(headers http.Header, routeID string) (probeRouteAuthEnvelope, error) {
	nonce, err := parseProbeRouteBearerToken(headers.Get("Authorization"))
	if err != nil {
		return probeRouteAuthEnvelope{}, err
	}
	env := probeRouteAuthEnvelope{
		Type:       probeRouteAuthPacketType,
		APIVersion: strings.TrimSpace(headers.Get(probeRouteCodexVersionHeader)),
		Timestamp:  strings.TrimSpace(headers.Get(probeRouteCodexAuthTimeHeader)),
		Mode:       strings.ToLower(strings.TrimSpace(headers.Get(probeRouteCodexAuthModeHeader))),
		RouteID:    strings.TrimSpace(routeID),
		Nonce:      nonce,
		MAC:        strings.TrimSpace(headers.Get(probeRouteCodexMACHeader)),
		AuthTicket: strings.TrimSpace(headers.Get(probeRouteCodexAuthTicketHeader)),
	}
	if env.APIVersion == "" {
		env.APIVersion = probeRouteAuthPacketVersion
	}
	if env.RouteID == "" {
		env.RouteID = strings.TrimSpace(headers.Get(probeRouteCodexRouteIDHeader))
	}
	return env, nil
}

func parseProbeRouteBearerToken(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("authorization bearer token is required")
	}
	fields := strings.Fields(value)
	if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
		return strings.TrimSpace(fields[1]), nil
	}
	return "", errors.New("authorization bearer token is invalid")
}

type probeRouteTunedTCPListener struct {
	*net.TCPListener
}

func (l *probeRouteTunedTCPListener) Accept() (net.Conn, error) {
	conn, err := l.AcceptTCP()
	if err != nil {
		return nil, err
	}
	applyProbeRouteTCPConnTuning(conn)
	return conn, nil
}

func applyProbeRouteTCPConnTuning(conn *net.TCPConn) {
	if conn == nil {
		return
	}
	_ = conn.SetKeepAlive(true)
	_ = conn.SetKeepAlivePeriod(probeRouteRelayTCPKeepAlivePeriod)
	_ = conn.SetReadBuffer(probeRouteRelayTCPSocketBufferBytes)
	_ = conn.SetWriteBuffer(probeRouteRelayTCPSocketBufferBytes)
}

func newProbeRouteQUICConfig(maxIncomingStreams int64) *quic.Config {
	return &quic.Config{
		InitialStreamReceiveWindow:     probeRouteRelayQUICInitialStreamWindow,
		MaxStreamReceiveWindow:         probeRouteRelayQUICMaxStreamWindow,
		InitialConnectionReceiveWindow: probeRouteRelayQUICInitialConnectionWindow,
		MaxConnectionReceiveWindow:     probeRouteRelayQUICMaxConnectionWindow,
		MaxIncomingStreams:             maxIncomingStreams,
		EnableDatagrams:                true,
	}
}

func recordProbeRouteAuthNonce(routeID string, nonce string) error {
	cleanRouteID := strings.TrimSpace(routeID)
	cleanNonce := strings.TrimSpace(nonce)
	if cleanRouteID == "" || cleanNonce == "" {
		return errors.New("auth nonce is required")
	}
	key := cleanRouteID + "\n" + cleanNonce
	now := time.Now()
	probeRouteAuthReplayStore.mu.Lock()
	defer probeRouteAuthReplayStore.mu.Unlock()
	for itemKey, expiresAt := range probeRouteAuthReplayStore.items {
		if now.After(expiresAt) {
			delete(probeRouteAuthReplayStore.items, itemKey)
		}
	}
	if expiresAt, exists := probeRouteAuthReplayStore.items[key]; exists && expiresAt.After(now) {
		return errors.New("auth nonce replay detected")
	}
	probeRouteAuthReplayStore.items[key] = now.Add(probeRouteAuthReplayTTL)
	return nil
}

func rememberProbeRouteAuthTicket(routeID string, authTicket string) {
	id := strings.TrimSpace(routeID)
	ticket := strings.TrimSpace(authTicket)
	if id == "" || ticket == "" {
		return
	}
	probeRouteAuthTicketStore.mu.Lock()
	probeRouteAuthTicketStore.items[id] = ticket
	for key, value := range probeRouteAuthTicketStore.items {
		if strings.EqualFold(strings.TrimSpace(key), id) && key != id {
			delete(probeRouteAuthTicketStore.items, key)
			probeRouteAuthTicketStore.items[id] = value
		}
	}
	probeRouteAuthTicketStore.mu.Unlock()
}

func lookupProbeRouteAuthTicket(routeID string) string {
	id := strings.TrimSpace(routeID)
	if id == "" {
		return ""
	}
	probeRouteAuthTicketStore.mu.RLock()
	ticket := strings.TrimSpace(probeRouteAuthTicketStore.items[id])
	probeRouteAuthTicketStore.mu.RUnlock()
	if ticket != "" {
		return ticket
	}
	lower := strings.ToLower(id)
	probeRouteAuthTicketStore.mu.Lock()
	for key, value := range probeRouteAuthTicketStore.items {
		if strings.EqualFold(strings.TrimSpace(key), lower) {
			probeRouteAuthTicketStore.items[id] = value
			ticket = strings.TrimSpace(value)
			break
		}
	}
	probeRouteAuthTicketStore.mu.Unlock()
	return ticket
}

func applyProbeRouteAuthTicketHeader(headers http.Header, routeID string) {
	if headers == nil {
		return
	}
	if ticket := lookupProbeRouteAuthTicket(routeID); ticket != "" {
		headers.Set(probeRouteCodexAuthTicketHeader, ticket)
	}
}

type probeRouteUserAuthTicketPayload struct {
	Version       string `json:"version,omitempty"`
	RouteID       string `json:"route_id,omitempty"`
	UserPublicKey string `json:"user_public_key,omitempty"`
	IssuedAt      string `json:"issued_at,omitempty"`
}

func verifyProbeRouteAuthTicketIssuedAt(raw string, now time.Time) error {
	text := strings.TrimSpace(raw)
	if text == "" {
		return errors.New("auth_ticket issued_at is required")
	}
	issuedAt, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return fmt.Errorf("auth_ticket issued_at invalid: %w", err)
	}
	if issuedAt.After(now.Add(5 * time.Minute)) {
		return errors.New("auth_ticket issued_at is in the future")
	}
	if now.Sub(issuedAt) > 35*24*time.Hour {
		return errors.New("auth_ticket expired")
	}
	return nil
}

func buildProbeRouteHMAC(secret string, routeID string, nonce string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(strings.TrimSpace(routeID)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(nonce)))
	return hex.EncodeToString(mac.Sum(nil))
}

func resolveProbeRouteSourceIPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			if ip := normalizeProbeRouteIP(strings.TrimSpace(parts[0])); ip != "" {
				return ip
			}
		}
	}
	return resolveProbeRouteSourceIPFromAddrString(strings.TrimSpace(r.RemoteAddr))
}

func resolveProbeRouteSourceIPFromAddrString(raw string) string {
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return normalizeProbeRouteIP(host)
	}
	return normalizeProbeRouteIP(raw)
}

func normalizeProbeRouteIP(raw string) string {
	clean := strings.TrimSpace(strings.Trim(raw, "[]"))
	if clean == "" {
		return ""
	}
	if parsed := net.ParseIP(clean); parsed != nil {
		return parsed.String()
	}
	return ""
}

func delayProbeRouteAuthFailure() {
	delay := probeRouteAuthFailureDelay()
	if delay > 0 {
		time.Sleep(delay)
	}
}

func probeRouteAuthFailureDelay() time.Duration {
	minDelay := probeRouteAuthFailureMinDelayMs
	maxDelay := probeRouteAuthFailureMaxDelayMs
	if maxDelay < minDelay {
		maxDelay = minDelay
	}
	span := maxDelay - minDelay + 1
	seed := time.Now().UnixNano()
	if raw := randomHexToken(2); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 16, 64); err == nil {
			seed = parsed
		}
	}
	randomOffset := int(seed % int64(span))
	if randomOffset < 0 {
		randomOffset = -randomOffset
	}
	return time.Duration(minDelay+randomOffset) * time.Millisecond
}

func isProbeRouteAuthIPBlacklisted(ip string) (bool, time.Time) {
	target := strings.TrimSpace(ip)
	if target == "" {
		return false, time.Time{}
	}
	now := time.Now()
	probeRouteAuthIPStateMap.mu.Lock()
	defer probeRouteAuthIPStateMap.mu.Unlock()
	state, ok := probeRouteAuthIPStateMap.items[target]
	if !ok {
		return false, time.Time{}
	}
	if state.Manual {
		return true, time.Time{}
	}
	if !state.BlacklistedTil.IsZero() && now.Before(state.BlacklistedTil) {
		return true, state.BlacklistedTil
	}
	if !state.BlacklistedTil.IsZero() {
		delete(probeRouteAuthIPStateMap.items, target)
	}
	return false, time.Time{}
}

func recordProbeRouteAuthFailure(ip string) (failures int, blacklisted bool, until time.Time) {
	target := strings.TrimSpace(ip)
	if target == "" {
		return 0, false, time.Time{}
	}
	now := time.Now()
	probeRouteAuthIPStateMap.mu.Lock()
	defer probeRouteAuthIPStateMap.mu.Unlock()
	state := probeRouteAuthIPStateMap.items[target]
	if state.Manual {
		return probeRouteAuthFailureThreshold, true, time.Time{}
	}
	if !state.BlacklistedTil.IsZero() && !now.Before(state.BlacklistedTil) {
		state.BlacklistedTil = time.Time{}
		state.FailedAttempts = 0
	}
	state.FailedAttempts++
	failures = state.FailedAttempts
	if state.FailedAttempts >= probeRouteAuthFailureThreshold {
		state.BlacklistedTil = now.Add(probeRouteAuthBlacklistTTL)
		state.FailedAttempts = 0
		blacklisted = true
		until = state.BlacklistedTil
		failures = probeRouteAuthFailureThreshold
	}
	probeRouteAuthIPStateMap.items[target] = state
	return failures, blacklisted, until
}

func resetProbeRouteAuthFailure(ip string) {
	target := strings.TrimSpace(ip)
	if target == "" {
		return
	}
	probeRouteAuthIPStateMap.mu.Lock()
	defer probeRouteAuthIPStateMap.mu.Unlock()
	state, ok := probeRouteAuthIPStateMap.items[target]
	if ok && state.Manual {
		state.FailedAttempts = 0
		probeRouteAuthIPStateMap.items[target] = state
		return
	}
	delete(probeRouteAuthIPStateMap.items, target)
}

func resolveProbeRouteAuthBlacklistPath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, "probe_route_auth_blacklist.json"), nil
}

func loadProbeRouteAuthBlacklistFromDisk() error {
	path, err := resolveProbeRouteAuthBlacklistPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return persistProbeRouteAuthBlacklistManualIPs(nil)
		}
		return err
	}
	var payload probeRouteAuthBlacklistFile
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
	}
	return setProbeRouteAuthBlacklistManualIPs(payload.IPs, false)
}

func persistProbeRouteAuthBlacklistManualIPs(ips []string) error {
	path, err := resolveProbeRouteAuthBlacklistPath()
	if err != nil {
		return err
	}
	payload := probeRouteAuthBlacklistFile{IPs: normalizeProbeRouteAuthBlacklistIPs(ips)}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func setProbeRouteAuthBlacklistManualIPs(ips []string, persist bool) error {
	normalized := normalizeProbeRouteAuthBlacklistIPs(ips)
	probeRouteAuthIPStateMap.mu.Lock()
	next := make(map[string]probeRouteAuthIPState, len(normalized))
	for _, ip := range normalized {
		next[ip] = probeRouteAuthIPState{Manual: true}
	}
	probeRouteAuthIPStateMap.items = next
	probeRouteAuthIPStateMap.mu.Unlock()
	if persist {
		return persistProbeRouteAuthBlacklistManualIPs(normalized)
	}
	return nil
}

func normalizeProbeRouteAuthBlacklistIPs(ips []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ips))
	for _, raw := range ips {
		ip := strings.TrimSpace(raw)
		if parsed := net.ParseIP(ip); parsed != nil {
			ip = parsed.String()
		}
		if ip == "" || net.ParseIP(ip) == nil {
			continue
		}
		if _, exists := seen[ip]; exists {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

func parseProbeRouteAuthBlacklistContent(content string) ([]string, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	ips := make([]string, 0, len(lines))
	for index, line := range lines {
		text := strings.TrimSpace(line)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if fields := strings.Fields(text); len(fields) > 0 {
			text = fields[0]
		}
		parsed := net.ParseIP(text)
		if parsed == nil {
			return nil, fmt.Errorf("line %d has invalid ip: %s", index+1, text)
		}
		ips = append(ips, parsed.String())
	}
	return normalizeProbeRouteAuthBlacklistIPs(ips), nil
}

func listProbeRouteAuthBlacklistEntries() []probeRouteAuthBlacklistEntry {
	now := time.Now()
	probeRouteAuthIPStateMap.mu.Lock()
	defer probeRouteAuthIPStateMap.mu.Unlock()
	items := make([]probeRouteAuthBlacklistEntry, 0, len(probeRouteAuthIPStateMap.items))
	for ip, state := range probeRouteAuthIPStateMap.items {
		if state.Manual {
			items = append(items, probeRouteAuthBlacklistEntry{IP: ip, Manual: true})
			continue
		}
		if state.BlacklistedTil.IsZero() {
			continue
		}
		if !now.Before(state.BlacklistedTil) {
			delete(probeRouteAuthIPStateMap.items, ip)
			continue
		}
		items = append(items, probeRouteAuthBlacklistEntry{
			IP:        ip,
			Until:     state.BlacklistedTil.UTC().Format(time.RFC3339),
			ExpiresIn: time.Until(state.BlacklistedTil).Round(time.Second).String(),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].IP < items[j].IP
	})
	return items
}

func probeRouteAuthBlacklistContent() string {
	entries := listProbeRouteAuthBlacklistEntries()
	lines := make([]string, 0, len(entries))
	for _, item := range entries {
		lines = append(lines, item.IP)
	}
	return strings.Join(lines, "\n")
}
