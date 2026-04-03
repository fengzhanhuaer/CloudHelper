package backend

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	// fakeIP 默认 TTL（秒）
	fakeIPDefaultTTLSeconds = 3600
)

// fakeIPEntry 记录 fake IP 到域名的映射
type fakeIPEntry struct {
	Domain  string
	Route   tunnelRouteDecision
	Expires time.Time
}

// fakeIPPool 管理 fake IP 分配（CIDR 池 + 双向映射）
type fakeIPPool struct {
	mu sync.Mutex

	// CIDR 网段
	network *net.IPNet
	// 当前分配游标（以网络首地址为基准的偏移量，从 1 开始跳过网络地址）
	cursor uint32
	// 网段大小（可用 IP 数量）
	size uint32

	// domain -> fake IP
	domainToIP map[string]string
	// fake IP -> entry
	ipToEntry map[string]fakeIPEntry

}

// newFakeIPPool 根据 CIDR 字符串创建 fake IP 池
// 如果 cidr 为空或为 0.0.0.0/0 则返回 nil（代表禁用 fake IP）
func newFakeIPPool(cidr string) (*fakeIPPool, error) {
	cidr = strings.TrimSpace(cidr)
	if cidr == "" || cidr == "0.0.0.0/0" {
		return nil, nil
	}
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid fake_ip_cidr %q: %w", cidr, err)
	}
	if network.IP.To4() == nil {
		return nil, fmt.Errorf("fake_ip_cidr must be an IPv4 CIDR: %q", cidr)
	}
	ones, bits := network.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("fake_ip_cidr must be an IPv4 CIDR")
	}
	hostBits := uint32(bits - ones)
	if hostBits < 2 {
		return nil, fmt.Errorf("fake_ip_cidr too small (needs at least /30)")
	}
	// size = 2^hostBits - 2（去掉网络地址和广播地址）
	size := (uint32(1) << hostBits) - 2
	if size == 0 {
		return nil, fmt.Errorf("fake_ip_cidr has no usable addresses")
	}

	return &fakeIPPool{
		network:    network,
		cursor:     0,
		size:       size,
		domainToIP: make(map[string]string),
		ipToEntry:  make(map[string]fakeIPEntry),
	}, nil
}

