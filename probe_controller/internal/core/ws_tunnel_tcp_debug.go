package core

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const controllerTCPDebugMaxFailures = 128

type tcpDebugConnectionItem struct {
	ID          string `json:"id"`
	Target      string `json:"target,omitempty"`
	RouteTarget string `json:"route_target,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	Group       string `json:"group,omitempty"`
	Direct      bool   `json:"direct"`
	Transport   string `json:"transport,omitempty"`
	OpenedAt    string `json:"opened_at,omitempty"`
	LastActive  string `json:"last_active,omitempty"`
	AgeMS       int64  `json:"age_ms"`
	IdleMS      int64  `json:"idle_ms"`
	BytesUp     int64  `json:"bytes_up,omitempty"`
	BytesDown   int64  `json:"bytes_down,omitempty"`
}

type tcpDebugFailureItem struct {
	Kind        string `json:"kind"`
	Reason      string `json:"reason,omitempty"`
	Target      string `json:"target,omitempty"`
	RouteTarget string `json:"route_target,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	Group       string `json:"group,omitempty"`
	Direct      bool   `json:"direct"`
	Transport   string `json:"transport,omitempty"`
	Error       string `json:"error,omitempty"`
	LastSeen    string `json:"last_seen,omitempty"`
}

type tcpDebugPayload struct {
	Kind         string                   `json:"kind"`
	Scope        string                   `json:"scope"`
	NodeID       string                   `json:"node_id,omitempty"`
	ActiveCount  int                      `json:"active_count"`
	Active       []tcpDebugConnectionItem `json:"active"`
	FailureCount int                      `json:"failure_count"`
	Failures     []tcpDebugFailureItem    `json:"failures"`
	FetchedAt    string                   `json:"fetched_at"`
}

type probeTCPDebugCommand struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Timestamp string `json:"timestamp"`
}

type probeTCPDebugResultMessage struct {
	Type         string                   `json:"type"`
	RequestID    string                   `json:"request_id"`
	NodeID       string                   `json:"node_id"`
	OK           bool                     `json:"ok"`
	Scope        string                   `json:"scope,omitempty"`
	ActiveCount  int                      `json:"active_count"`
	Active       []tcpDebugConnectionItem `json:"active"`
	FailureCount int                      `json:"failure_count"`
	Failures     []tcpDebugFailureItem    `json:"failures"`
	FetchedAt    string                   `json:"fetched_at,omitempty"`
	Error        string                   `json:"error,omitempty"`
	Timestamp    string                   `json:"timestamp,omitempty"`
}

type controllerTCPDebugFailureEvent struct {
	Kind        string
	Reason      string
	Target      string
	RouteTarget string
	Error       string
	At          time.Time
}

type controllerTCPTunnelRelay struct {
	id          string
	target      string
	routeTarget string
	openedAt    time.Time
	state       *controllerTCPDebugState

	lastActiveUnix atomic.Int64
	bytesUp        atomic.Int64
	bytesDown      atomic.Int64
	activeSides    atomic.Int32
}

type controllerTCPDebugState struct {
	mu       sync.Mutex
	seq      atomic.Uint64
	active   map[string]*controllerTCPTunnelRelay
	failures []controllerTCPDebugFailureEvent
}

type controllerTCPDebugWriter struct {
	dst       net.Conn
	relay     *controllerTCPTunnelRelay
	direction string
}

var globalControllerTCPDebugState = newControllerTCPDebugState()

var probeTCPDebugRequestSeq atomic.Uint64

var probeTCPDebugWaiters = struct {
	mu   sync.Mutex
	data map[string]chan probeTCPDebugResultMessage
}{data: make(map[string]chan probeTCPDebugResultMessage)}

func newControllerTCPDebugState() *controllerTCPDebugState {
	return &controllerTCPDebugState{active: make(map[string]*controllerTCPTunnelRelay)}
}

func (w *controllerTCPDebugWriter) Write(payload []byte) (int, error) {
	if w == nil || w.dst == nil {
		return 0, net.ErrClosed
	}
	n, err := w.dst.Write(payload)
	if n > 0 && w.relay != nil {
		w.relay.touch(w.direction, n)
	}
	return n, err
}

