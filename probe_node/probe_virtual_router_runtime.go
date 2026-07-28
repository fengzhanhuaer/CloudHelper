package main

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/quic-go/quic-go/http3"
)

const (
	probeVirtualRouterRuntimeRouteIDPrefix                 = "vrouter-"
	probeVirtualRouterRuntimeRouteLayer                    = "auto"
	probeVirtualRouterRuntimeRole                          = "virtual_router"
	probeVirtualRouterFrameLinkTXBufferFrames              = 256
	probeVirtualRouterFrameLinkTXControlBufferFrames       = 32
	probeVirtualRouterFrameLinkTXBulkBufferFrames          = 128
	probeVirtualRouterFrameLinkTXBusinessQuantum           = 8
	probeVirtualRouterFrameLinkTXBatchBytes                = 64 * 1024
	probeVirtualRouterFrameLinkTXCoalesceWindow            = 200 * time.Microsecond
	probeVirtualRouterFrameLinkRXBufferFrames              = 4096
	probeVirtualRouterFrameLinkRXDispatchShards            = 8
	probeVirtualRouterFrameLinkRXDispatchShardBufferFrames = 1024
	probeVirtualRouterRXDispatchDropLogPeriod              = time.Second
)

type probeVirtualRouterRuntimeConfig struct {
	routeID       string
	name          string
	userID        string
	rawPublicKey  string
	userPublicKey ed25519.PublicKey
	secret        string
	authTicket    string
	listenHost    string
	listenPort    int
	routeLayer    string
	fromNodeID    string
	toNodeID      string
	localNodeID   string
	peerNodeID    string
	fromTLSSPKI   string
	toTLSSPKI     string
	peerTLSSPKI   string
	peerName      string
	localIP       string
	peerIP        string
	peerHost      string
	peerPort      int
	dialer        bool
	identity      nodeIdentity
	controllerURL string
}

type probeVirtualRouterRuntime struct {
	cfg             probeVirtualRouterRuntimeConfig
	relayListenAddr string
	relay           *probeVirtualRouterRelayServer
	frameLink       *probeVirtualRouterFrameLink
	stopCh          chan struct{}
	bridgeWakeCh    chan struct{}
	seqMu           sync.Mutex
	seq             uint32
}

type probeVirtualRouterRelayServer struct {
	listenAddr    string
	httpsServer   *http.Server
	http3Server   *http3.Server
	tcpListener   net.Listener
	udpPacketConn net.PacketConn
	routeIDs      map[string]struct{}
	closeOnce     sync.Once
}

type probeVirtualRouterH3Conn struct {
	stream interface {
		Read([]byte) (int, error)
		Write([]byte) (int, error)
		Close() error
		SetReadDeadline(time.Time) error
		SetWriteDeadline(time.Time) error
	}
	local   net.Addr
	remote  net.Addr
	closeFn func() error
}

func (c *probeVirtualRouterH3Conn) Read(p []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, net.ErrClosed
	}
	return c.stream.Read(p)
}

func (c *probeVirtualRouterH3Conn) Write(p []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, net.ErrClosed
	}
	return c.stream.Write(p)
}

func (c *probeVirtualRouterH3Conn) Close() error {
	if c == nil || c.stream == nil {
		return nil
	}
	if c.closeFn != nil {
		return c.closeFn()
	}
	return c.stream.Close()
}

func (c *probeVirtualRouterH3Conn) LocalAddr() net.Addr {
	if c == nil || c.local == nil {
		return probeRouteRelayNetAddr{label: "probe-vrouter-h3-local"}
	}
	return c.local
}

func (c *probeVirtualRouterH3Conn) RemoteAddr() net.Addr {
	if c == nil || c.remote == nil {
		return probeRouteRelayNetAddr{label: "probe-vrouter-h3-remote"}
	}
	return c.remote
}

func (c *probeVirtualRouterH3Conn) SetDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return net.ErrClosed
	}
	if err := c.stream.SetReadDeadline(t); err != nil {
		return err
	}
	return c.stream.SetWriteDeadline(t)
}

func (c *probeVirtualRouterH3Conn) SetReadDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return net.ErrClosed
	}
	return c.stream.SetReadDeadline(t)
}

func (c *probeVirtualRouterH3Conn) SetWriteDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return net.ErrClosed
	}
	return c.stream.SetWriteDeadline(t)
}

type probeVirtualRouterFrameLink struct {
	key               string
	runtime           *probeVirtualRouterRuntime
	carrier           *probeVirtualRouterPhysicalCarrier
	requestPath       []string
	openedAt          time.Time
	lastUsed          time.Time
	tx                chan probeVirtualRouterFrame
	txControl         chan probeVirtualRouterFrame
	txBulk            chan probeVirtualRouterFrame
	rx                chan probeVirtualRouterFrame
	rxDispatchShards  []chan probeVirtualRouterFrame
	done              chan struct{}
	carrierNotify     chan struct{}
	startOnce         sync.Once
	closeOnce         sync.Once
	rxDispatchDrops   uint64
	rxDropLastLogAt   time.Time
	txLastWriteTime   time.Duration
	txWriteTimeEMA    time.Duration
	txLastBatchFrames int
	txLastBatchBytes  int
	mu                sync.Mutex
}

type probeVirtualRouterPhysicalCarrier struct {
	conn        net.Conn
	sessionID   string
	remoteAddr  string
	connectedAt time.Time
	lastReadAt  time.Time
	lastWriteAt time.Time
	done        chan struct{}
	closeOnce   sync.Once
	mu          sync.Mutex
}

type probeVirtualRouterRuleRuntime struct {
	RuleID            string
	RuleName          string
	RouteID           string
	FromNodeID        string
	ToNodeID          string
	LocalNodeID       string
	PeerNodeID        string
	LocalIP           string
	PeerIP            string
	LocalServicePort  int
	PeerServiceDomain string
	PeerServicePort   int
	Dialer            bool
	ListenHost        string
	ListenPort        int
	PeerHost          string
	PeerPort          int
	Status            string
	UpdatedAt         time.Time
}

