//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

var probeVirtualRouterLinuxRouteState = struct {
	mu                sync.Mutex
	fakeRouteDef      probeVirtualRouterLinuxRouteDef
	fakeApplied       bool
	takeoverRouteDefs []probeVirtualRouterLinuxRouteDef
}{}

const (
	probeVirtualRouterLinuxRouteMetric       = 3
	probeVirtualRouterLinuxRouteSplitPrefixA = "0.0.0.0/1"
	probeVirtualRouterLinuxRouteSplitPrefixB = "128.0.0.0/1"
)

type probeVirtualRouterLinuxRouteTarget struct {
	Dev     string
	Gateway string
}

type probeVirtualRouterLinuxRouteDef struct {
	Prefix  string
	Dev     string
	Gateway string
	Source  string
	Metric  int
}

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
	if err := ensureProbeVirtualRouterLinuxFakeIPRoute(dev, cleanIP); err != nil {
		return err
	}
	if !probeVirtualRouterLocalEntryEnabled() {
		if err := cleanupProbeVirtualRouterLinuxTakeoverRoutes(); err != nil {
			return err
		}
	} else if err := ensureProbeVirtualRouterLinuxTakeoverRoutes(dev, cleanIP); err != nil {
		return err
	}
	if err := startProbeLocalTUNDataPlane(); err != nil {
		return err
	}
	return nil
}

