package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/txthinking/socks5"
)

func TestProbeVRouteProxyFrameCodecsAndDispatchHash(t *testing.T) {
	sessionID := "00112233445566778899aabbccddeeff"
	tcpPayload, err := marshalProbeVRouteProxyTCPData(sessionID, []byte("hello"))
	if err != nil {
		t.Fatalf("marshal tcp data failed: %v", err)
	}
	gotID, gotData, err := unmarshalProbeVRouteProxyTCPData(tcpPayload)
	if err != nil || gotID != sessionID || string(gotData) != "hello" {
		t.Fatalf("tcp decode id=%q data=%q err=%v", gotID, gotData, err)
	}
	resultPayload, _ := marshalProbeVRouteProxyJSON(probeVRouteProxyTCPOpenResultPayload{SessionID: sessionID, OK: true})
	tcpHash := probeVRouteProxyFrameDispatchHash(probeVirtualRouterProxySubTypeTCPData, tcpPayload, 2166136261)
	resultHash := probeVRouteProxyFrameDispatchHash(probeVirtualRouterProxySubTypeTCPOpenResult, resultPayload, 2166136261)
	if tcpHash != resultHash {
		t.Fatalf("same stream hashes differ: data=%d result=%d", tcpHash, resultHash)
	}
	udpPayload, err := marshalProbeVRouteProxyUDPDatagram(sessionID, "dns.example:53", []byte{1, 2, 3})
	if err != nil {
		t.Fatalf("marshal udp failed: %v", err)
	}
	udpID, target, udpData, err := unmarshalProbeVRouteProxyUDPDatagram(udpPayload)
	if err != nil || udpID != sessionID || target != "dns.example:53" || !bytes.Equal(udpData, []byte{1, 2, 3}) {
		t.Fatalf("udp decode id=%q target=%q data=%v err=%v", udpID, target, udpData, err)
	}
}

func TestDecideProbeVRouteProxyTargetReusesFakeIPRoute(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		RouteRules: []probeVirtualRouterRouteRule{{
			ID: "docker-exit", Name: "Docker exit", Action: "probe_exit", ExitNodeID: "18", Entries: []string{"domain_suffix:docker.io"},
		}},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{Items: []probeVirtualRouterFakeIPEntry{{
			Domain: "registry-1.docker.io", FakeIP: "198.18.7.0", RuleID: "docker-exit", Action: "probe_exit", ExitNodeID: "18",
		}}},
	}
	probeVirtualRouterState.localNodeID = "9"
	probeVirtualRouterState.neighbors = map[string]map[string]struct{}{
		"9":  {"18": {}},
		"18": {"9": {}},
	}
	probeVirtualRouterState.mu.Unlock()
	decision, err := decideProbeVRouteProxyTarget("198.18.7.0:443")
	if err != nil {
		t.Fatalf("fake ip decision failed: %v", err)
	}
	if decision.TargetAddr != "registry-1.docker.io:443" || decision.ExitNodeID != "18" || strings.Join(decision.Path, ">") != "9>18" {
		t.Fatalf("unexpected fake ip decision: %+v", decision)
	}
	domainDecision, err := decideProbeVRouteProxyTarget("registry-1.docker.io:443")
	if err != nil {
		t.Fatalf("domain decision failed: %v", err)
	}
	if domainDecision.ExitNodeID != decision.ExitNodeID || strings.Join(domainDecision.Path, ">") != strings.Join(decision.Path, ">") {
		t.Fatalf("fake/domain decisions differ: fake=%+v domain=%+v", decision, domainDecision)
	}
}

