//go:build windows

package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	tunRouteIPv4Address      = "198.18.0.1"
	tunRouteIPv4PrefixLength = 15
	tunRouteSplitPrefixA     = "0.0.0.0/1"
	tunRouteSplitPrefixB     = "128.0.0.0/1"
)

type windowsRouteInfo struct {
	InterfaceIndex int    `json:"InterfaceIndex"`
	NextHop        string `json:"NextHop"`
}

type windowsAdapterInfo struct {
	InterfaceIndex       int      `json:"InterfaceIndex"`
	Name                 string   `json:"Name"`
	InterfaceDescription string   `json:"InterfaceDescription"`
	PreviousDNSServers   []string `json:"PreviousDNSServers"`
}

func (s *networkAssistantService) applyPlatformTUNSystemRouting(targets tunControlPlaneTargets) error {
	s.mu.RLock()
	prevState := s.tunRouteState
	s.mu.RUnlock()
	if prevState.BypassInterfaceIndex > 0 && strings.TrimSpace(prevState.BypassNextHop) != "" {
		for _, prefix := range prevState.BypassRoutePrefixes {
			_ = removeWindowsIPv4BypassRoute(prefix, prevState.BypassInterfaceIndex, prevState.BypassNextHop)
		}
	}

	adapter, err := ensureWindowsTUNAdapterIPv4Routing()
	if err != nil {
		return err
	}

	state := tunSystemRouteState{
		AdapterIndex:      adapter.InterfaceIndex,
		AdapterDNSServers: append([]string(nil), adapter.PreviousDNSServers...),
	}

	if targets.ControllerHost != "" && len(targets.IPv4Addrs) == 0 {
		return fmt.Errorf("resolve controller ipv4 failed: %s", targets.ControllerHost)
	}
	egress, routeErr := detectWindowsPrimaryIPv4Route()
	if routeErr != nil {
		return routeErr
	}
	state.BypassInterfaceIndex = egress.InterfaceIndex
	state.BypassNextHop = strings.TrimSpace(egress.NextHop)

	directDNSServers, dnsErr := detectWindowsInterfaceIPv4DNSServers(egress.InterfaceIndex)
	if dnsErr == nil {
		state.DirectDNSServers = append([]string(nil), directDNSServers...)
	}

	prefixSet := make(map[string]struct{})
	state.BypassRoutePrefixes = make([]string, 0, len(targets.IPv4Addrs)+len(state.DirectDNSServers))
	addBypassRoute := func(ipValue string) error {
		ipText := strings.TrimSpace(ipValue)
		if net.ParseIP(ipText) == nil {
			return nil
		}
		prefix := ipText + "/32"
		if _, exists := prefixSet[prefix]; exists {
			return nil
		}
		if err := ensureWindowsIPv4BypassRoute(prefix, egress.InterfaceIndex, egress.NextHop); err != nil {
			return err
		}
		prefixSet[prefix] = struct{}{}
		state.BypassRoutePrefixes = append(state.BypassRoutePrefixes, prefix)
		return nil
	}
	for _, ipValue := range targets.IPv4Addrs {
		if err := addBypassRoute(ipValue); err != nil {
			return err
		}
	}
	for _, dnsServer := range state.DirectDNSServers {
		if err := addBypassRoute(dnsServer); err != nil {
			return err
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
	dynamicBypass := make([]string, 0, len(s.tunDynamicBypass))
	for prefix := range s.tunDynamicBypass {
		dynamicBypass = append(dynamicBypass, prefix)
	}
	s.mu.RUnlock()

	var allErr error
	if state.BypassInterfaceIndex > 0 && strings.TrimSpace(state.BypassNextHop) != "" {
		for _, prefix := range dynamicBypass {
			if err := removeWindowsIPv4BypassRoute(prefix, state.BypassInterfaceIndex, state.BypassNextHop); err != nil {
				allErr = errors.Join(allErr, err)
			}
		}
		for _, prefix := range state.BypassRoutePrefixes {
			if err := removeWindowsIPv4BypassRoute(prefix, state.BypassInterfaceIndex, state.BypassNextHop); err != nil {
				allErr = errors.Join(allErr, err)
			}
		}
	}
	if err := clearWindowsTUNAdapterIPv4Routing(state.AdapterIndex, state.AdapterDNSServers); err != nil {
		allErr = errors.Join(allErr, err)
	}

	s.mu.Lock()
	s.tunRouteState = tunSystemRouteState{}
	s.tunDynamicBypass = make(map[string]int)
	s.mu.Unlock()

	return allErr
}

func (s *networkAssistantService) acquireTUNDirectBypassRoute(targetAddr string) (func(), error) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return func() {}, nil
	}
	ipv4 := net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]"))).To4()
	if ipv4 == nil {
		return func() {}, nil
	}
	prefix := ipv4.String() + "/32"

	s.mu.Lock()
	state := s.tunRouteState
	if state.BypassInterfaceIndex <= 0 || strings.TrimSpace(state.BypassNextHop) == "" {
		s.mu.Unlock()
		return func() {}, nil
	}
	for _, existing := range state.BypassRoutePrefixes {
		if strings.EqualFold(strings.TrimSpace(existing), prefix) {
			s.mu.Unlock()
			return func() {}, nil
		}
	}
	if s.tunDynamicBypass == nil {
		s.tunDynamicBypass = make(map[string]int)
	}
	refs := s.tunDynamicBypass[prefix]
	s.tunDynamicBypass[prefix] = refs + 1
	needCreate := refs == 0
	ifIndex := state.BypassInterfaceIndex
	nextHop := state.BypassNextHop
	s.mu.Unlock()

	if needCreate {
		if err := ensureWindowsIPv4BypassRoute(prefix, ifIndex, nextHop); err != nil {
			s.mu.Lock()
			if current := s.tunDynamicBypass[prefix]; current <= 1 {
				delete(s.tunDynamicBypass, prefix)
			} else {
				s.tunDynamicBypass[prefix] = current - 1
			}
			s.mu.Unlock()
			return nil, err
		}
	}

	released := false
	return func() {
		if released {
			return
		}
		released = true
		s.releaseTUNDirectBypassRoute(prefix)
	}, nil
}

