package mobilecore

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

const defaultReportIntervalSec = 60

var manager = &coreManager{}

type coreManager struct {
	mu     sync.Mutex
	cancel chan struct{}
	status string
}

type reportPayload struct {
	Type      string       `json:"type"`
	NodeID    string       `json:"node_id"`
	Platform  string       `json:"platform,omitempty"`
	OS        string       `json:"os,omitempty"`
	Arch      string       `json:"arch,omitempty"`
	System    systemStatus `json:"system"`
	Version   string       `json:"version,omitempty"`
	Timestamp string       `json:"timestamp"`
}

type systemStatus struct {
	CPUPercent        float64 `json:"cpu_percent"`
	MemoryTotalBytes  uint64  `json:"memory_total_bytes"`
	MemoryUsedBytes   uint64  `json:"memory_used_bytes"`
	MemoryUsedPercent float64 `json:"memory_used_percent"`
	SwapTotalBytes    uint64  `json:"swap_total_bytes"`
	SwapUsedBytes     uint64  `json:"swap_used_bytes"`
	SwapUsedPercent   float64 `json:"swap_used_percent"`
	DiskTotalBytes    uint64  `json:"disk_total_bytes"`
	DiskUsedBytes     uint64  `json:"disk_used_bytes"`
	DiskUsedPercent   float64 `json:"disk_used_percent"`
}

func Start(controllerURL string, nodeID string, nodeSecret string) string {
	controllerURL = strings.TrimSpace(controllerURL)
	nodeID = strings.TrimSpace(nodeID)
	nodeSecret = strings.TrimSpace(nodeSecret)
	if controllerURL == "" || nodeID == "" || nodeSecret == "" {
		return "controller URL, node ID, and node secret are required"
	}
	wsURL, err := resolveWebSocketURL(controllerURL)
	if err != nil {
		return err.Error()
	}

	manager.mu.Lock()
	if manager.cancel != nil {
		close(manager.cancel)
	}
	cancel := make(chan struct{})
	manager.cancel = cancel
	manager.status = "starting"
	manager.mu.Unlock()

	go runLoop(cancel, wsURL, nodeID, nodeSecret)
	return "starting"
}

func Stop() string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.cancel != nil {
		close(manager.cancel)
		manager.cancel = nil
	}
	manager.status = "stopped"
	return manager.status
}

func Status() string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if strings.TrimSpace(manager.status) == "" {
		return "stopped"
	}
	return manager.status
}

func runLoop(cancel <-chan struct{}, wsURL string, nodeID string, nodeSecret string) {
	for {
		select {
		case <-cancel:
			setStatus("stopped")
			return
		default:
		}
		if err := runSession(cancel, wsURL, nodeID, nodeSecret); err != nil {
			setStatus("disconnected: " + err.Error())
		}
		select {
		case <-cancel:
			setStatus("stopped")
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func runSession(cancel <-chan struct{}, wsURL string, nodeID string, nodeSecret string) error {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := buildAuthHeaders(nodeID, nodeSecret)
	wsConn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		return err
	}
	defer wsConn.Close()

	session, err := yamux.Client(newWebSocketNetConn(wsConn), yamux.DefaultConfig())
	if err != nil {
		return err
	}
	defer session.Close()

	stream, err := session.Open()
	if err != nil {
		return err
	}
	defer stream.Close()

	encoder := json.NewEncoder(stream)
	decoder := json.NewDecoder(stream)
	writeMu := &sync.Mutex{}
	setStatus("connected")
	if err := sendReport(stream, encoder, writeMu, nodeID); err != nil {
		return err
	}

	readErrCh := make(chan error, 1)
	go func() {
		for {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				readErrCh <- err
				return
			}
		}
	}()

	ticker := time.NewTicker(defaultReportIntervalSec * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			return nil
		case err := <-readErrCh:
			return err
		case <-ticker.C:
			if err := sendReport(stream, encoder, writeMu, nodeID); err != nil {
				return err
			}
		}
	}
}

func sendReport(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, nodeID string) error {
	payload := reportPayload{
		Type:      "report",
		NodeID:    nodeID,
		Platform:  "android",
		OS:        "android",
		Arch:      runtime.GOARCH,
		System:    systemStatus{},
		Version:   "android-mvp",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := encoder.Encode(payload)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}

func setStatus(status string) {
	manager.mu.Lock()
	manager.status = strings.TrimSpace(status)
	manager.mu.Unlock()
}

func resolveWebSocketURL(controllerURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(controllerURL))
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported controller scheme: %s", u.Scheme)
	}
	u.Path = "/api/probe"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func buildAuthHeaders(nodeID string, secret string) http.Header {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	randomToken := randomHexToken(16)
	signature := signConnect(secret, nodeID, timestamp, randomToken)
	headers := http.Header{}
	headers.Set("X-Probe-Node-Id", strings.TrimSpace(nodeID))
	headers.Set("X-Probe-Timestamp", timestamp)
	headers.Set("X-Probe-Rand", randomToken)
	headers.Set("X-Probe-Signature", signature)
	return headers
}

func signConnect(secret, nodeID, timestamp, randomToken string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(strings.TrimSpace(nodeID)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(timestamp)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(randomToken)))
	return hex.EncodeToString(mac.Sum(nil))
}

func randomHexToken(size int) string {
	if size <= 0 {
		size = 8
	}
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

type webSocketNetConn struct {
	ws      *websocket.Conn
	readMu  sync.Mutex
	writeMu sync.Mutex
	reader  netReader
}

type netReader interface {
	Read([]byte) (int, error)
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
		if errors.Is(err, net.ErrClosed) {
			return n, err
		}
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
	return n, closeErr
}

func (c *webSocketNetConn) Close() error {
	return c.ws.Close()
}

func (c *webSocketNetConn) LocalAddr() net.Addr {
	if c.ws == nil || c.ws.UnderlyingConn() == nil {
		return dummyAddr("local")
	}
	return c.ws.UnderlyingConn().LocalAddr()
}

func (c *webSocketNetConn) RemoteAddr() net.Addr {
	if c.ws == nil || c.ws.UnderlyingConn() == nil {
		return dummyAddr("remote")
	}
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

type dummyAddr string

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }
