package core

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestProbeControllerDoHFakeIPAndNonAProtection(t *testing.T) {
	useProbeControllerDoHTestState(t, []probeVirtualRouterRouteRule{{
		ID:         "rr-proxy",
		Name:       "proxy",
		Action:     probeVirtualRouterRouteRuleActionExit,
		ExitNodeID: "2",
		Entries:    []string{"domain_suffix:example.com"},
	}})

	upstreamCalls := 0
	probeControllerDoHQueryUpstream = func(context.Context, string, []byte) ([]byte, error) {
		upstreamCalls++
		return nil, nil
	}

	aPacket := buildProbeControllerDoHTestQuery(t, "api.example.com.", dnsmessage.TypeA)
	aResponse := serveProbeControllerDoHTestRequest(t, http.MethodPost, "/dns-query/test-doh-token", aPacket)
	if aResponse.Code != http.StatusOK || aResponse.Header().Get("Content-Type") != "application/dns-message" {
		t.Fatalf("A response status=%d content_type=%q body=%s", aResponse.Code, aResponse.Header().Get("Content-Type"), aResponse.Body.String())
	}
	var aMessage dnsmessage.Message
	if err := aMessage.Unpack(aResponse.Body.Bytes()); err != nil {
		t.Fatalf("unpack A response: %v", err)
	}
	if len(aMessage.Answers) != 1 || aMessage.Answers[0].Header.TTL != probeControllerDoHTTLSeconds {
		t.Fatalf("A answers=%+v", aMessage.Answers)
	}
	aBody, ok := aMessage.Answers[0].Body.(*dnsmessage.AResource)
	if !ok || aBody.A[0] != 198 || aBody.A[1] != 18 {
		t.Fatalf("A answer=%+v, want 198.18.0.0/15", aMessage.Answers[0].Body)
	}

	aaaaPacket := buildProbeControllerDoHTestQuery(t, "api.example.com.", dnsmessage.TypeAAAA)
	aaaaResponse := serveProbeControllerDoHTestRequest(t, http.MethodPost, "/dns-query/test-doh-token", aaaaPacket)
	var aaaaMessage dnsmessage.Message
	if err := aaaaMessage.Unpack(aaaaResponse.Body.Bytes()); err != nil {
		t.Fatalf("unpack AAAA response: %v", err)
	}
	if aaaaMessage.Header.RCode != dnsmessage.RCodeSuccess || len(aaaaMessage.Answers) != 0 {
		t.Fatalf("AAAA response=%+v, want successful empty response", aaaaMessage)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls=%d, matched proxy queries must not resolve real addresses", upstreamCalls)
	}

	items, _ := snapshotProbeControllerDoHQueryRecords(10)
	if len(items) != 2 || items[0].Action != "fake_ip" || items[0].QueryType != "AAAA" || items[1].Action != "fake_ip" || items[1].Answers[0] == "" {
		t.Fatalf("query records=%+v", items)
	}
}

func TestProbeControllerDoHDirectGETNormalizesTTLAndRejectsWrongToken(t *testing.T) {
	useProbeControllerDoHTestState(t, nil)
	query := buildProbeControllerDoHTestQuery(t, "direct.example.net.", dnsmessage.TypeA)
	probeControllerDoHQueryUpstream = func(_ context.Context, _ string, packet []byte) ([]byte, error) {
		var request dnsmessage.Message
		if err := request.Unpack(packet); err != nil {
			return nil, err
		}
		response := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: request.Header.ID, Response: true, RecursionAvailable: true},
			Questions: request.Questions,
			Answers: []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: request.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30},
				Body:   &dnsmessage.AResource{A: [4]byte{203, 0, 113, 9}},
			}},
		}
		return response.Pack()
	}

	wrong := serveProbeControllerDoHTestRequest(t, http.MethodPost, "/dns-query/wrong-token", query)
	if wrong.Code != http.StatusNotFound {
		t.Fatalf("wrong token status=%d, want 404", wrong.Code)
	}
	if items, _ := snapshotProbeControllerDoHQueryRecords(10); len(items) != 0 {
		t.Fatalf("wrong token must not create records: %+v", items)
	}

	encoded := base64.RawURLEncoding.EncodeToString(query)
	req := httptest.NewRequest(http.MethodGet, "/dns-query/test-doh-token?dns="+encoded, nil)
	req.TLS = &tls.ConnectionState{}
	req.RemoteAddr = "192.0.2.15:53000"
	rr := httptest.NewRecorder()
	ProbeControllerDoHHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rr.Code, rr.Body.String())
	}
	var message dnsmessage.Message
	if err := message.Unpack(rr.Body.Bytes()); err != nil {
		t.Fatalf("unpack direct response: %v", err)
	}
	if len(message.Answers) != 1 || message.Answers[0].Header.TTL != probeControllerDoHTTLSeconds {
		t.Fatalf("direct answers=%+v", message.Answers)
	}
	items, _ := snapshotProbeControllerDoHQueryRecords(10)
	if len(items) != 1 || items[0].ClientIP != "192.0.2.15" || items[0].Action != "direct" || strings.Join(items[0].Answers, ",") != "203.0.113.9" {
		t.Fatalf("direct query records=%+v", items)
	}
}

