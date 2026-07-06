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

type probeChainBridgeSessionSnapshot struct {
	ChainID             string `json:"chain_id,omitempty"`
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

type probeChainBridgeRuntimeStatus struct {
	DownstreamActive int                               `json:"downstream_active"`
	UpstreamActive   int                               `json:"upstream_active"`
	Sessions         []probeChainBridgeSessionSnapshot `json:"sessions,omitempty"`
	UpdatedAt        string                            `json:"updated_at,omitempty"`
}

type probeChainAuthEnvelope struct {
	Type       string                     `json:"type,omitempty"`
	APIVersion string                     `json:"api_version,omitempty"`
	RequestID  string                     `json:"request_id,omitempty"`
	Timestamp  string                     `json:"timestamp,omitempty"`
	Auth       *probeChainAuthPayloadBody `json:"auth,omitempty"`
	Mode       string                     `json:"mode,omitempty"`
	ChainID    string                     `json:"chain_id,omitempty"`
	Nonce      string                     `json:"nonce,omitempty"`
	Signature  string                     `json:"signature,omitempty"`
	MAC        string                     `json:"mac,omitempty"`
	AuthTicket string                     `json:"auth_ticket,omitempty"`
}

type probeChainAuthPayloadBody struct {
	Mode       string `json:"mode,omitempty"`
	ChainID    string `json:"chain_id,omitempty"`
	Nonce      string `json:"nonce,omitempty"`
	Timestamp  string `json:"timestamp,omitempty"`
	Signature  string `json:"signature,omitempty"`
	MAC        string `json:"mac,omitempty"`
	AuthTicket string `json:"auth_ticket,omitempty"`
}

type probeChainAuthIPState struct {
	FailedAttempts int
	BlacklistedTil time.Time
	Manual         bool
}

type probeChainAuthBlacklistEntry struct {
	IP        string `json:"ip"`
	Until     string `json:"until,omitempty"`
	Manual    bool   `json:"manual,omitempty"`
	ExpiresIn string `json:"expires_in,omitempty"`
}

type probeChainAuthBlacklistFile struct {
	IPs []string `json:"ips"`
}

type probeChainAssociationV2Meta struct {
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
	probeChainRelayAPIPath    = "/api/node/chain/relay"
	probeChainAuthNoncePrefix = "CHNONCE "

	probeChainLegacyChainIDHeader   = "X-CH-Chain-ID"
	probeChainCodexChainIDHeader    = "X-Codex-Chain-Id"
	probeChainCodexAuthModeHeader   = "X-Codex-Auth-Mode"
	probeChainCodexMACHeader        = "X-Codex-Mac"
	probeChainCodexAuthTicketHeader = "X-Codex-User-Auth-Ticket"
	probeChainCodexAuthTimeHeader   = "X-Codex-Auth-Timestamp"
	probeChainCodexVersionHeader    = "X-Codex-Api-Version"
	probeChainCodexRelayModeHeader  = "X-Codex-Relay-Mode"
	probeChainCodexRelayRoleHeader  = "X-Codex-Relay-Role"
	probeChainCodexConnIDHeader     = "X-Codex-Conn-Id"
	probeChainCodexSpeedBytesHeader = "X-Codex-Speed-Bytes"

	probeChainRelayModeBridge     = "bridge"
	probeChainRelayModeSpeedTest  = "speed_test"
	probeChainRelayModeSpeedDebug = "speed_debug"
	probeChainBridgeRoleToNext    = "to_next"
	probeChainBridgeRoleToPrev    = "to_prev"

	probeChainDialModeForward = "forward"
	probeChainDialModeReverse = "reverse"
	probeChainDialModeNone    = "none"

	probeChainBridgeRetryMin = 1 * time.Second
	probeChainBridgeRetryMax = 15 * time.Second

	probeChainPortForwardNetworkTCP = "tcp"
	probeChainPortForwardNetworkUDP = "udp"

	probeChainPortForwardSessionIdleTTL       = 90 * time.Second
	probeChainPortForwardSessionGCInterval    = 15 * time.Second
	probeChainPortForwardDialTimeout          = 12 * time.Second
	probeChainPortForwardResponseReadDeadline = 10 * time.Second

	probeChainRelayProtocolQualityTTL          = 10 * time.Minute
	probeChainRelayProtocolNegativeTTL         = 60 * time.Second
	probeChainRelayProtocolProbeTimeout        = 6 * time.Second
	probeChainRelayProtocolSwitchMinHold       = 30 * time.Second
	probeChainRelaySpeedTestBytes              = 128 * 1024 * 1024
	probeChainRelaySpeedTestMaxBytes           = 256 * 1024 * 1024
	probeChainRelaySpeedTestTimeout            = 10 * time.Second
	probeChainRelaySpeedTestChunkBytes         = 1024 * 1024
	probeChainRelayWebSocketBufferBytes        = 512 * 1024
	probeChainRelayWebSocketWriteBatchBytes    = 1024 * 1024
	probeChainRelayWebSocketWriteQueueDepth    = 64
	probeChainRelayTCPSocketBufferBytes        = 8 * 1024 * 1024
	probeChainRelayUDPSocketBufferBytes        = 64 * 1024 * 1024
	probeChainRelayTCPKeepAlivePeriod          = 30 * time.Second
	probeChainRelayQUICInitialStreamWindow     = 128 * 1024 * 1024
	probeChainRelayQUICMaxStreamWindow         = 512 * 1024 * 1024
	probeChainRelayQUICInitialConnectionWindow = 512 * 1024 * 1024
	probeChainRelayQUICMaxConnectionWindow     = 1024 * 1024 * 1024
	probeChainRelayQUICMaxIncomingStreams      = 1024
	probeChainRelayQUICDatagramMaxPayloadBytes = 1200

	probeChainAuthPacketType        = "github_copilot_auth_request"
	probeChainAuthPacketVersion     = "2025-03-22"
	probeChainAuthFailureThreshold  = 5
	probeChainAuthBlacklistTTL      = 5 * time.Hour
	probeChainAuthFailureMinDelayMs = 200
	probeChainAuthFailureMaxDelayMs = 400
	probeChainAuthReplayTTL         = 10 * time.Minute
)

var probeChainAuthIPStateMap = struct {
	mu    sync.Mutex
	items map[string]probeChainAuthIPState
}{items: make(map[string]probeChainAuthIPState)}

var probeChainAuthTicketStore = struct {
	mu    sync.RWMutex
	items map[string]string
}{items: make(map[string]string)}

var probeChainAuthTicketNow = time.Now

var probeChainAuthReplayStore = struct {
	mu    sync.Mutex
	items map[string]time.Time
}{items: make(map[string]time.Time)}

func normalizeProbeChainLinkLayer(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "websocket-h3", "h3", "http3", "quic":
		return "websocket-h3"
	default:
		return "websocket"
	}
}

func normalizeProbeChainDialMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case probeChainDialModeReverse, "rev":
		return probeChainDialModeReverse
	case probeChainDialModeNone:
		return probeChainDialModeNone
	default:
		return probeChainDialModeForward
	}
}

func parseProbeChainUserPublicKey(raw string) (ed25519.PublicKey, error) {
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

func nextProbeChainBridgeBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return probeChainBridgeRetryMin
	}
	next := current * 2
	if next > probeChainBridgeRetryMax {
		return probeChainBridgeRetryMax
	}
	return next
}

func normalizeProbeChainBridgeRole(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case probeChainBridgeRoleToPrev:
		return probeChainBridgeRoleToPrev
	default:
		return probeChainBridgeRoleToNext
	}
}

func resolveProbeChainIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	chainID := strings.TrimSpace(r.URL.Query().Get("chain_id"))
	if chainID == "" {
		chainID = strings.TrimSpace(r.Header.Get(probeChainCodexChainIDHeader))
	}
	if chainID == "" {
		chainID = strings.TrimSpace(r.Header.Get(probeChainLegacyChainIDHeader))
	}
	return chainID
}

func readProbeChainAuthEnvelopeFromHeaders(headers http.Header, chainID string) (probeChainAuthEnvelope, error) {
	nonce, err := parseProbeChainBearerToken(headers.Get("Authorization"))
	if err != nil {
		return probeChainAuthEnvelope{}, err
	}
	env := probeChainAuthEnvelope{
		Type:       probeChainAuthPacketType,
		APIVersion: strings.TrimSpace(headers.Get(probeChainCodexVersionHeader)),
		Timestamp:  strings.TrimSpace(headers.Get(probeChainCodexAuthTimeHeader)),
		Mode:       strings.ToLower(strings.TrimSpace(headers.Get(probeChainCodexAuthModeHeader))),
		ChainID:    strings.TrimSpace(chainID),
		Nonce:      nonce,
		MAC:        strings.TrimSpace(headers.Get(probeChainCodexMACHeader)),
		AuthTicket: strings.TrimSpace(headers.Get(probeChainCodexAuthTicketHeader)),
	}
	if env.APIVersion == "" {
		env.APIVersion = probeChainAuthPacketVersion
	}
	if env.ChainID == "" {
		env.ChainID = strings.TrimSpace(headers.Get(probeChainCodexChainIDHeader))
	}
	return env, nil
}

