package backend

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type localTUNUDPPacket struct {
	SrcIP   net.IP
	DstIP   net.IP
	SrcPort uint16
	DstPort uint16
	Payload []byte
}

type localTUNUDPRelay struct {
	service *networkAssistantService
	key     string

	srcIP   net.IP
	dstIP   net.IP
	srcPort uint16
	dstPort uint16

	routeTarget string
	routeNodeID string
	routeGroup  string

	directConn   *net.UDPConn
	tunnelStream *tunnelMuxStream

	closeOnce sync.Once
	writeMu   sync.Mutex
	closed    atomic.Bool
}

func (s *networkAssistantService) handleLocalTUNPacket(packet []byte) {
	s.mu.RLock()
	packetStack := s.tunPacketStack
	s.mu.RUnlock()
	if packetStack != nil {
		if _, err := packetStack.Write(packet); err == nil {
			return
		}
	}

	frame, err := parseLocalTUNUDPPacket(packet)
	if err != nil {
		return
	}
	relay, err := s.getOrCreateLocalTUNUDPRelay(frame)
	if err != nil {
		s.logf("local tun udp relay create failed: src=%s:%d dst=%s:%d err=%v", frame.SrcIP.String(), frame.SrcPort, frame.DstIP.String(), frame.DstPort, err)
		return
	}
	if err := relay.send(frame.Payload); err != nil {
		s.logf("local tun udp relay write failed: target=%s node=%s group=%s err=%v", relay.routeTarget, relay.routeNodeID, relay.routeGroup, err)
		relay.close()
	}
}

func (s *networkAssistantService) getOrCreateLocalTUNUDPRelay(frame localTUNUDPPacket) (*localTUNUDPRelay, error) {
	key := buildLocalTUNUDPRelayKey(frame)

	s.mu.Lock()
	if s.tunUDPRelays == nil {
		s.tunUDPRelays = make(map[string]*localTUNUDPRelay)
	}
	if existing, ok := s.tunUDPRelays[key]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	s.mu.Unlock()

	targetAddr := net.JoinHostPort(frame.DstIP.String(), strconv.Itoa(int(frame.DstPort)))
	route, err := s.decideRouteForTarget(targetAddr)
	if err != nil {
		return nil, err
	}

	relay := &localTUNUDPRelay{
		service:     s,
		key:         key,
		srcIP:       append(net.IP(nil), frame.SrcIP...),
		dstIP:       append(net.IP(nil), frame.DstIP...),
		srcPort:     frame.SrcPort,
		dstPort:     frame.DstPort,
		routeTarget: route.TargetAddr,
		routeNodeID: route.NodeID,
		routeGroup:  route.Group,
	}

	if route.Direct {
		dialer := s.newCachedDNSDialContext(&net.Dialer{Timeout: 30 * time.Second})
		conn, dialErr := dialer(context.Background(), "udp", route.TargetAddr)
		if dialErr != nil {
			return nil, dialErr
		}
		udpConn, ok := conn.(*net.UDPConn)
		if !ok {
			return nil, errors.New("failed to cast to net.UDPConn")
		}
		relay.directConn = udpConn
	} else {
		stream, openErr := s.openTunnelStreamForNode("udp", route.TargetAddr, route.NodeID)
		if openErr != nil {
			return nil, openErr
		}
		relay.tunnelStream = stream
	}

	s.mu.Lock()
	if existing, ok := s.tunUDPRelays[key]; ok {
		s.mu.Unlock()
		relay.close()
		return existing, nil
	}
	s.tunUDPRelays[key] = relay
	s.mu.Unlock()

	relay.startReadLoop()
	s.logf(
		"local tun udp relay created: src=%s:%d dst=%s:%d target=%s direct=%v node=%s group=%s",
		frame.SrcIP.String(),
		frame.SrcPort,
		frame.DstIP.String(),
		frame.DstPort,
		relay.routeTarget,
		route.Direct,
		relay.routeNodeID,
		relay.routeGroup,
	)
	return relay, nil
}

