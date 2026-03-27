package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	dnsCacheFileName   = "dns_cache.json"
	dnsCacheTTL        = 24 * time.Hour
	dnsCacheMaxEntries = 51200
)

// ── 磁盘文件格式 ──────────────────────────────────────────────────────────────

// dnsCacheDirectRecord 是 "直连" 类 DNS 记录（hostname → single IP）。
// 用于 newCachedDNSDialContext，key 就是规范化后的 hostname。
type dnsCacheDirectRecord struct {
	Host      string `json:"host"`
	IP        string `json:"ip"`
	ExpiresAt string `json:"expires_at"`
}

// dnsCacheRuleRecord 是 "规则/隧道" 类 DNS 记录（支持多 IP）。
// key 格式：nodeID|qType|domain，e.g. "cloudserver|1|github.com"
type dnsCacheRuleRecord struct {
	Key       string   `json:"key"`
	Addrs     []string `json:"addrs"`
	ExpiresAt string   `json:"expires_at"`
}

type dnsCacheFilePayload struct {
	DirectEntries []dnsCacheDirectRecord `json:"direct"`
	RuleEntries   []dnsCacheRuleRecord   `json:"rule"`
}

// ── 内存状态 ──────────────────────────────────────────────────────────────────

type dnsCacheDirectEntry struct {
	IP        string
	ExpiresAt time.Time
}

// directDNSCache 全局直连 DNS 缓存（hostname → single IP）。
var directDNSCache = struct {
	mu      sync.Mutex
	loaded  bool
	path    string
	entries map[string]dnsCacheDirectEntry
}{
	entries: make(map[string]dnsCacheDirectEntry),
}

// ruleDNSDiskCache 全局规则/隧道 DNS 磁盘缓存（cacheKey → addrs）。
// networkAssistantService 启动时从此处加载到 s.ruleDNSCache。
var ruleDNSDiskCache = struct {
	mu      sync.Mutex
	entries map[string]dnsCacheEntry // dnsCacheEntry 定义在 network_assistant.go
}{
	entries: make(map[string]dnsCacheEntry),
}

// ── 工具函数 ──────────────────────────────────────────────────────────────────

func normalizeDNSCacheHost(rawHost string) string {
	host := strings.TrimSpace(strings.Trim(rawHost, "[]"))
	if host == "" {
		return ""
	}
	if parsed := net.ParseIP(host); parsed != nil {
		return "" // 已是 IP，不需缓存 DNS
	}
	if strings.Contains(host, " ") {
		return ""
	}
	return strings.ToLower(host)
}

func normalizeDNSCacheIP(rawIP string) string {
	parsed := net.ParseIP(strings.TrimSpace(rawIP))
	if parsed == nil {
		return ""
	}
	if ipv4 := parsed.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return parsed.String()
}

func dnsCacheExpiresAt() time.Time {
	return time.Now().Add(dnsCacheTTL)
}

// ── 统一落盘：加载 & 保存 ─────────────────────────────────────────────────────

func ensureDNSCachePath() (string, error) {
	if directDNSCache.path != "" {
		return directDNSCache.path, nil
	}
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dataDir, dnsCacheFileName)
	directDNSCache.path = p
	return p, nil
}

// loadDNSCacheFromDiskLocked 从 dns_cache.json 读取所有记录，填充：
//   - directDNSCache.entries（直连 DNS）
//   - ruleDNSDiskCache.entries（规则/隧道 DNS）
//
// 调用前必须持有 directDNSCache.mu。
func loadDNSCacheFromDiskLocked() error {
	if directDNSCache.loaded {
		return nil
	}
	directDNSCache.loaded = true
	directDNSCache.entries = make(map[string]dnsCacheDirectEntry)

	path, err := ensureDNSCachePath()
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil
	}

	var payload dnsCacheFilePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}

	now := time.Now()

	// 直连记录
	for _, item := range payload.DirectEntries {
		host := normalizeDNSCacheHost(item.Host)
		ip := normalizeDNSCacheIP(item.IP)
		if host == "" || ip == "" {
			continue
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.ExpiresAt))
		if err != nil || now.After(expiresAt) {
			continue
		}
		directDNSCache.entries[host] = dnsCacheDirectEntry{IP: ip, ExpiresAt: expiresAt}
	}
	pruneDirectDNSCacheLocked(now)

	// 规则/隧道记录（写入全局 ruleDNSDiskCache，供服务初始化时拉取）
	ruleDNSDiskCache.mu.Lock()
	ruleDNSDiskCache.entries = make(map[string]dnsCacheEntry)
	for _, item := range payload.RuleEntries {
		key := strings.TrimSpace(item.Key)
		if key == "" || len(item.Addrs) == 0 {
			continue
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.ExpiresAt))
		if err != nil || now.After(expiresAt) {
			continue
		}
		addrs := make([]string, 0, len(item.Addrs))
		for _, a := range item.Addrs {
			ip := normalizeDNSCacheIP(a)
			if ip != "" {
				addrs = append(addrs, ip)
			}
		}
		if len(addrs) > 0 {
			ruleDNSDiskCache.entries[key] = dnsCacheEntry{Addrs: addrs, Expires: expiresAt}
		}
	}
	ruleDNSDiskCache.mu.Unlock()

	return nil
}

