package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/txthinking/socks5"
)

const (
	probeVRouteProxyUDPAddressMaxBytes = 1024
	probeVRouteProxyUDPReadInterval    = 30 * time.Second
	probeVRouteProxyUDPIdleTTL         = 2 * time.Minute
)

type probeVRouteProxyUDPAssociation struct {
	id           string
	clientIP     string
	clientAddr   *net.UDPAddr
	serverConn   *net.UDPConn
	paths        map[string][]string
	createdAt    time.Time
	lastActiveNS atomic.Int64
	done         chan struct{}
	closeOnce    sync.Once
	mu           sync.Mutex
}

type probeVRouteProxyUDPExitSession struct {
	key           string
	associationID string
	targetAddr    string
	responsePath  []string
	conn          net.Conn
	createdAt     time.Time
	lastActiveNS  atomic.Int64
	done          chan struct{}
	closeOnce     sync.Once
}

var probeVRouteProxyUDPState = struct {
	mu           sync.RWMutex
	associations map[string]*probeVRouteProxyUDPAssociation
	exitSessions map[string]*probeVRouteProxyUDPExitSession
	opened       atomic.Uint64
	closed       atomic.Uint64
	txDatagrams  atomic.Uint64
	rxDatagrams  atomic.Uint64
	txBytes      atomic.Uint64
	rxBytes      atomic.Uint64
}{
	associations: make(map[string]*probeVRouteProxyUDPAssociation),
	exitSessions: make(map[string]*probeVRouteProxyUDPExitSession),
}

var probeVRouteProxyExitUDPDial = dialProbeVRouteProxyExitUDP

func registerProbeVRouteProxyUDPAssociation(clientIP string, serverConn *net.UDPConn) (*probeVRouteProxyUDPAssociation, error) {
	if serverConn == nil {
		return nil, errors.New("socks5 udp listener is unavailable")
	}
	id, err := newProbeVRouteProxySessionID()
	if err != nil {
		return nil, err
	}
	association := &probeVRouteProxyUDPAssociation{
		id:         id,
		clientIP:   normalizeProbeVRouteProxyClientIP(clientIP),
		serverConn: serverConn,
		paths:      make(map[string][]string),
		createdAt:  time.Now(),
		done:       make(chan struct{}),
	}
	association.touch()
	probeVRouteProxyUDPState.mu.Lock()
	probeVRouteProxyUDPState.associations[id] = association
	probeVRouteProxyUDPState.mu.Unlock()
	probeVRouteProxyUDPState.opened.Add(1)
	return association, nil
}

func findProbeVRouteProxyUDPAssociation(clientAddr *net.UDPAddr) *probeVRouteProxyUDPAssociation {
	if clientAddr == nil {
		return nil
	}
	clientIP := normalizeProbeVRouteProxyClientIP(clientAddr.IP.String())
	probeVRouteProxyUDPState.mu.RLock()
	items := make([]*probeVRouteProxyUDPAssociation, 0)
	for _, association := range probeVRouteProxyUDPState.associations {
		if association != nil && association.clientIP == clientIP {
			items = append(items, association)
		}
	}
	probeVRouteProxyUDPState.mu.RUnlock()
	var selected *probeVRouteProxyUDPAssociation
	for _, association := range items {
		association.mu.Lock()
		bound := association.clientAddr
		if bound != nil && bound.String() == clientAddr.String() {
			association.mu.Unlock()
			return association
		}
		if bound == nil && (selected == nil || association.createdAt.After(selected.createdAt)) {
			selected = association
		}
		association.mu.Unlock()
	}
	if selected != nil {
		selected.mu.Lock()
		if selected.clientAddr == nil {
			selected.clientAddr = cloneProbeVRouteProxyUDPAddr(clientAddr)
		}
		selected.mu.Unlock()
	}
	return selected
}

func relayProbeVRouteProxyUDP(association *probeVRouteProxyUDPAssociation, targetAddr string, data []byte) error {
	if association == nil || len(data) == 0 {
		return errors.New("invalid socks5 udp association payload")
	}
	decision, err := decideProbeVRouteProxyTarget(targetAddr)
	if err != nil {
		return err
	}
	payload, err := marshalProbeVRouteProxyUDPDatagram(association.id, decision.TargetAddr, data)
	if err != nil {
		return err
	}
	association.touch()
	if decision.Direct() {
		go processProbeVRouteProxyUDPRequest(payload, []string{currentProbeVirtualRouterLocalNodeID()})
		return nil
	}
	association.mu.Lock()
	association.paths[strings.Join(decision.Path, ">")] = append([]string(nil), decision.Path...)
	association.mu.Unlock()
	probeVRouteProxyUDPState.txDatagrams.Add(1)
	probeVRouteProxyUDPState.txBytes.Add(uint64(len(data)))
	return forwardProbeVRouteProxyFrame(probeVirtualRouterProxySubTypeUDPRequest, payload, decision.Path)
}

