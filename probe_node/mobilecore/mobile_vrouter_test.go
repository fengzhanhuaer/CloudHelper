package mobilecore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"golang.org/x/net/dns/dnsmessage"
)

func resetMobileVRouteVPNStateForTest(t *testing.T, configDir string) {
	t.Helper()
	stopMobileVRouteCarrierWorkers()
	closeMobileVRouteCarriers()
	closeMobileVRouteTrackedFlows("test_reset")
	mobileVRouteRTTState.mu.Lock()
	mobileVRouteRTTState.pending = make(map[string]chan mobileVRouteControlProbePayload)
	mobileVRouteRTTState.mu.Unlock()
	mobileVRouteCarrierState.mu.Lock()
	oldCarrierItems := mobileVRouteCarrierState.items
	oldCarrierLastError := mobileVRouteCarrierState.lastError
	oldCarrierLastErrorUnixNS := mobileVRouteCarrierState.lastErrorUnixNS
	mobileVRouteCarrierState.items = map[string]*mobileVRouteCarrier{}
	mobileVRouteCarrierState.lastError = ""
	mobileVRouteCarrierState.lastErrorUnixNS = 0
	mobileVRouteCarrierState.mu.Unlock()

	vpnRuntime.mu.Lock()
	oldConfigDir := vpnRuntime.configDir
	vpnRuntime.configDir = strings.TrimSpace(configDir)
	vpnRuntime.mu.Unlock()
	setMobileRouteConfigDir(configDir)
	mobileVRouteControllerState.mu.Lock()
	oldControllerBaseURL := mobileVRouteControllerState.baseURL
	oldControllerNodeID := mobileVRouteControllerState.nodeID
	oldControllerNodeSecret := mobileVRouteControllerState.nodeSecret
	mobileVRouteControllerState.baseURL = ""
	mobileVRouteControllerState.nodeID = ""
	mobileVRouteControllerState.nodeSecret = ""
	mobileVRouteControllerState.mu.Unlock()

	vpnDNSState.mu.Lock()
	oldDNSState := *vpnDNSState
	vpnDNSState.nextFakeOffset = 2
	vpnDNSState.fakeDomainToIP = map[string]string{}
	vpnDNSState.fakeIPToEntry = map[string]androidVPNDNSFakeEntry{}
	vpnDNSState.routeIPHints = map[string]androidVPNDNSRouteHintEntry{}
	vpnDNSState.realIPToFake = map[string]androidVPNDNSRealIPFakeEntry{}
	vpnDNSState.fakeFlowToReal = map[string]androidVPNDNSRealIPFakeEntry{}
	vpnDNSState.cacheDir = ""
	vpnDNSState.cacheLoaded = false
	vpnDNSState.cacheDirty = false
	if vpnDNSState.cacheTimer != nil {
		vpnDNSState.cacheTimer.Stop()
		vpnDNSState.cacheTimer = nil
	}
	vpnDNSState.mu.Unlock()

	t.Cleanup(func() {
		stopMobileVRouteCarrierWorkers()
		closeMobileVRouteCarriers()
		mobileVRouteCarrierState.mu.Lock()
		mobileVRouteCarrierState.items = oldCarrierItems
		mobileVRouteCarrierState.lastError = oldCarrierLastError
		mobileVRouteCarrierState.lastErrorUnixNS = oldCarrierLastErrorUnixNS
		mobileVRouteCarrierState.mu.Unlock()

		vpnRuntime.mu.Lock()
		vpnRuntime.configDir = oldConfigDir
		vpnRuntime.mu.Unlock()
		mobileVRouteControllerState.mu.Lock()
		mobileVRouteControllerState.baseURL = oldControllerBaseURL
		mobileVRouteControllerState.nodeID = oldControllerNodeID
		mobileVRouteControllerState.nodeSecret = oldControllerNodeSecret
		mobileVRouteControllerState.mu.Unlock()
		closeMobileVRouteTrackedFlows("test_cleanup")
		vpnDNSState.mu.Lock()
		if vpnDNSState.cacheTimer != nil {
			vpnDNSState.cacheTimer.Stop()
		}
		*vpnDNSState = oldDNSState
		vpnDNSState.mu.Unlock()
	})
}

func serveMobileProbeChallengeForTest(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/probe/auth/challenge" {
		return false
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"challenge": "controller-issued-test-challenge"})
	return true
}

func TestAndroidVPNFakeIPMigratesToControllerAllocation(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMobileProbeChallengeForTest(w, r) {
			return
		}
		if r.URL.Path != mobileVRouteFakeIPResolveAPIPath || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		requestCount++
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request["domain"] != "play.googleapis.com" || request["action"] != "probe_exit" || request["exit_node_id"] != "17" {
			t.Errorf("unexpected controller fake ip request: %+v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"node_id": "15",
			"item": map[string]any{
				"domain":       "play.googleapis.com",
				"fake_ip":      "198.18.4.9",
				"action":       "probe_exit",
				"exit_node_id": "17",
			},
		})
	}))
	defer server.Close()
	setMobileVRouteControllerIdentity(server.URL, "15", "secret-15")

	vpnDNSState.mu.Lock()
	vpnDNSState.cacheLoaded = true
	vpnDNSState.cacheDir = configDir
	vpnDNSState.fakeDomainToIP["play.googleapis.com"] = "198.18.3.188"
	vpnDNSState.fakeIPToEntry["198.18.3.188"] = androidVPNDNSFakeEntry{
		Domain:          "play.googleapis.com",
		Group:           "Google",
		SelectedRouteID: "vroute:17",
		ExpiresAt:       time.Now().Add(time.Minute),
	}
	vpnDNSState.mu.Unlock()

	fakeIP, ok := allocateAndroidVPNDNSFakeIP("play.googleapis.com", androidRouteDecision{
		Group:           "Google",
		SelectedRouteID: "vroute:17",
	})
	if !ok || fakeIP != "198.18.4.9" {
		t.Fatalf("fake ip=%q ok=%t, want controller allocation", fakeIP, ok)
	}
	vpnDNSState.mu.Lock()
	entry := vpnDNSState.fakeIPToEntry[fakeIP]
	_, oldExists := vpnDNSState.fakeIPToEntry["198.18.3.188"]
	vpnDNSState.mu.Unlock()
	if !entry.ControllerManaged || oldExists {
		t.Fatalf("controller entry=%+v old_exists=%t", entry, oldExists)
	}
	if time.Until(entry.ExpiresAt) < 47*time.Hour || time.Until(entry.ExpiresAt) > 49*time.Hour {
		t.Fatalf("controller fake ip ttl=%s, want about 48h entry=%+v", time.Until(entry.ExpiresAt), entry)
	}
	firstExpiresAt := entry.ExpiresAt
	fakeIP, ok = allocateAndroidVPNDNSFakeIP("play.googleapis.com", androidRouteDecision{
		Group:           "Google",
		SelectedRouteID: "vroute:17",
	})
	if !ok || fakeIP != "198.18.4.9" || requestCount != 1 {
		t.Fatalf("cached fake ip=%q ok=%t controller_requests=%d, want cached controller entry", fakeIP, ok, requestCount)
	}
	vpnDNSState.mu.Lock()
	entry = vpnDNSState.fakeIPToEntry[fakeIP]
	vpnDNSState.mu.Unlock()
	if !entry.ExpiresAt.Equal(firstExpiresAt) {
		t.Fatalf("cached fake ip ttl should not slide: first=%s second=%s", firstExpiresAt, entry.ExpiresAt)
	}
}

func TestAndroidVPNDNSCacheDoesNotPersistOrLoadFakeIPs(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	vpnDNSState.cacheDir = configDir
	vpnDNSState.cacheLoaded = true
	vpnDNSState.cacheDirty = true
	vpnDNSState.fakeDomainToIP["api.example.com"] = "198.18.4.9"
	vpnDNSState.fakeIPToEntry["198.18.4.9"] = androidVPNDNSFakeEntry{
		Domain:            "api.example.com",
		Group:             "AI",
		SelectedRouteID:   "vroute:17",
		ControllerManaged: true,
		ExpiresAt:         now.Add(vpnDNSFakeIPTTL),
	}
	vpnDNSState.routeIPHints["203.0.113.10"] = androidVPNDNSRouteHintEntry{
		Domain:    "api.example.com",
		IP:        "203.0.113.10",
		IPv4:      []string{"203.0.113.10"},
		Group:     "AI",
		ExpiresAt: now.Add(vpnDNSCacheTTL),
	}
	vpnDNSState.mu.Unlock()

	persistAndroidVPNDNSCache(configDir)
	path, ok := resolveAndroidVPNDNSCachePath(configDir)
	if !ok {
		t.Fatalf("resolve dns cache path failed")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dns cache failed: %v", err)
	}
	if strings.Contains(string(raw), "fake_ips") || strings.Contains(string(raw), "198.18.4.9") {
		t.Fatalf("fake ip should not be persisted: %s", string(raw))
	}
	if !strings.Contains(string(raw), "route_hints") || !strings.Contains(string(raw), "203.0.113.10") {
		t.Fatalf("route hints should still persist: %s", string(raw))
	}

	rawWithLegacyFake := []byte(`{
  "version": 1,
  "saved_at": "2099-01-01T00:00:00Z",
  "fake_ips": [{
    "ip": "198.18.4.99",
    "domain": "legacy.example.com",
    "group": "legacy",
    "controller_managed": true,
    "expires_at": "2099-01-01T00:00:00Z"
  }],
  "route_hints": [{
    "ip": "203.0.113.11",
    "domain": "hint.example.com",
    "ipv4": ["203.0.113.11"],
    "expires_at": "2099-01-01T00:00:00Z"
  }]
}`)
	if err := os.WriteFile(path, rawWithLegacyFake, 0o644); err != nil {
		t.Fatalf("write legacy dns cache failed: %v", err)
	}
	vpnDNSState.mu.Lock()
	vpnDNSState.fakeDomainToIP = map[string]string{}
	vpnDNSState.fakeIPToEntry = map[string]androidVPNDNSFakeEntry{}
	vpnDNSState.routeIPHints = map[string]androidVPNDNSRouteHintEntry{}
	vpnDNSState.cacheDir = ""
	vpnDNSState.cacheLoaded = false
	vpnDNSState.cacheDirty = false
	vpnDNSState.mu.Unlock()

	ensureAndroidVPNDNSCacheLoaded(configDir)
	vpnDNSState.mu.Lock()
	_, fakeLoaded := vpnDNSState.fakeIPToEntry["198.18.4.99"]
	_, hintLoaded := vpnDNSState.routeIPHints["203.0.113.11"]
	vpnDNSState.mu.Unlock()
	if fakeLoaded {
		t.Fatalf("legacy fake ip should not be loaded from dns cache")
	}
	if !hintLoaded {
		t.Fatalf("route hint should still load from dns cache")
	}
}

