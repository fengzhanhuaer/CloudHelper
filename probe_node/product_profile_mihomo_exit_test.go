//go:build mihomo_exit

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/txthinking/socks5"
	"gvisor.dev/gvisor/pkg/tcpip"
)

const probeMihomoPOCDomain = "api.poc.test"

type probeMihomoPOCSOCKSServer struct {
	tcpListener *net.TCPListener
	udpConn     *net.UDPConn
	username    string
	password    string
	targets     map[string]string
	requests    chan string
	done        chan struct{}
	closeOnce   sync.Once
	mu          sync.Mutex
	udpSessions map[string]*probeMihomoPOCUDPRelay
}

type probeMihomoPOCUDPRelay struct {
	clientAddr *net.UDPAddr
	target     string
	conn       *net.UDPConn
}

type probeMihomoPOCPacketConn struct {
	conn   net.Conn
	remote net.Addr
}

func TestMihomoExitProductProfileIsIsolatedAndUsesWorkingDirectories(t *testing.T) {
	profile := activeProbeProductProfile
	if profile.BuildKind != probeBuildKindMihomoExit || profile.ServiceName != "probe_exit_node" || profile.UpgradeAssetPrefix != "cloudhelper-probe-exit-node" {
		t.Fatalf("unexpected mihomo exit profile: %+v", profile)
	}
	if profile.RuntimeLogDir != "log" || profile.DataDir != "data" || profile.TempDir != "temp" {
		t.Fatalf("unexpected mihomo exit directories: %+v", profile)
	}
	if profile.AllowLocalTUNInstall || profile.EnableLocalConsole || profile.EnableLocalProxy || profile.EnableSystemDNS || profile.EnableSyncScheduler || profile.EnableDDNSScheduler || profile.EnableLocalTUNStartupRecovery || profile.EnableVRoutePlatformInterface {
		t.Fatalf("mihomo exit profile enables an ordinary probe capability: %+v", profile)
	}
	workingDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		profile.DataDir:       filepath.Join(workingDir, "data"),
		profile.RuntimeLogDir: filepath.Join(workingDir, "log"),
		profile.TempDir:       filepath.Join(workingDir, "temp"),
	} {
		got, err := resolveProbeProductWorkingPath(name)
		if err != nil || got != want {
			t.Fatalf("working path %s=%q want=%q err=%v", name, got, want, err)
		}
	}
	upgradeDir, err := probeUpgradeWorkspaceBaseDir()
	if err != nil || upgradeDir != filepath.Join(workingDir, "temp") {
		t.Fatalf("upgrade dir=%q want=%q err=%v", upgradeDir, filepath.Join(workingDir, "temp"), err)
	}
	if err := validateProbeProductPlatform("linux", "amd64"); err != nil {
		t.Fatalf("linux amd64 rejected: %v", err)
	}
	if err := validateProbeProductPlatform("linux", "arm64"); err == nil {
		t.Fatal("linux arm64 unexpectedly accepted")
	}
	if err := validateProbeExpectedNodeKind(probeBuildKindMihomoExit); err != nil {
		t.Fatalf("mihomo_exit expected node kind rejected: %v", err)
	}
	if err := validateProbeExpectedNodeKind(probeBuildKindNormal); err == nil {
		t.Fatal("mihomo exit build accepted normal expected node kind")
	}
	if err := validateProbeUpgradeVerifyBuildKind(probeBuildKindMihomoExit); err != nil {
		t.Fatalf("mihomo exit candidate rejected: %v", err)
	}
	if err := validateProbeUpgradeVerifyBuildKind(probeBuildKindNormal); err == nil {
		t.Fatal("mihomo exit candidate accepted normal expected build kind")
	}
}

func TestMihomoExitUpgradeAssetCannotSelectOrdinaryProbe(t *testing.T) {
	assets := []releaseAsset{
		{Name: "cloudhelper-probe-node-linux-amd64", DownloadURL: "https://example.invalid/normal"},
		{Name: "cloudhelper-probe-exit-node-linux-amd64", DownloadURL: "https://example.invalid/exit"},
	}
	selected, err := pickCurrentProbeProductAsset(assets, runtimePlatformInfo{GOOS: "linux", GOARCH: "amd64"})
	if err != nil {
		t.Fatalf("pick exit asset: %v", err)
	}
	if selected.Name != "cloudhelper-probe-exit-node-linux-amd64" {
		t.Fatalf("selected asset=%q", selected.Name)
	}
}

