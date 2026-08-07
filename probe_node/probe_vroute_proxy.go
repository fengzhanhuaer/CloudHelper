package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/txthinking/socks5"
)

const (
	probeVRouteProxyDefaultHTTPListen   = "127.0.0.1:18080"
	probeVRouteProxyDefaultSOCKS5Listen = "127.0.0.1:18081"
	probeVRouteProxyHTTPHeaderTimeout   = 15 * time.Second
	probeVRouteProxyRecoveryInterval    = 15 * time.Second
)

type probeVRouteProxyListenerRuntime struct {
	httpListener  net.Listener
	httpServer    *http.Server
	httpTransport *http.Transport
	socksListener *net.TCPListener
	socksUDP      *net.UDPConn
	connections   map[net.Conn]struct{}
	settings      probeVirtualRouterLocalSettings
	startedAt     time.Time
	closeOnce     sync.Once
	mu            sync.RWMutex
}

var probeVRouteProxyRuntimeState = struct {
	reconcileMu  sync.Mutex
	mu           sync.RWMutex
	runtime      *probeVRouteProxyListenerRuntime
	lastError    string
	updatedAt    time.Time
	httpRequests atomic.Uint64
	socksTCP     atomic.Uint64
	socksUDP     atomic.Uint64
}{}

var (
	probeVRouteSystemProxySet     = setProbeVRouteSystemProxy
	probeVRouteSystemProxyRestore = restoreProbeVRouteSystemProxy
)

var probeVRouteProxyRecoveryLoopOnce sync.Once

func sanitizeProbeVRouteProxySettings(settings probeVirtualRouterLocalSettings) (probeVirtualRouterLocalSettings, error) {
	settings.HTTPProxyListen = strings.TrimSpace(settings.HTTPProxyListen)
	settings.SOCKS5ProxyListen = strings.TrimSpace(settings.SOCKS5ProxyListen)
	settings.ProxyUsername = strings.TrimSpace(settings.ProxyUsername)
	if settings.HTTPProxyListen == "" {
		settings.HTTPProxyListen = probeVRouteProxyDefaultHTTPListen
	}
	if settings.SOCKS5ProxyListen == "" {
		settings.SOCKS5ProxyListen = probeVRouteProxyDefaultSOCKS5Listen
	}
	if _, err := validateProbeVRouteProxyListen(settings.HTTPProxyListen); err != nil {
		return settings, fmt.Errorf("invalid http proxy listen: %w", err)
	}
	if _, err := validateProbeVRouteProxyListen(settings.SOCKS5ProxyListen); err != nil {
		return settings, fmt.Errorf("invalid socks5 proxy listen: %w", err)
	}
	if strings.EqualFold(settings.HTTPProxyListen, settings.SOCKS5ProxyListen) {
		return settings, errors.New("http and socks5 proxy listen addresses must be different")
	}
	if (settings.ProxyUsername == "") != (settings.ProxyPassword == "") {
		return settings, errors.New("proxy username and password must both be configured")
	}
	if settings.ProxyEnabled && settings.ProxyUsername == "" {
		if !probeVRouteProxyListenIsLoopback(settings.HTTPProxyListen) || !probeVRouteProxyListenIsLoopback(settings.SOCKS5ProxyListen) {
			return settings, errors.New("non-loopback proxy listen requires username and password")
		}
	}
	return settings, nil
}

