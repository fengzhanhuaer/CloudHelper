package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	probeLocalDNSReadBufferSize                = 4096
	probeLocalDNSUpstreamTimeout               = 5 * time.Second
	probeLocalDNSDoHReadLimit                  = 64 * 1024
	probeLocalDNSCacheTTL                      = 10 * time.Minute
	probeLocalDNSCachePersistInterval          = 5 * time.Second
	probeLocalDNSCacheDBFileName               = "dns_cache.db"
	probeLocalFakeIPDefaultCIDR                = "198.18.0.0/15"
	probeLocalVirtualRouterProbeIPReserveCount = 1024
)

type probeLocalDNSUnifiedRecord struct {
	Domain    string
	Group     string
	RealIPs   []string
	FakeIP    string
	UpdatedAt time.Time
	ExpiresAt time.Time
}

type probeLocalDNSUpstreamCandidate struct {
	Kind    string
	Address string
}

type probeLocalDNSPersistRecord struct {
	Domain    string
	Group     string
	RealIPs   []string
	FakeIP    string
	UpdatedAt time.Time
	ExpiresAt time.Time
}

type probeLocalDNSPersistFile struct {
	Version    int
	SavedAt    time.Time
	FakeIPCIDR string
	Records    []probeLocalDNSPersistRecord
}

type probeLocalDNSCachePersistRecord struct {
	URL       string
	IP        string
	UpdatedAt time.Time
	ExpiresAt time.Time
}

type probeLocalDNSCachePersistFile struct {
	Version int
	SavedAt time.Time
	Records []probeLocalDNSCachePersistRecord
}

// probeLocalDNSRouteDecision is transient routing state for DNS handling.
type probeLocalDNSRouteDecision struct {
	Group           string
	Action          string
	SelectedRouteID string
	TunnelNodeID    string
	Reject          bool
}

type probeLocalDNSFakeIPRuntimeEntry struct {
	Domain    string
	Group     string
	ExpiresAt time.Time
}

type probeLocalDNSFakeIPEntry struct {
	Domain          string `json:"domain"`
	FakeIP          string `json:"fake_ip"`
	Group           string `json:"group,omitempty"`
	Action          string `json:"action,omitempty"`
	SelectedRouteID string `json:"selected_route_id,omitempty"`
	TunnelNodeID    string `json:"tunnel_node_id,omitempty"`
	ExpiresAt       string `json:"expires_at"`
}

type probeLocalDNSRouteHintEntry struct {
	Domain    string
	IP        string
	Group     string
	ExpiresAt time.Time
}

var probeLocalDNSState = struct {
	mu                  sync.Mutex
	cache               map[string]probeLocalDNSUnifiedRecord
	cacheLoaded         bool
	cacheDirty          bool
	cachePersistStarted bool
	cachePersistStop    chan struct{}
	fakeCIDR            string
	fakeNetwork         *net.IPNet
	fakeCursor          uint32
	fakeDomainToIP      map[string]string
	fakeIPToEntry       map[string]probeLocalDNSFakeIPRuntimeEntry
	staticHostIPs       map[string][]string
	routeHints          map[string]probeLocalDNSRouteHintEntry
	routeIPHints        map[string]probeLocalDNSRouteHintEntry
}{
	cache:          make(map[string]probeLocalDNSUnifiedRecord),
	fakeDomainToIP: make(map[string]string),
	fakeIPToEntry:  make(map[string]probeLocalDNSFakeIPRuntimeEntry),
	staticHostIPs:  make(map[string][]string),
	routeHints:     make(map[string]probeLocalDNSRouteHintEntry),
	routeIPHints:   make(map[string]probeLocalDNSRouteHintEntry),
}

var (
	probeLocalDNSNow              = time.Now
	probeLocalDNSSystemServers    = currentProbeLocalSystemDNSServers
	probeLocalDNSLoadHostMappings = func() ([]probeLocalHostMapping, error) {
		_, hosts, err := loadProbeLocalHostMappingsWithContent()
		return hosts, err
	}
	probeLocalDNSBootstrapLookupIPv4 func(string) ([]string, error)
)

func init() {
	probeLocalDNSBootstrapLookupIPv4 = bootstrapProbeLocalDNSResolveIPv4s
}

func defaultProbeLocalDNSServers() []string {
	return []string{"223.5.5.5", "119.29.29.29"}
}

func defaultProbeLocalDoTServers() []string {
	return []string{"dns.alidns.com:853", "dot.pub:853"}
}

func defaultProbeLocalDoHServers() []string {
	return []string{"https://dns.alidns.com/dns-query", "https://doh.pub/dns-query"}
}

