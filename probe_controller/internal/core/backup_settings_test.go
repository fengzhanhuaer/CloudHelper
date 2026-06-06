package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeBackupSourceDirsForStoreDedupesAndAbsolutizes(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := normalizeBackupSourceDirsForStore([]string{source, source, " "}, "")
	if err != nil {
		t.Fatalf("normalize source dirs failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("source dir count=%d, want 1: %v", len(got), got)
	}
	want, _ := filepath.Abs(source)
	if got[0] != want {
		t.Fatalf("source dir=%q, want %q", got[0], want)
	}
}

func TestNormalizeBackupSourceDirsRejectsBackupOverlap(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	backup := filepath.Join(source, "backup")
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := normalizeBackupSourceDirsForStore([]string{source}, backup); err == nil {
		t.Fatal("expected source/backup overlap to be rejected")
	}
}

func TestNormalizeGoogleDriveFolderPath(t *testing.T) {
	got := normalizeGoogleDriveFolderPath(` /CloudHelper//controller/ `)
	if got != "CloudHelper/controller" {
		t.Fatalf("folder=%q, want CloudHelper/controller", got)
	}
	if empty := normalizeGoogleDriveFolderPath(""); empty != defaultBackupGoogleFolder {
		t.Fatalf("empty folder=%q, want default", empty)
	}
}
