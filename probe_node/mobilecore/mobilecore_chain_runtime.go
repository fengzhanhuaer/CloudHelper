package mobilecore

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

const (
	mobileChainRelayPath = "/api/node/chain/relay"

	mobileChainHeaderLegacyChainID = "X-CH-Chain-ID"
	mobileChainHeaderChainID       = "X-Codex-Chain-Id"
	mobileChainHeaderAuthMode      = "X-Codex-Auth-Mode"
	mobileChainHeaderMAC           = "X-Codex-Mac"
	mobileChainHeaderAuthTicket    = "X-Codex-User-Auth-Ticket"
	mobileChainHeaderVersion       = "X-Codex-Api-Version"
	mobileChainHeaderRelayMode     = "X-Codex-Relay-Mode"
	mobileChainHeaderRelayRole     = "X-Codex-Relay-Role"
	mobileChainHeaderSpeedBytes    = "X-Codex-Speed-Bytes"
	mobileChainHeaderAuthTimestamp = "X-Codex-Auth-Timestamp"
	mobileChainAuthPacketVersion   = "2025-03-22"

	mobileChainRelayModeBridge    = "bridge"
	mobileChainRelayModeStream    = "stream"
	mobileChainRelayModeSpeedTest = "speed_test"
	mobileChainRelayModePingPong  = "ping_pong"
	mobileChainRelayModePrepare   = "prepare"

	mobileChainBridgeRoleToNext = "to_next"
	mobileChainBridgeRoleToPrev = "to_prev"

	mobileChainRoleEntry     = "entry"
	mobileChainRoleRelay     = "relay"
	mobileChainRoleExit      = "exit"
	mobileChainRoleEntryExit = "entry_exit"

	mobileChainDialForward = "forward"
	mobileChainDialReverse = "reverse"
	mobileChainDialNone    = "none"

	mobileChainNetworkTCP  = "tcp"
	mobileChainNetworkUDP  = "udp"
	mobileChainNetworkBoth = "both"

	mobileChainEntrySideEntry = "chain_entry"
	mobileChainEntrySideExit  = "chain_exit"

	mobileChainOpenTimeout            = 15 * time.Second
	mobileChainRelayHeaderTimeout     = 5 * time.Second
	mobileChainBridgeRetryMin         = time.Second
	mobileChainBridgeRetryMax         = 30 * time.Second
	mobileChainPortForwardDialTimeout = 12 * time.Second
	mobileChainResponseTimeout        = 10 * time.Second
	mobileChainUDPIdleTTL             = 90 * time.Second
	mobileChainUDPGCInterval          = 15 * time.Second
	mobileChainCopyBufferBytes        = 1024 * 1024
	mobileChainFrameMaxPayload        = 64 * 1024
	mobileChainWSBufferBytes          = 512 * 1024
	mobileChainSourceIPHintPrefix     = "CHSRCIP "
	mobileChainBridgeControlPrefix    = "CHBRIDGE "

	mobileChainQUICInitialStreamWindow = 128 * 1024 * 1024
	mobileChainQUICMaxStreamWindow     = 512 * 1024 * 1024
	mobileChainQUICInitialConnWindow   = 512 * 1024 * 1024
	mobileChainQUICMaxConnWindow       = 1024 * 1024 * 1024
)

var mobileChainAuthTicketNow = time.Now

type mobileNodeIdentity struct {
	NodeID string
	Secret string
}

type mobileChainPortForwardConfig struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	EntrySide  string `json:"entry_side"`
	ListenHost string `json:"listen_host"`
	ListenPort int    `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Network    string `json:"network"`
	Enabled    bool   `json:"enabled"`
}

type mobileChainRuntimeConfig struct {
	ChainID                 string
	Name                    string
	UserID                  string
	RawPublicKey            string
	UserPublicKey           ed25519.PublicKey
	Secret                  string
	AuthTicket              string
	Role                    string
	ListenHost              string
	ListenPort              int
	LinkLayer               string
	NextLinkLayer           string
	NextDialMode            string
	NextHost                string
	NextPort                int
	NextPreserveRelayDomain bool
	PrevHost                string
	PrevPort                int
	PrevLinkLayer           string
	PrevDialMode            string
	PrevPreserveRelayDomain bool
	RequireUserAuth         bool
	NextAuthMode            string
	PortForwards            []mobileChainPortForwardConfig
	Identity                mobileNodeIdentity
}

type mobileChainRuntime struct {
	cfg                mobileChainRuntimeConfig
	relayListenAddr    string
	downstreamSessions map[string]*mobileChainBridgeSession
	upstreamSessions   map[string]*mobileChainBridgeSession
	bridgeMu           sync.Mutex
	bridgeSeq          uint64
	forwardMu          sync.Mutex
	tcpForwards        []net.Listener
	udpForwards        []net.PacketConn
	stopCh             chan struct{}
}

type mobileChainBridgeSession struct {
	ID      string
	Session *yamux.Session
}

type mobileChainSharedRelayServer struct {
	listenAddr    string
	chainIDs      map[string]struct{}
	refCount      int
	httpsServer   *http.Server
	http3Server   *http3.Server
	tcpListener   net.Listener
	udpPacketConn net.PacketConn
}

type mobileChainBridgeDialTarget struct {
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

type mobileChainTunnelOpenRequest struct {
	Type          string                          `json:"type"`
	Network       string                          `json:"network,omitempty"`
	Address       string                          `json:"address,omitempty"`
	FlowID        string                          `json:"flow_id,omitempty"`
	AssociationV2 *mobileChainAssociationV2Config `json:"association_v2,omitempty"`
	PingBytes     int64                           `json:"ping_bytes,omitempty"`
}

type mobileChainAssociationV2Config struct {
	Version         int    `json:"version"`
	Transport       string `json:"transport,omitempty"`
	RouteTarget     string `json:"route_target,omitempty"`
	NATMode         string `json:"nat_mode,omitempty"`
	TTLProfile      string `json:"ttl_profile,omitempty"`
	IdleTimeoutMS   int64  `json:"idle_timeout_ms,omitempty"`
	GCIntervalMS    int64  `json:"gc_interval_ms,omitempty"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms,omitempty"`
	AssocKeyV2      string `json:"assoc_key_v2,omitempty"`
	FlowID          string `json:"flow_id,omitempty"`
}

type mobileChainTunnelOpenResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type mobileChainBridgeControlRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
}

type mobileChainBridgeControlResponse struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type mobileChainNetAddr struct{ label string }

func (a mobileChainNetAddr) Network() string { return "mobile-chain" }
func (a mobileChainNetAddr) String() string  { return a.label }

type mobileChainH3Conn struct {
	stream interface {
		io.Reader
		io.Writer
		Close() error
		SetReadDeadline(time.Time) error
		SetWriteDeadline(time.Time) error
	}
	local   net.Addr
	remote  net.Addr
	closeFn func() error
}

func (c *mobileChainH3Conn) Read(p []byte) (int, error)  { return c.stream.Read(p) }
func (c *mobileChainH3Conn) Write(p []byte) (int, error) { return c.stream.Write(p) }
func (c *mobileChainH3Conn) Close() error {
	if c.closeFn != nil {
		return c.closeFn()
	}
	return c.stream.Close()
}
func (c *mobileChainH3Conn) LocalAddr() net.Addr  { return c.local }
func (c *mobileChainH3Conn) RemoteAddr() net.Addr { return c.remote }
func (c *mobileChainH3Conn) SetDeadline(t time.Time) error {
	if err := c.stream.SetReadDeadline(t); err != nil {
		return err
	}
	return c.stream.SetWriteDeadline(t)
}
func (c *mobileChainH3Conn) SetReadDeadline(t time.Time) error  { return c.stream.SetReadDeadline(t) }
func (c *mobileChainH3Conn) SetWriteDeadline(t time.Time) error { return c.stream.SetWriteDeadline(t) }

var mobileChainRuntimeState = struct {
	mu       sync.Mutex
	runtimes map[string]*mobileChainRuntime
}{runtimes: map[string]*mobileChainRuntime{}}

var mobileChainSharedRelayState = struct {
	mu      sync.Mutex
	servers map[string]*mobileChainSharedRelayServer
}{servers: map[string]*mobileChainSharedRelayServer{}}

var mobileChainCopyBufferPool = sync.Pool{New: func() any { return make([]byte, mobileChainCopyBufferBytes) }}

var mobileChainOpenBridgeStreamTimeout = mobileChainOpenTimeout

