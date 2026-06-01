//go:build !windows

package main

import "syscall"

func collectDiskPlatform(path string) disk {
	out := disk{Path: path}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		out.Error = err.Error()
		return out
	}
	out.TotalBytes = stat.Blocks * uint64(stat.Bsize)
	out.FreeBytes = stat.Bavail * uint64(stat.Bsize)
	return out
}
