package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultPublicIPRefreshInterval = 10 * time.Minute
	publicIPRequestTimeout         = 3 * time.Second
)

var probePublicIPCollector = newPublicIPCollector()

type publicIPCollector struct {
	mu          sync.Mutex
	lastAttempt time.Time
	refreshing  bool
	ipv4        []string
	ipv6        []string
}

func newPublicIPCollector() *publicIPCollector {
	return &publicIPCollector{
		ipv4: []string{},
		ipv6: []string{},
	}
}

func collectPublicIPs() ([]string, []string) {
	if !isPublicIPSniffEnabled() {
		return nil, nil
	}
	return probePublicIPCollector.collect()
}

func (c *publicIPCollector) collect() ([]string, []string) {
	refreshInterval := parsePublicIPRefreshInterval()
	now := time.Now()

	c.mu.Lock()
	hasCache := len(c.ipv4) > 0 || len(c.ipv6) > 0
	canAttempt := c.lastAttempt.IsZero() || now.Sub(c.lastAttempt) >= refreshInterval
	if !hasCache && !c.refreshing && canAttempt {
		c.refreshing = true
		c.lastAttempt = now
		c.mu.Unlock()
		ipv4, ipv6, _ := sniffPublicIPs()
		c.update(ipv4, ipv6, true)
		return append([]string{}, ipv4...), append([]string{}, ipv6...)
	}

	if hasCache && canAttempt && !c.refreshing {
		c.refreshing = true
		c.lastAttempt = now
		go c.refreshAsync()
	}

	cachedIPv4 := append([]string{}, c.ipv4...)
	cachedIPv6 := append([]string{}, c.ipv6...)
	c.mu.Unlock()
	return cachedIPv4, cachedIPv6
}

func (c *publicIPCollector) refreshAsync() {
	ipv4, ipv6, ok := sniffPublicIPs()
	c.update(ipv4, ipv6, ok)
}

func (c *publicIPCollector) update(ipv4 []string, ipv6 []string, shouldReplace bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if shouldReplace {
		c.ipv4 = append([]string{}, ipv4...)
		c.ipv6 = append([]string{}, ipv6...)
	}
	c.refreshing = false
}

func isPublicIPSniffEnabled() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("PROBE_PUBLIC_IP_SNIFF")))
	if raw == "" {
		return true
	}
	switch raw {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return true
	}
}

func parsePublicIPRefreshInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("PROBE_PUBLIC_IP_REFRESH_SEC"))
	if raw == "" {
		return defaultPublicIPRefreshInterval
	}
	sec, err := time.ParseDuration(raw + "s")
	if err != nil || sec < 15*time.Second {
		return defaultPublicIPRefreshInterval
	}
	if sec > 30*time.Minute {
		return 30 * time.Minute
	}
	return sec
}

func sniffPublicIPs() ([]string, []string, bool) {
	v4Endpoints := parsePublicIPEndpoints(
		os.Getenv("PROBE_PUBLIC_IPV4_ENDPOINTS"),
		[]string{"https://api4.ipify.org", "https://ipv4.icanhazip.com"},
	)
	v6Endpoints := parsePublicIPEndpoints(
		os.Getenv("PROBE_PUBLIC_IPV6_ENDPOINTS"),
		[]string{"https://api6.ipify.org", "https://ipv6.icanhazip.com"},
	)

	ipv4Set := map[string]struct{}{}
	ipv6Set := map[string]struct{}{}

	for _, endpoint := range v4Endpoints {
		ip := fetchPublicIP(endpoint, "tcp4")
		if ip == "" {
			continue
		}
		ipv4Set[ip] = struct{}{}
		break
	}

	for _, endpoint := range v6Endpoints {
		ip := fetchPublicIP(endpoint, "tcp6")
		if ip == "" {
			continue
		}
		ipv6Set[ip] = struct{}{}
		break
	}

	ipv4 := mapKeysSorted(ipv4Set)
	ipv6 := mapKeysSorted(ipv6Set)
	return ipv4, ipv6, len(ipv4) > 0 || len(ipv6) > 0
}

func parsePublicIPEndpoints(raw string, fallback []string) []string {
	parts := strings.Split(raw, ",")
	seen := map[string]struct{}{}
	out := make([]string, 0, len(parts)+len(fallback))
	for _, part := range parts {
		endpoint := strings.TrimSpace(part)
		if endpoint == "" {
			continue
		}
		if _, ok := seen[endpoint]; ok {
			continue
		}
		seen[endpoint] = struct{}{}
		out = append(out, endpoint)
	}
	if len(out) > 0 {
		return out
	}
	for _, endpoint := range fallback {
		normalized := strings.TrimSpace(endpoint)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func fetchPublicIP(endpoint string, network string) string {
	ctx, cancel := context.WithTimeout(context.Background(), publicIPRequestTimeout)
	defer cancel()

	dialer := &net.Dialer{
		Timeout:   publicIPRequestTimeout,
		KeepAlive: 15 * time.Second,
	}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         forceNetworkDialContext(dialer, network),
		TLSHandshakeTimeout: publicIPRequestTimeout,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   publicIPRequestTimeout,
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(endpoint), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "cloudhelper-probe-node")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return ""
	}
	value := normalizePublicIPValue(string(body), network)
	if value == "" {
		return ""
	}
	return value
}

func forceNetworkDialContext(dialer *net.Dialer, forceNetwork string) func(ctx context.Context, network, address string) (net.Conn, error) {
	forceNetwork = strings.TrimSpace(strings.ToLower(forceNetwork))
	if forceNetwork != "tcp4" && forceNetwork != "tcp6" {
		return dialer.DialContext
	}
	return func(ctx context.Context, _, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, forceNetwork, address)
	}
}

func normalizePublicIPValue(raw string, network string) string {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return ""
	}
	ip := net.ParseIP(strings.TrimSpace(fields[0]))
	if ip == nil {
		return ""
	}
	switch strings.TrimSpace(strings.ToLower(network)) {
	case "tcp4":
		ip4 := ip.To4()
		if ip4 == nil {
			return ""
		}
		return ip4.String()
	case "tcp6":
		if ip.To16() == nil || ip.To4() != nil {
			return ""
		}
		return ip.String()
	default:
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String()
		}
		if ip.To16() != nil {
			return ip.String()
		}
		return ""
	}
}

func mapKeysSorted(values map[string]struct{}) []string {
	if len(values) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