func runMobileChainLinkControl(cmd chainLinkControlMessage, identity mobileNodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	action := normalizeMobileChainAction(cmd.Action)
	if action == "" {
		action = "apply"
	}
	result := chainLinkControlResult{
		Type:      "chain_link_control_result",
		RequestID: strings.TrimSpace(cmd.RequestID),
		NodeID:    strings.TrimSpace(identity.NodeID),
		OK:        false,
		Action:    action,
		ChainID:   strings.TrimSpace(cmd.ChainID),
		Role:      strings.TrimSpace(cmd.Role),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	switch action {
	case "apply", "restart":
		cfg, err := buildMobileChainRuntimeConfig(cmd)
		if err != nil {
			result.Error = err.Error()
			sendChainLinkControlResult(stream, encoder, writeMu, result)
			return
		}
		cfg.Identity = identity
		rt, err := startMobileChainRuntime(cfg)
		if err != nil {
			result.Error = err.Error()
			sendChainLinkControlResult(stream, encoder, writeMu, result)
			return
		}
		result.OK = true
		result.Role = rt.cfg.Role
		result.Message = "android chain runtime started: listen=" + net.JoinHostPort(rt.cfg.ListenHost, strconv.Itoa(rt.cfg.ListenPort))
	case "remove":
		result.OK = true
		if stopMobileChainRuntime(cmd.ChainID, "remote remove command") {
			result.Message = "android chain runtime removed"
		} else {
			result.Message = "android chain runtime not found"
		}
	default:
		result.Error = "unsupported action"
	}
	sendChainLinkControlResult(stream, encoder, writeMu, result)
}

func buildMobileChainRuntimeConfig(cmd chainLinkControlMessage) (mobileChainRuntimeConfig, error) {
	chainID := strings.TrimSpace(cmd.ChainID)
	if chainID == "" {
		return mobileChainRuntimeConfig{}, errors.New("chain_id is required")
	}
	listenPort := normalizeMobileChainPort(cmd.ListenPort)
	if listenPort <= 0 {
		listenPort = normalizeMobileChainPort(cmd.InternalPort)
	}
	if listenPort <= 0 {
		return mobileChainRuntimeConfig{}, errors.New("listen_port must be between 1 and 65535")
	}
	secret := strings.TrimSpace(cmd.LinkSecret)
	if secret == "" {
		return mobileChainRuntimeConfig{}, errors.New("link_secret is required")
	}
	role := normalizeMobileChainRole(cmd.Role)
	if role == "" {
		role = mobileChainRoleRelay
	}
	nextAuthMode := normalizeMobileChainAuthMode(cmd.NextAuthMode)
	nextHost := strings.TrimSpace(cmd.NextHost)
	nextPort := normalizeMobileChainPort(cmd.NextPort)
	nextDialMode := normalizeMobileChainDialMode(cmd.NextDialMode)
	if nextAuthMode != "proxy" {
		if nextHost == "" || nextPort <= 0 {
			return mobileChainRuntimeConfig{}, errors.New("next_host and next_port are required")
		}
		if nextDialMode == mobileChainDialNone {
			nextDialMode = mobileChainDialForward
		}
	} else {
		nextDialMode = mobileChainDialNone
	}
	prevHost := strings.TrimSpace(cmd.PrevHost)
	prevPort := normalizeMobileChainPort(cmd.PrevPort)
	prevDialMode := normalizeMobileChainDialMode(cmd.PrevDialMode)
	if prevHost == "" || prevPort <= 0 {
		prevDialMode = mobileChainDialNone
	}
	forwards, err := normalizeMobileChainPortForwards(cmd.PortForwards)
	if err != nil {
		return mobileChainRuntimeConfig{}, err
	}
	preserveDomain := isMobileChainControlCFEntry(cmd)
	cfg := mobileChainRuntimeConfig{
		ChainID:                 chainID,
		Name:                    strings.TrimSpace(cmd.Name),
		UserID:                  strings.TrimSpace(cmd.UserID),
		RawPublicKey:            strings.TrimSpace(cmd.UserPublicKey),
		Secret:                  secret,
		AuthTicket:              strings.TrimSpace(cmd.AuthTicket),
		Role:                    role,
		ListenHost:              normalizeMobileChainListenHost(cmd.ListenHost),
		ListenPort:              listenPort,
		LinkLayer:               normalizeMobileChainLinkLayer(cmd.LinkLayer),
		NextLinkLayer:           normalizeMobileChainLinkLayer(firstMobileChainNonEmpty(cmd.NextLinkLayer, cmd.LinkLayer)),
		NextDialMode:            nextDialMode,
		NextHost:                nextHost,
		NextPort:                nextPort,
		NextPreserveRelayDomain: preserveDomain,
		PrevHost:                prevHost,
		PrevPort:                prevPort,
		PrevLinkLayer:           normalizeMobileChainLinkLayer(firstMobileChainNonEmpty(cmd.PrevLinkLayer, cmd.LinkLayer)),
		PrevDialMode:            prevDialMode,
		PrevPreserveRelayDomain: preserveDomain,
		RequireUserAuth:         cmd.RequireUserAuth,
		NextAuthMode:            nextAuthMode,
		PortForwards:            forwards,
	}
	if cfg.RequireUserAuth {
		pub, err := parseMobileChainUserPublicKey(cfg.RawPublicKey)
		if err != nil {
			return mobileChainRuntimeConfig{}, fmt.Errorf("parse user_public_key failed: %w", err)
		}
		cfg.UserPublicKey = pub
	}
	return cfg, nil
}

func startMobileChainRuntime(cfg mobileChainRuntimeConfig) (*mobileChainRuntime, error) {
	_ = stopMobileChainRuntime(cfg.ChainID, "restart before apply")
	rt := &mobileChainRuntime{
		cfg:                cfg,
		downstreamSessions: map[string]*mobileChainBridgeSession{},
		upstreamSessions:   map[string]*mobileChainBridgeSession{},
		stopCh:             make(chan struct{}),
	}
	if err := startMobileChainPublicRelayServer(rt); err != nil {
		close(rt.stopCh)
		return nil, err
	}
	mobileChainRuntimeState.mu.Lock()
	mobileChainRuntimeState.runtimes[cfg.ChainID] = rt
	mobileChainRuntimeState.mu.Unlock()
	startMobileChainBridgeWorkers(rt)
	startMobileChainPortForwardWorkers(rt)
	androidLogStore.add("chain", "normal", "android probe chain server started: chain="+cfg.ChainID+" role="+cfg.Role)
	return rt, nil
}

func stopMobileChainRuntime(chainID string, reason string) bool {
	id := strings.TrimSpace(chainID)
	if id == "" {
		return false
	}
	mobileChainRuntimeState.mu.Lock()
	rt := mobileChainRuntimeState.runtimes[id]
	if rt != nil {
		delete(mobileChainRuntimeState.runtimes, id)
	}
	mobileChainRuntimeState.mu.Unlock()
	if rt == nil {
		return false
	}
	close(rt.stopCh)
	rt.close()
	releaseMobileChainSharedRelayServer(rt)
	androidLogStore.add("chain", "normal", "android probe chain server stopped: chain="+id+" reason="+strings.TrimSpace(reason))
	return true
}

func stopAllMobileChainRuntimes(reason string) int {
	mobileChainRuntimeState.mu.Lock()
	ids := make([]string, 0, len(mobileChainRuntimeState.runtimes))
	for id := range mobileChainRuntimeState.runtimes {
		ids = append(ids, id)
	}
	mobileChainRuntimeState.mu.Unlock()
	stopped := 0
	for _, id := range ids {
		if stopMobileChainRuntime(id, reason) {
			stopped++
		}
	}
	return stopped
}

func applyMobileChainRuntimesFromConfigDir(configDir string, identity mobileNodeIdentity) (int, error) {
	dir := strings.TrimSpace(configDir)
	if dir == "" {
		return 0, errors.New("config dir is required")
	}
	items, err := loadMobileChainServerItemsFromConfigDir(dir)
	if err != nil {
		return 0, err
	}
	applied := 0
	for _, item := range items {
		if applyMobileChainServerItem(identity, item) {
			applied++
		}
	}
	return applied, nil
}

func loadMobileChainServerItemsFromConfigDir(configDir string) ([]linkChainServerItem, error) {
	var out []linkChainServerItem
	appendCache := func(path string) error {
		items, err := loadMobileChainServerItemsFromCache(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		out = append(out, items...)
		return nil
	}
	if err := appendCache(filepath.Join(configDir, "probe_link_chain_config.json")); err != nil {
		return nil, err
	}
	if err := appendCache(filepath.Join(configDir, "probe_link_port_forward_chain_config.json")); err != nil {
		return nil, err
	}
	groupedPath := filepath.Join(configDir, "probe_link_config_grouped.json")
	raw, err := os.ReadFile(groupedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return dedupeMobileChainServerItems(out), nil
		}
		return nil, err
	}
	var grouped struct {
		SelfChains        []linkChainServerItem `json:"self_chains"`
		PortForwardChains []linkChainServerItem `json:"port_forward_chains"`
	}
	if err := json.Unmarshal(raw, &grouped); err != nil {
		return nil, err
	}
	out = append(out, grouped.SelfChains...)
	out = append(out, grouped.PortForwardChains...)
	return dedupeMobileChainServerItems(out), nil
}

func loadMobileChainServerItemsFromCache(path string) ([]linkChainServerItem, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cache struct {
		Items *[]linkChainServerItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &cache); err == nil && cache.Items != nil {
		return *cache.Items, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil {
		if _, ok := object["items"]; ok {
			return []linkChainServerItem{}, nil
		}
	}
	var items []linkChainServerItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func dedupeMobileChainServerItems(items []linkChainServerItem) []linkChainServerItem {
	out := make([]linkChainServerItem, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		id := strings.TrimSpace(item.ChainID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, item)
	}
	return out
}

func applyMobileChainServerItem(identity mobileNodeIdentity, item linkChainServerItem) bool {
	nodeID := strings.TrimSpace(identity.NodeID)
	role := resolveMobileChainNodeRole(item, nodeID)
	if role == "" {
		return false
	}
	hop := findMobileChainHopForNode(item, nodeID)
	if hop.ListenPort <= 0 {
		androidLogStore.add("chain", "warn", "android chain restore skipped: chain="+strings.TrimSpace(item.ChainID)+" reason=missing_listen_port")
		return false
	}
	nextHost, nextPort, nextLayer, nextDialMode, nextAuthMode := resolveMobileChainNextHop(item, nodeID, role)
	prevHost, prevPort, prevLayer, prevDialMode := resolveMobileChainPrevHop(item, nodeID, role)
	forwards := make([]mobileChainPortForwardConfig, 0, len(item.PortForwards))
	for _, raw := range item.PortForwards {
		var cfg mobileChainPortForwardConfig
		if err := json.Unmarshal(raw, &cfg); err == nil {
			forwards = append(forwards, cfg)
		}
	}
	cmd := chainLinkControlMessage{
		ChainID:         strings.TrimSpace(item.ChainID),
		ChainType:       strings.TrimSpace(item.ChainType),
		ClientEntryID:   strings.TrimSpace(item.ClientEntryID),
		ClientEntryType: strings.TrimSpace(item.ClientEntryType),
		Name:            strings.TrimSpace(item.Name),
		UserID:          "",
		UserPublicKey:   "",
		LinkSecret:      strings.TrimSpace(item.Secret),
		AuthTicket:      strings.TrimSpace(item.AuthTicket),
		Role:            role,
		ListenHost:      normalizeMobileChainListenHost(hop.ListenHost),
		ListenPort:      hop.ListenPort,
		LinkLayer:       normalizeMobileChainLinkLayer(firstMobileChainNonEmpty(hop.LinkLayer, item.LinkLayer)),
		NextLinkLayer:   nextLayer,
		NextDialMode:    nextDialMode,
		NextHost:        nextHost,
		NextPort:        nextPort,
		PrevHost:        prevHost,
		PrevPort:        prevPort,
		PrevLinkLayer:   prevLayer,
		PrevDialMode:    prevDialMode,
		PortForwards:    forwards,
		RequireUserAuth: true,
		NextAuthMode:    nextAuthMode,
	}
	cmd.UserID = strings.TrimSpace(item.UserID)
	cmd.UserPublicKey = strings.TrimSpace(item.UserPublicKey)
	cfg, err := buildMobileChainRuntimeConfig(cmd)
	if err != nil {
		androidLogStore.add("chain", "warn", "android chain restore build failed: chain="+cmd.ChainID+" err="+err.Error())
		return false
	}
	cfg.Identity = identity
	if _, err := startMobileChainRuntime(cfg); err != nil {
		androidLogStore.add("chain", "warn", "android chain restore start failed: chain="+cmd.ChainID+" err="+err.Error())
		return false
	}
	return true
}

func getMobileChainRuntime(chainID string) *mobileChainRuntime {
	mobileChainRuntimeState.mu.Lock()
	defer mobileChainRuntimeState.mu.Unlock()
	return mobileChainRuntimeState.runtimes[strings.TrimSpace(chainID)]
}

func (rt *mobileChainRuntime) close() {
	rt.bridgeMu.Lock()
	for _, item := range rt.downstreamSessions {
		if item != nil && item.Session != nil {
			_ = item.Session.Close()
		}
	}
	for _, item := range rt.upstreamSessions {
		if item != nil && item.Session != nil {
			_ = item.Session.Close()
		}
	}
	rt.downstreamSessions = map[string]*mobileChainBridgeSession{}
	rt.upstreamSessions = map[string]*mobileChainBridgeSession{}
	rt.bridgeMu.Unlock()

	rt.forwardMu.Lock()
	for _, ln := range rt.tcpForwards {
		_ = ln.Close()
	}
	for _, pc := range rt.udpForwards {
		_ = pc.Close()
	}
	rt.tcpForwards = nil
	rt.udpForwards = nil
	rt.forwardMu.Unlock()
}

func startMobileChainPublicRelayServer(rt *mobileChainRuntime) error {
	listenAddr := net.JoinHostPort(rt.cfg.ListenHost, strconv.Itoa(rt.cfg.ListenPort))
	if err := acquireMobileChainSharedRelayServer(rt, listenAddr); err != nil {
		return err
	}
	rt.relayListenAddr = listenAddr
	return nil
}

func acquireMobileChainSharedRelayServer(rt *mobileChainRuntime, listenAddr string) error {
	chainID := strings.TrimSpace(rt.cfg.ChainID)
	mobileChainSharedRelayState.mu.Lock()
	if shared := mobileChainSharedRelayState.servers[listenAddr]; shared != nil {
		shared.chainIDs[chainID] = struct{}{}
		shared.refCount++
		mobileChainSharedRelayState.mu.Unlock()
		return nil
	}
	mobileChainSharedRelayState.mu.Unlock()

	shared, err := startMobileChainSharedRelayServer(listenAddr)
	if err != nil {
		return err
	}
	shared.chainIDs[chainID] = struct{}{}
	shared.refCount = 1
	mobileChainSharedRelayState.mu.Lock()
	if existing := mobileChainSharedRelayState.servers[listenAddr]; existing != nil {
		mobileChainSharedRelayState.mu.Unlock()
		closeMobileChainSharedRelayServer(shared)
		return acquireMobileChainSharedRelayServer(rt, listenAddr)
	}
	mobileChainSharedRelayState.servers[listenAddr] = shared
	mobileChainSharedRelayState.mu.Unlock()
	return nil
}

func startMobileChainSharedRelayServer(listenAddr string) (*mobileChainSharedRelayServer, error) {
	cert, err := generateMobileChainCertificate()
	if err != nil {
		return nil, err
	}
	handler := http.NewServeMux()
	handler.HandleFunc(mobileChainRelayPath, handleMobileChainRelayDispatch)
	tcpListener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen chain relay tcp failed: %w", err)
	}
	tcpListener = &mobileChainTCPListener{Listener: tcpListener}
	udpPacketConn, err := net.ListenPacket("udp", listenAddr)
	if err != nil {
		_ = tcpListener.Close()
		return nil, fmt.Errorf("listen chain relay udp failed: %w", err)
	}
	shared := &mobileChainSharedRelayServer{listenAddr: listenAddr, chainIDs: map[string]struct{}{}, tcpListener: tcpListener, udpPacketConn: udpPacketConn}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	shared.httpsServer = &http.Server{Addr: listenAddr, Handler: handler, ReadHeaderTimeout: mobileChainRelayHeaderTimeout}
	go func() {
		if err := shared.httpsServer.Serve(tls.NewListener(tcpListener, tlsConfig)); err != nil && err != http.ErrServerClosed {
			androidLogStore.add("chain", "error", "android chain websocket relay exited: "+err.Error())
		}
	}()
	shared.http3Server = &http3.Server{
		Addr:    listenAddr,
		Handler: handler,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{http3.NextProtoH3},
		},
		QUICConfig: newMobileChainQUICConfig(),
	}
	go func() {
		if err := shared.http3Server.Serve(udpPacketConn); err != nil && err != http.ErrServerClosed {
			androidLogStore.add("chain", "error", "android chain h3 relay exited: "+err.Error())
		}
	}()
	return shared, nil
}

func releaseMobileChainSharedRelayServer(rt *mobileChainRuntime) {
	listenAddr := strings.TrimSpace(rt.relayListenAddr)
	if listenAddr == "" {
		return
	}
	var closing *mobileChainSharedRelayServer
	mobileChainSharedRelayState.mu.Lock()
	if shared := mobileChainSharedRelayState.servers[listenAddr]; shared != nil {
		delete(shared.chainIDs, rt.cfg.ChainID)
		shared.refCount--
		if shared.refCount <= 0 {
			delete(mobileChainSharedRelayState.servers, listenAddr)
			closing = shared
		}
	}
	mobileChainSharedRelayState.mu.Unlock()
	closeMobileChainSharedRelayServer(closing)
}

func closeMobileChainSharedRelayServer(shared *mobileChainSharedRelayServer) {
	if shared == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if shared.httpsServer != nil {
		_ = shared.httpsServer.Shutdown(ctx)
	}
	if shared.http3Server != nil {
		_ = shared.http3Server.Close()
	}
	if shared.tcpListener != nil {
		_ = shared.tcpListener.Close()
	}
	if shared.udpPacketConn != nil {
		_ = shared.udpPacketConn.Close()
	}
}

func handleMobileChainRelayDispatch(w http.ResponseWriter, r *http.Request) {
	chainID := resolveMobileChainIDFromRequest(r)
	rt := getMobileChainRuntime(chainID)
	if rt == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	if err := verifyMobileChainRelayRequestAuth(rt, r); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(r.Header.Get(mobileChainHeaderRelayMode)))
	if mode == "" {
		mode = mobileChainRelayModeBridge
	}
	role := normalizeMobileChainBridgeRole(r.Header.Get(mobileChainHeaderRelayRole))
	switch mode {
	case mobileChainRelayModeBridge:
		if isMobileChainH3Connect(r) {
			handleMobileChainBridgeRelayH3(rt, role, w, r)
			return
		}
		handleMobileChainBridgeRelayWebSocket(rt, role, w, r)
	case mobileChainRelayModeStream, mobileChainRelayModePingPong:
		if isMobileChainH3Connect(r) {
			handleMobileChainStreamRelayH3(rt, role, w, r)
			return
		}
		handleMobileChainStreamRelayWebSocket(rt, role, w, r)
	case mobileChainRelayModeSpeedTest:
		handleMobileChainSpeedTest(w, r)
	default:
		http.Error(w, "unsupported relay mode", http.StatusBadRequest)
	}
}

