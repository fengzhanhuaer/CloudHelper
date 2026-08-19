package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeProbeRouteNodeID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "plain numeric", in: " 001 ", want: "1"},
		{name: "node dash numeric", in: "node-21", want: "21"},
		{name: "node underscore numeric", in: "Node_003", want: "3"},
		{name: "node dash text", in: "NODE-ABC", want: "abc"},
		{name: "custom id", in: " custom-id ", want: "custom-id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeProbeRouteNodeID(tc.in); got != tc.want {
				t.Fatalf("normalizeProbeRouteNodeID(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFetchProbeRouteConfigUsesRouteEndpoint(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PROBE_NODE_DATA_DIR", dataDir)
	previousApply := probeApplyProductRouteConfig
	probeApplyProductRouteConfig = func(*probeSpecialExitSnapshot, string) error { return nil }
	t.Cleanup(func() { probeApplyProductRouteConfig = previousApply })

	var requestedPath string
	var requestedNodeID string
	var requestedSecret string
	var requestedAuthNodeID string
	var requestedSignature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/probe/auth/challenge" {
			_ = json.NewEncoder(w).Encode(map[string]string{"challenge": "controller-issued-test-challenge"})
			return
		}
		requestedPath = r.URL.Path
		requestedNodeID = r.URL.Query().Get("node_id")
		requestedSecret = r.URL.Query().Get("secret")
		requestedAuthNodeID = r.Header.Get("X-Probe-Node-Id")
		requestedSignature = r.Header.Get("X-Probe-Signature")
		if r.URL.Path != probeRouteConfigAPIPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(probeRouteConfigResponse{
			ExpectedNodeKind: currentProbeBuildKind(),
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: "198.18.0.0/15",
				TopologyRules: []probeVirtualRouterTopologyRule{{
					ID:         "vr-a",
					FromNodeID: "1",
					ToNodeID:   "2",
					Enabled:    true,
				}},
				RouteRules: []probeVirtualRouterRouteRule{{
					Name:       "media",
					Action:     "probe_exit",
					ExitNodeID: "2",
					Entries:    []string{"domain_suffix:reddit.com"},
				}},
			},
		})
	}))
	defer server.Close()

	config, err := fetchProbeRouteConfig(context.Background(), server.URL, nodeIdentity{NodeID: "7", Secret: "secret-7"})
	if err != nil {
		t.Fatalf("fetchProbeRouteConfig failed: %v", err)
	}
	if requestedPath != probeRouteConfigAPIPath {
		t.Fatalf("requested path=%q, want %q", requestedPath, probeRouteConfigAPIPath)
	}
	if requestedNodeID != "" || requestedSecret != "" {
		t.Fatalf("node credentials must not appear in query: node_id=%q secret=%q", requestedNodeID, requestedSecret)
	}
	if requestedAuthNodeID != "7" || requestedSignature == "" {
		t.Fatalf("missing challenge auth headers: node_id=%q signature=%q", requestedAuthNodeID, requestedSignature)
	}
	if len(config.TopologyRules) != 1 || len(config.RouteRules) != 1 {
		t.Fatalf("unexpected route config: %+v", config)
	}
	if config.RouteRules[0].Action != "probe_exit" || config.RouteRules[0].ExitNodeID != "2" {
		t.Fatalf("unexpected route rule action: %+v", config.RouteRules[0])
	}
}

