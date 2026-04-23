//go:build windows

package main

import "fmt"

func installProbeLocalTUNDriver() error {
	if err := ensureProbeEmbeddedWintunLibrary(); err != nil {
		return fmt.Errorf("prepare wintun library: %w", err)
	}
	return nil
}
