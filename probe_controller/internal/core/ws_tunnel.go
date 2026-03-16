package core

import (
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
)

type tunnelControlMessage struct {
	Type     string `json:"type"`
	StreamID string `json:"stream_id,omitempty"`
	Network  string `json:"network,omitempty"`
	Address  string `json:"address,omitempty"`
	Category string `json:"category,omitempty"`
	Error    string `json:"error,omitempty"`
	Payload  []byte `json:"payload,omitempty"`
}

type tunnelRemoteStream struct {
	network string
	target  string
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
	log.Printf("tunnel ws connected from %s", r.RemoteAddr)
	defer log.Printf("tunnel ws disconnected from %s", r.RemoteAddr)
	defer conn.Close()

	var writeMu sync.Mutex
	send := func(msg tunnelControlMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
		return conn.WriteJSON(msg)
	}
	pushControllerException := func(category string, format string, args ...any) {
		message := strings.TrimSpace(fmt.Sprintf(format, args...))
		if message == "" {
			return
		}
		_ = send(tunnelControlMessage{Type: "controller_log", Category: strings.TrimSpace(category), Error: message})
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

	openTunnelStream := func(streamID string, network string, address string) {
		if network == "udp" {
			udpAddr, err := net.ResolveUDPAddr("udp", address)
			if err != nil {
				_ = send(tunnelControlMessage{Type: "open_error", StreamID: streamID, Error: err.Error()})
				pushControllerException("open", "open stream failed: network=%s target=%s stream=%s err=%v", network, address, streamID, err)
				return
			}
			udpConn, err := net.DialUDP("udp", nil, udpAddr)
			if err != nil {
				_ = send(tunnelControlMessage{Type: "open_error", StreamID: streamID, Error: err.Error()})
				pushControllerException("open", "open stream failed: network=%s target=%s stream=%s err=%v", network, address, streamID, err)
				return
			}
			streamsMu.Lock()
			streams[streamID] = &tunnelRemoteStream{network: "udp", target: address, udpConn: udpConn}
			streamsMu.Unlock()
			if err := send(tunnelControlMessage{Type: "opened", StreamID: streamID}); err != nil {
				closeStream(streamID)
				return
			}

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
						if !errors.Is(readErr, net.ErrClosed) {
							pushControllerException("read", "stream read failed: network=udp target=%s stream=%s err=%v", target, id, readErr)
						}
						_ = send(tunnelControlMessage{Type: "closed", StreamID: id})
						closeStream(id)
						return
					}
				}
			}(streamID, udpConn, address)
			return
		}

		dialer := &net.Dialer{Timeout: 10 * time.Second}
		remoteConn, err := dialer.Dial("tcp", address)
		if err != nil {
			_ = send(tunnelControlMessage{Type: "open_error", StreamID: streamID, Error: err.Error()})
			pushControllerException("open", "open stream failed: network=%s target=%s stream=%s err=%v", network, address, streamID, err)
			return
		}
		streamsMu.Lock()
		streams[streamID] = &tunnelRemoteStream{network: "tcp", target: address, tcpConn: remoteConn}
		streamsMu.Unlock()
		if err := send(tunnelControlMessage{Type: "opened", StreamID: streamID}); err != nil {
			closeStream(streamID)
			return
		}

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
					if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, net.ErrClosed) {
						pushControllerException("read", "stream read failed: network=tcp target=%s stream=%s err=%v", rc.RemoteAddr().String(), id, readErr)
					}
					_ = send(tunnelControlMessage{Type: "closed", StreamID: id})
					closeStream(id)
					return
				}
			}
		}(streamID, remoteConn)
	}

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
			go openTunnelStream(streamID, network, address)

		case "data":
			streamsMu.Lock()
			st := streams[streamID]
			streamsMu.Unlock()
			if st == nil {
				_ = send(tunnelControlMessage{Type: "error", StreamID: streamID, Error: "stream not found"})
				pushControllerException("state", "stream write rejected: stream=%s err=stream not found", streamID)
				continue
			}
			if st.network == "udp" && st.udpConn == nil {
				_ = send(tunnelControlMessage{Type: "error", StreamID: streamID, Error: "stream not opened yet"})
				pushControllerException("state", "stream write rejected: network=udp target=%s stream=%s err=stream not opened yet", st.target, streamID)
				continue
			}
			if st.network != "udp" && st.tcpConn == nil {
				_ = send(tunnelControlMessage{Type: "error", StreamID: streamID, Error: "stream not opened yet"})
				pushControllerException("state", "stream write rejected: network=tcp target=%s stream=%s err=stream not opened yet", st.target, streamID)
				continue
			}
			if st.network == "udp" {
				if len(msg.Payload) == 0 {
					continue
				}
				_ = st.udpConn.SetDeadline(time.Now().Add(15 * time.Second))
				if _, err := st.udpConn.Write(msg.Payload); err != nil {
					_ = send(tunnelControlMessage{Type: "error", StreamID: streamID, Error: err.Error()})
					pushControllerException("write", "stream write failed: network=udp target=%s stream=%s err=%v", st.target, streamID, err)
					closeStream(streamID)
				}
				continue
			}
			if len(msg.Payload) == 0 {
				continue
			}
			if _, err := st.tcpConn.Write(msg.Payload); err != nil {
				_ = send(tunnelControlMessage{Type: "error", StreamID: streamID, Error: err.Error()})
				pushControllerException("write", "stream write failed: network=tcp target=%s stream=%s err=%v", st.target, streamID, err)
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