func validateProbeVRouteProxyListen(value string) (*net.TCPAddr, error) {
	addr, err := net.ResolveTCPAddr("tcp", strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	if addr.Port <= 0 || addr.Port > 65535 {
		return nil, errors.New("listen port must be between 1 and 65535")
	}
	return addr, nil
}

func probeVRouteProxyListenIsLoopback(value string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func reconcileProbeVRouteProxyRuntime(settings probeVirtualRouterLocalSettings) error {
	settings, err := sanitizeProbeVRouteProxySettings(settings)
	if err != nil {
		return err
	}
	probeVRouteProxyRuntimeState.reconcileMu.Lock()
	defer probeVRouteProxyRuntimeState.reconcileMu.Unlock()
	probeVRouteProxyRuntimeState.mu.RLock()
	current := probeVRouteProxyRuntimeState.runtime
	probeVRouteProxyRuntimeState.mu.RUnlock()
	if !settings.ProxyEnabled {
		if err := probeVRouteSystemProxyRestore(); err != nil {
			setProbeVRouteProxyRuntime(current, err.Error())
			return fmt.Errorf("restore windows system proxy: %w", err)
		}
		if current != nil {
			current.close()
		}
		setProbeVRouteProxyRuntime(nil, "")
		return nil
	}
	if current != nil && current.sameListeners(settings) {
		current.updateSettings(settings)
		if err := probeVRouteSystemProxySet(current.httpListener.Addr().String(), current.socksListener.Addr().String()); err != nil {
			setProbeVRouteProxyRuntime(current, err.Error())
			return fmt.Errorf("set windows system proxy: %w", err)
		}
		setProbeVRouteProxyRuntime(current, "")
		return nil
	}
	var oldSettings probeVirtualRouterLocalSettings
	if current != nil {
		oldSettings = current.currentSettings()
		current.close()
	}
	next, err := startProbeVRouteProxyListenerRuntime(settings)
	if err != nil {
		setProbeVRouteProxyRuntime(nil, err.Error())
		if current != nil {
			if restored, restoreErr := startProbeVRouteProxyListenerRuntime(oldSettings); restoreErr == nil {
				setProbeVRouteProxyRuntime(restored, err.Error())
			} else {
				logProbeWarnf("probe vroute proxy listener rollback failed: err=%v", restoreErr)
			}
		}
		return err
	}
	if err := probeVRouteSystemProxySet(next.httpListener.Addr().String(), next.socksListener.Addr().String()); err != nil {
		next.close()
		var restored *probeVRouteProxyListenerRuntime
		var restoreErr error
		if current != nil {
			if restored, restoreErr = startProbeVRouteProxyListenerRuntime(oldSettings); restoreErr != nil {
				logProbeWarnf("probe vroute proxy listener rollback after system proxy failure failed: err=%v", restoreErr)
			} else if systemRestoreErr := probeVRouteSystemProxySet(restored.httpListener.Addr().String(), restored.socksListener.Addr().String()); systemRestoreErr != nil {
				logProbeWarnf("probe vroute system proxy rollback failed: err=%v", systemRestoreErr)
			}
		} else if restoreErr := probeVRouteSystemProxyRestore(); restoreErr != nil {
			logProbeWarnf("probe vroute system proxy restore after startup failure failed: err=%v", restoreErr)
		}
		setProbeVRouteProxyRuntime(restored, err.Error())
		return fmt.Errorf("set windows system proxy: %w", err)
	}
	setProbeVRouteProxyRuntime(next, "")
	return nil
}

func startProbeVRouteProxyRecoveryLoop() {
	if !probeVRouteSystemProxyRequired() {
		return
	}
	probeVRouteProxyRecoveryLoopOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(probeVRouteProxyRecoveryInterval)
			defer ticker.Stop()
			for range ticker.C {
				if err := recoverProbeVRouteProxyRuntimeOnce(); err != nil {
					logProbeWarnf("probe vroute proxy recovery failed: err=%v", err)
				}
			}
		}()
	})
}

func recoverProbeVRouteProxyRuntimeOnce() error {
	if !probeVRouteSystemProxyRequired() {
		return nil
	}
	settings := loadProbeVirtualRouterLocalSettings()
	if !settings.ProxyEnabled {
		return probeVRouteSystemProxyRestore()
	}
	applied, _, _ := snapshotProbeVRouteSystemProxy()
	if applied {
		return nil
	}
	return reconcileProbeVRouteProxyRuntime(settings)
}

func setProbeVRouteProxyRuntime(runtime *probeVRouteProxyListenerRuntime, lastError string) {
	probeVRouteProxyRuntimeState.mu.Lock()
	probeVRouteProxyRuntimeState.runtime = runtime
	probeVRouteProxyRuntimeState.lastError = strings.TrimSpace(lastError)
	probeVRouteProxyRuntimeState.updatedAt = time.Now()
	probeVRouteProxyRuntimeState.mu.Unlock()
}

