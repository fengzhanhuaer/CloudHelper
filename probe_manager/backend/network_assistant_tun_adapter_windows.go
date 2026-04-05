//go:build windows

package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createConfiguredTUNAdapter(libraryPath, adapterName, tunnelType, requestedGUID string) (uintptr, error) {
	path := strings.TrimSpace(libraryPath)
	if path == "" {
		return 0, errors.New("empty wintun.dll path")
	}
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("wintun.dll is not ready at %s: %w", path, err)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}

	name := strings.TrimSpace(adapterName)
	if name == "" {
		return 0, errors.New("empty tun adapter name")
	}

	kind := strings.TrimSpace(tunnelType)
	if kind == "" {
		kind = name
	}

	wintunDLL := syscall.NewLazyDLL(path)
	createProc := wintunDLL.NewProc("WintunCreateAdapter")
	openProc := wintunDLL.NewProc("WintunOpenAdapter")

	if err := wintunDLL.Load(); err != nil {
		return 0, fmt.Errorf("failed to load wintun.dll: %w", err)
	}

	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, fmt.Errorf("invalid tun adapter name: %w", err)
	}
	kindPtr, err := syscall.UTF16PtrFromString(kind)
	if err != nil {
		return 0, fmt.Errorf("invalid tun tunnel type: %w", err)
	}

	guidArg := uintptr(0)
	if guidText := strings.TrimSpace(requestedGUID); guidText != "" {
		reqGUID, parseErr := windows.GUIDFromString(guidText)
		if parseErr != nil {
			return 0, fmt.Errorf("invalid requested adapter guid: %w", parseErr)
		}
		guidArg = uintptr(unsafe.Pointer(&reqGUID))
	}

	handle, _, callErr := createProc.Call(
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(kindPtr)),
		guidArg,
	)
	if handle != 0 {
		return handle, nil
	}

	// If create fails because adapter already exists (or detection missed it), reuse the existing adapter handle.
	openHandle, _, openErr := openProc.Call(uintptr(unsafe.Pointer(namePtr)))
	if openHandle != 0 {
		return openHandle, nil
	}

	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		if errors.Is(callErr, syscall.ERROR_ACCESS_DENIED) {
			return 0, errors.New("WintunCreateAdapter access denied, please run manager as administrator")
		}
		return 0, fmt.Errorf("WintunCreateAdapter failed: %s", formatWindowsCallErr(callErr))
	}
	if openErr != nil && !errors.Is(openErr, syscall.Errno(0)) {
		if errors.Is(openErr, syscall.ERROR_ACCESS_DENIED) {
			return 0, errors.New("WintunOpenAdapter access denied, please run manager as administrator")
		}
		return 0, fmt.Errorf("WintunOpenAdapter failed: %s", formatWindowsCallErr(openErr))
	}
	return 0, fmt.Errorf("wintun adapter creation/open returned empty adapter handle (create=%s open=%s)", formatWindowsCallErr(callErr), formatWindowsCallErr(openErr))
}

func formatWindowsCallErr(err error) string {
	if err == nil {
		return "nil"
	}
	if errors.Is(err, syscall.Errno(0)) {
		return "errno=0"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == 0 {
			return "errno=0"
		}
		message := strings.TrimSpace(errno.Error())
		if message == "" || strings.EqualFold(message, "unknown error") {
			return fmt.Sprintf("errno=%d", uint32(errno))
		}
		return fmt.Sprintf("errno=%d (%s)", uint32(errno), message)
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return fmt.Sprintf("%T", err)
	}
	return message
}

func closeConfiguredTUNAdapter(libraryPath string, handle uintptr) error {
	if handle == 0 {
		return nil
	}

	path := strings.TrimSpace(libraryPath)
	if path == "" {
		return errors.New("empty wintun.dll path while closing adapter")
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}

	wintunDLL := syscall.NewLazyDLL(path)
	closeProc := wintunDLL.NewProc("WintunCloseAdapter")
	if err := wintunDLL.Load(); err != nil {
		return fmt.Errorf("failed to load wintun.dll for close: %w", err)
	}
	_, _, callErr := closeProc.Call(handle)
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return fmt.Errorf("WintunCloseAdapter call failed: %w", callErr)
	}
	return nil
}
