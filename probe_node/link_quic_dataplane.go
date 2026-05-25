package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

const (
	probeChainQUICDataPlaneALPN            = "probe-quic/1"
	probeChainQUICDataPlaneProtocolVersion = 1
	probeChainQUICDataPlanePortOffset      = 1
	probeChainQUICDataPlaneControlTimeout  = 10 * time.Second
)

type probeChainQUICDataPlaneControlFrame struct {
	Type               string                  `json:"type"`
	ProtocolVersion    int                     `json:"protocol_version"`
	RequestID          string                  `json:"request_id,omitempty"`
	ChainID            string                  `json:"chain_id,omitempty"`
	BridgeRole         string                  `json:"bridge_role,omitempty"`
	Auth               *probeChainAuthEnvelope `json:"auth,omitempty"`
	OK                 bool                    `json:"ok,omitempty"`
	Error              string                  `json:"error,omitempty"`
	QUICVersion        string                  `json:"quic_version,omitempty"`
	DatagramSupported  bool                    `json:"datagram_supported,omitempty"`
	MaxDatagramPayload int                     `json:"max_datagram_payload,omitempty"`
	MaxIncomingStreams int64                   `json:"max_incoming_streams,omitempty"`
	ServerTime         string                  `json:"server_time,omitempty"`
}

type probeChainQUICDataPlaneClientSession struct {
	conn       *quic.Conn
	control    *quic.Stream
	transport  *quic.Transport
	packetConn *net.UDPConn
	bridgeRole string
	remoteAddr net.Addr
	localAddr  net.Addr
	closeOnce  sync.Once
}

type probeChainQUICStreamNetConn struct {
	stream *quic.Stream
	local  net.Addr
	remote net.Addr
	once   sync.Once
}

type probeChainQUICDataPlaneControlNetConn struct {
	session *probeChainQUICDataPlaneClientSession
}

func isProbeChainQUICDataPlaneLayer(layer string) bool {
	switch normalizeProbeChainLinkLayer(layer) {
	case "quic-stream", "http3":
		return true
	default:
		return false
	}
}

func probeChainQUICDataPlaneListenPort(basePort int) (int, error) {
	if basePort <= 0 || basePort > 65535 {
		return 0, fmt.Errorf("invalid relay port: %d", basePort)
	}
	if basePort+probeChainQUICDataPlanePortOffset > 65535 {
		return 0, fmt.Errorf("quic data plane port overflow: base=%d offset=%d", basePort, probeChainQUICDataPlanePortOffset)
	}
	return basePort + probeChainQUICDataPlanePortOffset, nil
}

func probeChainQUICVersionString(v quic.Version) string {
	switch v {
	case quic.Version2:
		return "v2"
	case quic.Version1:
		return "v1"
	default:
		if v == 0 {
			return ""
		}
		return fmt.Sprintf("0x%x", uint32(v))
	}
}

func startProbeChainQUICDataPlaneServer(runtime *probeChainRuntime, cert probeServerCertificate) error {
	if runtime == nil {
		return errors.New("runtime is nil")
	}
	port, err := probeChainQUICDataPlaneListenPort(runtime.cfg.listenPort)
	if err != nil {
		return err
	}
	listenAddr := net.JoinHostPort(runtime.cfg.listenHost, strconv.Itoa(port))
	tlsCert, err := tlsLoadProbeChainCertificate(cert)
	if err != nil {
		return err
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{probeChainQUICDataPlaneALPN},
	}
	udpAddr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return err
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	tuneProbeChainUDPConn(udpConn)
	ln, err := quic.Listen(udpConn, tlsConf, newProbeChainQUICConfig(probeChainRelayQUICMaxIncomingStreams))
	if err != nil {
		_ = udpConn.Close()
		return err
	}
	runtime.quicDataPlaneListener = ln
	runtime.quicDataPlanePacketConn = udpConn
	markProbeChainRelayListenerStatus(net.JoinHostPort(runtime.cfg.listenHost, strconv.Itoa(runtime.cfg.listenPort)), "quic-stream", "listening", "")
	go acceptProbeChainQUICDataPlaneConnections(runtime, ln)
	return nil
}

func tlsLoadProbeChainCertificate(cert probeServerCertificate) (tls.Certificate, error) {
	return tls.LoadX509KeyPair(cert.CertPath, cert.KeyPath)
}

func acceptProbeChainQUICDataPlaneConnections(runtime *probeChainRuntime, ln *quic.Listener) {
	if runtime == nil || ln == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-runtime.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			select {
			case <-runtime.stopCh:
				return
			default:
			}
			log.Printf("probe chain quic dataplane accept failed: chain=%s err=%v", runtime.cfg.chainID, err)
			return
		}
		go handleProbeChainQUICDataPlaneConn(runtime, conn)
	}
}