func TestMihomoExitVRouteConfigDoesNotStartPlatformTUN(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeVirtualRouterTUNDataPlaneHooksForTest)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:  true,
		ProbeIPs: []probeVirtualRouterProbeIP{{NodeID: "19", IP: "198.18.0.21"}},
	}, "19")
	waitProbeVirtualRouterLocalInterfaceIPEnsure()
	if stats := probeVirtualRouterTUNDataPlaneStatsSnapshot(); stats.Running {
		t.Fatalf("mihomo exit profile started platform TUN: %+v", stats)
	}
}

func TestMihomoExitSOCKSTCPUDPPreservesDomainAndQUIC(t *testing.T) {
	tcpTarget, closeTCP := startProbeMihomoPOCTCPEcho(t)
	defer closeTCP()
	udpTarget, closeUDP := startProbeMihomoPOCUDPEcho(t)
	defer closeUDP()
	quicTarget, closeQUIC := startProbeMihomoPOCQUICEcho(t)
	defer closeQUIC()

	tcpPort := probeMihomoPOCPort(t, tcpTarget)
	udpPort := probeMihomoPOCPort(t, udpTarget)
	quicPort := probeMihomoPOCPort(t, quicTarget)
	server := startProbeMihomoPOCSOCKSServer(t, "poc-user", "poc-password", map[string]string{
		net.JoinHostPort(probeMihomoPOCDomain, tcpPort):  tcpTarget,
		net.JoinHostPort(probeMihomoPOCDomain, udpPort):  udpTarget,
		net.JoinHostPort(probeMihomoPOCDomain, quicPort): quicTarget,
	})
	defer server.Close()
	setProbeMihomoPOCReadyEnv(t, server.Address(), "poc-user", "poc-password")

	lookupCalls := 0
	oldLookup := probeVirtualRouterExitLookupIPv4
	probeVirtualRouterExitLookupIPv4 = func(string) ([]string, error) {
		lookupCalls++
		return nil, errors.New("business domain must not be resolved by probe_exit_node")
	}
	t.Cleanup(func() { probeVirtualRouterExitLookupIPv4 = oldLookup })

	applyProbeMihomoPOCFakeIPConfig(t)
	tcpTargetValue, _, err := probeVirtualRouterFakeIPTargetFromTransportID(tcpip.AddrFrom4([4]byte{198, 18, 4, 9}), uint16(probeMihomoPOCPortNumber(t, tcpPort)))
	if err != nil || !tcpTargetValue.IsDomain || tcpTargetValue.Host != probeMihomoPOCDomain {
		t.Fatalf("tcp exit target=%+v err=%v", tcpTargetValue, err)
	}
	tcpConn, err := dialProbeVirtualRouterProductExitTCP(tcpTargetValue)
	if err != nil {
		t.Fatalf("dial mihomo socks tcp: %v", err)
	}
	assertProbeMihomoPOCEcho(t, tcpConn, []byte("mihomo-tcp"))
	_ = tcpConn.Close()

	udpTargetValue := probeVirtualRouterExitTarget{Host: probeMihomoPOCDomain, Port: uint16(probeMihomoPOCPortNumber(t, udpPort)), IsDomain: true}
	udpConn, err := dialProbeVirtualRouterProductExitUDP(udpTargetValue)
	if err != nil {
		t.Fatalf("dial mihomo socks udp: %v", err)
	}
	assertProbeMihomoPOCEcho(t, udpConn, []byte("mihomo-udp"))
	_ = udpConn.Close()

	quicTargetValue := probeVirtualRouterExitTarget{Host: probeMihomoPOCDomain, Port: uint16(probeMihomoPOCPortNumber(t, quicPort)), IsDomain: true}
	quicUDP, err := dialProbeVirtualRouterProductExitUDP(quicTargetValue)
	if err != nil {
		t.Fatalf("dial mihomo socks udp for quic: %v", err)
	}
	packetConn := &probeMihomoPOCPacketConn{conn: quicUDP, remote: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: probeMihomoPOCPortNumber(t, quicPort)}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	quicConn, err := quic.Dial(ctx, packetConn, packetConn.remote, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"cloudhelper-mihomo-poc"}}, nil)
	if err != nil {
		t.Fatalf("quic dial through socks udp: %v", err)
	}
	stream, err := quicConn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("quic open stream: %v", err)
	}
	assertProbeMihomoPOCEcho(t, stream, []byte("mihomo-quic"))
	_ = stream.Close()
	_ = quicConn.CloseWithError(0, "poc complete")
	_ = packetConn.Close()

	wantRequests := []string{
		"tcp|" + tcpTargetValue.Address(),
		"udp|" + udpTargetValue.Address(),
		"udp|" + quicTargetValue.Address(),
	}
	gotRequests := server.WaitRequests(t, len(wantRequests))
	if !reflect.DeepEqual(gotRequests, wantRequests) {
		t.Fatalf("socks requests=%v want=%v", gotRequests, wantRequests)
	}
	if lookupCalls != 0 {
		t.Fatalf("probe resolved business domain %d times", lookupCalls)
	}
}

