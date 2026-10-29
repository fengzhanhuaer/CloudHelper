package controllerlink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestEndpointCooldownHTTPWebSocketAndIPv6Recovery(t *testing.T) {
	var challenges, sockets, v4Dials, v6Dials atomic.Int32
	var ipv6Recovered atomic.Bool
	var ipv6Supported atomic.Bool
	ipv6Supported.Store(true)
	direct := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "challenge") {
			fmt.Fprintf(w, `{"challenge":"nonce-%d"}`, challenges.Add(1))
			return
		}
		if r.URL.Path == "/api/probe" {
			c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if err == nil {
				sockets.Add(1)
				c.Close()
			}
			return
		}
		io.WriteString(w, "direct")
	}))
	defer direct.Close()
	primary := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "primary")
	}))
	defer primary.Close()
	hash := sha256.Sum256(direct.Certificate().RawSubjectPublicKeyInfo)
	v4 := Endpoint{"192.0.2.1", 15030, hex.EncodeToString(hash[:])}
	v6 := Endpoint{"2001:db8::1", 15030, v4.Pin}
	m := &Manager{entries: map[string]*state{}, endpointUsable: func(ip string) bool { return net.ParseIP(ip).To4() != nil || ipv6Supported.Load() }}
	m.Remember(primary.URL, "7", "secret")
	s := m.entry(primary.URL)
	s.data = Discovery{1, time.Now().Add(time.Hour).Unix(), []Endpoint{v6, v4}}
	s.lastDiscovery, s.lastProbe = time.Now(), time.Now()
	s.preferUntil = time.Now().Add(time.Hour)
	// Reproduce a previously usable IPv6 address losing connectivity.
	s.endpoints = map[Endpoint]endpointHealth{v6: {lastSuccess: time.Now().Add(-time.Minute)}}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		switch address {
		case "[2001:db8::1]:15030":
			v6Dials.Add(1)
			if !ipv6Recovered.Load() {
				return nil, &net.OpError{Op: "dial", Net: "tcp6", Err: errors.New("network unreachable")}
			}
		case "192.0.2.1:15030":
			v4Dials.Add(1)
		default:
			t.Errorf("unexpected address %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, direct.Listener.Addr().String())
	}
	tr := m.Wrap(primary.URL, primary.Client().Transport.(*http.Transport).Clone(), dial)
	defer tr.CloseIdleConnections()
	get := func(want string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), "GET", primary.URL+"/api/probe/route/config", nil)
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(body) != want {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
	get("direct")
	if v6Dials.Load() != 1 || v4Dials.Load() != 1 {
		t.Fatal("unreachable IPv6 did not fall through to IPv4", v6Dials.Load(), v4Dials.Load())
	}
	// Choosing direct again must skip the failed address, also across transports.
	m.Feedback(primary.URL, false, 0, false)
	get("direct")
	wsURL := "wss" + strings.TrimPrefix(primary.URL, "https") + "/api/probe"
	ws := &websocket.Dialer{TLSClientConfig: primary.Client().Transport.(*http.Transport).TLSClientConfig}
	connect := func() {
		t.Helper()
		c, resp, isDirect, err := m.DialWebSocket(t.Context(), wsURL, http.Header{}, ws, dial)
		if err != nil || !isDirect {
			if resp != nil {
				resp.Body.Close()
			}
			t.Fatalf("direct=%v err=%v", isDirect, err)
		}
		c.Close()
	}
	connect()
	if v6Dials.Load() != 1 || sockets.Load() != 1 || challenges.Load() != 1 {
		t.Fatal("HTTP/WS did not share cooldown or opened duplicate sessions")
	}
	// With all direct endpoints cooling down, the healthy primary still works.
	m.endpointFeedback(t.Context(), primary.URL, v4, false)
	get("primary")
	if v6Dials.Load() != 1 {
		t.Fatal("retried cooling IPv6")
	}
	// Expiry re-enables IPv6; do not globally or permanently disable it.
	m.mu.Lock()
	h := s.endpoints[v6]
	h.retryAt = time.Now().Add(-time.Second)
	s.endpoints[v6] = h
	m.mu.Unlock()
	ipv6Recovered.Store(true)
	m.Feedback(primary.URL, false, 0, false)
	connect()
	get("direct")
	if sockets.Load() != 2 || v6Dials.Load() != 4 {
		t.Fatal("IPv6 did not recover for authenticated WS and HTTP", sockets.Load(), v6Dials.Load())
	}
	// Network capability changes must override a previously successful IPv6
	// endpoint, without deleting it from the authenticated discovery cache.
	ipv6Supported.Store(false)
	m.endpointFeedback(t.Context(), primary.URL, v4, true)
	m.Feedback(primary.URL, false, 0, false)
	get("direct")
	connect()
	if v6Dials.Load() != 4 || len(s.data.Endpoints) != 2 {
		t.Fatal("unusable IPv6 dialed or trusted address forgotten")
	}
	ipv6Supported.Store(true)
	m.endpointFeedback(t.Context(), primary.URL, v4, false)
	m.Feedback(primary.URL, false, 0, false)
	get("direct")
	if v6Dials.Load() != 5 {
		t.Fatal("restored IPv6 capability not picked up", v6Dials.Load())
	}
}

func TestEndpointCooldownBoundedIsolatedAndCancellation(t *testing.T) {
	const origin = "https://controller.test"
	v4 := Endpoint{"192.0.2.1", 15030, strings.Repeat("a", 64)}
	v6 := Endpoint{"2001:db8::1", 15030, v4.Pin}
	m := &Manager{entries: map[string]*state{}, endpointUsable: func(string) bool { return true }}
	s := m.entry(origin)
	s.data = Discovery{1, time.Now().Add(time.Hour).Unix(), []Endpoint{v6, v4}}
	d, _ := m.snapshot(origin)
	if d.Endpoints[0] != v4 || s.data.Endpoints[0] != v6 {
		t.Fatal("initial IPv4 priority or trusted discovery order changed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m.endpointFeedback(ctx, origin, v6, false)
	if len(s.endpoints) != 0 {
		t.Fatal("cancellation penalized endpoint")
	}
	for i, delay := range []time.Duration{30, 60, 120, 240, 300, 300} {
		start := time.Now()
		m.endpointFeedback(t.Context(), origin, v6, false)
		got := s.endpoints[v6].retryAt.Sub(start)
		if got < delay*time.Second || got > delay*time.Second+time.Second {
			t.Fatalf("failure %d cooldown %s", i+1, got)
		}
	}
	d, _ = m.snapshot(origin)
	if len(d.Endpoints) != 1 || d.Endpoints[0] != v4 {
		t.Fatal("failed IPv6 not suppressed")
	}
	other := m.entry("https://other.test")
	other.data = s.data
	d, _ = m.snapshot("https://other.test")
	if len(d.Endpoints) != 2 {
		t.Fatal("cross-origin cooldown")
	}
	rotated := v6
	rotated.Pin = strings.Repeat("b", 64)
	s.data.Endpoints = []Endpoint{rotated}
	m.endpointFeedback(t.Context(), origin, v6, false)
	if len(s.endpoints) != 1 {
		t.Fatal("stale endpoints not pruned")
	}
	d, _ = m.snapshot(origin)
	if len(d.Endpoints) != 1 {
		t.Fatal("old pin poisoned new endpoint")
	}
	m.endpointFeedback(t.Context(), origin, rotated, true)
	if s.endpoints[rotated].failures != 0 || !s.endpoints[rotated].retryAt.IsZero() {
		t.Fatal("success did not clear backoff")
	}
}
