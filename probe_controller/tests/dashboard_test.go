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
	if msg, _ := payload["message"].(string); msg != "pong" {
		t.Fatalf("expected message=pong, got %v", payload["message"])
	}
	if _, ok := payload["uptime"]; !ok {
		t.Fatalf("expected uptime field in /dashboard/status response")
	}
}
