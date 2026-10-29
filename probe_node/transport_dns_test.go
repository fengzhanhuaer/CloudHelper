package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func TestProbeTransportDNSDualStackAndFamilyFailure(t *testing.T) {
	old := probeTransportLookupIPv6
	defer func() { probeTransportLookupIPv6 = old }()
	probeTransportLookupIPv6 = func(context.Context, string) ([]string, error) { return []string{"2001:db8::1", "2001:db8::2"}, nil }
	ips, err := resolveProbeTransportIPsWithLookup(t.Context(), "transport.example", func(string) ([]string, error) { return []string{"192.0.2.1", "192.0.2.2", "198.18.0.5"}, nil })
	if err != nil || strings.Join(ips, ",") != "192.0.2.1,2001:db8::1,192.0.2.2,2001:db8::2" {
		t.Fatalf("ips=%v err=%v", ips, err)
	}
	ips, err = resolveProbeTransportIPsWithLookup(t.Context(), "transport.example", func(string) ([]string, error) { return nil, errors.New("no A") })
	if err != nil || len(ips) != 2 || net.ParseIP(ips[0]).To4() != nil {
		t.Fatalf("AAAA-only ips=%v err=%v", ips, err)
	}
	probeTransportLookupIPv6 = func(ctx context.Context, _ string) ([]string, error) { <-ctx.Done(); return nil, ctx.Err() }
	started := time.Now()
	ips, err = resolveProbeTransportIPsWithLookup(t.Context(), "transport.example", func(string) ([]string, error) { return []string{"192.0.2.1"}, nil })
	if err != nil || len(ips) != 1 || time.Since(started) > time.Second {
		t.Fatalf("slow AAAA delayed IPv4 ips=%v err=%v", ips, err)
	}
	ips, err = resolveProbeTransportIPsWithLookup(t.Context(), "::1", func(string) ([]string, error) { t.Fatal("literal queried DNS"); return nil, nil })
	if err != nil || len(ips) != 1 || ips[0] != "::1" {
		t.Fatalf("literal %v %v", ips, err)
	}
	probeTransportLookupIPv6 = func(context.Context, string) ([]string, error) {
		t.Error("AAAA queried without local IPv6 support")
		return []string{"2001:db8::1"}, nil
	}
	ips, err = resolveProbeTransportIPsWithLookup(t.Context(), "transport.example", func(string) ([]string, error) {
		time.Sleep(250 * time.Millisecond) // Unusable AAAA must not win the DNS grace window.
		return []string{"192.0.2.1"}, nil
	}, false)
	if err != nil || strings.Join(ips, ",") != "192.0.2.1" {
		t.Fatalf("unusable AAAA displaced delayed IPv4: %v %v", ips, err)
	}
}