var probeVirtualRouterRuntimeState = struct {
	mu       sync.RWMutex
	runtimes map[string]*probeVirtualRouterRuntime
}{runtimes: make(map[string]*probeVirtualRouterRuntime)}

var probeVirtualRouterRelayServerState = struct {
	mu      sync.Mutex
	servers map[string]*probeVirtualRouterRelayServer
}{servers: make(map[string]*probeVirtualRouterRelayServer)}

var probeVirtualRouterFrameLinkState = struct {
	mu    sync.Mutex
	links map[string]*probeVirtualRouterFrameLink
}{links: make(map[string]*probeVirtualRouterFrameLink)}

var probeVirtualRouterRuleRuntimeState = struct {
	mu    sync.RWMutex
	items map[string]*probeVirtualRouterRuleRuntime
}{items: make(map[string]*probeVirtualRouterRuleRuntime)}

func isProbeVirtualRouterRuntimeRouteID(routeID string) bool {
	return strings.HasPrefix(strings.TrimSpace(routeID), probeVirtualRouterRuntimeRouteIDPrefix)
}

func applyProbeVirtualRouterRuntimesForNode(identity nodeIdentity, controllerBaseURL string, config probeVirtualRouterConfig) {
	localNodeID := normalizeProbeRouteNodeID(identity.NodeID)
	if localNodeID == "" {
		stopProbeVirtualRouterRuntimesExcept(nil, "virtual router local node id empty")
		return
	}
	config = sanitizeProbeVirtualRouterConfigForCache(config)
	if !config.Enabled {
		stopProbeVirtualRouterRuntimesExcept(nil, "virtual router disabled")
		clearProbeVirtualRouterRuleRuntimesExcept(nil)
		return
	}
	configs := buildProbeVirtualRouterRuntimeConfigsForNode(config, identity, controllerBaseURL)
	desired := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		desired[strings.TrimSpace(cfg.routeID)] = struct{}{}
	}
	stopProbeVirtualRouterRuntimesExcept(desired, "virtual router topology changed")
	clearProbeVirtualRouterRuleRuntimesExcept(desired)
	for _, cfg := range configs {
		if strings.TrimSpace(cfg.routeID) == "" {
			continue
		}
		if isSameProbeVirtualRouterRuntimeConfig(cfg.routeID, cfg) {
			updateRunningProbeVirtualRouterAuthTicket(cfg.routeID, cfg.authTicket)
			upsertProbeVirtualRouterRuleRuntime(config, cfg)
			continue
		}
		if _, err := startProbeVirtualRouterRuntime(cfg); err != nil {
			log.Printf("warning: probe virtual router runtime start failed: route=%s local=%s peer=%s err=%v", cfg.routeID, localNodeID, cfg.peerNodeID, err)
			clearProbeVirtualRouterRuleRuntime(cfg.routeID)
			continue
		}
		upsertProbeVirtualRouterRuleRuntime(config, cfg)
	}
}

func startProbeVirtualRouterRuntime(cfg probeVirtualRouterRuntimeConfig) (*probeVirtualRouterRuntime, error) {
	cfg.routeID = strings.TrimSpace(cfg.routeID)
	if cfg.routeID == "" {
		return nil, errors.New("virtual router route_id is required")
	}
	_ = stopProbeVirtualRouterRuntime(cfg.routeID, "restart before apply")
	if cfg.authTicket == "" {
		cfg.authTicket = lookupProbeRouteAuthTicket(cfg.routeID)
	}
	if cfg.authTicket == "" {
		return nil, errors.New("virtual router auth ticket is required")
	}
	rememberProbeRouteAuthTicket(cfg.routeID, cfg.authTicket)
	rt := &probeVirtualRouterRuntime{
		cfg:          cfg,
		stopCh:       make(chan struct{}),
		bridgeWakeCh: make(chan struct{}, 1),
	}
	if !cfg.dialer {
		if err := startProbeVirtualRouterRelayServer(rt); err != nil {
			close(rt.stopCh)
			rt.closeRuntimeResources()
			return nil, err
		}
	}
	if _, err := ensureProbeVirtualRouterRuntimeFrameLink(rt); err != nil {
		close(rt.stopCh)
		rt.closeRuntimeResources()
		return nil, err
	}
	probeVirtualRouterRuntimeState.mu.Lock()
	probeVirtualRouterRuntimeState.runtimes[cfg.routeID] = rt
	probeVirtualRouterRuntimeState.mu.Unlock()
	startProbeVirtualRouterBridgeWorker(rt)
	startProbeVirtualRouterKeepAliveWorker(rt)
	listenText := "-"
	if !cfg.dialer {
		listenText = net.JoinHostPort(cfg.listenHost, strconv.Itoa(cfg.listenPort))
	}
	log.Printf("probe virtual router runtime started: route=%s listen=%s peer=%s dialer=%t", cfg.routeID, listenText, cfg.peerNodeID, cfg.dialer)
	return rt, nil
}

func stopProbeVirtualRouterRuntime(routeID string, reason string) bool {
	target := strings.TrimSpace(routeID)
	if target == "" {
		return false
	}
	probeVirtualRouterRuntimeState.mu.Lock()
	rt, ok := probeVirtualRouterRuntimeState.runtimes[target]
	if ok {
		delete(probeVirtualRouterRuntimeState.runtimes, target)
	}
	probeVirtualRouterRuntimeState.mu.Unlock()
	if !ok || rt == nil {
		return false
	}
	close(rt.stopCh)
	rt.closeRuntimeResources()
	closeProbeVirtualRouterRuntimeFrameLink(rt)
	clearProbeVirtualRouterRuntimePingError(target)
	log.Printf("probe virtual router runtime stopped: route=%s reason=%s", target, strings.TrimSpace(reason))
	return true
}

