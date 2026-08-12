package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTGAssistantMessageStoreByAccountAndTarget(t *testing.T) {
	chdirTemp(t)

	if err := storeTGAssistantSessionMessages("account-a", "user:100", []tgAssistantSessionMessage{
		{ID: 1, Date: "2026-06-27T10:00:00Z", Text: "hello", SenderID: "user:100", SenderName: "Alice", Formats: []tgAssistantMessageFormat{{Type: "bold", Offset: 0, Length: 5}}},
		{ID: 2, Date: "2026-06-27T10:01:00Z", Text: "world", Out: true, SenderID: "user:200", SenderName: "Me", MediaType: "video", MediaPath: filepath.Join(tgAssistantVideoDirPath(), "a.mp4"), MediaSize: 123},
	}); err != nil {
		t.Fatalf("store account-a: %v", err)
	}
	if err := storeTGAssistantSessionMessages("account-b", "user:100", []tgAssistantSessionMessage{
		{ID: 1, Date: "2026-06-27T10:02:00Z", Text: "other account", SenderID: "user:100", SenderName: "Alice"},
	}); err != nil {
		t.Fatalf("store account-b: %v", err)
	}
	if err := storeTGAssistantSessionMessages("account-a", "user:100", []tgAssistantSessionMessage{
		{ID: 1, Date: "2026-06-27T10:03:00Z", Text: "hello updated", SenderID: "user:100", SenderName: "Alice", Formats: []tgAssistantMessageFormat{{Type: "url", Offset: 6, Length: 7, URL: "https://example.com"}}},
	}); err != nil {
		t.Fatalf("store duplicate: %v", err)
	}

	messages, err := listStoredTGAssistantSessionMessages("account-a", "user:100", 20)
	if err != nil {
		t.Fatalf("list account-a: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("account-a message count=%d, want 2", len(messages))
	}
	if messages[0].ID != 1 || messages[0].Text != "hello updated" {
		t.Fatalf("expected updated first message, got %+v", messages[0])
	}
	if len(messages[0].Formats) != 1 || messages[0].Formats[0].Type != "url" || messages[0].Formats[0].URL != "https://example.com" {
		t.Fatalf("expected formats to survive sqlite round trip, got %+v", messages[0].Formats)
	}
	if !messages[1].Out {
		t.Fatalf("expected second message to be outgoing: %+v", messages[1])
	}
	if messages[1].MediaType != "video" || messages[1].MediaSize != 123 {
		t.Fatalf("expected video index on second message: %+v", messages[1])
	}

	other, err := listStoredTGAssistantSessionMessages("account-b", "user:100", 20)
	if err != nil {
		t.Fatalf("list account-b: %v", err)
	}
	if len(other) != 1 || other[0].Text != "other account" {
		t.Fatalf("unexpected account-b messages: %+v", other)
	}

	if _, err := os.Stat(tgAssistantMessageDBPath()); err != nil {
		t.Fatalf("message db should exist: %v", err)
	}
}

func TestTGAssistantMessageStorePrunesVideoDir(t *testing.T) {
	chdirTemp(t)
	oldVideoDir := tgAssistantVideoDir
	oldVideoMax := tgAssistantVideoDirMaxBytes
	tgAssistantVideoDir = filepath.Join(t.TempDir(), "mv")
	tgAssistantVideoDirMaxBytes = 10
	t.Cleanup(func() {
		tgAssistantVideoDir = oldVideoDir
		tgAssistantVideoDirMaxBytes = oldVideoMax
	})

	oldPath := filepath.Join(tgAssistantVideoDirPath(), "old.mp4")
	newPath := filepath.Join(tgAssistantVideoDirPath(), "new.mp4")
	if err := os.MkdirAll(tgAssistantVideoDirPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("12345678"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("abcdefgh"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	newTime := time.Now()
	_ = os.Chtimes(oldPath, oldTime, oldTime)
	_ = os.Chtimes(newPath, newTime, newTime)

	if err := storeTGAssistantSessionMessages("account-a", "user:100", []tgAssistantSessionMessage{
		{ID: 1, Date: "2026-06-27T10:00:00Z", Text: "[视频]", MediaType: "video", MediaPath: oldPath, MediaSize: 8},
		{ID: 2, Date: "2026-06-27T10:01:00Z", Text: "[视频]", MediaType: "video", MediaPath: newPath, MediaSize: 8},
	}); err != nil {
		t.Fatalf("store messages: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old video should be pruned, stat err=%v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new video should remain: %v", err)
	}
	messages, err := listStoredTGAssistantSessionMessages("account-a", "user:100", 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if messages[0].MediaPath != "" {
		t.Fatalf("pruned video path should be cleared: %+v", messages[0])
	}
	if messages[1].MediaPath != newPath {
		t.Fatalf("new video path should remain: %+v", messages[1])
	}
}

func TestTGAssistantMessageStorePrunesOversizedDB(t *testing.T) {
	chdirTemp(t)
	oldMax := tgAssistantMessageDBMaxBytes
	tgAssistantMessageDBMaxBytes = 24 * 1024
	t.Cleanup(func() { tgAssistantMessageDBMaxBytes = oldMax })

	messages := make([]tgAssistantSessionMessage, 0, 80)
	for i := 1; i <= 80; i++ {
		messages = append(messages, tgAssistantSessionMessage{
			ID:   i,
			Date: "2026-06-27T10:00:00Z",
			Text: strings.Repeat("x", 2048),
		})
	}
	if err := storeTGAssistantSessionMessages("account-a", "user:100", messages); err != nil {
		t.Fatalf("store messages: %v", err)
	}
	stored, err := listStoredTGAssistantSessionMessages("account-a", "user:100", 100)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(stored) >= len(messages) {
		t.Fatalf("expected oversized db to prune old messages, kept=%d input=%d", len(stored), len(messages))
	}
}

func TestMergeTGAssistantSessionMessagesKeepsOrderAndDedup(t *testing.T) {
	base := []tgAssistantSessionMessage{
		{ID: 1, Date: "2026-06-27T10:00:00Z", Text: "old"},
		{ID: 2, Date: "2026-06-27T10:01:00Z", Text: "keep"},
	}
	incoming := []tgAssistantSessionMessage{
		{ID: 2, Date: "2026-06-27T10:01:30Z", Text: "keep updated"},
		{ID: 3, Date: "2026-06-27T10:02:00Z", Text: "new"},
	}
	merged := mergeTGAssistantSessionMessages(base, incoming, 10)
	if len(merged) != 3 {
		t.Fatalf("merged count=%d, want 3", len(merged))
	}
	if merged[1].Text != "keep updated" {
		t.Fatalf("expected deduped update, got %+v", merged[1])
	}
	if merged[2].ID != 3 {
		t.Fatalf("expected newest message last, got %+v", merged[2])
	}
}
