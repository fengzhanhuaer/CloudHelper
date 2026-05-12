//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeLocalWindowsRouteSplitPrefixA = "0.0.0.0"
	probeLocalWindowsRouteSplitMaskA   = "128.0.0.0"
	probeLocalWindowsRouteSplitPrefixB = "128.0.0.0"
	probeLocalWindowsRouteSplitMaskB   = "128.0.0.0"
	probeLocalWindowsRouteMetric       = 3
)

type probeLocalWindowsRouteDef struct {
	Prefix  string
	Mask    string
	Gateway string
	IfIndex int
}

var probeLocalWindowsTakeoverState = struct {
	mu                 sync.Mutex
	enabled            bool
	routePrintOutput   string
	tunGateway         string
	tunIfIndex         int
	bypassGateway      string
	bypassInterfaceIdx int
}{}

var (
	probeLocalWindowsRunCommand = runProbeLocalCommand
)

func applyProbeLocalProxyTakeover() error {
	gateway, ifIndex, err := resolveProbeLocalWindowsRouteTarget()
	if err != nil {
		return err
	}

	probeLocalWindowsTakeoverState.mu.Lock()
	if probeLocalWindowsTakeoverState.enabled {
		probeLocalWindowsTakeoverState.mu.Unlock()
		return nil
	}
	probeLocalWindowsTakeoverState.mu.Unlock()

	bypassTarget, err := resolveProbeLocalWindowsDirectBypassRouteTarget()
	if err != nil {
		return fmt.Errorf("resolve windows local bypass route target failed: %w", err)
	}

	out, err := probeLocalWindowsRunCommand(6*time.Second, "route", "print", "-4")
	if err != nil {
		return fmt.Errorf("inspect windows route table failed: %w", err)
	}

	createdRoutes := make([]probeLocalWindowsRouteDef, 0, 5)
	for _, routeDef := range append(probeLocalWindowsTakeoverRouteDefs(gateway, ifIndex), probeLocalWindowsLocalBypassRouteDefs(bypassTarget)...) {
		created, routeErr := ensureProbeLocalWindowsRoute(routeDef)
		if routeErr != nil {
			var rollbackErr error
			for i := len(createdRoutes) - 1; i >= 0; i-- {
				if delErr := deleteProbeLocalWindowsRoute(createdRoutes[i]); delErr != nil {
					rollbackErr = errors.Join(rollbackErr, delErr)
				}
			}
			if rollbackErr != nil {
				return fmt.Errorf("apply windows takeover route %s/%s failed: %w (rollback failed: %v)", routeDef.Prefix, routeDef.Mask, routeErr, rollbackErr)
			}
			return fmt.Errorf("apply windows takeover route %s/%s failed after rollback: %w", routeDef.Prefix, routeDef.Mask, routeErr)
		}
		if created {
			createdRoutes = append(createdRoutes, routeDef)
		}
	}

	probeLocalWindowsTakeoverState.mu.Lock()
	probeLocalWindowsTakeoverState.enabled = true
	probeLocalWindowsTakeoverState.routePrintOutput = out
	probeLocalWindowsTakeoverState.tunGateway = gateway
	probeLocalWindowsTakeoverState.tunIfIndex = ifIndex
	probeLocalWindowsTakeoverState.bypassGateway = bypassTarget.NextHop
	probeLocalWindowsTakeoverState.bypassInterfaceIdx = bypassTarget.InterfaceIndex
	probeLocalWindowsTakeoverState.mu.Unlock()

	logProbeInfof("probe local proxy takeover applied on windows: gateway=%s if_index=%d route_snapshot_len=%d", gateway, ifIndex, len(strings.TrimSpace(out)))
	return nil
}

