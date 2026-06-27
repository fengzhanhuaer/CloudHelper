//go:build !windows && !linux

package main

func flushProbeLocalSystemDNSCache() error {
	return nil
}