func TestMobileVRouteFlowAppearsInConnectionMonitor(t *testing.T) {
	oldConnections := globalandroidRouteConnectionState
	globalandroidRouteConnectionState = newandroidRouteConnectionState()
	t.Cleanup(func() {
		closeMobileVRouteTrackedFlows("test_cleanup")
		globalandroidRouteConnectionState = oldConnections
	})
	packet := buildMobileVRouteTestIPv4Packet(6, "10.111.0.2", "198.18.4.9", 42620, 443)
	route := vpnRouteDecision{TargetAddr: "play.googleapis.com:443", Group: "Google", SelectedRouteID: "vroute:17"}
	plan := mobileVRouteForwardPlan{RouteID: "vrouter-15-19", Path: []string{"15", "19", "17"}}
	trackMobileVRouteOutbound(packet, route, plan)
	snapshot := globalandroidRouteConnectionState.snapshot()
	if snapshot.ActiveCount != 1 || len(snapshot.Active) != 1 || snapshot.Active[0].Scope != "vpn_vroute" {
		t.Fatalf("active snapshot=%+v", snapshot)
	}
	statusPayload := struct {
		Connections androidRouteConnectionSnapshot `json:"connections"`
	}{}
	if err := json.Unmarshal([]byte(VpnStatus()), &statusPayload); err != nil {
		t.Fatalf("decode vpn status: %v", err)
	}
	if statusPayload.Connections.ActiveCount != 1 {
		t.Fatalf("vpn status connections=%+v, want active proxy flow", statusPayload.Connections)
	}
	reply := buildMobileVRouteTestIPv4Packet(6, "198.18.4.9", "10.111.0.2", 443, 42620)
	trackMobileVRouteInbound(reply)
	snapshot = globalandroidRouteConnectionState.snapshot()
	if len(snapshot.Active) != 1 || snapshot.Active[0].BytesUp == 0 || snapshot.Active[0].BytesDown == 0 {
		t.Fatalf("flow bytes not tracked: %+v", snapshot.Active)
	}
	finishMobileVRouteTrackedFlow(mobileVRouteFlowKey(6, "10.111.0.2", 42620, "198.18.4.9", 443), "test_closed")
	snapshot = globalandroidRouteConnectionState.snapshot()
	if snapshot.ActiveCount != 0 || snapshot.CompletedCount != 1 {
		t.Fatalf("completed snapshot=%+v", snapshot)
	}
	trackMobileVRouteOutbound(packet, route, plan)
	failMobileVRouteTrackedFlowsForCarrier(plan, "carrier_write_failed", errors.New("test carrier write failed"))
	snapshot = globalandroidRouteConnectionState.snapshot()
	if snapshot.ActiveCount != 0 || snapshot.CompletedCount != 2 || snapshot.FailureCount != 1 {
		t.Fatalf("failed carrier snapshot=%+v", snapshot)
	}
}

func TestMobileVRouteLocalExitIsDirect(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9"},
		},
		RouteRules: []mobileVRouteRouteRule{{
			ID:         "rr-local",
			Name:       "Local",
			Action:     "probe_exit",
			ExitNodeID: "9",
			Entries:    []string{"domain_suffix:example.com", "cidr:203.0.113.0/24"},
		}},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}

	domainRoute, err := decideVPNRouteForTarget("www.example.com:443")
	if err != nil {
		t.Fatalf("decide local domain route failed: %v", err)
	}
	if !domainRoute.Direct || domainRoute.Reject || domainRoute.SelectedRouteID != "" || domainRoute.TargetAddr != "www.example.com:443" {
		t.Fatalf("unexpected local domain route: %+v", domainRoute)
	}

	ipRoute, err := decideVPNRouteForTarget("203.0.113.8:443")
	if err != nil {
		t.Fatalf("decide local cidr route failed: %v", err)
	}
	if !ipRoute.Direct || ipRoute.SelectedRouteID != "" || ipRoute.TargetAddr != "203.0.113.8:443" {
		t.Fatalf("unexpected local cidr route: %+v", ipRoute)
	}
}

func TestMobileVRouteCIDRRuleSelectsRemoteProbeExit(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9", ServicePort: 12040},
			{NodeID: "17", IP: "198.18.0.17", ServicePort: 12041},
		},
		TopologyRules: []mobileVRouteTopology{{
			ID:              "link-9-17",
			FromNodeID:      "9",
			ToNodeID:        "17",
			ToServiceDomain: "edge-17.example.com",
			ToServicePort:   12041,
			Secret:          "secret-9-17",
			AuthTicket:      "ticket-9-17",
			Enabled:         true,
		}},
		RouteRules: []mobileVRouteRouteRule{{
			ID:         "rr-tg",
			Name:       "Telegram",
			Action:     "probe_exit",
			ExitNodeID: "17",
			Entries:    []string{"cidr:91.108.4.0/22"},
		}},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}

	route, err := decideVPNRouteForTarget("91.108.4.8:443")
	if err != nil {
		t.Fatalf("decide cidr route failed: %v", err)
	}
	if route.Direct || route.Reject || route.SelectedRouteID != "vroute:17" || route.Group != "Telegram" {
		t.Fatalf("unexpected cidr route: %+v", route)
	}

	plan, err := buildMobileVRouteForwardPlan(configDir, route.SelectedRouteID)
	if err != nil {
		t.Fatalf("build cidr forward plan failed: %v", err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	carrier := newMobileVRouteCarrier(mobileVRouteCarrierKey(plan), plan, left)
	if carrier == nil {
		t.Fatal("carrier is nil")
	}
	carrier.markActivity()
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items[carrier.key] = carrier
	mobileVRouteCarrierState.mu.Unlock()
	go carrier.runTXWorker()

	frameCh := make(chan mobileVRouteFrame, 1)
	errCh := make(chan error, 1)
	go func() {
		frame, readErr := readMobileVRouteFrame(bufio.NewReader(right))
		if readErr != nil {
			errCh <- readErr
			return
		}
		frameCh <- frame
	}()

	packet := buildMobileVRouteTestIPv4Packet(6, "10.0.0.2", "91.108.4.8", 12345, 443)
	handled, err := mobileVRouteHandleVPNPacket(configDir, packet, nil)
	if err != nil {
		t.Fatalf("handle cidr vroute packet failed: %v", err)
	}
	if !handled {
		t.Fatalf("cidr vroute packet was not handled")
	}
	select {
	case frame := <-frameCh:
		forwarded, rewriteErr := rewriteAndroidVPNIPv4Packet(packet, "198.18.0.9", "")
		if rewriteErr != nil {
			t.Fatalf("rewrite expected cidr frame: %v", rewriteErr)
		}
		if frame.MainType != mobileVRouteFrameMainTypeIP || frame.SubType != mobileVRouteIPSubTypeIPv4 || !bytes.Equal(frame.Data, forwarded) {
			t.Fatalf("unexpected cidr vroute frame: %+v", frame)
		}
	case readErr := <-errCh:
		t.Fatalf("read cidr vroute frame failed: %v", readErr)
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for cidr vroute frame")
	}
}

func TestMobileVRouteRewritesTUNSourceToLocalVirtualIP(t *testing.T) {
	plan := mobileVRouteForwardPlan{
		LocalNode: "15",
		Config: mobileVRouteConfig{ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "15", IP: "198.18.0.15"},
		}},
	}
	packet := buildMobileVRouteTestIPv4Packet(6, "10.111.0.2", "198.18.4.52", 42794, 443)
	forwarded, tunSourceIP, err := mobileVRouteRewriteTUNPacketForForward(packet, plan)
	if err != nil {
		t.Fatalf("rewrite forward packet: %v", err)
	}
	forwardInfo, ok := parseAndroidVPNIPv4TransportPacket(forwarded)
	if !ok || forwardInfo.SourceIP != "198.18.0.15" || forwardInfo.DestinationIP != "198.18.4.52" {
		t.Fatalf("unexpected forwarded packet: %+v", forwardInfo)
	}
	if tunSourceIP != "10.111.0.2" {
		t.Fatalf("TUN source=%q, want 10.111.0.2", tunSourceIP)
	}

	reply := buildMobileVRouteTestIPv4Packet(6, "198.18.4.52", "198.18.0.15", 443, 42794)
	restored, err := mobileVRouteRestoreTUNPacketFromReply(reply, tunSourceIP)
	if err != nil {
		t.Fatalf("restore reply packet: %v", err)
	}
	restoredInfo, ok := parseAndroidVPNIPv4TransportPacket(restored)
	if !ok || restoredInfo.SourceIP != "198.18.4.52" || restoredInfo.DestinationIP != "10.111.0.2" {
		t.Fatalf("unexpected restored packet: %+v", restoredInfo)
	}
	if got := binary.BigEndian.Uint16(forwarded[10:12]); got == 0 {
		t.Fatal("forwarded IPv4 checksum was not set")
	}
	if got := binary.BigEndian.Uint16(restored[10:12]); got == 0 {
		t.Fatal("restored IPv4 checksum was not set")
	}
}