func handleMobileChainBridgeRelayWebSocket(rt *mobileChainRuntime, role string, w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, ReadBufferSize: mobileChainWSBufferBytes, WriteBufferSize: mobileChainWSBufferBytes}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	handleMobileChainBridgeRelayConn(rt, role, newWebSocketNetConn(ws))
}

func handleMobileChainBridgeRelayH3(rt *mobileChainRuntime, role string, w http.ResponseWriter, r *http.Request) {
	conn, ok := mobileChainConnFromH3(w, r, "android-chain-h3-bridge")
	if !ok {
		http.Error(w, "http3 stream unavailable", http.StatusInternalServerError)
		return
	}
	defer conn.Close()
	handleMobileChainBridgeRelayConn(rt, role, conn)
}

func handleMobileChainBridgeRelayConn(rt *mobileChainRuntime, role string, conn net.Conn) {
	sessionID := rt.nextBridgeSessionID("inbound")
	session, err := yamux.Server(conn, newMobileChainYamuxConfig())
	if err != nil {
		return
	}
	if normalizeMobileChainBridgeRole(role) == mobileChainBridgeRoleToPrev {
		rt.setDownstreamSession(sessionID, session)
		go acceptMobileChainBridgeStreams(rt, session, sessionID, "reverse")
		waitMobileChainBridgeSession(rt.stopCh, session)
		rt.clearDownstreamSession(sessionID, session)
	} else {
		rt.setUpstreamSession(sessionID, session)
		go acceptMobileChainBridgeStreams(rt, session, sessionID, "forward")
		waitMobileChainBridgeSession(rt.stopCh, session)
		rt.clearUpstreamSession(sessionID, session)
	}
	_ = session.Close()
}

