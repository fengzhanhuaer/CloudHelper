package core

import (
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

func TestMngProbeConsoleTokenRoundTrip(t *testing.T) {
	token := mintMngProbeConsoleToken("3", "alpha-node")
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	node, ok := resolveMngProbeConsoleToken(token)
	if !ok || node != "3" {
		t.Fatalf("resolve failed: node=%q ok=%v", node, ok)
	}
	rec, ok := resolveMngProbeConsoleTokenRecord(token)
	if !ok || rec.DisplayName != "alpha-node" {
		t.Fatalf("expected display name to round-trip, rec=%+v ok=%v", rec, ok)
	}
	if _, ok := resolveMngProbeConsoleToken("does-not-exist"); ok {
		t.Fatal("unexpected resolve for unknown token")
	}
}

func TestMngProbeConsoleBridgeDeniedWithoutCookie(t *testing.T) {
	// API-style request -> 401 JSON.
	req := httptest.NewRequest(http.MethodGet, "/local/api/anything", nil)
	rec := httptest.NewRecorder()
	mngProbeConsoleBridgeHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without cookie, got %d", rec.Code)
	}

	// Top-level HTML navigation -> redirect back to the panel.
	nav := httptest.NewRequest(http.MethodGet, "/local/panel", nil)
	nav.Header.Set("Accept", "text/html")
	navRec := httptest.NewRecorder()
	mngProbeConsoleBridgeHandler(navRec, nav)
	if navRec.Code != http.StatusFound {
		t.Fatalf("expected redirect for html navigation, got %d", navRec.Code)
	}
}

func TestMngProbeConsoleTokenSlidingRenewal(t *testing.T) {
	token := mintMngProbeConsoleToken("7")
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	// Force the token close to expiry, then an active resolve should renew it.
	mngProbeConsoleTokens.mu.Lock()
	rec := mngProbeConsoleTokens.data[token]
	rec.ExpiresAt = time.Now().Add(2 * time.Second)
	mngProbeConsoleTokens.data[token] = rec
	mngProbeConsoleTokens.mu.Unlock()

	if node, ok := resolveMngProbeConsoleToken(token); !ok || node != "7" {
		t.Fatalf("resolve failed: node=%q ok=%v", node, ok)
	}

	mngProbeConsoleTokens.mu.Lock()
	got := mngProbeConsoleTokens.data[token].ExpiresAt
	mngProbeConsoleTokens.mu.Unlock()
	if time.Until(got) < time.Hour {
		t.Fatalf("expected sliding renewal to extend expiry, remaining=%v", time.Until(got))
	}
}

func TestMngProbeConsoleSessionRouteSupportsMultipleProbeTabs(t *testing.T) {
	tokenA := mintMngProbeConsoleToken("7", "node-a")
	tokenB := mintMngProbeConsoleToken("8", "node-b")
	if tokenA == "" || tokenB == "" {
		t.Fatal("expected non-empty tokens")
	}

	reqA := httptest.NewRequest(http.MethodGet, mngProbeConsoleSessionPrefix+tokenA+"/local/panel", nil)
	routeA, ok := resolveMngProbeConsoleBridgeRoute(reqA)
	if !ok {
		t.Fatal("expected route A to resolve")
	}
	reqB := httptest.NewRequest(http.MethodGet, mngProbeConsoleSessionPrefix+tokenB+"/local/shell", nil)
	routeB, ok := resolveMngProbeConsoleBridgeRoute(reqB)
	if !ok {
		t.Fatal("expected route B to resolve")
	}

	if routeA.TokenRecord.NodeID != "7" || routeA.ConsolePath != "/local/panel" {
		t.Fatalf("unexpected route A: %+v", routeA)
	}
	if routeB.TokenRecord.NodeID != "8" || routeB.ConsolePath != "/local/shell" {
		t.Fatalf("unexpected route B: %+v", routeB)
	}
	if routeA.URLPrefix == routeB.URLPrefix {
		t.Fatalf("session URL prefixes must be independent: %q", routeA.URLPrefix)
	}
}

func TestRewriteMngProbeConsoleHTMLLinksUsesSessionPrefix(t *testing.T) {
	headers := map[string][]string{"Content-Type": {"text/html; charset=utf-8"}}
	prefix := mngProbeConsoleSessionPrefix + "abc123"
	body := []byte("<a href=\"/local/panel\">home</a><script>fetch('/local/api/auth/session'); location.href = `/local/login`;</script>")

	got := string(rewriteMngProbeConsoleHTMLLinks(body, prefix, headers))

	for _, want := range []string{
		`href="` + prefix + `/local/panel"`,
		`fetch('` + prefix + `/local/api/auth/session')`,
		"`" + prefix + "/local/login`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten html missing %q: %s", want, got)
		}
	}
}