func ensureProbeLocalDNSCacheLoaded() {
	probeLocalDNSState.mu.Lock()
	alreadyLoaded := probeLocalDNSState.cacheLoaded
	if !probeLocalDNSState.cachePersistStarted {
		probeLocalDNSState.cachePersistStarted = true
		stopCh := make(chan struct{})
		probeLocalDNSState.cachePersistStop = stopCh
		go runProbeLocalDNSCachePersistLoop(stopCh)
	}
	probeLocalDNSState.mu.Unlock()
	if alreadyLoaded {
		return
	}

	cache, fakeCIDR, err := loadProbeLocalDNSCacheFromDisk()
	if err != nil {
		logProbeWarnf("probe local dns cache load failed: %v", err)
		cache = make(map[string]probeLocalDNSUnifiedRecord)
	}
	hosts, hostErr := probeLocalDNSLoadHostMappings()
	if hostErr != nil {
		logProbeWarnf("probe local dns host mapping load failed: %v", hostErr)
	}
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	if !probeLocalDNSState.cacheLoaded {
		removedRealIPs := false
		for domain, record := range cache {
			if len(record.RealIPs) == 0 {
				continue
			}
			record.RealIPs = nil
			cache[domain] = record
			removedRealIPs = true
		}
		probeLocalDNSState.cache = cache
		if strings.TrimSpace(fakeCIDR) != "" {
			probeLocalDNSState.fakeCIDR = strings.TrimSpace(fakeCIDR)
		}
		overlayProbeLocalDNSHostMappingsLocked(hosts, now)
		rebuildProbeLocalDNSRuntimeIndexesLocked(now)
		probeLocalDNSState.cacheLoaded = true
		probeLocalDNSState.cacheDirty = probeLocalDNSState.cacheDirty || removedRealIPs
		pruneProbeLocalDNSUnifiedRecordsLocked(now)
	}
	probeLocalDNSState.mu.Unlock()
}

func overlayProbeLocalDNSHostMappingsLocked(hosts []probeLocalHostMapping, now time.Time) {
	_ = now
	probeLocalDNSState.staticHostIPs = make(map[string][]string)
	for _, item := range hosts {
		domain := normalizeProbeLocalDNSDomain(item.DNS)
		if domain == "" {
			continue
		}
		parsedIP := net.ParseIP(strings.TrimSpace(item.IP))
		if parsedIP == nil || parsedIP.To4() == nil {
			continue
		}
		probeLocalDNSState.staticHostIPs[domain] = []string{parsedIP.To4().String()}
	}
}

func runProbeLocalDNSCachePersistLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(probeLocalDNSCachePersistInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			flushProbeLocalDNSCacheToDisk()
		case <-stopCh:
			return
		}
	}
}

func resolveProbeVirtualRouterDNSUpstreamResponse(packet []byte, domain string, decision probeLocalDNSRouteDecision) ([]byte, []string, error) {
	candidates := currentProbeLocalDNSUpstreamCandidatesForDecision(decision)
	if len(candidates) == 0 {
		return nil, nil, errors.New("dns upstream list is empty")
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
		response = normalizeProbeLocalDNSResponseTTL(response)
		ips, _ := extractProbeLocalDNSResponseIPs(response)
		realIPs := filterProbeLocalIPv4StringsFromList(ips)
		storeProbeLocalDNSInternalRealIPv4s(domain, realIPs, decision)
		return response, realIPs, nil
	}
	if lastErr == nil {
		lastErr = errors.New("dns upstream resolve failed")
	}
	return nil, nil, lastErr
}

func normalizeProbeLocalDNSResponseTTL(packet []byte) []byte {
	var message dnsmessage.Message
	if err := message.Unpack(packet); err != nil {
		return packet
	}
	ttl := uint32(probeLocalDNSCacheTTL / time.Second)
	normalize := func(resources []dnsmessage.Resource) {
		for index := range resources {
			if resources[index].Header.Type == dnsmessage.TypeOPT {
				continue
			}
			resources[index].Header.TTL = ttl
			if soa, ok := resources[index].Body.(*dnsmessage.SOAResource); ok {
				soa.MinTTL = ttl
			}
		}
	}
	normalize(message.Answers)
	normalize(message.Authorities)
	normalize(message.Additionals)
	normalized, err := message.Pack()
	if err != nil {
		return packet
	}
	return normalized
}

func resolveProbeLocalDNSRealIPsForRouteDomain(domain string, decision probeLocalDNSRouteDecision) []string {
	ips, err := resolveProbeLocalDNSInternalRealIPv4s(domain, decision)
	if err != nil || len(ips) == 0 {
		return nil
	}
	return ips
}

func resolveProbeLocalDNSInternalRealIPv4s(domain string, decision probeLocalDNSRouteDecision) ([]string, error) {
	cleanDomain := normalizeProbeLocalDNSDomain(domain)
	if cleanDomain == "" {
		return nil, errors.New("dns domain is empty")
	}
	if staticIPs := lookupProbeLocalDNSStaticHostIPv4ByDomain(cleanDomain); len(staticIPs) > 0 {
		storeProbeLocalDNSRouteHints(cleanDomain, staticIPs, decision)
		return staticIPs, nil
	}
	ips, err := resolveProbeLocalDNSRealIPv4sFromUpstreams(cleanDomain, decision)
	if err != nil {
		return nil, err
	}
	storeProbeLocalDNSInternalRealIPv4s(cleanDomain, ips, decision)
	return ips, nil
}

func storeProbeLocalDNSInternalRealIPv4s(domain string, ips []string, decision probeLocalDNSRouteDecision) {
	cleanDomain := normalizeProbeLocalDNSDomain(domain)
	realIPs := filterProbeLocalIPv4StringsFromList(ips)
	if cleanDomain == "" || len(realIPs) == 0 {
		return
	}
	storeProbeLocalDNSRouteHints(cleanDomain, realIPs, decision)
}

