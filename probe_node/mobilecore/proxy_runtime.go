package mobilecore

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	proxyGroupFileName       = "proxy_group.json"
	proxyStateFileName       = "proxy_state.json"
	proxyHTTPListenAddr      = "127.0.0.1:8080"
	proxySOCKS5ListenAddr    = "127.0.0.1:1080"
	proxyConnectTimeout      = 12 * time.Second
	proxyResponseReadTimeout = 10 * time.Second
)

var proxyRuntime = &androidProxyRuntime{sessions: map[string]*proxyChainSession{}}

type androidProxyRuntime struct {
	mu            sync.Mutex
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
	session *yamux.Session
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

type socksRequest struct {
	Version byte
	Cmd     byte
	Address string
}

// ProxyStart starts Android local HTTP and SOCKS5 proxy listeners.
func ProxyStart(configDir string) string {
	if strings.TrimSpace(configDir) == "" {
		return "proxy start failed: config dir is required"
	}
	proxyRuntime.mu.Lock()
	proxyRuntime.configDir = strings.TrimSpace(configDir)
	var errs []string
	if proxyRuntime.httpListener == nil {
		listener, err := net.Listen("tcp", proxyHTTPListenAddr)
		if err != nil {
			errs = append(errs, fmt.Sprintf("http %s: %v", proxyHTTPListenAddr, err))
		} else {
			proxyRuntime.httpListener = listener
			proxyRuntime.httpAddr = listener.Addr().String()
			go serveAndroidProxy(listener, "http")
		}
	}
	if proxyRuntime.socksListener == nil {
		listener, err := net.Listen("tcp", proxySOCKS5ListenAddr)
		if err != nil {
			errs = append(errs, fmt.Sprintf("socks5 %s: %v", proxySOCKS5ListenAddr, err))
		} else {
			proxyRuntime.socksListener = listener
			proxyRuntime.socksAddr = listener.Addr().String()
			go serveAndroidProxy(listener, "socks5")
		}
	}
	proxyRuntime.lastError = strings.Join(errs, "; ")
	proxyRuntime.updatedAt = time.Now().UTC().Format(time.RFC3339)
	httpAddr := proxyRuntime.httpAddr
	socksAddr := proxyRuntime.socksAddr
	lastErr := proxyRuntime.lastError
	proxyRuntime.mu.Unlock()

	if httpAddr == "" && socksAddr == "" {
		return "proxy start failed: " + firstNonEmptyString(lastErr, "listener unavailable")
	}
	if lastErr != "" {
		return "proxy partially started: " + lastErr
	}
	return fmt.Sprintf("proxy started: http=%s socks5=%s", httpAddr, socksAddr)
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

func serveAndroidProxy(listener net.Listener, protocol string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		if protocol == "http" {
			go handleAndroidHTTPProxyConn(conn)
		} else {
			go handleAndroidSOCKS5ProxyConn(conn)
		}
	}
}

func handleAndroidSOCKS5ProxyConn(conn net.Conn) {
	if conn == nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(proxyResponseReadTimeout))
	reader := bufio.NewReader(conn)
	req, err := readSOCKS5Request(reader, conn)
	if err != nil {
		return
	}
	if req.Cmd != 0x01 {
		_ = replySOCKS5(conn, req.Version, 0x07, "0.0.0.0:0")
		return
	}
	targetConn, err := openAndroidProxyTunnelStream("tcp", req.Address)
	if err != nil {
		_ = replySOCKS5(conn, req.Version, 0x05, "0.0.0.0:0")
		return
	}
	defer targetConn.Close()
	if err := replySOCKS5(conn, req.Version, 0x00, targetConn.LocalAddr().String()); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	_ = targetConn.SetDeadline(time.Time{})
	relayProxyBidirectional(conn, reader, targetConn, bufio.NewReader(targetConn))
}

func handleAndroidHTTPProxyConn(conn net.Conn) {
	if conn == nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(proxyResponseReadTimeout))
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	targetAddr := httpProxyTargetAddr(req)
	if targetAddr == "" {
		_ = writeHTTPProxyStatus(conn, http.StatusBadRequest, "invalid proxy target")
		return
	}
	targetConn, err := openAndroidProxyTunnelStream("tcp", targetAddr)
	if err != nil {
		_ = writeHTTPProxyStatus(conn, http.StatusBadGateway, err.Error())
		return
	}
	defer targetConn.Close()
	if strings.EqualFold(req.Method, http.MethodConnect) {
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = conn.SetDeadline(time.Time{})
		_ = targetConn.SetDeadline(time.Time{})
		relayProxyBidirectional(conn, reader, targetConn, bufio.NewReader(targetConn))
		return
	}
	req.RequestURI = ""
	if req.URL != nil {
		if strings.TrimSpace(req.URL.Scheme) == "" {
			req.URL.Scheme = "http"
		}
		if strings.TrimSpace(req.URL.Host) == "" {
			req.URL.Host = req.Host
		}
	}
	req.Header.Del("Proxy-Connection")
	if err := req.Write(targetConn); err != nil {
		_ = writeHTTPProxyStatus(conn, http.StatusBadGateway, "forward request failed")
		return
	}
	_ = conn.SetDeadline(time.Time{})
	_ = targetConn.SetDeadline(time.Time{})
	relayProxyBidirectional(conn, reader, targetConn, bufio.NewReader(targetConn))
}