func TestMobileVRouteRTTMillisecondsUsesFullRTT(t *testing.T) {
	if got := mobileVRouteRTTMilliseconds(558 * time.Millisecond); got != 558 {
		t.Fatalf("latency=%d, want 558", got)
	}
	if got := mobileVRouteRTTMilliseconds(time.Millisecond); got != 1 {
		t.Fatalf("minimum latency=%d, want 1", got)
	}
}

func TestMobileVRouteHandleStaleFakeIPLocalExitFallsThrough(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9"},
		},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}
	vpnDNSState.mu.Lock()
	vpnDNSState.cacheLoaded = true
	vpnDNSState.fakeIPToEntry["198.18.4.5"] = androidVPNDNSFakeEntry{
		Domain:          "www.example.com",
		Group:           "Local",
		SelectedRouteID: "vroute:9",
		ExpiresAt:       time.Now().Add(time.Hour),
	}
	vpnDNSState.mu.Unlock()

	handled, err := mobileVRouteHandleVPNPacket(configDir, buildMobileVRouteTestIPv4Packet(6, "10.0.0.2", "198.18.4.5", 12345, 443), nil)
	if err != nil {
		t.Fatalf("handle stale fake ip failed: %v", err)
	}
	if handled {
		t.Fatalf("stale fake ip for local exit was handled by vroute carrier, want gvisor/direct fallthrough")
	}
}

func TestMobileVRouteSNIWarmsFakeIPButDoesNotRouteRealIP(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9", ServicePort: 12040},
			{NodeID: "17", IP: "198.18.0.17", ServicePort: 12041},
		},
		TopologyRules: []mobileVRouteTopology{{
			ID:              "link-9-17",
			FromNodeID:      "9",
			ToNodeID:        "17",
			ToServiceDomain: "edge-17.example.com",
			ToServicePort:   12041,
			Secret:          "secret-9-17",
			AuthTicket:      "ticket-9-17",
			Enabled:         true,
		}},
		RouteRules: []mobileVRouteRouteRule{{
			ID:         "rr-google",
			Name:       "Google",
			Action:     "probe_exit",
			ExitNodeID: "17",
			Entries:    []string{"domain_suffix:googleapis.com"},
		}},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}

	route, err := decideVPNRouteForTarget("play.googleapis.com:443")
	if err != nil {
		t.Fatalf("decide sni route failed: %v", err)
	}
	if route.Direct || route.Reject || route.SelectedRouteID != "vroute:17" {
		t.Fatalf("unexpected sni route: %+v", route)
	}
	rememberAndroidVPNSNIFakeIP("216.239.38.223:443", "play.googleapis.com:443", route)

	vpnDNSState.mu.Lock()
	fakeIP := vpnDNSState.fakeDomainToIP["play.googleapis.com"]
	vpnDNSState.mu.Unlock()
	if fakeIP == "" || net.ParseIP(fakeIP).To4() == nil {
		t.Fatalf("fake ip was not warmed for play.googleapis.com: %q", fakeIP)
	}
	fakeRoute, err := decideVPNRouteForTarget(net.JoinHostPort(fakeIP, "443"))
	if err != nil {
		t.Fatalf("decide warmed fake-ip route failed: %v", err)
	}
	if fakeRoute.Direct || fakeRoute.Reject || fakeRoute.SelectedRouteID != "vroute:17" || fakeRoute.TargetAddr != "play.googleapis.com:443" {
		t.Fatalf("unexpected warmed fake-ip route: %+v", fakeRoute)
	}

	realRoute, err := decideVPNRouteForTarget("216.239.38.223:443")
	if err != nil {
		t.Fatalf("decide real-ip route failed: %v", err)
	}
	if !realRoute.Direct || realRoute.SelectedRouteID != "" {
		t.Fatalf("real ip must not be routed through vroute: %+v", realRoute)
	}
	handled, err := mobileVRouteHandleVPNPacket(configDir, buildMobileVRouteTestIPv4Packet(6, "10.0.0.2", "216.239.38.223", 12345, 443), nil)
	if err != nil {
		t.Fatalf("handle real-ip packet failed: %v", err)
	}
	if handled {
		t.Fatalf("real-ip packet was handled by vroute carrier; vroute must only carry fake/virtual IPs")
	}
}

func TestMobileVRouteCarrierWriteFailureClosesAndRecordsError(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	left, right := net.Pipe()
	_ = right.Close()
	plan := mobileVRouteForwardPlan{
		RouteID:    "vrouter-test",
		Path:       []string{"9", "17"},
		NextNode:   "17",
		ExitNode:   "17",
		RelayHost:  "edge.example.com",
		RelayPort:  12040,
		BridgeRole: mobileVRouteBridgeRoleToNext,
		Layer:      "websocket",
	}
	carrier := newMobileVRouteCarrier("test-carrier", plan, left)
	if carrier == nil {
		t.Fatal("carrier is nil")
	}
	carrier.markActivity()
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items[carrier.key] = carrier
	mobileVRouteCarrierState.mu.Unlock()
	go carrier.runTXWorker()

	if err := carrier.writeIPPacket(buildMobileVRouteTestIPv4Packet(6, "10.0.0.2", "198.18.0.17", 12345, 443), []string{"9", "17"}); err != nil {
		t.Fatalf("enqueue write failed: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := snapshotMobileVRouteCarriers()
		if status["active"] == 0 && strings.TrimSpace(stringFromAny(status["last_error"])) != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := snapshotMobileVRouteCarriers()
	if status["active"] != 0 {
		t.Fatalf("active carriers=%v, want 0", status["active"])
	}
	if strings.TrimSpace(stringFromAny(status["last_error"])) == "" {
		t.Fatalf("last_error is empty after write failure: %+v", status)
	}
}

func TestMobileVRouteRuntimeStatusDeclaresMobileCapabilityBoundary(t *testing.T) {
	status := mobileVRouteRuntimeStatusPayload(t.TempDir())
	capabilities, ok := status["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing from status: %+v", status)
	}
	if capabilities["ip_frame"] != true || capabilities["websocket_carrier"] != true || capabilities["vpn_tun_writeback"] != true {
		t.Fatalf("required mobile vroute capabilities missing: %+v", capabilities)
	}
	if capabilities["websocket_h3"] != true || capabilities["inbound_listener"] != false || capabilities["reverse_first_hop"] != true || capabilities["relay_forwarding"] != true || capabilities["control_ping"] != true || capabilities["path_rtt"] != true {
		t.Fatalf("unexpected mobile vroute capabilities: %+v", capabilities)
	}
	if capabilities["debug_log_pull"] != true {
		t.Fatalf("mobile peer debug log pull capability missing: %+v", capabilities)
	}
}

func TestMobileVRouteStatusPayloadIncludesExitNodeEndpoint(t *testing.T) {
	configDir := t.TempDir()
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9", ServicePort: 12040},
			{NodeID: "17", IP: "198.18.0.17", ServicePort: 12041},
		},
		RouteRules: []mobileVRouteRouteRule{{
			ID:         "rr-tg",
			Name:       "Telegram",
			Action:     "probe_exit",
			ExitNodeID: "17",
			Entries:    []string{"domain_suffix:telegram.org"},
		}},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}

	status := mobileVRouteStatusPayload(configDir)
	items, ok := status["exit_node_items"].([]map[string]any)
	if !ok || len(items) != 1 {
		t.Fatalf("exit_node_items = %#v, want one item", status["exit_node_items"])
	}
	if items[0]["node_id"] != "17" || items[0]["ip"] != "198.18.0.17" || items[0]["service_port"] != 12041 {
		t.Fatalf("unexpected exit node item: %#v", items[0])
	}
}

func TestMobileVRouteForwardPlanBuildsAdjacentCarrier(t *testing.T) {
	configDir := t.TempDir()
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9", ServicePort: 12040},
			{NodeID: "12", IP: "198.18.0.12", ServicePort: 12041},
			{NodeID: "17", IP: "198.18.0.17", ServicePort: 12042},
		},
		TopologyRules: []mobileVRouteTopology{
			{
				ID:              "link-9-12",
				FromNodeID:      "9",
				ToNodeID:        "12",
				ToServiceDomain: "api_copilot_edge.example.com",
				ToServicePort:   12040,
				RouteLayer:      "h2",
				Secret:          "secret-9-12",
				AuthTicket:      "ticket-9-12",
				Enabled:         true,
			},
			{
				ID:              "link-12-17",
				FromNodeID:      "12",
				ToNodeID:        "17",
				ToServiceDomain: "edge-17.example.com",
				ToServicePort:   12042,
				Secret:          "secret-12-17",
				AuthTicket:      "ticket-12-17",
				Enabled:         true,
			},
		},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}

	plan, err := buildMobileVRouteForwardPlan(configDir, "vroute:17")
	if err != nil {
		t.Fatalf("build forward plan failed: %v", err)
	}
	if got, want := strings.Join(plan.Path, ">"), "9>12>17"; got != want {
		t.Fatalf("path=%s, want %s", got, want)
	}
	if plan.NextNode != "12" || plan.ExitNode != "17" || plan.BridgeRole != mobileVRouteBridgeRoleToNext {
		t.Fatalf("unexpected plan endpoints: %+v", plan)
	}
	if got, want := plan.RouteID, mobileVRouteRuntimeRouteID(mobileVRouteTopology{ID: "link-9-12"}); got != want {
		t.Fatalf("route id=%s, want %s", got, want)
	}
	if plan.RelayHost != "api_copilot_edge.example.com" || plan.RelayPort != 443 {
		t.Fatalf("relay=%s:%d, want cloudflare host on 443", plan.RelayHost, plan.RelayPort)
	}
	if plan.Layer != "websocket" {
		t.Fatalf("layer=%s, want websocket", plan.Layer)
	}
}

