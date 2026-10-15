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
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, path)
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
		if err != nil || line != "READY\n" {
			cancel()
			cmd.Wait()
			t.Fatalf("C server not ready: %q %v %s", line, err, errors.String())
		}
		conn, multi, err := openProbeVirtualRouterBridgeRelayNetConnWithDomainPolicyForSlot(routeID, rule.Secret, "127.0.0.1", port, "http2", probeRouteBridgeRoleToNext, 5*time.Second, true, 0)
		if err != nil {
			cancel()
			cmd.Wait()
			t.Fatal(err)
		}
		if multi {
			conn.Close()
			cancel()
			cmd.Wait()
			t.Fatal("C server must advertise single carrier")
		}
		go runProbeVirtualRouterPhysicalCarrier(rt, 0, conn, "C-server-test", "127.0.0.1")
		var output bytes.Buffer
		_, copyErr := output.ReadFrom(reader)
		err = cmd.Wait()
		conn.Close()
		t.Logf("C server: %s", output.String())
		if copyErr != nil || err != nil {
			t.Fatalf("C server failed: %v %v %s", copyErr, err, errors.String())
		}
		return
	}
	out, err := cmd.CombinedOutput()
	t.Logf("C peer: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}