func resolveProbeLocalDNSRealIPv4sFromUpstreams(domain string, decision probeLocalDNSRouteDecision) ([]string, error) {
	cleanDomain := normalizeProbeLocalDNSDomain(domain)
	if cleanDomain == "" {
		return nil, errors.New("dns domain is empty")
	}
	query, err := buildProbeLocalDNSQueryA(cleanDomain)
	if err != nil {
		return nil, err
	}
	candidates := currentProbeLocalDNSUpstreamCandidatesForDecision(decision)
	if len(candidates) == 0 {
		return nil, errors.New("dns upstream list is empty")
	}
	var lastErr error
	for _, candidate := range candidates {
		var response []byte
		var queryErr error
		switch candidate.Kind {
		case "doh":
			response, queryErr = queryProbeLocalDNSViaDoH(candidate.Address, query)
		case "dot":
			response, queryErr = queryProbeLocalDNSViaDoT(candidate.Address, query)
		case "dns":
			response, queryErr = queryProbeLocalDNSViaPlain(candidate.Address, query)
		default:
			continue
		}
		if queryErr != nil {
			lastErr = queryErr
			continue
		}
		ips := filterProbeLocalIPv4StringsFromList(extractProbeLocalDNSResponseIPsBestEffort(response))
		if len(ips) == 0 {
			lastErr = fmt.Errorf("dns upstream returned no ipv4: kind=%s domain=%s", strings.TrimSpace(candidate.Kind), cleanDomain)
			continue
		}
		return ips, nil
	}
	if lastErr == nil {
		lastErr = errors.New("dns upstream resolve failed")
	}
	return nil, lastErr
}

func currentProbeLocalDNSUpstreamCandidatesForDecision(decision probeLocalDNSRouteDecision) []probeLocalDNSUpstreamCandidate {
	return currentProbeLocalDNSUpstreamCandidatesForRouteDecision(decision)
}

func currentProbeLocalDNSUpstreamCandidatesForRouteDecision(decision probeLocalDNSRouteDecision) []probeLocalDNSUpstreamCandidate {
	_ = decision
	candidates := make([]probeLocalDNSUpstreamCandidate, 0, 20)
	seen := make(map[string]struct{}, 16)
	appendDoH := func(items []string) {
		for _, item := range items {
			normalized, ok := normalizeProbeLocalDoHURL(item)
			if !ok {
				continue
			}
			if !shouldIncludeProbeLocalDNSUpstreamCandidate("doh", normalized) {
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
			if !shouldIncludeProbeLocalDNSUpstreamCandidate(kind, normalized) {
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

	appendDoH(defaultProbeLocalDoHServers())
	appendHostPort("dot", defaultProbeLocalDoTServers(), "853")
	appendHostPort("dns", defaultProbeLocalDNSServers(), "53")
	appendHostPort("dns", probeLocalDNSSystemServers(), "53")
	return candidates
}

func shouldIncludeProbeLocalDNSUpstreamCandidate(kind string, address string) bool {
	_ = kind
	_ = address
	return true
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
		target, _, err := resolveProbeLocalDNSHostPortDialTarget(cleanAddress)
		if err != nil {
			return "", false
		}
		return target, true
	case "doh":
		cleanEndpoint, ok := normalizeProbeLocalDoHURL(address)
		if !ok {
			return "", false
		}
		target, _, _, err := resolveProbeLocalDoHDialTarget(cleanEndpoint)
		if err != nil {
			return "", false
		}
		return target, true
	default:
		return "", false
	}
}

func ensureProbeLocalDNSUpstreamDirectBypass(kind string, address string) {
	target, ok := resolveProbeLocalDNSUpstreamBypassTarget(kind, address)
	if !ok {
		return
	}
	if err := ensureProbeRouteDirectBypass(target); err != nil {
		logProbeWarnf("probe local dns upstream direct bypass failed: kind=%s target=%s err=%v", strings.TrimSpace(kind), target, err)
	}
}

type probeLocalDNSProxyDialConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *probeLocalDNSProxyDialConn) Close() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	c.once.Do(func() {
		close(c.done)
	})
	return c.Conn.Close()
}

func queryProbeLocalDNSViaDoH(endpoint string, packet []byte) ([]byte, error) {
	cleanEndpoint, ok := normalizeProbeLocalDoHURL(endpoint)
	if !ok {
		return nil, fmt.Errorf("invalid doh endpoint: %q", endpoint)
	}
	dialTarget, serverName, transportEndpoint, err := resolveProbeLocalDoHDialTarget(cleanEndpoint)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeLocalDNSUpstreamTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cleanEndpoint, bytes.NewReader(packet))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/dns-message")
	request.Header.Set("Accept", "application/dns-message")
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverName,
		},
		DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			ensureProbeLocalDNSUpstreamDirectBypass("doh", cleanEndpoint)
			dialer := applyProbeRouteEgressDialer(&net.Dialer{Timeout: probeLocalDNSUpstreamTimeout})
			if strings.EqualFold(strings.TrimSpace(addr), strings.TrimSpace(transportEndpoint)) {
				return dialer.DialContext(ctx, probeRouteEgressDialNetwork(network, dialTarget), dialTarget)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	client := &http.Client{Timeout: probeLocalDNSUpstreamTimeout, Transport: transport}
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
	dialTarget, serverName, err := resolveProbeLocalDNSHostPortDialTarget(cleanAddress)
	if err != nil {
		return nil, err
	}
	ensureProbeLocalDNSUpstreamDirectBypass("dot", cleanAddress)
	dialer := applyProbeRouteEgressDialer(&net.Dialer{Timeout: probeLocalDNSUpstreamTimeout})
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if strings.TrimSpace(serverName) != "" {
		tlsConfig.ServerName = serverName
	}
	conn, err := tls.DialWithDialer(dialer, probeRouteEgressDialNetwork("tcp", dialTarget), dialTarget, tlsConfig)
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
	dialTarget, _, err := resolveProbeLocalDNSHostPortDialTarget(cleanAddress)
	if err != nil {
		return nil, err
	}
	ensureProbeLocalDNSUpstreamDirectBypass("dns", cleanAddress)
	dialer := applyProbeRouteEgressDialer(&net.Dialer{Timeout: probeLocalDNSUpstreamTimeout})
	conn, err := dialer.Dial(probeRouteEgressDialNetwork("udp", dialTarget), dialTarget)
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

func resolveProbeLocalDNSHostPortDialTarget(address string) (dialTarget string, serverName string, err error) {
	host, port, splitErr := net.SplitHostPort(strings.TrimSpace(address))
	if splitErr != nil {
		return "", "", splitErr
	}
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	cleanPort := strings.TrimSpace(port)
	if cleanHost == "" || cleanPort == "" {
		return "", "", errors.New("invalid upstream address")
	}
	if parsedIP := net.ParseIP(cleanHost); parsedIP != nil && parsedIP.To4() != nil {
		return net.JoinHostPort(parsedIP.To4().String(), cleanPort), "", nil
	}
	resolvedIP, resolveErr := resolveProbeLocalDNSUpstreamHostIPv4(cleanHost)
	if resolveErr != nil {
		return "", "", resolveErr
	}
	return net.JoinHostPort(resolvedIP, cleanPort), cleanHost, nil
}

func resolveProbeLocalDoHDialTarget(endpoint string) (dialTarget string, serverName string, transportEndpoint string, err error) {
	parsed, parseErr := url.Parse(strings.TrimSpace(endpoint))
	if parseErr != nil {
		return "", "", "", parseErr
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", "", "", errors.New("doh upstream host is empty")
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
		return "", "", "", fmt.Errorf("invalid doh upstream port: %s", port)
	}
	transportEndpoint = net.JoinHostPort(host, strconv.Itoa(portNum))
	if parsedIP := net.ParseIP(host); parsedIP != nil && parsedIP.To4() != nil {
		return net.JoinHostPort(parsedIP.To4().String(), strconv.Itoa(portNum)), "", transportEndpoint, nil
	}
	resolvedIP, resolveErr := resolveProbeLocalDNSUpstreamHostIPv4(host)
	if resolveErr != nil {
		return "", "", "", resolveErr
	}
	return net.JoinHostPort(resolvedIP, strconv.Itoa(portNum)), host, transportEndpoint, nil
}

func resolveProbeLocalDNSUpstreamHostIPv4(host string) (string, error) {
	ips, err := resolveProbeLocalDNSIPv4s(host)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("bootstrap resolve returned no ipv4 for %s", normalizeProbeLocalDNSDomain(host))
	}
	return ips[0], nil
}

