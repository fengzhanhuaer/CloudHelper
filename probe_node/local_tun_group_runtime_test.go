package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestProbeLocalTUNGroupRuntimeOpenStreamUsesBridgeSessionForWebSocket(t *testing.T) {
	testProbeLocalTUNGroupRuntimeOpenStreamUsesBridgeSession(t, "websocket")
}

func TestProbeLocalTUNGroupRuntimeOpenStreamUsesBridgeSessionForWebSocketH3(t *testing.T) {
	testProbeLocalTUNGroupRuntimeOpenStreamUsesBridgeSession(t, "websocket-h3")
}

func TestProbeLocalTUNGroupRuntimeOpenStreamUsesBridgeSessionForDefaultLayer(t *testing.T) {
	testProbeLocalTUNGroupRuntimeOpenStreamUsesBridgeSession(t, "")
}

func testProbeLocalTUNGroupRuntimeOpenStreamUsesBridgeSession(t *testing.T, linkLayer string) {
	t.Helper()
	originalOpenDataStream := probeLocalTUNOpenChainRelayDataStreamNetConn
	defer func() {
		probeLocalTUNOpenChainRelayDataStreamNetConn = originalOpenDataStream
	}()

	var dataStreamCalls int32
	probeLocalTUNOpenChainRelayDataStreamNetConn = func(chainID string, secret string, relayHost string, relayPort int, layer string) (net.Conn, error) {
		atomic.AddInt32(&dataStreamCalls, 1)
		return nil, errors.New("unexpected data stream dial")
	}

	clientConn, serverConn := net.Pipe()
	serverReady := make(chan *yamux.Session, 1)
	serverErr := make(chan error, 1)
	go func() {
		session, err := yamux.Server(serverConn, newProbeChainYamuxConfig())
		if err != nil {
			serverErr <- err
			return
		}
		serverReady <- session
		stream, err := session.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer stream.Close()

		var req probeChainTunnelOpenRequest
		if err := json.NewDecoder(stream).Decode(&req); err != nil {
			serverErr <- err
			return
		}
		if req.Type != "open" || req.Network != "tcp" || req.Address != "example.com:443" || req.FlowID != "flow-a" {
			serverErr <- errors.New("unexpected open request")
			return
		}
		if err := json.NewEncoder(stream).Encode(probeChainTunnelOpenResponse{OK: true}); err != nil {
			serverErr <- err
			return
		}
		_, _ = io.Copy(io.Discard, stream)
		serverErr <- nil
	}()

	clientSession, err := yamux.Client(clientConn, newProbeChainYamuxConfig())
	if err != nil {
		t.Fatalf("yamux client failed: %v", err)
	}
	serverSession := <-serverReady
	defer serverSession.Close()
	defer clientSession.Close()

	rt := &probeLocalTUNGroupRuntime{
		Group:           "test",
		SelectedChainID: "chain-a",
		RuntimeStatus:   "connected",
		Endpoint: probeLocalTUNChainEndpoint{
			ChainID:     "chain-a",
			EntryHost:   "relay.example.com",
			EntryPort:   16030,
			LinkLayer:   linkLayer,
			ChainSecret: "secret-a",
		},
		session: clientSession,
	}

	stream, flowID, err := rt.openStream("tcp", "example.com:443", nil, "flow-a")
	if err != nil {
		t.Fatalf("openStream returned error: %v", err)
	}
	if flowID != "flow-a" {
		t.Fatalf("flowID=%q want flow-a", flowID)
	}
	_ = stream.Close()

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server side failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server side")
	}
	if got := atomic.LoadInt32(&dataStreamCalls); got != 0 {
		t.Fatalf("data stream dial calls=%d want 0", got)
	}
}

func TestProbeLocalTUNGroupRuntimeOpenStreamDoesNotFallbackWithoutBridgeSession(t *testing.T) {
	for _, linkLayer := range []string{"", "websocket", "websocket-h3"} {
		t.Run(firstNonEmpty(linkLayer, "default"), func(t *testing.T) {
			originalOpenDataStream := probeLocalTUNOpenChainRelayDataStreamNetConn
			defer func() {
				probeLocalTUNOpenChainRelayDataStreamNetConn = originalOpenDataStream
			}()

			var dataStreamCalls int32
			probeLocalTUNOpenChainRelayDataStreamNetConn = func(chainID string, secret string, relayHost string, relayPort int, layer string) (net.Conn, error) {
				atomic.AddInt32(&dataStreamCalls, 1)
				return nil, errors.New("unexpected data stream dial")
			}

			rt := &probeLocalTUNGroupRuntime{
				Group:           "test",
				SelectedChainID: "chain-a",
				RuntimeStatus:   "connected",
				Endpoint: probeLocalTUNChainEndpoint{
					ChainID:     "chain-a",
					EntryHost:   "relay.example.com",
					EntryPort:   16030,
					LinkLayer:   linkLayer,
					ChainSecret: "secret-a",
				},
			}

			stream, _, err := rt.openStream("tcp", "example.com:443", nil, "flow-a")
			if err == nil {
				_ = stream.Close()
				t.Fatal("expected bridge session error")
			}
			if got := atomic.LoadInt32(&dataStreamCalls); got != 0 {
				t.Fatalf("data stream dial calls=%d want 0", got)
			}
		})
	}
}

