package main

import (
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

const probeAdaptiveQueueDefaultShrinkDelay = 2 * time.Minute

var errProbeAdaptiveQueueFull = errors.New("adaptive queue reached its hard limit")

type probeAdaptiveQueueOptions struct {
	ID              string
	Stage           string
	Direction       string
	RouteID         string
	CarrierSlot     int
	HasCarrierSlot  bool
	Shard           int
	HasShard        bool
	InitialCapacity int
	MaxCapacity     int
	ShrinkDelay     time.Duration
}

type probeAdaptiveQueueSnapshot struct {
	ID                    string `json:"id"`
	Stage                 string `json:"stage"`
	Direction             string `json:"direction,omitempty"`
	RouteID               string `json:"route_id,omitempty"`
	CarrierSlot           *int   `json:"carrier_slot,omitempty"`
	Shard                 *int   `json:"shard,omitempty"`
	Depth                 int    `json:"depth"`
	AllocatedCapacity     int    `json:"allocated_capacity"`
	MaxCapacity           int    `json:"max_capacity"`
	PeakDepth             int    `json:"peak_depth"`
	PeakAllocatedCapacity int    `json:"peak_allocated_capacity"`
	Enqueued              uint64 `json:"enqueued"`
	Dequeued              uint64 `json:"dequeued"`
	FullEvents            uint64 `json:"full_events"`
	GrowEvents            uint64 `json:"grow_events"`
	ShrinkEvents          uint64 `json:"shrink_events"`
	StatsStartedAt        string `json:"stats_started_at"`
}

type probeVirtualRouterBufferReport struct {
	StatsStartedAt string                       `json:"stats_started_at"`
	CollectedAt    string                       `json:"collected_at"`
	Items          []probeAdaptiveQueueSnapshot `json:"items"`
}

type probeAdaptiveQueueObservable interface {
	adaptiveQueueSnapshot(time.Time) probeAdaptiveQueueSnapshot
	resetAdaptiveQueueStats(time.Time)
}

var probeVirtualRouterBufferRegistry = struct {
	mu        sync.Mutex
	startedAt time.Time
	items     map[string]probeAdaptiveQueueObservable
}{
	startedAt: time.Now(),
	items:     make(map[string]probeAdaptiveQueueObservable),
}

type probeAdaptiveQueue[T any] struct {
	options probeAdaptiveQueueOptions

	mu       sync.Mutex
	buffer   []T
	head     int
	size     int
	closed   bool
	lowSince time.Time

	peakDepth             int
	peakAllocatedCapacity int
	enqueued              uint64
	dequeued              uint64
	fullEvents            uint64
	growEvents            uint64
	shrinkEvents          uint64
	statsStartedAt        time.Time

	ready chan struct{}
	space chan struct{}
}

func newProbeAdaptiveQueue[T any](options probeAdaptiveQueueOptions) *probeAdaptiveQueue[T] {
	options.ID = strings.TrimSpace(options.ID)
	options.Stage = strings.TrimSpace(options.Stage)
	options.Direction = strings.TrimSpace(options.Direction)
	options.RouteID = strings.TrimSpace(options.RouteID)
	if options.InitialCapacity <= 0 {
		options.InitialCapacity = 1
	}
	if options.MaxCapacity < options.InitialCapacity {
		options.MaxCapacity = options.InitialCapacity
	}
	if options.ShrinkDelay <= 0 {
		options.ShrinkDelay = probeAdaptiveQueueDefaultShrinkDelay
	}
	now := time.Now()
	queue := &probeAdaptiveQueue[T]{
		options:               options,
		buffer:                make([]T, options.InitialCapacity),
		peakAllocatedCapacity: options.InitialCapacity,
		statsStartedAt:        now,
		ready:                 make(chan struct{}, 1),
		space:                 make(chan struct{}, 1),
	}
	if options.ID != "" {
		probeVirtualRouterBufferRegistry.mu.Lock()
		if probeVirtualRouterBufferRegistry.startedAt.IsZero() {
			probeVirtualRouterBufferRegistry.startedAt = now
		}
		queue.statsStartedAt = probeVirtualRouterBufferRegistry.startedAt
		probeVirtualRouterBufferRegistry.items[options.ID] = queue
		probeVirtualRouterBufferRegistry.mu.Unlock()
	}
	return queue
}

func (q *probeAdaptiveQueue[T]) Ready() <-chan struct{} {
	if q == nil {
		return nil
	}
	return q.ready
}

func (q *probeAdaptiveQueue[T]) SpaceReady() <-chan struct{} {
	if q == nil {
		return nil
	}
	return q.space
}

func (q *probeAdaptiveQueue[T]) TryPush(value T) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	if q.size == len(q.buffer) && !q.growLocked() {
		q.fullEvents++
		q.mu.Unlock()
		return false
	}
	index := (q.head + q.size) % len(q.buffer)
	q.buffer[index] = value
	q.size++
	q.enqueued++
	if q.size > q.peakDepth {
		q.peakDepth = q.size
	}
	q.updateLowWaterLocked(time.Now())
	q.mu.Unlock()
	signalProbeAdaptiveQueue(q.ready)
	return true
}

