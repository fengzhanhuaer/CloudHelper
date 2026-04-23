//go:build !windows && !linux

package main

import (
	"fmt"
	"runtime"
)

func installProbeLocalTUNDriver() error {
	return fmt.Errorf("%w: %s", errProbeLocalTUNUnsupported, runtime.GOOS)
}
