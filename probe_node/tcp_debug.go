package main

import (
	"encoding/json"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const probeTCPDebugMaxFailures = 128
const probeTCPDebugMaxCompleted = 128
const probeTCPDebugBlockedWriteThreshold = 50 * time.Millisecond

type probeTCPDebugConnectionItemPayload struct {
	ID                    string `json:"id"`
	Status                string `json:"status,omitempty"`
	FlowID                string `json:"flow_id,omitempty"`
	Side                  string `json:"side,omitempty"`
	Scope                 string `json:"scope,omitempty"`
	Target                string `json:"target,omitempty"`
	RouteTarget           string `json:"route_target,omitempty"`
	Domain                string `json:"domain,omitempty"`
	DomainSource          string `json:"domain_source,omitempty"`
	NodeID                string `json:"node_id,omitempty"`
	Group                 string `json:"group,omitempty"`
	Direct                bool   `json:"direct"`
	Transport             string `json:"transport,omitempty"`
	SessionID             string `json:"session_id,omitempty"`
	SessionRole           string `json:"session_role,omitempty"`
	SessionStreamsOpen    int    `json:"session_streams_open,omitempty"`
	SessionStreamsAfter   int    `json:"session_streams_after,omitempty"`
	SessionStreamsCurrent int    `json:"session_streams_current,omitempty"`
	OpenedAt              string `json:"opened_at,omitempty"`
	ClosedAt              string `json:"closed_at,omitempty"`
	LastActive            string `json:"last_active,omitempty"`
	LastWriteBlockedAt    string `json:"last_write_blocked_at,omitempty"`
	LastCongestionSide    string `json:"last_congestion_side,omitempty"`
	CloseReason           string `json:"close_reason,omitempty"`
	OpenLatencyMS         int64  `json:"open_latency_ms,omitempty"`
	AgeMS                 int64  `json:"age_ms"`
	DurationMS            int64  `json:"duration_ms,omitempty"`
	IdleMS                int64  `json:"idle_ms"`
	BytesUp               int64  `json:"bytes_up,omitempty"`
	BytesDown             int64  `json:"bytes_down,omitempty"`
	WritesUp              int64  `json:"writes_up,omitempty"`
	WritesDown            int64  `json:"writes_down,omitempty"`
	BlockedWritesUp       int64  `json:"blocked_writes_up,omitempty"`
	BlockedWritesDown     int64  `json:"blocked_writes_down,omitempty"`
	WriteBlockMSUp        int64  `json:"write_block_ms_up,omitempty"`
	WriteBlockMSDown      int64  `json:"write_block_ms_down,omitempty"`
	MaxWriteBlockMSUp     int64  `json:"max_write_block_ms_up,omitempty"`
	MaxWriteBlockMSDown   int64  `json:"max_write_block_ms_down,omitempty"`
	LastWriteBlockMSUp    int64  `json:"last_write_block_ms_up,omitempty"`
	LastWriteBlockMSDown  int64  `json:"last_write_block_ms_down,omitempty"`
}

type probeTCPDebugFailureItemPayload struct {
	Kind         string `json:"kind"`
	Reason       string `json:"reason,omitempty"`
	FlowID       string `json:"flow_id,omitempty"`
	Side         string `json:"side,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Target       string `json:"target,omitempty"`
	RouteTarget  string `json:"route_target,omitempty"`
	Domain       string `json:"domain,omitempty"`
	DomainSource string `json:"domain_source,omitempty"`
	NodeID       string `json:"node_id,omitempty"`
	Group        string `json:"group,omitempty"`
	Direct       bool   `json:"direct"`
	Transport    string `json:"transport,omitempty"`
	Error        string `json:"error,omitempty"`
	LastSeen     string `json:"last_seen,omitempty"`
}

type probeTCPDebugResultPayload struct {
	Type           string                               `json:"type"`
	RequestID      string                               `json:"request_id"`
	NodeID         string                               `json:"node_id"`
	OK             bool                                 `json:"ok"`
	Scope          string                               `json:"scope,omitempty"`
	ActiveCount    int                                  `json:"active_count"`
	Active         []probeTCPDebugConnectionItemPayload `json:"active"`
	CompletedCount int                                  `json:"completed_count"`
	Completed      []probeTCPDebugConnectionItemPayload `json:"completed"`
	FailureCount   int                                  `json:"failure_count"`
	Failures       []probeTCPDebugFailureItemPayload    `json:"failures"`
	FetchedAt      string                               `json:"fetched_at,omitempty"`
	Error          string                               `json:"error,omitempty"`
	Timestamp      string                               `json:"timestamp,omitempty"`
}

type probeTCPDebugFailureEvent struct {
	Kind         string
	Reason       string
	Scope        string
	FlowID       string
	Side         string
	Target       string
	RouteTarget  string
	Domain       string
	DomainSource string
	NodeID       string
	Group        string
	Direct       bool
	Transport    string
	Error        string
	At           time.Time
}

type probeTCPDebugRelay struct {
	id                  string
	flowID              string
	side                string
	scope               string
	target              string
	routeTarget         string
	nodeID              string
	group               string
	direct              bool
	transport           string
	sessionID           string
	sessionRole         string
	session             interface{ NumStreams() int }
	sessionStreamsOpen  int
	sessionStreamsAfter int
	openedAt            time.Time
	state               *probeTCPDebugState

	openLatencyMS      atomic.Int64
	lastActiveUnix     atomic.Int64
	lastBlockedUnix    atomic.Int64
	bytesUp            atomic.Int64
	bytesDown          atomic.Int64
	writesUp           atomic.Int64
	writesDown         atomic.Int64
	blockedUp          atomic.Int64
	blockedDown        atomic.Int64
	blockMSUp          atomic.Int64
	blockMSDown        atomic.Int64
	maxBlockMSUp       atomic.Int64
	maxBlockMSDown     atomic.Int64
	lastBlockMSUp      atomic.Int64
	lastBlockMSDown    atomic.Int64
	lastCongestionSide atomic.Value
	activeSides        atomic.Int32
}

type probeTCPDebugRelayOptions struct {
	Scope               string
	FlowID              string
	Side                string
	Target              string
	RouteTarget         string
	Route               probeLocalTunnelRouteDecision
	Transport           string
	SessionID           string
	SessionRole         string
	Session             interface{ NumStreams() int }
	SessionStreamsOpen  int
	SessionStreamsAfter int
}

type probeTCPDebugState struct {
	mu        sync.Mutex
	seq       atomic.Uint64
	active    map[string]*probeTCPDebugRelay
	completed []probeTCPDebugConnectionItemPayload
	failures  []probeTCPDebugFailureEvent
}

type probeTCPDebugWriter struct {
	dst       net.Conn
	relay     *probeTCPDebugRelay
	direction string
}

var globalProbeTCPDebugState = newProbeTCPDebugState()

func newProbeTCPDebugState() *probeTCPDebugState {
	return &probeTCPDebugState{active: make(map[string]*probeTCPDebugRelay)}
}

func (w *probeTCPDebugWriter) Write(payload []byte) (int, error) {
	if w == nil || w.dst == nil {
		return 0, net.ErrClosed
	}
	startedAt := time.Now()
	n, err := w.dst.Write(payload)
	elapsed := time.Since(startedAt)
	if n > 0 && w.relay != nil {
		w.relay.touch(w.direction, n)
	}
	if w.relay != nil {
		w.relay.recordWrite(w.direction, elapsed)
	}
	return n, err
}

func (s *probeTCPDebugState) beginRelay(target string) *probeTCPDebugRelay {
	return s.beginRelayWithScope("chain", target, probeLocalTunnelRouteDecision{})
}

func (s *probeTCPDebugState) beginRelayWithRoute(target string, route probeLocalTunnelRouteDecision) *probeTCPDebugRelay {
	return s.beginRelayWithRouteAndFlow(target, route, strings.TrimSpace(route.FlowID), "local")
}

func (s *probeTCPDebugState) beginRelayWithRouteAndFlow(target string, route probeLocalTunnelRouteDecision, flowID string, side string) *probeTCPDebugRelay {
	return s.beginRelayWithOptions(probeTCPDebugRelayOptions{
		Scope:  "tun",
		Target: target,
		Route:  route,
		FlowID: flowID,
		Side:   side,
	})
}

func (s *probeTCPDebugState) beginRelayWithScopeAndFlow(scope string, target string, flowID string, side string) *probeTCPDebugRelay {
	return s.beginRelayWithOptions(probeTCPDebugRelayOptions{
		Scope:  scope,
		Target: target,
		FlowID: flowID,
		Side:   side,
	})
}

func (s *probeTCPDebugState) beginRelayWithScope(scope string, target string, route probeLocalTunnelRouteDecision) *probeTCPDebugRelay {
	return s.beginRelayWithOptions(probeTCPDebugRelayOptions{Scope: scope, Target: target, Route: route})
}

func (s *probeTCPDebugState) beginRelayWithOptions(opts probeTCPDebugRelayOptions) *probeTCPDebugRelay {
	if s == nil {
		return nil
	}
	now := time.Now().UTC()
	id := "probe-tcp-" + strconv.FormatInt(now.UnixNano(), 10) + "-" + strconv.FormatUint(s.seq.Add(1), 10)
	transport := "tcp"
	if strings.TrimSpace(opts.Transport) != "" {
		transport = strings.TrimSpace(opts.Transport)
	} else if opts.Route.Direct {
		transport = "direct"
	} else if strings.TrimSpace(opts.Route.Group) != "" || strings.TrimSpace(opts.Route.TunnelNodeID) != "" {
		transport = "tunnel"
	}
	routeTarget := strings.TrimSpace(opts.RouteTarget)
	if routeTarget == "" {
		routeTarget = strings.TrimSpace(opts.Route.TargetAddr)
	}
	relay := &probeTCPDebugRelay{
		id:                  id,
		flowID:              strings.TrimSpace(opts.FlowID),
		side:                strings.TrimSpace(opts.Side),
		scope:               firstNonEmptyProbeTCPDebugString(strings.TrimSpace(opts.Scope), "unknown"),
		target:              strings.TrimSpace(opts.Target),
		routeTarget:         firstNonEmptyProbeTCPDebugString(routeTarget, strings.TrimSpace(opts.Target)),
		nodeID:              strings.TrimSpace(opts.Route.TunnelNodeID),
		group:               strings.TrimSpace(opts.Route.Group),
		direct:              opts.Route.Direct,
		transport:           transport,
		sessionID:           strings.TrimSpace(opts.SessionID),
		sessionRole:         strings.TrimSpace(opts.SessionRole),
		session:             opts.Session,
		sessionStreamsOpen:  opts.SessionStreamsOpen,
		sessionStreamsAfter: opts.SessionStreamsAfter,
		openedAt:            now,
		state:               s,
	}
	relay.lastActiveUnix.Store(now.Unix())
	relay.activeSides.Store(2)
	s.mu.Lock()
	if s.active == nil {
		s.active = make(map[string]*probeTCPDebugRelay)
	}
	s.active[id] = relay
	s.mu.Unlock()
	return relay
}

func (r *probeTCPDebugRelay) setOpenLatency(elapsed time.Duration) {
	if r == nil || elapsed <= 0 {
		return
	}
	r.openLatencyMS.Store(probeDurationMilliseconds(elapsed))
}

func (r *probeTCPDebugRelay) touch(direction string, n int) {
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

func (r *probeTCPDebugRelay) recordWrite(direction string, elapsed time.Duration) {
	if r == nil {
		return
	}
	side := normalizeProbeTCPDebugDirection(direction)
	elapsedMS := elapsed.Milliseconds()
	if side == "down" {
		r.writesDown.Add(1)
		if elapsed >= probeTCPDebugBlockedWriteThreshold {
			r.blockedDown.Add(1)
			r.blockMSDown.Add(elapsedMS)
			r.lastBlockMSDown.Store(elapsedMS)
			updateProbeTCPDebugMax(&r.maxBlockMSDown, elapsedMS)
			r.lastBlockedUnix.Store(time.Now().UTC().Unix())
			r.lastCongestionSide.Store("down")
		}
		return
	}
	r.writesUp.Add(1)
	if elapsed >= probeTCPDebugBlockedWriteThreshold {
		r.blockedUp.Add(1)
		r.blockMSUp.Add(elapsedMS)
		r.lastBlockMSUp.Store(elapsedMS)
		updateProbeTCPDebugMax(&r.maxBlockMSUp, elapsedMS)
		r.lastBlockedUnix.Store(time.Now().UTC().Unix())
		r.lastCongestionSide.Store("up")
	}
}

func updateProbeTCPDebugMax(target *atomic.Int64, value int64) {
	if target == nil || value <= 0 {
		return
	}
	for {
		current := target.Load()
		if value <= current {
			return
		}
		if target.CompareAndSwap(current, value) {
			return
		}
	}
}

func (r *probeTCPDebugRelay) releaseSide() {
	if r == nil || r.state == nil {
		return
	}
	if r.activeSides.Add(-1) > 0 {
		return
	}
	closedAt := time.Now().UTC()
	item := buildProbeTCPDebugConnectionPayload(r, closedAt)
	item.Status = "closed"
	item.ClosedAt = closedAt.Format(time.RFC3339)
	item.CloseReason = "closed"
	item.DurationMS = closedAt.Sub(r.openedAt).Milliseconds()
	r.state.mu.Lock()
	delete(r.state.active, r.id)
	r.state.completed = append(r.state.completed, item)
	if len(r.state.completed) > probeTCPDebugMaxCompleted {
		r.state.completed = append([]probeTCPDebugConnectionItemPayload(nil), r.state.completed[len(r.state.completed)-probeTCPDebugMaxCompleted:]...)
	}
	r.state.mu.Unlock()
}

func (s *probeTCPDebugState) recordFailure(kind string, target string, err error) {
	s.recordFailureWithRoute(kind, target, probeLocalTunnelRouteDecision{}, err)
}

func (s *probeTCPDebugState) recordFailureWithRoute(kind string, target string, route probeLocalTunnelRouteDecision, err error) {
	s.recordFailureWithOptions(kind, probeTCPDebugRelayOptions{Scope: "unknown", Target: target, Route: route}, err)
}

func (s *probeTCPDebugState) recordFailureWithScopeAndFlow(kind string, scope string, target string, flowID string, side string, err error) {
	s.recordFailureWithOptions(kind, probeTCPDebugRelayOptions{Scope: scope, Target: target, FlowID: flowID, Side: side}, err)
}

func (s *probeTCPDebugState) recordFailureWithOptions(kind string, opts probeTCPDebugRelayOptions, err error) {
	if s == nil || err == nil {
		return
	}
	transport := "tcp"
	if strings.TrimSpace(opts.Transport) != "" {
		transport = strings.TrimSpace(opts.Transport)
	} else if opts.Route.Direct {
		transport = "direct"
	} else if strings.TrimSpace(opts.Route.Group) != "" || strings.TrimSpace(opts.Route.TunnelNodeID) != "" {
		transport = "tunnel"
	}
	routeTarget := strings.TrimSpace(opts.RouteTarget)
	if routeTarget == "" {
		routeTarget = strings.TrimSpace(opts.Route.TargetAddr)
	}
	domain, domainSource := resolveProbeTCPDebugDomain(firstNonEmptyProbeTCPDebugString(strings.TrimSpace(opts.Target), routeTarget), firstNonEmptyProbeTCPDebugString(routeTarget, strings.TrimSpace(opts.Target)))
	event := probeTCPDebugFailureEvent{
		Kind:         strings.TrimSpace(kind),
		Reason:       classifyProbeTCPDebugError(kind, err),
		Scope:        firstNonEmptyProbeTCPDebugString(strings.TrimSpace(opts.Scope), "unknown"),
		FlowID:       strings.TrimSpace(opts.FlowID),
		Side:         strings.TrimSpace(opts.Side),
		Target:       strings.TrimSpace(opts.Target),
		RouteTarget:  firstNonEmptyProbeTCPDebugString(routeTarget, strings.TrimSpace(opts.Target)),
		Domain:       domain,
		DomainSource: domainSource,
		NodeID:       strings.TrimSpace(opts.Route.TunnelNodeID),
		Group:        strings.TrimSpace(opts.Route.Group),
		Direct:       opts.Route.Direct,
		Transport:    transport,
		Error:        strings.TrimSpace(err.Error()),
		At:           time.Now().UTC(),
	}
	s.mu.Lock()
	s.failures = append(s.failures, event)
	if len(s.failures) > probeTCPDebugMaxFailures {
		s.failures = append([]probeTCPDebugFailureEvent(nil), s.failures[len(s.failures)-probeTCPDebugMaxFailures:]...)
	}
	s.mu.Unlock()
}

func (s *probeTCPDebugState) recordRelayFailure(relay *probeTCPDebugRelay, err error) {
	if relay == nil {
		s.recordFailure("relay_failed", "", err)
		return
	}
	route := probeLocalTunnelRouteDecision{
		TargetAddr:   relay.routeTarget,
		TunnelNodeID: relay.nodeID,
		Group:        relay.group,
		Direct:       relay.direct,
	}
	s.recordFailureWithOptions("relay_failed", probeTCPDebugRelayOptions{
		Scope:     relay.scope,
		FlowID:    relay.flowID,
		Side:      relay.side,
		Target:    relay.target,
		Route:     route,
		Transport: relay.transport,
	}, err)
}

func (s *probeTCPDebugState) snapshotPayload(nodeID string, requestID string) probeTCPDebugResultPayload {
	payload := probeTCPDebugResultPayload{
		Type:      "tcp_debug_result",
		RequestID: strings.TrimSpace(requestID),
		NodeID:    strings.TrimSpace(nodeID),
		OK:        true,
		Scope:     "probe",
		Active:    []probeTCPDebugConnectionItemPayload{},
		Completed: []probeTCPDebugConnectionItemPayload{},
		Failures:  []probeTCPDebugFailureItemPayload{},
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if s == nil {
		return payload
	}
	now := time.Now().UTC()
	s.mu.Lock()
	activeItems := make([]*probeTCPDebugRelay, 0, len(s.active))
	for _, relay := range s.active {
		if relay != nil {
			activeItems = append(activeItems, relay)
		}
	}
	completedItems := append([]probeTCPDebugConnectionItemPayload(nil), s.completed...)
	failureItems := append([]probeTCPDebugFailureEvent(nil), s.failures...)
	s.mu.Unlock()

	for _, relay := range activeItems {
		payload.Active = append(payload.Active, buildProbeTCPDebugConnectionPayload(relay, now))
	}
	sort.Slice(payload.Active, func(i, j int) bool {
		if payload.Active[i].Target == payload.Active[j].Target {
			return payload.Active[i].ID < payload.Active[j].ID
		}
		return payload.Active[i].Target < payload.Active[j].Target
	})
	payload.Completed = completedItems
	sort.Slice(payload.Completed, func(i, j int) bool {
		return payload.Completed[i].ClosedAt > payload.Completed[j].ClosedAt
	})

	for _, event := range failureItems {
		payload.Failures = append(payload.Failures, probeTCPDebugFailureItemPayload{
			Kind:         strings.TrimSpace(event.Kind),
			Reason:       strings.TrimSpace(event.Reason),
			FlowID:       strings.TrimSpace(event.FlowID),
			Side:         strings.TrimSpace(event.Side),
			Scope:        strings.TrimSpace(event.Scope),
			Target:       strings.TrimSpace(event.Target),
			RouteTarget:  firstNonEmptyProbeTCPDebugString(strings.TrimSpace(event.RouteTarget), strings.TrimSpace(event.Target)),
			Domain:       strings.TrimSpace(event.Domain),
			DomainSource: strings.TrimSpace(event.DomainSource),
			NodeID:       strings.TrimSpace(event.NodeID),
			Group:        strings.TrimSpace(event.Group),
			Direct:       event.Direct,
			Transport:    firstNonEmptyProbeTCPDebugString(strings.TrimSpace(event.Transport), "tcp"),
			Error:        strings.TrimSpace(event.Error),
			LastSeen:     event.At.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(payload.Failures, func(i, j int) bool {
		return payload.Failures[i].LastSeen > payload.Failures[j].LastSeen
	})
	payload.ActiveCount = len(payload.Active)
	payload.CompletedCount = len(payload.Completed)
	payload.FailureCount = len(payload.Failures)
	return payload
}

func buildProbeTCPDebugConnectionPayload(relay *probeTCPDebugRelay, now time.Time) probeTCPDebugConnectionItemPayload {
	if relay == nil {
		return probeTCPDebugConnectionItemPayload{}
	}
	target := strings.TrimSpace(relay.target)
	routeTarget := firstNonEmptyProbeTCPDebugString(strings.TrimSpace(relay.routeTarget), target)
	domain, domainSource := resolveProbeTCPDebugDomain(target, routeTarget)
	item := probeTCPDebugConnectionItemPayload{
		ID:                   strings.TrimSpace(relay.id),
		Status:               "active",
		FlowID:               strings.TrimSpace(relay.flowID),
		Side:                 strings.TrimSpace(relay.side),
		Scope:                strings.TrimSpace(relay.scope),
		Target:               target,
		RouteTarget:          routeTarget,
		Domain:               domain,
		DomainSource:         domainSource,
		NodeID:               strings.TrimSpace(relay.nodeID),
		Group:                strings.TrimSpace(relay.group),
		Direct:               relay.direct,
		Transport:            firstNonEmptyProbeTCPDebugString(strings.TrimSpace(relay.transport), "tcp"),
		SessionID:            strings.TrimSpace(relay.sessionID),
		SessionRole:          strings.TrimSpace(relay.sessionRole),
		SessionStreamsOpen:   relay.sessionStreamsOpen,
		SessionStreamsAfter:  relay.sessionStreamsAfter,
		OpenedAt:             relay.openedAt.UTC().Format(time.RFC3339),
		OpenLatencyMS:        relay.openLatencyMS.Load(),
		AgeMS:                now.Sub(relay.openedAt).Milliseconds(),
		BytesUp:              relay.bytesUp.Load(),
		BytesDown:            relay.bytesDown.Load(),
		WritesUp:             relay.writesUp.Load(),
		WritesDown:           relay.writesDown.Load(),
		BlockedWritesUp:      relay.blockedUp.Load(),
		BlockedWritesDown:    relay.blockedDown.Load(),
		WriteBlockMSUp:       relay.blockMSUp.Load(),
		WriteBlockMSDown:     relay.blockMSDown.Load(),
		MaxWriteBlockMSUp:    relay.maxBlockMSUp.Load(),
		MaxWriteBlockMSDown:  relay.maxBlockMSDown.Load(),
		LastWriteBlockMSUp:   relay.lastBlockMSUp.Load(),
		LastWriteBlockMSDown: relay.lastBlockMSDown.Load(),
	}
	if lastActive := relay.lastActiveUnix.Load(); lastActive > 0 {
		lastActiveAt := time.Unix(lastActive, 0).UTC()
		item.LastActive = lastActiveAt.Format(time.RFC3339)
		item.IdleMS = now.Sub(lastActiveAt).Milliseconds()
	}
	if lastBlocked := relay.lastBlockedUnix.Load(); lastBlocked > 0 {
		item.LastWriteBlockedAt = time.Unix(lastBlocked, 0).UTC().Format(time.RFC3339)
	}
	if side, ok := relay.lastCongestionSide.Load().(string); ok {
		item.LastCongestionSide = strings.TrimSpace(side)
	}
	if relay.session != nil {
		item.SessionStreamsCurrent = relay.session.NumStreams()
	}
	return item
}

func normalizeProbeTCPDebugDirection(direction string) string {
	if strings.EqualFold(strings.TrimSpace(direction), "down") {
		return "down"
	}
	return "up"
}

func runProbeTCPDebugFetch(cmd probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	requestID := strings.TrimSpace(cmd.RequestID)
	if requestID == "" {
		return
	}
	payload := globalProbeTCPDebugState.snapshotPayload(strings.TrimSpace(identity.NodeID), requestID)
	if writeErr := writeProbeStreamJSON(stream, encoder, writeMu, payload); writeErr != nil {
		log.Printf("probe tcp debug response send failed: request_id=%s err=%v", requestID, writeErr)
	}
}

func firstNonEmptyProbeTCPDebugString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func resolveProbeTCPDebugDomain(target string, routeTarget string) (string, string) {
	for _, candidate := range []string{strings.TrimSpace(target), strings.TrimSpace(routeTarget)} {
		host := probeTCPDebugTargetHost(candidate)
		if host == "" {
			continue
		}
		if net.ParseIP(host) == nil {
			return strings.ToLower(strings.TrimSpace(strings.Trim(host, "."))), "target"
		}
	}
	for _, candidate := range []string{strings.TrimSpace(target), strings.TrimSpace(routeTarget)} {
		host := probeTCPDebugTargetHost(candidate)
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		ipText := ip.String()
		if fakeEntry, ok := lookupProbeLocalDNSFakeIPEntry(ipText); ok && strings.TrimSpace(fakeEntry.Domain) != "" {
			return strings.ToLower(strings.TrimSpace(fakeEntry.Domain)), "fake-ip"
		}
		if hint, ok := lookupProbeLocalDNSRouteHintEntryByIP(ipText); ok && strings.TrimSpace(hint.Domain) != "" {
			return strings.ToLower(strings.TrimSpace(hint.Domain)), "dns-hint"
		}
	}
	return "", ""
}

func probeTCPDebugTargetHost(value string) string {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(clean)
	if err == nil {
		return strings.TrimSpace(strings.Trim(host, "[]"))
	}
	clean = strings.TrimSpace(strings.Trim(clean, "[]"))
	if strings.Count(clean, ":") > 1 {
		if ip := net.ParseIP(clean); ip != nil {
			return ip.String()
		}
		return ""
	}
	if strings.Contains(clean, ":") {
		parts := strings.Split(clean, ":")
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0])
		}
		return ""
	}
	return clean
}

func newProbeTCPDebugFlowID(scope string, target string) string {
	cleanScope := strings.ToLower(strings.TrimSpace(scope))
	if cleanScope == "" {
		cleanScope = "tcp"
	}
	token := strings.ToLower(strings.TrimSpace(randomHexToken(8)))
	if token == "" {
		token = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return cleanScope + "-" + token + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func classifyProbeTCPDebugError(defaultReason string, err error) string {
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
		return firstNonEmptyProbeTCPDebugString(strings.TrimSpace(defaultReason), "tcp_failed")
	}
}