func TestMobileVRouteOutboundCarrierPlansIncludeForwardAndReverseRules(t *testing.T) {
	plans := mobileVRouteOutboundCarrierPlans(mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9"},
			{NodeID: "17", IP: "198.18.0.17", ServicePort: 12040},
			{NodeID: "19", IP: "198.18.0.19", ServicePort: 12041},
		},
		TopologyRules: []mobileVRouteTopology{
			{ID: "forward", FromNodeID: "9", ToNodeID: "17", ToServiceDomain: "edge-17.example.com", Secret: "secret", AuthTicket: "ticket", Enabled: true},
			{ID: "inbound", FromNodeID: "19", ToNodeID: "9", FromServiceDomain: "edge-19.example.com", Secret: "secret", AuthTicket: "ticket", Enabled: true},
		},
	})
	if len(plans) != 2 {
		t.Fatalf("outbound plans=%d, want 2: %+v", len(plans), plans)
	}
	byNextNode := map[string]mobileVRouteForwardPlan{}
	for _, plan := range plans {
		byNextNode[plan.NextNode] = plan
	}
	if plan := byNextNode["17"]; plan.RelayHost != "edge-17.example.com" || plan.RelayPort != 12040 || plan.BridgeRole != mobileVRouteBridgeRoleToNext || plan.Layer != "websocket" {
		t.Fatalf("unexpected forward plan: %+v", plan)
	}
	if plan := byNextNode["19"]; plan.RelayHost != "edge-19.example.com" || plan.RelayPort != 12041 || plan.BridgeRole != mobileVRouteBridgeRoleToPrev || plan.Layer != "websocket" {
		t.Fatalf("unexpected reverse plan: %+v", plan)
	}
}

func TestMobileVRouteCarrierWorkerRetriesFailedDial(t *testing.T) {
	resetMobileVRouteVPNStateForTest(t, t.TempDir())
	oldDial := mobileVRouteCarrierDial
	oldRetryMin := mobileVRouteCarrierRetryMin
	mobileVRouteCarrierRetryMin = 5 * time.Millisecond
	t.Cleanup(func() {
		stopMobileVRouteCarrierWorkers()
		closeMobileVRouteCarriers()
		mobileVRouteCarrierDial = oldDial
		mobileVRouteCarrierRetryMin = oldRetryMin
	})

	attempts := make(chan int, 2)
	peerConns := make(chan net.Conn, 1)
	callCount := 0
	mobileVRouteCarrierDial = func(mobileVRouteForwardPlan) (net.Conn, error) {
		callCount++
		attempts <- callCount
		if callCount == 1 {
			return nil, errors.New("temporary dial failure")
		}
		left, right := net.Pipe()
		peerConns <- right
		return left, nil
	}

	config := mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9"},
			{NodeID: "17", IP: "198.18.0.17", ServicePort: 12040},
		},
		TopologyRules: []mobileVRouteTopology{{
			ID:              "worker-retry",
			FromNodeID:      "9",
			ToNodeID:        "17",
			ToServiceDomain: "edge-17.example.com",
			Secret:          "secret",
			AuthTicket:      "ticket",
			Enabled:         true,
		}},
	}
	startMobileVRouteCarrierWorkers(config)
	for want := 1; want <= 2; want++ {
		select {
		case got := <-attempts:
			if got != want {
				t.Fatalf("dial attempt=%d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for dial attempt %d", want)
		}
	}
	peer := <-peerConns
	defer peer.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if active := snapshotMobileVRouteCarriers()["active"]; active == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("active carriers=%v, want 1", snapshotMobileVRouteCarriers()["active"])
}

func TestMobileVRouteRelayDialCandidatesPreferIPv4(t *testing.T) {
	oldLookup := mobileVRouteLookupIP
	lookups := map[string]bool{}
	var lookupsMu sync.Mutex
	mobileVRouteLookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) {
		if host != "edge.example.com" {
			t.Fatalf("lookup network=%q host=%q", network, host)
		}
		lookupsMu.Lock()
		lookups[network] = true
		lookupsMu.Unlock()
		switch network {
		case "ip4":
			return []net.IP{net.ParseIP("192.0.2.17")}, nil
		case "ip6":
			return []net.IP{net.ParseIP("2001:db8::17")}, nil
		default:
			t.Fatalf("unexpected lookup network=%q", network)
			return nil, nil
		}
	}
	t.Cleanup(func() {
		mobileVRouteLookupIP = oldLookup
	})

	candidates, err := mobileVRouteRelayDialCandidates("edge.example.com")
	if err != nil {
		t.Fatalf("mobileVRouteRelayDialCandidates returned error: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates=%+v, want 2", candidates)
	}
	lookupsMu.Lock()
	defer lookupsMu.Unlock()
	if !lookups["ip4"] || !lookups["ip6"] {
		t.Fatalf("lookups=%+v, want both ip4 and ip6", lookups)
	}
	if candidates[0].URLHost != "192.0.2.17" || candidates[0].DialHost != "192.0.2.17" || candidates[0].Network != "tcp4" {
		t.Fatalf("first candidate should use ipv4 directly: %+v", candidates[0])
	}
	if candidates[1].URLHost != "2001:db8::17" || candidates[1].DialHost != "2001:db8::17" || candidates[1].Network != "tcp6" {
		t.Fatalf("second candidate should use ipv6 directly: %+v", candidates[1])
	}
}

func TestMobileVRouteRelayDialCandidatesPreserveCloudflareDomain(t *testing.T) {
	lookupCalled := false
	oldLookup := mobileVRouteLookupIP
	mobileVRouteLookupIP = func(context.Context, string, string) ([]net.IP, error) {
		lookupCalled = true
		return []net.IP{net.ParseIP("192.0.2.17")}, nil
	}
	t.Cleanup(func() {
		mobileVRouteLookupIP = oldLookup
	})

	host := "api_copilot_nw.example.com"
	candidates, err := mobileVRouteRelayDialCandidates(host)
	if err != nil {
		t.Fatalf("mobileVRouteRelayDialCandidates returned error: %v", err)
	}
	if lookupCalled {
		t.Fatalf("cloudflare copilot domain should not be resolved")
	}
	if len(candidates) != 1 || candidates[0].URLHost != host || candidates[0].DialHost != host || candidates[0].Network != "tcp" {
		t.Fatalf("cloudflare candidates=%+v", candidates)
	}
}

func TestMobileVRouteRelayDialCandidatesReturnResolveError(t *testing.T) {
	oldLookup := mobileVRouteLookupIP
	mobileVRouteLookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return nil, errors.New("dns unavailable")
	}
	t.Cleanup(func() {
		mobileVRouteLookupIP = oldLookup
	})

	if _, err := mobileVRouteRelayDialCandidates("edge.example.com"); err == nil || !strings.Contains(err.Error(), "resolve vroute relay host failed") {
		t.Fatalf("resolve error=%v", err)
	}
}

func TestMobileVRouteStatusUsesDefaultConfigDir(t *testing.T) {
	oldConfigDir := mobileRouteConfigDir()
	setMobileRouteConfigDir("")
	t.Cleanup(func() {
		setMobileRouteConfigDir(oldConfigDir)
	})
	t.Setenv(mobileDefaultConfigDirEnv, t.TempDir())

	status := mobileVRouteStatusPayload("")
	if status["error"] != nil {
		t.Fatalf("empty status should not expose config dir error: %+v", status)
	}
	if status["enabled"] != false || status["status"] != "not_loaded" {
		t.Fatalf("empty status=%+v, want not_loaded disabled", status)
	}
	if err := persistMobileVRouteConfig("", mobileVRouteConfig{Enabled: true, LocalNodeID: "9"}); err != nil {
		t.Fatalf("persist with default config dir failed: %v", err)
	}
	status = mobileVRouteStatusPayload("")
	if status["enabled"] != true || status["local_node_id"] != "9" {
		t.Fatalf("status from default config dir=%+v", status)
	}
}

func TestMobileVRouteExistingCarrierAcceptsVPNWriteBack(t *testing.T) {
	resetMobileVRouteVPNStateForTest(t, t.TempDir())

	plan := mobileVRouteForwardPlan{RouteID: "vrouter-prewarmed", RelayHost: "edge.example.com", RelayPort: 12040, BridgeRole: mobileVRouteBridgeRoleToNext}
	carrier := &mobileVRouteCarrier{key: mobileVRouteCarrierKey(plan), plan: plan}
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items[carrier.key] = carrier
	mobileVRouteCarrierState.mu.Unlock()
	called := false
	writeBack := func([]byte) error {
		called = true
		return nil
	}
	got, err := ensureMobileVRouteCarrier(plan, writeBack)
	if err != nil || got != carrier {
		t.Fatalf("ensure prewarmed carrier got=%p err=%v", got, err)
	}
	callback := carrier.currentWriteBack()
	if callback == nil {
		t.Fatal("prewarmed carrier should retain later VPN writeback")
	}
	if err := callback([]byte{0x45}); err != nil || !called {
		t.Fatalf("writeback err=%v called=%v", err, called)
	}
}

