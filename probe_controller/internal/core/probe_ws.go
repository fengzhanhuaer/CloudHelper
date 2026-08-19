package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

type probeReportMessage struct {
	Type                 string                        `json:"type"`
	NodeID               string                        `json:"node_id"`
	Platform             string                        `json:"platform,omitempty"`
	OS                   string                        `json:"os,omitempty"`
	Arch                 string                        `json:"arch,omitempty"`
	IPv4                 []string                      `json:"ipv4,omitempty"`
	IPv6                 []string                      `json:"ipv6,omitempty"`
	System               probeSystemMetrics            `json:"system"`
	MachineUptimeSeconds int64                         `json:"machine_uptime_seconds,omitempty"`
	Version              string                        `json:"version,omitempty"`
	BuildKind            string                        `json:"build_kind,omitempty"`
	SpecialExit          probeSpecialExitRuntimeReport `json:"special_exit,omitempty"`
	LinuxRouter          probeLinuxRouterRuntimeReport `json:"linux_router,omitempty"`
	RelayStatus          []probeRelayStatusItem        `json:"relay_status,omitempty"`
	Timestamp            string                        `json:"timestamp,omitempty"`
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
	decoder := json.NewDecoder(stream)

	probeSession := registerProbeSession(nodeID, stream)
	defer unregisterProbeSession(nodeID, probeSession)
	if node, ok := getProbeNodeByID(nodeID); ok {
		_, _ = dispatchProbeLocalConsoleControl(node)
	}

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

			msg.NodeID = nodeID
			updateProbeRuntimeReportWithPlatform(nodeID, msg.IPv4, msg.IPv6, msg.System, msg.Version, msg.Platform, msg.OS, msg.Arch, msg.MachineUptimeSeconds, msg.RelayStatus)
			if updateProbeRuntimeProductStatus(nodeID, msg.BuildKind, msg.SpecialExit, msg.LinuxRouter) {
				logProbeRouteConfigSyncSource(fmt.Sprintf("schedule:linux_router_report node_id=%s local_ip_proxy=%t published_cidrs=%s allowed_nodes=%s", nodeID, msg.LinuxRouter.LocalIPProxyEnabled, strings.Join(msg.LinuxRouter.PublishedCIDRs, ","), strings.Join(msg.LinuxRouter.AllowedNodeIDs, ",")))
				scheduleProbeRouteConfigSyncToKnownNodes()
			}

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
			msg.NodeID = nodeID
			consumeProbeLogsResult(msg)
		case "tcp_debug_result":
			var msg probeTCPDebugResultMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			msg.NodeID = nodeID
			consumeProbeTCPDebugResult(msg)
		case "network_monitor_result":
			var msg probeNetworkMonitorResultMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			msg.NodeID = nodeID
			consumeProbeNetworkMonitorResult(msg)
		case "shell_exec_result":
			var msg probeShellExecResultMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			msg.NodeID = nodeID
			consumeProbeShellExecResult(msg)
		case "shell_session_result":
			var msg probeShellSessionResultMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			msg.NodeID = nodeID
			consumeProbeShellSessionResult(msg)
		case "subscription_fetch_result":
			var msg probeSubscriptionFetchResultMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			msg.NodeID = nodeID
			consumeProbeSubscriptionFetchResult(msg)
		case "local_console_bridge_result":
			var msg probeLocalConsoleBridgeResultMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			msg.NodeID = nodeID
			consumeProbeLocalConsoleBridgeResult(msg)
		case "controller_rpc_request":
			var req probeControllerRPCRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				_ = probeSession.writeJSON(probeControllerRPCResponse{
					Type:  "controller_rpc_response",
					OK:    false,
					Error: "invalid controller rpc request",
				})
				continue
			}
			resp := handleProbeControllerRPCRequest(nodeID, req)
			_ = probeSession.writeJSON(resp)
		default:
			// Ignore unknown probe message types to keep backward compatibility.
		}
	}
}
