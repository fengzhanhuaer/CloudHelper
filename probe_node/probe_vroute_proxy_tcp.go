package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	probeVRouteProxyTCPOpenTimeout      = 12 * time.Second
	probeVRouteProxyTCPInboundQueueSize = 256
	probeVRouteProxyTCPInboundInitial   = 16
)

type probeVRouteProxyTCPSession struct {
	id           string
	role         string
	targetAddr   string
	path         []string
	conn         net.Conn
	inbound      *probeAdaptiveQueue[[]byte]
	openResult   chan probeVRouteProxyTCPOpenResultPayload
	done         chan struct{}
	createdAt    time.Time
	lastActiveNS atomic.Int64
	closeOnce    sync.Once
}

var probeVRouteProxyTCPState = struct {
	mu       sync.RWMutex
	sessions map[string]*probeVRouteProxyTCPSession
	opened   atomic.Uint64
	closed   atomic.Uint64
	txBytes  atomic.Uint64
	rxBytes  atomic.Uint64
}{sessions: make(map[string]*probeVRouteProxyTCPSession)}

var probeVRouteProxyExitTCPDial = dialProbeVRouteProxyExitTCP

func dialProbeVRouteProxyTCP(targetAddr string) (net.Conn, probeVRouteProxyTargetDecision, error) {
	decision, err := decideProbeVRouteProxyTarget(targetAddr)
	if err != nil {
		recordProbeVirtualRouterRecentDialFailure(targetAddr, decision, err)
		return nil, decision, err
	}
	if decision.Direct() {
		conn, dialErr := dialProbeVRouteProxyDirectTCP(decision.TargetAddr)
		if dialErr != nil {
			recordProbeVirtualRouterRecentDialFailure(targetAddr, decision, dialErr)
		}
		return conn, decision, dialErr
	}
	conn, err := openProbeVRouteProxyRemoteTCP(decision)
	if err != nil {
		recordProbeVirtualRouterRecentDialFailure(targetAddr, decision, err)
	}
	return conn, decision, err
}

func openProbeVRouteProxyRemoteTCP(decision probeVRouteProxyTargetDecision) (net.Conn, error) {
	if decision.Action != "probe_exit" || len(decision.Path) < 2 {
		return nil, errors.New("remote vroute proxy tcp decision is invalid")
	}
	sessionID, err := newProbeVRouteProxySessionID()
	if err != nil {
		return nil, err
	}
	appConn, bridgeConn := net.Pipe()
	session := &probeVRouteProxyTCPSession{
		id:         sessionID,
		role:       "source",
		targetAddr: decision.TargetAddr,
		path:       append([]string(nil), decision.Path...),
		conn:       bridgeConn,
		inbound:    newProbeVRouteProxyTCPInboundQueue(sessionID, "source"),
		openResult: make(chan probeVRouteProxyTCPOpenResultPayload, 1),
		done:       make(chan struct{}),
		createdAt:  time.Now(),
	}
	if err := registerProbeVRouteProxyTCPSession(session); err != nil {
		_ = appConn.Close()
		_ = bridgeConn.Close()
		return nil, err
	}
	openPayload, err := marshalProbeVRouteProxyJSON(probeVRouteProxyTCPOpenPayload{
		SessionID:    sessionID,
		TargetAddr:   decision.TargetAddr,
		SourceNodeID: decision.Path[0],
		ExitNodeID:   decision.Path[len(decision.Path)-1],
	})
	if err != nil {
		session.close(false, err)
		_ = appConn.Close()
		return nil, err
	}
	if err := forwardProbeVRouteProxyFrame(probeVirtualRouterProxySubTypeTCPOpen, openPayload, decision.Path); err != nil {
		session.close(false, err)
		_ = appConn.Close()
		return nil, err
	}
	timer := time.NewTimer(probeVRouteProxyTCPOpenTimeout)
	defer timer.Stop()
	select {
	case result := <-session.openResult:
		if !result.OK {
			err := errors.New(firstNonEmpty(strings.TrimSpace(result.Error), "remote proxy tcp open failed"))
			session.close(false, err)
			_ = appConn.Close()
			return nil, err
		}
		go session.runOutbound()
		return appConn, nil
	case <-session.done:
		_ = appConn.Close()
		return nil, io.ErrClosedPipe
	case <-timer.C:
		err := errors.New("remote proxy tcp open timeout")
		session.close(true, err)
		_ = appConn.Close()
		return nil, err
	}
}