func TestMobileVRouteCarrierSeparatesControlIPAndTUNWritebackStats(t *testing.T) {
	resetMobileVRouteVPNStateForTest(t, t.TempDir())

	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	plan := mobileVRouteForwardPlan{
		LocalNode:  "9",
		RouteID:    "vrouter-stats",
		Path:       []string{"9", "17"},
		NextNode:   "17",
		ExitNode:   "17",
		RelayHost:  "edge.example.com",
		RelayPort:  12040,
		BridgeRole: mobileVRouteBridgeRoleToNext,
		Layer:      "websocket",
	}
	carrier := newMobileVRouteCarrier(mobileVRouteCarrierKey(plan), plan, left)
	if carrier == nil {
		t.Fatal("carrier is nil")
	}
	defer carrier.close()
	wroteBack := make(chan []byte, 1)
	carrier.setWriteBack(func(packet []byte) error {
		wroteBack <- append([]byte(nil), packet...)
		return nil
	})
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items[carrier.key] = carrier
	mobileVRouteCarrierState.mu.Unlock()
	go carrier.runRXWorker()

	control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: []string{"17", "9"}})
	if err != nil {
		t.Fatalf("marshal control: %v", err)
	}
	responsePayload, err := json.Marshal(mobileVRouteControlProbePayload{RequestID: "stats-rtt"})
	if err != nil {
		t.Fatalf("marshal rtt response: %v", err)
	}
	if err := carrier.handleIncomingFrame(mobileVRouteFrame{
		MainType: mobileVRouteFrameMainTypePathRTT,
		SubType:  mobileVRoutePathRTTSubTypeResponse,
		Control:  control,
		Data:     responsePayload,
	}); err != nil {
		t.Fatalf("handle control frame: %v", err)
	}

	packet := buildMobileVRouteTestIPv4Packet(6, "198.18.0.17", "10.111.0.2", 443, 12345)
	if err := carrier.handleIncomingFrame(mobileVRouteFrame{
		MainType: mobileVRouteFrameMainTypeIP,
		SubType:  mobileVRouteIPSubTypeIPv4,
		Control:  control,
		Data:     packet,
	}); err != nil {
		t.Fatalf("handle ip frame: %v", err)
	}

	select {
	case got := <-wroteBack:
		if !bytes.Equal(got, packet) {
			t.Fatalf("writeback packet mismatch")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for TUN writeback")
	}

	status := snapshotMobileVRouteCarriers()
	items, ok := status["items"].([]map[string]any)
	if !ok || len(items) != 1 {
		t.Fatalf("carrier snapshot items=%T %+v", status["items"], status["items"])
	}
	item := items[0]
	for key, want := range map[string]int64{
		"rx_frames":         2,
		"rx_control_frames": 1,
		"rx_ip_frames":      1,
		"tun_write_frames":  1,
	} {
		if got, _ := item[key].(int64); got != want {
			t.Fatalf("%s=%v, want %d; item=%+v", key, item[key], want, item)
		}
	}
	if got, _ := item["rx_ip_bytes"].(int64); got != int64(len(packet)) {
		t.Fatalf("rx_ip_bytes=%v, want %d", item["rx_ip_bytes"], len(packet))
	}
	if got, _ := item["tun_write_bytes"].(int64); got != int64(len(packet)) {
		t.Fatalf("tun_write_bytes=%v, want %d", item["tun_write_bytes"], len(packet))
	}
}

func TestMobileVRouteCarrierReservesControlQueueAndPrioritizesIt(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	carrier := newMobileVRouteCarrier("queue-priority", mobileVRouteForwardPlan{RouteID: "vrouter-mobile-queue"}, left)
	if carrier == nil {
		t.Fatal("carrier is nil")
	}
	ipFrame := mobileVRouteFrame{MainType: mobileVRouteFrameMainTypeIP, SubType: mobileVRouteIPSubTypeIPv4, Data: []byte{0x45}}
	for i := 0; i < cap(carrier.tx); i++ {
		carrier.tx <- ipFrame
	}
	controlFrame := mobileVRouteFrame{MainType: mobileVRouteFrameMainTypePingPong, SubType: mobileVRoutePingPongSubTypePing, Data: []byte{1}}
	if err := carrier.enqueueFrame(controlFrame); err != nil {
		t.Fatalf("control enqueue should use reserved queue: %v", err)
	}
	frame, ok := carrier.nextTXFrame()
	if !ok || frame.MainType != mobileVRouteFrameMainTypePingPong {
		t.Fatalf("first frame=%+v ok=%v, want control", frame, ok)
	}
}

func TestMobileVRouteCarrierTXWorkerBatchesQueuedFrames(t *testing.T) {
	left, right := net.Pipe()
	countedLeft := &mobileVRouteWriteCountingConn{Conn: left}
	defer right.Close()
	carrier := newMobileVRouteCarrier("batched-writes", mobileVRouteForwardPlan{RouteID: "vrouter-mobile-batched"}, countedLeft)
	if carrier == nil {
		t.Fatal("carrier is nil")
	}
	defer carrier.close()
	for _, marker := range []byte{0x01, 0x02} {
		frame := mobileVRouteFrame{
			MainType: mobileVRouteFrameMainTypeIP,
			SubType:  mobileVRouteIPSubTypeIPv4,
			Data:     []byte{0x45, 0x00, 0x00, 0x14, marker},
		}
		if err := carrier.enqueueFrame(frame); err != nil {
			t.Fatalf("enqueue frame failed: %v", err)
		}
	}

	type result struct {
		frames []mobileVRouteFrame
		err    error
	}
	done := make(chan result, 1)
	go func() {
		reader := bufio.NewReader(right)
		frames := make([]mobileVRouteFrame, 0, 2)
		for range 2 {
			frame, err := readMobileVRouteFrame(reader)
			if err != nil {
				done <- result{err: err}
				return
			}
			frames = append(frames, frame)
		}
		done <- result{frames: frames}
	}()
	go carrier.runTXWorker()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read batched frames failed: %v", got.err)
		}
		if len(got.frames) != 2 || got.frames[0].Data[4] != 0x01 || got.frames[1].Data[4] != 0x02 {
			t.Fatalf("batched frames=%+v", got.frames)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for batched frames")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && carrier.txFrames.Load() != 2 {
		time.Sleep(time.Millisecond)
	}
	if writes := countedLeft.WriteCalls(); writes != 1 {
		t.Fatalf("carrier write calls=%d, want 1 for two queued frames", writes)
	}
	if got := carrier.txBatchFrames.Load(); got != 2 {
		t.Fatalf("last batch frames=%d, want 2", got)
	}
	if got := carrier.txFrames.Load(); got != 2 {
		t.Fatalf("tx frames=%d, want 2", got)
	}
}

func TestMobileVRouteCarrierTXCoalesceWaitsForNextFrame(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	carrier := newMobileVRouteCarrier("coalesce-wait", mobileVRouteForwardPlan{}, left)
	if carrier == nil {
		t.Fatal("carrier is nil")
	}
	want := mobileVRouteFrame{MainType: mobileVRouteFrameMainTypeIP, Data: []byte{0x45}}
	go func() {
		time.Sleep(10 * time.Millisecond)
		carrier.tx <- want
	}()
	got, ok := carrier.waitNextTXFrameUntil(time.Now().Add(time.Second))
	if !ok || got.MainType != want.MainType || !bytes.Equal(got.Data, want.Data) {
		t.Fatalf("coalesced frame=%+v ok=%v, want %+v", got, ok, want)
	}
}

type mobileVRouteWriteCountingConn struct {
	net.Conn
	mu         sync.Mutex
	writeCalls int
}

func (c *mobileVRouteWriteCountingConn) Write(payload []byte) (int, error) {
	c.mu.Lock()
	c.writeCalls++
	c.mu.Unlock()
	return c.Conn.Write(payload)
}

func (c *mobileVRouteWriteCountingConn) WriteCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeCalls
}

func TestMobileVRouteCarrierBuffersAreBounded(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	carrier := newMobileVRouteCarrier("bounded-buffers", mobileVRouteForwardPlan{}, left)
	if carrier == nil {
		t.Fatal("carrier is nil")
	}
	if cap(carrier.tx) != mobileVRouteCarrierTXBufferFrames || cap(carrier.txControl) != mobileVRouteCarrierTXControlBufferFrames || cap(carrier.rx) != mobileVRouteCarrierRXBufferFrames {
		t.Fatalf("unexpected queue capacities: ip=%d control=%d rx=%d", cap(carrier.tx), cap(carrier.txControl), cap(carrier.rx))
	}
	quicConfig := mobileVRouteQUICConfig()
	if quicConfig.MaxStreamReceiveWindow > 4*1024*1024 || quicConfig.MaxConnectionReceiveWindow > 8*1024*1024 {
		t.Fatalf("mobile h3 receive windows are too large: stream=%d connection=%d", quicConfig.MaxStreamReceiveWindow, quicConfig.MaxConnectionReceiveWindow)
	}
}

func TestMobileVRouteRelayReportMatchesProbeNodeStatusShape(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	rule := mobileVRouteTopology{
		ID:              "link-9-17",
		Name:            "mobile-link",
		FromNodeID:      "9",
		ToNodeID:        "17",
		ToServiceDomain: "edge-17.example.com",
		ToServicePort:   12041,
		RouteLayer:      "auto",
		Secret:          "secret-9-17",
		AuthTicket:      "ticket-9-17",
		Enabled:         true,
	}
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9", ServicePort: 12040},
			{NodeID: "17", IP: "198.18.0.17", ServicePort: 12041},
		},
		TopologyRules: []mobileVRouteTopology{rule},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}
	plan, err := buildMobileVRouteForwardPlan(configDir, "vroute:17")
	if err != nil {
		t.Fatalf("build forward plan failed: %v", err)
	}
	carrier := &mobileVRouteCarrier{
		key:           mobileVRouteCarrierKey(plan),
		plan:          plan,
		createdUnixNS: time.Now().Add(-2 * time.Second).UnixNano(),
	}
	carrier.markActivity()
	carrier.txFrames.Store(3)
	carrier.txBytes.Store(300)
	carrier.rxFrames.Store(2)
	carrier.rxBytes.Store(200)
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items[carrier.key] = carrier
	mobileVRouteCarrierState.mu.Unlock()

	reports := snapshotMobileVRouteRelayReports(configDir)
	if len(reports) != 1 {
		t.Fatalf("relay reports=%d, want 1: %+v", len(reports), reports)
	}
	report := reports[0]
	if report.RouteID != mobileVRouteRuntimeRouteID(rule) || report.RouteType != "virtual_router" || report.Role != "virtual_router" {
		t.Fatalf("unexpected relay report identity: %+v", report)
	}
	if report.NextNodeID != "17" || report.NextHost != "edge-17.example.com" || report.NextPort != 12041 || report.NextDialMode != "forward" {
		t.Fatalf("unexpected relay next endpoint: %+v", report)
	}
	if report.NextState == nil || report.NextState.SelectedProtocol != "websocket" {
		t.Fatalf("next state missing selected protocol: %+v", report.NextState)
	}
	if report.VirtualRouter == nil || report.VirtualRouter.FramesSent != 3 || report.VirtualRouter.FramesReceived != 2 {
		t.Fatalf("virtual router stats not included: %+v", report.VirtualRouter)
	}
	if report.BridgeStatus == nil || len(report.BridgeStatus.Sessions) != 1 {
		t.Fatalf("bridge status not included: %+v", report.BridgeStatus)
	}
	raw, err := json.Marshal(reportPayload{Type: "report", NodeID: "9", RelayStatus: reports})
	if err != nil {
		t.Fatalf("marshal report payload failed: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"relay_status"`)) || !bytes.Contains(raw, []byte(`"virtual_router"`)) {
		t.Fatalf("report payload does not match probe node status shape: %s", string(raw))
	}
}

