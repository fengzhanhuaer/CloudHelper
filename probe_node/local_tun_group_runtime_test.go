package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	clientConn, serverConn := net.Pipe()
	serverReady := make(chan *probeChainFrameSession, 1)
	serverErr := make(chan error, 1)
	go func() {
		session, err := newProbeChainFrameServer(serverConn)
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

		frameStream, ok := stream.(*probeChainFrameStream)
		if !ok {
			serverErr <- errors.New("expected frame stream")
			return
		}
		req, found := frameStream.OpenRequest()
		if !found {
			serverErr <- errors.New("missing frame open request")
			return
		}
		if req.Type != "open" || req.Network != "tcp" || req.Address != "example.com:443" || req.FlowID != "flow-a" {
			serverErr <- errors.New("unexpected open request")
			return
		}
		if err := frameStream.RespondOpen(probeChainTunnelOpenResponse{OK: true}); err != nil {
			serverErr <- err
			return
		}
		_, _ = io.Copy(io.Discard, stream)
		serverErr <- nil
	}()

	clientSession, err := newProbeChainFrameClient(clientConn)
	if err != nil {
		t.Fatalf("frame client failed: %v", err)
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
}

func TestProbeLocalTUNGroupRuntimeOpenStreamDoesNotFallbackWithoutBridgeSession(t *testing.T) {
	for _, linkLayer := range []string{"", "websocket", "websocket-h3"} {
		t.Run(firstNonEmpty(linkLayer, "default"), func(t *testing.T) {
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
		})
	}
}

func TestProbeLocalTUNGroupRuntimeFetchRemotePeerStatusRejectsOpenResponse(t *testing.T) {
	setProbeLocalProxyViewChains(nil)
	probeChainRuntimeState.mu.Lock()
	oldRuntimes := probeChainRuntimeState.runtimes
	probeChainRuntimeState.runtimes = map[string]*probeChainRuntime{}
	probeChainRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		setProbeLocalProxyViewChains(nil)
		probeChainRuntimeState.mu.Lock()
		probeChainRuntimeState.runtimes = oldRuntimes
		probeChainRuntimeState.mu.Unlock()
	})

	clientConn, serverConn := net.Pipe()
	serverReady := make(chan *probeChainFrameSession, 1)
	serverErr := make(chan error, 1)
	go func() {
		session, err := newProbeChainFrameServer(serverConn)
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
		frameStream, ok := stream.(*probeChainFrameStream)
		if !ok {
			serverErr <- errors.New("expected frame stream")
			return
		}
		req, found := frameStream.OpenRequest()
		if !found {
			serverErr <- errors.New("missing frame open request")
			return
		}
		if req.Type != "peer_status_get" {
			serverErr <- errors.New("unexpected peer status request")
			return
		}
		if err := frameStream.RespondOpen(probeChainTunnelOpenResponse{OK: false, Error: "missing address"}); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	clientSession, err := newProbeChainFrameClient(clientConn)
	if err != nil {
		t.Fatalf("frame client failed: %v", err)
	}
	serverSession := <-serverReady
	defer serverSession.Close()
	defer clientSession.Close()
	defer clientConn.Close()

	rt := &probeLocalTUNGroupRuntime{
		Group:           "test",
		SelectedChainID: "chain-a",
		RuntimeStatus:   "connected",
		Endpoint: probeLocalTUNChainEndpoint{
			ChainID:   "chain-a",
			EntryHost: "relay.example.com",
			EntryPort: 16030,
		},
		session: clientSession,
	}

	_, err = rt.fetchRemotePeerStatus("peer_status_get", "chain_exit")
	if err == nil || !strings.Contains(err.Error(), "missing address") {
		t.Fatalf("fetchRemotePeerStatus err=%v, want missing address", err)
	}
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server side failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server side")
	}
}

func TestProbeLocalTUNGroupRuntimeFetchRemoteSpeedDebugDecodesDirectPayload(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	serverReady := make(chan *probeChainFrameSession, 1)
	serverErr := make(chan error, 1)
	go func() {
		session, err := newProbeChainFrameServer(serverConn)
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
		frameStream, ok := stream.(*probeChainFrameStream)
		if !ok {
			serverErr <- errors.New("expected frame stream")
			return
		}
		req, found := frameStream.OpenRequest()
		if !found {
			serverErr <- errors.New("missing frame open request")
			return
		}
		if req.Type != "speed_debug_get" {
			serverErr <- errors.New("unexpected speed debug request")
			return
		}
		if err := frameStream.RespondOpen(probeChainTunnelOpenResponse{OK: true}); err != nil {
			serverErr <- err
			return
		}
		payload := probeSpeedDebugResultPayload{
			Type:      "speed_debug_result",
			OK:        true,
			NodeID:    "remote-node",
			RequestID: req.RequestID,
			Scope:     "chain_exit",
			Recent: []probeSpeedDebugItemPayload{{
				ChainID: "chain-a",
				Status:  "completed",
				Bytes:   1024,
			}},
		}
		payload.RecentCount = len(payload.Recent)
		if err := json.NewEncoder(stream).Encode(payload); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	clientSession, err := newProbeChainFrameClient(clientConn)
	if err != nil {
		t.Fatalf("frame client failed: %v", err)
	}
	serverSession := <-serverReady
	defer serverSession.Close()
	defer clientSession.Close()
	defer clientConn.Close()

	rt := &probeLocalTUNGroupRuntime{
		Group:           "test",
		SelectedChainID: "chain-a",
		RuntimeStatus:   "connected",
		Endpoint: probeLocalTUNChainEndpoint{
			ChainID:   "chain-a",
			EntryHost: "relay.example.com",
			EntryPort: 16030,
		},
		session: clientSession,
	}

	payload, err := rt.fetchRemoteSpeedDebug()
	if err != nil {
		t.Fatalf("fetchRemoteSpeedDebug failed: %v", err)
	}
	if payload.Type != "speed_debug_result" || !payload.OK || payload.NodeID != "remote-node" || len(payload.Recent) != 1 {
		t.Fatalf("unexpected speed debug payload: %+v", payload)
	}
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server side failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server side")
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
	staleServerSession, err := newProbeChainFrameServer(staleServerConn)
	if err != nil {
		t.Fatalf("create stale frame server failed: %v", err)
	}
	staleClientSession, err := newProbeChainFrameClient(staleClientConn)
	if err != nil {
		t.Fatalf("create stale frame client failed: %v", err)
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

func serveProbeLocalTUNGroupRuntimeRetryOpenOK(conn net.Conn, done <-chan struct{}) {
	defer conn.Close()
	session, err := newProbeChainFrameServer(conn)
	if err != nil {
		return
	}
	defer session.Close()
	stream, err := session.Accept()
	if err != nil {
		return
	}
	defer stream.Close()
	frameStream, ok := stream.(*probeChainFrameStream)
	if !ok {
		return
	}
	if _, found := frameStream.OpenRequest(); !found {
		return
	}
	_ = frameStream.RespondOpen(probeChainTunnelOpenResponse{OK: true})
	<-done
}

func serveProbeLocalTUNGroupRuntimeOpenCloseBeforeResponse(session *probeChainFrameSession, done chan<- struct{}) {
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
	frameStream, ok := stream.(*probeChainFrameStream)
	if !ok {
		return
	}
	_, _ = frameStream.OpenRequest()
}