func handleMobileChainStreamRelayWebSocket(rt *mobileChainRuntime, role string, w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, ReadBufferSize: mobileChainWSBufferBytes, WriteBufferSize: mobileChainWSBufferBytes}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn := newWebSocketNetConn(ws)
	if normalizeMobileChainBridgeRole(role) == mobileChainBridgeRoleToPrev {
		handleMobileChainReverseConn(rt, conn, "")
		return
	}
	handleMobileChainConn(rt, conn, "")
}

func handleMobileChainStreamRelayH3(rt *mobileChainRuntime, role string, w http.ResponseWriter, r *http.Request) {
	conn, ok := mobileChainConnFromH3(w, r, "android-chain-h3-stream")
	if !ok {
		http.Error(w, "http3 stream unavailable", http.StatusInternalServerError)
		return
	}
	if normalizeMobileChainBridgeRole(role) == mobileChainBridgeRoleToPrev {
		handleMobileChainReverseConn(rt, conn, "")
		return
	}
	handleMobileChainConn(rt, conn, "")
}

func handleMobileChainSpeedTest(w http.ResponseWriter, r *http.Request) {
	byteCount := int64(128 * 1024 * 1024)
	if raw := strings.TrimSpace(r.Header.Get(mobileChainHeaderSpeedBytes)); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 && parsed <= 256*1024*1024 {
			byteCount = parsed
		}
	}
	var conn net.Conn
	if isMobileChainH3Connect(r) {
		h3Conn, ok := mobileChainConnFromH3(w, r, "android-chain-h3-speed")
		if !ok {
			http.Error(w, "http3 stream unavailable", http.StatusInternalServerError)
			return
		}
		conn = h3Conn
	} else {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, ReadBufferSize: mobileChainWSBufferBytes, WriteBufferSize: mobileChainWSBufferBytes}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn = newWebSocketNetConn(ws)
	}
	defer conn.Close()
	buf := make([]byte, 256*1024)
	for i := range buf {
		buf[i] = byte(i % 251)
	}
	for byteCount > 0 {
		n := int64(len(buf))
		if byteCount < n {
			n = byteCount
		}
		if _, err := conn.Write(buf[:n]); err != nil {
			return
		}
		byteCount -= n
	}
}

func startMobileChainBridgeWorkers(rt *mobileChainRuntime) {
	cfg := rt.cfg
	if cfg.NextAuthMode != "proxy" && normalizeMobileChainDialMode(cfg.NextDialMode) == mobileChainDialForward {
		go runMobileChainBridgeDialLoop(rt, mobileChainBridgeDialTarget{
			Host:                cfg.NextHost,
			Port:                cfg.NextPort,
			LinkLayer:           firstMobileChainNonEmpty(cfg.NextLinkLayer, cfg.LinkLayer),
			RoleHeader:          mobileChainBridgeRoleToNext,
			PreserveRelayDomain: cfg.NextPreserveRelayDomain,
			AssignDownstream:    true,
			Tag:                 "downstream-forward",
		})
	}
	if normalizeMobileChainDialMode(cfg.PrevDialMode) == mobileChainDialReverse && cfg.PrevHost != "" && cfg.PrevPort > 0 {
		go runMobileChainBridgeDialLoop(rt, mobileChainBridgeDialTarget{
			Host:                cfg.PrevHost,
			Port:                cfg.PrevPort,
			LinkLayer:           cfg.PrevLinkLayer,
			RoleHeader:          mobileChainBridgeRoleToPrev,
			PreserveRelayDomain: cfg.PrevPreserveRelayDomain,
			AssignUpstream:      true,
			AcceptStreams:       true,
			Tag:                 "upstream-reverse",
		})
	}
}

func runMobileChainBridgeDialLoop(rt *mobileChainRuntime, target mobileChainBridgeDialTarget) {
	backoff := mobileChainBridgeRetryMin
	for {
		select {
		case <-rt.stopCh:
			return
		default:
		}
		conn, err := openMobileChainRelayBridgeConn(rt.cfg, target)
		if err != nil {
			androidLogStore.add("chain", "warn", "android chain bridge dial failed: chain="+rt.cfg.ChainID+" tag="+target.Tag+" err="+err.Error())
			sleepMobileChainBackoff(rt.stopCh, backoff)
			backoff = nextMobileChainBackoff(backoff)
			continue
		}
		session, err := yamux.Client(conn, newMobileChainYamuxConfig())
		if err != nil {
			_ = conn.Close()
			sleepMobileChainBackoff(rt.stopCh, backoff)
			backoff = nextMobileChainBackoff(backoff)
			continue
		}
		sessionID := rt.nextBridgeSessionID(target.Tag)
		backoff = mobileChainBridgeRetryMin
		if target.AssignDownstream {
			rt.setDownstreamSession(sessionID, session)
		}
		if target.AssignUpstream {
			rt.setUpstreamSession(sessionID, session)
		}
		if target.AcceptStreams || target.AssignDownstream || target.AssignUpstream {
			direction := "forward"
			if target.AssignDownstream {
				direction = "reverse"
			}
			go acceptMobileChainBridgeStreams(rt, session, sessionID, direction)
		}
		waitMobileChainBridgeSession(rt.stopCh, session)
		if target.AssignDownstream {
			rt.clearDownstreamSession(sessionID, session)
		}
		if target.AssignUpstream {
			rt.clearUpstreamSession(sessionID, session)
		}
		_ = session.Close()
		_ = conn.Close()
		sleepMobileChainBackoff(rt.stopCh, backoff)
		backoff = nextMobileChainBackoff(backoff)
	}
}

func acceptMobileChainBridgeStreams(rt *mobileChainRuntime, session *yamux.Session, sessionID string, direction string) {
	for {
		stream, err := session.Accept()
		if err != nil {
			return
		}
		if strings.EqualFold(direction, "reverse") {
			go handleMobileChainReverseConn(rt, stream, sessionID)
		} else {
			go handleMobileChainConn(rt, stream, sessionID)
		}
	}
}

func handleMobileChainConn(rt *mobileChainRuntime, conn net.Conn, preferredSessionID string) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if _, err := readMobileChainSourceIPHint(reader); err != nil {
		return
	}
	if handleMobileChainBridgeControlIfPresent(conn, reader) {
		return
	}
	if rt.cfg.NextAuthMode == "proxy" {
		_ = handleMobileChainProxyStream(conn, reader)
		return
	}
	next, err := openMobileChainDownstreamStream(rt, preferredSessionID, mobileChainOpenTimeout)
	if err != nil {
		return
	}
	defer next.Close()
	relayMobileChainDuplex(reader, next, bufio.NewReader(next), conn)
}

func handleMobileChainReverseConn(rt *mobileChainRuntime, conn net.Conn, preferredSessionID string) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if _, err := readMobileChainSourceIPHint(reader); err != nil {
		return
	}
	if handleMobileChainBridgeControlIfPresent(conn, reader) {
		return
	}
	role := normalizeMobileChainRole(rt.cfg.Role)
	if role == mobileChainRoleEntry || role == mobileChainRoleEntryExit {
		_ = handleMobileChainProxyStream(conn, reader)
		return
	}
	prev, err := openMobileChainUpstreamStream(rt, preferredSessionID, mobileChainOpenTimeout)
	if err != nil {
		return
	}
	defer prev.Close()
	relayMobileChainDuplex(reader, prev, bufio.NewReader(prev), conn)
}

func handleMobileChainProxyStream(stream net.Conn, reader *bufio.Reader) error {
	_ = stream.SetReadDeadline(time.Now().Add(20 * time.Second))
	var req mobileChainTunnelOpenRequest
	if err := json.NewDecoder(reader).Decode(&req); err != nil {
		return err
	}
	_ = stream.SetReadDeadline(time.Time{})
	if strings.EqualFold(strings.TrimSpace(req.Type), mobileChainRelayModePingPong) {
		return handleMobileChainPingPong(stream, req.PingBytes)
	}
	if strings.EqualFold(strings.TrimSpace(req.Type), mobileChainRelayModePrepare) {
		if err := writeMobileChainOpenResponse(stream, mobileChainTunnelOpenResponse{OK: true}); err != nil {
			return err
		}
		_ = stream.SetReadDeadline(time.Now().Add(mobileChainUDPIdleTTL + mobileChainResponseTimeout))
		req = mobileChainTunnelOpenRequest{}
		if err := json.NewDecoder(reader).Decode(&req); err != nil {
			return err
		}
		_ = stream.SetReadDeadline(time.Time{})
	}
	network := strings.ToLower(strings.TrimSpace(req.Network))
	if network == "" {
		network = mobileChainNetworkTCP
	}
	target := strings.TrimSpace(req.Address)
	if target == "" {
		return writeMobileChainOpenResponse(stream, mobileChainTunnelOpenResponse{OK: false, Error: "missing address"})
	}
	switch network {
	case mobileChainNetworkTCP:
		return handleMobileChainTunnelTCP(stream, target)
	case mobileChainNetworkUDP:
		return handleMobileChainTunnelUDP(stream, target)
	default:
		return writeMobileChainOpenResponse(stream, mobileChainTunnelOpenResponse{OK: false, Error: "unsupported network"})
	}
}

func handleMobileChainPingPong(stream net.Conn, byteCount int64) error {
	if byteCount <= 0 || byteCount > 64*1024 {
		byteCount = 64
	}
	if err := writeMobileChainOpenResponse(stream, mobileChainTunnelOpenResponse{OK: true}); err != nil {
		return err
	}
	buf := make([]byte, byteCount)
	if _, err := io.ReadFull(stream, buf); err != nil {
		return err
	}
	_, err := stream.Write(buf)
	return err
}

