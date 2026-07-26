//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	probeLocalLinuxStat       = os.Stat
	probeLocalLinuxLookPath   = exec.LookPath
	probeLocalLinuxRunCommand = runProbeLocalCommand
)

var probeRouteLinuxDirectBypassState = struct {
	mu     sync.Mutex
	routes map[string]probeVirtualRouterLinuxRouteDef
}{routes: make(map[string]probeVirtualRouterLinuxRouteDef)}

func ensureProbeRouteDirectBypass(targetAddr string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return err
	}
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return errors.New("empty bypass target host")
	}

	var ips []string
	if ip := net.ParseIP(cleanHost); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			ips = []string{ip4.String()}
		}
	} else {
		ips, err = lookupProbeLocalIPv4ForBypass(cleanHost)
		if err != nil {
			return err
		}
	}
	if len(ips) == 0 {
		return fmt.Errorf("bypass target has no ipv4 address: %s", cleanHost)
	}

	var allErr error
	for _, ipText := range ips {
		ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
		if ip4 == nil || probeRouteLinuxDirectBypassIPIsProtected(ip4) {
			continue
		}
		prefix := ip4.String() + "/32"
		if probeRouteLinuxDirectBypassAlreadyApplied(prefix) {
			continue
		}
		routeTarget, resolveErr := resolveProbeVirtualRouterLinuxPrimaryEgressRoute(probeRouteLinuxTUNDeviceName())
		if resolveErr != nil {
			allErr = errors.Join(allErr, resolveErr)
			continue
		}
		routeDef := probeVirtualRouterLinuxRouteDef{
			Prefix:  prefix,
			Dev:     routeTarget.Dev,
			Gateway: routeTarget.Gateway,
		}
		if routeErr := ensureProbeVirtualRouterLinuxRoute(routeDef); routeErr != nil {
			allErr = errors.Join(allErr, routeErr)
			continue
		}
		probeRouteLinuxDirectBypassState.mu.Lock()
		probeRouteLinuxDirectBypassState.routes[prefix] = routeDef
		probeRouteLinuxDirectBypassState.mu.Unlock()
	}
	return allErr
}

func probeRouteLinuxTUNDeviceName() string {
	dev, err := resolveProbeLocalLinuxTUNDeviceName()
	if err != nil {
		return probeLocalLinuxDefaultTUNDeviceName
	}
	return dev
}

func probeRouteLinuxDirectBypassAlreadyApplied(prefix string) bool {
	probeRouteLinuxDirectBypassState.mu.Lock()
	_, ok := probeRouteLinuxDirectBypassState.routes[strings.TrimSpace(prefix)]
	probeRouteLinuxDirectBypassState.mu.Unlock()
	return ok
}

func probeRouteLinuxDirectBypassIPIsProtected(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	for _, cidr := range []string{currentProbeLocalDNSFakeIPCIDR(), currentProbeVirtualRouterFakeIPCIDR()} {
		_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err == nil && network != nil && network.Contains(ip4) {
			return true
		}
	}
	return false
}

func cleanupProbeRouteDirectBypassForVirtualRouterRules(config probeVirtualRouterConfig) {
	probeRouteLinuxDirectBypassState.mu.Lock()
	routes := make([]probeVirtualRouterLinuxRouteDef, 0, len(probeRouteLinuxDirectBypassState.routes))
	for _, routeDef := range probeRouteLinuxDirectBypassState.routes {
		routes = append(routes, routeDef)
	}
	probeRouteLinuxDirectBypassState.mu.Unlock()

	for _, routeDef := range routes {
		ip, _, err := net.ParseCIDR(strings.TrimSpace(routeDef.Prefix))
		if err != nil || ip == nil || !probeVirtualRouterConfigRoutesIPViaProbeExit(config, ip) {
			continue
		}
		if err := deleteProbeVirtualRouterLinuxRoute(routeDef); err != nil {
			logProbeWarnf("probe route direct bypass cleanup failed: ip=%s gateway=%s dev=%s err=%v", routeDef.Prefix, routeDef.Gateway, routeDef.Dev, err)
			continue
		}
		probeRouteLinuxDirectBypassState.mu.Lock()
		if current, ok := probeRouteLinuxDirectBypassState.routes[routeDef.Prefix]; ok && probeVirtualRouterLinuxRouteDefEqual(current, routeDef) {
			delete(probeRouteLinuxDirectBypassState.routes, routeDef.Prefix)
		}
		probeRouteLinuxDirectBypassState.mu.Unlock()
	}
}

func probeVirtualRouterConfigRoutesIPViaProbeExit(config probeVirtualRouterConfig, ip net.IP) bool {
	target := ip.To4()
	if target == nil {
		return false
	}
	config = sanitizeProbeVirtualRouterConfigForCache(config)
	for _, rule := range config.RouteRules {
		if sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) != "probe_exit" || normalizeProbeRouteNodeID(rule.ExitNodeID) == "" {
			continue
		}
		for _, entry := range rule.Entries {
			if probeVirtualRouterRouteRuleEntryMatchesIP(target, entry) {
				return true
			}
		}
	}
	return false
}

func resetProbeRouteLinuxDirectBypassStateForTest() {
	probeRouteLinuxDirectBypassState.mu.Lock()
	probeRouteLinuxDirectBypassState.routes = make(map[string]probeVirtualRouterLinuxRouteDef)
	probeRouteLinuxDirectBypassState.mu.Unlock()
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
