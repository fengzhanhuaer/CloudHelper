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
	"log"
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
	Auth       *probeRouteAuthPayloadBody `json:"auth,omitempty"`
	Mode       string                     `json:"mode,omitempty"`
	RouteID    string                     `json:"route_id,omitempty"`
	Nonce      string                     `json:"nonce,omitempty"`
	Signature  string                     `json:"signature,omitempty"`
	MAC        string                     `json:"mac,omitempty"`
	AuthTicket string                     `json:"auth_ticket,omitempty"`
	Method     string                     `json:"method,omitempty"`
	Path       string                     `json:"path,omitempty"`
	RelayRole  string                     `json:"relay_role,omitempty"`
	SourceNode string                     `json:"source_node_id,omitempty"`
}

type probeRouteAuthPayloadBody struct {
	Mode       string `json:"mode,omitempty"`
	RouteID    string `json:"route_id,omitempty"`
	Nonce      string `json:"nonce,omitempty"`
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
	probeRouteCodexVersionHeader    = "X-Codex-Api-Version"
	probeRouteCodexRelayModeHeader  = "X-Codex-Relay-Mode"
	probeRouteCodexRelayRoleHeader  = "X-Codex-Relay-Role"
	probeRouteCodexConnIDHeader     = "X-Codex-Conn-Id"
	probeRouteCodexSourceNodeHeader = "X-Codex-Source-Node-Id"

	probeRouteRelayModeBridge     = "bridge"
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
	probeRouteRelayWebSocketBufferBytes        = 512 * 1024
	probeRouteRelayWebSocketWriteBatchBytes    = 1024 * 1024
	probeRouteRelayWebSocketWriteQueueDepth    = 64
	probeRouteRelayWebSocketSlowWriteTrace     = 200 * time.Millisecond
	probeRouteRelayTCPSocketBufferBytes        = 8 * 1024 * 1024
	probeRouteRelayUDPSocketBufferBytes        = 64 * 1024 * 1024
	probeRouteRelayTCPKeepAlivePeriod          = 30 * time.Second
	probeRouteRelayQUICInitialStreamWindow     = 128 * 1024 * 1024
	probeRouteRelayQUICMaxStreamWindow         = 512 * 1024 * 1024
	probeRouteRelayQUICInitialConnectionWindow = 512 * 1024 * 1024
	probeRouteRelayQUICMaxConnectionWindow     = 1024 * 1024 * 1024
	probeRouteRelayQUICMaxIncomingStreams      = 1024
	probeRouteRelayQUICDatagramMaxPayloadBytes = 1200
	probeRouteAuthReplayFileName               = "probe_vroute_auth_replay.json"

	probeRouteAuthPacketType        = "github_copilot_auth_request"
	probeRouteAuthPacketVersion     = "2025-03-22"
	probeRouteAuthFailureThreshold  = 5
	probeRouteAuthBlacklistTTL      = 5 * time.Hour
	probeRouteAuthFailureMinDelayMs = 200
	probeRouteAuthFailureMaxDelayMs = 400
)

var probeRouteAuthIPStateMap = struct {
	mu    sync.Mutex
	items map[string]probeRouteAuthIPState
}{items: make(map[string]probeRouteAuthIPState)}

var probeRouteAuthTicketStore = struct {
	mu    sync.RWMutex
	items map[string]string
}{items: make(map[string]string)}

var probeRouteTLSPinStore = struct {
	mu    sync.RWMutex
	items map[string]string
}{items: make(map[string]string)}

var probeRouteAuthReplayStore = struct {
	mu     sync.Mutex
	items  map[string]struct{}
	loaded bool
}{items: make(map[string]struct{})}

