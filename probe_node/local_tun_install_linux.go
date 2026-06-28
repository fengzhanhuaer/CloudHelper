//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const probeLocalLinuxDefaultTUNDeviceName = "cloudhelper0"

func init() {
	probeLocalDetectTUNInstalled = detectProbeLocalTUNInstalledLinux
	probeLocalResetTUNDetectInstalledHook = func() { probeLocalDetectTUNInstalled = detectProbeLocalTUNInstalledLinux }
}

func installProbeLocalTUNDriver() error {
	_, err := ensureProbeLocalLinuxTUNDeviceReady()
	return err
}

func detectProbeLocalTUNInstalledLinux() (bool, error) {
	if err := checkProbeLocalLinuxTUNDeviceNode(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if _, err := probeLocalLinuxLookPath("ip"); err != nil {
		return false, fmt.Errorf("ip command not found: %w", err)
	}
	dev, err := resolveProbeLocalLinuxTUNDeviceName()
	if err != nil {
		return false, err
	}
	if _, err := probeLocalLinuxRunCommand(5*time.Second, "ip", "link", "show", "dev", dev); err != nil {
		if isProbeLocalLinuxDeviceMissingErr(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func ensureProbeLocalLinuxTUNDeviceReady() (string, error) {
	if err := checkProbeLocalLinuxTUNDeviceNode(); err != nil {
		return "", err
	}
	if _, err := probeLocalLinuxLookPath("ip"); err != nil {
		return "", fmt.Errorf("ip command not found: %w", err)
	}
	dev, err := resolveProbeLocalLinuxTUNDeviceName()
	if err != nil {
		return "", err
	}
	if _, err := probeLocalLinuxRunCommand(5*time.Second, "ip", "link", "show", "dev", dev); err != nil {
		if !isProbeLocalLinuxDeviceMissingErr(err) {
			return "", fmt.Errorf("inspect linux tun device failed: dev=%s: %w", dev, err)
		}
		if _, createErr := probeLocalLinuxRunCommand(5*time.Second, "ip", "tuntap", "add", "dev", dev, "mode", "tun"); createErr != nil {
			return "", fmt.Errorf("create linux tun device failed: dev=%s: %w", dev, createErr)
		}
		logProbeInfof("probe local linux tun device created: dev=%s", dev)
	}
	if _, err := probeLocalLinuxRunCommand(5*time.Second, "ip", "link", "set", "dev", dev, "up"); err != nil {
		return "", fmt.Errorf("set linux tun device up failed: dev=%s: %w", dev, err)
	}
	if err := ensureProbeLocalLinuxInterfaceIPv4(dev, probeLocalTUNInterfaceIPv4); err != nil {
		return "", err
	}
	return dev, nil
}

func checkProbeLocalLinuxTUNDeviceNode() error {
	info, err := probeLocalLinuxStat("/dev/net/tun")
	if err != nil {
		return fmt.Errorf("check /dev/net/tun failed: %w", err)
	}
	if info.IsDir() {
		return errors.New("/dev/net/tun is not a character device")
	}
	return nil
}

func resolveProbeLocalLinuxTUNDeviceName() (string, error) {
	dev := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_DEV"))
	if dev == "" {
		dev = probeLocalLinuxDefaultTUNDeviceName
	}
	if err := validateProbeLocalLinuxTUNDeviceName(dev); err != nil {
		return "", err
	}
	return dev, nil
}

func validateProbeLocalLinuxTUNDeviceName(dev string) error {
	clean := strings.TrimSpace(dev)
	if clean == "" {
		return errors.New("linux tun device name is empty")
	}
	if len(clean) > 15 {
		return fmt.Errorf("linux tun device name is too long: %s", clean)
	}
	if strings.ContainsAny(clean, " \t\r\n/") {
		return fmt.Errorf("linux tun device name contains invalid characters: %s", clean)
	}
	return nil
}

func ensureProbeLocalLinuxInterfaceIPv4(dev string, ip string) error {
	cidr := fmt.Sprintf("%s/%d", ip, probeLocalTUNRouteIPv4PrefixLen)
	if _, err := probeLocalLinuxRunCommand(5*time.Second, "ip", "-4", "addr", "replace", cidr, "dev", dev); err != nil {
		return fmt.Errorf("replace linux tun ipv4 failed: dev=%s ip=%s: %w", dev, cidr, err)
	}
	return nil
}