func handleProbeChainQUICDataPlaneConn(runtime *probeChainRuntime, conn *quic.Conn) {
	if runtime == nil || conn == nil {
		return
	}
	remote := ""
	if conn.RemoteAddr() != nil {
		remote = conn.RemoteAddr().String()
	}
	control, err := conn.AcceptStream(context.Background())
	if err != nil {
		log.Printf("probe chain quic dataplane control accept failed: chain=%s remote=%s err=%v", runtime.cfg.chainID, remote, err)
		_ = conn.CloseWithError(1, "control accept failed")
		return
	}
	_ = control.SetDeadline(time.Now().Add(probeChainQUICDataPlaneControlTimeout))
	var frame probeChainQUICDataPlaneControlFrame
	if err := json.NewDecoder(control).Decode(&frame); err != nil {
		_ = writeProbeChainQUICControlFrame(control, probeChainQUICDataPlaneControlFrame{Type: "auth_response", OK: false, Error: err.Error()})
		_ = conn.CloseWithError(2, "control decode failed")
		return
	}
	if err := verifyProbeChainQUICDataPlaneControl(runtime, conn, frame); err != nil {
		_ = writeProbeChainQUICControlFrame(control, probeChainQUICDataPlaneControlFrame{Type: "auth_response", OK: false, Error: err.Error()})
		log.Printf("probe chain quic dataplane auth failed: chain=%s remote=%s err=%v", runtime.cfg.chainID, remote, err)
		_ = conn.CloseWithError(3, "auth failed")
		return
	}
	state := conn.ConnectionState()
	role := normalizeProbeChainBridgeRole(frame.BridgeRole)
	if err := writeProbeChainQUICControlFrame(control, probeChainQUICDataPlaneControlFrame{
		Type:               "auth_response",
		ProtocolVersion:    probeChainQUICDataPlaneProtocolVersion,
		RequestID:          strings.TrimSpace(frame.RequestID),
		OK:                 true,
		QUICVersion:        probeChainQUICVersionString(state.Version),
		DatagramSupported:  state.SupportsDatagrams,
		MaxDatagramPayload: probeChainRelayQUICDatagramMaxPayloadBytes,
		MaxIncomingStreams: probeChainRelayQUICMaxIncomingStreams,
		ServerTime:         time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		_ = conn.CloseWithError(4, "auth response failed")
		return
	}
	_ = control.SetDeadline(time.Time{})
	log.Printf("probe chain quic dataplane connected: chain=%s role=%s bridge_role=%s remote=%s version=%s datagram=%t", runtime.cfg.chainID, runtime.cfg.role, role, remote, probeChainQUICVersionString(state.Version), state.SupportsDatagrams)
	acceptProbeChainQUICDataPlaneStreams(runtime, conn, role, remote)
}

func verifyProbeChainQUICDataPlaneControl(runtime *probeChainRuntime, conn *quic.Conn, frame probeChainQUICDataPlaneControlFrame) error {
	if runtime == nil {
		return errors.New("runtime is nil")
	}
	if frame.ProtocolVersion != probeChainQUICDataPlaneProtocolVersion {
		return fmt.Errorf("unsupported quic dataplane protocol version: %d", frame.ProtocolVersion)
	}
	if strings.TrimSpace(frame.ChainID) == "" {
		return errors.New("chain_id is required")
	}
	if strings.TrimSpace(frame.ChainID) != strings.TrimSpace(runtime.cfg.chainID) {
		return errors.New("chain id mismatch")
	}
	if frame.Auth == nil {
		return errors.New("auth is required")
	}
	return verifyProbeChainInboundAuth(runtime.cfg, *frame.Auth)
}

func writeProbeChainQUICControlFrame(stream *quic.Stream, frame probeChainQUICDataPlaneControlFrame) error {
	if stream == nil {
		return errors.New("control stream is nil")
	}
	_ = stream.SetWriteDeadline(time.Now().Add(probeChainQUICDataPlaneControlTimeout))
	err := json.NewEncoder(stream).Encode(frame)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}

func acceptProbeChainQUICDataPlaneStreams(runtime *probeChainRuntime, conn *quic.Conn, bridgeRole string, remote string) {
	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
				log.Printf("probe chain quic dataplane stream accept closed: chain=%s remote=%s err=%v", runtime.cfg.chainID, remote, err)
			}
			return
		}
		netConn := &probeChainQUICStreamNetConn{stream: stream, local: conn.LocalAddr(), remote: conn.RemoteAddr()}
		if normalizeProbeChainBridgeRole(bridgeRole) == probeChainBridgeRoleToPrev {
			go handleProbeChainReverseConn(runtime, netConn, "")
		} else {
			go handleProbeChainConn(runtime, netConn, "")
		}
	}
}