// persistDNSCacheLocked 把直连 + 规则/隧道记录一起写回 dns_cache.json。
// 调用前必须持有 directDNSCache.mu。
func persistDNSCacheLocked() error {
	path, err := ensureDNSCachePath()
	if err != nil || path == "" {
		return err
	}
	now := time.Now()
	pruneDirectDNSCacheLocked(now)

	// 直连记录
	directItems := make([]dnsCacheDirectRecord, 0, len(directDNSCache.entries))
	for host, entry := range directDNSCache.entries {
		directItems = append(directItems, dnsCacheDirectRecord{
			Host:      host,
			IP:        entry.IP,
			ExpiresAt: entry.ExpiresAt.UTC().Format(time.RFC3339Nano),
		})
	}
	sort.Slice(directItems, func(i, j int) bool {
		return directItems[i].Host < directItems[j].Host
	})

	// 规则/隧道记录
	ruleDNSDiskCache.mu.Lock()
	ruleItems := make([]dnsCacheRuleRecord, 0, len(ruleDNSDiskCache.entries))
	for key, entry := range ruleDNSDiskCache.entries {
		if now.After(entry.Expires) {
			delete(ruleDNSDiskCache.entries, key)
			continue
		}
		ruleItems = append(ruleItems, dnsCacheRuleRecord{
			Key:       key,
			Addrs:     append([]string(nil), entry.Addrs...),
			ExpiresAt: entry.Expires.UTC().Format(time.RFC3339Nano),
		})
	}
	ruleDNSDiskCache.mu.Unlock()
	sort.Slice(ruleItems, func(i, j int) bool {
		return ruleItems[i].Key < ruleItems[j].Key
	})

	payload := dnsCacheFilePayload{
		DirectEntries: directItems,
		RuleEntries:   ruleItems,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

func pruneDirectDNSCacheLocked(now time.Time) {
	for host, entry := range directDNSCache.entries {
		if now.After(entry.ExpiresAt) {
			delete(directDNSCache.entries, host)
		}
	}
	if len(directDNSCache.entries) <= dnsCacheMaxEntries {
		return
	}
	type sortable struct {
		Host      string
		ExpiresAt time.Time
	}
	items := make([]sortable, 0, len(directDNSCache.entries))
	for h, e := range directDNSCache.entries {
		items = append(items, sortable{h, e.ExpiresAt})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ExpiresAt.After(items[j].ExpiresAt)
	})
	keep := make(map[string]struct{}, dnsCacheMaxEntries)
	for i := 0; i < len(items) && i < dnsCacheMaxEntries; i++ {
		keep[items[i].Host] = struct{}{}
	}
	for h := range directDNSCache.entries {
		if _, ok := keep[h]; !ok {
			delete(directDNSCache.entries, h)
		}
	}
}

// ── 直连 DNS 公共 API（供 newCachedDNSDialContext 和 collectConfiguredDNSBypassIPv4Addrs 使用）──

// getProbeDNSCachedIP 按 hostname 查直连 DNS 缓存，兼容旧调用名。
func getProbeDNSCachedIP(host string) (string, bool) {
	normalizedHost := normalizeDNSCacheHost(host)
	if normalizedHost == "" {
		return "", false
	}
	directDNSCache.mu.Lock()
	defer directDNSCache.mu.Unlock()
	if err := loadDNSCacheFromDiskLocked(); err != nil {
		return "", false
	}
	entry, ok := directDNSCache.entries[normalizedHost]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.ExpiresAt) {
		delete(directDNSCache.entries, normalizedHost)
		_ = persistDNSCacheLocked()
		return "", false
	}
	return entry.IP, true
}

