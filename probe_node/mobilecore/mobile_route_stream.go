package mobilecore

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mobileRouteConnectTimeout      = 12 * time.Second
	mobileRouteResponseReadTimeout = 10 * time.Second
)

var mobileRouteSessions = &mobileRouteSessionStore{sessions: map[string]*mobileRouteChainSession{}}

type mobileRouteSessionStore struct {
	mu        sync.Mutex
	configDir string
	sessions  map[string]*mobileRouteChainSession
}

type mobileRouteChainSession struct {
	chainID string
	conn    net.Conn
	session *mobileChainFrameSession
}

type androidRouteDecision struct {
	Direct          bool
	Reject          bool
	TargetAddr      string
	Group           string
	SelectedChainID string
}

func setMobileRouteConfigDir(configDir string) {
	mobileRouteSessions.mu.Lock()
	mobileRouteSessions.configDir = strings.TrimSpace(configDir)
	mobileRouteSessions.mu.Unlock()
}

func mobileRouteConfigDir() string {
	mobileRouteSessions.mu.Lock()
	defer mobileRouteSessions.mu.Unlock()
	return mobileRouteSessions.configDir
}

func closeMobileRouteSessions() {
	mobileRouteSessions.mu.Lock()
	sessions := mobileRouteSessions.sessions
	mobileRouteSessions.sessions = map[string]*mobileRouteChainSession{}
	mobileRouteSessions.mu.Unlock()
	for _, sess := range sessions {
		closeMobileRouteChainSession(sess)
	}
}

func openMobileRouteChainStream(selectedChainID string, network string, targetAddr string) (net.Conn, error) {
	return openMobileRouteChainStreamWithFlow(selectedChainID, network, targetAddr, newAndroidRouteFlowID("chain", targetAddr))
}

func openMobileRouteChainStreamWithFlow(selectedChainID string, network string, targetAddr string, flowID string) (net.Conn, error) {
	item, endpoint, err := loadLinkEndpointByID(mobileRouteConfigDir(), selectedChainID)
	if err != nil {
		return nil, err
	}
	request := linkTunnelOpenRequest{
		Type:             "open",
		Network:          strings.ToLower(strings.TrimSpace(network)),
		Address:          strings.TrimSpace(targetAddr),
		FlowID:           strings.TrimSpace(flowID),
		AppProtocol:      resolveLinkTunnelAppProtocol(network, targetAddr, nil),
		Priority:         resolveLinkTunnelPriority(network, targetAddr, nil),
		ResumePolicy:     resolveLinkTunnelResumePolicy(network, nil),
		LatencySensitive: isLinkTunnelLatencySensitive(network, targetAddr, nil),
	}
	return openMobileRouteIndependentStream(item, endpoint, request)
}

func openMobileRouteChainPacketStream(selectedChainID string, network string, targetAddr string, association *linkAssociationV2Meta) (net.Conn, error) {
	item, endpoint, err := loadLinkEndpointByID(mobileRouteConfigDir(), selectedChainID)
	if err != nil {
		return nil, err
	}
	request := linkTunnelOpenRequest{
		Type:             "open",
		Network:          strings.ToLower(strings.TrimSpace(network)),
		Address:          strings.TrimSpace(targetAddr),
		FlowID:           strings.TrimSpace(association.FlowID),
		AppProtocol:      resolveLinkTunnelAppProtocol(network, targetAddr, association),
		Priority:         resolveLinkTunnelPriority(network, targetAddr, association),
		ResumePolicy:     resolveLinkTunnelResumePolicy(network, association),
		LatencySensitive: isLinkTunnelLatencySensitive(network, targetAddr, association),
		AssociationV2:    association,
	}
	return openMobileRouteIndependentStream(item, endpoint, request)
}

func resolveLinkTunnelPriority(network string, targetAddr string, association *linkAssociationV2Meta) string {
	if association != nil && strings.EqualFold(strings.TrimSpace(association.Transport), "udp") {
		return "realtime"
	}
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "udp":
		return "realtime"
	default:
		if isLinkRealtimeTCPPort(targetAddr) {
			return "realtime"
		}
		return "normal"
	}
}

func resolveLinkTunnelAppProtocol(network string, targetAddr string, association *linkAssociationV2Meta) string {
	if association != nil && strings.EqualFold(strings.TrimSpace(association.Transport), "udp") {
		return "udp-association"
	}
	if strings.EqualFold(strings.TrimSpace(network), "udp") {
		return "udp-association"
	}
	switch port := linkTunnelTargetPort(targetAddr); {
	case port == 3389:
		return "rdp"
	case port >= 5900 && port <= 5999:
		return "vnc"
	case port == 4000:
		return "nomachine"
	case port == 22:
		return "ssh"
	default:
		return ""
	}
}

func resolveLinkTunnelResumePolicy(network string, association *linkAssociationV2Meta) string {
	if association != nil && strings.EqualFold(strings.TrimSpace(association.Transport), "udp") {
		return "rebind"
	}
	if strings.EqualFold(strings.TrimSpace(network), "udp") {
		return "rebind"
	}
	return "replay_required"
}

