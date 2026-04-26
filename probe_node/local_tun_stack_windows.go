//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type probeLocalTUNPacketStack interface {
	Write([]byte) (int, error)
	Close() error
}

type probeLocalTUNSimplePacketStack struct {
	closeOnce sync.Once
	closed    bool
}

var (
	probeLocalAcquireDirectBypassRoute = acquireProbeLocalTUNDirectBypassRoute
	probeLocalReleaseDirectBypassRoute = releaseProbeLocalTUNDirectBypassRoute
)

var probeLocalDirectBypassState = struct {
	mu      sync.Mutex
	ref     map[string]int
	hosts   map[string]string
	targets map[string]map[string]struct{}
}{
	ref:     map[string]int{},
	hosts:   map[string]string{},
	targets: map[string]map[string]struct{}{},
}

func startProbeLocalTUNPacketStack() error {
	probeLocalTUNDataPlaneState.mu.Lock()
	defer probeLocalTUNDataPlaneState.mu.Unlock()
	if probeLocalTUNDataPlaneState.packetStack != nil {
		return nil
	}
	probeLocalTUNDataPlaneState.packetStack = &probeLocalTUNSimplePacketStack{}
	logProbeInfof("probe local tun packet stack started")
	return nil
}

func stopProbeLocalTUNPacketStack() error {
	probeLocalTUNDataPlaneState.mu.Lock()
	packetStack := probeLocalTUNDataPlaneState.packetStack
	probeLocalTUNDataPlaneState.packetStack = nil
	probeLocalTUNDataPlaneState.mu.Unlock()
	if packetStack == nil {
		return nil
	}
	err := packetStack.Close()
	logProbeInfof("probe local tun packet stack stopped")
	return err
}

func (s *probeLocalTUNSimplePacketStack) Write(packet []byte) (int, error) {
	if s == nil {
		return 0, errors.New("packet stack is nil")
	}
	if s.closed {
		return 0, errors.New("packet stack is closed")
	}
	if len(packet) == 0 {
		return 0, nil
	}
	network, targetAddr, parseErr := parseProbeLocalTUNPacketTarget(packet)
	if parseErr != nil {
		return len(packet), nil
	}
	route, routeErr := decideProbeLocalRouteForTarget(targetAddr)
	if routeErr != nil {
		var rejectErr *probeLocalRouteRejectError
		if errors.As(routeErr, &rejectErr) {
			return len(packet), nil
		}
		return 0, routeErr
	}
	if route.Reject {
		return len(packet), nil
	}
	if !route.Direct {
		if _, _, err := normalizeProbeLocalTunnelNodeID(route.TunnelNodeID); err != nil {
			return 0, err
		}
		logProbeInfof("probe local tun packet routed to tunnel: network=%s target=%s group=%s node=%s", network, route.TargetAddr, route.Group, route.TunnelNodeID)
		return len(packet), nil
	}
	if err := ensureProbeLocalDirectBypassForTarget(route.TargetAddr); err != nil {
		return 0, err
	}
	return len(packet), nil
}

func (s *probeLocalTUNSimplePacketStack) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closed = true
		releaseProbeLocalAllDirectBypassRoutes()
	})
	return nil
}

func parseProbeLocalTUNPacketTarget(packet []byte) (network string, targetAddr string, err error) {
	if len(packet) == 0 {
		return "", "", errors.New("empty packet")
	}
	version := packet[0] >> 4
	switch version {
	case 4:
		return parseProbeLocalTUNIPv4Target(packet)
	case 6:
		return parseProbeLocalTUNIPv6Target(packet)
	default:
		return "", "", fmt.Errorf("unsupported ip version: %d", version)
	}
}

func parseProbeLocalTUNIPv4Target(packet []byte) (network string, targetAddr string, err error) {
	if len(packet) < 20 {
		return "", "", errors.New("ipv4 header too short")
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl+4 {
		return "", "", errors.New("invalid ipv4 header length")
	}
	proto := packet[9]
	dstIP := net.IPv4(packet[16], packet[17], packet[18], packet[19]).String()
	dstPort := uint16(packet[ihl+2])<<8 | uint16(packet[ihl+3])
	if dstPort == 0 {
		return "", "", errors.New("missing destination port")
	}
	switch proto {
	case 6:
		network = "tcp"
	case 17:
		network = "udp"
	default:
		return "", "", fmt.Errorf("unsupported ipv4 transport protocol: %d", proto)
	}
	return network, net.JoinHostPort(dstIP, strconv.Itoa(int(dstPort))), nil
}

func parseProbeLocalTUNIPv6Target(packet []byte) (network string, targetAddr string, err error) {
	if len(packet) < 44 {
		return "", "", errors.New("ipv6 packet too short")
	}
	nextHeader := packet[6]
	dstIP := net.IP(packet[24:40]).String()
	dstPort := uint16(packet[42])<<8 | uint16(packet[43])
	if strings.TrimSpace(dstIP) == "" || dstPort == 0 {
		return "", "", errors.New("invalid ipv6 target")
	}
	switch nextHeader {
	case 6:
		network = "tcp"
	case 17:
		network = "udp"
	default:
		return "", "", fmt.Errorf("unsupported ipv6 next header: %d", nextHeader)
	}
	return network, net.JoinHostPort(dstIP, strconv.Itoa(int(dstPort))), nil
}

func ensureProbeLocalDirectBypassForTarget(targetAddr string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return err
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return nil
	}
	ipText := ip.String()

	probeLocalDirectBypassState.mu.Lock()
	if probeLocalDirectBypassState.ref == nil {
		probeLocalDirectBypassState.ref = map[string]int{}
	}
	if probeLocalDirectBypassState.hosts == nil {
		probeLocalDirectBypassState.hosts = map[string]string{}
	}
	if probeLocalDirectBypassState.targets == nil {
		probeLocalDirectBypassState.targets = map[string]map[string]struct{}{}
	}
	probeLocalDirectBypassState.ref[ipText]++
	probeLocalDirectBypassState.hosts[ipText] = ipText
	if _, ok := probeLocalDirectBypassState.targets[ipText]; !ok {
		probeLocalDirectBypassState.targets[ipText] = map[string]struct{}{}
	}
	probeLocalDirectBypassState.targets[ipText][strings.TrimSpace(targetAddr)] = struct{}{}
	needCreate := probeLocalDirectBypassState.ref[ipText] == 1
	probeLocalDirectBypassState.mu.Unlock()

	if !needCreate {
		return nil
	}
	release, acqErr := probeLocalAcquireDirectBypassRoute(ipText)
	if acqErr != nil {
		probeLocalDirectBypassState.mu.Lock()
		probeLocalDirectBypassState.ref[ipText]--
		if probeLocalDirectBypassState.ref[ipText] <= 0 {
			delete(probeLocalDirectBypassState.ref, ipText)
			delete(probeLocalDirectBypassState.hosts, ipText)
			delete(probeLocalDirectBypassState.targets, ipText)
		}
		probeLocalDirectBypassState.mu.Unlock()
		return acqErr
	}
	if release != nil {
		release()
	}
	return nil
}

