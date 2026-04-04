package backend

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	dnsCacheTTL        = 1 * time.Hour
	dnsCacheMaxEntries = 51200
)

const (
	dnsCacheKindDirectHost = "direct_host"
	dnsCacheKindRuleDNS    = "rule_dns"
	dnsCacheKindRouteHint  = "route_hint"
	dnsCacheKindFakeIP     = "fake_ip"
	dnsCacheKindTraffic    = "traffic"
)

const (
	dnsCacheSourceSystem    = "system"
	dnsCacheSourceTunnel    = "tunnel"
	dnsCacheSourceFake      = "fake"
	dnsCacheSourceSynthetic = "synthetic"
	dnsCacheSourceMonitor   = "monitor"
)

// ── 内存状态 ──────────────────────────────────────────────────────────────────

type dnsCacheDirectEntry struct {
	IP        string
	ExpiresAt time.Time
}

// directDNSCache 全局直连 DNS 内存缓存（hostname → single IP，TTL 1h）。
var directDNSCache = struct {
	mu      sync.Mutex
	entries map[string]dnsCacheDirectEntry
}{
	entries: make(map[string]dnsCacheDirectEntry),
}

// ── 工具函数 ──────────────────────────────────────────────────────────────────

func dnsCacheExpiresAt() time.Time {
	return time.Now().Add(dnsCacheTTL)
}

func normalizeDNSCacheHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

func normalizeDNSCacheIP(ip string) string {
	p := net.ParseIP(strings.TrimSpace(ip))
	if p == nil {
		return ""
	}
	if v4 := p.To4(); v4 != nil {
		return v4.String()
	}
	return p.String()
}

func normalizeDNSCacheIPs(addresses []string) []string {
	if len(addresses) == 0 {
		return nil
	}
	out := make([]string, 0, len(addresses))
	seen := make(map[string]struct{}, len(addresses))
	for _, addr := range addresses {
		canonical := normalizeDNSCacheIP(addr)
		if canonical == "" {
			continue
		}
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	return out
}

func extractDomainFromRuleDNSCacheKey(cacheKey string) string {
	cacheKey = strings.ToLower(strings.TrimSpace(cacheKey))
	if cacheKey == "" {
		return ""
	}
	if idx := strings.LastIndex(cacheKey, "|"); idx >= 0 && idx+1 < len(cacheKey) {
		return cacheKey[idx+1:]
	}
	return cacheKey
}

// ── 直连 DNS 缓存读写 ─────────────────────────────────────────────────────────

// getProbeDNSCachedIP 读取直连 DNS 内存缓存。
func getProbeDNSCachedIP(host string) (string, bool) {
	normalizedHost := normalizeDNSCacheHost(host)
	if normalizedHost == "" {
		return "", false
	}
	directDNSCache.mu.Lock()
	defer directDNSCache.mu.Unlock()
	entry, ok := directDNSCache.entries[normalizedHost]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.ExpiresAt) {
		delete(directDNSCache.entries, normalizedHost)
		return "", false
	}
	return entry.IP, true
}

// setProbeDNSCachedIP 写入直连 DNS 内存缓存。
func setProbeDNSCachedIP(host string, ipValue string) error {
	normalizedHost := normalizeDNSCacheHost(host)
	normalizedIP := normalizeDNSCacheIP(ipValue)
	if normalizedHost == "" || normalizedIP == "" {
		return nil
	}
	directDNSCache.mu.Lock()
	defer directDNSCache.mu.Unlock()
	if existing, ok := directDNSCache.entries[normalizedHost]; ok {
		if existing.IP == normalizedIP && time.Until(existing.ExpiresAt) > dnsCacheTTL/2 {
			return nil
		}
	}
	if len(directDNSCache.entries) >= dnsCacheMaxEntries {
		// 淘汰最早过期的条目
		oldestKey := ""
		var oldestExpiry time.Time
		for k, v := range directDNSCache.entries {
			if oldestKey == "" || v.ExpiresAt.Before(oldestExpiry) {
				oldestKey = k
				oldestExpiry = v.ExpiresAt
			}
		}
		if oldestKey != "" {
			delete(directDNSCache.entries, oldestKey)
		}
	}
	directDNSCache.entries[normalizedHost] = dnsCacheDirectEntry{
		IP:        normalizedIP,
		ExpiresAt: dnsCacheExpiresAt(),
	}
	return nil
}