func restoreProbeLocalProxyDirect() error {
	probeLocalWindowsTakeoverState.mu.Lock()
	wasEnabled := probeLocalWindowsTakeoverState.enabled
	gateway := probeLocalWindowsTakeoverState.tunGateway
	ifIndex := probeLocalWindowsTakeoverState.tunIfIndex
	bypassGateway := probeLocalWindowsTakeoverState.bypassGateway
	bypassIfIndex := probeLocalWindowsTakeoverState.bypassInterfaceIdx
	probeLocalWindowsTakeoverState.enabled = false
	probeLocalWindowsTakeoverState.routePrintOutput = ""
	probeLocalWindowsTakeoverState.tunGateway = ""
	probeLocalWindowsTakeoverState.tunIfIndex = 0
	probeLocalWindowsTakeoverState.bypassGateway = ""
	probeLocalWindowsTakeoverState.bypassInterfaceIdx = 0
	probeLocalWindowsTakeoverState.mu.Unlock()

	if !wasEnabled {
		return nil
	}

	var allErr error
	for _, routeDef := range probeLocalWindowsTakeoverRouteDefs(gateway, ifIndex) {
		if err := deleteProbeLocalWindowsRoute(routeDef); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	if strings.TrimSpace(bypassGateway) != "" && bypassIfIndex > 0 {
		bypassTarget := probeLocalWindowsDirectBypassRouteTarget{InterfaceIndex: bypassIfIndex, NextHop: bypassGateway}
		for _, routeDef := range probeLocalWindowsLocalBypassRouteDefs(bypassTarget) {
			if err := deleteProbeLocalWindowsRoute(routeDef); err != nil {
				allErr = errors.Join(allErr, err)
			}
		}
	}
	if _, err := probeLocalWindowsRunCommand(6*time.Second, "route", "print", "-4"); err != nil {
		logProbeWarnf("probe local proxy restore on windows route inspect failed: %v", err)
	}
	if allErr != nil {
		return fmt.Errorf("restore windows proxy takeover failed: %w", allErr)
	}
	return nil
}

func resolveProbeLocalWindowsRouteTarget() (string, int, error) {
	gateway := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_GATEWAY"))
	if gateway == "" {
		return "", 0, errors.New("missing PROBE_LOCAL_TUN_GATEWAY")
	}
	rawIfIndex := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX"))
	if rawIfIndex == "" {
		return "", 0, errors.New("missing PROBE_LOCAL_TUN_IF_INDEX")
	}
	ifIndex, err := strconv.Atoi(rawIfIndex)
	if err != nil || ifIndex <= 0 {
		return "", 0, fmt.Errorf("invalid PROBE_LOCAL_TUN_IF_INDEX=%q", rawIfIndex)
	}
	return gateway, ifIndex, nil
}

func probeLocalWindowsTakeoverRouteDefs(gateway string, ifIndex int) []probeLocalWindowsRouteDef {
	return []probeLocalWindowsRouteDef{
		{Prefix: probeLocalWindowsRouteSplitPrefixA, Mask: probeLocalWindowsRouteSplitMaskA, Gateway: gateway, IfIndex: ifIndex},
		{Prefix: probeLocalWindowsRouteSplitPrefixB, Mask: probeLocalWindowsRouteSplitMaskB, Gateway: gateway, IfIndex: ifIndex},
	}
}

func probeLocalWindowsLocalBypassRouteDefs(routeTarget probeLocalWindowsDirectBypassRouteTarget) []probeLocalWindowsRouteDef {
	return []probeLocalWindowsRouteDef{
		{Prefix: "10.0.0.0", Mask: "255.0.0.0", Gateway: routeTarget.NextHop, IfIndex: routeTarget.InterfaceIndex},
		{Prefix: "172.16.0.0", Mask: "255.240.0.0", Gateway: routeTarget.NextHop, IfIndex: routeTarget.InterfaceIndex},
		{Prefix: "192.168.0.0", Mask: "255.255.0.0", Gateway: routeTarget.NextHop, IfIndex: routeTarget.InterfaceIndex},
	}
}

func ensureProbeLocalWindowsSplitRoute(prefix, mask, gateway string, ifIndex int) (bool, error) {
	return ensureProbeLocalWindowsRoute(probeLocalWindowsRouteDef{Prefix: prefix, Mask: mask, Gateway: gateway, IfIndex: ifIndex})
}

func ensureProbeLocalWindowsRoute(routeDef probeLocalWindowsRouteDef) (bool, error) {
	metric := strconv.Itoa(probeLocalWindowsRouteMetric)
	ifText := strconv.Itoa(routeDef.IfIndex)
	_, addErr := probeLocalWindowsRunCommand(6*time.Second, "route", "ADD", routeDef.Prefix, "MASK", routeDef.Mask, routeDef.Gateway, "METRIC", metric, "IF", ifText)
	if addErr == nil {
		return true, nil
	}
	if !isProbeLocalWindowsRouteExistsErr(addErr) {
		return false, addErr
	}
	_, changeErr := probeLocalWindowsRunCommand(6*time.Second, "route", "CHANGE", routeDef.Prefix, "MASK", routeDef.Mask, routeDef.Gateway, "METRIC", metric, "IF", ifText)
	if changeErr != nil {
		return false, fmt.Errorf("route exists but update failed: %w", changeErr)
	}
	return false, nil
}

func deleteProbeLocalWindowsSplitRoute(prefix, mask, gateway string, ifIndex int) error {
	return deleteProbeLocalWindowsRoute(probeLocalWindowsRouteDef{Prefix: prefix, Mask: mask, Gateway: gateway, IfIndex: ifIndex})
}

func deleteProbeLocalWindowsRoute(routeDef probeLocalWindowsRouteDef) error {
	if strings.TrimSpace(routeDef.Gateway) == "" || routeDef.IfIndex <= 0 {
		return nil
	}
	ifText := strconv.Itoa(routeDef.IfIndex)
	_, err := probeLocalWindowsRunCommand(6*time.Second, "route", "DELETE", routeDef.Prefix, "MASK", routeDef.Mask, routeDef.Gateway, "IF", ifText)
	if err != nil && !isProbeLocalWindowsRouteMissingErr(err) {
		return err
	}
	return nil
}

func isProbeLocalWindowsRouteExistsErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "object already exists") || strings.Contains(text, "对象已存在")
}

func isProbeLocalWindowsRouteMissingErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "route specified was not found") || strings.Contains(text, "找不到指定的路由")
}

func currentProbeLocalTUNDNSListenHost() string {
	probeLocalWindowsTakeoverState.mu.Lock()
	enabled := probeLocalWindowsTakeoverState.enabled
	gateway := strings.TrimSpace(probeLocalWindowsTakeoverState.tunGateway)
	probeLocalWindowsTakeoverState.mu.Unlock()
	if !enabled {
		return ""
	}
	host := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_DNS_HOST"))
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		return ip.To4().String()
	}
	if ip := net.ParseIP(gateway); ip != nil && ip.To4() != nil {
		return ip.To4().String()
	}
	return ""
}