func handleMobileChainTunnelTCP(stream net.Conn, target string) error {
	remote, err := net.DialTimeout("tcp", target, mobileChainPortForwardDialTimeout)
	if err != nil {
		_ = writeMobileChainOpenResponse(stream, mobileChainTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	defer remote.Close()
	if err := writeMobileChainOpenResponse(stream, mobileChainTunnelOpenResponse{OK: true}); err != nil {
		return err
	}
	relayMobileChainBidirectional(stream, remote)
	return nil
}

func handleMobileChainTunnelUDP(stream net.Conn, target string) error {
	remote, err := net.DialTimeout("udp", target, mobileChainPortForwardDialTimeout)
	if err != nil {
		_ = writeMobileChainOpenResponse(stream, mobileChainTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	defer remote.Close()
	if err := writeMobileChainOpenResponse(stream, mobileChainTunnelOpenResponse{OK: true}); err != nil {
		return err
	}
	reader := bufio.NewReader(stream)
	errCh := make(chan error, 2)
	go func() {
		buf := make([]byte, mobileChainFrameMaxPayload)
		for {
			n, err := readMobileChainFramedPacketInto(reader, buf)
			if err != nil {
				errCh <- err
				return
			}
			if n > 0 {
				if _, err := remote.Write(buf[:n]); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()
	go func() {
		buf := make([]byte, mobileChainFrameMaxPayload)
		for {
			n, err := remote.Read(buf)
			if n > 0 {
				if writeErr := writeMobileChainFramedPacket(stream, buf[:n]); writeErr != nil {
					errCh <- writeErr
					return
				}
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()
	err = <-errCh
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func startMobileChainPortForwardWorkers(rt *mobileChainRuntime) {
	for _, cfg := range rt.cfg.PortForwards {
		if !cfg.Enabled || !shouldRunMobileChainPortForwardOnRole(rt.cfg.Role, cfg.EntrySide) {
			continue
		}
		listenAddr := net.JoinHostPort(normalizeMobileChainListenHost(cfg.ListenHost), strconv.Itoa(cfg.ListenPort))
		network := normalizeMobileChainPortForwardNetwork(cfg.Network)
		if network == mobileChainNetworkTCP || network == mobileChainNetworkBoth {
			ln, err := net.Listen("tcp", listenAddr)
			if err == nil {
				rt.registerTCPForward(ln)
				go runMobileChainTCPPortForward(rt, cfg, ln)
			} else {
				androidLogStore.add("chain", "error", "android tcp port forward listen failed: id="+cfg.ID+" err="+err.Error())
			}
		}
		if network == mobileChainNetworkUDP || network == mobileChainNetworkBoth {
			pc, err := net.ListenPacket("udp", listenAddr)
			if err == nil {
				rt.registerUDPForward(pc)
				go runMobileChainUDPPortForward(rt, cfg, pc)
			} else {
				androidLogStore.add("chain", "error", "android udp port forward listen failed: id="+cfg.ID+" err="+err.Error())
			}
		}
	}
}

func runMobileChainTCPPortForward(rt *mobileChainRuntime, cfg mobileChainPortForwardConfig, listener net.Listener) {
	targetAddr := net.JoinHostPort(strings.TrimSpace(cfg.TargetHost), strconv.Itoa(cfg.TargetPort))
	for {
		local, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer local.Close()
			remote, err := openMobileChainPortForwardStream(rt, cfg.EntrySide, mobileChainNetworkTCP, targetAddr, "")
			if err != nil {
				return
			}
			defer remote.Close()
			relayMobileChainBidirectional(local, remote)
		}()
	}
}

func runMobileChainUDPPortForward(rt *mobileChainRuntime, cfg mobileChainPortForwardConfig, packetConn net.PacketConn) {
	targetAddr := net.JoinHostPort(strings.TrimSpace(cfg.TargetHost), strconv.Itoa(cfg.TargetPort))
	type udpSession struct {
		addr     net.Addr
		stream   net.Conn
		reader   *bufio.Reader
		lastSeen time.Time
	}
	sessions := map[string]*udpSession{}
	var sessionsMu sync.Mutex
	closeSession := func(key string, item *udpSession) {
		sessionsMu.Lock()
		if sessions[key] == item {
			delete(sessions, key)
		}
		sessionsMu.Unlock()
		_ = item.stream.Close()
	}
	go func() {
		ticker := time.NewTicker(mobileChainUDPGCInterval)
		defer ticker.Stop()
		for {
			select {
			case <-rt.stopCh:
				return
			case <-ticker.C:
				now := time.Now()
				sessionsMu.Lock()
				for key, item := range sessions {
					if now.Sub(item.lastSeen) >= mobileChainUDPIdleTTL {
						delete(sessions, key)
						_ = item.stream.Close()
					}
				}
				sessionsMu.Unlock()
			}
		}
	}()
	openSession := func(key string, addr net.Addr) (*udpSession, error) {
		stream, err := openMobileChainPortForwardStream(rt, cfg.EntrySide, mobileChainNetworkUDP, targetAddr, key)
		if err != nil {
			return nil, err
		}
		item := &udpSession{addr: addr, stream: stream, reader: bufio.NewReader(stream), lastSeen: time.Now()}
		go func() {
			for {
				payload, err := readMobileChainFramedPacket(item.reader)
				if err != nil {
					closeSession(key, item)
					return
				}
				if len(payload) > 0 {
					_, _ = packetConn.WriteTo(payload, item.addr)
				}
			}
		}()
		return item, nil
	}
	buf := make([]byte, mobileChainFrameMaxPayload)
	for {
		n, addr, err := packetConn.ReadFrom(buf)
		if err != nil {
			return
		}
		if n <= 0 || addr == nil {
			continue
		}
		key := strings.TrimSpace(addr.String())
		sessionsMu.Lock()
		item := sessions[key]
		if item == nil {
			var openErr error
			item, openErr = openSession(key, addr)
			if openErr != nil {
				sessionsMu.Unlock()
				continue
			}
			sessions[key] = item
		}
		item.lastSeen = time.Now()
		stream := item.stream
		sessionsMu.Unlock()
		if err := writeMobileChainFramedPacket(stream, buf[:n]); err != nil {
			closeSession(key, item)
		}
	}
}

func openMobileChainPortForwardStream(rt *mobileChainRuntime, entrySide string, network string, targetAddr string, flowID string) (net.Conn, error) {
	var stream net.Conn
	var err error
	if normalizeMobileChainPortForwardEntrySide(entrySide) == mobileChainEntrySideExit {
		stream, err = openMobileChainUpstreamStream(rt, "", mobileChainOpenBridgeStreamTimeout)
	} else {
		stream, err = openMobileChainDownstreamStream(rt, "", mobileChainOpenBridgeStreamTimeout)
	}
	if err != nil {
		return nil, err
	}
	req := mobileChainTunnelOpenRequest{Type: "open", Network: strings.ToLower(strings.TrimSpace(network)), Address: strings.TrimSpace(targetAddr), FlowID: strings.TrimSpace(flowID)}
	if strings.EqualFold(network, mobileChainNetworkUDP) {
		req.AssociationV2 = &mobileChainAssociationV2Config{
			Version:         2,
			Transport:       "udp",
			RouteTarget:     strings.TrimSpace(targetAddr),
			NATMode:         "endpoint_independent",
			TTLProfile:      "default",
			IdleTimeoutMS:   mobileChainUDPIdleTTL.Milliseconds(),
			GCIntervalMS:    mobileChainUDPGCInterval.Milliseconds(),
			CreatedAtUnixMS: time.Now().UnixMilli(),
			AssocKeyV2:      strings.TrimSpace(flowID),
			FlowID:          strings.TrimSpace(flowID),
		}
	}
	if err := writeMobileChainJSONWithDeadline(stream, req); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetReadDeadline(time.Now().Add(mobileChainResponseTimeout))
	var resp mobileChainTunnelOpenResponse
	if err := json.NewDecoder(stream).Decode(&resp); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetReadDeadline(time.Time{})
	if !resp.OK {
		_ = stream.Close()
		return nil, errors.New(firstMobileChainNonEmpty(strings.TrimSpace(resp.Error), "open upstream target failed"))
	}
	return stream, nil
}

func openMobileChainRelayBridgeConn(cfg mobileChainRuntimeConfig, target mobileChainBridgeDialTarget) (net.Conn, error) {
	protocols := mobileChainRelayProtocolCandidates(target.LinkLayer)
	var lastErr error
	for _, protocol := range protocols {
		conn, err := openMobileChainRelayBridgeConnProtocol(cfg, target, protocol)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no relay protocol candidate")
}

func openMobileChainRelayBridgeConnProtocol(cfg mobileChainRuntimeConfig, target mobileChainBridgeDialTarget, protocol string) (net.Conn, error) {
	switch normalizeMobileChainLinkLayer(protocol) {
	case "websocket-h3":
		return openMobileChainRelayBridgeH3Conn(cfg, target)
	default:
		return openMobileChainRelayBridgeWebSocketConn(cfg, target)
	}
}

func openMobileChainRelayBridgeWebSocketConn(cfg mobileChainRuntimeConfig, target mobileChainBridgeDialTarget) (net.Conn, error) {
	dialHost, hostHeader, err := resolveMobileChainDialHost(target.Host, target.PreserveRelayDomain)
	if err != nil {
		return nil, err
	}
	relayURL := buildMobileChainRelayURL("wss", hostHeader, target.Port, cfg.ChainID)
	header := buildMobileChainRelayHeaders(cfg, mobileChainRelayModeBridge, target.RoleHeader, 0)
	dialHostPort := net.JoinHostPort(dialHost, strconv.Itoa(target.Port))
	dialer := websocket.Dialer{
		HandshakeTimeout:  mobileChainOpenTimeout,
		Proxy:             nil,
		ReadBufferSize:    mobileChainWSBufferBytes,
		WriteBufferSize:   mobileChainWSBufferBytes,
		EnableCompression: false,
		NetDialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: mobileChainOpenTimeout}
			return d.DialContext(ctx, network, dialHostPort)
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: dialHost, InsecureSkipVerify: true},
	}
	ws, response, err := dialer.Dial(relayURL, header)
	if err != nil {
		if response != nil && response.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			return nil, fmt.Errorf("probe relay websocket failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, err
	}
	return newWebSocketNetConn(ws), nil
}

func openMobileChainRelayBridgeH3Conn(cfg mobileChainRuntimeConfig, target mobileChainBridgeDialTarget) (net.Conn, error) {
	dialHost, hostHeader, err := resolveMobileChainDialHost(target.Host, target.PreserveRelayDomain)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mobileChainOpenTimeout)
	dialHostPort := net.JoinHostPort(dialHost, strconv.Itoa(target.Port))
	quicConn, err := quic.DialAddr(ctx, dialHostPort, &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{http3.NextProtoH3}, ServerName: dialHost, InsecureSkipVerify: true}, newMobileChainQUICConfig())
	if err != nil {
		cancel()
		return nil, err
	}
	transport := &http3.Transport{}
	clientConn := transport.NewClientConn(quicConn)
	select {
	case <-clientConn.ReceivedSettings():
	case <-ctx.Done():
		_ = quicConn.CloseWithError(0, "settings timeout")
		cancel()
		return nil, ctx.Err()
	case <-clientConn.Context().Done():
		cancel()
		return nil, context.Cause(clientConn.Context())
	}
	stream, err := clientConn.OpenRequestStream(ctx)
	if err != nil {
		_ = quicConn.CloseWithError(0, "stream open failed")
		cancel()
		return nil, err
	}
	reqURL := buildMobileChainRelayURL("https", hostHeader, target.Port, cfg.ChainID)
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, reqURL, nil)
	if err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "request failed")
		cancel()
		return nil, err
	}
	req.Proto = "websocket"
	req.ProtoMajor = 3
	req.ProtoMinor = 0
	req.Host = hostHeader
	req.Header = buildMobileChainRelayHeaders(cfg, mobileChainRelayModeBridge, target.RoleHeader, 0)
	if err := stream.SendRequestHeader(req); err != nil {
		_ = quicConn.CloseWithError(0, "header failed")
		cancel()
		return nil, err
	}
	resp, err := stream.ReadResponse()
	if err != nil {
		_ = quicConn.CloseWithError(0, "response failed")
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		_ = quicConn.CloseWithError(0, "bad status")
		cancel()
		return nil, fmt.Errorf("probe relay h3 failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	cancelOnce := sync.Once{}
	return &mobileChainH3Conn{
		stream: stream,
		local:  mobileChainNetAddr{label: "android-chain-h3-local"},
		remote: mobileChainNetAddr{label: dialHostPort},
		closeFn: func() error {
			cancelOnce.Do(func() {
				cancel()
				_ = quicConn.CloseWithError(0, "closed")
			})
			return stream.Close()
		},
	}, nil
}

func openMobileChainDownstreamStream(rt *mobileChainRuntime, sessionID string, timeout time.Duration) (net.Conn, error) {
	return openMobileChainSessionStream(rt, true, sessionID, timeout)
}

func openMobileChainUpstreamStream(rt *mobileChainRuntime, sessionID string, timeout time.Duration) (net.Conn, error) {
	return openMobileChainSessionStream(rt, false, sessionID, timeout)
}

func openMobileChainSessionStream(rt *mobileChainRuntime, downstream bool, sessionID string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		var session *yamux.Session
		if downstream {
			session = rt.getDownstreamSession(sessionID)
		} else {
			session = rt.getUpstreamSession(sessionID)
		}
		if session != nil && !session.IsClosed() {
			stream, err := session.Open()
			if err == nil {
				return stream, nil
			}
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-rt.stopCh:
			return nil, errors.New("runtime stopped")
		case <-time.After(300 * time.Millisecond):
		}
	}
	if downstream {
		return nil, errors.New("downstream bridge is unavailable")
	}
	return nil, errors.New("upstream bridge is unavailable")
}

func (rt *mobileChainRuntime) nextBridgeSessionID(prefix string) string {
	seq := atomic.AddUint64(&rt.bridgeSeq, 1)
	return strings.TrimSpace(prefix) + "-" + strconv.FormatUint(seq, 10) + "-" + strings.ToLower(randomHexToken(4))
}

func (rt *mobileChainRuntime) setDownstreamSession(id string, session *yamux.Session) {
	rt.bridgeMu.Lock()
	rt.downstreamSessions[id] = &mobileChainBridgeSession{ID: id, Session: session}
	rt.bridgeMu.Unlock()
}

func (rt *mobileChainRuntime) setUpstreamSession(id string, session *yamux.Session) {
	rt.bridgeMu.Lock()
	rt.upstreamSessions[id] = &mobileChainBridgeSession{ID: id, Session: session}
	rt.bridgeMu.Unlock()
}

func (rt *mobileChainRuntime) clearDownstreamSession(id string, session *yamux.Session) {
	rt.bridgeMu.Lock()
	for key, item := range rt.downstreamSessions {
		if (strings.TrimSpace(id) == "" || key == id) && item != nil && item.Session == session {
			delete(rt.downstreamSessions, key)
		}
	}
	rt.bridgeMu.Unlock()
}

func (rt *mobileChainRuntime) clearUpstreamSession(id string, session *yamux.Session) {
	rt.bridgeMu.Lock()
	for key, item := range rt.upstreamSessions {
		if (strings.TrimSpace(id) == "" || key == id) && item != nil && item.Session == session {
			delete(rt.upstreamSessions, key)
		}
	}
	rt.bridgeMu.Unlock()
}

func (rt *mobileChainRuntime) getDownstreamSession(id string) *yamux.Session {
	return rt.getSession(rt.downstreamSessions, id)
}

func (rt *mobileChainRuntime) getUpstreamSession(id string) *yamux.Session {
	return rt.getSession(rt.upstreamSessions, id)
}

func (rt *mobileChainRuntime) getSession(items map[string]*mobileChainBridgeSession, id string) *yamux.Session {
	rt.bridgeMu.Lock()
	defer rt.bridgeMu.Unlock()
	if strings.TrimSpace(id) != "" {
		if item := items[strings.TrimSpace(id)]; item != nil {
			return item.Session
		}
		return nil
	}
	for key, item := range items {
		if item != nil && item.Session != nil && !item.Session.IsClosed() {
			return item.Session
		}
		delete(items, key)
	}
	return nil
}

func (rt *mobileChainRuntime) registerTCPForward(ln net.Listener) {
	rt.forwardMu.Lock()
	rt.tcpForwards = append(rt.tcpForwards, ln)
	rt.forwardMu.Unlock()
}

func (rt *mobileChainRuntime) registerUDPForward(pc net.PacketConn) {
	rt.forwardMu.Lock()
	rt.udpForwards = append(rt.udpForwards, pc)
	rt.forwardMu.Unlock()
}

func verifyMobileChainRelayRequestAuth(rt *mobileChainRuntime, r *http.Request) error {
	if resolveMobileChainIDFromRequest(r) != rt.cfg.ChainID {
		return errors.New("chain id mismatch")
	}
	nonce := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if nonce == "" {
		return errors.New("nonce is required")
	}
	gotMAC := strings.TrimSpace(r.Header.Get(mobileChainHeaderMAC))
	if gotMAC == "" {
		return errors.New("mac is required")
	}
	expected := mobileChainHMAC(rt.cfg.Secret, rt.cfg.ChainID, nonce)
	if !hmac.Equal([]byte(strings.ToLower(gotMAC)), []byte(strings.ToLower(expected))) {
		return errors.New("authentication failed")
	}
	rawTicket := strings.TrimSpace(r.Header.Get(mobileChainHeaderAuthTicket))
	if rt.cfg.RequireUserAuth {
		if err := verifyMobileChainUserAuthTicket(rt.cfg, rawTicket); err != nil {
			return err
		}
	} else if ticket := strings.TrimSpace(rt.cfg.AuthTicket); ticket != "" && rawTicket != ticket {
		return errors.New("auth ticket mismatch")
	}
	return nil
}

type mobileChainUserAuthTicketPayload struct {
	Version       string `json:"v"`
	ChainID       string `json:"chain_id"`
	ClientEntryID string `json:"client_entry_id,omitempty"`
	UserID        string `json:"user_id"`
	UserPublicKey string `json:"user_public_key"`
	IssuedAt      string `json:"issued_at"`
}

func verifyMobileChainUserAuthTicket(cfg mobileChainRuntimeConfig, rawTicket string) error {
	ticket := strings.TrimSpace(rawTicket)
	if ticket == "" {
		return errors.New("user auth ticket is required")
	}
	if len(cfg.UserPublicKey) != ed25519.PublicKeySize {
		return errors.New("user public key is not configured")
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		return errors.New("invalid user auth ticket")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errors.New("invalid user auth ticket payload")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errors.New("invalid user auth ticket signature")
	}
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(cfg.UserPublicKey, payloadBytes, signature) {
		return errors.New("user auth ticket verification failed")
	}
	var payload mobileChainUserAuthTicketPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return errors.New("invalid user auth ticket payload json")
	}
	if strings.TrimSpace(payload.Version) != "chain-auth-v1" {
		return errors.New("unsupported user auth ticket version")
	}
	if strings.TrimSpace(payload.ChainID) != strings.TrimSpace(cfg.ChainID) {
		return errors.New("user auth ticket chain mismatch")
	}
	if strings.TrimSpace(payload.UserPublicKey) != strings.TrimSpace(cfg.RawPublicKey) {
		return errors.New("user auth ticket public key mismatch")
	}
	if err := verifyMobileChainAuthTicketIssuedAt(payload.IssuedAt, mobileChainAuthTicketNow()); err != nil {
		return err
	}
	return nil
}

func verifyMobileChainAuthTicketIssuedAt(raw string, now time.Time) error {
	issuedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return errors.New("invalid user auth ticket issued_at")
	}
	if issuedAt.After(now.UTC().Add(5 * time.Minute)) {
		return errors.New("user auth ticket issued_at is in the future")
	}
	if !now.UTC().Before(issuedAt.UTC().AddDate(0, 2, 0)) {
		return errors.New("user auth ticket expired")
	}
	return nil
}

func parseMobileChainUserPublicKey(raw string) (ed25519.PublicKey, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("empty public key")
	}
	if block, _ := pem.Decode([]byte(trimmed)); block != nil {
		pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		pub, ok := pubAny.(ed25519.PublicKey)
		if !ok {
			return nil, errors.New("public key is not ed25519")
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
	return nil, errors.New("unsupported public key format")
}

func buildMobileChainRelayHeaders(cfg mobileChainRuntimeConfig, mode string, role string, speedBytes int64) http.Header {
	nonce := randomHexToken(16)
	header := http.Header{}
	header.Set(mobileChainHeaderLegacyChainID, cfg.ChainID)
	header.Set(mobileChainHeaderChainID, cfg.ChainID)
	header.Set(mobileChainHeaderVersion, mobileChainAuthPacketVersion)
	header.Set(mobileChainHeaderRelayMode, strings.TrimSpace(mode))
	header.Set(mobileChainHeaderRelayRole, normalizeMobileChainBridgeRole(role))
	header.Set(mobileChainHeaderAuthMode, "secret_hmac")
	header.Set(mobileChainHeaderAuthTimestamp, time.Now().UTC().Format(time.RFC3339Nano))
	header.Set("Authorization", "Bearer "+nonce)
	header.Set(mobileChainHeaderMAC, mobileChainHMAC(cfg.Secret, cfg.ChainID, nonce))
	if cfg.AuthTicket != "" {
		header.Set(mobileChainHeaderAuthTicket, cfg.AuthTicket)
	}
	if speedBytes > 0 {
		header.Set(mobileChainHeaderSpeedBytes, strconv.FormatInt(speedBytes, 10))
	}
	return header
}

func mobileChainHMAC(secret string, chainID string, nonce string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(strings.TrimSpace(chainID)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(nonce)))
	return hex.EncodeToString(mac.Sum(nil))
}

func handleMobileChainBridgeControlIfPresent(conn net.Conn, reader *bufio.Reader) bool {
	peek, err := reader.Peek(len(mobileChainBridgeControlPrefix))
	if err != nil || string(peek) != mobileChainBridgeControlPrefix {
		return false
	}
	line, _ := reader.ReadString('\n')
	var req mobileChainBridgeControlRequest
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), mobileChainBridgeControlPrefix))
	_ = json.Unmarshal([]byte(raw), &req)
	_ = writeMobileChainJSONWithDeadline(conn, mobileChainBridgeControlResponse{
		Type:      "reverse_data_open_result",
		RequestID: strings.TrimSpace(req.RequestID),
		OK:        false,
		Error:     "reverse data stream is disabled; use bridge yamux stream",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	return true
}

func resolveMobileChainIDFromRequest(r *http.Request) string {
	for _, key := range []string{mobileChainHeaderChainID, mobileChainHeaderLegacyChainID} {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			return value
		}
	}
	if r != nil && r.URL != nil {
		return strings.TrimSpace(r.URL.Query().Get("chain_id"))
	}
	return ""
}

func isMobileChainH3Connect(r *http.Request) bool {
	return r != nil && r.Method == http.MethodConnect && r.ProtoMajor == 3 && strings.EqualFold(strings.TrimSpace(r.Proto), "websocket")
}

func mobileChainConnFromH3(w http.ResponseWriter, r *http.Request, label string) (net.Conn, bool) {
	streamer, ok := w.(http3.HTTPStreamer)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	stream := streamer.HTTPStream()
	return &mobileChainH3Conn{
		stream: stream,
		local:  mobileChainNetAddr{label: label},
		remote: mobileChainNetAddr{label: strings.TrimSpace(r.RemoteAddr)},
		closeFn: func() error {
			return stream.Close()
		},
	}, true
}

func writeMobileChainOpenResponse(stream net.Conn, resp mobileChainTunnelOpenResponse) error {
	return writeMobileChainJSONWithDeadline(stream, resp)
}

func writeMobileChainJSONWithDeadline(stream net.Conn, value any) error {
	_ = stream.SetWriteDeadline(time.Now().Add(mobileChainResponseTimeout))
	err := json.NewEncoder(stream).Encode(value)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}

func relayMobileChainBidirectional(left net.Conn, right net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = mobileChainCopy(right, left)
		closeMobileChainWrite(right)
	}()
	go func() {
		defer wg.Done()
		_, _ = mobileChainCopy(left, right)
		closeMobileChainWrite(left)
	}()
	wg.Wait()
}

func relayMobileChainDuplex(leftReader io.Reader, rightWriter net.Conn, rightReader io.Reader, leftWriter net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = mobileChainCopy(rightWriter, leftReader)
		closeMobileChainWrite(rightWriter)
	}()
	go func() {
		defer wg.Done()
		_, _ = mobileChainCopy(leftWriter, rightReader)
		closeMobileChainWrite(leftWriter)
	}()
	wg.Wait()
}