func parseProbeChainBearerToken(raw string) (string, error) {
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

type probeChainTunedTCPListener struct {
	*net.TCPListener
}

func (l *probeChainTunedTCPListener) Accept() (net.Conn, error) {
	conn, err := l.AcceptTCP()
	if err != nil {
		return nil, err
	}
	applyProbeChainTCPConnTuning(conn)
	return conn, nil
}

func applyProbeChainTCPConnTuning(conn *net.TCPConn) {
	if conn == nil {
		return
	}
	_ = conn.SetKeepAlive(true)
	_ = conn.SetKeepAlivePeriod(probeChainRelayTCPKeepAlivePeriod)
	_ = conn.SetReadBuffer(probeChainRelayTCPSocketBufferBytes)
	_ = conn.SetWriteBuffer(probeChainRelayTCPSocketBufferBytes)
}

func newProbeChainQUICConfig(maxIncomingStreams int64) *quic.Config {
	return &quic.Config{
		InitialStreamReceiveWindow:     probeChainRelayQUICInitialStreamWindow,
		MaxStreamReceiveWindow:         probeChainRelayQUICMaxStreamWindow,
		InitialConnectionReceiveWindow: probeChainRelayQUICInitialConnectionWindow,
		MaxConnectionReceiveWindow:     probeChainRelayQUICMaxConnectionWindow,
		MaxIncomingStreams:             maxIncomingStreams,
		EnableDatagrams:                true,
	}
}

func recordProbeChainAuthNonce(chainID string, nonce string) error {
	cleanChainID := strings.TrimSpace(chainID)
	cleanNonce := strings.TrimSpace(nonce)
	if cleanChainID == "" || cleanNonce == "" {
		return errors.New("auth nonce is required")
	}
	key := cleanChainID + "\n" + cleanNonce
	now := time.Now()
	probeChainAuthReplayStore.mu.Lock()
	defer probeChainAuthReplayStore.mu.Unlock()
	for itemKey, expiresAt := range probeChainAuthReplayStore.items {
		if now.After(expiresAt) {
			delete(probeChainAuthReplayStore.items, itemKey)
		}
	}
	if expiresAt, exists := probeChainAuthReplayStore.items[key]; exists && expiresAt.After(now) {
		return errors.New("auth nonce replay detected")
	}
	probeChainAuthReplayStore.items[key] = now.Add(probeChainAuthReplayTTL)
	return nil
}

func rememberProbeChainAuthTicket(chainID string, authTicket string) {
	id := strings.TrimSpace(chainID)
	ticket := strings.TrimSpace(authTicket)
	if id == "" || ticket == "" {
		return
	}
	probeChainAuthTicketStore.mu.Lock()
	probeChainAuthTicketStore.items[id] = ticket
	for key, value := range probeChainAuthTicketStore.items {
		if strings.EqualFold(strings.TrimSpace(key), id) && key != id {
			delete(probeChainAuthTicketStore.items, key)
			probeChainAuthTicketStore.items[id] = value
		}
	}
	probeChainAuthTicketStore.mu.Unlock()
}

func lookupProbeChainAuthTicket(chainID string) string {
	id := strings.TrimSpace(chainID)
	if id == "" {
		return ""
	}
	probeChainAuthTicketStore.mu.RLock()
	ticket := strings.TrimSpace(probeChainAuthTicketStore.items[id])
	probeChainAuthTicketStore.mu.RUnlock()
	if ticket != "" {
		return ticket
	}
	lower := strings.ToLower(id)
	probeChainAuthTicketStore.mu.Lock()
	for key, value := range probeChainAuthTicketStore.items {
		if strings.EqualFold(strings.TrimSpace(key), lower) {
			probeChainAuthTicketStore.items[id] = value
			ticket = strings.TrimSpace(value)
			break
		}
	}
	probeChainAuthTicketStore.mu.Unlock()
	return ticket
}

func applyProbeChainAuthTicketHeader(headers http.Header, chainID string) {
	if headers == nil {
		return
	}
	if ticket := lookupProbeChainAuthTicket(chainID); ticket != "" {
		headers.Set(probeChainCodexAuthTicketHeader, ticket)
	}
}

type probeChainUserAuthTicketPayload struct {
	Version       string `json:"version,omitempty"`
	ChainID       string `json:"chain_id,omitempty"`
	UserPublicKey string `json:"user_public_key,omitempty"`
	IssuedAt      string `json:"issued_at,omitempty"`
}

func verifyProbeChainAuthTicketIssuedAt(raw string, now time.Time) error {
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

func buildProbeChainHMAC(secret string, chainID string, nonce string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(strings.TrimSpace(chainID)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(nonce)))
	return hex.EncodeToString(mac.Sum(nil))
}

func resolveProbeChainSourceIPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			if ip := normalizeProbeChainIP(strings.TrimSpace(parts[0])); ip != "" {
				return ip
			}
		}
	}
	return resolveProbeChainSourceIPFromAddrString(strings.TrimSpace(r.RemoteAddr))
}

