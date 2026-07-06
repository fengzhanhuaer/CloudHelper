package mobilecore

// This file owns probe-to-probe route relays and custom framed transport.
// It is separate from the controller reporter session in mobilecore_reporter.go.

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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	mobileRouteRelayPath = "/api/node/route/relay"

	mobileRouteHeaderLegacyRouteID = "X-CH-Route-ID"
	mobileRouteHeaderRouteID       = "X-Codex-Route-Id"
	mobileRouteHeaderAuthMode      = "X-Codex-Auth-Mode"
	mobileRouteHeaderMAC           = "X-Codex-Mac"
	mobileRouteHeaderAuthTicket    = "X-Codex-User-Auth-Ticket"
	mobileRouteHeaderVersion       = "X-Codex-Api-Version"
	mobileRouteHeaderRelayMode     = "X-Codex-Relay-Mode"
	mobileRouteHeaderRelayRole     = "X-Codex-Relay-Role"
	mobileRouteHeaderSpeedBytes    = "X-Codex-Speed-Bytes"
	mobileRouteHeaderAuthTimestamp = "X-Codex-Auth-Timestamp"
	mobileRouteAuthPacketVersion   = "2025-03-22"

	mobileRouteRelayModeBridge    = "bridge"
	mobileRouteRelayModeStream    = "stream"
	mobileRouteRelayModeSpeedTest = "speed_test"
	mobileRouteRelayModePingPong  = "ping_pong"
	mobileRouteRelayModePrepare   = "prepare"

	mobileRouteBridgeRoleToNext = "to_next"
	mobileRouteBridgeRoleToPrev = "to_prev"

	mobileRouteRoleEntry     = "entry"
	mobileRouteRoleRelay     = "relay"
	mobileRouteRoleExit      = "exit"
	mobileRouteRoleEntryExit = "entry_exit"

	mobileRouteDialForward = "forward"
	mobileRouteDialReverse = "reverse"
	mobileRouteDialNone    = "none"

	mobileRouteNetworkTCP  = "tcp"
	mobileRouteNetworkUDP  = "udp"
	mobileRouteNetworkBoth = "both"

	mobileRouteEntrySideEntry = "route_entry"
	mobileRouteEntrySideExit  = "route_exit"

	mobileRouteOpenTimeout        = 15 * time.Second
	mobileRouteRelayHeaderTimeout = 5 * time.Second
	mobileRouteBridgeRetryMin     = time.Second
	mobileRouteBridgeRetryMax     = 30 * time.Second
	mobileRouteDialTimeout        = 12 * time.Second
	mobileRouteResponseTimeout    = 10 * time.Second
	mobileRouteUDPIdleTTL         = 90 * time.Second
	mobileRouteUDPGCInterval      = 15 * time.Second
	mobileRouteCopyBufferBytes    = 1024 * 1024
	mobileRouteFrameMaxPayload    = 64 * 1024
	mobileRouteWSBufferBytes      = 512 * 1024

	mobileRouteQUICInitialStreamWindow = 128 * 1024 * 1024
	mobileRouteQUICMaxStreamWindow     = 512 * 1024 * 1024
	mobileRouteQUICInitialConnWindow   = 512 * 1024 * 1024
	mobileRouteQUICMaxConnWindow       = 1024 * 1024 * 1024
)

var mobileRouteAuthTicketNow = time.Now

type mobileNodeIdentity struct {
	NodeID string
	Secret string
}

type mobileRouteRuntimeConfig struct {
	RouteID                 string
	Name                    string
	UserID                  string
	RawPublicKey            string
	UserPublicKey           ed25519.PublicKey
	Secret                  string
	AuthTicket              string
	Role                    string
	ListenHost              string
	ListenPort              int
	RouteLayer              string
	NextRouteLayer          string
	NextDialMode            string
	NextHost                string
	NextPort                int
	NextPreserveRelayDomain bool
	PrevHost                string
	PrevPort                int
	PrevRouteLayer          string
	PrevDialMode            string
	PrevPreserveRelayDomain bool
	RequireUserAuth         bool
	NextAuthMode            string
	Identity                mobileNodeIdentity
}

type mobileRouteRuntime struct {
	cfg                mobileRouteRuntimeConfig
	relayListenAddr    string
	downstreamSessions map[string]*mobileRouteBridgeSession
	upstreamSessions   map[string]*mobileRouteBridgeSession
	bridgeMu           sync.Mutex
	bridgeSeq          uint64
	stopCh             chan struct{}
}

type mobileRouteBridgeSession struct {
	ID      string
	Session *mobileRouteFrameSession
}

type mobileRouteSharedRelayServer struct {
	listenAddr    string
	routeIDs      map[string]struct{}
	refCount      int
	httpsServer   *http.Server
	http3Server   *http3.Server
	tcpListener   net.Listener
	udpPacketConn net.PacketConn
}

type mobileRouteBridgeDialTarget struct {
	Host                string
	Port                int
	RouteLayer          string
	RoleHeader          string
	PreserveRelayDomain bool
	AssignDownstream    bool
	AssignUpstream      bool
	AcceptStreams       bool
	Tag                 string
}

type mobileRouteTunnelOpenRequest struct {
	Type             string                          `json:"type"`
	Network          string                          `json:"network,omitempty"`
	Address          string                          `json:"address,omitempty"`
	FlowID           string                          `json:"flow_id,omitempty"`
	ResumeToken      string                          `json:"resume_token,omitempty"`
	ResumeEpoch      uint64                          `json:"resume_epoch,omitempty"`
	ReadOffset       uint64                          `json:"read_offset,omitempty"`
	WriteOffset      uint64                          `json:"write_offset,omitempty"`
	AppProtocol      string                          `json:"app_protocol,omitempty"`
	Priority         string                          `json:"priority,omitempty"`
	ResumePolicy     string                          `json:"resume_policy,omitempty"`
	LatencySensitive bool                            `json:"latency_sensitive,omitempty"`
	AssociationV2    *mobileRouteAssociationV2Config `json:"association_v2,omitempty"`
	PingBytes        int64                           `json:"ping_bytes,omitempty"`
}

type mobileRouteAssociationV2Config struct {
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

type mobileRouteTunnelOpenResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	FlowID      string `json:"flow_id,omitempty"`
	ResumeToken string `json:"resume_token,omitempty"`
	ResumeEpoch uint64 `json:"resume_epoch,omitempty"`
	ReadOffset  uint64 `json:"read_offset,omitempty"`
	WriteOffset uint64 `json:"write_offset,omitempty"`
}

type mobileRouteNetAddr struct{ label string }

func (a mobileRouteNetAddr) Network() string { return "mobile-route" }
func (a mobileRouteNetAddr) String() string  { return a.label }

