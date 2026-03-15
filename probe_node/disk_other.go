//go:build !linux

package main

func readDiskUsageRoot() (total uint64, used uint64) {
	return 0, 0
}