func resolveProbeChainSourceIPFromAddrString(raw string) string {
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return normalizeProbeChainIP(host)
	}
	return normalizeProbeChainIP(raw)
}

func normalizeProbeChainIP(raw string) string {
	clean := strings.TrimSpace(strings.Trim(raw, "[]"))
	if clean == "" {
		return ""
	}
	if parsed := net.ParseIP(clean); parsed != nil {
		return parsed.String()
	}
	return ""
}

func delayProbeChainAuthFailure() {
	delay := probeChainAuthFailureDelay()
	if delay > 0 {
		time.Sleep(delay)
	}
}

func probeChainAuthFailureDelay() time.Duration {
	minDelay := probeChainAuthFailureMinDelayMs
	maxDelay := probeChainAuthFailureMaxDelayMs
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

func isProbeChainAuthIPBlacklisted(ip string) (bool, time.Time) {
	target := strings.TrimSpace(ip)
	if target == "" {
		return false, time.Time{}
	}
	now := time.Now()
	probeChainAuthIPStateMap.mu.Lock()
	defer probeChainAuthIPStateMap.mu.Unlock()
	state, ok := probeChainAuthIPStateMap.items[target]
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
		delete(probeChainAuthIPStateMap.items, target)
	}
	return false, time.Time{}
}

