//go:build !linux && !android

package mobilecore

func readNetworkTrafficCounters() (rxBytes, txBytes uint64, ok bool) {
	return 0, 0, false
}