// AllocateOrGet 为域名分配（或复用已有）fake IP，并记录路由决策
// 返回分配的 fake IP 字符串和 TTL（秒）
func (p *fakeIPPool) AllocateOrGet(domain string, route tunnelRouteDecision) (string, int) {
	if p == nil {
		return "", 0
	}
	normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if normalized == "" {
		return "", 0
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.pruneExpiredLocked()

	// 复用已有映射
	if existingIP, ok := p.domainToIP[normalized]; ok {
		if entry, exists := p.ipToEntry[existingIP]; exists {
			// 刷新过期时间
			entry.Route = route
			entry.Expires = time.Now().Add(fakeIPDefaultTTLSeconds * time.Second)
			p.ipToEntry[existingIP] = entry
			return existingIP, fakeIPDefaultTTLSeconds
		}
		delete(p.domainToIP, normalized)
	}

	// 分配新 fake IP
	fakeIP := p.nextIPLocked()
	if fakeIP == "" {
		return "", 0
	}

	expires := time.Now().Add(fakeIPDefaultTTLSeconds * time.Second)
	p.domainToIP[normalized] = fakeIP
	p.ipToEntry[fakeIP] = fakeIPEntry{
		Domain:  normalized,
		Route:   route,
		Expires: expires,
	}
	return fakeIP, fakeIPDefaultTTLSeconds
}

// LookupDomain 根据 fake IP 反查域名和路由决策
func (p *fakeIPPool) LookupDomain(ip string) (fakeIPEntry, bool) {
	if p == nil {
		return fakeIPEntry{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.ipToEntry[ip]
	if !ok {
		return fakeIPEntry{}, false
	}
	if time.Now().After(entry.Expires) {
		domain := entry.Domain
		delete(p.ipToEntry, ip)
		if p.domainToIP[domain] == ip {
			delete(p.domainToIP, domain)
		}
		return fakeIPEntry{}, false
	}
	return entry, true
}

// IsFakeIP 检查 IP 是否属于 fake IP 网段
func (p *fakeIPPool) IsFakeIP(ip string) bool {
	if p == nil {
		return false
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	return p.network.Contains(parsed)
}

// nextIPLocked 从池中取下一个可用 IP（循环分配）
// 调用者必须持有 p.mu
func (p *fakeIPPool) nextIPLocked() string {
	networkIP := p.network.IP.To4()
	if networkIP == nil {
		return ""
	}
	baseU32 := binary.BigEndian.Uint32(networkIP)

	// 最多轮询 size 次，跳过已被占用且未过期的
	for i := uint32(0); i < p.size; i++ {
		p.cursor = (p.cursor % p.size) + 1 // 从 1 开始，跳过网络地址 (.0)
		candidate := baseU32 + p.cursor
		candidateBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(candidateBytes, candidate)
		candidateIP := net.IP(candidateBytes).String()

		if existing, ok := p.ipToEntry[candidateIP]; ok {
			// 已被占用且未过期 → 跳过
			if !time.Now().After(existing.Expires) {
				continue
			}
			// 过期条目 → 清理并重用
			domain := existing.Domain
			delete(p.ipToEntry, candidateIP)
			if p.domainToIP[domain] == candidateIP {
				delete(p.domainToIP, domain)
			}
		}
		return candidateIP
	}
	// 池已满
	return ""
}

// ListAll 返回当前所有未过期的 fake IP 条目，map key 为 fake IP 字符串。
func (p *fakeIPPool) ListAll() map[string]fakeIPEntry {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	result := make(map[string]fakeIPEntry, len(p.ipToEntry))
	for ip, entry := range p.ipToEntry {
		if !now.After(entry.Expires) {
			result[ip] = entry
		}
	}
	return result
}

// pruneExpiredLocked 清理过期条目
// 调用者必须持有 p.mu
func (p *fakeIPPool) pruneExpiredLocked() {
	now := time.Now()
	for ip, entry := range p.ipToEntry {
		if now.After(entry.Expires) {
			domain := entry.Domain
			delete(p.ipToEntry, ip)
			if p.domainToIP[domain] == ip {
				delete(p.domainToIP, domain)
			}
		}
	}
}

// NetworkAssistantDNSUpstreamConfig DNS 上游配置（暴露给前端的结构体）
type NetworkAssistantDNSUpstreamConfig struct {
	Prefer          string   `json:"prefer"`
	DNSServers      []string `json:"dns_servers"`
	DoTServers      []string `json:"dot_servers"`
	DoHServers      []string `json:"doh_servers"`
	FakeIPCIDR      string   `json:"fake_ip_cidr"`
	FakeIPWhitelist []string `json:"fake_ip_whitelist"`
}

// shouldUseFakeIP 判断该域名是否应分配 fake IP。
// 配置来源改为规则组：命中 direct 组（直连）则不分配 fake IP。
func (s *networkAssistantService) shouldUseFakeIP(normalizedDomain string) bool {
	s.mu.RLock()
	pool := s.fakeIPPool
	s.mu.RUnlock()
	if pool == nil {
		return false
	}
	decision, err := s.decideRouteForTarget(net.JoinHostPort(normalizedDomain, "80"))
	if err != nil {
		return false
	}
	return !decision.Direct
}

// assignFakeIP 为域名分配 fake IP，并预先根据路由策略决定路由
// 返回分配到的 fake IP 字符串（IPv4）
func (s *networkAssistantService) assignFakeIP(normalizedDomain string) (string, error) {
	s.mu.RLock()
	pool := s.fakeIPPool
	s.mu.RUnlock()
	if pool == nil {
		return "", fmt.Errorf("fake IP pool not initialized")
	}

	// 根据域名决定路由
	route, routeErr := s.decideRouteForTarget(net.JoinHostPort(normalizedDomain, "80"))
	if routeErr != nil {
		// 路由决策失败时使用默认（direct）
		route = tunnelRouteDecision{Direct: true}
	}

	fakeIP, ttl := pool.AllocateOrGet(normalizedDomain, route)
	if fakeIP == "" {
		return "", fmt.Errorf("fake IP pool exhausted")
	}

	// 分表路径：仅维护 fakeIP 池 + 路由提示映射
	s.storeFakeIPRouteHint(fakeIP, normalizedDomain, route)
	_ = ttl
	return fakeIP, nil
}

// lookupFakeIPDomain 根据 fake IP 反查域名（若存在）
func (s *networkAssistantService) lookupFakeIPDomain(ip string) (string, tunnelRouteDecision, bool) {
	s.mu.RLock()
	pool := s.fakeIPPool
	s.mu.RUnlock()
	if pool == nil {
		return "", tunnelRouteDecision{}, false
	}
	entry, ok := pool.LookupDomain(ip)
	if !ok {
		return "", tunnelRouteDecision{}, false
	}
	return entry.Domain, entry.Route, true
}

// reloadFakeIPPool 根据当前 DNS 上游配置重新构建 fakeIPPool
func (s *networkAssistantService) reloadFakeIPPool() {
	dnsConfig, err := getDNSUpstreamConfig()
	if err != nil {
		return
	}
	pool, poolErr := newFakeIPPool(dnsConfig.FakeIPCIDR)
	if poolErr != nil {
		s.logf("fake IP pool init failed: %v", poolErr)
		pool = nil
	}
	s.mu.Lock()
	s.fakeIPPool = pool
	s.mu.Unlock()
}

// GetNetworkAssistantDNSUpstreamConfig 返回当前 DNS 上游配置供前端读取
func (s *networkAssistantService) GetDNSUpstreamConfig() (NetworkAssistantDNSUpstreamConfig, error) {
	path, err := ensureDNSUpstreamConfigPath()
	if err != nil {
		return NetworkAssistantDNSUpstreamConfig{}, err
	}
	payload, err := readDNSUpstreamConfigPayload(path)
	if err != nil {
		return NetworkAssistantDNSUpstreamConfig{}, err
	}
	return NetworkAssistantDNSUpstreamConfig{
		Prefer:          payload.Prefer,
		DNSServers:      payload.DNSServers,
		DoTServers:      payload.DoTServers,
		DoHServers:      payload.DoHServers,
		FakeIPCIDR:      payload.FakeIPCIDR,
		FakeIPWhitelist: payload.FakeIPWhitelist,
	}, nil
}

// SetDNSUpstreamConfig 保存 DNS 上游配置并重新加载 fake IP 池
func (s *networkAssistantService) SetDNSUpstreamConfig(cfg NetworkAssistantDNSUpstreamConfig) error {
	path, err := ensureDNSUpstreamConfigPath()
	if err != nil {
		return err
	}
	payload := dnsUpstreamConfigFilePayload{
		Prefer:          cfg.Prefer,
		DNSServers:      cfg.DNSServers,
		DoTServers:      cfg.DoTServers,
		DoHServers:      cfg.DoHServers,
		FakeIPCIDR:      cfg.FakeIPCIDR,
		FakeIPWhitelist: cfg.FakeIPWhitelist,
	}
	if err := writeDNSUpstreamConfigFile(path, payload); err != nil {
		return err
	}
	// 使缓存失效，触发下次重新加载
	invalidateDNSUpstreamConfigCache()
	// 重建 fake IP 池
	s.reloadFakeIPPool()
	return nil
}