type mobileRouteH3Conn struct {
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

func (c *mobileRouteH3Conn) Read(p []byte) (int, error)  { return c.stream.Read(p) }
func (c *mobileRouteH3Conn) Write(p []byte) (int, error) { return c.stream.Write(p) }
func (c *mobileRouteH3Conn) Close() error {
	if c.closeFn != nil {
		return c.closeFn()
	}
	return c.stream.Close()
}
func (c *mobileRouteH3Conn) LocalAddr() net.Addr  { return c.local }
func (c *mobileRouteH3Conn) RemoteAddr() net.Addr { return c.remote }
func (c *mobileRouteH3Conn) SetDeadline(t time.Time) error {
	if err := c.stream.SetReadDeadline(t); err != nil {
		return err
	}
	return c.stream.SetWriteDeadline(t)
}
func (c *mobileRouteH3Conn) SetReadDeadline(t time.Time) error  { return c.stream.SetReadDeadline(t) }
func (c *mobileRouteH3Conn) SetWriteDeadline(t time.Time) error { return c.stream.SetWriteDeadline(t) }

var mobileRouteRuntimeState = struct {
	mu       sync.Mutex
	runtimes map[string]*mobileRouteRuntime
}{runtimes: map[string]*mobileRouteRuntime{}}

var mobileRouteRuntimeLifecycleMu sync.Mutex

var mobileRouteSharedRelayState = struct {
	mu      sync.Mutex
	servers map[string]*mobileRouteSharedRelayServer
}{servers: map[string]*mobileRouteSharedRelayServer{}}

var mobileRouteCopyBufferPool = sync.Pool{New: func() any { return make([]byte, mobileRouteCopyBufferBytes) }}

var mobileRouteOpenBridgeStreamTimeout = mobileRouteOpenTimeout

func buildMobileRouteRuntimeConfig(cmd routeControlMessage) (mobileRouteRuntimeConfig, error) {
	routeID := strings.TrimSpace(cmd.RouteID)
	if routeID == "" {
		return mobileRouteRuntimeConfig{}, errors.New("route_id is required")
	}
	listenPort := normalizeMobileRoutePort(cmd.ListenPort)
	if listenPort <= 0 {
		listenPort = normalizeMobileRoutePort(cmd.InternalPort)
	}
	if listenPort <= 0 {
		return mobileRouteRuntimeConfig{}, errors.New("listen_port must be between 1 and 65535")
	}
	secret := strings.TrimSpace(cmd.RouteSecret)
	if secret == "" {
		return mobileRouteRuntimeConfig{}, errors.New("route_secret is required")
	}
	role := normalizeMobileRouteRole(cmd.Role)
	if role == "" {
		role = mobileRouteRoleRelay
	}
	nextAuthMode := normalizeMobileRouteAuthMode(cmd.NextAuthMode)
	nextHost := strings.TrimSpace(cmd.NextHost)
	nextPort := normalizeMobileRoutePort(cmd.NextPort)
	nextDialMode := normalizeMobileRouteDialMode(cmd.NextDialMode)
	if nextAuthMode != "route" {
		if nextHost == "" || nextPort <= 0 {
			return mobileRouteRuntimeConfig{}, errors.New("next_host and next_port are required")
		}
		if nextDialMode == mobileRouteDialNone {
			nextDialMode = mobileRouteDialForward
		}
	} else {
		nextDialMode = mobileRouteDialNone
	}
	prevHost := strings.TrimSpace(cmd.PrevHost)
	prevPort := normalizeMobileRoutePort(cmd.PrevPort)
	prevDialMode := normalizeMobileRouteDialMode(cmd.PrevDialMode)
	if prevHost == "" || prevPort <= 0 {
		prevDialMode = mobileRouteDialNone
	}
	preserveDomain := isMobileRouteControlCFEntry(cmd)
	cfg := mobileRouteRuntimeConfig{
		RouteID:                 routeID,
		Name:                    strings.TrimSpace(cmd.Name),
		UserID:                  strings.TrimSpace(cmd.UserID),
		RawPublicKey:            strings.TrimSpace(cmd.UserPublicKey),
		Secret:                  secret,
		AuthTicket:              strings.TrimSpace(cmd.AuthTicket),
		Role:                    role,
		ListenHost:              normalizeMobileRouteListenHost(cmd.ListenHost),
		ListenPort:              listenPort,
		RouteLayer:              normalizeMobileRouteRouteLayer(cmd.RouteLayer),
		NextRouteLayer:          normalizeMobileRouteRouteLayer(firstMobileRouteNonEmpty(cmd.NextRouteLayer, cmd.RouteLayer)),
		NextDialMode:            nextDialMode,
		NextHost:                nextHost,
		NextPort:                nextPort,
		NextPreserveRelayDomain: preserveDomain,
		PrevHost:                prevHost,
		PrevPort:                prevPort,
		PrevRouteLayer:          normalizeMobileRouteRouteLayer(firstMobileRouteNonEmpty(cmd.PrevRouteLayer, cmd.RouteLayer)),
		PrevDialMode:            prevDialMode,
		PrevPreserveRelayDomain: preserveDomain,
		RequireUserAuth:         cmd.RequireUserAuth,
		NextAuthMode:            nextAuthMode,
	}
	if cfg.RequireUserAuth {
		pub, err := parseMobileRouteUserPublicKey(cfg.RawPublicKey)
		if err != nil {
			return mobileRouteRuntimeConfig{}, fmt.Errorf("parse user_public_key failed: %w", err)
		}
		cfg.UserPublicKey = pub
	}
	return cfg, nil
}

func startMobileRouteRuntime(cfg mobileRouteRuntimeConfig) (*mobileRouteRuntime, error) {
	_ = stopMobileRouteRuntime(cfg.RouteID, "restart before apply")
	rt := &mobileRouteRuntime{
		cfg:                cfg,
		downstreamSessions: map[string]*mobileRouteBridgeSession{},
		upstreamSessions:   map[string]*mobileRouteBridgeSession{},
		stopCh:             make(chan struct{}),
	}
	if err := startMobileRoutePublicRelayServer(rt); err != nil {
		close(rt.stopCh)
		return nil, err
	}
	mobileRouteRuntimeState.mu.Lock()
	mobileRouteRuntimeState.runtimes[cfg.RouteID] = rt
	mobileRouteRuntimeState.mu.Unlock()
	startMobileRouteBridgeWorkers(rt)
	androidLogStore.add("route", "normal", "android probe route server started: route="+cfg.RouteID+" role="+cfg.Role)
	return rt, nil
}

func stopMobileRouteRuntime(routeID string, reason string) bool {
	id := strings.TrimSpace(routeID)
	if id == "" {
		return false
	}
	mobileRouteRuntimeState.mu.Lock()
	rt := mobileRouteRuntimeState.runtimes[id]
	if rt != nil {
		delete(mobileRouteRuntimeState.runtimes, id)
	}
	mobileRouteRuntimeState.mu.Unlock()
	if rt == nil {
		return false
	}
	close(rt.stopCh)
	rt.close()
	releaseMobileRouteSharedRelayServer(rt)
	androidLogStore.add("route", "normal", "android probe route server stopped: route="+id+" reason="+strings.TrimSpace(reason))
	return true
}

func stopAllMobileRouteRuntimes(reason string) int {
	mobileRouteRuntimeState.mu.Lock()
	ids := make([]string, 0, len(mobileRouteRuntimeState.runtimes))
	for id := range mobileRouteRuntimeState.runtimes {
		ids = append(ids, id)
	}
	mobileRouteRuntimeState.mu.Unlock()
	stopped := 0
	for _, id := range ids {
		if stopMobileRouteRuntime(id, reason) {
			stopped++
		}
	}
	return stopped
}

func applyMobileRouteRuntimesFromConfigDir(configDir string, identity mobileNodeIdentity) (int, error) {
	_ = configDir
	_ = identity
	return 0, nil
}

func getMobileRouteRuntime(routeID string) *mobileRouteRuntime {
	mobileRouteRuntimeState.mu.Lock()
	defer mobileRouteRuntimeState.mu.Unlock()
	return mobileRouteRuntimeState.runtimes[strings.TrimSpace(routeID)]
}

func (rt *mobileRouteRuntime) close() {
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
	rt.downstreamSessions = map[string]*mobileRouteBridgeSession{}
	rt.upstreamSessions = map[string]*mobileRouteBridgeSession{}
	rt.bridgeMu.Unlock()

}

func startMobileRoutePublicRelayServer(rt *mobileRouteRuntime) error {
	listenAddr := net.JoinHostPort(rt.cfg.ListenHost, strconv.Itoa(rt.cfg.ListenPort))
	if err := acquireMobileRouteSharedRelayServer(rt, listenAddr); err != nil {
		return err
	}
	rt.relayListenAddr = listenAddr
	return nil
}

func acquireMobileRouteSharedRelayServer(rt *mobileRouteRuntime, listenAddr string) error {
	routeID := strings.TrimSpace(rt.cfg.RouteID)
	mobileRouteSharedRelayState.mu.Lock()
	if shared := mobileRouteSharedRelayState.servers[listenAddr]; shared != nil {
		shared.routeIDs[routeID] = struct{}{}
		shared.refCount++
		mobileRouteSharedRelayState.mu.Unlock()
		return nil
	}
	mobileRouteSharedRelayState.mu.Unlock()

	shared, err := startMobileRouteSharedRelayServer(listenAddr)
	if err != nil {
		return err
	}
	shared.routeIDs[routeID] = struct{}{}
	shared.refCount = 1
	mobileRouteSharedRelayState.mu.Lock()
	if existing := mobileRouteSharedRelayState.servers[listenAddr]; existing != nil {
		mobileRouteSharedRelayState.mu.Unlock()
		closeMobileRouteSharedRelayServer(shared)
		return acquireMobileRouteSharedRelayServer(rt, listenAddr)
	}
	mobileRouteSharedRelayState.servers[listenAddr] = shared
	mobileRouteSharedRelayState.mu.Unlock()
	return nil
}

func startMobileRouteSharedRelayServer(listenAddr string) (*mobileRouteSharedRelayServer, error) {
	cert, err := generateMobileRouteCertificate()
	if err != nil {
		return nil, err
	}
	handler := http.NewServeMux()
	handler.HandleFunc(mobileRouteRelayPath, handleMobileRouteRelayDispatch)
	tcpListener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen route relay tcp failed: %w", err)
	}
	tcpListener = &mobileRouteTCPListener{Listener: tcpListener}
	udpPacketConn, err := net.ListenPacket("udp", listenAddr)
	if err != nil {
		_ = tcpListener.Close()
		return nil, fmt.Errorf("listen route relay udp failed: %w", err)
	}
	shared := &mobileRouteSharedRelayServer{listenAddr: listenAddr, routeIDs: map[string]struct{}{}, tcpListener: tcpListener, udpPacketConn: udpPacketConn}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	shared.httpsServer = &http.Server{Addr: listenAddr, Handler: handler, ReadHeaderTimeout: mobileRouteRelayHeaderTimeout}
	go func() {
		if err := shared.httpsServer.Serve(tls.NewListener(tcpListener, tlsConfig)); err != nil && err != http.ErrServerClosed {
			androidLogStore.add("route", "error", "android route websocket relay exited: "+err.Error())
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
		QUICConfig: newMobileRouteQUICConfig(),
	}
	go func() {
		if err := shared.http3Server.Serve(udpPacketConn); err != nil && err != http.ErrServerClosed {
			androidLogStore.add("route", "error", "android route h3 relay exited: "+err.Error())
		}
	}()
	return shared, nil
}

func releaseMobileRouteSharedRelayServer(rt *mobileRouteRuntime) {
	listenAddr := strings.TrimSpace(rt.relayListenAddr)
	if listenAddr == "" {
		return
	}
	var closing *mobileRouteSharedRelayServer
	mobileRouteSharedRelayState.mu.Lock()
	if shared := mobileRouteSharedRelayState.servers[listenAddr]; shared != nil {
		delete(shared.routeIDs, rt.cfg.RouteID)
		shared.refCount--
		if shared.refCount <= 0 {
			delete(mobileRouteSharedRelayState.servers, listenAddr)
			closing = shared
		}
	}
	mobileRouteSharedRelayState.mu.Unlock()
	closeMobileRouteSharedRelayServer(closing)
}

func closeMobileRouteSharedRelayServer(shared *mobileRouteSharedRelayServer) {
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

func handleMobileRouteRelayDispatch(w http.ResponseWriter, r *http.Request) {
	routeID := resolveMobileRouteIDFromRequest(r)
	rt := getMobileRouteRuntime(routeID)
	if rt == nil {
		http.Error(w, "route runtime not found", http.StatusNotFound)
		return
	}
	if err := verifyMobileRouteRelayRequestAuth(rt, r); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(r.Header.Get(mobileRouteHeaderRelayMode)))
	if mode == "" {
		mode = mobileRouteRelayModeBridge
	}
	role := normalizeMobileRouteBridgeRole(r.Header.Get(mobileRouteHeaderRelayRole))
	switch mode {
	case mobileRouteRelayModeBridge:
		if isMobileRouteH3Connect(r) {
			handleMobileRouteBridgeRelayH3(rt, role, w, r)
			return
		}
		handleMobileRouteBridgeRelayWebSocket(rt, role, w, r)
	case mobileRouteRelayModeStream, mobileRouteRelayModePingPong:
		if isMobileRouteH3Connect(r) {
			handleMobileRouteStreamRelayH3(rt, role, w, r)
			return
		}
		handleMobileRouteStreamRelayWebSocket(rt, role, w, r)
	case mobileRouteRelayModeSpeedTest:
		handleMobileRouteSpeedTest(w, r)
	default:
		http.Error(w, "unsupported relay mode", http.StatusBadRequest)
	}
}

func handleMobileRouteBridgeRelayWebSocket(rt *mobileRouteRuntime, role string, w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, ReadBufferSize: mobileRouteWSBufferBytes, WriteBufferSize: mobileRouteWSBufferBytes}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	handleMobileRouteBridgeRelayConn(rt, role, newWebSocketNetConn(ws))
}

