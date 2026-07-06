package mobilecore

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	proxyGroupFileName       = "proxy_group.json"
	proxyStateFileName       = "proxy_state.json"
	proxyConnectTimeout      = 12 * time.Second
	proxyResponseReadTimeout = 10 * time.Second
)

var proxyRuntime = &androidProxyRuntime{sessions: map[string]*proxyChainSession{}}

type androidProxyRuntime struct {
	mu            sync.Mutex
	sessionMu     sync.Mutex
	configDir     string
	httpListener  net.Listener
	socksListener net.Listener
	httpAddr      string
	socksAddr     string
	lastError     string
	updatedAt     string
	sessions      map[string]*proxyChainSession
}

type proxyChainSession struct {
	chainID string
	conn    net.Conn
	session *mobileChainFrameSession
}

type proxyGroupFile struct {
	Version int               `json:"version"`
	Groups  []proxyGroupEntry `json:"groups"`
	Note    string            `json:"note,omitempty"`
}

type proxyGroupEntry struct {
	Group     string   `json:"group"`
	Rules     []string `json:"rules,omitempty"`
	RulesText string   `json:"rules_text,omitempty"`
}

type proxyStateFile struct {
	Version   int               `json:"version"`
	UpdatedAt string            `json:"updated_at"`
	Groups    []proxyStateGroup `json:"groups"`
}

type proxyStateGroup struct {
	Group           string `json:"group"`
	Action          string `json:"action,omitempty"`
	SelectedChainID string `json:"selected_chain_id,omitempty"`
	TunnelNodeID    string `json:"tunnel_node_id,omitempty"`
	RuntimeStatus   string `json:"runtime_status,omitempty"`
}

type proxyRouteDecision struct {
	Direct          bool
	Reject          bool
	TargetAddr      string
	Group           string
	SelectedChainID string
}

func ProxyStart(configDir string) string {
	_ = configDir
	ProxyStop()
	return "legacy android local proxy removed"
}

func ProxyStop() string {
	proxyRuntime.mu.Lock()
	httpListener := proxyRuntime.httpListener
	socksListener := proxyRuntime.socksListener
	proxyRuntime.httpListener = nil
	proxyRuntime.socksListener = nil
	proxyRuntime.httpAddr = ""
	proxyRuntime.socksAddr = ""
	proxyRuntime.lastError = ""
	proxyRuntime.updatedAt = time.Now().UTC().Format(time.RFC3339)
	sessions := proxyRuntime.sessions
	proxyRuntime.sessions = map[string]*proxyChainSession{}
	proxyRuntime.mu.Unlock()
	if httpListener != nil {
		_ = httpListener.Close()
	}
	if socksListener != nil {
		_ = socksListener.Close()
	}
	for _, sess := range sessions {
		closeProxyChainSession(sess)
	}
	return "proxy stopped"
}

func ProxyStatus(configDir string) string {
	proxyRuntime.mu.Lock()
	status := map[string]any{
		"http_enabled":   proxyRuntime.httpListener != nil,
		"http_addr":      proxyRuntime.httpAddr,
		"socks5_enabled": proxyRuntime.socksListener != nil,
		"socks5_addr":    proxyRuntime.socksAddr,
		"last_error":     proxyRuntime.lastError,
		"updated_at":     proxyRuntime.updatedAt,
	}
	proxyRuntime.mu.Unlock()
	groups, _ := loadProxyGroupFile(configDir)
	state, _ := loadProxyStateFile(configDir)
	chains, _ := loadLinkProxyChains(configDir)
	status["groups"] = buildProxyGroupStatus(groups, state)
	status["chains"] = buildProxyChainStatus(chains)
	status["connections"] = globalAndroidProxyConnectionState.snapshot()
	status["ok"] = true
	return marshalLinkJSON(status)
}