func openProbeChainRelayQUICDataPlaneSession(chainID string, secret string, relayHost string, relayPort int, bridgeRole string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (*probeChainQUICDataPlaneClientSession, error) {
	if openTimeout <= 0 {
		openTimeout = probeChainPortForwardDialTimeout + probeChainPortForwardResponseReadDeadline
	}
	port, err := probeChainQUICDataPlaneListenPort(relayPort)
	if err != nil {
		return nil, err
	}
	startedAt := time.Now()
	dialHostPort := net.JoinHostPort(strings.TrimSpace(relayDialHost), strconv.Itoa(port))
	udpAddr, err := net.ResolveUDPAddr("udp", dialHostPort)
	if err != nil {
		return nil, err
	}
	packetConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	tuneProbeChainUDPConn(packetConn)
	transport := &quic.Transport{Conn: packetConn}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	tlsConf := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{probeChainQUICDataPlaneALPN},
		ServerName:         resolveProbeChainClientTLSServerName("quic-stream", relayDialHost, relayHostHeader),
		InsecureSkipVerify: true,
	}
	logProbeChainRelayDialAttempt("quic-stream", chainID, "quic-stream", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, openTimeout)
	conn, err := transport.Dial(ctx, udpAddr, tlsConf, newProbeChainQUICConfig(0))
	if err != nil {
		_ = transport.Close()
		_ = packetConn.Close()
		wrappedErr := wrapProbeChainRelayDialError("quic-stream", relayDialHost, port, err)
		logProbeChainRelayDialOutcome("quic-stream", chainID, "quic-stream", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), wrappedErr)
		return nil, wrappedErr
	}
	control, err := conn.OpenStreamSync(ctx)
	if err != nil {
		_ = conn.CloseWithError(1, "control open failed")
		_ = transport.Close()
		_ = packetConn.Close()
		return nil, err
	}
	nonce := randomHexToken(16)
	auth := newProbeChainAuthEnvelope("secret_hmac", chainID, nonce, "", buildProbeChainHMAC(secret, chainID, nonce))
	requestID := randomHexToken(8)
	if err := writeProbeChainQUICControlFrame(control, probeChainQUICDataPlaneControlFrame{
		Type:            "auth",
		ProtocolVersion: probeChainQUICDataPlaneProtocolVersion,
		RequestID:       requestID,
		ChainID:         strings.TrimSpace(chainID),
		BridgeRole:      normalizeProbeChainBridgeRole(bridgeRole),
		Auth:            &auth,
	}); err != nil {
		_ = conn.CloseWithError(2, "control auth failed")
		_ = transport.Close()
		_ = packetConn.Close()
		return nil, err
	}
	_ = control.SetReadDeadline(time.Now().Add(probeChainQUICDataPlaneControlTimeout))
	var response probeChainQUICDataPlaneControlFrame
	if err := json.NewDecoder(control).Decode(&response); err != nil {
		_ = conn.CloseWithError(3, "control response failed")
		_ = transport.Close()
		_ = packetConn.Close()
		return nil, err
	}
	_ = control.SetReadDeadline(time.Time{})
	if !response.OK {
		_ = conn.CloseWithError(4, "auth rejected")
		_ = transport.Close()
		_ = packetConn.Close()
		return nil, errors.New(firstNonEmpty(strings.TrimSpace(response.Error), "quic dataplane auth rejected"))
	}
	if cacheOnSuccess {
		refreshProbeChainRelayResolveCacheOnConnectSuccess(relayHost, relayDialHost, relayHostHeader)
	}
	state := conn.ConnectionState()
	logProbeChainRelayDialOutcome("quic-stream", chainID, "quic-stream", relayHost, relayPort, relayDialHost, relayHostHeader, bridgeRole, time.Since(startedAt), nil)
	log.Printf("probe chain quic dataplane session ready: chain=%s relay=%s:%d dataplane_port=%d version=%s datagram=%t", strings.TrimSpace(chainID), strings.TrimSpace(relayHost), relayPort, port, probeChainQUICVersionString(state.Version), state.SupportsDatagrams)
	return &probeChainQUICDataPlaneClientSession{
		conn:       conn,
		control:    control,
		transport:  transport,
		packetConn: packetConn,
		bridgeRole: normalizeProbeChainBridgeRole(bridgeRole),
		localAddr:  conn.LocalAddr(),
		remoteAddr: conn.RemoteAddr(),
	}, nil
}

