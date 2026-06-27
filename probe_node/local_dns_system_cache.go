package main

import "time"

const probeLocalSystemDNSCacheFlushTimeout = 4 * time.Second

func flushProbeLocalSystemDNSCacheAfterChange(reason string) {
	if err := probeLocalFlushSystemDNSCache(); err != nil {
		logProbeWarnf("probe local system dns cache flush failed: reason=%s err=%v", reason, err)
		return
	}
	logProbeInfof("probe local system dns cache flushed: reason=%s", reason)
}
