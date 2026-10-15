package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Optional local browser harness; serves the real DNS management handler/store.
func TestNodeDNSBrowserHarness(t *testing.T) {
	ready := os.Getenv("CLOUDHELPER_DNS_BROWSER_READY")
	if ready == "" {
		t.Skip("local browser harness")
	}
	useProbeControllerDoHTestState(t, nil)
	done := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/mng/route", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(mngRoutePageHTML))
	})
	mux.HandleFunc("/mng/api/route/dns-upstreams", mngRouteDNSUpstreamsHandler)
	mux.HandleFunc("/mng/api/route/doh", mngRouteDoHHandler)
	mux.HandleFunc("/done", func(w http.ResponseWriter, r *http.Request) { done <- struct{}{}; w.WriteHeader(204) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"authenticated": true, "items": []any{}, "nodes": []any{}, "records": []any{}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := os.MkdirAll(filepath.Dir(ready), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ready, []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
		t.Fatal("browser harness timed out")
	}
}

func TestNodeDNSManagementPersistsDispatchesAndRejectsAtomically(t *testing.T) {
	useProbeControllerDoHTestState(t, nil)
	originalDoH := ProbeRouteConfigStore.data.DoH
	oldScheduler := automaticProbeRouteConfigSyncScheduler
	notified := make(chan struct{}, 2)
	automaticProbeRouteConfigSyncScheduler = newProbeRouteConfigSyncScheduler(func() { notified <- struct{}{} })
	t.Cleanup(func() { automaticProbeRouteConfigSyncScheduler = oldScheduler })
	get := httptest.NewRecorder()
	mngRouteDNSUpstreamsHandler(get, httptest.NewRequest("GET", "/mng/api/route/dns-upstreams", nil))
	var view struct {
		Item probeDNSUpstreams `json:"item"`
	}
	if json.Unmarshal(get.Body.Bytes(), &view) != nil || view.Item != defaultProbeDNSUpstreams() {
		t.Fatalf("defaults %s", get.Body.String())
	}
	c := defaultProbeDNSUpstreams()
	c.DNS = "192.0.2.53:5353"
	raw, _ := json.Marshal(c)
	post := httptest.NewRecorder()
	mngRouteDNSUpstreamsHandler(post, httptest.NewRequest("POST", "/mng/api/route/dns-upstreams", bytes.NewReader(raw)))
	if post.Code != 200 {
		t.Fatalf("save %d %s", post.Code, post.Body.String())
	}
	select {
	case <-notified:
	case <-time.After(time.Second):
		t.Fatal("no config sync notification")
	}
	stored, err := os.ReadFile(ProbeRouteConfigStore.path)
	if err != nil {
		t.Fatal(err)
	}
	var data probeRouteConfigStoreData
	if json.Unmarshal(stored, &data) != nil || data.DNSUpstreams != c {
		t.Fatalf("persist %s", stored)
	}
	bad := httptest.NewRecorder()
	mngRouteDNSUpstreamsHandler(bad, httptest.NewRequest("POST", "/mng/api/route/dns-upstreams", bytes.NewBufferString(`{"doh":"http://bad","dot":"bad:853","dns":"127.0.0.1:53"}`)))
	if bad.Code != 400 || currentProbeDNSUpstreams() != c || ProbeRouteConfigStore.data.DoH != originalDoH {
		t.Fatal("invalid update changed configuration")
	}
}

func TestRouteConfigDeliversNodeDNSForAllProducts(t *testing.T) {
	useProbeControllerDoHTestState(t, nil)
	resetProbeAuthChallengeStateForTest()
	t.Cleanup(resetProbeAuthChallengeStateForTest)
	want := defaultProbeDNSUpstreams()
	want.DoT = "dns.example.com:8853"
	ProbeRouteConfigStore.data.DNSUpstreams = want
	for _, kind := range []string{probeNodeKindNormal, "android", probeNodeKindAsus56UV2} {
		ProbeStore.data.ProbeNodes = []probeNodeRecord{{NodeNo: 7, NodeKind: kind, NodeSecret: "fixture-secret"}}
		ProbeStore.data.ProbeSecrets = map[string]string{"7": "fixture-secret"}
		req := httptest.NewRequest(http.MethodGet, "/api/probe/route/config", nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		applyProbeChallengeAuthForTest(t, req, "7", "fixture-secret")
		rec := httptest.NewRecorder()
		ProbeRouteConfigHandler(rec, req)
		var payload probeRouteConfigResponse
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &payload) != nil || payload.DNSUpstreams != want {
			t.Fatalf("%s response %d %s", kind, rec.Code, rec.Body.String())
		}
	}
}
