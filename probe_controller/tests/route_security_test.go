package tests

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
