//go:build windows

package main

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrProbeLocalRelaunchAsAdmin 表示已触发 UAC 提权重启，调用方应视当前进程即将退出。
var ErrProbeLocalRelaunchAsAdmin = errors.New("relaunch as admin")

type probeLocalTokenElevation struct {
	TokenIsElevated uint32
}

// isWindowsAdmin 判断当前进程是否为提升权限（管理员）运行。
func isWindowsAdmin() bool {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()

	const tokenElevationClass = 20 // TokenElevation
	var elev probeLocalTokenElevation
	var retLen uint32
	err = windows.GetTokenInformation(
		token,
		tokenElevationClass,
		(*byte)(unsafe.Pointer(&elev)),
		uint32(unsafe.Sizeof(elev)),
		&retLen,
	)
	if err != nil {
		return false
	}
	return elev.TokenIsElevated != 0
}

// relaunchAsAdminForProbeLocalTUNInstall 使用 runas 提权重启当前进程，并附加仅安装 TUN 的内部参数。
func relaunchAsAdminForProbeLocalTUNInstall() error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	args := append([]string(nil), os.Args[1:]...)
	if !probeLocalHasArg(args, "--local-tun-install") {
		args = append(args, "--local-tun-install")
	}

	argStr := ""
	if len(args) > 0 {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		}
		argStr = strings.Join(quoted, " ")
	}

	verb, _ := syscall.UTF16PtrFromString("runas")
	exePtr, _ := syscall.UTF16PtrFromString(exePath)
	var argPtr *uint16
	if argStr != "" {
		argPtr, _ = syscall.UTF16PtrFromString(argStr)
	}

	shell32 := windows.NewLazyDLL("shell32.dll")
	shellExec := shell32.NewProc("ShellExecuteW")
	r, _, _ := shellExec.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(exePtr)),
		uintptr(unsafe.Pointer(argPtr)),
		0,
		1, // SW_SHOWNORMAL
	)
	if r <= 32 {
		return errors.New("ShellExecuteW failed to relaunch as admin")
	}
	return ErrProbeLocalRelaunchAsAdmin
}

func probeLocalHasArg(args []string, target string) bool {
	needle := strings.TrimSpace(target)
	if needle == "" {
		return false
	}
	for _, raw := range args {
		if strings.EqualFold(strings.TrimSpace(raw), needle) {
			return true
		}
	}
	return false
}
