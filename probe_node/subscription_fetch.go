package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeSubscriptionFetchMaxBytes     = 8 << 20
	probeSubscriptionFetchDefaultLimit = 20 * time.Second
	probeSubscriptionFetchMaxLimit     = 30 * time.Second
	probeSubscriptionFetchWriteLimit   = 30 * time.Second
	probeSubscriptionFetchUserAgent    = "clash.meta"
	probeSubscriptionFetchAccept       = "application/yaml, application/x-yaml, text/yaml, text/plain;q=0.9, */*;q=0.1"
	probeSubscriptionFetchMaxRedirects = 5
)

type probeSubscriptionFetchResultPayload struct {
	Type          string `json:"type"`
	RequestID     string `json:"request_id"`
	NodeID        string `json:"node_id"`
	OK            bool   `json:"ok"`
	ContentBase64 string `json:"content_base64,omitempty"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
	Size          int64  `json:"size,omitempty"`
	Error         string `json:"error,omitempty"`
	Timestamp     string `json:"timestamp"`
}

var probeSubscriptionFetchLookupIP = net.DefaultResolver.LookupIPAddr
var probeSubscriptionFetchDialContext = (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext
var probeSubscriptionFetchContent = fetchProbeSubscriptionContent
var probeSubscriptionFetchDo = doProbeSubscriptionFetch

func runProbeSubscriptionFetch(cmd probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	requestID := strings.TrimSpace(cmd.RequestID)
	if requestID == "" {
		return
	}
	maxBytes := cmd.MaxBytes
	if maxBytes <= 0 || maxBytes > probeSubscriptionFetchMaxBytes {
		maxBytes = probeSubscriptionFetchMaxBytes
	}
	timeout := time.Duration(cmd.TimeoutSec) * time.Second
	if timeout <= 0 || timeout > probeSubscriptionFetchMaxLimit {
		timeout = probeSubscriptionFetchDefaultLimit
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	content, err := probeSubscriptionFetchContent(ctx, cmd.URL, maxBytes)
	payload := probeSubscriptionFetchResultPayload{
		Type: "subscription_fetch_result", RequestID: requestID, NodeID: strings.TrimSpace(identity.NodeID),
		OK: err == nil, Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if err != nil {
		payload.Error = sanitizeProbeSubscriptionFetchError(err, cmd.URL)
	} else {
		sum := sha256.Sum256(content)
		payload.Size = int64(len(content))
		payload.ContentSHA256 = hex.EncodeToString(sum[:])
		payload.ContentBase64 = base64.StdEncoding.EncodeToString(content)
	}
	if writeErr := writeProbeSubscriptionFetchResult(stream, encoder, writeMu, payload); writeErr != nil {
		log.Printf("probe subscription response send failed: request_id=%s err=%v", requestID, writeErr)
	}
}

func writeProbeSubscriptionFetchResult(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, payload probeSubscriptionFetchResultPayload) error {
	if writeMu != nil {
		writeMu.Lock()
		defer writeMu.Unlock()
	}
	_ = stream.SetWriteDeadline(time.Now().Add(probeSubscriptionFetchWriteLimit))
	err := encoder.Encode(payload)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}

func fetchProbeSubscriptionContent(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	currentURL := strings.TrimSpace(rawURL)
	for redirects := 0; ; redirects++ {
		target, ips, err := validateProbeSubscriptionFetchURL(ctx, currentURL)
		if err != nil {
			return nil, err
		}
		response, err := probeSubscriptionFetchDo(ctx, target, ips)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("subscription fetch timed out or was canceled")
			}
			return nil, fmt.Errorf("subscription fetch failed")
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
			return nil, fmt.Errorf("subscription returned HTTP %d", response.StatusCode)
		}
		content, readErr := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
		_ = response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("subscription response could not be read")
		}
		if int64(len(content)) > maxBytes {
			return nil, fmt.Errorf("subscription exceeded %d bytes", maxBytes)
		}
		if len(content) == 0 {
			return nil, fmt.Errorf("subscription response is empty")
		}
		return content, nil
	}
}

func doProbeSubscriptionFetch(ctx context.Context, target *url.URL, ips []netip.Addr) (*http.Response, error) {
	port := strings.TrimSpace(target.Port())
	if port == "" {
		port = "443"
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: target.Hostname()}, Proxy: nil}
	transport.DialContext = func(dialCtx context.Context, network, _ string) (net.Conn, error) {
		return probeSubscriptionFetchDialContext(dialCtx, network, net.JoinHostPort(ips[0].String(), port))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("subscription request is invalid")
	}
	request.Header.Set("User-Agent", probeSubscriptionFetchUserAgent)
	request.Header.Set("Accept", probeSubscriptionFetchAccept)
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	transport.CloseIdleConnections()
	return response, err
}

func probeSubscriptionRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func validateProbeSubscriptionFetchURL(ctx context.Context, raw string) (*url.URL, []netip.Addr, error) {
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
	resolved, err := probeSubscriptionFetchLookupIP(ctx, target.Hostname())
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
		if !probeSubscriptionFetchPublicAddr(address) {
			return nil, nil, fmt.Errorf("subscription host resolves to a non-public address")
		}
		ips = append(ips, address)
	}
	return target, ips, nil
}

func probeSubscriptionFetchPublicAddr(address netip.Addr) bool {
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

func sanitizeProbeSubscriptionFetchError(err error, rawURL string) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "subscription fetch failed"
	}
	redactions := []string{strings.TrimSpace(rawURL)}
	if target, parseErr := url.Parse(strings.TrimSpace(rawURL)); parseErr == nil && target != nil {
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