func handleProbeVRouteProxyUDPRequest(payload []byte, path []string) error {
	_, targetAddr, _, err := unmarshalProbeVRouteProxyUDPDatagram(payload)
	if err != nil {
		return err
	}
	if err := authorizeProbeVRouteProxyExitTarget(targetAddr, path); err != nil {
		return err
	}
	go processProbeVRouteProxyUDPRequest(append([]byte(nil), payload...), append([]string(nil), path...))
	return nil
}

func processProbeVRouteProxyUDPRequest(payload []byte, requestPath []string) {
	associationID, targetAddr, data, err := unmarshalProbeVRouteProxyUDPDatagram(payload)
	if err != nil {
		return
	}
	responsePath := probeVirtualRouterReversePath(requestPath)
	if len(requestPath) == 1 {
		responsePath = append([]string(nil), requestPath...)
	}
	session, err := ensureProbeVRouteProxyUDPExitSession(associationID, targetAddr, responsePath)
	if err != nil {
		logProbeWarnf("probe vroute proxy udp exit open failed: association=%s target=%s err=%v", associationID, targetAddr, err)
		return
	}
	if _, err := session.conn.Write(data); err != nil {
		session.close()
		return
	}
	session.touch()
	probeVRouteProxyUDPState.rxDatagrams.Add(1)
	probeVRouteProxyUDPState.rxBytes.Add(uint64(len(data)))
}

func ensureProbeVRouteProxyUDPExitSession(associationID string, targetAddr string, responsePath []string) (*probeVRouteProxyUDPExitSession, error) {
	key := strings.ToLower(strings.TrimSpace(associationID)) + "|" + strings.ToLower(strings.TrimSpace(targetAddr))
	probeVRouteProxyUDPState.mu.RLock()
	current := probeVRouteProxyUDPState.exitSessions[key]
	probeVRouteProxyUDPState.mu.RUnlock()
	if current != nil {
		return current, nil
	}
	conn, err := probeVRouteProxyExitUDPDial(targetAddr)
	if err != nil {
		return nil, err
	}
	session := &probeVRouteProxyUDPExitSession{
		key:           key,
		associationID: strings.ToLower(strings.TrimSpace(associationID)),
		targetAddr:    strings.TrimSpace(targetAddr),
		responsePath:  append([]string(nil), responsePath...),
		conn:          conn,
		createdAt:     time.Now(),
		done:          make(chan struct{}),
	}
	session.touch()
	probeVRouteProxyUDPState.mu.Lock()
	if existing := probeVRouteProxyUDPState.exitSessions[key]; existing != nil {
		probeVRouteProxyUDPState.mu.Unlock()
		_ = conn.Close()
		return existing, nil
	}
	probeVRouteProxyUDPState.exitSessions[key] = session
	probeVRouteProxyUDPState.mu.Unlock()
	go session.readResponses()
	return session, nil
}

func (s *probeVRouteProxyUDPExitSession) readResponses() {
	buffer := make([]byte, 65507)
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(probeVRouteProxyUDPReadInterval))
		n, err := s.conn.Read(buffer)
		if n > 0 {
			remoteAddr := s.targetAddr
			if addr := s.conn.RemoteAddr(); addr != nil {
				remoteAddr = addr.String()
			}
			payload, marshalErr := marshalProbeVRouteProxyUDPDatagram(s.associationID, remoteAddr, buffer[:n])
			if marshalErr != nil {
				s.close()
				return
			}
			if len(s.responsePath) == 1 {
				_ = deliverProbeVRouteProxyUDPResponse(payload)
			} else if sendErr := forwardProbeVRouteProxyFrame(probeVirtualRouterProxySubTypeUDPResponse, payload, s.responsePath); sendErr != nil {
				s.close()
				return
			}
			probeVRouteProxyUDPState.txDatagrams.Add(1)
			probeVRouteProxyUDPState.txBytes.Add(uint64(n))
			s.touch()
		}
		if err == nil {
			continue
		}
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			lastActive := time.Unix(0, s.lastActiveNS.Load())
			if time.Since(lastActive) < probeVRouteProxyUDPIdleTTL {
				continue
			}
		}
		s.close()
		return
	}
}

