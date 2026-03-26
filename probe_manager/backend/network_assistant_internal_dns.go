package backend

import (
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	internalDNSListenIPv4        = "198.18.0.1"
	internalDNSListenPort        = 53
	internalDNSReadBufferSize    = 2048
	internalDNSDefaultTTLSeconds = 60
)

type localInternalDNSServer struct {
	service *networkAssistantService
	conn    net.PacketConn

	closeOnce sync.Once
	doneCh    chan struct{}
}

func (s *networkAssistantService) startInternalDNSServer() error {
	s.mu.Lock()
	if s.internalDNS != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	listenAddr := net.JoinHostPort(internalDNSListenIPv4, strconv.Itoa(internalDNSListenPort))
	conn, err := net.ListenPacket("udp4", listenAddr)
	if err != nil {
		return err
	}
	server := &localInternalDNSServer{
		service: s,
		conn:    conn,
		doneCh:  make(chan struct{}),
	}

	s.mu.Lock()
	if s.internalDNS != nil {
		s.mu.Unlock()
		_ = conn.Close()
		return nil
	}
	s.internalDNS = server
	s.mu.Unlock()

	go server.serve()
	s.logf("local internal dns service started: listen=%s", listenAddr)
	return nil
}

func (s *networkAssistantService) stopInternalDNSServer() error {
	s.mu.Lock()
	server := s.internalDNS
	s.internalDNS = nil
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Close()
}

func (d *localInternalDNSServer) Close() error {
	if d == nil {
		return nil
	}
	var closeErr error
	d.closeOnce.Do(func() {
		if d.conn != nil {
			closeErr = d.conn.Close()
		}
		select {
		case <-d.doneCh:
		case <-time.After(2 * time.Second):
		}
	})
	return closeErr
}

