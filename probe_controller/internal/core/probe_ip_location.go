package core

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const probeIPLocationCacheTTL = 24 * time.Hour

type probeIPLocationCacheItem struct {
	Label     string
	ExpiresAt time.Time
}

var probeIPLocationCache = struct {
	mu   sync.RWMutex
	data map[string]probeIPLocationCacheItem
}{
	data: map[string]probeIPLocationCacheItem{},
}

var probeIPLocationHTTPClient = &http.Client{Timeout: 6 * time.Second}

func resolveAndApplyProbeIPLocations(nodeID string, ips []string) {
	normalizedNodeID := normalizeProbeNodeID(nodeID)
	if normalizedNodeID == "" || len(ips) == 0 {
		return
	}
	uniqueIPs := make([]string, 0, len(ips))
	seen := map[string]struct{}{}
	for _, raw := range ips {
		ip := strings.TrimSpace(raw)
		if ip == "" {
			continue
		}
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		uniqueIPs = append(uniqueIPs, ip)
	}
	if len(uniqueIPs) == 0 {
		return
	}

	go func() {
		resolved := map[string]string{}
		for _, ip := range uniqueIPs {
			if localLabel := detectLocalProbeIPLocation(ip); localLabel != "" {
				resolved[ip] = localLabel
				continue
			}
			if cached := getCachedProbeIPLocation(ip); cached != "" {
				resolved[ip] = cached
				continue
			}

			label := queryProbeIPLocation(ip)
			if strings.TrimSpace(label) == "" {
				label = "未知"
			}
			setCachedProbeIPLocation(ip, label)
			resolved[ip] = label
		}
		applyProbeRuntimeIPLocations(normalizedNodeID, resolved)
	}()
}

func applyProbeRuntimeIPLocations(nodeID string, locations map[string]string) {
	if len(locations) == 0 {
		return
	}

	probeRuntimeStore.mu.Lock()
	current, ok := probeRuntimeStore.data[nodeID]
	if !ok {
		probeRuntimeStore.mu.Unlock()
		return
	}

	validIPs := map[string]struct{}{}
	for _, ip := range current.IPv4 {
		validIPs[strings.TrimSpace(ip)] = struct{}{}
	}
	for _, ip := range current.IPv6 {
		validIPs[strings.TrimSpace(ip)] = struct{}{}
	}
	if current.IPLocations == nil {
		current.IPLocations = map[string]string{}
	}

	changed := false
	for rawIP, rawLabel := range locations {
		ip := strings.TrimSpace(rawIP)
		label := strings.TrimSpace(rawLabel)
		if ip == "" || label == "" {
			continue
		}
		if _, ok := validIPs[ip]; !ok {
			continue
		}
		if current.IPLocations[ip] == label {
			continue
		}
		current.IPLocations[ip] = label
		changed = true
	}
	if changed {
		probeRuntimeStore.data[nodeID] = current
	}
	probeRuntimeStore.mu.Unlock()
}

func getCachedProbeIPLocation(ip string) string {
	key := strings.TrimSpace(ip)
	if key == "" {
		return ""
	}
	now := time.Now()

	probeIPLocationCache.mu.RLock()
	item, ok := probeIPLocationCache.data[key]
	probeIPLocationCache.mu.RUnlock()
	if !ok {
		return ""
	}
	if now.After(item.ExpiresAt) {
		probeIPLocationCache.mu.Lock()
		delete(probeIPLocationCache.data, key)
		probeIPLocationCache.mu.Unlock()
		return ""
	}
	return strings.TrimSpace(item.Label)
}

func setCachedProbeIPLocation(ip string, label string) {
	key := strings.TrimSpace(ip)
	value := strings.TrimSpace(label)
	if key == "" || value == "" {
		return
	}
	probeIPLocationCache.mu.Lock()
	probeIPLocationCache.data[key] = probeIPLocationCacheItem{
		Label:     value,
		ExpiresAt: time.Now().Add(probeIPLocationCacheTTL),
	}
	probeIPLocationCache.mu.Unlock()
}

func detectLocalProbeIPLocation(rawIP string) string {
	ip := strings.TrimSpace(rawIP)
	if ip == "" {
		return ""
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	if parsed.IsUnspecified() {
		return "未指定地址"
	}

	if ip4 := parsed.To4(); ip4 != nil {
		if ip4[0] == 127 {
			return "本机回环"
		}
		if ip4[0] == 169 && ip4[1] == 254 {
			return "链路本地"
		}
		if ip4[0] == 10 {
			return "内网"
		}
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return "内网"
		}
		if ip4[0] == 192 && ip4[1] == 168 {
			return "内网"
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return "内网"
		}
		return ""
	}

	if parsed.IsLoopback() {
		return "本机回环"
	}
	if parsed.IsLinkLocalUnicast() || parsed.IsLinkLocalMulticast() {
		return "链路本地"
	}
	if isUniqueLocalIPv6(parsed) {
		return "内网IPv6"
	}
	return ""
}

func queryProbeIPLocation(ip string) string {
	targetIP := strings.TrimSpace(ip)
	if targetIP == "" {
		return ""
	}

	req, err := http.NewRequest(http.MethodGet, "https://ipwho.is/"+url.PathEscape(targetIP), nil)
	if err != nil {
		return "未知"
	}
	resp, err := probeIPLocationHTTPClient.Do(req)
	if err != nil {
		return "未知"
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "未知"
	}

	var parsed struct {
		Success bool   `json:"success"`
		Country string `json:"country"`
		Region  string `json:"region"`
		City    string `json:"city"`
		ISP     string `json:"isp"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "未知"
	}
	if !parsed.Success {
		return "未知"
	}

	parts := make([]string, 0, 3)
	if v := strings.TrimSpace(parsed.Country); v != "" {
		parts = append(parts, v)
	}
	if v := strings.TrimSpace(parsed.Region); v != "" {
		parts = append(parts, v)
	}
	if v := strings.TrimSpace(parsed.City); v != "" {
		parts = append(parts, v)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	if v := strings.TrimSpace(parsed.ISP); v != "" {
		return v
	}
	return "未知"
}
