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
)

type NetworkAssistantLogResponse struct {
	Lines   int    `json:"lines"`
	Content string `json:"content"`
	Fetched string `json:"fetched_at"`
}

type networkAssistantLogStore struct {
	mu         sync.Mutex
	entries    []string
	maxEntries int
}

func newNetworkAssistantLogStore() *networkAssistantLogStore {
	return &networkAssistantLogStore{
		entries:    make([]string, 0, 512),
		maxEntries: networkAssistantLogMaxEntries,
	}
}

func (s *networkAssistantLogStore) Appendf(format string, args ...any) {
	if s == nil {
		return
	}
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}

	line := fmt.Sprintf("%s %s", time.Now().Format(logLineTimeLayout), msg)
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = append(s.entries, line)
	if len(s.entries) > s.maxEntries {
		trim := len(s.entries) - s.maxEntries
		s.entries = append([]string(nil), s.entries[trim:]...)
	}
}

func (s *networkAssistantLogStore) Tail(lines int) (int, string) {
	limit := normalizeNetworkAssistantLogLines(lines)
	if s == nil {
		return limit, ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.entries) == 0 {
		return limit, ""
	}

	start := 0
	if len(s.entries) > limit {
		start = len(s.entries) - limit
	}
	return limit, strings.Join(s.entries[start:], "\n")
}

func (a *App) GetNetworkAssistantLogs(lines int) (NetworkAssistantLogResponse, error) {
	if a.networkAssistant == nil {
		return NetworkAssistantLogResponse{}, fmt.Errorf("network assistant service is not initialized")
	}
	limit, content := a.networkAssistant.logStore.Tail(lines)
	return NetworkAssistantLogResponse{
		Lines:   limit,
		Content: content,
		Fetched: time.Now().Format(time.RFC3339),
	}, nil
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
