package main

import (
	"bytes"
	"context"
	"github.com/cloudhelper/probe_node/internal/controllerlink"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeControllerDirectIPv6UsesSystemRoute(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer listener.Close()
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := listener.Accept()
		if err == nil {
			defer c.Close()
			io.WriteString(c, "ipv6")
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	c, err := probeControllerDirectDial(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ipv6" {
		t.Fatalf("IPv6 communication failed: %q %v", buf, err)
	}
	<-done
	log.SetOutput(previous)
	if strings.Contains(logs.String(), "direct bypass failed") {
		t.Fatal("IPv6 incorrectly attempted IPv4 bypass:", logs.String())
	}
}

func TestProbeResolvedDialIPv6SkipsIPv4Bypass(t *testing.T) {
	for address, want := range map[string]bool{
		"[2001:db8::1]:15030":      false,
		"[::ffff:192.0.2.1]:15030": true,
		"[fe80::1%12]:15030":       false,
		"192.0.2.1:15030":          true,
	} {
		if got := probeResolvedDialUsesIPv4Bypass(address); got != want {
			t.Errorf("%s: bypass=%v want=%v", address, got, want)
		}
	}
}

func TestProbeResolvedDialFallsBackWhenFirstTLSStalls(t *testing.T) {
	slow, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	released := make(chan struct{})
	defer close(released)
	closed := make(chan struct{})
	go func() {
		conn, err := slow.Accept()
		if err != nil {
			close(closed)
			return
		}
		defer conn.Close()
		defer close(closed)
		go func() { <-released; conn.Close() }()
		io.Copy(io.Discard, conn)
	}()
	good := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer good.Close()
	config := good.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	// Verify the test server certificate against its actual IP, retaining verification.
	config.ServerName = "127.0.0.1"
	target := probeResolvedURLDialTarget{OriginalEndpoint: "controller.test:443", DialEndpoints: []string{slow.Addr().String(), good.Listener.Addr().String()}}
	started := time.Now()
	conn, err := newProbeResolvedConnectionDialContext(target, 3*time.Second, config)(t.Context(), "tcp", target.OriginalEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if time.Since(started) > 2*time.Second {
		t.Fatal("stalled first TLS exhausted budget")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("losing TLS connection leaked")
	}
}

func TestProbeResolvedDialCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	target := probeResolvedURLDialTarget{OriginalEndpoint: "controller.test:443", DialEndpoints: []string{"127.0.0.1:1", "127.0.0.1:2"}}
	started := time.Now()
	conn, err := newProbeResolvedDialContext(target, time.Second)(ctx, "tcp", target.OriginalEndpoint)
	if err == nil || conn != nil || time.Since(started) > time.Second {
		t.Fatalf("conn=%v err=%v", conn, err)
	}
}

func TestNewProbeResolvedHTTPClientForURLDisablesProxy(t *testing.T) {
	resetProbeLocalDNSServiceForTest()
	defer resetProbeLocalDNSServiceForTest()

	probeLocalDNSBootstrapLookupIPv4 = func(domain string) ([]string, error) {
		if domain != "controller.example.com" {
			t.Fatalf("unexpected bootstrap domain: %s", domain)
		}
		return []string{"203.0.113.10"}, nil
	}

	client, closeClient, err := newProbeResolvedHTTPClientForURL("https://controller.example.com/api/probe", 5*time.Second)
	if err != nil {
		t.Fatalf("newProbeResolvedHTTPClientForURL returned error: %v", err)
	}
	defer closeClient()

	wrapped, ok := client.Transport.(*controllerlink.Transport)
	if !ok {
		t.Fatalf("expected adaptive controller transport: %T", client.Transport)
	}
	transport := wrapped.Primary
	ok = transport != nil
	if !ok || transport == nil {
		t.Fatalf("transport type=%T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatalf("expected http transport proxy to be nil")
	}
}

func TestNewProbeResolvedWebSocketDialerForURLDisablesProxy(t *testing.T) {
	resetProbeLocalDNSServiceForTest()
	defer resetProbeLocalDNSServiceForTest()

	probeLocalDNSBootstrapLookupIPv4 = func(domain string) ([]string, error) {
		if domain != "controller.example.com" {
			t.Fatalf("unexpected bootstrap domain: %s", domain)
		}
		return []string{"203.0.113.11"}, nil
	}

	dialer, err := newProbeResolvedWebSocketDialerForURL("wss://controller.example.com/api/probe", 5*time.Second)
	if err != nil {
		t.Fatalf("newProbeResolvedWebSocketDialerForURL returned error: %v", err)
	}
	if dialer.Proxy != nil {
		t.Fatalf("expected websocket dialer proxy to be nil")
	}
}
