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
	probeVirtualRouterRuntimeChainIDPrefix    = "vrouter-"
	probeVirtualRouterRuntimeLinkLayer        = "websocket"
	probeVirtualRouterRuntimeRole             = "virtual_router"
	probeVirtualRouterFrameLinkTXBufferFrames = 1024
	probeVirtualRouterFrameLinkRXBufferFrames = 1024
)

type probeVirtualRouterRuntimeConfig struct {
	chainID       string
	name          string
	userID        string
	rawPublicKey  string
	userPublicKey ed25519.PublicKey
	secret        string
	authTicket    string
	listenHost    string
	listenPort    int
	linkLayer     string
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
	closeOnce   sync.Once
}

type probeVirtualRouterFrameLink struct {
	key           string
	runtime       *probeVirtualRouterRuntime
	carrier       *probeVirtualRouterPhysicalCarrier
	requestPath   []string
	openedAt      time.Time
	lastUsed      time.Time
	tx            chan probeVirtualRouterFrame
	rx            chan probeVirtualRouterFrame
	done          chan struct{}
	carrierNotify chan struct{}
	startOnce     sync.Once
	closeOnce     sync.Once
	mu            sync.Mutex
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
	ChainID           string
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

var probeVirtualRouterFrameLinkState = struct {
	mu    sync.Mutex
	links map[string]*probeVirtualRouterFrameLink
}{links: make(map[string]*probeVirtualRouterFrameLink)}

var probeVirtualRouterRuleRuntimeState = struct {
	mu    sync.RWMutex
	items map[string]*probeVirtualRouterRuleRuntime
}{items: make(map[string]*probeVirtualRouterRuleRuntime)}

func isProbeVirtualRouterRuntimeChainID(chainID string) bool {
	return strings.HasPrefix(strings.TrimSpace(chainID), probeVirtualRouterRuntimeChainIDPrefix)
}

func applyProbeVirtualRouterRuntimesForNode(identity nodeIdentity, controllerBaseURL string, config probeVirtualRouterConfig) {
	localNodeID := normalizeProbeChainNodeID(identity.NodeID)
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
		desired[strings.TrimSpace(cfg.chainID)] = struct{}{}
	}
	stopProbeVirtualRouterRuntimesExcept(desired, "virtual router topology changed")
	clearProbeVirtualRouterRuleRuntimesExcept(desired)
	for _, cfg := range configs {
		if strings.TrimSpace(cfg.chainID) == "" {
			continue
		}
		if isSameProbeVirtualRouterRuntimeConfig(cfg.chainID, cfg) {
			updateRunningProbeVirtualRouterAuthTicket(cfg.chainID, cfg.authTicket)
			upsertProbeVirtualRouterRuleRuntime(config, cfg)
			continue
		}
		if _, err := startProbeVirtualRouterRuntime(cfg); err != nil {
			log.Printf("warning: probe virtual router runtime start failed: chain=%s local=%s peer=%s err=%v", cfg.chainID, localNodeID, cfg.peerNodeID, err)
			clearProbeVirtualRouterRuleRuntime(cfg.chainID)
			continue
		}
		upsertProbeVirtualRouterRuleRuntime(config, cfg)
	}
}

func startProbeVirtualRouterRuntime(cfg probeVirtualRouterRuntimeConfig) (*probeVirtualRouterRuntime, error) {
	cfg.chainID = strings.TrimSpace(cfg.chainID)
	if cfg.chainID == "" {
		return nil, errors.New("virtual router chain_id is required")
	}
	_ = stopProbeVirtualRouterRuntime(cfg.chainID, "restart before apply")
	if cfg.authTicket == "" {
		cfg.authTicket = lookupProbeChainAuthTicket(cfg.chainID)
	}
	if cfg.authTicket == "" {
		return nil, errors.New("virtual router auth ticket is required")
	}
	rememberProbeChainAuthTicket(cfg.chainID, cfg.authTicket)
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
	probeVirtualRouterRuntimeState.runtimes[cfg.chainID] = rt
	probeVirtualRouterRuntimeState.mu.Unlock()
	startProbeVirtualRouterBridgeWorker(rt)
	startProbeVirtualRouterKeepAliveWorker(rt)
	listenText := "-"
	if !cfg.dialer {
		listenText = net.JoinHostPort(cfg.listenHost, strconv.Itoa(cfg.listenPort))
	}
	log.Printf("probe virtual router runtime started: chain=%s listen=%s peer=%s dialer=%t", cfg.chainID, listenText, cfg.peerNodeID, cfg.dialer)
	return rt, nil
}