func TestFetchProbeRouteConfigRejectsExpectedNodeKindMismatchAfterHMAC(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	mismatch := probeBuildKindNormal
	if currentProbeBuildKind() == probeBuildKindNormal {
		mismatch = probeBuildKindMihomoExit
	}
	authenticated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/probe/auth/challenge" {
			_ = json.NewEncoder(w).Encode(map[string]string{"challenge": "controller-issued-kind-mismatch"})
			return
		}
		authenticated = r.Header.Get("X-Probe-Node-Id") == "7" && r.Header.Get("X-Probe-Signature") != ""
		_ = json.NewEncoder(w).Encode(probeRouteConfigResponse{
			ExpectedNodeKind: mismatch,
			VirtualRouter:    probeVirtualRouterConfig{Enabled: true},
		})
	}))
	defer server.Close()

	_, err := fetchProbeRouteConfig(context.Background(), server.URL, nodeIdentity{NodeID: "7", Secret: "secret-7"})
	if err == nil {
		t.Fatal("expected node kind mismatch was accepted")
	}
	if !authenticated {
		t.Fatal("expected node kind check changed or bypassed HMAC request headers")
	}
}

func TestRouteConfigSyncControlReportsAppliedStateImmediately(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	previousRequest := probeRequestRouteConfig
	previousApply := probeApplyProductRouteConfig
	defer func() {
		probeRequestRouteConfig = previousRequest
		probeApplyProductRouteConfig = previousApply
		setProbeImmediateReporter(nil)
	}()
	probeRequestRouteConfig = func(context.Context, string, nodeIdentity) (probeVirtualRouterConfig, error) {
		return probeVirtualRouterConfig{Enabled: false}, nil
	}
	probeApplyProductRouteConfig = func(*probeSpecialExitSnapshot, string) error { return nil }
	reports := 0
	setProbeImmediateReporter(func() error {
		reports++
		return nil
	})
	runProbeRouteConfigSyncControl(probeControlMessage{ControllerBaseURL: "https://controller.example"}, nodeIdentity{NodeID: "19", Secret: "secret"})
	if reports != 1 {
		t.Fatalf("immediate report count=%d, want 1", reports)
	}
}

func TestRouteConfigSyncControlSchedulerCoalescesBurstAndKeepsLatest(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	var runs atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	var urlsMu sync.Mutex
	var urls []string
	scheduler := newProbeRouteConfigSyncControlScheduler(func(message probeControlMessage, _ nodeIdentity) {
		runs.Add(1)
		current := active.Add(1)
		for {
			maximum := maxActive.Load()
			if current <= maximum || maxActive.CompareAndSwap(maximum, current) {
				break
			}
		}
		urlsMu.Lock()
		urls = append(urls, message.ControllerBaseURL)
		urlsMu.Unlock()
		started <- message.ControllerBaseURL
		<-release
		active.Add(-1)
	})

	scheduler.Schedule(probeControlMessage{ControllerBaseURL: "https://first.example"}, nodeIdentity{NodeID: "1"})
	waitProbeRouteConfigControlRun(t, started, "https://first.example")
	for i := 0; i < 99; i++ {
		scheduler.Schedule(probeControlMessage{ControllerBaseURL: "https://stale.example"}, nodeIdentity{NodeID: "1"})
	}
	scheduler.Schedule(probeControlMessage{ControllerBaseURL: "https://latest.example"}, nodeIdentity{NodeID: "1"})
	release <- struct{}{}
	waitProbeRouteConfigControlRun(t, started, "https://latest.example")
	release <- struct{}{}
	waitProbeRouteConfigControlSchedulerIdle(t, scheduler)

	if got := runs.Load(); got != 2 {
		t.Fatalf("runs=%d, want 2", got)
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max active=%d, want 1", got)
	}
	urlsMu.Lock()
	defer urlsMu.Unlock()
	if len(urls) != 2 || urls[0] != "https://first.example" || urls[1] != "https://latest.example" {
		t.Fatalf("urls=%v", urls)
	}
}

func waitProbeRouteConfigControlRun(t *testing.T, started <-chan string, want string) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("controller URL=%q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}

func waitProbeRouteConfigControlSchedulerIdle(t *testing.T, scheduler *probeRouteConfigSyncControlScheduler) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		scheduler.mu.Lock()
		idle := !scheduler.running && !scheduler.pending
		scheduler.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("scheduler did not become idle")
}