func TestProbeControllerDoHRejectAndManagementAPIs(t *testing.T) {
	useProbeControllerDoHTestState(t, []probeVirtualRouterRouteRule{{
		ID:      "rr-reject",
		Name:    "blocked",
		Action:  probeVirtualRouterRouteRuleActionReject,
		Entries: []string{"domain_suffix:blocked.example"},
	}})
	query := buildProbeControllerDoHTestQuery(t, "ads.blocked.example.", dnsmessage.TypeA)
	response := serveProbeControllerDoHTestRequest(t, http.MethodPost, "/dns-query/test-doh-token", query)
	var message dnsmessage.Message
	if err := message.Unpack(response.Body.Bytes()); err != nil {
		t.Fatalf("unpack reject response: %v", err)
	}
	if message.Header.RCode != dnsmessage.RCodeRefused {
		t.Fatalf("rcode=%v, want refused", message.Header.RCode)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/mng/api/route/doh", nil)
	getRR := httptest.NewRecorder()
	mngRouteDoHHandler(getRR, getReq)
	if getRR.Code != http.StatusOK || !strings.Contains(getRR.Body.String(), "/dns-query/test-doh-token") {
		t.Fatalf("settings GET status=%d body=%s", getRR.Code, getRR.Body.String())
	}

	postReq := httptest.NewRequest(http.MethodPost, "/mng/api/route/doh", strings.NewReader(`{"enabled":true,"upstream":"https://doh.pub/dns-query","rotate_token":true}`))
	postRR := httptest.NewRecorder()
	mngRouteDoHHandler(postRR, postReq)
	if postRR.Code != http.StatusOK {
		t.Fatalf("settings POST status=%d body=%s", postRR.Code, postRR.Body.String())
	}
	var payload struct {
		Item probeControllerDoHConfigView `json:"item"`
	}
	if err := json.Unmarshal(postRR.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if payload.Item.EndpointPath == "/dns-query/test-doh-token" || !strings.HasPrefix(payload.Item.EndpointPath, "/dns-query/") || payload.Item.Upstream != "https://doh.pub/dns-query" {
		t.Fatalf("rotated settings=%+v", payload.Item)
	}

	queriesReq := httptest.NewRequest(http.MethodGet, "/mng/api/route/doh/queries", nil)
	queriesRR := httptest.NewRecorder()
	mngRouteDoHQueriesHandler(queriesRR, queriesReq)
	if queriesRR.Code != http.StatusOK || !strings.Contains(queriesRR.Body.String(), "ads.blocked.example") {
		t.Fatalf("queries GET status=%d body=%s", queriesRR.Code, queriesRR.Body.String())
	}
	clearReq := httptest.NewRequest(http.MethodDelete, "/mng/api/route/doh/queries", nil)
	clearRR := httptest.NewRecorder()
	mngRouteDoHQueriesHandler(clearRR, clearReq)
	if clearRR.Code != http.StatusOK || !strings.Contains(clearRR.Body.String(), `"total":0`) {
		t.Fatalf("queries DELETE status=%d body=%s", clearRR.Code, clearRR.Body.String())
	}
}

func TestMngRoutePageIncludesDoHManagementSurface(t *testing.T) {
	for _, marker := range []string{
		`data-tab="doh"`,
		`id="section-doh"`,
		`id="doh-endpoint"`,
		`id="doh-query-body"`,
		`/mng/api/route/doh`,
		`/mng/api/route/doh/queries`,
	} {
		if !strings.Contains(mngRoutePageHTML, marker) {
			t.Fatalf("route page is missing DoH marker %q", marker)
		}
	}
}

func useProbeControllerDoHTestState(t *testing.T, rules []probeVirtualRouterRouteRule) {
	t.Helper()
	oldRouteStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	oldUpstream := probeControllerDoHQueryUpstream
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldRouteStore
		ProbeStore = oldProbeStore
		probeControllerDoHQueryUpstream = oldUpstream
		clearProbeControllerDoHQueryRecords()
		probeControllerDoHRateLimitStore.Lock()
		probeControllerDoHRateLimitStore.items = make(map[string]probeControllerDoHRateLimitEntry)
		probeControllerDoHRateLimitStore.Unlock()
	})
	clearProbeControllerDoHQueryRecords()
	probeControllerDoHRateLimitStore.Lock()
	probeControllerDoHRateLimitStore.items = make(map[string]probeControllerDoHRateLimitEntry)
	probeControllerDoHRateLimitStore.Unlock()
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(t.TempDir(), "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
				RouteRules: rules,
			},
			VirtualRouterFakeIP: defaultProbeVirtualRouterFakeIPLibrary(),
			DoH: probeControllerDoHConfig{
				Enabled:     true,
				Upstream:    probeControllerDoHDefaultUpstream,
				AccessToken: "test-doh-token",
			},
		},
	}
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 1, NodeName: "source"}, {NodeNo: 2, NodeName: "exit"}},
		ProbeSecrets: map[string]string{},
	}}
}

func buildProbeControllerDoHTestQuery(t *testing.T, domain string, queryType dnsmessage.Type) []byte {
	t.Helper()
	name, err := dnsmessage.NewName(domain)
	if err != nil {
		t.Fatalf("new dns name: %v", err)
	}
	message := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 42, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  name,
			Type:  queryType,
			Class: dnsmessage.ClassINET,
		}},
	}
	packet, err := message.Pack()
	if err != nil {
		t.Fatalf("pack dns query: %v", err)
	}
	return packet
}

func serveProbeControllerDoHTestRequest(t *testing.T, method string, path string, packet []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(string(packet)))
	req.TLS = &tls.ConnectionState{}
	req.RemoteAddr = "192.0.2.10:53000"
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/dns-message")
	}
	rr := httptest.NewRecorder()
	ProbeControllerDoHHandler(rr, req)
	return rr
}