func (s *probeChainQUICDataPlaneClientSession) OpenStream(ctx context.Context) (net.Conn, error) {
	if s == nil || s.conn == nil {
		return nil, errors.New("quic dataplane session is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stream, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &probeChainQUICStreamNetConn{stream: stream, local: s.localAddr, remote: s.remoteAddr}, nil
}

func (s *probeChainQUICDataPlaneClientSession) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.closeOnce.Do(func() {
		if s.control != nil {
			s.control.CancelRead(0)
			s.control.CancelWrite(0)
		}
		if s.conn != nil {
			err = s.conn.CloseWithError(0, "closed")
		}
		if s.transport != nil {
			_ = s.transport.Close()
		}
		if s.packetConn != nil {
			_ = s.packetConn.Close()
		}
	})
	return err
}

func (s *probeChainQUICDataPlaneClientSession) IsClosed() bool {
	if s == nil || s.conn == nil {
		return true
	}
	select {
	case <-s.conn.Context().Done():
		return true
	default:
		return false
	}
}

func (c *probeChainQUICStreamNetConn) Read(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, io.EOF
	}
	return c.stream.Read(payload)
}

func (c *probeChainQUICStreamNetConn) Write(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, net.ErrClosed
	}
	return c.stream.Write(payload)
}

func (c *probeChainQUICStreamNetConn) Close() error {
	if c == nil || c.stream == nil {
		return nil
	}
	var err error
	c.once.Do(func() {
		c.stream.CancelRead(0)
		err = c.stream.Close()
	})
	return err
}

func (c *probeChainQUICStreamNetConn) LocalAddr() net.Addr {
	if c == nil || c.local == nil {
		return probeChainRelayNetAddr{label: "probe-chain-quic-local"}
	}
	return c.local
}

func (c *probeChainQUICStreamNetConn) RemoteAddr() net.Addr {
	if c == nil || c.remote == nil {
		return probeChainRelayNetAddr{label: "probe-chain-quic-remote"}
	}
	return c.remote
}

func (c *probeChainQUICStreamNetConn) SetDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetDeadline(t)
}

func (c *probeChainQUICStreamNetConn) SetReadDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetReadDeadline(t)
}

func (c *probeChainQUICStreamNetConn) SetWriteDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetWriteDeadline(t)
}

func (c *probeChainQUICDataPlaneControlNetConn) Read(_ []byte) (int, error) {
	return 0, errors.New("quic dataplane control connection does not carry bulk data")
}

func (c *probeChainQUICDataPlaneControlNetConn) Write(_ []byte) (int, error) {
	return 0, errors.New("quic dataplane control connection does not carry bulk data")
}

func (c *probeChainQUICDataPlaneControlNetConn) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	return c.session.Close()
}

func (c *probeChainQUICDataPlaneControlNetConn) LocalAddr() net.Addr {
	if c == nil || c.session == nil || c.session.localAddr == nil {
		return probeChainRelayNetAddr{label: "probe-chain-quic-control-local"}
	}
	return c.session.localAddr
}

func (c *probeChainQUICDataPlaneControlNetConn) RemoteAddr() net.Addr {
	if c == nil || c.session == nil || c.session.remoteAddr == nil {
		return probeChainRelayNetAddr{label: "probe-chain-quic-control-remote"}
	}
	return c.session.remoteAddr
}

func (c *probeChainQUICDataPlaneControlNetConn) SetDeadline(time.Time) error {
	return nil
}

func (c *probeChainQUICDataPlaneControlNetConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *probeChainQUICDataPlaneControlNetConn) SetWriteDeadline(time.Time) error {
	return nil
}

func openProbeChainQUICProxyStream(session *probeChainQUICDataPlaneClientSession, network string, targetAddr string, associationV2 *probeChainAssociationV2Meta) (net.Conn, error) {
	if session == nil {
		return nil, errors.New("quic dataplane session is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeChainDownstreamOpenTimeout)
	defer cancel()
	stream, err := session.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	request := probeChainTunnelOpenRequest{
		Type:          "open",
		Network:       strings.ToLower(strings.TrimSpace(network)),
		Address:       strings.TrimSpace(targetAddr),
		AssociationV2: associationV2,
	}
	if request.Network == "" {
		request.Network = probeChainPortForwardNetworkTCP
	}
	_ = stream.SetWriteDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	if err := json.NewEncoder(stream).Encode(request); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetWriteDeadline(time.Time{})
	_ = stream.SetReadDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	var response probeChainTunnelOpenResponse
	if err := json.NewDecoder(stream).Decode(&response); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetReadDeadline(time.Time{})
	if !response.OK {
		_ = stream.Close()
		return nil, errors.New(firstNonEmpty(strings.TrimSpace(response.Error), "open quic stream failed"))
	}
	return stream, nil
}
