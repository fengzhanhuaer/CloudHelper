package tests

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/cloudhelper/probe_controller/internal/core"
)

func TestPingRouteRequiresAuth(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	core.SetServerStartTimeForTest(time.Now().Add(-1 * time.Minute))

	mux := core.NewMux()

	req1 := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	req1.Header.Set("X-Forwarded-Proto", "https")
	rr1 := httptest.NewRecorder()
	mux.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated /api/ping to return 401, got %d", rr1.Code)
	}

	authManager.AddSessionForTest("tok-ping", time.Now().Add(2*time.Minute))
	req2 := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	req2.Header.Set("Authorization", "Bearer tok-ping")
	req2.Header.Set("X-Forwarded-Proto", "https")
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected authenticated /api/ping to return 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}
}

func TestAdminVersionRouteRequiresAuth(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/version", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated /api/admin/version to return 401, got %d", rr.Code)
	}
}

func TestAdminUpgradeRouteRequiresAuth(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/upgrade", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated /api/admin/upgrade to return 401, got %d", rr.Code)
	}
}

func TestAdminUpgradeProgressRouteRequiresAuth(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/upgrade/progress", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated /api/admin/upgrade/progress to return 401, got %d", rr.Code)
	}
}

func TestAdminUpgradeProgressRouteRequiresHTTPS(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	authManager.AddSessionForTest("tok-upg-progress", time.Now().Add(2*time.Minute))
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/upgrade/progress", nil)
	req.Header.Set("Authorization", "Bearer tok-upg-progress")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected /api/admin/upgrade/progress without https marker to return 426, got %d", rr.Code)
	}
}

func TestAdminProxyLatestRouteRequiresAuth(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/proxy/github/latest", bytes.NewBufferString(`{"project":"owner/repo"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated /api/admin/proxy/github/latest to return 401, got %d", rr.Code)
	}
}

func TestAdminProxyDownloadRouteRequiresAuth(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/proxy/download?url=https://github.com/owner/repo/releases/download/v1/app.exe", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated /api/admin/proxy/download to return 401, got %d", rr.Code)
	}
}

func TestAdminTunnelNodesRouteRequiresAuth(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/tunnel/nodes", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated /api/admin/tunnel/nodes to return 401, got %d", rr.Code)
	}
}

func TestAdminTunnelNodesRouteRequiresHTTPS(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	authManager.AddSessionForTest("tok-nodes", time.Now().Add(2*time.Minute))
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/tunnel/nodes", nil)
	req.Header.Set("Authorization", "Bearer tok-nodes")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected /api/admin/tunnel/nodes without https marker to return 426, got %d", rr.Code)
	}
}

func TestAdminProxyRoutesRequireHTTPS(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	authManager.AddSessionForTest("tok-proxy", time.Now().Add(2*time.Minute))
	mux := core.NewMux()

	req1 := httptest.NewRequest(http.MethodPost, "/api/admin/proxy/github/latest", bytes.NewBufferString(`{"project":"owner/repo"}`))
	req1.Header.Set("Authorization", "Bearer tok-proxy")
	req1.Header.Set("Content-Type", "application/json")
	rr1 := httptest.NewRecorder()
	mux.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected /api/admin/proxy/github/latest without https marker to return 426, got %d", rr1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/admin/proxy/download?url=https://example.com/file", nil)
	req2.Header.Set("Authorization", "Bearer tok-proxy")
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected /api/admin/proxy/download without https marker to return 426, got %d", rr2.Code)
	}
}

func TestAdminProxyDownloadAllowsAnyHTTPSHost(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	authManager.AddSessionForTest("tok-proxy-any", time.Now().Add(2*time.Minute))
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/proxy/download?url=https://example.com/file.bin", nil)
	req.Header.Set("Authorization", "Bearer tok-proxy-any")
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// URL pass validation then proxy request runs (usually 5xx, not 400).
	if rr.Code == http.StatusBadRequest {
		t.Fatalf("expected any https host to pass proxy host check, got 400 body=%s", rr.Body.String())
	}
}