func (s *networkAssistantService) closeAllLocalTUNUDPRelays() {
	s.mu.Lock()
	if len(s.tunUDPRelays) == 0 {
		s.mu.Unlock()
		return
	}
	relays := make([]*localTUNUDPRelay, 0, len(s.tunUDPRelays))
	for _, relay := range s.tunUDPRelays {
		if relay != nil {
			relays = append(relays, relay)
		}
	}
	s.tunUDPRelays = make(map[string]*localTUNUDPRelay)
	s.mu.Unlock()

	for _, relay := range relays {
		relay.close()
	}
}

func (s *networkAssistantService) removeLocalTUNUDPRelay(key string, relay *localTUNUDPRelay) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.tunUDPRelays[key]
	if !ok {
		return
	}
	if current == relay {
		delete(s.tunUDPRelays, key)
	}
}

func (r *localTUNUDPRelay) startReadLoop() {
	if r == nil {
		return
	}
	if r.directConn != nil {
		go r.readDirectLoop()
		return
	}
	if r.tunnelStream != nil {
		go r.readTunnelLoop()
	}
}

func (r *localTUNUDPRelay) send(payload []byte) error {
	if r == nil {
		return errors.New("nil local tun udp relay")
	}
	if len(payload) == 0 {
		return nil
	}
	if r.closed.Load() {
		return errors.New("local tun udp relay is closed")
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if r.closed.Load() {
		return errors.New("local tun udp relay is closed")
	}
	if r.directConn != nil {
		_, err := r.directConn.Write(payload)
		return err
	}
	if r.tunnelStream != nil {
		return r.tunnelStream.write(payload)
	}
	return errors.New("local tun udp relay has no transport")
}

func (r *localTUNUDPRelay) readDirectLoop() {
	buf := make([]byte, 65535)
	for {
		n, err := r.directConn.Read(buf)
		if n > 0 {
			payload := append([]byte(nil), buf[:n]...)
			r.service.injectLocalTUNUDPResponse(r, payload)
		}
		if err != nil {
			r.close()
			return
		}
	}
}

func (r *localTUNUDPRelay) readTunnelLoop() {
	for {
		select {
		case payload := <-r.tunnelStream.readCh:
			if len(payload) == 0 {
				continue
			}
			r.service.injectLocalTUNUDPResponse(r, payload)
		case <-r.tunnelStream.errCh:
			r.close()
			return
		}
	}
}

func (r *localTUNUDPRelay) close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		if r.directConn != nil {
			_ = r.directConn.Close()
		}
		if r.tunnelStream != nil {
			r.tunnelStream.close()
		}
		if r.service != nil {
			r.service.removeLocalTUNUDPRelay(r.key, r)
		}
	})
}

func (s *networkAssistantService) injectLocalTUNUDPResponse(relay *localTUNUDPRelay, payload []byte) {
	if relay == nil || len(payload) == 0 {
		return
	}
	packet, err := buildLocalTUNUDPPacket(
		relay.dstIP,
		relay.srcIP,
		relay.dstPort,
		relay.srcPort,
		payload,
		uint16(atomic.AddUint32(&s.tunIPIDSeq, 1)),
	)
	if err != nil {
		s.logf("build local tun udp response packet failed: %v", err)
		return
	}

	s.mu.RLock()
	dataPlane := s.tunDataPlane
	s.mu.RUnlock()
	if dataPlane == nil {
		return
	}
	if err := dataPlane.WritePacket(packet); err != nil {
		s.logf("local tun write packet failed: %v", err)
		relay.close()
	}
}

func buildLocalTUNUDPRelayKey(frame localTUNUDPPacket) string {
	return frame.SrcIP.String() + ":" + strconv.Itoa(int(frame.SrcPort)) + "->" + frame.DstIP.String() + ":" + strconv.Itoa(int(frame.DstPort))
}

