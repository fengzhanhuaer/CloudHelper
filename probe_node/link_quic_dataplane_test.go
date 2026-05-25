package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

func TestNewProbeChainQUICConfigUsesV2V1AndDatagrams(t *testing.T) {
	cfg := newProbeChainQUICConfig(7)
	if cfg == nil {
		t.Fatalf("config is nil")
	}
	if len(cfg.Versions) != 2 || cfg.Versions[0] != quic.Version2 || cfg.Versions[1] != quic.Version1 {
		t.Fatalf("unexpected versions: %+v", cfg.Versions)
	}
	if !cfg.EnableDatagrams {
		t.Fatalf("datagrams should be enabled")
	}
	if cfg.MaxIncomingStreams != 7 {
		t.Fatalf("MaxIncomingStreams=%d want=7", cfg.MaxIncomingStreams)
	}
}

func TestProbeChainQUICDataPlaneLayerIncludesHTTP3Alias(t *testing.T) {
	if !isProbeChainQUICDataPlaneLayer("quic-stream") {
		t.Fatal("quic-stream should use QUIC data plane")
	}
	if !isProbeChainQUICDataPlaneLayer("http3") {
		t.Fatal("http3 should use QUIC data plane in TUN group runtime")
	}
	if isProbeChainQUICDataPlaneLayer("websocket-h3") {
		t.Fatal("websocket-h3 should remain the legacy compatibility path")
	}
}

func TestProbeChainQUICDataPlaneTCPStreamRoundTrip(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	defer echoLn.Close()
	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	basePort := reserveProbeChainQUICDataPlaneBasePortForTest(t)
	cert := writeProbeChainQUICDataPlaneTestCert(t)
	rt := &probeChainRuntime{
		cfg: probeChainRuntimeConfig{
			chainID:      "chain-quic-test",
			secret:       "secret-quic-test",
			role:         "entry",
			listenHost:   "127.0.0.1",
			listenPort:   basePort,
			nextAuthMode: "proxy",
		},
		stopCh: make(chan struct{}),
	}
	if err := startProbeChainQUICDataPlaneServer(rt, cert); err != nil {
		t.Fatalf("start quic dataplane: %v", err)
	}
	t.Cleanup(func() {
		close(rt.stopCh)
		rt.closeRuntimeResources()
	})

	session, err := openProbeChainRelayQUICDataPlaneSession(
		"chain-quic-test",
		"secret-quic-test",
		"127.0.0.1",
		basePort,
		probeChainBridgeRoleToNext,
		"127.0.0.1",
		"127.0.0.1",
		5*time.Second,
		false,
	)
	if err != nil {
		t.Fatalf("open quic dataplane session: %v", err)
	}
	defer session.Close()

	stream, err := openProbeChainQUICProxyStream(session, probeChainPortForwardNetworkTCP, echoLn.Addr().String(), nil)
	if err != nil {
		t.Fatalf("open proxy stream: %v", err)
	}
	defer stream.Close()

	payload := []byte("hello over quic stream")
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("echo=%q want=%q", string(buf), string(payload))
	}

	speed := probeChainRelaySpeedTestWithLayer("chain-quic-test", "secret-quic-test", "127.0.0.1", basePort, "quic-stream", 32*1024, 5*time.Second)
	if !speed.OK {
		t.Fatalf("quic speed test failed: %+v", speed)
	}
	if speed.Bytes != 32*1024 {
		t.Fatalf("quic speed bytes=%d want=%d result=%+v", speed.Bytes, 32*1024, speed)
	}
	if speed.DurationMS <= 0 || speed.RateBPS <= 0 {
		t.Fatalf("quic speed should measure data window only: %+v", speed)
	}
}

func reserveProbeChainQUICDataPlaneBasePortForTest(t *testing.T) int {
	t.Helper()
	for i := 0; i < 32; i++ {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve udp port: %v", err)
		}
		port := pc.LocalAddr().(*net.UDPAddr).Port
		_ = pc.Close()
		if port > 1 {
			return port - 1
		}
	}
	t.Fatalf("unable to reserve quic dataplane base port")
	return 0
}

func writeProbeChainQUICDataPlaneTestCert(t *testing.T) probeServerCertificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return probeServerCertificate{CertPath: certPath, KeyPath: keyPath, Domain: "127.0.0.1", NotAfter: tmpl.NotAfter}
}
