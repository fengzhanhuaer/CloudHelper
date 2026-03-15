package core

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type tunnelControlMessage struct {
	Type    string `json:"type"`
	Network string `json:"network,omitempty"`
	Address string `json:"address,omitempty"`
	Error   string `json:"error,omitempty"`
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
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{
			"error": "https is required",
		})
		return
	}

	token, err := extractSessionTokenForWebSocket(r)
	if err != nil || !IsTokenValid(token) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "invalid or expired session token",
		})
		return
	}

	conn, err := tunnelWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	_, firstPayload, err := conn.ReadMessage()
	if err != nil {
		return
	}

	var connectReq tunnelControlMessage
	if err := json.Unmarshal(firstPayload, &connectReq); err != nil {
		_ = conn.WriteJSON(tunnelControlMessage{Type: "connect_error", Error: "invalid tunnel connect payload"})
		return
	}
	if connectReq.Type != "connect" || strings.TrimSpace(connectReq.Address) == "" {
		_ = conn.WriteJSON(tunnelControlMessage{Type: "connect_error", Error: "invalid tunnel connect request"})
		return
	}
	if n := strings.TrimSpace(connectReq.Network); n != "" && !strings.EqualFold(n, "tcp") {
		_ = conn.WriteJSON(tunnelControlMessage{Type: "connect_error", Error: "only tcp network is supported"})
		return
	}

	dialer := &net.Dialer{Timeout: 12 * time.Second}
	remoteConn, err := dialer.Dial("tcp", strings.TrimSpace(connectReq.Address))
	if err != nil {
		_ = conn.WriteJSON(tunnelControlMessage{Type: "connect_error", Error: err.Error()})
		return
	}
	defer remoteConn.Close()

	if err := conn.WriteJSON(tunnelControlMessage{Type: "connected"}); err != nil {
		return
	}

	var writeMu sync.Mutex
	errCh := make(chan error, 2)

	go func() {
		for {
			msgType, payload, readErr := conn.ReadMessage()
			if readErr != nil {
				errCh <- readErr
				return
			}
			switch msgType {
			case websocket.BinaryMessage:
				if len(payload) == 0 {
					continue
				}
				if _, writeErr := remoteConn.Write(payload); writeErr != nil {
					errCh <- writeErr
					return
				}
			case websocket.TextMessage:
				var msg tunnelControlMessage
				if err := json.Unmarshal(payload, &msg); err == nil && msg.Type == "close" {
					errCh <- io.EOF
					return
				}
			}
		}
	}()

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := remoteConn.Read(buf)
			if n > 0 {
				writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n])
				writeMu.Unlock()
				if writeErr != nil {
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

	<-errCh
}
