//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

var (
	probeLocalLinuxStat       = os.Stat
	probeLocalLinuxLookPath   = exec.LookPath
	probeLocalLinuxRunCommand = runProbeLocalCommand
)

func ensureProbeRouteDirectBypass(string) error {
	return nil
}

func cleanupProbeRouteDirectBypassForVirtualRouterRules(probeVirtualRouterConfig) {
}

func deleteProbeRouteLinuxSplitRoute(prefix, dev, gateway string) error {
	if strings.TrimSpace(dev) == "" {
		return nil
	}
	args := []string{"-4", "route", "del", prefix}
	if gateway != "" {
		args = append(args, "via", gateway)
	}
	args = append(args, "dev", dev)
	_, err := probeLocalLinuxRunCommand(5*time.Second, "ip", args...)
	if err != nil && !isProbeLocalLinuxRouteMissingErr(err) {
		return err
	}
	return nil
}

func isProbeLocalLinuxRouteMissingErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "no such process") || strings.Contains(text, "no such file or directory")
}

func isProbeLocalLinuxDeviceMissingErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "does not exist") ||
		strings.Contains(text, "cannot find device") ||
		strings.Contains(text, "device not found") ||
		strings.Contains(text, "no such device") ||
		strings.Contains(text, "no such file or directory")
}

func currentProbeLocalSystemDNSServers() []string {
	return nil
}

func uninstallProbeLocalTUNDriver() error {
	dev, err := resolveProbeLocalLinuxTUNDeviceName()
	if err != nil {
		return err
	}
	if dev != probeLocalLinuxDefaultTUNDeviceName {
		logProbeInfof("probe local linux tun uninstall skipped custom device: dev=%s", dev)
		return nil
	}
	if _, err := probeLocalLinuxLookPath("ip"); err != nil {
		return fmt.Errorf("ip command not found: %w", err)
	}
	_, err = probeLocalLinuxRunCommand(5*time.Second, "ip", "link", "del", "dev", dev)
	if err != nil && !isProbeLocalLinuxDeviceMissingErr(err) {
		return fmt.Errorf("delete linux tun device failed: dev=%s: %w", dev, err)
	}
	return nil
}
