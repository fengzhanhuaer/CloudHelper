package mobilecore

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const mobileVRoutePathSampleMax = 64

type mobileVRoutePathSample struct {
	RTT         int64
	At, Attempt time.Time
}

var mobileVRoutePathSamples = struct {
	sync.Mutex
	items map[string]mobileVRoutePathSample
	busy  bool
	epoch uint64
	next  time.Time
}{items: make(map[string]mobileVRoutePathSample)}

// Enumerate only shortest paths, in stable order, with the same three-hop
// limit as the wire protocol. The cap also bounds background probe traffic.
func mobileVRouteShortestCandidates(config mobileVRouteConfig, from, to string) [][]string {
	from, to = normalizeMobileRouteNodeID(from), normalizeMobileRouteNodeID(to)
	if from == "" || to == "" {
		return nil
	}
	if from == to {
		return [][]string{{from}}
	}
	neighbors := map[string]map[string]bool{}
	for _, r := range config.TopologyRules {
		if !r.Enabled {
			continue
		}
		a, b := normalizeMobileRouteNodeID(r.FromNodeID), normalizeMobileRouteNodeID(r.ToNodeID)
		if a == "" || b == "" || a == b {
			continue
		}
		if neighbors[a] == nil {
			neighbors[a] = map[string]bool{}
		}
		if neighbors[b] == nil {
			neighbors[b] = map[string]bool{}
		}
		neighbors[a][b], neighbors[b][a] = true, true
	}
	// A known disconnected first hop is ineligible; initial on-demand dials
	// remain possible when there is no worker state yet.
	known, live := map[string]bool{}, map[string]bool{}
	mobileVRouteCarrierState.mu.Lock()
	for _, w := range mobileVRouteCarrierState.workers {
		if w.plan.LocalNode == from {
			known[w.plan.NextNode] = true
		}
	}
	for _, c := range mobileVRouteCarrierState.items {
		if c.plan.LocalNode == from {
			live[c.plan.NextNode] = true
		}
	}
	mobileVRouteCarrierState.mu.Unlock()
	dist := map[string]int{from: 0}
	parents := map[string][]string{}
	queue := []string{from}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		if dist[at] >= mobileVRouteMaxHops {
			continue
		}
		ids := make([]string, 0, len(neighbors[at]))
		for id := range neighbors[at] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, next := range ids {
			if at == from && known[next] && !live[next] {
				continue
			}
			d, seen := dist[next]
			if !seen {
				dist[next] = dist[at] + 1
				queue = append(queue, next)
				d = dist[next]
			}
			if d == dist[at]+1 {
				parents[next] = append(parents[next], at)
			}
		}
	}
	if _, ok := dist[to]; !ok {
		return nil
	}
	var out [][]string
	var walk func(string, []string)
	walk = func(at string, suffix []string) {
		if len(out) >= mobileVRoutePathSampleMax {
			return
		}
		path := append([]string{at}, suffix...)
		if at == from {
			out = append(out, path)
			return
		}
		for _, p := range parents[at] {
			walk(p, path)
		}
	}
	walk(to, nil)
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i], ">") < strings.Join(out[j], ">") })
	return out
}

func mobileVRouteRuleRTT(rule mobileVRouteTopology) (bool, int64) {
	mobileVRouteCarrierState.mu.Lock()
	defer mobileVRouteCarrierState.mu.Unlock()
	live := false
	var best int64
	for _, c := range mobileVRouteCarrierState.items {
		if c.plan.RouteID != mobileVRouteRuntimeRouteID(rule) {
			continue
		}
		live = true
		rtt := c.rttMS.Load()
		if rtt > 0 && time.Since(time.Unix(0, c.rttAt.Load())) <= 90*time.Second && (best == 0 || rtt < best) {
			best = rtt
		}
	}
	return live, best
}

