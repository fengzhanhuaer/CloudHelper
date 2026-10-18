package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/cloudhelper/probe_node/internal/controllerlink"
)

func configureProbeControllerLink(raw string) {
	if dir, err := resolveDataDir(); err == nil {
		controllerlink.Default.Configure(raw, dir)
	}
}
func probeControllerDirectDial(ctx context.Context, network, address string) (net.Conn, error) {
	target := probeResolvedURLDialTarget{OriginalEndpoint: address, DialEndpoint: address}
	return newProbeResolvedDialContext(target, 5*time.Second)(ctx, network, address)
}
func newProbeControllerHTTPClient(raw string, timeout time.Duration) (*http.Client, func(), error) {
	configureProbeControllerLink(raw)
	base := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		target, e := resolveProbeLocalURLDialTarget(raw)
		if e != nil {
			return nil, e
		}
		return newProbeResolvedDialContext(target, 5*time.Second)(ctx, network, address)
	}
	base.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		target, e := resolveProbeLocalURLDialTarget(raw)
		if e != nil {
			return nil, e
		}
		return newProbeResolvedConnectionDialContext(target, 5*time.Second, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: target.ServerName})(ctx, network, address)
	}
	tr := controllerlink.Default.Wrap(raw, base, probeControllerDirectDial)
	client := &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, tr.CloseIdleConnections, nil
}