func (q *probeAdaptiveQueue[T]) PushUntil(value T, deadline time.Time, stop <-chan struct{}) error {
	if q == nil {
		return io.ErrClosedPipe
	}
	for {
		if q.TryPush(value) {
			return nil
		}
		if q.IsClosed() {
			return io.ErrClosedPipe
		}
		if !deadline.IsZero() {
			wait := time.Until(deadline)
			if wait <= 0 {
				return errProbeAdaptiveQueueFull
			}
			timer := time.NewTimer(wait)
			select {
			case <-q.space:
				stopProbeAdaptiveQueueTimer(timer)
			case <-stop:
				stopProbeAdaptiveQueueTimer(timer)
				return io.ErrClosedPipe
			case <-timer.C:
				return errProbeAdaptiveQueueFull
			}
			continue
		}
		select {
		case <-q.space:
		case <-stop:
			return io.ErrClosedPipe
		}
	}
}

func (q *probeAdaptiveQueue[T]) TryPop() (T, bool) {
	var zero T
	if q == nil {
		return zero, false
	}
	q.mu.Lock()
	if q.size == 0 {
		q.mu.Unlock()
		return zero, false
	}
	value := q.buffer[q.head]
	q.buffer[q.head] = zero
	q.head = (q.head + 1) % len(q.buffer)
	q.size--
	q.dequeued++
	remaining := q.size
	q.updateLowWaterLocked(time.Now())
	q.mu.Unlock()
	if remaining > 0 {
		signalProbeAdaptiveQueue(q.ready)
	}
	signalProbeAdaptiveQueue(q.space)
	return value, true
}

func (q *probeAdaptiveQueue[T]) Pop(stop <-chan struct{}) (T, bool) {
	var zero T
	if q == nil {
		return zero, false
	}
	for {
		if value, ok := q.TryPop(); ok {
			return value, true
		}
		if q.IsClosed() {
			return zero, false
		}
		select {
		case <-q.ready:
		case <-stop:
			return zero, false
		}
	}
}

func (q *probeAdaptiveQueue[T]) PopUntil(stop <-chan struct{}, deadline time.Time) (T, bool) {
	var zero T
	if q == nil {
		return zero, false
	}
	for {
		if value, ok := q.TryPop(); ok {
			return value, true
		}
		if q.IsClosed() {
			return zero, false
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return zero, false
		}
		timer := time.NewTimer(wait)
		select {
		case <-q.ready:
			stopProbeAdaptiveQueueTimer(timer)
		case <-stop:
			stopProbeAdaptiveQueueTimer(timer)
			return zero, false
		case <-timer.C:
			return zero, false
		}
	}
}

