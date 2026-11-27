package core

import (
	"testing"
	"time"
)

func TestBufferHistoryWindowsResetAndStale(t *testing.T) {
	h := newProbeBufferHistory()
	start := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	report := func(at time.Time, epoch string, full uint64) probeVirtualRouterBufferReport {
		return probeVirtualRouterBufferReport{CollectedAt: at.Format(time.RFC3339Nano), StatsStartedAt: epoch, Items: []probeAdaptiveQueueSnapshot{{ID: "q", Stage: "carrier_tx", StatsStartedAt: epoch, FullEvents: full}}}
	}
	for i := 0; i <= 10; i++ {
		h.observe("1", report(start.Add(time.Duration(i)*30*time.Second), "epoch1", uint64(i*3)))
	}
	r := h.recent("1", report(start.Add(5*time.Minute), "epoch1", 30), start.Add(5*time.Minute))
	if r["q"].Full1M == nil || *r["q"].Full1M != 6 || r["q"].Full5M == nil || *r["q"].Full5M != 30 {
		t.Fatalf("recent=%+v", r["q"])
	}
	// Repeated GETs and duplicate reports cannot manufacture samples.
	h.observe("1", report(start.Add(5*time.Minute), "epoch1", 30))
	if h.recent("1", report(start.Add(5*time.Minute), "epoch1", 30), start.Add(8*time.Minute))["q"].Full1M != nil {
		t.Fatal("stale data reported as a current zero")
	}
	h.observe("1", report(start.Add(6*time.Minute), "epoch2", 1))
	r = h.recent("1", report(start.Add(6*time.Minute), "epoch2", 1), start.Add(6*time.Minute))
	if r["q"].Full1M != nil || r["q"].Full5M != nil {
		t.Fatal("restart inherited old counters")
	}
}

func TestBufferHistoryCounterRollbackAndPartialCoverage(t *testing.T) {
	h := newProbeBufferHistory()
	start := time.Now().UTC().Truncate(time.Second)
	r := probeVirtualRouterBufferReport{StatsStartedAt: "epoch", CollectedAt: start.Format(time.RFC3339), Items: []probeAdaptiveQueueSnapshot{{ID: "q", Stage: "carrier_tx", FullEvents: 100}}}
	h.observe("1", r)
	r.CollectedAt = start.Add(30 * time.Second).Format(time.RFC3339)
	r.Items[0].FullEvents = 110
	h.observe("1", r)
	if h.recent("1", r, start.Add(30*time.Second))["q"].Full1M != nil {
		t.Fatal("30s of history presented as 1 minute")
	}
	r.CollectedAt = start.Add(time.Minute).Format(time.RFC3339)
	r.Items[0].FullEvents = 1
	h.observe("1", r)
	if h.recent("1", r, start.Add(time.Minute))["q"].Full1M != nil {
		t.Fatal("counter rollback retained old baseline")
	}
	if h.recent("2", r, start.Add(time.Minute))["q"].Full1M != nil {
		t.Fatal("node history leaked")
	}
}
