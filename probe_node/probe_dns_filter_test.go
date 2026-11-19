package main

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudhelper/probe_node/internal/dnsfilter"
)

func TestProbeDNSFilterRejectsDNSAndOldFakeIPBeforeForwarding(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PROBE_NODE_DATA_DIR", dir)
	old := probeDNSFilter
	probeDNSFilter = &dnsfilter.Store{}
	t.Cleanup(func() { probeDNSFilter = old })
	if err := probeDNSFilter.Save(dir, dnsfilter.Config{BlockDoH: true, BlockDoT: true, Blacklist: []string{"1.1.1.1"}}); err != nil {
		t.Fatal(err)
	}
	query, err := buildProbeLocalDNSQueryA("1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolveProbeVirtualRouterDNSPacket(query)
	if err != nil || len(result.Response) < 4 || result.Response[3]&15 != 5 {
		t.Fatalf("DNS refusal err=%v response=%x", err, result.Response)
	}
	p := make([]byte, 40)
	p[0] = 0x45
	p[9] = 6
	copy(p[16:20], net.ParseIP("1.1.1.1").To4())
	binary.BigEndian.PutUint16(p[22:24], 443)
	if !handleProbeVirtualRouterTUNPacket(p) {
		t.Fatal("blacklisted real IP continued forwarding")
	}
	if _, err := decideProbeVRouteProxyTarget("1.1.1.1:443"); err == nil {
		t.Fatal("local proxy escaped the DNS blacklist")
	}
	probeVirtualRouterState.mu.Lock()
	previous := probeVirtualRouterState.config.FakeIPLibrary
	probeVirtualRouterState.config.FakeIPLibrary = probeVirtualRouterFakeIPLibrary{Items: []probeVirtualRouterFakeIPEntry{{Domain: "1.1.1.1", FakeIP: "198.18.4.9"}}}
	probeVirtualRouterState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config.FakeIPLibrary = previous
		probeVirtualRouterState.mu.Unlock()
	})
	copy(p[16:20], net.ParseIP("198.18.4.9").To4())
	p[9] = 17
	binary.BigEndian.PutUint16(p[22:24], 80)
	if !probeDNSFilterBlocksPacket(p) || !handleProbeVirtualRouterTUNPacket(p) {
		t.Fatal("old cached Fake IP escaped UDP interception")
	}
}

func TestProbeDNSFilterSettingsRequireLogin(t *testing.T) {
	w := httptest.NewRecorder()
	probeLocalDNSFilterHandler(w, httptest.NewRequest(http.MethodPost, "/local/api/virtual_router/dns_filter", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestProbeDNSFilterAuthenticatedSaveAndValidation(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	cookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	old := probeDNSFilter
	probeDNSFilter = &dnsfilter.Store{}
	t.Cleanup(func() { probeDNSFilter = old })
	endpoint := "/local/api/virtual_router/dns_filter"
	response := doProbeLocalRequest(t, mux, http.MethodPost, endpoint, map[string]any{"block_doh": true, "blacklist": []string{"1.1.1.1"}}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("save=%d %s", response.Code, response.Body.String())
	}
	response = doProbeLocalRequest(t, mux, http.MethodPost, endpoint, map[string]any{"block_doh": false, "blacklist": []string{"https://invalid.example/dns-query"}}, cookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid save=%d", response.Code)
	}
	response = doProbeLocalRequest(t, mux, http.MethodGet, endpoint, nil, cookie)
	var payload struct {
		OK     bool             `json:"ok"`
		Config dnsfilter.Config `json:"config"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || !payload.OK || !payload.Config.BlockDoH || len(payload.Config.Blacklist) != 1 {
		t.Fatalf("saved policy changed after validation error: %s", response.Body.String())
	}
}