func ProxySetGroup(configDir string, group string, action string, selectedChainID string) string {
	cleanGroup := firstNonEmptyString(strings.TrimSpace(group), "fallback")
	cleanAction := strings.ToLower(strings.TrimSpace(action))
	switch cleanAction {
	case "direct", "reject":
		selectedChainID = ""
	case "tunnel":
		selectedChainID = strings.TrimSpace(selectedChainID)
		if selectedChainID == "" {
			return `{"ok":false,"error":"selected_chain_id is required"}`
		}
		if _, _, err := loadLinkEndpointByID(configDir, selectedChainID); err != nil {
			return marshalLinkJSON(map[string]any{"ok": false, "error": err.Error()})
		}
	default:
		return `{"ok":false,"error":"action must be direct, reject, or tunnel"}`
	}
	state, _ := loadProxyStateFile(configDir)
	state.Version = 1
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	found := false
	for i := range state.Groups {
		if strings.EqualFold(strings.TrimSpace(state.Groups[i].Group), cleanGroup) {
			state.Groups[i].Action = cleanAction
			state.Groups[i].SelectedChainID = selectedChainID
			state.Groups[i].TunnelNodeID = formatProxyLegacyTunnelNodeID(selectedChainID)
			state.Groups[i].RuntimeStatus = ""
			found = true
			break
		}
	}
	if !found {
		state.Groups = append(state.Groups, proxyStateGroup{
			Group:           cleanGroup,
			Action:          cleanAction,
			SelectedChainID: selectedChainID,
			TunnelNodeID:    formatProxyLegacyTunnelNodeID(selectedChainID),
		})
	}
	if err := writeJSONFile(filepath.Join(strings.TrimSpace(configDir), proxyStateFileName), state); err != nil {
		return marshalLinkJSON(map[string]any{"ok": false, "error": err.Error()})
	}
	return marshalLinkJSON(map[string]any{"ok": true, "group": cleanGroup, "action": cleanAction, "selected_chain_id": selectedChainID})
}

func openAndroidProxyChainStream(selectedChainID string, network string, targetAddr string) (net.Conn, error) {
	return openAndroidProxyChainStreamWithFlow(selectedChainID, network, targetAddr, newAndroidProxyFlowID("chain", targetAddr))
}

func openAndroidProxyChainStreamWithFlow(selectedChainID string, network string, targetAddr string, flowID string) (net.Conn, error) {
	item, endpoint, err := loadLinkEndpointByID(proxyRuntimeConfigDir(), selectedChainID)
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
	return openAndroidProxyIndependentStream(item, endpoint, request)
}

