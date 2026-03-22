package backend

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go/http3"
)

type probeLinkSession struct {
	mu sync.Mutex

	nodeID   string
	protocol string
	host     string
	port     int

	tcpConn net.Conn
	tcpRd   *bufio.Reader

	httpClient    *http.Client
	httpTransport *http.Transport

	http3Client    *http.Client
	http3Transport *http3.Transport

	closed bool
}

var probeLinkSessionState = struct {
	mu      sync.Mutex
	session *probeLinkSession
}{}

func (a *App) StartProbeLinkSession(nodeID, protocol, host string, port int) (ProbeLinkConnectResult, error) {
	return startProbeLinkSession(nodeID, protocol, host, port)
}

func (a *App) PingProbeLinkSession() (ProbeLinkConnectResult, error) {
	return pingProbeLinkSession()
}

func (a *App) StopProbeLinkSession() (bool, error) {
	stopped, err := stopProbeLinkSession("requested by manager")
	return stopped, err
}

func startProbeLinkSession(nodeID, protocol, host string, port int) (ProbeLinkConnectResult, error) {
	session, err := newProbeLinkSession(nodeID, protocol, host, port)
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}

	var previous *probeLinkSession
	probeLinkSessionState.mu.Lock()
	previous = probeLinkSessionState.session
	probeLinkSessionState.session = session
	probeLinkSessionState.mu.Unlock()
	if previous != nil {
		_ = previous.close("replaced by new session")
	}

	result, err := session.ping()
	if err != nil {
		_, _ = stopProbeLinkSession("start ping failed")
		return ProbeLinkConnectResult{}, err
	}
	return result, nil
}

func pingProbeLinkSession() (ProbeLinkConnectResult, error) {
	probeLinkSessionState.mu.Lock()
	session := probeLinkSessionState.session
	probeLinkSessionState.mu.Unlock()
	if session == nil {
		return ProbeLinkConnectResult{}, fmt.Errorf("probe link session is not started")
	}
	return session.ping()
}

func stopProbeLinkSession(reason string) (bool, error) {
	probeLinkSessionState.mu.Lock()
	session := probeLinkSessionState.session
	probeLinkSessionState.session = nil
	probeLinkSessionState.mu.Unlock()
	if session == nil {
		return false, nil
	}
	return true, session.close(reason)
}

func newProbeLinkSession(nodeID, protocol, host string, port int) (*probeLinkSession, error) {
	normalizedProtocol := normalizeProbeLinkTestProtocol(protocol)
	if normalizedProtocol == "" {
		return nil, fmt.Errorf("protocol must be tcp/https/http3")
	}
	trimmedHost := strings.TrimSpace(host)
	if trimmedHost == "" {
		return nil, fmt.Errorf("host is required")
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("port must be between 1 and 65535")
	}

	session := &probeLinkSession{
		nodeID:   normalizeProbeLinkNodeID(nodeID),
		protocol: normalizedProtocol,
		host:     trimmedHost,
		port:     port,
	}

	switch normalizedProtocol {
	case "tcp":
		target := net.JoinHostPort(trimmedHost, strconv.Itoa(port))
		conn, err := net.DialTimeout("tcp", target, probeLinkTimeout)
		if err != nil {
			return nil, err
		}
		session.tcpConn = conn
		session.tcpRd = bufio.NewReader(conn)
	case "https":
		transport := &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     60 * time.Second,
		}
		session.httpTransport = transport
		session.httpClient = &http.Client{
			Timeout:   probeLinkTimeout,
			Transport: transport,
		}
	case "http3":
		transport := &http3.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS13,
				NextProtos: []string{"h3"},
			},
		}
		session.http3Transport = transport
		session.http3Client = &http.Client{
			Timeout:   probeLinkTimeout,
			Transport: transport,
		}
	}

	return session, nil
}