func startProbeVRouteProxyListenerRuntime(settings probeVirtualRouterLocalSettings) (*probeVRouteProxyListenerRuntime, error) {
	httpListener, err := net.Listen("tcp", settings.HTTPProxyListen)
	if err != nil {
		return nil, fmt.Errorf("listen http proxy %s: %w", settings.HTTPProxyListen, err)
	}
	socksAddr, _ := validateProbeVRouteProxyListen(settings.SOCKS5ProxyListen)
	socksListener, err := net.ListenTCP("tcp", socksAddr)
	if err != nil {
		_ = httpListener.Close()
		return nil, fmt.Errorf("listen socks5 proxy tcp %s: %w", settings.SOCKS5ProxyListen, err)
	}
	socksUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: socksAddr.IP, Port: socksAddr.Port, Zone: socksAddr.Zone})
	if err != nil {
		_ = socksListener.Close()
		_ = httpListener.Close()
		return nil, fmt.Errorf("listen socks5 proxy udp %s: %w", settings.SOCKS5ProxyListen, err)
	}
	runtime := &probeVRouteProxyListenerRuntime{
		httpListener:  httpListener,
		socksListener: socksListener,
		socksUDP:      socksUDP,
		connections:   make(map[net.Conn]struct{}),
		settings:      settings,
		startedAt:     time.Now(),
	}
	runtime.httpTransport = &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network string, address string) (net.Conn, error) {
			if !strings.HasPrefix(strings.ToLower(network), "tcp") {
				return nil, fmt.Errorf("unsupported http proxy network: %s", network)
			}
			conn, _, err := dialProbeVRouteProxyTCP(address)
			return conn, err
		},
		ForceAttemptHTTP2: false,
	}
	runtime.httpServer = &http.Server{
		Handler:           runtime,
		ReadHeaderTimeout: probeVRouteProxyHTTPHeaderTimeout,
	}
	go runtime.serveHTTP()
	go runtime.serveSOCKSTCP()
	go runtime.serveSOCKSUDP()
	logProbeInfof("probe vroute proxy listening: http=%s socks5=%s udp=true", httpListener.Addr(), socksListener.Addr())
	return runtime, nil
}

func (r *probeVRouteProxyListenerRuntime) serveHTTP() {
	err := r.httpServer.Serve(r.httpListener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		logProbeWarnf("probe vroute http proxy stopped: err=%v", err)
	}
}

func (r *probeVRouteProxyListenerRuntime) serveSOCKSTCP() {
	for {
		conn, err := r.socksListener.AcceptTCP()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logProbeWarnf("probe vroute socks5 tcp accept failed: err=%v", err)
			}
			return
		}
		r.trackConnection(conn, true)
		go func(client *net.TCPConn) {
			defer r.trackConnection(client, false)
			defer client.Close()
			server := r.newSOCKSServer()
			if err := server.Negotiate(client); err != nil {
				return
			}
			request, err := server.GetRequest(client)
			if err != nil {
				return
			}
			_ = (&probeVRouteProxySOCKSHandler{runtime: r}).TCPHandle(server, client, request)
		}(conn)
	}
}

func (r *probeVRouteProxyListenerRuntime) newSOCKSServer() *socks5.Server {
	settings := r.currentSettings()
	method := byte(socks5.MethodNone)
	if settings.ProxyUsername == "" {
		method = socks5.MethodNone
	} else {
		method = socks5.MethodUsernamePassword
	}
	return &socks5.Server{
		UserName:          settings.ProxyUsername,
		Password:          settings.ProxyPassword,
		Method:            method,
		SupportedCommands: []byte{socks5.CmdConnect, socks5.CmdUDP},
		Addr:              settings.SOCKS5ProxyListen,
		ServerAddr:        r.socksUDP.LocalAddr(),
		UDPConn:           r.socksUDP,
	}
}

func (r *probeVRouteProxyListenerRuntime) serveSOCKSUDP() {
	buffer := make([]byte, 65507)
	for {
		n, clientAddr, err := r.socksUDP.ReadFromUDP(buffer)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logProbeWarnf("probe vroute socks5 udp read failed: err=%v", err)
			}
			return
		}
		datagram, err := socks5.NewDatagramFromBytes(buffer[:n])
		if err != nil || datagram.Frag != 0 {
			continue
		}
		payload := append([]byte(nil), datagram.Data...)
		targetAddr := datagram.Address()
		go func() {
			association := findProbeVRouteProxyUDPAssociation(clientAddr)
			if association == nil {
				return
			}
			if err := relayProbeVRouteProxyUDP(association, targetAddr, payload); err != nil {
				logProbeWarnf("probe vroute socks5 udp relay failed: target=%s err=%v", targetAddr, err)
				return
			}
			probeVRouteProxyRuntimeState.socksUDP.Add(1)
		}()
	}
}

