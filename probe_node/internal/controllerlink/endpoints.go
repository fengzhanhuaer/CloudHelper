package controllerlink

import (
	"context"
	"net"
	"sort"
	"time"
)

type endpointHealth struct {
	failures    int
	retryAt     time.Time
	lastSuccess time.Time
}

// Call with Manager.mu held. Never mutate the trusted discovery/cache order.
func availableEndpoints(s *state, now time.Time) []Endpoint {
	endpoints := make([]Endpoint, 0, len(s.data.Endpoints))
	for _, e := range s.data.Endpoints {
		if !now.Before(s.endpoints[e].retryAt) {
			endpoints = append(endpoints, e)
		}
	}
	sort.SliceStable(endpoints, func(i, j int) bool {
		a, b := s.endpoints[endpoints[i]], s.endpoints[endpoints[j]]
		if !a.lastSuccess.Equal(b.lastSuccess) {
			return a.lastSuccess.After(b.lastSuccess)
		}
		return net.ParseIP(endpoints[i].IP).To4() != nil && net.ParseIP(endpoints[j].IP).To4() == nil
	})
	return endpoints
}

func (m *Manager) endpointFeedback(ctx context.Context, raw string, endpoint Endpoint, ok bool) {
	// Caller cancellation says nothing about the remote endpoint's health.
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.entry(Origin(raw))
	current := make(map[Endpoint]endpointHealth, len(s.data.Endpoints))
	found := false
	for _, e := range s.data.Endpoints {
		current[e] = s.endpoints[e]
		found = found || e == endpoint
	}
	s.endpoints = current
	if !found { // A discovery refresh can remove an address or rotate its pin.
		return
	}
	h := s.endpoints[endpoint]
	now := time.Now()
	if ok {
		h = endpointHealth{lastSuccess: now}
	} else {
		if h.failures < 5 {
			h.failures++
		}
		cooldown := 30 * time.Second * time.Duration(1<<uint(h.failures-1))
		if cooldown > 5*time.Minute {
			cooldown = 5 * time.Minute
		}
		h.retryAt = now.Add(cooldown)
	}
	s.endpoints[endpoint] = h
}
