//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func resetProbeWintunEmbedHooksForTest() {
	probeWintunPathResolver = resolveProbeWintunPath
	probeWintunMkdirAll = os.MkdirAll
	probeWintunWriteFile = os.WriteFile
	probeWintunReadFile = os.ReadFile
}

func TestEnsureProbeEmbeddedWintunLibrarySkipsOverwriteWhenExistingValid(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("requires amd64")
	}
	defer resetProbeWintunEmbedHooksForTest()

	targetPath := filepath.Join(t.TempDir(), "wintun.dll")
	if err := os.WriteFile(targetPath, []byte{'M', 'Z', 0x00, 0x00}, 0o644); err != nil {
		t.Fatalf("prepare existing wintun failed: %v", err)
	}

	writeCalled := 0
	probeWintunPathResolver = func() (string, error) { return targetPath, nil }
	probeWintunWriteFile = func(_ string, _ []byte, _ os.FileMode) error {
		writeCalled++
		return errors.New("should not overwrite existing valid dll")
	}

	if err := ensureProbeEmbeddedWintunLibrary(); err != nil {
		t.Fatalf("ensureProbeEmbeddedWintunLibrary returned error: %v", err)
	}
	if writeCalled != 0 {
		t.Fatalf("writeCalled=%d, want 0", writeCalled)
	}
}

func TestEnsureProbeEmbeddedWintunLibraryFileInUseWithExistingValid(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("requires amd64")
	}
	defer resetProbeWintunEmbedHooksForTest()

	targetPath := filepath.Join(t.TempDir(), "wintun.dll")
	if err := os.WriteFile(targetPath, []byte{'M', 'Z', 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("prepare existing wintun failed: %v", err)
	}

	probeWintunPathResolver = func() (string, error) { return targetPath, nil }
	probeWintunWriteFile = func(path string, _ []byte, _ os.FileMode) error {
		return &os.PathError{Op: "open", Path: path, Err: syscall.Errno(32)}
	}

	if err := ensureProbeEmbeddedWintunLibrary(); err != nil {
		t.Fatalf("ensureProbeEmbeddedWintunLibrary returned error: %v", err)
	}
}

func TestEnsureProbeEmbeddedWintunLibraryFileInUseWithInvalidExisting(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("requires amd64")
	}
	defer resetProbeWintunEmbedHooksForTest()

	targetPath := filepath.Join(t.TempDir(), "wintun.dll")
	if err := os.WriteFile(targetPath, []byte{'N', 'O'}, 0o644); err != nil {
		t.Fatalf("prepare invalid wintun failed: %v", err)
	}

	probeWintunPathResolver = func() (string, error) { return targetPath, nil }
	probeWintunWriteFile = func(path string, _ []byte, _ os.FileMode) error {
		return &os.PathError{Op: "open", Path: path, Err: syscall.Errno(32)}
	}

	err := ensureProbeEmbeddedWintunLibrary()
	if err == nil {
		t.Fatal("expected ensureProbeEmbeddedWintunLibrary error")
	}
}
