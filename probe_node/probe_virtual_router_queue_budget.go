package main

import "sync"

const (
	probeVirtualRouterCarrierPayloadLimit   = 16 << 20
	probeVirtualRouterCarrierControlReserve = 256 << 10
)

var probeVirtualRouterCarrierBudget = probeQueueByteBudget{
	limit:          probeVirtualRouterCarrierPayloadLimit,
	controlReserve: probeVirtualRouterCarrierControlReserve,
}

type probeAdaptiveQueueByteAccounting[T any] struct {
	budget  *probeQueueByteBudget
	measure func(T) (bytes int, priority bool)
}

type probeQueueByteBudget struct {
	mu                    sync.Mutex
	limit, controlReserve int
	used, peak            int
	available             chan struct{}
}

type probeQueueByteBudgetSnapshot struct {
	LimitBytes          int `json:"limit_bytes"`
	ControlReserveBytes int `json:"control_reserve_bytes"`
	QueuedBytes         int `json:"queued_bytes"`
	PeakQueuedBytes     int `json:"peak_queued_bytes"`
}

func (b *probeQueueByteBudget) tryReserve(bytes int, priority, wait bool) (bool, <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit := b.limit
	if !priority {
		limit -= b.controlReserve
	}
	if bytes <= limit-b.used {
		b.used += bytes
		b.peak = max(b.peak, b.used)
		return true, nil
	}
	if !wait {
		return false, nil
	}
	if b.available == nil {
		b.available = make(chan struct{})
	}
	return false, b.available
}

func (b *probeQueueByteBudget) release(bytes int) {
	if bytes == 0 {
		return
	}
	b.mu.Lock()
	b.used -= bytes
	if b.available != nil {
		close(b.available)
		b.available = nil
	}
	b.mu.Unlock()
}

func (b *probeQueueByteBudget) snapshot() probeQueueByteBudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return probeQueueByteBudgetSnapshot{b.limit, b.controlReserve, b.used, b.peak}
}

func (b *probeQueueByteBudget) resetPeak() {
	b.mu.Lock()
	b.peak = b.used
	b.mu.Unlock()
}

func newProbeVirtualRouterFrameQueue(options probeAdaptiveQueueOptions) *probeAdaptiveQueue[probeVirtualRouterFrame] {
	return newProbeAdaptiveQueue(options, probeAdaptiveQueueByteAccounting[probeVirtualRouterFrame]{
		budget: &probeVirtualRouterCarrierBudget,
		measure: func(frame probeVirtualRouterFrame) (int, bool) {
			return probeVirtualRouterFrameEnvelopeHeaderSize + len(frame.Control) + len(frame.Data), probeVirtualRouterTXQueueName(frame) == "control"
		},
	})
}
