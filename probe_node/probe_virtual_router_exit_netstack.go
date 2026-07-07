package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	probeVirtualRouterExitNetstackNICID       = tcpip.NICID(1)
	probeVirtualRouterExitNetstackQueueSize   = 1024
	probeVirtualRouterExitNetstackMTU         = 65535
	probeVirtualRouterExitNetstackTCPWindow   = 1 << 20
	probeVirtualRouterExitNetstackTCPInflight = 512
	probeVirtualRouterExitDialTimeout         = 12 * time.Second
	probeVirtualRouterExitUDPIdleTimeout      = 90 * time.Second
	probeVirtualRouterExitICMPTimeout         = 5 * time.Second
)

type probeVirtualRouterExitNetstack struct {
	stack     *stack.Stack
	linkEP    *channel.Endpoint
	cancel    context.CancelFunc
	doneCh    chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
}

var probeVirtualRouterExitNetstackState = struct {
	mu     sync.Mutex
	runner *probeVirtualRouterExitNetstack
}{}

var probeVirtualRouterSendICMPEcho = sendProbeVirtualRouterICMPEcho

func handleProbeVirtualRouterFakeIPExitPacket(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, packet []byte, path []string) bool {
	dstIP := probeVirtualRouterIPv4Destination(packet)
	entry, ok := currentProbeVirtualRouterFakeIPEntryByIPWithControllerRefresh(dstIP)
	if !ok || normalizeProbeRouteNodeID(entry.ExitNodeID) != currentProbeVirtualRouterLocalNodeIDForRuntime(runtime) {
		return false
	}
	if handleProbeVirtualRouterFakeIPExitICMPEchoRequest(runtime, link, packet, path, entry) {
		return true
	}
	info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet)
	if !ok || info.DestinationPort == 0 {
		return false
	}
	runner, err := currentProbeVirtualRouterExitNetstack()
	if err != nil {
		recordProbeVirtualRouterRuntimeDeliveryError(runtime, err)
		log.Printf("probe virtual router fake ip exit netstack unavailable: route=%s dst=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), dstIP, err)
		return false
	}
	if err := runner.Inject(packet); err != nil {
		recordProbeVirtualRouterRuntimeDeliveryError(runtime, err)
		log.Printf("probe virtual router fake ip exit inject failed: route=%s proto=%s src=%s:%d dst=%s:%d domain=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, strings.TrimSpace(entry.Domain), err)
		return false
	}
	log.Printf("probe virtual router fake ip exit inject ok: route=%s proto=%s src=%s:%d dst=%s:%d domain=%s path=%s", probeVirtualRouterRuntimeLogRouteID(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, strings.TrimSpace(entry.Domain), strings.Join(path, ">"))
	return true
}

func currentProbeVirtualRouterExitNetstack() (*probeVirtualRouterExitNetstack, error) {
	probeVirtualRouterExitNetstackState.mu.Lock()
	defer probeVirtualRouterExitNetstackState.mu.Unlock()
	if probeVirtualRouterExitNetstackState.runner != nil && !probeVirtualRouterExitNetstackState.runner.closed.Load() {
		return probeVirtualRouterExitNetstackState.runner, nil
	}
	runner, err := newProbeVirtualRouterExitNetstack()
	if err != nil {
		return nil, err
	}
	probeVirtualRouterExitNetstackState.runner = runner
	return runner, nil
}

func resetProbeVirtualRouterExitNetstackForTest() {
	probeVirtualRouterExitNetstackState.mu.Lock()
	runner := probeVirtualRouterExitNetstackState.runner
	probeVirtualRouterExitNetstackState.runner = nil
	probeVirtualRouterExitNetstackState.mu.Unlock()
	if runner != nil {
		_ = runner.Close()
	}
}

func handleProbeVirtualRouterFakeIPExitICMPEchoRequest(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, packet []byte, path []string, entry probeVirtualRouterFakeIPEntry) bool {
	info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet)
	if !ok || info.Kind != "echo_request" {
		return false
	}
	targetIPs, err := probeVirtualRouterFakeIPRealIPs(strings.TrimSpace(entry.Domain))
	if err != nil {
		recordProbeVirtualRouterRuntimeDeliveryError(runtime, err)
		log.Printf("probe virtual router fake ip icmp exit resolve failed: route=%s fake_ip=%s domain=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), info.DestinationIP, strings.TrimSpace(entry.Domain), err)
		return true
	}
	request := append([]byte(nil), packet...)
	ingressPath := append([]string(nil), path...)
	go func() {
		reply, targetIP, err := probeVirtualRouterSendFakeIPICMPEcho(request, targetIPs)
		if err != nil {
			recordProbeVirtualRouterRuntimeDeliveryError(runtime, err)
			log.Printf("probe virtual router fake ip icmp exit failed: route=%s fake_ip=%s domain=%s targets=%s id=%d seq=%d err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), info.DestinationIP, strings.TrimSpace(entry.Domain), strings.Join(targetIPs, ","), info.ID, info.Sequence, err)
			return
		}
		dstIP := probeVirtualRouterIPv4Destination(reply)
		replyPath := probeVirtualRouterReversePath(ingressPath)
		if len(replyPath) < 2 {
			replyPath = currentProbeVirtualRouterPathForPacket(reply, dstIP)
		}
		if len(replyPath) < 2 {
			log.Printf("probe virtual router fake ip icmp exit reply path unavailable: route=%s dst=%s target=%s", probeVirtualRouterRuntimeLogRouteID(runtime), dstIP, targetIP)
			return
		}
		trace := appendProbeVirtualRouterICMPTrace(nil, runtime, "fake_exit_echo_reply", "", "")
		if link != nil {
			if err := writeProbeVirtualRouterIPFrame(link, reply, replyPath, trace); err != nil {
				recordProbeVirtualRouterRuntimeOpenError(probeVirtualRouterRuntimeLogRouteID(runtime), err)
				log.Printf("probe virtual router fake ip icmp exit reply write failed: route=%s dst=%s target=%s path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), dstIP, targetIP, strings.Join(replyPath, ">"), err)
				return
			}
			recordProbeVirtualRouterRuntimeFrameSent(runtime, len(reply))
			recordProbeVirtualRouterRuntimePacketForwarded(runtime, len(reply))
		} else if err := forwardProbeVirtualRouterPacketAlongPath(reply, dstIP, replyPath, trace); err != nil {
			log.Printf("probe virtual router fake ip icmp exit reply forward failed: route=%s dst=%s target=%s path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), dstIP, targetIP, strings.Join(replyPath, ">"), err)
			return
		}
		if replyInfo, ok := probeVirtualRouterParseICMPEchoLogInfo(reply); ok {
			log.Printf("probe virtual router fake ip icmp exit ok: route=%s fake_ip=%s target=%s src=%s dst=%s id=%d seq=%d path=%s", probeVirtualRouterRuntimeLogRouteID(runtime), info.DestinationIP, targetIP, replyInfo.SourceIP, replyInfo.DestinationIP, replyInfo.ID, replyInfo.Sequence, strings.Join(replyPath, ">"))
		}
	}()
	log.Printf("probe virtual router fake ip icmp exit start: route=%s src=%s fake_ip=%s domain=%s targets=%s id=%d seq=%d path=%s", probeVirtualRouterRuntimeLogRouteID(runtime), info.SourceIP, info.DestinationIP, strings.TrimSpace(entry.Domain), strings.Join(targetIPs, ","), info.ID, info.Sequence, strings.Join(path, ">"))
	return true
}

func newProbeVirtualRouterExitNetstack() (*probeVirtualRouterExitNetstack, error) {
	gStack := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	linkEP := channel.New(probeVirtualRouterExitNetstackQueueSize, probeVirtualRouterExitNetstackMTU, "")
	if err := tcpipErrorToError(gStack.CreateNIC(probeVirtualRouterExitNetstackNICID, linkEP)); err != nil {
		gStack.Destroy()
		return nil, err
	}
	if err := tcpipErrorToError(gStack.SetPromiscuousMode(probeVirtualRouterExitNetstackNICID, true)); err != nil {
		gStack.Destroy()
		return nil, err
	}
	if err := tcpipErrorToError(gStack.SetSpoofing(probeVirtualRouterExitNetstackNICID, true)); err != nil {
		gStack.Destroy()
		return nil, err
	}
	gStack.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: probeVirtualRouterExitNetstackNICID}})

	ctx, cancel := context.WithCancel(context.Background())
	runner := &probeVirtualRouterExitNetstack{
		stack:  gStack,
		linkEP: linkEP,
		cancel: cancel,
		doneCh: make(chan struct{}),
	}
	tcpForwarder := tcp.NewForwarder(gStack, probeVirtualRouterExitNetstackTCPWindow, probeVirtualRouterExitNetstackTCPInflight, runner.handleTCPForwarder)
	udpForwarder := udp.NewForwarder(gStack, runner.handleUDPForwarder)
	gStack.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)
	gStack.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)
	go runner.outputLoop(ctx)
	return runner, nil
}

