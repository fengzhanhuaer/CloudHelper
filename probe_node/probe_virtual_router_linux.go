//go:build linux

package main

import (
	"fmt"
	"net"
	"strings"
)

func ensureProbeVirtualRouterPlatformInterfaceIP(ip string) error {
	cleanIP := strings.TrimSpace(ip)
	if cleanIP == "" {
		return nil
	}
	if parsed := net.ParseIP(cleanIP); parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("invalid linux virtual router ipv4: %s", cleanIP)
	}
	dev, err := ensureProbeLocalLinuxTUNDeviceReady()
	if err != nil {
		return err
	}
	if cleanIP != probeLocalTUNInterfaceIPv4 {
		if err := ensureProbeLocalLinuxInterfaceIPv4(dev, cleanIP); err != nil {
			return err
		}
	}
	if err := ensureProbeLocalLinuxVirtualRoute(dev, cleanIP); err != nil {
		return err
	}
	if err := startProbeLocalTUNDataPlane(); err != nil {
		return err
	}
	return nil
}
