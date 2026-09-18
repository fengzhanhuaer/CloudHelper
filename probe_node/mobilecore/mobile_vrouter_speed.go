package mobilecore

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	mobileVRouteSpeedTestBytes        = 16 * 1024 * 1024
	mobileVRouteSpeedTestMaxBytes     = 128 * 1024 * 1024
	mobileVRouteSpeedTestDuration     = 8 * time.Second
	mobileVRouteSpeedTestMaxDuration  = 10 * time.Second
	mobileVRouteSpeedTestChunkBytes   = 1024
	mobileVRouteSpeedReceiveCompleted = 2 * time.Minute
)

type mobileVRouteSpeedResultPayload struct {
	RequestID         string   `json:"request_id"`
	Direction         string   `json:"direction,omitempty"`
	SourceNodeID      string   `json:"source_node_id,omitempty"`
	TargetNodeID      string   `json:"target_node_id,omitempty"`
	ResultNodeID      string   `json:"result_node_id,omitempty"`
	Path              []string `json:"path,omitempty"`
	MaxBytes          int64    `json:"max_bytes,omitempty"`
	MaxDurationMS     int64    `json:"max_duration_ms,omitempty"`
	CreatedAtUnixNano int64    `json:"created_at_unix_nano,omitempty"`
	Bytes             int64    `json:"bytes,omitempty"`
	Frames            int64    `json:"frames,omitempty"`
	DurationMS        int64    `json:"duration_ms,omitempty"`
	Mbps              float64  `json:"mbps,omitempty"`
	OK                bool     `json:"ok,omitempty"`
	Error             string   `json:"error,omitempty"`
	Responder         string   `json:"responder,omitempty"`
}

type mobileVRouteSpeedReceiveSession struct {
	Message   mobileVRouteSpeedResultPayload
	LocalNode string
	StartedAt time.Time
	LastAt    time.Time
	Bytes     int64
	Frames    int64
}

var mobileVRouteSpeedState = struct {
	mu        sync.Mutex
	pending   map[string]chan mobileVRouteSpeedResultPayload
	sessions  map[string]*mobileVRouteSpeedReceiveSession
	completed map[string]time.Time
}{
	pending:   make(map[string]chan mobileVRouteSpeedResultPayload),
	sessions:  make(map[string]*mobileVRouteSpeedReceiveSession),
	completed: make(map[string]time.Time),
}

func VRouteSpeedTest(targetNodeID string) string {
	return marshalRouteJSON(runMobileVRouteSpeedTest(strings.TrimSpace(targetNodeID)))
}

