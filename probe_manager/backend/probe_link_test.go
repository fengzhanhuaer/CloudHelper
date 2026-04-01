package backend

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildProbeLinkURL(t *testing.T) {
	got, err := buildProbeLinkURL("http", "127.0.0.1", 16030, probeLinkInfoPath)
	if err != nil {
		t.Fatalf("buildProbeLinkURL returned error: %v", err)
	}
	if got != "http://127.0.0.1:16030/api/node/info" {
		t.Fatalf("unexpected probe link url: %s", got)
	}
}

func TestBuildProbeChainPingCandidateChainIDs(t *testing.T) {
	ids, explicit := buildProbeChainPingCandidateChainIDs("chain:1")
	if !explicit {
		t.Fatalf("expected explicit chain target")
	}
	if !containsNodeID(ids, "1") || !containsNodeID(ids, "chain:1") {
		t.Fatalf("unexpected candidate ids: %#v", ids)
	}
}

func TestBuildProbeChainPingCandidateChainIDsWithQuotedInput(t *testing.T) {
	ids, explicit := buildProbeChainPingCandidateChainIDs("\"\ufeffchain\uff1a1\"")
	if !explicit {
		t.Fatalf("expected explicit chain target")
	}
	if !containsNodeID(ids, "1") || !containsNodeID(ids, "chain:1") {
		t.Fatalf("unexpected candidate ids: %#v", ids)
	}
}

func TestBuildProbeChainPingCandidateChainIDsForNode(t *testing.T) {
	ids, explicit := buildProbeChainPingCandidateChainIDs("cloudserver")
	if explicit {
		t.Fatalf("expected non-chain target")
	}
	if len(ids) != 1 || ids[0] != "cloudserver" {
		t.Fatalf("unexpected candidate ids: %#v", ids)
	}
}

func TestTestProbeLinkSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != probeLinkInfoPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"probe_node","node_id":"1","version":"v1.2.3"}`))
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("failed to parse test server url: %v", err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("failed to split host port: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("failed to parse port: %v", err)
	}

	result, err := testProbeLink("1", "service", parsed.Scheme, host, port)
	if err != nil {
		t.Fatalf("testProbeLink returned error: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected ok result, got false")
	}
	if result.NodeID != "1" {
		t.Fatalf("expected node_id=1, got %q", result.NodeID)
	}
	if result.Service != "probe_node" {
		t.Fatalf("expected service=probe_node, got %q", result.Service)
	}
	if result.URL == "" {
		t.Fatalf("expected result url to be populated")
	}
}

func TestProbeLinkSessionHTTPReusesSingleConnection(t *testing.T) {
	_, _ = stopProbeLinkSession("test reset")
	defer func() {
		_, _ = stopProbeLinkSession("test cleanup")
	}()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp failed: %v", err)
	}
	defer listener.Close()

	var accepted atomic.Int32
	countingListener := &countingAcceptListener{
		Listener: listener,
		accepted: &accepted,
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != probeLinkTestPingPath {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"node_id":"1","protocol":"http","message":"pong"}`))
		}),
		ReadHeaderTimeout: 3 * time.Second,
	}
	go func() {
		_ = server.Serve(countingListener)
	}()
	defer func() {
		_ = server.Close()
	}()

	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split host port failed: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse port failed: %v", err)
	}

	first, err := startProbeLinkSession("1", "http", host, port)
	if err != nil {
		t.Fatalf("startProbeLinkSession failed: %v", err)
	}
	if !first.OK {
		t.Fatalf("expected first result ok")
	}

	second, err := pingProbeLinkSession()
	if err != nil {
		t.Fatalf("second ping failed: %v", err)
	}
	if !second.OK {
		t.Fatalf("expected second result ok")
	}

	third, err := pingProbeLinkSession()
	if err != nil {
		t.Fatalf("third ping failed: %v", err)
	}
	if !third.OK {
		t.Fatalf("expected third result ok")
	}

	if got := accepted.Load(); got != 1 {
		t.Fatalf("expected http accepted connections=1 (persistent), got %d", got)
	}
}

func TestPingNetworkAssistantTunnelNodeRequiresNodeID(t *testing.T) {
	_, err := pingNetworkAssistantTunnelNode(&networkAssistantService{}, "")
	if err == nil {
		t.Fatalf("expected error when node_id is empty")
	}
}