func TestResolveProbeVRouteProxyFakeIPRefreshesMissingItem(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{Enabled: true, FakeIPCIDR: "198.18.0.0/15"}
	probeVirtualRouterState.localNodeID = "9"
	probeVirtualRouterState.mu.Unlock()
	rememberProbeVirtualRouterController(nodeIdentity{NodeID: "9", Secret: "secret"}, "https://controller.example")
	t.Cleanup(func() { rememberProbeVirtualRouterController(nodeIdentity{}, "") })
	oldRequest := probeRequestRouteFakeIPByIP
	probeRequestRouteFakeIPByIP = func(_ context.Context, baseURL string, identity nodeIdentity, fakeIP string) (probeVirtualRouterFakeIPEntry, error) {
		if baseURL != "https://controller.example" || identity.NodeID != "9" || fakeIP != "198.18.4.9" {
			t.Fatalf("unexpected refresh request: base=%q identity=%+v fake=%q", baseURL, identity, fakeIP)
		}
		return probeVirtualRouterFakeIPEntry{Domain: "api.example.com", FakeIP: fakeIP, Action: "direct", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, nil
	}
	t.Cleanup(func() { probeRequestRouteFakeIPByIP = oldRequest })
	item, err := resolveProbeVRouteProxyFakeIPEntry("198.18.4.9")
	if err != nil || item.Domain != "api.example.com" {
		t.Fatalf("refreshed item=%+v err=%v", item, err)
	}
}

func TestProbeVRouteProxyRemoteTCPSourceFlow(t *testing.T) {
	resetProbeVRouteProxyTCPStateForTest()
	oldSender := probeVRouteProxyFrameSender
	t.Cleanup(func() { probeVRouteProxyFrameSender = oldSender })
	t.Cleanup(resetProbeVRouteProxyTCPStateForTest)
	dataFrame := make(chan []byte, 1)
	closeFrame := make(chan struct{}, 1)
	probeVRouteProxyFrameSender = func(subType uint16, payload []byte, path []string) error {
		switch subType {
		case probeVirtualRouterProxySubTypeTCPOpen:
			var open probeVRouteProxyTCPOpenPayload
			if err := jsonUnmarshalForProxyTest(payload, &open); err != nil {
				return err
			}
			result, _ := marshalProbeVRouteProxyJSON(probeVRouteProxyTCPOpenResultPayload{SessionID: open.SessionID, OK: true})
			return handleProbeVRouteProxyTCPOpenResult(result)
		case probeVirtualRouterProxySubTypeTCPData:
			dataFrame <- append([]byte(nil), payload...)
		case probeVirtualRouterProxySubTypeTCPClose:
			closeFrame <- struct{}{}
		}
		return nil
	}
	conn, err := openProbeVRouteProxyRemoteTCP(probeVRouteProxyTargetDecision{Action: "probe_exit", TargetAddr: "example.com:443", Path: []string{"9", "18"}})
	if err != nil {
		t.Fatalf("open remote tcp failed: %v", err)
	}
	if _, err := conn.Write([]byte("request")); err != nil {
		t.Fatalf("write remote tcp failed: %v", err)
	}
	select {
	case payload := <-dataFrame:
		_, data, err := unmarshalProbeVRouteProxyTCPData(payload)
		if err != nil || string(data) != "request" {
			t.Fatalf("remote data=%q err=%v", data, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for remote tcp data frame")
	}
	_ = conn.Close()
	select {
	case <-closeFrame:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for remote tcp close frame")
	}
}

func TestProbeVRouteProxyListenersWorkWithoutTUN(t *testing.T) {
	resetProbeVRouteProxyRuntimeForTest()
	t.Cleanup(resetProbeVRouteProxyRuntimeForTest)
	tcpEchoAddr, closeTCPEcho := startProbeVRouteProxyTCPEcho(t)
	defer closeTCPEcho()
	udpEchoAddr, closeUDPEcho := startProbeVRouteProxyUDPEcho(t)
	defer closeUDPEcho()
	httpTarget, closeHTTP := startProbeVRouteProxyHTTPOrigin(t)
	defer closeHTTP()
	httpListen := reserveProbeVRouteProxyTCPAddress(t)
	socksListen := reserveProbeVRouteProxyTCPUDPAddress(t)
	runtime, err := startProbeVRouteProxyListenerRuntime(probeVirtualRouterLocalSettings{
		ProxyEnabled: true, HTTPProxyListen: httpListen, SOCKS5ProxyListen: socksListen,
	})
	if err != nil {
		t.Fatalf("start proxy runtime failed: %v", err)
	}
	defer runtime.close()
	if probeVirtualRouterTUNDataPlaneStatsSnapshot().Running {
		t.Fatal("proxy test unexpectedly depends on a running TUN data plane")
	}

	proxyURL, _ := url.Parse("http://" + runtime.httpListener.Addr().String())
	httpClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
	response, err := httpClient.Get(httpTarget + "/plain")
	if err != nil {
		t.Fatalf("http proxy request failed: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "vroute-http-ok" {
		t.Fatalf("http proxy response status=%d body=%q", response.StatusCode, body)
	}

	connectConn, err := net.DialTimeout("tcp", runtime.httpListener.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatalf("dial http proxy failed: %v", err)
	}
	defer connectConn.Close()
	_, _ = fmt.Fprintf(connectConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", tcpEchoAddr, tcpEchoAddr)
	connectResponse, err := http.ReadResponse(bufio.NewReader(connectConn), &http.Request{Method: http.MethodConnect})
	if err != nil || connectResponse.StatusCode != http.StatusOK {
		t.Fatalf("http connect response=%v err=%v", connectResponse, err)
	}
	assertProbeVRouteProxyEcho(t, connectConn, []byte("http-connect"))

	socksClient, _ := socks5.NewClient(runtime.socksListener.Addr().String(), "", "", 5, 5)
	socksTCP, err := socksClient.Dial("tcp", tcpEchoAddr)
	if err != nil {
		t.Fatalf("socks5 tcp dial failed: %v", err)
	}
	assertProbeVRouteProxyEcho(t, socksTCP, []byte("socks-tcp"))
	_ = socksTCP.Close()

	socksUDPClient, _ := socks5.NewClient(runtime.socksListener.Addr().String(), "", "", 5, 5)
	socksUDPConn, err := socksUDPClient.Dial("udp", udpEchoAddr)
	if err != nil {
		t.Fatalf("socks5 udp dial failed: %v", err)
	}
	assertProbeVRouteProxyEcho(t, socksUDPConn, []byte("socks-udp"))
	_ = socksUDPConn.Close()
}

func TestReconcileProbeVRouteProxyRuntimeControlsSystemProxy(t *testing.T) {
	resetProbeVRouteProxyRuntimeForTest()
	oldSet := probeVRouteSystemProxySet
	oldRestore := probeVRouteSystemProxyRestore
	t.Cleanup(func() {
		probeVRouteSystemProxySet = oldSet
		probeVRouteSystemProxyRestore = oldRestore
	})
	setCalls := make(chan [2]string, 2)
	restoreCalls := 0
	probeVRouteSystemProxySet = func(httpAddress string, socks5Address string) error {
		setCalls <- [2]string{httpAddress, socks5Address}
		return nil
	}
	probeVRouteSystemProxyRestore = func() error {
		restoreCalls++
		return nil
	}
	t.Cleanup(resetProbeVRouteProxyRuntimeForTest)

	httpListen := reserveProbeVRouteProxyTCPAddress(t)
	socksListen := reserveProbeVRouteProxyTCPUDPAddress(t)
	settings := probeVirtualRouterLocalSettings{
		ProxyEnabled: true, HTTPProxyListen: httpListen, SOCKS5ProxyListen: socksListen,
	}
	if err := reconcileProbeVRouteProxyRuntime(settings); err != nil {
		t.Fatalf("enable proxy runtime failed: %v", err)
	}
	select {
	case addresses := <-setCalls:
		if addresses[0] != httpListen || addresses[1] != socksListen {
			t.Fatalf("system proxy addresses=%v want=[%s %s]", addresses, httpListen, socksListen)
		}
	default:
		t.Fatal("system proxy was not applied")
	}
	settings.ProxyEnabled = false
	if err := reconcileProbeVRouteProxyRuntime(settings); err != nil {
		t.Fatalf("disable proxy runtime failed: %v", err)
	}
	if restoreCalls != 1 {
		t.Fatalf("system proxy restore calls=%d want=1", restoreCalls)
	}
	probeVRouteProxyRuntimeState.mu.RLock()
	runtime := probeVRouteProxyRuntimeState.runtime
	probeVRouteProxyRuntimeState.mu.RUnlock()
	if runtime != nil {
		t.Fatal("proxy runtime remained active after disable")
	}
}

func TestReconcileProbeVRouteProxyRuntimeRollsBackSystemProxyFailure(t *testing.T) {
	resetProbeVRouteProxyRuntimeForTest()
	oldSet := probeVRouteSystemProxySet
	oldRestore := probeVRouteSystemProxyRestore
	t.Cleanup(func() {
		probeVRouteSystemProxySet = oldSet
		probeVRouteSystemProxyRestore = oldRestore
	})
	restoreCalls := 0
	probeVRouteSystemProxySet = func(string, string) error { return errors.New("system proxy denied") }
	probeVRouteSystemProxyRestore = func() error {
		restoreCalls++
		return nil
	}
	t.Cleanup(resetProbeVRouteProxyRuntimeForTest)

	err := reconcileProbeVRouteProxyRuntime(probeVirtualRouterLocalSettings{
		ProxyEnabled: true, HTTPProxyListen: reserveProbeVRouteProxyTCPAddress(t), SOCKS5ProxyListen: reserveProbeVRouteProxyTCPUDPAddress(t),
	})
	if err == nil || !strings.Contains(err.Error(), "system proxy denied") {
		t.Fatalf("enable error=%v", err)
	}
	if restoreCalls != 1 {
		t.Fatalf("system proxy restore calls=%d want=1", restoreCalls)
	}
	probeVRouteProxyRuntimeState.mu.RLock()
	runtime := probeVRouteProxyRuntimeState.runtime
	probeVRouteProxyRuntimeState.mu.RUnlock()
	if runtime != nil {
		t.Fatal("failed startup left proxy runtime active")
	}
}

func jsonUnmarshalForProxyTest(payload []byte, value any) error {
	return json.Unmarshal(payload, value)
}

func startProbeVRouteProxyTCPEcho(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String(), func() {
		close(done)
		_ = listener.Close()
		wg.Wait()
	}
}

func startProbeVRouteProxyUDPEcho(t *testing.T) (string, func()) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buffer := make([]byte, 65507)
		for {
			n, addr, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(buffer[:n], addr)
		}
	}()
	return conn.LocalAddr().String(), func() { _ = conn.Close() }
}

func startProbeVRouteProxyHTTPOrigin(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "vroute-http-ok")
	})}
	go server.Serve(listener)
	return "http://" + listener.Addr().String(), func() { _ = server.Close() }
}

func reserveProbeVRouteProxyTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func reserveProbeVRouteProxyTCPUDPAddress(t *testing.T) string {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			continue
		}
		address := udpConn.LocalAddr().String()
		tcpAddr, _ := net.ResolveTCPAddr("tcp", address)
		tcpListener, err := net.ListenTCP("tcp", tcpAddr)
		if err != nil {
			_ = udpConn.Close()
			continue
		}
		_ = tcpListener.Close()
		_ = udpConn.Close()
		return address
	}
	t.Fatal("failed to reserve tcp/udp test address")
	return ""
}

func assertProbeVRouteProxyEcho(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("echo write failed: %v", err)
	}
	got := make([]byte, len(payload))
	if conn.LocalAddr() != nil && strings.HasPrefix(conn.LocalAddr().Network(), "udp") {
		buffer := make([]byte, 65535)
		n, err := conn.Read(buffer)
		if err != nil {
			t.Fatalf("echo read failed: %v", err)
		}
		got = buffer[:n]
	} else if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("echo read failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo=%q want=%q", got, payload)
	}
}