func openAndroidProxyTunnelStream(network string, targetAddr string) (net.Conn, error) {
	if err := rejectAndroidProxyLocalTarget(targetAddr); err != nil {
		return nil, err
	}
	route, err := decideAndroidProxyRouteForTarget(proxyRuntimeConfigDir(), targetAddr)
	if err != nil {
		return nil, err
	}
	if route.Reject {
		return nil, fmt.Errorf("route rejected by group: %s", route.Group)
	}
	if route.Direct {
		dialer := net.Dialer{Timeout: proxyConnectTimeout}
		return dialer.Dial(strings.ToLower(strings.TrimSpace(network)), route.TargetAddr)
	}
	return openAndroidProxyChainStream(route.SelectedChainID, strings.ToLower(strings.TrimSpace(network)), route.TargetAddr)
}

func openAndroidProxyChainStream(selectedChainID string, network string, targetAddr string) (net.Conn, error) {
	item, endpoint, err := loadLinkEndpointByID(proxyRuntimeConfigDir(), selectedChainID)
	if err != nil {
		return nil, err
	}
	session, err := ensureProxyChainSession(item, endpoint)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		stream, err := session.Open()
		if err != nil {
			invalidateProxyChainSession(endpoint.ChainID, session)
			if attempt == 0 {
				session, err = ensureProxyChainSession(item, endpoint)
				if err == nil {
					continue
				}
			}
			return nil, err
		}
		request := linkTunnelOpenRequest{Type: "open", Network: network, Address: strings.TrimSpace(targetAddr)}
		_ = stream.SetWriteDeadline(time.Now().Add(proxyResponseReadTimeout))
		if err := json.NewEncoder(stream).Encode(request); err != nil {
			_ = stream.Close()
			invalidateProxyChainSession(endpoint.ChainID, session)
			if attempt == 0 {
				session, err = ensureProxyChainSession(item, endpoint)
				if err == nil {
					continue
				}
			}
			return nil, err
		}
		_ = stream.SetWriteDeadline(time.Time{})
		_ = stream.SetReadDeadline(time.Now().Add(proxyResponseReadTimeout))
		var response linkTunnelOpenResponse
		if err := json.NewDecoder(stream).Decode(&response); err != nil {
			_ = stream.Close()
			invalidateProxyChainSession(endpoint.ChainID, session)
			if attempt == 0 {
				session, err = ensureProxyChainSession(item, endpoint)
				if err == nil {
					continue
				}
			}
			return nil, err
		}
		_ = stream.SetReadDeadline(time.Time{})
		if !response.OK {
			_ = stream.Close()
			return nil, errors.New(firstNonEmptyString(strings.TrimSpace(response.Error), "open stream failed"))
		}
		return stream, nil
	}
	return nil, errors.New("open stream failed")
}

func openAndroidProxyChainPacketStream(selectedChainID string, network string, targetAddr string, association *linkAssociationV2Meta) (net.Conn, error) {
	item, endpoint, err := loadLinkEndpointByID(proxyRuntimeConfigDir(), selectedChainID)
	if err != nil {
		return nil, err
	}
	session, err := ensureProxyChainSession(item, endpoint)
	if err != nil {
		return nil, err
	}
	stream, err := session.Open()
	if err != nil {
		invalidateProxyChainSession(endpoint.ChainID, session)
		return nil, err
	}
	request := linkTunnelOpenRequest{
		Type:          "open",
		Network:       strings.ToLower(strings.TrimSpace(network)),
		Address:       strings.TrimSpace(targetAddr),
		AssociationV2: association,
	}
	_ = stream.SetWriteDeadline(time.Now().Add(proxyResponseReadTimeout))
	if err := json.NewEncoder(stream).Encode(request); err != nil {
		_ = stream.Close()
		invalidateProxyChainSession(endpoint.ChainID, session)
		return nil, err
	}
	_ = stream.SetWriteDeadline(time.Time{})
	_ = stream.SetReadDeadline(time.Now().Add(proxyResponseReadTimeout))
	var response linkTunnelOpenResponse
	if err := json.NewDecoder(stream).Decode(&response); err != nil {
		_ = stream.Close()
		invalidateProxyChainSession(endpoint.ChainID, session)
		return nil, err
	}
	_ = stream.SetReadDeadline(time.Time{})
	if !response.OK {
		_ = stream.Close()
		return nil, errors.New(firstNonEmptyString(strings.TrimSpace(response.Error), "open stream failed"))
	}
	return stream, nil
}