func TestMngProbeConsoleBridgeDeniedRemintsWithNodeCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/local/panel", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(&http.Cookie{Name: mngProbeConsoleNodeCookieName, Value: "5"})
	rec := httptest.NewRecorder()
	mngProbeConsoleBridgeHandler(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/mng/probe-console?node=5" {
		t.Fatalf("expected transparent re-mint redirect, got %q", loc)
	}
}

func TestMngProbeConsoleHeaderFilters(t *testing.T) {
	if !mngProbeConsoleSkipRequestHeader("Cookie") || !mngProbeConsoleSkipRequestHeader("Host") {
		t.Fatal("Cookie/Host must not be forwarded to the probe")
	}
	if mngProbeConsoleSkipRequestHeader("Content-Type") {
		t.Fatal("Content-Type must be forwarded")
	}
	if !mngProbeConsoleSkipResponseHeader("Set-Cookie") {
		t.Fatal("Set-Cookie must be stripped from proxied responses")
	}
	if mngProbeConsoleSkipResponseHeader("Content-Type") {
		t.Fatal("Content-Type must be returned to the browser")
	}
}

func TestMngProbeConsoleBridgeMarksControllerBridgeResponse(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	oldRuntimeData := probeRuntimeStore.data
	probeRuntimeStore.mu.Lock()
	probeRuntimeStore.data = make(map[string]probeRuntimeStatus)
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = oldRuntimeData
		probeRuntimeStore.mu.Unlock()
	})

	nodeID := "bridge-node"
	session := &probeSession{nodeID: nodeID, stream: clientConn, enc: json.NewEncoder(clientConn)}
	probeSessions.mu.Lock()
	probeSessions.data[nodeID] = session
	probeSessions.mu.Unlock()
	defer func() {
		probeSessions.mu.Lock()
		delete(probeSessions.data, nodeID)
		probeSessions.mu.Unlock()
	}()

	var nodeWriteMu sync.Mutex
	go func() {
		decoder := json.NewDecoder(clientConn)
		for {
			var result probeLocalConsoleBridgeResultMessage
			if err := decoder.Decode(&result); err != nil {
				return
			}
			consumeProbeLocalConsoleBridgeResult(result)
		}
	}()

	go func() {
		decoder := json.NewDecoder(serverConn)
		var cmd probeLocalConsoleBridgeCommand
		if err := decoder.Decode(&cmd); err != nil {
			return
		}
		nodeWriteMu.Lock()
		_ = json.NewEncoder(serverConn).Encode(probeLocalConsoleBridgeResultMessage{
			Type:       "local_console_bridge_result",
			RequestID:  cmd.RequestID,
			NodeID:     nodeID,
			OK:         true,
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			Body:       base64.StdEncoding.EncodeToString([]byte("<!doctype html><title>Probe Node Shell</title>")),
		})
		nodeWriteMu.Unlock()
	}()

	token := mintMngProbeConsoleToken(nodeID, "bridge-node")
	req := httptest.NewRequest(http.MethodGet, "/local/shell", nil)
	req.AddCookie(&http.Cookie{Name: mngProbeConsoleCookieName, Value: token})
	rec := httptest.NewRecorder()

	mngProbeConsoleBridgeHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Probe-Console-Bridge"); got != "controller" {
		t.Fatalf("expected controller bridge marker, got %q", got)
	}
}

func TestApplyMngProbeConsoleTitle(t *testing.T) {
	body := []byte("<!doctype html><html><head><title>Probe Node 本地控制台</title></head><body></body></html>")
	headers := map[string][]string{"Content-Type": {"text/html; charset=utf-8"}}
	got := string(applyMngProbeConsoleTitle(body, "alpha-node", headers))
	if want := "<title>alpha-node - Probe Node 本地控制台</title>"; !strings.Contains(got, want) {
		t.Fatalf("expected injected title %q, got %q", want, got)
	}
}

func TestApplyMngProbeConsoleTitleSkipsNonHTML(t *testing.T) {
	body := []byte(`{"ok":true}`)
	headers := map[string][]string{"Content-Type": {"application/json"}}
	got := string(applyMngProbeConsoleTitle(body, "alpha-node", headers))
	if got != string(body) {
		t.Fatalf("expected non-html body unchanged, got %q", got)
	}
}
