package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProbeLocalConsoleTrustedBypass(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	mux := buildProbeLocalConsoleMux()

	// Without the trusted context, a protected API is unauthorized.
	req := httptest.NewRequest(http.MethodGet, "/local/api/tun/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without trust, got %d", rec.Code)
	}

	// With the trusted context (set only by in-process controller proxy), it passes.
	trusted := httptest.NewRequest(http.MethodGet, "/local/api/tun/status", nil).
		WithContext(withProbeLocalConsoleTrusted(context.Background()))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, trusted)
	if rec2.Code == http.StatusUnauthorized {
		t.Fatalf("expected trusted bypass to pass auth, got 401")
	}
}

func TestRunProbeLocalConsoleProxyServesPanel(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	var mu sync.Mutex
	enc := json.NewEncoder(serverConn)
	msg := probeControlMessage{
		Type:          "local_console_proxy",
		RequestID:     "req-1",
		ConsoleMethod: http.MethodGet,
		ConsolePath:   "/local/panel",
	}
	go runProbeLocalConsoleProxy(msg, nodeIdentity{NodeID: "1"}, serverConn, enc, &mu)

	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var res probeLocalConsoleProxyResult
	if err := json.NewDecoder(clientConn).Decode(&res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !res.OK || res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 panel, got ok=%v status=%d err=%s", res.OK, res.StatusCode, res.Error)
	}
	body, err := base64.StdEncoding.DecodeString(res.Body)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.Contains(string(body), "<") {
		t.Fatalf("expected HTML body, got %q", string(body))
	}
}

func TestIsProbeLocalConsoleHopHeader(t *testing.T) {
	if !isProbeLocalConsoleHopHeader("Connection") || !isProbeLocalConsoleHopHeader("Content-Length") {
		t.Fatal("expected hop headers to be filtered")
	}
	if isProbeLocalConsoleHopHeader("Content-Type") {
		t.Fatal("Content-Type must be forwarded")
	}
}