func openAndroidProxyChainPacketStream(selectedChainID string, network string, targetAddr string, association *linkAssociationV2Meta) (net.Conn, error) {
	item, endpoint, err := loadLinkEndpointByID(proxyRuntimeConfigDir(), selectedChainID)
	if err != nil {
		return nil, err
	}
	request := linkTunnelOpenRequest{
		Type:             "open",
		Network:          strings.ToLower(strings.TrimSpace(network)),
		Address:          strings.TrimSpace(targetAddr),
		AppProtocol:      resolveLinkTunnelAppProtocol(network, targetAddr, association),
		Priority:         resolveLinkTunnelPriority(network, targetAddr, association),
		ResumePolicy:     resolveLinkTunnelResumePolicy(network, association),
		LatencySensitive: isLinkTunnelLatencySensitive(network, targetAddr, association),
		AssociationV2:    association,
	}
	return openAndroidProxyIndependentStream(item, endpoint, request)
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

func openAndroidProxyIndependentStream(item linkChainServerItem, endpoint linkEndpoint, request linkTunnelOpenRequest) (net.Conn, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		session, err := ensureProxyChainSession(item, endpoint)
		if err != nil {
			return nil, err
		}
		stream, err := session.OpenWithRequest(request, proxyResponseReadTimeout)
		if err != nil {
			lastErr = err
			androidLogStore.add("proxy", "warn", "open frame stream failed: chain="+strings.TrimSpace(endpoint.ChainID)+" target="+strings.TrimSpace(request.Address)+" attempt="+strconv.Itoa(attempt+1)+" err="+err.Error())
			invalidateProxyChainSession(endpoint.ChainID, session)
			continue
		}
		androidLogStore.add("proxy", "debug", "open frame stream ok: chain="+strings.TrimSpace(endpoint.ChainID)+" target="+strings.TrimSpace(request.Address)+" priority="+strings.TrimSpace(request.Priority))
		return stream, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("open proxy bridge stream failed")
}

func openAndroidProxyLinkRelayDataStream(item linkChainServerItem, endpoint linkEndpoint) (net.Conn, error) {
	protocols := linkReachabilityProtocolsForEndpoint(item, endpoint)
	var lastErr error
	for _, protocol := range protocols {
		conn, err := openLinkRelayDataStreamConn(endpoint, protocol, proxyConnectTimeout+proxyResponseReadTimeout)
		if err == nil {
			if normalizeLinkLayer(endpoint.LinkLayer) == "" {
				androidLogStore.add("proxy", "normal", "relay stream protocol selected: chain="+endpoint.ChainID+" protocol="+normalizeLinkLayer(protocol))
			}
			return conn, nil
		}
		lastErr = err
		androidLogStore.add("proxy", "warn", "relay stream protocol failed: chain="+endpoint.ChainID+" protocol="+normalizeLinkLayer(protocol)+" err="+err.Error())
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no supported relay stream protocol")
}

func ensureProxyChainSession(item linkChainServerItem, endpoint linkEndpoint) (*mobileChainFrameSession, error) {
	proxyRuntime.sessionMu.Lock()
	defer proxyRuntime.sessionMu.Unlock()

	proxyRuntime.mu.Lock()
	if existing := proxyRuntime.sessions[endpoint.ChainID]; existing != nil && existing.session != nil && !existing.session.IsClosed() {
		session := existing.session
		proxyRuntime.mu.Unlock()
		androidLogStore.add("proxy", "debug", "reuse frame session: chain="+strings.TrimSpace(endpoint.ChainID))
		return session, nil
	}
	proxyRuntime.mu.Unlock()
	conn, err := openAndroidProxyLinkRelayConn(item, endpoint)
	if err != nil {
		return nil, err
	}
	session, err := newMobileChainFrameClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	ready := session.WaitReady(proxyResponseReadTimeout)
	androidLogStore.add("proxy", "debug", "frame session ready: chain="+strings.TrimSpace(endpoint.ChainID)+" ready="+strconv.FormatBool(ready))
	if !ready {
		_ = session.Close()
		_ = conn.Close()
		return nil, errors.New("frame session negotiation timeout")
	}
	androidLogStore.add("proxy", "normal", "created frame session: chain="+strings.TrimSpace(endpoint.ChainID))
	proxyRuntime.mu.Lock()
	if old := proxyRuntime.sessions[endpoint.ChainID]; old != nil {
		closeProxyChainSession(old)
	}
	proxyRuntime.sessions[endpoint.ChainID] = &proxyChainSession{chainID: effectiveLinkRelayChainID(item), conn: conn, session: session}
	proxyRuntime.mu.Unlock()
	return session, nil
}

func openAndroidProxyLinkRelayConn(item linkChainServerItem, endpoint linkEndpoint) (net.Conn, error) {
	protocols := linkReachabilityProtocolsForEndpoint(item, endpoint)
	var lastErr error
	for _, protocol := range protocols {
		conn, err := openLinkRelayConn(endpoint, protocol, proxyConnectTimeout+proxyResponseReadTimeout)
		if err == nil {
			if normalizeLinkLayer(endpoint.LinkLayer) == "" {
				androidLogStore.add("proxy", "normal", "relay protocol selected: chain="+endpoint.ChainID+" protocol="+normalizeLinkLayer(protocol))
			}
			return conn, nil
		}
		lastErr = err
		androidLogStore.add("proxy", "warn", "relay protocol failed: chain="+endpoint.ChainID+" protocol="+normalizeLinkLayer(protocol)+" err="+err.Error())
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no supported relay protocol")
}

func invalidateProxyChainSession(chainID string, session *mobileChainFrameSession) {
	proxyRuntime.mu.Lock()
	existing := proxyRuntime.sessions[strings.TrimSpace(chainID)]
	if existing != nil && existing.session == session {
		delete(proxyRuntime.sessions, strings.TrimSpace(chainID))
	}
	proxyRuntime.mu.Unlock()
	if existing != nil {
		closeProxyChainSession(existing)
	}
}

func closeProxyChainSession(sess *proxyChainSession) {
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

func decideAndroidProxyRouteForTarget(configDir string, targetAddr string) (proxyRouteDecision, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return proxyRouteDecision{}, err
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" || strings.TrimSpace(port) == "" {
		return proxyRouteDecision{}, errors.New("invalid target address")
	}
	decision := proxyRouteDecision{Direct: true, TargetAddr: net.JoinHostPort(host, port), Group: "fallback"}
	if direct, reason := shouldForceDirectProxyTarget(configDir, host, port); direct {
		decision.Group = reason
		return decision, nil
	}
	groups, _ := loadProxyGroupFile(configDir)
	state, _ := loadProxyStateFile(configDir)
	matchGroup := "fallback"
	if ip := net.ParseIP(host); ip != nil {
		if hintedRoute, ok := lookupAndroidVPNDNSRouteHint(configDir, ip.String(), port); ok {
			return hintedRoute, nil
		}
		for _, item := range groups.Groups {
			if proxyIPMatchesCIDRRules(ip, item.Rules) {
				matchGroup = strings.TrimSpace(item.Group)
				break
			}
		}
	} else {
		for _, item := range groups.Groups {
			if proxyDomainMatchesRules(host, item.Rules) {
				matchGroup = strings.TrimSpace(item.Group)
				break
			}
		}
	}
	decision.Group = firstNonEmptyString(matchGroup, "fallback")
	for _, entry := range state.Groups {
		if !strings.EqualFold(strings.TrimSpace(entry.Group), decision.Group) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(entry.Action)) {
		case "reject":
			decision.Direct = false
			decision.Reject = true
		case "tunnel":
			decision.Direct = false
			decision.SelectedChainID = firstNonEmptyString(strings.TrimSpace(entry.SelectedChainID), selectedChainIDFromLegacy(entry.TunnelNodeID))
			if decision.SelectedChainID == "" {
				return proxyRouteDecision{}, errors.New("tunnel route missing selected_chain_id")
			}
		default:
			decision.Direct = true
		}
		break
	}
	return decision, nil
}

func shouldForceDirectProxyTarget(configDir string, host string, port string) (bool, string) {
	controllerHost, controllerPort := currentControllerDirectTarget()
	if targetHostPortMatches(host, port, controllerHost, controllerPort) {
		return true, "controller"
	}
	chains, err := loadLinkProxyChains(configDir)
	if err != nil {
		return false, ""
	}
	for _, item := range chains {
		endpoint, err := resolveLinkEndpoint(item)
		if err != nil {
			continue
		}
		if targetHostPortMatches(host, port, normalizeDirectTargetHost(endpoint.EntryHost), strconv.Itoa(endpoint.EntryPort)) {
			return true, "link_entry"
		}
	}
	return false, ""
}

func targetHostPortMatches(targetHost string, targetPort string, bypassHost string, bypassPort string) bool {
	targetHost = normalizeDirectTargetHost(targetHost)
	bypassHost = normalizeDirectTargetHost(bypassHost)
	targetPort = strings.TrimSpace(targetPort)
	bypassPort = strings.TrimSpace(bypassPort)
	if targetHost == "" || bypassHost == "" || targetPort == "" || bypassPort == "" || targetPort != bypassPort {
		return false
	}
	targetIP := net.ParseIP(targetHost)
	bypassIP := net.ParseIP(bypassHost)
	if targetIP != nil || bypassIP != nil {
		return targetIP != nil && bypassIP != nil && targetIP.Equal(bypassIP)
	}
	return strings.EqualFold(targetHost, bypassHost)
}

func normalizeDirectTargetHost(host string) string {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return strings.ToLower(strings.Trim(host, "."))
}

func loadProxyGroupFile(configDir string) (proxyGroupFile, error) {
	raw, err := os.ReadFile(filepath.Join(strings.TrimSpace(configDir), proxyGroupFileName))
	if err != nil {
		return proxyGroupFile{Groups: []proxyGroupEntry{}}, err
	}
	var payload proxyGroupFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return proxyGroupFile{Groups: []proxyGroupEntry{}}, err
	}
	if payload.Groups == nil {
		payload.Groups = []proxyGroupEntry{}
	}
	return payload, nil
}

func loadProxyStateFile(configDir string) (proxyStateFile, error) {
	raw, err := os.ReadFile(filepath.Join(strings.TrimSpace(configDir), proxyStateFileName))
	if err != nil {
		return proxyStateFile{Version: 1, Groups: []proxyStateGroup{}}, err
	}
	var payload proxyStateFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return proxyStateFile{Version: 1, Groups: []proxyStateGroup{}}, err
	}
	if payload.Groups == nil {
		payload.Groups = []proxyStateGroup{}
	}
	return payload, nil
}

