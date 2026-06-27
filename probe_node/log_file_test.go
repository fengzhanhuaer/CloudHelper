package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeRotatingLogFileWriterEnforcesByteLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "probe_node.runtime.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open log file failed: %v", err)
	}
	writer := newProbeRotatingLogFileWriter(path, f, 128)
	for i := 0; i < 32; i++ {
		if _, err := writer.Write([]byte(fmt.Sprintf("line-%02d payload\n", i))); err != nil {
			t.Fatalf("write line %d failed: %v", i, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close log file failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log file failed: %v", err)
	}
	if info.Size() > 128 {
		t.Fatalf("log file size = %d, want <= %d", info.Size(), 128)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "line-31 payload") {
		t.Fatalf("latest line not found after trim: %q", text)
	}
	if strings.Contains(text, "line-00 payload") {
		t.Fatalf("oldest line should be trimmed: %q", text)
	}
}
