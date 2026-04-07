package backend

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go/http3"
)

const (
	tunnelStreamOpenTimeout = 20 * time.Second

	muxAutoMaintainInterval        = 20 * time.Second
	muxAutoMaintainMinRetry        = 30 * time.Second
	muxAutoMaintainMaxRetry        = 5 * time.Minute
	muxAutoMaintainFailLogInterval = 30 * time.Second
	muxKeepAliveFailThreshold      = 3

	probeChainRelayAPIPath         = "/api/node/chain/relay"
	probeChainAuthPacketVersion    = "2025-03-22"
	probeChainLegacyChainIDHeader  = "X-CH-Chain-ID"
	probeChainCodexChainIDHeader   = "X-Codex-Chain-Id"
	probeChainCodexAuthModeHeader  = "X-Codex-Auth-Mode"
	probeChainCodexMACHeader       = "X-Codex-Mac"
	probeChainCodexVersionHeader   = "X-Codex-Api-Version"
	probeChainCodexRelayModeHeader = "X-Codex-Relay-Mode"
	probeChainCodexRelayRoleHeader = "X-Codex-Relay-Role"
	probeChainRelayModeBridge      = "bridge"
	probeChainBridgeRoleToNext     = "to_next"
)

var errTunnelStreamOpenTimeout = errors.New("open stream timeout")
var tunnelMuxClientSeq uint64

