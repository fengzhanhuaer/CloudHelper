package tests

import (
	"bytes"
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
