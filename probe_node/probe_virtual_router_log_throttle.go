package main

import (
	"strings"
	"sync"
	"time"
)

type probeVirtualRouterLogThrottleEntry struct {
	LastLogAt  time.Time
	ExpiresAt  time.Time
	Suppressed int
}

var probeVirtualRouterLogThrottleState = struct {
	mu    sync.Mutex
	items map[string]probeVirtualRouterLogThrottleEntry
}{items: make(map[string]probeVirtualRouterLogThrottleEntry)}

func takeProbeVirtualRouterLogThrottle(key string, period time.Duration, now time.Time) (bool, int) {
	key = strings.TrimSpace(key)
	if key == "" || period <= 0 {
		return true, 0
	}
	if now.IsZero() {
		now = time.Now()
	}

	probeVirtualRouterLogThrottleState.mu.Lock()
	defer probeVirtualRouterLogThrottleState.mu.Unlock()
	for itemKey, item := range probeVirtualRouterLogThrottleState.items {
		if !item.ExpiresAt.IsZero() && now.After(item.ExpiresAt) {
			delete(probeVirtualRouterLogThrottleState.items, itemKey)
		}
	}

	item := probeVirtualRouterLogThrottleState.items[key]
	item.ExpiresAt = now.Add(4 * period)
	if item.LastLogAt.IsZero() || now.Sub(item.LastLogAt) >= period {
		suppressed := item.Suppressed
		item.LastLogAt = now
		item.Suppressed = 0
		probeVirtualRouterLogThrottleState.items[key] = item
		return true, suppressed
	}
	item.Suppressed++
	probeVirtualRouterLogThrottleState.items[key] = item
	return false, 0
}