type probeChainRelayHop struct {
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

type tunnelOpenRequest struct {
	Type    string `json:"type"`
	Network string `json:"network"`
	Address string `json:"address"`
}

type tunnelOpenResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type tunnelInboundMessage struct {
	Type     string `json:"type"`
	Category string `json:"category,omitempty"`
	Message  string `json:"message,omitempty"`
}

type tunnelOpenRemoteError struct {
	message string
}

func (e *tunnelOpenRemoteError) Error() string {
	if e == nil {
		return "stream open failed"
	}
	msg := strings.TrimSpace(e.message)
	if msg == "" {
		return "stream open failed"
	}
	return msg
}

func isTunnelOpenRemoteError(err error) bool {
	var target *tunnelOpenRemoteError
	return errors.As(err, &target)
}

type tunnelMuxStream struct {
	client  *tunnelMuxClient
	id      string
	network string
	conn    net.Conn

	readCh chan []byte
	errCh  chan error
	closed atomic.Bool
}

type tunnelMuxClient struct {
	id      uint64
	nodeID  string
	modeKey string

	onControllerLog func(string, string)

	wsConn  *websocket.Conn
	session *yamux.Session

	mu                sync.Mutex
	streams           map[string]*tunnelMuxStream
	seq               uint64
	closed            atomic.Bool
	keepAliveFailures atomic.Int32

	lastRecv atomic.Int64
	lastPong atomic.Int64
}

func (c *tunnelMuxClient) snapshot() (connected bool, activeStreams int, lastRecv string, lastPong string) {
	connected = !c.isClosed()
	c.mu.Lock()
	activeStreams = len(c.streams)
	c.mu.Unlock()

	if ts := c.lastRecv.Load(); ts > 0 {
		lastRecv = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	if ts := c.lastPong.Load(); ts > 0 {
		lastPong = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	return
}

func newTunnelMuxClient(baseURL, token, nodeID string, onControllerLog func(string, string)) (*tunnelMuxClient, error) {
	tunnelURL, err := buildTunnelWSURL(baseURL, nodeID, token)
	if err != nil {
		return nil, err
	}

	header := http.Header{}
	header.Set("X-Forwarded-Proto", "https")
	header.Set("Authorization", "Bearer "+token)
	muxDialer := buildControllerWSDialer(baseURL)
	wsConn, handshakeResp, err := muxDialer.Dial(tunnelURL, header)
	if err != nil {
		if handshakeResp != nil {
			defer handshakeResp.Body.Close()
			raw, _ := io.ReadAll(io.LimitReader(handshakeResp.Body, 2048))
			return nil, fmt.Errorf("websocket handshake failed: status=%d body=%s", handshakeResp.StatusCode, strings.TrimSpace(string(raw)))
		}
		return nil, err
	}

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second

	session, err := yamux.Client(newWebSocketNetConn(wsConn), cfg)
	if err != nil {
		_ = wsConn.Close()
		return nil, err
	}

	c := &tunnelMuxClient{
		id:              atomic.AddUint64(&tunnelMuxClientSeq, 1),
		nodeID:          nodeID,
		modeKey:         "ws",
		onControllerLog: onControllerLog,
		wsConn:          wsConn,
		session:         session,
		streams:         make(map[string]*tunnelMuxStream),
	}
	now := time.Now().Unix()
	c.lastRecv.Store(now)
	c.lastPong.Store(now)

	go c.acceptLoop()
	go c.keepAliveLoop()
	return c, nil
}

func newTunnelMuxClientViaProbeChain(baseURL, token, nodeID string, endpoint probeChainEndpoint, onControllerLog func(string, string)) (*tunnelMuxClient, error) {
	hop, err := openProbeChainRelayHop(endpoint)
	if err != nil {
		return nil, err
	}

	relayConn := &probeChainRelayNetConn{
		reader:  hop.Reader,
		writer:  hop.Writer,
		closeFn: hop.CloseFn,
	}

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	session, err := yamux.Client(relayConn, cfg)
	if err != nil {
		_ = relayConn.Close()
		return nil, err
	}

	modeKey := fmt.Sprintf(
		"chain:%s@%s:%d/%s",
		strings.TrimSpace(endpoint.ChainID),
		strings.TrimSpace(endpoint.EntryHost),
		endpoint.EntryPort,
		strings.TrimSpace(endpoint.LinkLayer),
	)
	c := &tunnelMuxClient{
		id:              atomic.AddUint64(&tunnelMuxClientSeq, 1),
		nodeID:          nodeID,
		modeKey:         modeKey,
		onControllerLog: onControllerLog,
		wsConn:          nil,
		session:         session,
		streams:         make(map[string]*tunnelMuxStream),
	}
	now := time.Now().Unix()
	c.lastRecv.Store(now)
	c.lastPong.Store(now)

	go c.acceptLoop()
	go c.keepAliveLoop()
	return c, nil
}

func openProbeChainRelayHop(endpoint probeChainEndpoint) (*probeChainRelayHop, error) {
	entryURL, entryDialHost, entryHostHeader, err := buildProbeChainEntryURL(endpoint)
	if err != nil {
		return nil, err
	}

	bodyReader, bodyWriter := io.Pipe()
	request, err := http.NewRequest(http.MethodPost, entryURL, bodyReader)
	if err != nil {
		_ = bodyReader.Close()
		_ = bodyWriter.Close()
		return nil, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set(probeChainLegacyChainIDHeader, strings.TrimSpace(endpoint.ChainID))
	request.Header.Set(probeChainCodexChainIDHeader, strings.TrimSpace(endpoint.ChainID))
	request.Header.Set(probeChainCodexVersionHeader, probeChainAuthPacketVersion)
	request.Header.Set(probeChainCodexRelayModeHeader, probeChainRelayModeBridge)
	request.Header.Set(probeChainCodexRelayRoleHeader, probeChainBridgeRoleToNext)
	if err := applyProbeChainHMACAuthHeaders(request.Header, endpoint.ChainID, endpoint.ChainSecret); err != nil {
		_ = bodyReader.Close()
		_ = bodyWriter.Close()
		return nil, err
	}
	if strings.TrimSpace(entryHostHeader) != "" {
		request.Host = strings.TrimSpace(entryHostHeader)
	}

	layer := normalizeChainLinkLayerValue(endpoint.LinkLayer)
	tlsServerName := resolveProbeChainClientTLSServerName(layer, entryDialHost, entryHostHeader)
	const probeChainRelayConnectTimeout = 8 * time.Second

	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)

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
		client = &http.Client{
			Transport: transport,
			Timeout:   probeChainRelayConnectTimeout,
		}
		closeTransport = func() error { return transport.Close() }
	case "http2":
		dialer := &net.Dialer{Timeout: probeChainRelayConnectTimeout, KeepAlive: 30 * time.Second}
		transport := &http.Transport{
			Proxy:               nil,
			ForceAttemptHTTP2:   true,
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: probeChainRelayConnectTimeout,
			ResponseHeaderTimeout: probeChainRelayConnectTimeout,
			ExpectContinueTimeout: 1 * time.Second,
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				ServerName:         tlsServerName,
				InsecureSkipVerify: true,
			},
		}
		client = &http.Client{
			Transport: transport,
			Timeout:   probeChainRelayConnectTimeout,
		}
		closeTransport = func() error {
			transport.CloseIdleConnections()
			return nil
		}
	default:
		dialer := &net.Dialer{Timeout: probeChainRelayConnectTimeout, KeepAlive: 30 * time.Second}
		transport := &http.Transport{
			Proxy:                 nil,
			ForceAttemptHTTP2:     false,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   probeChainRelayConnectTimeout,
			ResponseHeaderTimeout: probeChainRelayConnectTimeout,
			ExpectContinueTimeout: 1 * time.Second,
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				ServerName:         tlsServerName,
				InsecureSkipVerify: true,
			},
			TLSNextProto: make(map[string]func(string, *tls.Conn) http.RoundTripper),
		}
		client = &http.Client{
			Transport: transport,
			Timeout:   probeChainRelayConnectTimeout,
		}
		closeTransport = func() error {
			transport.CloseIdleConnections()
			return nil
		}
	}

	startedAt := time.Now()
	response, err := client.Do(request)
	if err != nil {
		sanitizedURL := ""
		if parsed, parseErr := url.Parse(entryURL); parseErr == nil {
			parsed.RawQuery = ""
			sanitizedURL = parsed.String()
		}
		cancel()
		_ = bodyWriter.Close()
		_ = closeTransport()
		log.Printf("[probe-chain/relay] request failed chain=%s entry=%s:%d layer=%s elapsed=%s err=%v", strings.TrimSpace(endpoint.ChainID), entryDialHost, endpoint.EntryPort, layer, time.Since(startedAt), err)
		return nil, fmt.Errorf("probe relay connect failed: url=%s dial_host=%s host_header=%s layer=%s server_name=%s elapsed=%s err=%w", sanitizedURL, entryDialHost, entryHostHeader, layer, tlsServerName, time.Since(startedAt), err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		cancel()
		_ = response.Body.Close()
		_ = bodyWriter.Close()
		_ = closeTransport()
		log.Printf("[probe-chain/relay] request bad status chain=%s entry=%s:%d layer=%s status=%d elapsed=%s body=%s", strings.TrimSpace(endpoint.ChainID), entryDialHost, endpoint.EntryPort, layer, response.StatusCode, time.Since(startedAt), strings.TrimSpace(string(body)))
		return nil, fmt.Errorf("probe relay failed: status=%d elapsed=%s body=%s", response.StatusCode, time.Since(startedAt), strings.TrimSpace(string(body)))
	}

	return &probeChainRelayHop{
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

func buildProbeChainEntryURL(endpoint probeChainEndpoint) (string, string, string, error) {
	dialHost, hostHeader, err := resolveProbeChainDialIPHost(endpoint.EntryHost)
	if err != nil {
		return "", "", "", err
	}
	if endpoint.EntryPort <= 0 || endpoint.EntryPort > 65535 {
		return "", "", "", fmt.Errorf("invalid entry port")
	}
	u := &url.URL{
		Scheme: "https",
		Host:   net.JoinHostPort(dialHost, strconv.Itoa(endpoint.EntryPort)),
		Path:   probeChainRelayAPIPath,
	}
	query := u.Query()
	query.Set("chain_id", strings.TrimSpace(endpoint.ChainID))
	u.RawQuery = query.Encode()
	return u.String(), dialHost, hostHeader, nil
}

func resolveProbeChainClientTLSServerName(layer string, dialHost string, hostHeader string) string {
	cleanDialHost := strings.TrimSpace(strings.Trim(dialHost, "[]"))
	cleanHostHeader := strings.TrimSpace(strings.Trim(hostHeader, "[]"))

	if normalizeChainLinkLayerValue(layer) == "http" {
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

func resolveProbeChainDialIPHost(rawHost string) (dialHost string, hostHeader string, err error) {
	return resolveProbeChainDialIPHostWithCache(rawHost, false)
}

func resolveProbeChainDialIPHostFresh(rawHost string) (dialHost string, hostHeader string, err error) {
	return resolveProbeChainDialIPHostWithCache(rawHost, true)
}

func resolveProbeChainDialIPHostWithCache(rawHost string, forceRefresh bool) (dialHost string, hostHeader string, err error) {
	host := strings.TrimSpace(strings.Trim(rawHost, "[]"))
	if host == "" {
		return "", "", fmt.Errorf("empty relay host")
	}
	if parsed := net.ParseIP(host); parsed != nil {
		return parsed.String(), host, nil
	}
	if !forceRefresh {
		if cachedIP, ok := getProbeDNSCachedIP(host); ok {
			return cachedIP, host, nil
		}
	}

	startedAt := time.Now()
	log.Printf("[manager/mux] resolve relay host begin: host=%s force_refresh=%v", host, forceRefresh)

	resolver := &networkAssistantService{}
	upstreamStartedAt := time.Now()
	log.Printf("[manager/mux] resolve relay host via upstream begin: host=%s", host)
	if addrs, _, upstreamErr := resolver.queryRuleDomainViaSystemDNS(host, 1); upstreamErr == nil && len(addrs) > 0 {
		log.Printf("[manager/mux] resolve relay host via upstream success: host=%s addrs=%v elapsed=%s", host, addrs, time.Since(upstreamStartedAt))
		for _, addr := range addrs {
			if parsed := net.ParseIP(strings.TrimSpace(addr)); parsed != nil {
				resolvedIP := parsed.String()
				_ = setProbeDNSCachedIP(host, resolvedIP)
				log.Printf("[manager/mux] resolve relay host done: host=%s ip=%s source=upstream elapsed=%s", host, resolvedIP, time.Since(startedAt))
				return resolvedIP, host, nil
			}
		}
		log.Printf("[manager/mux] resolve relay host via upstream unusable addrs: host=%s addrs=%v elapsed=%s", host, addrs, time.Since(upstreamStartedAt))
	} else {
		log.Printf("[manager/mux] resolve relay host via upstream failed: host=%s elapsed=%s err=%v", host, time.Since(upstreamStartedAt), upstreamErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	systemStartedAt := time.Now()
	log.Printf("[manager/mux] resolve relay host via system begin: host=%s", host)
	ips, resolveErr := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if resolveErr != nil {
		log.Printf("[manager/mux] resolve relay host via system failed: host=%s elapsed=%s err=%v", host, time.Since(systemStartedAt), resolveErr)
		return "", "", fmt.Errorf("resolve relay host failed: %w", resolveErr)
	}
	ip := selectProbeChainPreferredDialIP(ips)
	if ip == nil {
		log.Printf("[manager/mux] resolve relay host via system empty: host=%s ips=%v elapsed=%s", host, ips, time.Since(systemStartedAt))
		return "", "", fmt.Errorf("resolve relay host failed: no ip")
	}
	resolvedIP := ip.String()
	_ = setProbeDNSCachedIP(host, resolvedIP)
	log.Printf("[manager/mux] resolve relay host done: host=%s ip=%s source=system elapsed=%s", host, resolvedIP, time.Since(startedAt))
	return resolvedIP, host, nil
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

func applyProbeChainHMACAuthHeaders(headers http.Header, chainID string, secret string) error {
	cleanChainID := strings.TrimSpace(chainID)
	cleanSecret := strings.TrimSpace(secret)
	if cleanChainID == "" {
		return errors.New("chain_id is required")
	}
	if cleanSecret == "" {
		return errors.New("chain secret is required")
	}
	nonce := randomHexToken(16)
	headers.Set("Authorization", "Bearer "+nonce)
	headers.Set(probeChainCodexAuthModeHeader, "secret_hmac")
	headers.Set(probeChainCodexMACHeader, buildProbeChainHMAC(cleanSecret, cleanChainID, nonce))
	return nil
}

func buildProbeChainHMAC(secret string, chainID string, nonce string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(strings.TrimSpace(chainID)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(nonce)))
	return hex.EncodeToString(mac.Sum(nil))
}

func randomHexToken(size int) string {
	if size <= 0 {
		size = 8
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
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
		if c.writer != nil {
			if err := c.writer.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		if c.reader != nil {
			if err := c.reader.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		if c.closeFn != nil {
			if err := c.closeFn(); err != nil && closeErr == nil {
				closeErr = err
			}
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

func (c *probeChainRelayNetConn) SetDeadline(_ time.Time) error {
	return nil
}

func (c *probeChainRelayNetConn) SetReadDeadline(_ time.Time) error {
	return nil
}

func (c *probeChainRelayNetConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

func (c *tunnelMuxClient) acceptLoop() {
	for {
		if c.isClosed() {
			return
		}
		stream, err := c.session.Accept()
		if err != nil {
			log.Printf(
				"[network-assistant] tunnel mux accept failed: node=%s mode_key=%s err=%v state={%s}",
				strings.TrimSpace(c.nodeID),
				strings.TrimSpace(c.modeKey),
				err,
				describeTunnelMuxClientState(c),
			)
			c.close()
			return
		}
		go c.handleIncomingStream(stream)
	}
}

func (c *tunnelMuxClient) handleIncomingStream(stream net.Conn) {
	defer stream.Close()
	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	var msg tunnelInboundMessage
	if err := json.NewDecoder(stream).Decode(&msg); err != nil {
		return
	}
	typeName := strings.ToLower(strings.TrimSpace(msg.Type))
	if typeName != "controller_log" {
		return
	}
	if c.onControllerLog == nil {
		return
	}
	message := strings.TrimSpace(msg.Message)
	if message == "" {
		return
	}
	category := strings.TrimSpace(msg.Category)
	if category == "" {
		category = defaultLogCategory
	}
	c.onControllerLog(category, message)
}


func (c *tunnelMuxClient) isClosed() bool {
	if c.closed.Load() {
		return true
	}
	if c.session == nil {
		return true
	}
	return c.session.IsClosed()
}

func (c *tunnelMuxClient) close() {
	if c.closed.Swap(true) {
		return
	}


	if c.session != nil {
		_ = c.session.Close()
	}
	if c.wsConn != nil {
		_ = c.wsConn.Close()
	}

	c.mu.Lock()
	streams := make([]*tunnelMuxStream, 0, len(c.streams))
	for _, st := range c.streams {
		streams = append(streams, st)
	}
	c.streams = map[string]*tunnelMuxStream{}
	c.mu.Unlock()

	for _, st := range streams {
		st.closeLocal(io.EOF)
	}
}

func (c *tunnelMuxClient) keepAliveLoop() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if c.isClosed() {
			return
		}
		if _, err := c.session.Ping(); err != nil {
			failures := c.keepAliveFailures.Add(1)
			connected, activeStreams, lastRecvAt, lastPongAt := c.snapshot()
			log.Printf(
				"[network-assistant] tunnel mux keepalive ping failed: node=%s mode_key=%s failures=%d threshold=%d connected=%v active_streams=%d last_recv=%s last_pong=%s err=%v",
				strings.TrimSpace(c.nodeID),
				strings.TrimSpace(c.modeKey),
				failures,
				muxKeepAliveFailThreshold,
				connected,
				activeStreams,
				strings.TrimSpace(lastRecvAt),
				strings.TrimSpace(lastPongAt),
				err,
			)
			if failures >= muxKeepAliveFailThreshold {
				log.Printf(
					"[network-assistant] tunnel mux keepalive threshold reached: node=%s mode_key=%s failures=%d threshold=%d action=close",
					strings.TrimSpace(c.nodeID),
					strings.TrimSpace(c.modeKey),
					failures,
					muxKeepAliveFailThreshold,
				)
				c.close()
				return
			}
			continue
		}
		c.keepAliveFailures.Store(0)
		c.lastPong.Store(time.Now().Unix())
	}
}

// ping 通过 yamux 内置 Ping 测量到对端的往返延迟，不开新 stream，不影响业务流量。
func (c *tunnelMuxClient) ping() (time.Duration, error) {
	if c.isClosed() {
		return 0, errors.New("tunnel mux connection closed")
	}
	return c.session.Ping()
}

func (c *tunnelMuxClient) openStream(network, address string) (*tunnelMuxStream, error) {
	if c.isClosed() {
		return nil, errors.New("tunnel mux connection closed")
	}

	streamConn, err := c.session.Open()
	if err != nil {
		log.Printf(
			"[network-assistant] tunnel mux session open failed: node=%s mode_key=%s network=%s address=%s err=%v state={%s}",
			strings.TrimSpace(c.nodeID),
			strings.TrimSpace(c.modeKey),
			strings.TrimSpace(network),
			strings.TrimSpace(address),
			err,
			describeTunnelMuxClientState(c),
		)
		return nil, err
	}

	req := tunnelOpenRequest{Type: "open", Network: strings.TrimSpace(network), Address: strings.TrimSpace(address)}
	_ = streamConn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(streamConn).Encode(req); err != nil {
		_ = streamConn.Close()
		log.Printf(
			"[network-assistant] tunnel mux stream request encode failed: node=%s mode_key=%s network=%s address=%s err=%v state={%s}",
			strings.TrimSpace(c.nodeID),
			strings.TrimSpace(c.modeKey),
			strings.TrimSpace(network),
			strings.TrimSpace(address),
			err,
			describeTunnelMuxClientState(c),
		)
		return nil, err
	}
	_ = streamConn.SetWriteDeadline(time.Time{})

	_ = streamConn.SetReadDeadline(time.Now().Add(tunnelStreamOpenTimeout))
	var resp tunnelOpenResponse
	if err := json.NewDecoder(streamConn).Decode(&resp); err != nil {
		_ = streamConn.Close()
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			log.Printf(
				"[network-assistant] tunnel mux stream open response timeout: node=%s mode_key=%s network=%s address=%s timeout=%s state={%s}",
				strings.TrimSpace(c.nodeID),
				strings.TrimSpace(c.modeKey),
				strings.TrimSpace(network),
				strings.TrimSpace(address),
				tunnelStreamOpenTimeout,
				describeTunnelMuxClientState(c),
			)
			return nil, errTunnelStreamOpenTimeout
		}
		log.Printf(
			"[network-assistant] tunnel mux stream open response decode failed: node=%s mode_key=%s network=%s address=%s err=%v state={%s}",
			strings.TrimSpace(c.nodeID),
			strings.TrimSpace(c.modeKey),
			strings.TrimSpace(network),
			strings.TrimSpace(address),
			err,
			describeTunnelMuxClientState(c),
		)
		return nil, err
	}
	_ = streamConn.SetReadDeadline(time.Time{})

	if !resp.OK {
		_ = streamConn.Close()
		return nil, &tunnelOpenRemoteError{message: strings.TrimSpace(resp.Error)}
	}

	streamID := fmt.Sprintf("s%d", atomic.AddUint64(&c.seq, 1))
	st := &tunnelMuxStream{
		client:  c,
		id:      streamID,
		network: strings.ToLower(strings.TrimSpace(network)),
		conn:    streamConn,
		readCh:  make(chan []byte, 64),
		errCh:   make(chan error, 4),
	}

	c.mu.Lock()
	c.streams[streamID] = st
	c.mu.Unlock()

	go st.readLoop()
	return st, nil
}

func (s *tunnelMuxStream) readLoop() {
	if s == nil {
		return
	}
	if strings.EqualFold(s.network, "udp") {
		s.readUDPPackets()
		return
	}

	buf := make([]byte, 32*1024)
	for {
		n, err := s.conn.Read(buf)
		if n > 0 {
			s.client.lastRecv.Store(time.Now().Unix())
			payload := append([]byte(nil), buf[:n]...)
			select {
			case s.readCh <- payload:
			default:
				s.closeLocal(errors.New("tunnel stream read buffer full"))
				return
			}
		}
		if err != nil {
			s.closeLocal(err)
			return
		}
	}
}

func (s *tunnelMuxStream) readUDPPackets() {
	for {
		payload, err := readFramedPacket(s.conn)
		if len(payload) > 0 {
			s.client.lastRecv.Store(time.Now().Unix())
			select {
			case s.readCh <- payload:
			default:
				s.closeLocal(errors.New("udp tunnel stream read buffer full"))
				return
			}
		}
		if err != nil {
			s.closeLocal(err)
			return
		}
	}
}

func (s *tunnelMuxStream) write(payload []byte) error {
	if s == nil || s.closed.Load() {
		return io.EOF
	}
	if strings.EqualFold(s.network, "udp") {
		return writeFramedPacket(s.conn, payload)
	}
	return writeAll(s.conn, payload)
}

func (s *tunnelMuxStream) close() {
	if s == nil {
		return
	}
	s.closeLocal(io.EOF)
}

func (s *tunnelMuxStream) closeLocal(err error) {
	if s == nil {
		return
	}
	if s.closed.Swap(true) {
		return
	}
	_ = s.conn.Close()

	if s.client != nil {
		s.client.mu.Lock()
		delete(s.client.streams, s.id)
		s.client.mu.Unlock()
	}

	if err == nil {
		err = io.EOF
	}
	select {
	case s.errCh <- err:
	default:
	}
}

func (s *networkAssistantService) startMuxAutoMaintainLoop() {
	s.mu.Lock()
	if s.muxMaintainerStop != nil {
		s.mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	s.muxMaintainerStop = stopCh
	s.muxMaintainerDone = doneCh
	s.mu.Unlock()

	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(muxAutoMaintainInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				s.maintainSelectedTunnelMuxClients()
			}
		}
	}()
}

func (s *networkAssistantService) stopMuxAutoMaintainLoop() {
	s.mu.Lock()
	stopCh := s.muxMaintainerStop
	doneCh := s.muxMaintainerDone
	s.muxMaintainerStop = nil
	s.muxMaintainerDone = nil
	s.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		<-doneCh
	}
}

func (s *networkAssistantService) triggerMuxAutoMaintainNow() {
	go s.maintainSelectedTunnelMuxClients()
}

func collectAutoMaintainPolicyTunnelNodeIDs(routing tunnelRuleRouting, availableNodes []string, selectedChainID string) []string {
	defaultChainID := strings.TrimSpace(selectedChainID)
	if defaultChainID == "" {
		defaultChainID = defaultNodeID
	}
	tunnelOptions := buildRuleTunnelOptions(availableNodes, defaultChainID)
	groups := extractRuleGroupsFromRuleSet(routing.RuleSet)
	groups = append(groups, ruleFallbackGroupKey)

	chainIDs := make([]string, 0, len(groups))
	for _, group := range groups {
		policy, err := readRulePolicyForGroup(routing, group, defaultChainID, tunnelOptions)
		if err != nil || !strings.EqualFold(strings.TrimSpace(policy.Action), rulePolicyActionTunnel) {
			continue
		}
		chainID := strings.TrimSpace(policy.TunnelNodeID)
		if chainID == "" {
			chainID = defaultChainID
		}
		if chainID == "" || strings.EqualFold(chainID, defaultNodeID) || containsNodeID(chainIDs, chainID) {
			continue
		}
		chainIDs = append(chainIDs, chainID)
	}
	return chainIDs
}

func (s *networkAssistantService) collectAutoMaintainTunnelNodeIDs() []string {
	s.mu.RLock()
	selectedChainID := strings.TrimSpace(s.nodeID)
	availableNodes := append([]string(nil), s.availableNodes...)
	routing := s.ruleRouting
	s.mu.RUnlock()

	if selectedChainID == "" {
		selectedChainID = defaultNodeID
	}
	targetChainIDs := collectAutoMaintainPolicyTunnelNodeIDs(routing, availableNodes, selectedChainID)
	if len(targetChainIDs) == 0 {
		return nil
	}
	return targetChainIDs
}

func (s *networkAssistantService) maintainSelectedTunnelMuxClients() {
	s.mu.Lock()
	if s.muxMaintaining {
		s.mu.Unlock()
		return
	}
	s.muxMaintaining = true
	ensureRuleRoutingRuntimeMaps(&s.ruleRouting)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.muxMaintaining = false
		s.mu.Unlock()
	}()

	s.mu.RLock()
	selectedChainID := strings.TrimSpace(s.nodeID)
	availableNodes := append([]string(nil), s.availableNodes...)
	routing := s.ruleRouting
	chainTargets := copyProbeChainTargets(s.chainTargets)
	s.mu.RUnlock()
	if selectedChainID == "" {
		selectedChainID = defaultNodeID
	}

	tunnelOptions := buildRuleTunnelOptions(availableNodes, selectedChainID)
	groups := extractRuleGroupsFromRuleSet(routing.RuleSet)
	groups = append(groups, ruleFallbackGroupKey)
	now := time.Now()
	groupState := make(map[string]*ruleGroupRuntimeState, len(groups))
	activeClients := make(map[*tunnelMuxClient]struct{})

	for _, group := range groups {
		groupName := strings.TrimSpace(group)
		groupKey := strings.ToLower(groupName)
		if groupKey == "" {
			continue
		}

		state := &ruleGroupRuntimeState{ResolvedGroup: groupName}
		if existing := cloneRuleGroupRuntimeState(routing.GroupState[groupKey]); existing != nil {
			state = existing
			state.ResolvedGroup = groupName
			if state.Client != nil && state.Client.isClosed() {
				state.Client = nil
			}
		}
		item := NetworkAssistantGroupKeepaliveItem{Group: groupName}

		policy, err := readRulePolicyForGroup(routing, groupName, selectedChainID, tunnelOptions)
		if err != nil {
			state.Client = nil
			state.PolicyAction = ""
			state.PolicyNodeID = ""
			state.RetryAt = time.Time{}
			state.FailureCount = 0
			state.LastError = err.Error()
			item.Status = "规则错误"
			state.Snapshot = item
			groupState[groupKey] = state
			continue
		}

		state.PolicyAction = strings.TrimSpace(policy.Action)
		item.Action = state.PolicyAction
		switch policy.Action {
		case rulePolicyActionDirect:
			state.Client = nil
			state.PolicyNodeID = ""
			state.RetryAt = time.Time{}
			state.FailureCount = 0
			state.LastError = ""
			item.Status = "直连（无需隧道保活）"
		case rulePolicyActionReject:
			state.Client = nil
			state.PolicyNodeID = ""
			state.RetryAt = time.Time{}
			state.FailureCount = 0
			state.LastError = ""
			item.Status = "拒绝（无链路）"
		case rulePolicyActionTunnel:
			nodeID := strings.TrimSpace(policy.TunnelNodeID)
			if nodeID == "" {
				nodeID = selectedChainID
			}
			if nodeID == "" {
				nodeID = defaultNodeID
			}
			state.PolicyNodeID = nodeID
			item.TunnelNodeID = nodeID
			item.TunnelLabel = resolveRuleTunnelOptionLabel(nodeID, chainTargets)
			if strings.EqualFold(nodeID, defaultNodeID) {
				state.Client = nil
				state.RetryAt = time.Time{}
				state.FailureCount = 0
				state.LastError = ""
				item.Status = "未建立"
				state.Snapshot = item
				groupState[groupKey] = state
				continue
			}
			if state.Client != nil && strings.TrimSpace(state.Client.nodeID) != strings.TrimSpace(nodeID) {
				state.Client = nil
			}
			if state.Client != nil && state.Client.isClosed() {
				state.Client = nil
			}
			if state.Client != nil {
				item.Connected, item.ActiveStreams, item.LastRecv, item.LastPong = state.Client.snapshot()
				if item.Connected {
					item.Status = "在线"
				} else {
					item.Status = "离线"
				}
				state.RetryAt = time.Time{}
				state.FailureCount = 0
				state.LastError = ""
				state.Snapshot = item
				groupState[groupKey] = state
				activeClients[state.Client] = struct{}{}
				continue
			}
			if !state.RetryAt.IsZero() && now.Before(state.RetryAt) {
				item.Status = "重试中"
				state.Snapshot = item
				groupState[groupKey] = state
				continue
			}

			client, ensureErr := s.ensureTunnelMuxClientForGroup(groupName)
			if ensureErr != nil {
				attempt := state.FailureCount + 1
				state.Client = nil
				state.FailureCount = attempt
				state.RetryAt = now.Add(calcMuxAutoMaintainBackoff(attempt))
				state.LastError = ensureErr.Error()
				item.Status = "离线"
				state.Snapshot = item
				groupState[groupKey] = state
				continue
			}

			state.Client = client
			state.FailureCount = 0
			state.RetryAt = time.Time{}
			state.LastError = ""
			if client != nil {
				item.Connected, item.ActiveStreams, item.LastRecv, item.LastPong = client.snapshot()
				if item.Connected {
					item.Status = "在线"
				} else {
					item.Status = "离线"
				}
				activeClients[client] = struct{}{}
			} else {
				item.Status = "未建立"
			}
		default:
			state.Client = nil
			state.PolicyNodeID = ""
			state.RetryAt = time.Time{}
			state.FailureCount = 0
			state.LastError = ""
			item.Status = "未知"
		}

		state.Snapshot = item
		groupState[groupKey] = state
	}

	groupRuntime := indexGroupKeepaliveSnapshotFromState(groupState)
	staleClients := make([]*tunnelMuxClient, 0, len(routing.GroupState))
	for _, state := range routing.GroupState {
		if state == nil || state.Client == nil {
			continue
		}
		if _, ok := activeClients[state.Client]; ok {
			continue
		}
		staleClients = append(staleClients, state.Client)
	}

	s.mu.Lock()
	ensureRuleRoutingRuntimeMaps(&s.ruleRouting)
	s.ruleRouting.GroupState = groupState
	s.ruleRouting.GroupRuntime = copyGroupKeepaliveSnapshot(groupRuntime)
	s.mu.Unlock()

	closed := make(map[*tunnelMuxClient]struct{}, len(staleClients))
	for _, client := range staleClients {
		if client == nil {
			continue
		}
		if _, ok := closed[client]; ok {
			continue
		}
		closed[client] = struct{}{}
		client.close()
	}
}

func calcMuxAutoMaintainBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return muxAutoMaintainMinRetry
	}
	backoff := muxAutoMaintainMinRetry
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff >= muxAutoMaintainMaxRetry {
			return muxAutoMaintainMaxRetry
		}
	}
	if backoff > muxAutoMaintainMaxRetry {
		return muxAutoMaintainMaxRetry
	}
	return backoff
}

func cloneRuleGroupRuntimeState(src *ruleGroupRuntimeState) *ruleGroupRuntimeState {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}

func (s *networkAssistantService) getRuleGroupRuntimeState(group string) (*ruleGroupRuntimeState, bool) {
	groupKey := strings.ToLower(strings.TrimSpace(group))
	if groupKey == "" {
		return nil, false
	}
	if s == nil {
		return nil, false
	}
	
	s.mu.RLock()
	state, ok := s.ruleRouting.GroupState[groupKey]
	s.mu.RUnlock()
	if !ok || state == nil {
		return nil, false
	}
	return cloneRuleGroupRuntimeState(state), true
}

func (s *networkAssistantService) getExistingTunnelMuxClientForGroup(group string) (*tunnelMuxClient, bool) {
	state, ok := s.getRuleGroupRuntimeState(group)
	if !ok || state == nil {
		return nil, false
	}
	if state.Client == nil || state.Client.isClosed() {
		return nil, false
	}
	return state.Client, true
}

func formatMuxRuntimeTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func describeTunnelMuxClientState(client *tunnelMuxClient) string {
	if client == nil {
		return "client=nil"
	}
	connected, activeStreams, lastRecv, lastPong := client.snapshot()
	sessionClosed := true
	if client.session != nil {
		sessionClosed = client.session.IsClosed()
	}
	return fmt.Sprintf(
		"id=%d node=%s mode=%s connected=%v closed=%v session_closed=%v keepalive_failures=%d active_streams=%d last_recv=%s last_pong=%s",
		client.id,
		strings.TrimSpace(client.nodeID),
		strings.TrimSpace(client.modeKey),
		connected,
		client.closed.Load(),
		sessionClosed,
		client.keepAliveFailures.Load(),
		activeStreams,
		strings.TrimSpace(lastRecv),
		strings.TrimSpace(lastPong),
	)
}

func (s *networkAssistantService) describeRuleGroupRuntimeState(group string) string {
	state, ok := s.getRuleGroupRuntimeState(group)
	if !ok || state == nil {
		return "state=missing"
	}
	return fmt.Sprintf(
		"state=present resolved_group=%s action=%s policy_node=%s failure_count=%d retry_at=%s last_error=%s client={%s}",
		strings.TrimSpace(state.ResolvedGroup),
		strings.TrimSpace(state.PolicyAction),
		strings.TrimSpace(state.PolicyNodeID),
		state.FailureCount,
		formatMuxRuntimeTime(state.RetryAt),
		strings.TrimSpace(state.LastError),
		describeTunnelMuxClientState(state.Client),
	)
}

func findReusableTunnelMuxClientForTarget(states map[string]*ruleGroupRuntimeState, excludeGroupKey, nodeID, modeKey string) (*tunnelMuxClient, bool) {
	targetGroupKey := strings.ToLower(strings.TrimSpace(excludeGroupKey))
	targetNodeID := strings.TrimSpace(nodeID)
	targetModeKey := strings.TrimSpace(modeKey)
	if targetNodeID == "" || targetModeKey == "" {
		return nil, false
	}
	for groupKey, state := range states {
		if strings.EqualFold(strings.TrimSpace(groupKey), targetGroupKey) {
			continue
		}
		if state == nil || state.Client == nil || state.Client.isClosed() {
			continue
		}
		if strings.TrimSpace(state.Client.nodeID) != targetNodeID {
			continue
		}
		if strings.TrimSpace(state.Client.modeKey) != targetModeKey {
			continue
		}
		return state.Client, true
	}
	return nil, false
}

func hasOtherGroupUsingTunnelMuxClient(states map[string]*ruleGroupRuntimeState, excludeGroupKey string, client *tunnelMuxClient) bool {
	if client == nil {
		return false
	}
	targetGroupKey := strings.ToLower(strings.TrimSpace(excludeGroupKey))
	for groupKey, state := range states {
		if strings.EqualFold(strings.TrimSpace(groupKey), targetGroupKey) {
			continue
		}
		if state == nil || state.Client == nil {
			continue
		}
		if state.Client == client {
			return true
		}
	}
	return false
}

func (s *networkAssistantService) ensureTunnelMuxClientForGroup(group string) (*tunnelMuxClient, error) {
	groupName := strings.TrimSpace(group)
	groupKey := strings.ToLower(groupName)
	if groupKey == "" {
		return nil, errors.New("group is required")
	}

	s.mu.RLock()
	selectedNodeID := strings.TrimSpace(s.nodeID)
	availableNodes := append([]string(nil), s.availableNodes...)
	routing := s.ruleRouting
	chainTargets := copyProbeChainTargets(s.chainTargets)
	existingState := cloneRuleGroupRuntimeState(routing.GroupState[groupKey])
	s.mu.RUnlock()
	if selectedNodeID == "" {
		selectedNodeID = defaultNodeID
	}
	policy, err := readRulePolicyForGroup(routing, groupName, selectedNodeID, buildRuleTunnelOptions(availableNodes, selectedNodeID))
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(policy.Action), rulePolicyActionTunnel) {
		return nil, fmt.Errorf("group does not require tunnel mux: %s", groupName)
	}

	nodeID := strings.TrimSpace(policy.TunnelNodeID)
	if nodeID == "" {
		nodeID = selectedNodeID
	}
	if nodeID == "" {
		nodeID = defaultNodeID
	}
	chainTarget, hasChainTarget, resolvedNodeID, resolveErr := s.resolveTunnelMuxChainTargetForNode(nodeID)
	if resolveErr != nil {
		return nil, resolveErr
	}
	if resolvedNodeID != "" {
		nodeID = resolvedNodeID
	}
	if !hasChainTarget {
		return nil, fmt.Errorf("selected chain does not support chain mux keepalive: %s", nodeID)
	}
	modeKey := fmt.Sprintf(
		"chain:%s@%s:%d/%s",
		strings.TrimSpace(chainTarget.ChainID),
		strings.TrimSpace(chainTarget.EntryHost),
		chainTarget.EntryPort,
		strings.TrimSpace(chainTarget.LinkLayer),
	)
	if existingState != nil && existingState.Client != nil && !existingState.Client.isClosed() {
		if strings.TrimSpace(existingState.Client.nodeID) == strings.TrimSpace(nodeID) &&
			strings.TrimSpace(existingState.Client.modeKey) == strings.TrimSpace(modeKey) {
			log.Printf(
				"[network-assistant] mux client selected: group=%s source=group-state client_state={%s}",
				groupName,
				describeTunnelMuxClientState(existingState.Client),
			)
			return existingState.Client, nil
		}
	}

	client, _ := findReusableTunnelMuxClientForTarget(routing.GroupState, groupKey, nodeID, modeKey)
	created := false
	startedAt := time.Now()
	if client == nil {
		client, err = s.newTunnelMuxClientLocked(nodeID, chainTarget)
		if err != nil {
			return nil, err
		}
		created = true
	}
	if client != nil {
		source := "shared-existing"
		if created {
			source = "new"
		}
		log.Printf(
			"[network-assistant] mux client selected: group=%s source=%s client_state={%s}",
			groupName,
			source,
			describeTunnelMuxClientState(client),
		)
	}

	item := NetworkAssistantGroupKeepaliveItem{
		Group:        groupName,
		Action:       rulePolicyActionTunnel,
		TunnelNodeID: nodeID,
		TunnelLabel:  resolveRuleTunnelOptionLabel(nodeID, chainTargets),
	}
	if client != nil {
		item.Connected, item.ActiveStreams, item.LastRecv, item.LastPong = client.snapshot()
		if item.Connected {
			item.Status = "在线"
		} else {
			item.Status = "离线"
		}
	} else {
		item.Status = "未建立"
	}

	var staleClient *tunnelMuxClient
	var reconnects int64
	s.mu.Lock()
	ensureRuleRoutingRuntimeMaps(&s.ruleRouting)
	state := s.ruleRouting.GroupState[groupKey]
	if state == nil {
		state = &ruleGroupRuntimeState{}
		s.ruleRouting.GroupState[groupKey] = state
	}
	if state.Client != nil && state.Client != client {
		staleClient = state.Client
	}
	state.ResolvedGroup = groupName
	state.PolicyAction = rulePolicyActionTunnel
	state.PolicyNodeID = nodeID
	state.Client = client
	state.RetryAt = time.Time{}
	state.FailureCount = 0
	state.LastError = ""
	state.Snapshot = item
	s.ruleRouting.GroupRuntime[groupKey] = item
	if created {
		s.muxReconnects++
	}
	reconnects = s.muxReconnects
	s.mu.Unlock()

	if staleClient != nil && staleClient != client {
		s.mu.RLock()
		stillUsed := hasOtherGroupUsingTunnelMuxClient(s.ruleRouting.GroupState, groupKey, staleClient)
		s.mu.RUnlock()
		if !stillUsed {
			staleClient.close()
		}
	}

	_ = reconnects
	_ = startedAt
	return client, nil
}

func (s *networkAssistantService) tryPingExistingGroupMuxForNode(nodeID string) (time.Duration, bool) {
	targetNodeID := strings.TrimSpace(nodeID)
	if targetNodeID == "" || s == nil {
		return 0, false
	}

	s.mu.RLock()
	states := make([]*ruleGroupRuntimeState, 0, len(s.ruleRouting.GroupState))
	for _, state := range s.ruleRouting.GroupState {
		if state == nil {
			continue
		}
		states = append(states, cloneRuleGroupRuntimeState(state))
	}
	s.mu.RUnlock()

	seen := make(map[*tunnelMuxClient]struct{}, len(states))
	for _, state := range states {
		if state == nil || state.Client == nil || state.Client.isClosed() {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(state.PolicyNodeID), targetNodeID) &&
			!strings.EqualFold(strings.TrimSpace(state.Client.nodeID), targetNodeID) {
			continue
		}
		if _, ok := seen[state.Client]; ok {
			continue
		}
		seen[state.Client] = struct{}{}
		rtt, err := state.Client.ping()
		if err == nil {
			return rtt, true
		}
	}
	return 0, false
}

func (s *networkAssistantService) resolveTunnelMuxChainTargetForNode(nodeID string) (probeChainEndpoint, bool, string, error) {
	targetNodeID := strings.TrimSpace(nodeID)
	if targetNodeID == "" {
		return probeChainEndpoint{}, false, "", nil
	}

	targets := s.getChainTargetsSnapshot()
	endpoint, hasChainTarget, resolvedNodeID, resolveErr := resolveProbeChainTargetFromSnapshot(targetNodeID, targets)
	if resolveErr != nil {
		s.logf("resolve tunnel mux chain target failed: requested=%s cached_targets=%d err=%v", targetNodeID, len(targets), resolveErr)
		return probeChainEndpoint{}, false, resolvedNodeID, resolveErr
	}
	return endpoint, hasChainTarget, resolvedNodeID, nil
}

func resolveProbeChainTargetFromSnapshot(targetNodeID string, targets map[string]probeChainEndpoint) (probeChainEndpoint, bool, string, error) {
	cleanTargetID := normalizeProbeChainPingTargetID(targetNodeID)
	if cleanTargetID == "" {
		return probeChainEndpoint{}, false, strings.TrimSpace(targetNodeID), nil
	}

	candidateChainIDs, explicitChainTarget := buildProbeChainPingCandidateChainIDs(cleanTargetID)
	if !explicitChainTarget {
		return probeChainEndpoint{}, false, cleanTargetID, nil
	}

	for _, candidateID := range candidateChainIDs {
		if key := buildChainTargetNodeID(candidateID); key != "" {
			if item, ok := targets[key]; ok {
				resolvedNodeID := strings.TrimSpace(item.TargetID)
				if resolvedNodeID == "" {
					resolvedNodeID = key
				}
				return item, true, resolvedNodeID, nil
			}
		}
	}

	candidateSet := make(map[string]struct{}, len(candidateChainIDs))
	for _, candidateID := range candidateChainIDs {
		clean := strings.ToLower(strings.TrimSpace(candidateID))
		if clean == "" {
			continue
		}
		candidateSet[clean] = struct{}{}
	}
	for key, item := range targets {
		if _, ok := candidateSet[strings.ToLower(strings.TrimSpace(item.ChainID))]; !ok {
			continue
		}
		resolvedNodeID := strings.TrimSpace(item.TargetID)
		if resolvedNodeID == "" {
			resolvedNodeID = strings.TrimSpace(key)
		}
		if resolvedNodeID == "" {
			resolvedNodeID = buildChainTargetNodeID(item.ChainID)
		}
		return item, true, resolvedNodeID, nil
	}

	return probeChainEndpoint{}, false, cleanTargetID, fmt.Errorf("chain target config not found in local cache: node=%s", cleanTargetID)
}

func (s *networkAssistantService) newTunnelMuxClientLocked(nodeID string, chainTarget probeChainEndpoint) (*tunnelMuxClient, error) {
	startedAt := time.Now()

	client, err := newTunnelMuxClientViaProbeChain("", "", nodeID, chainTarget, func(category, message string) {
		s.logController(category, message)
	})
	if err != nil {
		s.logf(
			"create tunnel mux client failed, node=%s chain=%s entry=%s:%d layer=%s elapsed=%s err=%v",
			nodeID,
			strings.TrimSpace(chainTarget.ChainID),
			strings.TrimSpace(chainTarget.EntryHost),
			chainTarget.EntryPort,
			strings.TrimSpace(chainTarget.LinkLayer),
			time.Since(startedAt),
			err,
		)
		return nil, err
	}
	_ = startedAt
	return client, nil
}

func (s *networkAssistantService) openTunnelStreamForGroup(network, targetAddr, group string) (*tunnelMuxStream, error) {
	groupName := strings.TrimSpace(group)
	cleanNetwork := strings.ToLower(strings.TrimSpace(network))
	cleanTarget := strings.ToLower(strings.TrimSpace(targetAddr))
	startedAt := time.Now()

	client, ok := s.getExistingTunnelMuxClientForGroup(groupName)
	if !ok {
		err := errors.New("no available mux client")
		s.logfRateLimited(
			fmt.Sprintf("mux:stream-open:no-group-client:%s|%s|group:%s", cleanNetwork, cleanTarget, strings.ToLower(groupName)),
			5*time.Second,
			"open tunnel stream skipped: no available mux client (maintainer-owned): network=%s target=%s group=%s err=%v group_state={%s}",
			network,
			targetAddr,
			groupName,
			err,
			s.describeRuleGroupRuntimeState(groupName),
		)
		return nil, err
	}

	stream, err := client.openStream(network, targetAddr)
	if err == nil {
		return stream, nil
	}

	reason := "open_failed"
	switch {
	case errors.Is(err, errTunnelStreamOpenTimeout):
		reason = "open_timeout"
	case isTunnelOpenRemoteError(err):
		reason = "remote_rejected"
	case client == nil || client.isClosed():
		reason = "client_closed"
	default:
		errText := strings.ToLower(strings.TrimSpace(err.Error()))
		switch {
		case strings.Contains(errText, "session shutdown"):
			reason = "session_shutdown"
		case strings.Contains(errText, "closed pipe"):
			reason = "closed_pipe"
		}
	}

	if isTunnelOpenRemoteError(err) {
		s.logfRateLimited(
			fmt.Sprintf("mux:stream-open:group-remote-failed:%s|%s|group:%s", cleanNetwork, cleanTarget, strings.ToLower(groupName)),
			5*time.Second,
			"open tunnel stream remote rejected: network=%s target=%s group=%s reason=%s elapsed=%s err=%v client_state={%s} group_state={%s}",
			network,
			targetAddr,
			groupName,
			reason,
			time.Since(startedAt),
			err,
			describeTunnelMuxClientState(client),
			s.describeRuleGroupRuntimeState(groupName),
		)
		return nil, err
	}

	s.logfRateLimited(
		fmt.Sprintf("mux:stream-open:group-failed:%s|%s|group:%s", cleanNetwork, cleanTarget, strings.ToLower(groupName)),
		5*time.Second,
		"open tunnel stream failed with existing mux: network=%s target=%s group=%s reason=%s elapsed=%s err=%v client_state={%s} group_state={%s}",
		network,
		targetAddr,
		groupName,
		reason,
		time.Since(startedAt),
		err,
		describeTunnelMuxClientState(client),
		s.describeRuleGroupRuntimeState(groupName),
	)
	return nil, err
}


func writeAll(w io.Writer, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	written := 0
	for written < len(payload) {
		n, err := w.Write(payload[written:])
		if n > 0 {
			written += n
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func writeFramedPacket(w io.Writer, payload []byte) error {
	if len(payload) > 0xffff {
		return errors.New("udp payload too large")
	}
	header := [2]byte{}
	binary.BigEndian.PutUint16(header[:], uint16(len(payload)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, payload)
}

func readFramedPacket(r io.Reader) ([]byte, error) {
	header := [2]byte{}
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(header[:]))
	if length == 0 {
		return nil, nil
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

type webSocketNetConn struct {
	ws *websocket.Conn

	readMu  sync.Mutex
	writeMu sync.Mutex
	reader  io.Reader
}

func newWebSocketNetConn(ws *websocket.Conn) net.Conn {
	return &webSocketNetConn{ws: ws}
}

func (c *webSocketNetConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	for {
		if c.reader == nil {
			mt, reader, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if mt != websocket.BinaryMessage && mt != websocket.TextMessage {
				continue
			}
			c.reader = reader
		}

		n, err := c.reader.Read(p)
		if errors.Is(err, io.EOF) {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *webSocketNetConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	writer, err := c.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, writeErr := writer.Write(p)
	closeErr := writer.Close()
	if writeErr != nil {
		return n, writeErr
	}
	if closeErr != nil {
		return n, closeErr
	}
	return n, nil
}

func (c *webSocketNetConn) Close() error {
	return c.ws.Close()
}

func (c *webSocketNetConn) LocalAddr() net.Addr {
	return c.ws.UnderlyingConn().LocalAddr()
}

func (c *webSocketNetConn) RemoteAddr() net.Addr {
	return c.ws.UnderlyingConn().RemoteAddr()
}

func (c *webSocketNetConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}

func (c *webSocketNetConn) SetReadDeadline(t time.Time) error {
	return c.ws.SetReadDeadline(t)
}

func (c *webSocketNetConn) SetWriteDeadline(t time.Time) error {
	return c.ws.SetWriteDeadline(t)
}