func handleProbeVRouteProxyUDPResponse(payload []byte) error {
	return deliverProbeVRouteProxyUDPResponse(payload)
}

func deliverProbeVRouteProxyUDPResponse(payload []byte) error {
	associationID, remoteAddr, data, err := unmarshalProbeVRouteProxyUDPDatagram(payload)
	if err != nil {
		return err
	}
	probeVRouteProxyUDPState.mu.RLock()
	association := probeVRouteProxyUDPState.associations[associationID]
	probeVRouteProxyUDPState.mu.RUnlock()
	if association == nil {
		return nil
	}
	association.mu.Lock()
	clientAddr := cloneProbeVRouteProxyUDPAddr(association.clientAddr)
	serverConn := association.serverConn
	association.mu.Unlock()
	if clientAddr == nil || serverConn == nil {
		return nil
	}
	atyp, addr, port, err := socks5.ParseAddress(remoteAddr)
	if err != nil {
		return err
	}
	if atyp == socks5.ATYPDomain && len(addr) > 0 {
		addr = addr[1:]
	}
	datagram := socks5.NewDatagram(atyp, addr, port, data)
	if _, err := serverConn.WriteToUDP(datagram.Bytes(), clientAddr); err != nil {
		return err
	}
	association.touch()
	probeVRouteProxyUDPState.rxDatagrams.Add(1)
	probeVRouteProxyUDPState.rxBytes.Add(uint64(len(data)))
	return nil
}

func handleProbeVRouteProxyUDPClose(payload []byte) error {
	var msg probeVRouteProxyClosePayload
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}
	closeProbeVRouteProxyUDPExitSessions(msg.SessionID)
	return nil
}

func (a *probeVRouteProxyUDPAssociation) close(notify bool) {
	if a == nil {
		return
	}
	a.closeOnce.Do(func() {
		close(a.done)
		probeVRouteProxyUDPState.mu.Lock()
		if probeVRouteProxyUDPState.associations[a.id] == a {
			delete(probeVRouteProxyUDPState.associations, a.id)
		}
		probeVRouteProxyUDPState.mu.Unlock()
		probeVRouteProxyUDPState.closed.Add(1)
		if notify {
			a.mu.Lock()
			paths := make([][]string, 0, len(a.paths))
			for _, path := range a.paths {
				paths = append(paths, append([]string(nil), path...))
			}
			a.mu.Unlock()
			payload, _ := marshalProbeVRouteProxyJSON(probeVRouteProxyClosePayload{SessionID: a.id})
			for _, path := range paths {
				_ = forwardProbeVRouteProxyFrame(probeVirtualRouterProxySubTypeUDPClose, payload, path)
			}
		}
		closeProbeVRouteProxyUDPExitSessions(a.id)
	})
}

func (a *probeVRouteProxyUDPAssociation) touch() {
	if a != nil {
		a.lastActiveNS.Store(time.Now().UnixNano())
	}
}

func (s *probeVRouteProxyUDPExitSession) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.conn.Close()
		probeVRouteProxyUDPState.mu.Lock()
		if probeVRouteProxyUDPState.exitSessions[s.key] == s {
			delete(probeVRouteProxyUDPState.exitSessions, s.key)
		}
		probeVRouteProxyUDPState.mu.Unlock()
	})
}

func (s *probeVRouteProxyUDPExitSession) touch() {
	if s != nil {
		s.lastActiveNS.Store(time.Now().UnixNano())
	}
}

func closeProbeVRouteProxyUDPExitSessions(associationID string) {
	cleanID := strings.ToLower(strings.TrimSpace(associationID))
	probeVRouteProxyUDPState.mu.RLock()
	items := make([]*probeVRouteProxyUDPExitSession, 0)
	for _, session := range probeVRouteProxyUDPState.exitSessions {
		if session != nil && session.associationID == cleanID {
			items = append(items, session)
		}
	}
	probeVRouteProxyUDPState.mu.RUnlock()
	for _, session := range items {
		session.close()
	}
}

