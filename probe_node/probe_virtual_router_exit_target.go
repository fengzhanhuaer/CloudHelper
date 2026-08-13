package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"gvisor.dev/gvisor/pkg/tcpip"
)

type probeVirtualRouterExitTarget struct {
	Host     string
	Port     uint16
	IsDomain bool
}

func (t probeVirtualRouterExitTarget) Address() string {
	return net.JoinHostPort(strings.TrimSpace(t.Host), strconv.Itoa(int(t.Port)))
}

func probeVirtualRouterFakeIPTargetFromTransportID(addr tcpip.Address, port uint16) (probeVirtualRouterExitTarget, probeVirtualRouterFakeIPEntry, error) {
	if port == 0 {
		return probeVirtualRouterExitTarget{}, probeVirtualRouterFakeIPEntry{}, errors.New("transport target port is empty")
	}
	host := strings.TrimSpace(addr.String())
	var entry probeVirtualRouterFakeIPEntry
	ok := false
	if probeVirtualRouterIPInCurrentFakeCIDR(host) {
		entry, ok = currentProbeVirtualRouterFakeIPEntryByIPWithControllerRefresh(host)
	}
	if !ok {
		rule, ruleOK := currentProbeVirtualRouterRouteRuleForIP(host)
		if !ruleOK || sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) != "probe_exit" || normalizeProbeRouteNodeID(rule.ExitNodeID) != currentProbeVirtualRouterLocalNodeID() {
			return probeVirtualRouterExitTarget{}, probeVirtualRouterFakeIPEntry{}, errors.New("fake ip mapping is unavailable")
		}
		if net.ParseIP(host) == nil {
			return probeVirtualRouterExitTarget{}, probeVirtualRouterFakeIPEntry{}, fmt.Errorf("cidr route target is invalid: ip=%s", host)
		}
		return probeVirtualRouterExitTarget{Host: host, Port: port}, probeVirtualRouterFakeIPEntry{
			Domain:     host,
			FakeIP:     host,
			Action:     "probe_exit",
			ExitNodeID: normalizeProbeRouteNodeID(rule.ExitNodeID),
			RuleID:     strings.TrimSpace(rule.ID),
		}, nil
	}
	domain := normalizeProbeVirtualRouterDomain(entry.Domain)
	if domain == "" {
		return probeVirtualRouterExitTarget{}, probeVirtualRouterFakeIPEntry{}, errors.New("fake ip mapping domain is empty")
	}
	return probeVirtualRouterExitTarget{Host: domain, Port: port, IsDomain: true}, entry, nil
}

func probeVirtualRouterExitAddressesForTarget(target probeVirtualRouterExitTarget) ([]string, error) {
	if target.Port == 0 || strings.TrimSpace(target.Host) == "" {
		return nil, errors.New("exit target is empty")
	}
	if !target.IsDomain {
		return []string{target.Address()}, nil
	}
	ips, err := resolveProbeVirtualRouterFakeIPExitRealIPs(target.Host)
	if err != nil {
		return nil, err
	}
	targets := buildProbeLocalTunnelRouteTargetCandidates(ips, strconv.Itoa(int(target.Port)))
	if len(targets) == 0 {
		return nil, fmt.Errorf("resolve fake ip domain returned no usable ipv4: domain=%s", target.Host)
	}
	return targets, nil
}
