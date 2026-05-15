package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type probeControllerRPCRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	URL       string `json:"url,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Offset    int64  `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type probeControllerRPCResponse struct {
	Type        string `json:"type"`
	RequestID   string `json:"request_id"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	StatusCode  int    `json:"status_code,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	TotalSize   int64  `json:"total_size,omitempty"`
	EOF         bool   `json:"eof,omitempty"`
	PayloadJSON string `json:"payload_json,omitempty"`
	DataBase64  string `json:"data_base64,omitempty"`
}

var probeReporterRPCState = struct {
	mu      sync.Mutex
	stream  net.Conn
	encoder *json.Encoder
	writeMu *sync.Mutex
	pending map[string]chan probeControllerRPCResponse
}{
	pending: map[string]chan probeControllerRPCResponse{},
}

func attachProbeReporterRPCChannel(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	probeReporterRPCState.mu.Lock()
	probeReporterRPCState.stream = stream
	probeReporterRPCState.encoder = encoder
	probeReporterRPCState.writeMu = writeMu
	probeReporterRPCState.mu.Unlock()
}

func detachProbeReporterRPCChannel() {
	probeReporterRPCState.mu.Lock()
	pending := make([]chan probeControllerRPCResponse, 0, len(probeReporterRPCState.pending))
	for _, ch := range probeReporterRPCState.pending {
		pending = append(pending, ch)
	}
	probeReporterRPCState.pending = map[string]chan probeControllerRPCResponse{}
	probeReporterRPCState.stream = nil
	probeReporterRPCState.encoder = nil
	probeReporterRPCState.writeMu = nil
	probeReporterRPCState.mu.Unlock()

	for _, ch := range pending {
		select {
		case ch <- probeControllerRPCResponse{
			Type:  "controller_rpc_response",
			OK:    false,
			Error: "reporter channel disconnected",
		}:
		default:
		}
		close(ch)
	}
}

func handleProbeReporterRPCResponseRaw(raw json.RawMessage) bool {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(envelope.Type), "controller_rpc_response") {
		return false
	}
	var resp probeControllerRPCResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return true
	}
	reqID := strings.TrimSpace(resp.RequestID)
	if reqID == "" {
		return true
	}

	probeReporterRPCState.mu.Lock()
	ch, ok := probeReporterRPCState.pending[reqID]
	if ok {
		delete(probeReporterRPCState.pending, reqID)
	}
	probeReporterRPCState.mu.Unlock()
	if !ok {
		return true
	}
	select {
	case ch <- resp:
	default:
	}
	close(ch)
	return true
}

func callProbeControllerRPC(ctx context.Context, req probeControllerRPCRequest) (probeControllerRPCResponse, error) {
	reqID := strings.TrimSpace(req.RequestID)
	if reqID == "" {
		reqID = "rpc-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	req.Type = "controller_rpc_request"
	req.RequestID = reqID
	req.Action = strings.TrimSpace(strings.ToLower(req.Action))

	respCh := make(chan probeControllerRPCResponse, 1)
	probeReporterRPCState.mu.Lock()
	stream := probeReporterRPCState.stream
	encoder := probeReporterRPCState.encoder
	writeMu := probeReporterRPCState.writeMu
	if stream == nil || encoder == nil || writeMu == nil {
		probeReporterRPCState.mu.Unlock()
		return probeControllerRPCResponse{}, errors.New("reporter rpc channel is not connected")
	}
	probeReporterRPCState.pending[reqID] = respCh
	probeReporterRPCState.mu.Unlock()

	sendErr := writeProbeStreamJSON(stream, encoder, writeMu, req)
	if sendErr != nil {
		probeReporterRPCState.mu.Lock()
		delete(probeReporterRPCState.pending, reqID)
		probeReporterRPCState.mu.Unlock()
		close(respCh)
		return probeControllerRPCResponse{}, sendErr
	}

	select {
	case <-ctx.Done():
		probeReporterRPCState.mu.Lock()
		delete(probeReporterRPCState.pending, reqID)
		probeReporterRPCState.mu.Unlock()
		return probeControllerRPCResponse{}, ctx.Err()
	case resp := <-respCh:
		if !resp.OK {
			return resp, errors.New(strings.TrimSpace(resp.Error))
		}
		return resp, nil
	}
}

func probeControllerRPCPayloadJSON(resp probeControllerRPCResponse) ([]byte, error) {
	payload := strings.TrimSpace(resp.PayloadJSON)
	if payload == "" {
		return nil, errors.New("empty rpc payload json")
	}
	return []byte(payload), nil
}

func probeControllerRPCDecodeChunk(resp probeControllerRPCResponse) ([]byte, error) {
	value := strings.TrimSpace(resp.DataBase64)
	if value == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}