func stopProbeVirtualRouterRuntime(chainID string, reason string) bool {
	target := strings.TrimSpace(chainID)
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
	log.Printf("probe virtual router runtime stopped: chain=%s reason=%s", target, strings.TrimSpace(reason))
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

func getProbeVirtualRouterRuntime(chainID string) *probeVirtualRouterRuntime {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return nil
	}
	probeVirtualRouterRuntimeState.mu.RLock()
	rt := probeVirtualRouterRuntimeState.runtimes[cleanID]
	probeVirtualRouterRuntimeState.mu.RUnlock()
	return rt
}

func isSameProbeVirtualRouterRuntimeConfig(chainID string, cfg probeVirtualRouterRuntimeConfig) bool {
	rt := getProbeVirtualRouterRuntime(chainID)
	if rt == nil {
		return false
	}
	c := rt.cfg
	return c.listenHost == cfg.listenHost &&
		c.listenPort == cfg.listenPort &&
		c.linkLayer == cfg.linkLayer &&
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

func updateRunningProbeVirtualRouterAuthTicket(chainID string, authTicket string) {
	cleanTicket := strings.TrimSpace(authTicket)
	if cleanTicket == "" {
		return
	}
	rt := getProbeVirtualRouterRuntime(chainID)
	if rt == nil {
		return
	}
	rt.cfg.authTicket = cleanTicket
	rememberProbeChainAuthTicket(rt.cfg.chainID, cleanTicket)
}

func (rt *probeVirtualRouterRuntime) closeRuntimeResources() {
	if rt == nil {
		return
	}
	if rt.relay != nil {
		closeProbeVirtualRouterRelayServer(rt.relay)
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
	handler := buildProbeVirtualRouterRelayHandler()
	cert, err := prepareProbeServerCertificate(cfg.identity, strings.TrimSpace(cfg.controllerURL))
	if err != nil {
		return fmt.Errorf("prepare virtual router relay certificate failed: %w", err)
	}
	listenConfig := net.ListenConfig{KeepAlive: probeChainRelayTCPKeepAlivePeriod}
	tcpListener, err := listenConfig.Listen(context.Background(), "tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen virtual router relay tcp failed: %w", err)
	}
	relay := &probeVirtualRouterRelayServer{
		listenAddr: listenAddr,
	}
	relay.httpsServer = &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if serveErr := relay.httpsServer.ServeTLS(tcpListener, cert.CertPath, cert.KeyPath); serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("probe virtual router relay exited: layer=websocket listen=%s err=%v", listenAddr, serveErr)
		}
	}()
	rt.relayListenAddr = listenAddr
	rt.relay = relay
	log.Printf("probe virtual router relay started: chain=%s listen=%s", cfg.chainID, listenAddr)
	return nil
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
	mux.HandleFunc(probeChainRelayAPIPath, func(w http.ResponseWriter, r *http.Request) {
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
	chainID := resolveProbeChainIDFromRequest(r)
	if strings.TrimSpace(chainID) == "" {
		log.Printf("probe virtual router relay request rejected: remote=%s method=%s proto=%s host=%s reason=missing_chain_id", r.RemoteAddr, r.Method, r.Proto, r.Host)
		http.Error(w, "chain_id is required", http.StatusBadRequest)
		return
	}
	rt := getProbeVirtualRouterRuntime(chainID)
	if rt == nil {
		log.Printf("probe virtual router relay request rejected: requested_chain=%s remote=%s method=%s proto=%s host=%s reason=runtime_not_found", strings.TrimSpace(chainID), r.RemoteAddr, r.Method, r.Proto, r.Host)
		http.Error(w, "virtual router runtime not found", http.StatusNotFound)
		return
	}
	if err := verifyProbeVirtualRouterRelayRequestAuth(rt, r, chainID); err != nil {
		log.Printf("probe virtual router relay request rejected: chain=%s remote=%s method=%s proto=%s host=%s reason=unauthorized err=%v", rt.cfg.chainID, r.RemoteAddr, r.Method, r.Proto, r.Host, err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	bridgeRole := normalizeProbeChainBridgeRole(r.Header.Get(probeChainCodexRelayRoleHeader))
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
		ReadBufferSize:    probeChainRelayWebSocketBufferBytes,
		WriteBufferSize:   probeChainRelayWebSocketBufferBytes,
		EnableCompression: false,
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("probe virtual router websocket relay upgrade failed: chain=%s remote=%s err=%v", rt.cfg.chainID, r.RemoteAddr, err)
		return
	}
	defer ws.Close()
	conn := newWebSocketNetConn(ws)
	sessionID := rt.nextBridgeSessionID("vrouter-carrier")
	runProbeVirtualRouterPhysicalCarrier(rt, conn, sessionID, strings.TrimSpace(r.RemoteAddr))
}

func verifyProbeVirtualRouterRelayRequestAuth(rt *probeVirtualRouterRuntime, r *http.Request, chainID string) error {
	if rt == nil {
		return errors.New("virtual router runtime is nil")
	}
	env, err := readProbeChainAuthEnvelopeFromHeaders(r.Header, chainID)
	if err != nil {
		delayProbeChainAuthFailure()
		return err
	}
	if err := verifyProbeVirtualRouterInboundAuth(rt.cfg, env); err != nil {
		delayProbeChainAuthFailure()
		return err
	}
	resetProbeChainAuthFailure(resolveProbeChainSourceIPFromRequest(r))
	return nil
}

func verifyProbeVirtualRouterInboundAuth(cfg probeVirtualRouterRuntimeConfig, env probeChainAuthEnvelope) error {
	if env.ChainID != "" && env.ChainID != cfg.chainID {
		return fmt.Errorf("virtual router chain id mismatch")
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
	if err := verifyProbeVirtualRouterUserAuthTicket(cfg, env.AuthTicket); err != nil {
		return err
	}
	if err := recordProbeChainAuthNonce(cfg.chainID, env.Nonce); err != nil {
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

func startProbeVirtualRouterBridgeWorker(rt *probeVirtualRouterRuntime) {
	if rt == nil || !rt.cfg.dialer || strings.TrimSpace(rt.cfg.peerHost) == "" || rt.cfg.peerPort <= 0 {
		return
	}
	go runProbeVirtualRouterBridgeDialer(rt)
}

func runProbeVirtualRouterBridgeDialer(rt *probeVirtualRouterRuntime) {
	backoff := probeChainBridgeRetryMin
	for {
		select {
		case <-rt.stopCh:
			return
		default:
		}
		conn, err := openProbeVirtualRouterBridgeRelayNetConnWithDomainPolicy(
			rt.cfg.chainID,
			rt.cfg.secret,
			rt.cfg.peerHost,
			rt.cfg.peerPort,
			rt.cfg.linkLayer,
			probeChainBridgeRoleToNext,
			probeChainPortForwardDialTimeout+probeChainPortForwardResponseReadDeadline,
			true,
		)
		if err != nil {
			log.Printf("probe virtual router bridge dial failed: chain=%s peer=%s:%d err=%v", rt.cfg.chainID, rt.cfg.peerHost, rt.cfg.peerPort, err)
			sleepProbeVirtualRouterBridgeBackoff(rt, backoff)
			backoff = nextProbeChainBridgeBackoff(backoff)
			continue
		}
		backoff = probeChainBridgeRetryMin
		sessionID := rt.nextBridgeSessionID("vrouter-carrier")
		runProbeVirtualRouterPhysicalCarrier(rt, conn, sessionID, net.JoinHostPort(rt.cfg.peerHost, strconv.Itoa(rt.cfg.peerPort)))
		sleepProbeVirtualRouterBridgeBackoff(rt, backoff)
		backoff = nextProbeChainBridgeBackoff(backoff)
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
	for chainID := range probeVirtualRouterRuleRuntimeState.items {
		if desired != nil {
			if _, ok := desired[chainID]; ok {
				continue
			}
		}
		delete(probeVirtualRouterRuleRuntimeState.items, chainID)
	}
	probeVirtualRouterRuleRuntimeState.mu.Unlock()
}

func clearProbeVirtualRouterRuleRuntime(chainID string) {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return
	}
	probeVirtualRouterRuleRuntimeState.mu.Lock()
	delete(probeVirtualRouterRuleRuntimeState.items, cleanID)
	probeVirtualRouterRuleRuntimeState.mu.Unlock()
}

func upsertProbeVirtualRouterRuleRuntime(config probeVirtualRouterConfig, runtimeConfig probeVirtualRouterRuntimeConfig) {
	chainID := strings.TrimSpace(runtimeConfig.chainID)
	if chainID == "" {
		return
	}
	rule, ok := probeVirtualRouterRuleByChainID(config, chainID)
	if !ok {
		return
	}
	item := &probeVirtualRouterRuleRuntime{
		RuleID:            strings.TrimSpace(rule.ID),
		RuleName:          strings.TrimSpace(rule.Name),
		ChainID:           chainID,
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
	probeVirtualRouterRuleRuntimeState.items[chainID] = item
	probeVirtualRouterRuleRuntimeState.mu.Unlock()
}

func probeVirtualRouterRuleByChainID(config probeVirtualRouterConfig, chainID string) (probeVirtualRouterTopologyRule, bool) {
	target := strings.TrimSpace(chainID)
	for _, rule := range config.TopologyRules {
		if probeVirtualRouterRuntimeChainID(rule) == target {
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
		left := firstNonEmpty(strings.TrimSpace(items[i].RuleID), strings.TrimSpace(items[i].ChainID))
		right := firstNonEmpty(strings.TrimSpace(items[j].RuleID), strings.TrimSpace(items[j].ChainID))
		return left < right
	})
	return items
}

func buildProbeVirtualRouterRuntimeConfigsForNode(config probeVirtualRouterConfig, identity nodeIdentity, controllerBaseURL string) []probeVirtualRouterRuntimeConfig {
	localNodeID := normalizeProbeChainNodeID(identity.NodeID)
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
		fromNodeID := normalizeProbeChainNodeID(rule.FromNodeID)
		toNodeID := normalizeProbeChainNodeID(rule.ToNodeID)
		if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
			continue
		}
		if localNodeID != fromNodeID && localNodeID != toNodeID {
			continue
		}
		cfg, ok := buildProbeVirtualRouterRuntimeConfigForRule(rule, identity, controllerBaseURL)
		if !ok {
			continue
		}
		if _, exists := seen[cfg.chainID]; exists {
			continue
		}
		seen[cfg.chainID] = struct{}{}
		out = append(out, cfg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].chainID < out[j].chainID
	})
	return out
}

func buildProbeVirtualRouterRuntimeConfigForRule(rule probeVirtualRouterTopologyRule, identity nodeIdentity, controllerBaseURL string) (probeVirtualRouterRuntimeConfig, bool) {
	localNodeID := normalizeProbeChainNodeID(identity.NodeID)
	fromNodeID := normalizeProbeChainNodeID(rule.FromNodeID)
	toNodeID := normalizeProbeChainNodeID(rule.ToNodeID)
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
	peerPort := normalizeProbeVirtualRouterServicePort(rule.ToServicePort)
	localPort := 0
	if localIsTo {
		peerNodeID = fromNodeID
		localPort = normalizeProbeVirtualRouterServicePort(rule.ToServicePort)
	}

	chainID := probeVirtualRouterRuntimeChainID(rule)
	dialerNodeID := probeVirtualRouterRuleDialerNodeID(rule)
	if dialerNodeID == "" {
		return probeVirtualRouterRuntimeConfig{}, false
	}
	secret := strings.TrimSpace(rule.Secret)
	authTicket := strings.TrimSpace(rule.AuthTicket)
	rawPublicKey := strings.TrimSpace(rule.UserPublicKey)
	if secret == "" || authTicket == "" || rawPublicKey == "" {
		log.Printf("warning: probe virtual router rule skipped: chain=%s missing link auth fields", chainID)
		return probeVirtualRouterRuntimeConfig{}, false
	}
	userPublicKey, err := parseProbeChainUserPublicKey(rawPublicKey)
	if err != nil {
		log.Printf("warning: probe virtual router rule skipped: chain=%s invalid user_public_key: %v", chainID, err)
		return probeVirtualRouterRuntimeConfig{}, false
	}
	cfg := probeVirtualRouterRuntimeConfig{
		chainID:       chainID,
		name:          "Virtual Router " + firstNonEmpty(strings.TrimSpace(rule.Name), strings.TrimSpace(rule.ID), fromNodeID+"-"+toNodeID),
		userID:        strings.TrimSpace(rule.UserID),
		rawPublicKey:  rawPublicKey,
		userPublicKey: userPublicKey,
		secret:        secret,
		authTicket:    authTicket,
		listenHost:    "0.0.0.0",
		listenPort:    localPort,
		linkLayer:     probeVirtualRouterRuntimeLinkLayer,
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

func probeVirtualRouterRuntimeChainID(rule probeVirtualRouterTopologyRule) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"chain",
		strings.TrimSpace(rule.ID),
	}, "|")))
	return probeVirtualRouterRuntimeChainIDPrefix + hex.EncodeToString(sum[:])[:24]
}

func probeVirtualRouterRuleDialerNodeID(rule probeVirtualRouterTopologyRule) string {
	fromNodeID := normalizeProbeChainNodeID(rule.FromNodeID)
	toNodeID := normalizeProbeChainNodeID(rule.ToNodeID)
	if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
		return ""
	}
	return fromNodeID
}