func ensureProxyChainSession(item linkChainServerItem, endpoint linkEndpoint) (*yamux.Session, error) {
	proxyRuntime.mu.Lock()
	if existing := proxyRuntime.sessions[endpoint.ChainID]; existing != nil && existing.session != nil && !existing.session.IsClosed() {
		session := existing.session
		proxyRuntime.mu.Unlock()
		return session, nil
	}
	proxyRuntime.mu.Unlock()
	conn, err := openAndroidProxyLinkRelayConn(item, endpoint)
	if err != nil {
		return nil, err
	}
	session, err := yamux.Client(conn, newLinkYamuxConfig())
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
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
			if normalizeLinkLayer(endpoint.LinkLayer) == "auto" {
				androidLogStore.add("proxy", "normal", "auto relay protocol selected: chain="+endpoint.ChainID+" protocol="+normalizeLinkLayer(protocol))
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

func invalidateProxyChainSession(chainID string, session *yamux.Session) {
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

func readSOCKS5Request(reader *bufio.Reader, conn net.Conn) (socksRequest, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return socksRequest{}, err
	}
	if header[0] != 0x05 {
		return socksRequest{}, errors.New("unsupported socks version")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return socksRequest{}, err
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return socksRequest{}, err
	}
	reqHeader := make([]byte, 4)
	if _, err := io.ReadFull(reader, reqHeader); err != nil {
		return socksRequest{}, err
	}
	if reqHeader[0] != 0x05 {
		return socksRequest{}, errors.New("unsupported socks request version")
	}
	host, err := readSOCKS5Addr(reader, reqHeader[3])
	if err != nil {
		return socksRequest{}, err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return socksRequest{}, err
	}
	port := int(portBytes[0])<<8 | int(portBytes[1])
	return socksRequest{Version: reqHeader[0], Cmd: reqHeader[1], Address: net.JoinHostPort(host, strconv.Itoa(port))}, nil
}

func readSOCKS5Addr(reader *bufio.Reader, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case 0x03:
		size, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		buf := make([]byte, int(size))
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	case 0x04:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	default:
		return "", errors.New("unsupported socks address type")
	}
}

func replySOCKS5(conn net.Conn, version byte, code byte, bindAddr string) error {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(bindAddr))
	if err != nil {
		host, portText = "0.0.0.0", "0"
	}
	port, _ := strconv.Atoi(portText)
	ip := net.ParseIP(strings.Trim(host, "[]")).To4()
	if ip == nil {
		ip = net.IPv4zero
	}
	resp := []byte{version, code, 0x00, 0x01, ip[0], ip[1], ip[2], ip[3], byte(port >> 8), byte(port)}
	_, err = conn.Write(resp)
	return err
}

func httpProxyTargetAddr(req *http.Request) string {
	if req == nil {
		return ""
	}
	host := strings.TrimSpace(req.Host)
	if req.URL != nil && strings.TrimSpace(req.URL.Host) != "" {
		host = strings.TrimSpace(req.URL.Host)
	}
	if strings.EqualFold(req.Method, http.MethodConnect) {
		host = strings.TrimSpace(req.Host)
	}
	if host == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	port := "80"
	if req.URL != nil && strings.EqualFold(req.URL.Scheme, "https") {
		port = "443"
	}
	if strings.EqualFold(req.Method, http.MethodConnect) {
		port = "443"
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), port)
}

func writeHTTPProxyStatus(conn net.Conn, status int, message string) error {
	text := http.StatusText(status)
	if strings.TrimSpace(message) != "" {
		text = strings.TrimSpace(message)
	}
	_, err := fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status, text)
	return err
}

func relayProxyBidirectional(left net.Conn, leftReader *bufio.Reader, right net.Conn, rightReader *bufio.Reader) {
	done := make(chan struct{}, 2)
	go func() {
		if leftReader != nil && leftReader.Buffered() > 0 {
			_, _ = io.CopyN(right, leftReader, int64(leftReader.Buffered()))
		}
		_, _ = io.Copy(right, left)
		closeProxyConnWrite(right)
		done <- struct{}{}
	}()
	go func() {
		if rightReader != nil && rightReader.Buffered() > 0 {
			_, _ = io.CopyN(left, rightReader, int64(rightReader.Buffered()))
		}
		_, _ = io.Copy(left, right)
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
	if stream, ok := conn.(*yamux.Stream); ok {
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

func rejectAndroidProxyLocalTarget(targetAddr string) error {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return err
	}
	host = strings.Trim(host, "[]")
	if isProxySelfPort(portText) && isProxyLocalHost(host) {
		return fmt.Errorf("proxy loopback target blocked: %s", targetAddr)
	}
	return nil
}

func isProxySelfPort(portText string) bool {
	for _, addr := range []string{proxyHTTPListenAddr, proxySOCKS5ListenAddr} {
		_, port, err := net.SplitHostPort(addr)
		if err == nil && port == strings.TrimSpace(portText) {
			return true
		}
	}
	return false
}

func isProxyLocalHost(host string) bool {
	clean := strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]")))
	return clean == "localhost" || clean == "127.0.0.1" || clean == "::1"
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
