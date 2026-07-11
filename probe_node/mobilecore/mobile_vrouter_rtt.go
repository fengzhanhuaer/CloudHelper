package mobilecore

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

const mobileVRouteRTTTimeout = 5 * time.Second

type mobileVRouteControlProbePayload struct {
	RequestID         string   `json:"request_id"`
	SourceNodeID      string   `json:"source_node_id,omitempty"`
	TargetNodeID      string   `json:"target_node_id,omitempty"`
	Path              []string `json:"path,omitempty"`
	CreatedAtUnixNano int64    `json:"created_at_unix_nano,omitempty"`
	LatencyMS         int64    `json:"latency_ms,omitempty"`
	PingBytes         int      `json:"ping_bytes,omitempty"`
	OK                bool     `json:"ok,omitempty"`
	Error             string   `json:"error,omitempty"`
	Responder         string   `json:"responder,omitempty"`
}

var mobileVRouteRTTState = struct {
	mu      sync.Mutex
	pending map[string]chan mobileVRouteControlProbePayload
}{pending: make(map[string]chan mobileVRouteControlProbePayload)}

func VRoutePathRTT(targetNodeID string) string {
	result := runMobileVRoutePathRTT(strings.TrimSpace(targetNodeID))
	return marshalRouteJSON(result)
}