func bootstrapProbeLocalDNSResolveIPv4s(domain string) ([]string, error) {
	cleanDomain := normalizeProbeLocalDNSDomain(domain)
	if cleanDomain == "" {
		return nil, errors.New("bootstrap domain is empty")
	}
	servers := currentProbeLocalDNSBootstrapServerTargets()
	query, err := buildProbeLocalDNSQueryA(cleanDomain)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, server := range servers {
		response, queryErr := queryProbeLocalDNSViaPlain(server, query)
		if queryErr != nil {
			lastErr = queryErr
			continue
		}
		ips := filterProbeLocalIPv4StringsFromList(extractProbeLocalDNSResponseIPsBestEffort(response))
		if len(ips) == 0 {
			lastErr = fmt.Errorf("bootstrap resolve has no ipv4 answer: server=%s domain=%s", server, cleanDomain)
			continue
		}
		return ips, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("bootstrap dns servers are unavailable for %s", cleanDomain)
	}
	return nil, lastErr
}

func resolveProbeLocalDNSIPv4s(host string) ([]string, error) {
	cleanHost := normalizeProbeLocalDNSDomain(host)
	if cleanHost == "" {
		return nil, errors.New("dns host is empty")
	}
	if parsedIP := net.ParseIP(cleanHost); parsedIP != nil && parsedIP.To4() != nil {
		return []string{parsedIP.To4().String()}, nil
	}
	if staticIPs := lookupProbeLocalDNSStaticHostIPv4ByDomain(cleanHost); len(staticIPs) > 0 {
		return staticIPs, nil
	}
	ips, err := probeLocalDNSBootstrapLookupIPv4(cleanHost)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("bootstrap resolve returned no ipv4 for %s", cleanHost)
	}
	return ips, nil
}