func mobileChainCopy(dst io.Writer, src io.Reader) (int64, error) {
	buf, _ := mobileChainCopyBufferPool.Get().([]byte)
	if len(buf) == 0 {
		buf = make([]byte, mobileChainCopyBufferBytes)
	}
	defer mobileChainCopyBufferPool.Put(buf)
	return io.CopyBuffer(dst, src, buf)
}

func closeMobileChainWrite(conn net.Conn) {
	if closer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
		return
	}
	if _, ok := conn.(*yamux.Stream); ok {
		_ = conn.Close()
	}
}

func readMobileChainFramedPacket(reader *bufio.Reader) ([]byte, error) {
	buf := make([]byte, mobileChainFrameMaxPayload)
	n, err := readMobileChainFramedPacketInto(reader, buf)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), buf[:n]...), nil
}

func readMobileChainFramedPacketInto(reader *bufio.Reader, payload []byte) (int, error) {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, err
	}
	size := int(binary.BigEndian.Uint16(header[:]))
	if size <= 0 {
		return 0, nil
	}
	if size > len(payload) {
		return 0, fmt.Errorf("udp frame too large: %d", size)
	}
	_, err := io.ReadFull(reader, payload[:size])
	return size, err
}

func writeMobileChainFramedPacket(writer io.Writer, payload []byte) error {
	if len(payload) > mobileChainFrameMaxPayload {
		return fmt.Errorf("udp frame too large: %d", len(payload))
	}
	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}

