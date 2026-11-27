package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeAdaptiveQueueObservesTimingRetriesAndDrops(t *testing.T) {
	q := newProbeAdaptiveQueue[int](probeAdaptiveQueueOptions{InitialCapacity: 1, MaxCapacity: 1})
	defer q.Close()
	if !q.TryPush(1) {
		t.Fatal("initial push")
	}
	if err := q.PushUntil(2, time.Now().Add(5*time.Millisecond), nil); !errors.Is(err, errProbeAdaptiveQueueFull) {
		t.Fatalf("err=%v", err)
	}
	s := q.adaptiveQueueSnapshot(time.Now())
	if s.Dropped == nil || *s.Dropped != 1 || s.RetryEvents == nil || *s.RetryEvents != 0 || s.BacklogMS < 1 || s.OldestWaitUS < 1000 {
		t.Fatalf("snapshot=%+v", s)
	}
	q.TryPop()
	s = q.adaptiveQueueSnapshot(time.Now())
	if s.WaitSamples != 1 || s.WaitMaxUS < 1000 || s.BacklogMS != 0 {
		t.Fatalf("timing=%+v", s)
	}
	q.resetAdaptiveQueueStats(time.Now())
	s = q.adaptiveQueueSnapshot(time.Now())
	if *s.Dropped != 0 || *s.RetryEvents != 0 || s.WaitSamples != 0 || s.WaitMaxUS != 0 {
		t.Fatalf("reset=%+v", s)
	}
}

func TestProbeAdaptiveQueueSuccessfulRetryIsNotDropped(t *testing.T) {
	q := newProbeAdaptiveQueue[int](probeAdaptiveQueueOptions{InitialCapacity: 1, MaxCapacity: 1})
	defer q.Close()
	q.TryPush(1)
	done := make(chan error, 1)
	go func() { done <- q.PushUntil(2, time.Now().Add(time.Second), nil) }()
	deadline := time.Now().Add(time.Second)
	for q.adaptiveQueueSnapshot(time.Now()).FullEvents == 0 {
		if time.Now().After(deadline) {
			t.Fatal("producer did not wait")
		}
		time.Sleep(time.Millisecond)
	}
	q.TryPop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s := q.adaptiveQueueSnapshot(time.Now())
	if *s.RetryEvents == 0 || *s.Dropped != 0 || s.FullEvents == 0 {
		t.Fatalf("retry reported as a drop: %+v", s)
	}
}