func currentProbeLocalDNSBootstrapServerTargets() []string {
	seen := make(map[string]struct{}, 8)
	out := make([]string, 0, 8)
	appendServer := func(items []string) {
		for _, item := range items {
			target, ok := resolveProbeLocalDNSIPv4LiteralHostPort(item, "53")
			if !ok {
				continue
			}
			if _, exists := seen[target]; exists {
				continue
			}
			seen[target] = struct{}{}
			out = append(out, target)
		}
	}
	appendServer(defaultProbeLocalDNSServers())
	appendServer(probeLocalDNSSystemServers())
	return out
}

func resolveProbeLocalDNSIPv4LiteralHostPort(address string, defaultPort string) (string, bool) {
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
	return net.JoinHostPort(parsedIP.To4().String(), strings.TrimSpace(port)), true
}

func lookupProbeLocalDNSStaticHostIPv4ByDomain(domain string) []string {
	cleanDomain := normalizeProbeLocalDNSDomain(domain)
	if cleanDomain == "" {
		return nil
	}
	ensureProbeLocalDNSCacheLoaded()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	return append([]string(nil), probeLocalDNSState.staticHostIPs[cleanDomain]...)
}

func buildProbeLocalDNSQueryA(domain string) ([]byte, error) {
	cleanDomain := normalizeProbeLocalDNSDomain(domain)
	if cleanDomain == "" {
		return nil, errors.New("dns query domain is empty")
	}
	name, err := dnsmessage.NewName(cleanDomain + ".")
	if err != nil {
		return nil, err
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:               uint16(probeLocalDNSNow().UnixNano()),
		RecursionDesired: true,
	})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(dnsmessage.Question{
		Name:  name,
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		return nil, err
	}
	return builder.Finish()
}

func extractProbeLocalDNSResponseIPsBestEffort(packet []byte) []string {
	ips, err := extractProbeLocalDNSResponseIPs(packet)
	if err != nil {
		return nil
	}
	return ips
}

func filterProbeLocalIPv4StringsFromList(items []string) []string {
	out := make([]string, 0, len(items))
	for _, raw := range items {
		if parsedIP := net.ParseIP(strings.TrimSpace(raw)); parsedIP != nil && parsedIP.To4() != nil {
			out = append(out, parsedIP.To4().String())
		}
	}
	return dedupeProbeLocalDNSIPStrings(out)
}

func dedupeProbeLocalDNSIPStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, raw := range items {
		clean := strings.TrimSpace(raw)
		if clean == "" {
			continue
		}
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out
}

func normalizeProbeLocalDNSDomain(raw string) string {
	return strings.TrimSpace(strings.ToLower(strings.Trim(strings.TrimSpace(raw), ".")))
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

func mergeProbeLocalDNSUniqueIPs(base []string, items []string) []string {
	seen := make(map[string]struct{}, len(base)+len(items))
	out := make([]string, 0, len(base)+len(items))
	appendItem := func(raw string) {
		clean := strings.TrimSpace(raw)
		if clean == "" || net.ParseIP(clean) == nil {
			return
		}
		if _, ok := seen[clean]; ok {
			return
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	for _, item := range base {
		appendItem(item)
	}
	for _, item := range items {
		appendItem(item)
	}
	sort.Strings(out)
	return out
}

func allocateProbeLocalDNSFakeIP(domain string, decision probeLocalDNSRouteDecision) (string, bool) {
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" {
		return "", false
	}
	ensureProbeLocalDNSCacheLoaded()
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
			entry.Group = strings.TrimSpace(decision.Group)
			entry.ExpiresAt = now.Add(probeLocalDNSCacheTTL)
			probeLocalDNSState.fakeIPToEntry[existingIP] = entry
			storeProbeLocalDNSRouteHintLocked(cleanDomain, decision.Group, now)
			upsertProbeLocalDNSUnifiedRecordFakeIPLocked(cleanDomain, existingIP, decision.Group, now)
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
		Group:     strings.TrimSpace(decision.Group),
		ExpiresAt: now.Add(probeLocalDNSCacheTTL),
	}
	upsertProbeLocalDNSUnifiedRecordFakeIPLocked(cleanDomain, ip, decision.Group, now)
	storeProbeLocalDNSRouteHintLocked(cleanDomain, decision.Group, now)
	return ip, true
}

func ensureProbeLocalDNSFakePoolLocked() {
	cidr := strings.TrimSpace(currentProbeVirtualRouterFakeIPCIDR())
	if cidr == "" {
		cidr = probeLocalFakeIPDefaultCIDR
	}
	if strings.EqualFold(cidr, probeLocalDNSState.fakeCIDR) && probeLocalDNSState.fakeNetwork != nil {
		return
	}
	previousCIDR := probeLocalDNSState.fakeCIDR
	previousDomainToIP := probeLocalDNSState.fakeDomainToIP
	previousIPToEntry := probeLocalDNSState.fakeIPToEntry
	probeLocalDNSState.fakeCIDR = cidr
	probeLocalDNSState.fakeCursor = 0
	_, network, parseErr := net.ParseCIDR(cidr)
	if parseErr != nil || network == nil || network.IP.To4() == nil {
		probeLocalDNSState.fakeNetwork = nil
		return
	}
	probeLocalDNSState.fakeNetwork = network
	if strings.EqualFold(strings.TrimSpace(previousCIDR), cidr) {
		probeLocalDNSState.fakeDomainToIP = previousDomainToIP
		probeLocalDNSState.fakeIPToEntry = previousIPToEntry
	} else {
		rebuildProbeLocalDNSFakeIndexesFromRecordsLocked(probeLocalDNSNow().UTC())
	}
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
	for ip, entry := range probeLocalDNSState.routeIPHints {
		if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
			delete(probeLocalDNSState.routeIPHints, ip)
		}
	}
}

