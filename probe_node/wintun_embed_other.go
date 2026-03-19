//go:build !windows

package main

func ensureProbeEmbeddedWintunLibrary() error {
	return nil
}