func handleProbeVRouteProxyTCPOpen(payload []byte, path []string) error {
	var msg probeVRouteProxyTCPOpenPayload
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}
	if _, err := decodeProbeVRouteProxySessionID(msg.SessionID); err != nil {
		return err
	}
	msg.TargetAddr = strings.TrimSpace(msg.TargetAddr)
	if _, _, err := net.SplitHostPort(msg.TargetAddr); err != nil {
		return fmt.Errorf("invalid remote proxy tcp target: %w", err)
	}
	cleanPath := cleanProbeVirtualRouterPath(path)
	if len(cleanPath) < 2 || normalizeProbeRouteNodeID(cleanPath[len(cleanPath)-1]) != currentProbeVirtualRouterLocalNodeID() {
		return errors.New("remote proxy tcp open path does not end at local node")
	}
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if normalizeProbeRouteNodeID(msg.SourceNodeID) != cleanPath[0] || normalizeProbeRouteNodeID(msg.ExitNodeID) != localNodeID {
		return errors.New("remote proxy tcp open identity does not match path")
	}
	if err := authorizeProbeVRouteProxyExitTarget(msg.TargetAddr, cleanPath); err != nil {
		return err
	}
	go openProbeVRouteProxyTCPAtExit(msg, cleanPath)
	return nil
}

func openProbeVRouteProxyTCPAtExit(msg probeVRouteProxyTCPOpenPayload, requestPath []string) {
	responsePath := probeVirtualRouterReversePath(requestPath)
	result := probeVRouteProxyTCPOpenResultPayload{SessionID: msg.SessionID}
	conn, err := probeVRouteProxyExitTCPDial(msg.TargetAddr)
	if err != nil {
		result.Error = err.Error()
		_ = sendProbeVRouteProxyTCPOpenResult(result, responsePath)
		return
	}
	session := &probeVRouteProxyTCPSession{
		id:         strings.ToLower(strings.TrimSpace(msg.SessionID)),
		role:       "exit",
		targetAddr: msg.TargetAddr,
		path:       responsePath,
		conn:       conn,
		inbound:    newProbeVRouteProxyTCPInboundQueue(msg.SessionID, "exit"),
		done:       make(chan struct{}),
		createdAt:  time.Now(),
	}
	if err := registerProbeVRouteProxyTCPSession(session); err != nil {
		_ = conn.Close()
		result.Error = err.Error()
		_ = sendProbeVRouteProxyTCPOpenResult(result, responsePath)
		return
	}
	result.OK = true
	if err := sendProbeVRouteProxyTCPOpenResult(result, responsePath); err != nil {
		session.close(false, err)
		return
	}
	go session.runOutbound()
}

func sendProbeVRouteProxyTCPOpenResult(result probeVRouteProxyTCPOpenResultPayload, path []string) error {
	payload, err := marshalProbeVRouteProxyJSON(result)
	if err != nil {
		return err
	}
	return forwardProbeVRouteProxyFrame(probeVirtualRouterProxySubTypeTCPOpenResult, payload, path)
}

func handleProbeVRouteProxyTCPOpenResult(payload []byte) error {
	var result probeVRouteProxyTCPOpenResultPayload
	if err := json.Unmarshal(payload, &result); err != nil {
		return err
	}
	session := currentProbeVRouteProxyTCPSession(result.SessionID)
	if session == nil || session.role != "source" || session.openResult == nil {
		return nil
	}
	select {
	case session.openResult <- result:
	default:
	}
	return nil
}

func handleProbeVRouteProxyTCPData(payload []byte) error {
	sessionID, data, err := unmarshalProbeVRouteProxyTCPData(payload)
	if err != nil {
		return err
	}
	session := currentProbeVRouteProxyTCPSession(sessionID)
	if session == nil {
		return nil
	}
	packet := append([]byte(nil), data...)
	if session.inbound.TryPush(packet) {
		session.touch()
		probeVRouteProxyTCPState.rxBytes.Add(uint64(len(packet)))
		return nil
	}
	select {
	case <-session.done:
		return nil
	default:
		err := errors.New("vroute proxy tcp inbound queue is full")
		session.close(true, err)
		return err
	}
}

func newProbeVRouteProxyTCPInboundQueue(sessionID string, role string) *probeAdaptiveQueue[[]byte] {
	return newProbeAdaptiveQueue[[]byte](probeAdaptiveQueueOptions{
		ID:              fmt.Sprintf("vroute_proxy.tcp.%s.%s.inbound", strings.TrimSpace(role), strings.ToLower(strings.TrimSpace(sessionID))),
		Stage:           "vroute_proxy_tcp_inbound",
		Direction:       "rx",
		InitialCapacity: probeVRouteProxyTCPInboundInitial,
		MaxCapacity:     probeVRouteProxyTCPInboundQueueSize,
	})
}

func handleProbeVRouteProxyTCPClose(payload []byte) error {
	var msg probeVRouteProxyClosePayload
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}
	if session := currentProbeVRouteProxyTCPSession(msg.SessionID); session != nil {
		session.close(false, errors.New(strings.TrimSpace(msg.Error)))
	}
	return nil
}

