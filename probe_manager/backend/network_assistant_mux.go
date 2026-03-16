package backend

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

const tunnelStreamOpenTimeout = 20 * time.Second

var errTunnelStreamOpenTimeout = errors.New("open stream timeout")

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

func (c *tunnelMuxClient) sameEndpoint(baseURL, token, nodeID string) bool {
	return strings.TrimSpace(c.baseURL) == strings.TrimSpace(baseURL) && strings.TrimSpace(c.token) == strings.TrimSpace(token) && strings.TrimSpace(c.nodeID) == strings.TrimSpace(nodeID)
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
	s.mu.Lock()
	defer s.mu.Unlock()

	baseURL := strings.TrimSpace(s.controllerBaseURL)
	token := strings.TrimSpace(s.sessionToken)
	nodeID := strings.TrimSpace(s.nodeID)
	if nodeID == "" {
		nodeID = defaultNodeID
	}

	if baseURL == "" || token == "" {
		s.logf("tunnel mux connect skipped: missing controller url or session token")
		return nil, errors.New("missing controller url or session token")
	}

	if s.tunnelMuxClient != nil && !s.tunnelMuxClient.isClosed() && s.tunnelMuxClient.sameEndpoint(baseURL, token, nodeID) {
		return s.tunnelMuxClient, nil
	}
	if s.tunnelMuxClient != nil {
		s.logf("closing stale tunnel mux client")
		s.tunnelMuxClient.close()
		s.tunnelMuxClient = nil
	}

	client, err := newTunnelMuxClient(baseURL, token, nodeID, func(category, message string) {
		s.logController(category, message)
	})
	if err != nil {
		s.logf("create tunnel mux client failed, node=%s base=%s err=%v", nodeID, baseURL, err)
		return nil, err
	}
	s.tunnelMuxClient = client
	s.muxReconnects++
	s.logf("tunnel mux connected, node=%s reconnects=%d", nodeID, s.muxReconnects)
	return client, nil
}

func (s *networkAssistantService) openTunnelStream(network, targetAddr string) (*tunnelMuxStream, error) {
	client, err := s.ensureTunnelMuxClient()
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
	client, err = s.ensureTunnelMuxClient()
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