func (s *networkAssistantService) releaseTUNDirectBypassRoute(prefix string) {
	cleanPrefix := strings.TrimSpace(prefix)
	if cleanPrefix == "" {
		return
	}

	s.mu.Lock()
	state := s.tunRouteState
	refs := 0
	if s.tunDynamicBypass != nil {
		refs = s.tunDynamicBypass[cleanPrefix]
	}
	if refs <= 1 {
		if s.tunDynamicBypass != nil {
			delete(s.tunDynamicBypass, cleanPrefix)
		}
		refs = 0
	} else {
		s.tunDynamicBypass[cleanPrefix] = refs - 1
		refs = refs - 1
	}
	ifIndex := state.BypassInterfaceIndex
	nextHop := state.BypassNextHop
	s.mu.Unlock()

	if refs == 0 && ifIndex > 0 && strings.TrimSpace(nextHop) != "" {
		_ = removeWindowsIPv4BypassRoute(cleanPrefix, ifIndex, nextHop)
	}
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

func detectWindowsInterfaceIPv4DNSServers(interfaceIndex int) ([]string, error) {
	if interfaceIndex <= 0 {
		return nil, errors.New("invalid interface index")
	}
	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; " +
		"$ErrorActionPreference='Stop'; " +
		"$entry = Get-DnsClientServerAddress -InterfaceIndex " + strconv.Itoa(interfaceIndex) + " -AddressFamily IPv4 -ErrorAction SilentlyContinue | Select-Object -First 1; " +
		"if ($null -eq $entry -or $null -eq $entry.ServerAddresses) { '[]' } else { $entry.ServerAddresses | ConvertTo-Json -Compress }"

	output, err := runHiddenPowerShell(script)
	if err != nil {
		return nil, err
	}
	rawValue := strings.TrimSpace(string(output))
	if rawValue == "" || rawValue == "null" {
		return nil, nil
	}

	list := make([]string, 0)
	if strings.HasPrefix(rawValue, "[") {
		if unmarshalErr := json.Unmarshal([]byte(rawValue), &list); unmarshalErr != nil {
			return nil, unmarshalErr
		}
	} else {
		var single string
		if unmarshalErr := json.Unmarshal([]byte(rawValue), &single); unmarshalErr != nil {
			return nil, unmarshalErr
		}
		if strings.TrimSpace(single) != "" {
			list = append(list, single)
		}
	}

	clean := make([]string, 0, len(list))
	seen := make(map[string]struct{}, len(list))
	for _, item := range list {
		ipValue := strings.TrimSpace(item)
		parsed := net.ParseIP(ipValue)
		if parsed == nil || parsed.To4() == nil {
			continue
		}
		canonical := parsed.To4().String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		clean = append(clean, canonical)
	}
	sort.Strings(clean)
	return clean, nil
}

func ensureWindowsTUNAdapterIPv4Routing() (windowsAdapterInfo, error) {
	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; " +
		"$ErrorActionPreference='Stop'; " +
		"$adapter = Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue | " +
		"Where-Object { $_.Name -eq " + quotePowerShellSingle(tunAdapterName) + " -or $_.InterfaceDescription -eq " + quotePowerShellSingle(tunAdapterDescription) + " } | " +
		"Select-Object -First 1; " +
		"if ($null -eq $adapter) { throw 'tun adapter not found: " + escapePowerShellLiteral(tunAdapterName) + "' }; " +
		"$ifIndex = [int]$adapter.ifIndex; " +
		"$prevDns = @(); " +
		"$dnsEntry = Get-DnsClientServerAddress -InterfaceIndex $ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue | Select-Object -First 1; " +
		"if ($dnsEntry -and $dnsEntry.ServerAddresses) { $prevDns = @($dnsEntry.ServerAddresses) }; " +
		"$existing = Get-NetIPAddress -InterfaceIndex $ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue | " +
		"Where-Object { $_.IPAddress -eq " + quotePowerShellSingle(tunRouteIPv4Address) + " -and $_.PrefixLength -eq " + strconv.Itoa(tunRouteIPv4PrefixLength) + " }; " +
		"if ($null -eq $existing) { " +
		"  $oldIPs = Get-NetIPAddress -InterfaceIndex $ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -ne " + quotePowerShellSingle("127.0.0.1") + " }; " +
		"  if ($oldIPs) { $oldIPs | Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue | Out-Null }; " +
		"  New-NetIPAddress -AddressFamily IPv4 -InterfaceIndex $ifIndex -IPAddress " + quotePowerShellSingle(tunRouteIPv4Address) + " -PrefixLength " + strconv.Itoa(tunRouteIPv4PrefixLength) + " -Type Unicast -SkipAsSource $false -ErrorAction Stop | Out-Null; " +
		"}; " +
		"Set-DnsClientServerAddress -InterfaceIndex $ifIndex -ServerAddresses @(" + quotePowerShellSingle(internalDNSListenIPv4) + ") -ErrorAction SilentlyContinue | Out-Null; " +
		"Get-NetRoute -AddressFamily IPv4 -InterfaceIndex $ifIndex -DestinationPrefix " + quotePowerShellSingle(tunRouteSplitPrefixA) + " -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue | Out-Null; " +
		"Get-NetRoute -AddressFamily IPv4 -InterfaceIndex $ifIndex -DestinationPrefix " + quotePowerShellSingle(tunRouteSplitPrefixB) + " -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue | Out-Null; " +
		"New-NetRoute -AddressFamily IPv4 -InterfaceIndex $ifIndex -DestinationPrefix " + quotePowerShellSingle(tunRouteSplitPrefixA) + " -NextHop '0.0.0.0' -RouteMetric 6 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null; " +
		"New-NetRoute -AddressFamily IPv4 -InterfaceIndex $ifIndex -DestinationPrefix " + quotePowerShellSingle(tunRouteSplitPrefixB) + " -NextHop '0.0.0.0' -RouteMetric 6 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null; " +
		"[pscustomobject]@{ InterfaceIndex=[int]$ifIndex; Name=[string]$adapter.Name; InterfaceDescription=[string]$adapter.InterfaceDescription; PreviousDNSServers=[string[]]$prevDns } | ConvertTo-Json -Compress"

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

func clearWindowsTUNAdapterIPv4Routing(interfaceIndex int, restoreDNSServers []string) error {
	indexText := strconv.Itoa(interfaceIndex)
	if interfaceIndex <= 0 {
		indexText = "0"
	}
	cleanDNSServers := make([]string, 0, len(restoreDNSServers))
	seenDNS := make(map[string]struct{}, len(restoreDNSServers))
	for _, rawValue := range restoreDNSServers {
		value := strings.TrimSpace(rawValue)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seenDNS[key]; exists {
			continue
		}
		seenDNS[key] = struct{}{}
		cleanDNSServers = append(cleanDNSServers, value)
	}
	dnsRestoreCommand := "Set-DnsClientServerAddress -InterfaceIndex $ifIndex -ResetServerAddresses -ErrorAction SilentlyContinue | Out-Null"
	if len(cleanDNSServers) > 0 {
		dnsRestoreCommand = "Set-DnsClientServerAddress -InterfaceIndex $ifIndex -ServerAddresses " + powerShellStringArrayLiteral(cleanDNSServers) + " -ErrorAction SilentlyContinue | Out-Null"
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
		dnsRestoreCommand
	_, err := runHiddenPowerShell(script)
	if err != nil {
		return fmt.Errorf("clear tun adapter routing failed: %w", err)
	}
	return nil
}

func powerShellStringArrayLiteral(values []string) string {
	if len(values) == 0 {
		return "@()"
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		quoted = append(quoted, quotePowerShellSingle(clean))
	}
	if len(quoted) == 0 {
		return "@()"
	}
	return "@(" + strings.Join(quoted, ",") + ")"
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