func rebuildProbeLocalDNSRuntimeIndexesLocked(now time.Time) {
	rebuildProbeLocalDNSFakeIndexesFromRecordsLocked(now)
	rebuildProbeLocalDNSRouteHintsLocked(now)
}

func reconcileProbeLocalDNSRecordsForRouteRulesLocked(now time.Time) {
	fakeCIDR := strings.TrimSpace(currentProbeVirtualRouterFakeIPCIDR())
	if fakeCIDR == "" {
		fakeCIDR = probeLocalFakeIPDefaultCIDR
	}
	probeLocalDNSState.fakeCIDR = fakeCIDR
	probeLocalDNSState.fakeCursor = 0
	_, network, parseErr := net.ParseCIDR(fakeCIDR)
	if parseErr != nil || network == nil || network.IP.To4() == nil {
		probeLocalDNSState.fakeNetwork = nil
	} else {
		probeLocalDNSState.fakeNetwork = network
	}
	probeLocalDNSState.fakeDomainToIP = make(map[string]string)
	probeLocalDNSState.fakeIPToEntry = make(map[string]probeLocalDNSFakeIPRuntimeEntry)
	probeLocalDNSState.staticHostIPs = make(map[string][]string)
	probeLocalDNSState.routeHints = make(map[string]probeLocalDNSRouteHintEntry)
	probeLocalDNSState.routeIPHints = make(map[string]probeLocalDNSRouteHintEntry)
	for domain, record := range probeLocalDNSState.cache {
		record.Group = "fallback"
		record.FakeIP = ""
		record.UpdatedAt = now
		probeLocalDNSState.cache[domain] = record
	}
	probeLocalDNSState.cacheDirty = true
}

func rebuildProbeLocalDNSFakeIndexesFromRecordsLocked(now time.Time) {
	probeLocalDNSState.fakeDomainToIP = make(map[string]string)
	probeLocalDNSState.fakeIPToEntry = make(map[string]probeLocalDNSFakeIPRuntimeEntry)
	for _, record := range probeLocalDNSState.cache {
		fakeIP := strings.TrimSpace(record.FakeIP)
		group := strings.TrimSpace(record.Group)
		if fakeIP == "" || net.ParseIP(fakeIP) == nil {
			continue
		}
		probeLocalDNSState.fakeDomainToIP[record.Domain] = fakeIP
		probeLocalDNSState.fakeIPToEntry[fakeIP] = probeLocalDNSFakeIPRuntimeEntry{
			Domain:    record.Domain,
			Group:     group,
			ExpiresAt: now.Add(probeLocalDNSCacheTTL),
		}
	}
}

func rebuildProbeLocalDNSRouteHintsLocked(now time.Time) {
	probeLocalDNSState.routeHints = make(map[string]probeLocalDNSRouteHintEntry)
	probeLocalDNSState.routeIPHints = make(map[string]probeLocalDNSRouteHintEntry)
	for _, record := range probeLocalDNSState.cache {
		_ = record
	}
}

func upsertProbeLocalDNSUnifiedRecordFakeIPLocked(domain, fakeIP, group string, now time.Time) {
	cleanDomain := normalizeProbeLocalDNSDomain(domain)
	if cleanDomain == "" {
		return
	}
	record := probeLocalDNSState.cache[cleanDomain]
	record.Domain = cleanDomain
	record.Group = strings.TrimSpace(group)
	record.FakeIP = strings.TrimSpace(fakeIP)
	record.ExpiresAt = now.Add(probeLocalDNSCacheTTL)
	record.UpdatedAt = now
	probeLocalDNSState.cache[cleanDomain] = record
	probeLocalDNSState.cacheDirty = true
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
		if probeLocalDNSFakeCursorReservedForVirtualRouter(probeLocalDNSState.fakeCursor) {
			continue
		}
		candidate := baseU32 + probeLocalDNSState.fakeCursor
		candidateBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(candidateBytes, candidate)
		candidateIP := net.IP(candidateBytes).String()
		if candidateIP == "" {
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

func probeLocalDNSFakeCursorReservedForVirtualRouter(cursor uint32) bool {
	return cursor > 0 && cursor <= uint32(probeLocalVirtualRouterProbeIPReserveCount)
}

func storeProbeLocalDNSRouteHintLocked(domain string, group string, now time.Time) {
	if probeLocalDNSState.routeHints == nil {
		probeLocalDNSState.routeHints = make(map[string]probeLocalDNSRouteHintEntry)
	}
	probeLocalDNSState.routeHints[domain] = probeLocalDNSRouteHintEntry{
		Domain:    domain,
		Group:     strings.TrimSpace(group),
		ExpiresAt: now.Add(probeLocalDNSCacheTTL),
	}
}

func storeProbeLocalDNSRouteHints(domain string, ips []string, decision probeLocalDNSRouteDecision) {
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" || len(ips) == 0 {
		return
	}
	ensureProbeLocalDNSCacheLoaded()
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	pruneProbeLocalDNSFakeEntriesLocked(now)
	storeProbeLocalDNSRouteHintLocked(cleanDomain, decision.Group, now)
	if probeLocalDNSState.routeIPHints == nil {
		probeLocalDNSState.routeIPHints = make(map[string]probeLocalDNSRouteHintEntry)
	}
	for _, rawIP := range ips {
		ip := net.ParseIP(strings.TrimSpace(strings.Trim(rawIP, "[]")))
		if ip == nil {
			continue
		}
		ipText := ip.String()
		probeLocalDNSState.routeIPHints[ipText] = probeLocalDNSRouteHintEntry{
			Domain:    cleanDomain,
			IP:        ipText,
			Group:     strings.TrimSpace(decision.Group),
			ExpiresAt: now.Add(probeLocalDNSCacheTTL),
		}
	}
}

func lookupProbeLocalDNSRouteHintByIP(ipText string) (probeLocalDNSRouteDecision, bool) {
	_ = ipText
	return probeLocalDNSRouteDecision{}, false
}

func lookupProbeLocalDNSRouteHintEntryByIP(ipText string) (probeLocalDNSRouteHintEntry, bool) {
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(ipText, "[]")))
	if ip == nil {
		return probeLocalDNSRouteHintEntry{}, false
	}
	ensureProbeLocalDNSCacheLoaded()
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	pruneProbeLocalDNSFakeEntriesLocked(now)
	entry, ok := probeLocalDNSState.routeIPHints[ip.String()]
	if !ok {
		return probeLocalDNSRouteHintEntry{}, false
	}
	return entry, true
}

