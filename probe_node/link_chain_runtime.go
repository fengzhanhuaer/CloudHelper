package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
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

	"github.com/gorilla/websocket"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

type probeChainRuntimeConfig struct {
	chainID                 string
	chainType               string
	name                    string
	userID                  string
	userPublicKey           ed25519.PublicKey
	rawPublicKey            string
	secret                  string
	authTicket              string
	role                    string
	listenHost              string
	listenPort              int
	linkLayer               string
	nextLinkLayer           string
	nextDialMode            string
	nextNodeID              string
	nextHost                string
	nextPort                int
	nextPreserveRelayDomain bool
	prevHost                string
	prevPort                int
	prevPreserveRelayDomain bool
	prevLinkLayer           string
	prevDialMode            string
	prevNodeID              string
	requireUserAuth         bool
	nextAuthMode            string
	identity                nodeIdentity
	controllerURL           string
}

type probeChainBridgeSession struct {
	ID          string
	Session     *probeChainFrameSession
	BridgeRole  string
	RemoteAddr  string
	ConnectedAt time.Time
}

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

type probeChainRuntime struct {
	cfg                probeChainRuntimeConfig
	relayListenAddr    string
	downstreamSessions map[string]*probeChainBridgeSession
	upstreamSessions   map[string]*probeChainBridgeSession
	bridgeMu           sync.Mutex
	bridgeSeq          uint64
	stopCh             chan struct{}
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

type probeChainTunnelOpenRequest struct {
	Type             string                       `json:"type"`
	RequestID        string                       `json:"request_id,omitempty"`
	Scope            string                       `json:"scope,omitempty"`
	Network          string                       `json:"network"`
	Address          string                       `json:"address"`
	FlowID           string                       `json:"flow_id,omitempty"`
	ResumeToken      string                       `json:"resume_token,omitempty"`
	ResumeEpoch      uint64                       `json:"resume_epoch,omitempty"`
	ReadOffset       uint64                       `json:"read_offset,omitempty"`
	WriteOffset      uint64                       `json:"write_offset,omitempty"`
	SessionID        string                       `json:"session_id,omitempty"`
	AppProtocol      string                       `json:"app_protocol,omitempty"`
	Priority         string                       `json:"priority,omitempty"`
	ResumePolicy     string                       `json:"resume_policy,omitempty"`
	LatencySensitive bool                         `json:"latency_sensitive,omitempty"`
	AssociationV2    *probeChainAssociationV2Meta `json:"association_v2,omitempty"`
	SpeedBytes       int64                        `json:"speed_bytes,omitempty"`
	PingBytes        int64                        `json:"ping_bytes,omitempty"`
}

type probeChainTunnelOpenResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	FlowID      string `json:"flow_id,omitempty"`
	ResumeToken string `json:"resume_token,omitempty"`
	ResumeEpoch uint64 `json:"resume_epoch,omitempty"`
	ReadOffset  uint64 `json:"read_offset,omitempty"`
	WriteOffset uint64 `json:"write_offset,omitempty"`
}

type probeChainTunnelDNSResolveResponse struct {
	Addrs []string `json:"addrs,omitempty"`
	TTL   int      `json:"ttl,omitempty"`
	Error string   `json:"error,omitempty"`
}

type probeChainNextHop struct {
	Writer  io.WriteCloser
	Reader  io.ReadCloser
	CloseFn func() error
	Monitor probeChainFrameStreamMonitor
}

type probeChainRelayDirectionResult struct {
	Bytes int64
	Err   error
}

type probeChainBidirectionalRelayResult struct {
	LeftToRight probeChainRelayDirectionResult
	RightToLeft probeChainRelayDirectionResult
	Duration    time.Duration
}

var probeChainRuntimeState = struct {
	mu       sync.Mutex
	runtimes map[string]*probeChainRuntime
}{runtimes: make(map[string]*probeChainRuntime)}

type probeChainSharedRelayServer struct {
	listenAddr    string
	httpsServer   *http.Server
	http3Server   *http3.Server
	udpPacketConn net.PacketConn
	chainIDs      map[string]struct{}
	refCount      int
}

var probeChainSharedRelayState = struct {
	mu      sync.Mutex
	servers map[string]*probeChainSharedRelayServer
}{servers: make(map[string]*probeChainSharedRelayServer)}

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
	probeChainRelayModePrepare    = "prepare"
	probeChainRelayModeSpeedTest  = "speed_test"
	probeChainRelayModeSpeedDebug = "speed_debug"
	probeChainRelayModePingPong   = "ping_pong"
	probeChainBridgeRoleToNext    = "to_next"
	probeChainBridgeRoleToPrev    = "to_prev"

	probeChainDialModeForward = "forward"
	probeChainDialModeReverse = "reverse"
	probeChainDialModeNone    = "none"

	probeChainBridgeRetryMin = 1 * time.Second
	probeChainBridgeRetryMax = 15 * time.Second

	probeChainDownstreamOpenTimeout = 30 * time.Second

	probeChainPortForwardNetworkTCP  = "tcp"
	probeChainPortForwardNetworkUDP  = "udp"
	probeChainPortForwardNetworkBoth = "both"

	probeChainPortForwardSessionIdleTTL        = 90 * time.Second
	probeChainPortForwardSessionGCInterval     = 15 * time.Second
	probeChainPortForwardDialTimeout           = 12 * time.Second
	probeChainPortForwardResponseReadDeadline  = 10 * time.Second
	probeChainPortForwardPreconnectIdleTTL     = 60 * time.Second
	probeChainRelayProtocolQualityTTL          = 10 * time.Minute
	probeChainRelayProtocolNegativeTTL         = 60 * time.Second
	probeChainRelayProtocolProbeTimeout        = 6 * time.Second
	probeChainRelayProtocolSwitchMinHold       = 30 * time.Second
	probeChainRelaySpeedTestBytes              = 128 * 1024 * 1024
	probeChainRelaySpeedTestMaxBytes           = 256 * 1024 * 1024
	probeChainRelaySpeedTestTimeout            = 10 * time.Second
	probeChainRelaySpeedTestChunkBytes         = 1024 * 1024
	probeChainRelayIOCopyBufferBytes           = 1024 * 1024
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
}{
	items: make(map[string]probeChainAuthIPState),
}

var probeChainAuthTicketStore = struct {
	mu    sync.RWMutex
	items map[string]string
}{
	items: make(map[string]string),
}

var probeChainAuthTicketNow = time.Now

var probeChainAuthReplayStore = struct {
	mu    sync.Mutex
	items map[string]time.Time
}{
	items: make(map[string]time.Time),
}

var probeChainCopyBufferPool = sync.Pool{
	New: func() any {
		return make([]byte, probeChainRelayIOCopyBufferBytes)
	},
}

type probeChainFrameStreamMonitor struct {
	Session             *probeChainFrameSession
	SessionID           string
	SessionRole         string
	SessionStreamsOpen  int
	SessionStreamsAfter int
	OpenLatency         time.Duration
	PingStats           probeChainFramePingStats
}

// probeChainPreconnectPhase distinguishes a relay/link failure (a real transport
// problem) from a target-dial failure (the target is unreachable, link is fine).
type probeChainPreconnectPhase string

const (
	probeChainPreconnectPhaseTransport probeChainPreconnectPhase = "transport"
)

type probeChainPreconnectError struct {
	phase probeChainPreconnectPhase
	err   error
}

func (e *probeChainPreconnectError) Error() string { return e.err.Error() }
func (e *probeChainPreconnectError) Unwrap() error { return e.err }

var probeChainFrameBufferPool = sync.Pool{
	New: func() any {
		return make([]byte, probeChainFrameMaxBytes)
	},
}

func probeChainRuntimeConfigIsVirtualRouter(cfg probeChainRuntimeConfig) bool {
	return strings.EqualFold(strings.TrimSpace(cfg.chainType), "virtual_router") || isProbeVirtualRouterRuntimeChainID(cfg.chainID)
}

func normalizeProbeChainRole(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "entry":
		return "entry"
	case "relay":
		return "relay"
	case "exit":
		return "exit"
	case "entry_exit":
		return "entry_exit"
	default:
		return ""
	}
}

func normalizeProbeChainAuthMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "secret", "hmac":
		return "secret"
	case "proxy":
		return "proxy"
	default:
		return "none"
	}
}

func normalizeProbeChainLinkLayer(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto", "default", "http", "http2", "h2", "http3", "h3":
		return ""
	case "websocket", "ws", "wss":
		return "websocket"
	case "websocket-h3", "ws-h3", "h3-websocket", "h3-ws":
		return "websocket-h3"
	default:
		return ""
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

func buildProbeChainRuntimeConfigFromControl(cmd probeControlMessage) (probeChainRuntimeConfig, error) {
	chainID := strings.TrimSpace(cmd.ChainID)
	if chainID == "" {
		return probeChainRuntimeConfig{}, fmt.Errorf("chain_id is required")
	}
	role := normalizeProbeChainRole(cmd.Role)
	if role == "" {
		role = "relay"
	}
	listenHost := normalizeProbeLinkTestListenHost(cmd.ListenHost)
	listenPort := normalizeProbeLinkTestPort(cmd.ListenPort)
	if listenPort <= 0 {
		listenPort = normalizeProbeLinkTestPort(cmd.InternalPort)
	}
	if listenPort <= 0 {
		return probeChainRuntimeConfig{}, fmt.Errorf("listen_port must be between 1 and 65535")
	}
	nextHost := strings.TrimSpace(cmd.NextHost)
	nextPort := normalizeProbeLinkTestPort(cmd.NextPort)
	linkLayer := normalizeProbeChainLinkLayer(cmd.LinkLayer)
	nextLinkLayer := normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(cmd.NextLinkLayer), strings.TrimSpace(cmd.LinkLayer)))
	nextDialMode := normalizeProbeChainDialMode(strings.TrimSpace(cmd.NextDialMode))
	prevHost := strings.TrimSpace(cmd.PrevHost)
	prevPort := normalizeProbeLinkTestPort(cmd.PrevPort)
	prevLinkLayer := normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(cmd.PrevLinkLayer), strings.TrimSpace(cmd.LinkLayer)))
	prevDialMode := normalizeProbeChainDialMode(strings.TrimSpace(cmd.PrevDialMode))
	secret := strings.TrimSpace(cmd.LinkSecret)
	requireUserAuth := cmd.RequireUserAuth
	nextAuthMode := normalizeProbeChainAuthMode(cmd.NextAuthMode)
	if nextAuthMode != "proxy" {
		if nextHost == "" || nextPort <= 0 {
			return probeChainRuntimeConfig{}, fmt.Errorf("next_host and next_port are required")
		}
		if nextDialMode == probeChainDialModeNone {
			nextDialMode = probeChainDialModeForward
		}
	} else {
		nextDialMode = probeChainDialModeNone
	}
	if prevHost == "" || prevPort <= 0 {
		prevDialMode = probeChainDialModeNone
	}
	if prevDialMode == probeChainDialModeReverse {
		if prevHost == "" || prevPort <= 0 {
			return probeChainRuntimeConfig{}, fmt.Errorf("prev_host and prev_port are required when prev_dial_mode=reverse")
		}
	}

	cfg := probeChainRuntimeConfig{
		chainID:                 chainID,
		chainType:               strings.TrimSpace(cmd.ChainType),
		name:                    strings.TrimSpace(cmd.Name),
		userID:                  strings.TrimSpace(cmd.UserID),
		rawPublicKey:            strings.TrimSpace(cmd.UserPublicKey),
		secret:                  secret,
		authTicket:              strings.TrimSpace(cmd.AuthTicket),
		role:                    role,
		listenHost:              listenHost,
		listenPort:              listenPort,
		linkLayer:               linkLayer,
		nextLinkLayer:           nextLinkLayer,
		nextDialMode:            nextDialMode,
		nextHost:                nextHost,
		nextPort:                nextPort,
		nextPreserveRelayDomain: isProbeChainControlCFEntry(cmd),
		prevHost:                prevHost,
		prevPort:                prevPort,
		prevPreserveRelayDomain: isProbeChainControlCFEntry(cmd),
		prevLinkLayer:           prevLinkLayer,
		prevDialMode:            prevDialMode,
		requireUserAuth:         requireUserAuth,
		nextAuthMode:            nextAuthMode,
	}

	if requireUserAuth {
		pub, err := parseProbeChainUserPublicKey(cfg.rawPublicKey)
		if err != nil {
			return probeChainRuntimeConfig{}, fmt.Errorf("parse user_public_key failed: %w", err)
		}
		cfg.userPublicKey = pub
	} else if strings.TrimSpace(secret) == "" {
		return probeChainRuntimeConfig{}, fmt.Errorf("link_secret is required for relay/exit auth")
	}

	if cfg.nextAuthMode == "secret" && strings.TrimSpace(secret) == "" {
		return probeChainRuntimeConfig{}, fmt.Errorf("link_secret is required when next_auth_mode=secret")
	}

	return cfg, nil
}

func isProbeChainControlCFEntry(cmd probeControlMessage) bool {
	for _, value := range []string{
		cmd.ClientEntryType,
		cmd.ClientEntryID,
		cmd.ChainID,
		cmd.Name,
	} {
		clean := strings.ToLower(strings.TrimSpace(value))
		if clean == "cf" || strings.HasSuffix(clean, "_cf") {
			return true
		}
	}
	return false
}

func parseProbeChainUserPublicKey(raw string) (ed25519.PublicKey, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty public key")
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
		// Support base64-encoded PKIX DER public key (e.g. "MCowBQYDK2VwAyEA...").
		if pubAny, parseErr := x509.ParsePKIXPublicKey(rawBytes); parseErr == nil {
			if pub, ok := pubAny.(ed25519.PublicKey); ok {
				return pub, nil
			}
		}
	}
	if rawBytes, err := hex.DecodeString(trimmed); err == nil {
		if len(rawBytes) == ed25519.PublicKeySize {
			return ed25519.PublicKey(rawBytes), nil
		}
	}

	return nil, fmt.Errorf("unsupported public key format")
}

func startProbeChainRuntime(cfg probeChainRuntimeConfig) (*probeChainRuntime, error) {
	if !probeChainRuntimeConfigIsVirtualRouter(cfg) {
		_ = stopProbeChainRuntime(cfg.chainID, "legacy probe chain runtime removed")
		return nil, fmt.Errorf("probe chain runtime only supports virtual_router: chain=%s", strings.TrimSpace(cfg.chainID))
	}
	_ = stopProbeChainRuntime(cfg.chainID, "restart before apply")
	if err := ensureProbeChainRuntimeAuthTicket(&cfg); err != nil {
		return nil, err
	}
	rememberProbeChainAuthTicket(cfg.chainID, cfg.authTicket)

	rt := &probeChainRuntime{
		cfg:                cfg,
		downstreamSessions: make(map[string]*probeChainBridgeSession),
		upstreamSessions:   make(map[string]*probeChainBridgeSession),
		stopCh:             make(chan struct{}),
	}

	if err := startProbeChainPublicRelayServer(rt); err != nil {
		close(rt.stopCh)
		rt.closeRuntimeResources()
		return nil, err
	}

	probeChainRuntimeState.mu.Lock()
	probeChainRuntimeState.runtimes[cfg.chainID] = rt
	probeChainRuntimeState.mu.Unlock()
	startProbeChainBridgeWorkers(rt)

	nextTarget := "proxy"
	if cfg.nextAuthMode != "proxy" {
		nextTarget = net.JoinHostPort(cfg.nextHost, strconv.Itoa(cfg.nextPort))
	}
	log.Printf(
		"probe chain runtime started: chain=%s role=%s listen=%s layer=%s next_mode=%s next_dial=%s next=%s prev_dial=%s",
		cfg.chainID,
		cfg.role,
		net.JoinHostPort(cfg.listenHost, strconv.Itoa(cfg.listenPort)),
		normalizeProbeChainLinkLayer(cfg.linkLayer),
		cfg.nextAuthMode,
		cfg.nextDialMode,
		nextTarget,
		cfg.prevDialMode,
	)
	return rt, nil
}

