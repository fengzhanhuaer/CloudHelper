package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeLocalExplicitProxySOCKSListenAddr = "127.0.0.1:1080"
	probeLocalExplicitProxyHTTPListenAddr  = "127.0.0.1:8080"
)

var probeLocalExplicitProxyState = struct {
	mu            sync.Mutex
	socksListener net.Listener
	httpListener  net.Listener
	socksAddr     string
	httpAddr      string
	lastError     string
	updatedAt     string
}{}

func startProbeLocalExplicitProxyServer() {
	probeLocalExplicitProxyState.mu.Lock()
	defer probeLocalExplicitProxyState.mu.Unlock()

	var errs []string
	if probeLocalExplicitProxyState.socksListener == nil {
		listener, err := net.Listen("tcp", probeLocalExplicitProxySOCKSListenAddr)
		if err != nil {
			errs = append(errs, fmt.Sprintf("socks5 %s: %v", probeLocalExplicitProxySOCKSListenAddr, err))
		} else {
			probeLocalExplicitProxyState.socksListener = listener
			probeLocalExplicitProxyState.socksAddr = listener.Addr().String()
			go serveProbeLocalExplicitProxy(listener, "socks5")
			logProbeInfof("probe local explicit socks5 proxy listening: listen=%s", listener.Addr().String())
		}
	}
	if probeLocalExplicitProxyState.httpListener == nil {
		listener, err := net.Listen("tcp", probeLocalExplicitProxyHTTPListenAddr)
		if err != nil {
			errs = append(errs, fmt.Sprintf("http %s: %v", probeLocalExplicitProxyHTTPListenAddr, err))
		} else {
			probeLocalExplicitProxyState.httpListener = listener
			probeLocalExplicitProxyState.httpAddr = listener.Addr().String()
			go serveProbeLocalExplicitProxy(listener, "http")
			logProbeInfof("probe local explicit http proxy listening: listen=%s", listener.Addr().String())
		}
	}
	probeLocalExplicitProxyState.updatedAt = time.Now().UTC().Format(time.RFC3339)
	probeLocalExplicitProxyState.lastError = strings.Join(errs, "; ")
	if probeLocalExplicitProxyState.lastError != "" {
		logProbeWarnf("probe local explicit proxy partially unavailable: %s", probeLocalExplicitProxyState.lastError)
	}
	if err := applyProbeLocalExplicitProxySystemSettings(probeLocalExplicitProxyState.httpAddr, probeLocalExplicitProxyState.socksAddr); err != nil {
		if probeLocalExplicitProxyState.lastError != "" {
			probeLocalExplicitProxyState.lastError += "; "
		}
		probeLocalExplicitProxyState.lastError += "system settings: " + strings.TrimSpace(err.Error())
		logProbeWarnf("probe local explicit proxy system settings failed: %v", err)
	}
}

func stopProbeLocalExplicitProxyServer() {
	probeLocalExplicitProxyState.mu.Lock()
	socksListener := probeLocalExplicitProxyState.socksListener
	httpListener := probeLocalExplicitProxyState.httpListener
	probeLocalExplicitProxyState.socksListener = nil
	probeLocalExplicitProxyState.httpListener = nil
	probeLocalExplicitProxyState.socksAddr = ""
	probeLocalExplicitProxyState.httpAddr = ""
	probeLocalExplicitProxyState.lastError = ""
	probeLocalExplicitProxyState.updatedAt = time.Now().UTC().Format(time.RFC3339)
	probeLocalExplicitProxyState.mu.Unlock()

	if socksListener != nil {
		_ = socksListener.Close()
	}
	if httpListener != nil {
		_ = httpListener.Close()
	}
	if err := restoreProbeLocalExplicitProxySystemSettings(); err != nil {
		logProbeWarnf("probe local explicit proxy system settings restore failed: %v", err)
	}
}

