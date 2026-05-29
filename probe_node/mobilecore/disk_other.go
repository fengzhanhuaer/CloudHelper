//go:build !linux && !android

package mobilecore

func readDiskUsageRoot() (total uint64, used uint64) {
	return 0, 0
}
