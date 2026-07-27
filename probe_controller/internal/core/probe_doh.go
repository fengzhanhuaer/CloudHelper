package core

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	probeControllerDoHDefaultUpstream       = "https://dns.alidns.com/dns-query"
	probeControllerDoHTTLSeconds            = 600
	probeControllerDoHMaxWireBytes          = 64 << 10
	probeControllerDoHMaxQueryRecords       = 500
	probeControllerDoHMaxAnswersPerRecord   = 8
	probeControllerDoHUpstreamTimeout       = 5 * time.Second
	probeControllerDoHConcurrentQueries     = 128
	probeControllerDoHRequestsPerMinute     = 1200
	probeControllerDoHRateLimitStateMaxSize = 4096
)

type probeControllerDoHConfig struct {
	Enabled     bool   `json:"enabled"`
	Upstream    string `json:"upstream,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

type probeControllerDoHConfigView struct {
	Enabled      bool   `json:"enabled"`
	Upstream     string `json:"upstream"`
	EndpointPath string `json:"endpoint_path"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type probeControllerDoHQueryRecord struct {
	ID         uint64   `json:"id"`
	Timestamp  string   `json:"timestamp"`
	ClientIP   string   `json:"client_ip"`
	Domain     string   `json:"domain"`
	QueryType  string   `json:"query_type"`
	Action     string   `json:"action"`
	Answers    []string `json:"answers,omitempty"`
	RuleID     string   `json:"rule_id,omitempty"`
	RuleName   string   `json:"rule_name,omitempty"`
	ExitNodeID string   `json:"exit_node_id,omitempty"`
	Status     string   `json:"status"`
	Error      string   `json:"error,omitempty"`
	LatencyMS  int64    `json:"latency_ms"`
}

type probeControllerDoHRateLimitEntry struct {
	WindowStart time.Time
	Count       int
}

var probeControllerDoHQueryUpstream = queryProbeControllerDoHUpstream

var probeControllerDoHSemaphore = make(chan struct{}, probeControllerDoHConcurrentQueries)

var probeControllerDoHQuerySequence atomic.Uint64

var probeControllerDoHQueryStore = struct {
	sync.RWMutex
	revision uint64
	items    []probeControllerDoHQueryRecord
}{items: make([]probeControllerDoHQueryRecord, 0, probeControllerDoHMaxQueryRecords)}

var probeControllerDoHRateLimitStore = struct {
	sync.Mutex
	items map[string]probeControllerDoHRateLimitEntry
}{items: make(map[string]probeControllerDoHRateLimitEntry)}

func defaultProbeControllerDoHConfig() probeControllerDoHConfig {
	return probeControllerDoHConfig{Upstream: probeControllerDoHDefaultUpstream}
}

func normalizeProbeControllerDoHConfig(input probeControllerDoHConfig) probeControllerDoHConfig {
	upstream := strings.TrimSpace(input.Upstream)
	if _, err := validateProbeControllerDoHUpstream(upstream); err != nil {
		upstream = probeControllerDoHDefaultUpstream
	}
	return probeControllerDoHConfig{
		Enabled:     input.Enabled,
		Upstream:    upstream,
		AccessToken: strings.TrimSpace(input.AccessToken),
		UpdatedAt:   strings.TrimSpace(input.UpdatedAt),
	}
}

func validateProbeControllerDoHUpstream(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil {
		return "", errors.New("doh upstream is invalid")
	}
	if !strings.EqualFold(parsed.Scheme, "https") || strings.TrimSpace(parsed.Hostname()) == "" {
		return "", errors.New("doh upstream must be an https url")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("doh upstream must not contain user info or fragment")
	}
	if parsed.Path == "" {
		parsed.Path = "/dns-query"
	}
	return parsed.String(), nil
}

func currentProbeControllerDoHConfig() probeControllerDoHConfig {
	if ProbeRouteConfigStore == nil {
		return defaultProbeControllerDoHConfig()
	}
	ProbeRouteConfigStore.mu.RLock()
	config := normalizeProbeControllerDoHConfig(ProbeRouteConfigStore.data.DoH)
	ProbeRouteConfigStore.mu.RUnlock()
	return config
}

func ensureProbeControllerDoHConfig() (probeControllerDoHConfig, bool, error) {
	if ProbeRouteConfigStore == nil {
		return probeControllerDoHConfig{}, false, errors.New("route config store not initialized")
	}
	ProbeRouteConfigStore.mu.Lock()
	config := normalizeProbeControllerDoHConfig(ProbeRouteConfigStore.data.DoH)
	changed := false
	if config.AccessToken == "" {
		token, err := randomToken(32)
		if err != nil {
			ProbeRouteConfigStore.mu.Unlock()
			return probeControllerDoHConfig{}, false, err
		}
		config.AccessToken = token
		config.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		changed = true
	}
	ProbeRouteConfigStore.data.DoH = config
	ProbeRouteConfigStore.mu.Unlock()
	return config, changed, nil
}

func probeControllerDoHConfigToView(config probeControllerDoHConfig) probeControllerDoHConfigView {
	endpointPath := ""
	if token := strings.TrimSpace(config.AccessToken); token != "" {
		endpointPath = "/dns-query/" + token
	}
	return probeControllerDoHConfigView{
		Enabled:      config.Enabled,
		Upstream:     firstNonEmptyString(strings.TrimSpace(config.Upstream), probeControllerDoHDefaultUpstream),
		EndpointPath: endpointPath,
		UpdatedAt:    strings.TrimSpace(config.UpdatedAt),
	}
}

func mngRouteDoHHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		config, changed, err := ensureProbeControllerDoHConfig()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if changed {
			if err := ProbeRouteConfigStore.Save(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"item": probeControllerDoHConfigToView(config)})
	case http.MethodPost:
		var request struct {
			Enabled     bool   `json:"enabled"`
			Upstream    string `json:"upstream"`
			RotateToken bool   `json:"rotate_token,omitempty"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
		upstream, err := validateProbeControllerDoHUpstream(request.Upstream)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		current, _, err := ensureProbeControllerDoHConfig()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if request.RotateToken {
			current.AccessToken, err = randomToken(32)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
		current.Enabled = request.Enabled
		current.Upstream = upstream
		current.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		ProbeRouteConfigStore.mu.Lock()
		ProbeRouteConfigStore.data.DoH = current
		ProbeRouteConfigStore.mu.Unlock()
		if err := ProbeRouteConfigStore.Save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"item": probeControllerDoHConfigToView(current)})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func mngRouteDoHQueriesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit := probeControllerDoHMaxQueryRecords
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed < limit {
				limit = parsed
			}
		}
		items, revision := snapshotProbeControllerDoHQueryRecords(limit)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items":    items,
			"revision": revision,
			"summary":  summarizeProbeControllerDoHQueryRecords(items),
		})
	case http.MethodDelete:
		clearProbeControllerDoHQueryRecords()
		items, revision := snapshotProbeControllerDoHQueryRecords(probeControllerDoHMaxQueryRecords)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items":    items,
			"revision": revision,
			"summary":  summarizeProbeControllerDoHQueryRecords(items),
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func ProbeControllerDoHHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}
	config := currentProbeControllerDoHConfig()
	if !config.Enabled || !probeControllerDoHTokenMatchesPath(config.AccessToken, r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	clientIP, _ := getClientIP(r)
	if !allowProbeControllerDoHClient(clientIP, time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "doh query rate limit exceeded"})
		return
	}
	select {
	case probeControllerDoHSemaphore <- struct{}{}:
		defer func() { <-probeControllerDoHSemaphore }()
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "doh query concurrency limit exceeded"})
		return
	}

	packet, err := readProbeControllerDoHRequest(w, r)
	if err != nil {
		writeJSON(w, probeControllerDoHRequestErrorStatus(err), map[string]string{"error": err.Error()})
		return
	}
	var request dnsmessage.Message
	if err := request.Unpack(packet); err != nil || request.Header.Response || len(request.Questions) != 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid dns query message"})
		return
	}
	domain := normalizeProbeVirtualRouterFakeIPDomain(request.Questions[0].Name.String())
	if domain == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid dns query domain"})
		return
	}

	startedAt := time.Now()
	result := resolveProbeControllerDoHQuery(r.Context(), config, packet, request, domain)
	record := probeControllerDoHQueryRecord{
		Timestamp:  startedAt.UTC().Format(time.RFC3339Nano),
		ClientIP:   clientIP,
		Domain:     domain,
		QueryType:  probeControllerDoHQueryTypeLabel(request.Questions[0].Type),
		Action:     result.Action,
		Answers:    result.Answers,
		RuleID:     result.RuleID,
		RuleName:   result.RuleName,
		ExitNodeID: result.ExitNodeID,
		Status:     result.Status,
		LatencyMS:  maxInt64(0, time.Since(startedAt).Milliseconds()),
	}
	if result.Err != nil {
		record.Error = truncateProbeControllerDoHText(result.Err.Error(), 500)
	}
	appendProbeControllerDoHQueryRecord(record)

	response := result.Response
	if len(response) == 0 {
		response, _ = buildProbeControllerDoHResponse(request, dnsmessage.RCodeServerFailure, nil)
	}
	w.Header().Set("Content-Type", "application/dns-message")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response)
}

type probeControllerDoHResolveResult struct {
	Response   []byte
	Action     string
	Answers    []string
	RuleID     string
	RuleName   string
	ExitNodeID string
	Status     string
	Err        error
}

func resolveProbeControllerDoHQuery(ctx context.Context, config probeControllerDoHConfig, packet []byte, request dnsmessage.Message, domain string) probeControllerDoHResolveResult {
	result := probeControllerDoHResolveResult{Action: "direct", Status: "ok"}
	rule, matched := currentProbeControllerDoHRuleForDomain(domain)
	if matched {
		result.RuleID = strings.TrimSpace(rule.ID)
		result.RuleName = strings.TrimSpace(rule.Name)
		result.ExitNodeID = normalizeProbeNodeID(rule.ExitNodeID)
		switch normalizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) {
		case probeVirtualRouterRouteRuleActionReject:
			result.Action = "reject"
			result.Status = "rejected"
			result.Response, result.Err = buildProbeControllerDoHResponse(request, dnsmessage.RCodeRefused, nil)
			return result
		case probeVirtualRouterRouteRuleActionExit:
			result.Action = "fake_ip"
			if request.Questions[0].Type != dnsmessage.TypeA {
				result.Response, result.Err = buildProbeControllerDoHResponse(request, dnsmessage.RCodeSuccess, nil)
				return result
			}
			entry, _, changed, err := allocateProbeVirtualRouterFakeIPForDomain(domain, rule)
			if err != nil {
				result.Status = "error"
				result.Err = err
				result.Response, _ = buildProbeControllerDoHResponse(request, dnsmessage.RCodeServerFailure, nil)
				return result
			}
			if changed {
				if err := ProbeRouteConfigStore.SaveWithoutAutoBackup(); err != nil {
					result.Status = "error"
					result.Err = err
					result.Response, _ = buildProbeControllerDoHResponse(request, dnsmessage.RCodeServerFailure, nil)
					return result
				}
			}
			answer := strings.TrimSpace(entry.FakeIP)
			result.Answers = []string{answer}
			result.Response, result.Err = buildProbeControllerDoHResponse(request, dnsmessage.RCodeSuccess, []string{answer})
			if result.Err != nil {
				result.Status = "error"
			}
			return result
		}
	}

	upstreamResponse, err := probeControllerDoHQueryUpstream(ctx, config.Upstream, packet)
	if err != nil {
		result.Status = "error"
		result.Err = err
		result.Response, _ = buildProbeControllerDoHResponse(request, dnsmessage.RCodeServerFailure, nil)
		return result
	}
	result.Response, result.Answers, err = normalizeProbeControllerDoHUpstreamResponse(request, upstreamResponse)
	if err != nil {
		result.Status = "error"
		result.Err = err
		result.Response, _ = buildProbeControllerDoHResponse(request, dnsmessage.RCodeServerFailure, nil)
	}
	return result
}

func currentProbeControllerDoHRuleForDomain(domain string) (probeVirtualRouterRouteRule, bool) {
	if ProbeRouteConfigStore == nil {
		return probeVirtualRouterRouteRule{}, false
	}
	ProbeRouteConfigStore.mu.RLock()
	config := normalizeProbeVirtualRouterConfig(ProbeRouteConfigStore.data.VirtualRouter)
	ProbeRouteConfigStore.mu.RUnlock()
	if !config.Enabled {
		return probeVirtualRouterRouteRule{}, false
	}
	return probeVirtualRouterRouteRuleForFakeIPDomain(config.RouteRules, domain)
}

func buildProbeControllerDoHResponse(request dnsmessage.Message, rcode dnsmessage.RCode, ipv4Answers []string) ([]byte, error) {
	response := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:                 request.Header.ID,
			Response:           true,
			RecursionDesired:   request.Header.RecursionDesired,
			RecursionAvailable: true,
			RCode:              rcode,
		},
		Questions: append([]dnsmessage.Question(nil), request.Questions...),
	}
	if len(request.Questions) == 1 && request.Questions[0].Type == dnsmessage.TypeA {
		for _, raw := range ipv4Answers {
			parsed := parseProbeControllerDoHIPv4(raw)
			if parsed == nil {
				continue
			}
			response.Answers = append(response.Answers, dnsmessage.Resource{
				Header: dnsmessage.ResourceHeader{
					Name:  request.Questions[0].Name,
					Type:  dnsmessage.TypeA,
					Class: request.Questions[0].Class,
					TTL:   probeControllerDoHTTLSeconds,
				},
				Body: &dnsmessage.AResource{A: [4]byte{parsed[0], parsed[1], parsed[2], parsed[3]}},
			})
		}
	}
	return response.Pack()
}

func parseProbeControllerDoHIPv4(raw string) []byte {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) != 4 {
		return nil
	}
	out := make([]byte, 4)
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || value > 255 {
			return nil
		}
		out[index] = byte(value)
	}
	return out
}

func queryProbeControllerDoHUpstream(ctx context.Context, endpoint string, packet []byte) ([]byte, error) {
	cleanEndpoint, err := validateProbeControllerDoHUpstream(endpoint)
	if err != nil {
		return nil, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, probeControllerDoHUpstreamTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(queryCtx, http.MethodPost, cleanEndpoint, bytes.NewReader(packet))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/dns-message")
	request.Header.Set("Accept", "application/dns-message")
	request.Header.Set("User-Agent", "CloudHelper-Controller-DoH/1")
	client := &http.Client{Timeout: probeControllerDoHUpstreamTimeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("doh upstream status=%d", response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, probeControllerDoHMaxWireBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 || len(payload) > probeControllerDoHMaxWireBytes {
		return nil, errors.New("doh upstream response size is invalid")
	}
	return payload, nil
}

func normalizeProbeControllerDoHUpstreamResponse(request dnsmessage.Message, packet []byte) ([]byte, []string, error) {
	var response dnsmessage.Message
	if err := response.Unpack(packet); err != nil {
		return nil, nil, fmt.Errorf("invalid doh upstream response: %w", err)
	}
	if !response.Header.Response || len(response.Questions) != 1 || len(request.Questions) != 1 {
		return nil, nil, errors.New("doh upstream response question is invalid")
	}
	want := request.Questions[0]
	got := response.Questions[0]
	if normalizeProbeVirtualRouterFakeIPDomain(want.Name.String()) != normalizeProbeVirtualRouterFakeIPDomain(got.Name.String()) || want.Type != got.Type || want.Class != got.Class {
		return nil, nil, errors.New("doh upstream response question mismatch")
	}
	response.Header.ID = request.Header.ID
	normalize := func(resources []dnsmessage.Resource) {
		for index := range resources {
			if resources[index].Header.Type != dnsmessage.TypeOPT {
				resources[index].Header.TTL = probeControllerDoHTTLSeconds
			}
		}
	}
	normalize(response.Answers)
	normalize(response.Authorities)
	normalize(response.Additionals)
	answers := probeControllerDoHAnswerLabels(response.Answers)
	normalized, err := response.Pack()
	if err != nil {
		return nil, nil, err
	}
	return normalized, answers, nil
}

func probeControllerDoHAnswerLabels(resources []dnsmessage.Resource) []string {
	answers := make([]string, 0, minInt(len(resources), probeControllerDoHMaxAnswersPerRecord))
	for _, resource := range resources {
		if len(answers) >= probeControllerDoHMaxAnswersPerRecord {
			break
		}
		var value string
		switch body := resource.Body.(type) {
		case *dnsmessage.AResource:
			value = fmt.Sprintf("%d.%d.%d.%d", body.A[0], body.A[1], body.A[2], body.A[3])
		case *dnsmessage.AAAAResource:
			value = fmt.Sprintf("%x:%x:%x:%x:%x:%x:%x:%x",
				uint16(body.AAAA[0])<<8|uint16(body.AAAA[1]), uint16(body.AAAA[2])<<8|uint16(body.AAAA[3]),
				uint16(body.AAAA[4])<<8|uint16(body.AAAA[5]), uint16(body.AAAA[6])<<8|uint16(body.AAAA[7]),
				uint16(body.AAAA[8])<<8|uint16(body.AAAA[9]), uint16(body.AAAA[10])<<8|uint16(body.AAAA[11]),
				uint16(body.AAAA[12])<<8|uint16(body.AAAA[13]), uint16(body.AAAA[14])<<8|uint16(body.AAAA[15]))
		case *dnsmessage.CNAMEResource:
			value = body.CNAME.String()
		case *dnsmessage.TXTResource:
			value = strings.Join(body.TXT, " ")
		default:
			value = probeControllerDoHQueryTypeLabel(resource.Header.Type)
		}
		value = truncateProbeControllerDoHText(value, 180)
		if value != "" {
			answers = append(answers, value)
		}
	}
	return answers
}

func readProbeControllerDoHRequest(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Method == http.MethodGet {
		encoded := strings.TrimSpace(r.URL.Query().Get("dns"))
		if encoded == "" {
			return nil, errors.New("dns query parameter is required")
		}
		if len(encoded) > base64.RawURLEncoding.EncodedLen(probeControllerDoHMaxWireBytes) {
			return nil, errProbeControllerDoHRequestTooLarge
		}
		packet, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(packet) == 0 {
			return nil, errors.New("dns query parameter is invalid")
		}
		return packet, nil
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/dns-message") {
		return nil, errProbeControllerDoHUnsupportedMediaType
	}
	packet, err := io.ReadAll(io.LimitReader(r.Body, probeControllerDoHMaxWireBytes+1))
	if err != nil {
		return nil, err
	}
	if len(packet) == 0 {
		return nil, errors.New("dns request body is empty")
	}
	if len(packet) > probeControllerDoHMaxWireBytes {
		return nil, errProbeControllerDoHRequestTooLarge
	}
	return packet, nil
}

var (
	errProbeControllerDoHRequestTooLarge      = errors.New("dns request is too large")
	errProbeControllerDoHUnsupportedMediaType = errors.New("content type must be application/dns-message")
)

func probeControllerDoHRequestErrorStatus(err error) int {
	switch {
	case errors.Is(err, errProbeControllerDoHRequestTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, errProbeControllerDoHUnsupportedMediaType):
		return http.StatusUnsupportedMediaType
	default:
		return http.StatusBadRequest
	}
}

func probeControllerDoHTokenMatchesPath(token string, requestPath string) bool {
	want := strings.TrimSpace(token)
	got := strings.TrimPrefix(strings.TrimSpace(requestPath), "/dns-query/")
	if want == "" || got == "" || strings.Contains(got, "/") || len(want) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

func allowProbeControllerDoHClient(clientIP string, now time.Time) bool {
	key := strings.TrimSpace(clientIP)
	if key == "" {
		key = "0.0.0.0"
	}
	probeControllerDoHRateLimitStore.Lock()
	defer probeControllerDoHRateLimitStore.Unlock()
	entry := probeControllerDoHRateLimitStore.items[key]
	if entry.WindowStart.IsZero() || now.Sub(entry.WindowStart) >= time.Minute {
		entry = probeControllerDoHRateLimitEntry{WindowStart: now}
	}
	if entry.Count >= probeControllerDoHRequestsPerMinute {
		probeControllerDoHRateLimitStore.items[key] = entry
		return false
	}
	entry.Count++
	probeControllerDoHRateLimitStore.items[key] = entry
	if len(probeControllerDoHRateLimitStore.items) > probeControllerDoHRateLimitStateMaxSize {
		cutoff := now.Add(-2 * time.Minute)
		for itemKey, item := range probeControllerDoHRateLimitStore.items {
			if item.WindowStart.Before(cutoff) {
				delete(probeControllerDoHRateLimitStore.items, itemKey)
			}
		}
	}
	return true
}

func appendProbeControllerDoHQueryRecord(record probeControllerDoHQueryRecord) {
	record.ID = probeControllerDoHQuerySequence.Add(1)
	if record.Answers == nil {
		record.Answers = []string{}
	}
	probeControllerDoHQueryStore.Lock()
	probeControllerDoHQueryStore.revision++
	probeControllerDoHQueryStore.items = append(probeControllerDoHQueryStore.items, record)
	if len(probeControllerDoHQueryStore.items) > probeControllerDoHMaxQueryRecords {
		offset := len(probeControllerDoHQueryStore.items) - probeControllerDoHMaxQueryRecords
		copy(probeControllerDoHQueryStore.items, probeControllerDoHQueryStore.items[offset:])
		probeControllerDoHQueryStore.items = probeControllerDoHQueryStore.items[:probeControllerDoHMaxQueryRecords]
	}
	probeControllerDoHQueryStore.Unlock()
}

func snapshotProbeControllerDoHQueryRecords(limit int) ([]probeControllerDoHQueryRecord, uint64) {
	probeControllerDoHQueryStore.RLock()
	defer probeControllerDoHQueryStore.RUnlock()
	if limit <= 0 || limit > len(probeControllerDoHQueryStore.items) {
		limit = len(probeControllerDoHQueryStore.items)
	}
	items := make([]probeControllerDoHQueryRecord, 0, limit)
	for index := len(probeControllerDoHQueryStore.items) - 1; index >= 0 && len(items) < limit; index-- {
		item := probeControllerDoHQueryStore.items[index]
		item.Answers = append([]string(nil), item.Answers...)
		items = append(items, item)
	}
	return items, probeControllerDoHQueryStore.revision
}

func clearProbeControllerDoHQueryRecords() {
	probeControllerDoHQueryStore.Lock()
	probeControllerDoHQueryStore.revision++
	probeControllerDoHQueryStore.items = probeControllerDoHQueryStore.items[:0]
	probeControllerDoHQueryStore.Unlock()
}

func summarizeProbeControllerDoHQueryRecords(items []probeControllerDoHQueryRecord) map[string]int {
	summary := map[string]int{"total": len(items), "direct": 0, "fake_ip": 0, "reject": 0, "error": 0}
	for _, item := range items {
		if _, ok := summary[item.Action]; ok {
			summary[item.Action]++
		}
		if item.Status == "error" {
			summary["error"]++
		}
	}
	return summary
}

func probeControllerDoHQueryTypeLabel(queryType dnsmessage.Type) string {
	switch queryType {
	case dnsmessage.TypeA:
		return "A"
	case dnsmessage.TypeAAAA:
		return "AAAA"
	case dnsmessage.TypeCNAME:
		return "CNAME"
	case dnsmessage.TypeMX:
		return "MX"
	case dnsmessage.TypeNS:
		return "NS"
	case dnsmessage.TypePTR:
		return "PTR"
	case dnsmessage.TypeSRV:
		return "SRV"
	case dnsmessage.TypeTXT:
		return "TXT"
	default:
		return fmt.Sprintf("TYPE%d", uint16(queryType))
	}
}

func truncateProbeControllerDoHText(value string, maxRunes int) string {
	clean := strings.TrimSpace(value)
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(clean)
	if len(runes) <= maxRunes {
		return clean
	}
	return string(runes[:maxRunes])
}

func maxInt64(left int64, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}
