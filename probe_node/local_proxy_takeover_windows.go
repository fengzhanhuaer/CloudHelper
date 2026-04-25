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

var probeLocalWindowsTakeoverState = struct {
	mu               sync.Mutex
	enabled          bool
	routePrintOutput string
	tunGateway       string
	tunIfIndex       int
}{}

var (
	probeLocalWindowsEnsureWintunLibrary = ensureProbeEmbeddedWintunLibrary
	probeLocalWindowsRunCommand          = runProbeLocalCommand
)

func applyProbeLocalProxyTakeover() error {
	if err := probeLocalWindowsEnsureWintunLibrary(); err != nil {
		return fmt.Errorf("prepare wintun library: %w", err)
	}
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

	out, err := probeLocalWindowsRunCommand(6*time.Second, "route", "print", "-4")
	if err != nil {
		return fmt.Errorf("inspect windows route table failed: %w", err)
	}

	createdRoutes := make([][2]string, 0, 2)
	for _, routeDef := range [][2]string{
		{probeLocalWindowsRouteSplitPrefixA, probeLocalWindowsRouteSplitMaskA},
		{probeLocalWindowsRouteSplitPrefixB, probeLocalWindowsRouteSplitMaskB},
	} {
		created, routeErr := ensureProbeLocalWindowsSplitRoute(routeDef[0], routeDef[1], gateway, ifIndex)
		if routeErr != nil {
			var rollbackErr error
			for i := len(createdRoutes) - 1; i >= 0; i-- {
				r := createdRoutes[i]
				if delErr := deleteProbeLocalWindowsSplitRoute(r[0], r[1], gateway, ifIndex); delErr != nil {
					rollbackErr = errors.Join(rollbackErr, delErr)
				}
			}
			if rollbackErr != nil {
				return fmt.Errorf("apply windows takeover route %s/%s failed: %w (rollback failed: %v)", routeDef[0], routeDef[1], routeErr, rollbackErr)
			}
			return fmt.Errorf("apply windows takeover route %s/%s failed after rollback: %w", routeDef[0], routeDef[1], routeErr)
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
	probeLocalWindowsTakeoverState.mu.Unlock()

	logProbeInfof("probe local proxy takeover applied on windows: gateway=%s if_index=%d route_snapshot_len=%d", gateway, ifIndex, len(strings.TrimSpace(out)))
	return nil
}

func restoreProbeLocalProxyDirect() error {
	probeLocalWindowsTakeoverState.mu.Lock()
	wasEnabled := probeLocalWindowsTakeoverState.enabled
	gateway := probeLocalWindowsTakeoverState.tunGateway
	ifIndex := probeLocalWindowsTakeoverState.tunIfIndex
	probeLocalWindowsTakeoverState.enabled = false
	probeLocalWindowsTakeoverState.routePrintOutput = ""
	probeLocalWindowsTakeoverState.tunGateway = ""
	probeLocalWindowsTakeoverState.tunIfIndex = 0
	probeLocalWindowsTakeoverState.mu.Unlock()

	if !wasEnabled {
		return nil
	}

	var allErr error
	for _, routeDef := range [][2]string{
		{probeLocalWindowsRouteSplitPrefixA, probeLocalWindowsRouteSplitMaskA},
		{probeLocalWindowsRouteSplitPrefixB, probeLocalWindowsRouteSplitMaskB},
	} {
		if err := deleteProbeLocalWindowsSplitRoute(routeDef[0], routeDef[1], gateway, ifIndex); err != nil {
			allErr = errors.Join(allErr, err)
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

func ensureProbeLocalWindowsSplitRoute(prefix, mask, gateway string, ifIndex int) (bool, error) {
	metric := strconv.Itoa(probeLocalWindowsRouteMetric)
	ifText := strconv.Itoa(ifIndex)
	_, addErr := probeLocalWindowsRunCommand(6*time.Second, "route", "ADD", prefix, "MASK", mask, gateway, "METRIC", metric, "IF", ifText)
	if addErr == nil {
		return true, nil
	}
	if !isProbeLocalWindowsRouteExistsErr(addErr) {
		return false, addErr
	}
	_, changeErr := probeLocalWindowsRunCommand(6*time.Second, "route", "CHANGE", prefix, "MASK", mask, gateway, "METRIC", metric, "IF", ifText)
	if changeErr != nil {
		return false, fmt.Errorf("route exists but update failed: %w", changeErr)
	}
	return false, nil
}

func deleteProbeLocalWindowsSplitRoute(prefix, mask, gateway string, ifIndex int) error {
	if strings.TrimSpace(gateway) == "" || ifIndex <= 0 {
		return nil
	}
	ifText := strconv.Itoa(ifIndex)
	_, err := probeLocalWindowsRunCommand(6*time.Second, "route", "DELETE", prefix, "MASK", mask, gateway, "IF", ifText)
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
	defer probeLocalWindowsTakeoverState.mu.Unlock()
	if !probeLocalWindowsTakeoverState.enabled {
		return ""
	}
	host := strings.TrimSpace(probeLocalWindowsTakeoverState.tunGateway)
	if ip := net.ParseIP(host); ip == nil || ip.To4() == nil {
		return ""
	}
	return host
}
