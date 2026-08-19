package core

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeRouteConfigSyncSourceLogRateLimit(t *testing.T) {
	const source = "test:rate-limit"
	probeRouteConfigSyncSourceLog.mu.Lock()
	delete(probeRouteConfigSyncSourceLog.last, source)
	probeRouteConfigSyncSourceLog.mu.Unlock()
	t.Cleanup(func() {
		probeRouteConfigSyncSourceLog.mu.Lock()
		delete(probeRouteConfigSyncSourceLog.last, source)
		probeRouteConfigSyncSourceLog.mu.Unlock()
	})

	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	if !shouldLogProbeRouteConfigSyncSource(source, now) {
		t.Fatal("first source event was suppressed")
	}
	if shouldLogProbeRouteConfigSyncSource(source, now.Add(9*time.Second)) {
		t.Fatal("source event inside rate limit window was logged")
	}
	if !shouldLogProbeRouteConfigSyncSource(source, now.Add(10*time.Second)) {
		t.Fatal("source event at rate limit boundary was suppressed")
	}
}

func TestProbeRouteConfigSyncSchedulerCoalescesBurst(t *testing.T) {
	started := make(chan int32, 2)
	release := make(chan struct{})
	var runs atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	scheduler := newProbeRouteConfigSyncScheduler(func() {
		run := runs.Add(1)
		current := active.Add(1)
		for {
			maximum := maxActive.Load()
			if current <= maximum || maxActive.CompareAndSwap(maximum, current) {
				break
			}
		}
		started <- run
		<-release
		active.Add(-1)
	})

	scheduler.Schedule()
	waitProbeRouteConfigSyncRun(t, started, 1)
	for range 100 {
		scheduler.Schedule()
	}
	release <- struct{}{}
	waitProbeRouteConfigSyncRun(t, started, 2)
	release <- struct{}{}
	waitProbeRouteConfigSyncSchedulerIdle(t, scheduler)

	if got := runs.Load(); got != 2 {
		t.Fatalf("runs=%d, want 2", got)
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max active=%d, want 1", got)
	}
}

func waitProbeRouteConfigSyncRun(t *testing.T, started <-chan int32, want int32) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("run=%d, want %d", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for run %d", want)
	}
}

func waitProbeRouteConfigSyncSchedulerIdle(t *testing.T, scheduler *probeRouteConfigSyncScheduler) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		scheduler.mu.Lock()
		idle := !scheduler.running && !scheduler.pending
		scheduler.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("scheduler did not become idle")
}
