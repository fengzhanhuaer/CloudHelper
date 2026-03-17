package main

import (
	"log"
	"strings"
	"sync"
	"time"
)

const (
	probeLogMaxBytes   = 2 * 1024 * 1024
	probeLogSourceName = "memory"
	probeLogSourcePath = "memory://probe_node"
)

type probeLogEntry struct {
	at   time.Time
	line string
	size int
}

type probeInMemoryLogStore struct {
	mu        sync.Mutex
	maxBytes  int
	total     int
	entries   []probeLogEntry
	partial   string
	partialAt time.Time
}

var probeLogStore = newProbeInMemoryLogStore(probeLogMaxBytes)

func newProbeInMemoryLogStore(maxBytes int) *probeInMemoryLogStore {
	if maxBytes <= 0 {
		maxBytes = probeLogMaxBytes
	}
	return &probeInMemoryLogStore{
		maxBytes: maxBytes,
		entries:  make([]probeLogEntry, 0, 512),
	}
}

func initProbeLogger() {
	log.SetOutput(probeLogStore)
}

func (s *probeInMemoryLogStore) Write(p []byte) (int, error) {
	if s == nil {
		return len(p), nil
	}

	text := strings.ReplaceAll(string(p), "\r\n", "\n")
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	merged := s.partial + text
	parts := strings.Split(merged, "\n")
	if strings.HasSuffix(merged, "\n") {
		s.partial = ""
		s.partialAt = time.Time{}
	} else {
		partial := parts[len(parts)-1]
		if s.maxBytes > 0 && len(partial)+1 > s.maxBytes {
			keep := s.maxBytes - 1
			if keep <= 0 {
				partial = ""
			} else {
				partial = partial[len(partial)-keep:]
			}
		}
		s.partial = partial
		s.partialAt = now
		parts = parts[:len(parts)-1]
	}

	for _, line := range parts {
		if strings.TrimSpace(line) == "" {
			continue
		}
		s.appendLocked(now, line)
	}

	return len(p), nil
}

func (s *probeInMemoryLogStore) Tail(lines int, sinceMinutes int) string {
	if s == nil {
		return ""
	}

	limit := normalizeProbeLogLines(lines)
	cutoffEnabled := sinceMinutes > 0
	cutoff := time.Now().Add(-time.Duration(normalizeProbeLogSinceMinutes(sinceMinutes)) * time.Minute)

	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := make([]string, 0, len(s.entries)+1)
	for _, entry := range s.entries {
		if cutoffEnabled && entry.at.Before(cutoff) {
			continue
		}
		filtered = append(filtered, entry.line)
	}

	if strings.TrimSpace(s.partial) != "" {
		if !cutoffEnabled || !s.partialAt.Before(cutoff) {
			filtered = append(filtered, s.partial)
		}
	}

	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return strings.Join(filtered, "\n")
}

func (s *probeInMemoryLogStore) appendLocked(ts time.Time, line string) {
	if s.maxBytes <= 0 || strings.TrimSpace(line) == "" {
		return
	}

	normalized := line
	if len(normalized)+1 > s.maxBytes {
		keep := s.maxBytes - 1
		if keep <= 0 {
			return
		}
		normalized = normalized[len(normalized)-keep:]
	}

	entry := probeLogEntry{
		at:   ts,
		line: normalized,
		size: len(normalized) + 1,
	}
	s.entries = append(s.entries, entry)
	s.total += entry.size

	for s.total > s.maxBytes && len(s.entries) > 0 {
		s.total -= s.entries[0].size
		s.entries = s.entries[1:]
	}
}
