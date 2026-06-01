package mobilecore

import (
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	androidProxyConnectionMaxFailures           = 128
	androidProxyConnectionBlockedWriteThreshold = 50 * time.Millisecond
)

type androidProxyConnectionItem struct {
	ID                   string `json:"id"`
	FlowID               string `json:"flow_id,omitempty"`
	Side                 string `json:"side,omitempty"`
	Scope                string `json:"scope,omitempty"`
	Target               string `json:"target,omitempty"`
	RouteTarget          string `json:"route_target,omitempty"`
	ChainID              string `json:"chain_id,omitempty"`
	Group                string `json:"group,omitempty"`
	Direct               bool   `json:"direct"`
	Transport            string `json:"transport,omitempty"`
	OpenedAt             string `json:"opened_at,omitempty"`
	LastActive           string `json:"last_active,omitempty"`
	LastWriteBlockedAt   string `json:"last_write_blocked_at,omitempty"`
	LastCongestionSide   string `json:"last_congestion_side,omitempty"`
	AgeMS                int64  `json:"age_ms"`
	IdleMS               int64  `json:"idle_ms"`
	BytesUp              int64  `json:"bytes_up,omitempty"`
	BytesDown            int64  `json:"bytes_down,omitempty"`
	WritesUp             int64  `json:"writes_up,omitempty"`
	WritesDown           int64  `json:"writes_down,omitempty"`
	BlockedWritesUp      int64  `json:"blocked_writes_up,omitempty"`
	BlockedWritesDown    int64  `json:"blocked_writes_down,omitempty"`
	WriteBlockMSUp       int64  `json:"write_block_ms_up,omitempty"`
	WriteBlockMSDown     int64  `json:"write_block_ms_down,omitempty"`
	MaxWriteBlockMSUp    int64  `json:"max_write_block_ms_up,omitempty"`
	MaxWriteBlockMSDown  int64  `json:"max_write_block_ms_down,omitempty"`
	LastWriteBlockMSUp   int64  `json:"last_write_block_ms_up,omitempty"`
	LastWriteBlockMSDown int64  `json:"last_write_block_ms_down,omitempty"`
}

type androidProxyConnectionFailure struct {
	Kind        string `json:"kind"`
	Reason      string `json:"reason,omitempty"`
	FlowID      string `json:"flow_id,omitempty"`
	Side        string `json:"side,omitempty"`
	Scope       string `json:"scope,omitempty"`
	Target      string `json:"target,omitempty"`
	RouteTarget string `json:"route_target,omitempty"`
	ChainID     string `json:"chain_id,omitempty"`
	Group       string `json:"group,omitempty"`
	Direct      bool   `json:"direct"`
	Transport   string `json:"transport,omitempty"`
	Error       string `json:"error,omitempty"`
	LastSeen    string `json:"last_seen,omitempty"`
}

type androidProxyConnectionSnapshot struct {
	Type         string                          `json:"type"`
	OK           bool                            `json:"ok"`
	Scope        string                          `json:"scope,omitempty"`
	ActiveCount  int                             `json:"active_count"`
	Active       []androidProxyConnectionItem    `json:"active"`
	FailureCount int                             `json:"failure_count"`
	Failures     []androidProxyConnectionFailure `json:"failures"`
	FetchedAt    string                          `json:"fetched_at,omitempty"`
}

type androidProxyConnectionFailureEvent struct {
	Kind        string
	Reason      string
	Scope       string
	FlowID      string
	Side        string
	Target      string
	RouteTarget string
	ChainID     string
	Group       string
	Direct      bool
	Transport   string
	Error       string
	At          time.Time
}

type androidProxyConnectionRoute struct {
	Direct          bool
	TargetAddr      string
	Group           string
	SelectedChainID string
}

type androidProxyConnectionOptions struct {
	Scope     string
	FlowID    string
	Side      string
	Target    string
	Route     androidProxyConnectionRoute
	Transport string
}

