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
	nextHost        string
	nextPort        int
	requireUserAuth bool
	nextAuthMode    string
	identity        nodeIdentity
	controllerURL   string
}

type probeChainRuntime struct {
	cfg           probeChainRuntimeConfig
	relayListener net.Listener
	relayAddr     string
	httpsServer   *http.Server
	http3Server   *http3.Server
	stopCh        chan struct{}
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

type probeChainRuntimeCacheFile struct {
	Items []probeChainRuntimeCacheItem `json:"items"`
}

type probeChainRuntimeCacheItem struct {
	ChainID         string `json:"chain_id"`
	Name            string `json:"name"`
	UserID          string `json:"user_id"`
	UserPublicKey   string `json:"user_public_key"`
	LinkSecret      string `json:"link_secret"`
	Role            string `json:"role"`
	ListenHost      string `json:"listen_host"`
	ListenPort      int    `json:"listen_port"`
	LinkLayer       string `json:"link_layer"`
	NextHost        string `json:"next_host"`
	NextPort        int    `json:"next_port"`
	RequireUserAuth bool   `json:"require_user_auth"`
	NextAuthMode    string `json:"next_auth_mode"`
}

var probeChainRuntimeState = struct {
	mu       sync.Mutex
	runtimes map[string]*probeChainRuntime
}{runtimes: make(map[string]*probeChainRuntime)}

const (
	probeChainRuntimeCacheFileName = "probe_link_chains_cache.json"
	probeChainRelayAPIPath         = "/api/node/chain/relay"
	probeChainSourceIPHintPrefix   = "CHSRCIP "
	probeChainAuthNoncePrefix      = "CHNONCE "

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
	secret := strings.TrimSpace(cmd.LinkSecret)
	requireUserAuth := cmd.RequireUserAuth
	nextAuthMode := normalizeProbeChainAuthMode(cmd.NextAuthMode)
	if nextAuthMode != "proxy" {
		if nextHost == "" || nextPort <= 0 {
			return probeChainRuntimeConfig{}, fmt.Errorf("next_host and next_port are required")
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
		nextHost:        nextHost,
		nextPort:        nextPort,
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

	if err := startProbeChainInternalRelayListener(rt); err != nil {
		return nil, err
	}
	if err := startProbeChainPublicRelayServer(rt); err != nil {
		close(rt.stopCh)
		rt.closeRuntimeResources()
		return nil, err
	}

	probeChainRuntimeState.mu.Lock()
	probeChainRuntimeState.runtimes[cfg.chainID] = rt
	probeChainRuntimeState.mu.Unlock()
	if err := persistProbeChainRuntimesToCache(); err != nil {
		log.Printf("warning: persist probe chain runtime cache failed: %v", err)
	}

	nextTarget := "proxy"
	if cfg.nextAuthMode != "proxy" {
		nextTarget = net.JoinHostPort(cfg.nextHost, strconv.Itoa(cfg.nextPort))
	}
	log.Printf(
		"probe chain runtime started: chain=%s role=%s listen=%s layer=%s relay_internal=%s next_mode=%s next=%s",
		cfg.chainID,
		cfg.role,
		net.JoinHostPort(cfg.listenHost, strconv.Itoa(cfg.listenPort)),
		normalizeProbeChainLinkLayer(cfg.linkLayer),
		strings.TrimSpace(rt.relayAddr),
		cfg.nextAuthMode,
		nextTarget,
	)
	return rt, nil
}

func startProbeChainInternalRelayListener(runtime *probeChainRuntime) error {
	if runtime == nil {
		return errors.New("runtime is nil")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	runtime.relayListener = ln
	runtime.relayAddr = strings.TrimSpace(ln.Addr().String())

	go func(rt *probeChainRuntime) {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				select {
				case <-rt.stopCh:
					return
				default:
				}
				log.Printf("probe chain runtime internal accept failed: chain=%s err=%v", rt.cfg.chainID, acceptErr)
				continue
			}
			go handleProbeChainConn(rt, conn)
		}
	}(runtime)

	return nil
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

	chainID := strings.TrimSpace(r.URL.Query().Get("chain_id"))
	if chainID == "" {
		chainID = strings.TrimSpace(r.Header.Get("X-CH-Chain-ID"))
	}
	if chainID == "" {
		http.Error(w, "chain_id is required", http.StatusBadRequest)
		return
	}
	if chainID != strings.TrimSpace(runtime.cfg.chainID) {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}

	dialAddr := strings.TrimSpace(runtime.relayAddr)
	if dialAddr == "" {
		http.Error(w, "chain runtime relay is unavailable", http.StatusServiceUnavailable)
		return
	}
	targetConn, err := net.DialTimeout("tcp", dialAddr, 10*time.Second)
	if err != nil {
		http.Error(w, "dial chain runtime failed", http.StatusBadGateway)
		return
	}
	defer targetConn.Close()

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
	sourceIP := resolveProbeChainSourceIPFromRequest(r)
	done := make(chan error, 2)
	go func() {
		if strings.TrimSpace(sourceIP) != "" {
			_, _ = io.WriteString(targetConn, probeChainSourceIPHintPrefix+sourceIP+"\n")
		}
		_, copyErr := io.Copy(targetConn, r.Body)
		closeProbeChainConnWrite(targetConn)
		done <- copyErr
	}()
	go func() {
		_, copyErr := io.Copy(streamWriter, targetConn)
		done <- copyErr
	}()
	<-done
}

func (rt *probeChainRuntime) closeRuntimeResources() {
	if rt == nil {
		return
	}
	if rt.httpsServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		_ = rt.httpsServer.Shutdown(ctx)
		cancel()
	}
	if rt.http3Server != nil {
		_ = rt.http3Server.Close()
	}
	if rt.relayListener != nil {
		_ = rt.relayListener.Close()
	}
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

	sourceIP := resolveProbeChainSourceIPFromAddr(conn.RemoteAddr())
	if blocked, until := isProbeChainAuthIPBlacklisted(sourceIP); blocked {
		delayProbeChainAuthFailure()
		_, _ = io.WriteString(conn, "CHAUTHERR auth failed\n")
		log.Printf("probe chain auth rejected (ip blacklisted): chain=%s ip=%s until=%s", runtime.cfg.chainID, sourceIP, until.UTC().Format(time.RFC3339))
		return
	}

	reader := bufio.NewReader(conn)
	if hintedIP, hintErr := readProbeChainSourceIPHint(reader); hintErr != nil {
		recordProbeChainAuthFailure(sourceIP)
		delayProbeChainAuthFailure()
		_, _ = io.WriteString(conn, "CHAUTHERR auth failed\n")
		log.Printf("probe chain auth source hint parse failed: chain=%s ip=%s err=%v", runtime.cfg.chainID, sourceIP, hintErr)
		return
	} else if hintedIP != "" {
		sourceIP = hintedIP
		if blocked, until := isProbeChainAuthIPBlacklisted(sourceIP); blocked {
			delayProbeChainAuthFailure()
			_, _ = io.WriteString(conn, "CHAUTHERR auth failed\n")
			log.Printf("probe chain auth rejected (hinted ip blacklisted): chain=%s ip=%s until=%s", runtime.cfg.chainID, sourceIP, until.UTC().Format(time.RFC3339))
			return
		}
	}

	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	nonce, err := sendProbeChainNonceChallenge(conn)
	if err != nil {
		return
	}
	env, err := readProbeChainAuthEnvelope(reader)
	if err != nil {
		failures, blacklisted, until := recordProbeChainAuthFailure(sourceIP)
		delayProbeChainAuthFailure()
		_, _ = io.WriteString(conn, "CHAUTHERR auth failed\n")
		logProbeChainAuthFailure(runtime.cfg.chainID, sourceIP, failures, blacklisted, until, err)
		return
	}
	if err := verifyProbeChainInboundAuth(runtime.cfg, env, nonce); err != nil {
		failures, blacklisted, until := recordProbeChainAuthFailure(sourceIP)
		delayProbeChainAuthFailure()
		_, _ = io.WriteString(conn, "CHAUTHERR auth failed\n")
		logProbeChainAuthFailure(runtime.cfg.chainID, sourceIP, failures, blacklisted, until, err)
		return
	}
	resetProbeChainAuthFailure(sourceIP)
	if _, err := io.WriteString(conn, "CHAUTHOK\n"); err != nil {
		return
	}
	if runtime.cfg.nextAuthMode == "proxy" {
		_ = conn.SetDeadline(time.Time{})
		if err := handleProbeChainProxyConn(runtime, conn, reader); err != nil {
			log.Printf("probe chain proxy failed: chain=%s role=%s remote=%s err=%v", runtime.cfg.chainID, runtime.cfg.role, conn.RemoteAddr().String(), err)
		}
		return
	}

	nextHop, err := openProbeChainNextHop(runtime.cfg)
	if err != nil {
		return
	}
	defer func() {
		if nextHop != nil && nextHop.CloseFn != nil {
			_ = nextHop.CloseFn()
		}
	}()
	nextReader := bufio.NewReader(nextHop.Reader)
	if nextConn, ok := nextHop.Reader.(net.Conn); ok {
		_ = nextConn.SetDeadline(time.Now().Add(20 * time.Second))
	}
	if runtime.cfg.nextAuthMode == "secret" {
		if err := sendProbeChainSecretAuth(nextHop.Writer, nextReader, runtime.cfg.chainID, runtime.cfg.secret); err != nil {
			return
		}
	}
	if nextConn, ok := nextHop.Reader.(net.Conn); ok {
		_ = nextConn.SetDeadline(time.Time{})
	}
	_ = conn.SetDeadline(time.Time{})

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

func openProbeChainNextHop(cfg probeChainRuntimeConfig) (*probeChainNextHop, error) {
	switch normalizeProbeChainLinkLayer(cfg.linkLayer) {
	case "http":
		return openProbeChainHTTPRelayHop(cfg, "http")
	case "http2":
		return openProbeChainHTTPRelayHop(cfg, "http2")
	case "http3":
		return openProbeChainHTTPRelayHop(cfg, "http3")
	default:
		return openProbeChainHTTPRelayHop(cfg, "http")
	}
}

func openProbeChainHTTPRelayHop(cfg probeChainRuntimeConfig, layer string) (*probeChainNextHop, error) {
	relayDialHost, relayHostHeader, err := resolveProbeChainDialIPHost(cfg.nextHost)
	if err != nil {
		return nil, err
	}
	tlsServerName := resolveProbeChainTLSServerName(layer, relayDialHost, relayHostHeader)
	relayURL, err := buildProbeChainRelayURL(relayDialHost, cfg.nextPort, cfg.chainID)
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
	request.Header.Set("X-CH-Chain-ID", strings.TrimSpace(cfg.chainID))
	if strings.TrimSpace(relayHostHeader) != "" {
		request.Host = strings.TrimSpace(relayHostHeader)
	}

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

	return &probeChainNextHop{
		Writer: bodyWriter,
		Reader: response.Body,
		CloseFn: func() error {
			cancel()
			_ = bodyWriter.Close()
			_ = response.Body.Close()
			_ = closeTransport()
			return nil
		},
	}, nil
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

func resolveProbeChainTLSServerName(layer string, dialHost string, hostHeader string) string {
	cleanDialHost := strings.TrimSpace(strings.Trim(dialHost, "[]"))
	cleanHostHeader := strings.TrimSpace(strings.Trim(hostHeader, "[]"))

	if normalizeProbeChainLinkLayer(layer) == "http" {
		return cleanDialHost
	}
	if cleanHostHeader != "" {
		if parsed := net.ParseIP(cleanHostHeader); parsed == nil {
			return cleanHostHeader
		}
	}
	if cleanDialHost != "" {
		return cleanDialHost
	}
	return cleanHostHeader
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

func handleProbeChainRelayHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chainID := strings.TrimSpace(r.URL.Query().Get("chain_id"))
	if chainID == "" {
		chainID = strings.TrimSpace(r.Header.Get("X-CH-Chain-ID"))
	}
	if chainID == "" {
		http.Error(w, "chain_id is required", http.StatusBadRequest)
		return
	}

	runtime := getProbeChainRuntime(chainID)
	if runtime == nil {
		http.Error(w, "chain runtime not found", http.StatusNotFound)
		return
	}
	dialAddr := strings.TrimSpace(runtime.relayAddr)
	if dialAddr == "" {
		dialHost := resolveProbeChainLoopbackHost(runtime.cfg.listenHost)
		dialAddr = net.JoinHostPort(dialHost, strconv.Itoa(runtime.cfg.listenPort))
	}
	targetConn, err := net.DialTimeout("tcp", dialAddr, 10*time.Second)
	if err != nil {
		http.Error(w, "dial chain runtime failed", http.StatusBadGateway)
		return
	}
	defer targetConn.Close()

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
	sourceIP := resolveProbeChainSourceIPFromRequest(r)
	done := make(chan error, 2)
	go func() {
		if strings.TrimSpace(sourceIP) != "" {
			_, _ = io.WriteString(targetConn, probeChainSourceIPHintPrefix+sourceIP+"\n")
		}
		_, copyErr := io.Copy(targetConn, r.Body)
		closeProbeChainConnWrite(targetConn)
		done <- copyErr
	}()
	go func() {
		_, copyErr := io.Copy(streamWriter, targetConn)
		done <- copyErr
	}()
	<-done
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
	session, err := yamux.Server(baseConn, newProbeChainYamuxConfig())
	if err != nil {
		return err
	}
	defer session.Close()

	for {
		stream, acceptErr := session.Accept()
		if acceptErr != nil {
			if errors.Is(acceptErr, io.EOF) || errors.Is(acceptErr, net.ErrClosed) {
				return nil
			}
			return acceptErr
		}
		go handleProbeChainProxyStream(runtime, stream)
	}
}

func handleProbeChainProxyStream(runtime *probeChainRuntime, stream net.Conn) {
	if stream == nil {
		return
	}
	defer stream.Close()

	_ = stream.SetReadDeadline(time.Now().Add(20 * time.Second))
	var req probeChainTunnelOpenRequest
	if err := json.NewDecoder(stream).Decode(&req); err != nil {
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
	dialer := &net.Dialer{Timeout: 10 * time.Second}
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

func verifyProbeChainInboundAuth(cfg probeChainRuntimeConfig, env probeChainAuthEnvelope, expectedNonce string) error {
	if env.ChainID != "" && env.ChainID != cfg.chainID {
		return fmt.Errorf("chain id mismatch")
	}
	if env.Nonce == "" {
		return fmt.Errorf("nonce is required")
	}
	challengeNonce := strings.TrimSpace(expectedNonce)
	if challengeNonce != "" && env.Nonce != challengeNonce {
		return fmt.Errorf("nonce mismatch")
	}
	if cfg.requireUserAuth {
		if cfg.userPublicKey == nil {
			return fmt.Errorf("user public key is not configured")
		}
		if env.Signature == "" {
			return fmt.Errorf("signature is required")
		}
		sig, err := base64.StdEncoding.DecodeString(env.Signature)
		if err != nil {
			return fmt.Errorf("invalid signature encoding")
		}
		if !ed25519.Verify(cfg.userPublicKey, []byte(env.Nonce), sig) {
			return fmt.Errorf("signature verification failed")
		}
		return nil
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

func sendProbeChainSecretAuth(nextWriter io.Writer, nextReader *bufio.Reader, chainID string, secret string) error {
	nonce, err := readProbeChainNonceChallenge(nextReader)
	if err != nil {
		return err
	}
	env := newProbeChainAuthEnvelope("secret_hmac", chainID, nonce, "", buildProbeChainHMAC(secret, chainID, nonce))
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
			NextHost:        item.NextHost,
			NextPort:        item.NextPort,
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
			NextHost:        cfg.nextHost,
			NextPort:        cfg.nextPort,
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
