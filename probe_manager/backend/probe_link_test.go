package backend

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