func proxyDomainMatchesRules(domain string, rules []string) bool {
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" {
		return false
	}
	for _, rule := range rules {
		key, value, ok := splitProxyRule(rule)
		if !ok {
			continue
		}
		value = strings.ToLower(value)
		switch key {
		case "domain_suffix":
			if cleanDomain == value || strings.HasSuffix(cleanDomain, "."+value) {
				return true
			}
		case "domain_keyword":
			if strings.Contains(cleanDomain, value) {
				return true
			}
		case "domain_prefix":
			if strings.HasPrefix(cleanDomain, value) {
				return true
			}
		case "domain":
			if cleanDomain == value {
				return true
			}
		}
	}
	return false
}

func proxyIPMatchesCIDRRules(ip net.IP, rules []string) bool {
	if ip == nil {
		return false
	}
	for _, rule := range rules {
		key, value, ok := splitProxyRule(rule)
		if !ok || key != "cidr" {
			continue
		}
		_, network, err := net.ParseCIDR(value)
		if err == nil && network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func splitProxyRule(rule string) (string, string, bool) {
	trimmed := strings.TrimSpace(rule)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key := strings.ToLower(strings.TrimSpace(parts[0]))
	value := strings.TrimSpace(parts[1])
	return key, value, key != "" && value != ""
}

func relayProxyBidirectional(left net.Conn, leftReader *bufio.Reader, right net.Conn, rightReader *bufio.Reader, relay *androidProxyConnectionRelay) {
	done := make(chan struct{}, 2)
	go func() {
		defer func() {
			if relay != nil {
				relay.releaseSide()
			}
		}()
		rightWriter := io.Writer(right)
		if relay != nil {
			rightWriter = &androidProxyConnectionWriter{dst: right, relay: relay, direction: "up"}
		}
		if leftReader != nil && leftReader.Buffered() > 0 {
			if _, err := io.CopyN(rightWriter, leftReader, int64(leftReader.Buffered())); err != nil {
				globalAndroidProxyConnectionState.recordRelayFailure(relay, err)
			}
		}
		if _, err := mobileRelayCopy(rightWriter, left); err != nil {
			if relay != nil {
				relay.markCloseReason("up_" + classifyAndroidProxyRelayClose(err))
			}
			globalAndroidProxyConnectionState.recordRelayFailure(relay, err)
		} else if relay != nil {
			relay.markCloseReason("up_eof")
		}
		closeProxyConnWrite(right)
		done <- struct{}{}
	}()
	go func() {
		defer func() {
			if relay != nil {
				relay.releaseSide()
			}
		}()
		leftWriter := io.Writer(left)
		if relay != nil {
			leftWriter = &androidProxyConnectionWriter{dst: left, relay: relay, direction: "down"}
		}
		if rightReader != nil && rightReader.Buffered() > 0 {
			if _, err := io.CopyN(leftWriter, rightReader, int64(rightReader.Buffered())); err != nil {
				globalAndroidProxyConnectionState.recordRelayFailure(relay, err)
			}
		}
		if _, err := mobileRelayCopy(leftWriter, right); err != nil {
			if relay != nil {
				relay.markCloseReason("down_" + classifyAndroidProxyRelayClose(err))
			}
			globalAndroidProxyConnectionState.recordRelayFailure(relay, err)
		} else if relay != nil {
			relay.markCloseReason("down_eof")
		}
		closeProxyConnWrite(left)
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = left.Close()
	_ = right.Close()
}

func closeProxyConnWrite(conn net.Conn) {
	if conn == nil {
		return
	}
	if closer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
		return
	}
	if stream, ok := conn.(*mobileChainFrameStream); ok {
		_ = stream.Close()
	}
}

func readProxyFramedPacket(reader *bufio.Reader, payload []byte) (int, error) {
	var lengthBytes [2]byte
	if _, err := io.ReadFull(reader, lengthBytes[:]); err != nil {
		return 0, err
	}
	length := int(binary.BigEndian.Uint16(lengthBytes[:]))
	if length <= 0 {
		return 0, errors.New("invalid framed packet length")
	}
	if length > len(payload) {
		if _, err := io.CopyN(io.Discard, reader, int64(length)); err != nil {
			return 0, err
		}
		return 0, errors.New("framed packet payload exceeds read buffer")
	}
	if _, err := io.ReadFull(reader, payload[:length]); err != nil {
		return 0, err
	}
	return length, nil
}

func writeProxyFramedPacket(writer io.Writer, payload []byte) error {
	size := len(payload)
	if size <= 0 || size > 65535 {
		return errors.New("invalid framed packet payload")
	}
	frame := make([]byte, 2+size)
	binary.BigEndian.PutUint16(frame[:2], uint16(size))
	copy(frame[2:], payload)
	n, err := writer.Write(frame)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return io.ErrShortWrite
	}
	return nil
}

func proxyRuntimeConfigDir() string {
	proxyRuntime.mu.Lock()
	defer proxyRuntime.mu.Unlock()
	return proxyRuntime.configDir
}

func selectedChainIDFromLegacy(raw string) string {
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(value), "chain:") {
		return strings.TrimSpace(value[len("chain:"):])
	}
	return value
}