func (s *controllerTCPDebugState) beginRelay(target string) *controllerTCPTunnelRelay {
	if s == nil {
		return nil
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("controller-tcp-%d-%d", now.UnixNano(), s.seq.Add(1))
	relay := &controllerTCPTunnelRelay{
		id:          id,
		target:      strings.TrimSpace(target),
		routeTarget: strings.TrimSpace(target),
		openedAt:    now,
		state:       s,
	}
	relay.lastActiveUnix.Store(now.Unix())
	relay.activeSides.Store(2)
	s.mu.Lock()
	if s.active == nil {
		s.active = make(map[string]*controllerTCPTunnelRelay)
	}
	s.active[id] = relay
	s.mu.Unlock()
	return relay
}

func (r *controllerTCPTunnelRelay) touch(direction string, n int) {
	if r == nil || n <= 0 {
		return
	}
	now := time.Now().UTC()
	r.lastActiveUnix.Store(now.Unix())
	if strings.EqualFold(strings.TrimSpace(direction), "down") {
		r.bytesDown.Add(int64(n))
		return
	}
	r.bytesUp.Add(int64(n))
}

func (r *controllerTCPTunnelRelay) releaseSide() {
	if r == nil || r.state == nil {
		return
	}
	if r.activeSides.Add(-1) > 0 {
		return
	}
	r.state.mu.Lock()
	delete(r.state.active, r.id)
	r.state.mu.Unlock()
}

func (s *controllerTCPDebugState) recordFailure(kind string, target string, err error) {
	if s == nil || err == nil {
		return
	}
	event := controllerTCPDebugFailureEvent{
		Kind:        strings.TrimSpace(kind),
		Reason:      classifyControllerTCPDebugError(kind, err),
		Target:      strings.TrimSpace(target),
		RouteTarget: strings.TrimSpace(target),
		Error:       strings.TrimSpace(err.Error()),
		At:          time.Now().UTC(),
	}
	s.mu.Lock()
	s.failures = append(s.failures, event)
	if len(s.failures) > controllerTCPDebugMaxFailures {
		s.failures = append([]controllerTCPDebugFailureEvent(nil), s.failures[len(s.failures)-controllerTCPDebugMaxFailures:]...)
	}
	s.mu.Unlock()
}

func (s *controllerTCPDebugState) recordRelayFailure(relay *controllerTCPTunnelRelay, err error) {
	if relay == nil {
		s.recordFailure("relay_failed", "", err)
		return
	}
	s.recordFailure("relay_failed", relay.target, err)
}

func (s *controllerTCPDebugState) snapshotPayload(scope string, nodeID string) tcpDebugPayload {
	payload := tcpDebugPayload{
		Kind:      "network_tcp_debug",
		Scope:     strings.TrimSpace(scope),
		NodeID:    strings.TrimSpace(nodeID),
		Active:    []tcpDebugConnectionItem{},
		Failures:  []tcpDebugFailureItem{},
		FetchedAt: time.Now().Format(time.RFC3339),
	}
	if s == nil {
		return payload
	}
	now := time.Now().UTC()
	s.mu.Lock()
	activeItems := make([]*controllerTCPTunnelRelay, 0, len(s.active))
	for _, relay := range s.active {
		if relay != nil {
			activeItems = append(activeItems, relay)
		}
	}
	failureItems := append([]controllerTCPDebugFailureEvent(nil), s.failures...)
	s.mu.Unlock()

	for _, relay := range activeItems {
		item := tcpDebugConnectionItem{
			ID:          strings.TrimSpace(relay.id),
			Target:      strings.TrimSpace(relay.target),
			RouteTarget: firstNonEmptyControllerTCPDebugString(strings.TrimSpace(relay.routeTarget), strings.TrimSpace(relay.target)),
			Transport:   "tcp",
			OpenedAt:    relay.openedAt.UTC().Format(time.RFC3339),
			AgeMS:       now.Sub(relay.openedAt).Milliseconds(),
			BytesUp:     relay.bytesUp.Load(),
			BytesDown:   relay.bytesDown.Load(),
		}
		if lastActive := relay.lastActiveUnix.Load(); lastActive > 0 {
			lastActiveAt := time.Unix(lastActive, 0).UTC()
			item.LastActive = lastActiveAt.Format(time.RFC3339)
			item.IdleMS = now.Sub(lastActiveAt).Milliseconds()
		}
		payload.Active = append(payload.Active, item)
	}
	sort.Slice(payload.Active, func(i, j int) bool {
		if payload.Active[i].Target == payload.Active[j].Target {
			return payload.Active[i].ID < payload.Active[j].ID
		}
		return payload.Active[i].Target < payload.Active[j].Target
	})

	for _, event := range failureItems {
		payload.Failures = append(payload.Failures, tcpDebugFailureItem{
			Kind:        strings.TrimSpace(event.Kind),
			Reason:      strings.TrimSpace(event.Reason),
			Target:      strings.TrimSpace(event.Target),
			RouteTarget: firstNonEmptyControllerTCPDebugString(strings.TrimSpace(event.RouteTarget), strings.TrimSpace(event.Target)),
			Transport:   "tcp",
			Error:       strings.TrimSpace(event.Error),
			LastSeen:    event.At.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(payload.Failures, func(i, j int) bool {
		return payload.Failures[i].LastSeen > payload.Failures[j].LastSeen
	})
	payload.ActiveCount = len(payload.Active)
	payload.FailureCount = len(payload.Failures)
	return payload
}

func buildControllerTCPDebugPayload() tcpDebugPayload {
	return globalControllerTCPDebugState.snapshotPayload("controller", "")
}

func buildProbeTCPDebugPayloadFromResult(result probeTCPDebugResultMessage) tcpDebugPayload {
	active := append([]tcpDebugConnectionItem(nil), result.Active...)
	failures := append([]tcpDebugFailureItem(nil), result.Failures...)
	return tcpDebugPayload{
		Kind:         "network_tcp_debug",
		Scope:        firstNonEmptyControllerTCPDebugString(strings.TrimSpace(result.Scope), "probe"),
		NodeID:       strings.TrimSpace(result.NodeID),
		ActiveCount:  len(active),
		Active:       active,
		FailureCount: len(failures),
		Failures:     failures,
		FetchedAt:    firstNonEmptyControllerTCPDebugString(strings.TrimSpace(result.FetchedAt), strings.TrimSpace(result.Timestamp), time.Now().Format(time.RFC3339)),
	}
}

func fetchProbeTCPDebugFromNode(nodeID string) (probeTCPDebugResultMessage, error) {
	normalizedID := normalizeProbeNodeID(nodeID)
	if normalizedID == "" {
		return probeTCPDebugResultMessage{}, fmt.Errorf("node_id is required")
	}
	session, ok := getProbeSession(normalizedID)
	if !ok {
		return probeTCPDebugResultMessage{}, fmt.Errorf("probe is offline")
	}
	requestID := newProbeTCPDebugRequestID(normalizedID)
	waiter := make(chan probeTCPDebugResultMessage, 1)

	probeTCPDebugWaiters.mu.Lock()
	probeTCPDebugWaiters.data[requestID] = waiter
	probeTCPDebugWaiters.mu.Unlock()
	defer func() {
		probeTCPDebugWaiters.mu.Lock()
		delete(probeTCPDebugWaiters.data, requestID)
		probeTCPDebugWaiters.mu.Unlock()
	}()

	cmd := probeTCPDebugCommand{
		Type:      "tcp_debug_get",
		RequestID: requestID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if err := session.writeJSON(cmd); err != nil {
		unregisterProbeSession(normalizedID, session)
		return probeTCPDebugResultMessage{}, err
	}

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

	select {
	case result := <-waiter:
		if strings.TrimSpace(result.NodeID) == "" {
			result.NodeID = normalizedID
		}
		result.Scope = firstNonEmptyControllerTCPDebugString(strings.TrimSpace(result.Scope), "probe")
		if result.Active == nil {
			result.Active = []tcpDebugConnectionItem{}
		}
		if result.Failures == nil {
			result.Failures = []tcpDebugFailureItem{}
		}
		result.ActiveCount = len(result.Active)
		result.FailureCount = len(result.Failures)
		if !result.OK {
			errMsg := strings.TrimSpace(result.Error)
			if errMsg == "" {
				errMsg = "probe tcp debug fetch failed"
			}
			return result, fmt.Errorf(errMsg)
		}
		return result, nil
	case <-timer.C:
		return probeTCPDebugResultMessage{}, fmt.Errorf("probe tcp debug fetch timeout")
	}
}

func consumeProbeTCPDebugResult(result probeTCPDebugResultMessage) {
	requestID := strings.TrimSpace(result.RequestID)
	if requestID == "" {
		return
	}
	probeTCPDebugWaiters.mu.Lock()
	waiter, ok := probeTCPDebugWaiters.data[requestID]
	if ok {
		delete(probeTCPDebugWaiters.data, requestID)
	}
	probeTCPDebugWaiters.mu.Unlock()
	if !ok {
		return
	}
	select {
	case waiter <- result:
	default:
	}
}

func newProbeTCPDebugRequestID(nodeID string) string {
	seq := probeTCPDebugRequestSeq.Add(1)
	return fmt.Sprintf("probe-tcp-debug-%s-%d-%d", normalizeProbeNodeID(nodeID), time.Now().UnixNano(), seq)
}

func firstNonEmptyControllerTCPDebugString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func classifyControllerTCPDebugError(defaultReason string, err error) string {
	if err == nil {
		return strings.TrimSpace(defaultReason)
	}
	errText := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(errText, "timeout"):
		return "timeout"
	case strings.Contains(errText, "refused"):
		return "connection_refused"
	case strings.Contains(errText, "reset"):
		return "connection_reset"
	case strings.Contains(errText, "broken pipe"):
		return "broken_pipe"
	case strings.Contains(errText, "eof"):
		return "eof"
	case strings.Contains(errText, "closed"):
		return "closed"
	default:
		return firstNonEmptyControllerTCPDebugString(strings.TrimSpace(defaultReason), "tcp_failed")
	}
}
