package core

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type tunnelControlMessage struct {
	Type     string `json:"type"`
	StreamID string `json:"stream_id,omitempty"`
	Network  string `json:"network,omitempty"`
	Address  string `json:"address,omitempty"`
	Error    string `json:"error,omitempty"`
	Payload  []byte `json:"payload,omitempty"`
}

type tunnelRemoteStream struct {
	network string
	tcpConn net.Conn
	udpConn *net.UDPConn
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

	conn, err := tunnelWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	send := func(msg tunnelControlMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
		return conn.WriteJSON(msg)
	}

	streams := make(map[string]*tunnelRemoteStream)
	var streamsMu sync.Mutex
	closeStream := func(streamID string) {
		streamsMu.Lock()
		st := streams[streamID]
		delete(streams, streamID)
		streamsMu.Unlock()
		if st == nil {
			return
		}
		if st.tcpConn != nil {
			_ = st.tcpConn.Close()
		}
		if st.udpConn != nil {
			_ = st.udpConn.Close()
		}
	}
	defer func() {
		streamsMu.Lock()
		ids := make([]string, 0, len(streams))
		for id := range streams {
			ids = append(ids, id)
		}
		streamsMu.Unlock()
		for _, id := range ids {
			closeStream(id)
		}
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var msg tunnelControlMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		streamID := strings.TrimSpace(msg.StreamID)
		if streamID == "" {
			continue
		}

		switch msg.Type {
		case "open":
			network := strings.ToLower(strings.TrimSpace(msg.Network))
			if network == "" {
				network = "tcp"
			}
			address := strings.TrimSpace(msg.Address)
			if address == "" {
				_ = send(tunnelControlMessage{Type: "open_error", StreamID: streamID, Error: "missing address"})
				continue
			}

			if network == "udp" {
				udpAddr, err := net.ResolveUDPAddr("udp", address)
				if err != nil {
					_ = send(tunnelControlMessage{Type: "open_error", StreamID: streamID, Error: err.Error()})
					continue
				}
				udpConn, err := net.DialUDP("udp", nil, udpAddr)
				if err != nil {
					_ = send(tunnelControlMessage{Type: "open_error", StreamID: streamID, Error: err.Error()})
					continue
				}
				streamsMu.Lock()
				streams[streamID] = &tunnelRemoteStream{network: "udp", udpConn: udpConn}
				streamsMu.Unlock()
				_ = send(tunnelControlMessage{Type: "opened", StreamID: streamID})

				go func(id string, uc *net.UDPConn, target string) {
					buf := make([]byte, 65535)
					for {
						n, remote, readErr := uc.ReadFromUDP(buf)
						if n > 0 {
							respAddr := target
							if remote != nil {
								respAddr = remote.String()
							}
							if writeErr := send(tunnelControlMessage{Type: "data", StreamID: id, Address: respAddr, Payload: append([]byte(nil), buf[:n]...)}); writeErr != nil {
								closeStream(id)
								return
							}
						}
						if readErr != nil {
							_ = send(tunnelControlMessage{Type: "closed", StreamID: id})
							closeStream(id)
							return
						}
					}
				}(streamID, udpConn, address)
				continue
			}

			dialer := &net.Dialer{Timeout: 12 * time.Second}
			remoteConn, err := dialer.Dial("tcp", address)
			if err != nil {
				_ = send(tunnelControlMessage{Type: "open_error", StreamID: streamID, Error: err.Error()})
				continue
			}
			streamsMu.Lock()
			streams[streamID] = &tunnelRemoteStream{network: "tcp", tcpConn: remoteConn}
			streamsMu.Unlock()
			_ = send(tunnelControlMessage{Type: "opened", StreamID: streamID})

			go func(id string, rc net.Conn) {
				buf := make([]byte, 32*1024)
				for {
					n, readErr := rc.Read(buf)
					if n > 0 {
						if writeErr := send(tunnelControlMessage{Type: "data", StreamID: id, Payload: append([]byte(nil), buf[:n]...)}); writeErr != nil {
							closeStream(id)
							return
						}
					}
					if readErr != nil {
						_ = send(tunnelControlMessage{Type: "closed", StreamID: id})
						closeStream(id)
						return
					}
				}
			}(streamID, remoteConn)

		case "data":
			streamsMu.Lock()
			st := streams[streamID]
			streamsMu.Unlock()
			if st == nil {
				_ = send(tunnelControlMessage{Type: "error", StreamID: streamID, Error: "stream not found"})
				continue
			}
			if st.network == "udp" {
				if len(msg.Payload) == 0 {
					continue
				}
				_ = st.udpConn.SetDeadline(time.Now().Add(15 * time.Second))
				if _, err := st.udpConn.Write(msg.Payload); err != nil {
					_ = send(tunnelControlMessage{Type: "error", StreamID: streamID, Error: err.Error()})
					closeStream(streamID)
				}
				continue
			}
			if len(msg.Payload) == 0 {
				continue
			}
			if _, err := st.tcpConn.Write(msg.Payload); err != nil {
				_ = send(tunnelControlMessage{Type: "error", StreamID: streamID, Error: err.Error()})
				closeStream(streamID)
			}

		case "close":
			closeStream(streamID)
			_ = send(tunnelControlMessage{Type: "closed", StreamID: streamID})
		case "ping":
			_ = send(tunnelControlMessage{Type: "pong", StreamID: streamID})
		}
	}
}