func acquireProbeLocalTUNDirectBypassRoute(host string) (func(), error) {
	ip := strings.TrimSpace(host)
	if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
		return nil, fmt.Errorf("invalid bypass host: %s", host)
	}
	gateway, ifIndex, err := resolveProbeLocalWindowsRouteTarget()
	if err != nil {
		return nil, err
	}
	metric := strconv.Itoa(probeLocalWindowsRouteMetric)
	ifText := strconv.Itoa(ifIndex)
	_, addErr := probeLocalWindowsRunCommand(6*time.Second, "route", "ADD", ip, "MASK", "255.255.255.255", gateway, "METRIC", metric, "IF", ifText)
	if addErr != nil && !isProbeLocalWindowsRouteExistsErr(addErr) {
		return nil, addErr
	}
	if addErr != nil && isProbeLocalWindowsRouteExistsErr(addErr) {
		_, changeErr := probeLocalWindowsRunCommand(6*time.Second, "route", "CHANGE", ip, "MASK", "255.255.255.255", gateway, "METRIC", metric, "IF", ifText)
		if changeErr != nil {
			return nil, changeErr
		}
	}
	return func() {}, nil
}

func releaseProbeLocalTUNDirectBypassRoute(host string) {
	ip := strings.TrimSpace(host)
	if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
		return
	}
	gateway, ifIndex, err := resolveProbeLocalWindowsRouteTarget()
	if err != nil {
		return
	}
	ifText := strconv.Itoa(ifIndex)
	_, delErr := probeLocalWindowsRunCommand(6*time.Second, "route", "DELETE", ip, "MASK", "255.255.255.255", gateway, "IF", ifText)
	if delErr != nil && !isProbeLocalWindowsRouteMissingErr(delErr) {
		logProbeWarnf("probe local bypass route delete failed: host=%s err=%v", ip, delErr)
	}
}

func releaseProbeLocalDirectBypassForHost(host string) {
	ip := strings.TrimSpace(host)
	if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
		return
	}
	probeLocalDirectBypassState.mu.Lock()
	if probeLocalDirectBypassState.ref == nil {
		probeLocalDirectBypassState.ref = map[string]int{}
	}
	count := probeLocalDirectBypassState.ref[ip]
	if count <= 1 {
		delete(probeLocalDirectBypassState.ref, ip)
		delete(probeLocalDirectBypassState.hosts, ip)
		delete(probeLocalDirectBypassState.targets, ip)
		probeLocalDirectBypassState.mu.Unlock()
		probeLocalReleaseDirectBypassRoute(ip)
		return
	}
	probeLocalDirectBypassState.ref[ip] = count - 1
	probeLocalDirectBypassState.mu.Unlock()
}

func releaseProbeLocalAllDirectBypassRoutes() {
	probeLocalDirectBypassState.mu.Lock()
	hosts := make([]string, 0, len(probeLocalDirectBypassState.hosts))
	for host := range probeLocalDirectBypassState.hosts {
		hosts = append(hosts, host)
	}
	probeLocalDirectBypassState.ref = map[string]int{}
	probeLocalDirectBypassState.hosts = map[string]string{}
	probeLocalDirectBypassState.targets = map[string]map[string]struct{}{}
	probeLocalDirectBypassState.mu.Unlock()
	for _, host := range hosts {
		probeLocalReleaseDirectBypassRoute(host)
	}
}

func resetProbeLocalDirectBypassStateForTest() {
	probeLocalDirectBypassState.mu.Lock()
	probeLocalDirectBypassState.ref = map[string]int{}
	probeLocalDirectBypassState.hosts = map[string]string{}
	probeLocalDirectBypassState.targets = map[string]map[string]struct{}{}
	probeLocalDirectBypassState.mu.Unlock()
	probeLocalAcquireDirectBypassRoute = acquireProbeLocalTUNDirectBypassRoute
	probeLocalReleaseDirectBypassRoute = releaseProbeLocalTUNDirectBypassRoute
}
