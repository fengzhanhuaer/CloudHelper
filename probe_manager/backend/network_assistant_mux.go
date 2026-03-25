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

	probeChainRelayAPIPath         = "/api/node/chain/relay"
	probeChainAuthPacketVersion    = "2025-03-22"
	probeChainTLSServerName        = "api.githubcopilot.com"
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
	baseURL string
	token   string
	nodeID  string
	modeKey string

	onControllerLog func(string, string)

	wsConn  *websocket.Conn
	session *yamux.Session

	mu      sync.Mutex
	streams map[string]*tunnelMuxStream
	seq     uint64
	closed  atomic.Bool

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
	wsConn, handshakeResp, err := websocket.DefaultDialer.Dial(tunnelURL, header)
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
		baseURL:         baseURL,
		token:           token,
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
		baseURL:         baseURL,
		token:           token,
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
		_ = bodyWriter.Close()
		_ = closeTransport()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		_ = bodyWriter.Close()
		_ = closeTransport()
		return nil, fmt.Errorf("probe relay failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	return &probeChainRelayHop{
		Writer: bodyWriter,
		Reader: response.Body,
		CloseFn: func() error {
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

func resolveProbeChainDialIPHost(rawHost string) (dialHost string, hostHeader string, err error) {
	host := strings.TrimSpace(strings.Trim(rawHost, "[]"))
	if host == "" {
		return "", "", fmt.Errorf("empty relay host")
	}
	if parsed := net.ParseIP(host); parsed != nil {
		return parsed.String(), host, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, resolveErr := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if resolveErr != nil {
		return "", "", fmt.Errorf("resolve relay host failed: %w", resolveErr)
	}
	ip := selectProbeChainPreferredDialIP(ips)
	if ip == nil {
		return "", "", fmt.Errorf("resolve relay host failed: no ip")
	}
	return ip.String(), host, nil
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

func (c *tunnelMuxClient) sameEndpoint(baseURL, token, nodeID, modeKey string) bool {
	return strings.TrimSpace(c.baseURL) == strings.TrimSpace(baseURL) &&
		strings.TrimSpace(c.token) == strings.TrimSpace(token) &&
		strings.TrimSpace(c.nodeID) == strings.TrimSpace(nodeID) &&
		strings.TrimSpace(c.modeKey) == strings.TrimSpace(modeKey)
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
			c.close()
			return
		}
		c.lastPong.Store(time.Now().Unix())
	}
}

func (c *tunnelMuxClient) openStream(network, address string) (*tunnelMuxStream, error) {
	if c.isClosed() {
		return nil, errors.New("tunnel mux connection closed")
	}

	streamConn, err := c.session.Open()
	if err != nil {
		return nil, err
	}

	req := tunnelOpenRequest{Type: "open", Network: strings.TrimSpace(network), Address: strings.TrimSpace(address)}
	_ = streamConn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(streamConn).Encode(req); err != nil {
		_ = streamConn.Close()
		return nil, err
	}
	_ = streamConn.SetWriteDeadline(time.Time{})

	_ = streamConn.SetReadDeadline(time.Now().Add(tunnelStreamOpenTimeout))
	var resp tunnelOpenResponse
	if err := json.NewDecoder(streamConn).Decode(&resp); err != nil {
		_ = streamConn.Close()
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return nil, errTunnelStreamOpenTimeout
		}
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

func (s *networkAssistantService) ensureTunnelMuxClient() (*tunnelMuxClient, error) {
	return s.ensureTunnelMuxClientForNode("")
}

func (s *networkAssistantService) ensureTunnelMuxClientForNode(nodeIDInput string) (*tunnelMuxClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	baseURL := strings.TrimSpace(s.controllerBaseURL)
	token := strings.TrimSpace(s.sessionToken)
	selectedNodeID := strings.TrimSpace(s.nodeID)
	if selectedNodeID == "" {
		selectedNodeID = defaultNodeID
	}
	targetNodeID := strings.TrimSpace(nodeIDInput)
	if targetNodeID == "" {
		targetNodeID = selectedNodeID
	}
	if targetNodeID == "" {
		targetNodeID = defaultNodeID
	}

	chainTarget, hasChainTarget := s.chainTargets[targetNodeID]

	// If this is a chain target but not yet in the cache, try refreshing from server once.
	if !hasChainTarget && strings.HasPrefix(targetNodeID, chainTargetNodePrefix) {
		baseURLForRefresh := strings.TrimSpace(s.controllerBaseURL)
		tokenForRefresh := strings.TrimSpace(s.sessionToken)
		if baseURLForRefresh != "" && tokenForRefresh != "" {
			s.mu.Unlock()
			s.logf("chain target not in cache, refreshing from server: node=%s", targetNodeID)
			_ = s.refreshAvailableNodes()
			s.mu.Lock()
			chainTarget, hasChainTarget = s.chainTargets[targetNodeID]
		}
	}

	// Never fall back to controller WebSocket for chain: nodes — it will always get 404.
	if !hasChainTarget && strings.HasPrefix(targetNodeID, chainTargetNodePrefix) {
		return nil, fmt.Errorf("chain target config not found on server: node=%s", targetNodeID)
	}

	modeKey := "ws"
	if hasChainTarget {
		modeKey = fmt.Sprintf(
			"chain:%s@%s:%d/%s",
			strings.TrimSpace(chainTarget.ChainID),
			strings.TrimSpace(chainTarget.EntryHost),
			chainTarget.EntryPort,
			strings.TrimSpace(chainTarget.LinkLayer),
		)
	}

	if baseURL == "" || token == "" {
		s.logf("tunnel mux connect skipped: missing controller url or session token")
		return nil, errors.New("missing controller url or session token")
	}

	isPrimary := strings.EqualFold(targetNodeID, selectedNodeID)
	if isPrimary {
		if s.tunnelMuxClient != nil && !s.tunnelMuxClient.isClosed() && s.tunnelMuxClient.sameEndpoint(baseURL, token, targetNodeID, modeKey) {
			return s.tunnelMuxClient, nil
		}
		if s.tunnelMuxClient != nil {
			s.logf("closing stale tunnel mux client")
			s.tunnelMuxClient.close()
			s.tunnelMuxClient = nil
		}
	} else {
		if s.ruleMuxClients == nil {
			s.ruleMuxClients = make(map[string]*tunnelMuxClient)
		}
		if existing := s.ruleMuxClients[targetNodeID]; existing != nil {
			if !existing.isClosed() && existing.sameEndpoint(baseURL, token, targetNodeID, modeKey) {
				return existing, nil
			}
			existing.close()
			delete(s.ruleMuxClients, targetNodeID)
		}
	}

	client, err := s.newTunnelMuxClientLocked(baseURL, token, targetNodeID, chainTarget, hasChainTarget)
	if err != nil {
		return nil, err
	}

	if isPrimary {
		s.tunnelMuxClient = client
	} else {
		s.ruleMuxClients[targetNodeID] = client
	}
	s.muxReconnects++

	if hasChainTarget {
		s.logf(
			"tunnel mux connected via chain, node=%s chain=%s entry_node=%s entry=%s:%d layer=%s reconnects=%d",
			targetNodeID,
			strings.TrimSpace(chainTarget.ChainID),
			strings.TrimSpace(chainTarget.EntryNode),
			strings.TrimSpace(chainTarget.EntryHost),
			chainTarget.EntryPort,
			strings.TrimSpace(chainTarget.LinkLayer),
			s.muxReconnects,
		)
	} else {
		s.logf("tunnel mux connected, node=%s reconnects=%d", targetNodeID, s.muxReconnects)
	}
	return client, nil
}

func (s *networkAssistantService) newTunnelMuxClientLocked(baseURL, token, nodeID string, chainTarget probeChainEndpoint, hasChainTarget bool) (*tunnelMuxClient, error) {
	var (
		client *tunnelMuxClient
		err    error
	)
	if hasChainTarget {
		client, err = newTunnelMuxClientViaProbeChain(baseURL, token, nodeID, chainTarget, func(category, message string) {
			s.logController(category, message)
		})
	} else {
		client, err = newTunnelMuxClient(baseURL, token, nodeID, func(category, message string) {
			s.logController(category, message)
		})
	}
	if err != nil {
		if hasChainTarget {
			s.logf(
				"create tunnel mux client failed, node=%s base=%s chain=%s entry=%s:%d layer=%s err=%v",
				nodeID,
				baseURL,
				strings.TrimSpace(chainTarget.ChainID),
				strings.TrimSpace(chainTarget.EntryHost),
				chainTarget.EntryPort,
				strings.TrimSpace(chainTarget.LinkLayer),
				err,
			)
		} else {
			s.logf("create tunnel mux client failed, node=%s base=%s err=%v", nodeID, baseURL, err)
		}
		return nil, err
	}
	return client, nil
}

func (s *networkAssistantService) openTunnelStream(network, targetAddr string) (*tunnelMuxStream, error) {
	return s.openTunnelStreamForNode(network, targetAddr, "")
}

func (s *networkAssistantService) openTunnelStreamForNode(network, targetAddr, nodeID string) (*tunnelMuxStream, error) {
	client, err := s.ensureTunnelMuxClientForNode(nodeID)
	if err != nil {
		return nil, err
	}
	stream, err := client.openStream(network, targetAddr)
	if err == nil {
		return stream, nil
	}
	if isTunnelOpenRemoteError(err) {
		return nil, err
	}
	s.logf("open tunnel stream failed, retrying: network=%s target=%s err=%v", network, targetAddr, err)

	client.close()
	client, err = s.ensureTunnelMuxClientForNode(nodeID)
	if err != nil {
		return nil, err
	}
	return client.openStream(network, targetAddr)
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