func (n *probeVirtualRouterExitNetstack) Inject(packet []byte) error {
	if n == nil || n.closed.Load() {
		return io.ErrClosedPipe
	}
	if len(packet) == 0 {
		return nil
	}
	protocol, err := probeVirtualRouterNetstackProtocolFromPacket(packet)
	if err != nil {
		return err
	}
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(append([]byte(nil), packet...)),
	})
	defer pkt.DecRef()
	n.linkEP.InjectInbound(protocol, pkt)
	return nil
}

func (n *probeVirtualRouterExitNetstack) outputLoop(ctx context.Context) {
	defer close(n.doneCh)
	for {
		packet := n.linkEP.ReadContext(ctx)
		if packet == nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		view := packet.ToView()
		payload := append([]byte(nil), view.AsSlice()...)
		view.Release()
		packet.DecRef()
		if len(payload) == 0 {
			continue
		}
		dstIP := probeVirtualRouterIPv4Destination(payload)
		path := currentProbeVirtualRouterPathForPacket(payload, dstIP)
		if len(path) < 2 {
			log.Printf("probe virtual router fake ip exit response drop: dst=%s reason=path_unavailable", dstIP)
			continue
		}
		if err := forwardProbeVirtualRouterPacketAlongPath(payload, dstIP, path, nil); err != nil {
			log.Printf("probe virtual router fake ip exit response forward failed: dst=%s path=%s err=%v", dstIP, strings.Join(path, ">"), err)
		}
	}
}