func stopProbeVirtualRouterRuntimesExcept(desired map[string]struct{}, reason string) {
	probeVirtualRouterRuntimeState.mu.RLock()
	ids := make([]string, 0, len(probeVirtualRouterRuntimeState.runtimes))
	for id := range probeVirtualRouterRuntimeState.runtimes {
		if desired != nil {
			if _, ok := desired[id]; ok {
				continue
			}
		}
		ids = append(ids, id)
	}
	probeVirtualRouterRuntimeState.mu.RUnlock()
	sort.Strings(ids)
	for _, id := range ids {
		stopProbeVirtualRouterRuntime(id, reason)
		clearProbeVirtualRouterRuleRuntime(id)
	}
}

func getProbeVirtualRouterRuntime(routeID string) *probeVirtualRouterRuntime {
	cleanID := strings.TrimSpace(routeID)
	if cleanID == "" {
		return nil
	}
	probeVirtualRouterRuntimeState.mu.RLock()
	rt := probeVirtualRouterRuntimeState.runtimes[cleanID]
	probeVirtualRouterRuntimeState.mu.RUnlock()
	return rt
}

func isSameProbeVirtualRouterRuntimeConfig(routeID string, cfg probeVirtualRouterRuntimeConfig) bool {
	rt := getProbeVirtualRouterRuntime(routeID)
	if rt == nil {
		return false
	}
	c := rt.cfg
	return c.listenHost == cfg.listenHost &&
		c.listenPort == cfg.listenPort &&
		c.routeLayer == cfg.routeLayer &&
		c.fromNodeID == cfg.fromNodeID &&
		c.toNodeID == cfg.toNodeID &&
		c.localNodeID == cfg.localNodeID &&
		c.peerNodeID == cfg.peerNodeID &&
		c.localIP == cfg.localIP &&
		c.peerIP == cfg.peerIP &&
		c.peerHost == cfg.peerHost &&
		c.peerPort == cfg.peerPort &&
		c.dialer == cfg.dialer &&
		c.secret == cfg.secret &&
		c.rawPublicKey == cfg.rawPublicKey
}

func updateRunningProbeVirtualRouterAuthTicket(routeID string, authTicket string) {
	cleanTicket := strings.TrimSpace(authTicket)
	if cleanTicket == "" {
		return
	}
	rt := getProbeVirtualRouterRuntime(routeID)
	if rt == nil {
		return
	}
	rt.cfg.authTicket = cleanTicket
	rememberProbeRouteAuthTicket(rt.cfg.routeID, cleanTicket)
}

func (rt *probeVirtualRouterRuntime) closeRuntimeResources() {
	if rt == nil {
		return
	}
	if rt.relay != nil {
		releaseProbeVirtualRouterRelayServer(rt)
		rt.relay = nil
	}
}

func (rt *probeVirtualRouterRuntime) nextBridgeSessionID(tag string) string {
	if rt == nil {
		return ""
	}
	rt.seqMu.Lock()
	rt.seq++
	seq := rt.seq
	rt.seqMu.Unlock()
	cleanTag := strings.TrimSpace(tag)
	if cleanTag == "" {
		cleanTag = "carrier"
	}
	return fmt.Sprintf("%s-%d", cleanTag, seq)
}

func startProbeVirtualRouterRelayServer(rt *probeVirtualRouterRuntime) error {
	if rt == nil {
		return errors.New("virtual router runtime is nil")
	}
	cfg := rt.cfg
	listenAddr := net.JoinHostPort(cfg.listenHost, strconv.Itoa(cfg.listenPort))
	routeID := strings.TrimSpace(cfg.routeID)
	if routeID == "" {
		return errors.New("virtual router route_id is required")
	}
	probeVirtualRouterRelayServerState.mu.Lock()
	if existing := probeVirtualRouterRelayServerState.servers[listenAddr]; existing != nil {
		if existing.routeIDs == nil {
			existing.routeIDs = make(map[string]struct{})
		}
		existing.routeIDs[routeID] = struct{}{}
		rt.relayListenAddr = listenAddr
		rt.relay = existing
		probeVirtualRouterRelayServerState.mu.Unlock()
		log.Printf("probe virtual router relay reused: route=%s listen=%s routes=%d", cfg.routeID, listenAddr, len(existing.routeIDs))
		return nil
	}
	probeVirtualRouterRelayServerState.mu.Unlock()
	handler := buildProbeVirtualRouterRelayHandler()
	cert, err := prepareProbeServerCertificate(cfg.identity, strings.TrimSpace(cfg.controllerURL))
	if err != nil {
		return fmt.Errorf("prepare virtual router relay certificate failed: %w", err)
	}
	probeVirtualRouterRelayServerState.mu.Lock()
	if existing := probeVirtualRouterRelayServerState.servers[listenAddr]; existing != nil {
		if existing.routeIDs == nil {
			existing.routeIDs = make(map[string]struct{})
		}
		existing.routeIDs[routeID] = struct{}{}
		rt.relayListenAddr = listenAddr
		rt.relay = existing
		probeVirtualRouterRelayServerState.mu.Unlock()
		log.Printf("probe virtual router relay reused: route=%s listen=%s routes=%d", cfg.routeID, listenAddr, len(existing.routeIDs))
		return nil
	}
	listenConfig := net.ListenConfig{KeepAlive: probeRouteRelayTCPKeepAlivePeriod}
	tcpListener, err := listenConfig.Listen(context.Background(), "tcp", listenAddr)
	if err != nil {
		probeVirtualRouterRelayServerState.mu.Unlock()
		return fmt.Errorf("listen virtual router relay tcp failed: %w", err)
	}
	relay := &probeVirtualRouterRelayServer{
		listenAddr:  listenAddr,
		tcpListener: tcpListener,
		routeIDs:    map[string]struct{}{routeID: {}},
	}
	relay.httpsServer = &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if tlsCert, certErr := tls.LoadX509KeyPair(cert.CertPath, cert.KeyPath); certErr != nil {
		log.Printf("warning: probe virtual router h3 relay certificate load failed: route=%s listen=%s err=%v", cfg.routeID, listenAddr, certErr)
	} else if udpPacketConn, listenErr := net.ListenPacket("udp", listenAddr); listenErr != nil {
		log.Printf("warning: probe virtual router h3 relay udp listen failed: route=%s listen=%s err=%v", cfg.routeID, listenAddr, listenErr)
	} else {
		relay.udpPacketConn = udpPacketConn
		relay.http3Server = &http3.Server{
			Addr:    listenAddr,
			Handler: handler,
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{tlsCert},
				MinVersion:   tls.VersionTLS13,
				NextProtos:   []string{http3.NextProtoH3},
			},
			QUICConfig: newProbeRouteQUICConfig(0),
		}
	}
	probeVirtualRouterRelayServerState.servers[listenAddr] = relay
	probeVirtualRouterRelayServerState.mu.Unlock()
	go func() {
		if serveErr := relay.httpsServer.ServeTLS(tcpListener, cert.CertPath, cert.KeyPath); serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("probe virtual router relay exited: layer=websocket listen=%s err=%v", listenAddr, serveErr)
		}
	}()
	if relay.http3Server != nil && relay.udpPacketConn != nil {
		go func() {
			if serveErr := relay.http3Server.Serve(relay.udpPacketConn); serveErr != nil && serveErr != http.ErrServerClosed {
				log.Printf("probe virtual router relay exited: layer=websocket-h3 listen=%s err=%v", listenAddr, serveErr)
			}
		}()
	}
	rt.relayListenAddr = listenAddr
	rt.relay = relay
	log.Printf("probe virtual router relay started: route=%s listen=%s", cfg.routeID, listenAddr)
	return nil
}

