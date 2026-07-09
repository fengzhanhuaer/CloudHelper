package mobilecore

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func resetMobileVRouteVPNStateForTest(t *testing.T, configDir string) {
	t.Helper()
	closeMobileVRouteCarriers()
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

	vpnDNSState.mu.Lock()
	oldDNSState := *vpnDNSState
	vpnDNSState.nextFakeOffset = 2
	vpnDNSState.fakeDomainToIP = map[string]string{}
	vpnDNSState.fakeIPToEntry = map[string]androidVPNDNSFakeEntry{}
	vpnDNSState.routeIPHints = map[string]androidVPNDNSRouteHintEntry{}
	vpnDNSState.cacheDir = ""
	vpnDNSState.cacheLoaded = false
	vpnDNSState.cacheDirty = false
	if vpnDNSState.cacheTimer != nil {
		vpnDNSState.cacheTimer.Stop()
		vpnDNSState.cacheTimer = nil
	}
	vpnDNSState.mu.Unlock()

	t.Cleanup(func() {
		closeMobileVRouteCarriers()
		mobileVRouteCarrierState.mu.Lock()
		mobileVRouteCarrierState.items = oldCarrierItems
		mobileVRouteCarrierState.lastError = oldCarrierLastError
		mobileVRouteCarrierState.lastErrorUnixNS = oldCarrierLastErrorUnixNS
		mobileVRouteCarrierState.mu.Unlock()

		vpnRuntime.mu.Lock()
		vpnRuntime.configDir = oldConfigDir
		vpnRuntime.mu.Unlock()
		vpnDNSState.mu.Lock()
		if vpnDNSState.cacheTimer != nil {
			vpnDNSState.cacheTimer.Stop()
		}
		*vpnDNSState = oldDNSState
		vpnDNSState.mu.Unlock()
	})
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
	carrier := &mobileVRouteCarrier{
		key:           mobileVRouteCarrierKey(plan),
		plan:          plan,
		conn:          left,
		reader:        bufio.NewReader(left),
		createdUnixNS: time.Now().UnixNano(),
	}
	carrier.markActivity()
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items[carrier.key] = carrier
	mobileVRouteCarrierState.mu.Unlock()

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
		if frame.MainType != mobileVRouteFrameMainTypeIP || frame.SubType != mobileVRouteIPSubTypeIPv4 || !bytes.Equal(frame.Data, packet) {
			t.Fatalf("unexpected cidr vroute frame: %+v", frame)
		}
	case readErr := <-errCh:
		t.Fatalf("read cidr vroute frame failed: %v", readErr)
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for cidr vroute frame")
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

func TestMobileVRouteCarrierWriteFailureClosesAndRecordsError(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	left, right := net.Pipe()
	_ = right.Close()
	carrier := &mobileVRouteCarrier{
		key: "test-carrier",
		plan: mobileVRouteForwardPlan{
			RouteID:    "vrouter-test",
			Path:       []string{"9", "17"},
			NextNode:   "17",
			ExitNode:   "17",
			RelayHost:  "edge.example.com",
			RelayPort:  12040,
			BridgeRole: mobileVRouteBridgeRoleToNext,
			Layer:      "websocket",
		},
		conn:          left,
		reader:        bufio.NewReader(left),
		createdUnixNS: time.Now().UnixNano(),
	}
	carrier.markActivity()
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.items[carrier.key] = carrier
	mobileVRouteCarrierState.mu.Unlock()

	err := carrier.writeIPPacket(buildMobileVRouteTestIPv4Packet(6, "10.0.0.2", "198.18.0.17", 12345, 443), []string{"9", "17"})
	if err == nil {
		t.Fatalf("write succeeded, want failure")
	}
	if !errors.Is(err, io.ErrClosedPipe) && !strings.Contains(strings.ToLower(err.Error()), "closed") {
		t.Fatalf("write error=%v, want closed pipe", err)
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
	if capabilities["websocket_h3"] != false || capabilities["inbound_listener"] != false || capabilities["reverse_first_hop"] != false {
		t.Fatalf("unsupported mobile vroute capabilities should be explicit false: %+v", capabilities)
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
	plan := mobileVRouteForwardPlan{Layer: "websocket-h3"}
	if _, err := dialMobileVRouteCarrier(plan); err == nil || !strings.Contains(err.Error(), "websocket-h3") {
		t.Fatalf("dial h3 err=%v, want explicit unsupported websocket-h3", err)
	}
}

func TestMobileVRouteForwardPlanRejectsReverseAdjacentCarrier(t *testing.T) {
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

	if _, err := buildMobileVRouteForwardPlan(configDir, "vroute:9"); err == nil || !strings.Contains(err.Error(), "reverse first hop") {
		t.Fatalf("build reverse forward plan err=%v, want reverse first hop unsupported", err)
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
	if err := applyMobileVRouteSecretAuthHeaders(headers, "route-1", "secret-1", "ticket-1"); err != nil {
		t.Fatalf("apply auth headers failed: %v", err)
	}
	nonce := strings.TrimPrefix(headers.Get("Authorization"), "Bearer ")
	if nonce == "" || nonce == headers.Get("Authorization") {
		t.Fatalf("missing bearer nonce: %q", headers.Get("Authorization"))
	}
	expected := buildMobileVRouteTestHMAC("secret-1", "route-1", nonce)
	if headers.Get(mobileVRouteCodexMACHeader) != expected {
		t.Fatalf("mac=%s, want %s", headers.Get(mobileVRouteCodexMACHeader), expected)
	}
	if headers.Get(mobileVRouteCodexAuthModeHeader) != "secret_hmac" || headers.Get(mobileVRouteCodexAuthTicketHeader) != "ticket-1" {
		t.Fatalf("unexpected auth headers: %+v", headers)
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

func buildMobileVRouteTestHMAC(secret string, routeID string, nonce string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(routeID))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestMobileVRouteRefreshPersistsConfigAndVPNDecision(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
