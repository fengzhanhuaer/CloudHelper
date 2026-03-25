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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
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
	name            string
	userID          string
	userPublicKey   ed25519.PublicKey
	rawPublicKey    string
	secret          string
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

type probeChainRuntime struct {
	cfg               probeChainRuntimeConfig
	httpsServer       *http.Server
	http3Server       *http3.Server
	downstreamSession *yamux.Session
	bridgeMu          sync.Mutex
	forwardMu         sync.Mutex
	tcpForwards       []net.Listener
	udpForwards       []net.PacketConn
	stopCh            chan struct{}
}

type probeChainRuntimePortForward struct {
	ID         string
	Name       string
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
}

type probeChainAuthPayloadBody struct {
	Mode      string `json:"mode,omitempty"`
	ChainID   string `json:"chain_id,omitempty"`
	Nonce     string `json:"nonce,omitempty"`
	Signature string `json:"signature,omitempty"`
	MAC       string `json:"mac,omitempty"`
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

type probeChainTunnelOpenRequest struct {
	Type    string `json:"type"`
	Network string `json:"network"`
	Address string `json:"address"`
}

type probeChainTunnelOpenResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
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

type probeChainRelayNetConn struct {
	reader    io.ReadCloser
	writer    io.WriteCloser
	closeFn   func() error
	closeOnce sync.Once
}

type probeChainRelayNetAddr struct {
	label string
}

type probeChainRuntimeCacheFile struct {
	Items []probeChainRuntimeCacheItem `json:"items"`
}

type probeChainRuntimeCacheItem struct {
	ChainID         string                         `json:"chain_id"`
	Name            string                         `json:"name"`
	UserID          string                         `json:"user_id"`
	UserPublicKey   string                         `json:"user_public_key"`
	LinkSecret      string                         `json:"link_secret"`
	Role            string                         `json:"role"`
	ListenHost      string                         `json:"listen_host"`
	ListenPort      int                            `json:"listen_port"`
	LinkLayer       string                         `json:"link_layer"`
	NextLinkLayer   string                         `json:"next_link_layer,omitempty"`
	NextDialMode    string                         `json:"next_dial_mode,omitempty"`
	NextHost        string                         `json:"next_host"`
	NextPort        int                            `json:"next_port"`
	PrevHost        string                         `json:"prev_host,omitempty"`
	PrevPort        int                            `json:"prev_port,omitempty"`
	PrevLinkLayer   string                         `json:"prev_link_layer,omitempty"`
	PrevDialMode    string                         `json:"prev_dial_mode,omitempty"`
	PortForwards    []probeChainPortForwardMessage `json:"port_forwards,omitempty"`
	RequireUserAuth bool                           `json:"require_user_auth"`
	NextAuthMode    string                         `json:"next_auth_mode"`
}

var probeChainRuntimeState = struct {
	mu       sync.Mutex
	runtimes map[string]*probeChainRuntime
}{runtimes: make(map[string]*probeChainRuntime)}

const (
	probeChainRuntimeCacheFileName = "probe_link_chains_cache.json"
	probeChainRelayAPIPath         = "/api/node/chain/relay"
	probeChainSourceIPHintPrefix   = "CHSRCIP "
	probeChainTLSServerName        = "api.githubcopilot.com"
	probeChainLegacyChainIDHeader  = "X-CH-Chain-ID"
	probeChainCodexChainIDHeader   = "X-Codex-Chain-Id"
	probeChainCodexAuthModeHeader  = "X-Codex-Auth-Mode"
	probeChainCodexMACHeader       = "X-Codex-Mac"
	probeChainCodexVersionHeader   = "X-Codex-Api-Version"
	probeChainCodexRelayModeHeader = "X-Codex-Relay-Mode"
	probeChainCodexRelayRoleHeader = "X-Codex-Relay-Role"

	probeChainRelayModeBridge  = "bridge"
	probeChainBridgeRoleToNext = "to_next"
	probeChainBridgeRoleToPrev = "to_prev"

	probeChainDialModeForward = "forward"
	probeChainDialModeReverse = "reverse"
	probeChainDialModeNone    = "none"

	probeChainBridgeRetryMin = 1 * time.Second
	probeChainBridgeRetryMax = 15 * time.Second

	probeChainDownstreamOpenTimeout = 30 * time.Second

	probeChainPortForwardNetworkTCP  = "tcp"
	probeChainPortForwardNetworkUDP  = "udp"
	probeChainPortForwardNetworkBoth = "both"

	probeChainPortForwardSessionIdleTTL       = 90 * time.Second
	probeChainPortForwardSessionGCInterval    = 15 * time.Second
	probeChainPortForwardDialTimeout          = 12 * time.Second
	probeChainPortForwardResponseReadDeadline = 10 * time.Second

	probeChainAuthPacketType        = "github_copilot_auth_request"
	probeChainAuthPacketVersion     = "2025-03-22"
	probeChainAuthFailureThreshold  = 5
	probeChainAuthBlacklistTTL      = 5 * time.Hour
	probeChainAuthFailureMinDelayMs = 200
	probeChainAuthFailureMaxDelayMs = 400
)

var probeChainAuthIPStateMap = struct {
	mu    sync.Mutex
	items map[string]probeChainAuthIPState
}{
	items: make(map[string]probeChainAuthIPState),
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
	case "http":
		return "http"
	case "http2", "h2":
		return "http2"
	case "http3", "h3":
		return "http3"
	default:
		return "http"
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
		name:            strings.TrimSpace(cmd.Name),
		userID:          strings.TrimSpace(cmd.UserID),
		rawPublicKey:    strings.TrimSpace(cmd.UserPublicKey),
		secret:          secret,
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

	rt := &probeChainRuntime{
		cfg:    cfg,
		stopCh: make(chan struct{}),
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
	if err := persistProbeChainRuntimesToCache(); err != nil {
		log.Printf("warning: persist probe chain runtime cache failed: %v", err)
	}

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

type probeChainBridgeDialTarget struct {
	Host             string
	Port             int
	LinkLayer        string
	RoleHeader       string
	AssignDownstream bool
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
			AcceptStreams:    true,
			Tag:              "upstream-reverse",
		}
		if target.Host != "" && target.Port > 0 {
			go runProbeChainBridgeDialLoop(runtime, target)
		}
	}
}

func shouldStartProbeChainPortForwards(role string) bool {
	switch normalizeProbeChainRole(role) {
	case "entry", "entry_exit":
		return true
	default:
		return false
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
	if !shouldStartProbeChainPortForwards(runtime.cfg.role) {
		return
	}
	for _, item := range runtime.cfg.portForwards {
		cfg := item
		if !cfg.Enabled {
			continue
		}
		listenHost := strings.TrimSpace(cfg.ListenHost)
		if listenHost == "" {
			listenHost = "0.0.0.0"
		}
		if cfg.ListenPort <= 0 {
			continue
		}
		listenAddr := net.JoinHostPort(listenHost, strconv.Itoa(cfg.ListenPort))
		network := normalizeProbeChainPortForwardNetwork(cfg.Network)
		if network == probeChainPortForwardNetworkTCP || network == probeChainPortForwardNetworkBoth {
			ln, err := net.Listen("tcp", listenAddr)
			if err != nil {
				log.Printf("probe chain port forward tcp listen failed: chain=%s id=%s listen=%s err=%v", runtime.cfg.chainID, cfg.ID, listenAddr, err)
			} else {
				runtime.registerTCPForward(ln)
				go runProbeChainTCPPortForward(runtime, cfg, ln)
			}
		}
		if network == probeChainPortForwardNetworkUDP || network == probeChainPortForwardNetworkBoth {
			pc, err := net.ListenPacket("udp", listenAddr)
			if err != nil {
				log.Printf("probe chain port forward udp listen failed: chain=%s id=%s listen=%s err=%v", runtime.cfg.chainID, cfg.ID, listenAddr, err)
			} else {
				runtime.registerUDPForward(pc)
				go runProbeChainUDPPortForward(runtime, cfg, pc)
			}
		}
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

func openProbeChainPortForwardStream(runtime *probeChainRuntime, network string, targetAddr string) (net.Conn, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	requestedNetwork := strings.ToLower(strings.TrimSpace(network))
	if requestedNetwork == "" {
		requestedNetwork = probeChainPortForwardNetworkTCP
	}
	if runtime.cfg.nextAuthMode == "proxy" {
		if requestedNetwork != probeChainPortForwardNetworkTCP {
			return nil, errors.New("single-hop udp port forward is not supported")
		}
		dialer := &net.Dialer{Timeout: probeChainPortForwardDialTimeout}
		return dialer.Dial("tcp", strings.TrimSpace(targetAddr))
	}
	stream, err := openProbeChainDownstreamStream(runtime, probeChainDownstreamOpenTimeout)
	if err != nil {
		return nil, err
	}
	request := probeChainTunnelOpenRequest{
		Type:    "open",
		Network: requestedNetwork,
		Address: strings.TrimSpace(targetAddr),
	}
	_ = stream.SetWriteDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	if err := json.NewEncoder(stream).Encode(request); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetWriteDeadline(time.Time{})

	_ = stream.SetReadDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	var response probeChainTunnelOpenResponse
	if err := json.NewDecoder(stream).Decode(&response); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetReadDeadline(time.Time{})
	if !response.OK {
		_ = stream.Close()
		message := strings.TrimSpace(response.Error)
		if message == "" {
			message = "open downstream target failed"
		}
		return nil, errors.New(message)
	}
	return stream, nil
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
			downstream, openErr := openProbeChainPortForwardStream(runtime, probeChainPortForwardNetworkTCP, targetAddr)
			if openErr != nil {
				log.Printf("probe chain tcp forward open failed: chain=%s id=%s target=%s err=%v", runtime.cfg.chainID, cfg.ID, targetAddr, openErr)
				return
			}
			defer downstream.Close()

			errCh := make(chan error, 2)
			go func() {
				_, copyErr := io.Copy(downstream, localConn)
				closeProbeChainConnWrite(downstream)
				errCh <- copyErr
			}()
			go func() {
				_, copyErr := io.Copy(localConn, downstream)
				closeProbeChainConnWrite(localConn)
				errCh <- copyErr
			}()
			<-errCh
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
		stream, openErr := openProbeChainPortForwardStream(runtime, probeChainPortForwardNetworkUDP, targetAddr)
		if openErr != nil {
			return nil, openErr
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
			log.Printf("probe chain bridge dial failed: chain=%s tag=%s target=%s:%d err=%v", runtime.cfg.chainID, target.Tag, target.Host, target.Port, err)
			sleepProbeChainBridgeBackoff(runtime.stopCh, backoff)
			backoff = nextProbeChainBridgeBackoff(backoff)
			continue
		}

		session, err := yamux.Client(conn, newProbeChainYamuxConfig())
		if err != nil {
			_ = conn.Close()
			log.Printf("probe chain bridge session setup failed: chain=%s tag=%s target=%s:%d err=%v", runtime.cfg.chainID, target.Tag, target.Host, target.Port, err)
			sleepProbeChainBridgeBackoff(runtime.stopCh, backoff)
			backoff = nextProbeChainBridgeBackoff(backoff)
			continue
		}
		backoff = probeChainBridgeRetryMin

		if target.AssignDownstream {
			runtime.setDownstreamSession(session)
		}
		if target.AcceptStreams {
			go acceptProbeChainBridgeStreams(runtime, session, target.Tag)
		}

		waitProbeChainBridgeSession(runtime.stopCh, session)
		if target.AssignDownstream {
			runtime.clearDownstreamSession(session)
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

func acceptProbeChainBridgeStreams(runtime *probeChainRuntime, session *yamux.Session, tag string) {
	if runtime == nil || session == nil {
		return
	}
	for {
		stream, acceptErr := session.Accept()
		if acceptErr != nil {
			if errors.Is(acceptErr, io.EOF) || errors.Is(acceptErr, net.ErrClosed) || session.IsClosed() {
				return
			}
			log.Printf("probe chain bridge accept failed: chain=%s tag=%s err=%v", runtime.cfg.chainID, strings.TrimSpace(tag), acceptErr)
			return
		}
		go handleProbeChainConn(runtime, stream)
	}
}

func startProbeChainPublicRelayServer(runtime *probeChainRuntime) error {
	if runtime == nil {
		return errors.New("runtime is nil")
	}

	cfg := runtime.cfg
	listenAddr := net.JoinHostPort(cfg.listenHost, strconv.Itoa(cfg.listenPort))
	layer := normalizeProbeChainLinkLayer(cfg.linkLayer)
	handler := buildProbeChainRuntimeRelayHandler(runtime)

	cert, err := prepareProbeServerCertificate(cfg.identity, strings.TrimSpace(cfg.controllerURL))
	if err != nil {
		return fmt.Errorf("prepare chain relay certificate failed: %w", err)
	}

	switch layer {
	case "http3":
		h3Server := &http3.Server{
			Addr:    listenAddr,
			Handler: handler,
			TLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS13,
				NextProtos: []string{"h3"},
			},
		}
		runtime.http3Server = h3Server
		go func(rt *probeChainRuntime, srv *http3.Server, certFile string, keyFile string) {
			if serveErr := srv.ListenAndServeTLS(certFile, keyFile); serveErr != nil {
				select {
				case <-rt.stopCh:
					return
				default:
				}
				log.Printf("probe chain runtime public relay exited: chain=%s layer=http3 listen=%s err=%v", rt.cfg.chainID, listenAddr, serveErr)
			}
		}(runtime, h3Server, cert.CertPath, cert.KeyPath)
	default:
		httpsServer := &http.Server{
			Addr:              listenAddr,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
		}
		if layer == "http" {
			httpsServer.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
		}
		runtime.httpsServer = httpsServer
		go func(rt *probeChainRuntime, srv *http.Server, certFile string, keyFile string, protocol string) {
			serveErr := srv.ListenAndServeTLS(certFile, keyFile)
			if serveErr != nil && serveErr != http.ErrServerClosed {
				select {
				case <-rt.stopCh:
					return
				default:
				}
				log.Printf("probe chain runtime public relay exited: chain=%s layer=%s listen=%s err=%v", rt.cfg.chainID, protocol, listenAddr, serveErr)
			}
		}(runtime, httpsServer, cert.CertPath, cert.KeyPath, layer)
	}

	return nil
}

func buildProbeChainRuntimeRelayHandler(runtime *probeChainRuntime) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(probeChainRelayAPIPath, func(w http.ResponseWriter, r *http.Request) {
		handleProbeChainRelayToRuntime(runtime, w, r)
	})
	return mux
}

func handleProbeChainRelayToRuntime(runtime *probeChainRuntime, w http.ResponseWriter, r *http.Request) {
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	chainID := resolveProbeChainIDFromRequest(r)
	if chainID == "" {
		http.Error(w, "chain_id is required", http.StatusBadRequest)
		return
	}
	if chainID != strings.TrimSpace(runtime.cfg.chainID) {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	if err := verifyProbeChainRelayRequestAuth(runtime, r, chainID); err != nil {
		http.Error(w, "codex auth failed", http.StatusUnauthorized)
		return
	}
	bridgeRole := normalizeProbeChainBridgeRole(r.Header.Get(probeChainCodexRelayRoleHeader))
	handleProbeChainBridgeRelayHTTP(runtime, bridgeRole, w, r)
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
		Mode:       strings.ToLower(strings.TrimSpace(headers.Get(probeChainCodexAuthModeHeader))),
		ChainID:    strings.TrimSpace(chainID),
		Nonce:      strings.TrimSpace(nonce),
		MAC:        strings.TrimSpace(headers.Get(probeChainCodexMACHeader)),
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

func handleProbeChainBridgeRelayHTTP(runtime *probeChainRuntime, bridgeRole string, w http.ResponseWriter, r *http.Request) {
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}

	pipeClient, pipeRuntime := net.Pipe()
	defer pipeClient.Close()
	defer pipeRuntime.Close()

	if controller := http.NewResponseController(w); controller != nil {
		_ = controller.EnableFullDuplex()
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	streamWriter := &probeChainHTTPResponseStreamWriter{
		writer:  w,
		flusher: flusher,
	}
	done := make(chan error, 2)
	go func() {
		_, copyErr := io.Copy(pipeClient, r.Body)
		closeProbeChainConnWrite(pipeClient)
		done <- copyErr
	}()
	go func() {
		_, copyErr := io.Copy(streamWriter, pipeClient)
		done <- copyErr
	}()

	session, err := yamux.Server(pipeRuntime, newProbeChainYamuxConfig())
	if err != nil {
		return
	}

	role := normalizeProbeChainBridgeRole(bridgeRole)
	if role == probeChainBridgeRoleToPrev {
		runtime.setDownstreamSession(session)
		waitProbeChainBridgeSession(runtime.stopCh, session)
		runtime.clearDownstreamSession(session)
	} else {
		acceptProbeChainBridgeStreams(runtime, session, "inbound-bridge")
	}
	_ = session.Close()
	<-done
}

func (rt *probeChainRuntime) closeRuntimeResources() {
	if rt == nil {
		return
	}
	rt.bridgeMu.Lock()
	downstreamSession := rt.downstreamSession
	rt.downstreamSession = nil
	rt.bridgeMu.Unlock()
	rt.forwardMu.Lock()
	tcpForwards := rt.tcpForwards
	udpForwards := rt.udpForwards
	rt.tcpForwards = nil
	rt.udpForwards = nil
	rt.forwardMu.Unlock()
	if downstreamSession != nil {
		_ = downstreamSession.Close()
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
	if rt.httpsServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		_ = rt.httpsServer.Shutdown(ctx)
		cancel()
	}
	if rt.http3Server != nil {
		_ = rt.http3Server.Close()
	}
}

func (rt *probeChainRuntime) setDownstreamSession(session *yamux.Session) {
	if rt == nil || session == nil {
		return
	}
	rt.bridgeMu.Lock()
	old := rt.downstreamSession
	rt.downstreamSession = session
	rt.bridgeMu.Unlock()
	if old != nil && old != session {
		_ = old.Close()
	}
}

func (rt *probeChainRuntime) clearDownstreamSession(target *yamux.Session) {
	if rt == nil || target == nil {
		return
	}
	rt.bridgeMu.Lock()
	if rt.downstreamSession == target {
		rt.downstreamSession = nil
	}
	rt.bridgeMu.Unlock()
}

func (rt *probeChainRuntime) getDownstreamSession() *yamux.Session {
	if rt == nil {
		return nil
	}
	rt.bridgeMu.Lock()
	session := rt.downstreamSession
	rt.bridgeMu.Unlock()
	return session
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
	if err := persistProbeChainRuntimesToCache(); err != nil {
		log.Printf("warning: persist probe chain runtime cache failed: %v", err)
	}
	log.Printf("probe chain runtime stopped: chain=%s reason=%s", target, strings.TrimSpace(reason))
	return true
}

func handleProbeChainConn(runtime *probeChainRuntime, conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	if _, hintErr := readProbeChainSourceIPHint(reader); hintErr != nil {
		log.Printf("probe chain source hint parse failed: chain=%s err=%v", runtime.cfg.chainID, hintErr)
		return
	}

	_ = conn.SetDeadline(time.Time{})
	if runtime.cfg.nextAuthMode == "proxy" {
		if err := handleProbeChainProxyConn(runtime, conn, reader); err != nil {
			log.Printf("probe chain proxy failed: chain=%s role=%s remote=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String(), err)
		}
		return
	}

	nextHop, err := openProbeChainNextHop(runtime)
	if err != nil {
		log.Printf("probe chain open downstream stream failed: chain=%s role=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, err)
		return
	}
	defer func() {
		if nextHop != nil && nextHop.CloseFn != nil {
			_ = nextHop.CloseFn()
		}
	}()
	nextReader := bufio.NewReader(nextHop.Reader)

	relayErrCh := make(chan error, 2)
	go func() {
		_, copyErr := io.Copy(nextHop.Writer, reader)
		closeProbeChainWriter(nextHop.Writer)
		relayErrCh <- copyErr
	}()
	go func() {
		_, copyErr := io.Copy(conn, nextReader)
		relayErrCh <- copyErr
	}()
	<-relayErrCh
}

func openProbeChainNextHop(runtime *probeChainRuntime) (*probeChainNextHop, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	if runtime.cfg.nextAuthMode == "proxy" {
		return nil, errors.New("next hop is proxy mode")
	}
	stream, err := openProbeChainDownstreamStream(runtime, probeChainDownstreamOpenTimeout)
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

func openProbeChainDownstreamStream(runtime *probeChainRuntime, timeout time.Duration) (net.Conn, error) {
	if runtime == nil {
		return nil, errors.New("runtime is nil")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		session := runtime.getDownstreamSession()
		if session != nil && !session.IsClosed() {
			stream, openErr := session.Open()
			if openErr == nil {
				return stream, nil
			}
			if session.IsClosed() {
				runtime.clearDownstreamSession(session)
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
	return nil, fmt.Errorf("downstream bridge is unavailable")
}

func resolveProbeChainOutboundLinkLayer(cfg probeChainRuntimeConfig) string {
	return normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(cfg.nextLinkLayer), strings.TrimSpace(cfg.linkLayer), "http"))
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

func openProbeChainRelayNetConn(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
	relayDialHost, relayHostHeader, err := resolveProbeChainDialIPHost(relayHost)
	if err != nil {
		return nil, err
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
		transport := &http.Transport{
			Proxy:             http.ProxyFromEnvironment,
			ForceAttemptHTTP2: true,
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
		transport := &http.Transport{
			Proxy:             http.ProxyFromEnvironment,
			ForceAttemptHTTP2: false,
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

	response, err := client.Do(request)
	if err != nil {
		cancel()
		_ = bodyWriter.Close()
		_ = closeTransport()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		cancel()
		_ = bodyWriter.Close()
		_ = closeTransport()
		return nil, fmt.Errorf("probe relay failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	return &probeChainRelayNetConn{
		reader: response.Body,
		writer: bodyWriter,
		closeFn: func() error {
			cancel()
			_ = bodyWriter.Close()
			_ = response.Body.Close()
			_ = closeTransport()
			return nil
		},
	}, nil
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
	return c.reader.Read(payload)
}

func (c *probeChainRelayNetConn) Write(payload []byte) (int, error) {
	if c == nil || c.writer == nil {
		return 0, io.ErrClosedPipe
	}
	return c.writer.Write(payload)
}

func (c *probeChainRelayNetConn) Close() error {
	if c == nil {
		return nil
	}
	var closeErr error
	c.closeOnce.Do(func() {
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

func resolveProbeChainDialIPHost(rawHost string) (dialHost string, hostHeader string, err error) {
	cleanHost := strings.TrimSpace(strings.Trim(rawHost, "[]"))
	if cleanHost == "" {
		return "", "", fmt.Errorf("empty relay host")
	}
	if parsed := net.ParseIP(cleanHost); parsed != nil {
		return parsed.String(), cleanHost, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, resolveErr := net.DefaultResolver.LookupIP(ctx, "ip", cleanHost)
	if resolveErr != nil {
		return "", "", fmt.Errorf("resolve relay host failed: %w", resolveErr)
	}
	ip := selectProbeChainPreferredDialIP(ips)
	if ip == nil {
		return "", "", fmt.Errorf("resolve relay host failed: no ip")
	}
	return ip.String(), cleanHost, nil
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
	_ = layer
	cleanDialHost := strings.TrimSpace(strings.Trim(dialHost, "[]"))
	cleanHostHeader := strings.TrimSpace(strings.Trim(hostHeader, "[]"))
	if parsed := net.ParseIP(cleanDialHost); parsed != nil {
		return parsed.String()
	}
	if parsed := net.ParseIP(cleanHostHeader); parsed != nil {
		return parsed.String()
	}
	if cleanDialHost != "" {
		return cleanDialHost
	}
	if cleanHostHeader != "" {
		return cleanHostHeader
	}
	return probeChainTLSServerName
}

func handleProbeChainRelayHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chainID := resolveProbeChainIDFromRequest(r)
	if chainID == "" {
		http.Error(w, "chain_id is required", http.StatusBadRequest)
		return
	}

	runtime := getProbeChainRuntime(chainID)
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	if err := verifyProbeChainRelayRequestAuth(runtime, r, chainID); err != nil {
		http.Error(w, "codex auth failed", http.StatusUnauthorized)
		return
	}
	bridgeRole := normalizeProbeChainBridgeRole(r.Header.Get(probeChainCodexRelayRoleHeader))
	handleProbeChainBridgeRelayHTTP(runtime, bridgeRole, w, r)
}

type probeChainHTTPResponseStreamWriter struct {
	writer  http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
}

func (w *probeChainHTTPResponseStreamWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.writer.Write(payload)
	if err == nil && w.flusher != nil {
		w.flusher.Flush()
	}
	return n, err
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
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	return cfg
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

	network := strings.ToLower(strings.TrimSpace(req.Network))
	if network == "" {
		network = "tcp"
	}
	target := strings.TrimSpace(req.Address)
	if target == "" {
		_ = writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: false, Error: "missing address"})
		return
	}

	var proxyErr error
	switch network {
	case "tcp":
		proxyErr = handleProbeChainTunnelTCPStream(stream, target)
	case "udp":
		proxyErr = handleProbeChainTunnelUDPStream(stream, target)
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

func writeProbeChainTunnelOpenResponse(stream net.Conn, resp probeChainTunnelOpenResponse) error {
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := json.NewEncoder(stream).Encode(resp)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}

func handleProbeChainTunnelTCPStream(stream net.Conn, target string) error {
	dialer := &net.Dialer{Timeout: probeChainPortForwardDialTimeout}
	remoteConn, err := dialer.Dial("tcp", target)
	if err != nil {
		_ = writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	defer remoteConn.Close()

	if err := writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: true}); err != nil {
		return err
	}

	errCh := make(chan error, 2)
	go func() {
		_, copyErr := io.Copy(remoteConn, stream)
		errCh <- copyErr
	}()
	go func() {
		_, copyErr := io.Copy(stream, remoteConn)
		errCh <- copyErr
	}()

	copyErr := <-errCh
	if copyErr == nil || errors.Is(copyErr, io.EOF) || errors.Is(copyErr, net.ErrClosed) {
		return nil
	}
	return copyErr
}

func handleProbeChainTunnelUDPStream(stream net.Conn, target string) error {
	udpAddr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		_ = writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	udpConn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		_ = writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	defer udpConn.Close()

	if err := writeProbeChainTunnelOpenResponse(stream, probeChainTunnelOpenResponse{OK: true}); err != nil {
		return err
	}

	reader := bufio.NewReader(stream)
	errCh := make(chan error, 2)
	go func() {
		for {
			payload, readErr := readProbeChainFramedPacket(reader)
			if readErr != nil {
				errCh <- readErr
				return
			}
			if len(payload) == 0 {
				continue
			}
			if _, writeErr := udpConn.Write(payload); writeErr != nil {
				errCh <- writeErr
				return
			}
		}
	}()

	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, readErr := udpConn.Read(buf)
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

func relayProbeChainBidirectional(leftConn net.Conn, leftReader io.Reader, rightConn net.Conn, rightReader io.Reader) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(rightConn, leftReader)
		closeProbeChainConnWrite(rightConn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(leftConn, rightReader)
		closeProbeChainConnWrite(leftConn)
		done <- struct{}{}
	}()
	<-done
}

func closeProbeChainConnWrite(conn net.Conn) {
	if closer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
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
	lengthBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, lengthBytes); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(lengthBytes))
	if length <= 0 {
		return nil, errors.New("invalid framed packet length")
	}
	packet := make([]byte, length)
	if _, err := io.ReadFull(reader, packet); err != nil {
		return nil, err
	}
	return packet, nil
}

func writeProbeChainFramedPacket(writer io.Writer, payload []byte) error {
	size := len(payload)
	if size <= 0 || size > 65535 {
		return errors.New("invalid framed packet payload")
	}
	header := []byte{0x00, 0x00}
	binary.BigEndian.PutUint16(header, uint16(size))
	if _, err := writer.Write(header); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
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
		return fmt.Errorf("secret auth failed")
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

func restoreProbeChainRuntimesFromCache(identity nodeIdentity, controllerBaseURL string) {
	items, err := loadProbeChainRuntimeCacheItems()
	if err != nil {
		log.Printf("warning: load probe chain runtime cache failed: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}
	for _, item := range items {
		cfg, err := buildProbeChainRuntimeConfigFromControl(probeControlMessage{
			ChainID:         item.ChainID,
			Name:            item.Name,
			UserID:          item.UserID,
			UserPublicKey:   item.UserPublicKey,
			LinkSecret:      item.LinkSecret,
			Role:            item.Role,
			ListenHost:      item.ListenHost,
			ListenPort:      item.ListenPort,
			LinkLayer:       item.LinkLayer,
			NextLinkLayer:   item.NextLinkLayer,
			NextDialMode:    item.NextDialMode,
			NextHost:        item.NextHost,
			NextPort:        item.NextPort,
			PrevHost:        item.PrevHost,
			PrevPort:        item.PrevPort,
			PrevLinkLayer:   item.PrevLinkLayer,
			PrevDialMode:    item.PrevDialMode,
			PortForwards:    item.PortForwards,
			RequireUserAuth: item.RequireUserAuth,
			NextAuthMode:    item.NextAuthMode,
		})
		if err != nil {
			log.Printf("warning: skip invalid cached chain runtime: chain=%s err=%v", strings.TrimSpace(item.ChainID), err)
			continue
		}
		cfg.identity = identity
		cfg.controllerURL = resolveProbeControllerBaseURL(strings.TrimSpace(controllerBaseURL), "")
		if _, err := startProbeChainRuntime(cfg); err != nil {
			log.Printf("warning: restore cached chain runtime failed: chain=%s err=%v", cfg.chainID, err)
			continue
		}
		log.Printf("restored cached chain runtime: chain=%s role=%s listen=%s:%d", cfg.chainID, cfg.role, cfg.listenHost, cfg.listenPort)
	}
}

func loadProbeChainRuntimeCacheItems() ([]probeChainRuntimeCacheItem, error) {
	cachePath, err := resolveProbeChainRuntimeCachePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []probeChainRuntimeCacheItem{}, nil
		}
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return []probeChainRuntimeCacheItem{}, nil
	}
	var payload probeChainRuntimeCacheFile
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, err
	}
	return payload.Items, nil
}

func persistProbeChainRuntimesToCache() error {
	cachePath, err := resolveProbeChainRuntimeCachePath()
	if err != nil {
		return err
	}

	probeChainRuntimeState.mu.Lock()
	items := make([]probeChainRuntimeCacheItem, 0, len(probeChainRuntimeState.runtimes))
	for _, rt := range probeChainRuntimeState.runtimes {
		if rt == nil {
			continue
		}
		cfg := rt.cfg
		items = append(items, probeChainRuntimeCacheItem{
			ChainID:         cfg.chainID,
			Name:            cfg.name,
			UserID:          cfg.userID,
			UserPublicKey:   cfg.rawPublicKey,
			LinkSecret:      cfg.secret,
			Role:            cfg.role,
			ListenHost:      cfg.listenHost,
			ListenPort:      cfg.listenPort,
			LinkLayer:       cfg.linkLayer,
			NextLinkLayer:   cfg.nextLinkLayer,
			NextDialMode:    cfg.nextDialMode,
			NextHost:        cfg.nextHost,
			NextPort:        cfg.nextPort,
			PrevHost:        cfg.prevHost,
			PrevPort:        cfg.prevPort,
			PrevLinkLayer:   cfg.prevLinkLayer,
			PrevDialMode:    cfg.prevDialMode,
			PortForwards:    buildProbeChainPortForwardMessagesFromRuntime(cfg.portForwards),
			RequireUserAuth: cfg.requireUserAuth,
			NextAuthMode:    cfg.nextAuthMode,
		})
	}
	probeChainRuntimeState.mu.Unlock()

	payload := probeChainRuntimeCacheFile{Items: items}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(cachePath, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	return nil
}

func resolveProbeChainRuntimeCachePath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, probeChainRuntimeCacheFileName), nil
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
