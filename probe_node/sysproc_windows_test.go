//go:build windows

package main

import (
	"context"
	"os/exec"
	"testing"
)

func TestBuildProbeShellCommandSetsHiddenWindowAttrOnWindows(t *testing.T) {
	cmd := buildProbeShellCommand(context.Background(), "Write-Output 'ok'")
	if cmd == nil {
		t.Fatalf("expected command, got nil")
	}
	if cmd.SysProcAttr == nil {
		t.Fatalf("expected SysProcAttr to be set on windows")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatalf("expected HideWindow=true on windows")
	}
}

func TestBuildProbeInteractiveShellCommandSetsHiddenWindowAttrOnWindows(t *testing.T) {
	cmd := buildProbeInteractiveShellCommand()
	if cmd == nil {
		t.Fatalf("expected command, got nil")
	}
	if cmd.SysProcAttr == nil {
		t.Fatalf("expected SysProcAttr to be set on windows")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatalf("expected HideWindow=true on windows")
	}
}

func TestHideWindowSysProcAttrHandlesNilCmdOnWindows(t *testing.T) {
	hideWindowSysProcAttr(nil)
}

func TestHideWindowSysProcAttrSetsHiddenWindowAttrOnWindows(t *testing.T) {
	cmd := exec.Command("cmd", "/C", "echo", "ok")
	hideWindowSysProcAttr(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatalf("expected SysProcAttr to be set")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatalf("expected HideWindow=true")
	}
}
