package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeRotatingLogFileWriterEnforcesByteLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "probe_node.runtime.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("stale-log-data\n", 128)), 0o644); err != nil {
		t.Fatalf("seed oversized log file: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open log file failed: %v", err)
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		_ = f.Close()
		t.Fatalf("seek log file end: %v", err)
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
