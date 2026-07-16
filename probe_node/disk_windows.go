//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var probeDiskProcGetDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func readDiskUsageRoot() (total uint64, used uint64) {
	root := windowsDiskUsageRoot()
	rootPtr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return 0, 0
	}
	var freeAvailable, totalBytes, totalFree uint64
	ret, _, _ := probeDiskProcGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(rootPtr)),
		uintptr(unsafe.Pointer(&freeAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ret == 0 {
		return 0, 0
	}
	total = totalBytes
	if totalBytes >= totalFree {
		used = totalBytes - totalFree
	}
	return total, used
}

func windowsDiskUsageRoot() string {
	if exe, err := os.Executable(); err == nil {
		if root := windowsVolumeRoot(exe); root != "" {
			return root
		}
	}
	if wd, err := os.Getwd(); err == nil {
		if root := windowsVolumeRoot(wd); root != "" {
			return root
		}
	}
	return `C:\`
}

func windowsVolumeRoot(path string) string {
	volume := filepath.VolumeName(path)
	if volume == "" {
		return ""
	}
	return strings.TrimRight(volume, `\/`) + `\`
}