type probeVRouteProxySOCKSHandler struct {
	runtime *probeVRouteProxyListenerRuntime
}

func (h *probeVRouteProxySOCKSHandler) TCPHandle(server *socks5.Server, client *net.TCPConn, request *socks5.Request) error {
	switch request.Cmd {
	case socks5.CmdConnect:
		remote, _, err := dialProbeVRouteProxyTCP(request.Address())
		if err != nil {
			_ = writeProbeVRouteProxySOCKSReply(client, socks5.RepHostUnreachable, nil)
			return err
		}
		defer remote.Close()
		if err := writeProbeVRouteProxySOCKSReply(client, socks5.RepSuccess, remote.LocalAddr()); err != nil {
			return err
		}
		probeVRouteProxyRuntimeState.socksTCP.Add(1)
		pipeProbeVRouteProxyConnections(client, remote)
		return nil
	case socks5.CmdUDP:
		clientHost, _, _ := net.SplitHostPort(client.RemoteAddr().String())
		association, err := registerProbeVRouteProxyUDPAssociation(clientHost, h.runtime.socksUDP)
		if err != nil {
			_ = writeProbeVRouteProxySOCKSReply(client, socks5.RepServerFailure, nil)
			return err
		}
		defer association.close(true)
		if err := writeProbeVRouteProxySOCKSReply(client, socks5.RepSuccess, h.runtime.socksUDP.LocalAddr()); err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, client)
		return nil
	default:
		_ = writeProbeVRouteProxySOCKSReply(client, socks5.RepCommandNotSupported, nil)
		return socks5.ErrUnsupportCmd
	}
}

func (h *probeVRouteProxySOCKSHandler) UDPHandle(*socks5.Server, *net.UDPAddr, *socks5.Datagram) error {
	return nil
}

func writeProbeVRouteProxySOCKSReply(conn net.Conn, code byte, bound net.Addr) error {
	address := "0.0.0.0:0"
	if bound != nil {
		address = bound.String()
	}
	atyp, addr, port, err := socks5.ParseAddress(address)
	if err != nil {
		atyp = socks5.ATYPIPv4
		addr = []byte{0, 0, 0, 0}
		port = []byte{0, 0}
	}
	if atyp == socks5.ATYPDomain && len(addr) > 0 {
		addr = addr[1:]
	}
	_, err = socks5.NewReply(code, atyp, addr, port).WriteTo(conn)
	return err
}

func (r *probeVRouteProxyListenerRuntime) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if !r.authorizeHTTP(w, request) {
		return
	}
	probeVRouteProxyRuntimeState.httpRequests.Add(1)
	if request.Method == http.MethodConnect {
		r.serveHTTPConnect(w, request)
		return
	}
	r.serveHTTPForward(w, request)
}

