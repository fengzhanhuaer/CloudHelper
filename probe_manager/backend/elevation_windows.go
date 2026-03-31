//go:build windows

package backend

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrRelaunchAsAdmin 表示已触发 UAC 提权重启，调用方应视当前进程即将退出。
var ErrRelaunchAsAdmin = errors.New("relaunch as admin")

// tokenElevation 对应 Windows TOKEN_ELEVATION 结构体。
type tokenElevation struct {
	TokenIsElevated uint32
}

// isWindowsAdmin 判断当前进程是否以管理员（提升权限）身份运行。
func isWindowsAdmin() bool {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()

	const tokenElevationClass = 20 // TokenElevation
	var elev tokenElevation
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

// relaunchAsAdmin 通过 ShellExecute + "runas" 动词以管理员权限重新启动当前可执行文件。
// ShellExecute 成功后返回 ErrRelaunchAsAdmin，调用方收到此错误后应退出当前进程。
func relaunchAsAdmin() error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	// 构建命令行参数字符串（跳过 argv[0]）
	args := os.Args[1:]
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
	return ErrRelaunchAsAdmin
}