func releaseProbeVirtualRouterRelayServer(rt *probeVirtualRouterRuntime) {
	if rt == nil || rt.relay == nil {
		return
	}
	relay := rt.relay
	listenAddr := strings.TrimSpace(relay.listenAddr)
	routeID := strings.TrimSpace(rt.cfg.routeID)
	shouldClose := false
	probeVirtualRouterRelayServerState.mu.Lock()
	if current := probeVirtualRouterRelayServerState.servers[listenAddr]; current == relay {
		if relay.routeIDs != nil && routeID != "" {
			delete(relay.routeIDs, routeID)
		}
		if len(relay.routeIDs) == 0 {
			delete(probeVirtualRouterRelayServerState.servers, listenAddr)
			shouldClose = true
		}
	} else {
		shouldClose = true
	}
	probeVirtualRouterRelayServerState.mu.Unlock()
	if shouldClose {
		closeProbeVirtualRouterRelayServer(relay)
	}
}

func closeProbeVirtualRouterRelayServer(relay *probeVirtualRouterRelayServer) {
	if relay == nil {
		return
	}
	relay.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if relay.httpsServer != nil {
			_ = relay.httpsServer.Shutdown(ctx)
		}
		if relay.http3Server != nil {
			_ = relay.http3Server.Close()
		}
		if relay.tcpListener != nil {
			_ = relay.tcpListener.Close()
		}
		if relay.udpPacketConn != nil {
			_ = relay.udpPacketConn.Close()
		}
	})
}

func ensureProbeVirtualRouterRuntimeFrameLink(rt *probeVirtualRouterRuntime) (*probeVirtualRouterFrameLink, error) {
	if rt == nil {
		return nil, errors.New("virtual router runtime is nil")
	}
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	if key == "" {
		return nil, errors.New("virtual router frame link key is empty")
	}
	var stale *probeVirtualRouterFrameLink
	probeVirtualRouterFrameLinkState.mu.Lock()
	if existing := probeVirtualRouterFrameLinkState.links[key]; existing != nil {
		if isProbeVirtualRouterFrameLinkClosed(existing) {
			delete(probeVirtualRouterFrameLinkState.links, key)
			stale = existing
		} else {
			existing.runtime = rt
			rt.frameLink = existing
			probeVirtualRouterFrameLinkState.mu.Unlock()
			existing.Start()
			return existing, nil
		}
	}
	link := newProbeVirtualRouterFrameLink(key, rt, nil, nil)
	probeVirtualRouterFrameLinkState.links[key] = link
	rt.frameLink = link
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if stale != nil {
		stopProbeVirtualRouterFrameLink(stale)
	}
	link.Start()
	return link, nil
}

func buildProbeVirtualRouterRelayHandler() http.Handler {
	mux := http.NewServeMux()
	registerProbeVirtualRouterOpenAIStyleCamouflageRoutes(mux)
	mux.HandleFunc(probeRouteRelayAPIPath, func(w http.ResponseWriter, r *http.Request) {
		handleProbeVirtualRouterRelayDispatch(w, r)
	})
	return mux
}

func registerProbeVirtualRouterOpenAIStyleCamouflageRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeProbeVirtualRouterOpenAIStyleMethodNotAllowed(w, r.Method)
			return
		}
		sleepProbeVirtualRouterOpenAIStyleJitter()
		writeProbeVirtualRouterOpenAIStyleJSON(w, http.StatusOK, map[string]any{
			"message":   "OpenAI-compatible API endpoint",
			"api_base":  "/v1",
			"version":   BuildVersion,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/v1", func(w http.ResponseWriter, r *http.Request) {
		sleepProbeVirtualRouterOpenAIStyleJitter()
		writeProbeVirtualRouterOpenAIStyleUnauthorized(w)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		sleepProbeVirtualRouterOpenAIStyleJitter()
		writeProbeVirtualRouterOpenAIStyleUnauthorized(w)
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		sleepProbeVirtualRouterOpenAIStyleJitter()
		writeProbeVirtualRouterOpenAIStyleUnauthorized(w)
	})
}