func (n *probeVirtualRouterExitNetstack) Close() error {
	if n == nil {
		return nil
	}
	n.closeOnce.Do(func() {
		n.closed.Store(true)
		if n.cancel != nil {
			n.cancel()
		}
		if n.linkEP != nil {
			n.linkEP.Close()
		}
		select {
		case <-n.doneCh:
		case <-time.After(2 * time.Second):
		}
		if n.stack != nil {
			n.stack.Destroy()
		}
	})
	return nil
}

func (n *probeVirtualRouterExitNetstack) handleTCPForwarder(req *tcp.ForwarderRequest) {
	if req == nil {
		return
	}
	id := req.ID()
	targetAddrs, err := probeVirtualRouterFakeIPTargetsFromTransportID(id.LocalAddress, id.LocalPort)
	if err != nil {
		req.Complete(true)
		return
	}
	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		req.Complete(true)
		return
	}
	req.Complete(false)
	inbound := gonet.NewTCPConn(&wq, ep)
	outbound, err := dialProbeVirtualRouterExitTCP(targetAddrs)
	if err != nil {
		log.Printf("probe virtual router fake ip tcp exit open failed: targets=%s err=%v", strings.Join(targetAddrs, ","), err)
		_ = inbound.Close()
		return
	}
	log.Printf("probe virtual router fake ip tcp exit open ok: targets=%s remote=%s", strings.Join(targetAddrs, ","), outbound.RemoteAddr())
	go pipeProbeVirtualRouterExitConn(outbound, inbound)
	go pipeProbeVirtualRouterExitConn(inbound, outbound)
}

func (n *probeVirtualRouterExitNetstack) handleUDPForwarder(req *udp.ForwarderRequest) {
	if req == nil {
		return
	}
	id := req.ID()
	targetAddrs, err := probeVirtualRouterFakeIPTargetsFromTransportID(id.LocalAddress, id.LocalPort)
	if err != nil {
		return
	}
	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		return
	}
	inbound := gonet.NewUDPConn(&wq, ep)
	outbound, err := dialProbeVirtualRouterExitUDP(targetAddrs)
	if err != nil {
		log.Printf("probe virtual router fake ip udp exit open failed: targets=%s err=%v", strings.Join(targetAddrs, ","), err)
		_ = inbound.Close()
		return
	}
	log.Printf("probe virtual router fake ip udp exit open ok: targets=%s remote=%s", strings.Join(targetAddrs, ","), outbound.RemoteAddr())
	go relayProbeVirtualRouterExitUDP(inbound, outbound)
}