func ensureProbeChainRuntimeAuthTicket(cfg *probeChainRuntimeConfig) error {
	if cfg == nil || !cfg.requireUserAuth {
		return nil
	}
	if strings.TrimSpace(cfg.authTicket) != "" {
		return nil
	}
	if ticket := lookupProbeChainAuthTicket(cfg.chainID); ticket != "" {
		cfg.authTicket = ticket
		return nil
	}
	if isProbeVirtualRouterRuntimeChainID(cfg.chainID) || strings.EqualFold(strings.TrimSpace(cfg.chainType), "virtual_router") {
		baseURL := strings.TrimSpace(cfg.controllerURL)
		if baseURL == "" || strings.TrimSpace(cfg.identity.NodeID) == "" || strings.TrimSpace(cfg.identity.Secret) == "" {
			return fmt.Errorf("auth_ticket is required when require_user_auth=true")
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeLinkChainsSyncFetchTimeout)
		config, err := fetchProbeRouteConfig(ctx, baseURL, cfg.identity)
		cancel()
		if err != nil {
			return fmt.Errorf("active auth_ticket refresh failed: %w", err)
		}
		if item, ok := findProbeChainAuthTicketItem(cfg.chainID, probeVirtualRouterAuthTicketItems(config)); ok {
			cfg.authTicket = strings.TrimSpace(item.AuthTicket)
			if strings.TrimSpace(cfg.authTicket) != "" {
				rememberProbeChainAuthTicket(cfg.chainID, cfg.authTicket)
				log.Printf("probe chain auth ticket refreshed: chain=%s", strings.TrimSpace(cfg.chainID))
				return nil
			}
		}
		return fmt.Errorf("auth_ticket is required when require_user_auth=true")
	}
	return fmt.Errorf("auth_ticket is required when require_user_auth=true")
}

func findProbeChainAuthTicketItem(chainID string, groups ...[]probeLinkChainServerItem) (probeLinkChainServerItem, bool) {
	target := strings.TrimSpace(chainID)
	if target == "" {
		return probeLinkChainServerItem{}, false
	}
	for _, items := range groups {
		for _, item := range items {
			if strings.EqualFold(strings.TrimSpace(item.ChainID), target) ||
				strings.EqualFold(strings.TrimSpace(item.RelayChainID), target) ||
				strings.EqualFold(effectiveProbeLinkRelayChainID(item), target) {
				return item, true
			}
		}
	}
	return probeLinkChainServerItem{}, false
}

type probeChainBridgeDialTarget struct {
	Host                string
	Port                int
	LinkLayer           string
	RoleHeader          string
	PreserveRelayDomain bool
	AssignDownstream    bool
	AssignUpstream      bool
	AcceptStreams       bool
	Tag                 string
}

func startProbeChainBridgeWorkers(runtime *probeChainRuntime) {
	if runtime == nil {
		return
	}
	cfg := runtime.cfg
	if cfg.nextAuthMode != "proxy" {
		switch normalizeProbeChainDialMode(cfg.nextDialMode) {
		case probeChainDialModeForward:
			target := probeChainBridgeDialTarget{
				Host:                strings.TrimSpace(cfg.nextHost),
				Port:                cfg.nextPort,
				LinkLayer:           resolveProbeChainOutboundLinkLayer(cfg),
				RoleHeader:          probeChainBridgeRoleToNext,
				PreserveRelayDomain: cfg.nextPreserveRelayDomain,
				AssignDownstream:    true,
				AcceptStreams:       false,
				Tag:                 "downstream-forward",
			}
			go runProbeChainBridgeDialLoop(runtime, target)
		case probeChainDialModeReverse:
			log.Printf("probe chain waiting reverse downstream bridge: chain=%s listen=%s:%d", cfg.chainID, cfg.listenHost, cfg.listenPort)
		}
	}

	if normalizeProbeChainDialMode(cfg.prevDialMode) == probeChainDialModeReverse {
		target := probeChainBridgeDialTarget{
			Host:                strings.TrimSpace(cfg.prevHost),
			Port:                cfg.prevPort,
			LinkLayer:           normalizeProbeChainLinkLayer(cfg.prevLinkLayer),
			RoleHeader:          probeChainBridgeRoleToPrev,
			PreserveRelayDomain: cfg.prevPreserveRelayDomain,
			AssignDownstream:    false,
			AssignUpstream:      true,
			AcceptStreams:       true,
			Tag:                 "upstream-reverse",
		}
		if target.Host != "" && target.Port > 0 {
			go runProbeChainBridgeDialLoop(runtime, target)
		}
	}
}

func buildProbeChainTunnelOpenRequest(openType string, network string, targetAddr string, flowID string, associationV2 *probeChainAssociationV2Meta) probeChainTunnelOpenRequest {
	requestedNetwork := strings.ToLower(strings.TrimSpace(network))
	if requestedNetwork == "" {
		requestedNetwork = probeChainPortForwardNetworkTCP
	}
	cleanFlowID := strings.TrimSpace(flowID)
	if cleanFlowID == "" {
		cleanFlowID = resolveProbeChainTunnelFlowID(requestedNetwork, targetAddr, associationV2)
	}
	cleanType := strings.TrimSpace(openType)
	if cleanType == "" {
		cleanType = "open"
	}
	return probeChainTunnelOpenRequest{
		Type:             cleanType,
		Network:          requestedNetwork,
		Address:          strings.TrimSpace(targetAddr),
		FlowID:           cleanFlowID,
		AppProtocol:      resolveProbeChainTunnelAppProtocol(requestedNetwork, targetAddr, associationV2),
		Priority:         resolveProbeChainTunnelPriority(requestedNetwork, targetAddr, associationV2),
		ResumePolicy:     resolveProbeChainTunnelResumePolicy(requestedNetwork, associationV2),
		LatencySensitive: isProbeChainTunnelLatencySensitive(requestedNetwork, targetAddr, associationV2),
		AssociationV2:    associationV2,
	}
}

func resolveProbeChainTunnelFlowID(network string, targetAddr string, associationV2 *probeChainAssociationV2Meta) string {
	if associationV2 != nil && strings.TrimSpace(associationV2.FlowID) != "" {
		return strings.TrimSpace(associationV2.FlowID)
	}
	if strings.EqualFold(strings.TrimSpace(network), probeChainPortForwardNetworkTCP) || strings.EqualFold(strings.TrimSpace(network), "tcp") {
		return newProbeTCPDebugFlowID("chain_stream", targetAddr)
	}
	return strings.TrimSpace(targetAddr)
}

func resolveProbeChainTunnelPriority(network string, targetAddr string, associationV2 *probeChainAssociationV2Meta) string {
	if associationV2 != nil && strings.EqualFold(strings.TrimSpace(associationV2.Transport), probeChainPortForwardNetworkUDP) {
		return "realtime"
	}
	switch strings.ToLower(strings.TrimSpace(network)) {
	case probeChainPortForwardNetworkUDP:
		return "realtime"
	default:
		if isProbeChainRealtimeTCPPort(targetAddr) {
			return "realtime"
		}
		return "normal"
	}
}

func resolveProbeChainTunnelAppProtocol(network string, targetAddr string, associationV2 *probeChainAssociationV2Meta) string {
	if associationV2 != nil && strings.EqualFold(strings.TrimSpace(associationV2.Transport), probeChainPortForwardNetworkUDP) {
		return "udp-association"
	}
	if strings.EqualFold(strings.TrimSpace(network), probeChainPortForwardNetworkUDP) {
		return "udp-association"
	}
	port := probeChainTargetPort(targetAddr)
	switch {
	case port == 3389:
		return "rdp"
	case port >= 5900 && port <= 5999:
		return "vnc"
	case port == 4000:
		return "nomachine"
	case port == 22:
		return "ssh"
	default:
		return ""
	}
}

func resolveProbeChainTunnelResumePolicy(network string, associationV2 *probeChainAssociationV2Meta) string {
	if associationV2 != nil && strings.EqualFold(strings.TrimSpace(associationV2.Transport), probeChainPortForwardNetworkUDP) {
		return "rebind"
	}
	if strings.EqualFold(strings.TrimSpace(network), probeChainPortForwardNetworkUDP) {
		return "rebind"
	}
	return "replay_required"
}

func isProbeChainTunnelLatencySensitive(network string, targetAddr string, associationV2 *probeChainAssociationV2Meta) bool {
	return resolveProbeChainTunnelPriority(network, targetAddr, associationV2) == "realtime"
}

func isProbeChainRealtimeTCPPort(targetAddr string) bool {
	switch probeChainTargetPort(targetAddr) {
	case 22, 3389, 4000:
		return true
	default:
		port := probeChainTargetPort(targetAddr)
		return port >= 5900 && port <= 5999
	}
}

func probeChainTargetPort(targetAddr string) int {
	_, portText, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil {
		return 0
	}
	return port
}

func runProbeChainBridgeDialLoop(runtime *probeChainRuntime, target probeChainBridgeDialTarget) {
	if runtime == nil {
		return
	}
	backoff := probeChainBridgeRetryMin
	if backoff <= 0 {
		backoff = time.Second
	}

	for {
		select {
		case <-runtime.stopCh:
			return
		default:
		}

		conn, err := openProbeChainBridgeRelayNetConn(runtime.cfg, target)
		if err != nil {
			log.Printf("probe chain bridge dial failed: chain=%s role=%s tag=%s target=%s:%d %s err=%v", runtime.cfg.chainID, runtime.cfg.role, runtime.bridgeDialTag(target.Tag), target.Host, target.Port, runtime.bridgeDialLogFields(target), err)
			sleepProbeChainBridgeBackoff(runtime.stopCh, backoff)
			backoff = nextProbeChainBridgeBackoff(backoff)
			continue
		}

		session, err := newProbeChainFrameClient(conn)
		if err != nil {
			_ = conn.Close()
			log.Printf("probe chain bridge session setup failed: chain=%s role=%s tag=%s target=%s:%d %s err=%v", runtime.cfg.chainID, runtime.cfg.role, runtime.bridgeDialTag(target.Tag), target.Host, target.Port, runtime.bridgeDialLogFields(target), err)
			sleepProbeChainBridgeBackoff(runtime.stopCh, backoff)
			backoff = nextProbeChainBridgeBackoff(backoff)
			continue
		}
		sessionID := runtime.nextBridgeSessionID(target.Tag)
		log.Printf("probe chain bridge connected: chain=%s role=%s tag=%s session_id=%s target=%s:%d %s", runtime.cfg.chainID, runtime.cfg.role, runtime.bridgeDialTag(target.Tag), sessionID, target.Host, target.Port, runtime.bridgeDialLogFields(target))
		backoff = probeChainBridgeRetryMin

		if target.AssignDownstream {
			runtime.setDownstreamSession(sessionID, session, target.RoleHeader, net.JoinHostPort(target.Host, strconv.Itoa(target.Port)))
		}
		if target.AssignUpstream {
			runtime.setUpstreamSession(sessionID, session, target.RoleHeader, net.JoinHostPort(target.Host, strconv.Itoa(target.Port)))
		}
		if target.AcceptStreams || target.AssignDownstream || target.AssignUpstream {
			routeDirection := "forward"
			if target.AssignDownstream {
				routeDirection = "reverse"
			}
			go acceptProbeChainBridgeStreams(runtime, session, sessionID, target.Tag+"|session:"+sessionID, routeDirection)
		}

		waitProbeChainBridgeSession(runtime.stopCh, session)
		log.Printf("probe chain bridge disconnected: chain=%s role=%s tag=%s session_id=%s target=%s:%d %s", runtime.cfg.chainID, runtime.cfg.role, runtime.bridgeDialTag(target.Tag), sessionID, target.Host, target.Port, runtime.bridgeDialLogFields(target))
		if target.AssignDownstream {
			runtime.clearDownstreamSession(sessionID, session)
		}
		if target.AssignUpstream {
			runtime.clearUpstreamSession(sessionID, session)
		}
		_ = session.Close()
		_ = conn.Close()
		sleepProbeChainBridgeBackoff(runtime.stopCh, backoff)
		backoff = nextProbeChainBridgeBackoff(backoff)
	}
}

func sleepProbeChainBridgeBackoff(stopCh <-chan struct{}, delay time.Duration) {
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-stopCh:
	case <-timer.C:
	}
}

func nextProbeChainBridgeBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return probeChainBridgeRetryMin
	}
	next := current * 2
	if next > probeChainBridgeRetryMax {
		next = probeChainBridgeRetryMax
	}
	return next
}

func waitProbeChainBridgeSession(stopCh <-chan struct{}, session *probeChainFrameSession) {
	if session == nil {
		return
	}
	ticker := time.NewTicker(600 * time.Millisecond)
	defer ticker.Stop()
	for {
		if session.IsClosed() {
			return
		}
		select {
		case <-stopCh:
			_ = session.Close()
			return
		case <-ticker.C:
		}
	}
}

func acceptProbeChainBridgeStreams(runtime *probeChainRuntime, session *probeChainFrameSession, sessionID string, tag string, routeDirection string) {
	if runtime == nil || session == nil {
		return
	}
	cleanSessionID := strings.TrimSpace(sessionID)
	for {
		stream, acceptErr := session.Accept()
		if acceptErr != nil {
			if errors.Is(acceptErr, io.EOF) || errors.Is(acceptErr, net.ErrClosed) || session.IsClosed() {
				return
			}
			log.Printf("probe chain bridge accept failed: chain=%s tag=%s session_id=%s err=%v", runtime.cfg.chainID, strings.TrimSpace(tag), cleanSessionID, acceptErr)
			return
		}
		if strings.EqualFold(strings.TrimSpace(routeDirection), "reverse") {
			go handleProbeChainReverseConn(runtime, stream, cleanSessionID)
		} else {
			go handleProbeChainConn(runtime, stream, cleanSessionID)
		}
	}
}

func startProbeChainPublicRelayServer(runtime *probeChainRuntime) error {
	if runtime == nil {
		return errors.New("runtime is nil")
	}

	cfg := runtime.cfg
	listenAddr := net.JoinHostPort(cfg.listenHost, strconv.Itoa(cfg.listenPort))
	if err := acquireProbeChainSharedRelayServer(runtime, listenAddr); err != nil {
		return err
	}
	runtime.relayListenAddr = listenAddr
	return nil
}

func acquireProbeChainSharedRelayServer(runtime *probeChainRuntime, listenAddr string) error {
	if runtime == nil {
		return errors.New("runtime is nil")
	}
	chainID := strings.TrimSpace(runtime.cfg.chainID)
	if chainID == "" {
		return errors.New("chain_id is required")
	}

	probeChainSharedRelayState.mu.Lock()
	if shared := probeChainSharedRelayState.servers[listenAddr]; shared != nil {
		if shared.chainIDs == nil {
			shared.chainIDs = make(map[string]struct{})
		}
		if _, exists := shared.chainIDs[chainID]; !exists {
			shared.chainIDs[chainID] = struct{}{}
			shared.refCount++
		}
		refCount := shared.refCount
		probeChainSharedRelayState.mu.Unlock()
		markProbeChainRelayListenerStatus(listenAddr, "websocket", "listening", "")
		markProbeChainRelayListenerStatus(listenAddr, "websocket-h3", "listening", "")
		log.Printf("probe chain shared relay reused: chain=%s listen=%s ref_count=%d", chainID, listenAddr, refCount)
		return nil
	}
	probeChainSharedRelayState.mu.Unlock()

	shared, err := startProbeChainSharedRelayServer(runtime, listenAddr)
	if err != nil {
		return err
	}

	probeChainSharedRelayState.mu.Lock()
	if existing := probeChainSharedRelayState.servers[listenAddr]; existing != nil {
		probeChainSharedRelayState.mu.Unlock()
		closeProbeChainSharedRelayServer(shared)
		return acquireProbeChainSharedRelayServer(runtime, listenAddr)
	}
	probeChainSharedRelayState.servers[listenAddr] = shared
	probeChainSharedRelayState.mu.Unlock()
	log.Printf("probe chain shared relay started: chain=%s listen=%s", chainID, listenAddr)
	return nil
}