func normalizeProbeRouteRouteLayer(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto", "default":
		return "auto"
	case "websocket-h3", "ws-h3", "h3-websocket", "h3-ws", "h3", "http3", "quic":
		return "websocket-h3"
	case "websocket", "ws", "wss", "http2", "h2", "http", "https":
		return "websocket"
	default:
		return "auto"
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

func readProbeRouteAuthEnvelopeFromHeaders(headers http.Header, routeID string, method string, requestPath string) (probeRouteAuthEnvelope, error) {
	nonce, err := parseProbeRouteBearerToken(headers.Get("Authorization"))
	if err != nil {
		return probeRouteAuthEnvelope{}, err
	}
	env := probeRouteAuthEnvelope{
		Type:       probeRouteAuthPacketType,
		APIVersion: strings.TrimSpace(headers.Get(probeRouteCodexVersionHeader)),
		Mode:       strings.ToLower(strings.TrimSpace(headers.Get(probeRouteCodexAuthModeHeader))),
		RouteID:    strings.TrimSpace(routeID),
		Nonce:      nonce,
		MAC:        strings.TrimSpace(headers.Get(probeRouteCodexMACHeader)),
		AuthTicket: strings.TrimSpace(headers.Get(probeRouteCodexAuthTicketHeader)),
		Method:     strings.ToUpper(strings.TrimSpace(method)),
		Path:       strings.TrimSpace(requestPath),
		RelayRole:  normalizeProbeRouteBridgeRole(headers.Get(probeRouteCodexRelayRoleHeader)),
		SourceNode: normalizeProbeRouteNodeID(headers.Get(probeRouteCodexSourceNodeHeader)),
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
	applyProbeRouteTCPConnTuningWithContext(conn, "listener_accept")
}

func applyProbeRouteTCPConnTuningWithContext(conn *net.TCPConn, context string) {
	if conn == nil {
		return
	}
	_ = conn.SetKeepAlive(true)
	_ = conn.SetKeepAlivePeriod(probeRouteRelayTCPKeepAlivePeriod)
	readErr := conn.SetReadBuffer(probeRouteRelayTCPSocketBufferBytes)
	writeErr := conn.SetWriteBuffer(probeRouteRelayTCPSocketBufferBytes)
	actualRead, actualWrite, snapshotErr := probeRouteTCPConnSocketBufferSnapshot(conn)
	forceAttempted := false
	var forceReadErr error
	var forceWriteErr error
	if snapshotErr == nil && probeRouteTCPConnSocketBufferBelowRequested(actualRead, actualWrite, probeRouteRelayTCPSocketBufferBytes) {
		forceAttempted, forceReadErr, forceWriteErr = probeRouteTCPConnForceSocketBuffer(conn, probeRouteRelayTCPSocketBufferBytes, probeRouteRelayTCPSocketBufferBytes)
		if forceAttempted {
			actualRead, actualWrite, snapshotErr = probeRouteTCPConnSocketBufferSnapshot(conn)
		}
	}
	effectiveRead := probeRouteTCPConnSocketBufferEffectiveBytes(actualRead)
	effectiveWrite := probeRouteTCPConnSocketBufferEffectiveBytes(actualWrite)
	hint := probeRouteTCPConnSocketBufferTuneHint(probeRouteRelayTCPSocketBufferBytes, effectiveRead, effectiveWrite, snapshotErr, forceAttempted, forceReadErr, forceWriteErr)
	if !probeRouteTCPConnTuningShouldLog(hint, readErr, writeErr, snapshotErr, forceReadErr, forceWriteErr) {
		return
	}
	log.Printf(
		"probe route tcp socket buffer tuned: context=%s local=%s remote=%s requested_read=%d requested_write=%d actual_read=%d actual_write=%d effective_read=%d effective_write=%d force_attempted=%t force_read_err=%v force_write_err=%v set_read_err=%v set_write_err=%v snapshot_err=%v hint=%q",
		strings.TrimSpace(context),
		conn.LocalAddr(),
		conn.RemoteAddr(),
		probeRouteRelayTCPSocketBufferBytes,
		probeRouteRelayTCPSocketBufferBytes,
		actualRead,
		actualWrite,
		effectiveRead,
		effectiveWrite,
		forceAttempted,
		forceReadErr,
		forceWriteErr,
		readErr,
		writeErr,
		snapshotErr,
		hint,
	)
}

func probeRouteTCPConnTuningShouldLog(hint string, errs ...error) bool {
	if !strings.EqualFold(strings.TrimSpace(hint), "ok") {
		return true
	}
	for _, err := range errs {
		if err != nil {
			return true
		}
	}
	return false
}

func probeRouteTCPConnSocketBufferBelowRequested(actualRead int, actualWrite int, requested int) bool {
	if requested <= 0 {
		return false
	}
	return probeRouteTCPConnSocketBufferEffectiveBytes(actualRead) < requested ||
		probeRouteTCPConnSocketBufferEffectiveBytes(actualWrite) < requested
}

func probeRouteTCPConnSocketBufferEffectiveBytes(actual int) int {
	if actual <= 0 {
		return actual
	}
	scale := probeRouteTCPConnSocketBufferKernelScale()
	if scale <= 1 {
		return actual
	}
	return actual / scale
}

func probeRouteTCPConnSocketBufferTuneHint(requested int, effectiveRead int, effectiveWrite int, snapshotErr error, forceAttempted bool, forceReadErr error, forceWriteErr error) string {
	if snapshotErr != nil {
		return "snapshot_failed"
	}
	if requested <= 0 || (effectiveRead >= requested && effectiveWrite >= requested) {
		return "ok"
	}
	if forceAttempted && (forceReadErr != nil || forceWriteErr != nil) {
		return "socket_buffer_below_request; linux may require CAP_NET_ADMIN or higher net.core.rmem_max/net.core.wmem_max"
	}
	return "socket_buffer_below_request; raise OS socket buffer limits"
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

type probeRouteAuthReplayFile struct {
	Keys []string `json:"keys"`
}

func recordProbeRouteAuthNonce(routeID string, authTicket string, nonce string) error {
	cleanRouteID := strings.TrimSpace(routeID)
	cleanTicket := strings.TrimSpace(authTicket)
	cleanNonce := strings.TrimSpace(nonce)
	if cleanRouteID == "" || cleanTicket == "" || cleanNonce == "" {
		return errors.New("auth nonce is required")
	}
	ticketHash := sha256.Sum256([]byte(cleanTicket))
	nonceHash := sha256.Sum256([]byte(cleanNonce))
	routePrefix := cleanRouteID + "\n"
	ticketPrefix := routePrefix + hex.EncodeToString(ticketHash[:]) + "\n"
	key := ticketPrefix + hex.EncodeToString(nonceHash[:])
	probeRouteAuthReplayStore.mu.Lock()
	defer probeRouteAuthReplayStore.mu.Unlock()
	if err := loadProbeRouteAuthReplayStoreLocked(); err != nil {
		return fmt.Errorf("load auth replay store: %w", err)
	}
	if _, exists := probeRouteAuthReplayStore.items[key]; exists {
		return errors.New("auth nonce replay detected")
	}
	for itemKey := range probeRouteAuthReplayStore.items {
		if strings.HasPrefix(itemKey, routePrefix) && !strings.HasPrefix(itemKey, ticketPrefix) {
			delete(probeRouteAuthReplayStore.items, itemKey)
		}
	}
	probeRouteAuthReplayStore.items[key] = struct{}{}
	if err := persistProbeRouteAuthReplayStoreLocked(); err != nil {
		return fmt.Errorf("persist auth replay store: %w", err)
	}
	return nil
}

func loadProbeRouteAuthReplayStoreLocked() error {
	if probeRouteAuthReplayStore.loaded {
		return nil
	}
	dataDir, err := resolveDataDir()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, probeRouteAuthReplayFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			probeRouteAuthReplayStore.loaded = true
			return nil
		}
		return err
	}
	var payload probeRouteAuthReplayFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	for _, key := range payload.Keys {
		if value := strings.TrimSpace(key); value != "" {
			probeRouteAuthReplayStore.items[value] = struct{}{}
		}
	}
	probeRouteAuthReplayStore.loaded = true
	return nil
}

func persistProbeRouteAuthReplayStoreLocked() error {
	dataDir, err := resolveDataDir()
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(probeRouteAuthReplayStore.items))
	for key := range probeRouteAuthReplayStore.items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return persistProbeLocalJSONFile(filepath.Join(dataDir, probeRouteAuthReplayFileName), probeRouteAuthReplayFile{Keys: keys})
}

