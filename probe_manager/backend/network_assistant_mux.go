package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type tunnelMuxFrame struct {
	Type     string `json:"type"`
	StreamID string `json:"stream_id,omitempty"`
	Network  string `json:"network,omitempty"`
	Address  string `json:"address,omitempty"`
	Category string `json:"category,omitempty"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type tunnelMuxStream struct {
	client *tunnelMuxClient
	id     string
	readCh chan []byte
	errCh  chan error
	openCh chan error
	closed atomic.Bool
}

type tunnelMuxClient struct {
	baseURL string
	token   string
	nodeID  string

	onControllerLog func(string, string)

	conn   *websocket.Conn
	writeM sync.Mutex

	mu       sync.Mutex
	streams  map[string]*tunnelMuxStream
	seq      uint64
	closed   atomic.Bool
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

	c := &tunnelMuxClient{
		baseURL:         baseURL,
		token:           token,
		nodeID:          nodeID,
		onControllerLog: onControllerLog,
		conn:            wsConn,
		streams:         make(map[string]*tunnelMuxStream),
	}
	now := time.Now().Unix()
	c.lastRecv.Store(now)
	c.lastPong.Store(now)
	go c.readLoop()
	go c.keepAliveLoop()
	return c, nil
}

func (c *tunnelMuxClient) sameEndpoint(baseURL, token, nodeID string) bool {
	return strings.TrimSpace(c.baseURL) == strings.TrimSpace(baseURL) && strings.TrimSpace(c.token) == strings.TrimSpace(token) && strings.TrimSpace(c.nodeID) == strings.TrimSpace(nodeID)
}

func (c *tunnelMuxClient) isClosed() bool {
	return c.closed.Load()
}

func (c *tunnelMuxClient) close() {
	if c.closed.Swap(true) {
		return
	}
	_ = c.conn.Close()

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, st := range c.streams {
		if !st.closed.Load() {
			st.closed.Store(true)
			select {
			case st.errCh <- io.EOF:
			default:
			}
		}
	}
	c.streams = map[string]*tunnelMuxStream{}
}

func (c *tunnelMuxClient) readLoop() {
	defer c.close()
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		c.lastRecv.Store(time.Now().Unix())

		var frame tunnelMuxFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			continue
		}
		if frame.Type == "pong" {
			c.lastPong.Store(time.Now().Unix())
			continue
		}
		if frame.Type == "controller_log" {
			if c.onControllerLog != nil {
				category := strings.TrimSpace(frame.Category)
				if category == "" {
					category = "general"
				}
				msg := strings.TrimSpace(frame.Error)
				if msg == "" && len(frame.Payload) > 0 {
					msg = strings.TrimSpace(string(frame.Payload))
				}
				if msg != "" {
					c.onControllerLog(category, msg)
				}
			}
			continue
		}
		if frame.Type == "ping" {
			_ = c.sendFrame(tunnelMuxFrame{Type: "pong", StreamID: frame.StreamID})
			continue
		}
		streamID := strings.TrimSpace(frame.StreamID)
		if streamID == "" {
			continue
		}

		c.mu.Lock()
		st := c.streams[streamID]
		c.mu.Unlock()
		if st == nil {
			continue
		}

		switch frame.Type {
		case "opened":
			select {
			case st.openCh <- nil:
			default:
			}
		case "open_error":
			err := errors.New(strings.TrimSpace(frame.Error))
			if err.Error() == "" {
				err = errors.New("stream open failed")
			}
			select {
			case st.openCh <- err:
			default:
			}
			st.closeLocal()
		case "data":
			select {
			case st.readCh <- append([]byte(nil), frame.Payload...):
			default:
			}
		case "error":
			err := errors.New(strings.TrimSpace(frame.Error))
			if err.Error() == "" {
				err = errors.New("stream error")
			}
			select {
			case st.errCh <- err:
			default:
			}
			st.closeLocal()
		case "closed":
			select {
			case st.errCh <- io.EOF:
			default:
			}
			st.closeLocal()
		}
	}
}

func (c *tunnelMuxClient) keepAliveLoop() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if c.isClosed() {
			return
		}
		now := time.Now()
		lastRecv := time.Unix(c.lastRecv.Load(), 0)
		lastPong := time.Unix(c.lastPong.Load(), 0)
		if now.Sub(lastRecv) > 90*time.Second || now.Sub(lastPong) > 90*time.Second {
			c.close()
			return
		}
		if err := c.sendFrame(tunnelMuxFrame{Type: "ping", StreamID: "__keepalive__"}); err != nil {
			c.close()
			return
		}
	}
}

func (c *tunnelMuxClient) sendFrame(frame tunnelMuxFrame) error {
	if c.isClosed() {
		return errors.New("tunnel mux connection closed")
	}
	c.writeM.Lock()
	defer c.writeM.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return c.conn.WriteJSON(frame)
}

func (c *tunnelMuxClient) openStream(network, address string) (*tunnelMuxStream, error) {
	if c.isClosed() {
		return nil, errors.New("tunnel mux connection closed")
	}
	streamID := fmt.Sprintf("s%d", atomic.AddUint64(&c.seq, 1))
	st := &tunnelMuxStream{
		client: c,
		id:     streamID,
		readCh: make(chan []byte, 32),
		errCh:  make(chan error, 4),
		openCh: make(chan error, 1),
	}

	c.mu.Lock()
	c.streams[streamID] = st
	c.mu.Unlock()

	if err := c.sendFrame(tunnelMuxFrame{Type: "open", StreamID: streamID, Network: network, Address: address}); err != nil {
		st.closeLocal()
		return nil, err
	}

	select {
	case err := <-st.openCh:
		if err != nil {
			return nil, err
		}
		return st, nil
	case <-time.After(20 * time.Second):
		st.close()
		return nil, errors.New("open stream timeout")
	}
}

func (s *tunnelMuxStream) write(payload []byte) error {
	if s.closed.Load() {
		return io.EOF
	}
	return s.client.sendFrame(tunnelMuxFrame{Type: "data", StreamID: s.id, Payload: payload})
}

func (s *tunnelMuxStream) close() {
	if !s.markClosed() {
		return
	}
	_ = s.client.sendFrame(tunnelMuxFrame{Type: "close", StreamID: s.id})
	s.closeLocal()
}

func (s *tunnelMuxStream) closeLocal() {
	if !s.markClosed() {
		return
	}
	s.client.mu.Lock()
	delete(s.client.streams, s.id)
	s.client.mu.Unlock()
}

func (s *tunnelMuxStream) markClosed() bool {
	if s.closed.Load() {
		return false
	}
	s.closed.Store(true)
	return true
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
	s.logf("open tunnel stream failed, retrying: network=%s target=%s err=%v", network, targetAddr, err)

	// try reconnect once
	client.close()
	client, err = s.ensureTunnelMuxClient()
	if err != nil {
		return nil, err
	}
	return client.openStream(network, targetAddr)
}
