package backend

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const localTUNTCPDebugMaxFailures = 128

type localTUNTCPDebugFailureEvent struct {
	Kind        string
	Reason      string
	Target      string
	RouteTarget string
	NodeID      string
	Group       string
	Direct      bool
	Transport   string
	Error       string
	At          time.Time
}

type localTUNTCPRelay struct {
	id          string
	target      string
	routeTarget string
	nodeID      string
	group       string
	direct      bool
	transport   string
	openedAt    time.Time
	state       *localTUNTCPDebugState

	lastActiveUnix atomic.Int64
	bytesUp        atomic.Int64
	bytesDown      atomic.Int64
	activeSides    atomic.Int32
}

type localTUNTCPDebugState struct {
	mu       sync.Mutex
	seq      atomic.Uint64
	active   map[string]*localTUNTCPRelay
	failures []localTUNTCPDebugFailureEvent
}

type localTUNTCPDebugWriter struct {
	dst       net.Conn
	relay     *localTUNTCPRelay
	direction string
}

func newLocalTUNTCPDebugState() *localTUNTCPDebugState {
	return &localTUNTCPDebugState{active: make(map[string]*localTUNTCPRelay)}
}

func (n *localTUNNetstack) beginTCPRelay(target string, route tunnelRouteDecision) *localTUNTCPRelay {
	if n == nil || n.tcpDebug == nil {
		return nil
	}
	return n.tcpDebug.beginRelay(target, route)
}

func (w *localTUNTCPDebugWriter) Write(payload []byte) (int, error) {
	if w == nil || w.dst == nil {
		return 0, net.ErrClosed
	}
	n, err := w.dst.Write(payload)
	if n > 0 && w.relay != nil {
		w.relay.touch(w.direction, n)
	}
	return n, err
}

func (s *localTUNTCPDebugState) beginRelay(target string, route tunnelRouteDecision) *localTUNTCPRelay {
	if s == nil {
		return nil
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("manager-tcp-%d-%d", now.UnixNano(), s.seq.Add(1))
	relay := &localTUNTCPRelay{
		id:          id,
		target:      strings.TrimSpace(target),
		routeTarget: strings.TrimSpace(route.TargetAddr),
		nodeID:      strings.TrimSpace(route.NodeID),
		group:       strings.TrimSpace(route.Group),
		direct:      route.Direct,
		transport: func() string {
			if route.Direct {
				return "direct"
			}
			return "tunnel"
		}(),
		openedAt: now,
		state:    s,
	}
	relay.lastActiveUnix.Store(now.Unix())
	relay.activeSides.Store(2)

	s.mu.Lock()
	if s.active == nil {
		s.active = make(map[string]*localTUNTCPRelay)
	}
	s.active[id] = relay
	s.mu.Unlock()
	return relay
}

func (r *localTUNTCPRelay) touch(direction string, n int) {
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

func (r *localTUNTCPRelay) releaseSide() {
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

func (s *localTUNTCPDebugState) recordFailure(kind string, reason string, target string, route tunnelRouteDecision, err error) {
	if s == nil || err == nil {
		return
	}
	transport := "tcp"
	switch {
	case route.Direct:
		transport = "direct"
	case strings.TrimSpace(route.Group) != "" || strings.TrimSpace(route.NodeID) != "" || strings.TrimSpace(route.TargetAddr) != "":
		transport = "tunnel"
	}
	event := localTUNTCPDebugFailureEvent{
		Kind:        strings.TrimSpace(kind),
		Reason:      firstNonEmpty(strings.TrimSpace(reason), classifyManagerTCPDebugError(kind, err)),
		Target:      strings.TrimSpace(target),
		RouteTarget: strings.TrimSpace(route.TargetAddr),
		NodeID:      strings.TrimSpace(route.NodeID),
		Group:       strings.TrimSpace(route.Group),
		Direct:      route.Direct,
		Transport:   transport,
		Error:       strings.TrimSpace(err.Error()),
		At:          time.Now().UTC(),
	}
	s.mu.Lock()
	s.failures = append(s.failures, event)
	if len(s.failures) > localTUNTCPDebugMaxFailures {
		s.failures = append([]localTUNTCPDebugFailureEvent(nil), s.failures[len(s.failures)-localTUNTCPDebugMaxFailures:]...)
	}
	s.mu.Unlock()
}

func (s *localTUNTCPDebugState) snapshotPayload(scope string, nodeID string) aiDebugTCPDebugPayload {
	payload := aiDebugTCPDebugPayload{
		Kind:      "network_tcp_debug",
		Scope:     strings.TrimSpace(scope),
		NodeID:    strings.TrimSpace(nodeID),
		Active:    []aiDebugTCPConnectionItemPayload{},
		Failures:  []aiDebugTCPFailureItemPayload{},
		FetchedAt: time.Now().Format(time.RFC3339),
	}
	if s == nil {
		return payload
	}

	now := time.Now().UTC()
	s.mu.Lock()
	activeItems := make([]*localTUNTCPRelay, 0, len(s.active))
	for _, relay := range s.active {
		if relay != nil {
			activeItems = append(activeItems, relay)
		}
	}
	failureItems := append([]localTUNTCPDebugFailureEvent(nil), s.failures...)
	s.mu.Unlock()

	for _, relay := range activeItems {
		item := aiDebugTCPConnectionItemPayload{
			ID:          strings.TrimSpace(relay.id),
			Target:      strings.TrimSpace(relay.target),
			RouteTarget: firstNonEmpty(strings.TrimSpace(relay.routeTarget), strings.TrimSpace(relay.target)),
			NodeID:      strings.TrimSpace(relay.nodeID),
			Group:       strings.TrimSpace(relay.group),
			Direct:      relay.direct,
			Transport:   firstNonEmpty(strings.TrimSpace(relay.transport), "tcp"),
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
		item := aiDebugTCPFailureItemPayload{
			Kind:        strings.TrimSpace(event.Kind),
			Reason:      strings.TrimSpace(event.Reason),
			Target:      strings.TrimSpace(event.Target),
			RouteTarget: firstNonEmpty(strings.TrimSpace(event.RouteTarget), strings.TrimSpace(event.Target)),
			NodeID:      strings.TrimSpace(event.NodeID),
			Group:       strings.TrimSpace(event.Group),
			Direct:      event.Direct,
			Transport:   firstNonEmpty(strings.TrimSpace(event.Transport), "tcp"),
			Error:       strings.TrimSpace(event.Error),
			LastSeen:    event.At.UTC().Format(time.RFC3339),
		}
		payload.Failures = append(payload.Failures, item)
	}
	sort.Slice(payload.Failures, func(i, j int) bool {
		return payload.Failures[i].LastSeen > payload.Failures[j].LastSeen
	})
	payload.ActiveCount = len(payload.Active)
	payload.FailureCount = len(payload.Failures)
	return payload
}

func classifyManagerTCPDebugError(defaultReason string, err error) string {
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
		return firstNonEmpty(strings.TrimSpace(defaultReason), "tcp_failed")
	}
}
