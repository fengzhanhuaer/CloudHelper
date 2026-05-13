//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeLocalLinuxRouteSplitPrefixA = "0.0.0.0/1"
	probeLocalLinuxRouteSplitPrefixB = "128.0.0.0/1"
	probeLocalLinuxRouteMetric       = 3
)

var probeLocalLinuxTakeoverState = struct {
	mu                   sync.Mutex
	enabled              bool
	defaultRouteSnapshot string
	tunDevice            string
	tunGateway           string
}{}

var (
	probeLocalLinuxStat       = os.Stat
	probeLocalLinuxLookPath   = exec.LookPath
	probeLocalLinuxRunCommand = runProbeLocalCommand
)

func applyProbeLocalProxyTakeover() error {
	info, err := probeLocalLinuxStat("/dev/net/tun")
	if err != nil {
		return fmt.Errorf("check /dev/net/tun failed: %w", err)
	}
	if info.IsDir() {
		return errors.New("/dev/net/tun is not a character device")
	}
	if _, err := probeLocalLinuxLookPath("ip"); err != nil {
		return fmt.Errorf("ip command not found: %w", err)
	}
	dev, gateway, err := resolveProbeLocalLinuxRouteTarget()
	if err != nil {
		return err
	}

	probeLocalLinuxTakeoverState.mu.Lock()
	if probeLocalLinuxTakeoverState.enabled {
		probeLocalLinuxTakeoverState.mu.Unlock()
		return nil
	}
	probeLocalLinuxTakeoverState.mu.Unlock()

	defaultRoute, err := probeLocalLinuxRunCommand(5*time.Second, "ip", "-4", "route", "show", "default")
	if err != nil {
		return fmt.Errorf("inspect linux default route failed: %w", err)
	}
	if strings.TrimSpace(defaultRoute) == "" {
		return errors.New("linux default ipv4 route is empty")
	}

	appliedPrefixes := make([]string, 0, 5)
	for _, prefix := range probeLocalLinuxTakeoverRoutePrefixes() {
		if routeErr := ensureProbeLocalLinuxSplitRoute(prefix, dev, gateway); routeErr != nil {
			var rollbackErr error
			for i := len(appliedPrefixes) - 1; i >= 0; i-- {
				if delErr := deleteProbeLocalLinuxSplitRoute(appliedPrefixes[i], dev, gateway); delErr != nil {
					rollbackErr = errors.Join(rollbackErr, delErr)
				}
			}
			if rollbackErr != nil {
				return fmt.Errorf("apply linux takeover route %s failed: %w (rollback: %v)", prefix, routeErr, rollbackErr)
			}
			return fmt.Errorf("apply linux takeover route %s failed: %w", prefix, routeErr)
		}
		appliedPrefixes = append(appliedPrefixes, prefix)
	}

	probeLocalLinuxTakeoverState.mu.Lock()
	probeLocalLinuxTakeoverState.enabled = true
	probeLocalLinuxTakeoverState.defaultRouteSnapshot = defaultRoute
	probeLocalLinuxTakeoverState.tunDevice = dev
	probeLocalLinuxTakeoverState.tunGateway = gateway
	probeLocalLinuxTakeoverState.mu.Unlock()

	logProbeInfof("probe local proxy takeover applied on linux: dev=%s gateway=%s default_route=%s", dev, gateway, strings.TrimSpace(defaultRoute))
	return nil
}

func restoreProbeLocalProxyDirect() error {
	probeLocalLinuxTakeoverState.mu.Lock()
	wasEnabled := probeLocalLinuxTakeoverState.enabled
	dev := probeLocalLinuxTakeoverState.tunDevice
	gateway := probeLocalLinuxTakeoverState.tunGateway
	probeLocalLinuxTakeoverState.enabled = false
	probeLocalLinuxTakeoverState.defaultRouteSnapshot = ""
	probeLocalLinuxTakeoverState.tunDevice = ""
	probeLocalLinuxTakeoverState.tunGateway = ""
	probeLocalLinuxTakeoverState.mu.Unlock()

	if !wasEnabled {
		return nil
	}

	var allErr error
	for _, prefix := range probeLocalLinuxTakeoverRoutePrefixes() {
		if err := deleteProbeLocalLinuxSplitRoute(prefix, dev, gateway); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	if _, err := probeLocalLinuxRunCommand(5*time.Second, "ip", "-4", "route", "show", "default"); err != nil {
		logProbeWarnf("probe local proxy restore on linux route inspect failed: %v", err)
	}
	if allErr != nil {
		return fmt.Errorf("restore linux proxy takeover failed: %w", allErr)
	}
	return nil
}

func resolveProbeLocalLinuxRouteTarget() (string, string, error) {
	dev := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_DEV"))
	if dev == "" {
		return "", "", errors.New("missing PROBE_LOCAL_TUN_DEV")
	}
	gateway := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_GATEWAY"))
	return dev, gateway, nil
}

func probeLocalLinuxTakeoverRoutePrefixes() []string {
	return []string{
		probeLocalLinuxRouteSplitPrefixA,
		probeLocalLinuxRouteSplitPrefixB,
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
	}
}

func ensureProbeLocalLinuxSplitRoute(prefix, dev, gateway string) error {
	args := []string{"-4", "route", "replace", prefix}
	if gateway != "" {
		args = append(args, "via", gateway)
	}
	args = append(args, "dev", dev, "metric", strconv.Itoa(probeLocalLinuxRouteMetric))
	_, err := probeLocalLinuxRunCommand(5*time.Second, "ip", args...)
	if err != nil {
		return err
	}
	return nil
}

func deleteProbeLocalLinuxSplitRoute(prefix, dev, gateway string) error {
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

func currentProbeLocalTUNDNSListenHost() string {
	return ""
}

func currentProbeLocalSystemDNSServers() []string {
	return nil
}

func applyProbeLocalTUNPrimaryDNS() error {
	return nil
}

func restoreProbeLocalTUNPrimaryDNS() error {
	return nil
}

func uninstallProbeLocalTUNDriver() error {
	return errProbeLocalTUNUnsupported
}
