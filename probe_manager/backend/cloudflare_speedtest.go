package backend

import (
	"fmt"
	"math/rand"
	"net"
	"sort"
	"sync"
	"time"
)

// CloudflareIPTestResult 单个 IP 的测速结果
type CloudflareIPTestResult struct {
	IP        string `json:"ip"`
	LatencyMS int64  `json:"latency_ms"` // -1 表示超时/失败
}

// CloudflareSpeedTestRequest 测速请求参数
type CloudflareSpeedTestRequest struct {
	SampleCount int `json:"sample_count"` // 采样 IP 数，建议 80~200
	TimeoutMS   int `json:"timeout_ms"`   // 单 IP 超时（毫秒），建议 2000
	TopN        int `json:"top_n"`        // 返回延迟最低的前 N 个
}

// CloudflareSpeedTestResponse 测速响应
type CloudflareSpeedTestResponse struct {
	Results    []CloudflareIPTestResult `json:"results"`
	TotalTested int                    `json:"total_tested"`
	ValidCount  int                    `json:"valid_count"`
	Message     string                 `json:"message"`
}

// Cloudflare 官方 IP 段前缀（用于随机采样）
// 来源：https://www.cloudflare.com/ips/
var cfIPPrefixes = []string{
	// 104.16.0.0/12 → 104.16 ~ 104.31
	"104.16", "104.17", "104.18", "104.19", "104.20", "104.21",
	"104.22", "104.23", "104.24", "104.25", "104.26", "104.27",
	"104.28", "104.29", "104.30", "104.31",
	// 172.64.0.0/13 → 172.64 ~ 172.71
	"172.64", "172.65", "172.66", "172.67", "172.68", "172.69",
	"172.70", "172.71",
	// 162.158.0.0/15
	"162.158", "162.159",
	// 198.41.128.0/17
	"198.41.128", "198.41.192",
	// 141.101.64.0/18
	"141.101.64", "141.101.96",
	// 108.162.192.0/18
	"108.162.192", "108.162.208",
	// 190.93.240.0/20
	"190.93.240",
	// 188.114.96.0/20
	"188.114.96", "188.114.97",
	// 197.234.240.0/22
	"197.234.240",
	// 203.28.8.0/22
	"203.28.8",
	// 103.21.244.0/22
	"103.21.244",
	// 103.22.200.0/22
	"103.22.200",
	// 103.31.4.0/22
	"103.31.4",
}

// sampleCFIPs 从 Cloudflare IP 段随机采样指定数量的 IP
func sampleCFIPs(count int) []string {
	ips := make([]string, 0, count)
	prefixes := make([]string, len(cfIPPrefixes))
	copy(prefixes, cfIPPrefixes)

	// 随机打乱顺序
	rand.Shuffle(len(prefixes), func(i, j int) {
		prefixes[i], prefixes[j] = prefixes[j], prefixes[i]
	})

	for _, prefix := range prefixes {
		// 判断前缀段数：2段 = /16，3段 = /24
		dotCount := 0
		for _, c := range prefix {
			if c == '.' {
				dotCount++
			}
		}

		if dotCount == 1 {
			// /16 段：补充第三、四段（各随机 1~254）
			for i := 0; i < 3 && len(ips) < count; i++ {
				c := rand.Intn(254) + 1
				d := rand.Intn(254) + 1
				ips = append(ips, fmt.Sprintf("%s.%d.%d", prefix, c, d))
			}
		} else {
			// /24 段：补充末段（随机 1~254）
			for i := 0; i < 3 && len(ips) < count; i++ {
				d := rand.Intn(254) + 1
				ips = append(ips, fmt.Sprintf("%s.%d", prefix, d))
			}
		}

		if len(ips) >= count {
			break
		}
	}

	return ips[:min(len(ips), count)]
}

// testTCPLatency 对单个 IP 的 443 端口做 TCP 拨号延迟测试
func testTCPLatency(ip string, timeoutMS int) CloudflareIPTestResult {
	addr := fmt.Sprintf("%s:443", ip)
	timeout := time.Duration(timeoutMS) * time.Millisecond
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return CloudflareIPTestResult{IP: ip, LatencyMS: -1}
	}
	latency := time.Since(start).Milliseconds()
	_ = conn.Close()
	return CloudflareIPTestResult{IP: ip, LatencyMS: latency}
}

// CloudflareSpeedTest Wails 暴露方法：对 Cloudflare IP 进行 TCP 延迟测速
func (a *App) CloudflareSpeedTest(req CloudflareSpeedTestRequest) CloudflareSpeedTestResponse {
	// 参数校验与默认值
	sampleCount := req.SampleCount
	if sampleCount <= 0 {
		sampleCount = 80
	}
	if sampleCount > 500 {
		sampleCount = 500
	}
	timeoutMS := req.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = 2000
	}
	if timeoutMS > 15000 {
		timeoutMS = 15000
	}
	topN := req.TopN
	if topN <= 0 {
		topN = 20
	}
	if topN > 100 {
		topN = 100
	}

	// 采样 IP
	ips := sampleCFIPs(sampleCount)

	// 并发测速（最大并发 30）
	results := make([]CloudflareIPTestResult, len(ips))
	sem := make(chan struct{}, 30)
	var wg sync.WaitGroup

	for i, ip := range ips {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, target string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx] = testTCPLatency(target, timeoutMS)
		}(i, ip)
	}
	wg.Wait()

	// 过滤有效结果并排序
	var valid []CloudflareIPTestResult
	for _, r := range results {
		if r.LatencyMS >= 0 {
			valid = append(valid, r)
		}
	}
	sort.Slice(valid, func(i, j int) bool {
		return valid[i].LatencyMS < valid[j].LatencyMS
	})

	// 取前 N 个
	top := valid
	if len(top) > topN {
		top = top[:topN]
	}

	msg := fmt.Sprintf("共测试 %d 个 IP，有效 %d 个，返回延迟最低前 %d 个",
		len(ips), len(valid), len(top))
	if len(valid) == 0 {
		msg = fmt.Sprintf("所有 %d 个 IP 均超时（>%dms），请尝试增大超时时间", len(ips), timeoutMS)
	}

	return CloudflareSpeedTestResponse{
		Results:     top,
		TotalTested: len(ips),
		ValidCount:  len(valid),
		Message:     msg,
	}
}