func TestMobileVRouteTopologyKeepsAuthIdentityFields(t *testing.T) {
	config := sanitizeMobileVRouteConfig(mobileVRouteConfig{
		Enabled: true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9"},
			{NodeID: "17", IP: "198.18.0.17"},
		},
		TopologyRules: []mobileVRouteTopology{{
			ID:            "link-9-17",
			FromNodeID:    "9",
			ToNodeID:      "17",
			UserID:        " admin ",
			UserPublicKey: " public-key ",
			Secret:        " secret ",
			AuthTicket:    " ticket ",
			Enabled:       true,
		}},
	})
	if len(config.TopologyRules) != 1 {
		t.Fatalf("topology rules=%d, want 1", len(config.TopologyRules))
	}
	rule := config.TopologyRules[0]
	if rule.UserID != "admin" || rule.UserPublicKey != "public-key" || rule.Secret != "secret" || rule.AuthTicket != "ticket" {
		t.Fatalf("auth fields not sanitized/preserved: %+v", rule)
	}
}

func TestMobileVRouteForwardPlanRequiresAuthTicket(t *testing.T) {
	configDir := t.TempDir()
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9"},
			{NodeID: "17", IP: "198.18.0.17"},
		},
		TopologyRules: []mobileVRouteTopology{{
			ID:              "link-9-17",
			FromNodeID:      "9",
			ToNodeID:        "17",
			ToServiceDomain: "edge-17.example.com",
			Secret:          "secret-9-17",
			Enabled:         true,
		}},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}

	if _, err := buildMobileVRouteForwardPlan(configDir, "vroute:17"); err == nil || !strings.Contains(err.Error(), "auth ticket missing") {
		t.Fatalf("build forward plan err=%v, want auth ticket missing", err)
	}
}

func TestMobileVRouteExplicitHTTP3IsNotSilentlyDowngraded(t *testing.T) {
	if got := normalizeMobileVRouteRelayLayer("http3"); got != "websocket-h3" {
		t.Fatalf("http3 normalized to %q, want websocket-h3", got)
	}
	oldDial := mobileVRouteH3QUICDial
	defer func() { mobileVRouteH3QUICDial = oldDial }()
	called := false
	mobileVRouteH3QUICDial = func(context.Context, string, *tls.Config, *quic.Config) (*quic.Conn, error) {
		called = true
		return nil, errors.New("h3 dial hook")
	}
	plan := mobileVRouteForwardPlan{
		LocalNode: "9",
		NextNode:  "17",
		Layer:     "websocket-h3",
		RouteID:   "vrouter-h3",
		RelayHost: "203.0.113.17",
		RelayPort: 12040,
		Rule:      mobileVRouteTopology{FromNodeID: "9", ToNodeID: "17", Secret: "secret", AuthTicket: "ticket", ToTLSSPKISHA256: strings.Repeat("ab", 32)},
	}
	if _, err := dialMobileVRouteCarrier(plan); err == nil || !strings.Contains(err.Error(), "h3 dial hook") {
		t.Fatalf("dial h3 err=%v, want hook error", err)
	}
	if !called {
		t.Fatalf("h3 dial hook was not called")
	}
}

func TestMobileVRouteRelayTLSOmitsSNIAndDoesNotUseNodeCertificatePin(t *testing.T) {
	config, err := newMobileVRouteRelayTLSConfig(
		mobileVRouteForwardPlan{RouteID: "vrouter-mobile-no-pin"},
		mobileVRouteRelayDialCandidate{URLHost: "203.0.113.17", DialHost: "203.0.113.17"},
		tls.VersionTLS12,
		nil,
	)
	if err != nil {
		t.Fatalf("build mobile relay tls config: %v", err)
	}
	if config.ServerName != "" || !config.InsecureSkipVerify || config.VerifyConnection != nil {
		t.Fatalf("ordinary mobile relay must omit sni and leave peer authentication to route auth: %+v", config)
	}
}

func TestMobileVRouteForwardPlanBuildsReverseAdjacentCarrier(t *testing.T) {
	configDir := t.TempDir()
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		LocalNodeID: "12",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9", ServicePort: 13000},
			{NodeID: "12", IP: "198.18.0.12", ServicePort: 12041},
		},
		TopologyRules: []mobileVRouteTopology{{
			ID:                "link-9-12",
			FromNodeID:        "9",
			ToNodeID:          "12",
			FromServiceDomain: "edge-9.example.com",
			FromServicePort:   12040,
			ToServiceDomain:   "edge-12.example.com",
			ToServicePort:     12041,
			Secret:            "secret-9-12",
			AuthTicket:        "ticket-9-12",
			Enabled:           true,
		}},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}

	plan, err := buildMobileVRouteForwardPlan(configDir, "vroute:9")
	if err != nil {
		t.Fatalf("build reverse forward plan: %v", err)
	}
	if plan.NextNode != "9" || plan.RelayHost != "edge-9.example.com" || plan.RelayPort != 13000 || plan.BridgeRole != mobileVRouteBridgeRoleToPrev {
		t.Fatalf("unexpected reverse forward plan: %+v", plan)
	}
}

func TestMobileVRouteFrameRoundTrip(t *testing.T) {
	packet := buildMobileVRouteTestIPv4Packet(6, "10.0.0.2", "198.18.0.17", 12345, 443)
	frame, err := buildMobileVRouteIPFrame(packet, []string{"9", "", "17"})
	if err != nil {
		t.Fatalf("build ip frame failed: %v", err)
	}
	encoded, err := encodeMobileVRouteFrame(frame)
	if err != nil {
		t.Fatalf("encode frame failed: %v", err)
	}
	decoded, err := readMobileVRouteFrame(bufio.NewReader(bytes.NewReader(encoded)))
	if err != nil {
		t.Fatalf("read frame failed: %v", err)
	}
	if decoded.MainType != mobileVRouteFrameMainTypeIP || decoded.SubType != mobileVRouteIPSubTypeIPv4 {
		t.Fatalf("unexpected frame type: %+v", decoded)
	}
	if !bytes.Equal(decoded.Data, packet) {
		t.Fatalf("decoded packet mismatch")
	}
	var control mobileVRouteFrameControlEnvelope
	if err := json.Unmarshal(decoded.Control, &control); err != nil {
		t.Fatalf("unmarshal control failed: %v", err)
	}
	if got, want := strings.Join(control.Path, ">"), "9>17"; got != want {
		t.Fatalf("control path=%s, want %s", got, want)
	}

	encoded[len(encoded)-1] ^= 0xff
	if _, err := readMobileVRouteFrame(bufio.NewReader(bytes.NewReader(encoded))); err == nil {
		t.Fatalf("read corrupted frame succeeded, want checksum error")
	}
}

