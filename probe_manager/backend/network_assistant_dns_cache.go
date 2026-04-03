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

var unifiedDNSCache = struct {
	mu      sync.Mutex
	records map[string]unifiedDNSCacheRecord
}{
	records: make(map[string]unifiedDNSCacheRecord),
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

// ── 直连 DNS 缓存读写 ─────────────────────────────────────────────────────────

// getProbeDNSCachedIP 读取直连 DNS 内存缓存。
func getProbeDNSCachedIP(host string) (string, bool) {
	normalizedHost := normalizeDNSCacheHost(host)
	if normalizedHost == "" {
		return "", false
	}
	if ip, ok := getUnifiedDirectDNSCachedIP(normalizedHost); ok {
		return ip, true
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
			_ = setUnifiedDirectDNSCachedIP(normalizedHost, normalizedIP, int(dnsCacheTTL/time.Second), unifiedDNSRecordSourceSystem)
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
	_ = setUnifiedDirectDNSCachedIP(normalizedHost, normalizedIP, int(dnsCacheTTL/time.Second), unifiedDNSRecordSourceSystem)
	return nil
}

// clearDNSCache 清空全部内存 DNS 缓存（直连 + 规则/隧道）。
func (s *networkAssistantService) clearDNSCache() {
	directDNSCache.mu.Lock()
	directDNSCache.entries = make(map[string]dnsCacheDirectEntry)
	directDNSCache.mu.Unlock()

	unifiedDNSCache.mu.Lock()
	unifiedDNSCache.records = make(map[string]unifiedDNSCacheRecord)
	unifiedDNSCache.mu.Unlock()

	s.mu.Lock()
	s.ruleDNSCache = make(map[string]dnsCacheEntry)
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

func unifiedDirectHostKey(host string) string {
	return "direct|" + normalizeDNSCacheHost(host)
}

func unifiedRuleDNSKey(cacheKey string) string {
	return "rule|" + strings.ToLower(strings.TrimSpace(cacheKey))
}

func unifiedCachePruneExpiredLocked(now time.Time) {
	for key, record := range unifiedDNSCache.records {
		if !record.Expires.IsZero() && now.After(record.Expires) {
			delete(unifiedDNSCache.records, key)
		}
	}
}

func unifiedCacheEvictOneLocked() {
	oldestKey := ""
	var oldest time.Time
	for key, record := range unifiedDNSCache.records {
		if oldestKey == "" || record.Expires.Before(oldest) {
			oldestKey = key
			oldest = record.Expires
		}
	}
	if oldestKey != "" {
		delete(unifiedDNSCache.records, oldestKey)
	}
}

func setUnifiedDirectDNSCachedIP(host string, ip string, ttlSeconds int, source unifiedDNSRecordSource) error {
	if ttlSeconds <= 0 {
		ttlSeconds = int(dnsCacheTTL / time.Second)
	}
	normalizedHost := normalizeDNSCacheHost(host)
	normalizedIP := normalizeDNSCacheIP(ip)
	if normalizedHost == "" || normalizedIP == "" {
		return nil
	}
	now := time.Now()
	unifiedDNSCache.mu.Lock()
	defer unifiedDNSCache.mu.Unlock()
	unifiedCachePruneExpiredLocked(now)
	if len(unifiedDNSCache.records) >= dnsCacheMaxEntries {
		unifiedCacheEvictOneLocked()
	}
	unifiedDNSCache.records[unifiedDirectHostKey(normalizedHost)] = unifiedDNSCacheRecord{
		Kind:    unifiedDNSRecordKindDirectHost,
		Source:  source,
		HostKey: normalizedHost,
		IPKey:   normalizedIP,
		Addrs:   []string{normalizedIP},
		Route:   tunnelRouteDecision{Direct: true},
		Domain:  normalizedHost,
		FakeIP:  false,
		Expires: now.Add(time.Duration(ttlSeconds) * time.Second),
	}
	return nil
}

func getUnifiedDirectDNSCachedIP(host string) (string, bool) {
	normalizedHost := normalizeDNSCacheHost(host)
	if normalizedHost == "" {
		return "", false
	}
	key := unifiedDirectHostKey(normalizedHost)
	now := time.Now()
	unifiedDNSCache.mu.Lock()
	defer unifiedDNSCache.mu.Unlock()
	record, ok := unifiedDNSCache.records[key]
	if !ok {
		return "", false
	}
	if !record.Expires.IsZero() && now.After(record.Expires) {
		delete(unifiedDNSCache.records, key)
		return "", false
	}
	if record.IPKey != "" {
		return record.IPKey, true
	}
	if len(record.Addrs) > 0 {
		return normalizeDNSCacheIP(record.Addrs[0]), true
	}
	return "", false
}

func getUnifiedRuleDNSCache(cacheKey string) ([]string, bool) {
	key := unifiedRuleDNSKey(cacheKey)
	now := time.Now()
	unifiedDNSCache.mu.Lock()
	defer unifiedDNSCache.mu.Unlock()
	record, ok := unifiedDNSCache.records[key]
	if !ok {
		return nil, false
	}
	if !record.Expires.IsZero() && now.After(record.Expires) {
		delete(unifiedDNSCache.records, key)
		return nil, false
	}
	if len(record.Addrs) == 0 {
		return nil, false
	}
	return append([]string(nil), record.Addrs...), true
}

func setUnifiedRuleDNSCache(cacheKey string, addresses []string, ttlSeconds int, source unifiedDNSRecordSource) {
	if len(addresses) == 0 {
		return
	}
	normalized := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		if canonical := normalizeDNSCacheIP(addr); canonical != "" {
			normalized = append(normalized, canonical)
		}
	}
	if len(normalized) == 0 {
		return
	}
	if ttlSeconds <= 0 {
		ttlSeconds = ruleDNSCacheMinTTLSeconds
	}
	now := time.Now()
	unifiedDNSCache.mu.Lock()
	defer unifiedDNSCache.mu.Unlock()
	unifiedCachePruneExpiredLocked(now)
	if len(unifiedDNSCache.records) >= dnsCacheMaxEntries {
		unifiedCacheEvictOneLocked()
	}
	cacheKey = strings.ToLower(strings.TrimSpace(cacheKey))
	domain := cacheKey
	if idx := strings.LastIndex(cacheKey, "|"); idx >= 0 && idx+1 < len(cacheKey) {
		domain = cacheKey[idx+1:]
	}
	unifiedDNSCache.records[unifiedRuleDNSKey(cacheKey)] = unifiedDNSCacheRecord{
		Kind:    unifiedDNSRecordKindRuleDNS,
		Source:  source,
		HostKey: cacheKey,
		Addrs:   append([]string(nil), normalized...),
		Route:   tunnelRouteDecision{Direct: source != unifiedDNSRecordSourceTunnel},
		Domain:  domain,
		FakeIP:  false,
		Expires: now.Add(time.Duration(ttlSeconds) * time.Second),
	}
}

func queryUnifiedDNSCacheEntries(query string) []NetworkAssistantDNSCacheEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	now := time.Now()
	unifiedDNSCache.mu.Lock()
	unifiedCachePruneExpiredLocked(now)
	records := make([]unifiedDNSCacheRecord, 0, len(unifiedDNSCache.records))
	for _, item := range unifiedDNSCache.records {
		records = append(records, item)
	}
	unifiedDNSCache.mu.Unlock()

	results := make([]NetworkAssistantDNSCacheEntry, 0, len(records))
	add := func(domain string, ip string, record unifiedDNSCacheRecord) {
		if q != "" {
			if !strings.Contains(strings.ToLower(domain), q) && !strings.Contains(strings.ToLower(ip), q) {
				return
			}
		}
		fakeIPValue := ""
		if record.FakeIP {
			fakeIPValue = ip
		}
		results = append(results, NetworkAssistantDNSCacheEntry{
			Domain:      domain,
			IP:          ip,
			FakeIP:      record.FakeIP,
			FakeIPValue: fakeIPValue,
			Direct:      record.Route.Direct,
			NodeID:      strings.TrimSpace(record.Route.NodeID),
			Group:       strings.TrimSpace(record.Route.Group),
			Kind:        string(record.Kind),
			Source:      string(record.Source),
			ExpiresAt:   record.Expires.Format(time.RFC3339),
		})
	}

	for _, record := range records {
		switch record.Kind {
		case unifiedDNSRecordKindRuleDNS:
			for _, addr := range record.Addrs {
				add(record.Domain, addr, record)
			}
		default:
			ip := record.IPKey
			if ip == "" && len(record.Addrs) > 0 {
				ip = record.Addrs[0]
			}
			domain := record.Domain
			if domain == "" {
				domain = record.HostKey
			}
			if ip != "" {
				add(domain, ip, record)
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
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

func unifiedRouteHintKey(ip string) string {
	return "route|" + normalizeDNSCacheIP(ip)
}

func unifiedFakeIPKey(ip string) string {
	return "fake|" + normalizeDNSCacheIP(ip)
}

func setUnifiedRouteHintByIP(ip string, domain string, route tunnelRouteDecision, ttlSeconds int, source unifiedDNSRecordSource, fake bool) {
	normalizedIP := normalizeDNSCacheIP(ip)
	if normalizedIP == "" {
		return
	}
	if ttlSeconds <= 0 {
		ttlSeconds = ruleDNSCacheMinTTLSeconds
	}
	now := time.Now()
	unifiedDNSCache.mu.Lock()
	defer unifiedDNSCache.mu.Unlock()
	unifiedCachePruneExpiredLocked(now)
	if len(unifiedDNSCache.records) >= dnsCacheMaxEntries {
		unifiedCacheEvictOneLocked()
	}
	unifiedDNSCache.records[unifiedRouteHintKey(normalizedIP)] = unifiedDNSCacheRecord{
		Kind:    unifiedDNSRecordKindRouteHint,
		Source:  source,
		IPKey:   normalizedIP,
		Route:   route,
		Domain:  strings.ToLower(strings.TrimSpace(domain)),
		FakeIP:  fake,
		Expires: now.Add(time.Duration(ttlSeconds) * time.Second),
	}
}

func getUnifiedRouteHintByIP(ip string) (dnsRouteHintEntry, bool) {
	normalizedIP := normalizeDNSCacheIP(ip)
	if normalizedIP == "" {
		return dnsRouteHintEntry{}, false
	}
	key := unifiedRouteHintKey(normalizedIP)
	now := time.Now()
	unifiedDNSCache.mu.Lock()
	defer unifiedDNSCache.mu.Unlock()
	record, ok := unifiedDNSCache.records[key]
	if !ok {
		return dnsRouteHintEntry{}, false
	}
	if !record.Expires.IsZero() && now.After(record.Expires) {
		delete(unifiedDNSCache.records, key)
		return dnsRouteHintEntry{}, false
	}
	if record.Kind != unifiedDNSRecordKindRouteHint && record.Kind != unifiedDNSRecordKindFakeIP {
		return dnsRouteHintEntry{}, false
	}
	return dnsRouteHintEntry{
		Direct:  record.Route.Direct,
		NodeID:  strings.TrimSpace(record.Route.NodeID),
		Group:   strings.TrimSpace(record.Route.Group),
		Expires: record.Expires,
		Domain:  strings.ToLower(strings.TrimSpace(record.Domain)),
		FakeIP:  record.FakeIP,
	}, true
}

func setUnifiedFakeIPMapping(fakeIP string, domain string, route tunnelRouteDecision, ttlSeconds int) {
	normalizedIP := normalizeDNSCacheIP(fakeIP)
	if normalizedIP == "" {
		return
	}
	if ttlSeconds <= 0 {
		ttlSeconds = fakeIPDefaultTTLSeconds
	}
	now := time.Now()
	cleanDomain := strings.ToLower(strings.TrimSpace(domain))
	unifiedDNSCache.mu.Lock()
	defer unifiedDNSCache.mu.Unlock()
	unifiedCachePruneExpiredLocked(now)
	if len(unifiedDNSCache.records) >= dnsCacheMaxEntries {
		unifiedCacheEvictOneLocked()
	}
	unifiedDNSCache.records[unifiedFakeIPKey(normalizedIP)] = unifiedDNSCacheRecord{
		Kind:    unifiedDNSRecordKindFakeIP,
		Source:  unifiedDNSRecordSourceFake,
		IPKey:   normalizedIP,
		Route:   route,
		Domain:  cleanDomain,
		FakeIP:  true,
		Expires: now.Add(time.Duration(ttlSeconds) * time.Second),
	}
	unifiedDNSCache.records[unifiedRouteHintKey(normalizedIP)] = unifiedDNSCacheRecord{
		Kind:    unifiedDNSRecordKindRouteHint,
		Source:  unifiedDNSRecordSourceFake,
		IPKey:   normalizedIP,
		Route:   route,
		Domain:  cleanDomain,
		FakeIP:  true,
		Expires: now.Add(time.Duration(ttlSeconds) * time.Second),
	}
}

func getUnifiedFakeIPMapping(fakeIP string) (fakeIPEntry, bool) {
	normalizedIP := normalizeDNSCacheIP(fakeIP)
	if normalizedIP == "" {
		return fakeIPEntry{}, false
	}
	key := unifiedFakeIPKey(normalizedIP)
	now := time.Now()
	unifiedDNSCache.mu.Lock()
	defer unifiedDNSCache.mu.Unlock()
	record, ok := unifiedDNSCache.records[key]
	if !ok {
		return fakeIPEntry{}, false
	}
	if !record.Expires.IsZero() && now.After(record.Expires) {
		delete(unifiedDNSCache.records, key)
		return fakeIPEntry{}, false
	}
	if record.Kind != unifiedDNSRecordKindFakeIP {
		return fakeIPEntry{}, false
	}
	return fakeIPEntry{
		Domain:  strings.ToLower(strings.TrimSpace(record.Domain)),
		Route:   record.Route,
		Expires: record.Expires,
	}, true
}

func clearUnifiedRouteAndFakeHints() {
	unifiedDNSCache.mu.Lock()
	defer unifiedDNSCache.mu.Unlock()
	for key, record := range unifiedDNSCache.records {
		// 仅清理路由提示；fake IP 映射属于公共 DNS 缓存，不应在路由重建时被整体清空。
		if record.Kind == unifiedDNSRecordKindRouteHint {
			delete(unifiedDNSCache.records, key)
		}
	}
}