func runMobileVRouteSpeedTest(targetNodeID string) map[string]any {
	config, err := loadMobileVRouteConfig(currentAndroidVPNConfigDir())
	if err != nil {
		return mobileVRouteSpeedErrorResult(targetNodeID, nil, err)
	}
	localNodeID := normalizeMobileRouteNodeID(config.LocalNodeID)
	targetNodeID = normalizeMobileRouteNodeID(targetNodeID)
	if localNodeID == "" || targetNodeID == "" || localNodeID == targetNodeID {
		return mobileVRouteSpeedErrorResult(targetNodeID, nil, errors.New("mobile vroute speed target is invalid"))
	}
	path, err := mobileVRouteShortestPath(config, localNodeID, targetNodeID)
	if err != nil {
		return mobileVRouteSpeedErrorResult(targetNodeID, nil, err)
	}
	// Match the probe-node route diagnostic: measure one download direction,
	// with the target node sending data back to the selected local node.
	download, downloadErr := runMobileVRouteReverseSpeed(config, path)
	result := map[string]any{
		"ok":             downloadErr == nil && download.OK,
		"source_node_id": localNodeID,
		"target_node_id": targetNodeID,
		"path":           path,
		"download":       download,
		"updated_at":     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if downloadErr != nil {
		result["error"] = downloadErr.Error()
	}
	return result
}

func mobileVRouteSpeedErrorResult(targetNodeID string, path []string, err error) map[string]any {
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

func runMobileVRouteOneWaySpeed(config mobileVRouteConfig, path []string, direction string) (mobileVRouteSpeedResultPayload, error) {
	localNodeID := normalizeMobileRouteNodeID(config.LocalNodeID)
	requestID := newAndroidRouteFlowID("vroute_speed_"+direction, strings.Join(path, ">"))
	message := mobileVRouteSpeedResultPayload{
		RequestID: requestID, Direction: direction, SourceNodeID: localNodeID,
		TargetNodeID: path[len(path)-1], ResultNodeID: localNodeID, Path: append([]string(nil), path...),
		MaxBytes: mobileVRouteSpeedTestBytes, MaxDurationMS: mobileVRouteSpeedTestDuration.Milliseconds(),
		CreatedAtUnixNano: time.Now().UnixNano(),
	}
	waiter := registerMobileVRouteSpeedResponse(requestID)
	defer unregisterMobileVRouteSpeedResponse(requestID)
	if err := runMobileVRouteSpeedSender(config, path, message, mobileVRouteSpeedTestDuration); err != nil {
		return mobileVRouteSpeedResultPayload{Direction: direction, Error: err.Error()}, err
	}
	return waitMobileVRouteSpeedResponse(waiter, mobileVRouteSpeedTestDuration+10*time.Second)
}

func runMobileVRouteReverseSpeed(config mobileVRouteConfig, path []string) (mobileVRouteSpeedResultPayload, error) {
	localNodeID := normalizeMobileRouteNodeID(config.LocalNodeID)
	requestID := newAndroidRouteFlowID("vroute_speed_down", strings.Join(path, ">"))
	message := mobileVRouteSpeedResultPayload{
		RequestID: requestID, Direction: "down", SourceNodeID: localNodeID,
		TargetNodeID: path[len(path)-1], ResultNodeID: localNodeID, Path: append([]string(nil), path...),
		MaxBytes: mobileVRouteSpeedTestBytes, MaxDurationMS: mobileVRouteSpeedTestDuration.Milliseconds(),
		CreatedAtUnixNano: time.Now().UnixNano(),
	}
	waiter := registerMobileVRouteSpeedResponse(requestID)
	defer unregisterMobileVRouteSpeedResponse(requestID)
	if err := forwardMobileVRouteSpeedMessage(config, mobileVRouteSpeedSubTypeSend, message, path, time.Now().Add(2*time.Second)); err != nil {
		return mobileVRouteSpeedResultPayload{Direction: "down", Error: err.Error()}, err
	}
	return waitMobileVRouteSpeedResponse(waiter, mobileVRouteSpeedTestDuration+10*time.Second)
}

func runMobileVRouteSpeedSender(config mobileVRouteConfig, path []string, message mobileVRouteSpeedResultPayload, duration time.Duration) error {
	path = mobileVRouteCleanPath(path)
	localNodeID := normalizeMobileRouteNodeID(config.LocalNodeID)
	if len(path) < 2 || path[0] != localNodeID {
		return errors.New("mobile vroute speed sender path must start at local node")
	}
	plan, err := buildMobileVRouteAdjacentPlan(config, path, localNodeID, path[1])
	if err != nil {
		return err
	}
	message.Path = path
	message.MaxBytes = normalizeMobileVRouteSpeedBytes(message.MaxBytes)
	duration = normalizeMobileVRouteSpeedDuration(duration.Milliseconds())
	message.MaxDurationMS = duration.Milliseconds()
	control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: path})
	if err != nil {
		return err
	}
	startPayload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	startFrame := mobileVRouteFrame{MainType: mobileVRouteFrameMainTypeSpeed, SubType: mobileVRouteSpeedSubTypeStart, Control: control, Data: startPayload}
	carrier, err := selectMobileVRouteCarrier(plan, startFrame, nil)
	if err != nil {
		return err
	}
	if err := carrier.enqueueFrameUntil(startFrame, time.Now().Add(2*time.Second)); err != nil {
		return err
	}
	deadline := time.Now().Add(duration)
	var bytesSent, framesSent int64
	for bytesSent < message.MaxBytes && time.Now().Before(deadline) {
		size := int64(mobileVRouteSpeedTestChunkBytes)
		if remain := message.MaxBytes - bytesSent; remain < size {
			size = remain
		}
		payload := buildMobileVRouteSpeedChunk(message.RequestID, int(size))
		frame := mobileVRouteFrame{MainType: mobileVRouteFrameMainTypeSpeed, SubType: mobileVRouteSpeedSubTypeChunk, Control: control, Data: payload}
		if err := carrier.enqueueFrameUntil(frame, deadline); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}
			return err
		}
		bytesSent += int64(len(payload))
		framesSent++
	}
	message.Bytes = bytesSent
	message.Frames = framesSent
	finishPayload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return carrier.enqueueFrameUntil(mobileVRouteFrame{MainType: mobileVRouteFrameMainTypeSpeed, SubType: mobileVRouteSpeedSubTypeFinish, Control: control, Data: finishPayload}, time.Now().Add(2*time.Second))
}

func (c *mobileVRouteCarrier) enqueueFrameUntil(frame mobileVRouteFrame, deadline time.Time) error {
	queue, _ := c.txQueueForFrame(frame)
	if queue == nil || c.done == nil {
		return io.ErrClosedPipe
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		return os.ErrDeadlineExceeded
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case queue <- frame:
		return nil
	case <-c.done:
		return io.ErrClosedPipe
	case <-timer.C:
		return os.ErrDeadlineExceeded
	}
}