func resetProbeRouteAuthReplayMemoryForTest() {
	probeRouteAuthReplayStore.mu.Lock()
	probeRouteAuthReplayStore.items = make(map[string]struct{})
	probeRouteAuthReplayStore.loaded = false
	probeRouteAuthReplayStore.mu.Unlock()
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
	ClientEntryID string `json:"client_entry_id,omitempty"`
	UserID        string `json:"user_id,omitempty"`
	UserPublicKey string `json:"user_public_key,omitempty"`
	FromNodeID    string `json:"from_node_id,omitempty"`
	ToNodeID      string `json:"to_node_id,omitempty"`
	FromTLSSPKI   string `json:"from_tls_spki_sha256,omitempty"`
	ToTLSSPKI     string `json:"to_tls_spki_sha256,omitempty"`
	TicketID      string `json:"ticket_id,omitempty"`
	IssuedAt      string `json:"issued_at,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

func normalizeProbeRouteTLSSPKI(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if len(value) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

func rememberProbeRouteTLSPin(routeID string, pin string) {
	routeID = strings.TrimSpace(routeID)
	pin = normalizeProbeRouteTLSSPKI(pin)
	if routeID == "" {
		return
	}
	probeRouteTLSPinStore.mu.Lock()
	if pin == "" {
		delete(probeRouteTLSPinStore.items, routeID)
	} else {
		probeRouteTLSPinStore.items[routeID] = pin
	}
	probeRouteTLSPinStore.mu.Unlock()
}

func lookupProbeRouteTLSPin(routeID string) string {
	probeRouteTLSPinStore.mu.RLock()
	pin := probeRouteTLSPinStore.items[strings.TrimSpace(routeID)]
	probeRouteTLSPinStore.mu.RUnlock()
	return normalizeProbeRouteTLSSPKI(pin)
}

func buildProbeRouteHMAC(secret string, routeID string, nonce string, method string, requestPath string, sourceNodeID string, relayRole string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	canonical := strings.Join([]string{
		strings.TrimSpace(routeID),
		strings.TrimSpace(nonce),
		strings.ToUpper(strings.TrimSpace(method)),
		strings.TrimSpace(requestPath),
		normalizeProbeRouteNodeID(sourceNodeID),
		normalizeProbeRouteBridgeRole(relayRole),
	}, "\n")
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

func resolveProbeRouteSourceIPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
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
