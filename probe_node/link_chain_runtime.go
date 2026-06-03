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
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

type probeChainLinkControlResultPayload struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	NodeID    string `json:"node_id"`
	OK        bool   `json:"ok"`
	Action    string `json:"action,omitempty"`
	ChainID   string `json:"chain_id,omitempty"`
	Role      string `json:"role,omitempty"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
	Timestamp string `json:"timestamp"`
}

type probeChainRuntimeConfig struct {
	chainID         string
	chainType       string
	name            string
	userID          string
	userPublicKey   ed25519.PublicKey
	rawPublicKey    string
	secret          string
	authTicket      string
	role            string
	listenHost      string
	listenPort      int
	linkLayer       string
	nextLinkLayer   string
	nextDialMode    string
	nextHost        string
	nextPort        int
	prevHost        string
	prevPort        int
	prevLinkLayer   string
	prevDialMode    string
	requireUserAuth bool
	nextAuthMode    string
	portForwards    []probeChainRuntimePortForward
	identity        nodeIdentity
	controllerURL   string
}

type probeChainBridgeSession struct {
	ID          string
	Session     *yamux.Session
	BridgeRole  string
	RemoteAddr  string
	ConnectedAt time.Time
}

type probeChainRuntime struct {
	cfg                probeChainRuntimeConfig
	relayListenAddr    string
	downstreamSessions map[string]*probeChainBridgeSession
	upstreamSessions   map[string]*probeChainBridgeSession
	bridgeMu           sync.Mutex
	bridgeSeq          uint64
	reverseDataMu      sync.Mutex
	reverseDataStreams map[string]chan *probeChainReverseDataConn
	forwardMu          sync.Mutex
	tcpForwards        []net.Listener
	udpForwards        []net.PacketConn
	stopCh             chan struct{}
}

type probeChainReverseDataConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func newProbeChainReverseDataConn(conn net.Conn) *probeChainReverseDataConn {
	return &probeChainReverseDataConn{Conn: conn, done: make(chan struct{})}
}

func (c *probeChainReverseDataConn) Close() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}

type probeChainBridgeControlRequest struct {
	Type       string `json:"type"`
	RequestID  string `json:"request_id,omitempty"`
	Token      string `json:"token,omitempty"`
	BridgeRole string `json:"bridge_role,omitempty"`
	FlowID     string `json:"flow_id,omitempty"`
	Timestamp  string `json:"timestamp,omitempty"`
}

type probeChainBridgeControlResponse struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	OK        bool   `json:"ok"`
	Token     string `json:"token,omitempty"`
	Error     string `json:"error,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type probeChainRuntimePortForward struct {
	ID         string
	Name       string
	EntrySide  string
	ListenHost string
	ListenPort int
	TargetHost string
	TargetPort int
	Network    string
	Enabled    bool
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
}

type probeChainSocksRequest struct {
	Version byte
	Cmd     byte
	Address string
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
	Type          string                       `json:"type"`
	RequestID     string                       `json:"request_id,omitempty"`
	Network       string                       `json:"network"`
	Address       string                       `json:"address"`
	FlowID        string                       `json:"flow_id,omitempty"`
	SessionID     string                       `json:"session_id,omitempty"`
	AssociationV2 *probeChainAssociationV2Meta `json:"association_v2,omitempty"`
	SpeedBytes    int64                        `json:"speed_bytes,omitempty"`
	PingBytes     int64                        `json:"ping_bytes,omitempty"`
}

type probeChainTunnelOpenResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type probeChainTunnelDNSResolveResponse struct {
	Addrs []string `json:"addrs,omitempty"`
	TTL   int      `json:"ttl,omitempty"`
	Error string   `json:"error,omitempty"`
}

type probeChainStreamProxyConn struct {
	net.Conn
	reader *bufio.Reader
}

type probeChainNextHop struct {
	Writer  io.WriteCloser
	Reader  io.ReadCloser
	CloseFn func() error
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
	probeChainRelayAPIPath       = "/api/node/chain/relay"
	probeChainSourceIPHintPrefix = "CHSRCIP "
	probeChainAuthNoncePrefix    = "CHNONCE "

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

	probeChainBridgeControlPrefix        = "CHCTRL "
	probeChainBridgeControlReverseOpen   = "reverse_data_open"
	probeChainBridgeControlReverseResult = "reverse_data_open_result"

	probeChainRelayModeBridge     = "bridge"
	probeChainRelayModeStream     = "stream"
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

	probeChainPortForwardEntryChainEntry = "chain_entry"
	probeChainPortForwardEntryChainExit  = "chain_exit"

	probeChainPortForwardSessionIdleTTL        = 90 * time.Second
	probeChainPortForwardSessionGCInterval     = 15 * time.Second
	probeChainPortForwardDialTimeout           = 12 * time.Second
	probeChainPortForwardResponseReadDeadline  = 10 * time.Second
	probeChainPortForwardListenRetryTimeout    = 5 * time.Second
	probeChainPortForwardListenRetryInterval   = 100 * time.Millisecond
	probeChainPortForwardListenRetryMaxBackoff = 800 * time.Millisecond
	probeChainPortForwardPreconnectIdleTTL     = 60 * time.Second
	probeChainPortForwardPreconnectRetryMin    = 500 * time.Millisecond
	probeChainPortForwardPreconnectRetryMax    = 10 * time.Second
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
	probeChainRelayUDPFrameBufferBytes         = 2 + 65535
	probeChainRelayTCPKeepAlivePeriod          = 30 * time.Second
	probeChainRelayYamuxKeepAliveInterval      = 20 * time.Second
	probeChainRelayYamuxAcceptBacklog          = 1024
	probeChainRelayYamuxMaxStreamWindowBytes   = 64 * 1024 * 1024
	probeChainRelayYamuxWriteTimeout           = 2 * time.Minute
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

type probeChainPortForwardPreconnectPool struct {
	runtime           *probeChainRuntime
	cfg               probeChainRuntimePortForward
	network           string
	targetAddr        string
	capacity          int
	ready             chan *probeChainPortForwardPreconnectedConn
	refillCh          chan struct{}
	stopCh            chan struct{}
	closeOnce         sync.Once
	targetUnreachable bool // run() loop only: whether the last refill failed at the target-dial phase
}

type probeChainPortForwardPreconnectedConn struct {
	conn      net.Conn
	openedAt  time.Time
	flowID    string
	expiresAt time.Time
}

// probeChainPreconnectPhase distinguishes a relay/link failure (a real transport
// problem) from a target-dial failure (the target is unreachable, link is fine).
type probeChainPreconnectPhase string

const (
	probeChainPreconnectPhaseTransport probeChainPreconnectPhase = "transport"
	probeChainPreconnectPhaseTarget    probeChainPreconnectPhase = "target"

	// When the relay link is healthy but the target is unreachable, prewarming is
	// pointless (no connection could succeed anyway), so we retry slowly and quietly
	// instead of hammering a dead target and spamming logs.
	probeChainPortForwardPreconnectTargetRetryInterval = 30 * time.Second
)

type probeChainPreconnectError struct {
	phase probeChainPreconnectPhase
	err   error
}

func (e *probeChainPreconnectError) Error() string { return e.err.Error() }
func (e *probeChainPreconnectError) Unwrap() error { return e.err }

func isProbeChainPreconnectTargetError(err error) bool {
	var pcErr *probeChainPreconnectError
	return errors.As(err, &pcErr) && pcErr.phase == probeChainPreconnectPhaseTarget
}

var probeChainUDPFrameBufferPool = sync.Pool{
	New: func() any {
		return make([]byte, probeChainRelayUDPFrameBufferBytes)
	},
}

func runProbeChainLinkControl(cmd probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	requestID := strings.TrimSpace(cmd.RequestID)
	action := normalizeProbeChainAction(cmd.Action)
	if action == "" {
		action = "apply"
	}
	result := probeChainLinkControlResultPayload{
		Type:      "chain_link_control_result",
		RequestID: requestID,
		NodeID:    strings.TrimSpace(identity.NodeID),
		OK:        false,
		Action:    action,
		ChainID:   strings.TrimSpace(cmd.ChainID),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	switch action {
	case "apply":
		cfg, err := buildProbeChainRuntimeConfigFromControl(cmd)
		if err != nil {
			result.Error = err.Error()
			sendProbeChainLinkControlResult(stream, encoder, writeMu, result)
			return
		}
		cfg.identity = identity
		cfg.controllerURL = resolveProbeControllerBaseURL(strings.TrimSpace(cmd.ControllerBaseURL), "")
		rt, err := startProbeChainRuntime(cfg)
		if err != nil {
			result.Error = err.Error()
			sendProbeChainLinkControlResult(stream, encoder, writeMu, result)
			return
		}
		result.OK = true
		result.Role = rt.cfg.role
		result.Message = fmt.Sprintf("chain runtime started: chain=%s role=%s listen=%s", rt.cfg.chainID, rt.cfg.role, net.JoinHostPort(rt.cfg.listenHost, strconv.Itoa(rt.cfg.listenPort)))
	case "remove":
		chainID := strings.TrimSpace(cmd.ChainID)
		removed := stopProbeChainRuntime(chainID, "remote remove command")
		result.OK = true
		if removed {
			result.Message = "chain runtime removed"
		} else {
			result.Message = "chain runtime not found"
		}
	default:
		result.Error = "unsupported action"
	}

	sendProbeChainLinkControlResult(stream, encoder, writeMu, result)
}

func sendProbeChainLinkControlResult(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, payload probeChainLinkControlResultPayload) {
	if writeErr := writeProbeStreamJSON(stream, encoder, writeMu, payload); writeErr != nil {
		log.Printf("probe chain link control response send failed: request_id=%s err=%v", strings.TrimSpace(payload.RequestID), writeErr)
	}
}

func normalizeProbeChainAction(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "apply":
		return "apply"
	case "remove", "delete", "stop":
		return "remove"
	default:
		return ""
	}
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

func normalizeProbeChainPortForwardNetwork(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case probeChainPortForwardNetworkUDP:
		return probeChainPortForwardNetworkUDP
	case probeChainPortForwardNetworkBoth, "tcp+udp", "udp+tcp":
		return probeChainPortForwardNetworkBoth
	default:
		return probeChainPortForwardNetworkTCP
	}
}

func parseProbeChainPortForwardNetwork(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", true
	}
	switch strings.ToLower(trimmed) {
	case probeChainPortForwardNetworkTCP:
		return probeChainPortForwardNetworkTCP, true
	case probeChainPortForwardNetworkUDP:
		return probeChainPortForwardNetworkUDP, true
	case probeChainPortForwardNetworkBoth, "tcp+udp", "udp+tcp":
		return probeChainPortForwardNetworkBoth, true
	default:
		return "", false
	}
}

func normalizeProbeChainPortForwardEntrySide(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case probeChainPortForwardEntryChainExit, "exit", "egress":
		return probeChainPortForwardEntryChainExit
	default:
		return probeChainPortForwardEntryChainEntry
	}
}

func parseProbeChainPortForwardEntrySide(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", true
	}
	switch strings.ToLower(trimmed) {
	case probeChainPortForwardEntryChainEntry, "entry", "ingress":
		return probeChainPortForwardEntryChainEntry, true
	case probeChainPortForwardEntryChainExit, "exit", "egress":
		return probeChainPortForwardEntryChainExit, true
	default:
		return "", false
	}
}

func normalizeProbeChainPortForwardID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = "pf-" + strings.ToLower(strings.TrimSpace(randomHexToken(6)))
	}
	if len(value) > 96 {
		value = value[:96]
	}
	return value
}

