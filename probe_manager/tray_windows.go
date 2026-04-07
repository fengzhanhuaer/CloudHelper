//go:build windows
// +build windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmNull        = 0x0000
	wmCommand     = 0x0111
	wmClose       = 0x0010
	wmDestroy     = 0x0002
	wmRButtonUp   = 0x0205
	wmLButtonUp   = 0x0202
	wmLButtonDbl  = 0x0203
	wmApp         = 0x8000
	wmTrayMessage = wmApp + 1

	nimAdd    = 0x00000000
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	mfString = 0x00000000

	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	idiApplication = 32512

	trayCmdShow = 1001
	trayCmdExit = 1002

	imageIcon      = 1
	lrLoadFromFile = 0x0010
	smCxSmIcon     = 49
	smCySmIcon     = 50
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW  = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW   = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow    = modUser32.NewProc("DestroyWindow")
	procGetMessageW      = modUser32.NewProc("GetMessageW")
	procTranslateMessage = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage  = modUser32.NewProc("PostQuitMessage")
	procPostMessageW     = modUser32.NewProc("PostMessageW")
	procLoadIconW        = modUser32.NewProc("LoadIconW")
	procLoadImageW       = modUser32.NewProc("LoadImageW")
	procDestroyIcon      = modUser32.NewProc("DestroyIcon")
	procCreatePopupMenu  = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW      = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenu   = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu      = modUser32.NewProc("DestroyMenu")
	procGetCursorPos     = modUser32.NewProc("GetCursorPos")
	procSetForegroundWnd = modUser32.NewProc("SetForegroundWindow")
	procGetSystemMetrics = modUser32.NewProc("GetSystemMetrics")

	procShellNotifyIconW = modShell32.NewProc("Shell_NotifyIconW")
	procExtractIconExW   = modShell32.NewProc("ExtractIconExW")
	procGetModuleHandleW = modKernel32.NewProc("GetModuleHandleW")

	trayWindowProc = windows.NewCallback(trayWndProc)

	activeTrayMu sync.RWMutex
	activeTray   *trayController
)

type point struct {
	X int32
	Y int32
}

type msg struct {
	HWnd     windows.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type notifyIconData struct {
	CbSize            uint32
	HWnd              windows.Handle
	UID               uint32
	UFlags            uint32
	UCallbackMessage  uint32
	HIcon             windows.Handle
	SzTip             [128]uint16
	State             uint32
	StateMask         uint32
	SzInfo            [256]uint16
	UTimeoutOrVersion uint32
	SzInfoTitle       [64]uint16
	DwInfoFlags       uint32
	GuidItem          windows.GUID
	HBalloonIcon      windows.Handle
}

type trayController struct {
	app *App

	startOnce sync.Once
	stopOnce  sync.Once
	doneCh    chan struct{}

	hwndMu sync.RWMutex
	hwnd   windows.Handle

	trayIcon      windows.Handle
	trayIconOwned bool
}

func newTrayController(app *App) *trayController {
	return &trayController{
		app:    app,
		doneCh: make(chan struct{}),
	}
}

func (t *trayController) Start() {
	if t == nil {
		return
	}
	t.startOnce.Do(func() {
		setActiveTrayController(t)
		go t.runLoop()
	})
}

func (t *trayController) Stop() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		hwnd := t.getHWND()
		if hwnd != 0 {
			procPostMessageW.Call(uintptr(hwnd), wmClose, 0, 0)
		}
		<-t.doneCh
		setActiveTrayController(nil)
	})
}

func (t *trayController) runLoop() {
	defer close(t.doneCh)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className, _ := windows.UTF16PtrFromString("CloudHelperProbeManagerTrayWindow")
	windowName, _ := windows.UTF16PtrFromString("Probe Manager Tray")
	showText, _ := windows.UTF16PtrFromString("显示主窗口")
	exitText, _ := windows.UTF16PtrFromString("退出")

	hinst, _, _ := procGetModuleHandleW.Call(0)
	hIcon, _, _ := procLoadIconW.Call(0, idiApplication)

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   trayWindowProc,
		HInstance:     windows.Handle(hinst),
		HIcon:         windows.Handle(hIcon),
		LpszClassName: className,
	}
	atom, _, regErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 && regErr != windows.ERROR_CLASS_ALREADY_EXISTS {
		return
	}

	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		0,
		0,
		0,
		0,
		0,
		0,
		0,
		hinst,
		0,
	)
	if hwnd == 0 {
		return
	}
	t.setHWND(windows.Handle(hwnd))
	defer t.setHWND(0)

	if !t.addTrayIcon(windows.Handle(hwnd)) {
		procDestroyWindow.Call(hwnd)
		return
	}
	defer t.removeTrayIcon(windows.Handle(hwnd))

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	_ = showText
	_ = exitText
}

func (t *trayController) setHWND(hwnd windows.Handle) {
	t.hwndMu.Lock()
	t.hwnd = hwnd
	t.hwndMu.Unlock()
}

func (t *trayController) getHWND() windows.Handle {
	t.hwndMu.RLock()
	defer t.hwndMu.RUnlock()
	return t.hwnd
}

