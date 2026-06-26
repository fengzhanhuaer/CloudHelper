package mobilecore

// This file owns the Android <-> controller reporter session.
// It uses yamux + JSON on top of the controller websocket.
// It does not participate in the probe-to-probe custom frame protocol;
// that path lives in mobilecore_chain_runtime.go and chain_frame_session.go.

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

type reportPayload struct {
	Type      string       `json:"type"`
	NodeID    string       `json:"node_id"`
	Platform  string       `json:"platform,omitempty"`
	OS        string       `json:"os,omitempty"`
	Arch      string       `json:"arch,omitempty"`
	IPv4      []string     `json:"ipv4,omitempty"`
	IPv6      []string     `json:"ipv6,omitempty"`
	System    systemStatus `json:"system"`
	Version   string       `json:"version,omitempty"`
	Timestamp string       `json:"timestamp"`
}

type probeAckMessage struct {
	Type      string `json:"type"`
	Message   string `json:"message,omitempty"`
	ServerUTC string `json:"server_utc,omitempty"`
}

type controlEnvelope struct {
	Type string `json:"type"`
}

type peerStatusControlMessage struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Scope     string `json:"scope,omitempty"`
}

type peerStatusControlResult struct {
	Type        string                         `json:"type"`
	RequestID   string                         `json:"request_id,omitempty"`
	NodeID      string                         `json:"node_id,omitempty"`
	OK          bool                           `json:"ok"`
	Scope       string                         `json:"scope,omitempty"`
	Status      map[string]any                 `json:"status,omitempty"`
	Connections androidProxyConnectionSnapshot `json:"connections,omitempty"`
	DNS         map[string]any                 `json:"dns,omitempty"`
	Logs        []androidLogEntry              `json:"logs,omitempty"`
	Chain       map[string]any                 `json:"chain,omitempty"`
	FetchedAt   string                         `json:"fetched_at,omitempty"`
	Error       string                         `json:"error,omitempty"`
	Timestamp   string                         `json:"timestamp,omitempty"`
}

type logsControlMessage struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	Lines        int    `json:"lines"`
	SinceMinutes int    `json:"since_minutes"`
	MinLevel     string `json:"min_level,omitempty"`
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
			var envelope controlEnvelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(envelope.Type)) {
			case "logs_get":
				var msg logsControlMessage
				if err := json.Unmarshal(raw, &msg); err != nil {
					continue
				}
				sendLogsControlResult(stream, writeMu, buildLogsControlResult(msg, nodeID))
			case "peer_status_get":
				var msg peerStatusControlMessage
				if err := json.Unmarshal(raw, &msg); err != nil {
					continue
				}
				sendPeerStatusControlResult(stream, writeMu, buildPeerStatusControlResult(msg, mobileNodeIdentity{NodeID: nodeID, Secret: nodeSecret}))
			case "chain_link_control":
				var msg chainLinkControlMessage
				if err := json.Unmarshal(raw, &msg); err != nil {
					continue
				}
				runMobileChainLinkControl(msg, mobileNodeIdentity{NodeID: nodeID, Secret: nodeSecret}, stream, writeMu)
			}
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
		Type:      "report",
		NodeID:    nodeID,
		Platform:  "android",
		OS:        "android",
		Arch:      runtime.GOARCH,
		IPv4:      ipv4,
		IPv6:      ipv6,
		System:    collectSystemStatus(&reportCPUSampler),
		Version:   currentVersion(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
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
	case "peer_status_get":
		var msg peerStatusControlMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return
		}
		sendPeerStatusControlResult(stream, writeMu, buildPeerStatusControlResult(msg, identity))
	case "chain_link_control":
		var msg chainLinkControlMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return
		}
		runMobileChainLinkControl(msg, identity, stream, writeMu)
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

func buildPeerStatusControlResult(msg peerStatusControlMessage, identity mobileNodeIdentity) peerStatusControlResult {
	requestID := strings.TrimSpace(msg.RequestID)
	scope := strings.TrimSpace(msg.Scope)
	if scope == "" {
		scope = "android"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	result := peerStatusControlResult{
		Type:        "peer_status_result",
		RequestID:   requestID,
		NodeID:      strings.TrimSpace(identity.NodeID),
		OK:          true,
		Scope:       scope,
		Status:      map[string]any{},
		Connections: globalAndroidProxyConnectionState.snapshot(),
		DNS:         snapshotAndroidVPNDNSStatus(),
		Logs:        androidLogStore.snapshot(120),
		Chain:       buildMobilePeerChainSnapshot(),
		FetchedAt:   now,
		Timestamp:   now,
	}
	if result.Status == nil {
		result.Status = map[string]any{}
	}
	return result
}

func buildMobilePeerChainSnapshot() map[string]any {
	snapshot := map[string]any{
		"runtimes": map[string]any{},
	}
	mobileChainRuntimeState.mu.Lock()
	runtimes := make([]*mobileChainRuntime, 0, len(mobileChainRuntimeState.runtimes))
	for _, rt := range mobileChainRuntimeState.runtimes {
		if rt != nil {
			runtimes = append(runtimes, rt)
		}
	}
	mobileChainRuntimeState.mu.Unlock()
	items := make([]map[string]any, 0, len(runtimes))
	for _, rt := range runtimes {
		item := map[string]any{
			"chain_id":        strings.TrimSpace(rt.cfg.ChainID),
			"role":            strings.TrimSpace(rt.cfg.Role),
			"listen_addr":     strings.TrimSpace(rt.relayListenAddr),
			"link_layer":      strings.TrimSpace(rt.cfg.LinkLayer),
			"next_link_layer": strings.TrimSpace(rt.cfg.NextLinkLayer),
			"next_host":       strings.TrimSpace(rt.cfg.NextHost),
			"next_port":       rt.cfg.NextPort,
			"prev_host":       strings.TrimSpace(rt.cfg.PrevHost),
			"prev_port":       rt.cfg.PrevPort,
			"downstream":      len(rt.downstreamSessions),
			"upstream":        len(rt.upstreamSessions),
			"updated_at":      time.Now().UTC().Format(time.RFC3339),
		}
		items = append(items, item)
	}
	snapshot["runtimes"] = items
	return snapshot
}

func sendPeerStatusControlResult(stream net.Conn, writeMu *sync.Mutex, result peerStatusControlResult) {
	if err := writeReporterJSON(stream, writeMu, result); err != nil {
		log.Printf("peer status result send failed: request_id=%s err=%v", strings.TrimSpace(result.RequestID), err)
	}
}
