//go:build windows

package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const (
	tunRouteIPv4Address      = "198.18.0.1"
	tunRouteIPv4PrefixLength = 15
	tunRouteSplitPrefixA     = "0.0.0.0/1"
	tunRouteSplitPrefixB     = "128.0.0.0/1"
	tunRouteDNSPrimary       = "1.1.1.1"
	tunRouteDNSSecondary     = "8.8.8.8"
)

type windowsRouteInfo struct {
	InterfaceIndex int    `json:"InterfaceIndex"`
	NextHop        string `json:"NextHop"`
}

type windowsAdapterInfo struct {
	InterfaceIndex       int    `json:"InterfaceIndex"`
	Name                 string `json:"Name"`
	InterfaceDescription string `json:"InterfaceDescription"`
}

func (s *networkAssistantService) applyPlatformTUNSystemRouting(targets tunControlPlaneTargets) error {
	adapter, err := ensureWindowsTUNAdapterIPv4Routing()
	if err != nil {
		return err
	}

	state := tunSystemRouteState{
		AdapterIndex: adapter.InterfaceIndex,
	}

	if targets.ControllerHost != "" && len(targets.IPv4Addrs) == 0 {
		return fmt.Errorf("resolve controller ipv4 failed: %s", targets.ControllerHost)
	}
	if len(targets.IPv4Addrs) > 0 {
		egress, routeErr := detectWindowsPrimaryIPv4Route()
		if routeErr != nil {
			return routeErr
		}
		state.BypassInterfaceIndex = egress.InterfaceIndex
		state.BypassNextHop = strings.TrimSpace(egress.NextHop)
		state.BypassRoutePrefixes = make([]string, 0, len(targets.IPv4Addrs))
		for _, ipValue := range targets.IPv4Addrs {
			prefix := strings.TrimSpace(ipValue) + "/32"
			if err := ensureWindowsIPv4BypassRoute(prefix, egress.InterfaceIndex, egress.NextHop); err != nil {
				return err
			}
			state.BypassRoutePrefixes = append(state.BypassRoutePrefixes, prefix)
		}
	}

	s.mu.Lock()
	s.tunRouteState = state
	s.mu.Unlock()

	if len(state.BypassRoutePrefixes) > 0 {
		s.logf("tun system routing applied: adapter_if=%d bypass_if=%d next_hop=%s routes=%s", state.AdapterIndex, state.BypassInterfaceIndex, state.BypassNextHop, strings.Join(state.BypassRoutePrefixes, ","))
	} else {
		s.logf("tun system routing applied: adapter_if=%d", state.AdapterIndex)
	}
	return nil
}

func (s *networkAssistantService) clearPlatformTUNSystemRouting() error {
	s.mu.RLock()
	state := s.tunRouteState
	s.mu.RUnlock()

	var allErr error
	if state.BypassInterfaceIndex > 0 && strings.TrimSpace(state.BypassNextHop) != "" {
		for _, prefix := range state.BypassRoutePrefixes {
			if err := removeWindowsIPv4BypassRoute(prefix, state.BypassInterfaceIndex, state.BypassNextHop); err != nil {
				allErr = errors.Join(allErr, err)
			}
		}
	}
	if err := clearWindowsTUNAdapterIPv4Routing(state.AdapterIndex); err != nil {
		allErr = errors.Join(allErr, err)
	}

	s.mu.Lock()
	s.tunRouteState = tunSystemRouteState{}
	s.mu.Unlock()

	return allErr
}

func detectWindowsPrimaryIPv4Route() (windowsRouteInfo, error) {
	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; " +
		"$ErrorActionPreference='Stop'; " +
		"$route = Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | " +
		"Where-Object { $_.NextHop -and $_.NextHop -ne '0.0.0.0' } | " +
		"Sort-Object -Property @{Expression={($_.RouteMetric + $_.InterfaceMetric)}; Ascending=$true}, @{Expression={$_.RouteMetric}; Ascending=$true} | " +
		"Select-Object -First 1; " +
		"if ($null -eq $route) { throw 'usable ipv4 default route not found' }; " +
		"[pscustomobject]@{ InterfaceIndex = [int]$route.InterfaceIndex; NextHop = [string]$route.NextHop } | ConvertTo-Json -Compress"

	output, err := runHiddenPowerShell(script)
	if err != nil {
		return windowsRouteInfo{}, err
	}
	var route windowsRouteInfo
	if err := json.Unmarshal(output, &route); err != nil {
		return windowsRouteInfo{}, fmt.Errorf("parse default route output failed: %w", err)
	}
	if route.InterfaceIndex <= 0 || strings.TrimSpace(route.NextHop) == "" {
		return windowsRouteInfo{}, errors.New("invalid ipv4 default route")
	}
	return route, nil
}