// setProbeDNSCachedIP 写入直连 DNS 缓存，兼容旧调用名。
func setProbeDNSCachedIP(host string, ipValue string) error {
	normalizedHost := normalizeDNSCacheHost(host)
	normalizedIP := normalizeDNSCacheIP(ipValue)
	if normalizedHost == "" || normalizedIP == "" {
		return nil
	}
	directDNSCache.mu.Lock()
	defer directDNSCache.mu.Unlock()
	if err := loadDNSCacheFromDiskLocked(); err != nil {
		return err
	}
	if existing, ok := directDNSCache.entries[normalizedHost]; ok {
		if existing.IP == normalizedIP && time.Until(existing.ExpiresAt) > dnsCacheTTL/2 {
			return nil // 未超过一半 TTL，不刷新
		}
	}
	directDNSCache.entries[normalizedHost] = dnsCacheDirectEntry{
		IP:        normalizedIP,
		ExpiresAt: dnsCacheExpiresAt(),
	}
	return persistDNSCacheLocked()
}

// clearDNSCacheFile 清空全部 DNS 缓存（直连 + 规则/隧道）并删除文件。
func clearDNSCacheFile() error {
	directDNSCache.mu.Lock()
	defer directDNSCache.mu.Unlock()
	if err := loadDNSCacheFromDiskLocked(); err != nil {
		return err
	}
	directDNSCache.entries = make(map[string]dnsCacheDirectEntry)

	ruleDNSDiskCache.mu.Lock()
	ruleDNSDiskCache.entries = make(map[string]dnsCacheEntry)
	ruleDNSDiskCache.mu.Unlock()

	path, err := ensureDNSCachePath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ── 规则/隧道 DNS 落盘 API（供 storeRuleDNSCache 调用）─────────────────────────

// persistRuleDNSEntry 把一条规则/隧道 DNS 记录写入 ruleDNSDiskCache 并落盘。
// 同时触发联合文件写入，确保 direct + rule 保持一致。
func persistRuleDNSEntry(key string, addrs []string) {
	if key == "" || len(addrs) == 0 {
		return
	}
	expires := dnsCacheExpiresAt()
	normalized := make([]string, 0, len(addrs))
	for _, a := range addrs {
		ip := normalizeDNSCacheIP(a)
		if ip != "" {
			normalized = append(normalized, ip)
		}
	}
	if len(normalized) == 0 {
		return
	}

	ruleDNSDiskCache.mu.Lock()
	ruleDNSDiskCache.entries[key] = dnsCacheEntry{Addrs: normalized, Expires: expires}
	ruleDNSDiskCache.mu.Unlock()

	// 联合落盘（在 directDNSCache.mu 保护下写文件）
	directDNSCache.mu.Lock()
	defer directDNSCache.mu.Unlock()
	_ = loadDNSCacheFromDiskLocked()
	_ = persistDNSCacheLocked()
}

// loadRuleDNSFromDisk 把磁盘上的规则/隧道 DNS 记录加载到 s.ruleDNSCache 中。
// 在 newNetworkAssistantService 调用，实现跨会话 DNS 热恢复。
func loadRuleDNSFromDisk() map[string]dnsCacheEntry {
	// 确保磁盘数据已加载
	directDNSCache.mu.Lock()
	_ = loadDNSCacheFromDiskLocked()
	directDNSCache.mu.Unlock()

	ruleDNSDiskCache.mu.Lock()
	defer ruleDNSDiskCache.mu.Unlock()
	now := time.Now()
	out := make(map[string]dnsCacheEntry, len(ruleDNSDiskCache.entries))
	for k, v := range ruleDNSDiskCache.entries {
		if now.After(v.Expires) {
			continue
		}
		out[k] = dnsCacheEntry{Addrs: append([]string(nil), v.Addrs...), Expires: v.Expires}
	}
	return out
}

// ── 全局 DialContext 安装 ─────────────────────────────────────────────────────

// newCachedDNSDialContext 返回一个 DialContext 函数：
//  1. 已是 IP → 直接建连；
//  2. 命中直连 DNS 缓存（24h TTL）→ 直接用 IP 建连；
//  3. 未命中 → 系统 DNS 解析 → 成功后写入缓存。
func newCachedDNSDialContext(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
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

		// 缓存未命中：系统 DNS
		addrs, rerr := net.DefaultResolver.LookupHost(ctx, host)
		if rerr != nil {
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