func handleMobileVRouteSpeedFrame(c *mobileVRouteCarrier, frame mobileVRouteFrame, path []string) error {
	if c == nil {
		return io.ErrClosedPipe
	}
	if frame.SubType == mobileVRouteSpeedSubTypeChunk {
		requestID, ok := parseMobileVRouteSpeedChunk(frame.Data)
		if !ok {
			return errors.New("invalid mobile vroute speed chunk")
		}
		recordMobileVRouteSpeedChunk(requestID, int64(len(frame.Data)))
		return nil
	}
	message := mobileVRouteSpeedResultPayload{}
	if err := json.Unmarshal(frame.Data, &message); err != nil {
		return err
	}
	message.Path = mobileVRouteCleanPath(message.Path)
	if len(message.Path) == 0 {
		message.Path = mobileVRouteCleanPath(path)
	}
	localNodeID := normalizeMobileRouteNodeID(c.plan.LocalNode)
	if message.RequestID == "" || localNodeID == "" || len(message.Path) < 2 {
		return errors.New("mobile vroute speed frame is incomplete")
	}
	switch frame.SubType {
	case mobileVRouteSpeedSubTypeStart:
		startMobileVRouteSpeedReceive(message, localNodeID)
	case mobileVRouteSpeedSubTypeFinish:
		result, ok := finishMobileVRouteSpeedReceive(message, localNodeID)
		if !ok {
			return nil
		}
		if normalizeMobileRouteNodeID(result.ResultNodeID) == localNodeID {
			completeMobileVRouteSpeedResponse(result)
			return nil
		}
		result.Path = reverseMobileVRoutePath(message.Path)
		return forwardMobileVRouteSpeedMessage(c.plan.Config, mobileVRouteSpeedSubTypeResult, result, result.Path, time.Now().Add(2*time.Second))
	case mobileVRouteSpeedSubTypeResult:
		completeMobileVRouteSpeedResponse(message)
	case mobileVRouteSpeedSubTypeSend:
		go func() {
			reversePath := reverseMobileVRoutePath(message.Path)
			if err := runMobileVRouteSpeedSender(c.plan.Config, reversePath, message, normalizeMobileVRouteSpeedDuration(message.MaxDurationMS)); err != nil {
				message.OK = false
				message.Error = err.Error()
				message.Responder = localNodeID
				message.Path = reversePath
				_ = forwardMobileVRouteSpeedMessage(c.plan.Config, mobileVRouteSpeedSubTypeResult, message, reversePath, time.Now().Add(2*time.Second))
			}
		}()
	default:
		return fmt.Errorf("unsupported mobile vroute speed subtype=%d", frame.SubType)
	}
	return nil
}

func forwardMobileVRouteSpeedMessage(config mobileVRouteConfig, subType uint16, message mobileVRouteSpeedResultPayload, path []string, deadline time.Time) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: path})
	if err != nil {
		return err
	}
	localNodeID := normalizeMobileRouteNodeID(config.LocalNodeID)
	path = mobileVRouteCleanPath(path)
	if len(path) < 2 || path[0] != localNodeID {
		return errors.New("mobile vroute speed forward path must start at local node")
	}
	plan, err := buildMobileVRouteAdjacentPlan(config, path, localNodeID, path[1])
	if err != nil {
		return err
	}
	frame := mobileVRouteFrame{MainType: mobileVRouteFrameMainTypeSpeed, SubType: subType, Control: control, Data: payload}
	carrier, err := selectMobileVRouteCarrier(plan, frame, nil)
	if err != nil {
		return err
	}
	return carrier.enqueueFrameUntil(frame, deadline)
}

func startMobileVRouteSpeedReceive(message mobileVRouteSpeedResultPayload, localNodeID string) {
	now := time.Now()
	session := &mobileVRouteSpeedReceiveSession{Message: message, LocalNode: localNodeID, LastAt: now}
	mobileVRouteSpeedState.mu.Lock()
	cleanupMobileVRouteSpeedCompletedLocked(now)
	delete(mobileVRouteSpeedState.completed, message.RequestID)
	mobileVRouteSpeedState.sessions[message.RequestID] = session
	mobileVRouteSpeedState.mu.Unlock()
}

func recordMobileVRouteSpeedChunk(requestID string, size int64) {
	now := time.Now()
	mobileVRouteSpeedState.mu.Lock()
	session := mobileVRouteSpeedState.sessions[strings.TrimSpace(requestID)]
	if session != nil {
		if session.Frames == 0 {
			session.StartedAt = now
		}
		session.LastAt = now
		session.Bytes += size
		session.Frames++
	}
	mobileVRouteSpeedState.mu.Unlock()
}