func probeVirtualRouterFakeIPTargetsFromTransportID(addr tcpip.Address, port uint16) ([]string, error) {
	if port == 0 {
		return nil, errors.New("transport target port is empty")
	}
	host := strings.TrimSpace(addr.String())
	entry, ok := currentProbeVirtualRouterFakeIPEntryByIPWithControllerRefresh(host)
	if !ok {
		return nil, errors.New("fake ip mapping is unavailable")
	}
	domain := normalizeProbeVirtualRouterDomain(entry.Domain)
	if domain == "" {
		return nil, errors.New("fake ip mapping domain is empty")
	}
	ips, err := resolveProbeVirtualRouterFakeIPExitRealIPs(domain)
	if err != nil {
		return nil, err
	}
	targets := buildProbeLocalTunnelRouteTargetCandidates(ips, strconv.Itoa(int(port)))
	if len(targets) == 0 {
		return nil, fmt.Errorf("resolve fake ip domain returned no usable ipv4: domain=%s", domain)
	}
	return targets, nil
}

func currentProbeVirtualRouterFakeIPEntryByIPWithControllerRefresh(ip string) (probeVirtualRouterFakeIPEntry, bool) {
	if entry, ok := currentProbeVirtualRouterFakeIPEntryByIP(ip); ok {
		return entry, true
	}
	if err := refreshProbeVirtualRouterFakeIPLibraryFromController(); err != nil {
		logProbeWarnf("probe virtual router fake ip library refresh failed: fake_ip=%s err=%v", strings.TrimSpace(ip), err)
		return probeVirtualRouterFakeIPEntry{}, false
	}
	return currentProbeVirtualRouterFakeIPEntryByIP(ip)
}

func refreshProbeVirtualRouterFakeIPLibraryFromController() error {
	identity, controllerBaseURL, ok := currentProbeVirtualRouterController()
	if !ok {
		return errors.New("virtual router controller is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeRouteConfigSyncFetchTimeout)
	config, err := fetchProbeRouteConfig(ctx, controllerBaseURL, identity)
	cancel()
	if err != nil {
		return err
	}
	applyProbeVirtualRouterFakeIPLibrary(config.FakeIPLibrary)
	return nil
}

func probeVirtualRouterFakeIPRealIPs(domain string) ([]string, error) {
	cleanDomain := normalizeProbeVirtualRouterDomain(domain)
	if cleanDomain == "" {
		return nil, errors.New("fake ip mapping domain is empty")
	}
	ips, err := resolveProbeVirtualRouterFakeIPExitRealIPs(cleanDomain)
	if err != nil {
		return nil, err
	}
	ips = filterProbeLocalIPv4StringsFromList(ips)
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve fake ip domain returned no usable ipv4: domain=%s", cleanDomain)
	}
	return ips, nil
}

func resolveProbeVirtualRouterFakeIPExitRealIPs(domain string) ([]string, error) {
	cleanDomain := normalizeProbeVirtualRouterDomain(domain)
	if cleanDomain == "" {
		return nil, errors.New("fake ip mapping domain is empty")
	}
	ips, err := probeLocalDNSBootstrapLookupIPv4(cleanDomain)
	if err != nil {
		return nil, fmt.Errorf("resolve fake ip domain failed: domain=%s err=%w", cleanDomain, err)
	}
	ips = filterProbeLocalIPv4StringsFromList(ips)
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve fake ip domain returned no usable ipv4: domain=%s", cleanDomain)
	}
	return ips, nil
}

