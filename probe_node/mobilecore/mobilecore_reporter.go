package mobilecore

// This file owns the Android <-> controller reporter session.
// It uses yamux + JSON on top of the controller websocket.
// It does not participate in the vRoute data plane or frame protocol.

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

type reportPayload struct {
	Type        string                            `json:"type"`
	NodeID      string                            `json:"node_id"`
	Platform    string                            `json:"platform,omitempty"`
	OS          string                            `json:"os,omitempty"`
	Arch        string                            `json:"arch,omitempty"`
	IPv4        []string                          `json:"ipv4,omitempty"`
	IPv6        []string                          `json:"ipv6,omitempty"`
	System      systemStatus                      `json:"system"`
	Version     string                            `json:"version,omitempty"`
	RelayStatus []mobileProbeRouteRelayReportItem `json:"relay_status,omitempty"`
	Timestamp   string                            `json:"timestamp"`
}

type probeAckMessage struct {
	Type      string `json:"type"`
	Message   string `json:"message,omitempty"`
	ServerUTC string `json:"server_utc,omitempty"`
}

type controlEnvelope struct {
	Type string `json:"type"`
}

type logsControlMessage struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	Lines        int    `json:"lines"`
	SinceMinutes int    `json:"since_minutes"`
	MinLevel     string `json:"min_level,omitempty"`
}

type routeConfigSyncControlMessage struct {
	Type              string `json:"type"`
	ControllerBaseURL string `json:"controller_base_url"`
}

type logsControlResult struct {
	Type         string            `json:"type"`
	RequestID    string            `json:"request_id"`
	NodeID       string            `json:"node_id"`
	OK           bool              `json:"ok"`
	Source       string            `json:"source,omitempty"`
	FilePath     string            `json:"file_path,omitempty"`
	Lines        int               `json:"lines"`
	SinceMinutes int               `json:"since_minutes"`
	MinLevel     string            `json:"min_level,omitempty"`
	Content      string            `json:"content,omitempty"`
	Entries      []androidLogEntry `json:"entries,omitempty"`
	Error        string            `json:"error,omitempty"`
	Timestamp    string            `json:"timestamp"`
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
	headers, err := buildAuthHeaders(wsURL, nodeID, nodeSecret, http.MethodGet)
	if err != nil {
		return err
	}
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
	identity := mobileNodeIdentity{NodeID: nodeID, Secret: nodeSecret}
	ackCh := make(chan error, 1)
	readErrCh := make(chan error, 1)
	go func() {
		acked := false
		for {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				readErrCh <- err
				return
			}
			if !acked {
				var ack probeAckMessage
				if err := json.Unmarshal(raw, &ack); err == nil && strings.EqualFold(strings.TrimSpace(ack.Type), "ack") {
					acked = true
					ackCh <- nil
					continue
				}
			}
			processControlMessage(raw, stream, writeMu, identity)
		}
	}()
	if err := sendReport(stream, encoder, writeMu, nodeID); err != nil {
		return err
	}
	select {
	case err := <-ackCh:
		if err != nil {
			return err
		}
	case err := <-readErrCh:
		return err
	case <-time.After(10 * time.Second):
		return errors.New("probe report ack timeout")
	case <-cancel:
		return nil
	}
	setStatus("connected")

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
	ipv4, ipv6 := collectIPs()
	payload := reportPayload{
		Type:        "report",
		NodeID:      nodeID,
		Platform:    "android",
		OS:          "android",
		Arch:        runtime.GOARCH,
		IPv4:        ipv4,
		IPv6:        ipv6,
		System:      collectSystemStatus(&reportCPUSampler),
		Version:     currentVersion(),
		RelayStatus: snapshotMobileVRouteRelayReports(currentAndroidVPNConfigDir()),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
	if writeMu != nil {
		writeMu.Lock()
		defer writeMu.Unlock()
	}
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := encoder.Encode(payload)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}

func processControlMessage(raw json.RawMessage, stream net.Conn, writeMu *sync.Mutex, identity mobileNodeIdentity) {
	var envelope controlEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(envelope.Type)) {
	case "logs_get":
		var msg logsControlMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return
		}
		sendLogsControlResult(stream, writeMu, buildLogsControlResult(msg, identity.NodeID))
	case "route_config_sync":
		var msg routeConfigSyncControlMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return
		}
		go runMobileRouteConfigSyncControl(msg, identity)
	}
}

func runMobileRouteConfigSyncControl(msg routeConfigSyncControlMessage, identity mobileNodeIdentity) {
	controllerBaseURL := strings.TrimSpace(msg.ControllerBaseURL)
	if controllerBaseURL == "" {
		controllerBaseURL, _, _ = currentMobileVRouteControllerIdentity()
	}
	if controllerBaseURL == "" {
		androidLogStore.add("route", "warn", "android vroute config sync skipped: controller base url is empty")
		return
	}
	if _, err := refreshConfigFiles(controllerBaseURL, identity.NodeID, identity.Secret, currentAndroidVPNConfigDir()); err != nil {
		androidLogStore.add("route", "error", "android vroute config sync failed: "+err.Error())
	}
}

func buildLogsControlResult(msg logsControlMessage, nodeID string) logsControlResult {
	lines := normalizeAndroidLogLines(msg.Lines)
	sinceMinutes := normalizeAndroidLogSinceMinutes(msg.SinceMinutes)
	minLevel := strings.TrimSpace(msg.MinLevel)
	content, entries := androidLogStore.tail(lines, sinceMinutes, minLevel)
	return logsControlResult{
		Type:         "logs_result",
		RequestID:    strings.TrimSpace(msg.RequestID),
		NodeID:       strings.TrimSpace(nodeID),
		OK:           true,
		Source:       "android",
		FilePath:     "memory://android_app",
		Lines:        lines,
		SinceMinutes: sinceMinutes,
		MinLevel:     minLevel,
		Content:      content,
		Entries:      entries,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	}
}

func sendLogsControlResult(stream net.Conn, writeMu *sync.Mutex, result logsControlResult) {
	if err := writeReporterJSON(stream, writeMu, result); err != nil {
		log.Printf("logs result send failed: request_id=%s err=%v", strings.TrimSpace(result.RequestID), err)
	}
}