func TestPingNetworkAssistantTunnelNodeExistingMux(t *testing.T) {
	oldPing := probeLinkTryPingExistingMux
	oldEnsure := probeLinkEnsureMuxForNode
	defer func() {
		probeLinkTryPingExistingMux = oldPing
		probeLinkEnsureMuxForNode = oldEnsure
	}()

	probeLinkTryPingExistingMux = func(service *networkAssistantService, nodeID string) (time.Duration, bool) {
		if service == nil {
			t.Fatalf("service should not be nil")
		}
		if nodeID != "cloudserver" {
			t.Fatalf("unexpected node id: %s", nodeID)
		}
		return 12 * time.Millisecond, true
	}
	probeLinkEnsureMuxForNode = func(service *networkAssistantService, nodeID string) error {
		t.Fatalf("ensure should not be called when mux already exists")
		return nil
	}

	result, err := pingNetworkAssistantTunnelNode(&networkAssistantService{}, "cloudserver")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected ok result")
	}
	if result.DurationMS != 12 {
		t.Fatalf("expected duration 12ms, got %d", result.DurationMS)
	}
	if !strings.Contains(result.Message, "已有连接") {
		t.Fatalf("unexpected message: %s", result.Message)
	}
}

func TestPingNetworkAssistantTunnelNodeEnsureMuxThenSuccess(t *testing.T) {
	oldPing := probeLinkTryPingExistingMux
	oldEnsure := probeLinkEnsureMuxForNode
	defer func() {
		probeLinkTryPingExistingMux = oldPing
		probeLinkEnsureMuxForNode = oldEnsure
	}()

	var pingCount int
	probeLinkTryPingExistingMux = func(service *networkAssistantService, nodeID string) (time.Duration, bool) {
		pingCount++
		if pingCount == 1 {
			return 0, false
		}
		return 23 * time.Millisecond, true
	}
	probeLinkEnsureMuxForNode = func(service *networkAssistantService, nodeID string) error {
		if service == nil {
			t.Fatalf("service should not be nil")
		}
		if nodeID != "cloudserver" {
			t.Fatalf("unexpected node id: %s", nodeID)
		}
		return nil
	}

	result, err := pingNetworkAssistantTunnelNode(&networkAssistantService{}, "cloudserver")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected ok result")
	}
	if result.DurationMS != 23 {
		t.Fatalf("expected duration 23ms, got %d", result.DurationMS)
	}
	if !strings.Contains(result.Message, "按需建链") {
		t.Fatalf("unexpected message: %s", result.Message)
	}
}

func TestPingNetworkAssistantTunnelNodeEnsureMuxFailed(t *testing.T) {
	oldPing := probeLinkTryPingExistingMux
	oldEnsure := probeLinkEnsureMuxForNode
	defer func() {
		probeLinkTryPingExistingMux = oldPing
		probeLinkEnsureMuxForNode = oldEnsure
	}()

	probeLinkTryPingExistingMux = func(service *networkAssistantService, nodeID string) (time.Duration, bool) {
		return 0, false
	}
	probeLinkEnsureMuxForNode = func(service *networkAssistantService, nodeID string) error {
		return fmt.Errorf("dial failed")
	}

	result, err := pingNetworkAssistantTunnelNode(&networkAssistantService{}, "cloudserver")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.OK {
		t.Fatalf("expected failed result")
	}
	if !strings.Contains(result.Message, "按需建链失败") {
		t.Fatalf("unexpected message: %s", result.Message)
	}
}

func TestPingNetworkAssistantTunnelNodeEnsureMuxStillUnavailable(t *testing.T) {
	oldPing := probeLinkTryPingExistingMux
	oldEnsure := probeLinkEnsureMuxForNode
	defer func() {
		probeLinkTryPingExistingMux = oldPing
		probeLinkEnsureMuxForNode = oldEnsure
	}()

	probeLinkTryPingExistingMux = func(service *networkAssistantService, nodeID string) (time.Duration, bool) {
		return 0, false
	}
	probeLinkEnsureMuxForNode = func(service *networkAssistantService, nodeID string) error {
		return nil
	}

	result, err := pingNetworkAssistantTunnelNode(&networkAssistantService{}, "cloudserver")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.OK {
		t.Fatalf("expected failed result")
	}
	if !strings.Contains(result.Message, "按需建链后链路仍不可用") {
		t.Fatalf("unexpected message: %s", result.Message)
	}
}

type countingAcceptListener struct {
	net.Listener
	accepted *atomic.Int32
}

func (l *countingAcceptListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if l.accepted != nil {
		l.accepted.Add(1)
	}
	return conn, nil
}
