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

var mobileRouteSessions = &mobileRouteSessionStore{sessions: map[string]*mobileRoutePathSession{}}

type mobileRouteSessionStore struct {
	mu        sync.Mutex
	configDir string
	sessions  map[string]*mobileRoutePathSession
}

type mobileRoutePathSession struct {
	routeID string
	conn    net.Conn
	session *mobileRouteFrameSession
}

type androidRouteDecision struct {
	Direct          bool
	Reject          bool
	TargetAddr      string
	Group           string
	SelectedRouteID string
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
	mobileRouteSessions.sessions = map[string]*mobileRoutePathSession{}
	mobileRouteSessions.mu.Unlock()
	for _, sess := range sessions {
		closeMobileRoutePathSession(sess)
	}
}

func openMobileRoutePathStream(selectedRouteID string, network string, targetAddr string) (net.Conn, error) {
	return openMobileRoutePathStreamWithFlow(selectedRouteID, network, targetAddr, newAndroidRouteFlowID("route", targetAddr))
}

func openMobileRoutePathStreamWithFlow(selectedRouteID string, network string, targetAddr string, flowID string) (net.Conn, error) {
	item, endpoint, err := loadRouteEndpointByID(mobileRouteConfigDir(), selectedRouteID)
	if err != nil {
		return nil, err
	}
	request := routeTunnelOpenRequest{
		Type:             "open",
		Network:          strings.ToLower(strings.TrimSpace(network)),
		Address:          strings.TrimSpace(targetAddr),
		FlowID:           strings.TrimSpace(flowID),
		AppProtocol:      resolveRouteTunnelAppProtocol(network, targetAddr, nil),
		Priority:         resolveRouteTunnelPriority(network, targetAddr, nil),
		ResumePolicy:     resolveRouteTunnelResumePolicy(network, nil),
		LatencySensitive: isRouteTunnelLatencySensitive(network, targetAddr, nil),
	}
	return openMobileRouteIndependentStream(item, endpoint, request)
}

func openMobileRoutePathPacketStream(selectedRouteID string, network string, targetAddr string, association *routeAssociationV2Meta) (net.Conn, error) {
	item, endpoint, err := loadRouteEndpointByID(mobileRouteConfigDir(), selectedRouteID)
	if err != nil {
		return nil, err
	}
	request := routeTunnelOpenRequest{
		Type:             "open",
		Network:          strings.ToLower(strings.TrimSpace(network)),
		Address:          strings.TrimSpace(targetAddr),
		FlowID:           strings.TrimSpace(association.FlowID),
		AppProtocol:      resolveRouteTunnelAppProtocol(network, targetAddr, association),
		Priority:         resolveRouteTunnelPriority(network, targetAddr, association),
		ResumePolicy:     resolveRouteTunnelResumePolicy(network, association),
		LatencySensitive: isRouteTunnelLatencySensitive(network, targetAddr, association),
		AssociationV2:    association,
	}
	return openMobileRouteIndependentStream(item, endpoint, request)
}