func (d *localInternalDNSServer) serve() {
	defer close(d.doneCh)
	buffer := make([]byte, internalDNSReadBufferSize)
	for {
		n, peerAddr, err := d.conn.ReadFrom(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if n <= 0 || peerAddr == nil {
			continue
		}
		packet := append([]byte(nil), buffer[:n]...)
		go d.handlePacket(packet, peerAddr)
	}
}

func (d *localInternalDNSServer) handlePacket(packet []byte, peerAddr net.Addr) {
	queryID, domain, qType, err := parseDNSQueryQuestion(packet)
	if err != nil {
		return
	}

	addrs, ttlSeconds, route, resolveErr := d.service.resolveDomainForInternalDNS(domain, qType)
	if resolveErr == nil && len(addrs) > 0 {
		d.service.storeDNSRouteHint(addrs, route, ttlSeconds)
	}

	var response []byte
	if resolveErr != nil || len(addrs) == 0 {
		response, err = buildDNSErrorResponse(queryID, domain, qType, 2)
	} else {
		response, err = buildDNSSuccessResponse(queryID, domain, qType, addrs, ttlSeconds)
	}
	if err != nil || len(response) == 0 {
		return
	}
	_, _ = d.conn.WriteTo(response, peerAddr)
}

func (s *networkAssistantService) resolveDomainForInternalDNS(domain string, qType uint16) ([]string, int, tunnelRouteDecision, error) {
	normalizedDomain := normalizeRuleDomain(domain)
	if normalizedDomain == "" {
		return nil, 0, tunnelRouteDecision{}, errors.New("invalid domain")
	}
	route, err := s.decideRouteForTarget(net.JoinHostPort(normalizedDomain, "53"))
	if err != nil {
		return nil, 0, tunnelRouteDecision{}, err
	}
	cacheKey := buildInternalDNSCacheKey(route, normalizedDomain, qType)
	if cached, ok := s.loadRuleDNSCache(cacheKey); ok && len(cached) > 0 {
		return filterDNSResponseAddrs(cached, qType), clampRuleDNSTTL(internalDNSDefaultTTLSeconds), route, nil
	}

	var addrs []string
	var ttl int
	if route.Direct {
		addrs, ttl, err = s.queryRuleDomainViaSystemDNS(normalizedDomain, qType)
	} else {
		addrs, ttl, err = s.queryRuleDomainViaTunnel(route.NodeID, normalizedDomain, qType)
	}
	if err != nil {
		return nil, 0, tunnelRouteDecision{}, err
	}
	if len(addrs) == 0 {
		return nil, 0, tunnelRouteDecision{}, errors.New("dns resolve returned empty result")
	}
	ttl = clampRuleDNSTTL(ttl)
	s.storeRuleDNSCache(cacheKey, addrs, ttl)
	return filterDNSResponseAddrs(addrs, qType), ttl, route, nil
}

func buildInternalDNSCacheKey(route tunnelRouteDecision, domain string, qType uint16) string {
	nodeKey := "direct"
	if !route.Direct {
		nodeKey = strings.TrimSpace(route.NodeID)
		if nodeKey == "" {
			nodeKey = defaultNodeID
		}
	}
	return strings.ToLower(nodeKey) + "|" + strconv.Itoa(int(qType)) + "|" + strings.ToLower(strings.TrimSpace(domain))
}

func (s *networkAssistantService) queryRuleDomainViaSystemDNS(domain string, qType uint16) ([]string, int, error) {
	normalizedDomain := normalizeRuleDomain(domain)
	if normalizedDomain == "" {
		return nil, 0, errors.New("invalid domain")
	}
	queryID := uint16(atomic.AddUint32(&s.ruleDNSQuerySeq, 1))
	packet, err := buildDNSQueryPacket(normalizedDomain, qType, queryID)
	if err != nil {
		return nil, 0, err
	}
	dnsConfig, configErr := getDNSUpstreamConfig()
	if configErr != nil {
		s.logfRateLimited("dns-upstream-config-load", 30*time.Second, "load dns upstream config failed, fallback to defaults: %v", configErr)
	}

	deadline := time.Now().Add(ruleDNSResolveTimeout)
	var lastErr error

	tryParseResponse := func(payload []byte) ([]string, int, error) {
		addrs, ttlSeconds, parseErr := parseDNSResponseAddrs(payload, queryID, qType)
		if parseErr != nil {
			return nil, 0, parseErr
		}
		if len(addrs) == 0 {
			return nil, 0, errors.New("dns resolve returned empty result")
		}
		return addrs, ttlSeconds, nil
	}

	queryByDoH := func() ([]string, int, bool) {
		trials := 0
		for _, server := range dnsConfig.DoHServers {
			if trials >= ruleDNSResolveServerTrials {
				break
			}
			trials++
			remaining := time.Until(deadline)
			if remaining <= 0 {
				lastErr = errors.New("dns resolve timeout")
				return nil, 0, false
			}
			payload, queryErr := s.queryRawDNSPacketViaDoH(server, packet, remaining, dnsConfig.DNSServers)
			if queryErr != nil {
				lastErr = queryErr
				continue
			}
			addrs, ttlSeconds, parseErr := tryParseResponse(payload)
			if parseErr != nil {
				lastErr = parseErr
				continue
			}
			return addrs, ttlSeconds, true
		}
		return nil, 0, false
	}

	queryByDoT := func() ([]string, int, bool) {
		trials := 0
		for _, server := range dnsConfig.DoTServers {
			if trials >= ruleDNSResolveServerTrials {
				break
			}
			trials++
			remaining := time.Until(deadline)
			if remaining <= 0 {
				lastErr = errors.New("dns resolve timeout")
				return nil, 0, false
			}
			payload, queryErr := s.queryRawDNSPacketViaDoT(server, packet, remaining, dnsConfig.DNSServers)
			if queryErr != nil {
				lastErr = queryErr
				continue
			}
			addrs, ttlSeconds, parseErr := tryParseResponse(payload)
			if parseErr != nil {
				lastErr = parseErr
				continue
			}
			return addrs, ttlSeconds, true
		}
		return nil, 0, false
	}

	queryByPlainDNS := func() ([]string, int, bool) {
		trials := 0
		for _, server := range dnsConfig.DNSServers {
			if trials >= ruleDNSResolveServerTrials {
				break
			}
			trials++
			remaining := time.Until(deadline)
			if remaining <= 0 {
				lastErr = errors.New("dns resolve timeout")
				return nil, 0, false
			}
			payload, queryErr := queryRawDNSPacket(server.Address, packet, remaining)
			if queryErr != nil {
				lastErr = queryErr
				continue
			}
			addrs, ttlSeconds, parseErr := tryParseResponse(payload)
			if parseErr != nil {
				lastErr = parseErr
				continue
			}
			return addrs, ttlSeconds, true
		}
		return nil, 0, false
	}

	for _, tier := range buildDNSUpstreamQueryOrder(dnsConfig.Prefer) {
		switch tier {
		case "doh":
			if addrs, ttlSeconds, ok := queryByDoH(); ok {
				return addrs, ttlSeconds, nil
			}
		case "dot":
			if addrs, ttlSeconds, ok := queryByDoT(); ok {
				return addrs, ttlSeconds, nil
			}
		default:
			if addrs, ttlSeconds, ok := queryByPlainDNS(); ok {
				return addrs, ttlSeconds, nil
			}
		}
	}

	if lastErr != nil {
		return nil, 0, lastErr
	}
	return nil, 0, errors.New("dns resolvers are not configured")
}

func queryRawDNSPacket(serverAddr string, packet []byte, timeout time.Duration) ([]byte, error) {
	conn, err := net.DialTimeout("udp", serverAddr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(packet); err != nil {
		return nil, err
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, errors.New("dns resolve returned empty payload")
	}
	return append([]byte(nil), buf[:n]...), nil
}

func parseDNSQueryQuestion(payload []byte) (uint16, string, uint16, error) {
	if len(payload) < 12 {
		return 0, "", 0, errors.New("dns query too short")
	}
	queryID := binary.BigEndian.Uint16(payload[0:2])
	flags := binary.BigEndian.Uint16(payload[2:4])
	if flags&0x8000 != 0 {
		return 0, "", 0, errors.New("dns packet is not a query")
	}
	questionCount := int(binary.BigEndian.Uint16(payload[4:6]))
	if questionCount <= 0 {
		return 0, "", 0, errors.New("dns query has no question")
	}
	domain, offset, err := decodeDNSQueryName(payload, 12)
	if err != nil {
		return 0, "", 0, err
	}
	if offset+4 > len(payload) {
		return 0, "", 0, errors.New("dns question truncated")
	}
	qType := binary.BigEndian.Uint16(payload[offset : offset+2])
	qClass := binary.BigEndian.Uint16(payload[offset+2 : offset+4])
	if qClass != 1 {
		return 0, "", 0, errors.New("unsupported dns class")
	}
	if qType != 1 && qType != 28 {
		return 0, "", 0, errors.New("unsupported dns query type")
	}
	return queryID, normalizeRuleDomain(domain), qType, nil
}

func decodeDNSQueryName(payload []byte, offset int) (string, int, error) {
	if offset < 0 || offset >= len(payload) {
		return "", 0, errors.New("invalid dns query name offset")
	}
	labels := make([]string, 0, 4)
	for {
		if offset >= len(payload) {
			return "", 0, errors.New("dns query name truncated")
		}
		labelLen := int(payload[offset])
		offset++
		if labelLen == 0 {
			break
		}
		if labelLen > 63 || offset+labelLen > len(payload) {
			return "", 0, errors.New("invalid dns label")
		}
		labels = append(labels, string(payload[offset:offset+labelLen]))
		offset += labelLen
	}
	domain := strings.TrimSpace(strings.Join(labels, "."))
	if normalizeRuleDomain(domain) == "" {
		return "", 0, errors.New("invalid dns query domain")
	}
	return domain, offset, nil
}

func buildDNSSuccessResponse(queryID uint16, domain string, qType uint16, addrs []string, ttlSeconds int) ([]byte, error) {
	questionName, err := encodeDNSName(domain)
	if err != nil {
		return nil, err
	}
	addresses := filterDNSResponseAddrs(addrs, qType)
	if len(addresses) == 0 {
		return buildDNSErrorResponse(queryID, domain, qType, 3)
	}
	ttl := uint32(clampRuleDNSTTL(ttlSeconds))
	answerSize := 0
	for _, addr := range addresses {
		ipValue := net.ParseIP(strings.TrimSpace(addr))
		if ipValue == nil {
			continue
		}
		if qType == 1 {
			answerSize += 2 + 2 + 2 + 4 + 2 + 4
		} else {
			answerSize += 2 + 2 + 2 + 4 + 2 + 16
		}
	}
	if answerSize == 0 {
		return buildDNSErrorResponse(queryID, domain, qType, 3)
	}

	question := make([]byte, len(questionName)+4)
	copy(question, questionName)
	questionOffset := len(questionName)
	binary.BigEndian.PutUint16(question[questionOffset:questionOffset+2], qType)
	binary.BigEndian.PutUint16(question[questionOffset+2:questionOffset+4], 1)

	packet := make([]byte, 12+len(question)+answerSize)
	binary.BigEndian.PutUint16(packet[0:2], queryID)
	binary.BigEndian.PutUint16(packet[2:4], 0x8180)
	binary.BigEndian.PutUint16(packet[4:6], 1)
	binary.BigEndian.PutUint16(packet[6:8], uint16(len(addresses)))
	copy(packet[12:], question)

	offset := 12 + len(question)
	for _, addr := range addresses {
		ipValue := net.ParseIP(strings.TrimSpace(addr))
		if ipValue == nil {
			continue
		}
		binary.BigEndian.PutUint16(packet[offset:offset+2], 0xC00C)
		offset += 2
		binary.BigEndian.PutUint16(packet[offset:offset+2], qType)
		offset += 2
		binary.BigEndian.PutUint16(packet[offset:offset+2], 1)
		offset += 2
		binary.BigEndian.PutUint32(packet[offset:offset+4], ttl)
		offset += 4
		if qType == 1 {
			ipv4 := ipValue.To4()
			if ipv4 == nil {
				continue
			}
			binary.BigEndian.PutUint16(packet[offset:offset+2], 4)
			offset += 2
			copy(packet[offset:offset+4], ipv4)
			offset += 4
		} else {
			ipv6 := ipValue.To16()
			if ipv6 == nil {
				continue
			}
			binary.BigEndian.PutUint16(packet[offset:offset+2], 16)
			offset += 2
			copy(packet[offset:offset+16], ipv6)
			offset += 16
		}
	}
	return packet[:offset], nil
}

func buildDNSErrorResponse(queryID uint16, domain string, qType uint16, rcode uint16) ([]byte, error) {
	questionName, err := encodeDNSName(domain)
	if err != nil {
		return nil, err
	}
	question := make([]byte, len(questionName)+4)
	copy(question, questionName)
	questionOffset := len(questionName)
	binary.BigEndian.PutUint16(question[questionOffset:questionOffset+2], qType)
	binary.BigEndian.PutUint16(question[questionOffset+2:questionOffset+4], 1)

	packet := make([]byte, 12+len(question))
	binary.BigEndian.PutUint16(packet[0:2], queryID)
	binary.BigEndian.PutUint16(packet[2:4], 0x8180|(rcode&0x000F))
	binary.BigEndian.PutUint16(packet[4:6], 1)
	binary.BigEndian.PutUint16(packet[6:8], 0)
	copy(packet[12:], question)
	return packet, nil
}

func filterDNSResponseAddrs(addrs []string, qType uint16) []string {
	out := make([]string, 0, len(addrs))
	seen := make(map[string]struct{}, len(addrs))
	for _, rawAddr := range addrs {
		ipValue := net.ParseIP(strings.TrimSpace(rawAddr))
		if ipValue == nil {
			continue
		}
		if qType == 1 && ipValue.To4() == nil {
			continue
		}
		if qType == 28 && ipValue.To4() != nil {
			continue
		}
		canonical := canonicalIP(ipValue)
		if canonical == "" {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	return out
}

func (s *networkAssistantService) storeDNSRouteHint(addrs []string, route tunnelRouteDecision, ttlSeconds int) {
	expiresAt := time.Now().Add(time.Duration(clampRuleDNSTTL(ttlSeconds)) * time.Second)
	hint := dnsRouteHintEntry{
		Direct:  route.Direct,
		NodeID:  strings.TrimSpace(route.NodeID),
		Group:   strings.TrimSpace(route.Group),
		Expires: expiresAt,
	}

	s.mu.Lock()
	if s.dnsRouteHints == nil {
		s.dnsRouteHints = make(map[string]dnsRouteHintEntry)
	}
	for _, rawAddr := range addrs {
		ipValue := net.ParseIP(strings.TrimSpace(rawAddr))
		if ipValue == nil {
			continue
		}
		s.dnsRouteHints[canonicalIP(ipValue)] = hint
	}
	s.mu.Unlock()
}

func (s *networkAssistantService) loadDNSRouteHint(ipAddr string) (dnsRouteHintEntry, bool) {
	canonical := canonicalIP(net.ParseIP(strings.TrimSpace(ipAddr)))
	if canonical == "" {
		return dnsRouteHintEntry{}, false
	}

	s.mu.RLock()
	hint, ok := s.dnsRouteHints[canonical]
	s.mu.RUnlock()
	if !ok {
		return dnsRouteHintEntry{}, false
	}
	if time.Now().After(hint.Expires) {
		s.mu.Lock()
		if latest, exists := s.dnsRouteHints[canonical]; exists && time.Now().After(latest.Expires) {
			delete(s.dnsRouteHints, canonical)
		}
		s.mu.Unlock()
		return dnsRouteHintEntry{}, false
	}
	return hint, true
}

func (s *networkAssistantService) clearDNSRouteHints() {
	s.mu.Lock()
	s.dnsRouteHints = make(map[string]dnsRouteHintEntry)
	s.mu.Unlock()
}