func TestProbeLocalTUNGroupRuntimeOpenStreamReconnectsAfterBridgeResponseFailure(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalTUNGroupRuntimeRegistryForTest()
	t.Cleanup(resetProbeLocalTUNGroupRuntimeRegistryForTest)

	if err := persistProbeProxyChainCache([]probeLinkChainServerItem{{
		ChainID:     "chain-retry",
		ChainType:   "proxy_chain",
		Name:        "retry",
		Secret:      "secret",
		EntryNodeID: "12",
		ExitNodeID:  "12",
		LinkLayer:   "websocket",
		HopConfigs: []probeLinkChainHopServerItem{{
			NodeNo:       12,
			ListenHost:   "0.0.0.0",
			ListenPort:   16030,
			ExternalPort: 16030,
			LinkLayer:    "websocket",
			RelayHost:    "127.0.0.1",
		}},
	}}); err != nil {
		t.Fatalf("persist proxy chain cache failed: %v", err)
	}

	staleClientConn, staleServerConn := net.Pipe()
	staleServerSession, err := yamux.Server(staleServerConn, newProbeChainYamuxConfig())
	if err != nil {
		t.Fatalf("create stale yamux server failed: %v", err)
	}
	staleClientSession, err := yamux.Client(staleClientConn, newProbeChainYamuxConfig())
	if err != nil {
		t.Fatalf("create stale yamux client failed: %v", err)
	}
	staleDone := make(chan struct{})
	go serveProbeLocalTUNGroupRuntimeOpenCloseBeforeResponse(staleServerSession, staleDone)

	newDone := make(chan struct{})
	var relayOpenCalls int32
	probeLocalTUNOpenChainRelayNetConn = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
		atomic.AddInt32(&relayOpenCalls, 1)
		if chainID != "chain-retry" {
			t.Fatalf("chainID=%q", chainID)
		}
		client, server := net.Pipe()
		go serveProbeLocalTUNGroupRuntimeRetryOpenOK(server, newDone)
		return client, nil
	}
	t.Cleanup(func() {
		close(newDone)
		probeLocalTUNOpenChainRelayNetConn = openProbeLocalTUNChainRelayNetConn
		_ = staleClientSession.Close()
		_ = staleServerSession.Close()
		_ = staleClientConn.Close()
	})

	rt := &probeLocalTUNGroupRuntime{
		Group:           "test",
		SelectedChainID: "chain-retry",
		RuntimeStatus:   "connected",
		Endpoint: probeLocalTUNChainEndpoint{
			ChainID:     "chain-retry",
			EntryHost:   "127.0.0.1",
			EntryPort:   16030,
			LinkLayer:   "websocket",
			ChainSecret: "secret",
		},
		relayConn: staleClientConn,
		session:   staleClientSession,
	}

	stream, flowID, err := rt.openStream("tcp", "example.com:443", nil, "flow-retry")
	if err != nil {
		t.Fatalf("openStream returned error: %v", err)
	}
	if flowID != "flow-retry" {
		t.Fatalf("flowID=%q want flow-retry", flowID)
	}
	_ = stream.Close()
	<-staleDone
	if got := atomic.LoadInt32(&relayOpenCalls); got != 1 {
		t.Fatalf("relay open calls=%d want 1", got)
	}
	snapshot := rt.snapshot()
	if !snapshot.Connected || snapshot.RuntimeStatus != "connected" {
		t.Fatalf("runtime snapshot=%+v", snapshot)
	}
}

func TestShouldUseProbeLocalTUNGroupRuntimeBridgeStreamForCachedWebSocketH3(t *testing.T) {
	resetProbeChainRelayProtocolStateForTest()
	defer resetProbeChainRelayProtocolStateForTest()

	endpoint := probeLocalTUNChainEndpoint{
		ChainID:     "chain-a",
		EntryHost:   "relay.example.com",
		EntryPort:   16030,
		LinkLayer:   "",
		ChainSecret: "secret-a",
	}
	recordProbeChainRelayProtocolSuccess(
		probeChainRelayProtocolEndpointKey(endpoint.EntryHost, endpoint.EntryPort),
		probeChainRelayProtocolDialResult{
			Protocol: "websocket-h3",
			Latency:  2 * time.Millisecond,
		},
		"test",
	)

	if !shouldUseProbeLocalTUNGroupRuntimeBridgeStream(endpoint) {
		t.Fatal("expected cached websocket-h3 to use bridge yamux stream")
	}
}

func serveProbeLocalTUNGroupRuntimeRetryOpenOK(conn net.Conn, done <-chan struct{}) {
	defer conn.Close()
	session, err := yamux.Server(conn, newProbeChainYamuxConfig())
	if err != nil {
		return
	}
	defer session.Close()
	stream, err := session.Accept()
	if err != nil {
		return
	}
	defer stream.Close()
	var req probeChainTunnelOpenRequest
	if err := json.NewDecoder(stream).Decode(&req); err != nil {
		return
	}
	_ = json.NewEncoder(stream).Encode(probeChainTunnelOpenResponse{OK: true})
	<-done
}

func serveProbeLocalTUNGroupRuntimeOpenCloseBeforeResponse(session *yamux.Session, done chan<- struct{}) {
	defer close(done)
	if session == nil {
		return
	}
	defer session.Close()
	stream, err := session.Accept()
	if err != nil {
		return
	}
	defer stream.Close()
	var req probeChainTunnelOpenRequest
	_ = json.NewDecoder(stream).Decode(&req)
}
