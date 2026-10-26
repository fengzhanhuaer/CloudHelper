package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/cloudhelper/probe_node/internal/controllerlink"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const probeResolvedDialDefaultTimeout = 15 * time.Second

type probeResolvedURLDialTarget struct {
	OriginalEndpoint string
	DialEndpoint     string
	DialEndpoints    []string
	ServerName       string
	TLSEnabled       bool
}

func resolveProbeLocalURLDialTarget(rawURL string) (probeResolvedURLDialTarget, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return probeResolvedURLDialTarget{}, err
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return probeResolvedURLDialTarget{}, fmt.Errorf("url host is empty")
	}
	port := strings.TrimSpace(parsed.Port())
	if port == "" {
		switch strings.ToLower(strings.TrimSpace(parsed.Scheme)) {
		case "http", "ws":
			port = "80"
		case "https", "wss":
			port = "443"
		default:
			return probeResolvedURLDialTarget{}, fmt.Errorf("unsupported url scheme: %s", parsed.Scheme)
		}
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum <= 0 || portNum > 65535 {
		return probeResolvedURLDialTarget{}, fmt.Errorf("invalid url port: %s", port)
	}

	target := probeResolvedURLDialTarget{
		OriginalEndpoint: net.JoinHostPort(host, strconv.Itoa(portNum)),
		TLSEnabled:       strings.EqualFold(strings.TrimSpace(parsed.Scheme), "https") || strings.EqualFold(strings.TrimSpace(parsed.Scheme), "wss"),
	}

	if parsedIP := net.ParseIP(host); parsedIP != nil {
		target.DialEndpoint = net.JoinHostPort(parsedIP.String(), strconv.Itoa(portNum))
		return target, nil
	}

	ips, err := resolveProbeTransportIPs(context.Background(), host)
	if err != nil {
		return probeResolvedURLDialTarget{}, err
	}
	if len(ips) == 0 {
		return probeResolvedURLDialTarget{}, fmt.Errorf("resolved url host has no address: %s", host)
	}
	for _, ip := range ips {
		if parsed := net.ParseIP(strings.TrimSpace(ip)); parsed != nil {
			target.DialEndpoints = append(target.DialEndpoints, net.JoinHostPort(parsed.String(), strconv.Itoa(portNum)))
		}
	}
	if len(target.DialEndpoints) == 0 {
		return target, errors.New("resolved url host has no valid address")
	}
	target.DialEndpoint = target.DialEndpoints[0]
	target.ServerName = host
	return target, nil
}

func normalizeProbeResolvedDialTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return probeResolvedDialDefaultTimeout
	}
	return timeout
}

func newProbeResolvedDialContext(target probeResolvedURLDialTarget, timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	return newProbeResolvedConnectionDialContext(target, timeout, nil)
}

// Race full TLS handshakes too: a CDN address can accept TCP and then stall TLS.
func newProbeResolvedConnectionDialContext(target probeResolvedURLDialTarget, timeout time.Duration, tlsConfig *tls.Config) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, normalizeProbeResolvedDialTimeout(timeout))
		defer cancel()
		endpoints := []string{addr}
		matched := strings.EqualFold(strings.TrimSpace(addr), strings.TrimSpace(target.OriginalEndpoint))
		if matched {
			endpoints = target.DialEndpoints
			if len(endpoints) == 0 {
				endpoints = []string{target.DialEndpoint}
			}
		}
		// Limit parallel sockets for domains publishing many records.
		if len(endpoints) > 4 {
			endpoints = endpoints[:4]
		}
		type result struct {
			conn net.Conn
			err  error
		}
		results := make(chan result)
		dialCtx, stop := context.WithCancel(ctx)
		defer stop()
		for index, endpoint := range endpoints {
			go func(index int, endpoint string) {
				if index > 0 {
					if err := waitProbeUpgradeRetry(dialCtx, time.Duration(index)*250*time.Millisecond); err != nil {
						return
					}
				}
				if dialCtx.Err() != nil {
					return
				}
				if matched && probeResolvedDialUsesIPv4Bypass(endpoint) {
					if err := ensureProbeRouteTransportDirectBypass(endpoint); err != nil {
						logProbeWarnf("probe resolved dial direct bypass failed: target=%s err=%v", endpoint, err)
					}
				}
				dialer := applyProbeRouteEgressDialer(&net.Dialer{Timeout: normalizeProbeResolvedDialTimeout(timeout), KeepAlive: 30 * time.Second})
				conn, err := dialer.DialContext(dialCtx, probeRouteEgressDialNetwork(network, endpoint), endpoint)
				if err == nil && tlsConfig != nil {
					config := tlsConfig.Clone()
					if config.ServerName == "" {
						config.ServerName, _, _ = net.SplitHostPort(addr)
					}
					secured := tls.Client(conn, config)
					if err = secured.HandshakeContext(dialCtx); err != nil {
						conn.Close()
						conn = nil
					} else {
						conn = secured
					}
				}
				select {
				case results <- result{conn, err}:
				case <-dialCtx.Done():
					if conn != nil {
						conn.Close()
					}
				}
			}(index, endpoint)
		}
		var failures []error
		for range endpoints {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case result := <-results:
				if result.err == nil {
					return result.conn, nil
				}
				failures = append(failures, result.err)
			}
		}
		return nil, errors.Join(failures...)
	}
}

// The managed bypass routes cover IPv4 only. IPv6 uses the system IPv6
// routes; attempting to install an IPv4 host route for it is meaningless.
func probeResolvedDialUsesIPv4Bypass(endpoint string) bool {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.SplitN(host, "%", 2)[0])
	return ip == nil || ip.To4() != nil
}

func newProbeResolvedHTTPClientForURL(rawURL string, timeout time.Duration) (*http.Client, func(), error) {
	if controllerlink.IsProbeURL(rawURL) {
		return newProbeControllerHTTPClient(rawURL, timeout)
	}
	target, err := resolveProbeLocalURLDialTarget(rawURL)
	if err != nil {
		return nil, nil, err
	}
	dialTimeout := normalizeProbeResolvedDialTimeout(timeout)
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         newProbeResolvedDialContext(target, dialTimeout),
		TLSHandshakeTimeout: dialTimeout,
	}
	if target.TLSEnabled {
		transport.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: strings.TrimSpace(target.ServerName),
		}
		transport.DialTLSContext = newProbeResolvedConnectionDialContext(target, dialTimeout, transport.TLSClientConfig)
	}
	client := &http.Client{
		Timeout:   dialTimeout,
		Transport: transport,
	}
	return client, func() { transport.CloseIdleConnections() }, nil
}

func newProbeResolvedWebSocketDialerForURL(rawURL string, timeout time.Duration) (*websocket.Dialer, error) {
	target, err := resolveProbeLocalURLDialTarget(rawURL)
	if err != nil {
		return nil, err
	}
	dialer := &websocket.Dialer{
		HandshakeTimeout: normalizeProbeResolvedDialTimeout(timeout),
		Proxy:            nil,
		NetDialContext:   newProbeResolvedDialContext(target, timeout),
	}
	if target.TLSEnabled {
		dialer.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: strings.TrimSpace(target.ServerName),
		}
		dialer.NetDialTLSContext = newProbeResolvedConnectionDialContext(target, timeout, dialer.TLSClientConfig)
	}
	return dialer, nil
}