func snapshotProbeLocalExplicitProxyStatus() map[string]any {
	probeLocalExplicitProxyState.mu.Lock()
	defer probeLocalExplicitProxyState.mu.Unlock()
	return map[string]any{
		"socks5_enabled": probeLocalExplicitProxyState.socksListener != nil,
		"socks5_addr":    strings.TrimSpace(probeLocalExplicitProxyState.socksAddr),
		"http_enabled":   probeLocalExplicitProxyState.httpListener != nil,
		"http_addr":      strings.TrimSpace(probeLocalExplicitProxyState.httpAddr),
		"last_error":     strings.TrimSpace(probeLocalExplicitProxyState.lastError),
		"updated_at":     strings.TrimSpace(probeLocalExplicitProxyState.updatedAt),
		"system":         snapshotProbeLocalExplicitProxySystemSettingsStatus(),
	}
}

func serveProbeLocalExplicitProxy(listener net.Listener, protocol string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logProbeWarnf("probe local explicit %s proxy accept failed: listen=%s err=%v", protocol, listener.Addr().String(), err)
			}
			return
		}
		if strings.EqualFold(protocol, "http") {
			go handleProbeLocalExplicitHTTPProxyConn(conn)
		} else {
			go handleProbeLocalExplicitSOCKSProxyConn(conn)
		}
	}
}

func handleProbeLocalExplicitSOCKSProxyConn(conn net.Conn) {
	if conn == nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	reader := bufio.NewReader(conn)
	request, err := readProbeChainSocksRequest(reader, conn)
	if err != nil {
		return
	}
	if request.Cmd != 0x01 {
		if request.Cmd == 0x03 && request.Version == 0x05 {
			if err := handleProbeLocalExplicitSOCKS5UDPAssociate(conn, reader, request.Version); err != nil {
				logProbeWarnf("probe local explicit socks5 udp associate failed: err=%v", err)
			}
			return
		}
		_ = replyProbeChainProxyFailure(conn, request.Version)
		return
	}
	targetConn, err := openProbeLocalExplicitProxyTunnelStream("tcp", request.Address)
	if err != nil {
		_ = replyProbeChainProxyFailure(conn, request.Version)
		logProbeWarnf("probe local explicit socks5 proxy tunnel open failed: target=%s err=%v", request.Address, err)
		return
	}
	defer targetConn.Close()
	if err := replyProbeChainProxySuccess(conn, request.Version, targetConn.LocalAddr().String()); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	_ = targetConn.SetDeadline(time.Time{})
	relayProbeChainBidirectional(conn, reader, targetConn, bufio.NewReader(targetConn))
}

type probeLocalExplicitSOCKS5UDPAssociation struct {
	target string
	stream net.Conn
	reader *bufio.Reader
	mu     sync.Mutex
}

func handleProbeLocalExplicitSOCKS5UDPAssociate(conn net.Conn, reader *bufio.Reader, version byte) error {
	udpPacketConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		_ = replyProbeChainProxyFailure(conn, version)
		return err
	}
	udpConn, ok := udpPacketConn.(*net.UDPConn)
	if !ok {
		_ = udpPacketConn.Close()
		_ = replyProbeChainProxyFailure(conn, version)
		return errors.New("udp listener is not udp conn")
	}
	defer udpConn.Close()
	if err := replyProbeChainProxySuccess(conn, version, udpConn.LocalAddr().String()); err != nil {
		return err
	}

	_ = conn.SetDeadline(time.Time{})
	controlDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(controlDone)
		_ = udpConn.Close()
	}()

	associations := make(map[string]*probeLocalExplicitSOCKS5UDPAssociation)
	assocMu := &sync.Mutex{}
	defer func() {
		assocMu.Lock()
		defer assocMu.Unlock()
		for _, assoc := range associations {
			if assoc != nil && assoc.stream != nil {
				_ = assoc.stream.Close()
			}
		}
	}()

	buffer := make([]byte, 64*1024)
	var clientAddr net.Addr
	for {
		_ = udpConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, fromAddr, readErr := udpConn.ReadFrom(buffer)
		if readErr != nil {
			select {
			case <-controlDone:
				return nil
			default:
			}
			if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return readErr
		}
		if clientAddr == nil {
			clientAddr = fromAddr
		} else if fromAddr.String() != clientAddr.String() {
			continue
		}
		targetAddr, payload, parseErr := parseProbeChainSocks5UDPDatagram(buffer[:n])
		if parseErr != nil || len(payload) == 0 {
			continue
		}
		assoc, assocErr := getProbeLocalExplicitSOCKS5UDPAssociation(associations, assocMu, targetAddr, fromAddr, udpConn)
		if assocErr != nil {
			logProbeWarnf("probe local explicit socks5 udp tunnel open failed: target=%s err=%v", targetAddr, assocErr)
			continue
		}
		assoc.mu.Lock()
		writeErr := writeProbeChainFramedPacket(assoc.stream, payload)
		assoc.mu.Unlock()
		if writeErr != nil {
			assocMu.Lock()
			if associations[targetAddr] == assoc {
				delete(associations, targetAddr)
			}
			assocMu.Unlock()
			_ = assoc.stream.Close()
		}
	}
}

