//go:build !windows

package main

func enterProbeNodeStartupGate() (func(), error) {
	return func() {}, nil
}

func releaseProbeNodeStartupGate() {}