// clearDNSCache 清空全部内存 DNS 缓存（直连 + 规则/隧道）。
func (s *networkAssistantService) clearDNSCache() {
	directDNSCache.mu.Lock()
	directDNSCache.entries = make(map[string]dnsCacheDirectEntry)
	directDNSCache.mu.Unlock()

	s.mu.Lock()
	s.ruleDNSCache = make(map[string]dnsCacheEntry)
	s.dnsRouteHints = make(map[string]dnsRouteHintEntry)
	s.mu.Unlock()
}

// ── 全局 DialContext 安装 ─────────────────────────────────────────────────────

// newCachedDNSDialContext 返回一个 DialContext 函数：
//  1. 已是 IP → 直接建连；
//  2. 命中直连 DNS 缓存（1h TTL）→ 直接用 IP 建连；
//  3. 未命中 → 系统 DNS 解析 → 成功后写入缓存。
func (s *networkAssistantService) newCachedDNSDialContext(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if base == nil {
		base = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return base.DialContext(ctx, network, addr)
		}
		if net.ParseIP(host) != nil {
			return base.DialContext(ctx, network, addr)
		}
		normalized := normalizeDNSCacheHost(host)
		if normalized == "" {
			return base.DialContext(ctx, network, addr)
		}

		// 缓存命中
		if cachedIP, ok := getProbeDNSCachedIP(normalized); ok {
			conn, cerr := base.DialContext(ctx, network, net.JoinHostPort(cachedIP, port))
			if cerr == nil {
				return conn, nil
			}
		}

		// 缓存未命中：优先通过我们内置的高可用上游 DNS 解析（规避受污系统 DNS）
		addrs, _, rerr := s.queryRuleDomainViaSystemDNS(host, 1) // 查 A 记录
		if rerr != nil || len(addrs) == 0 {
			// 回退到系统原本的 DNS
			addrs, rerr = net.DefaultResolver.LookupHost(ctx, host)
		}

		if rerr != nil || len(addrs) == 0 {
			return nil, rerr
		}

		var lastErr error
		for _, resolvedIP := range addrs {
			conn, cerr := base.DialContext(ctx, network, net.JoinHostPort(resolvedIP, port))
			if cerr == nil {
				_ = setProbeDNSCachedIP(normalized, resolvedIP)
				return conn, nil
			}
			lastErr = cerr
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("dns cached dial: no address succeeded for " + host)
	}
}

type dnsCachePresentationRecord struct {
	Kind    string
	Source  string
	Domain  string
	IP      string
	Route   tunnelRouteDecision
	FakeIP  bool
	Expires time.Time
}

type dnsCacheTrafficCounters struct {
	DNSCount       int
	IPConnectCount int
}

type dnsCacheIPRouteCandidate struct {
	Route     tunnelRouteDecision
	Count     int
	Timestamp int64
}

func collectDNSCacheTrafficStats(events []NetworkProcessEvent) (map[string]dnsCacheTrafficCounters, map[string]dnsCacheTrafficCounters, map[string]dnsCacheIPRouteCandidate) {
	domainStats := make(map[string]dnsCacheTrafficCounters)
	ipStats := make(map[string]dnsCacheTrafficCounters)
	ipRoute := make(map[string]dnsCacheIPRouteCandidate)

	for _, ev := range events {
		count := ev.Count
		if count <= 0 {
			count = 1
		}

		domain := strings.ToLower(strings.TrimSpace(ev.Domain))
		targetIP := normalizeDNSCacheIP(ev.TargetIP)
		resolvedIPs := normalizeDNSCacheIPs(ev.ResolvedIPs)

		switch ev.Kind {
		case NetworkProcessEventDNS:
			if domain != "" {
				stats := domainStats[domain]
				stats.DNSCount += count
				domainStats[domain] = stats
			}
			for _, ip := range resolvedIPs {
				stats := ipStats[ip]
				stats.DNSCount += count
				ipStats[ip] = stats
				if route, ok := ipRoute[ip]; !ok || count > route.Count || (count == route.Count && ev.Timestamp >= route.Timestamp) {
					ipRoute[ip] = dnsCacheIPRouteCandidate{
						Route: tunnelRouteDecision{
							Direct: ev.Direct,
							NodeID: strings.TrimSpace(ev.NodeID),
							Group:  strings.TrimSpace(ev.Group),
						},
						Count:     count,
						Timestamp: ev.Timestamp,
					}
				}
			}
		case NetworkProcessEventTCP, NetworkProcessEventUDP:
			if targetIP != "" {
				stats := ipStats[targetIP]
				stats.IPConnectCount += count
				ipStats[targetIP] = stats
				if route, ok := ipRoute[targetIP]; !ok || count > route.Count || (count == route.Count && ev.Timestamp >= route.Timestamp) {
					ipRoute[targetIP] = dnsCacheIPRouteCandidate{
						Route: tunnelRouteDecision{
							Direct: ev.Direct,
							NodeID: strings.TrimSpace(ev.NodeID),
							Group:  strings.TrimSpace(ev.Group),
						},
						Count:     count,
						Timestamp: ev.Timestamp,
					}
				}
			}
		}
	}

	return domainStats, ipStats, ipRoute
}

