//go:build !windows

package backend

import "os/exec"

// hideWindowSysProcAttr 在非 Windows 平台上为空操作。
func hideWindowSysProcAttr(_ *exec.Cmd) {}
