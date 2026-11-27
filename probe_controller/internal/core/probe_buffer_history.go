package core

import (
	"sync"
	"time"
)

// Recent counters live only in controller RAM. Reading a page never adds a
// sample. A reporter restart, reset, counter rollback or queue recreation starts
// a new baseline, so missing history cannot masquerade as zero loss.
type probeBufferPoint struct {
	at   time.Time
	full uint64
}
type probeBufferQueueHistory struct {
	epoch  string
	points []probeBufferPoint
}
type probeBufferHistoryStore struct {
	mu    sync.Mutex
	nodes map[string]map[string]probeBufferQueueHistory
}
type probeBufferRecent struct {
	Full1M   *uint64 `json:"full_1m"`
	Full5M   *uint64 `json:"full_5m"`
	Window1S int64   `json:"window_1s,omitempty"`
	Window5S int64   `json:"window_5s,omitempty"`
}

func newProbeBufferHistory() *probeBufferHistoryStore {
	return &probeBufferHistoryStore{nodes: map[string]map[string]probeBufferQueueHistory{}}
}

var probeBufferHistory = newProbeBufferHistory()

func (h *probeBufferHistoryStore) observe(node string, report probeVirtualRouterBufferReport) {
	at, err := time.Parse(time.RFC3339Nano, report.CollectedAt)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.nodes[node]
	next := make(map[string]probeBufferQueueHistory, len(report.Items))
	for i, q := range report.Items {
		if i >= 2048 {
			break
		}
		epoch := report.StatsStartedAt + "|" + q.StatsStartedAt
		v := old[q.ID]
		if v.epoch != epoch {
			v = probeBufferQueueHistory{epoch: epoch}
		}
		if len(v.points) > 0 {
			last := v.points[len(v.points)-1]
			if !at.After(last.at) {
				next[q.ID] = v
				continue
			}
			if q.FullEvents < last.full {
				v.points = nil
			} else if at.Sub(last.at) < 5*time.Second {
				next[q.ID] = v
				continue
			}
		}
		cut := at.Add(-7 * time.Minute)
		begin := 0
		for begin < len(v.points) && v.points[begin].at.Before(cut) {
			begin++
		}
		points := append([]probeBufferPoint(nil), v.points[begin:]...)
		points = append(points, probeBufferPoint{at, q.FullEvents})
		if len(points) > 80 {
			points = points[len(points)-80:]
		}
		v.points = points
		next[q.ID] = v
	}
	if len(h.nodes) >= 256 && old == nil {
		for id := range h.nodes {
			delete(h.nodes, id)
			break
		}
	}
	h.nodes[node] = next
}

func (h *probeBufferHistoryStore) recent(node string, report probeVirtualRouterBufferReport, now time.Time) map[string]probeBufferRecent {
	out := map[string]probeBufferRecent{}
	at, err := time.Parse(time.RFC3339Nano, report.CollectedAt)
	if err != nil || now.Sub(at) > 2*time.Minute || at.Sub(now) > 2*time.Minute {
		return out
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, q := range report.Items {
		v := h.nodes[node][q.ID]
		if v.epoch != report.StatsStartedAt+"|"+q.StatsStartedAt {
			continue
		}
		var r probeBufferRecent
		for _, window := range []time.Duration{time.Minute, 5 * time.Minute} {
			cut := at.Add(-window)
			var baseline *probeBufferPoint
			for i := range v.points {
				p := &v.points[i]
				if !p.at.After(cut) {
					baseline = p
				}
			}
			if baseline == nil || cut.Sub(baseline.at) > 90*time.Second || q.FullEvents < baseline.full {
				continue
			}
			delta := q.FullEvents - baseline.full
			span := int64(at.Sub(baseline.at).Seconds())
			if window == time.Minute {
				r.Full1M = &delta
				r.Window1S = span
			} else {
				r.Full5M = &delta
				r.Window5S = span
			}
		}
		out[q.ID] = r
	}
	return out
}
