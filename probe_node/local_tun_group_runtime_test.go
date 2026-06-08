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