type androidProxyConnectionRelay struct {
	id          string
	flowID      string
	side        string
	scope       string
	target      string
	routeTarget string
	chainID     string
	group       string
	direct      bool
	transport   string
	openedAt    time.Time
	state       *androidProxyConnectionState

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

type androidProxyConnectionState struct {
	mu       sync.Mutex
	seq      atomic.Uint64
	active   map[string]*androidProxyConnectionRelay
	failures []androidProxyConnectionFailureEvent
}

type androidProxyConnectionWriter struct {
	dst       io.Writer
	relay     *androidProxyConnectionRelay
	direction string
}

var globalAndroidProxyConnectionState = newAndroidProxyConnectionState()

func newAndroidProxyConnectionState() *androidProxyConnectionState {
	return &androidProxyConnectionState{active: map[string]*androidProxyConnectionRelay{}}
}

func (w *androidProxyConnectionWriter) Write(payload []byte) (int, error) {
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

func (s *androidProxyConnectionState) begin(opts androidProxyConnectionOptions) *androidProxyConnectionRelay {
	if s == nil {
		return nil
	}
	now := time.Now().UTC()
	id := "android-proxy-" + strconv.FormatInt(now.UnixNano(), 10) + "-" + strconv.FormatUint(s.seq.Add(1), 10)
	transport := strings.TrimSpace(opts.Transport)
	if transport == "" {
		if opts.Route.Direct {
			transport = "direct"
		} else {
			transport = "stream"
		}
	}
	relay := &androidProxyConnectionRelay{
		id:          id,
		flowID:      strings.TrimSpace(opts.FlowID),
		side:        strings.TrimSpace(opts.Side),
		scope:       firstNonEmptyString(strings.TrimSpace(opts.Scope), "unknown"),
		target:      strings.TrimSpace(opts.Target),
		routeTarget: firstNonEmptyString(strings.TrimSpace(opts.Route.TargetAddr), strings.TrimSpace(opts.Target)),
		chainID:     strings.TrimSpace(opts.Route.SelectedChainID),
		group:       strings.TrimSpace(opts.Route.Group),
		direct:      opts.Route.Direct,
		transport:   transport,
		openedAt:    now,
		state:       s,
	}
	relay.lastActiveUnix.Store(now.Unix())
	relay.activeSides.Store(2)
	s.mu.Lock()
	if s.active == nil {
		s.active = map[string]*androidProxyConnectionRelay{}
	}
	s.active[id] = relay
	s.mu.Unlock()
	return relay
}

func (s *androidProxyConnectionState) recordFailure(kind string, opts androidProxyConnectionOptions, err error) {
	if s == nil || err == nil {
		return
	}
	transport := strings.TrimSpace(opts.Transport)
	if transport == "" {
		if opts.Route.Direct {
			transport = "direct"
		} else {
			transport = "stream"
		}
	}
	event := androidProxyConnectionFailureEvent{
		Kind:        strings.TrimSpace(kind),
		Reason:      classifyAndroidProxyConnectionError(kind, err),
		Scope:       firstNonEmptyString(strings.TrimSpace(opts.Scope), "unknown"),
		FlowID:      strings.TrimSpace(opts.FlowID),
		Side:        strings.TrimSpace(opts.Side),
		Target:      strings.TrimSpace(opts.Target),
		RouteTarget: firstNonEmptyString(strings.TrimSpace(opts.Route.TargetAddr), strings.TrimSpace(opts.Target)),
		ChainID:     strings.TrimSpace(opts.Route.SelectedChainID),
		Group:       strings.TrimSpace(opts.Route.Group),
		Direct:      opts.Route.Direct,
		Transport:   transport,
		Error:       strings.TrimSpace(err.Error()),
		At:          time.Now().UTC(),
	}
	s.mu.Lock()
	s.failures = append(s.failures, event)
	if len(s.failures) > androidProxyConnectionMaxFailures {
		s.failures = append([]androidProxyConnectionFailureEvent(nil), s.failures[len(s.failures)-androidProxyConnectionMaxFailures:]...)
	}
	s.mu.Unlock()
}

func (s *androidProxyConnectionState) recordRelayFailure(relay *androidProxyConnectionRelay, err error) {
	if s == nil || relay == nil || err == nil || isAndroidProxyExpectedRelayError(err) {
		return
	}
	s.recordFailure("relay_failed", androidProxyConnectionOptions{
		Scope:     relay.scope,
		FlowID:    relay.flowID,
		Side:      relay.side,
		Target:    relay.target,
		Transport: relay.transport,
		Route: androidProxyConnectionRoute{
			Direct:          relay.direct,
			TargetAddr:      relay.routeTarget,
			Group:           relay.group,
			SelectedChainID: relay.chainID,
		},
	}, err)
}

func (s *androidProxyConnectionState) snapshot() androidProxyConnectionSnapshot {
	payload := androidProxyConnectionSnapshot{
		Type:      "android_proxy_connections",
		OK:        true,
		Scope:     "android",
		Active:    []androidProxyConnectionItem{},
		Failures:  []androidProxyConnectionFailure{},
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if s == nil {
		return payload
	}
	now := time.Now().UTC()
	s.mu.Lock()
	active := make([]*androidProxyConnectionRelay, 0, len(s.active))
	for _, relay := range s.active {
		if relay != nil {
			active = append(active, relay)
		}
	}
	failures := append([]androidProxyConnectionFailureEvent(nil), s.failures...)
	s.mu.Unlock()
	for _, relay := range active {
		item := androidProxyConnectionItem{
			ID:                   strings.TrimSpace(relay.id),
			FlowID:               strings.TrimSpace(relay.flowID),
			Side:                 strings.TrimSpace(relay.side),
			Scope:                strings.TrimSpace(relay.scope),
			Target:               strings.TrimSpace(relay.target),
			RouteTarget:          firstNonEmptyString(strings.TrimSpace(relay.routeTarget), strings.TrimSpace(relay.target)),
			ChainID:              strings.TrimSpace(relay.chainID),
			Group:                strings.TrimSpace(relay.group),
			Direct:               relay.direct,
			Transport:            firstNonEmptyString(strings.TrimSpace(relay.transport), "stream"),
			OpenedAt:             relay.openedAt.UTC().Format(time.RFC3339),
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
		payload.Active = append(payload.Active, item)
	}
	sort.Slice(payload.Active, func(i, j int) bool {
		if payload.Active[i].Target == payload.Active[j].Target {
			return payload.Active[i].ID < payload.Active[j].ID
		}
		return payload.Active[i].Target < payload.Active[j].Target
	})
	for _, event := range failures {
		payload.Failures = append(payload.Failures, androidProxyConnectionFailure{
			Kind:        strings.TrimSpace(event.Kind),
			Reason:      strings.TrimSpace(event.Reason),
			FlowID:      strings.TrimSpace(event.FlowID),
			Side:        strings.TrimSpace(event.Side),
			Scope:       strings.TrimSpace(event.Scope),
			Target:      strings.TrimSpace(event.Target),
			RouteTarget: firstNonEmptyString(strings.TrimSpace(event.RouteTarget), strings.TrimSpace(event.Target)),
			ChainID:     strings.TrimSpace(event.ChainID),
			Group:       strings.TrimSpace(event.Group),
			Direct:      event.Direct,
			Transport:   firstNonEmptyString(strings.TrimSpace(event.Transport), "stream"),
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

func (r *androidProxyConnectionRelay) touch(direction string, n int) {
	if r == nil || n <= 0 {
		return
	}
	r.lastActiveUnix.Store(time.Now().UTC().Unix())
	if normalizeAndroidProxyConnectionDirection(direction) == "down" {
		r.bytesDown.Add(int64(n))
		return
	}
	r.bytesUp.Add(int64(n))
}

func (r *androidProxyConnectionRelay) recordWrite(direction string, elapsed time.Duration) {
	if r == nil {
		return
	}
	side := normalizeAndroidProxyConnectionDirection(direction)
	elapsedMS := elapsed.Milliseconds()
	if side == "down" {
		r.writesDown.Add(1)
		if elapsed >= androidProxyConnectionBlockedWriteThreshold {
			r.blockedDown.Add(1)
			r.blockMSDown.Add(elapsedMS)
			r.lastBlockMSDown.Store(elapsedMS)
			updateAndroidProxyConnectionMax(&r.maxBlockMSDown, elapsedMS)
			r.lastBlockedUnix.Store(time.Now().UTC().Unix())
			r.lastCongestionSide.Store("down")
		}
		return
	}
	r.writesUp.Add(1)
	if elapsed >= androidProxyConnectionBlockedWriteThreshold {
		r.blockedUp.Add(1)
		r.blockMSUp.Add(elapsedMS)
		r.lastBlockMSUp.Store(elapsedMS)
		updateAndroidProxyConnectionMax(&r.maxBlockMSUp, elapsedMS)
		r.lastBlockedUnix.Store(time.Now().UTC().Unix())
		r.lastCongestionSide.Store("up")
	}
}

func (r *androidProxyConnectionRelay) releaseSide() {
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

func updateAndroidProxyConnectionMax(target *atomic.Int64, value int64) {
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

func newAndroidProxyFlowID(scope string, target string) string {
	cleanScope := strings.ToLower(strings.TrimSpace(scope))
	if cleanScope == "" {
		cleanScope = "proxy"
	}
	token := strconv.FormatUint(globalAndroidProxyConnectionState.seq.Add(1), 36)
	return cleanScope + "-" + token + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func androidProxyConnectionRouteFromProxy(route proxyRouteDecision) androidProxyConnectionRoute {
	return androidProxyConnectionRoute{
		Direct:          route.Direct,
		TargetAddr:      route.TargetAddr,
		Group:           route.Group,
		SelectedChainID: route.SelectedChainID,
	}
}

func androidProxyConnectionRouteFromVPN(route vpnRouteDecision) androidProxyConnectionRoute {
	return androidProxyConnectionRoute{
		Direct:          route.Direct,
		TargetAddr:      route.TargetAddr,
		Group:           route.Group,
		SelectedChainID: route.SelectedChainID,
	}
}

func normalizeAndroidProxyConnectionDirection(direction string) string {
	if strings.EqualFold(strings.TrimSpace(direction), "down") {
		return "down"
	}
	return "up"
}

func isAndroidProxyExpectedRelayError(err error) bool {
	if err == nil {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return text == "" || strings.Contains(text, "closed") || strings.Contains(text, "eof") || strings.Contains(text, "use of closed network connection")
}

func classifyAndroidProxyConnectionError(defaultReason string, err error) string {
	if err == nil {
		return strings.TrimSpace(defaultReason)
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(text, "timeout"):
		return "timeout"
	case strings.Contains(text, "refused"):
		return "connection_refused"
	case strings.Contains(text, "reset"):
		return "connection_reset"
	case strings.Contains(text, "broken pipe"):
		return "broken_pipe"
	case strings.Contains(text, "eof"):
		return "eof"
	case strings.Contains(text, "closed"):
		return "closed"
	default:
		return firstNonEmptyString(strings.TrimSpace(defaultReason), "connection_failed")
	}
}
