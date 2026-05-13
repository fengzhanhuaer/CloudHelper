package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	probeLocalDNSListenHost      = "127.0.0.1"
	probeLocalDNSPrimaryPort     = 53
	probeLocalDNSFallbackPort    = 5353
	probeLocalDNSReadBufferSize  = 4096
	probeLocalDNSUpstreamTimeout = 5 * time.Second
	probeLocalDNSDoHReadLimit    = 64 * 1024
	probeLocalDNSCacheTTL        = 15 * 24 * time.Hour
	probeLocalFakeIPDefaultCIDR  = "198.18.0.0/15"
)

type probeLocalDNSStatus struct {
	Enabled      bool   `json:"enabled"`
	ListenAddr   string `json:"listen_addr,omitempty"`
	Port         int    `json:"port,omitempty"`
	FallbackUsed bool   `json:"fallback_used"`
	LastError    string `json:"last_error,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type probeLocalDNSCacheRecord struct {
	URL string `json:"url"`
	IP  string `json:"ip"`
}

type probeLocalDNSUpstreamCandidate struct {
	Kind    string
	Address string
}

type probeLocalDNSCacheEntry struct {
	URL       string
	IP        string
	UpdatedAt time.Time
	ExpiresAt time.Time
}

type probeLocalDNSRouteDecision struct {
	Group           string
	Action          string
	SelectedChainID string
	TunnelNodeID    string
	Reject          bool
}

type probeLocalDNSFakeIPRuntimeEntry struct {
	Domain    string
	Decision  probeLocalDNSRouteDecision
	ExpiresAt time.Time
}

type probeLocalDNSFakeIPEntry struct {
	Domain          string `json:"domain"`
	FakeIP          string `json:"fake_ip"`
	Group           string `json:"group,omitempty"`
	Action          string `json:"action,omitempty"`
	SelectedChainID string `json:"selected_chain_id,omitempty"`
	TunnelNodeID    string `json:"tunnel_node_id,omitempty"`
	ExpiresAt       string `json:"expires_at"`
}

type probeLocalDNSRouteHintEntry struct {
	Domain    string
	Decision  probeLocalDNSRouteDecision
	ExpiresAt time.Time
}

var probeLocalDNSState = struct {
	mu             sync.Mutex
	started        bool
	conn           net.PacketConn
	tunConn        net.PacketConn
	status         probeLocalDNSStatus
	tunStatus      probeLocalDNSStatus
	cache          map[string]probeLocalDNSCacheEntry
	fakeCIDR       string
	fakeNetwork    *net.IPNet
	fakeCursor     uint32
	fakeDomainToIP map[string]string
	fakeIPToEntry  map[string]probeLocalDNSFakeIPRuntimeEntry
	routeHints     map[string]probeLocalDNSRouteHintEntry
}{
	cache:          make(map[string]probeLocalDNSCacheEntry),
	fakeDomainToIP: make(map[string]string),
	fakeIPToEntry:  make(map[string]probeLocalDNSFakeIPRuntimeEntry),
	routeHints:     make(map[string]probeLocalDNSRouteHintEntry),
	status: probeLocalDNSStatus{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	},
	tunStatus: probeLocalDNSStatus{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	},
}

var (
	probeLocalDNSListenPacket                = net.ListenPacket
	probeLocalDNSNow                         = time.Now
	probeLocalDNSSystemServers               = currentProbeLocalSystemDNSServers
	probeLocalDNSEnsureDirectBypassForTarget = func(string) error {
		return nil
	}
)

func defaultProbeLocalDNSServers() []string {
	return []string{"223.5.5.5", "119.29.29.29"}
}

func defaultProbeLocalDoTServers() []string {
	return []string{"dns.alidns.com:853", "dot.pub:853"}
}

func defaultProbeLocalDoHServers() []string {
	return []string{"https://dns.alidns.com/dns-query", "https://doh.pub/dns-query"}
}

func defaultProbeLocalDoHProxyServers() []string {
	return []string{"https://cloudflare-dns.com/dns-query", "https://dns.google/dns-query"}
}

func ensureProbeLocalDNSServiceStarted() {
	probeLocalDNSState.mu.Lock()
	alreadyStarted := probeLocalDNSState.started
	if !alreadyStarted {
		probeLocalDNSState.started = true
	}
	probeLocalDNSState.mu.Unlock()

	if !alreadyStarted {
		startProbeLocalDNSLoopbackListener()
	}
	reconcileProbeLocalDNSRuntime()
}

func reconcileProbeLocalDNSRuntime() {
	if runtime.GOOS != "windows" {
		stopProbeLocalDNSTUNListener()
		return
	}
	host := strings.TrimSpace(currentProbeLocalTUNDNSListenHost())
	if host == "" {
		stopProbeLocalDNSTUNListener()
		return
	}
	startProbeLocalDNSTUNListener(host)
}

func startProbeLocalDNSLoopbackListener() {
	candidates := []struct {
		port         int
		fallbackUsed bool
	}{
		{port: probeLocalDNSPrimaryPort, fallbackUsed: false},
		{port: probeLocalDNSFallbackPort, fallbackUsed: true},
	}

	var lastErr error
	for _, candidate := range candidates {
		addr := net.JoinHostPort(probeLocalDNSListenHost, strconv.Itoa(candidate.port))
		conn, err := probeLocalDNSListenPacket("udp", addr)
		if err != nil {
			lastErr = err
			continue
		}
		now := probeLocalDNSNow().UTC()
		probeLocalDNSState.mu.Lock()
		probeLocalDNSState.conn = conn
		probeLocalDNSState.status = probeLocalDNSStatus{
			Enabled:      true,
			ListenAddr:   addr,
			Port:         candidate.port,
			FallbackUsed: candidate.fallbackUsed,
			LastError:    "",
			UpdatedAt:    now.Format(time.RFC3339),
		}
		probeLocalDNSState.mu.Unlock()
		logProbeInfof("probe local dns service enabled: listen=%s fallback_used=%t", addr, candidate.fallbackUsed)
		go serveProbeLocalDNS(conn)
		return
	}

	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	probeLocalDNSState.status = probeLocalDNSStatus{
		Enabled:      false,
		ListenAddr:   "",
		Port:         0,
		FallbackUsed: false,
		LastError:    strings.TrimSpace(errorString(lastErr)),
		UpdatedAt:    now.Format(time.RFC3339),
	}
	probeLocalDNSState.mu.Unlock()
	if lastErr != nil {
		logProbeWarnf("probe local dns service startup failed: %v", lastErr)
	}
}

func startProbeLocalDNSTUNListener(host string) {
	cleanHost := strings.TrimSpace(host)
	ip := net.ParseIP(cleanHost)
	if ip == nil || ip.To4() == nil {
		stopProbeLocalDNSTUNListener()
		return
	}
	addr := net.JoinHostPort(cleanHost, strconv.Itoa(probeLocalDNSPrimaryPort))
	probeLocalDNSState.mu.Lock()
	if probeLocalDNSState.tunConn != nil && probeLocalDNSState.tunStatus.Enabled && strings.EqualFold(strings.TrimSpace(probeLocalDNSState.tunStatus.ListenAddr), addr) {
		probeLocalDNSState.mu.Unlock()
		return
	}
	oldConn := probeLocalDNSState.tunConn
	probeLocalDNSState.tunConn = nil
	probeLocalDNSState.tunStatus = probeLocalDNSStatus{
		Enabled:      false,
		ListenAddr:   addr,
		Port:         probeLocalDNSPrimaryPort,
		FallbackUsed: false,
		LastError:    "",
		UpdatedAt:    probeLocalDNSNow().UTC().Format(time.RFC3339),
	}
	probeLocalDNSState.mu.Unlock()
	if oldConn != nil {
		_ = oldConn.Close()
	}
	conn, err := probeLocalDNSListenPacket("udp", addr)
	if err != nil {
		probeLocalDNSState.mu.Lock()
		probeLocalDNSState.tunStatus.Enabled = false
		probeLocalDNSState.tunStatus.LastError = strings.TrimSpace(errorString(err))
		probeLocalDNSState.tunStatus.UpdatedAt = probeLocalDNSNow().UTC().Format(time.RFC3339)
		probeLocalDNSState.mu.Unlock()
		logProbeWarnf("probe local dns tun listener startup failed: %v", err)
		return
	}
	probeLocalDNSState.mu.Lock()
	probeLocalDNSState.tunConn = conn
	probeLocalDNSState.tunStatus = probeLocalDNSStatus{
		Enabled:      true,
		ListenAddr:   addr,
		Port:         probeLocalDNSPrimaryPort,
		FallbackUsed: false,
		LastError:    "",
		UpdatedAt:    probeLocalDNSNow().UTC().Format(time.RFC3339),
	}
	probeLocalDNSState.mu.Unlock()
	logProbeInfof("probe local dns tun listener enabled: listen=%s", addr)
	go serveProbeLocalDNS(conn)
}

func stopProbeLocalDNSTUNListener() {
	probeLocalDNSState.mu.Lock()
	conn := probeLocalDNSState.tunConn
	probeLocalDNSState.tunConn = nil
	probeLocalDNSState.tunStatus = probeLocalDNSStatus{
		Enabled:      false,
		ListenAddr:   "",
		Port:         0,
		FallbackUsed: false,
		LastError:    "",
		UpdatedAt:    probeLocalDNSNow().UTC().Format(time.RFC3339),
	}
	probeLocalDNSState.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func serveProbeLocalDNS(conn net.PacketConn) {
	if conn == nil {
		return
	}
	buf := make([]byte, probeLocalDNSReadBufferSize)
	for {
		n, remoteAddr, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || isProbeLocalDNSClosedErr(err) {
				return
			}
			updateProbeLocalDNSStatusError(err)
			continue
		}
		packet := append([]byte(nil), buf[:n]...)
		go handleProbeLocalDNSPacket(conn, remoteAddr, packet)
	}
}

func handleProbeLocalDNSPacket(conn net.PacketConn, remoteAddr net.Addr, packet []byte) {
	if conn == nil || remoteAddr == nil || len(packet) == 0 {
		return
	}
	response, domain, ips, err := resolveProbeLocalDNSResponse(packet)
	if err != nil {
		updateProbeLocalDNSStatusError(err)
		logProbeWarnf("probe local dns resolve failed: %v", err)
	}
	if len(response) == 0 {
		response = buildProbeLocalDNSServfail(packet)
	}
	if len(response) > 0 {
		_, _ = conn.WriteTo(response, remoteAddr)
	}
	if domain != "" && len(ips) > 0 {
		storeProbeLocalDNSCacheRecords(domain, ips)
	}
}

func resolveProbeLocalDNSResponse(packet []byte) ([]byte, string, []string, error) {
	domain, qType := parseProbeLocalDNSQueryDomainAndType(packet)
	decision := resolveProbeLocalDNSRouteDecision(domain)
	if decision.Reject {
		return buildProbeLocalDNSRefused(packet), domain, nil, nil
	}
	if shouldUseProbeLocalDNSFakeIP(domain, qType, decision) {
		if fakeIP, ok := allocateProbeLocalDNSFakeIP(domain, decision); ok {
			return buildProbeLocalDNSSuccessA(packet, fakeIP), domain, nil, nil
		}
	}
	candidates := currentProbeLocalDNSUpstreamCandidatesForDecision(decision)
	if len(candidates) == 0 {
		return nil, domain, nil, errors.New("dns upstream list is empty")
	}
	var lastErr error
	for _, candidate := range candidates {
		var response []byte
		var err error
		switch candidate.Kind {
		case "doh":
			response, err = queryProbeLocalDNSViaDoH(candidate.Address, packet)
		case "dot":
			response, err = queryProbeLocalDNSViaDoT(candidate.Address, packet)
		case "dns":
			response, err = queryProbeLocalDNSViaPlain(candidate.Address, packet)
		default:
			continue
		}
		if err != nil {
			lastErr = err
			continue
		}
		if len(response) == 0 {
			lastErr = errors.New("empty dns response payload")
			continue
		}
		ips, _ := extractProbeLocalDNSResponseIPs(response)
		return response, domain, ips, nil
	}
	if lastErr == nil {
		lastErr = errors.New("dns upstream resolve failed")
	}
	return nil, domain, nil, lastErr
}

func currentProbeLocalDNSUpstreamCandidatesForDecision(_ probeLocalDNSRouteDecision) []probeLocalDNSUpstreamCandidate {
	cfg, err := loadProbeLocalProxyGroupFile()
	if err != nil {
		logProbeWarnf("load proxy_group for dns upstream failed, use defaults: %v", err)
		cfg = defaultProbeLocalProxyGroupFile()
	}
	candidates := make([]probeLocalDNSUpstreamCandidate, 0, 20)
	seen := make(map[string]struct{}, 16)
	appendDoH := func(items []string) {
		for _, item := range items {
			normalized, ok := normalizeProbeLocalDoHURL(item)
			if !ok {
				continue
			}
			key := "doh|" + normalized
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			candidates = append(candidates, probeLocalDNSUpstreamCandidate{Kind: "doh", Address: normalized})
		}
	}
	appendHostPort := func(kind string, items []string, defaultPort string) {
		for _, item := range items {
			normalized, ok := normalizeProbeLocalDNSHostPort(item, defaultPort)
			if !ok {
				continue
			}
			key := kind + "|" + normalized
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			candidates = append(candidates, probeLocalDNSUpstreamCandidate{Kind: kind, Address: normalized})
		}
	}

	appendDoH(cfg.DoHProxyServers)
	appendDoH(cfg.DoHServers)
	appendHostPort("dot", cfg.DoTServers, "853")
	appendHostPort("dns", cfg.DNSServers, "53")
	appendHostPort("dns", probeLocalDNSSystemServers(), "53")
	return candidates
}

func resolveProbeLocalDNSUpstreamBypassTarget(kind string, address string) (string, bool) {
	cleanKind := strings.ToLower(strings.TrimSpace(kind))
	switch cleanKind {
	case "dns", "dot":
		defaultPort := "53"
		if cleanKind == "dot" {
			defaultPort = "853"
		}
		cleanAddress, ok := normalizeProbeLocalDNSHostPort(address, defaultPort)
		if !ok {
			return "", false
		}
		host, port, err := net.SplitHostPort(cleanAddress)
		if err != nil {
			return "", false
		}
		parsedIP := net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]")))
		if parsedIP == nil || parsedIP.To4() == nil {
			return "", false
		}
		return net.JoinHostPort(parsedIP.String(), strings.TrimSpace(port)), true
	case "doh":
		cleanEndpoint, ok := normalizeProbeLocalDoHURL(address)
		if !ok {
			return "", false
		}
		parsed, err := url.Parse(cleanEndpoint)
		if err != nil {
			return "", false
		}
		host := strings.TrimSpace(parsed.Hostname())
		parsedIP := net.ParseIP(host)
		if parsedIP == nil || parsedIP.To4() == nil {
			return "", false
		}
		port := strings.TrimSpace(parsed.Port())
		if port == "" {
			if strings.EqualFold(strings.TrimSpace(parsed.Scheme), "http") {
				port = "80"
			} else {
				port = "443"
			}
		}
		portNum, convErr := strconv.Atoi(port)
		if convErr != nil || portNum <= 0 || portNum > 65535 {
			return "", false
		}
		return net.JoinHostPort(parsedIP.String(), strconv.Itoa(portNum)), true
	default:
		return "", false
	}
}

func ensureProbeLocalDNSUpstreamDirectBypass(kind string, address string) {
	_ = kind
	_ = address
}

func queryProbeLocalDNSViaDoH(endpoint string, packet []byte) ([]byte, error) {
	cleanEndpoint, ok := normalizeProbeLocalDoHURL(endpoint)
	if !ok {
		return nil, fmt.Errorf("invalid doh endpoint: %q", endpoint)
	}
	ensureProbeLocalDNSUpstreamDirectBypass("doh", cleanEndpoint)
	ctx, cancel := context.WithTimeout(context.Background(), probeLocalDNSUpstreamTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cleanEndpoint, bytes.NewReader(packet))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/dns-message")
	request.Header.Set("Accept", "application/dns-message")
	client := &http.Client{Timeout: probeLocalDNSUpstreamTimeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("doh upstream status=%d", response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, probeLocalDNSDoHReadLimit))
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, errors.New("doh upstream returned empty payload")
	}
	return payload, nil
}

func queryProbeLocalDNSViaDoT(address string, packet []byte) ([]byte, error) {
	cleanAddress, ok := normalizeProbeLocalDNSHostPort(address, "853")
	if !ok {
		return nil, fmt.Errorf("invalid dot upstream: %q", address)
	}
	ensureProbeLocalDNSUpstreamDirectBypass("dot", cleanAddress)
	dialer := &net.Dialer{Timeout: probeLocalDNSUpstreamTimeout}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if host := probeLocalDNSHostFromAddress(cleanAddress); host != "" && net.ParseIP(host) == nil {
		tlsConfig.ServerName = host
	}
	conn, err := tls.DialWithDialer(dialer, "tcp", cleanAddress, tlsConfig)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(probeLocalDNSNow().Add(probeLocalDNSUpstreamTimeout))
	lengthPrefix := make([]byte, 2)
	binary.BigEndian.PutUint16(lengthPrefix, uint16(len(packet)))
	if _, err := conn.Write(append(lengthPrefix, packet...)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(conn, lengthPrefix); err != nil {
		return nil, err
	}
	responseLen := int(binary.BigEndian.Uint16(lengthPrefix))
	if responseLen <= 0 || responseLen > 65535 {
		return nil, fmt.Errorf("invalid dot payload length=%d", responseLen)
	}
	payload := make([]byte, responseLen)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func queryProbeLocalDNSViaPlain(address string, packet []byte) ([]byte, error) {
	cleanAddress, ok := normalizeProbeLocalDNSHostPort(address, "53")
	if !ok {
		return nil, fmt.Errorf("invalid dns upstream: %q", address)
	}
	ensureProbeLocalDNSUpstreamDirectBypass("dns", cleanAddress)
	conn, err := net.DialTimeout("udp", cleanAddress, probeLocalDNSUpstreamTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(probeLocalDNSNow().Add(probeLocalDNSUpstreamTimeout))
	if _, err := conn.Write(packet); err != nil {
		return nil, err
	}
	response := make([]byte, probeLocalDNSReadBufferSize)
	n, err := conn.Read(response)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, errors.New("dns upstream returned empty payload")
	}
	return append([]byte(nil), response[:n]...), nil
}

func parseProbeLocalDNSQueryDomainAndType(packet []byte) (string, dnsmessage.Type) {
	parser := dnsmessage.Parser{}
	if _, err := parser.Start(packet); err != nil {
		return "", dnsmessage.TypeA
	}
	question, err := parser.Question()
	if err != nil {
		return "", dnsmessage.TypeA
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.ToLower(question.Name.String()), "."))
	return name, question.Type
}

func extractProbeLocalDNSResponseIPs(packet []byte) ([]string, error) {
	parser := dnsmessage.Parser{}
	if _, err := parser.Start(packet); err != nil {
		return nil, err
	}
	for {
		if _, err := parser.Question(); err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			return nil, err
		}
	}
	seen := make(map[string]struct{}, 4)
	ips := make([]string, 0, 4)
	for {
		answerHeader, err := parser.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return ips, err
		}
		switch answerHeader.Type {
		case dnsmessage.TypeA:
			answer, err := parser.AResource()
			if err != nil {
				return ips, err
			}
			ip := net.IP(answer.A[:]).String()
			if ip == "" {
				continue
			}
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			ips = append(ips, ip)
		case dnsmessage.TypeAAAA:
			answer, err := parser.AAAAResource()
			if err != nil {
				return ips, err
			}
			ip := net.IP(answer.AAAA[:]).String()
			if ip == "" {
				continue
			}
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			ips = append(ips, ip)
		default:
			if err := parser.SkipAnswer(); err != nil {
				return ips, err
			}
		}
	}
	return ips, nil
}

func buildProbeLocalDNSRefused(request []byte) []byte {
	return buildProbeLocalDNSResponseWithRCode(request, dnsmessage.RCodeRefused)
}

func buildProbeLocalDNSServfail(request []byte) []byte {
	return buildProbeLocalDNSResponseWithRCode(request, dnsmessage.RCodeServerFailure)
}

func buildProbeLocalDNSResponseWithRCode(request []byte, rcode dnsmessage.RCode) []byte {
	parser := dnsmessage.Parser{}
	requestHeader, err := parser.Start(request)
	if err != nil {
		return nil
	}
	questions := make([]dnsmessage.Question, 0, 1)
	for {
		question, qErr := parser.Question()
		if qErr != nil {
			if errors.Is(qErr, dnsmessage.ErrSectionDone) {
				break
			}
			return nil
		}
		questions = append(questions, question)
	}
	responseHeader := dnsmessage.Header{
		ID:                 requestHeader.ID,
		Response:           true,
		OpCode:             requestHeader.OpCode,
		RecursionDesired:   requestHeader.RecursionDesired,
		RecursionAvailable: true,
		RCode:              rcode,
	}
	builder := dnsmessage.NewBuilder(nil, responseHeader)
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil
	}
	for _, question := range questions {
		if err := builder.Question(question); err != nil {
			return nil
		}
	}
	message, err := builder.Finish()
	if err != nil {
		return nil
	}
	return message
}

func buildProbeLocalDNSSuccessA(request []byte, ip string) []byte {
	parser := dnsmessage.Parser{}
	requestHeader, err := parser.Start(request)
	if err != nil {
		return nil
	}
	questions := make([]dnsmessage.Question, 0, 1)
	var firstA *dnsmessage.Question
	for {
		question, qErr := parser.Question()
		if qErr != nil {
			if errors.Is(qErr, dnsmessage.ErrSectionDone) {
				break
			}
			return nil
		}
		questions = append(questions, question)
		if firstA == nil && question.Type == dnsmessage.TypeA {
			qCopy := question
			firstA = &qCopy
		}
	}
	if firstA == nil {
		return nil
	}
	parsedIP := net.ParseIP(strings.TrimSpace(ip))
	if parsedIP == nil || parsedIP.To4() == nil {
		return nil
	}
	responseHeader := dnsmessage.Header{
		ID:                 requestHeader.ID,
		Response:           true,
		OpCode:             requestHeader.OpCode,
		RecursionDesired:   requestHeader.RecursionDesired,
		RecursionAvailable: true,
		RCode:              dnsmessage.RCodeSuccess,
	}
	builder := dnsmessage.NewBuilder(nil, responseHeader)
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil
	}
	for _, question := range questions {
		if err := builder.Question(question); err != nil {
			return nil
		}
	}
	if err := builder.StartAnswers(); err != nil {
		return nil
	}
	var a dnsmessage.AResource
	copy(a.A[:], parsedIP.To4())
	if err := builder.AResource(dnsmessage.ResourceHeader{
		Name:  firstA.Name,
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
		TTL:   uint32(probeLocalDNSCacheTTL / time.Second),
	}, a); err != nil {
		return nil
	}
	message, err := builder.Finish()
	if err != nil {
		return nil
	}
	return message
}

func normalizeProbeLocalDNSHostPort(raw string, defaultPort string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || strings.Contains(value, "://") {
		return "", false
	}
	host := ""
	port := strings.TrimSpace(defaultPort)
	if parsedHost, parsedPort, err := net.SplitHostPort(value); err == nil {
		host = strings.TrimSpace(strings.Trim(parsedHost, "[]"))
		port = strings.TrimSpace(parsedPort)
	} else {
		host = strings.TrimSpace(strings.Trim(value, "[]"))
	}
	if host == "" {
		return "", false
	}
	if net.ParseIP(host) == nil {
		if strings.ContainsAny(host, " /\\") {
			return "", false
		}
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum <= 0 || portNum > 65535 {
		return "", false
	}
	return net.JoinHostPort(host, strconv.Itoa(portNum)), true
}

func normalizeProbeLocalDoHURL(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", false
	}
	if strings.TrimSpace(parsed.Path) == "" {
		parsed.Path = "/dns-query"
	}
	return parsed.String(), true
}

func normalizeProbeLocalDNSHostPortList(items []string, defaultPort string, fallback []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		normalized, ok := normalizeProbeLocalDNSHostPort(item, defaultPort)
		if !ok {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) > 0 {
		return out
	}
	for _, item := range fallback {
		normalized, ok := normalizeProbeLocalDNSHostPort(item, defaultPort)
		if !ok {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizeProbeLocalDoHURLList(items []string, fallback []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		normalized, ok := normalizeProbeLocalDoHURL(item)
		if !ok {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) > 0 {
		return out
	}
	for _, item := range fallback {
		normalized, ok := normalizeProbeLocalDoHURL(item)
		if !ok {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func resolveProbeLocalDNSRouteDecision(domain string) probeLocalDNSRouteDecision {
	return resolveProbeLocalProxyRouteDecisionByDomain(domain)
}

func probeLocalDNSDomainMatchesRules(domain string, rules []string) bool {
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" {
		return false
	}
	for _, rule := range rules {
		key, value, ok := splitProbeLocalProxyRule(rule)
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

func shouldUseProbeLocalDNSFakeIP(domain string, qType dnsmessage.Type, decision probeLocalDNSRouteDecision) bool {
	if qType != dnsmessage.TypeA {
		return false
	}
	if decision.Reject || strings.EqualFold(strings.TrimSpace(decision.Action), "reject") {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(decision.Action), "tunnel") {
		return false
	}
	if strings.TrimSpace(decision.SelectedChainID) == "" && strings.TrimSpace(decision.TunnelNodeID) == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(decision.Group), "fallback") {
		return false
	}
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" {
		return false
	}
	return true
}

func allocateProbeLocalDNSFakeIP(domain string, decision probeLocalDNSRouteDecision) (string, bool) {
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" {
		return "", false
	}
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	ensureProbeLocalDNSFakePoolLocked()
	pruneProbeLocalDNSFakeEntriesLocked(now)
	if probeLocalDNSState.fakeNetwork == nil {
		return "", false
	}
	if existingIP, exists := probeLocalDNSState.fakeDomainToIP[cleanDomain]; exists {
		if entry, ok := probeLocalDNSState.fakeIPToEntry[existingIP]; ok {
			entry.Decision = decision
			entry.ExpiresAt = now.Add(probeLocalDNSCacheTTL)
			probeLocalDNSState.fakeIPToEntry[existingIP] = entry
			storeProbeLocalDNSRouteHintLocked(cleanDomain, decision, now)
			return existingIP, true
		}
		delete(probeLocalDNSState.fakeDomainToIP, cleanDomain)
	}
	ip := nextProbeLocalDNSFakeIPLocked(now)
	if ip == "" {
		return "", false
	}
	probeLocalDNSState.fakeDomainToIP[cleanDomain] = ip
	probeLocalDNSState.fakeIPToEntry[ip] = probeLocalDNSFakeIPRuntimeEntry{
		Domain:    cleanDomain,
		Decision:  decision,
		ExpiresAt: now.Add(probeLocalDNSCacheTTL),
	}
	storeProbeLocalDNSRouteHintLocked(cleanDomain, decision, now)
	return ip, true
}

func ensureProbeLocalDNSFakePoolLocked() {
	cfg, err := loadProbeLocalProxyGroupFile()
	if err != nil {
		cfg = defaultProbeLocalProxyGroupFile()
	}
	cidr := strings.TrimSpace(cfg.FakeIPCIDR)
	if cidr == "" {
		cidr = probeLocalFakeIPDefaultCIDR
	}
	if strings.EqualFold(cidr, probeLocalDNSState.fakeCIDR) && probeLocalDNSState.fakeNetwork != nil {
		return
	}
	probeLocalDNSState.fakeCIDR = cidr
	probeLocalDNSState.fakeCursor = 0
	probeLocalDNSState.fakeDomainToIP = make(map[string]string)
	probeLocalDNSState.fakeIPToEntry = make(map[string]probeLocalDNSFakeIPRuntimeEntry)
	_, network, parseErr := net.ParseCIDR(cidr)
	if parseErr != nil || network == nil || network.IP.To4() == nil {
		probeLocalDNSState.fakeNetwork = nil
		return
	}
	probeLocalDNSState.fakeNetwork = network
}

func pruneProbeLocalDNSFakeEntriesLocked(now time.Time) {
	for ip, entry := range probeLocalDNSState.fakeIPToEntry {
		if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
			delete(probeLocalDNSState.fakeIPToEntry, ip)
			if probeLocalDNSState.fakeDomainToIP[entry.Domain] == ip {
				delete(probeLocalDNSState.fakeDomainToIP, entry.Domain)
			}
		}
	}
	for domain, ip := range probeLocalDNSState.fakeDomainToIP {
		if _, ok := probeLocalDNSState.fakeIPToEntry[ip]; !ok {
			delete(probeLocalDNSState.fakeDomainToIP, domain)
		}
	}
	for domain, entry := range probeLocalDNSState.routeHints {
		if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
			delete(probeLocalDNSState.routeHints, domain)
		}
	}
}

func nextProbeLocalDNSFakeIPLocked(now time.Time) string {
	if probeLocalDNSState.fakeNetwork == nil {
		return ""
	}
	networkIP := probeLocalDNSState.fakeNetwork.IP.To4()
	if networkIP == nil {
		return ""
	}
	ones, bits := probeLocalDNSState.fakeNetwork.Mask.Size()
	if bits != 32 || ones >= 31 {
		return ""
	}
	hostBits := uint32(bits - ones)
	size := (uint32(1) << hostBits) - 2
	if size == 0 {
		return ""
	}
	baseU32 := binary.BigEndian.Uint32(networkIP)
	reserved := strings.TrimSpace(currentProbeLocalTUNDNSListenHost())
	gatewayReserved := ""
	if ip := net.ParseIP(strings.TrimSpace(probeLocalTUNRouteGatewayIPv4)).To4(); ip != nil {
		gatewayReserved = ip.String()
	}
	interfaceReserved := ""
	if ip := net.ParseIP(strings.TrimSpace(probeLocalTUNInterfaceIPv4)).To4(); ip != nil {
		interfaceReserved = ip.String()
	}
	for i := uint32(0); i < size; i++ {
		probeLocalDNSState.fakeCursor = (probeLocalDNSState.fakeCursor % size) + 1
		candidate := baseU32 + probeLocalDNSState.fakeCursor
		candidateBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(candidateBytes, candidate)
		candidateIP := net.IP(candidateBytes).String()
		if candidateIP == "" {
			continue
		}
		if candidateIP == reserved {
			continue
		}
		if gatewayReserved != "" && candidateIP == gatewayReserved {
			continue
		}
		if interfaceReserved != "" && candidateIP == interfaceReserved {
			continue
		}
		if existing, ok := probeLocalDNSState.fakeIPToEntry[candidateIP]; ok {
			if !existing.ExpiresAt.IsZero() && now.Before(existing.ExpiresAt) {
				continue
			}
			delete(probeLocalDNSState.fakeIPToEntry, candidateIP)
			if probeLocalDNSState.fakeDomainToIP[existing.Domain] == candidateIP {
				delete(probeLocalDNSState.fakeDomainToIP, existing.Domain)
			}
		}
		return candidateIP
	}
	return ""
}

func storeProbeLocalDNSRouteHintLocked(domain string, decision probeLocalDNSRouteDecision, now time.Time) {
	if probeLocalDNSState.routeHints == nil {
		probeLocalDNSState.routeHints = make(map[string]probeLocalDNSRouteHintEntry)
	}
	probeLocalDNSState.routeHints[domain] = probeLocalDNSRouteHintEntry{
		Domain:    domain,
		Decision:  decision,
		ExpiresAt: now.Add(probeLocalDNSCacheTTL),
	}
}

func queryProbeLocalDNSFakeIPEntries() []probeLocalDNSFakeIPEntry {
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	pruneProbeLocalDNSFakeEntriesLocked(now)
	out := make([]probeLocalDNSFakeIPEntry, 0, len(probeLocalDNSState.fakeIPToEntry))
	for ip, entry := range probeLocalDNSState.fakeIPToEntry {
		out = append(out, probeLocalDNSFakeIPEntry{
			Domain:          entry.Domain,
			FakeIP:          ip,
			Group:           entry.Decision.Group,
			Action:          entry.Decision.Action,
			SelectedChainID: firstNonEmpty(strings.TrimSpace(entry.Decision.SelectedChainID), mustProbeLocalSelectedChainIDFromLegacy(entry.Decision.TunnelNodeID)),
			TunnelNodeID:    entry.Decision.TunnelNodeID,
			ExpiresAt:       entry.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

func lookupProbeLocalDNSFakeIPEntry(ip string) (probeLocalDNSFakeIPEntry, bool) {
	cleanIP := strings.TrimSpace(ip)
	if net.ParseIP(cleanIP) == nil {
		return probeLocalDNSFakeIPEntry{}, false
	}
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	pruneProbeLocalDNSFakeEntriesLocked(now)
	entry, ok := probeLocalDNSState.fakeIPToEntry[cleanIP]
	if !ok {
		return probeLocalDNSFakeIPEntry{}, false
	}
	return probeLocalDNSFakeIPEntry{
		Domain:          entry.Domain,
		FakeIP:          cleanIP,
		Group:           entry.Decision.Group,
		Action:          entry.Decision.Action,
		SelectedChainID: firstNonEmpty(strings.TrimSpace(entry.Decision.SelectedChainID), mustProbeLocalSelectedChainIDFromLegacy(entry.Decision.TunnelNodeID)),
		TunnelNodeID:    entry.Decision.TunnelNodeID,
		ExpiresAt:       entry.ExpiresAt.UTC().Format(time.RFC3339),
	}, true
}

func probeLocalDNSRouteHintCount() int {
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	pruneProbeLocalDNSFakeEntriesLocked(now)
	return len(probeLocalDNSState.routeHints)
}

func currentProbeLocalDNSFakeIPCIDR() string {
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	ensureProbeLocalDNSFakePoolLocked()
	return strings.TrimSpace(probeLocalDNSState.fakeCIDR)
}

func storeProbeLocalDNSCacheRecords(urlText string, ips []string) {
	cleanURL := strings.TrimSpace(strings.ToLower(urlText))
	if cleanURL == "" || len(ips) == 0 {
		return
	}
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	pruneProbeLocalDNSCacheLocked(now)
	if probeLocalDNSState.cache == nil {
		probeLocalDNSState.cache = make(map[string]probeLocalDNSCacheEntry)
	}
	for _, rawIP := range ips {
		ipText := strings.TrimSpace(rawIP)
		if net.ParseIP(ipText) == nil {
			continue
		}
		key := cleanURL + "|" + ipText
		probeLocalDNSState.cache[key] = probeLocalDNSCacheEntry{
			URL:       cleanURL,
			IP:        ipText,
			UpdatedAt: now,
			ExpiresAt: now.Add(probeLocalDNSCacheTTL),
		}
	}
}

func queryProbeLocalDNSCacheRecords() []probeLocalDNSCacheRecord {
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	pruneProbeLocalDNSCacheLocked(now)
	records := make([]probeLocalDNSCacheRecord, 0, len(probeLocalDNSState.cache))
	for _, entry := range probeLocalDNSState.cache {
		records = append(records, probeLocalDNSCacheRecord{URL: entry.URL, IP: entry.IP})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].URL == records[j].URL {
			return records[i].IP < records[j].IP
		}
		return records[i].URL < records[j].URL
	})
	return records
}

func lookupProbeLocalDNSCacheRecordsByDomain(domain string) []probeLocalDNSCacheRecord {
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" {
		return nil
	}
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	pruneProbeLocalDNSCacheLocked(now)
	out := make([]probeLocalDNSCacheRecord, 0, 2)
	for _, entry := range probeLocalDNSState.cache {
		if strings.EqualFold(strings.TrimSpace(entry.URL), cleanDomain) {
			out = append(out, probeLocalDNSCacheRecord{URL: entry.URL, IP: entry.IP})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

func pruneProbeLocalDNSCacheLocked(now time.Time) {
	if len(probeLocalDNSState.cache) == 0 {
		return
	}
	for key, entry := range probeLocalDNSState.cache {
		if entry.ExpiresAt.IsZero() || now.After(entry.ExpiresAt) {
			delete(probeLocalDNSState.cache, key)
		}
	}
}

func currentProbeLocalDNSStatus() probeLocalDNSStatus {
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	status := probeLocalDNSState.status
	if strings.TrimSpace(status.UpdatedAt) == "" {
		status.UpdatedAt = probeLocalDNSNow().UTC().Format(time.RFC3339)
	}
	return status
}

func resetProbeLocalDNSRuntimeCachesForProxyGroupRefresh() {
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	now := probeLocalDNSNow().UTC().Format(time.RFC3339)
	probeLocalDNSState.cache = make(map[string]probeLocalDNSCacheEntry)
	probeLocalDNSState.fakeCIDR = ""
	probeLocalDNSState.fakeNetwork = nil
	probeLocalDNSState.fakeCursor = 0
	probeLocalDNSState.fakeDomainToIP = make(map[string]string)
	probeLocalDNSState.fakeIPToEntry = make(map[string]probeLocalDNSFakeIPRuntimeEntry)
	probeLocalDNSState.routeHints = make(map[string]probeLocalDNSRouteHintEntry)
	probeLocalDNSState.status.UpdatedAt = now
	if probeLocalDNSState.tunStatus.Enabled {
		probeLocalDNSState.tunStatus.UpdatedAt = now
	}
}

func updateProbeLocalDNSStatusError(err error) {
	if err == nil {
		return
	}
	probeLocalDNSState.mu.Lock()
	probeLocalDNSState.status.LastError = strings.TrimSpace(err.Error())
	probeLocalDNSState.status.UpdatedAt = probeLocalDNSNow().UTC().Format(time.RFC3339)
	if probeLocalDNSState.tunStatus.Enabled {
		probeLocalDNSState.tunStatus.LastError = strings.TrimSpace(err.Error())
		probeLocalDNSState.tunStatus.UpdatedAt = probeLocalDNSNow().UTC().Format(time.RFC3339)
	}
	probeLocalDNSState.mu.Unlock()
}

func currentProbeLocalDNSTUNStatus() probeLocalDNSStatus {
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	status := probeLocalDNSState.tunStatus
	if strings.TrimSpace(status.UpdatedAt) == "" {
		status.UpdatedAt = probeLocalDNSNow().UTC().Format(time.RFC3339)
	}
	return status
}

func isProbeLocalDNSClosedErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "closed network connection")
}

func probeLocalDNSHostFromAddress(addr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.Trim(host, "[]"))
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func resetProbeLocalDNSServiceForTest() {
	probeLocalDNSState.mu.Lock()
	if probeLocalDNSState.conn != nil {
		_ = probeLocalDNSState.conn.Close()
	}
	if probeLocalDNSState.tunConn != nil {
		_ = probeLocalDNSState.tunConn.Close()
	}
	probeLocalDNSState.conn = nil
	probeLocalDNSState.tunConn = nil
	probeLocalDNSState.started = false
	probeLocalDNSState.cache = make(map[string]probeLocalDNSCacheEntry)
	probeLocalDNSState.fakeCIDR = ""
	probeLocalDNSState.fakeNetwork = nil
	probeLocalDNSState.fakeCursor = 0
	probeLocalDNSState.fakeDomainToIP = make(map[string]string)
	probeLocalDNSState.fakeIPToEntry = make(map[string]probeLocalDNSFakeIPRuntimeEntry)
	probeLocalDNSState.routeHints = make(map[string]probeLocalDNSRouteHintEntry)
	probeLocalDNSState.status = probeLocalDNSStatus{UpdatedAt: probeLocalDNSNow().UTC().Format(time.RFC3339)}
	probeLocalDNSState.tunStatus = probeLocalDNSStatus{UpdatedAt: probeLocalDNSNow().UTC().Format(time.RFC3339)}
	probeLocalDNSState.mu.Unlock()
	resetProbeLocalDNSHooksForTest()
}

func resetProbeLocalDNSHooksForTest() {
	probeLocalDNSListenPacket = net.ListenPacket
	probeLocalDNSNow = time.Now
	probeLocalDNSSystemServers = currentProbeLocalSystemDNSServers
}
