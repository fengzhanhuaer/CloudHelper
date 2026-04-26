//go:build windows

package main

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

const probeLocalWindowsErrorInsufficientBuffer = 122

type probeLocalWindowsNetAdapter struct {
	InterfaceIndex       int
	InterfaceLUID        uint64
	Name                 string
	InterfaceDescription string
}

func detectProbeLocalWintunAdapter() (bool, error) {
	_, exists, err := findProbeLocalWintunAdapter()
	if err != nil {
		return false, err
	}
	return exists, nil
}

func findProbeLocalWintunAdapter() (probeLocalWindowsNetAdapter, bool, error) {
	adapters, err := listProbeLocalWindowsNetAdapters()
	if err != nil {
		return probeLocalWindowsNetAdapter{}, false, err
	}
	for _, adapter := range adapters {
		if probeLocalWintunAdapterMatches(adapter.Name, adapter.InterfaceDescription) {
			return adapter, true, nil
		}
	}
	return probeLocalWindowsNetAdapter{}, false, nil
}

func findProbeLocalWintunAdapterLUID() (uint64, bool, error) {
	adapter, exists, err := findProbeLocalWintunAdapter()
	if err != nil {
		return 0, false, err
	}
	if !exists {
		return 0, false, nil
	}
	if adapter.InterfaceLUID == 0 {
		return 0, false, nil
	}
	return adapter.InterfaceLUID, true, nil
}

func probeLocalWintunAdapterMatches(name, description string) bool {
	cleanName := strings.TrimSpace(name)
	cleanDesc := strings.TrimSpace(description)
	if strings.EqualFold(cleanName, probeLocalTUNAdapterName) || strings.HasPrefix(strings.ToLower(cleanName), strings.ToLower(probeLocalTUNAdapterName)+" ") {
		return true
	}
	if strings.EqualFold(cleanDesc, probeLocalTUNAdapterDescription) {
		return true
	}
	return false
}

func listProbeLocalWindowsNetAdapters() ([]probeLocalWindowsNetAdapter, error) {
	flags := uint32(windows.GAA_FLAG_INCLUDE_PREFIX)
	var size uint32 = 15 * 1024
	buf := make([]byte, size)
	for i := 0; i < 3; i++ {
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		errCode := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, first, &size)
		if errCode == nil {
			return parseProbeLocalWindowsNetAdapters(first), nil
		}
		if errors.Is(errCode, syscall.Errno(probeLocalWindowsErrorInsufficientBuffer)) {
			buf = make([]byte, size)
			continue
		}
		return nil, errCode
	}
	return nil, errors.New("GetAdaptersAddresses failed after retries")
}

func parseProbeLocalWindowsNetAdapters(first *windows.IpAdapterAddresses) []probeLocalWindowsNetAdapter {
	items := make([]probeLocalWindowsNetAdapter, 0)
	for curr := first; curr != nil; curr = curr.Next {
		items = append(items, probeLocalWindowsNetAdapter{
			InterfaceIndex:       int(curr.IfIndex),
			InterfaceLUID:        curr.Luid,
			Name:                 strings.TrimSpace(windows.UTF16PtrToString(curr.FriendlyName)),
			InterfaceDescription: strings.TrimSpace(windows.UTF16PtrToString(curr.Description)),
		})
	}
	return items
}

func createProbeLocalWintunAdapter(libraryPath, adapterName, tunnelType string) (uintptr, error) {
	path := strings.TrimSpace(libraryPath)
	if path == "" {
		return 0, errors.New("empty wintun.dll path")
	}
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("wintun.dll is not ready at %s: %w", path, err)
	}
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
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
	if guidText := strings.TrimSpace(probeLocalTUNAdapterRequestedGUID); guidText != "" {
		reqGUID, parseErr := windows.GUIDFromString(guidText)
		if parseErr != nil {
			return 0, fmt.Errorf("invalid requested adapter guid: %w", parseErr)
		}
		guidArg = uintptr(unsafe.Pointer(&reqGUID))
	}

	handle, _, createErr := createProc.Call(
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(kindPtr)),
		guidArg,
	)
	if handle != 0 {
		return handle, nil
	}

	openHandle, _, openErr := openProc.Call(uintptr(unsafe.Pointer(namePtr)))
	if openHandle != 0 {
		return openHandle, nil
	}

	if createErr != nil && !errors.Is(createErr, syscall.Errno(0)) {
		if errors.Is(createErr, syscall.ERROR_ACCESS_DENIED) {
			return 0, errors.New("WintunCreateAdapter access denied, please run probe as administrator")
		}
		return 0, fmt.Errorf("WintunCreateAdapter failed: %s", formatProbeLocalWindowsCallErr(createErr))
	}
	if openErr != nil && !errors.Is(openErr, syscall.Errno(0)) {
		if errors.Is(openErr, syscall.ERROR_ACCESS_DENIED) {
			return 0, errors.New("WintunOpenAdapter access denied, please run probe as administrator")
		}
		return 0, fmt.Errorf("WintunOpenAdapter failed: %s", formatProbeLocalWindowsCallErr(openErr))
	}
	return 0, fmt.Errorf("wintun adapter creation/open returned empty adapter handle (create=%s open=%s)", formatProbeLocalWindowsCallErr(createErr), formatProbeLocalWindowsCallErr(openErr))
}

func closeProbeLocalWintunAdapter(libraryPath string, handle uintptr) error {
	if handle == 0 {
		return nil
	}
	path := strings.TrimSpace(libraryPath)
	if path == "" {
		return errors.New("empty wintun.dll path while closing adapter")
	}
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
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

func getProbeLocalWintunAdapterLUIDFromHandle(libraryPath string, handle uintptr) (uint64, error) {
	if handle == 0 {
		return 0, errors.New("empty wintun adapter handle")
	}
	path := strings.TrimSpace(libraryPath)
	if path == "" {
		return 0, errors.New("empty wintun.dll path")
	}
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
	}
	wintunDLL := syscall.NewLazyDLL(path)
	getLUIDProc := wintunDLL.NewProc("WintunGetAdapterLUID")
	if err := wintunDLL.Load(); err != nil {
		return 0, fmt.Errorf("failed to load wintun.dll for luid query: %w", err)
	}
	var luid uint64
	_, _, callErr := getLUIDProc.Call(
		handle,
		uintptr(unsafe.Pointer(&luid)),
	)
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return 0, fmt.Errorf("WintunGetAdapterLUID call failed: %w", callErr)
	}
	if luid == 0 {
		return 0, errors.New("WintunGetAdapterLUID returned zero")
	}
	return luid, nil
}

func formatProbeLocalWindowsCallErr(err error) string {
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
