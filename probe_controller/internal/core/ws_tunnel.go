package core

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

type tunnelAssociationV2Meta struct {
	Version          int    `json:"version"`
	AssocKeyV2       string `json:"assoc_key_v2,omitempty"`
	FlowID           string `json:"flow_id,omitempty"`
	SrcIP            string `json:"src_ip,omitempty"`
	SrcPort          uint16 `json:"src_port,omitempty"`
	DstIP            string `json:"dst_ip,omitempty"`
	DstPort          uint16 `json:"dst_port,omitempty"`
	IPFamily         uint8  `json:"ip_family,omitempty"`
	Transport        string `json:"transport,omitempty"`
	RouteGroup       string `json:"route_group,omitempty"`
	RouteNodeID      string `json:"route_node_id,omitempty"`
	RouteTarget      string `json:"route_target,omitempty"`
	RouteFingerprint string `json:"route_fingerprint,omitempty"`
	NATMode          string `json:"nat_mode,omitempty"`
	TTLProfile       string `json:"ttl_profile,omitempty"`
	IdleTimeoutMS    int64  `json:"idle_timeout_ms,omitempty"`
	GCIntervalMS     int64  `json:"gc_interval_ms,omitempty"`
	CreatedAtUnixMS  int64  `json:"created_at_unix_ms,omitempty"`
}

type tunnelOpenRequest struct {
	Type          string                   `json:"type"`
	Network       string                   `json:"network"`
	Address       string                   `json:"address"`
	AssociationV2 *tunnelAssociationV2Meta `json:"association_v2,omitempty"`
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

const (
	tunnelControllerExceptionRateLimitWindow   = 3 * time.Second
	tunnelControllerExceptionRateLimitBurst    = 6
	tunnelControllerExceptionRateLimitMaxKeys  = 2048
	tunnelControllerExceptionRateLimitKeyMaxLen = 160
)

type tunnelControllerExceptionBucket struct {
	windowStart time.Time
	count       int
}

type tunnelControllerExceptionRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]tunnelControllerExceptionBucket
}

var globalTunnelControllerExceptionRateLimiter = tunnelControllerExceptionRateLimiter{
	buckets: make(map[string]tunnelControllerExceptionBucket),
}

var tunnelWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  2048,
	WriteBufferSize: 2048,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func NetworkAssistantTunnelWSHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}

	token, err := extractSessionTokenForWebSocket(r)
	if err != nil || !IsTokenValid(token) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired session token"})
		return
	}

	wsConn, err := tunnelWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	log.Printf("tunnel ws connected from %s", r.RemoteAddr)
	defer log.Printf("tunnel ws disconnected from %s", r.RemoteAddr)
	defer wsConn.Close()

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second

	session, err := yamux.Server(newWebSocketNetConn(wsConn), cfg)
	if err != nil {
		return
	}
	defer session.Close()

	pushControllerException := func(category string, format string, args ...any) {
		message := strings.TrimSpace(fmt.Sprintf(format, args...))
		if message == "" {
			return
		}
		if !globalTunnelControllerExceptionRateLimiter.Allow(category, message, time.Now()) {
			return
		}
		if err := pushTunnelControllerLog(session, strings.TrimSpace(category), message); err != nil {
			log.Printf("tunnel controller log push failed: %v", err)
		}
	}

	for {
		stream, acceptErr := session.Accept()
		if acceptErr != nil {
			return
		}
		go handleTunnelStream(stream, pushControllerException)
	}
}

func (l *tunnelControllerExceptionRateLimiter) Allow(category, message string, now time.Time) bool {
	if l == nil {
		return true
	}
	key := buildTunnelControllerExceptionRateLimitKey(category, message)
	if key == "" {
		return true
	}
	if now.IsZero() {
		now = time.Now()
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = make(map[string]tunnelControllerExceptionBucket)
	}

	if len(l.buckets) >= tunnelControllerExceptionRateLimitMaxKeys {
		threshold := now.Add(-2 * tunnelControllerExceptionRateLimitWindow)
		for k, v := range l.buckets {
			if v.windowStart.Before(threshold) {
				delete(l.buckets, k)
			}
		}
	}

	bucket := l.buckets[key]
	if bucket.windowStart.IsZero() || now.Sub(bucket.windowStart) >= tunnelControllerExceptionRateLimitWindow {
		bucket.windowStart = now
		bucket.count = 1
		l.buckets[key] = bucket
		return true
	}
	bucket.count++
	l.buckets[key] = bucket
	return bucket.count <= tunnelControllerExceptionRateLimitBurst
}

func buildTunnelControllerExceptionRateLimitKey(category, message string) string {
	cat := strings.ToLower(strings.TrimSpace(category))
	msg := strings.ToLower(strings.TrimSpace(message))
	if len(msg) > tunnelControllerExceptionRateLimitKeyMaxLen {
		msg = msg[:tunnelControllerExceptionRateLimitKeyMaxLen]
	}
	if cat == "" && msg == "" {
		return ""
	}
	return cat + "|" + msg
}

