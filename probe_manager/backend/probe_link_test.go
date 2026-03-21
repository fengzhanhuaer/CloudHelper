package backend

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
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
