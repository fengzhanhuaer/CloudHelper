package main

import "log"

func runProbeNodeEntry(options probeLaunchOptions) error {
	if err := ensureProbeEmbeddedWintunLibrary(); err != nil {
		log.Printf("warning: failed to prepare embedded wintun library: %v", err)
	}
	return runProbeNode(options)
}
