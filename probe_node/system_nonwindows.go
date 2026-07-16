//go:build !windows

package main

func readWindowsCPUSnapshot() (cpuSnapshot, bool) {
	return cpuSnapshot{}, false
}

func readWindowsMemoryInfo() (memoryTotal uint64, memoryUsed uint64, swapTotal uint64, swapUsed uint64) {
	return 0, 0, 0, 0
}
