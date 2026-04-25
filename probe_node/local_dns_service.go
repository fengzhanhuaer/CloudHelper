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

var probeLocalDNSState = struct {
	mu      sync.Mutex
	started bool
	conn    net.PacketConn
	status  probeLocalDNSStatus
	cache   map[string]probeLocalDNSCacheEntry
}{
	cache: make(map[string]probeLocalDNSCacheEntry),
	status: probeLocalDNSStatus{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	},
}

var (
	probeLocalDNSListenPacket = net.ListenPacket
	probeLocalDNSNow          = time.Now
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
	if probeLocalDNSState.started {
		probeLocalDNSState.mu.Unlock()
		return
	}
	probeLocalDNSState.started = true
	probeLocalDNSState.mu.Unlock()

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
	domain := parseProbeLocalDNSQueryDomain(packet)
	candidates := currentProbeLocalDNSUpstreamCandidates()
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

func currentProbeLocalDNSUpstreamCandidates() []probeLocalDNSUpstreamCandidate {
	cfg, err := loadProbeLocalProxyGroupFile()
	if err != nil {
		logProbeWarnf("load proxy_group for dns upstream failed, use defaults: %v", err)
		cfg = defaultProbeLocalProxyGroupFile()
	}
	candidates := make([]probeLocalDNSUpstreamCandidate, 0, 16)
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
	return candidates
}

func queryProbeLocalDNSViaDoH(endpoint string, packet []byte) ([]byte, error) {
	cleanEndpoint, ok := normalizeProbeLocalDoHURL(endpoint)
	if !ok {
		return nil, fmt.Errorf("invalid doh endpoint: %q", endpoint)
	}
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

func parseProbeLocalDNSQueryDomain(packet []byte) string {
	parser := dnsmessage.Parser{}
	if _, err := parser.Start(packet); err != nil {
		return ""
	}
	question, err := parser.Question()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.ToLower(question.Name.String()), "."))
	return name
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

func buildProbeLocalDNSServfail(request []byte) []byte {
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
		RCode:              dnsmessage.RCodeServerFailure,
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

func updateProbeLocalDNSStatusError(err error) {
	if err == nil {
		return
	}
	probeLocalDNSState.mu.Lock()
	probeLocalDNSState.status.LastError = strings.TrimSpace(err.Error())
	probeLocalDNSState.status.UpdatedAt = probeLocalDNSNow().UTC().Format(time.RFC3339)
	probeLocalDNSState.mu.Unlock()
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
	probeLocalDNSState.conn = nil
	probeLocalDNSState.started = false
	probeLocalDNSState.cache = make(map[string]probeLocalDNSCacheEntry)
	probeLocalDNSState.status = probeLocalDNSStatus{UpdatedAt: probeLocalDNSNow().UTC().Format(time.RFC3339)}
	probeLocalDNSState.mu.Unlock()
	resetProbeLocalDNSHooksForTest()
}

func resetProbeLocalDNSHooksForTest() {
	probeLocalDNSListenPacket = net.ListenPacket
	probeLocalDNSNow = time.Now
}
