package main

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
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
)

const (
	probeVirtualRouterRuntimeRouteIDPrefix                 = "vrouter-"
	probeVirtualRouterRuntimeRouteLayer                    = "websocket"
	probeVirtualRouterRuntimeRole                          = "virtual_router"
	probeVirtualRouterFrameLinkTXBufferFrames              = 1024
	probeVirtualRouterFrameLinkRXBufferFrames              = 1024
	probeVirtualRouterFrameLinkRXDispatchShards            = 8
	probeVirtualRouterFrameLinkRXDispatchShardBufferFrames = probeVirtualRouterFrameLinkRXBufferFrames / probeVirtualRouterFrameLinkRXDispatchShards
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
	listenAddr  string
	httpsServer *http.Server
	routeIDs    map[string]struct{}
	closeOnce   sync.Once
}

type probeVirtualRouterFrameLink struct {
	key              string
	runtime          *probeVirtualRouterRuntime
	carrier          *probeVirtualRouterPhysicalCarrier
	requestPath      []string
	openedAt         time.Time
	lastUsed         time.Time
	tx               chan probeVirtualRouterFrame
	rx               chan probeVirtualRouterFrame
	rxDispatchShards []chan probeVirtualRouterFrame
	done             chan struct{}
	carrierNotify    chan struct{}
	startOnce        sync.Once
	closeOnce        sync.Once
	mu               sync.Mutex
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
		listenAddr: listenAddr,
		routeIDs:   map[string]struct{}{routeID: {}},
	}
	relay.httpsServer = &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	probeVirtualRouterRelayServerState.servers[listenAddr] = relay
	probeVirtualRouterRelayServerState.mu.Unlock()
	go func() {
		if serveErr := relay.httpsServer.ServeTLS(tcpListener, cert.CertPath, cert.KeyPath); serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("probe virtual router relay exited: layer=websocket listen=%s err=%v", listenAddr, serveErr)
		}
	}()
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

func verifyProbeVirtualRouterRelayRequestAuth(rt *probeVirtualRouterRuntime, r *http.Request, routeID string) error {
	if rt == nil {
		return errors.New("virtual router runtime is nil")
	}
	env, err := readProbeRouteAuthEnvelopeFromHeaders(r.Header, routeID)
	if err != nil {
		delayProbeRouteAuthFailure()
		return err
	}
	if err := verifyProbeVirtualRouterInboundAuth(rt.cfg, env); err != nil {
		delayProbeRouteAuthFailure()
		return err
	}
	resetProbeRouteAuthFailure(resolveProbeRouteSourceIPFromRequest(r))
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
	if mode != "" && mode != "secret_hmac" && mode != "hmac" {
		return fmt.Errorf("unsupported auth mode")
	}
	if strings.TrimSpace(cfg.secret) == "" {
		return fmt.Errorf("secret is not configured")
	}
	if env.MAC == "" {
		return fmt.Errorf("mac is required")
	}
	expected := buildProbeRouteHMAC(cfg.secret, cfg.routeID, env.Nonce)
	if !hmac.Equal([]byte(strings.ToLower(env.MAC)), []byte(strings.ToLower(expected))) {
		return fmt.Errorf("authentication failed")
	}
	if err := verifyProbeVirtualRouterUserAuthTicket(cfg, env.AuthTicket); err != nil {
		return err
	}
	if err := recordProbeRouteAuthNonce(cfg.routeID, env.Nonce); err != nil {
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
	if strings.TrimSpace(payload.Version) != "route-auth-v1" {
		return fmt.Errorf("unsupported user auth ticket version")
	}
	if strings.TrimSpace(payload.RouteID) != strings.TrimSpace(cfg.routeID) {
		return fmt.Errorf("user auth ticket route mismatch")
	}
	if strings.TrimSpace(payload.UserPublicKey) != strings.TrimSpace(cfg.rawPublicKey) {
		return fmt.Errorf("user auth ticket public key mismatch")
	}
	if err := verifyProbeRouteAuthTicketIssuedAt(payload.IssuedAt, probeRouteAuthTicketNow()); err != nil {
		return err
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
			true,
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
	peerDomain := strings.TrimSpace(rule.ToServiceDomain)
	listenerPort := probeVirtualRouterServicePortForNode(config, toNodeID, rule.ToServicePort)
	peerPort := listenerPort
	if isProbeVirtualRouterCloudflareCopilotDomain(peerDomain) {
		peerPort = 443
	}
	localPort := 0
	if localIsTo {
		peerNodeID = fromNodeID
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
		routeLayer:    probeVirtualRouterRuntimeRouteLayer,
		fromNodeID:    fromNodeID,
		toNodeID:      toNodeID,
		localNodeID:   localNodeID,
		peerNodeID:    peerNodeID,
		localIP:       currentProbeVirtualRouterIPForNode(localNodeID),
		peerIP:        currentProbeVirtualRouterIPForNode(peerNodeID),
		peerHost:      "",
		peerPort:      0,
		dialer:        localNodeID == dialerNodeID,
		identity:      identity,
		controllerURL: resolveProbeControllerBaseURL(strings.TrimSpace(controllerBaseURL), ""),
	}
	if cfg.dialer {
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