func (q *probeAdaptiveQueue[T]) WaitBelow(limit int, deadline time.Time, stop <-chan struct{}) error {
	if q == nil {
		return io.ErrClosedPipe
	}
	for {
		if q.Len() <= limit {
			return nil
		}
		if q.IsClosed() {
			return io.ErrClosedPipe
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return errProbeAdaptiveQueueFull
		}
		timer := time.NewTimer(wait)
		select {
		case <-q.space:
			stopProbeAdaptiveQueueTimer(timer)
		case <-stop:
			stopProbeAdaptiveQueueTimer(timer)
			return io.ErrClosedPipe
		case <-timer.C:
			return errProbeAdaptiveQueueFull
		}
	}
}

func (q *probeAdaptiveQueue[T]) Drain(consume func(T)) int {
	if q == nil {
		return 0
	}
	count := 0
	for {
		value, ok := q.TryPop()
		if !ok {
			return count
		}
		count++
		if consume != nil {
			consume(value)
		}
	}
}

func (q *probeAdaptiveQueue[T]) Len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	value := q.size
	q.mu.Unlock()
	return value
}

func (q *probeAdaptiveQueue[T]) Capacity() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	value := len(q.buffer)
	q.mu.Unlock()
	return value
}

func (q *probeAdaptiveQueue[T]) MaxCapacity() int {
	if q == nil {
		return 0
	}
	return q.options.MaxCapacity
}

func (q *probeAdaptiveQueue[T]) IsClosed() bool {
	if q == nil {
		return true
	}
	q.mu.Lock()
	closed := q.closed
	q.mu.Unlock()
	return closed
}

func (q *probeAdaptiveQueue[T]) Close() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.mu.Unlock()
	signalProbeAdaptiveQueue(q.ready)
	signalProbeAdaptiveQueue(q.space)
	if q.options.ID != "" {
		probeVirtualRouterBufferRegistry.mu.Lock()
		if current := probeVirtualRouterBufferRegistry.items[q.options.ID]; current == q {
			delete(probeVirtualRouterBufferRegistry.items, q.options.ID)
		}
		probeVirtualRouterBufferRegistry.mu.Unlock()
	}
}

func (q *probeAdaptiveQueue[T]) growLocked() bool {
	current := len(q.buffer)
	if current >= q.options.MaxCapacity {
		return false
	}
	next := current * 2
	if next <= current {
		next = current + 1
	}
	if next > q.options.MaxCapacity {
		next = q.options.MaxCapacity
	}
	q.resizeLocked(next)
	q.growEvents++
	if next > q.peakAllocatedCapacity {
		q.peakAllocatedCapacity = next
	}
	return true
}

func (q *probeAdaptiveQueue[T]) resizeLocked(capacity int) {
	if capacity < q.size || capacity <= 0 || capacity == len(q.buffer) {
		return
	}
	next := make([]T, capacity)
	for i := 0; i < q.size; i++ {
		next[i] = q.buffer[(q.head+i)%len(q.buffer)]
	}
	q.buffer = next
	q.head = 0
}

func (q *probeAdaptiveQueue[T]) updateLowWaterLocked(now time.Time) {
	capacity := len(q.buffer)
	if capacity <= q.options.InitialCapacity || q.size*4 > capacity {
		q.lowSince = time.Time{}
		return
	}
	if q.lowSince.IsZero() {
		q.lowSince = now
	}
}

func (q *probeAdaptiveQueue[T]) maybeShrinkLocked(now time.Time) {
	q.updateLowWaterLocked(now)
	if q.lowSince.IsZero() || now.Sub(q.lowSince) < q.options.ShrinkDelay {
		return
	}
	target := q.options.InitialCapacity
	required := q.size * 2
	for target < required && target < q.options.MaxCapacity {
		target *= 2
		if target > q.options.MaxCapacity {
			target = q.options.MaxCapacity
		}
	}
	if target < len(q.buffer) {
		q.resizeLocked(target)
		q.shrinkEvents++
	}
	q.lowSince = time.Time{}
}

