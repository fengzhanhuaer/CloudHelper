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

type probeLogLevel string

const (
	probeLogLevelRealtime probeLogLevel = "realtime"
	probeLogLevelNormal   probeLogLevel = "normal"
	probeLogLevelWarning  probeLogLevel = "warning"
	probeLogLevelError    probeLogLevel = "error"
)

type probeLogViewEntry struct {
	Time    string        `json:"time"`
	Level   probeLogLevel `json:"level"`
	Message string        `json:"message"`
	Line    string        `json:"line"`
}

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
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

func logProbeRealtimef(format string, args ...any) {
	log.Printf("[realtime] "+format, args...)
}

func logProbeInfof(format string, args ...any) {
	log.Printf("[normal] "+format, args...)
}

func logProbeWarnf(format string, args ...any) {
	log.Printf("[warning] "+format, args...)
}

func logProbeErrorf(format string, args ...any) {
	log.Printf("[error] "+format, args...)
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

func (s *probeInMemoryLogStore) Tail(lines int, sinceMinutes int, minLevel string) (string, []probeLogViewEntry) {
	if s == nil {
		return "", nil
	}

	limit := normalizeProbeLogLines(lines)
	cutoffEnabled := sinceMinutes > 0
	cutoff := time.Now().Add(-time.Duration(normalizeProbeLogSinceMinutes(sinceMinutes)) * time.Minute)
	threshold := normalizeProbeLogLevel(minLevel)

	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := make([]probeLogViewEntry, 0, len(s.entries)+1)
	for _, entry := range s.entries {
		if cutoffEnabled && entry.at.Before(cutoff) {
			continue
		}
		viewEntry := buildProbeLogViewEntry(entry.line)
		if !probeLogLevelGTE(viewEntry.Level, threshold) {
			continue
		}
		filtered = append(filtered, viewEntry)
	}

	if strings.TrimSpace(s.partial) != "" {
		if !cutoffEnabled || !s.partialAt.Before(cutoff) {
			viewEntry := buildProbeLogViewEntry(s.partial)
			if probeLogLevelGTE(viewEntry.Level, threshold) {
				filtered = append(filtered, viewEntry)
			}
		}
	}

	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	linesOut := make([]string, 0, len(filtered))
	for _, entry := range filtered {
		linesOut = append(linesOut, entry.Line)
	}
	return strings.Join(linesOut, "\n"), filtered
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

func buildProbeLogViewEntry(line string) probeLogViewEntry {
	trimmed := strings.TrimSpace(line)
	entry := probeLogViewEntry{
		Level:   inferProbeLogLevel(trimmed),
		Message: trimmed,
		Line:    trimmed,
	}
	if ts, ok := parseProbeLogLineTime(trimmed); ok {
		entry.Time = ts.Format(time.RFC3339)
		message := strings.TrimSpace(trimmed[len("2006/01/02 15:04:05.000000"):])
		if message != "" {
			entry.Message = message
		}
	}
	return entry
}

func parseProbeLogLineTime(line string) (time.Time, bool) {
	const layout = "2006/01/02 15:04:05.000000"
	if len(line) < len(layout) {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation(layout, line[:len(layout)], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func normalizeProbeLogLevel(raw string) probeLogLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "realtime", "实时":
		return probeLogLevelRealtime
	case "warning", "warn", "告警":
		return probeLogLevelWarning
	case "error", "err", "错误":
		return probeLogLevelError
	default:
		return probeLogLevelNormal
	}
}

func inferProbeLogLevel(line string) probeLogLevel {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.Contains(lower, "[error]") || strings.Contains(lower, " error ") || strings.Contains(lower, "failed") || strings.Contains(lower, "panic") || strings.Contains(lower, "fatal") || strings.Contains(lower, "错误"):
		return probeLogLevelError
	case strings.Contains(lower, "[warning]") || strings.Contains(lower, "[warn]") || strings.Contains(lower, " warning") || strings.Contains(lower, "告警") || strings.Contains(lower, "警告"):
		return probeLogLevelWarning
	case strings.Contains(lower, "[realtime]") || strings.Contains(lower, "实时") || strings.Contains(lower, "trace") || strings.Contains(lower, "debug"):
		return probeLogLevelRealtime
	default:
		return probeLogLevelNormal
	}
}

func probeLogLevelRank(level probeLogLevel) int {
	switch normalizeProbeLogLevel(string(level)) {
	case probeLogLevelRealtime:
		return 0
	case probeLogLevelNormal:
		return 1
	case probeLogLevelWarning:
		return 2
	case probeLogLevelError:
		return 3
	default:
		return 1
	}
}

func probeLogLevelGTE(level probeLogLevel, minLevel probeLogLevel) bool {
	return probeLogLevelRank(level) >= probeLogLevelRank(minLevel)
}