func handleMobileRouteBridgeRelayH3(rt *mobileRouteRuntime, role string, w http.ResponseWriter, r *http.Request) {
	conn, ok := mobileRouteConnFromH3(w, r, "android-route-h3-bridge")
	if !ok {
		http.Error(w, "http3 stream unavailable", http.StatusInternalServerError)
		return
	}
	defer conn.Close()
	handleMobileRouteBridgeRelayConn(rt, role, conn)
}

func handleMobileRouteBridgeRelayConn(rt *mobileRouteRuntime, role string, conn net.Conn) {
	sessionID := rt.nextBridgeSessionID("inbound")
	session, err := newMobileRouteFrameServer(conn)
	if err != nil {
		return
	}
	if normalizeMobileRouteBridgeRole(role) == mobileRouteBridgeRoleToPrev {
		rt.setDownstreamSession(sessionID, session)
		go acceptMobileRouteBridgeStreams(rt, session, sessionID, "reverse")
		waitMobileRouteBridgeSession(rt.stopCh, session)
		rt.clearDownstreamSession(sessionID, session)
	} else {
		rt.setUpstreamSession(sessionID, session)
		go acceptMobileRouteBridgeStreams(rt, session, sessionID, "forward")
		waitMobileRouteBridgeSession(rt.stopCh, session)
		rt.clearUpstreamSession(sessionID, session)
	}
	_ = session.Close()
}

func handleMobileRouteStreamRelayWebSocket(rt *mobileRouteRuntime, role string, w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, ReadBufferSize: mobileRouteWSBufferBytes, WriteBufferSize: mobileRouteWSBufferBytes}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn := newWebSocketNetConn(ws)
	if normalizeMobileRouteBridgeRole(role) == mobileRouteBridgeRoleToPrev {
		handleMobileRouteReverseConn(rt, conn, "")
		return
	}
	handleMobileRouteConn(rt, conn, "")
}

func handleMobileRouteStreamRelayH3(rt *mobileRouteRuntime, role string, w http.ResponseWriter, r *http.Request) {
	conn, ok := mobileRouteConnFromH3(w, r, "android-route-h3-stream")
	if !ok {
		http.Error(w, "http3 stream unavailable", http.StatusInternalServerError)
		return
	}
	if normalizeMobileRouteBridgeRole(role) == mobileRouteBridgeRoleToPrev {
		handleMobileRouteReverseConn(rt, conn, "")
		return
	}
	handleMobileRouteConn(rt, conn, "")
}