func registerProbeVRouteProxyTCPSession(session *probeVRouteProxyTCPSession) error {
	if session == nil || session.conn == nil || session.id == "" || session.done == nil {
		return errors.New("invalid vroute proxy tcp session")
	}
	probeVRouteProxyTCPState.mu.Lock()
	if _, exists := probeVRouteProxyTCPState.sessions[session.id]; exists {
		probeVRouteProxyTCPState.mu.Unlock()
		return errors.New("duplicate vroute proxy tcp session")
	}
	probeVRouteProxyTCPState.sessions[session.id] = session
	probeVRouteProxyTCPState.mu.Unlock()
	session.touch()
	probeVRouteProxyTCPState.opened.Add(1)
	go session.runInbound()
	return nil
}

func currentProbeVRouteProxyTCPSession(sessionID string) *probeVRouteProxyTCPSession {
	cleanID := strings.ToLower(strings.TrimSpace(sessionID))
	probeVRouteProxyTCPState.mu.RLock()
	session := probeVRouteProxyTCPState.sessions[cleanID]
	probeVRouteProxyTCPState.mu.RUnlock()
	return session
}

func (s *probeVRouteProxyTCPSession) runInbound() {
	for {
		select {
		case <-s.done:
			return
		case <-s.inbound.Ready():
			payload, ok := s.inbound.TryPop()
			if !ok {
				continue
			}
			if len(payload) == 0 {
				continue
			}
			if _, err := s.conn.Write(payload); err != nil {
				s.close(true, err)
				return
			}
			s.touch()
		}
	}
}

func (s *probeVRouteProxyTCPSession) runOutbound() {
	buffer := make([]byte, probeVRouteProxyTCPChunkBytes)
	for {
		n, err := s.conn.Read(buffer)
		if n > 0 {
			payload, marshalErr := marshalProbeVRouteProxyTCPData(s.id, buffer[:n])
			if marshalErr != nil {
				s.close(true, marshalErr)
				return
			}
			if sendErr := forwardProbeVRouteProxyFrame(probeVirtualRouterProxySubTypeTCPData, payload, s.path); sendErr != nil {
				s.close(true, sendErr)
				return
			}
			probeVRouteProxyTCPState.txBytes.Add(uint64(n))
			s.touch()
		}
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				s.close(true, err)
				return
			}
		}
	}
}

func (s *probeVRouteProxyTCPSession) close(notify bool, cause error) {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		close(s.done)
		if s.inbound != nil {
			s.inbound.Drain(nil)
			s.inbound.Close()
		}
		_ = s.conn.Close()
		probeVRouteProxyTCPState.mu.Lock()
		if probeVRouteProxyTCPState.sessions[s.id] == s {
			delete(probeVRouteProxyTCPState.sessions, s.id)
		}
		probeVRouteProxyTCPState.mu.Unlock()
		probeVRouteProxyTCPState.closed.Add(1)
		if notify && len(s.path) >= 2 {
			message := probeVRouteProxyClosePayload{SessionID: s.id}
			if cause != nil && !errors.Is(cause, io.EOF) && !errors.Is(cause, net.ErrClosed) {
				message.Error = cause.Error()
			}
			if payload, err := marshalProbeVRouteProxyJSON(message); err == nil {
				_ = forwardProbeVRouteProxyFrame(probeVirtualRouterProxySubTypeTCPClose, payload, s.path)
			}
		}
	})
}

func (s *probeVRouteProxyTCPSession) touch() {
	if s != nil {
		s.lastActiveNS.Store(time.Now().UnixNano())
	}
}

func closeProbeVRouteProxyTCPSessionsForEdge(fromNodeID string, toNodeID string, reason string) {
	probeVRouteProxyTCPState.mu.RLock()
	items := make([]*probeVRouteProxyTCPSession, 0)
	for _, session := range probeVRouteProxyTCPState.sessions {
		if session != nil && probeVirtualRouterPathContainsAdjacentEdge(session.path, fromNodeID, toNodeID) {
			items = append(items, session)
		}
	}
	probeVRouteProxyTCPState.mu.RUnlock()
	for _, session := range items {
		session.close(false, errors.New(firstNonEmpty(strings.TrimSpace(reason), "vroute carrier disconnected")))
	}
}

func newProbeVRouteProxySessionID() (string, error) {
	value := make([]byte, probeVRouteProxySessionIDBytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func resetProbeVRouteProxyTCPStateForTest() {
	probeVRouteProxyTCPState.mu.RLock()
	items := make([]*probeVRouteProxyTCPSession, 0, len(probeVRouteProxyTCPState.sessions))
	for _, session := range probeVRouteProxyTCPState.sessions {
		items = append(items, session)
	}
	probeVRouteProxyTCPState.mu.RUnlock()
	for _, session := range items {
		session.close(false, net.ErrClosed)
	}
}