func mobileVRoutePathScore(config mobileVRouteConfig, path []string) (int64, int) {
	mobileVRoutePathSamples.Lock()
	sample := mobileVRoutePathSamples.items[strings.Join(path, ">")]
	mobileVRoutePathSamples.Unlock()
	if sample.RTT > 0 && time.Since(sample.At) <= 90*time.Second {
		return sample.RTT, 0
	}
	var rtt int64
	missing := len(path) - 1
	if len(path) > 1 {
		if rule, _, ok := mobileVRouteFindTopologyRule(config, path[0], path[1]); ok {
			_, rtt = mobileVRouteRuleRTT(rule)
			if rtt > 0 {
				missing--
			}
		}
	}
	return rtt, missing
}

func mobileVRoutePathLess(config mobileVRouteConfig, a, b []string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	ar, am := mobileVRoutePathScore(config, a)
	br, bm := mobileVRoutePathScore(config, b)
	if am != bm {
		return am < bm
	}
	if ar != br {
		return ar < br
	}
	return strings.Join(a, ">") < strings.Join(b, ">")
}

func measureMobileVRoutePath(carrier *mobileVRouteCarrier, path []string, cancel <-chan struct{}) (int64, error) {
	if carrier == nil || len(path) < 2 {
		return 0, errors.New("RTT carrier/path unavailable")
	}
	id := newAndroidRouteFlowID("path_sample", path[len(path)-1])
	started := time.Now()
	payload, _ := json.Marshal(mobileVRouteControlProbePayload{RequestID: id, SourceNodeID: path[0], TargetNodeID: path[len(path)-1], Path: path})
	control, _ := json.Marshal(mobileVRouteFrameControlEnvelope{Path: path})
	main := mobileVRouteFrameMainTypePathRTT
	if len(path) == 2 {
		main = mobileVRouteFrameMainTypePingPong
	}
	ch := registerMobileVRouteRTTResponse(id)
	defer unregisterMobileVRouteRTTResponse(id)
	if err := carrier.enqueueFrame(mobileVRouteFrame{MainType: main, SubType: 1, Control: control, Data: payload}); err != nil {
		return 0, err
	}
	timer := time.NewTimer(mobileVRouteRTTTimeout)
	defer timer.Stop()
	select {
	case <-cancel:
		return 0, errors.New("RTT canceled")
	case <-carrier.done:
		return 0, errors.New("RTT carrier closed")
	case <-timer.C:
		return 0, errors.New("RTT timeout")
	case response := <-ch:
		if !response.OK || normalizeMobileRouteNodeID(response.Responder) != path[len(path)-1] {
			return 0, errors.New("RTT response rejected")
		}
		rtt := mobileVRouteRTTMilliseconds(time.Since(started))
		if len(path) == 2 {
			carrier.rttMS.Store(rtt)
			carrier.rttAt.Store(time.Now().UnixNano())
		}
		return rtt, nil
	}
}