func handleMobileRouteSpeedTest(w http.ResponseWriter, r *http.Request) {
	byteCount := int64(128 * 1024 * 1024)
	if raw := strings.TrimSpace(r.Header.Get(mobileRouteHeaderSpeedBytes)); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 && parsed <= 256*1024*1024 {
			byteCount = parsed
		}
	}
	var conn net.Conn
	if isMobileRouteH3Connect(r) {
		h3Conn, ok := mobileRouteConnFromH3(w, r, "android-route-h3-speed")
		if !ok {
			http.Error(w, "http3 stream unavailable", http.StatusInternalServerError)
			return
		}
		conn = h3Conn
	} else {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, ReadBufferSize: mobileRouteWSBufferBytes, WriteBufferSize: mobileRouteWSBufferBytes}
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

func startMobileRouteBridgeWorkers(rt *mobileRouteRuntime) {
	cfg := rt.cfg
	if cfg.NextAuthMode != "route" && normalizeMobileRouteDialMode(cfg.NextDialMode) == mobileRouteDialForward {
		go runMobileRouteBridgeDialLoop(rt, mobileRouteBridgeDialTarget{
			Host:                cfg.NextHost,
			Port:                cfg.NextPort,
			RouteLayer:          firstMobileRouteNonEmpty(cfg.NextRouteLayer, cfg.RouteLayer),
			RoleHeader:          mobileRouteBridgeRoleToNext,
			PreserveRelayDomain: cfg.NextPreserveRelayDomain,
			AssignDownstream:    true,
			Tag:                 "downstream-forward",
		})
	}
	if normalizeMobileRouteDialMode(cfg.PrevDialMode) == mobileRouteDialReverse && cfg.PrevHost != "" && cfg.PrevPort > 0 {
		go runMobileRouteBridgeDialLoop(rt, mobileRouteBridgeDialTarget{
			Host:                cfg.PrevHost,
			Port:                cfg.PrevPort,
			RouteLayer:          cfg.PrevRouteLayer,
			RoleHeader:          mobileRouteBridgeRoleToPrev,
			PreserveRelayDomain: cfg.PrevPreserveRelayDomain,
			AssignUpstream:      true,
			AcceptStreams:       true,
			Tag:                 "upstream-reverse",
		})
	}
}

func runMobileRouteBridgeDialLoop(rt *mobileRouteRuntime, target mobileRouteBridgeDialTarget) {
	backoff := mobileRouteBridgeRetryMin
	for {
		select {
		case <-rt.stopCh:
			return
		default:
		}
		conn, err := openMobileRouteRelayBridgeConn(rt.cfg, target)
		if err != nil {
			androidLogStore.add("route", "warn", "android route bridge dial failed: route="+rt.cfg.RouteID+" tag="+target.Tag+" err="+err.Error())
			sleepMobileRouteBackoff(rt.stopCh, backoff)
			backoff = nextMobileRouteBackoff(backoff)
			continue
		}
		session, err := newMobileRouteFrameClient(conn)
		if err != nil {
			_ = conn.Close()
			sleepMobileRouteBackoff(rt.stopCh, backoff)
			backoff = nextMobileRouteBackoff(backoff)
			continue
		}
		ready := session.WaitReady(500 * time.Millisecond)
		androidLogStore.add("route", "debug", "android route bridge frame session ready: route="+rt.cfg.RouteID+" tag="+target.Tag+" ready="+strconv.FormatBool(ready))
		sessionID := rt.nextBridgeSessionID(target.Tag)
		backoff = mobileRouteBridgeRetryMin
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
			go acceptMobileRouteBridgeStreams(rt, session, sessionID, direction)
		}
		waitMobileRouteBridgeSession(rt.stopCh, session)
		if target.AssignDownstream {
			rt.clearDownstreamSession(sessionID, session)
		}
		if target.AssignUpstream {
			rt.clearUpstreamSession(sessionID, session)
		}
		_ = session.Close()
		_ = conn.Close()
		sleepMobileRouteBackoff(rt.stopCh, backoff)
		backoff = nextMobileRouteBackoff(backoff)
	}
}

func acceptMobileRouteBridgeStreams(rt *mobileRouteRuntime, session *mobileRouteFrameSession, sessionID string, direction string) {
	for {
		stream, err := session.Accept()
		if err != nil {
			return
		}
		if strings.EqualFold(direction, "reverse") {
			go handleMobileRouteReverseConn(rt, stream, sessionID)
		} else {
			go handleMobileRouteConn(rt, stream, sessionID)
		}
	}
}

func handleMobileRouteConn(rt *mobileRouteRuntime, conn net.Conn, preferredSessionID string) {
	defer conn.Close()
	if frameStream, ok := conn.(*mobileRouteFrameStream); ok {
		if rt.cfg.NextAuthMode == "route" {
			_ = handleMobileRouteRouteStream(conn)
			return
		}
		req := mobileRouteOpenRequestFromStream(frameStream)
		next, err := openMobileRouteDownstreamStream(rt, preferredSessionID, mobileRouteOpenTimeout, req)
		if err != nil {
			return
		}
		defer next.Close()
		relayMobileRouteBidirectional(conn, next)
		return
	}
	androidLogStore.add("route", "warning", "rejected non-frame downstream stream: route="+rt.cfg.RouteID)
}

func handleMobileRouteReverseConn(rt *mobileRouteRuntime, conn net.Conn, preferredSessionID string) {
	defer conn.Close()
	if frameStream, ok := conn.(*mobileRouteFrameStream); ok {
		role := normalizeMobileRouteRole(rt.cfg.Role)
		if role == mobileRouteRoleEntry || role == mobileRouteRoleEntryExit {
			_ = handleMobileRouteRouteStream(conn)
			return
		}
		req := mobileRouteOpenRequestFromStream(frameStream)
		prev, err := openMobileRouteUpstreamStream(rt, preferredSessionID, mobileRouteOpenTimeout, req)
		if err != nil {
			return
		}
		defer prev.Close()
		relayMobileRouteBidirectional(conn, prev)
		return
	}
	androidLogStore.add("route", "warning", "rejected non-frame upstream stream: route="+rt.cfg.RouteID)
}

func handleMobileRouteRouteStream(stream net.Conn) error {
	var req mobileRouteTunnelOpenRequest
	var responder func(mobileRouteTunnelOpenResponse) error
	frameStream, ok := stream.(*mobileRouteFrameStream)
	if !ok {
		return errors.New("non-frame mobile route stream is unsupported")
	}
	var found bool
	req, found = frameStream.MobileOpenRequest()
	if !found {
		linkReq, linkFound := frameStream.OpenRequest()
		if !linkFound {
			return errors.New("missing mobile frame open request")
		}
		req = mobileRouteRequestFromLinkRequest(linkReq)
		responder = func(resp mobileRouteTunnelOpenResponse) error {
			return frameStream.RespondOpen(routeTunnelOpenResponse{OK: resp.OK, Error: resp.Error})
		}
	} else {
		responder = frameStream.RespondMobileOpen
	}
	if strings.EqualFold(strings.TrimSpace(req.Type), mobileRouteRelayModePingPong) {
		return handleMobileRoutePingPong(stream, req.PingBytes, responder)
	}
	if strings.EqualFold(strings.TrimSpace(req.Type), mobileRouteRelayModePrepare) {
		if err := responder(mobileRouteTunnelOpenResponse{OK: true}); err != nil {
			return err
		}
		updateReq, err := frameStream.WaitOpenUpdate(mobileRouteUDPIdleTTL + mobileRouteResponseTimeout)
		if err != nil {
			return err
		}
		req = mobileRouteRequestFromLinkRequest(updateReq)
		responder = func(resp mobileRouteTunnelOpenResponse) error {
			return frameStream.RespondOpenUpdate(routeTunnelOpenResponse{OK: resp.OK, Error: resp.Error})
		}
	}
	network := strings.ToLower(strings.TrimSpace(req.Network))
	if network == "" {
		network = mobileRouteNetworkTCP
	}
	target := strings.TrimSpace(req.Address)
	if target == "" {
		return responder(mobileRouteTunnelOpenResponse{OK: false, Error: "missing address"})
	}
	switch network {
	case mobileRouteNetworkTCP:
		return handleMobileRouteTunnelTCP(stream, target, responder)
	case mobileRouteNetworkUDP:
		return handleMobileRouteTunnelUDP(stream, target, responder)
	default:
		return responder(mobileRouteTunnelOpenResponse{OK: false, Error: "unsupported network"})
	}
}