func startProbeChainSharedRelayServer(runtime *probeChainRuntime, listenAddr string) (*probeChainSharedRelayServer, error) {
	cfg := runtime.cfg
	handler := buildProbeChainSharedRelayHandler()
	cert, err := prepareProbeServerCertificate(cfg.identity, strings.TrimSpace(cfg.controllerURL))
	if err != nil {
		return nil, fmt.Errorf("prepare chain relay certificate failed: %w", err)
	}

	listenConfig := net.ListenConfig{KeepAlive: probeChainRelayTCPKeepAlivePeriod}
	tcpListener, err := listenConfig.Listen(context.Background(), "tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen chain relay tcp failed: %w", err)
	}
	tcpListener = &probeChainTunedTCPListener{Listener: tcpListener}
	udpPacketConn, err := net.ListenPacket("udp", listenAddr)
	if err != nil {
		_ = tcpListener.Close()
		return nil, fmt.Errorf("listen chain relay udp failed: %w", err)
	}
	if udpConn, ok := udpPacketConn.(*net.UDPConn); ok {
		tuneProbeChainUDPConn(udpConn)
	}
	h3Cert, err := tls.LoadX509KeyPair(cert.CertPath, cert.KeyPath)
	if err != nil {
		_ = tcpListener.Close()
		_ = udpPacketConn.Close()
		return nil, fmt.Errorf("load chain relay certificate failed: %w", err)
	}

	shared := &probeChainSharedRelayServer{
		listenAddr:    listenAddr,
		chainIDs:      map[string]struct{}{strings.TrimSpace(cfg.chainID): {}},
		refCount:      1,
		udpPacketConn: udpPacketConn,
	}
	httpsServer := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	shared.httpsServer = httpsServer
	markProbeChainRelayListenerStatus(listenAddr, "websocket", "starting", "")
	go func(s *probeChainSharedRelayServer, certFile string, keyFile string) {
		markProbeChainRelayListenerStatus(listenAddr, "websocket", "listening", "")
		serveErr := s.httpsServer.ServeTLS(tcpListener, certFile, keyFile)
		if serveErr != nil && serveErr != http.ErrServerClosed {
			markProbeChainRelayListenerStatus(listenAddr, "websocket", "failed", serveErr.Error())
			log.Printf("probe chain shared relay exited: layer=websocket listen=%s err=%v", listenAddr, serveErr)
			return
		}
		markProbeChainRelayListenerStatus(listenAddr, "websocket", "stopped", "")
	}(shared, cert.CertPath, cert.KeyPath)

	h3Server := &http3.Server{
		Addr:    listenAddr,
		Handler: handler,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{h3Cert},
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{"h3"},
		},
		QUICConfig: newProbeChainQUICConfig(probeChainRelayQUICMaxIncomingStreams),
	}
	shared.http3Server = h3Server
	markProbeChainRelayListenerStatus(listenAddr, "websocket-h3", "starting", "")
	go func(s *probeChainSharedRelayServer) {
		markProbeChainRelayListenerStatus(listenAddr, "websocket-h3", "listening", "")
		if serveErr := s.http3Server.Serve(udpPacketConn); serveErr != nil && serveErr != http.ErrServerClosed {
			markProbeChainRelayListenerStatus(listenAddr, "websocket-h3", "failed", serveErr.Error())
			log.Printf("probe chain shared relay exited: layer=websocket-h3 listen=%s err=%v", listenAddr, serveErr)
			return
		}
		markProbeChainRelayListenerStatus(listenAddr, "websocket-h3", "stopped", "")
	}(shared)

	return shared, nil
}

func buildProbeChainSharedRelayHandler() http.Handler {
	mux := http.NewServeMux()
	registerProbeOpenAIStyleCamouflageRoutes(mux)
	mux.HandleFunc(probeChainRelayAPIPath, func(w http.ResponseWriter, r *http.Request) {
		handleProbeChainRelayDispatch(w, r)
	})
	return mux
}