func normalizeProbeChainPortForwards(values []probeChainPortForwardMessage) ([]probeChainRuntimePortForward, error) {
	if len(values) == 0 {
		return []probeChainRuntimePortForward{}, nil
	}
	out := make([]probeChainRuntimePortForward, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, item := range values {
		listenPort := normalizeProbeLinkTestPort(item.ListenPort)
		if listenPort <= 0 {
			return nil, fmt.Errorf("port_forwards listen_port must be between 1 and 65535")
		}
		targetPort := normalizeProbeLinkTestPort(item.TargetPort)
		if targetPort <= 0 {
			return nil, fmt.Errorf("port_forwards target_port must be between 1 and 65535")
		}
		targetHost := strings.TrimSpace(item.TargetHost)
		if targetHost == "" {
			return nil, fmt.Errorf("port_forwards target_host is required")
		}
		network, ok := parseProbeChainPortForwardNetwork(item.Network)
		if !ok {
			return nil, fmt.Errorf("port_forwards network must be tcp/udp/both")
		}
		if strings.TrimSpace(network) == "" {
			network = probeChainPortForwardNetworkTCP
		}
		entrySide, ok := parseProbeChainPortForwardEntrySide(item.EntrySide)
		if !ok {
			return nil, fmt.Errorf("port_forwards entry_side must be chain_entry/chain_exit")
		}
		if strings.TrimSpace(entrySide) == "" {
			entrySide = probeChainPortForwardEntryChainEntry
		}
		id := normalizeProbeChainPortForwardID(item.ID)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		listenHost := strings.TrimSpace(item.ListenHost)
		if listenHost == "" {
			listenHost = "0.0.0.0"
		}
		out = append(out, probeChainRuntimePortForward{
			ID:         id,
			Name:       strings.TrimSpace(item.Name),
			EntrySide:  entrySide,
			ListenHost: listenHost,
			ListenPort: listenPort,
			TargetHost: targetHost,
			TargetPort: targetPort,
			Network:    network,
			Enabled:    item.Enabled,
		})
	}
	return out, nil
}

func buildProbeChainPortForwardMessagesFromRuntime(values []probeChainRuntimePortForward) []probeChainPortForwardMessage {
	if len(values) == 0 {
		return []probeChainPortForwardMessage{}
	}
	out := make([]probeChainPortForwardMessage, 0, len(values))
	for _, item := range values {
		out = append(out, probeChainPortForwardMessage{
			ID:         strings.TrimSpace(item.ID),
			Name:       strings.TrimSpace(item.Name),
			EntrySide:  strings.TrimSpace(item.EntrySide),
			ListenHost: strings.TrimSpace(item.ListenHost),
			ListenPort: item.ListenPort,
			TargetHost: strings.TrimSpace(item.TargetHost),
			TargetPort: item.TargetPort,
			Network:    strings.TrimSpace(item.Network),
			Enabled:    item.Enabled,
		})
	}
	return out
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
	portForwards, forwardErr := normalizeProbeChainPortForwards(cmd.PortForwards)
	if forwardErr != nil {
		return probeChainRuntimeConfig{}, forwardErr
	}
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
		chainID:         chainID,
		chainType:       strings.TrimSpace(cmd.ChainType),
		name:            strings.TrimSpace(cmd.Name),
		userID:          strings.TrimSpace(cmd.UserID),
		rawPublicKey:    strings.TrimSpace(cmd.UserPublicKey),
		secret:          secret,
		authTicket:      strings.TrimSpace(cmd.AuthTicket),
		role:            role,
		listenHost:      listenHost,
		listenPort:      listenPort,
		linkLayer:       linkLayer,
		nextLinkLayer:   nextLinkLayer,
		nextDialMode:    nextDialMode,
		nextHost:        nextHost,
		nextPort:        nextPort,
		prevHost:        prevHost,
		prevPort:        prevPort,
		prevLinkLayer:   prevLinkLayer,
		prevDialMode:    prevDialMode,
		portForwards:    portForwards,
		requireUserAuth: requireUserAuth,
		nextAuthMode:    nextAuthMode,
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
	_ = stopProbeChainRuntime(cfg.chainID, "restart before apply")
	if err := ensureProbeChainRuntimeAuthTicket(&cfg); err != nil {
		return nil, err
	}
	rememberProbeChainAuthTicket(cfg.chainID, cfg.authTicket)

	rt := &probeChainRuntime{
		cfg:                cfg,
		downstreamSessions: make(map[string]*probeChainBridgeSession),
		upstreamSessions:   make(map[string]*probeChainBridgeSession),
		reverseDataStreams: make(map[string]chan *probeChainReverseDataConn),
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
	startProbeChainPortForwardWorkers(rt)

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
	baseURL := strings.TrimSpace(cfg.controllerURL)
	if baseURL == "" || strings.TrimSpace(cfg.identity.NodeID) == "" || strings.TrimSpace(cfg.identity.Secret) == "" {
		return fmt.Errorf("auth_ticket is required when require_user_auth=true")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeLinkChainsSyncFetchTimeout)
	config, err := fetchProbeLinkChainConfig(ctx, baseURL, cfg.identity)
	cancel()
	if err != nil {
		return fmt.Errorf("active auth_ticket refresh failed: %w", err)
	}
	if item, ok := findProbeChainAuthTicketItem(cfg.chainID, config.SelfChains, config.GlobalProxyForwardChains); ok {
		cfg.authTicket = strings.TrimSpace(item.AuthTicket)
		if strings.TrimSpace(cfg.authTicket) != "" {
			rememberProbeChainAuthTicket(cfg.chainID, cfg.authTicket)
			log.Printf("probe chain auth ticket refreshed: chain=%s", strings.TrimSpace(cfg.chainID))
			return nil
		}
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
	Host             string
	Port             int
	LinkLayer        string
	RoleHeader       string
	AssignDownstream bool
	AssignUpstream   bool
	AcceptStreams    bool
	Tag              string
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
				Host:             strings.TrimSpace(cfg.nextHost),
				Port:             cfg.nextPort,
				LinkLayer:        resolveProbeChainOutboundLinkLayer(cfg),
				RoleHeader:       probeChainBridgeRoleToNext,
				AssignDownstream: true,
				AcceptStreams:    false,
				Tag:              "downstream-forward",
			}
			go runProbeChainBridgeDialLoop(runtime, target)
		case probeChainDialModeReverse:
			log.Printf("probe chain waiting reverse downstream bridge: chain=%s listen=%s:%d", cfg.chainID, cfg.listenHost, cfg.listenPort)
		}
	}

	if normalizeProbeChainDialMode(cfg.prevDialMode) == probeChainDialModeReverse {
		target := probeChainBridgeDialTarget{
			Host:             strings.TrimSpace(cfg.prevHost),
			Port:             cfg.prevPort,
			LinkLayer:        normalizeProbeChainLinkLayer(cfg.prevLinkLayer),
			RoleHeader:       probeChainBridgeRoleToPrev,
			AssignDownstream: false,
			AssignUpstream:   true,
			AcceptStreams:    true,
			Tag:              "upstream-reverse",
		}
		if target.Host != "" && target.Port > 0 {
			go runProbeChainBridgeDialLoop(runtime, target)
		}
	}
}

func shouldRunProbeChainPortForwardOnRole(role string, entrySide string) bool {
	switch normalizeProbeChainPortForwardEntrySide(entrySide) {
	case probeChainPortForwardEntryChainExit:
		switch normalizeProbeChainRole(role) {
		case "exit", "entry_exit":
			return true
		default:
			return false
		}
	default:
		switch normalizeProbeChainRole(role) {
		case "entry", "entry_exit":
			return true
		default:
			return false
		}
	}
}

func (rt *probeChainRuntime) registerTCPForward(ln net.Listener) {
	if rt == nil || ln == nil {
		return
	}
	rt.forwardMu.Lock()
	rt.tcpForwards = append(rt.tcpForwards, ln)
	rt.forwardMu.Unlock()
}

func (rt *probeChainRuntime) registerUDPForward(pc net.PacketConn) {
	if rt == nil || pc == nil {
		return
	}
	rt.forwardMu.Lock()
	rt.udpForwards = append(rt.udpForwards, pc)
	rt.forwardMu.Unlock()
}

func startProbeChainPortForwardWorkers(runtime *probeChainRuntime) {
	if runtime == nil {
		return
	}
	total := len(runtime.cfg.portForwards)
	enabled := 0
	roleMatched := 0
	startedWorkers := 0
	for _, item := range runtime.cfg.portForwards {
		cfg := item
		if !cfg.Enabled {
			log.Printf("probe chain port forward skipped: chain=%s id=%s reason=disabled", runtime.cfg.chainID, cfg.ID)
			continue
		}
		enabled++
		if !shouldRunProbeChainPortForwardOnRole(runtime.cfg.role, cfg.EntrySide) {
			log.Printf("probe chain port forward skipped: chain=%s id=%s reason=role_mismatch role=%s entry_side=%s", runtime.cfg.chainID, cfg.ID, normalizeProbeChainRole(runtime.cfg.role), normalizeProbeChainPortForwardEntrySide(cfg.EntrySide))
			continue
		}
		roleMatched++
		listenHost := strings.TrimSpace(cfg.ListenHost)
		if listenHost == "" {
			listenHost = "0.0.0.0"
		}
		if cfg.ListenPort <= 0 {
			log.Printf("probe chain port forward skipped: chain=%s id=%s reason=invalid_listen_port listen_port=%d", runtime.cfg.chainID, cfg.ID, cfg.ListenPort)
			continue
		}
		listenAddr := net.JoinHostPort(listenHost, strconv.Itoa(cfg.ListenPort))
		network := normalizeProbeChainPortForwardNetwork(cfg.Network)
		if network == probeChainPortForwardNetworkTCP || network == probeChainPortForwardNetworkBoth {
			startedWorkers++
			go runProbeChainTCPPortForwardWorker(runtime, cfg, listenAddr)
		}
		if network == probeChainPortForwardNetworkUDP || network == probeChainPortForwardNetworkBoth {
			startedWorkers++
			go runProbeChainUDPPortForwardWorker(runtime, cfg, listenAddr)
		}
		if network != probeChainPortForwardNetworkTCP && network != probeChainPortForwardNetworkUDP && network != probeChainPortForwardNetworkBoth {
			log.Printf("probe chain port forward skipped: chain=%s id=%s reason=invalid_network network=%s", runtime.cfg.chainID, cfg.ID, strings.TrimSpace(cfg.Network))
		}
	}
	log.Printf("probe chain port forward workers initialized: chain=%s total=%d enabled=%d role_matched=%d worker_count=%d", runtime.cfg.chainID, total, enabled, roleMatched, startedWorkers)
}

func runProbeChainTCPPortForwardWorker(runtime *probeChainRuntime, cfg probeChainRuntimePortForward, listenAddr string) {
	if runtime == nil {
		return
	}
	for {
		select {
		case <-runtime.stopCh:
			return
		default:
		}
		log.Printf("probe chain port forward tcp listen start: chain=%s id=%s listen=%s", runtime.cfg.chainID, cfg.ID, listenAddr)
		ln, err := listenTCPWithRetry(listenAddr, probeChainPortForwardListenRetryTimeout)
		if err != nil {
			log.Printf("probe chain port forward tcp listen failed, will retry: chain=%s id=%s listen=%s err=%v", runtime.cfg.chainID, cfg.ID, listenAddr, err)
			select {
			case <-runtime.stopCh:
				return
			case <-time.After(probeChainPortForwardListenRetryInterval):
			}
			continue
		}
		runtime.registerTCPForward(ln)
		log.Printf("probe chain port forward tcp listen ready: chain=%s id=%s listen=%s", runtime.cfg.chainID, cfg.ID, listenAddr)
		runProbeChainTCPPortForward(runtime, cfg, ln)
		select {
		case <-runtime.stopCh:
			return
		default:
			log.Printf("probe chain port forward tcp worker exited, rebind scheduled: chain=%s id=%s listen=%s", runtime.cfg.chainID, cfg.ID, listenAddr)
			select {
			case <-runtime.stopCh:
				return
			case <-time.After(probeChainPortForwardListenRetryInterval):
			}
		}
	}
}

func runProbeChainUDPPortForwardWorker(runtime *probeChainRuntime, cfg probeChainRuntimePortForward, listenAddr string) {
	if runtime == nil {
		return
	}
	for {
		select {
		case <-runtime.stopCh:
			return
		default:
		}
		log.Printf("probe chain port forward udp listen start: chain=%s id=%s listen=%s", runtime.cfg.chainID, cfg.ID, listenAddr)
		pc, err := listenUDPWithRetry(listenAddr, probeChainPortForwardListenRetryTimeout)
		if err != nil {
			log.Printf("probe chain port forward udp listen failed, will retry: chain=%s id=%s listen=%s err=%v", runtime.cfg.chainID, cfg.ID, listenAddr, err)
			select {
			case <-runtime.stopCh:
				return
			case <-time.After(probeChainPortForwardListenRetryInterval):
			}
			continue
		}
		runtime.registerUDPForward(pc)
		log.Printf("probe chain port forward udp listen ready: chain=%s id=%s listen=%s", runtime.cfg.chainID, cfg.ID, listenAddr)
		runProbeChainUDPPortForward(runtime, cfg, pc)
		select {
		case <-runtime.stopCh:
			return
		default:
			log.Printf("probe chain port forward udp worker exited, rebind scheduled: chain=%s id=%s listen=%s", runtime.cfg.chainID, cfg.ID, listenAddr)
			select {
			case <-runtime.stopCh:
				return
			case <-time.After(probeChainPortForwardListenRetryInterval):
			}
		}
	}
}

func listenTCPWithRetry(listenAddr string, timeout time.Duration) (net.Listener, error) {
	if timeout <= 0 {
		timeout = probeChainPortForwardListenRetryTimeout
	}
	deadline := time.Now().Add(timeout)
	backoff := probeChainPortForwardListenRetryInterval
	for {
		listenConfig := net.ListenConfig{KeepAlive: probeChainRelayTCPKeepAlivePeriod}
		ln, err := listenConfig.Listen(context.Background(), "tcp", listenAddr)
		if err == nil {
			return &probeChainTunedTCPListener{Listener: ln}, nil
		}
		if !isRetryablePortForwardListenErr(err) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(backoff)
		backoff = nextProbeChainListenRetryBackoff(backoff)
	}
}

func listenUDPWithRetry(listenAddr string, timeout time.Duration) (net.PacketConn, error) {
	if timeout <= 0 {
		timeout = probeChainPortForwardListenRetryTimeout
	}
	deadline := time.Now().Add(timeout)
	backoff := probeChainPortForwardListenRetryInterval
	for {
		pc, err := net.ListenPacket("udp", listenAddr)
		if err == nil {
			if udpConn, ok := pc.(*net.UDPConn); ok {
				tuneProbeChainUDPConn(udpConn)
			}
			return pc, nil
		}
		if !isRetryablePortForwardListenErr(err) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(backoff)
		backoff = nextProbeChainListenRetryBackoff(backoff)
	}
}

func nextProbeChainListenRetryBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		current = probeChainPortForwardListenRetryInterval
	}
	next := current * 2
	if next <= 0 {
		next = probeChainPortForwardListenRetryInterval
	}
	if maxBackoff := probeChainPortForwardListenRetryMaxBackoff; maxBackoff > 0 && next > maxBackoff {
		next = maxBackoff
	}
	return next
}

func isRetryablePortForwardListenErr(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}
	var errno syscall.Errno
	if !errors.As(opErr.Err, &errno) {
		return false
	}
	switch errno {
	case syscall.EADDRINUSE, syscall.EADDRNOTAVAIL:
		return true
	default:
		return false
	}
}

func buildProbeChainPortForwardTarget(cfg probeChainRuntimePortForward) (string, error) {
	host := strings.TrimSpace(cfg.TargetHost)
	if host == "" {
		return "", fmt.Errorf("target_host is required")
	}
	if cfg.TargetPort <= 0 || cfg.TargetPort > 65535 {
		return "", fmt.Errorf("target_port must be between 1 and 65535")
	}
	return net.JoinHostPort(host, strconv.Itoa(cfg.TargetPort)), nil
}

func openProbeChainPortForwardLocalTarget(network string, targetAddr string) (net.Conn, error) {
	requestedNetwork := strings.ToLower(strings.TrimSpace(network))
	if requestedNetwork == "" {
		requestedNetwork = probeChainPortForwardNetworkTCP
	}
	if requestedNetwork != probeChainPortForwardNetworkTCP {
		return nil, errors.New("single-hop udp port forward is not supported")
	}
	dialer := &net.Dialer{Timeout: probeChainPortForwardDialTimeout}
	conn, err := dialer.Dial("tcp", strings.TrimSpace(targetAddr))
	if err != nil {
		return nil, err
	}
	tuneProbeChainNetConn(conn)
	return conn, nil
}

func openProbeChainPortForwardStream(runtime *probeChainRuntime, entrySide string, network string, targetAddr string) (net.Conn, error) {
	return openProbeChainPortForwardStreamWithAssociation(runtime, entrySide, network, targetAddr, nil)
}

func openProbeChainPortForwardStreamWithFlow(runtime *probeChainRuntime, entrySide string, network string, targetAddr string, flowID string) (net.Conn, error) {
	return openProbeChainPortForwardStreamWithFlowAndAssociation(runtime, entrySide, network, targetAddr, flowID, nil)
}

func openProbeChainPortForwardStreamWithAssociation(runtime *probeChainRuntime, entrySide string, network string, targetAddr string, associationV2 *probeChainAssociationV2Meta) (net.Conn, error) {
	return openProbeChainPortForwardStreamWithFlowAndAssociation(runtime, entrySide, network, targetAddr, "", associationV2)
}

func openProbeChainPortForwardStreamWithFlowAndAssociation(runtime *probeChainRuntime, entrySide string, network string, targetAddr string, flowID string, associationV2 *probeChainAssociationV2Meta) (net.Conn, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	requestedNetwork := strings.ToLower(strings.TrimSpace(network))
	if requestedNetwork == "" {
		requestedNetwork = probeChainPortForwardNetworkTCP
	}
	normalizedEntrySide := normalizeProbeChainPortForwardEntrySide(entrySide)
	role := normalizeProbeChainRole(runtime.cfg.role)
	if role == "entry_exit" {
		return openProbeChainPortForwardLocalTarget(requestedNetwork, targetAddr)
	}

	cleanFlowID := strings.TrimSpace(flowID)
	if cleanFlowID == "" {
		cleanFlowID = resolveProbeChainPortForwardFlowID(requestedNetwork, targetAddr, associationV2)
	}
	failurePrompt := ""
	if normalizedEntrySide == probeChainPortForwardEntryChainExit {
		failurePrompt = "open upstream target failed"
	} else if runtime.cfg.nextAuthMode == "proxy" {
		return openProbeChainPortForwardLocalTarget(requestedNetwork, targetAddr)
	} else {
		failurePrompt = "open downstream target failed"
	}

	// Phase 1: build the relay substream toward the exit (the "prepared link" phase).
	stream, err := openProbeChainPortForwardRelaySubstream(runtime, normalizedEntrySide, cleanFlowID)
	if err != nil {
		return nil, err
	}
	// Phase 2: ask the exit to dial the target (the "business" phase).
	if err := finishProbeChainPortForwardOpen(stream, requestedNetwork, targetAddr, cleanFlowID, associationV2, failurePrompt); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream, nil
}

// openProbeChainPortForwardRelaySubstream opens only the relay data substream toward
// the exit, without sending the target open request. It is valid only for relay roles
// (entry_exit and proxy-next are short-circuited to a local target by the caller).
func openProbeChainPortForwardRelaySubstream(runtime *probeChainRuntime, normalizedEntrySide string, flowID string) (net.Conn, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	if normalizedEntrySide == probeChainPortForwardEntryChainExit {
		return openProbeChainPortForwardDataStreamByDialMode(runtime, probeChainBridgeRoleToPrev, strings.TrimSpace(flowID))
	}
	return openProbeChainPortForwardDataStreamByDialMode(runtime, probeChainBridgeRoleToNext, strings.TrimSpace(flowID))
}

// finishProbeChainPortForwardOpen sends the tunnel open request over an already
// established relay substream and waits for the exit to dial the target. On error the
// caller owns closing the stream.
func finishProbeChainPortForwardOpen(stream net.Conn, network string, targetAddr string, flowID string, associationV2 *probeChainAssociationV2Meta, failurePrompt string) error {
	requestedNetwork := strings.ToLower(strings.TrimSpace(network))
	if requestedNetwork == "" {
		requestedNetwork = probeChainPortForwardNetworkTCP
	}
	cleanFlowID := strings.TrimSpace(flowID)
	if cleanFlowID == "" {
		cleanFlowID = resolveProbeChainPortForwardFlowID(requestedNetwork, targetAddr, associationV2)
	}
	request := probeChainTunnelOpenRequest{
		Type:          "open",
		Network:       requestedNetwork,
		Address:       strings.TrimSpace(targetAddr),
		FlowID:        cleanFlowID,
		AssociationV2: associationV2,
	}
	_ = stream.SetWriteDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	if err := json.NewEncoder(stream).Encode(request); err != nil {
		return err
	}
	_ = stream.SetWriteDeadline(time.Time{})

	_ = stream.SetReadDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	var response probeChainTunnelOpenResponse
	if err := json.NewDecoder(stream).Decode(&response); err != nil {
		return err
	}
	_ = stream.SetReadDeadline(time.Time{})
	if !response.OK {
		message := strings.TrimSpace(response.Error)
		if message == "" {
			message = strings.TrimSpace(failurePrompt)
		}
		if message == "" {
			message = "open target failed"
		}
		return errors.New(message)
	}
	return nil
}

func openProbeChainPortForwardDataStreamByDialMode(runtime *probeChainRuntime, bridgeRole string, flowID string) (net.Conn, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	role := normalizeProbeChainBridgeRole(bridgeRole)
	switch role {
	case probeChainBridgeRoleToPrev:
		if normalizeProbeChainDialMode(runtime.cfg.prevDialMode) == probeChainDialModeReverse {
			return openProbeChainPortForwardIndependentDataStream(runtime, role)
		}
		if normalizeProbeChainDialMode(runtime.cfg.prevDialMode) == probeChainDialModeForward {
			return openProbeChainPortForwardReverseDataStream(runtime, runtime.getUpstreamSession(), role, flowID)
		}
		return nil, errors.New("previous hop is not configured")
	default:
		if normalizeProbeChainDialMode(runtime.cfg.nextDialMode) == probeChainDialModeForward {
			return openProbeChainPortForwardIndependentDataStream(runtime, role)
		}
		if normalizeProbeChainDialMode(runtime.cfg.nextDialMode) == probeChainDialModeReverse {
			return openProbeChainPortForwardReverseDataStream(runtime, runtime.getDownstreamSession(), role, flowID)
		}
		return nil, errors.New("next hop is not configured")
	}
}

func openProbeChainPortForwardReverseDataStream(runtime *probeChainRuntime, session *yamux.Session, targetRole string, flowID string) (net.Conn, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	if session == nil || session.IsClosed() {
		return nil, errors.New("reverse dial management channel is unavailable")
	}
	token := "revdata-" + randomHexToken(12)
	waiter, err := runtime.registerReverseDataWaiter(token)
	if err != nil {
		return nil, err
	}
	defer runtime.unregisterReverseDataWaiter(token)

	controlStream, err := session.Open()
	if err != nil {
		return nil, err
	}
	defer controlStream.Close()

	requestID := "revdata-open-" + randomHexToken(8)
	req := probeChainBridgeControlRequest{
		Type:       probeChainBridgeControlReverseOpen,
		RequestID:  requestID,
		Token:      token,
		BridgeRole: reverseProbeChainBridgeRole(normalizeProbeChainBridgeRole(targetRole)),
		FlowID:     strings.TrimSpace(flowID),
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
	_ = controlStream.SetDeadline(time.Now().Add(probeChainDownstreamOpenTimeout))
	if err := writeProbeChainBridgeControlRequest(controlStream, req); err != nil {
		return nil, err
	}
	var resp probeChainBridgeControlResponse
	if err := json.NewDecoder(controlStream).Decode(&resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, errors.New(firstNonEmpty(strings.TrimSpace(resp.Error), "reverse data open rejected"))
	}
	select {
	case conn := <-waiter:
		if conn == nil {
			return nil, errors.New("reverse data stream is nil")
		}
		return conn, nil
	case <-time.After(probeChainDownstreamOpenTimeout):
		return nil, errors.New("reverse data stream accept timeout")
	}
}

func reverseProbeChainBridgeRole(role string) string {
	if normalizeProbeChainBridgeRole(role) == probeChainBridgeRoleToPrev {
		return probeChainBridgeRoleToNext
	}
	return probeChainBridgeRoleToPrev
}

func resolveProbeChainPortForwardFlowID(network string, targetAddr string, associationV2 *probeChainAssociationV2Meta) string {
	if associationV2 != nil && strings.TrimSpace(associationV2.FlowID) != "" {
		return strings.TrimSpace(associationV2.FlowID)
	}
	if strings.EqualFold(strings.TrimSpace(network), probeChainPortForwardNetworkTCP) || strings.EqualFold(strings.TrimSpace(network), "tcp") {
		return newProbeTCPDebugFlowID("port_forward", targetAddr)
	}
	return ""
}

func openProbeChainPortForwardIndependentDataStream(runtime *probeChainRuntime, bridgeRole string) (net.Conn, error) {
	return openProbeChainPortForwardIndependentDataStreamWithToken(runtime, bridgeRole, "")
}

func openProbeChainPortForwardIndependentDataStreamWithToken(runtime *probeChainRuntime, bridgeRole string, token string) (net.Conn, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	role := normalizeProbeChainBridgeRole(bridgeRole)
	host := ""
	port := 0
	layer := ""
	if role == probeChainBridgeRoleToPrev {
		host = strings.TrimSpace(runtime.cfg.prevHost)
		port = runtime.cfg.prevPort
		layer = normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(runtime.cfg.prevLinkLayer), strings.TrimSpace(runtime.cfg.linkLayer)))
	} else {
		host = strings.TrimSpace(runtime.cfg.nextHost)
		port = runtime.cfg.nextPort
		layer = normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(runtime.cfg.nextLinkLayer), strings.TrimSpace(runtime.cfg.linkLayer)))
	}
	if host == "" || port <= 0 {
		return nil, fmt.Errorf("chain %s relay target is unavailable", role)
	}
	return openProbeChainRelayDataStreamNetConnWithRoleAndToken(runtime.cfg.chainID, runtime.cfg.secret, host, port, layer, role, strings.TrimSpace(token), probeChainDownstreamOpenTimeout)
}

func newProbeChainPortForwardPreconnectPool(runtime *probeChainRuntime, cfg probeChainRuntimePortForward, network string, targetAddr string) *probeChainPortForwardPreconnectPool {
	if !shouldUseProbeChainPortForwardPreconnect(runtime, cfg) {
		return nil
	}
	pool := &probeChainPortForwardPreconnectPool{
		runtime:    runtime,
		cfg:        cfg,
		network:    strings.ToLower(strings.TrimSpace(network)),
		targetAddr: strings.TrimSpace(targetAddr),
		capacity:   1,
		ready:      make(chan *probeChainPortForwardPreconnectedConn, 1),
		refillCh:   make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
	}
	go pool.run()
	pool.requestRefill()
	return pool
}

func shouldUseProbeChainPortForwardPreconnect(runtime *probeChainRuntime, cfg probeChainRuntimePortForward) bool {
	if runtime == nil {
		return false
	}
	if normalizeProbeChainRole(runtime.cfg.role) == "entry_exit" {
		return false
	}
	if normalizeProbeChainPortForwardEntrySide(cfg.EntrySide) != probeChainPortForwardEntryChainExit && runtime.cfg.nextAuthMode == "proxy" {
		return false
	}
	return true
}

func (p *probeChainPortForwardPreconnectPool) requestRefill() {
	if p == nil {
		return
	}
	select {
	case p.refillCh <- struct{}{}:
	default:
	}
}