func mobileRouteOpenRequestFromStream(stream *mobileRouteFrameStream) mobileRouteTunnelOpenRequest {
	if stream == nil {
		return mobileRouteTunnelOpenRequest{Type: "open", Network: mobileRouteNetworkTCP, Priority: "normal"}
	}
	if req, found := stream.MobileOpenRequest(); found {
		if strings.TrimSpace(req.Type) == "" {
			req.Type = "open"
		}
		if strings.TrimSpace(req.Network) == "" {
			req.Network = mobileRouteNetworkTCP
		}
		if strings.TrimSpace(req.Priority) == "" {
			req.Priority = mobileRoutePriorityForRequest(req)
		}
		return req
	}
	return mobileRouteTunnelOpenRequest{Type: "open", Network: mobileRouteNetworkTCP, Priority: "normal"}
}

func mobileRouteRequestFromLinkRequest(req routeTunnelOpenRequest) mobileRouteTunnelOpenRequest {
	return mobileRouteTunnelOpenRequest{
		Type:             req.Type,
		Network:          req.Network,
		Address:          req.Address,
		FlowID:           req.FlowID,
		ResumeToken:      req.ResumeToken,
		ResumeEpoch:      req.ResumeEpoch,
		ReadOffset:       req.ReadOffset,
		WriteOffset:      req.WriteOffset,
		AppProtocol:      req.AppProtocol,
		Priority:         req.Priority,
		ResumePolicy:     req.ResumePolicy,
		LatencySensitive: req.LatencySensitive,
		AssociationV2:    mobileRouteAssociationFromRouteAssociation(req.AssociationV2),
		PingBytes:        req.PingBytes,
	}
}

func mobileRouteAssociationFromRouteAssociation(meta *routeAssociationV2Meta) *mobileRouteAssociationV2Config {
	if meta == nil {
		return nil
	}
	return &mobileRouteAssociationV2Config{
		Version:         meta.Version,
		Transport:       strings.TrimSpace(meta.Transport),
		RouteTarget:     strings.TrimSpace(meta.RouteTarget),
		NATMode:         strings.TrimSpace(meta.NATMode),
		TTLProfile:      strings.TrimSpace(meta.TTLProfile),
		IdleTimeoutMS:   meta.IdleTimeoutMS,
		GCIntervalMS:    meta.GCIntervalMS,
		CreatedAtUnixMS: meta.CreatedAtUnixMS,
		AssocKeyV2:      strings.TrimSpace(meta.AssocKeyV2),
		FlowID:          strings.TrimSpace(meta.FlowID),
	}
}

func mobileRoutePriorityForRequest(req mobileRouteTunnelOpenRequest) string {
	if req.AssociationV2 != nil && strings.EqualFold(strings.TrimSpace(req.AssociationV2.Transport), mobileRouteNetworkUDP) {
		return "realtime"
	}
	if strings.EqualFold(strings.TrimSpace(req.Network), mobileRouteNetworkUDP) {
		return "realtime"
	}
	if isMobileRouteRealtimeTCPPort(req.Address) {
		return "realtime"
	}
	return "normal"
}

