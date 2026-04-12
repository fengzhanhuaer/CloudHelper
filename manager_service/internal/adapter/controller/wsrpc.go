// Package controller — wsrpc.go
// WS-RPC 客户端：将 JSON 请求通过 WebSocket 转发至 probe_controller /api/admin/ws
// 并同步等待对应 id 的响应，支持 context 超时。
//
// 协议格式（参见 probe_controller/internal/core/ws_admin.go）：
//   发送: {"id":"<uuid>","action":"<method>","payload":{...}}
//   认证: {"id":"<uuid>","action":"auth.session","payload":{"token":"<token>"}}
//   响应: {"id":"<uuid>","ok":true|false,"data":{...},"error":"..."}
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsRPCDialTimeout  = 10 * time.Second
	wsRPCCallTimeout  = 30 * time.Second
	wsRPCLongTimeout  = 120 * time.Second
)

// wsRPCRequest is the wire format for admin WS-RPC requests.
type wsRPCRequest struct {
	ID      string      `json:"id"`
	Action  string      `json:"action"`
	Payload interface{} `json:"payload"`
}

// wsRPCResponse is the wire format for admin WS-RPC responses.
type wsRPCResponse struct {
	ID    string          `json:"id"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// CallWS executes a single WS-RPC action against the probe_controller admin WebSocket.
// It opens a fresh WebSocket connection, authenticates, sends the action, waits for
// the matching response, and closes the connection.
//
// payload may be nil (empty payload).
// The returned json.RawMessage is the "data" field from the response.
func (s *Session) CallWS(ctx context.Context, action string, payload interface{}, timeoutOverride time.Duration) (json.RawMessage, error) {
	s.mu.RLock()
	base := s.baseURL
	token := s.token
	s.mu.RUnlock()

	if token == "" {
		return nil, fmt.Errorf("controller session not established")
	}

	timeout := wsRPCCallTimeout
	if timeoutOverride > 0 {
		timeout = timeoutOverride
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, wsRPCDialTimeout)
	defer dialCancel()

	wsURL := toWSURL(base) + "/api/admin/ws"
	dialer := websocket.Dialer{
		HandshakeTimeout: wsRPCDialTimeout,
		NetDialContext:   nil,
	}
	header := http.Header{}
	header.Set("X-Forwarded-Proto", "https")

	conn, _, err := dialer.DialContext(dialCtx, wsURL, header)
	if err != nil {
		return nil, fmt.Errorf("dial controller ws: %w", err)
	}
	defer conn.Close()

	// callCtx governs the total call duration.
	callCtx, callCancel := context.WithTimeout(ctx, timeout)
	defer callCancel()

	// pending maps request IDs to their response channels.
	type pendingEntry struct {
		ch chan wsRPCResponse
	}
	pending := make(map[string]*pendingEntry)
	var pendingMu sync.Mutex

	// readDone signals that the read loop has exited.
	readDone := make(chan error, 1)

	// Start read loop.
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				readDone <- err
				return
			}
			var resp wsRPCResponse
			if err := json.Unmarshal(msg, &resp); err != nil {
				continue
			}
			pendingMu.Lock()
			if entry, ok := pending[resp.ID]; ok {
				select {
				case entry.ch <- resp:
				default:
				}
			}
			pendingMu.Unlock()
		}
	}()

	// Helper: send a request and wait for its response.
	sendAndWait := func(id, act string, pl interface{}) (wsRPCResponse, error) {
		ch := make(chan wsRPCResponse, 1)
		pendingMu.Lock()
		pending[id] = &pendingEntry{ch: ch}
		pendingMu.Unlock()
		defer func() {
			pendingMu.Lock()
			delete(pending, id)
			pendingMu.Unlock()
		}()

		req := wsRPCRequest{ID: id, Action: act, Payload: pl}
		if err := conn.WriteJSON(req); err != nil {
			return wsRPCResponse{}, fmt.Errorf("write ws request: %w", err)
		}
		select {
		case resp := <-ch:
			return resp, nil
		case err := <-readDone:
			return wsRPCResponse{}, fmt.Errorf("ws connection closed: %w", err)
		case <-callCtx.Done():
			return wsRPCResponse{}, fmt.Errorf("ws call timeout: %w", callCtx.Err())
		}
	}

	// Step 1: authenticate.
	authID := newRPCID()
	authResp, err := sendAndWait(authID, "auth.session", map[string]string{"token": token})
	if err != nil {
		return nil, fmt.Errorf("ws auth: %w", err)
	}
	if !authResp.OK {
		return nil, fmt.Errorf("ws auth rejected: %s", authResp.Error)
	}

	// Step 2: call the actual action.
	callID := newRPCID()
	callResp, err := sendAndWait(callID, action, payload)
	if err != nil {
		return nil, fmt.Errorf("ws rpc %s: %w", action, err)
	}
	if !callResp.OK {
		return nil, fmt.Errorf("ws rpc %s error: %s", action, callResp.Error)
	}

	return callResp.Data, nil
}

// toWSURL converts http://host to ws://host and https://host to wss://host.
func toWSURL(httpURL string) string {
	s := strings.TrimRight(httpURL, "/")
	if strings.HasPrefix(s, "https://") {
		return "wss://" + s[len("https://"):]
	}
	if strings.HasPrefix(s, "http://") {
		return "ws://" + s[len("http://"):]
	}
	return s
}

// newRPCID generates a short random request ID.
func newRPCID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

// ensure gorilla/websocket is referenced (imported).
var _ io.Reader