func parseLocalTUNUDPPacket(packet []byte) (localTUNUDPPacket, error) {
	if len(packet) < 20 {
		return localTUNUDPPacket{}, errors.New("packet too short")
	}
	version := packet[0] >> 4
	if version != 4 {
		return localTUNUDPPacket{}, errors.New("not ipv4 packet")
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl+8 {
		return localTUNUDPPacket{}, errors.New("invalid ipv4 header length")
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) {
		totalLen = len(packet)
	}
	if packet[9] != 17 {
		return localTUNUDPPacket{}, errors.New("not udp packet")
	}
	flagsFragment := binary.BigEndian.Uint16(packet[6:8])
	if flagsFragment&0x1FFF != 0 {
		return localTUNUDPPacket{}, errors.New("fragmented ipv4 packet is not supported")
	}

	srcIP := net.IP(packet[12:16]).To4()
	dstIP := net.IP(packet[16:20]).To4()
	if srcIP == nil || dstIP == nil {
		return localTUNUDPPacket{}, errors.New("invalid ipv4 addresses")
	}

	udpStart := ihl
	udpLen := int(binary.BigEndian.Uint16(packet[udpStart+4 : udpStart+6]))
	if udpLen < 8 || udpStart+udpLen > totalLen {
		return localTUNUDPPacket{}, errors.New("invalid udp length")
	}
	payloadStart := udpStart + 8
	payloadEnd := udpStart + udpLen

	return localTUNUDPPacket{
		SrcIP:   append(net.IP(nil), srcIP...),
		DstIP:   append(net.IP(nil), dstIP...),
		SrcPort: binary.BigEndian.Uint16(packet[udpStart : udpStart+2]),
		DstPort: binary.BigEndian.Uint16(packet[udpStart+2 : udpStart+4]),
		Payload: append([]byte(nil), packet[payloadStart:payloadEnd]...),
	}, nil
}

func buildLocalTUNUDPPacket(srcIP net.IP, dstIP net.IP, srcPort uint16, dstPort uint16, payload []byte, ipID uint16) ([]byte, error) {
	src4 := srcIP.To4()
	dst4 := dstIP.To4()
	if src4 == nil || dst4 == nil {
		return nil, errors.New("only ipv4 is supported")
	}
	udpLen := 8 + len(payload)
	totalLen := 20 + udpLen
	if totalLen > 0xFFFF {
		return nil, errors.New("packet too large")
	}

	packet := make([]byte, totalLen)
	packet[0] = 0x45
	packet[1] = 0x00
	binary.BigEndian.PutUint16(packet[2:4], uint16(totalLen))
	binary.BigEndian.PutUint16(packet[4:6], ipID)
	binary.BigEndian.PutUint16(packet[6:8], 0x0000)
	packet[8] = 64
	packet[9] = 17
	copy(packet[12:16], src4)
	copy(packet[16:20], dst4)

	udpStart := 20
	binary.BigEndian.PutUint16(packet[udpStart:udpStart+2], srcPort)
	binary.BigEndian.PutUint16(packet[udpStart+2:udpStart+4], dstPort)
	binary.BigEndian.PutUint16(packet[udpStart+4:udpStart+6], uint16(udpLen))
	copy(packet[udpStart+8:], payload)

	ipChecksum := calculateChecksum(packet[:20])
	binary.BigEndian.PutUint16(packet[10:12], ipChecksum)

	udpChecksum := calculateIPv4UDPChecksum(src4, dst4, packet[udpStart:udpStart+udpLen])
	if udpChecksum == 0 {
		udpChecksum = 0xFFFF
	}
	binary.BigEndian.PutUint16(packet[udpStart+6:udpStart+8], udpChecksum)
	return packet, nil
}

func calculateIPv4UDPChecksum(srcIP net.IP, dstIP net.IP, udpPacket []byte) uint16 {
	pseudoHeader := make([]byte, 12+len(udpPacket))
	copy(pseudoHeader[0:4], srcIP.To4())
	copy(pseudoHeader[4:8], dstIP.To4())
	pseudoHeader[8] = 0
	pseudoHeader[9] = 17
	binary.BigEndian.PutUint16(pseudoHeader[10:12], uint16(len(udpPacket)))
	copy(pseudoHeader[12:], udpPacket)
	return calculateChecksum(pseudoHeader)
}

func calculateChecksum(data []byte) uint16 {
	var sum uint32
	length := len(data)
	for i := 0; i+1 < length; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(data[i : i+2]))
	}
	if length%2 == 1 {
		sum += uint32(data[length-1]) << 8
	}
	for (sum >> 16) != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}
