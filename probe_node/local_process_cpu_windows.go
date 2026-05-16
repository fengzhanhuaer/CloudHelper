//go:build windows

package main

import (
	"syscall"
	"time"
)

func currentProbeProcessCPUSample() probeProcessCPUSample {
	now := time.Now().UTC()
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		return probeProcessCPUSample{At: now}
	}
	var creationTime, exitTime, kernelTime, userTime syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &creationTime, &exitTime, &kernelTime, &userTime); err != nil {
		return probeProcessCPUSample{At: now}
	}
	return probeProcessCPUSample{
		At:        now,
		Total:     probeProcessFiletimeDuration(kernelTime) + probeProcessFiletimeDuration(userTime),
		Available: true,
	}
}

func probeProcessFiletimeDuration(value syscall.Filetime) time.Duration {
	ticks := uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
	return time.Duration(ticks * 100)
}