func (t *trayController) addTrayIcon(hwnd windows.Handle) bool {
	hIcon, owned := loadDefaultTrayIcon()
	if hIcon == 0 {
		hLoaded, _, _ := procLoadIconW.Call(0, idiApplication)
		hIcon = windows.Handle(hLoaded)
		owned = false
	}
	t.trayIcon = hIcon
	t.trayIconOwned = owned

	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = wmTrayMessage
	nid.HIcon = hIcon
	copyUTF16(nid.SzTip[:], "Probe Manager")

	ret, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if ret == 0 {
		t.releaseTrayIcon()
		return false
	}
	return true
}

func (t *trayController) removeTrayIcon(hwnd windows.Handle) {
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	t.releaseTrayIcon()
}

func (t *trayController) handleCommand(cmd uint16) {
	switch cmd {
	case trayCmdShow:
		if t.app != nil {
			t.app.ShowMainWindowFromTray()
		}
	case trayCmdExit:
		if t.app != nil {
			t.app.ExitFromTray()
		}
	}
}

func (t *trayController) showContextMenu(hwnd windows.Handle) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	showText, _ := windows.UTF16PtrFromString("显示主窗口")
	exitText, _ := windows.UTF16PtrFromString("退出")

	procAppendMenuW.Call(menu, mfString, trayCmdShow, uintptr(unsafe.Pointer(showText)))
	procAppendMenuW.Call(menu, mfString, trayCmdExit, uintptr(unsafe.Pointer(exitText)))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWnd.Call(uintptr(hwnd))

	cmd, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmRightButton|tpmReturnCmd,
		uintptr(pt.X),
		uintptr(pt.Y),
		0,
		uintptr(hwnd),
		0,
	)
	if cmd != 0 {
		t.handleCommand(uint16(cmd))
	}
	procPostMessageW.Call(uintptr(hwnd), wmNull, 0, 0)
}

func trayWndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	t := getActiveTrayController()
	switch message {
	case wmTrayMessage:
		if t != nil {
			switch uint32(lParam) {
			case wmRButtonUp:
				t.showContextMenu(windows.Handle(hwnd))
			case wmLButtonUp, wmLButtonDbl:
				t.handleCommand(trayCmdShow)
			}
		}
		return 0
	case wmCommand:
		if t != nil {
			t.handleCommand(uint16(wParam & 0xFFFF))
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	default:
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return ret
	}
}

func loadDefaultTrayIcon() (windows.Handle, bool) {
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		exePtr, convErr := windows.UTF16PtrFromString(exe)
		if convErr == nil {
			var small windows.Handle
			count, _, _ := procExtractIconExW.Call(
				uintptr(unsafe.Pointer(exePtr)),
				0,
				0,
				uintptr(unsafe.Pointer(&small)),
				1,
			)
			if count > 0 && small != 0 {
				return small, true
			}
		}
	}

	iconPaths := []string{
		filepath.Join(".", "build", "windows", "icon.ico"),
	}
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		exeDir := filepath.Dir(exe)
		iconPaths = append(iconPaths,
			filepath.Join(exeDir, "build", "windows", "icon.ico"),
			filepath.Join(exeDir, "icon.ico"),
		)
	}

	cx, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	cy, _, _ := procGetSystemMetrics.Call(smCySmIcon)
	if cx <= 0 {
		cx = 16
	}
	if cy <= 0 {
		cy = 16
	}

	for _, iconPath := range iconPaths {
		abs, absErr := filepath.Abs(iconPath)
		if absErr != nil {
			continue
		}
		if _, statErr := os.Stat(abs); statErr != nil {
			continue
		}
		pathPtr, convErr := windows.UTF16PtrFromString(abs)
		if convErr != nil {
			continue
		}
		hIcon, _, _ := procLoadImageW.Call(
			0,
			uintptr(unsafe.Pointer(pathPtr)),
			imageIcon,
			cx,
			cy,
			lrLoadFromFile,
		)
		if hIcon != 0 {
			return windows.Handle(hIcon), true
		}
	}

	return 0, false
}

func (t *trayController) releaseTrayIcon() {
	if t == nil {
		return
	}
	if t.trayIconOwned && t.trayIcon != 0 {
		procDestroyIcon.Call(uintptr(t.trayIcon))
	}
	t.trayIcon = 0
	t.trayIconOwned = false
}

func copyUTF16(dst []uint16, text string) {
	encoded, err := syscall.UTF16FromString(text)
	if err != nil {
		return
	}
	if len(encoded) == 0 || len(dst) == 0 {
		return
	}
	n := len(encoded)
	if n > len(dst) {
		n = len(dst)
	}
	copy(dst[:n], encoded[:n])
	if n == len(dst) {
		dst[n-1] = 0
	}
}

func setActiveTrayController(t *trayController) {
	activeTrayMu.Lock()
	activeTray = t
	activeTrayMu.Unlock()
}

func getActiveTrayController() *trayController {
	activeTrayMu.RLock()
	defer activeTrayMu.RUnlock()
	return activeTray
}