func ensureWindowsTUNAdapterIPv4Routing() (windowsAdapterInfo, error) {
	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; " +
		"$ErrorActionPreference='Stop'; " +
		"$adapter = Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue | " +
		"Where-Object { $_.Name -eq " + quotePowerShellSingle(tunAdapterName) + " -or $_.InterfaceDescription -eq " + quotePowerShellSingle(tunAdapterDescription) + " } | " +
		"Select-Object -First 1; " +
		"if ($null -eq $adapter) { throw 'tun adapter not found: " + escapePowerShellLiteral(tunAdapterName) + "' }; " +
		"$ifIndex = [int]$adapter.ifIndex; " +
		"$existing = Get-NetIPAddress -InterfaceIndex $ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue | " +
		"Where-Object { $_.IPAddress -eq " + quotePowerShellSingle(tunRouteIPv4Address) + " -and $_.PrefixLength -eq " + strconv.Itoa(tunRouteIPv4PrefixLength) + " }; " +
		"if ($null -eq $existing) { " +
		"  $oldIPs = Get-NetIPAddress -InterfaceIndex $ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -ne " + quotePowerShellSingle("127.0.0.1") + " }; " +
		"  if ($oldIPs) { $oldIPs | Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue | Out-Null }; " +
		"  New-NetIPAddress -AddressFamily IPv4 -InterfaceIndex $ifIndex -IPAddress " + quotePowerShellSingle(tunRouteIPv4Address) + " -PrefixLength " + strconv.Itoa(tunRouteIPv4PrefixLength) + " -Type Unicast -SkipAsSource $false -ErrorAction Stop | Out-Null; " +
		"}; " +
		"Set-DnsClientServerAddress -InterfaceIndex $ifIndex -ServerAddresses @(" + quotePowerShellSingle(tunRouteDNSPrimary) + "," + quotePowerShellSingle(tunRouteDNSSecondary) + ") -ErrorAction SilentlyContinue | Out-Null; " +
		"Get-NetRoute -AddressFamily IPv4 -InterfaceIndex $ifIndex -DestinationPrefix " + quotePowerShellSingle(tunRouteSplitPrefixA) + " -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue | Out-Null; " +
		"Get-NetRoute -AddressFamily IPv4 -InterfaceIndex $ifIndex -DestinationPrefix " + quotePowerShellSingle(tunRouteSplitPrefixB) + " -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue | Out-Null; " +
		"New-NetRoute -AddressFamily IPv4 -InterfaceIndex $ifIndex -DestinationPrefix " + quotePowerShellSingle(tunRouteSplitPrefixA) + " -NextHop '0.0.0.0' -RouteMetric 6 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null; " +
		"New-NetRoute -AddressFamily IPv4 -InterfaceIndex $ifIndex -DestinationPrefix " + quotePowerShellSingle(tunRouteSplitPrefixB) + " -NextHop '0.0.0.0' -RouteMetric 6 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null; " +
		"[pscustomobject]@{ InterfaceIndex=[int]$ifIndex; Name=[string]$adapter.Name; InterfaceDescription=[string]$adapter.InterfaceDescription } | ConvertTo-Json -Compress"

	output, err := runHiddenPowerShell(script)
	if err != nil {
		return windowsAdapterInfo{}, err
	}
	var adapter windowsAdapterInfo
	if err := json.Unmarshal(output, &adapter); err != nil {
		return windowsAdapterInfo{}, fmt.Errorf("parse tun adapter output failed: %w", err)
	}
	if adapter.InterfaceIndex <= 0 {
		return windowsAdapterInfo{}, errors.New("invalid tun adapter interface index")
	}
	return adapter, nil
}

