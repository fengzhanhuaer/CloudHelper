//go:build windows

package backend

import (
	"os/exec"
	"syscall"
)

// hideWindowSysProcAttr 在 Windows 上隐藏子进程控制台窗口。
func hideWindowSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
