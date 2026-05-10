//go:build !windows

package main

func startProbeLocalTUNPacketStack() error { return nil }
func stopProbeLocalTUNPacketStack() error  { return nil }

func ensureProbeLocalExplicitDirectBypassForTarget(string) error { return nil }