func (r *probeVRouteProxyListenerRuntime) authorizeHTTP(w http.ResponseWriter, request *http.Request) bool {
	settings := r.currentSettings()
	if settings.ProxyUsername == "" {
		return true
	}
	value := strings.TrimSpace(request.Header.Get("Proxy-Authorization"))
	const prefix = "basic "
	if len(value) < len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="CloudHelper VRoute Proxy"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value[len(prefix):]))
	parts := strings.SplitN(string(decoded), ":", 2)
	username, password, ok := "", "", err == nil && len(parts) == 2
	if ok {
		username, password = parts[0], parts[1]
	}
	if !ok || subtle.ConstantTimeCompare([]byte(username), []byte(settings.ProxyUsername)) != 1 || subtle.ConstantTimeCompare([]byte(password), []byte(settings.ProxyPassword)) != 1 {
		w.Header().Set("Proxy-Authenticate", `Basic realm="CloudHelper VRoute Proxy"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return false
	}
	return true
}

func (r *probeVRouteProxyListenerRuntime) serveHTTPConnect(w http.ResponseWriter, request *http.Request) {
	target := strings.TrimSpace(request.Host)
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "443")
	}
	remote, _, err := dialProbeVRouteProxyTCP(target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = remote.Close()
		http.Error(w, "hijacking is unsupported", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = remote.Close()
		return
	}
	defer client.Close()
	defer remote.Close()
	_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	if err := buffered.Flush(); err != nil {
		return
	}
	pipeProbeVRouteProxyConnections(client, remote)
}

func (r *probeVRouteProxyListenerRuntime) serveHTTPForward(w http.ResponseWriter, request *http.Request) {
	clone := request.Clone(request.Context())
	clone.RequestURI = ""
	clone.Header = request.Header.Clone()
	clone.Header.Del("Proxy-Authorization")
	clone.Header.Del("Proxy-Connection")
	if clone.URL.Scheme == "" {
		clone.URL.Scheme = "http"
	}
	if clone.URL.Host == "" {
		clone.URL.Host = request.Host
	}
	response, err := r.httpTransport.RoundTrip(clone)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	copyProbeVRouteProxyHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func copyProbeVRouteProxyHeaders(target http.Header, source http.Header) {
	for key, values := range source {
		if probeVRouteProxyHopHeader(key) {
			continue
		}
		for _, value := range values {
			target.Add(key, value)
		}
	}
}

func probeVRouteProxyHopHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func pipeProbeVRouteProxyConnections(left net.Conn, right net.Conn) {
	done := make(chan struct{}, 2)
	copyOne := func(dst net.Conn, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if closer, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go copyOne(left, right)
	go copyOne(right, left)
	<-done
	_ = left.Close()
	_ = right.Close()
	<-done
}

func (r *probeVRouteProxyListenerRuntime) trackConnection(conn net.Conn, add bool) {
	r.mu.Lock()
	if add {
		r.connections[conn] = struct{}{}
	} else {
		delete(r.connections, conn)
	}
	r.mu.Unlock()
}

func (r *probeVRouteProxyListenerRuntime) sameListeners(settings probeVirtualRouterLocalSettings) bool {
	current := r.currentSettings()
	return strings.EqualFold(current.HTTPProxyListen, settings.HTTPProxyListen) && strings.EqualFold(current.SOCKS5ProxyListen, settings.SOCKS5ProxyListen)
}

func (r *probeVRouteProxyListenerRuntime) currentSettings() probeVirtualRouterLocalSettings {
	r.mu.RLock()
	settings := r.settings
	r.mu.RUnlock()
	return settings
}

func (r *probeVRouteProxyListenerRuntime) updateSettings(settings probeVirtualRouterLocalSettings) {
	r.mu.Lock()
	r.settings = settings
	r.mu.Unlock()
}

func (r *probeVRouteProxyListenerRuntime) close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		if r.httpTransport != nil {
			r.httpTransport.CloseIdleConnections()
		}
		if r.httpServer != nil {
			_ = r.httpServer.Close()
		}
		if r.httpListener != nil {
			_ = r.httpListener.Close()
		}
		if r.socksListener != nil {
			_ = r.socksListener.Close()
		}
		if r.socksUDP != nil {
			_ = r.socksUDP.Close()
		}
		r.mu.Lock()
		connections := make([]net.Conn, 0, len(r.connections))
		for conn := range r.connections {
			connections = append(connections, conn)
		}
		r.mu.Unlock()
		for _, conn := range connections {
			_ = conn.Close()
		}
		closeProbeVRouteProxySourceSessions("proxy listener stopped")
	})
}

func closeProbeVRouteProxySourceSessions(reason string) {
	probeVRouteProxyTCPState.mu.RLock()
	tcpSessions := make([]*probeVRouteProxyTCPSession, 0)
	for _, session := range probeVRouteProxyTCPState.sessions {
		if session != nil && session.role == "source" {
			tcpSessions = append(tcpSessions, session)
		}
	}
	probeVRouteProxyTCPState.mu.RUnlock()
	for _, session := range tcpSessions {
		session.close(true, errors.New(reason))
	}
	probeVRouteProxyUDPState.mu.RLock()
	associations := make([]*probeVRouteProxyUDPAssociation, 0, len(probeVRouteProxyUDPState.associations))
	for _, association := range probeVRouteProxyUDPState.associations {
		associations = append(associations, association)
	}
	probeVRouteProxyUDPState.mu.RUnlock()
	for _, association := range associations {
		association.close(true)
	}
}

func snapshotProbeVRouteProxyRuntime() map[string]any {
	probeVRouteProxyRuntimeState.mu.RLock()
	runtime := probeVRouteProxyRuntimeState.runtime
	lastError := probeVRouteProxyRuntimeState.lastError
	updatedAt := probeVRouteProxyRuntimeState.updatedAt
	probeVRouteProxyRuntimeState.mu.RUnlock()
	settings := loadProbeVirtualRouterLocalSettings()
	activeConnections := 0
	httpListen := ""
	socksListen := ""
	startedAt := time.Time{}
	if runtime != nil {
		runtime.mu.RLock()
		activeConnections = len(runtime.connections)
		startedAt = runtime.startedAt
		runtime.mu.RUnlock()
		if runtime.httpListener != nil {
			httpListen = runtime.httpListener.Addr().String()
		}
		if runtime.socksListener != nil {
			socksListen = runtime.socksListener.Addr().String()
		}
	}
	probeVRouteProxyTCPState.mu.RLock()
	tcpSource, tcpExit := 0, 0
	for _, session := range probeVRouteProxyTCPState.sessions {
		if session.role == "source" {
			tcpSource++
		} else {
			tcpExit++
		}
	}
	probeVRouteProxyTCPState.mu.RUnlock()
	probeVRouteProxyUDPState.mu.RLock()
	udpAssociations := len(probeVRouteProxyUDPState.associations)
	udpExitSessions := len(probeVRouteProxyUDPState.exitSessions)
	probeVRouteProxyUDPState.mu.RUnlock()
	systemProxyApplied, systemProxyHTTP, systemProxySOCKS5 := snapshotProbeVRouteSystemProxy()
	return map[string]any{
		"enabled":                   settings.ProxyEnabled,
		"running":                   runtime != nil,
		"http_listen":               firstNonEmpty(httpListen, settings.HTTPProxyListen),
		"socks5_listen":             firstNonEmpty(socksListen, settings.SOCKS5ProxyListen),
		"udp_enabled":               true,
		"authentication":            settings.ProxyUsername != "",
		"system_proxy_applied":      systemProxyApplied,
		"system_proxy_http":         systemProxyHTTP,
		"system_proxy_socks5":       systemProxySOCKS5,
		"active_client_connections": activeConnections,
		"tcp_source_sessions":       tcpSource,
		"tcp_exit_sessions":         tcpExit,
		"udp_associations":          udpAssociations,
		"udp_exit_sessions":         udpExitSessions,
		"http_requests_total":       probeVRouteProxyRuntimeState.httpRequests.Load(),
		"socks_tcp_total":           probeVRouteProxyRuntimeState.socksTCP.Load(),
		"socks_udp_total":           probeVRouteProxyRuntimeState.socksUDP.Load(),
		"tcp_tx_bytes":              probeVRouteProxyTCPState.txBytes.Load(),
		"tcp_rx_bytes":              probeVRouteProxyTCPState.rxBytes.Load(),
		"udp_tx_bytes":              probeVRouteProxyUDPState.txBytes.Load(),
		"udp_rx_bytes":              probeVRouteProxyUDPState.rxBytes.Load(),
		"last_error":                lastError,
		"started_at":                formatProbeVRouteProxyTime(startedAt),
		"updated_at":                formatProbeVRouteProxyTime(updatedAt),
	}
}

func formatProbeVRouteProxyTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func stopProbeVRouteProxyRuntime() {
	probeVRouteProxyRuntimeState.reconcileMu.Lock()
	defer probeVRouteProxyRuntimeState.reconcileMu.Unlock()
	probeVRouteProxyRuntimeState.mu.RLock()
	runtime := probeVRouteProxyRuntimeState.runtime
	probeVRouteProxyRuntimeState.mu.RUnlock()
	if err := probeVRouteSystemProxyRestore(); err != nil {
		logProbeWarnf("probe vroute system proxy restore failed: err=%v", err)
	}
	if runtime != nil {
		runtime.close()
	}
	setProbeVRouteProxyRuntime(nil, "")
}

func resetProbeVRouteProxyRuntimeForTest() {
	stopProbeVRouteProxyRuntime()
	resetProbeVRouteProxyTCPStateForTest()
	resetProbeVRouteProxyUDPStateForTest()
}

var _ socks5.Handler = (*probeVRouteProxySOCKSHandler)(nil)
