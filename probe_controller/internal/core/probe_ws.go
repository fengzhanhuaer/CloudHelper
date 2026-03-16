package core

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

type probeReportMessage struct {
	Type      string             `json:"type"`
	NodeID    string             `json:"node_id"`
	IPv4      []string           `json:"ipv4,omitempty"`
	IPv6      []string           `json:"ipv6,omitempty"`
	System    probeSystemMetrics `json:"system"`
	Version   string             `json:"version,omitempty"`
	Timestamp string             `json:"timestamp,omitempty"`
}

type probeAckMessage struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	ServerUTC string `json:"server_utc"`
}

var probeWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func ProbeWSHandler(w http.ResponseWriter, r *http.Request) {
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

	nodeID, err := authenticateProbeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	wsConn, err := probeWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer wsConn.Close()

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	session, err := yamux.Server(newWebSocketNetConn(wsConn), cfg)
	if err != nil {
		return
	}
	defer session.Close()

	stream, err := session.Accept()
	if err != nil {
		return
	}
	defer stream.Close()

	probeSession := registerProbeSession(nodeID, stream)
	defer unregisterProbeSession(nodeID, probeSession)

	decoder := json.NewDecoder(stream)
	for {
		var msg probeReportMessage
		if err := decoder.Decode(&msg); err != nil {
			return
		}

		reportedNodeID := strings.TrimSpace(msg.NodeID)
		if reportedNodeID == "" {
			reportedNodeID = nodeID
		}

		updateProbeRuntimeReport(reportedNodeID, msg.IPv4, msg.IPv6, msg.System, msg.Version)

		_ = probeSession.writeJSON(probeAckMessage{
			Type:      "ack",
			Message:   "report accepted",
			ServerUTC: time.Now().UTC().Format(time.RFC3339),
		})
	}
}
