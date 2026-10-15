package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Optional real-browser harness. It uses the production handlers and store,
// with the same trusted context as the authenticated controller console bridge.
func TestProbeGatewayBrowserHarness(t *testing.T) {
	ready := os.Getenv("CLOUDHELPER_GATEWAY_BROWSER_READY")
	if ready == "" {
		t.Skip("interactive browser harness")
	}
	prepareGatewayTest(t)
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.localNodeID = "23"
	probeVirtualRouterState.config.ProbeIPs = []probeVirtualRouterProbeIP{{NodeID: "9", IP: "198.18.0.9", DisplayName: "Work"}, {NodeID: "14", IP: "198.18.0.14", DisplayName: "Desktop"}}
	probeVirtualRouterState.mu.Unlock()
	if err := loadProbeGatewayConfig(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 1)
	mux := buildProbeLocalConsoleMux()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/done" {
			done <- struct{}{}
			return
		}
		mux.ServeHTTP(w, r.WithContext(withProbeLocalConsoleTrusted(r.Context())))
	}))
	defer server.Close()
	raw, _ := json.Marshal(map[string]string{"origin": server.URL})
	if err := os.WriteFile(ready, raw, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(4 * time.Minute):
		t.Fatal("browser did not finish")
	}
}

func TestProbeGatewayWebRequiresAuthentication(t *testing.T) {
	w := httptest.NewRecorder()
	probeGatewayConfigHandler(w, httptest.NewRequest("GET", "/local/api/virtual_router/gateway", nil))
	if w.Code != 401 {
		t.Fatalf("API auth status %d", w.Code)
	}
	w = httptest.NewRecorder()
	probeGatewayPageHandler(w, httptest.NewRequest("GET", "/local/virtual-router/gateway", nil))
	if w.Code != 302 {
		t.Fatalf("page auth status %d", w.Code)
	}
}
