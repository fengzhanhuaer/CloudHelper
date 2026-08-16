package core

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	probeSubscriptionFetchTimeout      = 55 * time.Second
	probeSubscriptionFetchMaxRedirects = 5
	probeSubscriptionFetchUserAgent    = "clash.meta"
	probeSubscriptionFetchAccept       = "application/yaml, application/x-yaml, text/yaml, text/plain;q=0.9, */*;q=0.1"
)

type probeSubscriptionFetchCommand struct {
	Type       string `json:"type"`
	RequestID  string `json:"request_id"`
	URL        string `json:"url"`
	MaxBytes   int64  `json:"max_bytes"`
	TimeoutSec int    `json:"timeout_sec"`
	Timestamp  string `json:"timestamp"`
}

type probeSubscriptionFetchResultMessage struct {
	Type          string `json:"type"`
	RequestID     string `json:"request_id"`
	NodeID        string `json:"node_id"`
	OK            bool   `json:"ok"`
	ContentBase64 string `json:"content_base64,omitempty"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
	Size          int64  `json:"size,omitempty"`
	Error         string `json:"error,omitempty"`
	Timestamp     string `json:"timestamp,omitempty"`
}

type probeControllerSubscriptionFetchRequestError struct {
	message string
}

func (e *probeControllerSubscriptionFetchRequestError) Error() string {
	return e.message
}

var probeSubscriptionFetchRequestSeq atomic.Uint64

var probeControllerSubscriptionFetchLookupIP = net.DefaultResolver.LookupIPAddr
var probeControllerSubscriptionFetchDialContext = (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext
var probeControllerSubscriptionFetchHTTPDo = func(client *http.Client, request *http.Request) (*http.Response, error) {
	return client.Do(request)
}
var probeControllerSubscriptionFetchDo = doProbeControllerSubscriptionFetch

var probeSubscriptionFetchWaiters = struct {
	mu   sync.Mutex
	data map[string]chan probeSubscriptionFetchResultMessage
}{data: make(map[string]chan probeSubscriptionFetchResultMessage)}

func fetchProbeSpecialExitSubscriptionFromNode(ctx context.Context, nodeID, rawURL string) ([]byte, error) {
	normalizedID := normalizeProbeNodeID(nodeID)
	if normalizedID == "" {
		return nil, fmt.Errorf("subscription fetch probe is required")
	}
	node, ok := getProbeNodeByID(normalizedID)
	if !ok {
		return nil, fmt.Errorf("subscription fetch probe not found")
	}
	if strings.EqualFold(strings.TrimSpace(node.TargetSystem), "android") {
		return nil, fmt.Errorf("subscription fetch probe uses an unsupported Android build")
	}
	session, ok := getProbeSession(normalizedID)
	if !ok {
		return nil, fmt.Errorf("subscription fetch probe is offline")
	}

	requestID := newProbeSubscriptionFetchRequestID(normalizedID)
	waiter := make(chan probeSubscriptionFetchResultMessage, 1)
	probeSubscriptionFetchWaiters.mu.Lock()
	probeSubscriptionFetchWaiters.data[requestID] = waiter
	probeSubscriptionFetchWaiters.mu.Unlock()
	defer func() {
		probeSubscriptionFetchWaiters.mu.Lock()
		delete(probeSubscriptionFetchWaiters.data, requestID)
		probeSubscriptionFetchWaiters.mu.Unlock()
	}()

	command := probeSubscriptionFetchCommand{
		Type: "subscription_fetch", RequestID: requestID, URL: strings.TrimSpace(rawURL),
		MaxBytes: probeSpecialExitSubscriptionMaxBytes, TimeoutSec: int(probeSpecialExitSubscriptionTimeout / time.Second),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if err := session.writeJSON(command); err != nil {
		unregisterProbeSession(normalizedID, session)
		return nil, fmt.Errorf("subscription fetch command could not be sent")
	}

	timer := time.NewTimer(probeSubscriptionFetchTimeout)
	defer timer.Stop()
	select {
	case result := <-waiter:
		if normalizeProbeNodeID(result.NodeID) != normalizedID {
			return nil, fmt.Errorf("subscription fetch probe identity mismatch")
		}
		if !result.OK {
			return nil, errors.New(sanitizeProbeSubscriptionFetchResultError(result.Error, rawURL))
		}
		return decodeProbeSubscriptionFetchContent(result)
	case <-timer.C:
		return nil, fmt.Errorf("subscription fetch probe timed out")
	case <-ctx.Done():
		return nil, fmt.Errorf("subscription fetch was canceled")
	}
}

func fetchProbeSpecialExitSubscriptionFromController(ctx context.Context, rawURL string) ([]byte, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, probeSpecialExitSubscriptionTimeout)
	defer cancel()
	currentURL := strings.TrimSpace(rawURL)
	for redirects := 0; ; redirects++ {
		target, ips, err := validateProbeControllerSubscriptionFetchURL(fetchCtx, currentURL)
		if err != nil {
			return nil, err
		}
		response, err := probeControllerSubscriptionFetchDo(fetchCtx, target, ips)
		if err != nil {
			if fetchCtx.Err() != nil {
				return nil, fmt.Errorf("subscription fetch timed out or was canceled")
			}
			return nil, classifyProbeControllerSubscriptionFetchRequestError(err)
		}
		if probeSubscriptionRedirectStatus(response.StatusCode) {
			location := strings.TrimSpace(response.Header.Get("Location"))
			_ = response.Body.Close()
			if redirects >= probeSubscriptionFetchMaxRedirects {
				return nil, fmt.Errorf("subscription exceeded %d redirects", probeSubscriptionFetchMaxRedirects)
			}
			if location == "" {
				return nil, fmt.Errorf("subscription redirect is missing a location")
			}
			next, resolveErr := target.Parse(location)
			if resolveErr != nil {
				return nil, fmt.Errorf("subscription redirect is invalid")
			}
			currentURL = next.String()
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusForbidden {
				return nil, fmt.Errorf("subscription returned HTTP 403; the provider rejected or temporarily rate-limited this egress IP")
			}
			return nil, fmt.Errorf("subscription returned HTTP %d", response.StatusCode)
		}
		content, readErr := io.ReadAll(io.LimitReader(response.Body, probeSpecialExitSubscriptionMaxBytes+1))
		_ = response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("subscription response could not be read")
		}
		if len(content) > probeSpecialExitSubscriptionMaxBytes {
			return nil, fmt.Errorf("subscription exceeded %d bytes", probeSpecialExitSubscriptionMaxBytes)
		}
		if len(content) == 0 {
			return nil, fmt.Errorf("subscription response is empty")
		}
		return content, nil
	}
}

func doProbeControllerSubscriptionFetch(ctx context.Context, target *url.URL, ips []netip.Addr) (*http.Response, error) {
	port := strings.TrimSpace(target.Port())
	if port == "" {
		port = "443"
	}
	dialAddresses := probeControllerSubscriptionFetchDialAddresses(ips, port)
	if len(dialAddresses) == 0 {
		return nil, &probeControllerSubscriptionFetchRequestError{message: "subscription has no usable public dial address"}
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: target.Hostname()}, Proxy: nil}
	transport.DialContext = func(dialCtx context.Context, network, _ string) (net.Conn, error) {
		var lastErr error
		for _, address := range dialAddresses {
			conn, dialErr := probeControllerSubscriptionFetchDialContext(dialCtx, network, address)
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer transport.CloseIdleConnections()
	request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if requestErr != nil {
		return nil, &probeControllerSubscriptionFetchRequestError{message: "subscription request is invalid"}
	}
	request.Header.Set("User-Agent", probeSubscriptionFetchUserAgent)
	request.Header.Set("Accept", probeSubscriptionFetchAccept)
	response, requestErr := probeControllerSubscriptionFetchHTTPDo(client, request)
	if requestErr != nil {
		return nil, classifyProbeControllerSubscriptionFetchRequestError(requestErr)
	}
	return response, nil
}

func probeControllerSubscriptionFetchDialAddresses(ips []netip.Addr, port string) []string {
	ordered := make([]netip.Addr, 0, len(ips))
	for _, address := range ips {
		if address.Is4() {
			ordered = append(ordered, address)
		}
	}
	for _, address := range ips {
		if !address.Is4() {
			ordered = append(ordered, address)
		}
	}
	seen := make(map[string]struct{}, len(ordered))
	result := make([]string, 0, len(ordered))
	for _, address := range ordered {
		if !address.IsValid() {
			continue
		}
		dialAddress := net.JoinHostPort(address.String(), port)
		if _, ok := seen[dialAddress]; ok {
			continue
		}
		seen[dialAddress] = struct{}{}
		result = append(result, dialAddress)
	}
	return result
}

func classifyProbeControllerSubscriptionFetchRequestError(err error) error {
	if err == nil {
		return nil
	}
	var safeErr *probeControllerSubscriptionFetchRequestError
	if errors.As(err, &safeErr) {
		return safeErr
	}
	if errors.Is(err, context.Canceled) {
		return &probeControllerSubscriptionFetchRequestError{message: "subscription request was canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &probeControllerSubscriptionFetchRequestError{message: "subscription network request timed out"}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &probeControllerSubscriptionFetchRequestError{message: "subscription network request timed out"}
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection refused"):
		return &probeControllerSubscriptionFetchRequestError{message: "subscription TCP connection was refused"}
	case strings.Contains(message, "network is unreachable"), strings.Contains(message, "no route to host"):
		return &probeControllerSubscriptionFetchRequestError{message: "subscription network is unreachable"}
	case strings.Contains(message, "certificate"), strings.Contains(message, "x509"):
		return &probeControllerSubscriptionFetchRequestError{message: "subscription TLS certificate verification failed"}
	case strings.Contains(message, "tls"), strings.Contains(message, "handshake"):
		return &probeControllerSubscriptionFetchRequestError{message: "subscription TLS handshake failed"}
	case strings.Contains(message, "connection reset"), strings.Contains(message, "forcibly closed"):
		return &probeControllerSubscriptionFetchRequestError{message: "subscription connection was reset"}
	default:
		return &probeControllerSubscriptionFetchRequestError{message: "subscription network request failed"}
	}
}

func validateProbeControllerSubscriptionFetchURL(ctx context.Context, raw string) (*url.URL, []netip.Addr, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target == nil || !strings.EqualFold(target.Scheme, "https") || target.User != nil || strings.TrimSpace(target.Hostname()) == "" {
		return nil, nil, fmt.Errorf("subscription URL must be HTTPS without credentials")
	}
	if port := strings.TrimSpace(target.Port()); port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return nil, nil, fmt.Errorf("subscription URL port must be between 1 and 65535")
		}
	}
	resolved, err := probeControllerSubscriptionFetchLookupIP(ctx, target.Hostname())
	if err != nil || len(resolved) == 0 {
		return nil, nil, fmt.Errorf("subscription host resolution failed")
	}
	ips := make([]netip.Addr, 0, len(resolved))
	for _, item := range resolved {
		address, ok := netip.AddrFromSlice(item.IP)
		if !ok {
			return nil, nil, fmt.Errorf("subscription host returned an invalid address")
		}
		address = address.Unmap()
		if !probeControllerSubscriptionFetchPublicAddr(address) {
			return nil, nil, fmt.Errorf("subscription host resolves to a non-public address")
		}
		ips = append(ips, address)
	}
	return target, ips, nil
}

func probeControllerSubscriptionFetchPublicAddr(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsUnspecified() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() {
		return false
	}
	blocked := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"224.0.0.0/4", "240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23",
		"2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10",
	}
	for _, raw := range blocked {
		if netip.MustParsePrefix(raw).Contains(address) {
			return false
		}
	}
	return true
}

func probeSubscriptionRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func sanitizeProbeSubscriptionFetchResultError(raw, rawURL string) string {
	message := strings.TrimSpace(raw)
	if message == "" {
		message = "subscription fetch failed"
	}
	redactions := []string{strings.TrimSpace(rawURL)}
	if target, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && target != nil {
		redactions = append(redactions, target.Host, target.Hostname(), target.RawQuery)
		for _, values := range target.Query() {
			redactions = append(redactions, values...)
		}
	}
	for _, value := range redactions {
		value = strings.TrimSpace(value)
		if len(value) >= 4 {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	message = strings.Join(strings.Fields(message), " ")
	runes := []rune(message)
	if len(runes) > 240 {
		message = string(runes[:240])
	}
	return message
}

func decodeProbeSubscriptionFetchContent(result probeSubscriptionFetchResultMessage) ([]byte, error) {
	maxEncoded := base64.StdEncoding.EncodedLen(probeSpecialExitSubscriptionMaxBytes)
	if len(result.ContentBase64) == 0 || len(result.ContentBase64) > maxEncoded {
		return nil, fmt.Errorf("subscription fetch result size is invalid")
	}
	content, err := base64.StdEncoding.DecodeString(result.ContentBase64)
	if err != nil || len(content) == 0 || len(content) > probeSpecialExitSubscriptionMaxBytes || result.Size != int64(len(content)) {
		return nil, fmt.Errorf("subscription fetch result content is invalid")
	}
	expectedHash := strings.ToLower(strings.TrimSpace(result.ContentSHA256))
	if len(expectedHash) != sha256.Size*2 {
		return nil, fmt.Errorf("subscription fetch result hash is invalid")
	}
	sum := sha256.Sum256(content)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), expectedHash) {
		return nil, fmt.Errorf("subscription fetch result hash mismatch")
	}
	return content, nil
}

func consumeProbeSubscriptionFetchResult(result probeSubscriptionFetchResultMessage) {
	requestID := strings.TrimSpace(result.RequestID)
	if requestID == "" {
		return
	}
	probeSubscriptionFetchWaiters.mu.Lock()
	waiter, ok := probeSubscriptionFetchWaiters.data[requestID]
	if ok {
		delete(probeSubscriptionFetchWaiters.data, requestID)
	}
	probeSubscriptionFetchWaiters.mu.Unlock()
	if !ok {
		return
	}
	select {
	case waiter <- result:
	default:
	}
}

func newProbeSubscriptionFetchRequestID(nodeID string) string {
	sequence := probeSubscriptionFetchRequestSeq.Add(1)
	return fmt.Sprintf("subscription-fetch-%s-%d-%d", normalizeProbeNodeID(nodeID), time.Now().UTC().UnixNano(), sequence)
}
