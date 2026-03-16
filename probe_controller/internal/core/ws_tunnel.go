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

	switch network {
	case "tcp":
		handleTCPTunnelStream(stream, target, pushControllerException)
	case "udp":
		handleUDPTunnelStream(stream, target, pushControllerException)
	default:
		_ = writeTunnelOpenResponse(stream, tunnelOpenResponse{OK: false, Error: "unsupported network"})
		pushControllerException("state", "open stream rejected: network=%s target=%s err=unsupported network", network, target)
	}
}

func handleTCPTunnelStream(stream net.Conn, target string, pushControllerException func(category string, format string, args ...any)) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	remoteConn, err := dialer.Dial("tcp", target)
	if err != nil {
		_ = writeTunnelOpenResponse(stream, tunnelOpenResponse{OK: false, Error: err.Error()})
		pushControllerException("open", "open stream failed: network=tcp target=%s err=%v", target, err)
		return
	}
	defer remoteConn.Close()

	if err := writeTunnelOpenResponse(stream, tunnelOpenResponse{OK: true}); err != nil {
		return
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

	err = <-errCh
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		pushControllerException("read", "stream relay failed: network=tcp target=%s err=%v", target, err)
	}
}

func handleUDPTunnelStream(stream net.Conn, target string, pushControllerException func(category string, format string, args ...any)) {
	udpAddr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		_ = writeTunnelOpenResponse(stream, tunnelOpenResponse{OK: false, Error: err.Error()})
		pushControllerException("open", "open stream failed: network=udp target=%s err=%v", target, err)
		return
	}
	udpConn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		_ = writeTunnelOpenResponse(stream, tunnelOpenResponse{OK: false, Error: err.Error()})
		pushControllerException("open", "open stream failed: network=udp target=%s err=%v", target, err)
		return
	}
	defer udpConn.Close()

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
		pushControllerException("read", "stream relay failed: network=udp target=%s err=%v", target, err)
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