func handleTunnelStream(stream net.Conn, pushControllerException func(category string, format string, args ...any)) {
	defer stream.Close()

	_ = stream.SetReadDeadline(time.Now().Add(20 * time.Second))
	var req tunnelOpenRequest
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
		_ = writeTunnelOpenResponse(stream, tunnelOpenResponse{OK: false, Error: "missing address"})
		pushControllerException("state", "open stream rejected: network=%s err=missing address", network)
		return
	}

	associationV2 := req.AssociationV2
	switch network {
	case "tcp":
		handleTCPTunnelStream(stream, target, pushControllerException)
	case "udp":
		handleUDPTunnelStream(stream, target, associationV2, pushControllerException)
	default:
		_ = writeTunnelOpenResponse(stream, tunnelOpenResponse{OK: false, Error: "unsupported network"})
		pushControllerException("state", "open stream rejected: network=%s target=%s err=unsupported network", network, target)
	}
}

func handleTCPTunnelStream(stream net.Conn, target string, pushControllerException func(category string, format string, args ...any)) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	remoteConn, err := dialer.Dial("tcp", target)
	if err != nil {
		globalControllerTCPDebugState.recordFailure("open_failed", target, err)
		_ = writeTunnelOpenResponse(stream, tunnelOpenResponse{OK: false, Error: err.Error()})
		pushControllerException("open", "open stream failed: network=tcp target=%s err=%v", target, err)
		return
	}
	defer remoteConn.Close()

	if err := writeTunnelOpenResponse(stream, tunnelOpenResponse{OK: true}); err != nil {
		return
	}

	relay := globalControllerTCPDebugState.beginRelay(target)
	errCh := make(chan error, 2)
	go func() {
		if relay != nil {
			defer relay.releaseSide()
		}
		writer := io.Writer(remoteConn)
		if relay != nil {
			writer = &controllerTCPDebugWriter{dst: remoteConn, relay: relay, direction: "up"}
		}
		_, copyErr := io.Copy(writer, stream)
		errCh <- copyErr
	}()
	go func() {
		if relay != nil {
			defer relay.releaseSide()
		}
		writer := io.Writer(stream)
		if relay != nil {
			writer = &controllerTCPDebugWriter{dst: stream, relay: relay, direction: "down"}
		}
		_, copyErr := io.Copy(writer, remoteConn)
		errCh <- copyErr
	}()

	err = <-errCh
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		if relay != nil {
			globalControllerTCPDebugState.recordRelayFailure(relay, err)
		} else {
			globalControllerTCPDebugState.recordFailure("relay_failed", target, err)
		}
		pushControllerException("read", "stream relay failed: network=tcp target=%s err=%v", target, err)
	}
}

func handleUDPTunnelStream(stream net.Conn, target string, associationV2 *tunnelAssociationV2Meta, pushControllerException func(category string, format string, args ...any)) {
	assoc, err := globalTunnelUDPAssociationPool.Acquire(associationV2, target)
	if err != nil {
		_ = writeTunnelOpenResponse(stream, tunnelOpenResponse{OK: false, Error: err.Error()})
		assocKey := ""
		if associationV2 != nil {
			assocKey = strings.TrimSpace(associationV2.AssocKeyV2)
		}
		pushControllerException("open", "open stream failed: network=udp target=%s assoc_key_v2=%s err=%v", target, assocKey, err)
		return
	}
	defer assoc.Release()

	if err := writeTunnelOpenResponse(stream, tunnelOpenResponse{OK: true}); err != nil {
		return
	}

	errCh := make(chan error, 2)
	go func() {
		for {
			payload, readErr := readFramedPacket(stream)
			if readErr != nil {
				errCh <- readErr
				return
			}
			if len(payload) == 0 {
				continue
			}
			if writeErr := assoc.Write(payload); writeErr != nil {
				errCh <- writeErr
				return
			}
		}
	}()

	go func() {
		buf := make([]byte, tunnelUDPAssociationReadBufSize)
		for {
			n, readErr := assoc.Read(buf)
			if n > 0 {
				if writeErr := writeFramedPacket(stream, buf[:n]); writeErr != nil {
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

	err = <-errCh
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		assocKey := ""
		if associationV2 != nil {
			assocKey = strings.TrimSpace(associationV2.AssocKeyV2)
		}
		pushControllerException("read", "stream relay failed: network=udp target=%s assoc_key_v2=%s err=%v", target, assocKey, err)
	}
}

func writeTunnelOpenResponse(stream net.Conn, resp tunnelOpenResponse) error {
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := json.NewEncoder(stream).Encode(resp)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}

func pushTunnelControllerLog(session *yamux.Session, category, message string) error {
	if session == nil || session.IsClosed() {
		return errors.New("tunnel session closed")
	}
	stream, err := session.Open()
	if err != nil {
		return err
	}
	defer stream.Close()

	if strings.TrimSpace(category) == "" {
		category = "general"
	}
	inbound := tunnelInboundMessage{Type: "controller_log", Category: category, Message: strings.TrimSpace(message)}
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err = json.NewEncoder(stream).Encode(inbound)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
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

func writeAll(w io.Writer, payload []byte) error {
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