func finishMobileVRouteSpeedReceive(fallback mobileVRouteSpeedResultPayload, localNodeID string) (mobileVRouteSpeedResultPayload, bool) {
	now := time.Now()
	requestID := strings.TrimSpace(fallback.RequestID)
	mobileVRouteSpeedState.mu.Lock()
	session := mobileVRouteSpeedState.sessions[requestID]
	delete(mobileVRouteSpeedState.sessions, requestID)
	mobileVRouteSpeedState.completed[requestID] = now
	cleanupMobileVRouteSpeedCompletedLocked(now)
	mobileVRouteSpeedState.mu.Unlock()
	if session == nil {
		return mobileVRouteSpeedResultPayload{}, false
	}
	result := fallback
	result.OK = true
	result.Error = ""
	result.Responder = normalizeMobileRouteNodeID(localNodeID)
	result.Bytes = session.Bytes
	result.Frames = session.Frames
	if !session.StartedAt.IsZero() && !session.LastAt.IsZero() && session.Frames > 0 {
		result.DurationMS = session.LastAt.Sub(session.StartedAt).Milliseconds()
		if result.DurationMS < 1 {
			result.DurationMS = 1
		}
	}
	result.Mbps = mobileVRouteSpeedMbps(result.Bytes, result.DurationMS)
	return result, true
}

func cleanupMobileVRouteSpeedCompletedLocked(now time.Time) {
	for requestID, completedAt := range mobileVRouteSpeedState.completed {
		if completedAt.IsZero() || now.Sub(completedAt) > mobileVRouteSpeedReceiveCompleted {
			delete(mobileVRouteSpeedState.completed, requestID)
		}
	}
}

func registerMobileVRouteSpeedResponse(requestID string) chan mobileVRouteSpeedResultPayload {
	ch := make(chan mobileVRouteSpeedResultPayload, 1)
	mobileVRouteSpeedState.mu.Lock()
	mobileVRouteSpeedState.pending[strings.TrimSpace(requestID)] = ch
	mobileVRouteSpeedState.mu.Unlock()
	return ch
}

func unregisterMobileVRouteSpeedResponse(requestID string) {
	mobileVRouteSpeedState.mu.Lock()
	delete(mobileVRouteSpeedState.pending, strings.TrimSpace(requestID))
	mobileVRouteSpeedState.mu.Unlock()
}

func completeMobileVRouteSpeedResponse(message mobileVRouteSpeedResultPayload) {
	mobileVRouteSpeedState.mu.Lock()
	ch := mobileVRouteSpeedState.pending[strings.TrimSpace(message.RequestID)]
	mobileVRouteSpeedState.mu.Unlock()
	if ch != nil {
		select {
		case ch <- message:
		default:
		}
	}
}

func waitMobileVRouteSpeedResponse(ch chan mobileVRouteSpeedResultPayload, timeout time.Duration) (mobileVRouteSpeedResultPayload, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-ch:
		if !response.OK {
			return response, errors.New(firstNonEmptyString(response.Error, "mobile vroute speed test failed"))
		}
		return response, nil
	case <-timer.C:
		return mobileVRouteSpeedResultPayload{}, errors.New("mobile vroute speed response timeout")
	}
}

func buildMobileVRouteSpeedChunk(requestID string, size int) []byte {
	requestID = strings.TrimSpace(requestID)
	headerSize := 6 + len(requestID)
	if size < headerSize {
		size = headerSize
	}
	payload := make([]byte, size)
	copy(payload[:4], []byte("VRS1"))
	binary.BigEndian.PutUint16(payload[4:6], uint16(len(requestID)))
	copy(payload[6:], requestID)
	return payload
}

func parseMobileVRouteSpeedChunk(payload []byte) (string, bool) {
	if len(payload) < 6 || string(payload[:4]) != "VRS1" {
		return "", false
	}
	size := int(binary.BigEndian.Uint16(payload[4:6]))
	if size <= 0 || 6+size > len(payload) {
		return "", false
	}
	requestID := strings.TrimSpace(string(payload[6 : 6+size]))
	return requestID, requestID != ""
}

func normalizeMobileVRouteSpeedBytes(value int64) int64 {
	if value <= 0 || value > mobileVRouteSpeedTestMaxBytes {
		return mobileVRouteSpeedTestMaxBytes
	}
	return value
}

func normalizeMobileVRouteSpeedDuration(milliseconds int64) time.Duration {
	if milliseconds <= 0 || milliseconds > mobileVRouteSpeedTestMaxDuration.Milliseconds() {
		return mobileVRouteSpeedTestMaxDuration
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func mobileVRouteSpeedMbps(bytes int64, durationMS int64) float64 {
	if bytes <= 0 || durationMS <= 0 {
		return 0
	}
	return float64(bytes*8) / (float64(durationMS) / 1000) / 1000 / 1000
}