func dialProbeVirtualRouterExitTCP(targetAddrs []string) (net.Conn, error) {
	var lastErr error
	for _, targetAddr := range probeVirtualRouterExitTargetCandidates(targetAddrs) {
		if err := probeVirtualRouterEnsureDirectBypass(targetAddr); err != nil {
			logProbeWarnf("probe virtual router fake ip tcp exit direct bypass failed: target=%s err=%v", targetAddr, err)
		}
		dialer := applyProbeRouteEgressDialer(&net.Dialer{Timeout: probeVirtualRouterExitDialTimeout})
		conn, err := dialer.Dial(probeRouteEgressDialNetwork("tcp", targetAddr), targetAddr)
		if err != nil {
			lastErr = err
			continue
		}
		tuneProbeRouteNetConn(conn)
		return conn, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("tcp exit target is empty")
}

func dialProbeVirtualRouterExitUDP(targetAddrs []string) (*net.UDPConn, error) {
	var lastErr error
	for _, targetAddr := range probeVirtualRouterExitTargetCandidates(targetAddrs) {
		if err := probeVirtualRouterEnsureDirectBypass(targetAddr); err != nil {
			logProbeWarnf("probe virtual router fake ip udp exit direct bypass failed: target=%s err=%v", targetAddr, err)
		}
		udpAddr, err := net.ResolveUDPAddr(probeRouteEgressDialNetwork("udp", targetAddr), targetAddr)
		if err != nil {
			lastErr = err
			continue
		}
		conn, err := net.DialUDP(probeRouteEgressDialNetwork("udp", targetAddr), nil, udpAddr)
		if err != nil {
			lastErr = err
			continue
		}
		return conn, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("udp exit target is empty")
}

func probeVirtualRouterExitTargetCandidates(targetAddrs []string) []string {
	if len(targetAddrs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(targetAddrs))
	out := make([]string, 0, len(targetAddrs))
	for _, raw := range targetAddrs {
		target := strings.TrimSpace(raw)
		if target == "" {
			continue
		}
		key := strings.ToLower(target)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, target)
	}
	return out
}

func probeVirtualRouterSendFakeIPICMPEcho(request []byte, targetIPs []string) ([]byte, string, error) {
	icmpPayload, err := probeVirtualRouterICMPEchoPayload(request)
	if err != nil {
		return nil, "", err
	}
	var lastErr error
	for _, targetIP := range probeVirtualRouterExitIPCandidates(targetIPs) {
		if err := probeVirtualRouterEnsureDirectBypass(net.JoinHostPort(targetIP, "0")); err != nil {
			logProbeWarnf("probe virtual router fake ip icmp exit direct bypass failed: target=%s err=%v", targetIP, err)
		}
		replyPayload, err := probeVirtualRouterSendICMPEcho(targetIP, icmpPayload, probeVirtualRouterExitICMPTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		reply, err := buildProbeVirtualRouterFakeIPICMPEchoReplyPacket(request, replyPayload)
		if err != nil {
			lastErr = err
			continue
		}
		return reply, targetIP, nil
	}
	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", errors.New("icmp exit target is empty")
}

func sendProbeVirtualRouterICMPEcho(targetIP string, icmpPayload []byte, timeout time.Duration) ([]byte, error) {
	target := net.ParseIP(strings.TrimSpace(targetIP)).To4()
	if target == nil {
		return nil, fmt.Errorf("invalid icmp target ip: %s", targetIP)
	}
	if len(icmpPayload) < 8 || icmpPayload[0] != 8 || icmpPayload[1] != 0 {
		return nil, errors.New("icmp echo request payload is invalid")
	}
	conn, err := net.DialTimeout("ip4:icmp", target.String(), timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(icmpPayload); err != nil {
		return nil, err
	}
	wantID := binary.BigEndian.Uint16(icmpPayload[4:6])
	wantSeq := binary.BigEndian.Uint16(icmpPayload[6:8])
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		reply := normalizeProbeVirtualRouterICMPPayload(buf[:n])
		if len(reply) < 8 || reply[0] != 0 || reply[1] != 0 {
			continue
		}
		if binary.BigEndian.Uint16(reply[4:6]) != wantID || binary.BigEndian.Uint16(reply[6:8]) != wantSeq {
			continue
		}
		return append([]byte(nil), reply...), nil
	}
}

func probeVirtualRouterICMPEchoPayload(packet []byte) ([]byte, error) {
	if len(packet) < 28 || packet[0]>>4 != 4 {
		return nil, errors.New("icmp packet is not ipv4")
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl+8 {
		return nil, errors.New("icmp packet header is invalid")
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl+8 {
		return nil, errors.New("icmp packet length is invalid")
	}
	if packet[9] != 1 {
		return nil, errors.New("packet is not icmp")
	}
	icmp := append([]byte(nil), packet[ihl:totalLen]...)
	if len(icmp) < 8 || icmp[0] != 8 || icmp[1] != 0 {
		return nil, errors.New("icmp packet is not echo request")
	}
	return icmp, nil
}

func buildProbeVirtualRouterFakeIPICMPEchoReplyPacket(request []byte, replyPayload []byte) ([]byte, error) {
	info, ok := probeVirtualRouterParseICMPEchoLogInfo(request)
	if !ok || info.Kind != "echo_request" {
		return nil, errors.New("request is not icmp echo request")
	}
	replyICMP := append([]byte(nil), normalizeProbeVirtualRouterICMPPayload(replyPayload)...)
	if len(replyICMP) < 8 || replyICMP[0] != 0 || replyICMP[1] != 0 {
		return nil, errors.New("reply is not icmp echo reply")
	}
	if binary.BigEndian.Uint16(replyICMP[4:6]) != info.ID || binary.BigEndian.Uint16(replyICMP[6:8]) != info.Sequence {
		return nil, errors.New("icmp echo reply id or sequence mismatch")
	}
	fakeIP := net.ParseIP(info.DestinationIP).To4()
	dstIP := net.ParseIP(info.SourceIP).To4()
	if fakeIP == nil || dstIP == nil {
		return nil, errors.New("icmp reply address is invalid")
	}
	replyICMP[2], replyICMP[3] = 0, 0
	binary.BigEndian.PutUint16(replyICMP[2:4], probeVirtualRouterChecksum(replyICMP))

	packet := make([]byte, 20+len(replyICMP))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	binary.BigEndian.PutUint16(packet[4:6], binary.BigEndian.Uint16(request[4:6]))
	packet[8] = 64
	packet[9] = 1
	copy(packet[12:16], fakeIP)
	copy(packet[16:20], dstIP)
	copy(packet[20:], replyICMP)
	binary.BigEndian.PutUint16(packet[10:12], probeVirtualRouterChecksum(packet[:20]))
	return packet, nil
}

func normalizeProbeVirtualRouterICMPPayload(packet []byte) []byte {
	if len(packet) >= 28 && packet[0]>>4 == 4 {
		ihl := int(packet[0]&0x0F) * 4
		totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
		if ihl >= 20 && totalLen >= ihl+8 && totalLen <= len(packet) && packet[9] == 1 {
			return packet[ihl:totalLen]
		}
	}
	return packet
}

func probeVirtualRouterExitIPCandidates(ips []string) []string {
	if len(ips) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ips))
	out := make([]string, 0, len(ips))
	for _, raw := range ips {
		ip := net.ParseIP(strings.TrimSpace(raw)).To4()
		if ip == nil {
			continue
		}
		target := ip.String()
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		out = append(out, target)
	}
	return out
}

func pipeProbeVirtualRouterExitConn(dst net.Conn, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

func relayProbeVirtualRouterExitUDP(inbound *gonet.UDPConn, outbound *net.UDPConn) {
	defer inbound.Close()
	defer outbound.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(outbound, inbound)
		done <- struct{}{}
	}()
	go func() {
		buf := make([]byte, 64*1024)
		for {
			_ = outbound.SetReadDeadline(time.Now().Add(probeVirtualRouterExitUDPIdleTimeout))
			n, err := outbound.Read(buf)
			if n > 0 {
				_, _ = inbound.Write(buf[:n])
			}
			if err != nil {
				done <- struct{}{}
				return
			}
		}
	}()
	<-done
}

func probeVirtualRouterNetstackProtocolFromPacket(packet []byte) (tcpip.NetworkProtocolNumber, error) {
	if len(packet) == 0 {
		return 0, errors.New("empty packet")
	}
	switch packet[0] >> 4 {
	case 4:
		return ipv4.ProtocolNumber, nil
	default:
		return 0, errors.New("unsupported ip version")
	}
}

func tcpipErrorToError(err tcpip.Error) error {
	if err == nil {
		return nil
	}
	return errors.New(err.String())
}
