package main

import (
	"net/http"
	"testing"
	"time"
)

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

	transport, ok := client.Transport.(*http.Transport)
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
