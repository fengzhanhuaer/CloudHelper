package tests

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

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
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/version to be removed and return 404, got %d", rr.Code)
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
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/upgrade to be removed and return 404, got %d", rr.Code)
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
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/upgrade/progress to be removed and return 404, got %d", rr.Code)
	}
}

func TestAdminUpgradeProgressRouteRequiresHTTPS(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/upgrade/progress", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/upgrade/progress to be removed and return 404, got %d", rr.Code)
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
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/proxy/github/latest to be removed and return 404, got %d", rr.Code)
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
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/proxy/download to be removed and return 404, got %d", rr.Code)
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
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/tunnel/nodes to be removed and return 404, got %d", rr.Code)
	}
}

func TestAdminTunnelNodesRouteRequiresHTTPS(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/tunnel/nodes", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/tunnel/nodes to be removed and return 404, got %d", rr.Code)
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
	if rr1.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/proxy/github/latest to be removed and return 404, got %d", rr1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/admin/proxy/download?url=https://example.com/file", nil)
	req2.Header.Set("Authorization", "Bearer tok-proxy")
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/proxy/download to be removed and return 404, got %d", rr2.Code)
	}
}

func TestAdminProxyDownloadRouteRemoved(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	authManager.AddSessionForTest("tok-proxy-any", time.Now().Add(2*time.Minute))
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/proxy/download?url=https://example.com/file.bin", nil)
	req.Header.Set("Authorization", "Bearer tok-proxy-any")
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/proxy/download to be removed and return 404, got %d", rr.Code)
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
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/ws/status to be removed and return 404, got %d", rr.Code)
	}
}

func TestAdminWSStatusRouteRequiresHTTPS(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/ws/status?token=tok-ws", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected /api/admin/ws/status to be removed and return 404, got %d", rr.Code)
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
	session, err := yamux.Client(newTestWebSocketNetConn(conn), nil)
	if err != nil {
		t.Fatalf("failed to create yamux client: %v", err)
	}
	defer session.Close()

	stream, err := session.Open()
	if err != nil {
		t.Fatalf("failed to open yamux stream: %v", err)
	}
	defer stream.Close()

	_ = stream.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(stream).Encode(map[string]string{
		"type":    "open",
		"network": "tcp",
		"address": echoListener.Addr().String(),
	}); err != nil {
		t.Fatalf("failed to send tunnel open request: %v", err)
	}
	_ = stream.SetWriteDeadline(time.Time{})

	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(stream).Decode(&ack); err != nil {
		t.Fatalf("failed to read tunnel open ack: %v", err)
	}
	if !ack.OK {
		t.Fatalf("expected tunnel open ack ok=true")
	}
	_ = stream.SetReadDeadline(time.Time{})

	if _, err := stream.Write([]byte("hello-tunnel")); err != nil {
		t.Fatalf("failed to write tunnel stream payload: %v", err)
	}

	payload := make([]byte, len("hello-tunnel"))
	if _, err := io.ReadFull(stream, payload); err != nil {
		t.Fatalf("failed to read tunnel echo payload: %v", err)
	}
	if string(payload) != "hello-tunnel" {
		t.Fatalf("expected tunnel echo payload hello-tunnel, got %q", string(payload))
	}
}

func TestProbeCertificateRouteMethodNotAllowed(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodPost, "/api/probe/certificate", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected POST /api/probe/certificate to return 405, got %d", rr.Code)
	}
}

func TestProbeCertificateRouteRequiresHTTPS(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/probe/certificate", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected /api/probe/certificate without https marker to return 426, got %d", rr.Code)
	}
}

func TestProbeCertificateRouteRequiresProbeAuth(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/api/probe/certificate", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected /api/probe/certificate without probe auth to return 401, got %d", rr.Code)
	}
}

type testWebSocketNetConn struct {
	ws *websocket.Conn

	readMu  sync.Mutex
	writeMu sync.Mutex
	reader  io.Reader
}

func newTestWebSocketNetConn(ws *websocket.Conn) net.Conn {
	return &testWebSocketNetConn{ws: ws}
}

func (c *testWebSocketNetConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	for {
		if c.reader == nil {
			mt, reader, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if mt != websocket.BinaryMessage && mt != websocket.TextMessage {
				continue
			}
			c.reader = reader
		}

		n, err := c.reader.Read(p)
		if errors.Is(err, io.EOF) {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *testWebSocketNetConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	writer, err := c.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, writeErr := writer.Write(p)
	closeErr := writer.Close()
	if writeErr != nil {
		return n, writeErr
	}
	if closeErr != nil {
		return n, closeErr
	}
	return n, nil
}

func (c *testWebSocketNetConn) Close() error {
	return c.ws.Close()
}

func (c *testWebSocketNetConn) LocalAddr() net.Addr {
	return c.ws.UnderlyingConn().LocalAddr()
}

func (c *testWebSocketNetConn) RemoteAddr() net.Addr {
	return c.ws.UnderlyingConn().RemoteAddr()
}

func (c *testWebSocketNetConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}

func (c *testWebSocketNetConn) SetReadDeadline(t time.Time) error {
	return c.ws.SetReadDeadline(t)
}

func (c *testWebSocketNetConn) SetWriteDeadline(t time.Time) error {
	return c.ws.SetWriteDeadline(t)
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