func (q *probeAdaptiveQueue[T]) adaptiveQueueSnapshot(now time.Time) probeAdaptiveQueueSnapshot {
	if q == nil {
		return probeAdaptiveQueueSnapshot{}
	}
	q.mu.Lock()
	q.maybeShrinkLocked(now)
	snapshot := probeAdaptiveQueueSnapshot{
		ID:                    q.options.ID,
		Stage:                 q.options.Stage,
		Direction:             q.options.Direction,
		RouteID:               q.options.RouteID,
		Depth:                 q.size,
		AllocatedCapacity:     len(q.buffer),
		MaxCapacity:           q.options.MaxCapacity,
		PeakDepth:             q.peakDepth,
		PeakAllocatedCapacity: q.peakAllocatedCapacity,
		Enqueued:              q.enqueued,
		Dequeued:              q.dequeued,
		FullEvents:            q.fullEvents,
		GrowEvents:            q.growEvents,
		ShrinkEvents:          q.shrinkEvents,
		StatsStartedAt:        q.statsStartedAt.UTC().Format(time.RFC3339),
	}
	if q.options.HasCarrierSlot {
		value := q.options.CarrierSlot
		snapshot.CarrierSlot = &value
	}
	if q.options.HasShard {
		value := q.options.Shard
		snapshot.Shard = &value
	}
	q.mu.Unlock()
	return snapshot
}

func (q *probeAdaptiveQueue[T]) resetAdaptiveQueueStats(now time.Time) {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.peakDepth = q.size
	q.peakAllocatedCapacity = len(q.buffer)
	q.enqueued = 0
	q.dequeued = 0
	q.fullEvents = 0
	q.growEvents = 0
	q.shrinkEvents = 0
	q.statsStartedAt = now
	q.lowSince = time.Time{}
	q.mu.Unlock()
}

func snapshotProbeVirtualRouterBuffers() probeVirtualRouterBufferReport {
	now := time.Now()
	probeVirtualRouterBufferRegistry.mu.Lock()
	startedAt := probeVirtualRouterBufferRegistry.startedAt
	items := make([]probeAdaptiveQueueObservable, 0, len(probeVirtualRouterBufferRegistry.items))
	for _, item := range probeVirtualRouterBufferRegistry.items {
		items = append(items, item)
	}
	probeVirtualRouterBufferRegistry.mu.Unlock()
	report := probeVirtualRouterBufferReport{
		StatsStartedAt: startedAt.UTC().Format(time.RFC3339),
		CollectedAt:    now.UTC().Format(time.RFC3339),
		Items:          make([]probeAdaptiveQueueSnapshot, 0, len(items)),
	}
	for _, item := range items {
		snapshot := item.adaptiveQueueSnapshot(now)
		if strings.TrimSpace(snapshot.ID) != "" {
			report.Items = append(report.Items, snapshot)
		}
	}
	sort.Slice(report.Items, func(i, j int) bool {
		if report.Items[i].Stage != report.Items[j].Stage {
			return report.Items[i].Stage < report.Items[j].Stage
		}
		return report.Items[i].ID < report.Items[j].ID
	})
	return report
}

func resetProbeVirtualRouterBufferStats() probeVirtualRouterBufferReport {
	now := time.Now()
	probeVirtualRouterBufferRegistry.mu.Lock()
	probeVirtualRouterBufferRegistry.startedAt = now
	items := make([]probeAdaptiveQueueObservable, 0, len(probeVirtualRouterBufferRegistry.items))
	for _, item := range probeVirtualRouterBufferRegistry.items {
		items = append(items, item)
	}
	probeVirtualRouterBufferRegistry.mu.Unlock()
	for _, item := range items {
		item.resetAdaptiveQueueStats(now)
	}
	return snapshotProbeVirtualRouterBuffers()
}

func signalProbeAdaptiveQueue(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func stopProbeAdaptiveQueueTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}
