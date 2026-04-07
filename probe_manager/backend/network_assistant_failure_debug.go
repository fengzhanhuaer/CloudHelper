package backend

import (
	"strings"
	"sync"
	"time"
)

const (
	networkDebugFailureMaxEntries  = 1000
	defaultFailureEventsQueryLimit = 200
	maxFailureEventsQueryLimit     = 1000
)

type networkDebugFailureEvent struct {
	Timestamp    time.Time
	Scope        string
	Kind         string
	Target       string
	RoutedTarget string
	Group        string
	NodeID       string
	ModeKey      string
	Reason       string
	Error        string
	Message      string
	Direct       bool
}

type networkDebugFailureStore struct {
	mu         sync.Mutex
	entries    []networkDebugFailureEvent
	maxEntries int
}

var globalNetworkDebugFailureStore = &networkDebugFailureStore{
	entries:    make([]networkDebugFailureEvent, 0, 256),
	maxEntries: networkDebugFailureMaxEntries,
}

func recordNetworkDebugFailure(event networkDebugFailureEvent) {
	if globalNetworkDebugFailureStore == nil {
		return
	}
	globalNetworkDebugFailureStore.append(event)
}

func queryNetworkDebugFailures(sinceMS int64, kind string, limit int) []networkDebugFailureEvent {
	if globalNetworkDebugFailureStore == nil {
		return []networkDebugFailureEvent{}
	}
	return globalNetworkDebugFailureStore.query(sinceMS, kind, limit)
}

func normalizeNetworkDebugFailureLimit(limit int) int {
	if limit <= 0 {
		return defaultFailureEventsQueryLimit
	}
	if limit > maxFailureEventsQueryLimit {
		return maxFailureEventsQueryLimit
	}
	return limit
}

func normalizeNetworkDebugFailureEvent(event networkDebugFailureEvent) networkDebugFailureEvent {
	event.Scope = strings.ToLower(strings.TrimSpace(event.Scope))
	event.Kind = strings.ToLower(strings.TrimSpace(event.Kind))
	event.Target = strings.TrimSpace(event.Target)
	event.RoutedTarget = strings.TrimSpace(event.RoutedTarget)
	event.Group = strings.TrimSpace(event.Group)
	event.NodeID = strings.TrimSpace(event.NodeID)
	event.ModeKey = strings.TrimSpace(event.ModeKey)
	event.Reason = strings.ToLower(strings.TrimSpace(event.Reason))
	event.Error = strings.TrimSpace(event.Error)
	event.Message = strings.TrimSpace(event.Message)
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	} else {
		event.Timestamp = event.Timestamp.UTC()
	}
	return event
}

func (s *networkDebugFailureStore) append(event networkDebugFailureEvent) {
	if s == nil {
		return
	}
	normalized := normalizeNetworkDebugFailureEvent(event)
	if normalized.Scope == "" && normalized.Kind == "" && normalized.Target == "" && normalized.Message == "" && normalized.Error == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = append(s.entries, normalized)
	if len(s.entries) > s.maxEntries {
		trim := len(s.entries) - s.maxEntries
		s.entries = append([]networkDebugFailureEvent(nil), s.entries[trim:]...)
	}
}

func (s *networkDebugFailureStore) query(sinceMS int64, kind string, limit int) []networkDebugFailureEvent {
	if s == nil {
		return []networkDebugFailureEvent{}
	}
	cleanKind := strings.ToLower(strings.TrimSpace(kind))
	resolvedLimit := normalizeNetworkDebugFailureLimit(limit)
	var since time.Time
	if sinceMS > 0 {
		since = time.UnixMilli(sinceMS).UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	items := make([]networkDebugFailureEvent, 0, len(s.entries))
	for _, entry := range s.entries {
		if !since.IsZero() && entry.Timestamp.Before(since) {
			continue
		}
		if cleanKind != "" && !strings.EqualFold(entry.Kind, cleanKind) && !strings.EqualFold(entry.Scope, cleanKind) {
			continue
		}
		items = append(items, entry)
	}
	if len(items) > resolvedLimit {
		items = append([]networkDebugFailureEvent(nil), items[len(items)-resolvedLimit:]...)
	}
	if len(items) == 0 {
		return []networkDebugFailureEvent{}
	}
	return append([]networkDebugFailureEvent(nil), items...)
}
