//go:build !linux && !android && !windows

package main

func readNetworkTrafficCounters() (rxBytes, txBytes uint64, ok bool) {
	return 0, 0, false
}