func writeProbeVirtualRouterOpenAIStyleUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeProbeVirtualRouterOpenAIStyleJSON(w, http.StatusUnauthorized, map[string]any{
		"error": map[string]any{
			"message": "Incorrect API key provided.",
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    "invalid_api_key",
		},
	})
}

func writeProbeVirtualRouterOpenAIStyleMethodNotAllowed(w http.ResponseWriter, method string) {
	writeProbeVirtualRouterOpenAIStyleJSON(w, http.StatusMethodNotAllowed, map[string]any{
		"error": map[string]any{
			"message": fmt.Sprintf("Method %s is not allowed for this endpoint.", strings.TrimSpace(method)),
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    "method_not_allowed",
		},
	})
}

func probeVirtualRouterOpenAIStyleJitterDuration() time.Duration {
	const minMs = int64(300)
	const spanMs = int64(701)
	offset := time.Now().UnixNano() % spanMs
	if offset < 0 {
		offset = -offset
	}
	return time.Duration(minMs+offset) * time.Millisecond
}

func sleepProbeVirtualRouterOpenAIStyleJitter() {
	time.Sleep(probeVirtualRouterOpenAIStyleJitterDuration())
}

func writeProbeVirtualRouterOpenAIStyleJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func handleProbeVirtualRouterRelayDispatch(w http.ResponseWriter, r *http.Request) {
	routeID := resolveProbeRouteIDFromRequest(r)
	if strings.TrimSpace(routeID) == "" {
		log.Printf("probe virtual router relay request rejected: remote=%s method=%s proto=%s host=%s reason=missing_route_id", r.RemoteAddr, r.Method, r.Proto, r.Host)
		http.Error(w, "route_id is required", http.StatusBadRequest)
		return
	}
	rt := getProbeVirtualRouterRuntime(routeID)
	if rt == nil {
		log.Printf("probe virtual router relay request rejected: requested_route=%s remote=%s method=%s proto=%s host=%s reason=runtime_not_found", strings.TrimSpace(routeID), r.RemoteAddr, r.Method, r.Proto, r.Host)
		http.Error(w, "virtual router runtime not found", http.StatusNotFound)
		return
	}
	if err := verifyProbeVirtualRouterRelayRequestAuth(rt, r, routeID); err != nil {
		log.Printf("probe virtual router relay request rejected: route=%s remote=%s method=%s proto=%s host=%s reason=unauthorized err=%v", rt.cfg.routeID, r.RemoteAddr, r.Method, r.Proto, r.Host, err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	bridgeRole := normalizeProbeRouteBridgeRole(r.Header.Get(probeRouteCodexRelayRoleHeader))
	if isProbeVirtualRouterH3Connect(r) {
		handleProbeVirtualRouterBridgeRelayH3(rt, bridgeRole, w, r)
		return
	}
	if websocket.IsWebSocketUpgrade(r) {
		handleProbeVirtualRouterBridgeRelayWebSocket(rt, bridgeRole, w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func handleProbeVirtualRouterBridgeRelayWebSocket(rt *probeVirtualRouterRuntime, bridgeRole string, w http.ResponseWriter, r *http.Request) {
	if rt == nil {
		http.Error(w, "virtual router runtime not found", http.StatusNotFound)
		return
	}
	upgrader := websocket.Upgrader{
		CheckOrigin:       func(*http.Request) bool { return true },
		ReadBufferSize:    probeRouteRelayWebSocketBufferBytes,
		WriteBufferSize:   probeRouteRelayWebSocketBufferBytes,
		EnableCompression: false,
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("probe virtual router websocket relay upgrade failed: route=%s remote=%s err=%v", rt.cfg.routeID, r.RemoteAddr, err)
		return
	}
	defer ws.Close()
	conn := newWebSocketNetConn(ws)
	sessionID := rt.nextBridgeSessionID("vrouter-carrier")
	runProbeVirtualRouterPhysicalCarrier(rt, conn, sessionID, strings.TrimSpace(r.RemoteAddr))
}

func handleProbeVirtualRouterBridgeRelayH3(rt *probeVirtualRouterRuntime, bridgeRole string, w http.ResponseWriter, r *http.Request) {
	if rt == nil {
		http.Error(w, "virtual router runtime not found", http.StatusNotFound)
		return
	}
	conn, ok := probeVirtualRouterConnFromH3(w, r, "probe-vrouter-h3-bridge")
	if !ok {
		log.Printf("probe virtual router h3 relay stream unavailable: route=%s remote=%s proto=%s", rt.cfg.routeID, r.RemoteAddr, r.Proto)
		http.Error(w, "http3 stream unavailable", http.StatusInternalServerError)
		return
	}
	defer conn.Close()
	sessionID := rt.nextBridgeSessionID("vrouter-carrier")
	runProbeVirtualRouterPhysicalCarrier(rt, conn, sessionID, strings.TrimSpace(r.RemoteAddr))
}

func isProbeVirtualRouterH3Connect(r *http.Request) bool {
	return r != nil && r.Method == http.MethodConnect && r.ProtoMajor == 3 && strings.EqualFold(strings.TrimSpace(r.Proto), "websocket")
}

func probeVirtualRouterConnFromH3(w http.ResponseWriter, r *http.Request, label string) (net.Conn, bool) {
	streamer, ok := w.(http3.HTTPStreamer)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	stream := streamer.HTTPStream()
	return &probeVirtualRouterH3Conn{
		stream: stream,
		local:  probeRouteRelayNetAddr{label: strings.TrimSpace(label)},
		remote: probeRouteRelayNetAddr{label: strings.TrimSpace(r.RemoteAddr)},
		closeFn: func() error {
			return stream.Close()
		},
	}, true
}

func verifyProbeVirtualRouterRelayRequestAuth(rt *probeVirtualRouterRuntime, r *http.Request, routeID string) error {
	if rt == nil {
		return errors.New("virtual router runtime is nil")
	}
	sourceIP := resolveProbeRouteSourceIPFromRequest(r)
	if blacklisted, until := isProbeRouteAuthIPBlacklisted(sourceIP); blacklisted {
		if until.IsZero() {
			return errors.New("source ip is blacklisted")
		}
		return fmt.Errorf("source ip is blacklisted until %s", until.UTC().Format(time.RFC3339))
	}
	fail := func(err error) error {
		recordProbeRouteAuthFailure(sourceIP)
		delayProbeRouteAuthFailure()
		return err
	}
	env, err := readProbeRouteAuthEnvelopeFromHeaders(r.Header, routeID, r.Method, r.URL.Path)
	if err != nil {
		return fail(err)
	}
	if err := verifyProbeVirtualRouterInboundAuth(rt.cfg, env); err != nil {
		return fail(err)
	}
	resetProbeRouteAuthFailure(sourceIP)
	return nil
}

func verifyProbeVirtualRouterInboundAuth(cfg probeVirtualRouterRuntimeConfig, env probeRouteAuthEnvelope) error {
	if env.RouteID != "" && env.RouteID != cfg.routeID {
		return fmt.Errorf("virtual router route id mismatch")
	}
	if env.Nonce == "" {
		return fmt.Errorf("nonce is required")
	}
	mode := strings.ToLower(strings.TrimSpace(env.Mode))
	if mode != "secret_hmac" {
		return fmt.Errorf("unsupported auth mode")
	}
	if env.Method != http.MethodGet && env.Method != http.MethodConnect {
		return fmt.Errorf("unsupported relay method")
	}
	if env.Path != probeRouteRelayAPIPath {
		return fmt.Errorf("relay path mismatch")
	}
	if env.RelayRole != probeRouteBridgeRoleToNext && env.RelayRole != probeRouteBridgeRoleToPrev {
		return fmt.Errorf("relay role mismatch")
	}
	if normalizeProbeRouteNodeID(env.SourceNode) == "" || normalizeProbeRouteNodeID(env.SourceNode) != normalizeProbeRouteNodeID(cfg.peerNodeID) {
		return fmt.Errorf("relay source node mismatch")
	}
	if strings.TrimSpace(cfg.secret) == "" {
		return fmt.Errorf("secret is not configured")
	}
	if env.MAC == "" {
		return fmt.Errorf("mac is required")
	}
	expected := buildProbeRouteHMAC(cfg.secret, cfg.routeID, env.Nonce, env.Method, env.Path, env.SourceNode, env.RelayRole)
	if !hmac.Equal([]byte(strings.ToLower(env.MAC)), []byte(strings.ToLower(expected))) {
		return fmt.Errorf("authentication failed")
	}
	if err := verifyProbeVirtualRouterUserAuthTicket(cfg, env.AuthTicket); err != nil {
		return err
	}
	if err := recordProbeRouteAuthNonce(cfg.routeID, env.AuthTicket, env.Nonce); err != nil {
		return err
	}
	return nil
}

func verifyProbeVirtualRouterUserAuthTicket(cfg probeVirtualRouterRuntimeConfig, rawTicket string) error {
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
	var payload probeRouteUserAuthTicketPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return fmt.Errorf("invalid user auth ticket payload json")
	}
	version := strings.TrimSpace(payload.Version)
	if version != "route-auth-v2" && version != "route-auth-v3" {
		return fmt.Errorf("unsupported user auth ticket version")
	}
	if strings.TrimSpace(payload.RouteID) != strings.TrimSpace(cfg.routeID) {
		return fmt.Errorf("user auth ticket route mismatch")
	}
	if strings.TrimSpace(payload.UserPublicKey) != strings.TrimSpace(cfg.rawPublicKey) {
		return fmt.Errorf("user auth ticket public key mismatch")
	}
	if strings.TrimSpace(payload.ClientEntryID) == "" || strings.TrimSpace(payload.TicketID) == "" {
		return fmt.Errorf("user auth ticket identity is incomplete")
	}
	fromNodeID := normalizeProbeRouteNodeID(payload.FromNodeID)
	toNodeID := normalizeProbeRouteNodeID(payload.ToNodeID)
	if fromNodeID != normalizeProbeRouteNodeID(cfg.fromNodeID) || toNodeID != normalizeProbeRouteNodeID(cfg.toNodeID) {
		return fmt.Errorf("user auth ticket endpoint mismatch")
	}
	issuedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.IssuedAt))
	if err != nil {
		return fmt.Errorf("invalid user auth ticket issued_at")
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.ExpiresAt))
	if err != nil {
		return fmt.Errorf("invalid user auth ticket expires_at")
	}
	now := probeRouteAuthTicketNow().UTC()
	if issuedAt.After(now.Add(5 * time.Minute)) {
		return fmt.Errorf("user auth ticket is not active")
	}
	if !expiresAt.After(now) {
		return fmt.Errorf("user auth ticket is expired")
	}
	return nil
}

func startProbeVirtualRouterBridgeWorker(rt *probeVirtualRouterRuntime) {
	if rt == nil || !rt.cfg.dialer || strings.TrimSpace(rt.cfg.peerHost) == "" || rt.cfg.peerPort <= 0 {
		return
	}
	go runProbeVirtualRouterBridgeDialer(rt)
}

func runProbeVirtualRouterBridgeDialer(rt *probeVirtualRouterRuntime) {
	backoff := probeRouteBridgeRetryMin
	for {
		select {
		case <-rt.stopCh:
			return
		default:
		}
		conn, err := openProbeVirtualRouterBridgeRelayNetConnWithDomainPolicy(
			rt.cfg.routeID,
			rt.cfg.secret,
			rt.cfg.peerHost,
			rt.cfg.peerPort,
			rt.cfg.routeLayer,
			probeRouteBridgeRoleToNext,
			probeRouteRelayDialTimeout+probeRouteRelayResponseReadDeadline,
			isProbeVirtualRouterCloudflareCopilotDomain(rt.cfg.peerHost),
		)
		if err != nil {
			log.Printf("probe virtual router bridge dial failed: route=%s peer=%s:%d err=%v", rt.cfg.routeID, rt.cfg.peerHost, rt.cfg.peerPort, err)
			sleepProbeVirtualRouterBridgeBackoff(rt, backoff)
			backoff = nextProbeRouteBridgeBackoff(backoff)
			continue
		}
		backoff = probeRouteBridgeRetryMin
		sessionID := rt.nextBridgeSessionID("vrouter-carrier")
		runProbeVirtualRouterPhysicalCarrier(rt, conn, sessionID, net.JoinHostPort(rt.cfg.peerHost, strconv.Itoa(rt.cfg.peerPort)))
		sleepProbeVirtualRouterBridgeBackoff(rt, backoff)
		backoff = nextProbeRouteBridgeBackoff(backoff)
	}
}

func signalProbeVirtualRouterBridgeDialer(rt *probeVirtualRouterRuntime) {
	if rt == nil || rt.bridgeWakeCh == nil {
		return
	}
	select {
	case rt.bridgeWakeCh <- struct{}{}:
	default:
	}
}

func sleepProbeVirtualRouterBridgeBackoff(rt *probeVirtualRouterRuntime, delay time.Duration) {
	if rt == nil {
		return
	}
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-rt.stopCh:
	case <-rt.bridgeWakeCh:
	case <-timer.C:
	}
}

