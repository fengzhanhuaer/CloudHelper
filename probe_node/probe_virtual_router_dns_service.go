package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	probeVirtualRouterDNSListenHost     = probeLocalTUNInterfaceIPv4
	probeVirtualRouterDNSListenPort     = 53
	probeVirtualRouterDNSReadBufferSize = 4096
	probeVirtualRouterDNSHandlerLimit   = 64
)

type probeVirtualRouterDNSStatus struct {
	Enabled    bool   `json:"enabled"`
	ListenAddr string `json:"listen_addr,omitempty"`
	Port       int    `json:"port,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type probeVirtualRouterDNSPacketResponse struct {
	Response []byte
	Domain   string
	RealIPs  []string
}

var probeVirtualRouterDNSState = struct {
	mu        sync.Mutex
	udpConn   net.PacketConn
	tcpLn     net.Listener
	status    probeVirtualRouterDNSStatus
	semaphore chan struct{}
}{
	semaphore: make(chan struct{}, probeVirtualRouterDNSHandlerLimit),
}

var probeVirtualRouterDNSListenPacket = net.ListenPacket
var probeVirtualRouterDNSListen = net.Listen
var probeVirtualRouterApplySystemDNS = applyProbeVirtualRouterSystemDNS
var probeVirtualRouterRestoreSystemDNS = restoreProbeVirtualRouterSystemDNS
var probeVirtualRouterDNSAfterTUNReady = ensureProbeVirtualRouterDNSRuntime
var probeVirtualRouterDNSResolveRealPacket = resolveProbeVirtualRouterDNSRealPacket

var probeVirtualRouterDNSDirectPriorityState = struct {
	mu  sync.Mutex
	ips map[string]time.Time
}{ips: map[string]time.Time{}}

func ensureProbeVirtualRouterDNSRuntime() {
	_ = reconcileProbeVirtualRouterDNSRuntime()
}

func reconcileProbeVirtualRouterDNSRuntime() error {
	if !probeVirtualRouterLocalDNSEnabled() {
		stopProbeVirtualRouterDNSService()
		if err := probeVirtualRouterRestoreSystemDNS(); err != nil {
			logProbeWarnf("restore virtual router system dns failed: %v", err)
			return err
		}
		return nil
	}
	if err := startProbeVirtualRouterDNSService(); err != nil {
		logProbeWarnf("probe virtual router dns service startup failed: %v", err)
		return err
	}
	if err := probeVirtualRouterApplySystemDNS(); err != nil {
		logProbeWarnf("apply virtual router system dns failed: %v", err)
		return err
	}
	return nil
}

func startProbeVirtualRouterDNSService() error {
	addr := net.JoinHostPort(probeVirtualRouterDNSListenHost, strconv.Itoa(probeVirtualRouterDNSListenPort))
	probeVirtualRouterDNSState.mu.Lock()
	if probeVirtualRouterDNSState.udpConn != nil && probeVirtualRouterDNSState.tcpLn != nil && probeVirtualRouterDNSState.status.Enabled {
		probeVirtualRouterDNSState.mu.Unlock()
		return nil
	}
	oldUDP := probeVirtualRouterDNSState.udpConn
	oldTCP := probeVirtualRouterDNSState.tcpLn
	probeVirtualRouterDNSState.udpConn = nil
	probeVirtualRouterDNSState.tcpLn = nil
	probeVirtualRouterDNSState.mu.Unlock()
	if oldUDP != nil {
		_ = oldUDP.Close()
	}
	if oldTCP != nil {
		_ = oldTCP.Close()
	}

	udpConn, udpErr := probeVirtualRouterDNSListenPacket("udp", addr)
	if udpErr != nil {
		setProbeVirtualRouterDNSStatus(false, "", udpErr)
		return udpErr
	}
	tcpLn, tcpErr := probeVirtualRouterDNSListen("tcp", addr)
	if tcpErr != nil {
		_ = udpConn.Close()
		setProbeVirtualRouterDNSStatus(false, "", tcpErr)
		return tcpErr
	}

	probeVirtualRouterDNSState.mu.Lock()
	probeVirtualRouterDNSState.udpConn = udpConn
	probeVirtualRouterDNSState.tcpLn = tcpLn
	probeVirtualRouterDNSState.status = probeVirtualRouterDNSStatus{
		Enabled:    true,
		ListenAddr: addr,
		Port:       probeVirtualRouterDNSListenPort,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	probeVirtualRouterDNSState.mu.Unlock()
	logProbeInfof("probe virtual router dns service enabled: listen=%s", addr)
	go serveProbeVirtualRouterDNSUDP(udpConn)
	go serveProbeVirtualRouterDNSTCP(tcpLn)
	return nil
}

func stopProbeVirtualRouterDNSService() {
	probeVirtualRouterDNSState.mu.Lock()
	udpConn := probeVirtualRouterDNSState.udpConn
	tcpLn := probeVirtualRouterDNSState.tcpLn
	probeVirtualRouterDNSState.udpConn = nil
	probeVirtualRouterDNSState.tcpLn = nil
	probeVirtualRouterDNSState.status = probeVirtualRouterDNSStatus{
		Enabled:   false,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	probeVirtualRouterDNSState.mu.Unlock()
	if udpConn != nil {
		_ = udpConn.Close()
	}
	if tcpLn != nil {
		_ = tcpLn.Close()
	}
}

func setProbeVirtualRouterDNSStatus(enabled bool, listenAddr string, err error) {
	status := probeVirtualRouterDNSStatus{
		Enabled:    enabled,
		ListenAddr: strings.TrimSpace(listenAddr),
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if status.ListenAddr != "" {
		_, portText, splitErr := net.SplitHostPort(status.ListenAddr)
		if splitErr == nil {
			if port, parseErr := strconv.Atoi(portText); parseErr == nil {
				status.Port = port
			}
		}
	}
	if err != nil {
		status.LastError = strings.TrimSpace(err.Error())
	}
	probeVirtualRouterDNSState.mu.Lock()
	probeVirtualRouterDNSState.status = status
	probeVirtualRouterDNSState.mu.Unlock()
}

func currentProbeVirtualRouterDNSStatus() probeVirtualRouterDNSStatus {
	probeVirtualRouterDNSState.mu.Lock()
	defer probeVirtualRouterDNSState.mu.Unlock()
	return probeVirtualRouterDNSState.status
}

func serveProbeVirtualRouterDNSUDP(conn net.PacketConn) {
	if conn == nil {
		return
	}
	buf := make([]byte, probeVirtualRouterDNSReadBufferSize)
	for {
		n, remoteAddr, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || isProbeLocalDNSClosedErr(err) {
				return
			}
			logProbeWarnf("probe virtual router dns udp read failed: %v", err)
			continue
		}
		if n <= 0 || remoteAddr == nil {
			continue
		}
		packet := append([]byte(nil), buf[:n]...)
		dispatchProbeVirtualRouterDNSPacketFromSource(func(response []byte) {
			_, _ = conn.WriteTo(response, remoteAddr)
		}, packet, remoteAddr.String())
	}
}

func serveProbeVirtualRouterDNSTCP(listener net.Listener) {
	if listener == nil {
		return
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || isProbeLocalDNSClosedErr(err) {
				return
			}
			logProbeWarnf("probe virtual router dns tcp accept failed: %v", err)
			continue
		}
		go serveProbeVirtualRouterDNSTCPConn(conn)
	}
}

func serveProbeVirtualRouterDNSTCPConn(conn net.Conn) {
	if conn == nil {
		return
	}
	defer conn.Close()
	requestSource := ""
	if conn.RemoteAddr() != nil {
		requestSource = conn.RemoteAddr().String()
	}
	reader := bufio.NewReader(conn)
	lenBuf := make([]byte, 2)
	for {
		_ = conn.SetDeadline(time.Now().Add(probeLocalDNSUpstreamTimeout))
		if _, err := io.ReadFull(reader, lenBuf); err != nil {
			return
		}
		packetLen := int(binary.BigEndian.Uint16(lenBuf))
		if packetLen <= 0 || packetLen > probeVirtualRouterDNSReadBufferSize {
			return
		}
		packet := make([]byte, packetLen)
		if _, err := io.ReadFull(reader, packet); err != nil {
			return
		}
		response := resolveProbeVirtualRouterDNSPacketBestEffortFromSource(packet, requestSource)
		if len(response) == 0 || len(response) > 65535 {
			return
		}
		binary.BigEndian.PutUint16(lenBuf, uint16(len(response)))
		if _, err := conn.Write(append(lenBuf, response...)); err != nil {
			return
		}
	}
}

func dispatchProbeVirtualRouterDNSPacket(write func([]byte), packet []byte) {
	dispatchProbeVirtualRouterDNSPacketFromSource(write, packet, "")
}

func dispatchProbeVirtualRouterDNSPacketFromSource(write func([]byte), packet []byte, requestSource string) {
	if write == nil || len(packet) == 0 {
		return
	}
	select {
	case probeVirtualRouterDNSState.semaphore <- struct{}{}:
		go func() {
			defer func() { <-probeVirtualRouterDNSState.semaphore }()
			write(resolveProbeVirtualRouterDNSPacketBestEffortFromSource(packet, requestSource))
		}()
	default:
		write(resolveProbeVirtualRouterDNSPacketBestEffortFromSource(packet, requestSource))
	}
}

func resolveProbeVirtualRouterDNSPacketBestEffort(packet []byte) []byte {
	return resolveProbeVirtualRouterDNSPacketBestEffortFromSource(packet, "")
}

func resolveProbeVirtualRouterDNSPacketBestEffortFromSource(packet []byte, requestSource string) []byte {
	result, err := resolveProbeVirtualRouterDNSPacketFromSource(packet, requestSource)
	if err != nil {
		logProbeWarnf("probe virtual router dns resolve failed: %v", err)
	}
	if len(result.Response) == 0 {
		return buildProbeLocalDNSServfail(packet)
	}
	return result.Response
}

func resolveProbeVirtualRouterDNSPacket(packet []byte) (result probeVirtualRouterDNSPacketResponse, err error) {
	return resolveProbeVirtualRouterDNSPacketFromSource(packet, "")
}

func resolveProbeVirtualRouterDNSPacketFromSource(packet []byte, requestSource string) (result probeVirtualRouterDNSPacketResponse, err error) {
	domain, qType := parseProbeLocalDNSQueryDomainAndType(packet)
	cleanDomain := normalizeProbeVirtualRouterDomain(domain)
	result = probeVirtualRouterDNSPacketResponse{Domain: cleanDomain}
	trackingAction := "direct"
	trackingExitNodeID := ""
	trackingFakeIP := ""
	defer func() {
		recordProbeVirtualRouterRecentDNSQuery(cleanDomain, trackingAction, trackingExitNodeID, trackingFakeIP, result.RealIPs, err)
		recordProbeDomainObservation(cleanDomain, "dns", requestSource, trackingAction, result.RealIPs, err)
	}()
	if cleanDomain == "" {
		result.Response = buildProbeLocalDNSRefused(packet)
		return result, nil
	}
	rule, matched := currentProbeVirtualRouterRouteRuleForDomain(cleanDomain)
	if !matched {
		response, realIPs, err := resolveProbeVirtualRouterDNSDirectPacket(packet, cleanDomain, false)
		result.Response = response
		result.RealIPs = realIPs
		return result, err
	}
	switch strings.TrimSpace(rule.Action) {
	case "reject":
		trackingAction = "reject"
		result.Response = buildProbeLocalDNSRefused(packet)
		return result, nil
	case "probe_exit":
		trackingAction = "fake_ip"
		trackingExitNodeID = normalizeProbeRouteNodeID(rule.ExitNodeID)
		if qType != dnsmessage.TypeA {
			response, realIPs, err := probeVirtualRouterDNSResolveRealPacket(packet, cleanDomain)
			result.Response = response
			result.RealIPs = realIPs
			return result, err
		}
		item, err := resolveProbeVirtualRouterFakeIPForDNS(cleanDomain, rule)
		if err != nil {
			result.Response = buildProbeLocalDNSServfail(packet)
			return result, err
		}
		trackingFakeIP = strings.TrimSpace(item.FakeIP)
		result.Response = buildProbeLocalDNSSuccessA(packet, item.FakeIP)
		return result, nil
	default:
		response, realIPs, err := resolveProbeVirtualRouterDNSDirectPacket(packet, cleanDomain, true)
		result.Response = response
		result.RealIPs = realIPs
		return result, err
	}
}

func resolveProbeVirtualRouterDNSDirectPacket(packet []byte, domain string, domainRuleMatched bool) ([]byte, []string, error) {
	response, realIPs, err := probeVirtualRouterDNSResolveRealPacket(packet, domain)
	if err != nil {
		return response, realIPs, err
	}
	if err := ensureProbeVirtualRouterDNSDirectBypasses(realIPs, domainRuleMatched); err != nil {
		return nil, realIPs, err
	}
	return response, realIPs, nil
}

func ensureProbeVirtualRouterDNSDirectBypasses(realIPs []string, domainRuleMatched bool) error {
	if !probeVirtualRouterLocalEntryEnabled() {
		return nil
	}
	var allErr error
	for _, ipText := range filterProbeLocalIPv4StringsFromList(realIPs) {
		if !domainRuleMatched {
			if rule, ok := currentProbeVirtualRouterRouteRuleForIP(ipText); ok && sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) == "probe_exit" {
				continue
			}
		}
		target := net.JoinHostPort(ipText, "0")
		if err := probeVirtualRouterEnsureDirectBypass(target); err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("prepare direct dns bypass for %s: %w", ipText, err))
		}
	}
	if allErr == nil && domainRuleMatched {
		rememberProbeVirtualRouterDNSDirectPriorityIPs(realIPs)
	}
	return allErr
}

func rememberProbeVirtualRouterDNSDirectPriorityIPs(realIPs []string) {
	now := time.Now().UTC()
	probeVirtualRouterDNSDirectPriorityState.mu.Lock()
	defer probeVirtualRouterDNSDirectPriorityState.mu.Unlock()
	for ipText, expiresAt := range probeVirtualRouterDNSDirectPriorityState.ips {
		if !expiresAt.After(now) {
			delete(probeVirtualRouterDNSDirectPriorityState.ips, ipText)
		}
	}
	for _, ipText := range filterProbeLocalIPv4StringsFromList(realIPs) {
		probeVirtualRouterDNSDirectPriorityState.ips[ipText] = now.Add(probeLocalDNSCacheTTL)
	}
}

func probeVirtualRouterDNSDirectPriorityIP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	now := time.Now().UTC()
	key := ip4.String()
	probeVirtualRouterDNSDirectPriorityState.mu.Lock()
	defer probeVirtualRouterDNSDirectPriorityState.mu.Unlock()
	expiresAt, ok := probeVirtualRouterDNSDirectPriorityState.ips[key]
	if !ok || !expiresAt.After(now) {
		delete(probeVirtualRouterDNSDirectPriorityState.ips, key)
		return false
	}
	return true
}

func resolveProbeVirtualRouterDNSRealPacket(packet []byte, domain string) ([]byte, []string, error) {
	decision := probeLocalDNSRouteDecision{
		Group:  "virtual-router",
		Action: "direct",
	}
	return resolveProbeVirtualRouterDNSUpstreamResponse(packet, domain, decision)
}
