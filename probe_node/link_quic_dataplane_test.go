package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	if cfg.InitialStreamReceiveWindow < 128*1024*1024 || cfg.MaxStreamReceiveWindow < 512*1024*1024 {
		t.Fatalf("stream receive windows are too small: initial=%d max=%d", cfg.InitialStreamReceiveWindow, cfg.MaxStreamReceiveWindow)
	}
	if cfg.InitialConnectionReceiveWindow < 512*1024*1024 || cfg.MaxConnectionReceiveWindow < 1024*1024*1024 {
		t.Fatalf("connection receive windows are too small: initial=%d max=%d", cfg.InitialConnectionReceiveWindow, cfg.MaxConnectionReceiveWindow)
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

func TestProbeChainQUICDataPlaneLoopbackThroughputDiagnostic(t *testing.T) {
	if os.Getenv("PROBE_QUIC_LOOPBACK_DIAG") != "1" {
		t.Skip("set PROBE_QUIC_LOOPBACK_DIAG=1 to run QUIC loopback throughput diagnostic")
	}
	byteCount := int64(32 * 1024 * 1024)
	if raw := strings.TrimSpace(os.Getenv("PROBE_QUIC_LOOPBACK_DIAG_BYTES")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			byteCount = parsed
		}
	}
	basePort := reserveProbeChainQUICDataPlaneBasePortForTest(t)
	cert := writeProbeChainQUICDataPlaneTestCert(t)
	rt := &probeChainRuntime{
		cfg: probeChainRuntimeConfig{
			chainID:      "chain-quic-loopback-diag",
			secret:       "secret-quic-loopback-diag",
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
		"chain-quic-loopback-diag",
		"secret-quic-loopback-diag",
		"127.0.0.1",
		basePort,
		probeChainBridgeRoleToNext,
		"127.0.0.1",
		"127.0.0.1",
		10*time.Second,
		false,
	)
	if err != nil {
		t.Fatalf("open quic dataplane session: %v", err)
	}
	defer session.Close()

	for _, streams := range []int{1, 2, 4} {
		result, err := runProbeChainQUICLoopbackDiagnosticStreams(session, streams, byteCount)
		if err != nil {
			t.Fatalf("diagnostic streams=%d: %v", streams, err)
		}
		t.Logf("quic loopback diag streams=%d total_bytes=%d duration_ms=%d aggregate_rate_bps=%d aggregate_rate_mib_s=%.2f per_stream_mib_s=%.2f", streams, result.bytes, probeDurationMilliseconds(result.duration), result.rateBPS(), float64(result.rateBPS())/1024/1024, float64(result.rateBPS())/float64(streams)/1024/1024)
	}
}

func TestProbeChainRawUDPLoopbackThroughputDiagnostic(t *testing.T) {
	if os.Getenv("PROBE_UDP_LOOPBACK_DIAG") != "1" {
		t.Skip("set PROBE_UDP_LOOPBACK_DIAG=1 to run raw UDP loopback throughput diagnostic")
	}
	byteCount := int64(32 * 1024 * 1024)
	if raw := strings.TrimSpace(os.Getenv("PROBE_UDP_LOOPBACK_DIAG_BYTES")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			byteCount = parsed
		}
	}
	packetBytes := 1200
	if raw := strings.TrimSpace(os.Getenv("PROBE_UDP_LOOPBACK_DIAG_PACKET_BYTES")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 64 && parsed <= 1400 {
			packetBytes = parsed
		}
	}

	server, err := net.ListenUDP("udp", mustResolveProbeChainUDPAddrForTest(t, "127.0.0.1:0"))
	if err != nil {
		t.Fatalf("listen udp server: %v", err)
	}
	defer server.Close()
	tuneProbeChainUDPConn(server)

	client, err := net.ListenUDP("udp", mustResolveProbeChainUDPAddrForTest(t, "127.0.0.1:0"))
	if err != nil {
		t.Fatalf("listen udp client: %v", err)
	}
	defer client.Close()
	tuneProbeChainUDPConn(client)

	var serverSent int64
	var serverDurationNS int64
	serverErrCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		n, addr, err := server.ReadFromUDP(buf)
		if err != nil {
			serverErrCh <- err
			return
		}
		if n < 16 || string(buf[:8]) != "UDPDIAG1" {
			serverErrCh <- fmt.Errorf("invalid udp diag request: bytes=%d", n)
			return
		}
		total := int64(binary.BigEndian.Uint64(buf[8:16]))
		payload := make([]byte, packetBytes)
		for i := range payload {
			payload[i] = byte(i % 251)
		}
		startedAt := time.Now()
		remaining := total
		for remaining > 0 {
			n := len(payload)
			if remaining < int64(n) {
				n = int(remaining)
			}
			written, err := server.WriteToUDP(payload[:n], addr)
			if written > 0 {
				atomic.AddInt64(&serverSent, int64(written))
				remaining -= int64(written)
			}
			if err != nil {
				serverErrCh <- err
				return
			}
			if written == 0 {
				serverErrCh <- errors.New("udp zero write")
				return
			}
		}
		atomic.StoreInt64(&serverDurationNS, int64(time.Since(startedAt)))
		serverErrCh <- nil
	}()

	request := make([]byte, 16)
	copy(request[:8], []byte("UDPDIAG1"))
	binary.BigEndian.PutUint64(request[8:16], uint64(byteCount))
	if _, err := client.WriteToUDP(request, server.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatalf("write udp diag request: %v", err)
	}

	readBuf := make([]byte, packetBytes+64)
	received := int64(0)
	startedAt := time.Now()
	deadline := time.Now().Add(10 * time.Second)
	_ = client.SetReadDeadline(deadline)
	for received < byteCount {
		n, _, err := client.ReadFromUDP(readBuf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
				break
			}
			t.Fatalf("read udp payload: %v", err)
		}
		received += int64(n)
	}
	clientDuration := time.Since(startedAt)
	serverErr := <-serverErrCh
	if serverErr != nil {
		t.Fatalf("udp diag server: %v", serverErr)
	}
	sent := atomic.LoadInt64(&serverSent)
	sendDuration := time.Duration(atomic.LoadInt64(&serverDurationNS))
	t.Logf("raw udp loopback diag requested_bytes=%d packet_bytes=%d server_sent=%d server_duration_ms=%d server_rate_mib_s=%.2f client_received=%d client_duration_ms=%d client_rate_mib_s=%.2f loss_bytes=%d", byteCount, packetBytes, sent, probeDurationMilliseconds(sendDuration), bytesPerSecondMiB(sent, sendDuration), received, probeDurationMilliseconds(clientDuration), bytesPerSecondMiB(received, clientDuration), sent-received)
}

