//go:build linux

package main

import "syscall"

func readDiskUsageRoot() (total uint64, used uint64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, 0
	}

	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	if total >= free {
		used = total - free
	}
	return total, used
}