func clearProbeVirtualRouterRuleRuntimesExcept(desired map[string]struct{}) {
	probeVirtualRouterRuleRuntimeState.mu.Lock()
	for routeID := range probeVirtualRouterRuleRuntimeState.items {
		if desired != nil {
			if _, ok := desired[routeID]; ok {
				continue
			}
		}
		delete(probeVirtualRouterRuleRuntimeState.items, routeID)
	}
	probeVirtualRouterRuleRuntimeState.mu.Unlock()
}

func clearProbeVirtualRouterRuleRuntime(routeID string) {
	cleanID := strings.TrimSpace(routeID)
	if cleanID == "" {
		return
	}
	probeVirtualRouterRuleRuntimeState.mu.Lock()
	delete(probeVirtualRouterRuleRuntimeState.items, cleanID)
	probeVirtualRouterRuleRuntimeState.mu.Unlock()
}

func upsertProbeVirtualRouterRuleRuntime(config probeVirtualRouterConfig, runtimeConfig probeVirtualRouterRuntimeConfig) {
	routeID := strings.TrimSpace(runtimeConfig.routeID)
	if routeID == "" {
		return
	}
	rule, ok := probeVirtualRouterRuleByRouteID(config, routeID)
	if !ok {
		return
	}
	item := &probeVirtualRouterRuleRuntime{
		RuleID:            strings.TrimSpace(rule.ID),
		RuleName:          strings.TrimSpace(rule.Name),
		RouteID:           routeID,
		FromNodeID:        runtimeConfig.fromNodeID,
		ToNodeID:          runtimeConfig.toNodeID,
		LocalNodeID:       runtimeConfig.localNodeID,
		PeerNodeID:        runtimeConfig.peerNodeID,
		LocalIP:           runtimeConfig.localIP,
		PeerIP:            runtimeConfig.peerIP,
		LocalServicePort:  runtimeConfig.listenPort,
		PeerServiceDomain: runtimeConfig.peerHost,
		PeerServicePort:   runtimeConfig.peerPort,
		Dialer:            runtimeConfig.dialer,
		ListenHost:        runtimeConfig.listenHost,
		ListenPort:        runtimeConfig.listenPort,
		PeerHost:          runtimeConfig.peerHost,
		PeerPort:          runtimeConfig.peerPort,
		Status:            "running",
		UpdatedAt:         time.Now(),
	}
	probeVirtualRouterRuleRuntimeState.mu.Lock()
	probeVirtualRouterRuleRuntimeState.items[routeID] = item
	probeVirtualRouterRuleRuntimeState.mu.Unlock()
}