func mobileRouteAppProtocolForRequest(network string, targetAddr string, association *mobileRouteAssociationV2Config) string {
	if association != nil && strings.EqualFold(strings.TrimSpace(association.Transport), mobileRouteNetworkUDP) {
		return "udp-association"
	}
	if strings.EqualFold(strings.TrimSpace(network), mobileRouteNetworkUDP) {
		return "udp-association"
	}
	switch port := mobileRouteTargetPort(targetAddr); {
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

func mobileRouteResumePolicyForRequest(network string, association *mobileRouteAssociationV2Config) string {
	if association != nil && strings.EqualFold(strings.TrimSpace(association.Transport), mobileRouteNetworkUDP) {
		return "rebind"
	}
	if strings.EqualFold(strings.TrimSpace(network), mobileRouteNetworkUDP) {
		return "rebind"
	}
	return "replay_required"
}

func mobileRouteLatencySensitiveForRequest(network string, targetAddr string, association *mobileRouteAssociationV2Config) bool {
	return mobileRoutePriorityForRequest(mobileRouteTunnelOpenRequest{Network: network, Address: targetAddr, AssociationV2: association}) == "realtime"
}

func isMobileRouteRealtimeTCPPort(targetAddr string) bool {
	switch mobileRouteTargetPort(targetAddr) {
	case 22, 3389, 4000:
		return true
	default:
		port := mobileRouteTargetPort(targetAddr)
		return port >= 5900 && port <= 5999
	}
}

func mobileRouteTargetPort(targetAddr string) int {
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

func handleMobileRoutePingPong(stream net.Conn, byteCount int64, responder func(mobileRouteTunnelOpenResponse) error) error {
	if byteCount <= 0 || byteCount > 64*1024 {
		byteCount = 64
	}
	if responder == nil {
		return errors.New("missing mobile frame open responder")
	}
	if err := responder(mobileRouteTunnelOpenResponse{OK: true}); err != nil {
		return err
	}
	buf := make([]byte, byteCount)
	if _, err := io.ReadFull(stream, buf); err != nil {
		return err
	}
	_, err := stream.Write(buf)
	return err
}

func handleMobileRouteTunnelTCP(stream net.Conn, target string, responder func(mobileRouteTunnelOpenResponse) error) error {
	if responder == nil {
		return errors.New("missing mobile frame open responder")
	}
	remote, err := net.DialTimeout("tcp", target, mobileRouteDialTimeout)
	if err != nil {
		_ = responder(mobileRouteTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	defer remote.Close()
	if err := responder(mobileRouteTunnelOpenResponse{OK: true}); err != nil {
		return err
	}
	relayMobileRouteBidirectional(stream, remote)
	return nil
}

func handleMobileRouteTunnelUDP(stream net.Conn, target string, responder func(mobileRouteTunnelOpenResponse) error) error {
	if responder == nil {
		return errors.New("missing mobile frame open responder")
	}
	remote, err := net.DialTimeout("udp", target, mobileRouteDialTimeout)
	if err != nil {
		_ = responder(mobileRouteTunnelOpenResponse{OK: false, Error: err.Error()})
		return err
	}
	defer remote.Close()
	if err := responder(mobileRouteTunnelOpenResponse{OK: true}); err != nil {
		return err
	}
	reader := bufio.NewReader(stream)
	errCh := make(chan error, 2)
	go func() {
		buf := make([]byte, mobileRouteFrameMaxPayload)
		for {
			n, err := readMobileRouteFramedPacketInto(reader, buf)
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
		buf := make([]byte, mobileRouteFrameMaxPayload)
		for {
			n, err := remote.Read(buf)
			if n > 0 {
				if writeErr := writeMobileRouteFramedPacket(stream, buf[:n]); writeErr != nil {
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

func openMobileRouteRelayBridgeConn(cfg mobileRouteRuntimeConfig, target mobileRouteBridgeDialTarget) (net.Conn, error) {
	protocols := mobileRouteRelayProtocolCandidates(target.RouteLayer)
	var lastErr error
	for _, protocol := range protocols {
		conn, err := openMobileRouteRelayBridgeConnProtocol(cfg, target, protocol)
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

func openMobileRouteRelayBridgeConnProtocol(cfg mobileRouteRuntimeConfig, target mobileRouteBridgeDialTarget, protocol string) (net.Conn, error) {
	switch normalizeMobileRouteRouteLayer(protocol) {
	case "websocket-h3":
		return openMobileRouteRelayBridgeH3Conn(cfg, target)
	default:
		return openMobileRouteRelayBridgeWebSocketConn(cfg, target)
	}
}

func openMobileRouteRelayBridgeWebSocketConn(cfg mobileRouteRuntimeConfig, target mobileRouteBridgeDialTarget) (net.Conn, error) {
	dialHost, hostHeader, err := resolveMobileRouteDialHost(target.Host, target.PreserveRelayDomain)
	if err != nil {
		return nil, err
	}
	relayURL := buildMobileRouteRelayURL("wss", hostHeader, target.Port, cfg.RouteID)
	header := buildMobileRouteRelayHeaders(cfg, mobileRouteRelayModeBridge, target.RoleHeader, 0)
	dialHostPort := net.JoinHostPort(dialHost, strconv.Itoa(target.Port))
	dialer := websocket.Dialer{
		HandshakeTimeout:  mobileRouteOpenTimeout,
		Proxy:             nil,
		ReadBufferSize:    mobileRouteWSBufferBytes,
		WriteBufferSize:   mobileRouteWSBufferBytes,
		EnableCompression: false,
		NetDialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: mobileRouteOpenTimeout}
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

func openMobileRouteRelayBridgeH3Conn(cfg mobileRouteRuntimeConfig, target mobileRouteBridgeDialTarget) (net.Conn, error) {
	dialHost, hostHeader, err := resolveMobileRouteDialHost(target.Host, target.PreserveRelayDomain)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mobileRouteOpenTimeout)
	dialHostPort := net.JoinHostPort(dialHost, strconv.Itoa(target.Port))
	quicConn, err := quic.DialAddr(ctx, dialHostPort, &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{http3.NextProtoH3}, ServerName: dialHost, InsecureSkipVerify: true}, newMobileRouteQUICConfig())
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
	reqURL := buildMobileRouteRelayURL("https", hostHeader, target.Port, cfg.RouteID)
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
	req.Header = buildMobileRouteRelayHeaders(cfg, mobileRouteRelayModeBridge, target.RoleHeader, 0)
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
	return &mobileRouteH3Conn{
		stream: stream,
		local:  mobileRouteNetAddr{label: "android-route-h3-local"},
		remote: mobileRouteNetAddr{label: dialHostPort},
		closeFn: func() error {
			cancelOnce.Do(func() {
				cancel()
				_ = quicConn.CloseWithError(0, "closed")
			})
			return stream.Close()
		},
	}, nil
}

func openMobileRouteDownstreamStream(rt *mobileRouteRuntime, sessionID string, timeout time.Duration, request mobileRouteTunnelOpenRequest) (net.Conn, error) {
	return openMobileRouteSessionStream(rt, true, sessionID, timeout, request)
}

func openMobileRouteUpstreamStream(rt *mobileRouteRuntime, sessionID string, timeout time.Duration, request mobileRouteTunnelOpenRequest) (net.Conn, error) {
	return openMobileRouteSessionStream(rt, false, sessionID, timeout, request)
}

func openMobileRouteSessionStream(rt *mobileRouteRuntime, downstream bool, sessionID string, timeout time.Duration, request mobileRouteTunnelOpenRequest) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		var session *mobileRouteFrameSession
		if downstream {
			session = rt.getDownstreamSession(sessionID)
		} else {
			session = rt.getUpstreamSession(sessionID)
		}
		if session != nil && !session.IsClosed() {
			stream, err := session.OpenWithMobileRequest(request, timeout)
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

func (rt *mobileRouteRuntime) nextBridgeSessionID(prefix string) string {
	seq := atomic.AddUint64(&rt.bridgeSeq, 1)
	return strings.TrimSpace(prefix) + "-" + strconv.FormatUint(seq, 10) + "-" + strings.ToLower(randomHexToken(4))
}

func (rt *mobileRouteRuntime) setDownstreamSession(id string, session *mobileRouteFrameSession) {
	rt.bridgeMu.Lock()
	rt.downstreamSessions[id] = &mobileRouteBridgeSession{ID: id, Session: session}
	rt.bridgeMu.Unlock()
}

func (rt *mobileRouteRuntime) setUpstreamSession(id string, session *mobileRouteFrameSession) {
	rt.bridgeMu.Lock()
	rt.upstreamSessions[id] = &mobileRouteBridgeSession{ID: id, Session: session}
	rt.bridgeMu.Unlock()
}

func (rt *mobileRouteRuntime) clearDownstreamSession(id string, session *mobileRouteFrameSession) {
	rt.bridgeMu.Lock()
	for key, item := range rt.downstreamSessions {
		if (strings.TrimSpace(id) == "" || key == id) && item != nil && item.Session == session {
			delete(rt.downstreamSessions, key)
		}
	}
	rt.bridgeMu.Unlock()
}

func (rt *mobileRouteRuntime) clearUpstreamSession(id string, session *mobileRouteFrameSession) {
	rt.bridgeMu.Lock()
	for key, item := range rt.upstreamSessions {
		if (strings.TrimSpace(id) == "" || key == id) && item != nil && item.Session == session {
			delete(rt.upstreamSessions, key)
		}
	}
	rt.bridgeMu.Unlock()
}

func (rt *mobileRouteRuntime) getDownstreamSession(id string) *mobileRouteFrameSession {
	return rt.getSession(rt.downstreamSessions, id)
}

func (rt *mobileRouteRuntime) getUpstreamSession(id string) *mobileRouteFrameSession {
	return rt.getSession(rt.upstreamSessions, id)
}

func (rt *mobileRouteRuntime) getSession(items map[string]*mobileRouteBridgeSession, id string) *mobileRouteFrameSession {
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

func verifyMobileRouteRelayRequestAuth(rt *mobileRouteRuntime, r *http.Request) error {
	if resolveMobileRouteIDFromRequest(r) != rt.cfg.RouteID {
		return errors.New("route id mismatch")
	}
	nonce := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if nonce == "" {
		return errors.New("nonce is required")
	}
	gotMAC := strings.TrimSpace(r.Header.Get(mobileRouteHeaderMAC))
	if gotMAC == "" {
		return errors.New("mac is required")
	}
	expected := mobileRouteHMAC(rt.cfg.Secret, rt.cfg.RouteID, nonce)
	if !hmac.Equal([]byte(strings.ToLower(gotMAC)), []byte(strings.ToLower(expected))) {
		return errors.New("authentication failed")
	}
	rawTicket := strings.TrimSpace(r.Header.Get(mobileRouteHeaderAuthTicket))
	if rt.cfg.RequireUserAuth {
		if err := verifyMobileRouteUserAuthTicket(rt.cfg, rawTicket); err != nil {
			return err
		}
	} else if ticket := strings.TrimSpace(rt.cfg.AuthTicket); ticket != "" && rawTicket != ticket {
		return errors.New("auth ticket mismatch")
	}
	return nil
}

type mobileRouteUserAuthTicketPayload struct {
	Version       string `json:"version"`
	RouteID       string `json:"route_id"`
	ClientEntryID string `json:"client_entry_id,omitempty"`
	UserID        string `json:"user_id"`
	UserPublicKey string `json:"user_public_key"`
	IssuedAt      string `json:"issued_at"`
}

func verifyMobileRouteUserAuthTicket(cfg mobileRouteRuntimeConfig, rawTicket string) error {
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
	var payload mobileRouteUserAuthTicketPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return errors.New("invalid user auth ticket payload json")
	}
	if strings.TrimSpace(payload.Version) != "route-auth-v1" {
		return errors.New("unsupported user auth ticket version")
	}
	if strings.TrimSpace(payload.RouteID) != strings.TrimSpace(cfg.RouteID) {
		return errors.New("user auth ticket route mismatch")
	}
	if strings.TrimSpace(payload.UserPublicKey) != strings.TrimSpace(cfg.RawPublicKey) {
		return errors.New("user auth ticket public key mismatch")
	}
	if err := verifyMobileRouteAuthTicketIssuedAt(payload.IssuedAt, mobileRouteAuthTicketNow()); err != nil {
		return err
	}
	return nil
}

func verifyMobileRouteAuthTicketIssuedAt(raw string, now time.Time) error {
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

func parseMobileRouteUserPublicKey(raw string) (ed25519.PublicKey, error) {
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

func buildMobileRouteRelayHeaders(cfg mobileRouteRuntimeConfig, mode string, role string, speedBytes int64) http.Header {
	nonce := randomHexToken(16)
	header := http.Header{}
	header.Set(mobileRouteHeaderLegacyRouteID, cfg.RouteID)
	header.Set(mobileRouteHeaderRouteID, cfg.RouteID)
	header.Set(mobileRouteHeaderVersion, mobileRouteAuthPacketVersion)
	header.Set(mobileRouteHeaderRelayMode, strings.TrimSpace(mode))
	header.Set(mobileRouteHeaderRelayRole, normalizeMobileRouteBridgeRole(role))
	header.Set(mobileRouteHeaderAuthMode, "secret_hmac")
	header.Set(mobileRouteHeaderAuthTimestamp, time.Now().UTC().Format(time.RFC3339Nano))
	header.Set("Authorization", "Bearer "+nonce)
	header.Set(mobileRouteHeaderMAC, mobileRouteHMAC(cfg.Secret, cfg.RouteID, nonce))
	if cfg.AuthTicket != "" {
		header.Set(mobileRouteHeaderAuthTicket, cfg.AuthTicket)
	}
	if speedBytes > 0 {
		header.Set(mobileRouteHeaderSpeedBytes, strconv.FormatInt(speedBytes, 10))
	}
	return header
}

func mobileRouteHMAC(secret string, routeID string, nonce string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(strings.TrimSpace(routeID)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(nonce)))
	return hex.EncodeToString(mac.Sum(nil))
}

func resolveMobileRouteIDFromRequest(r *http.Request) string {
	for _, key := range []string{mobileRouteHeaderRouteID, mobileRouteHeaderLegacyRouteID} {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			return value
		}
	}
	if r != nil && r.URL != nil {
		return strings.TrimSpace(r.URL.Query().Get("route_id"))
	}
	return ""
}

func isMobileRouteH3Connect(r *http.Request) bool {
	return r != nil && r.Method == http.MethodConnect && r.ProtoMajor == 3 && strings.EqualFold(strings.TrimSpace(r.Proto), "websocket")
}

func mobileRouteConnFromH3(w http.ResponseWriter, r *http.Request, label string) (net.Conn, bool) {
	streamer, ok := w.(http3.HTTPStreamer)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	stream := streamer.HTTPStream()
	return &mobileRouteH3Conn{
		stream: stream,
		local:  mobileRouteNetAddr{label: label},
		remote: mobileRouteNetAddr{label: strings.TrimSpace(r.RemoteAddr)},
		closeFn: func() error {
			return stream.Close()
		},
	}, true
}

func relayMobileRouteBidirectional(left net.Conn, right net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = mobileRouteCopy(right, left)
		closeMobileRouteWrite(right)
	}()
	go func() {
		defer wg.Done()
		_, _ = mobileRouteCopy(left, right)
		closeMobileRouteWrite(left)
	}()
	wg.Wait()
}

func relayMobileRouteDuplex(leftReader io.Reader, rightWriter net.Conn, rightReader io.Reader, leftWriter net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = mobileRouteCopy(rightWriter, leftReader)
		closeMobileRouteWrite(rightWriter)
	}()
	go func() {
		defer wg.Done()
		_, _ = mobileRouteCopy(leftWriter, rightReader)
		closeMobileRouteWrite(leftWriter)
	}()
	wg.Wait()
}

func mobileRouteCopy(dst io.Writer, src io.Reader) (int64, error) {
	buf, _ := mobileRouteCopyBufferPool.Get().([]byte)
	if len(buf) == 0 {
		buf = make([]byte, mobileRouteCopyBufferBytes)
	}
	defer mobileRouteCopyBufferPool.Put(buf)
	return io.CopyBuffer(dst, src, buf)
}

func closeMobileRouteWrite(conn net.Conn) {
	if closer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
		return
	}
	if _, ok := conn.(*mobileRouteFrameStream); ok {
		_ = conn.Close()
	}
}

func readMobileRouteFramedPacket(reader *bufio.Reader) ([]byte, error) {
	buf := make([]byte, mobileRouteFrameMaxPayload)
	n, err := readMobileRouteFramedPacketInto(reader, buf)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), buf[:n]...), nil
}

func readMobileRouteFramedPacketInto(reader *bufio.Reader, payload []byte) (int, error) {
	frame, err := readMobileRouteFrame(reader)
	if err != nil {
		return 0, err
	}
	if frame.Kind != mobileRouteFrameKindData {
		return 0, fmt.Errorf("invalid framed packet kind")
	}
	if len(frame.Control) != 0 {
		return 0, fmt.Errorf("invalid framed packet control")
	}
	if len(frame.Data) == 0 {
		return 0, nil
	}
	if len(frame.Data) > len(payload) {
		return 0, fmt.Errorf("udp frame too large: %d", len(frame.Data))
	}
	copy(payload, frame.Data)
	return len(frame.Data), nil
}

func writeMobileRouteFramedPacket(writer io.Writer, payload []byte) error {
	if len(payload) > mobileRouteFrameMaxPayload {
		return fmt.Errorf("udp frame too large: %d", len(payload))
	}
	frame, _ := mobileRouteFrameBufferPool.Get().([]byte)
	defer mobileRouteFrameBufferPool.Put(frame[:cap(frame)])
	encoded, err := encodeMobileRouteFrame(mobileRouteFrame{Kind: mobileRouteFrameKindData, Data: payload}, frame)
	if err != nil {
		return err
	}
	return writeAll(writer, encoded)
}

func resolveMobileRouteDialHost(rawHost string, preserveDomain bool) (string, string, error) {
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

func buildMobileRouteRelayURL(scheme string, host string, port int, routeID string) string {
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port)), Path: mobileRouteRelayPath}
	q := u.Query()
	q.Set("route_id", strings.TrimSpace(routeID))
	u.RawQuery = q.Encode()
	return u.String()
}

func firstMobileRouteNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeMobileRouteRole(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case mobileRouteRoleEntry:
		return mobileRouteRoleEntry
	case mobileRouteRoleRelay:
		return mobileRouteRoleRelay
	case mobileRouteRoleExit:
		return mobileRouteRoleExit
	case mobileRouteRoleEntryExit:
		return mobileRouteRoleEntryExit
	default:
		return ""
	}
}

func normalizeMobileRouteAuthMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "route":
		return "route"
	case "secret", "hmac":
		return "secret"
	default:
		return "none"
	}
}

func normalizeMobileRouteDialMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case mobileRouteDialReverse, "rev":
		return mobileRouteDialReverse
	case mobileRouteDialNone:
		return mobileRouteDialNone
	default:
		return mobileRouteDialForward
	}
}

func normalizeMobileRouteBridgeRole(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), mobileRouteBridgeRoleToPrev) {
		return mobileRouteBridgeRoleToPrev
	}
	return mobileRouteBridgeRoleToNext
}

