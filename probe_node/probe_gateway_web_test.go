package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

func waitProbeGatewayReportIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		probeGatewayReport.Lock()
		running := probeGatewayReport.running
		probeGatewayReport.Unlock()
		if !running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("gateway report worker did not finish")
}

func postGatewayTestConfig(c probeGatewayProxyConfig) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(c)
	r := httptest.NewRequest(http.MethodPost, "/local/api/virtual_router/gateway", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	probeGatewayConfigHandler(w, r.WithContext(withProbeLocalConsoleTrusted(r.Context())))
	return w
}

func TestProbeGatewaySaveDoesNotWaitForUploadAndCoalescesLatest(t *testing.T) {
	prepareGatewayTest(t)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() { unblock(); waitProbeGatewayReportIdle(t); setProbeImmediateReporter(nil) }()
	var reports []probeGatewayProxyConfig // Read only after the worker is idle.
	setProbeImmediateReporter(func() error {
		reports = append(reports, *currentProbeGatewayConfig())
		if len(reports) == 1 {
			close(started)
			<-release
			return errors.New("controller unavailable")
		}
		return nil
	})
	first := probeGatewayProxyConfig{true, []string{"192.168.50.0/24"}, []string{"9"}}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- postGatewayTestConfig(first) }()
	select {
	case w := <-response:
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("save waited for the controller report")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("save did not schedule a report")
	}
	latest := first
	for i := 0; i < 20; i++ {
		latest.AllowedNodeIDs = []string{fmt.Sprint(i + 100)}
		if w := postGatewayTestConfig(latest); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	unblock()
	waitProbeGatewayReportIdle(t)
	if len(reports) != 2 || !reflect.DeepEqual(reports[1], latest) {
		t.Fatalf("burst was not coalesced into the latest saved config: %+v", reports)
	}
	if err := loadProbeGatewayConfig(); err != nil || !reflect.DeepEqual(currentProbeGatewayConfig(), &latest) {
		t.Fatalf("failed upload lost persisted authorization: %v", err)
	}
}

func TestProbeGatewaySaveOfflineAndInvalidRequests(t *testing.T) {
	prepareGatewayTest(t)
	setProbeImmediateReporter(nil)
	defer waitProbeGatewayReportIdle(t)
	c := probeGatewayProxyConfig{true, []string{"192.168.50.0/24"}, []string{"9"}}
	if w := postGatewayTestConfig(c); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	waitProbeGatewayReportIdle(t)
	setProbeImmediateReporter(func() error { t.Error("invalid request scheduled an upload"); return nil })
	defer setProbeImmediateReporter(nil)
	bad := c
	bad.ProxyCIDRs = []string{"0.0.0.0/0"}
	if w := postGatewayTestConfig(bad); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code)
	}
	waitProbeGatewayReportIdle(t)
	if err := loadProbeGatewayConfig(); err != nil || !reflect.DeepEqual(currentProbeGatewayConfig(), &c) {
		t.Fatalf("offline save/restart lost configuration: %v", err)
	}
}

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