func probeVirtualRouterRuleByRouteID(config probeVirtualRouterConfig, routeID string) (probeVirtualRouterTopologyRule, bool) {
	target := strings.TrimSpace(routeID)
	for _, rule := range config.TopologyRules {
		if probeVirtualRouterRuntimeRouteID(rule) == target {
			return rule, true
		}
	}
	return probeVirtualRouterTopologyRule{}, false
}

func snapshotProbeVirtualRouterRuleRuntimes() []probeVirtualRouterRuleRuntime {
	probeVirtualRouterRuleRuntimeState.mu.RLock()
	items := make([]probeVirtualRouterRuleRuntime, 0, len(probeVirtualRouterRuleRuntimeState.items))
	for _, item := range probeVirtualRouterRuleRuntimeState.items {
		if item == nil {
			continue
		}
		items = append(items, *item)
	}
	probeVirtualRouterRuleRuntimeState.mu.RUnlock()
	sort.SliceStable(items, func(i, j int) bool {
		left := firstNonEmpty(strings.TrimSpace(items[i].RuleID), strings.TrimSpace(items[i].RouteID))
		right := firstNonEmpty(strings.TrimSpace(items[j].RuleID), strings.TrimSpace(items[j].RouteID))
		return left < right
	})
	return items
}

func buildProbeVirtualRouterRuntimeConfigsForNode(config probeVirtualRouterConfig, identity nodeIdentity, controllerBaseURL string) []probeVirtualRouterRuntimeConfig {
	localNodeID := normalizeProbeRouteNodeID(identity.NodeID)
	if localNodeID == "" {
		return []probeVirtualRouterRuntimeConfig{}
	}
	config = sanitizeProbeVirtualRouterConfigForCache(config)
	if !config.Enabled {
		return []probeVirtualRouterRuntimeConfig{}
	}
	out := make([]probeVirtualRouterRuntimeConfig, 0)
	seen := make(map[string]struct{})
	for _, rule := range config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		fromNodeID := normalizeProbeRouteNodeID(rule.FromNodeID)
		toNodeID := normalizeProbeRouteNodeID(rule.ToNodeID)
		if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
			continue
		}
		if localNodeID != fromNodeID && localNodeID != toNodeID {
			continue
		}
		cfg, ok := buildProbeVirtualRouterRuntimeConfigForRule(config, rule, identity, controllerBaseURL)
		if !ok {
			continue
		}
		if _, exists := seen[cfg.routeID]; exists {
			continue
		}
		seen[cfg.routeID] = struct{}{}
		out = append(out, cfg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].routeID < out[j].routeID
	})
	return out
}