func (session *probeLinkSession) ping() (ProbeLinkConnectResult, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return ProbeLinkConnectResult{}, fmt.Errorf("probe link session already closed")
	}

	switch session.protocol {
	case "tcp":
		return session.pingTCP()
	case "https":
		return session.pingHTTP(session.httpClient, "https")
	case "http3":
		return session.pingHTTP(session.http3Client, "http3")
	default:
		return ProbeLinkConnectResult{}, fmt.Errorf("unsupported session protocol: %s", session.protocol)
	}
}

func (session *probeLinkSession) pingTCP() (ProbeLinkConnectResult, error) {
	if session.tcpConn == nil || session.tcpRd == nil {
		return ProbeLinkConnectResult{}, fmt.Errorf("tcp session is not initialized")
	}
	target := net.JoinHostPort(session.host, strconv.Itoa(session.port))
	startedAt := time.Now()

	_ = session.tcpConn.SetDeadline(time.Now().Add(probeLinkTimeout))
	nonce := strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := io.WriteString(session.tcpConn, probeLinkTCPPingPrefix+nonce+"\n"); err != nil {
		_ = session.closeLocked("tcp write failed")
		return ProbeLinkConnectResult{}, err
	}

	line, err := session.tcpRd.ReadString('\n')
	if err != nil {
		_ = session.closeLocked("tcp read failed")
		return ProbeLinkConnectResult{}, err
	}
	expected := probeLinkTCPPongPrefix + nonce
	received := strings.TrimSpace(line)
	if received != expected {
		return ProbeLinkConnectResult{}, fmt.Errorf("unexpected tcp pong payload: %s", received)
	}

	return ProbeLinkConnectResult{
		OK:           true,
		NodeID:       session.nodeID,
		EndpointType: "tcp",
		URL:          "tcp://" + target,
		StatusCode:   200,
		Service:      "probe_link_test",
		Version:      "",
		Message:      "tcp ping/pong connected",
		ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
		DurationMS:   time.Since(startedAt).Milliseconds(),
	}, nil
}

func (session *probeLinkSession) pingHTTP(client *http.Client, protocol string) (ProbeLinkConnectResult, error) {
	if client == nil {
		return ProbeLinkConnectResult{}, fmt.Errorf("%s session is not initialized", protocol)
	}
	targetURL, nonce, err := buildProbeLinkTestURL(session.host, session.port)
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}

	startedAt := time.Now()
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ProbeLinkConnectResult{}, fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var data probeLinkTestPingResponse
	if len(strings.TrimSpace(string(body))) > 0 {
		_ = json.Unmarshal(body, &data)
	}

	message := strings.TrimSpace(data.Message)
	if message == "" {
		if protocol == "http3" {
			message = "probe link http3 ping success"
		} else {
			message = "probe link ping success"
		}
	}
	if strings.TrimSpace(nonce) != "" {
		message += ", nonce=" + nonce
	}

	normalizedActual := normalizeProbeLinkNodeID(data.NodeID)
	if session.nodeID != "" && normalizedActual != "" && session.nodeID != normalizedActual {
		message = fmt.Sprintf("%s, but node_id mismatch: expected=%s actual=%s", message, session.nodeID, normalizedActual)
	}

	return ProbeLinkConnectResult{
		OK:           true,
		NodeID:       firstNonEmptyString(normalizedActual, session.nodeID),
		EndpointType: protocol,
		URL:          targetURL,
		StatusCode:   resp.StatusCode,
		Service:      "probe_link_test",
		Version:      "",
		Message:      message,
		ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
		DurationMS:   time.Since(startedAt).Milliseconds(),
	}, nil
}

func (session *probeLinkSession) close(reason string) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.closeLocked(reason)
}

func (session *probeLinkSession) closeLocked(reason string) error {
	_ = reason
	if session.closed {
		return nil
	}
	session.closed = true

	var firstErr error
	if session.tcpConn != nil {
		if err := session.tcpConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		session.tcpConn = nil
		session.tcpRd = nil
	}
	if session.httpTransport != nil {
		session.httpTransport.CloseIdleConnections()
		session.httpTransport = nil
		session.httpClient = nil
	}
	if session.http3Transport != nil {
		if err := session.http3Transport.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		session.http3Transport = nil
		session.http3Client = nil
	}
	return firstErr
}
