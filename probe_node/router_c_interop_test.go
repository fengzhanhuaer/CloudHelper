package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/cloudhelper/probe_node/internal/routewire"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Optional cross-language test, run in the disposable local Linux test container.
// The Go test binary is a PC fixture and is never included in router firmware.
func TestRouterCCarrierWithExistingProbe(t *testing.T) {
	binary := os.Getenv("CHVR_CARRIER_TEST")
	if binary == "" {
		t.Skip("set CHVR_CARRIER_TEST to the native C test-carrier executable")
	}
	t.Run("CClient", func(t *testing.T) { testRouterCCarrier(t, binary, false) })
	t.Run("CServer", func(t *testing.T) { testRouterCCarrier(t, binary, true) })

}

func testRouterCCarrier(t *testing.T, binary string, serverMode bool) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("C interoperability test") })
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeRouteAuthTicketStoreForTest()
	resetProbeRouteAuthIPStateForTest()
	oldProfile := activeProbeProductProfile
	activeProbeProductProfile.EnableVRoutePlatformInterface = false
	t.Cleanup(func() { activeProbeProductProfile = oldProfile })
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rule := probeVirtualRouterTopologyRule{ID: "router-c-interop", FromNodeID: "7", ToNodeID: "8", Direction: "forward", RouteLayer: "http2", Secret: "fixture-only-carrier-secret", Enabled: true, UserPublicKey: base64.StdEncoding.EncodeToString(pub)}
	if serverMode {
		rule.FromNodeID = "8"
		rule.ToNodeID = "7"
	}
	routeID := probeVirtualRouterRuntimeRouteID(rule)
	rule.AuthTicket = buildProbeRouteUserAuthTicketForTest(t, priv, routeID, rule.UserPublicKey, rule.FromNodeID, rule.ToNodeID)
	rememberProbeRouteAuthTicket(routeID, rule.AuthTicket)
	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: routeID, secret: rule.Secret, authTicket: rule.AuthTicket, rawPublicKey: rule.UserPublicKey, userPublicKey: pub, fromNodeID: "7", toNodeID: "8", localNodeID: "8", peerNodeID: "7", localIP: "198.18.0.8", peerIP: "198.18.0.7", identity: nodeIdentity{NodeID: "8"}}, stopCh: make(chan struct{})}
	rt.cfg.fromNodeID = rule.FromNodeID
	rt.cfg.toNodeID = rule.ToNodeID
	probeVirtualRouterRuntimeState.mu.Lock()
	oldRuntimes := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{routeID: rt}
	probeVirtualRouterRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = oldRuntimes
		probeVirtualRouterRuntimeState.mu.Unlock()
	})
	server := httptest.NewTLSServer(buildProbeVirtualRouterRelayHandler())
	t.Cleanup(server.Close)
	spki := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	rule.ToTLSSPKISHA256 = hex.EncodeToString(spki[:])
	rule.ToServiceDomain = "127.0.0.1"
	rule.ToServicePort = server.Listener.Addr().(*net.TCPAddr).Port
	config := probeVirtualRouterConfig{Enabled: true, FakeIPCIDR: "198.18.0.0/15", ProbeIPs: []probeVirtualRouterProbeIP{{NodeID: "7", IP: "198.18.0.7"}, {NodeID: "8", IP: "198.18.0.8"}}, TopologyRules: []probeVirtualRouterTopologyRule{rule}}
	if !serverMode {
		// Match controller snapshots: the node port is authoritative; a link may omit it.
		config.ProbeIPs[1].ServicePort = rule.ToServicePort
		config.TopologyRules[0].ToServicePort = 0
	}
	applyProbeVirtualRouterConfigForNode(config, "8")
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	if serverMode {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port = listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		key, err := x509.MarshalPKCS8PrivateKey(server.TLS.Certificates[0].PrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]interface{}
		if err = json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		payload["test_server"] = true
		payload["test_port"] = port
		payload["test_cert"] = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
		payload["test_key"] = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
		data, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "carrier.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	// The C transparent source uses the unchanged Probe TCP proxy wire contract.
	// Only the exit's physical dial is redirected to a disposable echo server;
	// target assignment and path authorization remain production code.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			conn, acceptErr := echo.Accept()
			if acceptErr != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	oldDial := probeVRouteProxyExitTCPDial
	probeVRouteProxyExitTCPDial = func(target string) (net.Conn, error) {
		if target != "203.0.113.5:443" {
			return nil, fmt.Errorf("unexpected C proxy target %q", target)
		}
		return net.DialTimeout("tcp", echo.Addr().String(), time.Second)
	}
	t.Cleanup(func() { resetProbeVRouteProxyTCPStateForTest(); probeVRouteProxyExitTCPDial = oldDial })
	config.RouteRules = []probeVirtualRouterRouteRule{{ID: "interop-tcp", Name: "Interop TCP", Action: "probe_exit", ExitNodeID: "8", Entries: []string{"cidr:203.0.113.0/24"}}}
	applyProbeVirtualRouterConfigForNode(config, "8")
	if err := authorizeProbeVRouteProxyExitTarget("203.0.113.5:443", []string{"7", "8"}); err != nil {
		t.Fatalf("C proxy fixture exit assignment: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, path)
	// Exercise real Probe carrier TX workers on every new TLS connection,
	// including the replacement session, rather than merely counting sockets.
	var sender sync.WaitGroup
	sender.Add(1)
	go func() {
		defer sender.Done()
		seen := map[*probeVirtualRouterPhysicalCarrier]bool{}
		for ctx.Err() == nil {
			probeVirtualRouterFrameLinkState.mu.Lock()
			for _, link := range probeVirtualRouterFrameLinkState.links {
				link.mu.Lock()
				for slot, carrier := range link.carriers {
					if seen[carrier] {
						continue
					}
					msg, _ := json.Marshal(map[string]interface{}{"request_id": fmt.Sprintf("slot-fixture-%d", slot), "source_node_id": "8", "target_node_id": "7"})
					control, _ := routewire.MarshalPath([]string{"8", "7"})
					if carrier.tx.TryPush(probeVirtualRouterFrame{MainType: 2, SubType: 1, Control: control, Data: msg}) {
						seen[carrier] = true
					}
				}
				link.mu.Unlock()
			}
			probeVirtualRouterFrameLinkState.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	defer func() { cancel(); sender.Wait() }()
	if serverMode {
		var errors bytes.Buffer
		cmd.Stderr = &errors
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(stdout)
		line, err := reader.ReadString('\n')
		flowCount := 0
		for err == nil && strings.HasPrefix(line, "FLOW ") {
			checkRouterCFlow(t, line)
			flowCount++
			line, err = reader.ReadString('\n')
		}
		if err != nil || line != "READY\n" || flowCount != 32 {
			cancel()
			cmd.Wait()
			t.Fatalf("C server not ready: %q %v %s", line, err, errors.String())
		}
		for _, slot := range []int{2, 263} {
			conn, _, err := openProbeVirtualRouterBridgeRelayNetConnWithDomainPolicyForSlot(routeID, rule.Secret, "127.0.0.1", port, "http2", probeRouteBridgeRoleToNext, time.Second, true, slot)
			if err == nil {
				conn.Close()
				cancel()
				cmd.Wait()
				t.Fatalf("invalid slot %d accepted", slot)
			}
		}
		var workers sync.WaitGroup
		for slot := range routewire.DirectCarrierSlots {
			workers.Add(1)
			go func(slot int) {
				defer workers.Done()
				for ctx.Err() == nil {
					conn, multi, err := openProbeVirtualRouterBridgeRelayNetConnWithDomainPolicyForSlot(routeID, rule.Secret, "127.0.0.1", port, "http2", probeRouteBridgeRoleToNext, 2*time.Second, true, slot)
					if err != nil {
						select {
						case <-ctx.Done():
							return
						case <-time.After(100 * time.Millisecond):
						}
						continue
					}
					if !multi {
						conn.Close()
						t.Error("C server did not advertise multi-carrier")
						return
					}
					runProbeVirtualRouterPhysicalCarrier(rt, slot, conn, fmt.Sprintf("C-server-test-%d", slot), "127.0.0.1")
				}
			}(slot)
		}
		var output bytes.Buffer
		_, copyErr := output.ReadFrom(reader)
		err = cmd.Wait()
		cancel()
		closeProbeVirtualRouterFrameLinks("C server finished")
		workers.Wait()
		t.Logf("C server: %s", output.String())
		if copyErr != nil || err != nil {
			t.Fatalf("C server failed: %v %v %s", copyErr, err, errors.String())
		}
		checkRouterCCompletion(t, output.String())
		return
	}
	out, err := cmd.CombinedOutput()
	t.Logf("C peer: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	checkRouterCCompletion(t, string(out))
	flowCount := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "FLOW ") {
			checkRouterCFlow(t, line)
			flowCount++
		}
	}
	if flowCount != 32 {
		t.Fatalf("C fixture exited before flow assertions: %d/32", flowCount)
	}
}

func checkRouterCCompletion(t *testing.T, output string) {
	t.Helper()
	markers := []string{"PASS: Probe VR frames received on every physical slot", "PASS: C to existing Probe ping", "PASS: C to existing Probe upload", "PASS: C to existing Probe download", "PASS: two carriers, isolated detach and reconnect"}
	for _, marker := range markers {
		if !strings.Contains(output, marker) {
			t.Fatalf("C fixture missing completion marker %q", marker)
		}
	}
}

func checkRouterCFlow(t *testing.T, line string) {
	t.Helper()
	var port, slot int
	if _, err := fmt.Sscanf(line, "FLOW %d %d", &port, &slot); err != nil {
		t.Fatal(err)
	}
	if want := (port - 1000) / 2 % routewire.DirectCarrierSlots; slot != want {
		t.Fatalf("C round-robin: port=%d slot=%d want=%d", port, slot, want)
	}
}
