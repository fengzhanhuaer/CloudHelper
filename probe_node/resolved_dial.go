package main

import (
	"context"
	"crypto/tls"
	"fmt"
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

	ips, err := resolveProbeLocalDNSIPv4s(host)
	if err != nil {
		return probeResolvedURLDialTarget{}, err
	}
	if len(ips) == 0 {
		return probeResolvedURLDialTarget{}, fmt.Errorf("resolved url host has no ipv4 result: %s", host)
	}
	target.DialEndpoint = net.JoinHostPort(strings.TrimSpace(ips[0]), strconv.Itoa(portNum))
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
	dialTimeout := normalizeProbeResolvedDialTimeout(timeout)
	return func(ctx context.Context, network string, addr string) (net.Conn, error) {
		dialer := applyProbeRouteEgressDialer(&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		})
		if strings.EqualFold(strings.TrimSpace(addr), strings.TrimSpace(target.OriginalEndpoint)) {
			if err := ensureProbeRouteTransportDirectBypass(target.DialEndpoint); err != nil {
				logProbeWarnf("probe resolved dial direct bypass failed: target=%s err=%v", target.DialEndpoint, err)
			}
			return dialer.DialContext(ctx, probeRouteEgressDialNetwork(network, target.DialEndpoint), target.DialEndpoint)
		}
		return dialer.DialContext(ctx, network, addr)
	}
}

func newProbeResolvedHTTPClientForURL(rawURL string, timeout time.Duration) (*http.Client, func(), error) {
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
	}
	return dialer, nil
}
