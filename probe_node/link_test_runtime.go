package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go/http3"
)

const probeLinkTestPingAPIPath = "/api/node/link-test/ping"

type probeLinkTestControlResultPayload struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	NodeID       string `json:"node_id"`
	OK           bool   `json:"ok"`
	Action       string `json:"action,omitempty"`
	Protocol     string `json:"protocol,omitempty"`
	ListenHost   string `json:"listen_host,omitempty"`
	InternalPort int    `json:"internal_port,omitempty"`
	Message      string `json:"message,omitempty"`
	Error        string `json:"error,omitempty"`
	Timestamp    string `json:"timestamp"`
}

type probeLinkTestRuntime struct {
	protocol     string
	listenHost   string
	internalPort int
	stop         func() error
}

type probeLinkTestPingResponse struct {
	OK        bool   `json:"ok"`
	NodeID    string `json:"node_id"`
	Protocol  string `json:"protocol"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

var probeLinkTestState = struct {
	mu      sync.Mutex
	runtime *probeLinkTestRuntime
}{}

func runProbeLinkTestControl(cmd probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	requestID := strings.TrimSpace(cmd.RequestID)
	action := normalizeProbeLinkTestAction(cmd.Action)
	if action == "" {
		action = "start"
	}

	result := probeLinkTestControlResultPayload{
		Type:      "link_test_control_result",
		RequestID: requestID,
		NodeID:    strings.TrimSpace(identity.NodeID),
		OK:        false,
		Action:    action,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	switch action {
	case "start":
		protocol := normalizeProbeLinkTestProtocol(cmd.Protocol)
		listenHost := normalizeProbeLinkTestListenHost(cmd.ListenHost)
		internalPort := normalizeProbeLinkTestPort(cmd.InternalPort)
		result.Protocol = protocol
		result.ListenHost = listenHost
		result.InternalPort = internalPort

		if protocol == "" {
			result.Error = "protocol must be http/https/http3"
			sendProbeLinkTestControlResult(stream, encoder, writeMu, result)
			return
		}
		if internalPort <= 0 {
			result.Error = "internal_port must be between 1 and 65535"
			sendProbeLinkTestControlResult(stream, encoder, writeMu, result)
			return
		}

		runtime, err := startProbeLinkTestService(protocol, listenHost, internalPort, identity, strings.TrimSpace(cmd.ControllerBaseURL))
		if err != nil {
			result.Error = err.Error()
			sendProbeLinkTestControlResult(stream, encoder, writeMu, result)
			return
		}

		result.OK = true
		result.Protocol = runtime.protocol
		result.ListenHost = runtime.listenHost
		result.InternalPort = runtime.internalPort
		result.Message = fmt.Sprintf("probe link test service started: protocol=%s listen=%s", runtime.protocol, net.JoinHostPort(runtime.listenHost, strconv.Itoa(runtime.internalPort)))
	case "stop":
		stopped := stopProbeLinkTestService("remote stop command")
		if stopped != nil {
			result.Protocol = stopped.protocol
			result.ListenHost = stopped.listenHost
			result.InternalPort = stopped.internalPort
			result.Message = "probe link test service stopped"
		} else {
			result.Message = "probe link test service is not running"
		}
		result.OK = true
	default:
		result.Error = "unsupported action"
	}

	sendProbeLinkTestControlResult(stream, encoder, writeMu, result)
}

func sendProbeLinkTestControlResult(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, payload probeLinkTestControlResultPayload) {
	if writeErr := writeProbeStreamJSON(stream, encoder, writeMu, payload); writeErr != nil {
		log.Printf("probe link test control response send failed: request_id=%s err=%v", strings.TrimSpace(payload.RequestID), writeErr)
	}
}

func startProbeLinkTestService(protocol string, listenHost string, internalPort int, identity nodeIdentity, controllerBaseURL string) (*probeLinkTestRuntime, error) {
	stopProbeLinkTestService("restart before start")

	normalizedProtocol := normalizeProbeLinkTestProtocol(protocol)
	if normalizedProtocol == "" {
		return nil, fmt.Errorf("unsupported link test protocol: %s", strings.TrimSpace(protocol))
	}
	host := normalizeProbeLinkTestListenHost(listenHost)
	port := normalizeProbeLinkTestPort(internalPort)
	if port <= 0 {
		return nil, fmt.Errorf("invalid internal port")
	}

	runtime, err := buildProbeLinkTestRuntime(normalizedProtocol, host, port, identity, controllerBaseURL)
	if err != nil {
		return nil, err
	}

	probeLinkTestState.mu.Lock()
	probeLinkTestState.runtime = runtime
	probeLinkTestState.mu.Unlock()
	return runtime, nil
}

func stopProbeLinkTestService(reason string) *probeLinkTestRuntime {
	probeLinkTestState.mu.Lock()
	current := probeLinkTestState.runtime
	probeLinkTestState.runtime = nil
	probeLinkTestState.mu.Unlock()

	if current == nil {
		return nil
	}
	if current.stop != nil {
		if err := current.stop(); err != nil {
			log.Printf("probe link test service stop failed: protocol=%s listen=%s:%d reason=%s err=%v", current.protocol, current.listenHost, current.internalPort, strings.TrimSpace(reason), err)
		}
	}
	log.Printf("probe link test service stopped: protocol=%s listen=%s:%d reason=%s", current.protocol, current.listenHost, current.internalPort, strings.TrimSpace(reason))
	return current
}

func buildProbeLinkTestRuntime(protocol string, listenHost string, internalPort int, identity nodeIdentity, controllerBaseURL string) (*probeLinkTestRuntime, error) {
	switch protocol {
	case "http":
		return startProbeLinkTestHTTPServer(listenHost, internalPort, identity)
	case "https":
		return startProbeLinkTestHTTPSServer(listenHost, internalPort, identity, controllerBaseURL, "https")
	case "http3":
		return startProbeLinkTestHTTP3Server(listenHost, internalPort, identity, controllerBaseURL)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", protocol)
	}
}

func startProbeLinkTestHTTPServer(listenHost string, internalPort int, identity nodeIdentity) (*probeLinkTestRuntime, error) {
	listenAddr := net.JoinHostPort(listenHost, strconv.Itoa(internalPort))
	handler := buildProbeLinkTestHTTPHandler(strings.TrimSpace(identity.NodeID), "http")
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if serveErr := server.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("probe link test http service exited: listen=%s err=%v", listenAddr, serveErr)
		}
	}()

	log.Printf("probe link test http service started: listen=http://%s", listenAddr)
	return &probeLinkTestRuntime{
		protocol:     "http",
		listenHost:   listenHost,
		internalPort: internalPort,
		stop: func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return server.Shutdown(ctx)
		},
	}, nil
}

func startProbeLinkTestHTTPSServer(listenHost string, internalPort int, identity nodeIdentity, controllerBaseURL string, protocolLabel string) (*probeLinkTestRuntime, error) {
	listenAddr := net.JoinHostPort(listenHost, strconv.Itoa(internalPort))
	cert, err := prepareProbeServerCertificate(identity, strings.TrimSpace(controllerBaseURL))
	if err != nil {
		return nil, err
	}

	handler := buildProbeLinkTestHTTPHandler(strings.TrimSpace(identity.NodeID), protocolLabel)
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if serveErr := server.ListenAndServeTLS(cert.CertPath, cert.KeyPath); serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("probe link test https service exited: listen=%s err=%v", listenAddr, serveErr)
		}
	}()

	log.Printf("probe link test https service started: listen=https://%s", listenAddr)
	return &probeLinkTestRuntime{
		protocol:     protocolLabel,
		listenHost:   listenHost,
		internalPort: internalPort,
		stop: func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return server.Shutdown(ctx)
		},
	}, nil
}

func startProbeLinkTestHTTP3Server(listenHost string, internalPort int, identity nodeIdentity, controllerBaseURL string) (*probeLinkTestRuntime, error) {
	listenAddr := net.JoinHostPort(listenHost, strconv.Itoa(internalPort))
	cert, err := prepareProbeServerCertificate(identity, strings.TrimSpace(controllerBaseURL))
	if err != nil {
		return nil, err
	}

	handler := buildProbeLinkTestHTTPHandler(strings.TrimSpace(identity.NodeID), "http3")
	server := &http3.Server{
		Addr:      listenAddr,
		Handler:   handler,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}},
	}

	go func() {
		if serveErr := server.ListenAndServeTLS(cert.CertPath, cert.KeyPath); serveErr != nil {
			log.Printf("probe link test http3 service exited: listen=%s err=%v", listenAddr, serveErr)
		}
	}()

	log.Printf("probe link test http3 service started: listen=https://%s (h3)", listenAddr)
	return &probeLinkTestRuntime{
		protocol:     "http3",
		listenHost:   listenHost,
		internalPort: internalPort,
		stop: func() error {
			return server.Close()
		},
	}, nil
}

func buildProbeLinkTestHTTPHandler(nodeID string, protocol string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(probeLinkTestPingAPIPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, probeLinkTestPingResponse{
			OK:        true,
			NodeID:    strings.TrimSpace(nodeID),
			Protocol:  strings.TrimSpace(protocol),
			Message:   "pong",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	})
	return mux
}

func normalizeProbeLinkTestAction(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "start":
		return "start"
	case "stop":
		return "stop"
	default:
		return ""
	}
}

func normalizeProbeLinkTestProtocol(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "http":
		return "http"
	case "https":
		return "https"
	case "http3", "h3":
		return "http3"
	default:
		return ""
	}
}

func normalizeProbeLinkTestListenHost(raw string) string {
	host := strings.TrimSpace(raw)
	if host == "" {
		return "0.0.0.0"
	}
	if host == "::" {
		return "::"
	}
	host = strings.Trim(host, "[]")
	if parsed := net.ParseIP(host); parsed == nil {
		return "0.0.0.0"
	}
	return host
}

func normalizeProbeLinkTestPort(port int) int {
	if port <= 0 || port > 65535 {
		return 0
	}
	return port
}
