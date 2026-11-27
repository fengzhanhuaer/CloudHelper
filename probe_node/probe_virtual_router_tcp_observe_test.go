package main

import (
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
	"net/http"
	"testing"
	"time"
)

type observedTestEndpoint struct {
	tcpip.Endpoint
	reads int
}

func (e *observedTestEndpoint) GetSockOpt(option tcpip.GettableSocketOption) tcpip.Error {
	e.reads++
	if info, ok := option.(*tcpip.TCPInfoOption); ok {
		*info = tcpip.TCPInfoOption{RTT: 80 * time.Millisecond, RTO: 250 * time.Millisecond, SndCwnd: 72, SndSsthresh: 128}
		return nil
	}
	return &tcpip.ErrUnknownProtocolOption{}
}
func (e *observedTestEndpoint) GetSockOptInt(option tcpip.SockOptInt) (int, tcpip.Error) {
	return 4096, nil
}
func (e *observedTestEndpoint) SocketOptions() *tcpip.SocketOptions { return nil }
func (e *observedTestEndpoint) Stats() tcpip.EndpointStats          { return nil }

func TestExitTCPObservationsBoundedLazyAndReleased(t *testing.T) {
	var registry probeExitTCPObserver
	ep := &observedTestEndpoint{}
	release := registry.register(ep, "198.18.0.15:20000", "172.66.0.218:443")
	if ep.reads != 0 {
		t.Fatal("TCP_INFO read on data path")
	}
	snapshot := registry.snapshot()
	if len(snapshot.Items) != 1 || snapshot.Items[0].CwndPackets != 72 || snapshot.Items[0].RTTMS != 80 || snapshot.Items[0].SendQueueBytes == nil || *snapshot.Items[0].SendQueueBytes != 4096 {
		t.Fatalf("snapshot %+v", snapshot)
	}
	var latest func()
	for i := 0; i < 100; i++ {
		latest = registry.register(ep, "source", "target")
	}

	if got := len(registry.snapshot().Items); got != probeExitTCPObserveLimit {
		t.Fatalf("unbounded registry %d", got)
	}
	release()
	release()
	if got := len(registry.snapshot().Items); got != probeExitTCPObserveLimit {
		t.Fatal("evicted lease removed a current connection")
	}
	latest()
	latest()
	if got := len(registry.snapshot().Items); got != probeExitTCPObserveLimit-1 {
		t.Fatalf("cleanup %d", got)
	}
}

func TestExitTCPObservationReadsRealEndpointOptions(t *testing.T) {
	g := stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{ipv4.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol}})
	defer g.Destroy()
	if err := configureProbeVirtualRouterExitNetstackTCP(g); err != nil {
		t.Fatal(err)
	}
	var queue waiter.Queue
	ep, err := g.NewEndpoint(tcp.ProtocolNumber, ipv4.ProtocolNumber, &queue)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	var observer probeExitTCPObserver
	release := observer.register(ep, "origin", "target")
	defer release()
	result := observer.snapshot()
	if len(result.Items) != 1 {
		t.Fatal(result)
	}
	item := result.Items[0]
	if item.SendBufferBytes == nil || *item.SendBufferBytes != probeVirtualRouterExitNetstackTCPBufferDefault || item.ReceiveBufferBytes == nil || item.Retransmits == nil {
		t.Fatalf("missing real TCP endpoint options: %+v", item)
	}
}

func TestExitOverlayTCPUsesReno(t *testing.T) {
	runner, err := newProbeVirtualRouterExitNetstack()
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	var algorithm tcpip.CongestionControlOption
	if err := runner.stack.TransportProtocolOption(tcp.ProtocolNumber, &algorithm); err != nil {
		t.Fatal(err)
	}
	if algorithm != "reno" {
		t.Fatalf("packet exit TCP algorithm = %q, want Reno", algorithm)
	}
}
func TestProbeLocalTCPObservationIsAuthenticatedAndOptIn(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	response := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/virtual_router/debug?tcp=1", nil)
	if response.Code != 401 {
		t.Fatal(response.Code)
	}
	cookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	response = doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/virtual_router/debug", nil, cookie)
	payload := decodeProbeLocalJSON(t, response)
	if _, ok := payload["tcp_connections"]; ok {
		t.Fatal("ordinary status samples TCP state")
	}
	response = doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/virtual_router/debug?tcp=1", nil, cookie)
	payload = decodeProbeLocalJSON(t, response)
	snapshot, ok := payload["tcp_connections"].(map[string]any)
	if response.Code != 200 || !ok || snapshot["limit"] != float64(probeExitTCPObserveLimit) {
		t.Fatal(response.Code, payload)
	}
}