func TestMobileVRouteRespondsToPeerDebugLogPull(t *testing.T) {
	androidLogStore.mu.Lock()
	oldEntries := append([]androidLogEntry(nil), androidLogStore.entries...)
	androidLogStore.entries = nil
	androidLogStore.mu.Unlock()
	t.Cleanup(func() {
		androidLogStore.mu.Lock()
		androidLogStore.entries = oldEntries
		androidLogStore.mu.Unlock()
	})
	androidLogStore.add("vpn", "warning", "takeover-test carrier failed")
	androidLogStore.add("vpn", "normal", "unrelated message")

	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	carrier := newMobileVRouteCarrier("debug-log", mobileVRouteForwardPlan{LocalNode: "9", RouteID: "vrouter-9-19"}, left)
	if carrier == nil {
		t.Fatal("carrier is nil")
	}
	path := []string{"19", "9"}
	control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: path})
	if err != nil {
		t.Fatalf("marshal control: %v", err)
	}
	request := mobileVRouteDebugLogPayload{
		RequestID:    "debug-request-1",
		SourceNodeID: "19",
		TargetNodeID: "9",
		Path:         path,
		Lines:        200,
		MinLevel:     "realtime",
		Keyword:      "takeover-test",
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := carrier.handleIncomingFrame(mobileVRouteFrame{
		MainType: mobileVRouteFrameMainTypeDebugLog,
		SubType:  mobileVRouteDebugLogSubTypeQuery,
		Control:  control,
		Data:     payload,
	}); err != nil {
		t.Fatalf("handle debug log query: %v", err)
	}

	select {
	case frame := <-carrier.txControl:
		if frame.MainType != mobileVRouteFrameMainTypeDebugLog || frame.SubType != mobileVRouteDebugLogSubTypeResponse {
			t.Fatalf("unexpected response frame: %+v", frame)
		}
		var responseControl mobileVRouteFrameControlEnvelope
		if err := json.Unmarshal(frame.Control, &responseControl); err != nil {
			t.Fatalf("unmarshal response control: %v", err)
		}
		if got := strings.Join(responseControl.Path, ">"); got != "9>19" {
			t.Fatalf("response path=%s, want 9>19", got)
		}
		response := mobileVRouteDebugLogPayload{}
		if err := json.Unmarshal(frame.Data, &response); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if !response.OK || response.Responder != "9" || response.Source != "android" || response.Count != 1 || !strings.Contains(response.Content, "takeover-test") {
			t.Fatalf("unexpected debug log response: %+v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for mobile debug log response")
	}
}

func TestMobileVRouteRespondsToPingAndPathRTT(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	carrier := newMobileVRouteCarrier("rtt", mobileVRouteForwardPlan{LocalNode: "9", RouteID: "vrouter-9-19"}, left)
	if carrier == nil {
		t.Fatal("carrier is nil")
	}
	path := []string{"19", "9"}
	control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: path})
	if err != nil {
		t.Fatalf("marshal control: %v", err)
	}

	tests := []struct {
		name                string
		mainType            uint16
		querySubType        uint16
		responseSubType     uint16
		preserveCreatedTime bool
	}{
		{name: "adjacent ping", mainType: mobileVRouteFrameMainTypePingPong, querySubType: mobileVRoutePingPongSubTypePing, responseSubType: mobileVRoutePingPongSubTypePong},
		{name: "path rtt", mainType: mobileVRouteFrameMainTypePathRTT, querySubType: mobileVRoutePathRTTSubTypeQuery, responseSubType: mobileVRoutePathRTTSubTypeResponse, preserveCreatedTime: true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			createdAt := time.Now().Add(time.Hour).UnixNano() + int64(index)
			request := mobileVRouteControlProbePayload{
				RequestID:         "rtt-request-" + strconv.Itoa(index),
				SourceNodeID:      "19",
				TargetNodeID:      "9",
				Path:              path,
				CreatedAtUnixNano: createdAt,
				PingBytes:         64,
			}
			payload, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			if err := carrier.handleIncomingFrame(mobileVRouteFrame{
				MainType: test.mainType,
				SubType:  test.querySubType,
				Control:  control,
				Data:     payload,
			}); err != nil {
				t.Fatalf("handle rtt query: %v", err)
			}

			select {
			case frame := <-carrier.txControl:
				if frame.MainType != test.mainType || frame.SubType != test.responseSubType {
					t.Fatalf("response frame=%d/%d, want %d/%d", frame.MainType, frame.SubType, test.mainType, test.responseSubType)
				}
				var responseControl mobileVRouteFrameControlEnvelope
				if err := json.Unmarshal(frame.Control, &responseControl); err != nil {
					t.Fatalf("unmarshal response control: %v", err)
				}
				if got := strings.Join(responseControl.Path, ">"); got != "9>19" {
					t.Fatalf("response path=%s, want 9>19", got)
				}
				response := mobileVRouteControlProbePayload{}
				if err := json.Unmarshal(frame.Data, &response); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if !response.OK || response.Responder != "9" || response.LatencyMS != 0 {
					t.Fatalf("unexpected rtt response: %+v", response)
				}
				if test.preserveCreatedTime && response.CreatedAtUnixNano != createdAt {
					t.Fatalf("path rtt timestamp=%d, want original %d", response.CreatedAtUnixNano, createdAt)
				}
				if !test.preserveCreatedTime && (response.CreatedAtUnixNano <= 0 || response.CreatedAtUnixNano == createdAt) {
					t.Fatalf("ping timestamp=%d should be responder time, request=%d", response.CreatedAtUnixNano, createdAt)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for mobile rtt response")
			}
		})
	}
}

func TestMobileVRouteActivelyMeasuresAdjacentRTT(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	config := mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "9", IP: "198.18.0.9"},
			{NodeID: "19", IP: "198.18.0.19", ServicePort: 12040},
		},
		TopologyRules: []mobileVRouteTopology{{
			ID:              "vrouter-9-19",
			FromNodeID:      "9",
			ToNodeID:        "19",
			ToServiceDomain: "edge-19.example.com",
			ToServicePort:   12040,
			Secret:          "secret-9-19",
			AuthTicket:      "ticket-9-19",
			Enabled:         true,
		}},
	}
	if err := persistMobileVRouteConfig(configDir, config); err != nil {
		t.Fatalf("persist config: %v", err)
	}
	plan, err := buildMobileVRouteForwardPlan(configDir, mobileVRouteProbeExitRouteID("19"))
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	left, right := net.Pipe()
	defer right.Close()
	carrier := newMobileVRouteCarrier(mobileVRouteCarrierKey(plan), plan, left)
	if carrier == nil {
		t.Fatal("carrier is nil")
	}
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items[carrier.key] = carrier
	mobileVRouteCarrierState.mu.Unlock()
	carrier.start()
	defer carrier.close()

	peerDone := make(chan error, 1)
	go func() {
		frame, err := readMobileVRouteFrame(bufio.NewReader(right))
		if err != nil {
			peerDone <- err
			return
		}
		if frame.MainType != mobileVRouteFrameMainTypePingPong || frame.SubType != mobileVRoutePingPongSubTypePing {
			peerDone <- fmt.Errorf("query frame=%d/%d, want ping", frame.MainType, frame.SubType)
			return
		}
		request := mobileVRouteControlProbePayload{}
		if err := json.Unmarshal(frame.Data, &request); err != nil {
			peerDone <- err
			return
		}
		request.OK = true
		request.Responder = "19"
		request.LatencyMS = 0
		request.CreatedAtUnixNano = time.Now().UnixNano()
		payload, err := json.Marshal(request)
		if err != nil {
			peerDone <- err
			return
		}
		control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: []string{"19", "9"}})
		if err != nil {
			peerDone <- err
			return
		}
		encoded, err := encodeMobileVRouteFrame(mobileVRouteFrame{
			MainType: mobileVRouteFrameMainTypePingPong,
			SubType:  mobileVRoutePingPongSubTypePong,
			Control:  control,
			Data:     payload,
		})
		if err == nil {
			_, err = right.Write(encoded)
		}
		peerDone <- err
	}()

	result := runMobileVRoutePathRTT("19")
	if ok, _ := result["ok"].(bool); !ok {
		t.Fatalf("active RTT failed: %+v", result)
	}
	if result["responder"] != "19" || result["target_node_id"] != "19" {
		t.Fatalf("unexpected active RTT result: %+v", result)
	}
	if latency, _ := result["latency_ms"].(int64); latency < 1 {
		t.Fatalf("latency=%v, want positive RTT: %+v", result["latency_ms"], result)
	}
	if err := <-peerDone; err != nil {
		t.Fatalf("peer exchange failed: %v", err)
	}
}

func TestMobileVRouteFrameChecksumKeepsOddControlAdjacentToData(t *testing.T) {
	header := make([]byte, mobileVRouteFrameEnvelopeHeaderSize-2)
	control := []byte{0x01}
	data := []byte{0x02}
	if got, want := mobileVRouteFrameChecksum(header, control, data), uint16(0xfefd); got != want {
		t.Fatalf("checksum=0x%x, want 0x%x", got, want)
	}
}

func TestMobileVRouteIPv4PacketTargetParsesPorts(t *testing.T) {
	tcp := buildMobileVRouteTestIPv4Packet(6, "10.0.0.2", "198.18.0.17", 34567, 443)
	ip, port, ok := mobileVRouteIPv4PacketTarget(tcp)
	if !ok || ip != "198.18.0.17" || port != "443" {
		t.Fatalf("tcp target=%s:%s ok=%t", ip, port, ok)
	}

	udp := buildMobileVRouteTestIPv4Packet(17, "10.0.0.2", "8.8.8.8", 53000, 53)
	ip, port, ok = mobileVRouteIPv4PacketTarget(udp)
	if !ok || ip != "8.8.8.8" || port != "53" {
		t.Fatalf("udp target=%s:%s ok=%t", ip, port, ok)
	}

	icmp := buildMobileVRouteTestIPv4Packet(1, "10.0.0.2", "1.1.1.1", 0, 0)
	ip, port, ok = mobileVRouteIPv4PacketTarget(icmp)
	if !ok || ip != "1.1.1.1" || port != "0" {
		t.Fatalf("icmp target=%s:%s ok=%t", ip, port, ok)
	}
}

func TestMobileVRouteSecretAuthHeaders(t *testing.T) {
	headers := http.Header{}
	if err := applyMobileVRouteSecretAuthHeaders(headers, "route-1", "secret-1", "ticket-1", "7", http.MethodGet, mobileVRouteRelayAPIPath, mobileVRouteBridgeRoleToNext); err != nil {
		t.Fatalf("apply auth headers failed: %v", err)
	}
	nonce := strings.TrimPrefix(headers.Get("Authorization"), "Bearer ")
	if nonce == "" || nonce == headers.Get("Authorization") {
		t.Fatalf("missing bearer nonce: %q", headers.Get("Authorization"))
	}
	expected := buildMobileVRouteHMAC("secret-1", "route-1", nonce, http.MethodGet, mobileVRouteRelayAPIPath, "7", mobileVRouteBridgeRoleToNext)
	if headers.Get(mobileVRouteCodexMACHeader) != expected {
		t.Fatalf("mac=%s, want %s", headers.Get(mobileVRouteCodexMACHeader), expected)
	}
	if headers.Get(mobileVRouteCodexAuthModeHeader) != "secret_hmac" || headers.Get(mobileVRouteCodexAuthTicketHeader) != "ticket-1" {
		t.Fatalf("unexpected auth headers: %+v", headers)
	}
	if headers.Get(mobileVRouteCodexSourceNodeHeader) != "7" {
		t.Fatalf("unexpected source node header: %+v", headers)
	}
}

func buildMobileVRouteTestIPv4Packet(proto uint8, src string, dst string, srcPort uint16, dstPort uint16) []byte {
	packet := make([]byte, 24)
	packet[0] = 0x45
	packet[8] = 64
	packet[9] = proto
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[12:16], net.ParseIP(src).To4())
	copy(packet[16:20], net.ParseIP(dst).To4())
	binary.BigEndian.PutUint16(packet[20:22], srcPort)
	binary.BigEndian.PutUint16(packet[22:24], dstPort)
	return packet
}