func (p *probeChainPortForwardPreconnectPool) run() {
	if p == nil {
		return
	}
	ticker := time.NewTicker(probeChainPortForwardPreconnectIdleTTL / 2)
	defer ticker.Stop()
	backoff := probeChainPortForwardPreconnectRetryMin
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.dropExpired()
		case <-p.refillCh:
		}
		for len(p.ready) < p.capacity {
			conn, err := p.open()
			if err != nil {
				if isProbeChainPreconnectTargetError(err) {
					// The relay link is healthy; only the target is unreachable. Prewarming
					// is pointless here (no connection could succeed), so we stay quiet and
					// retry slowly. Real connections are still served on demand the moment
					// the target recovers. Log once per down-streak, not every retry.
					if !p.targetUnreachable {
						p.targetUnreachable = true
						logProbeInfof("probe chain port forward preconnect deferred: relay link healthy, target unreachable, serving on demand: chain=%s id=%s network=%s target=%s err=%v", p.runtime.cfg.chainID, p.cfg.ID, p.network, p.targetAddr, err)
					}
					select {
					case <-p.stopCh:
						return
					case <-time.After(probeChainPortForwardPreconnectTargetRetryInterval):
					}
					break
				}

				// Transport/relay failure: this is a real link problem worth surfacing.
				logProbeWarnf("probe chain port forward preconnect link failed: chain=%s id=%s network=%s target=%s err=%v", p.runtime.cfg.chainID, p.cfg.ID, p.network, p.targetAddr, err)
				select {
				case <-p.stopCh:
					return
				case <-time.After(backoff):
				}
				backoff *= 2
				if backoff <= 0 || backoff > probeChainPortForwardPreconnectRetryMax {
					backoff = probeChainPortForwardPreconnectRetryMax
				}
				break
			}
			if p.targetUnreachable {
				p.targetUnreachable = false
				logProbeInfof("probe chain port forward preconnect recovered: chain=%s id=%s network=%s target=%s", p.runtime.cfg.chainID, p.cfg.ID, p.network, p.targetAddr)
			}
			backoff = probeChainPortForwardPreconnectRetryMin
			select {
			case p.ready <- conn:
				log.Printf("probe chain port forward preconnected: chain=%s id=%s network=%s target=%s flow_id=%s", p.runtime.cfg.chainID, p.cfg.ID, p.network, p.targetAddr, conn.flowID)
			default:
				_ = conn.conn.Close()
			}
		}
	}
}

func (p *probeChainPortForwardPreconnectPool) dropExpired() {
	if p == nil {
		return
	}
	kept := make([]*probeChainPortForwardPreconnectedConn, 0, p.capacity)
	for {
		select {
		case item := <-p.ready:
			if item == nil || item.conn == nil {
				continue
			}
			if !item.expiresAt.IsZero() && time.Now().After(item.expiresAt) {
				_ = item.conn.Close()
				continue
			}
			kept = append(kept, item)
		default:
			for _, item := range kept {
				select {
				case p.ready <- item:
				default:
					_ = item.conn.Close()
				}
			}
			p.requestRefill()
			return
		}
	}
}

func (p *probeChainPortForwardPreconnectPool) open() (*probeChainPortForwardPreconnectedConn, error) {
	if p == nil {
		return nil, errors.New("preconnect pool is nil")
	}
	network := strings.ToLower(strings.TrimSpace(p.network))
	if network != probeChainPortForwardNetworkUDP {
		network = probeChainPortForwardNetworkTCP
	}
	flowID := newProbeTCPDebugFlowID("port_forward_preconnect", p.targetAddr)

	var associationV2 *probeChainAssociationV2Meta
	if network == probeChainPortForwardNetworkUDP {
		key := "preconnect:" + strings.TrimSpace(p.cfg.ID) + ":" + strings.ToLower(randomHexToken(6))
		associationV2 = &probeChainAssociationV2Meta{
			Version:         2,
			AssocKeyV2:      key,
			FlowID:          flowID,
			Transport:       "udp",
			RouteTarget:     strings.TrimSpace(p.targetAddr),
			NATMode:         probeChainUDPAssociationNATModeDefault,
			TTLProfile:      probeChainUDPAssociationTTLProfileDefault,
			IdleTimeoutMS:   probeChainPortForwardSessionIdleTTL.Milliseconds(),
			GCIntervalMS:    probeChainPortForwardSessionGCInterval.Milliseconds(),
			CreatedAtUnixMS: time.Now().UnixMilli(),
		}
	}

	normalizedEntrySide := normalizeProbeChainPortForwardEntrySide(p.cfg.EntrySide)

	// Phase 1: prepare the relay substream (the link). A failure here is a real transport problem.
	stream, err := openProbeChainPortForwardRelaySubstream(p.runtime, normalizedEntrySide, flowID)
	if err != nil {
		return nil, &probeChainPreconnectError{phase: probeChainPreconnectPhaseTransport, err: err}
	}
	// Phase 2: ask the exit to dial the target. A failure here means the target is
	// unreachable (business), not that the forwarding link is broken.
	if err := finishProbeChainPortForwardOpen(stream, network, p.targetAddr, flowID, associationV2, ""); err != nil {
		_ = stream.Close()
		return nil, &probeChainPreconnectError{phase: probeChainPreconnectPhaseTarget, err: err}
	}

	now := time.Now()
	return &probeChainPortForwardPreconnectedConn{
		conn:      stream,
		openedAt:  now,
		flowID:    flowID,
		expiresAt: now.Add(probeChainPortForwardPreconnectIdleTTL),
	}, nil
}

func (p *probeChainPortForwardPreconnectPool) acquire() (*probeChainPortForwardPreconnectedConn, bool) {
	if p == nil {
		return nil, false
	}
	for {
		select {
		case item := <-p.ready:
			if item == nil || item.conn == nil {
				continue
			}
			if !item.expiresAt.IsZero() && time.Now().After(item.expiresAt) {
				_ = item.conn.Close()
				p.requestRefill()
				continue
			}
			p.requestRefill()
			return item, true
		default:
			p.requestRefill()
			return nil, false
		}
	}
}

func (p *probeChainPortForwardPreconnectPool) close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		close(p.stopCh)
		for {
			select {
			case item := <-p.ready:
				if item != nil && item.conn != nil {
					_ = item.conn.Close()
				}
			default:
				return
			}
		}
	})
}

func runProbeChainTCPPortForward(runtime *probeChainRuntime, cfg probeChainRuntimePortForward, listener net.Listener) {
	if runtime == nil || listener == nil {
		return
	}
	targetAddr, err := buildProbeChainPortForwardTarget(cfg)
	if err != nil {
		log.Printf("probe chain tcp forward disabled: chain=%s id=%s err=%v", runtime.cfg.chainID, cfg.ID, err)
		return
	}
	preconnect := newProbeChainPortForwardPreconnectPool(runtime, cfg, probeChainPortForwardNetworkTCP, targetAddr)
	if preconnect != nil {
		defer preconnect.close()
	}
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if errors.Is(acceptErr, net.ErrClosed) {
				return
			}
			select {
			case <-runtime.stopCh:
				return
			default:
			}
			log.Printf("probe chain tcp forward accept failed: chain=%s id=%s err=%v", runtime.cfg.chainID, cfg.ID, acceptErr)
			return
		}
		go func(localConn net.Conn) {
			defer localConn.Close()
			var downstream net.Conn
			var openErr error
			if preconnected, ok := preconnect.acquire(); ok {
				downstream = preconnected.conn
				log.Printf("probe chain tcp forward using preconnected stream: chain=%s id=%s target=%s flow_id=%s age_ms=%d", runtime.cfg.chainID, cfg.ID, targetAddr, preconnected.flowID, time.Since(preconnected.openedAt).Milliseconds())
			} else {
				downstream, openErr = openProbeChainPortForwardStream(runtime, cfg.EntrySide, probeChainPortForwardNetworkTCP, targetAddr)
			}
			if openErr != nil {
				log.Printf("probe chain tcp forward open failed: chain=%s id=%s target=%s err=%v", runtime.cfg.chainID, cfg.ID, targetAddr, openErr)
				return
			}
			defer downstream.Close()

			result := relayProbeChainBidirectional(localConn, localConn, downstream, downstream)
			if relayErr := firstProbeChainRelayError(result); relayErr != nil {
				log.Printf("probe chain tcp forward relay failed: chain=%s id=%s target=%s duration_ms=%d up_bytes=%d down_bytes=%d err=%v", runtime.cfg.chainID, cfg.ID, targetAddr, result.Duration.Milliseconds(), result.LeftToRight.Bytes, result.RightToLeft.Bytes, relayErr)
			}
		}(conn)
	}
}