func mustResolveProbeChainUDPAddrForTest(t *testing.T, addr string) *net.UDPAddr {
	t.Helper()
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("resolve udp addr %s: %v", addr, err)
	}
	return udpAddr
}

func bytesPerSecondMiB(bytes int64, duration time.Duration) float64 {
	if bytes <= 0 || duration <= 0 {
		return 0
	}
	return float64(bytes) / duration.Seconds() / 1024 / 1024
}

type probeChainQUICLoopbackDiagnosticResult struct {
	bytes    int64
	duration time.Duration
}

func (r probeChainQUICLoopbackDiagnosticResult) rateBPS() int64 {
	if r.duration <= 0 {
		return 0
	}
	return int64(float64(r.bytes) / r.duration.Seconds())
}

func runProbeChainQUICLoopbackDiagnosticStreams(session *probeChainQUICDataPlaneClientSession, streamCount int, byteCount int64) (probeChainQUICLoopbackDiagnosticResult, error) {
	if session == nil {
		return probeChainQUICLoopbackDiagnosticResult{}, errors.New("session is nil")
	}
	if streamCount <= 0 {
		return probeChainQUICLoopbackDiagnosticResult{}, errors.New("stream count must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	startedAt := time.Now()
	var wg sync.WaitGroup
	errCh := make(chan error, streamCount)
	bytesCh := make(chan int64, streamCount)
	for i := 0; i < streamCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			stream, err := session.OpenStream(ctx)
			if err != nil {
				errCh <- fmt.Errorf("open stream %d: %w", index, err)
				return
			}
			defer stream.Close()
			_ = stream.SetWriteDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
			if err := json.NewEncoder(stream).Encode(probeChainTunnelOpenRequest{
				Type:       probeChainRelayModeSpeedTest,
				SpeedBytes: byteCount,
			}); err != nil {
				errCh <- fmt.Errorf("write request %d: %w", index, err)
				return
			}
			_ = stream.SetWriteDeadline(time.Time{})
			n, err := probeChainCopy(io.Discard, io.LimitReader(stream, byteCount))
			if err != nil {
				errCh <- fmt.Errorf("read stream %d: %w", index, err)
				return
			}
			if n != byteCount {
				errCh <- fmt.Errorf("read stream %d: bytes=%d want=%d", index, n, byteCount)
				return
			}
			bytesCh <- n
		}(i)
	}
	wg.Wait()
	close(errCh)
	close(bytesCh)
	for err := range errCh {
		if err != nil {
			return probeChainQUICLoopbackDiagnosticResult{}, err
		}
	}
	totalBytes := int64(0)
	for n := range bytesCh {
		totalBytes += n
	}
	return probeChainQUICLoopbackDiagnosticResult{bytes: totalBytes, duration: time.Since(startedAt)}, nil
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