func resolveRouteTunnelPriority(network string, targetAddr string, association *routeAssociationV2Meta) string {
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

func resolveRouteTunnelAppProtocol(network string, targetAddr string, association *routeAssociationV2Meta) string {
	if association != nil && strings.EqualFold(strings.TrimSpace(association.Transport), "udp") {
		return "udp-association"
	}
	if strings.EqualFold(strings.TrimSpace(network), "udp") {
		return "udp-association"
	}
	switch port := routeTunnelTargetPort(targetAddr); {
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

func resolveRouteTunnelResumePolicy(network string, association *routeAssociationV2Meta) string {
	if association != nil && strings.EqualFold(strings.TrimSpace(association.Transport), "udp") {
		return "rebind"
	}
	if strings.EqualFold(strings.TrimSpace(network), "udp") {
		return "rebind"
	}
	return "replay_required"
}

func isRouteTunnelLatencySensitive(network string, targetAddr string, association *routeAssociationV2Meta) bool {
	return resolveRouteTunnelPriority(network, targetAddr, association) == "realtime"
}

func isLinkRealtimeTCPPort(targetAddr string) bool {
	switch routeTunnelTargetPort(targetAddr) {
	case 22, 3389, 4000:
		return true
	default:
		port := routeTunnelTargetPort(targetAddr)
		return port >= 5900 && port <= 5999
	}
}

func routeTunnelTargetPort(targetAddr string) int {
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

func openMobileRouteIndependentStream(item routeServerItem, endpoint routeEndpoint, request routeTunnelOpenRequest) (net.Conn, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		session, err := ensureMobileRoutePathSession(item, endpoint)
		if err != nil {
			return nil, err
		}
		stream, err := session.OpenWithRequest(request, mobileRouteResponseReadTimeout)
		if err != nil {
			lastErr = err
			androidLogStore.add("route", "warn", "open frame stream failed: route="+strings.TrimSpace(endpoint.RouteID)+" target="+strings.TrimSpace(request.Address)+" attempt="+strconv.Itoa(attempt+1)+" err="+err.Error())
			invalidateMobileRoutePathSession(endpoint.RouteID, session)
			continue
		}
		androidLogStore.add("route", "debug", "open frame stream ok: route="+strings.TrimSpace(endpoint.RouteID)+" target="+strings.TrimSpace(request.Address)+" priority="+strings.TrimSpace(request.Priority))
		return stream, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("open route bridge stream failed")
}

func ensureMobileRoutePathSession(item routeServerItem, endpoint routeEndpoint) (*mobileRouteFrameSession, error) {
	mobileRouteSessions.mu.Lock()
	if existing := mobileRouteSessions.sessions[endpoint.RouteID]; existing != nil && existing.session != nil && !existing.session.IsClosed() {
		session := existing.session
		mobileRouteSessions.mu.Unlock()
		androidLogStore.add("route", "debug", "reuse frame session: route="+strings.TrimSpace(endpoint.RouteID))
		return session, nil
	}
	mobileRouteSessions.mu.Unlock()

	conn, err := openMobileRouteRouteRelayConn(item, endpoint)
	if err != nil {
		return nil, err
	}
	session, err := newMobileRouteFrameClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	ready := session.WaitReady(mobileRouteResponseReadTimeout)
	androidLogStore.add("route", "debug", "frame session ready: route="+strings.TrimSpace(endpoint.RouteID)+" ready="+strconv.FormatBool(ready))
	if !ready {
		_ = session.Close()
		_ = conn.Close()
		return nil, errors.New("frame session negotiation timeout")
	}
	androidLogStore.add("route", "normal", "created frame session: route="+strings.TrimSpace(endpoint.RouteID))
	mobileRouteSessions.mu.Lock()
	if old := mobileRouteSessions.sessions[endpoint.RouteID]; old != nil {
		closeMobileRoutePathSession(old)
	}
	mobileRouteSessions.sessions[endpoint.RouteID] = &mobileRoutePathSession{routeID: effectiveRouteRelayRouteID(item), conn: conn, session: session}
	mobileRouteSessions.mu.Unlock()
	return session, nil
}

func openMobileRouteRouteRelayConn(item routeServerItem, endpoint routeEndpoint) (net.Conn, error) {
	protocols := routeReachabilityProtocolsForEndpoint(item, endpoint)
	var lastErr error
	for _, protocol := range protocols {
		conn, err := openRouteRelayConn(endpoint, protocol, mobileRouteConnectTimeout+mobileRouteResponseReadTimeout)
		if err == nil {
			if normalizeRouteLayer(endpoint.RouteLayer) == "" {
				androidLogStore.add("route", "normal", "relay protocol selected: route="+endpoint.RouteID+" protocol="+normalizeRouteLayer(protocol))
			}
			return conn, nil
		}
		lastErr = err
		androidLogStore.add("route", "warn", "relay protocol failed: route="+endpoint.RouteID+" protocol="+normalizeRouteLayer(protocol)+" err="+err.Error())
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no supported relay protocol")
}

func invalidateMobileRoutePathSession(routeID string, session *mobileRouteFrameSession) {
	mobileRouteSessions.mu.Lock()
	existing := mobileRouteSessions.sessions[strings.TrimSpace(routeID)]
	if existing != nil && existing.session == session {
		delete(mobileRouteSessions.sessions, strings.TrimSpace(routeID))
	}
	mobileRouteSessions.mu.Unlock()
	if existing != nil {
		closeMobileRoutePathSession(existing)
	}
}

func closeMobileRoutePathSession(sess *mobileRoutePathSession) {
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
