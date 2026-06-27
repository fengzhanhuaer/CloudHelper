//go:build windows

package main

import "errors"

func flushProbeLocalSystemDNSCache() error {
	if _, err := runProbeLocalCommand(probeLocalSystemDNSCacheFlushTimeout, "ipconfig", "/flushdns"); err == nil {
		return nil
	} else {
		if _, psErr := runProbeLocalCommand(
			probeLocalSystemDNSCacheFlushTimeout,
			"powershell",
			"-NoLogo",
			"-NoProfile",
			"-NonInteractive",
			"-ExecutionPolicy",
			"Bypass",
			"-Command",
			"Clear-DnsClientCache",
		); psErr == nil {
			return nil
		} else {
			return errors.Join(err, psErr)
		}
	}
}
