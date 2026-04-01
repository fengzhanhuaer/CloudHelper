//go:build windows

package backend

import (
	"errors"
	"fmt"
	"net"
	"strings"
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
	AdapterGUID          string   `json:"AdapterGUID,omitempty"`
	PreviousDNSServers   []string `json:"PreviousDNSServers"`
	IPv4Addrs            []string `json:"IPv4Addrs,omitempty"`
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

	state.DirectDNSServers = s.collectConfiguredDNSBypassIPv4Addrs()

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
	return windowsDetectPrimaryIPv4Route()
}

func detectWindowsInterfaceIPv4DNSServers(interfaceIndex int) ([]string, error) {
	if interfaceIndex <= 0 {
		return nil, errors.New("invalid interface index")
	}
	adapter, err := windowsFindAdapterByIfIndex(interfaceIndex)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), adapter.PreviousDNSServers...), nil
}

func ensureWindowsTUNAdapterIPv4Routing() (windowsAdapterInfo, error) {
	adapter, err := windowsFindAdapterByNameOrDescription(tunAdapterName, tunAdapterDescription)
	if err != nil {
		return windowsAdapterInfo{}, err
	}
	if adapter.InterfaceIndex <= 0 {
		return windowsAdapterInfo{}, errors.New("invalid tun adapter interface index")
	}
	if err := windowsEnsureInterfaceIPv4Address(adapter.InterfaceIndex, tunRouteIPv4Address, tunRouteIPv4PrefixLength); err != nil {
		return windowsAdapterInfo{}, err
	}
	if err := windowsSetInterfaceIPv4DNSServers(adapter.InterfaceIndex, []string{internalDNSListenIPv4}); err != nil {
		return windowsAdapterInfo{}, err
	}
	if err := windowsDeleteIPv4Route(tunRouteSplitPrefixA, adapter.InterfaceIndex, "", false); err != nil {
		return windowsAdapterInfo{}, err
	}
	if err := windowsDeleteIPv4Route(tunRouteSplitPrefixB, adapter.InterfaceIndex, "", false); err != nil {
		return windowsAdapterInfo{}, err
	}
	if err := windowsEnsureIPv4Route(tunRouteSplitPrefixA, adapter.InterfaceIndex, "0.0.0.0", 6); err != nil {
		return windowsAdapterInfo{}, err
	}
	if err := windowsEnsureIPv4Route(tunRouteSplitPrefixB, adapter.InterfaceIndex, "0.0.0.0", 6); err != nil {
		return windowsAdapterInfo{}, err
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
	if err := windowsEnsureIPv4Route(cleanPrefix, interfaceIndex, cleanHop, 1); err != nil {
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
	if err := windowsDeleteIPv4Route(cleanPrefix, interfaceIndex, cleanHop, true); err != nil {
		return fmt.Errorf("remove bypass route failed (%s via %s if %d): %w", cleanPrefix, cleanHop, interfaceIndex, err)
	}
	return nil
}

func clearWindowsTUNAdapterIPv4Routing(interfaceIndex int, restoreDNSServers []string) error {
	ifIndex := interfaceIndex
	if ifIndex <= 0 {
		adapter, err := windowsFindAdapterByNameOrDescription(tunAdapterName, tunAdapterDescription)
		if err != nil {
			return nil
		}
		ifIndex = adapter.InterfaceIndex
	}
	if ifIndex <= 0 {
		return nil
	}
	if err := windowsDeleteIPv4Route(tunRouteSplitPrefixA, ifIndex, "", false); err != nil {
		return fmt.Errorf("clear tun adapter routing failed: %w", err)
	}
	if err := windowsDeleteIPv4Route(tunRouteSplitPrefixB, ifIndex, "", false); err != nil {
		return fmt.Errorf("clear tun adapter routing failed: %w", err)
	}
	if err := windowsSetInterfaceIPv4DNSServers(ifIndex, restoreDNSServers); err != nil {
		return fmt.Errorf("clear tun adapter routing failed: %w", err)
	}
	return nil
}