func scheduleMobileVRoutePathSamples(config mobileVRouteConfig, paths [][]string) {
	now := time.Now()
	mobileVRoutePathSamples.Lock()
	if mobileVRoutePathSamples.busy || now.Before(mobileVRoutePathSamples.next) {
		mobileVRoutePathSamples.Unlock()
		return
	}
	var selected []string
	var oldest time.Time
	for _, path := range paths {
		if len(path) < 3 {
			continue
		}
		item := mobileVRoutePathSamples.items[strings.Join(path, ">")]
		if now.Sub(item.Attempt) < 30*time.Second {
			continue
		}
		if selected == nil || item.Attempt.Before(oldest) {
			selected = path
			oldest = item.Attempt
		}
	}
	if selected == nil {
		mobileVRoutePathSamples.Unlock()
		return
	}
	var carrier *mobileVRouteCarrier
	if plan, err := buildMobileVRouteAdjacentPlan(config, selected, selected[0], selected[1]); err == nil {
		mobileVRouteCarrierState.mu.Lock()
		for _, c := range mobileVRouteCarrierState.items {
			if mobileVRouteCarrierBaseKey(c.plan) == mobileVRouteCarrierBaseKey(plan) && (carrier == nil || c.plan.CarrierSlot < carrier.plan.CarrierSlot) {
				carrier = c
			}
		}
		mobileVRouteCarrierState.mu.Unlock()
	}
	if carrier == nil {
		mobileVRoutePathSamples.Unlock()
		return
	}
	epoch := mobileVRoutePathSamples.epoch
	key := strings.Join(selected, ">")
	if _, ok := mobileVRoutePathSamples.items[key]; !ok && len(mobileVRoutePathSamples.items) >= mobileVRoutePathSampleMax {
		var oldestKey string
		var at time.Time
		for k, item := range mobileVRoutePathSamples.items {
			if oldestKey == "" || item.Attempt.Before(at) {
				oldestKey, at = k, item.Attempt
			}
		}
		delete(mobileVRoutePathSamples.items, oldestKey)
	}
	item := mobileVRoutePathSamples.items[key]
	item.Attempt = now
	mobileVRoutePathSamples.items[key] = item
	mobileVRoutePathSamples.busy = true
	mobileVRoutePathSamples.next = now.Add(500 * time.Millisecond)
	mobileVRoutePathSamples.Unlock()
	go func(path []string) {
		rtt, err := measureMobileVRoutePath(carrier, path, nil)
		mobileVRoutePathSamples.Lock()
		defer mobileVRoutePathSamples.Unlock()
		if epoch != mobileVRoutePathSamples.epoch {
			return
		}
		item := mobileVRoutePathSamples.items[key]
		if err == nil {
			item.RTT = rtt
			item.At = time.Now()
		}
		mobileVRoutePathSamples.items[key] = item
		mobileVRoutePathSamples.busy = false
	}(append([]string(nil), selected...))
}

func resetMobileVRoutePathSamples() {
	mobileVRoutePathSamples.Lock()
	defer mobileVRoutePathSamples.Unlock()
	mobileVRoutePathSamples.epoch++
	mobileVRoutePathSamples.items = make(map[string]mobileVRoutePathSample)
	mobileVRoutePathSamples.busy = false
	mobileVRoutePathSamples.next = time.Time{}
}

func mobileVRouteBindingKey(plan mobileVRouteForwardPlan, hash uint32) string {
	return plan.LocalNode + "|" + plan.ExitNode + "|" + strconv.FormatUint(uint64(hash), 10)
}

// A latency update affects new flows. Established raw TUN flows retain both
// their source path and their carrier until that carrier/edge disappears.
func pinMobileVRouteFlowPlan(plan mobileVRouteForwardPlan, packet []byte) mobileVRouteForwardPlan {
	key := mobileVRouteBindingKey(plan, mobileVRoutePacketFlowHash(packet, 2166136261))
	mobileVRouteCarrierState.mu.Lock()
	binding, ok := mobileVRouteCarrierState.flowBindings[key]
	carrier := mobileVRouteCarrierState.items[binding.CarrierKey]
	mobileVRouteCarrierState.mu.Unlock()
	if !ok || carrier == nil || len(binding.Path) < 2 || binding.Path[0] != plan.LocalNode || binding.Path[len(binding.Path)-1] != plan.ExitNode || time.Since(binding.LastSeen) > mobileVRouteCarrierFlowBindingTTL {
		return plan
	}
	for i := 1; i < len(binding.Path); i++ {
		found := false
		for _, r := range plan.Config.TopologyRules {
			if r.Enabled && (r.FromNodeID == binding.Path[i-1] && r.ToNodeID == binding.Path[i] || r.ToNodeID == binding.Path[i-1] && r.FromNodeID == binding.Path[i]) {
				found = true
				break
			}
		}
		if !found {
			return plan
		}
	}
	for _, r := range plan.Config.TopologyRules {
		if r.Enabled && mobileVRouteRuntimeRouteID(r) == carrier.plan.RouteID {
			pinned := carrier.plan
			pinned.Config = plan.Config
			pinned.Path = append([]string(nil), binding.Path...)
			pinned.ExitNode = plan.ExitNode
			return pinned
		}
	}
	return plan
}