func handleProbeChainRelayDispatch(w http.ResponseWriter, r *http.Request) {
	chainID := resolveProbeChainIDFromRequest(r)
	if strings.TrimSpace(chainID) == "" {
		log.Printf("probe chain relay request rejected: remote=%s method=%s proto=%s host=%s reason=missing_chain_id", r.RemoteAddr, r.Method, r.Proto, r.Host)
		http.Error(w, "chain_id is required", http.StatusBadRequest)
		return
	}
	runtime := getProbeChainRuntime(chainID)
	if runtime == nil {
		log.Printf("probe chain relay request rejected: requested_chain=%s remote=%s method=%s proto=%s host=%s reason=runtime_not_found", strings.TrimSpace(chainID), r.RemoteAddr, r.Method, r.Proto, r.Host)
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	handleProbeChainRelayToRuntime(runtime, w, r)
}

func handleProbeChainRelayToRuntime(runtime *probeChainRuntime, w http.ResponseWriter, r *http.Request) {
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	if !websocket.IsWebSocketUpgrade(r) && !isProbeChainHTTP3WebSocketRequest(r) {
		log.Printf("probe chain relay request rejected: chain=%s role=%s remote=%s method=%s proto=%s host=%s reason=method_not_allowed", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, r.Method, r.Proto, r.Host)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	chainID := resolveProbeChainIDFromRequest(r)
	if chainID == "" {
		log.Printf("probe chain relay request rejected: chain=%s role=%s remote=%s method=%s proto=%s host=%s reason=missing_chain_id", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, r.Method, r.Proto, r.Host)
		http.Error(w, "chain_id is required", http.StatusBadRequest)
		return
	}
	if chainID != strings.TrimSpace(runtime.cfg.chainID) {
		log.Printf("probe chain relay request rejected: chain=%s role=%s remote=%s method=%s proto=%s host=%s requested_chain=%s reason=runtime_not_found", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, r.Method, r.Proto, r.Host, chainID)
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	if err := verifyProbeChainRelayRequestAuth(runtime, r, chainID); err != nil {
		log.Printf("probe chain relay request rejected: chain=%s role=%s remote=%s method=%s proto=%s host=%s reason=unauthorized err=%v", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, r.Method, r.Proto, r.Host, err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	relayMode := strings.ToLower(strings.TrimSpace(r.Header.Get(probeChainCodexRelayModeHeader)))
	bridgeRole := normalizeProbeChainBridgeRole(r.Header.Get(probeChainCodexRelayRoleHeader))
	requestTransport := "http"
	if isProbeChainHTTP3WebSocketRequest(r) {
		requestTransport = "websocket-h3"
	} else if websocket.IsWebSocketUpgrade(r) {
		requestTransport = "websocket"
	}
	log.Printf("probe chain relay request accepted: chain=%s role=%s remote=%s method=%s proto=%s host=%s mode=%s bridge_role=%s transport=%s content_length=%d", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, r.Method, r.Proto, r.Host, firstNonEmpty(relayMode, probeChainRelayModeBridge), bridgeRole, requestTransport, r.ContentLength)
	if relayMode == probeChainRelayModeSpeedDebug {
		if isProbeChainHTTP3WebSocketRequest(r) {
			handleProbeChainSpeedDebugHTTP3WebSocket(runtime, w, r)
			return
		}
		if websocket.IsWebSocketUpgrade(r) {
			handleProbeChainSpeedDebugWebSocket(runtime, w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if relayMode == probeChainRelayModeSpeedTest {
		if isProbeChainHTTP3WebSocketRequest(r) {
			handleProbeChainSpeedTestHTTP3WebSocket(runtime, w, r)
			return
		}
		if websocket.IsWebSocketUpgrade(r) {
			handleProbeChainSpeedTestWebSocket(runtime, w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if isProbeChainHTTP3WebSocketRequest(r) {
		handleProbeChainBridgeRelayHTTP3WebSocket(runtime, bridgeRole, w, r)
		return
	}
	if websocket.IsWebSocketUpgrade(r) {
		handleProbeChainBridgeRelayWebSocket(runtime, bridgeRole, w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func isProbeChainHTTP3WebSocketRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	return r.Method == http.MethodConnect && r.ProtoMajor == 3 && strings.EqualFold(strings.TrimSpace(r.Proto), "websocket")
}

func handleProbeChainBridgeRelayWebSocket(runtime *probeChainRuntime, bridgeRole string, w http.ResponseWriter, r *http.Request) {
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	log.Printf("probe chain websocket relay request: chain=%s role=%s bridge_role=%s remote=%s host=%s proto=%s", runtime.cfg.chainID, runtime.cfg.role, normalizeProbeChainBridgeRole(bridgeRole), r.RemoteAddr, r.Host, r.Proto)
	upgrader := websocket.Upgrader{
		CheckOrigin:       func(*http.Request) bool { return true },
		ReadBufferSize:    probeChainRelayWebSocketBufferBytes,
		WriteBufferSize:   probeChainRelayWebSocketBufferBytes,
		EnableCompression: false,
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("probe chain websocket relay upgrade failed: chain=%s role=%s remote=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, err)
		return
	}
	defer ws.Close()

	conn := newWebSocketNetConn(ws)
	role := normalizeProbeChainBridgeRole(bridgeRole)
	assignTarget := "upstream"
	routeDirection := "forward"
	if role == probeChainBridgeRoleToPrev {
		assignTarget = "downstream"
		routeDirection = "reverse"
	}
	sessionID := runtime.nextBridgeSessionID(assignTarget)
	session, err := newProbeChainFrameServer(conn)
	if err != nil {
		log.Printf("probe chain websocket bridge session setup failed: chain=%s role=%s bridge_role=%s remote=%s session_id=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, role, r.RemoteAddr, sessionID, err)
		return
	}

	log.Printf("probe chain websocket bridge connected: chain=%s role=%s bridge_role=%s assign_target=%s route_direction=%s remote=%s session_id=%s", runtime.cfg.chainID, runtime.cfg.role, role, assignTarget, routeDirection, r.RemoteAddr, sessionID)
	if role == probeChainBridgeRoleToPrev {
		runtime.setDownstreamSession(sessionID, session, role, strings.TrimSpace(r.RemoteAddr))
		go acceptProbeChainBridgeStreams(runtime, session, sessionID, "websocket-bridge|session:"+sessionID, "reverse")
		waitProbeChainBridgeSession(runtime.stopCh, session)
		runtime.clearDownstreamSession(sessionID, session)
	} else {
		runtime.setUpstreamSession(sessionID, session, role, strings.TrimSpace(r.RemoteAddr))
		go acceptProbeChainBridgeStreams(runtime, session, sessionID, "websocket-bridge|session:"+sessionID, "forward")
		waitProbeChainBridgeSession(runtime.stopCh, session)
		runtime.clearUpstreamSession(sessionID, session)
	}
	log.Printf("probe chain websocket bridge disconnected: chain=%s role=%s bridge_role=%s assign_target=%s route_direction=%s remote=%s session_id=%s", runtime.cfg.chainID, runtime.cfg.role, role, assignTarget, routeDirection, r.RemoteAddr, sessionID)
	_ = session.Close()
}

func handleProbeChainBridgeRelayHTTP3WebSocket(runtime *probeChainRuntime, bridgeRole string, w http.ResponseWriter, r *http.Request) {
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	log.Printf("probe chain h3 websocket relay request: chain=%s role=%s bridge_role=%s remote=%s host=%s proto=%s", runtime.cfg.chainID, runtime.cfg.role, normalizeProbeChainBridgeRole(bridgeRole), r.RemoteAddr, r.Host, r.Proto)
	streamer, ok := w.(http3.HTTPStreamer)
	if !ok {
		log.Printf("probe chain h3 websocket relay rejected: chain=%s role=%s bridge_role=%s remote=%s reason=http3_stream_unavailable", runtime.cfg.chainID, runtime.cfg.role, normalizeProbeChainBridgeRole(bridgeRole), r.RemoteAddr)
		http.Error(w, "http3 stream unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	stream := streamer.HTTPStream()
	conn := &probeChainHTTP3StreamNetConn{
		stream: stream,
		local:  probeChainRelayNetAddr{label: "probe-chain-h3-websocket-local"},
		remote: probeChainRelayNetAddr{label: strings.TrimSpace(r.RemoteAddr)},
		closeFn: func() error {
			return stream.Close()
		},
	}
	defer conn.Close()

	role := normalizeProbeChainBridgeRole(bridgeRole)
	assignTarget := "upstream"
	routeDirection := "forward"
	if role == probeChainBridgeRoleToPrev {
		assignTarget = "downstream"
		routeDirection = "reverse"
	}
	sessionID := runtime.nextBridgeSessionID(assignTarget)
	session, err := newProbeChainFrameServer(conn)
	if err != nil {
		log.Printf("probe chain h3 websocket bridge session setup failed: chain=%s role=%s bridge_role=%s remote=%s session_id=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, role, r.RemoteAddr, sessionID, err)
		return
	}

	log.Printf("probe chain h3 websocket bridge connected: chain=%s role=%s bridge_role=%s assign_target=%s route_direction=%s remote=%s session_id=%s", runtime.cfg.chainID, runtime.cfg.role, role, assignTarget, routeDirection, r.RemoteAddr, sessionID)
	if role == probeChainBridgeRoleToPrev {
		runtime.setDownstreamSession(sessionID, session, role, strings.TrimSpace(r.RemoteAddr))
		go acceptProbeChainBridgeStreams(runtime, session, sessionID, "h3-websocket-bridge|session:"+sessionID, "reverse")
		waitProbeChainBridgeSession(runtime.stopCh, session)
		runtime.clearDownstreamSession(sessionID, session)
	} else {
		runtime.setUpstreamSession(sessionID, session, role, strings.TrimSpace(r.RemoteAddr))
		go acceptProbeChainBridgeStreams(runtime, session, sessionID, "h3-websocket-bridge|session:"+sessionID, "forward")
		waitProbeChainBridgeSession(runtime.stopCh, session)
		runtime.clearUpstreamSession(sessionID, session)
	}
	log.Printf("probe chain h3 websocket bridge disconnected: chain=%s role=%s bridge_role=%s assign_target=%s route_direction=%s remote=%s session_id=%s", runtime.cfg.chainID, runtime.cfg.role, role, assignTarget, routeDirection, r.RemoteAddr, sessionID)
	_ = session.Close()
}

func handleProbeChainSpeedDebugWebSocket(runtime *probeChainRuntime, w http.ResponseWriter, r *http.Request) {
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	log.Printf("probe chain websocket speed debug request: chain=%s role=%s remote=%s proto=%s", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, r.Proto)
	upgrader := websocket.Upgrader{
		CheckOrigin:       func(*http.Request) bool { return true },
		ReadBufferSize:    probeChainRelayWebSocketBufferBytes,
		WriteBufferSize:   probeChainRelayWebSocketBufferBytes,
		EnableCompression: false,
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("probe chain websocket speed debug upgrade failed: chain=%s role=%s remote=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, err)
		return
	}
	conn := newWebSocketNetConn(ws)
	defer conn.Close()
	writeProbeChainSpeedDebugPayload(runtime, conn, "relay-speed-debug-"+randomHexToken(8))
}

func handleProbeChainSpeedDebugHTTP3WebSocket(runtime *probeChainRuntime, w http.ResponseWriter, r *http.Request) {
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	streamer, ok := w.(http3.HTTPStreamer)
	if !ok {
		log.Printf("probe chain h3 websocket speed debug rejected: chain=%s role=%s remote=%s reason=http3_stream_unavailable", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr)
		http.Error(w, "http3 stream unavailable", http.StatusInternalServerError)
		return
	}
	log.Printf("probe chain h3 websocket speed debug request: chain=%s role=%s remote=%s proto=%s", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, r.Proto)
	w.Header().Set("Content-Type", "application/json")
	stream := streamer.HTTPStream()
	conn := &probeChainHTTP3StreamNetConn{
		stream: stream,
		local:  probeChainRelayNetAddr{label: "probe-chain-h3-speed-debug-local"},
		remote: probeChainRelayNetAddr{label: strings.TrimSpace(r.RemoteAddr)},
		closeFn: func() error {
			return stream.Close()
		},
	}
	defer conn.Close()
	writeProbeChainSpeedDebugPayload(runtime, conn, "relay-speed-debug-"+randomHexToken(8))
}

func writeProbeChainSpeedDebugPayload(runtime *probeChainRuntime, conn net.Conn, requestID string) {
	nodeID := ""
	if runtime != nil {
		nodeID = strings.TrimSpace(runtime.cfg.identity.NodeID)
	}
	payload := globalProbeSpeedDebugState.snapshotPayload(nodeID, strings.TrimSpace(requestID))
	payload.Scope = "chain_relay"
	if err := writeProbeChainTunnelJSONResponse(conn, payload); err != nil && runtime != nil {
		log.Printf("probe chain speed debug direct response failed: chain=%s role=%s request_id=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, strings.TrimSpace(requestID), err)
	}
}

func handleProbeChainSpeedTestWebSocket(runtime *probeChainRuntime, w http.ResponseWriter, r *http.Request) {
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	byteCount := parseProbeChainSpeedTestBytes(r)
	log.Printf("probe chain websocket speed test request: chain=%s role=%s remote=%s proto=%s bytes=%d", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, r.Proto, byteCount)
	upgrader := websocket.Upgrader{
		CheckOrigin:       func(*http.Request) bool { return true },
		ReadBufferSize:    probeChainRelayWebSocketBufferBytes,
		WriteBufferSize:   probeChainRelayWebSocketBufferBytes,
		EnableCompression: false,
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("probe chain websocket speed test upgrade failed: chain=%s role=%s remote=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, err)
		return
	}
	defer ws.Close()
	conn := newWebSocketNetConn(ws)
	defer conn.Close()
	streamProbeChainSpeedTestBytes(runtime, conn, strings.TrimSpace(r.RemoteAddr), byteCount, "websocket")
}

func handleProbeChainSpeedTestHTTP3WebSocket(runtime *probeChainRuntime, w http.ResponseWriter, r *http.Request) {
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	streamer, ok := w.(http3.HTTPStreamer)
	if !ok {
		log.Printf("probe chain h3 websocket speed test rejected: chain=%s role=%s remote=%s reason=http3_stream_unavailable", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr)
		http.Error(w, "http3 stream unavailable", http.StatusInternalServerError)
		return
	}
	byteCount := parseProbeChainSpeedTestBytes(r)
	log.Printf("probe chain h3 websocket speed test request: chain=%s role=%s remote=%s proto=%s bytes=%d", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, r.Proto, byteCount)
	w.Header().Set("Content-Type", "application/octet-stream")
	stream := streamer.HTTPStream()
	conn := &probeChainHTTP3StreamNetConn{
		stream: stream,
		local:  probeChainRelayNetAddr{label: "probe-chain-h3-speed-local"},
		remote: probeChainRelayNetAddr{label: strings.TrimSpace(r.RemoteAddr)},
		closeFn: func() error {
			return stream.Close()
		},
	}
	defer conn.Close()
	streamProbeChainSpeedTestBytes(runtime, conn, strings.TrimSpace(r.RemoteAddr), byteCount, "websocket-h3")
}

func streamProbeChainSpeedTestBytes(runtime *probeChainRuntime, writer io.Writer, remoteAddr string, byteCount int64, transport string) {
	if runtime == nil || writer == nil {
		return
	}
	cleanTransport := strings.TrimSpace(transport)
	chunkBytes := probeChainSpeedTestChunkBytesForTransport(cleanTransport)
	buf := make([]byte, chunkBytes)
	for i := range buf {
		buf[i] = byte(i % 251)
	}
	startedAt := time.Now()
	deadlineAt := startedAt.Add(probeChainRelaySpeedTestTimeout)
	debugItem := globalProbeSpeedDebugState.begin(probeSpeedDebugBeginOptions{
		ChainID:        runtime.cfg.chainID,
		Role:           runtime.cfg.role,
		Side:           "remote",
		Transport:      cleanTransport,
		RemoteAddr:     strings.TrimSpace(remoteAddr),
		RequestedBytes: byteCount,
		ChunkBytes:     int64(chunkBytes),
	})
	lastLogAt := startedAt
	sent := int64(0)
	nextLogBytes := int64(16 * 1024 * 1024)
	writeCalls := int64(0)
	var blockedTotal time.Duration
	var maxBlocked time.Duration
	remaining := byteCount
	for remaining > 0 {
		if !time.Now().Before(deadlineAt) {
			log.Printf("probe chain %s speed test stopped: chain=%s role=%s remote=%s reason=duration_limit sent=%d remaining=%d elapsed_ms=%d write_calls=%d max_write_block_ms=%d total_write_block_ms=%d", cleanTransport, runtime.cfg.chainID, runtime.cfg.role, strings.TrimSpace(remoteAddr), sent, remaining, probeDurationMilliseconds(time.Since(startedAt)), writeCalls, probeDurationMilliseconds(maxBlocked), probeDurationMilliseconds(blockedTotal))
			if debugItem != nil {
				globalProbeSpeedDebugState.end(debugItem, "duration_limit", nil)
			}
			return
		}
		n := int64(len(buf))
		if remaining < n {
			n = remaining
		}
		if deadliner, ok := writer.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = deadliner.SetWriteDeadline(deadlineAt)
		}
		writeStartedAt := time.Now()
		written, err := writer.Write(buf[:n])
		if deadliner, ok := writer.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = deadliner.SetWriteDeadline(time.Time{})
		}
		blocked := time.Since(writeStartedAt)
		writeCalls++
		blockedTotal += blocked
		if blocked > maxBlocked {
			maxBlocked = blocked
		}
		if written > 0 {
			sent += int64(written)
			remaining -= int64(written)
		}
		if debugItem != nil {
			debugItem.recordWrite(written, blocked, remaining)
		}
		if err != nil {
			log.Printf("probe chain %s speed test interrupted: chain=%s role=%s remote=%s bytes=%d sent=%d remaining=%d elapsed_ms=%d write_calls=%d max_write_block_ms=%d total_write_block_ms=%d err=%v", cleanTransport, runtime.cfg.chainID, runtime.cfg.role, strings.TrimSpace(remoteAddr), byteCount, sent, remaining, probeDurationMilliseconds(time.Since(startedAt)), writeCalls, probeDurationMilliseconds(maxBlocked), probeDurationMilliseconds(blockedTotal), err)
			if debugItem != nil {
				globalProbeSpeedDebugState.end(debugItem, "failed", err)
			}
			return
		}
		if written == 0 {
			log.Printf("probe chain %s speed test stopped: chain=%s role=%s remote=%s reason=zero_write sent=%d remaining=%d elapsed_ms=%d write_calls=%d", cleanTransport, runtime.cfg.chainID, runtime.cfg.role, strings.TrimSpace(remoteAddr), sent, remaining, probeDurationMilliseconds(time.Since(startedAt)), writeCalls)
			if debugItem != nil {
				globalProbeSpeedDebugState.end(debugItem, "zero_write", nil)
			}
			return
		}
		if sent >= nextLogBytes || remaining == 0 {
			elapsed := time.Since(startedAt)
			if elapsed <= 0 {
				elapsed = time.Millisecond
			}
			rateBPS := int64(float64(sent) / elapsed.Seconds())
			log.Printf("probe chain %s speed test write progress: chain=%s role=%s remote=%s sent=%d total=%d chunk_bytes=%d elapsed_ms=%d since_last_ms=%d rate_bps=%d write_calls=%d max_write_block_ms=%d total_write_block_ms=%d", cleanTransport, runtime.cfg.chainID, runtime.cfg.role, strings.TrimSpace(remoteAddr), sent, byteCount, chunkBytes, probeDurationMilliseconds(elapsed), probeDurationMilliseconds(time.Since(lastLogAt)), rateBPS, writeCalls, probeDurationMilliseconds(maxBlocked), probeDurationMilliseconds(blockedTotal))
			lastLogAt = time.Now()
			for nextLogBytes <= sent {
				nextLogBytes += 16 * 1024 * 1024
			}
		}
		if int64(written) != n {
			log.Printf("probe chain %s speed test short write: chain=%s role=%s remote=%s requested=%d written=%d sent=%d remaining=%d", cleanTransport, runtime.cfg.chainID, runtime.cfg.role, strings.TrimSpace(remoteAddr), n, written, sent, remaining)
		}
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	elapsed := time.Since(startedAt)
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	rateBPS := int64(float64(sent) / elapsed.Seconds())
	log.Printf("probe chain %s speed test completed: chain=%s role=%s remote=%s bytes=%d chunk_bytes=%d elapsed_ms=%d rate_bps=%d write_calls=%d max_write_block_ms=%d total_write_block_ms=%d", cleanTransport, runtime.cfg.chainID, runtime.cfg.role, strings.TrimSpace(remoteAddr), sent, chunkBytes, probeDurationMilliseconds(elapsed), rateBPS, writeCalls, probeDurationMilliseconds(maxBlocked), probeDurationMilliseconds(blockedTotal))
	if debugItem != nil {
		globalProbeSpeedDebugState.end(debugItem, "completed", nil)
	}
}

func probeChainSpeedTestChunkBytesForTransport(transport string) int {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "websocket-h3":
		return probeChainRelaySpeedTestChunkBytes
	default:
		return probeChainRelaySpeedTestChunkBytes
	}
}

func parseProbeChainSpeedTestBytes(r *http.Request) int64 {
	if r == nil {
		return probeChainRelaySpeedTestBytes
	}
	raw := firstNonEmpty(strings.TrimSpace(r.Header.Get(probeChainCodexSpeedBytesHeader)), strings.TrimSpace(r.URL.Query().Get("speed_bytes")))
	if raw == "" {
		return probeChainRelaySpeedTestBytes
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return probeChainRelaySpeedTestBytes
	}
	if value > probeChainRelaySpeedTestMaxBytes {
		return probeChainRelaySpeedTestMaxBytes
	}
	return value
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
	return strings.TrimSpace(chainID)
}

func verifyProbeChainRelayRequestAuth(runtime *probeChainRuntime, r *http.Request, chainID string) error {
	if runtime == nil {
		return errors.New("runtime is nil")
	}
	sourceIP := resolveProbeChainSourceIPFromRequest(r)
	useBlacklist := shouldUseProbeChainAuthIPBlacklist(chainID)
	if isProbeChainAuthIPManuallyBlacklisted(sourceIP) {
		delayProbeChainAuthFailure()
		log.Printf("probe chain auth rejected (ip blacklisted): chain=%s ip=%s until=manual", strings.TrimSpace(chainID), sourceIP)
		return errors.New("source ip is blacklisted")
	}
	if useBlacklist {
		if blocked, until := isProbeChainAuthIPBlacklisted(sourceIP); blocked {
			delayProbeChainAuthFailure()
			log.Printf("probe chain auth rejected (ip blacklisted): chain=%s ip=%s until=%s", strings.TrimSpace(chainID), sourceIP, until.UTC().Format(time.RFC3339))
			return errors.New("source ip is blacklisted")
		}
	}

	env, err := readProbeChainAuthEnvelopeFromHeaders(r.Header, chainID)
	if err != nil {
		delayProbeChainAuthFailure()
		recordOrLogProbeChainAuthFailure(strings.TrimSpace(chainID), sourceIP, useBlacklist, err)
		return err
	}
	if err := verifyProbeChainInboundAuth(runtime.cfg, env); err != nil {
		delayProbeChainAuthFailure()
		recordOrLogProbeChainAuthFailure(strings.TrimSpace(chainID), sourceIP, useBlacklist, err)
		return err
	}
	resetProbeChainAuthFailure(sourceIP)
	return nil
}

func shouldUseProbeChainAuthIPBlacklist(chainID string) bool {
	return !isProbeVirtualRouterRuntimeChainID(chainID)
}

func recordOrLogProbeChainAuthFailure(chainID string, sourceIP string, useBlacklist bool, err error) {
	if useBlacklist {
		failures, blacklisted, until := recordProbeChainAuthFailure(sourceIP)
		logProbeChainAuthFailure(chainID, sourceIP, failures, blacklisted, until, err)
		return
	}
	logProbeChainAuthFailureWithoutBlacklist(chainID, sourceIP, err)
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
		Nonce:      strings.TrimSpace(nonce),
		MAC:        strings.TrimSpace(headers.Get(probeChainCodexMACHeader)),
		AuthTicket: strings.TrimSpace(headers.Get(probeChainCodexAuthTicketHeader)),
	}
	if env.APIVersion == "" {
		env.APIVersion = probeChainAuthPacketVersion
	}
	if env.Mode == "" {
		env.Mode = "secret_hmac"
	}
	return env, nil
}

func parseProbeChainBearerToken(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("authorization bearer token is required")
	}
	parts := strings.Fields(trimmed)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", errors.New("invalid authorization header")
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", errors.New("authorization bearer token is required")
	}
	return token, nil
}

func (rt *probeChainRuntime) closeRuntimeResources() {
	if rt == nil {
		return
	}
	rt.bridgeMu.Lock()
	downstreamSessions := make([]*probeChainBridgeSession, 0, len(rt.downstreamSessions))
	for _, item := range rt.downstreamSessions {
		downstreamSessions = append(downstreamSessions, item)
	}
	upstreamSessions := make([]*probeChainBridgeSession, 0, len(rt.upstreamSessions))
	for _, item := range rt.upstreamSessions {
		upstreamSessions = append(upstreamSessions, item)
	}
	rt.downstreamSessions = make(map[string]*probeChainBridgeSession)
	rt.upstreamSessions = make(map[string]*probeChainBridgeSession)
	rt.bridgeMu.Unlock()
	closedSessions := make(map[*probeChainFrameSession]struct{})
	closeBridgeSession := func(item *probeChainBridgeSession) {
		if item == nil || item.Session == nil {
			return
		}
		if _, exists := closedSessions[item.Session]; exists {
			return
		}
		closedSessions[item.Session] = struct{}{}
		_ = item.Session.Close()
	}
	for _, item := range downstreamSessions {
		closeBridgeSession(item)
	}
	for _, item := range upstreamSessions {
		closeBridgeSession(item)
	}
	releaseProbeChainSharedRelayServer(rt)
}

func releaseProbeChainSharedRelayServer(rt *probeChainRuntime) {
	if rt == nil {
		return
	}
	listenAddr := strings.TrimSpace(rt.relayListenAddr)
	if listenAddr == "" {
		listenAddr = net.JoinHostPort(rt.cfg.listenHost, strconv.Itoa(rt.cfg.listenPort))
	}
	chainID := strings.TrimSpace(rt.cfg.chainID)
	if listenAddr == "" || chainID == "" {
		return
	}

	var closeTarget *probeChainSharedRelayServer
	refCount := 0
	probeChainSharedRelayState.mu.Lock()
	shared := probeChainSharedRelayState.servers[listenAddr]
	if shared != nil {
		if _, exists := shared.chainIDs[chainID]; exists {
			delete(shared.chainIDs, chainID)
			if shared.refCount > 0 {
				shared.refCount--
			}
		}
		refCount = shared.refCount
		if shared.refCount <= 0 || len(shared.chainIDs) == 0 {
			delete(probeChainSharedRelayState.servers, listenAddr)
			closeTarget = shared
		}
	}
	probeChainSharedRelayState.mu.Unlock()

	if closeTarget != nil {
		closeProbeChainSharedRelayServer(closeTarget)
		log.Printf("probe chain shared relay stopped: listen=%s", listenAddr)
		return
	}
	if shared != nil {
		log.Printf("probe chain shared relay released: chain=%s listen=%s ref_count=%d", chainID, listenAddr, refCount)
	}
}

func closeProbeChainSharedRelayServer(shared *probeChainSharedRelayServer) {
	if shared == nil {
		return
	}
	listenAddr := strings.TrimSpace(shared.listenAddr)
	if shared.httpsServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		_ = shared.httpsServer.Shutdown(ctx)
		cancel()
	}
	if shared.http3Server != nil {
		_ = shared.http3Server.Close()
	}
	if shared.udpPacketConn != nil {
		_ = shared.udpPacketConn.Close()
	}
	if listenAddr != "" {
		markProbeChainRelayListenerStatus(listenAddr, "websocket", "stopped", "")
		markProbeChainRelayListenerStatus(listenAddr, "websocket-h3", "stopped", "")
	}
}

func (rt *probeChainRuntime) nextBridgeSessionID(prefix string) string {
	if rt == nil {
		return ""
	}
	cleanPrefix := strings.ToLower(strings.TrimSpace(prefix))
	if cleanPrefix == "" {
		cleanPrefix = "bridge"
	}
	rt.bridgeMu.Lock()
	rt.bridgeSeq++
	seq := rt.bridgeSeq
	rt.bridgeMu.Unlock()
	return fmt.Sprintf("%s-%06d", cleanPrefix, seq)
}

func (rt *probeChainRuntime) singleBridgeSessionPerRule() bool {
	if rt == nil {
		return false
	}
	chainType := strings.TrimSpace(rt.cfg.chainType)
	chainID := strings.TrimSpace(rt.cfg.chainID)
	return strings.EqualFold(chainType, "virtual_router") || strings.HasPrefix(chainID, probeVirtualRouterRuntimeChainIDPrefix)
}

func (rt *probeChainRuntime) replaceBridgeSessionsLocked(keep *probeChainFrameSession) []*probeChainFrameSession {
	if rt == nil {
		return nil
	}
	superseded := make([]*probeChainFrameSession, 0)
	seen := make(map[*probeChainFrameSession]struct{})
	collect := func(item *probeChainBridgeSession) {
		if item == nil || item.Session == nil || item.Session == keep {
			return
		}
		if _, ok := seen[item.Session]; ok {
			return
		}
		seen[item.Session] = struct{}{}
		superseded = append(superseded, item.Session)
	}
	for _, item := range rt.downstreamSessions {
		collect(item)
	}
	for _, item := range rt.upstreamSessions {
		collect(item)
	}
	rt.downstreamSessions = make(map[string]*probeChainBridgeSession)
	rt.upstreamSessions = make(map[string]*probeChainBridgeSession)
	return superseded
}

func closeSupersededProbeChainBridgeSessions(chainID string, role string, direction string, sessions []*probeChainFrameSession) {
	for _, session := range sessions {
		if session == nil {
			continue
		}
		if isProbeVirtualRouterRuntimeChainID(chainID) {
			log.Printf("probe chain bridge session superseded: chain=%s role=%s session=%p", strings.TrimSpace(chainID), strings.TrimSpace(role), session)
		} else {
			log.Printf("probe chain bridge session superseded: chain=%s role=%s direction=%s session=%p", strings.TrimSpace(chainID), strings.TrimSpace(role), strings.TrimSpace(direction), session)
		}
		_ = session.Close()
	}
}

func latestProbeChainBridgeSessionLocked(items map[string]*probeChainBridgeSession) *probeChainBridgeSession {
	var latest *probeChainBridgeSession
	for _, item := range items {
		if item == nil || item.Session == nil || item.Session.IsClosed() {
			continue
		}
		if latest == nil || item.ConnectedAt.After(latest.ConnectedAt) {
			latest = item
		}
	}
	return latest
}

func (rt *probeChainRuntime) latestAnyBridgeSessionLocked() *probeChainBridgeSession {
	latest := latestProbeChainBridgeSessionLocked(rt.downstreamSessions)
	upstreamLatest := latestProbeChainBridgeSessionLocked(rt.upstreamSessions)
	if upstreamLatest != nil && (latest == nil || upstreamLatest.ConnectedAt.After(latest.ConnectedAt)) {
		latest = upstreamLatest
	}
	return latest
}

func (rt *probeChainRuntime) latestPhysicalBridgeSession() *probeChainBridgeSession {
	if rt == nil {
		return nil
	}
	rt.bridgeMu.Lock()
	defer rt.bridgeMu.Unlock()
	return rt.latestAnyBridgeSessionLocked()
}

func probeChainBridgeSessionByIDLocked(items map[string]*probeChainBridgeSession, sessionID string) *probeChainBridgeSession {
	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		return nil
	}
	item, ok := items[cleanID]
	if !ok || item == nil || item.Session == nil || item.Session.IsClosed() {
		return nil
	}
	return item
}

func (rt *probeChainRuntime) bridgeSessionByIDLocked(sessionID string) *probeChainBridgeSession {
	if item := probeChainBridgeSessionByIDLocked(rt.downstreamSessions, sessionID); item != nil {
		return item
	}
	return probeChainBridgeSessionByIDLocked(rt.upstreamSessions, sessionID)
}

func (rt *probeChainRuntime) setDownstreamSession(sessionID string, session *probeChainFrameSession, bridgeRole string, remoteAddr string) {
	if rt == nil || session == nil {
		return
	}
	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		cleanID = rt.nextBridgeSessionID("downstream")
	}
	item := &probeChainBridgeSession{
		ID:          cleanID,
		Session:     session,
		BridgeRole:  strings.TrimSpace(bridgeRole),
		RemoteAddr:  strings.TrimSpace(remoteAddr),
		ConnectedAt: time.Now().UTC(),
	}
	var superseded []*probeChainFrameSession
	rt.bridgeMu.Lock()
	if rt.singleBridgeSessionPerRule() {
		superseded = rt.replaceBridgeSessionsLocked(session)
	}
	if rt.downstreamSessions == nil {
		rt.downstreamSessions = make(map[string]*probeChainBridgeSession)
	}
	rt.downstreamSessions[cleanID] = item
	active := len(rt.downstreamSessions) + len(rt.upstreamSessions)
	rt.bridgeMu.Unlock()
	closeSupersededProbeChainBridgeSessions(rt.cfg.chainID, rt.cfg.role, "downstream", superseded)
	log.Printf("probe chain %s assigned: chain=%s role=%s session_id=%s active=%d remote=%s single_per_rule=%t", rt.bridgeSessionLogLabel("downstream"), strings.TrimSpace(rt.cfg.chainID), strings.TrimSpace(rt.cfg.role), cleanID, active, item.RemoteAddr, rt.singleBridgeSessionPerRule())
}

func (rt *probeChainRuntime) clearDownstreamSession(sessionID string, target *probeChainFrameSession) {
	if rt == nil || target == nil {
		return
	}
	cleanID := strings.TrimSpace(sessionID)
	cleared := false
	remaining := 0
	rt.bridgeMu.Lock()
	if cleanID != "" {
		if item, ok := rt.downstreamSessions[cleanID]; ok && item != nil && item.Session == target {
			delete(rt.downstreamSessions, cleanID)
			cleared = true
		}
		if rt.singleBridgeSessionPerRule() {
			if item, ok := rt.upstreamSessions[cleanID]; ok && item != nil && item.Session == target {
				delete(rt.upstreamSessions, cleanID)
				cleared = true
			}
		}
	} else {
		for key, item := range rt.downstreamSessions {
			if item != nil && item.Session == target {
				delete(rt.downstreamSessions, key)
				cleanID = key
				cleared = true
				break
			}
		}
		if rt.singleBridgeSessionPerRule() && !cleared {
			for key, item := range rt.upstreamSessions {
				if item != nil && item.Session == target {
					delete(rt.upstreamSessions, key)
					cleanID = key
					cleared = true
					break
				}
			}
		}
	}
	remaining = len(rt.downstreamSessions) + len(rt.upstreamSessions)
	rt.bridgeMu.Unlock()
	log.Printf("probe chain %s cleared: chain=%s role=%s session_id=%s target=%p cleared=%t remaining=%d", rt.bridgeSessionLogLabel("downstream"), strings.TrimSpace(rt.cfg.chainID), strings.TrimSpace(rt.cfg.role), cleanID, target, cleared, remaining)
}

func (rt *probeChainRuntime) getDownstreamSession() *probeChainFrameSession {
	if rt == nil {
		return nil
	}
	rt.bridgeMu.Lock()
	defer rt.bridgeMu.Unlock()
	latest := latestProbeChainBridgeSessionLocked(rt.downstreamSessions)
	if latest == nil && rt.singleBridgeSessionPerRule() {
		latest = rt.latestAnyBridgeSessionLocked()
	}
	if latest == nil {
		return nil
	}
	return latest.Session
}

func (rt *probeChainRuntime) setUpstreamSession(sessionID string, session *probeChainFrameSession, bridgeRole string, remoteAddr string) {
	if rt == nil || session == nil {
		return
	}
	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		cleanID = rt.nextBridgeSessionID("upstream")
	}
	item := &probeChainBridgeSession{
		ID:          cleanID,
		Session:     session,
		BridgeRole:  strings.TrimSpace(bridgeRole),
		RemoteAddr:  strings.TrimSpace(remoteAddr),
		ConnectedAt: time.Now().UTC(),
	}
	var superseded []*probeChainFrameSession
	rt.bridgeMu.Lock()
	if rt.singleBridgeSessionPerRule() {
		superseded = rt.replaceBridgeSessionsLocked(session)
	}
	if rt.upstreamSessions == nil {
		rt.upstreamSessions = make(map[string]*probeChainBridgeSession)
	}
	rt.upstreamSessions[cleanID] = item
	active := len(rt.downstreamSessions) + len(rt.upstreamSessions)
	rt.bridgeMu.Unlock()
	closeSupersededProbeChainBridgeSessions(rt.cfg.chainID, rt.cfg.role, "upstream", superseded)
	log.Printf("probe chain %s assigned: chain=%s role=%s session_id=%s active=%d remote=%s single_per_rule=%t", rt.bridgeSessionLogLabel("upstream"), strings.TrimSpace(rt.cfg.chainID), strings.TrimSpace(rt.cfg.role), cleanID, active, item.RemoteAddr, rt.singleBridgeSessionPerRule())
}

func (rt *probeChainRuntime) clearUpstreamSession(sessionID string, target *probeChainFrameSession) {
	if rt == nil || target == nil {
		return
	}
	cleanID := strings.TrimSpace(sessionID)
	cleared := false
	remaining := 0
	rt.bridgeMu.Lock()
	if cleanID != "" {
		if item, ok := rt.upstreamSessions[cleanID]; ok && item != nil && item.Session == target {
			delete(rt.upstreamSessions, cleanID)
			cleared = true
		}
		if rt.singleBridgeSessionPerRule() {
			if item, ok := rt.downstreamSessions[cleanID]; ok && item != nil && item.Session == target {
				delete(rt.downstreamSessions, cleanID)
				cleared = true
			}
		}
	} else {
		for key, item := range rt.upstreamSessions {
			if item != nil && item.Session == target {
				delete(rt.upstreamSessions, key)
				cleanID = key
				cleared = true
				break
			}
		}
		if rt.singleBridgeSessionPerRule() && !cleared {
			for key, item := range rt.downstreamSessions {
				if item != nil && item.Session == target {
					delete(rt.downstreamSessions, key)
					cleanID = key
					cleared = true
					break
				}
			}
		}
	}
	remaining = len(rt.downstreamSessions) + len(rt.upstreamSessions)
	rt.bridgeMu.Unlock()
	log.Printf("probe chain %s cleared: chain=%s role=%s session_id=%s target=%p cleared=%t remaining=%d", rt.bridgeSessionLogLabel("upstream"), strings.TrimSpace(rt.cfg.chainID), strings.TrimSpace(rt.cfg.role), cleanID, target, cleared, remaining)
}

func (rt *probeChainRuntime) bridgeSessionLogLabel(legacy string) string {
	if rt != nil && rt.singleBridgeSessionPerRule() {
		return "bridge session"
	}
	return strings.TrimSpace(legacy) + " session"
}

func (rt *probeChainRuntime) bridgeStreamLogLabel(legacy string) string {
	if rt != nil && rt.singleBridgeSessionPerRule() {
		return "bridge stream"
	}
	return strings.TrimSpace(legacy) + " stream"
}

func (rt *probeChainRuntime) bridgeDialTag(tag string) string {
	if rt != nil && rt.singleBridgeSessionPerRule() {
		return "physical-bridge"
	}
	return strings.TrimSpace(tag)
}

func (rt *probeChainRuntime) bridgeDialLogFields(target probeChainBridgeDialTarget) string {
	if rt != nil && rt.singleBridgeSessionPerRule() {
		return fmt.Sprintf("physical_bridge=true accept_streams=%t", target.AcceptStreams)
	}
	return fmt.Sprintf("assign_downstream=%t assign_upstream=%t accept_streams=%t", target.AssignDownstream, target.AssignUpstream, target.AcceptStreams)
}

func (rt *probeChainRuntime) bridgeUnavailableError(legacy string, preferredSessionID string, sawSession bool, lastOpenErr error) error {
	label := strings.TrimSpace(legacy) + " bridge"
	if rt != nil && rt.singleBridgeSessionPerRule() {
		label = "bridge"
	}
	if sawSession && lastOpenErr != nil {
		return fmt.Errorf("%s stream open failed: %w", label, lastOpenErr)
	}
	if strings.TrimSpace(preferredSessionID) != "" {
		return fmt.Errorf("%s is unavailable for session_id=%s", label, strings.TrimSpace(preferredSessionID))
	}
	return fmt.Errorf("%s is unavailable", label)
}

func (rt *probeChainRuntime) getUpstreamSession() *probeChainFrameSession {
	if rt == nil {
		return nil
	}
	rt.bridgeMu.Lock()
	defer rt.bridgeMu.Unlock()
	latest := latestProbeChainBridgeSessionLocked(rt.upstreamSessions)
	if latest == nil && rt.singleBridgeSessionPerRule() {
		latest = rt.latestAnyBridgeSessionLocked()
	}
	if latest == nil {
		return nil
	}
	return latest.Session
}

func (rt *probeChainRuntime) describeBridgeSession(session *probeChainFrameSession, fallbackRole string) (string, string) {
	if rt == nil || session == nil {
		return "", strings.TrimSpace(fallbackRole)
	}
	rt.bridgeMu.Lock()
	defer rt.bridgeMu.Unlock()
	for _, item := range rt.downstreamSessions {
		if item != nil && item.Session == session {
			return strings.TrimSpace(item.ID), firstNonEmpty(strings.TrimSpace(item.BridgeRole), "downstream")
		}
	}
	for _, item := range rt.upstreamSessions {
		if item != nil && item.Session == session {
			return strings.TrimSpace(item.ID), firstNonEmpty(strings.TrimSpace(item.BridgeRole), "upstream")
		}
	}
	return "", strings.TrimSpace(fallbackRole)
}

func (rt *probeChainRuntime) snapshotBridgeSessions() []probeChainBridgeSessionSnapshot {
	if rt == nil {
		return nil
	}
	now := time.Now().UTC()
	rt.bridgeMu.Lock()
	items := make([]probeChainBridgeSessionSnapshot, 0, len(rt.downstreamSessions)+len(rt.upstreamSessions))
	appendItems := func(direction string, sessions map[string]*probeChainBridgeSession) {
		for _, item := range sessions {
			if item == nil {
				continue
			}
			snapshot := probeChainBridgeSessionSnapshot{
				ChainID:     strings.TrimSpace(rt.cfg.chainID),
				RuntimeRole: strings.TrimSpace(rt.cfg.role),
				Direction:   strings.TrimSpace(direction),
				SessionID:   strings.TrimSpace(item.ID),
				BridgeRole:  strings.TrimSpace(item.BridgeRole),
				RemoteAddr:  strings.TrimSpace(item.RemoteAddr),
			}
			if !item.ConnectedAt.IsZero() {
				snapshot.ConnectedAt = item.ConnectedAt.UTC().Format(time.RFC3339)
				snapshot.ConnectedMS = now.Sub(item.ConnectedAt).Milliseconds()
			}
			if item.Session != nil {
				snapshot.Closed = item.Session.IsClosed()
				snapshot.StreamsCurrent = item.Session.NumStreams()
				ping := item.Session.PingStats()
				snapshot.RTTMS = probeDurationMilliseconds(ping.RTT)
				snapshot.PingsSent = ping.PingsSent
				snapshot.PongsReceived = ping.PongsReceived
				snapshot.PingTimeouts = ping.Timeouts
				snapshot.PendingPings = ping.Pending
				if !ping.LastPingAt.IsZero() {
					snapshot.LastPingAt = ping.LastPingAt.Format(time.RFC3339)
				}
				if !ping.LastPongAt.IsZero() {
					snapshot.LastPongAt = ping.LastPongAt.Format(time.RFC3339)
				}
				frameIO := item.Session.IOStats()
				snapshot.FramesSent = frameIO.FramesSent
				snapshot.FrameBytesSent = frameIO.FrameBytesSent
				snapshot.FramesReceived = frameIO.FramesReceived
				snapshot.FrameBytesReceived = frameIO.FrameBytesReceived
				if !frameIO.LastFrameSentAt.IsZero() {
					snapshot.LastFrameSentAt = frameIO.LastFrameSentAt.Format(time.RFC3339)
				}
				if !frameIO.LastFrameReceivedAt.IsZero() {
					snapshot.LastFrameReceivedAt = frameIO.LastFrameReceivedAt.Format(time.RFC3339)
				}
			} else {
				snapshot.Closed = true
			}
			items = append(items, snapshot)
		}
	}
	appendItems("downstream", rt.downstreamSessions)
	appendItems("upstream", rt.upstreamSessions)
	rt.bridgeMu.Unlock()
	return items
}

func (rt *probeChainRuntime) snapshotBridgeStatus() probeChainBridgeRuntimeStatus {
	sessions := rt.snapshotBridgeSessions()
	status := probeChainBridgeRuntimeStatus{
		Sessions:  sessions,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	for _, item := range sessions {
		if item.Closed {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.Direction)) {
		case "downstream":
			status.DownstreamActive++
		case "upstream":
			status.UpstreamActive++
		}
	}
	return status
}

func snapshotProbeChainBridgeSessions() []probeChainBridgeSessionSnapshot {
	probeChainRuntimeState.mu.Lock()
	runtimes := make([]*probeChainRuntime, 0, len(probeChainRuntimeState.runtimes))
	for _, rt := range probeChainRuntimeState.runtimes {
		if rt != nil {
			runtimes = append(runtimes, rt)
		}
	}
	probeChainRuntimeState.mu.Unlock()
	items := make([]probeChainBridgeSessionSnapshot, 0)
	for _, rt := range runtimes {
		items = append(items, rt.snapshotBridgeSessions()...)
	}
	return items
}

func stopProbeChainRuntime(chainID string, reason string) bool {
	target := strings.TrimSpace(chainID)
	if target == "" {
		return false
	}
	probeChainRuntimeState.mu.Lock()
	rt, ok := probeChainRuntimeState.runtimes[target]
	if ok {
		delete(probeChainRuntimeState.runtimes, target)
	}
	probeChainRuntimeState.mu.Unlock()
	if !ok || rt == nil {
		return false
	}
	close(rt.stopCh)
	rt.closeRuntimeResources()
	log.Printf("probe chain runtime stopped: chain=%s reason=%s", target, strings.TrimSpace(reason))
	return true
}

func stopAllProbeChainRuntimes(reason string) int {
	probeChainRuntimeState.mu.Lock()
	ids := make([]string, 0, len(probeChainRuntimeState.runtimes))
	for id := range probeChainRuntimeState.runtimes {
		ids = append(ids, id)
	}
	probeChainRuntimeState.mu.Unlock()
	stopped := 0
	for _, id := range ids {
		if stopProbeChainRuntime(id, reason) {
			stopped++
		}
	}
	return stopped
}

func handleProbeChainConn(runtime *probeChainRuntime, conn net.Conn, preferredSessionID string) {
	defer conn.Close()

	if _, ok := conn.(*probeChainFrameStream); ok {
		_ = conn.SetDeadline(time.Time{})
		if handleProbeChainPingPongStreamIfNeeded(runtime, conn) {
			return
		}
		if handleProbeChainVirtualRouterStreamIfNeeded(runtime, conn) {
			return
		}
		if runtime.cfg.nextAuthMode == "proxy" {
			handleProbeChainProxyStream(runtime, conn)
			return
		}
		openReq := probeChainOpenRequestFromConn(conn)
		nextHop, err := openProbeChainNextHop(runtime, preferredSessionID, openReq)
		if err != nil {
			log.Printf("probe chain open %s failed: chain=%s role=%s err=%v", runtime.bridgeStreamLogLabel("downstream"), runtime.cfg.chainID, runtime.cfg.role, err)
			return
		}
		defer func() {
			if nextHop != nil && nextHop.CloseFn != nil {
				_ = nextHop.CloseFn()
			}
		}()
		result := relayProbeChainDuplex(
			conn,
			nextHop.Writer,
			func() { closeProbeChainWriter(nextHop.Writer) },
			nextHop.Reader,
			conn,
			func() { closeProbeChainConnWrite(conn) },
		)
		if relayErr := firstProbeChainRelayError(result); relayErr != nil {
			log.Printf("probe chain %s frame relay failed: chain=%s role=%s duration_ms=%d up_bytes=%d down_bytes=%d err=%v", runtime.bridgeStreamLogLabel("downstream"), runtime.cfg.chainID, runtime.cfg.role, result.Duration.Milliseconds(), result.LeftToRight.Bytes, result.RightToLeft.Bytes, relayErr)
		}
		return
	}

	log.Printf("probe chain rejected non-frame %s: chain=%s role=%s remote=%s", runtime.bridgeStreamLogLabel("downstream"), runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String())
}

func handleProbeChainReverseConn(runtime *probeChainRuntime, conn net.Conn, preferredSessionID string) {
	defer conn.Close()

	if _, ok := conn.(*probeChainFrameStream); ok {
		_ = conn.SetDeadline(time.Time{})
		if handleProbeChainPingPongStreamIfNeeded(runtime, conn) {
			return
		}
		if handleProbeChainVirtualRouterStreamIfNeeded(runtime, conn) {
			return
		}
		role := normalizeProbeChainRole(runtime.cfg.role)
		if role == "entry" || role == "entry_exit" {
			handleProbeChainProxyStream(runtime, conn)
			return
		}
		openReq := probeChainOpenRequestFromConn(conn)
		prevHop, err := openProbeChainPrevHop(runtime, preferredSessionID, openReq)
		if err != nil {
			log.Printf("probe chain open %s failed: chain=%s role=%s err=%v", runtime.bridgeStreamLogLabel("upstream"), runtime.cfg.chainID, runtime.cfg.role, err)
			return
		}
		defer func() {
			if prevHop != nil && prevHop.CloseFn != nil {
				_ = prevHop.CloseFn()
			}
		}()
		result := relayProbeChainDuplex(
			conn,
			prevHop.Writer,
			func() { closeProbeChainWriter(prevHop.Writer) },
			prevHop.Reader,
			conn,
			func() { closeProbeChainConnWrite(conn) },
		)
		if relayErr := firstProbeChainRelayError(result); relayErr != nil {
			log.Printf("probe chain %s frame relay failed: chain=%s role=%s duration_ms=%d up_bytes=%d down_bytes=%d err=%v", runtime.bridgeStreamLogLabel("upstream"), runtime.cfg.chainID, runtime.cfg.role, result.Duration.Milliseconds(), result.LeftToRight.Bytes, result.RightToLeft.Bytes, relayErr)
		}
		return
	}

	log.Printf("probe chain rejected non-frame %s: chain=%s role=%s remote=%s", runtime.bridgeStreamLogLabel("upstream"), runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String())
}

func openProbeChainNextHop(runtime *probeChainRuntime, preferredSessionID string, request probeChainTunnelOpenRequest) (*probeChainNextHop, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	if runtime.cfg.nextAuthMode == "proxy" {
		return nil, errors.New("next hop is proxy mode")
	}
	stream, monitor, err := openProbeChainDownstreamStream(runtime, strings.TrimSpace(preferredSessionID), probeChainDownstreamOpenTimeout, request)
	if err != nil {
		return nil, err
	}
	return &probeChainNextHop{
		Writer:  stream,
		Reader:  stream,
		Monitor: monitor,
		CloseFn: func() error {
			return stream.Close()
		},
	}, nil
}

func openProbeChainPrevHop(runtime *probeChainRuntime, preferredSessionID string, request probeChainTunnelOpenRequest) (*probeChainNextHop, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	stream, monitor, err := openProbeChainUpstreamStream(runtime, strings.TrimSpace(preferredSessionID), probeChainDownstreamOpenTimeout, request)
	if err != nil {
		return nil, err
	}
	return &probeChainNextHop{
		Writer:  stream,
		Reader:  stream,
		Monitor: monitor,
		CloseFn: func() error {
			return stream.Close()
		},
	}, nil
}

func handleProbeChainPingPongStreamIfNeeded(runtime *probeChainRuntime, conn net.Conn) bool {
	frameStream, ok := conn.(*probeChainFrameStream)
	if !ok {
		return false
	}
	req, found := frameStream.OpenRequest()
	if !found || !strings.EqualFold(strings.TrimSpace(req.Type), probeChainRelayModePingPong) {
		return false
	}
	handleProbeChainPingPongStream(runtime, conn, req.PingBytes, frameStream.RespondOpen)
	return true
}

func handleProbeChainVirtualRouterStreamIfNeeded(runtime *probeChainRuntime, conn net.Conn) bool {
	return false
}

func (rt *probeChainRuntime) getDownstreamSessionByID(sessionID string) *probeChainFrameSession {
	if rt == nil {
		return nil
	}
	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		return rt.getDownstreamSession()
	}
	rt.bridgeMu.Lock()
	defer rt.bridgeMu.Unlock()
	item := probeChainBridgeSessionByIDLocked(rt.downstreamSessions, cleanID)
	if item == nil && rt.singleBridgeSessionPerRule() {
		item = rt.bridgeSessionByIDLocked(cleanID)
	}
	if item == nil {
		return nil
	}
	return item.Session
}

func (rt *probeChainRuntime) getUpstreamSessionByID(sessionID string) *probeChainFrameSession {
	if rt == nil {
		return nil
	}
	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		return rt.getUpstreamSession()
	}
	rt.bridgeMu.Lock()
	defer rt.bridgeMu.Unlock()
	item := probeChainBridgeSessionByIDLocked(rt.upstreamSessions, cleanID)
	if item == nil && rt.singleBridgeSessionPerRule() {
		item = rt.bridgeSessionByIDLocked(cleanID)
	}
	if item == nil {
		return nil
	}
	return item.Session
}

func openProbeChainDownstreamStream(runtime *probeChainRuntime, preferredSessionID string, timeout time.Duration, request probeChainTunnelOpenRequest) (net.Conn, probeChainFrameStreamMonitor, error) {
	if runtime == nil {
		return nil, probeChainFrameStreamMonitor{}, errors.New("runtime is nil")
	}
	if runtime.singleBridgeSessionPerRule() {
		return nil, probeChainFrameStreamMonitor{}, errors.New("virtual router does not use probe chain downstream streams")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	sawSession := false
	var lastOpenErr error
	for {
		session := runtime.getDownstreamSessionByID(preferredSessionID)
		if session != nil && !session.IsClosed() {
			sawSession = true
			sessionID, sessionRole := runtime.describeBridgeSession(session, "downstream")
			streamsOpen := session.NumStreams()
			startedAt := time.Now()
			stream, openErr := session.OpenWithRequest(request, timeout)
			openLatency := time.Since(startedAt)
			if openErr == nil {
				return stream, probeChainFrameStreamMonitor{
					Session:             session,
					SessionID:           sessionID,
					SessionRole:         sessionRole,
					SessionStreamsOpen:  streamsOpen,
					SessionStreamsAfter: session.NumStreams(),
					OpenLatency:         openLatency,
					PingStats:           session.PingStats(),
				}, nil
			}
			lastOpenErr = openErr
			if session.IsClosed() {
				runtime.clearDownstreamSession("", session)
			}
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-runtime.stopCh:
			return nil, probeChainFrameStreamMonitor{}, errors.New("runtime stopped")
		case <-time.After(300 * time.Millisecond):
		}
	}
	return nil, probeChainFrameStreamMonitor{}, runtime.bridgeUnavailableError("downstream", preferredSessionID, sawSession, lastOpenErr)
}

func openProbeChainUpstreamStream(runtime *probeChainRuntime, preferredSessionID string, timeout time.Duration, request probeChainTunnelOpenRequest) (net.Conn, probeChainFrameStreamMonitor, error) {
	if runtime == nil {
		return nil, probeChainFrameStreamMonitor{}, errors.New("runtime is nil")
	}
	if runtime.singleBridgeSessionPerRule() {
		return nil, probeChainFrameStreamMonitor{}, errors.New("virtual router does not use probe chain upstream streams")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	attempt := 0
	sawSession := false
	var lastOpenErr error
	for {
		attempt++
		session := runtime.getUpstreamSessionByID(preferredSessionID)
		if session != nil {
			sawSession = true
			closed := session.IsClosed()
			log.Printf("probe chain %s attempt: chain=%s role=%s attempt=%d session=%p closed=%t", runtime.bridgeStreamLogLabel("upstream"), runtime.cfg.chainID, runtime.cfg.role, attempt, session, closed)
			if !closed {
				sessionID, sessionRole := runtime.describeBridgeSession(session, "upstream")
				streamsOpen := session.NumStreams()
				startedAt := time.Now()
				stream, openErr := session.OpenWithRequest(request, timeout)
				openLatency := time.Since(startedAt)
				if openErr == nil {
					log.Printf("probe chain %s opened: chain=%s role=%s attempt=%d session=%p", runtime.bridgeStreamLogLabel("upstream"), runtime.cfg.chainID, runtime.cfg.role, attempt, session)
					return stream, probeChainFrameStreamMonitor{
						Session:             session,
						SessionID:           sessionID,
						SessionRole:         sessionRole,
						SessionStreamsOpen:  streamsOpen,
						SessionStreamsAfter: session.NumStreams(),
						OpenLatency:         openLatency,
						PingStats:           session.PingStats(),
					}, nil
				}
				lastOpenErr = openErr
				log.Printf("probe chain %s open failed: chain=%s role=%s attempt=%d session=%p err=%v", runtime.bridgeStreamLogLabel("upstream"), runtime.cfg.chainID, runtime.cfg.role, attempt, session, openErr)
				if session.IsClosed() {
					log.Printf("probe chain %s became closed while opening stream: chain=%s role=%s attempt=%d session=%p", runtime.bridgeSessionLogLabel("upstream"), runtime.cfg.chainID, runtime.cfg.role, attempt, session)
					runtime.clearUpstreamSession("", session)
				}
			}
		} else {
			log.Printf("probe chain %s attempt: chain=%s role=%s attempt=%d session=nil", runtime.bridgeStreamLogLabel("upstream"), runtime.cfg.chainID, runtime.cfg.role, attempt)
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-runtime.stopCh:
			return nil, probeChainFrameStreamMonitor{}, errors.New("runtime stopped")
		case <-time.After(300 * time.Millisecond):
		}
	}
	log.Printf("probe chain %s unavailable: chain=%s role=%s attempts=%d timeout=%s session_id=%s", runtime.bridgeStreamLogLabel("upstream"), runtime.cfg.chainID, runtime.cfg.role, attempt, timeout, strings.TrimSpace(preferredSessionID))
	return nil, probeChainFrameStreamMonitor{}, runtime.bridgeUnavailableError("upstream", preferredSessionID, sawSession, lastOpenErr)
}

func resolveProbeChainOutboundLinkLayer(cfg probeChainRuntimeConfig) string {
	return normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(cfg.nextLinkLayer), strings.TrimSpace(cfg.linkLayer)))
}

func openProbeChainBridgeRelayNetConn(cfg probeChainRuntimeConfig, target probeChainBridgeDialTarget) (net.Conn, error) {
	if strings.EqualFold(strings.TrimSpace(cfg.chainType), "virtual_router") || isProbeVirtualRouterRuntimeChainID(cfg.chainID) {
		if target.PreserveRelayDomain {
			return openProbeVirtualRouterBridgeRelayNetConnWithDomainPolicy(
				cfg.chainID,
				cfg.secret,
				target.Host,
				target.Port,
				target.LinkLayer,
				target.RoleHeader,
				probeChainPortForwardDialTimeout+probeChainPortForwardResponseReadDeadline,
				true,
			)
		}
		return openProbeVirtualRouterBridgeRelayNetConn(
			cfg.chainID,
			cfg.secret,
			target.Host,
			target.Port,
			target.LinkLayer,
			target.RoleHeader,
		)
	}
	if target.PreserveRelayDomain {
		return openProbeChainRelayNetConnWithLayerConnAndDomainPolicy(
			cfg.chainID,
			cfg.secret,
			target.Host,
			target.Port,
			target.LinkLayer,
			target.RoleHeader,
			probeChainPortForwardDialTimeout+probeChainPortForwardResponseReadDeadline,
			true,
		)
	}
	return openProbeChainRelayNetConn(
		cfg.chainID,
		cfg.secret,
		target.Host,
		target.Port,
		target.LinkLayer,
		target.RoleHeader,
	)
}

func handleProbeChainRelayHTTP(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func getProbeChainRuntime(chainID string) *probeChainRuntime {
	target := strings.TrimSpace(chainID)
	if target == "" {
		return nil
	}
	probeChainRuntimeState.mu.Lock()
	runtime := probeChainRuntimeState.runtimes[target]
	probeChainRuntimeState.mu.Unlock()
	return runtime
}

func resolveProbeChainLoopbackHost(raw string) string {
	host := strings.TrimSpace(strings.Trim(raw, "[]"))
	if host == "" {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if ip.To4() != nil {
			return "127.0.0.1"
		}
		return "::1"
	}
	if host == "::" {
		return "::1"
	}
	return host
}

type probeChainTunedTCPListener struct {
	net.Listener
}

func (l *probeChainTunedTCPListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tuneProbeChainNetConn(conn)
	return conn, nil
}

func newProbeChainQUICConfig(maxIncomingStreams int64) *quic.Config {
	cfg := &quic.Config{
		Versions:                       []quic.Version{quic.Version2, quic.Version1},
		EnableDatagrams:                true,
		KeepAlivePeriod:                10 * time.Second,
		InitialStreamReceiveWindow:     probeChainRelayQUICInitialStreamWindow,
		MaxStreamReceiveWindow:         probeChainRelayQUICMaxStreamWindow,
		InitialConnectionReceiveWindow: probeChainRelayQUICInitialConnectionWindow,
		MaxConnectionReceiveWindow:     probeChainRelayQUICMaxConnectionWindow,
	}
	if maxIncomingStreams > 0 {
		cfg.MaxIncomingStreams = maxIncomingStreams
	}
	return cfg
}

func probeChainCopy(dst io.Writer, src io.Reader) (int64, error) {
	buf, _ := probeChainCopyBufferPool.Get().([]byte)
	if len(buf) == 0 {
		buf = make([]byte, probeChainRelayIOCopyBufferBytes)
	}
	defer probeChainCopyBufferPool.Put(buf)
	return io.CopyBuffer(dst, src, buf)
}

func handleProbeChainProxyStream(runtime *probeChainRuntime, stream net.Conn) {
	if stream == nil {
		return
	}
	defer stream.Close()

	var req probeChainTunnelOpenRequest
	var responder func(probeChainTunnelOpenResponse) error
	frameStream, ok := stream.(*probeChainFrameStream)
	if ok {
		var found bool
		req, found = frameStream.OpenRequest()
		if !found {
			logProbeChainProxyOpenDecodeFailure(runtime, errors.New("missing frame open request"))
			return
		}
		responder = frameStream.RespondOpen
	} else {
		logProbeChainProxyOpenDecodeFailure(runtime, errors.New("non-frame probe chain stream is unsupported"))
		return
	}

	if strings.EqualFold(strings.TrimSpace(req.Type), probeChainRelayModePrepare) {
		if err := responder(probeChainTunnelOpenResponse{OK: true}); err != nil {
			return
		}
		updateReq, err := frameStream.WaitOpenUpdate(probeChainPortForwardPreconnectIdleTTL + probeChainPortForwardResponseReadDeadline)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				chainID := ""
				role := ""
				if runtime != nil {
					chainID = strings.TrimSpace(runtime.cfg.chainID)
					role = strings.TrimSpace(runtime.cfg.role)
				}
				log.Printf("probe chain proxy prepared stream closed before open_update: chain=%s role=%s err=%v", chainID, role, err)
			}
			return
		}
		req = updateReq
		responder = frameStream.RespondOpenUpdate
	}

	handleProbeChainProxyOpenRequest(runtime, stream, req, responder)
}

func logProbeChainProxyOpenDecodeFailure(runtime *probeChainRuntime, err error) {
	chainID := ""
	role := ""
	if runtime != nil {
		chainID = strings.TrimSpace(runtime.cfg.chainID)
		role = strings.TrimSpace(runtime.cfg.role)
	}
	log.Printf("probe chain proxy open request decode failed: chain=%s role=%s err=%v", chainID, role, err)
}

func handleProbeChainProxyOpenRequest(runtime *probeChainRuntime, stream net.Conn, req probeChainTunnelOpenRequest, responder func(probeChainTunnelOpenResponse) error) {
	if responder == nil {
		logProbeChainProxyOpenDecodeFailure(runtime, errors.New("missing frame open responder"))
		return
	}
	if strings.EqualFold(strings.TrimSpace(req.Type), probeChainRelayModePingPong) {
		handleProbeChainPingPongStream(runtime, stream, req.PingBytes, responder)
		return
	}
	if strings.EqualFold(strings.TrimSpace(req.Type), "tcp_debug_get") {
		if err := responder(probeChainTunnelOpenResponse{OK: true}); err != nil {
			return
		}
		handleProbeChainTCPDebugGet(runtime, stream, req)
		return
	}
	if strings.EqualFold(strings.TrimSpace(req.Type), "speed_debug_get") {
		if err := responder(probeChainTunnelOpenResponse{OK: true}); err != nil {
			return
		}
		handleProbeChainSpeedDebugGet(runtime, stream, req)
		return
	}
	if strings.EqualFold(strings.TrimSpace(req.Type), "peer_status_get") {
		if err := responder(probeChainTunnelOpenResponse{OK: true}); err != nil {
			return
		}
		handleProbeChainPeerStatusGet(runtime, stream, req)
		return
	}
	if strings.EqualFold(strings.TrimSpace(req.Type), "substreams_get") {
		if err := responder(probeChainTunnelOpenResponse{OK: true}); err != nil {
			return
		}
		handleProbeChainSubstreamsGet(runtime, stream, req)
		return
	}

	requestedSessionID := strings.TrimSpace(req.SessionID)
	network := strings.ToLower(strings.TrimSpace(req.Network))
	if network == "" {
		network = "tcp"
	}
	target := strings.TrimSpace(req.Address)
	if target == "" {
		_ = responder(probeChainTunnelOpenResponse{OK: false, Error: "missing address"})
		return
	}

	if requestedSessionID != "" && runtime != nil {
		log.Printf("probe chain proxy open request: chain=%s role=%s network=%s target=%s session_id=%s", strings.TrimSpace(runtime.cfg.chainID), strings.TrimSpace(runtime.cfg.role), network, target, requestedSessionID)
	}

	associationV2 := req.AssociationV2
	flowID := resolveProbeChainTunnelOpenFlowID(req)
	var proxyErr error
	switch network {
	case "tcp":
		proxyErr = handleProbeChainTunnelTCPStream(stream, target, flowID, responder)
	case "udp":
		proxyErr = handleProbeChainTunnelUDPStream(stream, target, associationV2, responder)
	default:
		proxyErr = responder(probeChainTunnelOpenResponse{OK: false, Error: "unsupported network"})
	}
	if proxyErr == nil || errors.Is(proxyErr, io.EOF) || errors.Is(proxyErr, net.ErrClosed) {
		return
	}
	chainID := ""
	role := ""
	if runtime != nil {
		chainID = strings.TrimSpace(runtime.cfg.chainID)
		role = strings.TrimSpace(runtime.cfg.role)
	}
	remote := ""
	if stream.RemoteAddr() != nil {
		remote = strings.TrimSpace(stream.RemoteAddr().String())
	}
	log.Printf("probe chain tunnel stream failed: chain=%s role=%s remote=%s err=%v", chainID, role, remote, proxyErr)
}

func handleProbeChainTCPDebugGet(runtime *probeChainRuntime, stream net.Conn, req probeChainTunnelOpenRequest) {
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = "chain-tcp-debug-" + randomHexToken(8)
	}
	nodeID := ""
	if runtime != nil {
		nodeID = strings.TrimSpace(runtime.cfg.identity.NodeID)
	}
	payload := globalProbeTCPDebugState.snapshotPayload(nodeID, requestID)
	payload.Scope = "chain_exit"
	if err := writeProbeChainTunnelJSONResponse(stream, payload); err != nil && runtime != nil {
		log.Printf("probe chain tcp debug response failed: chain=%s role=%s request_id=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, requestID, err)
	}
}

func handleProbeChainSpeedDebugGet(runtime *probeChainRuntime, stream net.Conn, req probeChainTunnelOpenRequest) {
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = "chain-speed-debug-" + randomHexToken(8)
	}
	nodeID := ""
	if runtime != nil {
		nodeID = strings.TrimSpace(runtime.cfg.identity.NodeID)
	}
	payload := globalProbeSpeedDebugState.snapshotPayload(nodeID, requestID)
	payload.Scope = "chain_exit"
	if err := writeProbeChainTunnelJSONResponse(stream, payload); err != nil && runtime != nil {
		log.Printf("probe chain speed debug response failed: chain=%s role=%s request_id=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, requestID, err)
	}
}

func handleProbeChainPeerStatusGet(runtime *probeChainRuntime, stream net.Conn, req probeChainTunnelOpenRequest) {
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = "chain-peer-status-" + randomHexToken(8)
	}
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = "chain_exit"
	}
	nodeID := ""
	protocolState := probeChainRelayProtocolStateSnapshot{}
	if runtime != nil {
		nodeID = strings.TrimSpace(runtime.cfg.identity.NodeID)
		protocolState = snapshotProbeChainProtocolState(runtime.cfg.listenHost, runtime.cfg.listenPort)
	}
	payload := snapshotProbePeerStatusSidePayload(nodeID, requestID, scope, protocolState)
	if err := writeProbeChainTunnelJSONResponse(stream, payload); err != nil && runtime != nil {
		log.Printf("probe chain peer status response failed: chain=%s role=%s request_id=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, requestID, err)
	}
}

func handleProbeChainPingPongStream(runtime *probeChainRuntime, stream net.Conn, byteCount int64, responder func(probeChainTunnelOpenResponse) error) {
	if stream == nil {
		return
	}
	if byteCount <= 0 || byteCount > 64*1024 {
		byteCount = 64
	}
	if responder == nil {
		return
	}
	if err := responder(probeChainTunnelOpenResponse{OK: true}); err != nil {
		return
	}
	buf := make([]byte, byteCount)
	_ = stream.SetReadDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	_, err := io.ReadFull(stream, buf)
	_ = stream.SetReadDeadline(time.Time{})
	if err != nil {
		if runtime != nil {
			log.Printf("probe chain ping-pong read failed: chain=%s role=%s bytes=%d err=%v", runtime.cfg.chainID, runtime.cfg.role, byteCount, err)
		}
		return
	}
	_ = stream.SetWriteDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	_, err = stream.Write(buf)
	_ = stream.SetWriteDeadline(time.Time{})
	if err != nil && runtime != nil {
		log.Printf("probe chain ping-pong write failed: chain=%s role=%s bytes=%d err=%v", runtime.cfg.chainID, runtime.cfg.role, byteCount, err)
	}
}

func handleProbeChainSubstreamsGet(runtime *probeChainRuntime, stream net.Conn, req probeChainTunnelOpenRequest) {
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = "chain-substreams-" + randomHexToken(8)
	}
	nodeID := ""
	if runtime != nil {
		nodeID = strings.TrimSpace(runtime.cfg.identity.NodeID)
	}
	payload := snapshotProbeSubstreamMonitorPayload(nodeID, requestID, "chain_exit")
	if err := writeProbeChainTunnelJSONResponse(stream, payload); err != nil && runtime != nil {
		log.Printf("probe chain substreams response failed: chain=%s role=%s request_id=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, requestID, err)
	}
}

func writeProbeChainTunnelJSONResponse(stream net.Conn, resp any) error {
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := json.NewEncoder(stream).Encode(resp)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}

func probeChainOpenRequestFromConn(conn net.Conn) probeChainTunnelOpenRequest {
	req := probeChainTunnelOpenRequest{
		Type:     "open",
		Network:  probeChainPortForwardNetworkTCP,
		Priority: "normal",
	}
	frameStream, ok := conn.(*probeChainFrameStream)
	if !ok {
		return req
	}
	frameReq, found := frameStream.OpenRequest()
	if !found {
		return req
	}
	if strings.TrimSpace(frameReq.Type) == "" {
		frameReq.Type = "open"
	}
	if strings.TrimSpace(frameReq.Network) == "" {
		frameReq.Network = probeChainPortForwardNetworkTCP
	}
	if strings.TrimSpace(frameReq.Priority) == "" {
		frameReq.Priority = resolveProbeChainTunnelPriority(frameReq.Network, frameReq.Address, frameReq.AssociationV2)
	}
	return frameReq
}

func resolveProbeChainTunnelOpenFlowID(req probeChainTunnelOpenRequest) string {
	if flowID := strings.TrimSpace(req.FlowID); flowID != "" {
		return flowID
	}
	if req.AssociationV2 != nil {
		return strings.TrimSpace(req.AssociationV2.FlowID)
	}
	return ""
}

func handleProbeChainTunnelTCPStream(stream net.Conn, target string, flowID string, responder func(probeChainTunnelOpenResponse) error) error {
	if responder == nil {
		return errors.New("missing frame open responder")
	}
	dialer := &net.Dialer{Timeout: probeChainPortForwardDialTimeout}
	dialStartedAt := time.Now()
	remoteConn, err := dialer.Dial("tcp", target)
	openLatency := time.Since(dialStartedAt)
	if err != nil {
		globalProbeTCPDebugState.recordFailureWithScopeAndFlow("open_failed", "chain_exit", target, flowID, "remote", err)
		_ = responder(probeChainTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	tuneProbeChainNetConn(remoteConn)
	defer remoteConn.Close()

	if err := responder(probeChainTunnelOpenResponse{OK: true}); err != nil {
		return err
	}

	relay := globalProbeTCPDebugState.beginRelayWithScopeAndFlow("chain_exit", target, flowID, "remote")
	if relay != nil {
		relay.setOpenLatency(openLatency)
		defer relay.releaseSide()
		defer relay.releaseSide()
	}
	upWriter := io.Writer(remoteConn)
	downWriter := io.Writer(stream)
	if relay != nil {
		upWriter = &probeTCPDebugWriter{dst: remoteConn, relay: relay, direction: "up"}
		downWriter = &probeTCPDebugWriter{dst: stream, relay: relay, direction: "down"}
	}
	result := relayProbeChainBidirectionalWithWriters(stream, stream, remoteConn, remoteConn, upWriter, downWriter)
	copyErr := firstProbeChainRelayError(result)
	if copyErr == nil {
		return nil
	}
	if relay != nil {
		globalProbeTCPDebugState.recordRelayFailure(relay, copyErr)
	} else {
		globalProbeTCPDebugState.recordFailure("relay_failed", target, copyErr)
	}
	return copyErr
}

func handleProbeChainTunnelUDPStream(stream net.Conn, target string, associationV2 *probeChainAssociationV2Meta, responder func(probeChainTunnelOpenResponse) error) error {
	if responder == nil {
		return errors.New("missing frame open responder")
	}
	assoc, err := globalProbeChainUDPAssociationPool.Acquire(associationV2, target)
	if err != nil {
		_ = responder(probeChainTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	defer assoc.Release()

	if err := responder(probeChainTunnelOpenResponse{OK: true}); err != nil {
		return err
	}

	reader := bufio.NewReader(stream)
	errCh := make(chan error, 2)
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, readErr := readProbeChainFramedPacketInto(reader, buf)
			if readErr != nil {
				errCh <- readErr
				return
			}
			if n == 0 {
				continue
			}
			if writeErr := assoc.Write(buf[:n]); writeErr != nil {
				errCh <- writeErr
				return
			}
		}
	}()

	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, readErr := assoc.Read(buf)
			if n > 0 {
				writeStartedAt := time.Now()
				writeErr := writeProbeChainFramedPacket(stream, buf[:n])
				assoc.RecordFrameWrite("down", n, time.Since(writeStartedAt))
				if writeErr != nil {
					errCh <- writeErr
					return
				}
			}
			if readErr != nil {
				errCh <- readErr
				return
			}
		}
	}()

	copyErr := <-errCh
	if copyErr == nil || errors.Is(copyErr, io.EOF) || errors.Is(copyErr, net.ErrClosed) {
		return nil
	}
	return copyErr
}

func normalizeProbeChainTargetAddr(raw string, defaultPort int) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("empty target")
	}
	value = strings.TrimSpace(strings.Split(value, "/")[0])
	if value == "" {
		return "", fmt.Errorf("empty target")
	}
	if host, portStr, err := net.SplitHostPort(value); err == nil {
		host = strings.TrimSpace(strings.Trim(host, "[]"))
		portStr = strings.TrimSpace(portStr)
		if host == "" || portStr == "" {
			return "", fmt.Errorf("invalid target")
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 || port > 65535 {
			return "", fmt.Errorf("invalid target port")
		}
		return net.JoinHostPort(host, strconv.Itoa(port)), nil
	}
	host := strings.TrimSpace(strings.Trim(value, "[]"))
	if host == "" {
		return "", fmt.Errorf("invalid target host")
	}
	if defaultPort <= 0 || defaultPort > 65535 {
		defaultPort = 80
	}
	return net.JoinHostPort(host, strconv.Itoa(defaultPort)), nil
}

func relayProbeChainBidirectional(leftConn net.Conn, leftReader io.Reader, rightConn net.Conn, rightReader io.Reader) probeChainBidirectionalRelayResult {
	return relayProbeChainBidirectionalWithWriters(leftConn, leftReader, rightConn, rightReader, rightConn, leftConn)
}

func relayProbeChainBidirectionalWithWriters(leftConn net.Conn, leftReader io.Reader, rightConn net.Conn, rightReader io.Reader, rightWriter io.Writer, leftWriter io.Writer) probeChainBidirectionalRelayResult {
	return relayProbeChainDuplex(
		leftReader,
		rightWriter,
		func() { closeProbeChainConnWrite(rightConn) },
		rightReader,
		leftWriter,
		func() { closeProbeChainConnWrite(leftConn) },
	)
}

func relayProbeChainDuplex(leftReader io.Reader, rightWriter io.Writer, closeRightWrite func(), rightReader io.Reader, leftWriter io.Writer, closeLeftWrite func()) probeChainBidirectionalRelayResult {
	startedAt := time.Now()
	type relaySideResult struct {
		leftToRight bool
		bytes       int64
		err         error
	}
	done := make(chan relaySideResult, 2)
	go func() {
		n, copyErr := probeChainCopy(rightWriter, leftReader)
		if closeRightWrite != nil {
			closeRightWrite()
		}
		done <- relaySideResult{leftToRight: true, bytes: n, err: copyErr}
	}()
	go func() {
		n, copyErr := probeChainCopy(leftWriter, rightReader)
		if closeLeftWrite != nil {
			closeLeftWrite()
		}
		done <- relaySideResult{bytes: n, err: copyErr}
	}()

	var result probeChainBidirectionalRelayResult
	for i := 0; i < 2; i++ {
		side := <-done
		if side.leftToRight {
			result.LeftToRight = probeChainRelayDirectionResult{Bytes: side.bytes, Err: side.err}
		} else {
			result.RightToLeft = probeChainRelayDirectionResult{Bytes: side.bytes, Err: side.err}
		}
	}
	result.Duration = time.Since(startedAt)
	return result
}

func firstProbeChainRelayError(result probeChainBidirectionalRelayResult) error {
	if !isProbeChainRelayBenignError(result.LeftToRight.Err) {
		return result.LeftToRight.Err
	}
	if !isProbeChainRelayBenignError(result.RightToLeft.Err) {
		return result.RightToLeft.Err
	}
	return nil
}

func isProbeChainRelayBenignError(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

func formatProbeChainRelayError(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

func closeProbeChainConnWrite(conn net.Conn) {
	if closer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
		return
	}
	if stream, ok := conn.(*probeChainFrameStream); ok {
		_ = stream.Close()
	}
}

func closeProbeChainWriter(writer io.WriteCloser) {
	if writer == nil {
		return
	}
	if conn, ok := writer.(net.Conn); ok {
		closeProbeChainConnWrite(conn)
		return
	}
	_ = writer.Close()
}

func readProbeChainFramedPacket(reader *bufio.Reader) ([]byte, error) {
	frame, err := readProbeChainFrame(reader)
	if err != nil {
		return nil, err
	}
	if frame.Kind != probeChainFrameKindData {
		return nil, errors.New("invalid framed packet kind")
	}
	if len(frame.Control) != 0 {
		return nil, errors.New("invalid framed packet control")
	}
	if len(frame.Data) == 0 {
		return nil, nil
	}
	return frame.Data, nil
}

func readProbeChainFramedPacketInto(reader *bufio.Reader, payload []byte) (int, error) {
	packet, err := readProbeChainFramedPacket(reader)
	if err != nil {
		return 0, err
	}
	if len(packet) == 0 {
		return 0, nil
	}
	if len(packet) > len(payload) {
		return 0, errors.New("framed packet payload exceeds read buffer")
	}
	copy(payload, packet)
	return len(packet), nil
}

func writeProbeChainFramedPacket(writer io.Writer, payload []byte) error {
	size := len(payload)
	if size <= 0 || size > probeChainFrameMaxDataBytes {
		return errors.New("invalid framed packet payload")
	}
	frame, _ := probeChainFrameBufferPool.Get().([]byte)
	defer probeChainFrameBufferPool.Put(frame[:cap(frame)])
	encoded, err := encodeProbeChainFrame(probeChainFrame{Kind: probeChainFrameKindData, Data: payload}, frame)
	if err != nil {
		return err
	}
	return writeAll(writer, encoded)
}

func readProbeChainAuthEnvelope(reader *bufio.Reader) (probeChainAuthEnvelope, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return probeChainAuthEnvelope{}, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return probeChainAuthEnvelope{}, fmt.Errorf("empty auth envelope")
	}
	var env probeChainAuthEnvelope
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return probeChainAuthEnvelope{}, err
	}
	if env.Auth != nil {
		if strings.TrimSpace(env.Mode) == "" {
			env.Mode = env.Auth.Mode
		}
		if strings.TrimSpace(env.ChainID) == "" {
			env.ChainID = env.Auth.ChainID
		}
		if strings.TrimSpace(env.Nonce) == "" {
			env.Nonce = env.Auth.Nonce
		}
		if strings.TrimSpace(env.Signature) == "" {
			env.Signature = env.Auth.Signature
		}
		if strings.TrimSpace(env.MAC) == "" {
			env.MAC = env.Auth.MAC
		}
		if strings.TrimSpace(env.AuthTicket) == "" {
			env.AuthTicket = env.Auth.AuthTicket
		}
	}
	env.Type = strings.TrimSpace(env.Type)
	env.APIVersion = strings.TrimSpace(env.APIVersion)
	env.RequestID = strings.TrimSpace(env.RequestID)
	env.Timestamp = strings.TrimSpace(env.Timestamp)
	env.Mode = strings.ToLower(strings.TrimSpace(env.Mode))
	env.ChainID = strings.TrimSpace(env.ChainID)
	env.Nonce = strings.TrimSpace(env.Nonce)
	env.Signature = strings.TrimSpace(env.Signature)
	env.MAC = strings.TrimSpace(env.MAC)
	env.AuthTicket = strings.TrimSpace(env.AuthTicket)
	return env, nil
}

func sendProbeChainNonceChallenge(writer io.Writer) (string, error) {
	nonce := randomHexToken(16)
	nonce = strings.TrimSpace(nonce)
	if nonce == "" {
		nonce = randomHexToken(8)
	}
	if nonce == "" {
		return "", fmt.Errorf("generate nonce failed")
	}
	if _, err := io.WriteString(writer, probeChainAuthNoncePrefix+nonce+"\n"); err != nil {
		return "", err
	}
	return nonce, nil
}

func readProbeChainNonceChallenge(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "CHAUTHERR") {
		return "", fmt.Errorf("next probe auth rejected: %s", trimmed)
	}
	if !strings.HasPrefix(trimmed, probeChainAuthNoncePrefix) {
		return "", fmt.Errorf("invalid nonce challenge")
	}
	nonce := strings.TrimSpace(strings.TrimPrefix(trimmed, probeChainAuthNoncePrefix))
	if nonce == "" {
		return "", fmt.Errorf("invalid nonce challenge")
	}
	return nonce, nil
}

func verifyProbeChainInboundAuth(cfg probeChainRuntimeConfig, env probeChainAuthEnvelope) error {
	if env.ChainID != "" && env.ChainID != cfg.chainID {
		return fmt.Errorf("chain id mismatch")
	}
	if env.Nonce == "" {
		return fmt.Errorf("nonce is required")
	}
	mode := strings.ToLower(strings.TrimSpace(env.Mode))
	if mode != "" && mode != "secret_hmac" && mode != "hmac" {
		return fmt.Errorf("unsupported auth mode")
	}

	if strings.TrimSpace(cfg.secret) == "" {
		return fmt.Errorf("secret is not configured")
	}
	if env.MAC == "" {
		return fmt.Errorf("mac is required")
	}
	expected := buildProbeChainHMAC(cfg.secret, cfg.chainID, env.Nonce)
	if !hmac.Equal([]byte(strings.ToLower(env.MAC)), []byte(strings.ToLower(expected))) {
		return fmt.Errorf("authentication failed")
	}
	if err := verifyProbeChainUserAuthTicket(cfg, env.AuthTicket); err != nil {
		return err
	}
	if err := recordProbeChainAuthNonce(cfg.chainID, env.Nonce); err != nil {
		return err
	}
	return nil
}

func recordProbeChainAuthNonce(chainID string, nonce string) error {
	id := strings.TrimSpace(chainID)
	n := strings.TrimSpace(nonce)
	if id == "" || n == "" {
		return fmt.Errorf("nonce is required")
	}
	key := id + "\n" + n
	now := time.Now()
	probeChainAuthReplayStore.mu.Lock()
	defer probeChainAuthReplayStore.mu.Unlock()
	for itemKey, expiresAt := range probeChainAuthReplayStore.items {
		if !expiresAt.After(now) {
			delete(probeChainAuthReplayStore.items, itemKey)
		}
	}
	if expiresAt, exists := probeChainAuthReplayStore.items[key]; exists && expiresAt.After(now) {
		return fmt.Errorf("auth nonce replay detected")
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
	snapshot := map[string]string{}
	probeChainAuthTicketStore.mu.Lock()
	probeChainAuthTicketStore.items[id] = ticket
	for key, value := range probeChainAuthTicketStore.items {
		snapshot[key] = value
	}
	probeChainAuthTicketStore.mu.Unlock()
	if err := persistProbeChainAuthTicketSnapshot(snapshot); err != nil {
		log.Printf("warning: persist probe chain auth ticket cache failed: %v", err)
	}
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
	items, err := loadProbeChainAuthTicketSnapshot()
	if err != nil {
		log.Printf("warning: load probe chain auth ticket cache failed: %v", err)
		return ""
	}
	if len(items) == 0 {
		return ""
	}
	probeChainAuthTicketStore.mu.Lock()
	for key, value := range items {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			probeChainAuthTicketStore.items[key] = value
		}
	}
	ticket = strings.TrimSpace(probeChainAuthTicketStore.items[id])
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
	Version       string `json:"v"`
	ChainID       string `json:"chain_id"`
	ClientEntryID string `json:"client_entry_id,omitempty"`
	UserID        string `json:"user_id"`
	UserPublicKey string `json:"user_public_key"`
	IssuedAt      string `json:"issued_at"`
}

func verifyProbeChainUserAuthTicket(cfg probeChainRuntimeConfig, rawTicket string) error {
	ticket := strings.TrimSpace(rawTicket)
	if ticket == "" {
		return fmt.Errorf("user auth ticket is required")
	}
	if len(cfg.userPublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("user public key is not configured")
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		return fmt.Errorf("invalid user auth ticket")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("invalid user auth ticket payload")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("invalid user auth ticket signature")
	}
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(cfg.userPublicKey, payloadBytes, signature) {
		return fmt.Errorf("user auth ticket verification failed")
	}
	var payload probeChainUserAuthTicketPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return fmt.Errorf("invalid user auth ticket payload json")
	}
	if strings.TrimSpace(payload.Version) != "chain-auth-v1" {
		return fmt.Errorf("unsupported user auth ticket version")
	}
	if strings.TrimSpace(payload.ChainID) != strings.TrimSpace(cfg.chainID) {
		return fmt.Errorf("user auth ticket chain mismatch")
	}
	if strings.TrimSpace(payload.UserPublicKey) != strings.TrimSpace(cfg.rawPublicKey) {
		return fmt.Errorf("user auth ticket public key mismatch")
	}
	if err := verifyProbeChainAuthTicketIssuedAt(payload.IssuedAt, probeChainAuthTicketNow()); err != nil {
		return err
	}
	return nil
}

func verifyProbeChainAuthTicketIssuedAt(raw string, now time.Time) error {
	issuedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid user auth ticket issued_at")
	}
	if issuedAt.After(now.UTC().Add(5 * time.Minute)) {
		return fmt.Errorf("user auth ticket issued_at is in the future")
	}
	if !now.UTC().Before(issuedAt.UTC().AddDate(0, 2, 0)) {
		return fmt.Errorf("user auth ticket expired")
	}
	return nil
}

func sendProbeChainSecretAuth(nextWriter io.Writer, nextReader *bufio.Reader, chainID string, secret string) error {
	return sendProbeChainSecretAuthWithTicket(nextWriter, nextReader, chainID, secret, "")
}

func sendProbeChainSecretAuthWithTicket(nextWriter io.Writer, nextReader *bufio.Reader, chainID string, secret string, authTicket string) error {
	nonce, err := readProbeChainNonceChallenge(nextReader)
	if err != nil {
		return err
	}
	ticket := strings.TrimSpace(authTicket)
	if ticket == "" {
		ticket = lookupProbeChainAuthTicket(chainID)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	env := newProbeChainAuthEnvelope("secret_hmac", chainID, nonce, "", buildProbeChainHMAC(secret, chainID, nonce))
	env.Timestamp = timestamp
	if env.Auth != nil {
		env.Auth.Timestamp = timestamp
	}
	env.AuthTicket = ticket
	if env.Auth != nil {
		env.Auth.AuthTicket = env.AuthTicket
	}
	encoded, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if _, err := nextWriter.Write(append(encoded, '\n')); err != nil {
		return err
	}
	line, err := nextReader.ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != "CHAUTHOK" {
		return fmt.Errorf("next probe auth rejected: %s", strings.TrimSpace(line))
	}
	return nil
}

func newProbeChainAuthEnvelope(mode string, chainID string, nonce string, signature string, macValue string) probeChainAuthEnvelope {
	cleanMode := strings.ToLower(strings.TrimSpace(mode))
	cleanChainID := strings.TrimSpace(chainID)
	cleanNonce := strings.TrimSpace(nonce)
	cleanSignature := strings.TrimSpace(signature)
	cleanMAC := strings.TrimSpace(macValue)
	body := &probeChainAuthPayloadBody{
		Mode:      cleanMode,
		ChainID:   cleanChainID,
		Nonce:     cleanNonce,
		Signature: cleanSignature,
		MAC:       cleanMAC,
	}
	return probeChainAuthEnvelope{
		Type:       probeChainAuthPacketType,
		APIVersion: probeChainAuthPacketVersion,
		RequestID:  randomHexToken(8),
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		Auth:       body,
		Mode:       cleanMode,
		ChainID:    cleanChainID,
		Nonce:      cleanNonce,
		Signature:  cleanSignature,
		MAC:        cleanMAC,
	}
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

func resolveProbeChainSourceIPFromAddr(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return resolveProbeChainSourceIPFromAddrString(strings.TrimSpace(addr.String()))
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
	if delay <= 0 {
		return
	}
	time.Sleep(delay)
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
	state, ok := probeChainAuthIPStateMap.items[target]
	if !ok {
		probeChainAuthIPStateMap.mu.Unlock()
		return false, time.Time{}
	}
	if state.Manual {
		probeChainAuthIPStateMap.mu.Unlock()
		return true, time.Time{}
	}
	if !state.BlacklistedTil.IsZero() && now.Before(state.BlacklistedTil) {
		until := state.BlacklistedTil
		probeChainAuthIPStateMap.mu.Unlock()
		return true, until
	}
	if !state.BlacklistedTil.IsZero() && !now.Before(state.BlacklistedTil) {
		delete(probeChainAuthIPStateMap.items, target)
	}
	probeChainAuthIPStateMap.mu.Unlock()
	return false, time.Time{}
}

func isProbeChainAuthIPManuallyBlacklisted(ip string) bool {
	target := strings.TrimSpace(ip)
	if target == "" {
		return false
	}
	probeChainAuthIPStateMap.mu.Lock()
	state, ok := probeChainAuthIPStateMap.items[target]
	probeChainAuthIPStateMap.mu.Unlock()
	return ok && state.Manual
}

func recordProbeChainAuthFailure(ip string) (failures int, blacklisted bool, until time.Time) {
	target := strings.TrimSpace(ip)
	if target == "" {
		return 0, false, time.Time{}
	}
	now := time.Now()
	probeChainAuthIPStateMap.mu.Lock()
	state := probeChainAuthIPStateMap.items[target]
	if state.Manual {
		probeChainAuthIPStateMap.mu.Unlock()
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
	probeChainAuthIPStateMap.mu.Unlock()
	return failures, blacklisted, until
}

func resetProbeChainAuthFailure(ip string) {
	target := strings.TrimSpace(ip)
	if target == "" {
		return
	}
	probeChainAuthIPStateMap.mu.Lock()
	state, ok := probeChainAuthIPStateMap.items[target]
	if ok && state.Manual {
		state.FailedAttempts = 0
		probeChainAuthIPStateMap.items[target] = state
	} else {
		delete(probeChainAuthIPStateMap.items, target)
	}
	probeChainAuthIPStateMap.mu.Unlock()
}

func clearProbeChainAuthIPBlacklist(ip string) {
	resetProbeChainAuthFailure(ip)
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

func logProbeChainAuthFailure(chainID string, ip string, failures int, blacklisted bool, until time.Time, err error) {
	reason := sanitizeProbeChainAuthErr(fmt.Sprint(err))
	targetIP := strings.TrimSpace(ip)
	if targetIP == "" {
		targetIP = "unknown"
	}
	if blacklisted {
		untilText := "manual"
		if !until.IsZero() {
			untilText = until.UTC().Format(time.RFC3339)
		}
		log.Printf("probe chain auth failed: chain=%s ip=%s failures=%d blacklist_until=%s reason=%s", chainID, targetIP, failures, untilText, reason)
		return
	}
	log.Printf("probe chain auth failed: chain=%s ip=%s failures=%d reason=%s", chainID, targetIP, failures, reason)
}

func logProbeChainAuthFailureWithoutBlacklist(chainID string, ip string, err error) {
	reason := sanitizeProbeChainAuthErr(fmt.Sprint(err))
	targetIP := strings.TrimSpace(ip)
	if targetIP == "" {
		targetIP = "unknown"
	}
	log.Printf("probe chain auth failed: chain=%s ip=%s blacklist=disabled reason=%s", chainID, targetIP, reason)
}

func sanitizeProbeChainAuthErr(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "auth failed"
	}
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	if len(text) > 120 {
		return text[:120]
	}
	return text
}