func TestProbeTransportIPv6AuthenticatedCarrierSlots(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip(err)
	}
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeRouteAuthTicketStoreForTest()
	resetProbeRouteAuthIPStateForTest()
	t.Cleanup(resetProbeRouteAuthTicketStoreForTest)
	t.Cleanup(resetProbeRouteAuthIPStateForTest)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	routeID, secret := "vrouter-ipv6-carrier-test", "fixture-carrier-secret"
	key := base64.StdEncoding.EncodeToString(pub)
	ticket := buildProbeRouteUserAuthTicketForTest(t, priv, routeID, key, "7", "8")
	rememberProbeRouteAuthTicket(routeID, ticket)
	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: routeID, secret: secret, authTicket: ticket, rawPublicKey: key, userPublicKey: pub, fromNodeID: "7", toNodeID: "8", localNodeID: "8", peerNodeID: "7", identity: nodeIdentity{NodeID: "8"}}, stopCh: make(chan struct{})}
	probeVirtualRouterRuntimeState.mu.Lock()
	oldRuntimes := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{routeID: rt}
	probeVirtualRouterRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		closeProbeVirtualRouterFrameLinks("IPv6 carrier test")
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = oldRuntimes
		probeVirtualRouterRuntimeState.mu.Unlock()
	})
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.localNodeID = "7"
	probeVirtualRouterState.mu.Unlock()
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	writeProbeVirtualRouterTestTLSCertificate(t)
	rt.cfg.listenPort = port
	if err := startProbeVirtualRouterRelayServer(rt); err != nil {
		t.Fatal(err)
	}
	defer releaseProbeVirtualRouterRelayServer(rt)
	for slot := range 4 {
		conn, err := openProbeRouteRelayWebSocketNetConn(routeID, secret, "::1", port, probeRouteBridgeRoleToNext, probeRouteRelayModeBridge, "vrouter-carrier-slot-"+strconv.Itoa(slot), "::1", "::1", 2*time.Second, false)
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
		defer conn.Close()
		if !probeVirtualRouterConnSupportsMultiCarrier(conn) {
			t.Fatal("multi-slot negotiation missing")
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		probeVirtualRouterFrameLinkState.mu.Lock()
		link := probeVirtualRouterFrameLinkState.links[probeVirtualRouterFrameLinkKey(rt, "", "", nil)]
		probeVirtualRouterFrameLinkState.mu.Unlock()
		count := 0
		if link != nil {
			link.mu.Lock()
			count = len(link.carriers)
			link.mu.Unlock()
		}
		if count == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("IPv6 authenticated slots=%d", count)
		}
		time.Sleep(5 * time.Millisecond)
	}
	bad, err := openProbeRouteRelayWebSocketNetConn(routeID, "wrong-secret", "::1", port, probeRouteBridgeRoleToNext, probeRouteRelayModeBridge, "vrouter-carrier-slot-4", "::1", "::1", 2*time.Second, false)
	if bad != nil {
		bad.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "status=401") {
		t.Fatalf("bad auth accepted: %v", err)
	}
	// The default production listener also accepts native IPv6 UDP/QUIC.
	config, err := newProbeRouteRelayTLSConfig(routeID, "::1", tls.VersionTLS13, []string{http3.NextProtoH3})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started := time.Now()
	q, endpoint, err := dialProbeRouteRelayQUIC(ctx, []string{"127.0.0.1:1", net.JoinHostPort("::1", strconv.Itoa(port))}, config)
	if err != nil {
		t.Fatal(err)
	}
	q.CloseWithError(0, "test finished")
	if !strings.HasPrefix(endpoint, "[::1]:") || time.Since(started) > time.Second {
		t.Fatalf("QUIC fallback endpoint=%s elapsed=%s", endpoint, time.Since(started))
	}
	// Failed HMAC is counted on this source; a good handshake must still work.
	conn, err := openProbeRouteRelayHTTP3WebSocketNetConn(routeID, secret, "::1", port, probeRouteBridgeRoleToNext, probeRouteRelayModeBridge, "vrouter-carrier-slot-4", "::1", "::1", 2*time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !probeVirtualRouterConnSupportsMultiCarrier(conn) {
		t.Fatal("IPv6 H3 lost slot negotiation")
	}
}

func TestProbeTransportResolvedTLSIPv6AndIPv4Fallback(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	server.Listener.Close()
	server.Listener = listener
	server.StartTLS()
	defer server.Close()
	tlsConfig := server.Client().Transport.(*http.Transport).TLSClientConfig
	tlsConfig = tlsConfig.Clone()
	tlsConfig.ServerName = "example.com"
	target := probeResolvedURLDialTarget{OriginalEndpoint: "transport.example:443", DialEndpoints: []string{"[::1]:1", listener.Addr().String()}}
	conn, err := newProbeResolvedConnectionDialContext(target, 2*time.Second, tlsConfig)(t.Context(), "tcp", target.OriginalEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(conn.RemoteAddr().String(), "[::1]:") {
		t.Fatal(conn.RemoteAddr())
	}
	conn.Close()
	v4 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer v4.Close()
	target.DialEndpoints = []string{"[::1]:1", v4.Listener.Addr().String()}
	tlsConfig = v4.Client().Transport.(*http.Transport).TLSClientConfig
	tlsConfig = tlsConfig.Clone()
	tlsConfig.ServerName = "example.com"
	started := time.Now()
	conn, err = newProbeResolvedConnectionDialContext(target, 2*time.Second, tlsConfig)(t.Context(), "tcp", target.OriginalEndpoint)
	if err != nil || time.Since(started) > time.Second {
		t.Fatalf("IPv4 fallback conn=%v err=%v", conn, err)
	}
	conn.Close()
}