func recordProbeChainAuthFailure(ip string) (failures int, blacklisted bool, until time.Time) {
	target := strings.TrimSpace(ip)
	if target == "" {
		return 0, false, time.Time{}
	}
	now := time.Now()
	probeChainAuthIPStateMap.mu.Lock()
	defer probeChainAuthIPStateMap.mu.Unlock()
	state := probeChainAuthIPStateMap.items[target]
	if state.Manual {
		return probeChainAuthFailureThreshold, true, time.Time{}
	}
	if !state.BlacklistedTil.IsZero() && !now.Before(state.BlacklistedTil) {
		state.BlacklistedTil = time.Time{}
		state.FailedAttempts = 0
	}
	state.FailedAttempts++
	failures = state.FailedAttempts
	if state.FailedAttempts >= probeChainAuthFailureThreshold {
		state.BlacklistedTil = now.Add(probeChainAuthBlacklistTTL)
		state.FailedAttempts = 0
		blacklisted = true
		until = state.BlacklistedTil
		failures = probeChainAuthFailureThreshold
	}
	probeChainAuthIPStateMap.items[target] = state
	return failures, blacklisted, until
}

func resetProbeChainAuthFailure(ip string) {
	target := strings.TrimSpace(ip)
	if target == "" {
		return
	}
	probeChainAuthIPStateMap.mu.Lock()
	defer probeChainAuthIPStateMap.mu.Unlock()
	state, ok := probeChainAuthIPStateMap.items[target]
	if ok && state.Manual {
		state.FailedAttempts = 0
		probeChainAuthIPStateMap.items[target] = state
		return
	}
	delete(probeChainAuthIPStateMap.items, target)
}

func resolveProbeChainAuthBlacklistPath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, "probe_chain_auth_blacklist.json"), nil
}

func loadProbeChainAuthBlacklistFromDisk() error {
	path, err := resolveProbeChainAuthBlacklistPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return persistProbeChainAuthBlacklistManualIPs(nil)
		}
		return err
	}
	var payload probeChainAuthBlacklistFile
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
	}
	return setProbeChainAuthBlacklistManualIPs(payload.IPs, false)
}

func persistProbeChainAuthBlacklistManualIPs(ips []string) error {
	path, err := resolveProbeChainAuthBlacklistPath()
	if err != nil {
		return err
	}
	payload := probeChainAuthBlacklistFile{IPs: normalizeProbeChainAuthBlacklistIPs(ips)}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func setProbeChainAuthBlacklistManualIPs(ips []string, persist bool) error {
	normalized := normalizeProbeChainAuthBlacklistIPs(ips)
	probeChainAuthIPStateMap.mu.Lock()
	next := make(map[string]probeChainAuthIPState, len(normalized))
	for _, ip := range normalized {
		next[ip] = probeChainAuthIPState{Manual: true}
	}
	probeChainAuthIPStateMap.items = next
	probeChainAuthIPStateMap.mu.Unlock()
	if persist {
		return persistProbeChainAuthBlacklistManualIPs(normalized)
	}
	return nil
}

func normalizeProbeChainAuthBlacklistIPs(ips []string) []string {
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

func parseProbeChainAuthBlacklistContent(content string) ([]string, error) {
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
	return normalizeProbeChainAuthBlacklistIPs(ips), nil
}

func listProbeChainAuthBlacklistEntries() []probeChainAuthBlacklistEntry {
	now := time.Now()
	probeChainAuthIPStateMap.mu.Lock()
	defer probeChainAuthIPStateMap.mu.Unlock()
	items := make([]probeChainAuthBlacklistEntry, 0, len(probeChainAuthIPStateMap.items))
	for ip, state := range probeChainAuthIPStateMap.items {
		if state.Manual {
			items = append(items, probeChainAuthBlacklistEntry{IP: ip, Manual: true})
			continue
		}
		if state.BlacklistedTil.IsZero() {
			continue
		}
		if !now.Before(state.BlacklistedTil) {
			delete(probeChainAuthIPStateMap.items, ip)
			continue
		}
		items = append(items, probeChainAuthBlacklistEntry{
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

func probeChainAuthBlacklistContent() string {
	entries := listProbeChainAuthBlacklistEntries()
	lines := make([]string, 0, len(entries))
	for _, item := range entries {
		lines = append(lines, item.IP)
	}
	return strings.Join(lines, "\n")
}