func TestMihomoExitReadinessFailsClosed(t *testing.T) {
	setProbeMihomoPOCReadyEnv(t, "127.0.0.1:9", "poc-user", "poc-password")
	t.Setenv(probeMihomoExitAppliedRevisionEnv, "6")
	target := probeVirtualRouterExitTarget{Host: probeMihomoPOCDomain, Port: 443, IsDomain: true}
	if _, err := dialProbeVirtualRouterProductExitTCP(target); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("revision mismatch did not fail closed: %v", err)
	}
	t.Setenv(probeMihomoExitAppliedRevisionEnv, "7")
	t.Setenv(probeMihomoExitHealthyEnv, "false")
	if _, err := dialProbeVirtualRouterProductExitUDP(target); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("unhealthy runtime did not fail closed: %v", err)
	}
	t.Setenv(probeMihomoExitHealthyEnv, "true")
	t.Setenv(probeMihomoExitSOCKSAddressEnv, "192.0.2.10:1080")
	if _, err := dialProbeVirtualRouterProductExitTCP(target); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback socks endpoint accepted: %v", err)
	}
}

func TestMihomoExitOfficialBinaryTCPUDPQUIC(t *testing.T) {
	binaryPath := strings.TrimSpace(os.Getenv("PROBE_MIHOMO_POC_BINARY"))
	if binaryPath == "" {
		t.Skip("PROBE_MIHOMO_POC_BINARY is not configured")
	}
	if _, err := os.Stat(binaryPath); err != nil {
		t.Fatalf("stat mihomo binary: %v", err)
	}

	tcpTarget, closeTCP := startProbeMihomoPOCTCPEcho(t)
	defer closeTCP()
	udpTarget, closeUDP := startProbeMihomoPOCUDPEcho(t)
	defer closeUDP()
	quicTarget, closeQUIC := startProbeMihomoPOCQUICEcho(t)
	defer closeQUIC()
	mihomoAddress := reserveProbeMihomoPOCTCPUDPAddress(t)
	homeDir := t.TempDir()
	configPath := filepath.Join(homeDir, "config.yaml")
	_, socksPort, err := net.SplitHostPort(mihomoAddress)
	if err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("mode: rule\nlog-level: warning\nipv6: false\nlisteners:\n  - name: cloudhelper-poc\n    type: socks\n    port: %s\n    listen: 127.0.0.1\n    udp: true\n    users:\n      - username: poc-user\n        password: poc-password\nrules:\n  - MATCH,DIRECT\n", socksPort)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write mihomo config: %v", err)
	}

	var processLog bytes.Buffer
	command := exec.Command(binaryPath, "-d", homeDir, "-f", configPath)
	command.Stdout = &processLog
	command.Stderr = &processLog
	if err := command.Start(); err != nil {
		t.Fatalf("start mihomo: %v", err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- command.Wait() }()
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		select {
		case <-processDone:
		case <-time.After(3 * time.Second):
		}
	}()
	if err := waitProbeMihomoPOCTCPReady(mihomoAddress, processDone, 8*time.Second); err != nil {
		t.Fatalf("mihomo did not become ready: %v\n%s", err, processLog.String())
	}
	setProbeMihomoPOCReadyEnv(t, mihomoAddress, "poc-user", "poc-password")

	tcpTargetValue := probeVirtualRouterExitTarget{Host: "localhost", Port: uint16(probeMihomoPOCPortNumber(t, probeMihomoPOCPort(t, tcpTarget))), IsDomain: true}
	tcpConn, err := dialProbeVirtualRouterProductExitTCP(tcpTargetValue)
	if err != nil {
		t.Fatalf("official mihomo tcp dial: %v\n%s", err, processLog.String())
	}
	assertProbeMihomoPOCEcho(t, tcpConn, []byte("official-mihomo-tcp"))
	_ = tcpConn.Close()

	udpTargetValue := probeVirtualRouterExitTarget{Host: "localhost", Port: uint16(probeMihomoPOCPortNumber(t, probeMihomoPOCPort(t, udpTarget))), IsDomain: true}
	udpConn, err := dialProbeVirtualRouterProductExitUDP(udpTargetValue)
	if err != nil {
		t.Fatalf("official mihomo udp dial: %v\n%s", err, processLog.String())
	}
	assertProbeMihomoPOCEcho(t, udpConn, []byte("official-mihomo-udp"))
	_ = udpConn.Close()

	quicTargetValue := probeVirtualRouterExitTarget{Host: "localhost", Port: uint16(probeMihomoPOCPortNumber(t, probeMihomoPOCPort(t, quicTarget))), IsDomain: true}
	quicUDP, err := dialProbeVirtualRouterProductExitUDP(quicTargetValue)
	if err != nil {
		t.Fatalf("official mihomo quic udp dial: %v\n%s", err, processLog.String())
	}
	packetConn := &probeMihomoPOCPacketConn{conn: quicUDP, remote: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: probeMihomoPOCPortNumber(t, probeMihomoPOCPort(t, quicTarget))}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	quicConn, err := quic.Dial(ctx, packetConn, packetConn.remote, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"cloudhelper-mihomo-poc"}}, nil)
	if err != nil {
		t.Fatalf("official mihomo quic dial: %v\n%s", err, processLog.String())
	}
	stream, err := quicConn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("official mihomo quic stream: %v", err)
	}
	assertProbeMihomoPOCEcho(t, stream, []byte("official-mihomo-quic"))
	_ = stream.Close()
	_ = quicConn.CloseWithError(0, "poc complete")
	_ = packetConn.Close()
}