func cleanupProbeVirtualRouterPlatformRoutes() error {
	probeVirtualRouterLinuxRouteState.mu.Lock()
	fakeRouteDef := probeVirtualRouterLinuxRouteState.fakeRouteDef
	fakeApplied := probeVirtualRouterLinuxRouteState.fakeApplied
	takeoverRouteDefs := append([]probeVirtualRouterLinuxRouteDef(nil), probeVirtualRouterLinuxRouteState.takeoverRouteDefs...)
	probeVirtualRouterLinuxRouteState.fakeRouteDef = probeVirtualRouterLinuxRouteDef{}
	probeVirtualRouterLinuxRouteState.fakeApplied = false
	probeVirtualRouterLinuxRouteState.takeoverRouteDefs = nil
	probeVirtualRouterLinuxRouteState.mu.Unlock()

	var allErr error
	for _, routeDef := range takeoverRouteDefs {
		if err := deleteProbeVirtualRouterLinuxRoute(routeDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	if fakeApplied {
		if err := deleteProbeVirtualRouterLinuxRoute(fakeRouteDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	return allErr
}

func ensureProbeVirtualRouterLinuxFakeIPRoute(dev string, srcIP string) error {
	routeDef := probeVirtualRouterLinuxRouteDef{
		Prefix: strings.TrimSpace(currentProbeVirtualRouterFakeIPCIDR()),
		Dev:    strings.TrimSpace(dev),
		Source: strings.TrimSpace(srcIP),
	}
	probeVirtualRouterLinuxRouteState.mu.Lock()
	oldRouteDef := probeVirtualRouterLinuxRouteState.fakeRouteDef
	needDeleteOld := probeVirtualRouterLinuxRouteState.fakeApplied && !probeVirtualRouterLinuxRouteDefEqual(oldRouteDef, routeDef)
	probeVirtualRouterLinuxRouteState.mu.Unlock()

	if needDeleteOld {
		if err := deleteProbeVirtualRouterLinuxRoute(oldRouteDef); err != nil {
			return err
		}
	}
	if err := ensureProbeVirtualRouterLinuxRoute(routeDef); err != nil {
		return err
	}
	probeVirtualRouterLinuxRouteState.mu.Lock()
	probeVirtualRouterLinuxRouteState.fakeRouteDef = routeDef
	probeVirtualRouterLinuxRouteState.fakeApplied = true
	probeVirtualRouterLinuxRouteState.mu.Unlock()
	return nil
}

func ensureProbeVirtualRouterLinuxTakeoverRoutes(dev string, srcIP string) error {
	routeDefs, err := buildProbeVirtualRouterLinuxTakeoverRouteDefs(dev, srcIP)
	if err != nil {
		return err
	}
	probeVirtualRouterLinuxRouteState.mu.Lock()
	oldRouteDefs := append([]probeVirtualRouterLinuxRouteDef(nil), probeVirtualRouterLinuxRouteState.takeoverRouteDefs...)
	if probeVirtualRouterLinuxRouteDefsEqual(oldRouteDefs, routeDefs) {
		probeVirtualRouterLinuxRouteState.mu.Unlock()
		return nil
	}
	probeVirtualRouterLinuxRouteState.mu.Unlock()

	var allErr error
	for _, oldRouteDef := range oldRouteDefs {
		if err := deleteProbeVirtualRouterLinuxRoute(oldRouteDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	for _, routeDef := range routeDefs {
		if err := ensureProbeVirtualRouterLinuxRoute(routeDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	if allErr != nil {
		return allErr
	}
	probeVirtualRouterLinuxRouteState.mu.Lock()
	probeVirtualRouterLinuxRouteState.takeoverRouteDefs = append([]probeVirtualRouterLinuxRouteDef(nil), routeDefs...)
	probeVirtualRouterLinuxRouteState.mu.Unlock()
	return nil
}

func buildProbeVirtualRouterLinuxTakeoverRouteDefs(dev string, srcIP string) ([]probeVirtualRouterLinuxRouteDef, error) {
	tunDev := strings.TrimSpace(dev)
	routeDefs := []probeVirtualRouterLinuxRouteDef{
		{Prefix: probeVirtualRouterLinuxRouteSplitPrefixA, Dev: tunDev, Source: strings.TrimSpace(srcIP), Metric: probeVirtualRouterLinuxRouteMetric},
		{Prefix: probeVirtualRouterLinuxRouteSplitPrefixB, Dev: tunDev, Source: strings.TrimSpace(srcIP), Metric: probeVirtualRouterLinuxRouteMetric},
	}
	bypassTarget, err := resolveProbeVirtualRouterLinuxPrimaryEgressRoute(tunDev)
	if err != nil {
		return nil, err
	}
	routeDefs = append(routeDefs, probeVirtualRouterLinuxLocalBypassRouteDefs(bypassTarget)...)
	return routeDefs, nil
}

func cleanupProbeVirtualRouterLinuxTakeoverRoutes() error {
	probeVirtualRouterLinuxRouteState.mu.Lock()
	routeDefs := append([]probeVirtualRouterLinuxRouteDef(nil), probeVirtualRouterLinuxRouteState.takeoverRouteDefs...)
	probeVirtualRouterLinuxRouteState.takeoverRouteDefs = nil
	probeVirtualRouterLinuxRouteState.mu.Unlock()
	var allErr error
	for _, routeDef := range routeDefs {
		if err := deleteProbeVirtualRouterLinuxRoute(routeDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	return allErr
}

func resolveProbeVirtualRouterLinuxPrimaryEgressRoute(excludedDev string) (probeVirtualRouterLinuxRouteTarget, error) {
	output, err := probeLocalLinuxRunCommand(5*time.Second, "ip", "-4", "route", "show", "default")
	if err != nil {
		return probeVirtualRouterLinuxRouteTarget{}, fmt.Errorf("resolve linux virtual router default route failed: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		target, ok := parseProbeVirtualRouterLinuxDefaultRouteLine(line)
		if !ok {
			continue
		}
		if strings.TrimSpace(excludedDev) != "" && target.Dev == strings.TrimSpace(excludedDev) {
			continue
		}
		return target, nil
	}
	return probeVirtualRouterLinuxRouteTarget{}, errors.New("usable linux virtual router ipv4 default route not found")
}

func parseProbeVirtualRouterLinuxDefaultRouteLine(line string) (probeVirtualRouterLinuxRouteTarget, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 || fields[0] != "default" {
		return probeVirtualRouterLinuxRouteTarget{}, false
	}
	var target probeVirtualRouterLinuxRouteTarget
	for i := 1; i < len(fields); i++ {
		switch fields[i] {
		case "via":
			if i+1 < len(fields) {
				target.Gateway = strings.TrimSpace(fields[i+1])
				i++
			}
		case "dev":
			if i+1 < len(fields) {
				target.Dev = strings.TrimSpace(fields[i+1])
				i++
			}
		}
	}
	if target.Dev == "" {
		return probeVirtualRouterLinuxRouteTarget{}, false
	}
	return target, true
}

func probeVirtualRouterLinuxLocalBypassRouteDefs(routeTarget probeVirtualRouterLinuxRouteTarget) []probeVirtualRouterLinuxRouteDef {
	return []probeVirtualRouterLinuxRouteDef{
		{Prefix: "10.0.0.0/8", Dev: routeTarget.Dev, Gateway: routeTarget.Gateway, Metric: probeVirtualRouterLinuxRouteMetric},
		{Prefix: "172.16.0.0/12", Dev: routeTarget.Dev, Gateway: routeTarget.Gateway, Metric: probeVirtualRouterLinuxRouteMetric},
		{Prefix: "192.168.0.0/16", Dev: routeTarget.Dev, Gateway: routeTarget.Gateway, Metric: probeVirtualRouterLinuxRouteMetric},
	}
}

func ensureProbeVirtualRouterLinuxRoute(routeDef probeVirtualRouterLinuxRouteDef) error {
	if strings.TrimSpace(routeDef.Prefix) == "" || strings.TrimSpace(routeDef.Dev) == "" {
		return nil
	}
	args := []string{"-4", "route", "replace", strings.TrimSpace(routeDef.Prefix)}
	if gateway := strings.TrimSpace(routeDef.Gateway); gateway != "" {
		args = append(args, "via", gateway)
	}
	args = append(args, "dev", strings.TrimSpace(routeDef.Dev))
	if source := strings.TrimSpace(routeDef.Source); source != "" {
		args = append(args, "src", source)
	}
	if routeDef.Metric > 0 {
		args = append(args, "metric", fmt.Sprintf("%d", routeDef.Metric))
	}
	_, err := probeLocalLinuxRunCommand(5*time.Second, "ip", args...)
	if err != nil {
		return fmt.Errorf("replace linux virtual router route failed: prefix=%s dev=%s gateway=%s: %w", routeDef.Prefix, routeDef.Dev, routeDef.Gateway, err)
	}
	return nil
}

func deleteProbeVirtualRouterLinuxRoute(routeDef probeVirtualRouterLinuxRouteDef) error {
	return deleteProbeLocalLinuxSplitRoute(strings.TrimSpace(routeDef.Prefix), strings.TrimSpace(routeDef.Dev), strings.TrimSpace(routeDef.Gateway))
}

func probeVirtualRouterLinuxRouteDefsEqual(a, b []probeVirtualRouterLinuxRouteDef) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if !probeVirtualRouterLinuxRouteDefEqual(a[index], b[index]) {
			return false
		}
	}
	return true
}

func probeVirtualRouterLinuxRouteDefEqual(a, b probeVirtualRouterLinuxRouteDef) bool {
	return strings.TrimSpace(a.Prefix) == strings.TrimSpace(b.Prefix) &&
		strings.TrimSpace(a.Dev) == strings.TrimSpace(b.Dev) &&
		strings.TrimSpace(a.Gateway) == strings.TrimSpace(b.Gateway) &&
		strings.TrimSpace(a.Source) == strings.TrimSpace(b.Source) &&
		a.Metric == b.Metric
}
