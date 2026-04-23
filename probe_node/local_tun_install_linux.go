//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
)

func installProbeLocalTUNDriver() error {
	info, err := os.Stat("/dev/net/tun")
	if err != nil {
		return fmt.Errorf("check /dev/net/tun failed: %w", err)
	}
	if info.IsDir() {
		return errors.New("/dev/net/tun is not a character device")
	}
	return nil
}