func TestMihomoExitICMPDoesNotResolveOrUsePhysicalExit(t *testing.T) {
	lookupCalls := 0
	oldLookup := probeVirtualRouterExitLookupIPv4
	probeVirtualRouterExitLookupIPv4 = func(string) ([]string, error) {
		lookupCalls++
		return []string{"203.0.113.10"}, nil
	}
	sendCalls := 0
	oldSend := probeVirtualRouterSendICMPEcho
	probeVirtualRouterSendICMPEcho = func(string, []byte, time.Duration) ([]byte, error) {
		sendCalls++
		return nil, nil
	}
	t.Cleanup(func() {
		probeVirtualRouterExitLookupIPv4 = oldLookup
		probeVirtualRouterSendICMPEcho = oldSend
	})
	packet := buildProbeVirtualRouterTestICMPEchoRequest(t, "198.18.0.18", "198.18.4.9")
	handled := handleProbeVirtualRouterFakeIPExitICMPEchoRequest(nil, nil, packet, []string{"16", "19"}, probeVirtualRouterFakeIPEntry{Domain: probeMihomoPOCDomain, FakeIP: "198.18.4.9", Action: "probe_exit", ExitNodeID: "19"})
	if !handled || lookupCalls != 0 || sendCalls != 0 {
		t.Fatalf("icmp handled=%v lookup_calls=%d send_calls=%d", handled, lookupCalls, sendCalls)
	}
}

func startProbeMihomoPOCSOCKSServer(t *testing.T, username string, password string, targets map[string]string) *probeMihomoPOCSOCKSServer {
	t.Helper()
	tcpListener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	port := tcpListener.Addr().(*net.TCPAddr).Port
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		_ = tcpListener.Close()
		t.Fatal(err)
	}
	server := &probeMihomoPOCSOCKSServer{
		tcpListener: tcpListener,
		udpConn:     udpConn,
		username:    username,
		password:    password,
		targets:     targets,
		requests:    make(chan string, 16),
		done:        make(chan struct{}),
		udpSessions: make(map[string]*probeMihomoPOCUDPRelay),
	}
	go server.serveTCP()
	go server.serveUDP()
	return server
}

func reserveProbeMihomoPOCTCPUDPAddress(t *testing.T) string {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			continue
		}
		address := udpConn.LocalAddr().String()
		tcpAddr, _ := net.ResolveTCPAddr("tcp4", address)
		tcpListener, err := net.ListenTCP("tcp4", tcpAddr)
		if err != nil {
			_ = udpConn.Close()
			continue
		}
		_ = tcpListener.Close()
		_ = udpConn.Close()
		return address
	}
	t.Fatal("failed to reserve mihomo tcp/udp address")
	return ""
}

func waitProbeMihomoPOCTCPReady(address string, processDone <-chan error, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-processDone:
			if err == nil {
				return errors.New("mihomo exited before readiness")
			}
			return err
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		case <-deadline.C:
			return errors.New("mihomo readiness timeout")
		}
	}
}