func TestAdminWSStatusRouteRequiresAuth(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/ws/status", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated /api/admin/ws/status to return 401, got %d", rr.Code)
	}
}

func TestAdminWSStatusRouteRequiresHTTPS(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	authManager.AddSessionForTest("tok-ws", time.Now().Add(2*time.Minute))
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/ws/status?token=tok-ws", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected /api/admin/ws/status without https marker to return 426, got %d", rr.Code)
	}
}

func TestAdminWSStatusRouteConnectSuccess(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	core.SetServerStartTimeForTest(time.Now().Add(-1 * time.Minute))
	authManager.AddSessionForTest("tok-ws-ok", time.Now().Add(2*time.Minute))

	server := httptest.NewServer(core.NewMux())
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/admin/ws/status?token=tok-ws-ok"
	header := http.Header{}
	header.Set("X-Forwarded-Proto", "https")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("failed to connect websocket: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var msg map[string]interface{}
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("failed to read websocket status payload: %v", err)
	}
	if msg["type"] != "status" {
		t.Fatalf("expected websocket message type=status, got %v", msg["type"])
	}
	if _, ok := msg["uptime"]; !ok {
		t.Fatalf("expected websocket payload to contain uptime")
	}
}

func TestNetworkTunnelWSRouteRequiresAuth(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/ws/tunnel/cloudserver", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated /api/ws/tunnel/cloudserver to return 401, got %d", rr.Code)
	}
}

func TestNetworkTunnelWSRouteRequiresHTTPS(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	authManager.AddSessionForTest("tok-tunnel-https", time.Now().Add(2*time.Minute))
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/ws/tunnel/cloudserver?token=tok-tunnel-https", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected /api/ws/tunnel/cloudserver without https marker to return 426, got %d", rr.Code)
	}
}

func TestNetworkTunnelWSRouteConnectSuccess(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	authManager.AddSessionForTest("tok-tunnel-ok", time.Now().Add(2*time.Minute))

	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create echo listener: %v", err)
	}
	defer echoListener.Close()

	go func() {
		for {
			conn, acceptErr := echoListener.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	server := httptest.NewServer(core.NewMux())
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/ws/tunnel/cloudserver?token=tok-tunnel-ok"
	header := http.Header{}
	header.Set("X-Forwarded-Proto", "https")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("failed to connect websocket tunnel: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]string{
		"type":    "connect",
		"network": "tcp",
		"address": echoListener.Addr().String(),
	}); err != nil {
		t.Fatalf("failed to send tunnel connect request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack map[string]interface{}
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("failed to read tunnel connect ack: %v", err)
	}
	if ack["type"] != "connected" {
		t.Fatalf("expected tunnel connect ack type=connected, got %v", ack["type"])
	}

	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("hello-tunnel")); err != nil {
		t.Fatalf("failed to write tunnel binary payload: %v", err)
	}

	msgType, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read tunnel echo payload: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("expected websocket binary message, got %d", msgType)
	}
	if string(payload) != "hello-tunnel" {
		t.Fatalf("expected tunnel echo payload hello-tunnel, got %q", string(payload))
	}
}

func TestHTTPSRequiredViaProxyHeader(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	core.SetServerStartTimeForTest(time.Now().Add(-1 * time.Minute))
	authManager.AddSessionForTest("tok-https", time.Now().Add(2*time.Minute))

	mux := core.NewMux()

	req1 := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	req1.Header.Set("Authorization", "Bearer tok-https")
	rr1 := httptest.NewRecorder()
	mux.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected /api/ping without https marker to return 426, got %d", rr1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	req2.Header.Set("Authorization", "Bearer tok-https")
	req2.Header.Set("X-Forwarded-Proto", "https")
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected /api/ping with https marker to return 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}
}
