package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProbeLogMaxBytesConstant(t *testing.T) {
	if probeLogMaxBytes != 2*1024*1024 {
		t.Fatalf("probeLogMaxBytes = %d, want %d", probeLogMaxBytes, 2*1024*1024)
	}
}

func TestProbeInMemoryLogStoreEnforcesByteLimit(t *testing.T) {
	store := newProbeInMemoryLogStore(96)
	for i := 0; i < 30; i++ {
		_, _ = store.Write([]byte(fmt.Sprintf("line-%02d payload\n", i)))
	}

	store.mu.Lock()
	total := store.total
	store.mu.Unlock()
	if total > 96 {
		t.Fatalf("store total bytes = %d, exceed max = %d", total, 96)
	}

	content, entries := store.Tail(200, 0, "")
	if !strings.Contains(content, "line-29 payload") {
		t.Fatalf("latest line not found in tail: %q", content)
	}
	if strings.Contains(content, "line-00 payload") {
		t.Fatalf("oldest line should be trimmed when max bytes exceeded: %q", content)
	}
	if len(entries) == 0 {
		t.Fatalf("expected structured entries in tail result")
	}
}

func TestProbeInMemoryLogStoreTailWithSinceMinutes(t *testing.T) {
	store := newProbeInMemoryLogStore(1024)
	_, _ = store.Write([]byte("old-line\n"))
	_, _ = store.Write([]byte("new-line\n"))

	store.mu.Lock()
	if len(store.entries) < 2 {
		store.mu.Unlock()
		t.Fatalf("expected at least 2 entries, got %d", len(store.entries))
	}
	store.entries[0].at = time.Now().Add(-10 * time.Minute)
	store.mu.Unlock()

	content, entries := store.Tail(100, 5, "")
	if strings.Contains(content, "old-line") {
		t.Fatalf("old line should be filtered by since window: %q", content)
	}
	if !strings.Contains(content, "new-line") {
		t.Fatalf("new line should remain in since window: %q", content)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 structured entry after since filter, got %d", len(entries))
	}
}
