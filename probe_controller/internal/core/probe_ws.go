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

type probeInboundEnvelope struct {
	Type string `json:"type"`
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
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return
		}

		var envelope probeInboundEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(envelope.Type)) {
		case "", "report":
			var msg probeReportMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
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
		case "logs_result":
			var msg probeLogsResultMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if strings.TrimSpace(msg.NodeID) == "" {
				msg.NodeID = nodeID
			}
			consumeProbeLogsResult(msg)
		case "link_test_control_result":
			var msg probeLinkTestControlResultMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if strings.TrimSpace(msg.NodeID) == "" {
				msg.NodeID = nodeID
			}
			consumeProbeLinkTestControlResult(msg)
		case "shell_exec_result":
			var msg probeShellExecResultMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if strings.TrimSpace(msg.NodeID) == "" {
				msg.NodeID = nodeID
			}
			consumeProbeShellExecResult(msg)
		case "shell_session_result":
			var msg probeShellSessionResultMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if strings.TrimSpace(msg.NodeID) == "" {
				msg.NodeID = nodeID
			}
			consumeProbeShellSessionResult(msg)
		default:
			// Ignore unknown probe message types to keep backward compatibility.
		}
	}
}