func runProbeChainUDPPortForward(runtime *probeChainRuntime, cfg probeChainRuntimePortForward, packetConn net.PacketConn) {
	if runtime == nil || packetConn == nil {
		return
	}
	targetAddr, err := buildProbeChainPortForwardTarget(cfg)
	if err != nil {
		log.Printf("probe chain udp forward disabled: chain=%s id=%s err=%v", runtime.cfg.chainID, cfg.ID, err)
		return
	}
	preconnect := newProbeChainPortForwardPreconnectPool(runtime, cfg, probeChainPortForwardNetworkUDP, targetAddr)
	if preconnect != nil {
		defer preconnect.close()
	}

	type udpForwardSession struct {
		clientAddr net.Addr
		stream     net.Conn
		reader     *bufio.Reader
		lastSeen   time.Time
	}

	sessions := make(map[string]*udpForwardSession)
	var sessionsMu sync.Mutex
	done := make(chan struct{})
	defer close(done)

	closeSession := func(key string, session *udpForwardSession) {
		if session == nil {
			return
		}
		sessionsMu.Lock()
		if current, ok := sessions[key]; ok && current == session {
			delete(sessions, key)
		}
		sessionsMu.Unlock()
		_ = session.stream.Close()
	}

	defer func() {
		sessionsMu.Lock()
		all := make([]*udpForwardSession, 0, len(sessions))
		for key, session := range sessions {
			delete(sessions, key)
			all = append(all, session)
		}
		sessionsMu.Unlock()
		for _, session := range all {
			if session != nil {
				_ = session.stream.Close()
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(probeChainPortForwardSessionGCInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-runtime.stopCh:
				return
			case <-ticker.C:
				now := time.Now()
				stale := make([]*udpForwardSession, 0)
				sessionsMu.Lock()
				for key, session := range sessions {
					if session == nil {
						delete(sessions, key)
						continue
					}
					if now.Sub(session.lastSeen) >= probeChainPortForwardSessionIdleTTL {
						delete(sessions, key)
						stale = append(stale, session)
					}
				}
				sessionsMu.Unlock()
				for _, session := range stale {
					_ = session.stream.Close()
				}
			}
		}
	}()

	openSession := func(key string, addr net.Addr) (*udpForwardSession, error) {
		associationV2 := &probeChainAssociationV2Meta{
			Version:         2,
			AssocKeyV2:      strings.TrimSpace(key),
			FlowID:          strings.TrimSpace(key),
			Transport:       "udp",
			RouteTarget:     strings.TrimSpace(targetAddr),
			NATMode:         probeChainUDPAssociationNATModeDefault,
			TTLProfile:      probeChainUDPAssociationTTLProfileDefault,
			IdleTimeoutMS:   probeChainPortForwardSessionIdleTTL.Milliseconds(),
			GCIntervalMS:    probeChainPortForwardSessionGCInterval.Milliseconds(),
			CreatedAtUnixMS: time.Now().UnixMilli(),
		}
		var stream net.Conn
		if preconnected, ok := preconnect.acquire(); ok {
			stream = preconnected.conn
			log.Printf("probe chain udp forward using preconnected stream: chain=%s id=%s target=%s flow_id=%s age_ms=%d client=%s", runtime.cfg.chainID, cfg.ID, targetAddr, preconnected.flowID, time.Since(preconnected.openedAt).Milliseconds(), key)
		} else {
			var openErr error
			stream, openErr = openProbeChainPortForwardStreamWithAssociation(runtime, cfg.EntrySide, probeChainPortForwardNetworkUDP, targetAddr, associationV2)
			if openErr != nil {
				return nil, openErr
			}
		}
		session := &udpForwardSession{
			clientAddr: addr,
			stream:     stream,
			reader:     bufio.NewReader(stream),
			lastSeen:   time.Now(),
		}
		go func(sessionKey string, current *udpForwardSession) {
			for {
				payload, readErr := readProbeChainFramedPacket(current.reader)
				if readErr != nil {
					closeSession(sessionKey, current)
					return
				}
				if len(payload) == 0 {
					continue
				}
				if _, writeErr := packetConn.WriteTo(payload, current.clientAddr); writeErr != nil {
					closeSession(sessionKey, current)
					return
				}
				sessionsMu.Lock()
				if active, ok := sessions[sessionKey]; ok && active == current {
					active.lastSeen = time.Now()
				}
				sessionsMu.Unlock()
			}
		}(key, session)
		return session, nil
	}

	buf := make([]byte, 64*1024)
	for {
		n, addr, readErr := packetConn.ReadFrom(buf)
		if readErr != nil {
			if errors.Is(readErr, net.ErrClosed) {
				return
			}
			select {
			case <-runtime.stopCh:
				return
			default:
			}
			log.Printf("probe chain udp forward read failed: chain=%s id=%s err=%v", runtime.cfg.chainID, cfg.ID, readErr)
			return
		}
		if n <= 0 || addr == nil {
			continue
		}
		key := strings.TrimSpace(addr.String())
		if key == "" {
			continue
		}
		payload := append([]byte(nil), buf[:n]...)

		sessionsMu.Lock()
		session := sessions[key]
		if session == nil {
			created, openErr := openSession(key, addr)
			if openErr != nil {
				sessionsMu.Unlock()
				log.Printf("probe chain udp forward open failed: chain=%s id=%s target=%s err=%v", runtime.cfg.chainID, cfg.ID, targetAddr, openErr)
				continue
			}
			sessions[key] = created
			session = created
		}
		session.lastSeen = time.Now()
		stream := session.stream
		sessionsMu.Unlock()

		if writeErr := writeProbeChainFramedPacket(stream, payload); writeErr != nil {
			log.Printf("probe chain udp forward write failed: chain=%s id=%s target=%s err=%v", runtime.cfg.chainID, cfg.ID, targetAddr, writeErr)
			closeSession(key, session)
		}
	}
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
			log.Printf("probe chain bridge dial failed: chain=%s role=%s tag=%s target=%s:%d assign_downstream=%t assign_upstream=%t accept_streams=%t err=%v", runtime.cfg.chainID, runtime.cfg.role, target.Tag, target.Host, target.Port, target.AssignDownstream, target.AssignUpstream, target.AcceptStreams, err)
			sleepProbeChainBridgeBackoff(runtime.stopCh, backoff)
			backoff = nextProbeChainBridgeBackoff(backoff)
			continue
		}

		session, err := yamux.Client(conn, newProbeChainYamuxConfig())
		if err != nil {
			_ = conn.Close()
			log.Printf("probe chain bridge session setup failed: chain=%s role=%s tag=%s target=%s:%d assign_downstream=%t assign_upstream=%t accept_streams=%t err=%v", runtime.cfg.chainID, runtime.cfg.role, target.Tag, target.Host, target.Port, target.AssignDownstream, target.AssignUpstream, target.AcceptStreams, err)
			sleepProbeChainBridgeBackoff(runtime.stopCh, backoff)
			backoff = nextProbeChainBridgeBackoff(backoff)
			continue
		}
		sessionID := runtime.nextBridgeSessionID(target.Tag)
		log.Printf("probe chain bridge connected: chain=%s role=%s tag=%s session_id=%s target=%s:%d assign_downstream=%t assign_upstream=%t accept_streams=%t", runtime.cfg.chainID, runtime.cfg.role, target.Tag, sessionID, target.Host, target.Port, target.AssignDownstream, target.AssignUpstream, target.AcceptStreams)
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
		log.Printf("probe chain bridge disconnected: chain=%s role=%s tag=%s session_id=%s target=%s:%d assign_downstream=%t assign_upstream=%t accept_streams=%t", runtime.cfg.chainID, runtime.cfg.role, target.Tag, sessionID, target.Host, target.Port, target.AssignDownstream, target.AssignUpstream, target.AcceptStreams)
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

func waitProbeChainBridgeSession(stopCh <-chan struct{}, session *yamux.Session) {
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

func acceptProbeChainBridgeStreams(runtime *probeChainRuntime, session *yamux.Session, sessionID string, tag string, routeDirection string) {
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
	if relayMode == probeChainRelayModeStream {
		connToken := strings.TrimSpace(r.Header.Get(probeChainCodexConnIDHeader))
		if isProbeChainHTTP3WebSocketRequest(r) {
			handleProbeChainStreamRelayHTTP3WebSocket(runtime, bridgeRole, connToken, w, r)
			return
		}
		if websocket.IsWebSocketUpgrade(r) {
			handleProbeChainStreamRelayWebSocket(runtime, bridgeRole, connToken, w, r)
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

func handleProbeChainStreamRelayWebSocket(runtime *probeChainRuntime, bridgeRole string, connToken string, w http.ResponseWriter, r *http.Request) {
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	role := normalizeProbeChainBridgeRole(bridgeRole)
	log.Printf("probe chain websocket stream request: chain=%s role=%s bridge_role=%s remote=%s host=%s proto=%s", runtime.cfg.chainID, runtime.cfg.role, role, r.RemoteAddr, r.Host, r.Proto)
	upgrader := websocket.Upgrader{
		CheckOrigin:       func(*http.Request) bool { return true },
		ReadBufferSize:    probeChainRelayWebSocketBufferBytes,
		WriteBufferSize:   probeChainRelayWebSocketBufferBytes,
		EnableCompression: false,
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("probe chain websocket stream upgrade failed: chain=%s role=%s remote=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr, err)
		return
	}
	conn := newWebSocketNetConn(ws)
	if reverseConn := runtime.acceptReverseDataStream(strings.TrimSpace(connToken), conn); reverseConn != nil {
		<-reverseConn.done
		return
	}
	if role == probeChainBridgeRoleToPrev {
		handleProbeChainReverseConn(runtime, conn, "")
		return
	}
	handleProbeChainConn(runtime, conn, "")
}

func handleProbeChainStreamRelayHTTP3WebSocket(runtime *probeChainRuntime, bridgeRole string, connToken string, w http.ResponseWriter, r *http.Request) {
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	role := normalizeProbeChainBridgeRole(bridgeRole)
	log.Printf("probe chain h3 websocket stream request: chain=%s role=%s bridge_role=%s remote=%s host=%s proto=%s", runtime.cfg.chainID, runtime.cfg.role, role, r.RemoteAddr, r.Host, r.Proto)
	streamer, ok := w.(http3.HTTPStreamer)
	if !ok {
		log.Printf("probe chain h3 websocket stream rejected: chain=%s role=%s remote=%s reason=http3_stream_unavailable", runtime.cfg.chainID, runtime.cfg.role, r.RemoteAddr)
		http.Error(w, "http3 stream unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	stream := streamer.HTTPStream()
	conn := &probeChainHTTP3StreamNetConn{
		stream: stream,
		local:  probeChainRelayNetAddr{label: "probe-chain-h3-stream-local"},
		remote: probeChainRelayNetAddr{label: strings.TrimSpace(r.RemoteAddr)},
		closeFn: func() error {
			return stream.Close()
		},
	}
	if reverseConn := runtime.acceptReverseDataStream(strings.TrimSpace(connToken), conn); reverseConn != nil {
		<-reverseConn.done
		return
	}
	if role == probeChainBridgeRoleToPrev {
		handleProbeChainReverseConn(runtime, conn, "")
		return
	}
	handleProbeChainConn(runtime, conn, "")
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
	session, err := yamux.Server(conn, newProbeChainYamuxConfig())
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
	session, err := yamux.Server(conn, newProbeChainYamuxConfig())
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
	if blocked, until := isProbeChainAuthIPBlacklisted(sourceIP); blocked {
		delayProbeChainAuthFailure()
		log.Printf("probe chain auth rejected (ip blacklisted): chain=%s ip=%s until=%s", strings.TrimSpace(chainID), sourceIP, until.UTC().Format(time.RFC3339))
		return errors.New("source ip is blacklisted")
	}

	env, err := readProbeChainAuthEnvelopeFromHeaders(r.Header, chainID)
	if err != nil {
		failures, blacklisted, until := recordProbeChainAuthFailure(sourceIP)
		delayProbeChainAuthFailure()
		logProbeChainAuthFailure(strings.TrimSpace(chainID), sourceIP, failures, blacklisted, until, err)
		return err
	}
	if err := verifyProbeChainInboundAuth(runtime.cfg, env); err != nil {
		failures, blacklisted, until := recordProbeChainAuthFailure(sourceIP)
		delayProbeChainAuthFailure()
		logProbeChainAuthFailure(strings.TrimSpace(chainID), sourceIP, failures, blacklisted, until, err)
		return err
	}
	resetProbeChainAuthFailure(sourceIP)
	return nil
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
	rt.forwardMu.Lock()
	tcpForwards := rt.tcpForwards
	udpForwards := rt.udpForwards
	rt.tcpForwards = nil
	rt.udpForwards = nil
	rt.forwardMu.Unlock()
	closedSessions := make(map[*yamux.Session]struct{})
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
	for _, ln := range tcpForwards {
		if ln != nil {
			_ = ln.Close()
		}
	}
	for _, pc := range udpForwards {
		if pc != nil {
			_ = pc.Close()
		}
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

func (rt *probeChainRuntime) setDownstreamSession(sessionID string, session *yamux.Session, bridgeRole string, remoteAddr string) {
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
	rt.bridgeMu.Lock()
	if rt.downstreamSessions == nil {
		rt.downstreamSessions = make(map[string]*probeChainBridgeSession)
	}
	rt.downstreamSessions[cleanID] = item
	active := len(rt.downstreamSessions)
	rt.bridgeMu.Unlock()
	log.Printf("probe chain downstream session assigned: chain=%s role=%s session_id=%s active=%d remote=%s", strings.TrimSpace(rt.cfg.chainID), strings.TrimSpace(rt.cfg.role), cleanID, active, item.RemoteAddr)
}

func (rt *probeChainRuntime) clearDownstreamSession(sessionID string, target *yamux.Session) {
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
	} else {
		for key, item := range rt.downstreamSessions {
			if item != nil && item.Session == target {
				delete(rt.downstreamSessions, key)
				cleanID = key
				cleared = true
				break
			}
		}
	}
	remaining = len(rt.downstreamSessions)
	rt.bridgeMu.Unlock()
	log.Printf("probe chain downstream session cleared: chain=%s role=%s session_id=%s target=%p cleared=%t remaining=%d", strings.TrimSpace(rt.cfg.chainID), strings.TrimSpace(rt.cfg.role), cleanID, target, cleared, remaining)
}

func (rt *probeChainRuntime) getDownstreamSession() *yamux.Session {
	if rt == nil {
		return nil
	}
	rt.bridgeMu.Lock()
	defer rt.bridgeMu.Unlock()
	var latest *probeChainBridgeSession
	for _, item := range rt.downstreamSessions {
		if item == nil || item.Session == nil || item.Session.IsClosed() {
			continue
		}
		if latest == nil || item.ConnectedAt.After(latest.ConnectedAt) {
			latest = item
		}
	}
	if latest == nil {
		return nil
	}
	return latest.Session
}

func (rt *probeChainRuntime) setUpstreamSession(sessionID string, session *yamux.Session, bridgeRole string, remoteAddr string) {
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
	rt.bridgeMu.Lock()
	if rt.upstreamSessions == nil {
		rt.upstreamSessions = make(map[string]*probeChainBridgeSession)
	}
	rt.upstreamSessions[cleanID] = item
	active := len(rt.upstreamSessions)
	rt.bridgeMu.Unlock()
	log.Printf("probe chain upstream session assigned: chain=%s role=%s session_id=%s active=%d remote=%s", strings.TrimSpace(rt.cfg.chainID), strings.TrimSpace(rt.cfg.role), cleanID, active, item.RemoteAddr)
}

func (rt *probeChainRuntime) clearUpstreamSession(sessionID string, target *yamux.Session) {
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
	} else {
		for key, item := range rt.upstreamSessions {
			if item != nil && item.Session == target {
				delete(rt.upstreamSessions, key)
				cleanID = key
				cleared = true
				break
			}
		}
	}
	remaining = len(rt.upstreamSessions)
	rt.bridgeMu.Unlock()
	log.Printf("probe chain upstream session cleared: chain=%s role=%s session_id=%s target=%p cleared=%t remaining=%d", strings.TrimSpace(rt.cfg.chainID), strings.TrimSpace(rt.cfg.role), cleanID, target, cleared, remaining)
}

func (rt *probeChainRuntime) getUpstreamSession() *yamux.Session {
	if rt == nil {
		return nil
	}
	rt.bridgeMu.Lock()
	defer rt.bridgeMu.Unlock()
	var latest *probeChainBridgeSession
	for _, item := range rt.upstreamSessions {
		if item == nil || item.Session == nil || item.Session.IsClosed() {
			continue
		}
		if latest == nil || item.ConnectedAt.After(latest.ConnectedAt) {
			latest = item
		}
	}
	if latest == nil {
		return nil
	}
	return latest.Session
}

func (rt *probeChainRuntime) registerReverseDataWaiter(token string) (chan *probeChainReverseDataConn, error) {
	if rt == nil {
		return nil, errors.New("runtime is nil")
	}
	cleanToken := strings.TrimSpace(token)
	if cleanToken == "" {
		return nil, errors.New("reverse data token is required")
	}
	ch := make(chan *probeChainReverseDataConn, 1)
	rt.reverseDataMu.Lock()
	if rt.reverseDataStreams == nil {
		rt.reverseDataStreams = make(map[string]chan *probeChainReverseDataConn)
	}
	if _, exists := rt.reverseDataStreams[cleanToken]; exists {
		rt.reverseDataMu.Unlock()
		return nil, fmt.Errorf("reverse data token already exists: %s", cleanToken)
	}
	rt.reverseDataStreams[cleanToken] = ch
	rt.reverseDataMu.Unlock()
	return ch, nil
}

func (rt *probeChainRuntime) unregisterReverseDataWaiter(token string) {
	if rt == nil {
		return
	}
	cleanToken := strings.TrimSpace(token)
	if cleanToken == "" {
		return
	}
	rt.reverseDataMu.Lock()
	delete(rt.reverseDataStreams, cleanToken)
	rt.reverseDataMu.Unlock()
}

func (rt *probeChainRuntime) acceptReverseDataStream(token string, conn net.Conn) *probeChainReverseDataConn {
	if rt == nil || conn == nil {
		return nil
	}
	cleanToken := strings.TrimSpace(token)
	if cleanToken == "" {
		return nil
	}
	rt.reverseDataMu.Lock()
	ch := rt.reverseDataStreams[cleanToken]
	if ch != nil {
		delete(rt.reverseDataStreams, cleanToken)
	}
	rt.reverseDataMu.Unlock()
	if ch == nil {
		return nil
	}
	reverseConn := newProbeChainReverseDataConn(conn)
	select {
	case ch <- reverseConn:
	default:
		_ = reverseConn.Close()
		return nil
	}
	return reverseConn
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

	reader := bufio.NewReader(conn)
	if _, hintErr := readProbeChainSourceIPHint(reader); hintErr != nil {
		log.Printf("probe chain source hint parse failed: chain=%s err=%v", runtime.cfg.chainID, hintErr)
		return
	}
	if handled := handleProbeChainBridgeControlIfPresent(runtime, conn, reader); handled {
		return
	}

	_ = conn.SetDeadline(time.Time{})
	if runtime.cfg.nextAuthMode == "proxy" {
		if err := handleProbeChainProxyConn(runtime, conn, reader); err != nil {
			log.Printf("probe chain proxy failed: chain=%s role=%s remote=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String(), err)
		}
		return
	}

	nextHop, err := openProbeChainNextHop(runtime, preferredSessionID)
	if err != nil {
		log.Printf("probe chain open downstream stream failed: chain=%s role=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, err)
		return
	}
	log.Printf("probe chain downstream stream connected: chain=%s role=%s remote=%s", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String())
	defer func() {
		if nextHop != nil && nextHop.CloseFn != nil {
			_ = nextHop.CloseFn()
		}
	}()
	nextReader := bufio.NewReader(nextHop.Reader)

	result := relayProbeChainDuplex(
		reader,
		nextHop.Writer,
		func() { closeProbeChainWriter(nextHop.Writer) },
		nextReader,
		conn,
		func() { closeProbeChainConnWrite(conn) },
	)
	relayErr := firstProbeChainRelayError(result)
	if relayErr != nil {
		log.Printf("probe chain downstream relay failed: chain=%s role=%s remote=%s duration_ms=%d up_bytes=%d down_bytes=%d err=%v", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String(), result.Duration.Milliseconds(), result.LeftToRight.Bytes, result.RightToLeft.Bytes, relayErr)
	} else {
		log.Printf("probe chain downstream relay closed: chain=%s role=%s remote=%s duration_ms=%d up_bytes=%d down_bytes=%d", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String(), result.Duration.Milliseconds(), result.LeftToRight.Bytes, result.RightToLeft.Bytes)
	}
}

func handleProbeChainReverseConn(runtime *probeChainRuntime, conn net.Conn, preferredSessionID string) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	if _, hintErr := readProbeChainSourceIPHint(reader); hintErr != nil {
		log.Printf("probe chain reverse source hint parse failed: chain=%s err=%v", runtime.cfg.chainID, hintErr)
		return
	}
	if handled := handleProbeChainBridgeControlIfPresent(runtime, conn, reader); handled {
		return
	}

	_ = conn.SetDeadline(time.Time{})
	role := normalizeProbeChainRole(runtime.cfg.role)
	if role == "entry" || role == "entry_exit" {
		if err := handleProbeChainProxyConn(runtime, conn, reader); err != nil {
			log.Printf("probe chain reverse proxy failed: chain=%s role=%s remote=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String(), err)
		}
		return
	}

	currentUpstream := runtime.getUpstreamSession()
	upstreamState := "nil"
	upstreamClosed := false
	if currentUpstream != nil {
		upstreamState = fmt.Sprintf("%p", currentUpstream)
		upstreamClosed = currentUpstream.IsClosed()
	}
	log.Printf("probe chain reverse conn opening prev hop: chain=%s role=%s remote=%s upstream_session=%s upstream_closed=%t", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String(), upstreamState, upstreamClosed)

	prevHop, err := openProbeChainPrevHop(runtime, preferredSessionID)
	if err != nil {
		latestUpstream := runtime.getUpstreamSession()
		latestState := "nil"
		latestClosed := false
		if latestUpstream != nil {
			latestState = fmt.Sprintf("%p", latestUpstream)
			latestClosed = latestUpstream.IsClosed()
		}
		log.Printf("probe chain open upstream stream failed: chain=%s role=%s remote=%s upstream_session=%s upstream_closed=%t err=%v", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String(), latestState, latestClosed, err)
		return
	}
	log.Printf("probe chain upstream stream connected: chain=%s role=%s remote=%s", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String())
	defer func() {
		if prevHop != nil && prevHop.CloseFn != nil {
			_ = prevHop.CloseFn()
		}
	}()
	prevReader := bufio.NewReader(prevHop.Reader)

	result := relayProbeChainDuplex(
		reader,
		prevHop.Writer,
		func() { closeProbeChainWriter(prevHop.Writer) },
		prevReader,
		conn,
		func() { closeProbeChainConnWrite(conn) },
	)
	relayErr := firstProbeChainRelayError(result)
	if relayErr != nil {
		log.Printf("probe chain upstream relay failed: chain=%s role=%s remote=%s duration_ms=%d up_bytes=%d down_bytes=%d err=%v", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String(), result.Duration.Milliseconds(), result.LeftToRight.Bytes, result.RightToLeft.Bytes, relayErr)
	} else {
		log.Printf("probe chain upstream relay closed: chain=%s role=%s remote=%s duration_ms=%d up_bytes=%d down_bytes=%d", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String(), result.Duration.Milliseconds(), result.LeftToRight.Bytes, result.RightToLeft.Bytes)
	}
}

func handleProbeChainBridgeControlIfPresent(runtime *probeChainRuntime, conn net.Conn, reader *bufio.Reader) bool {
	if runtime == nil || conn == nil || reader == nil {
		return false
	}
	peek, err := reader.Peek(len(probeChainBridgeControlPrefix))
	if err != nil || string(peek) != probeChainBridgeControlPrefix {
		return false
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("probe chain bridge control read failed: chain=%s role=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, err)
		return true
	}
	var req probeChainBridgeControlRequest
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), probeChainBridgeControlPrefix))
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		_ = writeProbeChainBridgeControlResponse(conn, probeChainBridgeControlResponse{
			Type:      probeChainBridgeControlReverseResult,
			RequestID: strings.TrimSpace(req.RequestID),
			OK:        false,
			Error:     "invalid bridge control request",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		return true
	}
	handleProbeChainBridgeControl(runtime, conn, req)
	return true
}

func handleProbeChainBridgeControl(runtime *probeChainRuntime, conn net.Conn, req probeChainBridgeControlRequest) {
	if runtime == nil || conn == nil {
		return
	}
	resp := probeChainBridgeControlResponse{
		Type:      probeChainBridgeControlReverseResult,
		RequestID: strings.TrimSpace(req.RequestID),
		OK:        false,
		Token:     strings.TrimSpace(req.Token),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if !strings.EqualFold(strings.TrimSpace(req.Type), probeChainBridgeControlReverseOpen) {
		resp.Error = "unsupported bridge control request"
		_ = writeProbeChainBridgeControlResponse(conn, resp)
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		resp.Error = "reverse data token is required"
		_ = writeProbeChainBridgeControlResponse(conn, resp)
		return
	}
	dialRole := normalizeProbeChainBridgeRole(req.BridgeRole)
	dataConn, err := openProbeChainPortForwardIndependentDataStreamWithToken(runtime, dialRole, token)
	if err != nil {
		resp.Error = err.Error()
		_ = writeProbeChainBridgeControlResponse(conn, resp)
		return
	}
	resp.OK = true
	if err := writeProbeChainBridgeControlResponse(conn, resp); err != nil {
		_ = dataConn.Close()
		return
	}
	if dialRole == probeChainBridgeRoleToNext {
		go handleProbeChainReverseConn(runtime, dataConn, "")
		return
	}
	go handleProbeChainConn(runtime, dataConn, "")
}

func writeProbeChainBridgeControlRequest(conn net.Conn, req probeChainBridgeControlRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(conn, "%s%s\n", probeChainBridgeControlPrefix, raw)
	return err
}

func writeProbeChainBridgeControlResponse(conn net.Conn, resp probeChainBridgeControlResponse) error {
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := json.NewEncoder(conn).Encode(resp)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

func openProbeChainNextHop(runtime *probeChainRuntime, preferredSessionID string) (*probeChainNextHop, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	if runtime.cfg.nextAuthMode == "proxy" {
		return nil, errors.New("next hop is proxy mode")
	}
	stream, err := openProbeChainDownstreamStream(runtime, strings.TrimSpace(preferredSessionID), probeChainDownstreamOpenTimeout)
	if err != nil {
		return nil, err
	}
	return &probeChainNextHop{
		Writer: stream,
		Reader: stream,
		CloseFn: func() error {
			return stream.Close()
		},
	}, nil
}

func openProbeChainPrevHop(runtime *probeChainRuntime, preferredSessionID string) (*probeChainNextHop, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	stream, err := openProbeChainUpstreamStream(runtime, strings.TrimSpace(preferredSessionID), probeChainDownstreamOpenTimeout)
	if err != nil {
		return nil, err
	}
	return &probeChainNextHop{
		Writer: stream,
		Reader: stream,
		CloseFn: func() error {
			return stream.Close()
		},
	}, nil
}

func (rt *probeChainRuntime) getDownstreamSessionByID(sessionID string) *yamux.Session {
	if rt == nil {
		return nil
	}
	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		return rt.getDownstreamSession()
	}
	rt.bridgeMu.Lock()
	defer rt.bridgeMu.Unlock()
	item, ok := rt.downstreamSessions[cleanID]
	if !ok || item == nil || item.Session == nil || item.Session.IsClosed() {
		return nil
	}
	return item.Session
}

func (rt *probeChainRuntime) getUpstreamSessionByID(sessionID string) *yamux.Session {
	if rt == nil {
		return nil
	}
	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		return rt.getUpstreamSession()
	}
	rt.bridgeMu.Lock()
	defer rt.bridgeMu.Unlock()
	item, ok := rt.upstreamSessions[cleanID]
	if !ok || item == nil || item.Session == nil || item.Session.IsClosed() {
		return nil
	}
	return item.Session
}

func openProbeChainDownstreamStream(runtime *probeChainRuntime, preferredSessionID string, timeout time.Duration) (net.Conn, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		session := runtime.getDownstreamSessionByID(preferredSessionID)
		if session != nil && !session.IsClosed() {
			stream, openErr := session.Open()
			if openErr == nil {
				return stream, nil
			}
			if session.IsClosed() {
				runtime.clearDownstreamSession("", session)
			}
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-runtime.stopCh:
			return nil, errors.New("runtime stopped")
		case <-time.After(300 * time.Millisecond):
		}
	}
	if strings.TrimSpace(preferredSessionID) != "" {
		return nil, fmt.Errorf("downstream bridge is unavailable for session_id=%s", strings.TrimSpace(preferredSessionID))
	}
	return nil, fmt.Errorf("downstream bridge is unavailable")
}

func openProbeChainUpstreamStream(runtime *probeChainRuntime, preferredSessionID string, timeout time.Duration) (net.Conn, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	attempt := 0
	for {
		attempt++
		session := runtime.getUpstreamSessionByID(preferredSessionID)
		if session != nil {
			closed := session.IsClosed()
			log.Printf("probe chain upstream stream attempt: chain=%s role=%s attempt=%d session=%p closed=%t", runtime.cfg.chainID, runtime.cfg.role, attempt, session, closed)
			if !closed {
				stream, openErr := session.Open()
				if openErr == nil {
					log.Printf("probe chain upstream stream opened: chain=%s role=%s attempt=%d session=%p", runtime.cfg.chainID, runtime.cfg.role, attempt, session)
					return stream, nil
				}
				log.Printf("probe chain upstream stream open failed: chain=%s role=%s attempt=%d session=%p err=%v", runtime.cfg.chainID, runtime.cfg.role, attempt, session, openErr)
				if session.IsClosed() {
					log.Printf("probe chain upstream session became closed while opening stream: chain=%s role=%s attempt=%d session=%p", runtime.cfg.chainID, runtime.cfg.role, attempt, session)
					runtime.clearUpstreamSession("", session)
				}
			}
		} else {
			log.Printf("probe chain upstream stream attempt: chain=%s role=%s attempt=%d session=nil", runtime.cfg.chainID, runtime.cfg.role, attempt)
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-runtime.stopCh:
			return nil, errors.New("runtime stopped")
		case <-time.After(300 * time.Millisecond):
		}
	}
	log.Printf("probe chain upstream stream unavailable: chain=%s role=%s attempts=%d timeout=%s session_id=%s", runtime.cfg.chainID, runtime.cfg.role, attempt, timeout, strings.TrimSpace(preferredSessionID))
	if strings.TrimSpace(preferredSessionID) != "" {
		return nil, fmt.Errorf("upstream bridge is unavailable for session_id=%s", strings.TrimSpace(preferredSessionID))
	}
	return nil, fmt.Errorf("upstream bridge is unavailable")
}

func resolveProbeChainOutboundLinkLayer(cfg probeChainRuntimeConfig) string {
	return normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(cfg.nextLinkLayer), strings.TrimSpace(cfg.linkLayer)))
}

func openProbeChainBridgeRelayNetConn(cfg probeChainRuntimeConfig, target probeChainBridgeDialTarget) (net.Conn, error) {
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

func (c *probeChainStreamProxyConn) Read(payload []byte) (int, error) {
	if c == nil || c.reader == nil {
		if c == nil || c.Conn == nil {
			return 0, io.EOF
		}
		return c.Conn.Read(payload)
	}
	return c.reader.Read(payload)
}

func newProbeChainYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.AcceptBacklog = probeChainRelayYamuxAcceptBacklog
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = probeChainRelayYamuxKeepAliveInterval
	cfg.ConnectionWriteTimeout = probeChainRelayYamuxWriteTimeout
	cfg.MaxStreamWindowSize = probeChainRelayYamuxMaxStreamWindowBytes
	return cfg
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

func handleProbeChainProxyConn(runtime *probeChainRuntime, conn net.Conn, reader *bufio.Reader) error {
	baseConn := net.Conn(conn)
	if reader != nil {
		baseConn = &probeChainStreamProxyConn{
			Conn:   conn,
			reader: reader,
		}
	}
	handleProbeChainProxyStream(runtime, baseConn)
	return nil
}

func handleProbeChainProxyStream(runtime *probeChainRuntime, stream net.Conn) {
	if stream == nil {
		return
	}
	defer stream.Close()

	_ = stream.SetReadDeadline(time.Now().Add(20 * time.Second))
	var req probeChainTunnelOpenRequest
	if err := json.NewDecoder(stream).Decode(&req); err != nil {
		chainID := ""
		role := ""
		if runtime != nil {
			chainID = strings.TrimSpace(runtime.cfg.chainID)
			role = strings.TrimSpace(runtime.cfg.role)
		}
		log.Printf("probe chain proxy open request decode failed: chain=%s role=%s err=%v", chainID, role, err)
		return
	}
	_ = stream.SetReadDeadline(time.Time{})

	if strings.EqualFold(strings.TrimSpace(req.Type), probeChainRelayModePingPong) {
		handleProbeChainPingPongStream(runtime, stream, req.PingBytes)
		return
	}
	if strings.EqualFold(strings.TrimSpace(req.Type), "tcp_debug_get") {
		handleProbeChainTCPDebugGet(runtime, stream, req)
		return
	}
	if strings.EqualFold(strings.TrimSpace(req.Type), "speed_debug_get") {
		handleProbeChainSpeedDebugGet(runtime, stream, req)
		return
	}

	requestedSessionID := strings.TrimSpace(req.SessionID)
	network := strings.ToLower(strings.TrimSpace(req.Network))
	if network == "" {
		network = "tcp"
	}
	target := strings.TrimSpace(req.Address)
	if target == "" {
		_ = writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: false, Error: "missing address"})
		return
	}

	if requestedSessionID != "" {
		log.Printf("probe chain proxy open request: chain=%s role=%s network=%s target=%s session_id=%s", strings.TrimSpace(runtime.cfg.chainID), strings.TrimSpace(runtime.cfg.role), network, target, requestedSessionID)
	}

	associationV2 := req.AssociationV2
	flowID := resolveProbeChainTunnelOpenFlowID(req)
	var proxyErr error
	switch network {
	case "tcp":
		proxyErr = handleProbeChainTunnelTCPStream(stream, target, flowID)
	case "udp":
		proxyErr = handleProbeChainTunnelUDPStream(stream, target, associationV2)
	case "http":
		proxyErr = handleProbeChainTunnelHTTPProxyStream(runtime, stream)
	default:
		proxyErr = writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: false, Error: "unsupported network"})
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
	log.Printf("probe chain proxy stream failed: chain=%s role=%s remote=%s err=%v", chainID, role, remote, proxyErr)
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

func handleProbeChainPingPongStream(runtime *probeChainRuntime, stream net.Conn, byteCount int64) {
	if stream == nil {
		return
	}
	if byteCount <= 0 || byteCount > 64*1024 {
		byteCount = 64
	}
	if err := writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: true}); err != nil {
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

func writeProbeChainTunnelOpenResponse(stream net.Conn, resp probeChainTunnelOpenResponse) error {
	return writeProbeChainTunnelJSONResponse(stream, resp)
}

func writeProbeChainTunnelJSONResponse(stream net.Conn, resp any) error {
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := json.NewEncoder(stream).Encode(resp)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
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

func handleProbeChainTunnelTCPStream(stream net.Conn, target string, flowID string) error {
	dialer := &net.Dialer{Timeout: probeChainPortForwardDialTimeout}
	remoteConn, err := dialer.Dial("tcp", target)
	if err != nil {
		globalProbeTCPDebugState.recordFailureWithScopeAndFlow("open_failed", "chain_exit", target, flowID, "remote", err)
		_ = writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	tuneProbeChainNetConn(remoteConn)
	defer remoteConn.Close()

	if err := writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: true}); err != nil {
		return err
	}

	relay := globalProbeTCPDebugState.beginRelayWithScopeAndFlow("chain_exit", target, flowID, "remote")
	if relay != nil {
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

func handleProbeChainTunnelUDPStream(stream net.Conn, target string, associationV2 *probeChainAssociationV2Meta) error {
	assoc, err := globalProbeChainUDPAssociationPool.Acquire(associationV2, target)
	if err != nil {
		_ = writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	defer assoc.Release()

	if err := writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: true}); err != nil {
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
				if writeErr := writeProbeChainFramedPacket(stream, buf[:n]); writeErr != nil {
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

func handleProbeChainTunnelHTTPProxyStream(runtime *probeChainRuntime, stream net.Conn) error {
	if err := writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: true}); err != nil {
		return err
	}
	return handleProbeChainHTTPProxy(runtime, stream, bufio.NewReader(stream))
}

func handleProbeChainSocksProxy(runtime *probeChainRuntime, conn net.Conn, reader *bufio.Reader) error {
	request, err := readProbeChainSocksRequest(reader, conn)
	if err != nil {
		return err
	}
	switch request.Cmd {
	case 0x01:
		targetConn, err := net.DialTimeout("tcp", request.Address, 12*time.Second)
		if err != nil {
			_ = replyProbeChainProxyFailure(conn, request.Version)
			return err
		}
		tuneProbeChainNetConn(targetConn)
		defer targetConn.Close()
		if err := replyProbeChainProxySuccess(conn, request.Version, targetConn.LocalAddr().String()); err != nil {
			return err
		}

		_ = conn.SetDeadline(time.Time{})
		_ = targetConn.SetDeadline(time.Time{})
		targetReader := bufio.NewReader(targetConn)
		relayProbeChainBidirectional(conn, reader, targetConn, targetReader)
		return nil
	case 0x03:
		// UDP ASSOCIATE over chain stream: encapsulate SOCKS5 UDP datagrams into framed TCP payloads.
		return handleProbeChainSocks5UDPAssociate(conn, reader, request.Version)
	default:
		_ = replyProbeChainProxyFailure(conn, request.Version)
		return fmt.Errorf("unsupported socks command: %d", request.Cmd)
	}
}

func handleProbeChainHTTPProxy(runtime *probeChainRuntime, conn net.Conn, reader *bufio.Reader) error {
	request, err := http.ReadRequest(reader)
	if err != nil {
		_ = writeProbeChainHTTPProxyStatus(conn, http.StatusBadRequest, "invalid proxy request")
		return err
	}
	defer request.Body.Close()

	targetAddr, err := resolveProbeChainHTTPProxyTarget(request)
	if err != nil {
		_ = writeProbeChainHTTPProxyStatus(conn, http.StatusBadRequest, "invalid proxy target")
		return err
	}
	targetConn, err := net.DialTimeout("tcp", targetAddr, 12*time.Second)
	if err != nil {
		_ = writeProbeChainHTTPProxyStatus(conn, http.StatusBadGateway, "dial target failed")
		return err
	}
	tuneProbeChainNetConn(targetConn)
	defer targetConn.Close()

	if strings.EqualFold(strings.TrimSpace(request.Method), http.MethodConnect) {
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return err
		}
		_ = conn.SetDeadline(time.Time{})
		_ = targetConn.SetDeadline(time.Time{})
		targetReader := bufio.NewReader(targetConn)
		relayProbeChainBidirectional(conn, reader, targetConn, targetReader)
		return nil
	}

	request.RequestURI = ""
	if request.URL != nil {
		if strings.TrimSpace(request.URL.Scheme) == "" {
			request.URL.Scheme = "http"
		}
		if strings.TrimSpace(request.URL.Host) == "" {
			request.URL.Host = request.Host
		}
	}
	request.Header.Del("Proxy-Connection")
	if err := request.Write(targetConn); err != nil {
		_ = writeProbeChainHTTPProxyStatus(conn, http.StatusBadGateway, "forward request failed")
		return err
	}

	_ = conn.SetDeadline(time.Time{})
	_ = targetConn.SetDeadline(time.Time{})
	targetReader := bufio.NewReader(targetConn)
	relayProbeChainBidirectional(conn, reader, targetConn, targetReader)
	return nil
}

func resolveProbeChainHTTPProxyTarget(request *http.Request) (string, error) {
	hostPort := strings.TrimSpace(request.Host)
	if request.URL != nil && strings.TrimSpace(request.URL.Host) != "" {
		hostPort = strings.TrimSpace(request.URL.Host)
	}
	defaultPort := 80
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if method == http.MethodConnect {
		defaultPort = 443
	}
	if request.URL != nil {
		switch strings.ToLower(strings.TrimSpace(request.URL.Scheme)) {
		case "https":
			defaultPort = 443
		case "http":
			defaultPort = 80
		}
	}
	return normalizeProbeChainTargetAddr(hostPort, defaultPort)
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
	if stream, ok := conn.(*yamux.Stream); ok {
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

func writeProbeChainHTTPProxyStatus(conn net.Conn, statusCode int, message string) error {
	statusText := strings.TrimSpace(http.StatusText(statusCode))
	if statusText == "" {
		statusText = "Error"
	}
	bodyText := strings.TrimSpace(message)
	if bodyText == "" {
		bodyText = statusText
	}
	body := bodyText + "\n"
	response := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\nConnection: close\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\n\r\n%s",
		statusCode,
		statusText,
		len(body),
		body,
	)
	_, err := io.WriteString(conn, response)
	return err
}

func readProbeChainSocksRequest(br *bufio.Reader, conn net.Conn) (probeChainSocksRequest, error) {
	peek, err := br.Peek(1)
	if err != nil {
		return probeChainSocksRequest{}, err
	}
	version := peek[0]
	switch version {
	case 0x04:
		req, err := probeChainSocks4ReadRequest(br, conn)
		if err != nil {
			return probeChainSocksRequest{}, err
		}
		req.Version = version
		return req, nil
	case 0x05:
		if err := probeChainSocks5Handshake(br, conn); err != nil {
			return probeChainSocksRequest{}, err
		}
		req, err := probeChainSocks5ReadRequest(br, conn)
		if err != nil {
			return probeChainSocksRequest{}, err
		}
		req.Version = version
		return req, nil
	default:
		return probeChainSocksRequest{}, fmt.Errorf("unsupported socks version: %d", version)
	}
}

func probeChainSocks5Handshake(br *bufio.Reader, conn net.Conn) error {
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil {
		return err
	}
	if head[0] != 0x05 {
		return errors.New("invalid socks version")
	}
	nMethods := int(head[1])
	if nMethods <= 0 {
		return errors.New("invalid socks auth methods")
	}
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(br, methods); err != nil {
		return err
	}
	for _, method := range methods {
		if method == 0x00 {
			_, err := conn.Write([]byte{0x05, 0x00})
			return err
		}
	}
	_, _ = conn.Write([]byte{0x05, 0xFF})
	return errors.New("no supported socks auth methods")
}

func probeChainSocks5ReadRequest(br *bufio.Reader, conn net.Conn) (probeChainSocksRequest, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(br, head); err != nil {
		return probeChainSocksRequest{}, err
	}
	if head[0] != 0x05 {
		return probeChainSocksRequest{}, errors.New("invalid socks version")
	}
	cmd := head[1]
	if cmd != 0x01 && cmd != 0x03 {
		_ = probeChainSocks5Reply(conn, 0x07)
		return probeChainSocksRequest{}, errors.New("only CONNECT and UDP ASSOCIATE are supported")
	}

	atyp := head[3]
	host := ""
	switch atyp {
	case 0x01:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(br, ip); err != nil {
			return probeChainSocksRequest{}, err
		}
		host = net.IP(ip).String()
	case 0x03:
		size, err := br.ReadByte()
		if err != nil {
			return probeChainSocksRequest{}, err
		}
		domain := make([]byte, int(size))
		if _, err := io.ReadFull(br, domain); err != nil {
			return probeChainSocksRequest{}, err
		}
		host = strings.TrimSpace(string(domain))
	case 0x04:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(br, ip); err != nil {
			return probeChainSocksRequest{}, err
		}
		host = net.IP(ip).String()
	default:
		_ = probeChainSocks5Reply(conn, 0x08)
		return probeChainSocksRequest{}, errors.New("unsupported address type")
	}
	if strings.TrimSpace(host) == "" {
		_ = probeChainSocks5Reply(conn, 0x01)
		return probeChainSocksRequest{}, errors.New("invalid target host")
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(br, portBytes); err != nil {
		return probeChainSocksRequest{}, err
	}
	port := binary.BigEndian.Uint16(portBytes)
	if cmd == 0x01 && port == 0 {
		_ = probeChainSocks5Reply(conn, 0x01)
		return probeChainSocksRequest{}, errors.New("invalid target port")
	}
	return probeChainSocksRequest{
		Cmd:     cmd,
		Address: net.JoinHostPort(host, strconv.Itoa(int(port))),
	}, nil
}

func probeChainSocks4ReadRequest(br *bufio.Reader, conn net.Conn) (probeChainSocksRequest, error) {
	head := make([]byte, 8)
	if _, err := io.ReadFull(br, head); err != nil {
		return probeChainSocksRequest{}, err
	}
	if head[0] != 0x04 {
		return probeChainSocksRequest{}, errors.New("invalid socks4 version")
	}
	if head[1] != 0x01 {
		_ = probeChainSocks4Reply(conn, 0x5B)
		return probeChainSocksRequest{}, errors.New("only CONNECT is supported")
	}

	port := binary.BigEndian.Uint16(head[2:4])
	if port == 0 {
		_ = probeChainSocks4Reply(conn, 0x5B)
		return probeChainSocksRequest{}, errors.New("invalid target port")
	}

	if _, err := probeChainReadNullTerminated(br, 512); err != nil {
		_ = probeChainSocks4Reply(conn, 0x5B)
		return probeChainSocksRequest{}, err
	}
	ipBytes := head[4:8]
	host := ""
	if ipBytes[0] == 0x00 && ipBytes[1] == 0x00 && ipBytes[2] == 0x00 && ipBytes[3] != 0x00 {
		domain, err := probeChainReadNullTerminated(br, 1024)
		if err != nil {
			_ = probeChainSocks4Reply(conn, 0x5B)
			return probeChainSocksRequest{}, err
		}
		host = strings.TrimSpace(domain)
	} else {
		host = net.IP(ipBytes).String()
	}
	if strings.TrimSpace(host) == "" {
		_ = probeChainSocks4Reply(conn, 0x5B)
		return probeChainSocksRequest{}, errors.New("invalid target host")
	}

	return probeChainSocksRequest{
		Cmd:     0x01,
		Address: net.JoinHostPort(host, strconv.Itoa(int(port))),
	}, nil
}

func probeChainReadNullTerminated(br *bufio.Reader, maxLen int) (string, error) {
	if maxLen <= 0 {
		maxLen = 256
	}
	buffer := make([]byte, 0, maxLen)
	for len(buffer) < maxLen {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == 0x00 {
			return string(buffer), nil
		}
		buffer = append(buffer, b)
	}
	return "", errors.New("null-terminated field exceeds max length")
}

func replyProbeChainProxySuccess(conn net.Conn, version byte, bindAddr string) error {
	if version == 0x04 {
		return probeChainSocks4Reply(conn, 0x5A)
	}
	return probeChainSocks5ReplyWithAddr(conn, 0x00, bindAddr)
}

func replyProbeChainProxyFailure(conn net.Conn, version byte) error {
	if version == 0x04 {
		return probeChainSocks4Reply(conn, 0x5B)
	}
	return probeChainSocks5Reply(conn, 0x01)
}

func probeChainSocks5Reply(conn net.Conn, rep byte) error {
	return probeChainSocks5ReplyWithAddr(conn, rep, "0.0.0.0:0")
}

func probeChainSocks4Reply(conn net.Conn, rep byte) error {
	response := []byte{0x00, rep, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, err := conn.Write(response)
	return err
}

func probeChainSocks5ReplyWithAddr(conn net.Conn, rep byte, bindAddr string) error {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(bindAddr))
	if err != nil {
		host = "0.0.0.0"
		portText = "0"
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil || port < 0 || port > 65535 {
		port = 0
	}

	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip4 := ip.To4(); ip4 != nil {
		payload := []byte{0x05, rep, 0x00, 0x01, ip4[0], ip4[1], ip4[2], ip4[3], 0x00, 0x00}
		binary.BigEndian.PutUint16(payload[8:], uint16(port))
		_, err := conn.Write(payload)
		return err
	}
	if ip16 := ip.To16(); ip16 != nil {
		payload := make([]byte, 22)
		payload[0] = 0x05
		payload[1] = rep
		payload[2] = 0x00
		payload[3] = 0x04
		copy(payload[4:20], ip16)
		binary.BigEndian.PutUint16(payload[20:], uint16(port))
		_, err := conn.Write(payload)
		return err
	}

	hostBytes := []byte(strings.TrimSpace(strings.Trim(host, "[]")))
	if len(hostBytes) > 255 {
		hostBytes = hostBytes[:255]
	}
	payload := make([]byte, 5+len(hostBytes)+2)
	payload[0] = 0x05
	payload[1] = rep
	payload[2] = 0x00
	payload[3] = 0x03
	payload[4] = byte(len(hostBytes))
	copy(payload[5:5+len(hostBytes)], hostBytes)
	binary.BigEndian.PutUint16(payload[5+len(hostBytes):], uint16(port))
	_, err = conn.Write(payload)
	return err
}

func handleProbeChainSocks5UDPAssociate(conn net.Conn, reader *bufio.Reader, version byte) error {
	udpConn, err := net.ListenPacket("udp", "0.0.0.0:0")
	if err != nil {
		_ = replyProbeChainProxyFailure(conn, version)
		return err
	}
	defer udpConn.Close()

	if err := replyProbeChainProxySuccess(conn, version, udpConn.LocalAddr().String()); err != nil {
		return err
	}

	_ = conn.SetDeadline(time.Time{})
	writeMu := &sync.Mutex{}
	stopUDPRead := make(chan struct{})
	udpReadDone := make(chan struct{})
	go func() {
		defer close(udpReadDone)
		buffer := make([]byte, 64*1024)
		for {
			_ = udpConn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, fromAddr, readErr := udpConn.ReadFrom(buffer)
			if readErr != nil {
				if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
					select {
					case <-stopUDPRead:
						return
					default:
						continue
					}
				}
				return
			}
			packet, buildErr := buildProbeChainSocks5UDPDatagram(fromAddr.String(), buffer[:n])
			if buildErr != nil {
				continue
			}
			writeMu.Lock()
			frameErr := writeProbeChainFramedPacket(conn, packet)
			writeMu.Unlock()
			if frameErr != nil {
				return
			}
		}
	}()

	for {
		packet, frameErr := readProbeChainFramedPacket(reader)
		if frameErr != nil {
			close(stopUDPRead)
			<-udpReadDone
			return frameErr
		}
		targetAddr, payload, parseErr := parseProbeChainSocks5UDPDatagram(packet)
		if parseErr != nil {
			continue
		}
		remote, resolveErr := net.ResolveUDPAddr("udp", targetAddr)
		if resolveErr != nil {
			continue
		}
		if _, writeErr := udpConn.WriteTo(payload, remote); writeErr != nil {
			continue
		}
	}
}

func readProbeChainFramedPacket(reader *bufio.Reader) ([]byte, error) {
	var lengthBytes [2]byte
	if _, err := io.ReadFull(reader, lengthBytes[:]); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(lengthBytes[:]))
	if length <= 0 {
		return nil, errors.New("invalid framed packet length")
	}
	packet := make([]byte, length)
	if _, err := io.ReadFull(reader, packet); err != nil {
		return nil, err
	}
	return packet, nil
}

func readProbeChainFramedPacketInto(reader *bufio.Reader, payload []byte) (int, error) {
	var lengthBytes [2]byte
	if _, err := io.ReadFull(reader, lengthBytes[:]); err != nil {
		return 0, err
	}
	length := int(binary.BigEndian.Uint16(lengthBytes[:]))
	if length <= 0 {
		return 0, errors.New("invalid framed packet length")
	}
	if length > len(payload) {
		if _, err := io.CopyN(io.Discard, reader, int64(length)); err != nil {
			return 0, err
		}
		return 0, errors.New("framed packet payload exceeds read buffer")
	}
	if _, err := io.ReadFull(reader, payload[:length]); err != nil {
		return 0, err
	}
	return length, nil
}

func writeProbeChainFramedPacket(writer io.Writer, payload []byte) error {
	size := len(payload)
	if size <= 0 || size > 65535 {
		return errors.New("invalid framed packet payload")
	}
	frame, _ := probeChainUDPFrameBufferPool.Get().([]byte)
	if cap(frame) < 2+size {
		frame = make([]byte, 2+size)
	}
	frame = frame[:2+size]
	defer probeChainUDPFrameBufferPool.Put(frame[:cap(frame)])
	binary.BigEndian.PutUint16(frame[:2], uint16(size))
	copy(frame[2:], payload)
	n, err := writer.Write(frame)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return io.ErrShortWrite
	}
	return nil
}

func parseProbeChainSocks5UDPDatagram(packet []byte) (targetAddr string, payload []byte, err error) {
	if len(packet) < 10 {
		return "", nil, errors.New("udp packet too short")
	}
	if packet[2] != 0x00 {
		return "", nil, errors.New("fragmented udp packet is not supported")
	}

	offset := 3
	var host string
	switch packet[offset] {
	case 0x01:
		offset++
		if len(packet) < offset+4+2 {
			return "", nil, errors.New("invalid ipv4 udp packet")
		}
		host = net.IP(packet[offset : offset+4]).String()
		offset += 4
	case 0x03:
		offset++
		if len(packet) < offset+1 {
			return "", nil, errors.New("invalid domain udp packet")
		}
		hostLen := int(packet[offset])
		offset++
		if len(packet) < offset+hostLen+2 {
			return "", nil, errors.New("invalid domain udp packet")
		}
		host = string(packet[offset : offset+hostLen])
		offset += hostLen
	case 0x04:
		offset++
		if len(packet) < offset+16+2 {
			return "", nil, errors.New("invalid ipv6 udp packet")
		}
		host = net.IP(packet[offset : offset+16]).String()
		offset += 16
	default:
		return "", nil, errors.New("unsupported udp atyp")
	}

	port := binary.BigEndian.Uint16(packet[offset : offset+2])
	offset += 2
	return net.JoinHostPort(host, strconv.Itoa(int(port))), append([]byte(nil), packet[offset:]...), nil
}

func buildProbeChainSocks5UDPDatagram(addr string, payload []byte) ([]byte, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil || port <= 0 || port > 65535 {
		return nil, errors.New("invalid udp port")
	}

	ip := net.ParseIP(strings.Trim(host, "[]"))
	buffer := make([]byte, 0, 64+len(payload))
	buffer = append(buffer, 0x00, 0x00, 0x00)
	if ip4 := ip.To4(); ip4 != nil {
		buffer = append(buffer, 0x01)
		buffer = append(buffer, ip4...)
	} else if ip16 := ip.To16(); ip16 != nil {
		buffer = append(buffer, 0x04)
		buffer = append(buffer, ip16...)
	} else {
		hostBytes := []byte(host)
		if len(hostBytes) == 0 || len(hostBytes) > 255 {
			return nil, errors.New("invalid udp host")
		}
		buffer = append(buffer, 0x03, byte(len(hostBytes)))
		buffer = append(buffer, hostBytes...)
	}
	buffer = append(buffer, 0x00, 0x00)
	binary.BigEndian.PutUint16(buffer[len(buffer)-2:], uint16(port))
	buffer = append(buffer, payload...)
	return buffer, nil
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
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	env := newProbeChainAuthEnvelope("secret_hmac", chainID, nonce, "", buildProbeChainHMAC(secret, chainID, nonce))
	env.Timestamp = timestamp
	if env.Auth != nil {
		env.Auth.Timestamp = timestamp
	}
	env.AuthTicket = strings.TrimSpace(authTicket)
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

func readProbeChainSourceIPHint(reader *bufio.Reader) (string, error) {
	peek, err := reader.Peek(len(probeChainSourceIPHintPrefix))
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", err
	}
	if string(peek) != probeChainSourceIPHintPrefix {
		return "", nil
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	rawIP := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), probeChainSourceIPHintPrefix))
	if rawIP == "" {
		return "", nil
	}
	if parsed := net.ParseIP(rawIP); parsed != nil {
		return parsed.String(), nil
	}
	return "", fmt.Errorf("invalid source ip hint")
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

func recordProbeChainAuthFailure(ip string) (failures int, blacklisted bool, until time.Time) {
	target := strings.TrimSpace(ip)
	if target == "" {
		return 0, false, time.Time{}
	}
	now := time.Now()
	probeChainAuthIPStateMap.mu.Lock()
	state := probeChainAuthIPStateMap.items[target]
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
	delete(probeChainAuthIPStateMap.items, target)
	probeChainAuthIPStateMap.mu.Unlock()
}

func logProbeChainAuthFailure(chainID string, ip string, failures int, blacklisted bool, until time.Time, err error) {
	reason := sanitizeProbeChainAuthErr(fmt.Sprint(err))
	targetIP := strings.TrimSpace(ip)
	if targetIP == "" {
		targetIP = "unknown"
	}
	if blacklisted {
		log.Printf("probe chain auth failed: chain=%s ip=%s failures=%d blacklist_until=%s reason=%s", chainID, targetIP, failures, until.UTC().Format(time.RFC3339), reason)
		return
	}
	log.Printf("probe chain auth failed: chain=%s ip=%s failures=%d reason=%s", chainID, targetIP, failures, reason)
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
