//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	probeLocalLinuxStat       = os.Stat
	probeLocalLinuxLookPath   = exec.LookPath
	probeLocalLinuxRunCommand = runProbeLocalCommand
)

const probeRouteLinuxDirectRouteMetric = 4273

type probeRouteLinuxMainRoute struct {
	Prefix   *net.IPNet
	Dev      string
	Gateway  string
	Protocol string
	Metric   uint32
}

var probeRouteLinuxDirectBypassState = struct {
	mu                 sync.Mutex
	routes             map[string]probeVirtualRouterLinuxRouteDef
	transportProtected map[string]struct{}
}{
	routes:             make(map[string]probeVirtualRouterLinuxRouteDef),
	transportProtected: make(map[string]struct{}),
}

func ensureProbeRouteDirectBypass(targetAddr string) error {
	return ensureProbeRouteDirectBypassWithPurpose(targetAddr, false)
}

func ensureProbeRouteTransportDirectBypass(targetAddr string) error {
	return ensureProbeRouteDirectBypassWithPurpose(targetAddr, true)
}

func ensureProbeRouteDirectBypassWithPurpose(targetAddr string, protectTransport bool) error {
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
	if protectTransport {
		probeRouteLinuxDirectBypassState.mu.Lock()
		for _, ipText := range ips {
			if ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4(); ip4 != nil {
				probeRouteLinuxDirectBypassState.transportProtected[ip4.String()] = struct{}{}
			}
		}
		probeRouteLinuxDirectBypassState.mu.Unlock()
	}

	var allErr error
	for _, ipText := range ips {
		ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
		if ip4 == nil || probeRouteLinuxDirectBypassIPIsProtected(ip4) {
			continue
		}
		prefix := ip4.String() + "/32"
		routeTarget, resolveErr := resolveProbeVirtualRouterLinuxPrimaryEgressRoute(probeRouteLinuxTUNDeviceName())
		if resolveErr != nil {
			allErr = errors.Join(allErr, resolveErr)
			continue
		}
		covered, existing, staleRoutes, inspectErr := inspectProbeRouteLinuxDirectBypass(ip4, probeRouteLinuxTUNDeviceName(), routeTarget)
		if inspectErr != nil {
			allErr = errors.Join(allErr, inspectErr)
			continue
		}
		if covered {
			cleaned := true
			for _, staleRoute := range staleRoutes {
				if deleteErr := deleteProbeVirtualRouterLinuxRoute(staleRoute); deleteErr != nil {
					allErr = errors.Join(allErr, deleteErr)
					cleaned = false
					continue
				}
			}
			if cleaned {
				forgetProbeRouteLinuxDirectBypass(ip4.String(), prefix)
			}
			continue
		}
		if existing || probeRouteLinuxDirectBypassAlreadyApplied(prefix) {
			continue
		}
		routeDef := probeVirtualRouterLinuxRouteDef{
			Prefix:  prefix,
			Dev:     routeTarget.Dev,
			Gateway: routeTarget.Gateway,
			Metric:  probeRouteLinuxDirectRouteMetric,
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

func inspectProbeRouteLinuxDirectBypass(ip net.IP, tunDev string, routeTarget probeVirtualRouterLinuxRouteTarget) (bool, bool, []probeVirtualRouterLinuxRouteDef, error) {
	routes, err := listProbeRouteLinuxMainRoutes()
	if err != nil {
		return false, false, nil, err
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false, false, nil, nil
	}
	targetDev := strings.TrimSpace(routeTarget.Dev)
	targetGateway := strings.TrimSpace(routeTarget.Gateway)
	cleanTUNDev := strings.TrimSpace(tunDev)
	var best *probeRouteLinuxMainRoute
	var staleRoutes []probeVirtualRouterLinuxRouteDef
	existing := false
	for index := range routes {
		route := &routes[index]
		if route.Prefix == nil || !route.Prefix.Contains(ip4) {
			continue
		}
		bits, _ := route.Prefix.Mask.Size()
		if bits == 32 && route.Dev == targetDev && route.Gateway == targetGateway {
			existing = true
			staleRoutes = append(staleRoutes, probeVirtualRouterLinuxRouteDef{
				Prefix:  ip4.String() + "/32",
				Dev:     route.Dev,
				Gateway: route.Gateway,
				Metric:  int(route.Metric),
			})
			continue
		}
		if bits <= 1 || route.Dev == "" {
			continue
		}
		if best == nil || probeRouteLinuxMainRoutePreferred(*route, *best) {
			best = route
		}
	}
	if best == nil || best.Dev == cleanTUNDev {
		return false, existing, nil, nil
	}
	return true, existing, staleRoutes, nil
}

func probeRouteLinuxMainRoutePreferred(candidate, current probeRouteLinuxMainRoute) bool {
	candidateBits, _ := candidate.Prefix.Mask.Size()
	currentBits, _ := current.Prefix.Mask.Size()
	if candidateBits != currentBits {
		return candidateBits > currentBits
	}
	return candidate.Metric < current.Metric
}

func listProbeRouteLinuxMainRoutes() ([]probeRouteLinuxMainRoute, error) {
	output, err := probeLocalLinuxRunCommand(5*time.Second, "ip", "-4", "route", "show", "table", "main")
	if err != nil {
		return nil, fmt.Errorf("list linux main routes: %w", err)
	}
	var routes []probeRouteLinuxMainRoute
	for _, line := range strings.Split(output, "\n") {
		if route, ok := parseProbeRouteLinuxMainRoute(line); ok {
			routes = append(routes, route)
		}
	}
	return routes, nil
}

func parseProbeRouteLinuxMainRoute(line string) (probeRouteLinuxMainRoute, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 || fields[0] == "default" {
		return probeRouteLinuxMainRoute{}, false
	}
	prefixText := fields[0]
	if !strings.Contains(prefixText, "/") {
		prefixText += "/32"
	}
	_, prefix, err := net.ParseCIDR(prefixText)
	if err != nil || prefix == nil {
		return probeRouteLinuxMainRoute{}, false
	}
	route := probeRouteLinuxMainRoute{Prefix: prefix}
	for index := 1; index+1 < len(fields); index++ {
		switch fields[index] {
		case "dev":
			route.Dev = strings.TrimSpace(fields[index+1])
		case "via":
			route.Gateway = strings.TrimSpace(fields[index+1])
		case "proto":
			route.Protocol = strings.TrimSpace(fields[index+1])
		case "metric":
			if metric, parseErr := strconv.ParseUint(fields[index+1], 10, 32); parseErr == nil {
				route.Metric = uint32(metric)
			}
		}
	}
	return route, true
}

func cleanupProbeRouteLinuxRedundantPhysicalHostRoutes(dev, gateway string, cidrs []string) error {
	routes, err := listProbeRouteLinuxMainRoutes()
	if err != nil {
		return err
	}
	cleanDev := strings.TrimSpace(dev)
	cleanGateway := strings.TrimSpace(gateway)
	var networks []*net.IPNet
	for _, cidr := range cidrs {
		_, network, parseErr := net.ParseCIDR(strings.TrimSpace(cidr))
		if parseErr == nil && network != nil {
			networks = append(networks, network)
		}
	}
	var allErr error
	for _, route := range routes {
		if route.Prefix == nil || route.Dev != cleanDev || route.Gateway != cleanGateway {
			continue
		}
		bits, _ := route.Prefix.Mask.Size()
		if bits != 32 || (route.Protocol != "" && route.Protocol != "boot") || (route.Metric != 0 && route.Metric != probeRouteLinuxDirectRouteMetric) {
			continue
		}
		ip4 := route.Prefix.IP.To4()
		if ip4 == nil || !probeRouteLinuxIPInNetworks(ip4, networks) {
			continue
		}
		routeDef := probeVirtualRouterLinuxRouteDef{
			Prefix:  ip4.String() + "/32",
			Dev:     route.Dev,
			Gateway: route.Gateway,
			Metric:  int(route.Metric),
		}
		if deleteErr := deleteProbeVirtualRouterLinuxRoute(routeDef); deleteErr != nil {
			allErr = errors.Join(allErr, deleteErr)
			continue
		}
		forgetProbeRouteLinuxDirectBypass(ip4.String(), routeDef.Prefix)
		logProbeInfof("probe route direct bypass removed redundant physical host route: ip=%s gateway=%s dev=%s", ip4.String(), route.Gateway, route.Dev)
	}
	return allErr
}

func probeRouteLinuxIPInNetworks(ip net.IP, networks []*net.IPNet) bool {
	for _, network := range networks {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func forgetProbeRouteLinuxDirectBypass(ip, prefix string) {
	probeRouteLinuxDirectBypassState.mu.Lock()
	delete(probeRouteLinuxDirectBypassState.routes, strings.TrimSpace(prefix))
	delete(probeRouteLinuxDirectBypassState.transportProtected, strings.TrimSpace(ip))
	probeRouteLinuxDirectBypassState.mu.Unlock()
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
		if err != nil || ip == nil || probeVirtualRouterDNSDirectPriorityIP(ip) || probeRouteLinuxTransportBypassIsProtected(ip) || !probeVirtualRouterConfigRoutesIPViaProbeExit(config, ip) {
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

func probeRouteLinuxTransportBypassIsProtected(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	probeRouteLinuxDirectBypassState.mu.Lock()
	_, ok := probeRouteLinuxDirectBypassState.transportProtected[ip4.String()]
	probeRouteLinuxDirectBypassState.mu.Unlock()
	return ok
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
	probeRouteLinuxDirectBypassState.transportProtected = make(map[string]struct{})
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