func normalizeMobileRouteRouteLayer(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "websocket", "ws", "wss":
		return "websocket"
	case "websocket-h3", "ws-h3", "h3-websocket", "h3-ws":
		return "websocket-h3"
	default:
		return ""
	}
}

func mobileRouteRelayProtocolCandidates(layer string) []string {
	switch normalizeMobileRouteRouteLayer(layer) {
	case "websocket":
		return []string{"websocket"}
	case "websocket-h3":
		return []string{"websocket-h3"}
	default:
		return []string{"websocket-h3", "websocket"}
	}
}

func normalizeMobileRouteListenHost(raw string) string {
	if host := strings.TrimSpace(raw); host != "" {
		return host
	}
	return "0.0.0.0"
}

func normalizeMobileRoutePort(port int) int {
	if port <= 0 || port > 65535 {
		return 0
	}
	return port
}

func isMobileRouteControlCFEntry(cmd routeControlMessage) bool {
	for _, value := range []string{cmd.ClientEntryType, cmd.ClientEntryID, cmd.RouteID, cmd.Name} {
		clean := strings.ToLower(strings.TrimSpace(value))
		if clean == "cf" || strings.HasSuffix(clean, "_cf") {
			return true
		}
	}
	return false
}

func normalizeMobileRouteNodeID(raw string) string {
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

func buildMobileRouteRoute(item routeServerItem) []string {
	route := make([]string, 0, 2+len(item.CascadeNodeIDs))
	seen := map[string]struct{}{}
	push := func(raw string) {
		id := normalizeMobileRouteNodeID(raw)
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

func resolveMobileRouteNodeRole(item routeServerItem, nodeID string) string {
	target := normalizeMobileRouteNodeID(nodeID)
	if target == "" {
		return ""
	}
	entry := normalizeMobileRouteNodeID(item.EntryNodeID)
	exit := normalizeMobileRouteNodeID(item.ExitNodeID)
	isEntry := entry != "" && target == entry
	isExit := exit != "" && target == exit
	if isEntry && isExit {
		return mobileRouteRoleEntryExit
	}
	if isEntry {
		return mobileRouteRoleEntry
	}
	if isExit {
		return mobileRouteRoleExit
	}
	route := buildMobileRouteRoute(item)
	if len(route) > 0 {
		if target == normalizeMobileRouteNodeID(route[0]) {
			return mobileRouteRoleEntry
		}
		if target == normalizeMobileRouteNodeID(route[len(route)-1]) {
			return mobileRouteRoleExit
		}
	}
	for _, id := range item.CascadeNodeIDs {
		if normalizeMobileRouteNodeID(id) == target {
			return mobileRouteRoleRelay
		}
	}
	return ""
}

func findMobileRouteHopForNodeID(item routeServerItem, nodeID string) (routeHopItem, bool) {
	target := normalizeMobileRouteNodeID(nodeID)
	for _, hop := range item.HopConfigs {
		if hop.NodeNo <= 0 {
			continue
		}
		if normalizeMobileRouteNodeID(strconv.Itoa(hop.NodeNo)) == target {
			return hop, true
		}
	}
	return routeHopItem{}, false
}

func findMobileRouteHopForNode(item routeServerItem, nodeID string) routeHopItem {
	if hop, ok := findMobileRouteHopForNodeID(item, nodeID); ok {
		return hop
	}
	target := normalizeMobileRouteNodeID(nodeID)
	route := buildMobileRouteRoute(item)
	for index, id := range route {
		if normalizeMobileRouteNodeID(id) != target {
			continue
		}
		for _, hop := range item.HopConfigs {
			if hop.NodeNo == index+1 {
				return hop
			}
		}
	}
	return routeHopItem{}
}

func resolveMobileRouteNextHop(item routeServerItem, nodeID string, role string) (host string, port int, layer string, dialMode string, authMode string) {
	if role == mobileRouteRoleExit || role == mobileRouteRoleEntryExit {
		return "", 0, "", mobileRouteDialNone, "route"
	}
	route := buildMobileRouteRoute(item)
	target := normalizeMobileRouteNodeID(nodeID)
	for i, id := range route {
		if normalizeMobileRouteNodeID(id) != target || i+1 >= len(route) {
			continue
		}
		currentHop, _ := findMobileRouteHopForNodeID(item, id)
		nextHop := findMobileRouteHopForNode(item, route[i+1])
		externalPort := nextHop.ExternalPort
		if externalPort <= 0 {
			externalPort = nextHop.ListenPort
		}
		return strings.TrimSpace(nextHop.RelayHost), externalPort, normalizeMobileRouteRouteLayer(firstMobileRouteNonEmpty(nextHop.RouteLayer, item.RouteLayer)), normalizeMobileRouteDialMode(currentHop.DialMode), "secret"
	}
	return "", 0, "", mobileRouteDialNone, "none"
}

func resolveMobileRoutePrevHop(item routeServerItem, nodeID string, role string) (host string, port int, layer string, dialMode string) {
	if role == mobileRouteRoleEntry {
		return "", 0, "", mobileRouteDialNone
	}
	route := buildMobileRouteRoute(item)
	target := normalizeMobileRouteNodeID(nodeID)
	for i, id := range route {
		if normalizeMobileRouteNodeID(id) != target || i <= 0 {
			continue
		}
		prevHop := findMobileRouteHopForNode(item, route[i-1])
		externalPort := prevHop.ExternalPort
		if externalPort <= 0 {
			externalPort = prevHop.ListenPort
		}
		return strings.TrimSpace(prevHop.RelayHost), externalPort, normalizeMobileRouteRouteLayer(firstMobileRouteNonEmpty(prevHop.RouteLayer, item.RouteLayer)), normalizeMobileRouteDialMode(prevHop.DialMode)
	}
	return "", 0, "", mobileRouteDialNone
}

func sleepMobileRouteBackoff(stopCh <-chan struct{}, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-stopCh:
	case <-timer.C:
	}
}

func nextMobileRouteBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return mobileRouteBridgeRetryMin
	}
	next := current * 2
	if next > mobileRouteBridgeRetryMax {
		next = mobileRouteBridgeRetryMax
	}
	return next
}

func waitMobileRouteBridgeSession(stopCh <-chan struct{}, session *mobileRouteFrameSession) {
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

func newMobileRouteQUICConfig() *quic.Config {
	return &quic.Config{
		Versions:                       []quic.Version{quic.Version2, quic.Version1},
		EnableDatagrams:                true,
		KeepAlivePeriod:                10 * time.Second,
		InitialStreamReceiveWindow:     mobileRouteQUICInitialStreamWindow,
		MaxStreamReceiveWindow:         mobileRouteQUICMaxStreamWindow,
		InitialConnectionReceiveWindow: mobileRouteQUICInitialConnWindow,
		MaxConnectionReceiveWindow:     mobileRouteQUICMaxConnWindow,
		MaxIncomingStreams:             1024,
	}
}

type mobileRouteTCPListener struct {
	net.Listener
}

func (l *mobileRouteTCPListener) Accept() (net.Conn, error) {
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

func generateMobileRouteCertificate() (tls.Certificate, error) {
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
		Subject:      pkix.Name{CommonName: "android-probe-route"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", "android-probe-route"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