func readMobileChainSourceIPHint(reader *bufio.Reader) (string, error) {
	peek, err := reader.Peek(len(mobileChainSourceIPHintPrefix))
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", err
	}
	if string(peek) != mobileChainSourceIPHintPrefix {
		return "", nil
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), mobileChainSourceIPHintPrefix))
	if ip == "" {
		return "", nil
	}
	if parsed := net.ParseIP(ip); parsed != nil {
		return parsed.String(), nil
	}
	return "", errors.New("invalid source ip hint")
}

func resolveMobileChainDialHost(rawHost string, preserveDomain bool) (string, string, error) {
	host := strings.TrimSpace(strings.Trim(rawHost, "[]"))
	if host == "" {
		return "", "", errors.New("empty relay host")
	}
	if preserveDomain {
		return host, host, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), ip.String(), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", "", err
	}
	for _, addr := range addrs {
		if addr.IP != nil {
			return addr.IP.String(), addr.IP.String(), nil
		}
	}
	return "", "", errors.New("resolve relay host failed: no ip")
}

func buildMobileChainRelayURL(scheme string, host string, port int, chainID string) string {
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port)), Path: mobileChainRelayPath}
	q := u.Query()
	q.Set("chain_id", strings.TrimSpace(chainID))
	u.RawQuery = q.Encode()
	return u.String()
}

func normalizeMobileChainAction(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "apply", "start":
		return "apply"
	case "restart", "reload":
		return "restart"
	case "remove", "delete", "stop":
		return "remove"
	default:
		return ""
	}
}

func firstMobileChainNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeMobileChainRole(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case mobileChainRoleEntry:
		return mobileChainRoleEntry
	case mobileChainRoleRelay:
		return mobileChainRoleRelay
	case mobileChainRoleExit:
		return mobileChainRoleExit
	case mobileChainRoleEntryExit:
		return mobileChainRoleEntryExit
	default:
		return ""
	}
}

func normalizeMobileChainAuthMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "proxy":
		return "proxy"
	case "secret", "hmac":
		return "secret"
	default:
		return "none"
	}
}

func normalizeMobileChainDialMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case mobileChainDialReverse, "rev":
		return mobileChainDialReverse
	case mobileChainDialNone:
		return mobileChainDialNone
	default:
		return mobileChainDialForward
	}
}

func normalizeMobileChainBridgeRole(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), mobileChainBridgeRoleToPrev) {
		return mobileChainBridgeRoleToPrev
	}
	return mobileChainBridgeRoleToNext
}

func normalizeMobileChainLinkLayer(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "websocket", "ws", "wss":
		return "websocket"
	case "websocket-h3", "ws-h3", "h3-websocket", "h3-ws":
		return "websocket-h3"
	default:
		return ""
	}
}