func buildProbeVirtualRouterRuntimeConfigForRule(config probeVirtualRouterConfig, rule probeVirtualRouterTopologyRule, identity nodeIdentity, controllerBaseURL string) (probeVirtualRouterRuntimeConfig, bool) {
	localNodeID := normalizeProbeRouteNodeID(identity.NodeID)
	fromNodeID := normalizeProbeRouteNodeID(rule.FromNodeID)
	toNodeID := normalizeProbeRouteNodeID(rule.ToNodeID)
	if localNodeID == "" || fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
		return probeVirtualRouterRuntimeConfig{}, false
	}
	localIsFrom := localNodeID == fromNodeID
	localIsTo := localNodeID == toNodeID
	if !localIsFrom && !localIsTo {
		return probeVirtualRouterRuntimeConfig{}, false
	}
	peerNodeID := toNodeID
	peerTLSSPKI := normalizeProbeRouteTLSSPKI(rule.ToTLSSPKISHA256)
	peerDomain := strings.TrimSpace(rule.ToServiceDomain)
	listenerPort := probeVirtualRouterServicePortForNode(config, toNodeID, rule.ToServicePort)
	peerPort := listenerPort
	if isProbeVirtualRouterCloudflareCopilotDomain(peerDomain) {
		peerPort = 443
	}
	localPort := 0
	if localIsTo {
		peerNodeID = fromNodeID
		peerTLSSPKI = normalizeProbeRouteTLSSPKI(rule.FromTLSSPKISHA256)
		localPort = listenerPort
	}

	routeID := probeVirtualRouterRuntimeRouteID(rule)
	dialerNodeID := probeVirtualRouterRuleDialerNodeID(rule)
	if dialerNodeID == "" {
		return probeVirtualRouterRuntimeConfig{}, false
	}
	secret := strings.TrimSpace(rule.Secret)
	authTicket := strings.TrimSpace(rule.AuthTicket)
	rawPublicKey := strings.TrimSpace(rule.UserPublicKey)
	if secret == "" || authTicket == "" || rawPublicKey == "" {
		log.Printf("warning: probe virtual router rule skipped: route=%s missing route auth fields", routeID)
		return probeVirtualRouterRuntimeConfig{}, false
	}
	userPublicKey, err := parseProbeRouteUserPublicKey(rawPublicKey)
	if err != nil {
		log.Printf("warning: probe virtual router rule skipped: route=%s invalid user_public_key: %v", routeID, err)
		return probeVirtualRouterRuntimeConfig{}, false
	}
	cfg := probeVirtualRouterRuntimeConfig{
		routeID:       routeID,
		name:          "Virtual Router " + firstNonEmpty(strings.TrimSpace(rule.Name), strings.TrimSpace(rule.ID), fromNodeID+"-"+toNodeID),
		userID:        strings.TrimSpace(rule.UserID),
		rawPublicKey:  rawPublicKey,
		userPublicKey: userPublicKey,
		secret:        secret,
		authTicket:    authTicket,
		listenHost:    "0.0.0.0",
		listenPort:    localPort,
		routeLayer:    normalizeProbeRouteRouteLayer(firstNonEmpty(strings.TrimSpace(rule.RouteLayer), probeVirtualRouterRuntimeRouteLayer)),
		fromNodeID:    fromNodeID,
		toNodeID:      toNodeID,
		localNodeID:   localNodeID,
		peerNodeID:    peerNodeID,
		fromTLSSPKI:   normalizeProbeRouteTLSSPKI(rule.FromTLSSPKISHA256),
		toTLSSPKI:     normalizeProbeRouteTLSSPKI(rule.ToTLSSPKISHA256),
		peerTLSSPKI:   peerTLSSPKI,
		peerName:      probeVirtualRouterDisplayNameForNode(config, peerNodeID),
		localIP:       currentProbeVirtualRouterIPForNode(localNodeID),
		peerIP:        currentProbeVirtualRouterIPForNode(peerNodeID),
		peerHost:      "",
		peerPort:      0,
		dialer:        localNodeID == dialerNodeID,
		identity:      identity,
		controllerURL: resolveProbeControllerBaseURL(strings.TrimSpace(controllerBaseURL), ""),
	}
	if cfg.dialer {
		rememberProbeRouteTLSPin(cfg.routeID, cfg.peerTLSSPKI)
		cfg.peerHost = peerDomain
		cfg.peerPort = peerPort
	}
	return cfg, true
}

func probeVirtualRouterRuntimeRouteID(rule probeVirtualRouterTopologyRule) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"route",
		strings.TrimSpace(rule.ID),
	}, "|")))
	return probeVirtualRouterRuntimeRouteIDPrefix + hex.EncodeToString(sum[:])[:24]
}

func probeVirtualRouterRuleDialerNodeID(rule probeVirtualRouterTopologyRule) string {
	fromNodeID := normalizeProbeRouteNodeID(rule.FromNodeID)
	toNodeID := normalizeProbeRouteNodeID(rule.ToNodeID)
	if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
		return ""
	}
	return fromNodeID
}

func isProbeVirtualRouterCloudflareCopilotDomain(domain string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(domain)), "api_copilot_")
}