func formatProxyLegacyTunnelNodeID(selectedChainID string) string {
	selectedChainID = strings.TrimSpace(selectedChainID)
	if selectedChainID == "" {
		return ""
	}
	return "chain:" + selectedChainID
}

func buildProxyGroupStatus(groups proxyGroupFile, state proxyStateFile) []map[string]any {
	names := []string{}
	for _, group := range groups.Groups {
		name := strings.TrimSpace(group.Group)
		if name == "" || strings.EqualFold(name, "fallback") {
			continue
		}
		names = append(names, name)
	}
	names = append(names, "fallback")
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		entry := proxyStateGroup{Group: name, Action: "direct"}
		for _, stateEntry := range state.Groups {
			if strings.EqualFold(strings.TrimSpace(stateEntry.Group), name) {
				entry = stateEntry
				if strings.TrimSpace(entry.Action) == "" {
					entry.Action = "direct"
				}
				break
			}
		}
		out = append(out, map[string]any{
			"group":             name,
			"action":            strings.ToLower(strings.TrimSpace(firstNonEmptyString(entry.Action, "direct"))),
			"selected_chain_id": firstNonEmptyString(strings.TrimSpace(entry.SelectedChainID), selectedChainIDFromLegacy(entry.TunnelNodeID)),
		})
	}
	return out
}

func buildProxyChainStatus(chains []linkChainServerItem) []map[string]any {
	out := make([]map[string]any, 0, len(chains))
	for _, chain := range chains {
		out = append(out, map[string]any{
			"chain_id":        strings.TrimSpace(chain.ChainID),
			"relay_chain_id":  strings.TrimSpace(chain.RelayChainID),
			"client_entry_id": strings.TrimSpace(chain.ClientEntryID),
			"name":            strings.TrimSpace(chain.Name),
		})
	}
	return out
}
