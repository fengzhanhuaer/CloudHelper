package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

const probeLocalConsoleProxyMaxBodyBytes = 8 << 20 // 8 MiB

// probeLocalConsoleTrustedCtxKey marks an in-process local-console request as
// originating from the already-authenticated controller, allowing it to bypass the
// local login. It is a private context key; external HTTP cannot set it.
type probeLocalConsoleTrustedCtxKeyType struct{}

var probeLocalConsoleTrustedCtxKey probeLocalConsoleTrustedCtxKeyType

func withProbeLocalConsoleTrusted(ctx context.Context) context.Context {
	return context.WithValue(ctx, probeLocalConsoleTrustedCtxKey, true)
}

func isProbeLocalConsoleTrusted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	trusted, ok := ctx.Value(probeLocalConsoleTrustedCtxKey).(bool)
	return ok && trusted
}

type probeLocalConsoleProxyResult struct {
	Type       string              `json:"type"`
	RequestID  string              `json:"request_id"`
	NodeID     string              `json:"node_id"`
	OK         bool                `json:"ok"`
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Body       string              `json:"body,omitempty"` // base64
	Error      string              `json:"error,omitempty"`
	Timestamp  string              `json:"timestamp,omitempty"`
}

var (
	probeLocalConsoleProxyMuxOnce sync.Once
	probeLocalConsoleProxyMux     http.Handler
)

// probeLocalConsoleProxyHandler returns a cached local-console mux used to serve
// controller-proxied requests in-process (no extra TCP listener / loopback dial).
func probeLocalConsoleProxyHandler() http.Handler {
	probeLocalConsoleProxyMuxOnce.Do(func() {
		probeLocalConsoleProxyMux = buildProbeLocalConsoleMux()
	})
	return probeLocalConsoleProxyMux
}

// runProbeLocalConsoleProxy serves a single HTTP request (forwarded by the
// controller over the control channel) against the local console mux and returns
// the response back to the controller.
func runProbeLocalConsoleProxy(msg probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		return
	}
	result := probeLocalConsoleProxyResult{
		Type:      "local_console_proxy_result",
		RequestID: requestID,
		NodeID:    strings.TrimSpace(identity.NodeID),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	method := strings.ToUpper(strings.TrimSpace(msg.ConsoleMethod))
	if method == "" {
		method = http.MethodGet
	}
	path := strings.TrimSpace(msg.ConsolePath)
	if path == "" || !strings.HasPrefix(path, "/") {
		result.Error = "invalid console path"
		sendProbeLocalConsoleProxyResult(stream, encoder, writeMu, result)
		return
	}

	var bodyBytes []byte
	if strings.TrimSpace(msg.ConsoleBody) != "" {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(msg.ConsoleBody))
		if err != nil {
			result.Error = "invalid console body encoding"
			sendProbeLocalConsoleProxyResult(stream, encoder, writeMu, result)
			return
		}
		bodyBytes = decoded
	}

	// http.NewRequest (vs httptest.NewRequest) returns an error instead of panicking
	// on a malformed target. The host is irrelevant: ServeMux routes on URL.Path.
	req, err := http.NewRequest(method, "http://probe-local"+path, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Error = "invalid console request: " + err.Error()
		sendProbeLocalConsoleProxyResult(stream, encoder, writeMu, result)
		return
	}
	req.RequestURI = ""
	for key, values := range msg.ConsoleHeaders {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if canonical == "" || isProbeLocalConsoleHopHeader(canonical) {
			continue
		}
		for _, value := range values {
			req.Header.Add(canonical, value)
		}
	}
	req = req.WithContext(withProbeLocalConsoleTrusted(req.Context()))

	recorder := httptest.NewRecorder()
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("probe local console proxy panic: request_id=%s path=%s err=%v", requestID, path, rec)
				recorder.Code = http.StatusInternalServerError
			}
		}()
		probeLocalConsoleProxyHandler().ServeHTTP(recorder, req)
	}()

	body := recorder.Body.Bytes()
	if len(body) > probeLocalConsoleProxyMaxBodyBytes {
		body = body[:probeLocalConsoleProxyMaxBodyBytes]
	}
	headers := make(map[string][]string, len(recorder.Header()))
	for key, values := range recorder.Header() {
		if isProbeLocalConsoleHopHeader(http.CanonicalHeaderKey(key)) {
			continue
		}
		headers[key] = append([]string(nil), values...)
	}

	result.OK = true
	result.StatusCode = recorder.Code
	result.Headers = headers
	result.Body = base64.StdEncoding.EncodeToString(body)
	sendProbeLocalConsoleProxyResult(stream, encoder, writeMu, result)
}

// isProbeLocalConsoleHopHeader reports hop-by-hop / connection-specific headers that
// must not be forwarded across the proxy boundary.
func isProbeLocalConsoleHopHeader(canonical string) bool {
	switch canonical {
	case "Connection", "Proxy-Connection", "Keep-Alive", "Te", "Trailer",
		"Transfer-Encoding", "Upgrade", "Content-Length", "Host":
		return true
	default:
		return false
	}
}

func sendProbeLocalConsoleProxyResult(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, result probeLocalConsoleProxyResult) {
	if writeErr := writeProbeStreamJSON(stream, encoder, writeMu, result); writeErr != nil {
		log.Printf("probe local console proxy result send failed: request_id=%s err=%v", strings.TrimSpace(result.RequestID), writeErr)
	}
}
