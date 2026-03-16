package backend

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultNetworkAssistantLogLines = 200
	maxNetworkAssistantLogLines     = 2000
	networkAssistantLogMaxEntries   = 3000
	logSourceManager                = "manager"
	logSourceController             = "controller"
	defaultLogCategory              = "general"
)

type NetworkAssistantLogEntry struct {
	Time     string `json:"time"`
	Source   string `json:"source"`
	Category string `json:"category"`
	Message  string `json:"message"`
	Line     string `json:"line"`
}

type NetworkAssistantLogResponse struct {
	Lines   int                        `json:"lines"`
	Content string                     `json:"content"`
	Fetched string                     `json:"fetched_at"`
	Entries []NetworkAssistantLogEntry `json:"entries"`
}

type networkAssistantLogStore struct {
	mu         sync.Mutex
	entries    []NetworkAssistantLogEntry
	maxEntries int
}

func newNetworkAssistantLogStore() *networkAssistantLogStore {
	return &networkAssistantLogStore{
		entries:    make([]NetworkAssistantLogEntry, 0, 512),
		maxEntries: networkAssistantLogMaxEntries,
	}
}

func (s *networkAssistantLogStore) Append(source, category, message string) {
	if s == nil {
		return
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		return
	}
	src := normalizeNetworkAssistantLogSource(source)
	cat := normalizeNetworkAssistantLogCategory(category)
	ts := time.Now().Format(logLineTimeLayout)
	line := fmt.Sprintf("%s [%s/%s] %s", ts, src, cat, msg)
	entry := NetworkAssistantLogEntry{
		Time:     ts,
		Source:   src,
		Category: cat,
		Message:  msg,
		Line:     line,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = append(s.entries, entry)
	if len(s.entries) > s.maxEntries {
		trim := len(s.entries) - s.maxEntries
		s.entries = append([]NetworkAssistantLogEntry(nil), s.entries[trim:]...)
	}
}

func (s *networkAssistantLogStore) Appendf(source, category, format string, args ...any) {
	s.Append(source, category, fmt.Sprintf(format, args...))
}

func (s *networkAssistantLogStore) Tail(lines int) (int, string, []NetworkAssistantLogEntry) {
	limit := normalizeNetworkAssistantLogLines(lines)
	if s == nil {
		return limit, "", nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.entries) == 0 {
		return limit, "", nil
	}

	start := 0
	if len(s.entries) > limit {
		start = len(s.entries) - limit
	}
	tail := append([]NetworkAssistantLogEntry(nil), s.entries[start:]...)
	linesOut := make([]string, 0, len(tail))
	for _, entry := range tail {
		linesOut = append(linesOut, entry.Line)
	}
	return limit, strings.Join(linesOut, "\n"), tail
}

func (a *App) GetNetworkAssistantLogs(lines int) (NetworkAssistantLogResponse, error) {
	if a.networkAssistant == nil {
		return NetworkAssistantLogResponse{}, fmt.Errorf("network assistant service is not initialized")
	}
	limit, content, entries := a.networkAssistant.logStore.Tail(lines)
	return NetworkAssistantLogResponse{
		Lines:   limit,
		Content: content,
		Fetched: time.Now().Format(time.RFC3339),
		Entries: entries,
	}, nil
}

func normalizeNetworkAssistantLogSource(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == logSourceController {
		return logSourceController
	}
	return logSourceManager
}

func normalizeNetworkAssistantLogCategory(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return defaultLogCategory
	}
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ReplaceAll(value, "/", "-")
	return value
}

func normalizeNetworkAssistantLogLines(lines int) int {
	if lines <= 0 {
		return defaultNetworkAssistantLogLines
	}
	if lines > maxNetworkAssistantLogLines {
		return maxNetworkAssistantLogLines
	}
	return lines
}