func (s *probeMihomoPOCSOCKSServer) Address() string { return s.tcpListener.Addr().String() }

func (s *probeMihomoPOCSOCKSServer) serveTCP() {
	for {
		conn, err := s.tcpListener.AcceptTCP()
		if err != nil {
			return
		}
		go s.handleTCP(conn)
	}
}

func (s *probeMihomoPOCSOCKSServer) handleTCP(conn *net.TCPConn) {
	defer conn.Close()
	protocol := &socks5.Server{UserName: s.username, Password: s.password, Method: socks5.MethodUsernamePassword, SupportedCommands: []byte{socks5.CmdConnect, socks5.CmdUDP}}
	if err := protocol.Negotiate(conn); err != nil {
		return
	}
	request, err := protocol.GetRequest(conn)
	if err != nil {
		return
	}
	switch request.Cmd {
	case socks5.CmdConnect:
		target := request.Address()
		s.requests <- "tcp|" + target
		mapped := s.targets[target]
		remote, err := net.DialTimeout("tcp4", mapped, 3*time.Second)
		if err != nil {
			_, _ = socks5.NewReply(socks5.RepHostUnreachable, socks5.ATYPIPv4, []byte{0, 0, 0, 0}, []byte{0, 0}).WriteTo(conn)
			return
		}
		defer remote.Close()
		_, _ = socks5.NewReply(socks5.RepSuccess, socks5.ATYPIPv4, []byte{127, 0, 0, 1}, []byte{0, 0}).WriteTo(conn)
		probeMihomoPOCPipe(conn, remote)
	case socks5.CmdUDP:
		addr := s.udpConn.LocalAddr().(*net.UDPAddr)
		port := []byte{byte(addr.Port >> 8), byte(addr.Port)}
		_, _ = socks5.NewReply(socks5.RepSuccess, socks5.ATYPIPv4, []byte{127, 0, 0, 1}, port).WriteTo(conn)
		_, _ = io.Copy(io.Discard, conn)
	}
}

func (s *probeMihomoPOCSOCKSServer) serveUDP() {
	buffer := make([]byte, 65535)
	for {
		n, clientAddr, err := s.udpConn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		datagram, err := socks5.NewDatagramFromBytes(buffer[:n])
		if err != nil || datagram.Frag != 0 {
			continue
		}
		target := datagram.Address()
		session, err := s.udpRelay(clientAddr, target)
		if err != nil {
			continue
		}
		_, _ = session.conn.Write(datagram.Data)
	}
}

func (s *probeMihomoPOCSOCKSServer) udpRelay(clientAddr *net.UDPAddr, target string) (*probeMihomoPOCUDPRelay, error) {
	key := clientAddr.String() + "|" + target
	s.mu.Lock()
	if current := s.udpSessions[key]; current != nil {
		s.mu.Unlock()
		return current, nil
	}
	mapped := s.targets[target]
	remoteAddr, err := net.ResolveUDPAddr("udp4", mapped)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	conn, err := net.DialUDP("udp4", nil, remoteAddr)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	relay := &probeMihomoPOCUDPRelay{clientAddr: cloneProbeVRouteProxyUDPAddr(clientAddr), target: target, conn: conn}
	s.udpSessions[key] = relay
	s.requests <- "udp|" + target
	s.mu.Unlock()
	go s.readUDPResponses(relay)
	return relay, nil
}

func (s *probeMihomoPOCSOCKSServer) readUDPResponses(relay *probeMihomoPOCUDPRelay) {
	buffer := make([]byte, 65535)
	for {
		n, err := relay.conn.Read(buffer)
		if err != nil {
			return
		}
		atyp, addr, port, err := socks5.ParseAddress(relay.target)
		if err != nil {
			return
		}
		if atyp == socks5.ATYPDomain {
			addr = addr[1:]
		}
		response := socks5.NewDatagram(atyp, addr, port, append([]byte(nil), buffer[:n]...))
		_, _ = s.udpConn.WriteToUDP(response.Bytes(), relay.clientAddr)
	}
}

func (s *probeMihomoPOCSOCKSServer) WaitRequests(t *testing.T, count int) []string {
	t.Helper()
	requests := make([]string, 0, count)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(requests) < count {
		select {
		case request := <-s.requests:
			requests = append(requests, request)
		case <-deadline.C:
			t.Fatalf("timed out waiting for socks requests: %v", requests)
		}
	}
	return requests
}