func lookupProbeLocalDNSFakeIPEntry(ip string) (probeLocalDNSFakeIPEntry, bool) {
	cleanIP := strings.TrimSpace(ip)
	if net.ParseIP(cleanIP) == nil {
		return probeLocalDNSFakeIPEntry{}, false
	}
	ensureProbeLocalDNSCacheLoaded()
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	pruneProbeLocalDNSFakeEntriesLocked(now)
	entry, ok := probeLocalDNSState.fakeIPToEntry[cleanIP]
	if !ok {
		return probeLocalDNSFakeIPEntry{}, false
	}
	return probeLocalDNSFakeIPEntry{
		Domain:    entry.Domain,
		FakeIP:    cleanIP,
		Group:     firstNonEmpty(strings.TrimSpace(entry.Group), "fallback"),
		Action:    "direct",
		ExpiresAt: entry.ExpiresAt.UTC().Format(time.RFC3339),
	}, true
}

func currentProbeLocalDNSFakeIPCIDR() string {
	ensureProbeLocalDNSCacheLoaded()
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	ensureProbeLocalDNSFakePoolLocked()
	return strings.TrimSpace(probeLocalDNSState.fakeCIDR)
}

func pruneProbeLocalDNSUnifiedRecordsLocked(now time.Time) {
	if len(probeLocalDNSState.cache) == 0 {
		return
	}
	for domain, entry := range probeLocalDNSState.cache {
		if entry.ExpiresAt.IsZero() || now.After(entry.ExpiresAt) {
			delete(probeLocalDNSState.cache, domain)
			probeLocalDNSState.cacheDirty = true
		}
	}
}

func resolveProbeLocalDNSCachePath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, probeLocalDNSCacheDBFileName), nil
}

func loadProbeLocalDNSCacheFromDisk() (map[string]probeLocalDNSUnifiedRecord, string, error) {
	path, err := resolveProbeLocalDNSCachePath()
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]probeLocalDNSUnifiedRecord), "", nil
		}
		return nil, "", err
	}
	if len(raw) == 0 {
		return make(map[string]probeLocalDNSUnifiedRecord), "", nil
	}
	if records, cidr, ok := decodeProbeLocalDNSUnifiedPersistFile(raw); ok {
		return records, cidr, nil
	}
	if records, ok := decodeProbeLocalDNSLegacyCacheFile(raw); ok {
		return records, "", nil
	}
	return nil, "", errors.New("dns cache payload decode failed")
}

func decodeProbeLocalDNSUnifiedPersistFile(raw []byte) (map[string]probeLocalDNSUnifiedRecord, string, bool) {
	var payload probeLocalDNSPersistFile
	if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&payload); err != nil {
		return nil, "", false
	}
	now := probeLocalDNSNow().UTC()
	cache := make(map[string]probeLocalDNSUnifiedRecord, len(payload.Records))
	for _, record := range payload.Records {
		cleanDomain := normalizeProbeLocalDNSDomain(record.Domain)
		if cleanDomain == "" {
			continue
		}
		if !record.ExpiresAt.IsZero() && now.After(record.ExpiresAt) {
			continue
		}
		entry := probeLocalDNSUnifiedRecord{
			Domain:    cleanDomain,
			Group:     strings.TrimSpace(record.Group),
			RealIPs:   dedupeProbeLocalDNSIPStrings(record.RealIPs),
			FakeIP:    strings.TrimSpace(record.FakeIP),
			UpdatedAt: record.UpdatedAt,
			ExpiresAt: record.ExpiresAt,
		}
		if entry.ExpiresAt.IsZero() {
			entry.ExpiresAt = now.Add(probeLocalDNSCacheTTL)
		}
		cache[cleanDomain] = entry
	}
	return cache, strings.TrimSpace(payload.FakeIPCIDR), true
}