func ensureWindowsIPv4BypassRoute(prefix string, interfaceIndex int, nextHop string) error {
	cleanPrefix := strings.TrimSpace(prefix)
	cleanHop := strings.TrimSpace(nextHop)
	if cleanPrefix == "" {
		return errors.New("empty bypass route prefix")
	}
	if interfaceIndex <= 0 {
		return errors.New("invalid bypass route interface")
	}
	if cleanHop == "" {
		return errors.New("empty bypass route next hop")
	}
	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; " +
		"$ErrorActionPreference='Stop'; " +
		"Get-NetRoute -AddressFamily IPv4 -DestinationPrefix " + quotePowerShellSingle(cleanPrefix) + " -InterfaceIndex " + strconv.Itoa(interfaceIndex) + " -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue | Out-Null; " +
		"New-NetRoute -AddressFamily IPv4 -DestinationPrefix " + quotePowerShellSingle(cleanPrefix) + " -InterfaceIndex " + strconv.Itoa(interfaceIndex) + " -NextHop " + quotePowerShellSingle(cleanHop) + " -RouteMetric 1 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null"
	_, err := runHiddenPowerShell(script)
	if err != nil {
		return fmt.Errorf("apply bypass route failed (%s via %s if %d): %w", cleanPrefix, cleanHop, interfaceIndex, err)
	}
	return nil
}

func removeWindowsIPv4BypassRoute(prefix string, interfaceIndex int, nextHop string) error {
	cleanPrefix := strings.TrimSpace(prefix)
	cleanHop := strings.TrimSpace(nextHop)
	if cleanPrefix == "" || interfaceIndex <= 0 || cleanHop == "" {
		return nil
	}
	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; " +
		"$ErrorActionPreference='Continue'; " +
		"Get-NetRoute -AddressFamily IPv4 -DestinationPrefix " + quotePowerShellSingle(cleanPrefix) + " -InterfaceIndex " + strconv.Itoa(interfaceIndex) + " -NextHop " + quotePowerShellSingle(cleanHop) + " -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue | Out-Null"
	_, err := runHiddenPowerShell(script)
	if err != nil {
		return fmt.Errorf("remove bypass route failed (%s via %s if %d): %w", cleanPrefix, cleanHop, interfaceIndex, err)
	}
	return nil
}

func clearWindowsTUNAdapterIPv4Routing(interfaceIndex int) error {
	indexText := strconv.Itoa(interfaceIndex)
	if interfaceIndex <= 0 {
		indexText = "0"
	}
	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; " +
		"$ErrorActionPreference='Stop'; " +
		"$ifIndex = " + indexText + "; " +
		"if ($ifIndex -le 0) { " +
		"  $adapter = Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue | Where-Object { $_.Name -eq " + quotePowerShellSingle(tunAdapterName) + " -or $_.InterfaceDescription -eq " + quotePowerShellSingle(tunAdapterDescription) + " } | Select-Object -First 1; " +
		"  if ($adapter) { $ifIndex = [int]$adapter.ifIndex } " +
		"}; " +
		"if ($ifIndex -le 0) { return }; " +
		"Get-NetRoute -AddressFamily IPv4 -InterfaceIndex $ifIndex -DestinationPrefix " + quotePowerShellSingle(tunRouteSplitPrefixA) + " -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue | Out-Null; " +
		"Get-NetRoute -AddressFamily IPv4 -InterfaceIndex $ifIndex -DestinationPrefix " + quotePowerShellSingle(tunRouteSplitPrefixB) + " -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue | Out-Null; " +
		"Set-DnsClientServerAddress -InterfaceIndex $ifIndex -ResetServerAddresses -ErrorAction SilentlyContinue | Out-Null"
	_, err := runHiddenPowerShell(script)
	if err != nil {
		return fmt.Errorf("clear tun adapter routing failed: %w", err)
	}
	return nil
}

func runHiddenPowerShell(script string) ([]byte, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, message)
	}
	return output, nil
}

func quotePowerShellSingle(value string) string {
	return "'" + escapePowerShellLiteral(value) + "'"
}

func escapePowerShellLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