func (s *probeMihomoPOCSOCKSServer) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.tcpListener.Close()
		_ = s.udpConn.Close()
		s.mu.Lock()
		for _, relay := range s.udpSessions {
			_ = relay.conn.Close()
		}
		s.udpSessions = nil
		s.mu.Unlock()
	})
}

func probeMihomoPOCPipe(left net.Conn, right net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(left, right); done <- struct{}{} }()
	go func() { _, _ = io.Copy(right, left); done <- struct{}{} }()
	<-done
}

func (c *probeMihomoPOCPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.conn.Read(p)
	return n, c.remote, err
}
func (c *probeMihomoPOCPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) { return c.conn.Write(p) }
func (c *probeMihomoPOCPacketConn) Close() error                              { return c.conn.Close() }
func (c *probeMihomoPOCPacketConn) LocalAddr() net.Addr                       { return c.conn.LocalAddr() }
func (c *probeMihomoPOCPacketConn) SetDeadline(value time.Time) error {
	return c.conn.SetDeadline(value)
}
func (c *probeMihomoPOCPacketConn) SetReadDeadline(value time.Time) error {
	return c.conn.SetReadDeadline(value)
}
func (c *probeMihomoPOCPacketConn) SetWriteDeadline(value time.Time) error {
	return c.conn.SetWriteDeadline(value)
}

func setProbeMihomoPOCReadyEnv(t *testing.T, address string, username string, password string) {
	t.Helper()
	t.Setenv(probeMihomoExitPOCModeEnv, "true")
	hash := strings.Repeat("ab", 32)
	t.Setenv(probeMihomoExitSOCKSAddressEnv, address)
	t.Setenv(probeMihomoExitSOCKSUsernameEnv, username)
	t.Setenv(probeMihomoExitSOCKSPasswordEnv, password)
	t.Setenv(probeMihomoExitDesiredRevisionEnv, "7")
	t.Setenv(probeMihomoExitAppliedRevisionEnv, "7")
	t.Setenv(probeMihomoExitDesiredSHA256Env, hash)
	t.Setenv(probeMihomoExitAppliedSHA256Env, hash)
	t.Setenv(probeMihomoExitHealthyEnv, "true")
}

func applyProbeMihomoPOCFakeIPConfig(t *testing.T) {
	t.Helper()
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	applyProbeVirtualRouterConfigForNode(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs:   []probeVirtualRouterProbeIP{{NodeID: "19", IP: "198.18.0.21"}},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{Items: []probeVirtualRouterFakeIPEntry{{
			Domain: probeMihomoPOCDomain, FakeIP: "198.18.4.9", Action: "probe_exit", ExitNodeID: "19",
		}}},
	}, "19")
}

func startProbeMihomoPOCTCPEcho(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	return listener.Addr().String(), func() { _ = listener.Close() }
}

func startProbeMihomoPOCUDPEcho(t *testing.T) (string, func()) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, addr, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(buffer[:n], addr)
		}
	}()
	return conn.LocalAddr().String(), func() { _ = conn.Close() }
}

func startProbeMihomoPOCQUICEcho(t *testing.T) (string, func()) {
	t.Helper()
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := quic.Listen(udpConn, probeMihomoPOCTestTLSConfig(t), nil)
	if err != nil {
		_ = udpConn.Close()
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return
			}
			go func() {
				stream, err := conn.AcceptStream(context.Background())
				if err == nil {
					_, _ = io.Copy(stream, stream)
					_ = stream.Close()
				}
			}()
		}
	}()
	return udpConn.LocalAddr().String(), func() { _ = listener.Close(); _ = udpConn.Close() }
}

func probeMihomoPOCTestTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}, NextProtos: []string{"cloudhelper-mihomo-poc"}}
}

func assertProbeMihomoPOCEcho(t *testing.T, conn interface {
	io.Reader
	io.Writer
}, payload []byte) {
	t.Helper()
	if deadlineConn, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = deadlineConn.SetDeadline(time.Now().Add(5 * time.Second))
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("echo write: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(bufio.NewReader(conn), got); err != nil {
		t.Fatalf("echo read: %v", err)
	}
	if !reflect.DeepEqual(got, payload) {
		t.Fatalf("echo=%q want=%q", got, payload)
	}
}

func probeMihomoPOCPort(t *testing.T, address string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func probeMihomoPOCPortNumber(t *testing.T, port string) int {
	t.Helper()
	number, err := net.LookupPort("udp", port)
	if err != nil {
		t.Fatal(err)
	}
	return number
}