func TestMobileVRouteRefreshPersistsConfigAndVPNDecision(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMobileProbeChallengeForTest(w, r) {
			return
		}
		if r.URL.Path != mobileVRouteConfigAPIPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Probe-Node-Id") != "9" || r.Header.Get("X-Probe-Signature") == "" {
			t.Fatalf("missing auth headers: %+v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(mobileVRouteConfigResponse{
			NodeID: "9",
			VirtualRouter: mobileVRouteConfig{
				Enabled:    true,
				FakeIPCIDR: "198.18.0.0/15",
				ProbeIPs: []mobileVRouteProbeIP{
					{NodeID: "1", IP: "198.18.0.1"},
					{NodeID: "9", IP: "198.18.0.9"},
				},
				RouteRules: []mobileVRouteRouteRule{{
					ID:         "rr-ai",
					Name:       "AI",
					Action:     "probe_exit",
					ExitNodeID: "1",
					Entries:    []string{"domain_suffix:chatgpt.com"},
				}},
			},
		})
	}))
	defer server.Close()

	config, err := refreshMobileVRouteConfig(server.URL, "9", "secret-9", configDir)
	if err != nil {
		t.Fatalf("refresh vroute config failed: %v", err)
	}
	if !config.Enabled || mobileVRouteConfigRuleCount(config) != 1 {
		t.Fatalf("unexpected config: %+v", config)
	}
	loaded, err := loadMobileVRouteConfig(configDir)
	if err != nil {
		t.Fatalf("load vroute config failed: %v", err)
	}
	if !loaded.Enabled || len(loaded.RouteRules) != 1 {
		t.Fatalf("unexpected loaded config: %+v", loaded)
	}

	route, err := decideVPNRouteForTarget("www.chatgpt.com:443")
	if err != nil {
		t.Fatalf("decide vpn route failed: %v", err)
	}
	if route.Direct || route.Reject || route.Group != "AI" || route.SelectedRouteID != "vroute:1" || route.TargetAddr != "www.chatgpt.com:443" {
		t.Fatalf("unexpected vpn route: %+v", route)
	}
}

func TestMobileVRouteRefreshConfigFilesClosesCarriers(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMobileProbeChallengeForTest(w, r) {
			return
		}
		if r.URL.Path != mobileVRouteConfigAPIPath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(mobileVRouteConfigResponse{
			NodeID: "9",
			VirtualRouter: mobileVRouteConfig{
				Enabled: true,
				ProbeIPs: []mobileVRouteProbeIP{
					{NodeID: "9", IP: "198.18.0.9"},
				},
			},
		})
	}))
	defer server.Close()

	carrier := &mobileVRouteCarrier{key: "old-carrier"}
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items[carrier.key] = carrier
	mobileVRouteCarrierState.mu.Unlock()

	if _, err := refreshConfigFiles(server.URL, "9", "secret-9", configDir); err != nil {
		t.Fatalf("refresh config files failed: %v", err)
	}
	if status := snapshotMobileVRouteCarriers(); status["active"] != 0 {
		t.Fatalf("active carriers=%v, want 0 after config refresh", status["active"])
	}
}

func TestMobileVRouteDNSReturnsFakeIPForProbeExitDomain(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []mobileVRouteProbeIP{
			{NodeID: "1", IP: "198.18.0.1"},
			{NodeID: "9", IP: "198.18.0.9"},
		},
		RouteRules: []mobileVRouteRouteRule{{
			ID:         "rr-ai",
			Name:       "AI",
			Action:     "probe_exit",
			ExitNodeID: "1",
			Entries:    []string{"domain_suffix:chatgpt.com"},
		}},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}

	query, err := buildAndroidVPNDNSQuery("chatgpt.com", dnsmessage.TypeA)
	if err != nil {
		t.Fatalf("build dns query failed: %v", err)
	}
	response, err := resolveAndroidVPNDNSPacket(query)
	if err != nil {
		t.Fatalf("resolve dns packet failed: %v", err)
	}
	ips := extractAndroidVPNDNSResponseIPs(response)
	if len(ips) != 1 || net.ParseIP(ips[0]).To4() == nil || !strings.HasPrefix(ips[0], "198.18.") {
		t.Fatalf("dns ips=%v, want one 198.18 fake ip", ips)
	}
	route, err := decideVPNRouteForTarget(net.JoinHostPort(ips[0], "443"))
	if err != nil {
		t.Fatalf("decide fake ip route failed: %v", err)
	}
	if route.SelectedRouteID != "vroute:1" || route.Group != "AI" || route.TargetAddr != "chatgpt.com:443" {
		t.Fatalf("unexpected fake ip route: %+v", route)
	}
}

func TestAndroidVPNDNSSuppressesAAAAWhenIPv6Disabled(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		Enabled: true,
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}

	query, err := buildAndroidVPNDNSQuery("example.com", dnsmessage.TypeAAAA)
	if err != nil {
		t.Fatalf("build dns query failed: %v", err)
	}
	response, err := resolveAndroidVPNDNSPacket(query)
	if err != nil {
		t.Fatalf("resolve dns packet failed: %v", err)
	}
	if ips := extractAndroidVPNDNSResponseIPs(response); len(ips) != 0 {
		t.Fatalf("AAAA ips=%v, want empty response while android vpn ipv6 is disabled", ips)
	}
}

func TestAndroidVPNTCPDNSReturnsFakeIPForProbeExitDomain(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		RouteRules: []mobileVRouteRouteRule{{
			ID:         "rr-ai",
			Name:       "AI",
			Action:     "probe_exit",
			ExitNodeID: "1",
			Entries:    []string{"domain_suffix:chatgpt.com"},
		}},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}
	client, server := net.Pipe()
	defer client.Close()
	go serveAndroidVPNTCPDNS(server, "10.111.0.1:53")

	query, err := buildAndroidVPNDNSQuery("chatgpt.com", dnsmessage.TypeA)
	if err != nil {
		t.Fatalf("build dns query failed: %v", err)
	}
	request := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(request[:2], uint16(len(query)))
	copy(request[2:], query)
	if _, err := client.Write(request); err != nil {
		t.Fatalf("write tcp dns query failed: %v", err)
	}
	header := make([]byte, 2)
	if _, err := io.ReadFull(client, header); err != nil {
		t.Fatalf("read tcp dns header failed: %v", err)
	}
	response := make([]byte, int(binary.BigEndian.Uint16(header)))
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatalf("read tcp dns response failed: %v", err)
	}
	ips := extractAndroidVPNDNSResponseIPs(response)
	if len(ips) != 1 || net.ParseIP(ips[0]).To4() == nil || !strings.HasPrefix(ips[0], "198.18.") {
		t.Fatalf("tcp dns ips=%v, want one 198.18 fake ip", ips)
	}
}

func TestAndroidVPNRealIPFakeNATRewritesPacketsLocally(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	if err := persistMobileVRouteConfig(configDir, mobileVRouteConfig{
		LocalNodeID: "9",
		Enabled:     true,
		RouteRules: []mobileVRouteRouteRule{{
			ID:         "rr-google",
			Name:       "Google",
			Action:     "probe_exit",
			ExitNodeID: "17",
			Entries:    []string{"domain_suffix:googleapis.com"},
		}},
	}); err != nil {
		t.Fatalf("persist vroute config failed: %v", err)
	}
	route, err := decideVPNRouteForTarget("play.googleapis.com:443")
	if err != nil {
		t.Fatalf("decide route failed: %v", err)
	}
	rememberAndroidVPNSNIFakeIP("216.239.38.223:443", "play.googleapis.com:443", route)

	vpnDNSState.mu.Lock()
	fakeIP := vpnDNSState.fakeDomainToIP["play.googleapis.com"]
	natCount := len(vpnDNSState.realIPToFake)
	vpnDNSState.mu.Unlock()
	if fakeIP == "" || natCount != 2 {
		t.Fatalf("fakeIP=%q natCount=%d, want mapping", fakeIP, natCount)
	}

	outbound := buildMobileVRouteTestIPv4Packet(6, "10.111.0.2", "216.239.38.223", 45678, 443)
	rewritten, ok, err := rewriteAndroidVPNRealIPPacketToFake(outbound)
	if err != nil {
		t.Fatalf("rewrite outbound failed: %v", err)
	}
	if !ok {
		t.Fatalf("outbound packet was not rewritten")
	}
	info, ok := parseAndroidVPNIPv4TransportPacket(rewritten)
	if !ok || info.DestinationIP != fakeIP || info.SourceIP != "10.111.0.2" {
		t.Fatalf("unexpected rewritten outbound: %+v fake=%s", info, fakeIP)
	}
	if got := binary.BigEndian.Uint16(rewritten[10:12]); got == 0 {
		t.Fatalf("ipv4 checksum was not set")
	}

	reply := buildMobileVRouteTestIPv4Packet(6, fakeIP, "10.111.0.2", 443, 45678)
	restored, ok, err := rewriteAndroidVPNFakeIPPacketToReal(reply)
	if err != nil {
		t.Fatalf("rewrite reply failed: %v", err)
	}
	if !ok {
		t.Fatalf("reply packet was not rewritten")
	}
	replyInfo, ok := parseAndroidVPNIPv4TransportPacket(restored)
	if !ok || replyInfo.SourceIP != "216.239.38.223" || replyInfo.DestinationIP != "10.111.0.2" {
		t.Fatalf("unexpected restored reply: %+v", replyInfo)
	}

	udpOutbound := buildMobileVRouteTestIPv4Packet(17, "10.111.0.2", "216.239.38.223", 45679, 443)
	udpRewritten, ok, err := rewriteAndroidVPNRealIPPacketToFake(udpOutbound)
	if err != nil {
		t.Fatalf("rewrite udp outbound failed: %v", err)
	}
	if !ok {
		t.Fatalf("udp outbound packet was not rewritten")
	}
	udpInfo, ok := parseAndroidVPNIPv4TransportPacket(udpRewritten)
	if !ok || udpInfo.DestinationIP != fakeIP || udpInfo.Protocol != 17 {
		t.Fatalf("unexpected rewritten udp outbound: %+v fake=%s", udpInfo, fakeIP)
	}
}
