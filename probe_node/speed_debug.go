package main

import (
	"encoding/json"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const probeSpeedDebugMaxRecent = 64

type probeSpeedDebugItemPayload struct {
	ID                 string `json:"id"`
	ChainID            string `json:"chain_id,omitempty"`
	Role               string `json:"role,omitempty"`
	Side               string `json:"side,omitempty"`
	Transport          string `json:"transport,omitempty"`
	RemoteAddr         string `json:"remote_addr,omitempty"`
	Status             string `json:"status,omitempty"`
	Error              string `json:"error,omitempty"`
	RequestedBytes     int64  `json:"requested_bytes,omitempty"`
	ChunkBytes         int64  `json:"chunk_bytes,omitempty"`
	Bytes              int64  `json:"bytes,omitempty"`
	RemainingBytes     int64  `json:"remaining_bytes,omitempty"`
	RateBPS            int64  `json:"rate_bps,omitempty"`
	WriteCalls         int64  `json:"write_calls,omitempty"`
	TotalWriteBlockMS  int64  `json:"total_write_block_ms,omitempty"`
	MaxWriteBlockMS    int64  `json:"max_write_block_ms,omitempty"`
	LastWriteBlockMS   int64  `json:"last_write_block_ms,omitempty"`
	LastWriteBlockedAt string `json:"last_write_blocked_at,omitempty"`
	StartedAt          string `json:"started_at,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
	EndedAt            string `json:"ended_at,omitempty"`
	AgeMS              int64  `json:"age_ms,omitempty"`
	DurationMS         int64  `json:"duration_ms,omitempty"`
}

type probeSpeedDebugResultPayload struct {
	Type        string                       `json:"type"`
	RequestID   string                       `json:"request_id,omitempty"`
	NodeID      string                       `json:"node_id,omitempty"`
	OK          bool                         `json:"ok"`
	Scope       string                       `json:"scope,omitempty"`
	ActiveCount int                          `json:"active_count"`
	Active      []probeSpeedDebugItemPayload `json:"active"`
	RecentCount int                          `json:"recent_count"`
	Recent      []probeSpeedDebugItemPayload `json:"recent"`
	FetchedAt   string                       `json:"fetched_at,omitempty"`
	Error       string                       `json:"error,omitempty"`
	Timestamp   string                       `json:"timestamp,omitempty"`
}

type probeSpeedDebugState struct {
	mu     sync.Mutex
	seq    atomic.Uint64
	active map[string]*probeSpeedDebugItem
	recent []probeSpeedDebugItemPayload
}

type probeSpeedDebugItem struct {
	id             string
	chainID        string
	role           string
	side           string
	transport      string
	remoteAddr     string
	status         string
	errText        string
	requestedBytes int64
	chunkBytes     int64
	startedAt      time.Time
	updatedAt      time.Time

	bytes              atomic.Int64
	remainingBytes     atomic.Int64
	writeCalls         atomic.Int64
	totalWriteBlockMS  atomic.Int64
	maxWriteBlockMS    atomic.Int64
	lastWriteBlockMS   atomic.Int64
	lastWriteBlockedAt atomic.Int64
}

type probeSpeedDebugBeginOptions struct {
	ChainID        string
	Role           string
	Side           string
	Transport      string
	RemoteAddr     string
	RequestedBytes int64
	ChunkBytes     int64
}

var globalProbeSpeedDebugState = newProbeSpeedDebugState()

func newProbeSpeedDebugState() *probeSpeedDebugState {
	return &probeSpeedDebugState{active: map[string]*probeSpeedDebugItem{}}
}

func (s *probeSpeedDebugState) begin(opts probeSpeedDebugBeginOptions) *probeSpeedDebugItem {
	if s == nil {
		return nil
	}
	now := time.Now().UTC()
	id := "speed-" + strings.ToLower(randomHexToken(6)) + "-" + time.Now().Format("150405.000")
	item := &probeSpeedDebugItem{
		id:             id,
		chainID:        strings.TrimSpace(opts.ChainID),
		role:           strings.TrimSpace(opts.Role),
		side:           firstNonEmpty(strings.TrimSpace(opts.Side), "remote"),
		transport:      strings.TrimSpace(opts.Transport),
		remoteAddr:     strings.TrimSpace(opts.RemoteAddr),
		status:         "running",
		requestedBytes: opts.RequestedBytes,
		chunkBytes:     opts.ChunkBytes,
		startedAt:      now,
		updatedAt:      now,
	}
	item.remainingBytes.Store(opts.RequestedBytes)
	s.mu.Lock()
	if s.active == nil {
		s.active = map[string]*probeSpeedDebugItem{}
	}
	s.active[id] = item
	s.mu.Unlock()
	return item
}

func (i *probeSpeedDebugItem) recordWrite(written int, blocked time.Duration, remaining int64) {
	if i == nil {
		return
	}
	now := time.Now().UTC()
	if written > 0 {
		i.bytes.Add(int64(written))
	}
	i.remainingBytes.Store(remaining)
	i.writeCalls.Add(1)
	blockMS := probeDurationMilliseconds(blocked)
	i.totalWriteBlockMS.Add(blockMS)
	i.lastWriteBlockMS.Store(blockMS)
	updateProbeSpeedDebugMax(&i.maxWriteBlockMS, blockMS)
	if blockMS > 0 {
		i.lastWriteBlockedAt.Store(now.Unix())
	}
	i.updatedAt = now
}

func (s *probeSpeedDebugState) end(item *probeSpeedDebugItem, status string, err error) {
	if s == nil || item == nil {
		return
	}
	now := time.Now().UTC()
	item.status = firstNonEmpty(strings.TrimSpace(status), "completed")
	item.updatedAt = now
	if err != nil {
		item.errText = strings.TrimSpace(err.Error())
		if item.status == "" || item.status == "completed" {
			item.status = "failed"
		}
	}
	payload := item.snapshot(now)
	payload.EndedAt = now.Format(time.RFC3339)
	payload.DurationMS = probeDurationMilliseconds(now.Sub(item.startedAt))
	s.mu.Lock()
	delete(s.active, item.id)
	s.recent = append([]probeSpeedDebugItemPayload{payload}, s.recent...)
	if len(s.recent) > probeSpeedDebugMaxRecent {
		s.recent = append([]probeSpeedDebugItemPayload(nil), s.recent[:probeSpeedDebugMaxRecent]...)
	}
	s.mu.Unlock()
}

func (s *probeSpeedDebugState) snapshotPayload(nodeID string, requestID string) probeSpeedDebugResultPayload {
	payload := probeSpeedDebugResultPayload{
		Type:      "speed_debug_result",
		RequestID: strings.TrimSpace(requestID),
		NodeID:    strings.TrimSpace(nodeID),
		OK:        true,
		Scope:     "probe",
		Active:    []probeSpeedDebugItemPayload{},
		Recent:    []probeSpeedDebugItemPayload{},
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if s == nil {
		return payload
	}
	now := time.Now().UTC()
	s.mu.Lock()
	active := make([]*probeSpeedDebugItem, 0, len(s.active))
	for _, item := range s.active {
		if item != nil {
			active = append(active, item)
		}
	}
	recent := append([]probeSpeedDebugItemPayload(nil), s.recent...)
	s.mu.Unlock()
	for _, item := range active {
		payload.Active = append(payload.Active, item.snapshot(now))
	}
	sort.Slice(payload.Active, func(i, j int) bool {
		return payload.Active[i].StartedAt > payload.Active[j].StartedAt
	})
	payload.Recent = recent
	payload.ActiveCount = len(payload.Active)
	payload.RecentCount = len(payload.Recent)
	return payload
}

func (i *probeSpeedDebugItem) snapshot(now time.Time) probeSpeedDebugItemPayload {
	if i == nil {
		return probeSpeedDebugItemPayload{}
	}
	bytesSent := i.bytes.Load()
	duration := now.Sub(i.startedAt)
	if duration <= 0 {
		duration = time.Millisecond
	}
	item := probeSpeedDebugItemPayload{
		ID:                strings.TrimSpace(i.id),
		ChainID:           strings.TrimSpace(i.chainID),
		Role:              strings.TrimSpace(i.role),
		Side:              strings.TrimSpace(i.side),
		Transport:         strings.TrimSpace(i.transport),
		RemoteAddr:        strings.TrimSpace(i.remoteAddr),
		Status:            firstNonEmpty(strings.TrimSpace(i.status), "running"),
		Error:             strings.TrimSpace(i.errText),
		RequestedBytes:    i.requestedBytes,
		ChunkBytes:        i.chunkBytes,
		Bytes:             bytesSent,
		RemainingBytes:    i.remainingBytes.Load(),
		RateBPS:           int64(float64(bytesSent) / duration.Seconds()),
		WriteCalls:        i.writeCalls.Load(),
		TotalWriteBlockMS: i.totalWriteBlockMS.Load(),
		MaxWriteBlockMS:   i.maxWriteBlockMS.Load(),
		LastWriteBlockMS:  i.lastWriteBlockMS.Load(),
		StartedAt:         i.startedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         i.updatedAt.UTC().Format(time.RFC3339),
		AgeMS:             probeDurationMilliseconds(now.Sub(i.startedAt)),
		DurationMS:        probeDurationMilliseconds(duration),
	}
	if lastBlocked := i.lastWriteBlockedAt.Load(); lastBlocked > 0 {
		item.LastWriteBlockedAt = time.Unix(lastBlocked, 0).UTC().Format(time.RFC3339)
	}
	return item
}

func updateProbeSpeedDebugMax(target *atomic.Int64, value int64) {
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

func runProbeSpeedDebugFetch(cmd probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	requestID := strings.TrimSpace(cmd.RequestID)
	if requestID == "" {
		return
	}
	payload := globalProbeSpeedDebugState.snapshotPayload(strings.TrimSpace(identity.NodeID), requestID)
	if writeErr := writeProbeStreamJSON(stream, encoder, writeMu, payload); writeErr != nil {
		log.Printf("probe speed debug response send failed: request_id=%s err=%v", requestID, writeErr)
	}
}

type probeLocalProxyLinkSpeedStatus struct {
	ChainID   string                           `json:"chain_id"`
	UpdatedAt string                           `json:"updated_at,omitempty"`
	Results   []probeChainRelaySpeedTestResult `json:"results,omitempty"`
}

var probeLocalProxyLinkSpeedState = struct {
	mu    sync.Mutex
	items map[string]probeLocalProxyLinkSpeedStatus
}{items: map[string]probeLocalProxyLinkSpeedStatus{}}

func recordProbeLocalProxyLinkSpeedStatus(chainID string, results []probeChainRelaySpeedTestResult) {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return
	}
	copied := append([]probeChainRelaySpeedTestResult(nil), results...)
	probeLocalProxyLinkSpeedState.mu.Lock()
	if probeLocalProxyLinkSpeedState.items == nil {
		probeLocalProxyLinkSpeedState.items = map[string]probeLocalProxyLinkSpeedStatus{}
	}
	probeLocalProxyLinkSpeedState.items[strings.ToLower(cleanID)] = probeLocalProxyLinkSpeedStatus{
		ChainID:   cleanID,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Results:   copied,
	}
	probeLocalProxyLinkSpeedState.mu.Unlock()
}

func snapshotProbeLocalProxyLinkSpeedStatus(chainID string) *probeLocalProxyLinkSpeedStatus {
	cleanID := strings.ToLower(strings.TrimSpace(chainID))
	if cleanID == "" {
		return nil
	}
	probeLocalProxyLinkSpeedState.mu.Lock()
	defer probeLocalProxyLinkSpeedState.mu.Unlock()
	item, ok := probeLocalProxyLinkSpeedState.items[cleanID]
	if !ok {
		return nil
	}
	item.Results = append([]probeChainRelaySpeedTestResult(nil), item.Results...)
	return &item
}