func runMobileVRoutePathRTT(targetNodeID string) map[string]any {
	configDir := currentAndroidVPNConfigDir()
	config, err := loadMobileVRouteConfig(configDir)
	if err != nil {
		return mobileVRouteRTTErrorResult(targetNodeID, nil, err)
	}
	localNodeID := normalizeMobileRouteNodeID(config.LocalNodeID)
	targetNodeID = normalizeMobileRouteNodeID(targetNodeID)
	if localNodeID == "" || targetNodeID == "" || targetNodeID == localNodeID {
		return mobileVRouteRTTErrorResult(targetNodeID, nil, errors.New("mobile vroute rtt target is invalid"))
	}
	path, err := mobileVRouteShortestPath(config, localNodeID, targetNodeID)
	if err != nil {
		return mobileVRouteRTTErrorResult(targetNodeID, nil, err)
	}
	plan, err := buildMobileVRouteForwardPlan(configDir, mobileVRouteProbeExitRouteID(targetNodeID))
	if err != nil {
		return mobileVRouteRTTErrorResult(targetNodeID, path, err)
	}
	carrier, err := ensureMobileVRouteCarrier(plan, nil)
	if err != nil {
		return mobileVRouteRTTErrorResult(targetNodeID, path, err)
	}
	requestID := newAndroidRouteFlowID("vroute_rtt", targetNodeID)
	startedAt := time.Now()
	request := mobileVRouteControlProbePayload{
		RequestID:         requestID,
		SourceNodeID:      localNodeID,
		TargetNodeID:      targetNodeID,
		Path:              path,
		CreatedAtUnixNano: startedAt.UnixNano(),
		PingBytes:         64,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return mobileVRouteRTTErrorResult(targetNodeID, path, err)
	}
	control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: path})
	if err != nil {
		return mobileVRouteRTTErrorResult(targetNodeID, path, err)
	}
	mainType := mobileVRouteFrameMainTypePathRTT
	subType := mobileVRoutePathRTTSubTypeQuery
	if len(path) == 2 {
		mainType = mobileVRouteFrameMainTypePingPong
		subType = mobileVRoutePingPongSubTypePing
	}
	waiter := registerMobileVRouteRTTResponse(requestID)
	defer unregisterMobileVRouteRTTResponse(requestID)
	if err := carrier.enqueueFrame(mobileVRouteFrame{MainType: mainType, SubType: subType, Control: control, Data: payload}); err != nil {
		return mobileVRouteRTTErrorResult(targetNodeID, path, err)
	}
	response, err := waitMobileVRouteRTTResponse(waiter, mobileVRouteRTTTimeout)
	if err != nil {
		return mobileVRouteRTTErrorResult(targetNodeID, path, err)
	}
	latencyMS := mobileVRouteRTTMilliseconds(time.Since(startedAt))
	return map[string]any{
		"ok":             response.OK,
		"source_node_id": localNodeID,
		"target_node_id": targetNodeID,
		"path":           path,
		"latency_ms":     latencyMS,
		"responder":      normalizeMobileRouteNodeID(response.Responder),
		"error":          strings.TrimSpace(response.Error),
		"updated_at":     time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func mobileVRouteRTTMilliseconds(elapsed time.Duration) int64 {
	latencyMS := elapsed.Milliseconds()
	if latencyMS < 1 {
		return 1
	}
	return latencyMS
}

func mobileVRouteRTTErrorResult(targetNodeID string, path []string, err error) map[string]any {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return map[string]any{
		"ok":             false,
		"target_node_id": normalizeMobileRouteNodeID(targetNodeID),
		"path":           mobileVRouteCleanPath(path),
		"error":          message,
		"updated_at":     time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func registerMobileVRouteRTTResponse(requestID string) chan mobileVRouteControlProbePayload {
	ch := make(chan mobileVRouteControlProbePayload, 1)
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ch
	}
	mobileVRouteRTTState.mu.Lock()
	mobileVRouteRTTState.pending[requestID] = ch
	mobileVRouteRTTState.mu.Unlock()
	return ch
}

func unregisterMobileVRouteRTTResponse(requestID string) {
	mobileVRouteRTTState.mu.Lock()
	delete(mobileVRouteRTTState.pending, strings.TrimSpace(requestID))
	mobileVRouteRTTState.mu.Unlock()
}

func waitMobileVRouteRTTResponse(ch chan mobileVRouteControlProbePayload, timeout time.Duration) (mobileVRouteControlProbePayload, error) {
	if ch == nil {
		return mobileVRouteControlProbePayload{}, errors.New("mobile vroute rtt waiter is nil")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-ch:
		return response, nil
	case <-timer.C:
		return mobileVRouteControlProbePayload{}, errors.New("mobile vroute rtt response timeout")
	}
}

func completeMobileVRouteRTTResponse(frame mobileVRouteFrame) error {
	response := mobileVRouteControlProbePayload{}
	if err := json.Unmarshal(frame.Data, &response); err != nil {
		return err
	}
	requestID := strings.TrimSpace(response.RequestID)
	if requestID == "" {
		return errors.New("mobile vroute rtt response request id is empty")
	}
	mobileVRouteRTTState.mu.Lock()
	ch := mobileVRouteRTTState.pending[requestID]
	mobileVRouteRTTState.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case ch <- response:
	default:
	}
	return nil
}

func (c *mobileVRouteCarrier) respondToRTTFrame(frame mobileVRouteFrame, path []string, responseMainType uint16, responseSubType uint16) error {
	if c == nil {
		return errors.New("mobile vroute rtt carrier is nil")
	}
	request := mobileVRouteControlProbePayload{}
	if err := json.Unmarshal(frame.Data, &request); err != nil {
		return err
	}
	if strings.TrimSpace(request.RequestID) == "" {
		return errors.New("mobile vroute rtt request id is empty")
	}
	localNodeID := normalizeMobileRouteNodeID(c.plan.LocalNode)
	if localNodeID == "" {
		return errors.New("mobile vroute rtt local node id is empty")
	}
	requestPath := mobileVRouteCleanPath(request.Path)
	if len(requestPath) == 0 {
		requestPath = mobileVRouteCleanPath(path)
	}
	if err := validateMobileVRoutePath(requestPath); err != nil {
		return err
	}
	if requestPath[len(requestPath)-1] != localNodeID {
		return errors.New("mobile vroute rtt request does not target local node")
	}
	response := request
	response.Path = requestPath
	response.OK = true
	response.Error = ""
	response.Responder = localNodeID
	response.LatencyMS = 0
	if responseMainType == mobileVRouteFrameMainTypePingPong {
		response.CreatedAtUnixNano = time.Now().UnixNano()
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	reversePath := reverseMobileVRoutePath(requestPath)
	control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: reversePath})
	if err != nil {
		return err
	}
	logAndroidVPNDiagnostic(
		"rtt_response_"+strconv.Itoa(int(responseMainType)),
		"realtime",
		"vroute rtt response queued: request_id="+response.RequestID+" type="+strconv.Itoa(int(responseMainType))+" path="+strings.Join(reversePath, ">"),
		2*time.Second,
	)
	return c.enqueueFrame(mobileVRouteFrame{
		MainType: responseMainType,
		SubType:  responseSubType,
		Control:  control,
		Data:     payload,
	})
}