func decodeProbeLocalDNSLegacyCacheFile(raw []byte) (map[string]probeLocalDNSUnifiedRecord, bool) {
	var payload probeLocalDNSCachePersistFile
	if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&payload); err != nil {
		return nil, false
	}
	now := probeLocalDNSNow().UTC()
	cache := make(map[string]probeLocalDNSUnifiedRecord)
	for _, record := range payload.Records {
		cleanDomain := normalizeProbeLocalDNSDomain(record.URL)
		ipText := strings.TrimSpace(record.IP)
		if cleanDomain == "" || net.ParseIP(ipText) == nil {
			continue
		}
		if record.ExpiresAt.IsZero() || now.After(record.ExpiresAt) {
			continue
		}
		entry := cache[cleanDomain]
		entry.Domain = cleanDomain
		entry.RealIPs = mergeProbeLocalDNSUniqueIPs(entry.RealIPs, []string{ipText})
		entry.UpdatedAt = record.UpdatedAt
		entry.ExpiresAt = record.ExpiresAt
		cache[cleanDomain] = entry
	}
	return cache, true
}

func flushProbeLocalDNSCacheToDisk() {
	ensureProbeLocalDNSCacheLoaded()
	now := probeLocalDNSNow().UTC()
	probeLocalDNSState.mu.Lock()
	pruneProbeLocalDNSUnifiedRecordsLocked(now)
	if !probeLocalDNSState.cacheDirty {
		probeLocalDNSState.mu.Unlock()
		return
	}
	records := make([]probeLocalDNSPersistRecord, 0, len(probeLocalDNSState.cache))
	for _, entry := range probeLocalDNSState.cache {
		records = append(records, probeLocalDNSPersistRecord{
			Domain:    entry.Domain,
			Group:     entry.Group,
			RealIPs:   append([]string(nil), entry.RealIPs...),
			FakeIP:    entry.FakeIP,
			UpdatedAt: entry.UpdatedAt,
			ExpiresAt: entry.ExpiresAt,
		})
	}
	fakeCIDR := strings.TrimSpace(probeLocalDNSState.fakeCIDR)
	probeLocalDNSState.cacheDirty = false
	probeLocalDNSState.mu.Unlock()

	if err := persistProbeLocalDNSCacheRecordsToDisk(records, fakeCIDR); err != nil {
		logProbeWarnf("probe local dns cache persist failed: %v", err)
		probeLocalDNSState.mu.Lock()
		probeLocalDNSState.cacheDirty = true
		probeLocalDNSState.mu.Unlock()
	}
}

func persistProbeLocalDNSCacheRecordsToDisk(records []probeLocalDNSPersistRecord, fakeCIDR string) error {
	path, err := resolveProbeLocalDNSCachePath()
	if err != nil {
		return err
	}
	payload := probeLocalDNSPersistFile{
		Version:    1,
		SavedAt:    probeLocalDNSNow().UTC(),
		FakeIPCIDR: strings.TrimSpace(fakeCIDR),
		Records:    records,
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(payload); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func removeProbeLocalDNSCacheFromDisk() error {
	path, err := resolveProbeLocalDNSCachePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func resetProbeLocalDNSRuntimeCachesForRouteRefresh() {
	ensureProbeLocalDNSCacheLoaded()
	probeLocalDNSState.mu.Lock()
	reconcileProbeLocalDNSRecordsForRouteRulesLocked(probeLocalDNSNow().UTC())
	probeLocalDNSState.mu.Unlock()
	flushProbeLocalDNSCacheToDisk()
}

func isProbeLocalDNSClosedErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "closed network connection")
}

func resetProbeLocalDNSServiceForTest() {
	probeLocalDNSState.mu.Lock()
	stopCh := probeLocalDNSState.cachePersistStop
	probeLocalDNSState.cache = make(map[string]probeLocalDNSUnifiedRecord)
	probeLocalDNSState.cacheLoaded = false
	probeLocalDNSState.cacheDirty = false
	probeLocalDNSState.cachePersistStarted = false
	probeLocalDNSState.cachePersistStop = nil
	probeLocalDNSState.fakeCIDR = ""
	probeLocalDNSState.fakeNetwork = nil
	probeLocalDNSState.fakeCursor = 0
	probeLocalDNSState.fakeDomainToIP = make(map[string]string)
	probeLocalDNSState.fakeIPToEntry = make(map[string]probeLocalDNSFakeIPRuntimeEntry)
	probeLocalDNSState.routeHints = make(map[string]probeLocalDNSRouteHintEntry)
	probeLocalDNSState.routeIPHints = make(map[string]probeLocalDNSRouteHintEntry)
	probeLocalDNSState.mu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
	resetProbeLocalDNSHooksForTest()
}

func resetProbeLocalDNSHooksForTest() {
	probeLocalDNSNow = time.Now
	probeLocalDNSSystemServers = currentProbeLocalSystemDNSServers
	probeLocalDNSLoadHostMappings = func() ([]probeLocalHostMapping, error) {
		_, hosts, err := loadProbeLocalHostMappingsWithContent()
		return hosts, err
	}
	probeLocalDNSBootstrapLookupIPv4 = func(domain string) ([]string, error) {
		return bootstrapProbeLocalDNSResolveIPv4s(domain)
	}
}
