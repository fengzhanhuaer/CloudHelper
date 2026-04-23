package main

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunProbeLocalCommandEmptyName(t *testing.T) {
	_, err := runProbeLocalCommand(0, "   ")
	if err == nil {
		t.Fatalf("expected error for empty command")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "empty command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunProbeLocalCommandSuccess(t *testing.T) {
	name, args := probeLocalEchoCommand("hello")
	out, err := runProbeLocalCommand(3*time.Second, name, args...)
	if err != nil {
		t.Fatalf("runProbeLocalCommand returned error: %v (out=%q)", err, out)
	}
	if !strings.Contains(strings.ToLower(out), "hello") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestRunProbeLocalCommandFailureIncludesOutput(t *testing.T) {
	name, args := probeLocalFailureCommand("boom")
	out, err := runProbeLocalCommand(3*time.Second, name, args...)
	if err == nil {
		t.Fatalf("expected command failure, got nil error (out=%q)", out)
	}
	if !strings.Contains(strings.ToLower(out), "boom") {
		t.Fatalf("expected output to include boom, got: %q", out)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "run command failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunProbeLocalCommandTimeout(t *testing.T) {
	name, args := probeLocalSlowCommand()
	start := time.Now()
	_, err := runProbeLocalCommand(150*time.Millisecond, name, args...)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("timeout error should contain timeout marker, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout did not trigger in acceptable time, elapsed=%s err=%v", elapsed, err)
	}
}

func probeLocalEchoCommand(text string) (string, []string) {
	clean := strings.TrimSpace(text)
	if clean == "" {
		clean = "hello"
	}
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", "echo", clean}
	}
	return "sh", []string{"-lc", "echo " + clean}
}

func probeLocalFailureCommand(text string) (string, []string) {
	clean := strings.TrimSpace(text)
	if clean == "" {
		clean = "boom"
	}
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", "echo " + clean + " && exit /b 7"}
	}
	return "sh", []string{"-lc", "echo " + clean + "; exit 7"}
}

func probeLocalSlowCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "powershell", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 2"}
	}
	return "sh", []string{"-lc", "sleep 2"}
}