func getProbeLocalExplicitSOCKS5UDPAssociation(associations map[string]*probeLocalExplicitSOCKS5UDPAssociation, assocMu *sync.Mutex, targetAddr string, clientAddr net.Addr, udpConn *net.UDPConn) (*probeLocalExplicitSOCKS5UDPAssociation, error) {
	target := strings.TrimSpace(targetAddr)
	if target == "" {
		return nil, errors.New("empty udp target")
	}
	assocMu.Lock()
	if assoc := associations[target]; assoc != nil {
		assocMu.Unlock()
		return assoc, nil
	}
	assocMu.Unlock()

	stream, err := openProbeLocalExplicitProxyUDPTunnelStream(target, clientAddr)
	if err != nil {
		return nil, err
	}
	assoc := &probeLocalExplicitSOCKS5UDPAssociation{
		target: target,
		stream: stream,
		reader: bufio.NewReader(stream),
	}
	assocMu.Lock()
	if existing := associations[target]; existing != nil {
		assocMu.Unlock()
		_ = stream.Close()
		return existing, nil
	}
	associations[target] = assoc
	assocMu.Unlock()

	go relayProbeLocalExplicitSOCKS5UDPAssociationDownstream(assoc, associations, assocMu, clientAddr, udpConn)
	return assoc, nil
}

func relayProbeLocalExplicitSOCKS5UDPAssociationDownstream(assoc *probeLocalExplicitSOCKS5UDPAssociation, associations map[string]*probeLocalExplicitSOCKS5UDPAssociation, assocMu *sync.Mutex, clientAddr net.Addr, udpConn *net.UDPConn) {
	defer func() {
		assocMu.Lock()
		if assoc != nil && associations[assoc.target] == assoc {
			delete(associations, assoc.target)
		}
		assocMu.Unlock()
		if assoc != nil && assoc.stream != nil {
			_ = assoc.stream.Close()
		}
	}()
	if assoc == nil || assoc.reader == nil || udpConn == nil || clientAddr == nil {
		return
	}
	for {
		payload, err := readProbeChainFramedPacket(assoc.reader)
		if err != nil {
			return
		}
		packet, err := buildProbeChainSocks5UDPDatagram(assoc.target, payload)
		if err != nil {
			continue
		}
		if _, err := udpConn.WriteTo(packet, clientAddr); err != nil {
			return
		}
	}
}

func handleProbeLocalExplicitHTTPProxyConn(conn net.Conn) {
	if conn == nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	reader := bufio.NewReader(conn)
	request, err := http.ReadRequest(reader)
	if err != nil {
		_ = writeProbeChainHTTPProxyStatus(conn, http.StatusBadRequest, "invalid proxy request")
		return
	}
	defer request.Body.Close()
	targetAddr, err := resolveProbeChainHTTPProxyTarget(request)
	if err != nil {
		_ = writeProbeChainHTTPProxyStatus(conn, http.StatusBadRequest, "invalid proxy target")
		return
	}
	targetConn, err := openProbeLocalExplicitProxyTunnelStream("tcp", targetAddr)
	if err != nil {
		_ = writeProbeChainHTTPProxyStatus(conn, http.StatusBadGateway, "open tunnel failed")
		logProbeWarnf("probe local explicit http proxy tunnel open failed: target=%s err=%v", targetAddr, err)
		return
	}
	defer targetConn.Close()
	if strings.EqualFold(strings.TrimSpace(request.Method), http.MethodConnect) {
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		_ = conn.SetDeadline(time.Time{})
		_ = targetConn.SetDeadline(time.Time{})
		relayProbeChainBidirectional(conn, reader, targetConn, bufio.NewReader(targetConn))
		return
	}
	request.RequestURI = ""
	if request.URL != nil {
		if strings.TrimSpace(request.URL.Scheme) == "" {
			request.URL.Scheme = "http"
		}
		if strings.TrimSpace(request.URL.Host) == "" {
			request.URL.Host = request.Host
		}
	}
	request.Header.Del("Proxy-Connection")
	if err := request.Write(targetConn); err != nil {
		_ = writeProbeChainHTTPProxyStatus(conn, http.StatusBadGateway, "forward request failed")
		return
	}
	_ = conn.SetDeadline(time.Time{})
	_ = targetConn.SetDeadline(time.Time{})
	relayProbeChainBidirectional(conn, reader, targetConn, bufio.NewReader(targetConn))
}

