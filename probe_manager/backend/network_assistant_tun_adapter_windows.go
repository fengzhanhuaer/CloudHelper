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
)

func createConfiguredTUNAdapter(libraryPath, adapterName, tunnelType string) (uintptr, error) {
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

	handle, _, callErr := createProc.Call(
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(kindPtr)),
		0,
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
		return 0, fmt.Errorf("WintunCreateAdapter failed: %w", callErr)
	}
	if openErr != nil && !errors.Is(openErr, syscall.Errno(0)) {
		if errors.Is(openErr, syscall.ERROR_ACCESS_DENIED) {
			return 0, errors.New("WintunOpenAdapter access denied, please run manager as administrator")
		}
		return 0, fmt.Errorf("WintunOpenAdapter failed: %w", openErr)
	}
	return 0, errors.New("wintun adapter creation/open returned empty adapter handle")
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
