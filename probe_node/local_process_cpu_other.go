//go:build !windows

package main

import "time"

func currentProbeProcessCPUSample() probeProcessCPUSample {
	return probeProcessCPUSample{At: time.Now().UTC()}
}
