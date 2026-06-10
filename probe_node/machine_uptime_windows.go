//go:build windows

package main

import "syscall"

var kernel32GetTickCount64 = syscall.NewLazyDLL("kernel32.dll").NewProc("GetTickCount64")

func readMachineUptimeSeconds() int64 {
	ms, _, _ := kernel32GetTickCount64.Call()
	if ms == 0 {
		return 0
	}
	return int64(ms) / 1000
}