func mobileChainRelayProtocolCandidates(layer string) []string {
	switch normalizeMobileChainLinkLayer(layer) {
	case "websocket":
		return []string{"websocket"}
	case "websocket-h3":
		return []string{"websocket-h3"}
	default:
		return []string{"websocket-h3", "websocket"}
	}
}

func normalizeMobileChainListenHost(raw string) string {
	if host := strings.TrimSpace(raw); host != "" {
		return host
	}
	return "0.0.0.0"
}

func normalizeMobileChainPort(port int) int {
	if port <= 0 || port > 65535 {
		return 0
	}
	return port
}

func normalizeMobileChainPortForwardNetwork(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case mobileChainNetworkUDP:
		return mobileChainNetworkUDP
	case mobileChainNetworkBoth, "tcp+udp", "udp+tcp":
		return mobileChainNetworkBoth
	default:
		return mobileChainNetworkTCP
	}
}

func normalizeMobileChainPortForwardEntrySide(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case mobileChainEntrySideExit, "exit", "egress":
		return mobileChainEntrySideExit
	default:
		return mobileChainEntrySideEntry
	}
}

func normalizeMobileChainPortForwards(values []mobileChainPortForwardConfig) ([]mobileChainPortForwardConfig, error) {
	out := make([]mobileChainPortForwardConfig, 0, len(values))
	seen := map[string]struct{}{}
	for _, item := range values {
		if item.ListenPort <= 0 || item.ListenPort > 65535 {
			return nil, errors.New("port_forwards listen_port must be between 1 and 65535")
		}
		if item.TargetPort <= 0 || item.TargetPort > 65535 {
			return nil, errors.New("port_forwards target_port must be between 1 and 65535")
		}
		if strings.TrimSpace(item.TargetHost) == "" {
			return nil, errors.New("port_forwards target_host is required")
		}
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = "pf-" + strings.ToLower(randomHexToken(6))
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		item.ID = id
		item.ListenHost = normalizeMobileChainListenHost(item.ListenHost)
		item.TargetHost = strings.TrimSpace(item.TargetHost)
		item.Network = normalizeMobileChainPortForwardNetwork(item.Network)
		item.EntrySide = normalizeMobileChainPortForwardEntrySide(item.EntrySide)
		out = append(out, item)
	}
	return out, nil
}

func shouldRunMobileChainPortForwardOnRole(role string, entrySide string) bool {
	switch normalizeMobileChainPortForwardEntrySide(entrySide) {
	case mobileChainEntrySideExit:
		return normalizeMobileChainRole(role) == mobileChainRoleExit || normalizeMobileChainRole(role) == mobileChainRoleEntryExit
	default:
		return normalizeMobileChainRole(role) == mobileChainRoleEntry || normalizeMobileChainRole(role) == mobileChainRoleEntryExit
	}
}

func isMobileChainControlCFEntry(cmd chainLinkControlMessage) bool {
	for _, value := range []string{cmd.ClientEntryType, cmd.ClientEntryID, cmd.ChainID, cmd.Name} {
		clean := strings.ToLower(strings.TrimSpace(value))
		if clean == "cf" || strings.HasSuffix(clean, "_cf") {
			return true
		}
	}
	return false
}

func normalizeMobileChainNodeID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "node-") || strings.HasPrefix(lower, "node_") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(lower, "node-"), "node_")
		if n, err := strconv.Atoi(strings.TrimSpace(suffix)); err == nil && n > 0 {
			return strconv.Itoa(n)
		}
		return strings.TrimSpace(suffix)
	}
	if n, err := strconv.Atoi(value); err == nil && n > 0 {
		return strconv.Itoa(n)
	}
	return value
}

func buildMobileChainRoute(item linkChainServerItem) []string {
	route := make([]string, 0, 2+len(item.CascadeNodeIDs))
	seen := map[string]struct{}{}
	push := func(raw string) {
		id := normalizeMobileChainNodeID(raw)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		route = append(route, id)
	}
	push(item.EntryNodeID)
	for _, id := range item.CascadeNodeIDs {
		push(id)
	}
	push(item.ExitNodeID)
	return route
}

func resolveMobileChainNodeRole(item linkChainServerItem, nodeID string) string {
	target := normalizeMobileChainNodeID(nodeID)
	if target == "" {
		return ""
	}
	entry := normalizeMobileChainNodeID(item.EntryNodeID)
	exit := normalizeMobileChainNodeID(item.ExitNodeID)
	isEntry := entry != "" && target == entry
	isExit := exit != "" && target == exit
	if isEntry && isExit {
		return mobileChainRoleEntryExit
	}
	if isEntry {
		return mobileChainRoleEntry
	}
	if isExit {
		return mobileChainRoleExit
	}
	route := buildMobileChainRoute(item)
	if len(route) > 0 {
		if target == normalizeMobileChainNodeID(route[0]) {
			return mobileChainRoleEntry
		}
		if target == normalizeMobileChainNodeID(route[len(route)-1]) {
			return mobileChainRoleExit
		}
	}
	for _, id := range item.CascadeNodeIDs {
		if normalizeMobileChainNodeID(id) == target {
			return mobileChainRoleRelay
		}
	}
	return ""
}

func findMobileChainHopForNodeID(item linkChainServerItem, nodeID string) (linkChainHopItem, bool) {
	target := normalizeMobileChainNodeID(nodeID)
	for _, hop := range item.HopConfigs {
		if hop.NodeNo <= 0 {
			continue
		}
		if normalizeMobileChainNodeID(strconv.Itoa(hop.NodeNo)) == target {
			return hop, true
		}
	}
	return linkChainHopItem{}, false
}

func findMobileChainHopForNode(item linkChainServerItem, nodeID string) linkChainHopItem {
	if hop, ok := findMobileChainHopForNodeID(item, nodeID); ok {
		return hop
	}
	target := normalizeMobileChainNodeID(nodeID)
	route := buildMobileChainRoute(item)
	for index, id := range route {
		if normalizeMobileChainNodeID(id) != target {
			continue
		}
		for _, hop := range item.HopConfigs {
			if hop.NodeNo == index+1 {
				return hop
			}
		}
	}
	return linkChainHopItem{}
}

func resolveMobileChainNextHop(item linkChainServerItem, nodeID string, role string) (host string, port int, layer string, dialMode string, authMode string) {
	if role == mobileChainRoleExit || role == mobileChainRoleEntryExit {
		return "", 0, "", mobileChainDialNone, "proxy"
	}
	route := buildMobileChainRoute(item)
	target := normalizeMobileChainNodeID(nodeID)
	for i, id := range route {
		if normalizeMobileChainNodeID(id) != target || i+1 >= len(route) {
			continue
		}
		currentHop, _ := findMobileChainHopForNodeID(item, id)
		nextHop := findMobileChainHopForNode(item, route[i+1])
		externalPort := nextHop.ExternalPort
		if externalPort <= 0 {
			externalPort = nextHop.ListenPort
		}
		return strings.TrimSpace(nextHop.RelayHost), externalPort, normalizeMobileChainLinkLayer(firstMobileChainNonEmpty(nextHop.LinkLayer, item.LinkLayer)), normalizeMobileChainDialMode(currentHop.DialMode), "secret"
	}
	return "", 0, "", mobileChainDialNone, "none"
}

func resolveMobileChainPrevHop(item linkChainServerItem, nodeID string, role string) (host string, port int, layer string, dialMode string) {
	if role == mobileChainRoleEntry {
		return "", 0, "", mobileChainDialNone
	}
	route := buildMobileChainRoute(item)
	target := normalizeMobileChainNodeID(nodeID)
	for i, id := range route {
		if normalizeMobileChainNodeID(id) != target || i <= 0 {
			continue
		}
		prevHop := findMobileChainHopForNode(item, route[i-1])
		externalPort := prevHop.ExternalPort
		if externalPort <= 0 {
			externalPort = prevHop.ListenPort
		}
		return strings.TrimSpace(prevHop.RelayHost), externalPort, normalizeMobileChainLinkLayer(firstMobileChainNonEmpty(prevHop.LinkLayer, item.LinkLayer)), normalizeMobileChainDialMode(prevHop.DialMode)
	}
	return "", 0, "", mobileChainDialNone
}

func sleepMobileChainBackoff(stopCh <-chan struct{}, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-stopCh:
	case <-timer.C:
	}
}

func nextMobileChainBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return mobileChainBridgeRetryMin
	}
	next := current * 2
	if next > mobileChainBridgeRetryMax {
		next = mobileChainBridgeRetryMax
	}
	return next
}

func waitMobileChainBridgeSession(stopCh <-chan struct{}, session *yamux.Session) {
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

func newMobileChainYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.AcceptBacklog = 1024
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.MaxStreamWindowSize = 16 * 1024 * 1024
	return cfg
}

func newMobileChainQUICConfig() *quic.Config {
	return &quic.Config{
		Versions:                       []quic.Version{quic.Version2, quic.Version1},
		EnableDatagrams:                true,
		KeepAlivePeriod:                10 * time.Second,
		InitialStreamReceiveWindow:     mobileChainQUICInitialStreamWindow,
		MaxStreamReceiveWindow:         mobileChainQUICMaxStreamWindow,
		InitialConnectionReceiveWindow: mobileChainQUICInitialConnWindow,
		MaxConnectionReceiveWindow:     mobileChainQUICMaxConnWindow,
		MaxIncomingStreams:             1024,
	}
}

type mobileChainTCPListener struct {
	net.Listener
}

func (l *mobileChainTCPListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(mobileRelayTCPKeepAlivePeriod)
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetReadBuffer(mobileRelayTCPSocketBufferBytes)
		_ = tcp.SetWriteBuffer(mobileRelayTCPSocketBufferBytes)
	}
	return conn, nil
}

func generateMobileChainCertificate() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "android-probe-chain"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", "android-probe-chain"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