func openProbeLocalExplicitProxyTunnelStream(network string, targetAddr string) (net.Conn, error) {
	if !isProbeLocalProxyTunnelModeEnabled() {
		return nil, errors.New("proxy is not enabled")
	}
	rt, err := resolveProbeLocalExplicitProxyRuntime()
	if err != nil {
		return nil, err
	}
	return rt.openStream(network, targetAddr, nil)
}

func openProbeLocalExplicitProxyUDPTunnelStream(targetAddr string, clientAddr net.Addr) (net.Conn, error) {
	if !isProbeLocalProxyTunnelModeEnabled() {
		return nil, errors.New("proxy is not enabled")
	}
	rt, err := resolveProbeLocalExplicitProxyRuntime()
	if err != nil {
		return nil, err
	}
	association := buildProbeLocalExplicitProxyUDPAssociationMeta(rt, targetAddr, clientAddr)
	return rt.openStream("udp", targetAddr, association)
}

func buildProbeLocalExplicitProxyUDPAssociationMeta(rt *probeLocalTUNGroupRuntime, targetAddr string, clientAddr net.Addr) *probeChainAssociationV2Meta {
	meta := &probeChainAssociationV2Meta{
		Version:         2,
		Transport:       "udp",
		RouteTarget:     strings.TrimSpace(targetAddr),
		CreatedAtUnixMS: time.Now().UnixMilli(),
		NATMode:         "socks5_udp",
	}
	if rt != nil {
		rt.mu.Lock()
		meta.RouteGroup = strings.TrimSpace(rt.Group)
		meta.RouteNodeID = strings.TrimSpace(rt.SelectedChainID)
		rt.mu.Unlock()
	}
	if clientUDPAddr, ok := clientAddr.(*net.UDPAddr); ok && clientUDPAddr != nil {
		meta.SourceKey = clientUDPAddr.String()
		meta.SrcIP = clientUDPAddr.IP.String()
		meta.SrcPort = uint16(clientUDPAddr.Port)
	}
	if host, portText, err := net.SplitHostPort(strings.TrimSpace(targetAddr)); err == nil {
		if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
			meta.DstIP = ip.String()
			if ip.To4() != nil {
				meta.IPFamily = 4
			} else if ip.To16() != nil {
				meta.IPFamily = 6
			}
		}
		if port, err := strconv.Atoi(strings.TrimSpace(portText)); err == nil && port > 0 && port <= 65535 {
			meta.DstPort = uint16(port)
		}
	}
	meta.AssocKeyV2 = strings.Join([]string{meta.RouteGroup, meta.RouteNodeID, meta.SourceKey, strings.TrimSpace(targetAddr)}, "|")
	meta.FlowID = meta.AssocKeyV2
	return meta
}

func resolveProbeLocalExplicitProxyRuntime() (*probeLocalTUNGroupRuntime, error) {
	state := currentProbeLocalProxyViewState()
	for _, entry := range state.Groups {
		if !strings.EqualFold(strings.TrimSpace(entry.Action), "tunnel") {
			continue
		}
		group := strings.TrimSpace(entry.Group)
		selectedChainID := firstNonEmpty(strings.TrimSpace(entry.SelectedChainID), mustProbeLocalSelectedChainIDFromLegacy(entry.TunnelNodeID))
		if group == "" || selectedChainID == "" {
			continue
		}
		rt, err := ensureProbeLocalTUNGroupRuntime(group, selectedChainID)
		if err != nil {
			return nil, err
		}
		return rt, nil
	}
	return nil, errors.New("no selected tunnel chain")
}
