package backend

import (
	"context"
	"errors"
	"net"
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