func closeProbeVRouteProxyUDPSessionsForEdge(fromNodeID string, toNodeID string) {
	probeVRouteProxyUDPState.mu.RLock()
	associations := make([]*probeVRouteProxyUDPAssociation, 0)
	for _, association := range probeVRouteProxyUDPState.associations {
		association.mu.Lock()
		matched := false
		for _, path := range association.paths {
			if probeVirtualRouterPathContainsAdjacentEdge(path, fromNodeID, toNodeID) {
				matched = true
				break
			}
		}
		association.mu.Unlock()
		if matched {
			associations = append(associations, association)
		}
	}
	exitSessions := make([]*probeVRouteProxyUDPExitSession, 0)
	for _, session := range probeVRouteProxyUDPState.exitSessions {
		if probeVirtualRouterPathContainsAdjacentEdge(session.responsePath, fromNodeID, toNodeID) {
			exitSessions = append(exitSessions, session)
		}
	}
	probeVRouteProxyUDPState.mu.RUnlock()
	for _, association := range associations {
		association.close(false)
	}
	for _, session := range exitSessions {
		session.close()
	}
}

func marshalProbeVRouteProxyUDPDatagram(associationID string, targetAddr string, data []byte) ([]byte, error) {
	id, err := decodeProbeVRouteProxySessionID(associationID)
	if err != nil {
		return nil, err
	}
	target := strings.TrimSpace(targetAddr)
	if target == "" || len(target) > probeVRouteProxyUDPAddressMaxBytes {
		return nil, errors.New("invalid vroute proxy udp target")
	}
	if len(data) == 0 {
		return nil, errors.New("vroute proxy udp data is empty")
	}
	headerSize := probeVRouteProxySessionIDBytes + 2 + len(target)
	if headerSize+len(data) > probeVirtualRouterFrameMaxDataBytes {
		return nil, errors.New("vroute proxy udp datagram is too large")
	}
	payload := make([]byte, headerSize+len(data))
	copy(payload, id)
	binary.BigEndian.PutUint16(payload[probeVRouteProxySessionIDBytes:], uint16(len(target)))
	copy(payload[probeVRouteProxySessionIDBytes+2:], target)
	copy(payload[headerSize:], data)
	return payload, nil
}

func unmarshalProbeVRouteProxyUDPDatagram(payload []byte) (string, string, []byte, error) {
	if len(payload) <= probeVRouteProxySessionIDBytes+2 {
		return "", "", nil, errors.New("invalid vroute proxy udp payload")
	}
	targetSize := int(binary.BigEndian.Uint16(payload[probeVRouteProxySessionIDBytes : probeVRouteProxySessionIDBytes+2]))
	headerSize := probeVRouteProxySessionIDBytes + 2 + targetSize
	if targetSize <= 0 || targetSize > probeVRouteProxyUDPAddressMaxBytes || len(payload) <= headerSize {
		return "", "", nil, errors.New("invalid vroute proxy udp target size")
	}
	associationID := hex.EncodeToString(payload[:probeVRouteProxySessionIDBytes])
	target := string(payload[probeVRouteProxySessionIDBytes+2 : headerSize])
	return associationID, target, payload[headerSize:], nil
}

func normalizeProbeVRouteProxyClientIP(value string) string {
	if ip := net.ParseIP(strings.TrimSpace(strings.Trim(value, "[]"))); ip != nil {
		return ip.String()
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(value))
	if err == nil {
		if ip := net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]"))); ip != nil {
			return ip.String()
		}
	}
	return strings.TrimSpace(value)
}

func cloneProbeVRouteProxyUDPAddr(value *net.UDPAddr) *net.UDPAddr {
	if value == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), value.IP...), Port: value.Port, Zone: value.Zone}
}

func resetProbeVRouteProxyUDPStateForTest() {
	probeVRouteProxyUDPState.mu.RLock()
	associations := make([]*probeVRouteProxyUDPAssociation, 0, len(probeVRouteProxyUDPState.associations))
	for _, association := range probeVRouteProxyUDPState.associations {
		associations = append(associations, association)
	}
	exitSessions := make([]*probeVRouteProxyUDPExitSession, 0, len(probeVRouteProxyUDPState.exitSessions))
	for _, session := range probeVRouteProxyUDPState.exitSessions {
		exitSessions = append(exitSessions, session)
	}
	probeVRouteProxyUDPState.mu.RUnlock()
	for _, association := range associations {
		association.close(false)
	}
	for _, session := range exitSessions {
		session.close()
	}
}