func isLinkTunnelLatencySensitive(network string, targetAddr string, association *linkAssociationV2Meta) bool {
	return resolveLinkTunnelPriority(network, targetAddr, association) == "realtime"
}

func isLinkRealtimeTCPPort(targetAddr string) bool {
	switch linkTunnelTargetPort(targetAddr) {
	case 22, 3389, 4000:
		return true
	default:
		port := linkTunnelTargetPort(targetAddr)
		return port >= 5900 && port <= 5999
	}
}

func linkTunnelTargetPort(targetAddr string) int {
	_, portText, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil {
		return 0
	}
	return port
}

func openMobileRouteIndependentStream(item linkChainServerItem, endpoint linkEndpoint, request linkTunnelOpenRequest) (net.Conn, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		session, err := ensureMobileRouteChainSession(item, endpoint)
		if err != nil {
			return nil, err
		}
		stream, err := session.OpenWithRequest(request, mobileRouteResponseReadTimeout)
		if err != nil {
			lastErr = err
			androidLogStore.add("route", "warn", "open frame stream failed: chain="+strings.TrimSpace(endpoint.ChainID)+" target="+strings.TrimSpace(request.Address)+" attempt="+strconv.Itoa(attempt+1)+" err="+err.Error())
			invalidateMobileRouteChainSession(endpoint.ChainID, session)
			continue
		}
		androidLogStore.add("route", "debug", "open frame stream ok: chain="+strings.TrimSpace(endpoint.ChainID)+" target="+strings.TrimSpace(request.Address)+" priority="+strings.TrimSpace(request.Priority))
		return stream, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("open route bridge stream failed")
}

func ensureMobileRouteChainSession(item linkChainServerItem, endpoint linkEndpoint) (*mobileChainFrameSession, error) {
	mobileRouteSessions.mu.Lock()
	if existing := mobileRouteSessions.sessions[endpoint.ChainID]; existing != nil && existing.session != nil && !existing.session.IsClosed() {
		session := existing.session
		mobileRouteSessions.mu.Unlock()
		androidLogStore.add("route", "debug", "reuse frame session: chain="+strings.TrimSpace(endpoint.ChainID))
		return session, nil
	}
	mobileRouteSessions.mu.Unlock()

	conn, err := openMobileRouteLinkRelayConn(item, endpoint)
	if err != nil {
		return nil, err
	}
	session, err := newMobileChainFrameClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	ready := session.WaitReady(mobileRouteResponseReadTimeout)
	androidLogStore.add("route", "debug", "frame session ready: chain="+strings.TrimSpace(endpoint.ChainID)+" ready="+strconv.FormatBool(ready))
	if !ready {
		_ = session.Close()
		_ = conn.Close()
		return nil, errors.New("frame session negotiation timeout")
	}
	androidLogStore.add("route", "normal", "created frame session: chain="+strings.TrimSpace(endpoint.ChainID))
	mobileRouteSessions.mu.Lock()
	if old := mobileRouteSessions.sessions[endpoint.ChainID]; old != nil {
		closeMobileRouteChainSession(old)
	}
	mobileRouteSessions.sessions[endpoint.ChainID] = &mobileRouteChainSession{chainID: effectiveLinkRelayChainID(item), conn: conn, session: session}
	mobileRouteSessions.mu.Unlock()
	return session, nil
}

func openMobileRouteLinkRelayConn(item linkChainServerItem, endpoint linkEndpoint) (net.Conn, error) {
	protocols := linkReachabilityProtocolsForEndpoint(item, endpoint)
	var lastErr error
	for _, protocol := range protocols {
		conn, err := openLinkRelayConn(endpoint, protocol, mobileRouteConnectTimeout+mobileRouteResponseReadTimeout)
		if err == nil {
			if normalizeLinkLayer(endpoint.LinkLayer) == "" {
				androidLogStore.add("route", "normal", "relay protocol selected: chain="+endpoint.ChainID+" protocol="+normalizeLinkLayer(protocol))
			}
			return conn, nil
		}
		lastErr = err
		androidLogStore.add("route", "warn", "relay protocol failed: chain="+endpoint.ChainID+" protocol="+normalizeLinkLayer(protocol)+" err="+err.Error())
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no supported relay protocol")
}

func invalidateMobileRouteChainSession(chainID string, session *mobileChainFrameSession) {
	mobileRouteSessions.mu.Lock()
	existing := mobileRouteSessions.sessions[strings.TrimSpace(chainID)]
	if existing != nil && existing.session == session {
		delete(mobileRouteSessions.sessions, strings.TrimSpace(chainID))
	}
	mobileRouteSessions.mu.Unlock()
	if existing != nil {
		closeMobileRouteChainSession(existing)
	}
}

func closeMobileRouteChainSession(sess *mobileRouteChainSession) {
	if sess == nil {
		return
	}
	if sess.session != nil {
		_ = sess.session.Close()
	}
	if sess.conn != nil {
		_ = sess.conn.Close()
	}
}
