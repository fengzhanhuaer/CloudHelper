//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	probeSystemKernel32                 = syscall.NewLazyDLL("kernel32.dll")
	probeSystemProcGetSystemTimes       = probeSystemKernel32.NewProc("GetSystemTimes")
	probeSystemProcGlobalMemoryStatusEx = probeSystemKernel32.NewProc("GlobalMemoryStatusEx")
)

type probeWindowsMemoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func readWindowsCPUSnapshot() (cpuSnapshot, bool) {
	var idleTime, kernelTime, userTime syscall.Filetime
	ret, _, _ := probeSystemProcGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idleTime)),
		uintptr(unsafe.Pointer(&kernelTime)),
		uintptr(unsafe.Pointer(&userTime)),
	)
	if ret == 0 {
		return cpuSnapshot{}, false
	}
	idle := windowsFiletimeTicks(idleTime)
	kernel := windowsFiletimeTicks(kernelTime)
	user := windowsFiletimeTicks(userTime)
	total := kernel + user
	if total == 0 || idle > total {
		return cpuSnapshot{}, false
	}
	return cpuSnapshot{total: total, idle: idle}, true
}

func readWindowsMemoryInfo() (memoryTotal uint64, memoryUsed uint64, swapTotal uint64, swapUsed uint64) {
	var status probeWindowsMemoryStatusEx
	status.Length = uint32(unsafe.Sizeof(status))
	ret, _, _ := probeSystemProcGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if ret == 0 {
		return 0, 0, 0, 0
	}

	memoryTotal = status.TotalPhys
	if status.TotalPhys >= status.AvailPhys {
		memoryUsed = status.TotalPhys - status.AvailPhys
	}

	if status.TotalPageFile > status.TotalPhys {
		swapTotal = status.TotalPageFile - status.TotalPhys
	}
	pageFileUsed := uint64(0)
	if status.TotalPageFile >= status.AvailPageFile {
		pageFileUsed = status.TotalPageFile - status.AvailPageFile
	}
	if pageFileUsed > memoryUsed {
		swapUsed = pageFileUsed - memoryUsed
	}
	if swapUsed > swapTotal {
		swapUsed = swapTotal
	}
	return memoryTotal, memoryUsed, swapTotal, swapUsed
}

func windowsFiletimeTicks(value syscall.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}
