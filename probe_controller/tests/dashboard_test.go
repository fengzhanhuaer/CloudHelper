package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudhelper/probe_controller/internal/core"
)

func TestDashboardRouteAndRootRedirect(t *testing.T) {
	mux := core.NewMux()

	reqDashboard := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rrDashboard := httptest.NewRecorder()
	mux.ServeHTTP(rrDashboard, reqDashboard)

	if rrDashboard.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard 200, got %d", rrDashboard.Code)
	}

	reqRoot := httptest.NewRequest(http.MethodGet, "/", nil)
	rrRoot := httptest.NewRecorder()
	mux.ServeHTTP(rrRoot, reqRoot)

	if rrRoot.Code != http.StatusFound {
		t.Fatalf("expected GET / 302, got %d", rrRoot.Code)
	}
	if got := rrRoot.Header().Get("Location"); got != "/dashboard" {
		t.Fatalf("expected redirect to /dashboard, got %q", got)
	}
}

func TestDashboardStatusRouteNoAuthRequired(t *testing.T) {
	core.SetServerStartTimeForTest(time.Now().Add(-1 * time.Minute))
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard/status 200, got %d", rr.Code)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse /dashboard/status response: %v", err)
	}
	if _, ok := payload["uptime"]; !ok {
		t.Fatalf("expected uptime field in /dashboard/status response")
	}
}

func TestFaviconRoutesNoAuthRequired(t *testing.T) {
	mux := core.NewMux()

	reqSVG := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	rrSVG := httptest.NewRecorder()
	mux.ServeHTTP(rrSVG, reqSVG)
	if rrSVG.Code != http.StatusOK {
		t.Fatalf("expected GET /favicon.svg 200, got %d", rrSVG.Code)
	}
	if got := rrSVG.Header().Get("Content-Type"); got == "" {
		t.Fatalf("expected content-type for /favicon.svg")
	}

	reqICO := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	rrICO := httptest.NewRecorder()
	mux.ServeHTTP(rrICO, reqICO)
	if rrICO.Code != http.StatusFound {
		t.Fatalf("expected GET /favicon.ico 302, got %d", rrICO.Code)
	}
	if got := rrICO.Header().Get("Location"); got != "/favicon.svg" {
		t.Fatalf("expected /favicon.ico redirect to /favicon.svg, got %q", got)
	}
}

func TestDashboardPrefixedFaviconRoutesNoAuthRequired(t *testing.T) {
	mux := core.NewMux()

	reqSVG := httptest.NewRequest(http.MethodGet, "/dashboard/favicon.svg", nil)
	rrSVG := httptest.NewRecorder()
	mux.ServeHTTP(rrSVG, reqSVG)
	if rrSVG.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard/favicon.svg 200, got %d", rrSVG.Code)
	}
	if got := rrSVG.Header().Get("Content-Type"); got == "" {
		t.Fatalf("expected content-type for /dashboard/favicon.svg")
	}

	reqICO := httptest.NewRequest(http.MethodGet, "/dashboard/favicon.ico", nil)
	rrICO := httptest.NewRecorder()
	mux.ServeHTTP(rrICO, reqICO)
	if rrICO.Code != http.StatusFound {
		t.Fatalf("expected GET /dashboard/favicon.ico 302, got %d", rrICO.Code)
	}
	if got := rrICO.Header().Get("Location"); got != "/dashboard/favicon.svg" {
		t.Fatalf("expected /dashboard/favicon.ico redirect to /dashboard/favicon.svg, got %q", got)
	}
}
