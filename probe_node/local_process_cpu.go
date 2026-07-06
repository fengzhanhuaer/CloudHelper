package main

import "time"

type probeProcessCPUSample struct {
	At        time.Time
	Total     time.Duration
	Available bool
}