func TestProbeAdaptiveQueueGrowsToHardLimitAndPreservesOrder(t *testing.T) {
	queue := newProbeAdaptiveQueue[int](probeAdaptiveQueueOptions{
		ID:              "test.adaptive.grow",
		Stage:           "test",
		InitialCapacity: 2,
		MaxCapacity:     8,
	})
	t.Cleanup(queue.Close)
	for value := 1; value <= 8; value++ {
		if !queue.TryPush(value) {
			t.Fatalf("push %d failed before hard limit", value)
		}
	}
	if queue.TryPush(9) {
		t.Fatal("push beyond hard limit should fail")
	}
	if got := queue.Capacity(); got != 8 {
		t.Fatalf("capacity=%d, want 8", got)
	}
	for want := 1; want <= 8; want++ {
		got, ok := queue.TryPop()
		if !ok || got != want {
			t.Fatalf("pop=(%d,%v), want (%d,true)", got, ok, want)
		}
	}
	snapshot := queue.adaptiveQueueSnapshot(time.Now())
	if snapshot.PeakDepth != 8 || snapshot.PeakAllocatedCapacity != 8 || snapshot.FullEvents != 1 || snapshot.GrowEvents != 2 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestProbeAdaptiveQueueShrinksAfterSustainedLowWater(t *testing.T) {
	queue := newProbeAdaptiveQueue[int](probeAdaptiveQueueOptions{
		ID:              "test.adaptive.shrink",
		Stage:           "test",
		InitialCapacity: 2,
		MaxCapacity:     16,
		ShrinkDelay:     time.Millisecond,
	})
	t.Cleanup(queue.Close)
	for value := 0; value < 16; value++ {
		if !queue.TryPush(value) {
			t.Fatalf("push %d failed", value)
		}
	}
	for value := 0; value < 15; value++ {
		if _, ok := queue.TryPop(); !ok {
			t.Fatalf("pop %d failed", value)
		}
	}
	queue.mu.Lock()
	queue.lowSince = time.Now().Add(-time.Second)
	queue.mu.Unlock()
	snapshot := queue.adaptiveQueueSnapshot(time.Now())
	if snapshot.AllocatedCapacity != 2 || snapshot.Depth != 1 || snapshot.ShrinkEvents != 1 {
		t.Fatalf("unexpected shrunken snapshot: %+v", snapshot)
	}
	if got, ok := queue.TryPop(); !ok || got != 15 {
		t.Fatalf("remaining value=(%d,%v), want (15,true)", got, ok)
	}
}

func TestProbeAdaptiveQueueResetStartsPeaksFromCurrentUsage(t *testing.T) {
	queue := newProbeAdaptiveQueue[int](probeAdaptiveQueueOptions{
		ID:              "test.adaptive.reset",
		Stage:           "test",
		InitialCapacity: 1,
		MaxCapacity:     4,
	})
	t.Cleanup(queue.Close)
	for value := 0; value < 3; value++ {
		if !queue.TryPush(value) {
			t.Fatalf("push %d failed", value)
		}
	}
	queue.resetAdaptiveQueueStats(time.Now())
	snapshot := queue.adaptiveQueueSnapshot(time.Now())
	if snapshot.PeakDepth != 3 || snapshot.PeakAllocatedCapacity != 4 || snapshot.Enqueued != 0 || snapshot.GrowEvents != 0 {
		t.Fatalf("unexpected reset snapshot: %+v", snapshot)
	}
}

func TestProbeAdaptiveQueuePushUntilHonorsDeadline(t *testing.T) {
	queue := newProbeAdaptiveQueue[int](probeAdaptiveQueueOptions{
		ID:              "test.adaptive.deadline",
		Stage:           "test",
		InitialCapacity: 1,
		MaxCapacity:     1,
	})
	t.Cleanup(queue.Close)
	if !queue.TryPush(1) {
		t.Fatal("initial push failed")
	}
	err := queue.PushUntil(2, time.Now().Add(5*time.Millisecond), make(chan struct{}))
	if !errors.Is(err, errProbeAdaptiveQueueFull) {
		t.Fatalf("PushUntil error=%v, want queue full", err)
	}
}

func TestProbeAdaptiveQueueConcurrentProducersAndConsumers(t *testing.T) {
	queue := newProbeAdaptiveQueue[int](probeAdaptiveQueueOptions{
		ID: "test.adaptive.concurrent", Stage: "test", InitialCapacity: 4, MaxCapacity: 128,
	})
	t.Cleanup(queue.Close)
	stop := make(chan struct{})
	const producers = 8
	const valuesPerProducer = 1000
	const consumers = 4
	var produced atomic.Int64
	var consumed atomic.Int64
	var stopOnce sync.Once
	var producerWG sync.WaitGroup
	var consumerWG sync.WaitGroup
	consumerWG.Add(consumers)
	for consumer := 0; consumer < consumers; consumer++ {
		go func() {
			defer consumerWG.Done()
			for {
				if consumed.Load() >= producers*valuesPerProducer {
					return
				}
				if _, ok := queue.PopUntil(stop, time.Now().Add(2*time.Second)); ok {
					if consumed.Add(1) == producers*valuesPerProducer {
						stopOnce.Do(func() { close(stop) })
					}
					continue
				}
				if produced.Load() >= producers*valuesPerProducer && queue.Len() == 0 {
					return
				}
			}
		}()
	}
	producerWG.Add(producers)
	for producer := 0; producer < producers; producer++ {
		go func(base int) {
			defer producerWG.Done()
			for offset := 0; offset < valuesPerProducer; offset++ {
				if err := queue.PushUntil(base+offset, time.Now().Add(2*time.Second), stop); err != nil {
					t.Errorf("concurrent push failed: %v", err)
					return
				}
				produced.Add(1)
			}
		}(producer * valuesPerProducer)
	}
	producerWG.Wait()
	consumerWG.Wait()
	stopOnce.Do(func() { close(stop) })
	if produced.Load() != producers*valuesPerProducer || consumed.Load() != producers*valuesPerProducer || queue.Len() != 0 {
		t.Fatalf("concurrent counts produced=%d consumed=%d depth=%d", produced.Load(), consumed.Load(), queue.Len())
	}
}

func TestProbeVRouteProxyTCPInboundUsesAdaptiveQueue(t *testing.T) {
	queue := newProbeVRouteProxyTCPInboundQueue("session-test", "source")
	t.Cleanup(queue.Close)
	if queue.Capacity() != probeVRouteProxyTCPInboundInitial || queue.MaxCapacity() != probeVRouteProxyTCPInboundQueueSize {
		t.Fatalf("proxy inbound capacity=%d/%d, want %d/%d", queue.Capacity(), queue.MaxCapacity(), probeVRouteProxyTCPInboundInitial, probeVRouteProxyTCPInboundQueueSize)
	}
	snapshot := queue.adaptiveQueueSnapshot(time.Now())
	if snapshot.Stage != "vroute_proxy_tcp_inbound" || snapshot.Direction != "rx" {
		t.Fatalf("unexpected proxy inbound snapshot: %+v", snapshot)
	}
}
