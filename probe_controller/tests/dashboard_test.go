package tests

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
