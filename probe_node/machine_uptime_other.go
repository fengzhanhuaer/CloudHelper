//go:build !linux && !windows

package main

func readMachineUptimeSeconds() int64 {
	return 0
}