func querySplitDNSCacheEntries(s *networkAssistantService, query string) []NetworkAssistantDNSCacheEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	now := time.Now()

	directRecords := make([]dnsCachePresentationRecord, 0)
	directDNSCache.mu.Lock()
	for host, entry := range directDNSCache.entries {
		if now.After(entry.ExpiresAt) {
			delete(directDNSCache.entries, host)
			continue
		}
		directRecords = append(directRecords, dnsCachePresentationRecord{
			Kind:    dnsCacheKindDirectHost,
			Source:  dnsCacheSourceSystem,
			Domain:  host,
			IP:      entry.IP,
			Route:   tunnelRouteDecision{Direct: true},
			FakeIP:  false,
			Expires: entry.ExpiresAt,
		})
	}
	directDNSCache.mu.Unlock()

	s.mu.Lock()
	if s.ruleDNSCache == nil {
		s.ruleDNSCache = make(map[string]dnsCacheEntry)
	}
	if s.dnsRouteHints == nil {
		s.dnsRouteHints = make(map[string]dnsRouteHintEntry)
	}

	ruleRecords := make([]dnsCachePresentationRecord, 0, len(s.ruleDNSCache))
	for key, entry := range s.ruleDNSCache {
		if now.After(entry.Expires) {
			delete(s.ruleDNSCache, key)
			continue
		}
		domain := extractDomainFromRuleDNSCacheKey(key)
		for _, addr := range entry.Addrs {
			ruleRecords = append(ruleRecords, dnsCachePresentationRecord{
				Kind:    dnsCacheKindRuleDNS,
				Source:  dnsCacheSourceTunnel,
				Domain:  domain,
				IP:      addr,
				Route:   tunnelRouteDecision{Direct: false},
				FakeIP:  false,
				Expires: entry.Expires,
			})
		}
	}

	hintRecords := make([]dnsCachePresentationRecord, 0, len(s.dnsRouteHints))
	for ip, hint := range s.dnsRouteHints {
		if now.After(hint.Expires) {
			delete(s.dnsRouteHints, ip)
			continue
		}
		hintRecords = append(hintRecords, dnsCachePresentationRecord{
			Kind:   dnsCacheKindRouteHint,
			Source: dnsCacheSourceSynthetic,
			Domain: strings.ToLower(strings.TrimSpace(hint.Domain)),
			IP:     ip,
			Route: tunnelRouteDecision{
				Direct:    hint.Direct,
				BypassTUN: hint.BypassTUN,
				NodeID:    strings.TrimSpace(hint.NodeID),
				Group:     strings.TrimSpace(hint.Group),
			},
			FakeIP:  hint.FakeIP,
			Expires: hint.Expires,
		})
	}

	pool := s.fakeIPPool
	monitor := s.processMonitor
	s.mu.Unlock()

	fakeRecords := make([]dnsCachePresentationRecord, 0)
	if pool != nil {
		for ip, entry := range pool.ListAll() {
			fakeRecords = append(fakeRecords, dnsCachePresentationRecord{
				Kind:   dnsCacheKindFakeIP,
				Source: dnsCacheSourceFake,
				Domain: strings.ToLower(strings.TrimSpace(entry.Domain)),
				IP:     ip,
				Route: tunnelRouteDecision{
					Direct:    entry.Route.Direct,
					BypassTUN: entry.Route.BypassTUN,
					NodeID:    strings.TrimSpace(entry.Route.NodeID),
					Group:     strings.TrimSpace(entry.Route.Group),
				},
				FakeIP:  true,
				Expires: entry.Expires,
			})
		}
	}

	domainTrafficStats := make(map[string]dnsCacheTrafficCounters)
	ipTrafficStats := make(map[string]dnsCacheTrafficCounters)
	ipRouteCandidates := make(map[string]dnsCacheIPRouteCandidate)
	if monitor != nil {
		domainTrafficStats, ipTrafficStats, ipRouteCandidates = collectDNSCacheTrafficStats(monitor.GetEvents())
	}

	rankKind := func(kind string) int {
		switch kind {
		case dnsCacheKindFakeIP:
			return 4
		case dnsCacheKindRouteHint:
			return 3
		case dnsCacheKindRuleDNS:
			return 2
		case dnsCacheKindDirectHost:
			return 1
		case dnsCacheKindTraffic:
			return 0
		default:
			return 0
		}
	}
	rankSource := func(source string) int {
		switch source {
		case dnsCacheSourceFake:
			return 4
		case dnsCacheSourceTunnel:
			return 3
		case dnsCacheSourceSynthetic:
			return 2
		case dnsCacheSourceSystem:
			return 1
		case dnsCacheSourceMonitor:
			return 0
		default:
			return 0
		}
	}
	mergeWins := func(current dnsCachePresentationRecord, candidate dnsCachePresentationRecord) bool {
		if rankKind(candidate.Kind) != rankKind(current.Kind) {
			return rankKind(candidate.Kind) > rankKind(current.Kind)
		}
		if rankSource(candidate.Source) != rankSource(current.Source) {
			return rankSource(candidate.Source) > rankSource(current.Source)
		}
		return candidate.Expires.After(current.Expires)
	}

	merged := make(map[string]dnsCachePresentationRecord)
	buildMergedKey := func(domain, ip string) string {
		if domain != "" {
			return domain + "|" + ip
		}
		return "|" + ip
	}
	appendMerged := func(record dnsCachePresentationRecord) {
		domain := strings.ToLower(strings.TrimSpace(record.Domain))
		ip := normalizeDNSCacheIP(record.IP)
		if ip == "" {
			return
		}
		if q != "" && !strings.Contains(domain, q) && !strings.Contains(strings.ToLower(ip), q) {
			return
		}
		record.Domain = domain
		record.IP = ip
		key := buildMergedKey(domain, ip)
		if existing, ok := merged[key]; ok {
			if mergeWins(existing, record) {
				merged[key] = record
			}
			return
		}
		merged[key] = record
	}

	for _, record := range directRecords {
		appendMerged(record)
	}
	for _, record := range ruleRecords {
		appendMerged(record)
	}
	for _, record := range hintRecords {
		appendMerged(record)
	}
	for _, record := range fakeRecords {
		appendMerged(record)
	}

	ipsWithCacheRecord := make(map[string]struct{}, len(merged))
	for _, record := range merged {
		if record.IP == "" {
			continue
		}
		ipsWithCacheRecord[record.IP] = struct{}{}
	}
	for ip, counters := range ipTrafficStats {
		if counters.DNSCount <= 0 && counters.IPConnectCount <= 0 {
			continue
		}
		if _, ok := ipsWithCacheRecord[ip]; ok {
			continue
		}
		route := tunnelRouteDecision{}
		if candidate, ok := ipRouteCandidates[ip]; ok {
			route = candidate.Route
		}
		appendMerged(dnsCachePresentationRecord{
			Kind:    dnsCacheKindTraffic,
			Source:  dnsCacheSourceMonitor,
			Domain:  "",
			IP:      ip,
			Route:   route,
			FakeIP:  false,
			Expires: time.Time{},
		})
	}

	results := make([]NetworkAssistantDNSCacheEntry, 0, len(merged))
	for _, record := range merged {
		fakeIPValue := ""
		if record.FakeIP {
			fakeIPValue = record.IP
		}
		domainStats := domainTrafficStats[record.Domain]
		ipStats := ipTrafficStats[record.IP]
		dnsCount := domainStats.DNSCount
		if dnsCount <= 0 {
			dnsCount = ipStats.DNSCount
		}
		ipConnectCount := ipStats.IPConnectCount
		expiresAt := ""
		if !record.Expires.IsZero() {
			expiresAt = record.Expires.Format(time.RFC3339)
		}
		results = append(results, NetworkAssistantDNSCacheEntry{
			Domain:         record.Domain,
			IP:             record.IP,
			FakeIP:         record.FakeIP,
			FakeIPValue:    fakeIPValue,
			Direct:         record.Route.Direct,
			NodeID:         strings.TrimSpace(record.Route.NodeID),
			Group:          strings.TrimSpace(record.Route.Group),
			Kind:           record.Kind,
			Source:         record.Source,
			DNSCount:       dnsCount,
			IPConnectCount: ipConnectCount,
			TotalCount:     dnsCount + ipConnectCount,
			ExpiresAt:      expiresAt,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].TotalCount != results[j].TotalCount {
			return results[i].TotalCount > results[j].TotalCount
		}
		if results[i].Domain != results[j].Domain {
			return results[i].Domain < results[j].Domain
		}
		if results[i].IP != results[j].IP {
			return results[i].IP < results[j].IP
		}
		return results[i].ExpiresAt < results[j].ExpiresAt
	})
	return results
}
