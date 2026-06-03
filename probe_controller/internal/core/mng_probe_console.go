package core

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Control-channel request/response for proxying a probe's local console.
// ---------------------------------------------------------------------------

type probeLocalConsoleProxyCommand struct {
	Type           string              `json:"type"`
	RequestID      string              `json:"request_id"`
	ConsoleMethod  string              `json:"console_method"`
	ConsolePath    string              `json:"console_path"`
	ConsoleHeaders map[string][]string `json:"console_headers,omitempty"`
	ConsoleBody    string              `json:"console_body,omitempty"` // base64
	Timestamp      string              `json:"timestamp"`
}

type probeLocalConsoleProxyResultMessage struct {
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

const probeLocalConsoleProxyMaxBodyBytes = 8 << 20 // 8 MiB

var probeLocalConsoleProxyRequestSeq atomic.Uint64

var probeLocalConsoleProxyWaiters = struct {
	mu   sync.Mutex
	data map[string]chan probeLocalConsoleProxyResultMessage
}{data: make(map[string]chan probeLocalConsoleProxyResultMessage)}

func newProbeLocalConsoleProxyRequestID(nodeID string) string {
	seq := probeLocalConsoleProxyRequestSeq.Add(1)
	return fmt.Sprintf("probe-console-%s-%d-%d", normalizeProbeNodeID(nodeID), time.Now().UnixNano(), seq)
}

// dispatchProbeLocalConsoleRequest forwards one HTTP request to the probe's local
// console over the control channel and waits for the response.
func dispatchProbeLocalConsoleRequest(nodeID, method, path string, headers map[string][]string, body []byte) (probeLocalConsoleProxyResultMessage, error) {
	normalizedID := normalizeProbeNodeID(nodeID)
	if normalizedID == "" {
		return probeLocalConsoleProxyResultMessage{}, fmt.Errorf("node_id is required")
	}
	session, ok := getProbeSession(normalizedID)
	if !ok {
		return probeLocalConsoleProxyResultMessage{}, fmt.Errorf("probe is offline")
	}

	requestID := newProbeLocalConsoleProxyRequestID(normalizedID)
	waiter := make(chan probeLocalConsoleProxyResultMessage, 1)
	probeLocalConsoleProxyWaiters.mu.Lock()
	probeLocalConsoleProxyWaiters.data[requestID] = waiter
	probeLocalConsoleProxyWaiters.mu.Unlock()
	defer func() {
		probeLocalConsoleProxyWaiters.mu.Lock()
		delete(probeLocalConsoleProxyWaiters.data, requestID)
		probeLocalConsoleProxyWaiters.mu.Unlock()
	}()

	cmd := probeLocalConsoleProxyCommand{
		Type:           "local_console_proxy",
		RequestID:      requestID,
		ConsoleMethod:  method,
		ConsolePath:    path,
		ConsoleHeaders: headers,
		ConsoleBody:    base64.StdEncoding.EncodeToString(body),
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
	}
	if err := session.writeJSON(cmd); err != nil {
		unregisterProbeSession(normalizedID, session)
		return probeLocalConsoleProxyResultMessage{}, err
	}

	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	select {
	case result := <-waiter:
		if strings.TrimSpace(result.NodeID) == "" {
			result.NodeID = normalizedID
		}
		if !result.OK && strings.TrimSpace(result.Error) != "" {
			return result, errors.New(result.Error)
		}
		return result, nil
	case <-timer.C:
		return probeLocalConsoleProxyResultMessage{}, fmt.Errorf("probe local console proxy timeout")
	}
}

func consumeProbeLocalConsoleProxyResult(result probeLocalConsoleProxyResultMessage) {
	requestID := strings.TrimSpace(result.RequestID)
	if requestID == "" {
		return
	}
	probeLocalConsoleProxyWaiters.mu.Lock()
	waiter, ok := probeLocalConsoleProxyWaiters.data[requestID]
	if ok {
		delete(probeLocalConsoleProxyWaiters.data, requestID)
	}
	probeLocalConsoleProxyWaiters.mu.Unlock()
	if !ok {
		return
	}
	select {
	case waiter <- result:
	default:
	}
}

// ---------------------------------------------------------------------------
// Browser-facing reverse proxy: /mng/probe-console (entry, mng-authed) mints a
// capability token cookie scoped to "/", and /local/* proxies to the selected
// node authenticated by that token (the mng cookie is path-scoped to /mng and is
// not sent for /local/*).
// ---------------------------------------------------------------------------

const (
	mngProbeConsoleCookieName = "mng_probe_console"
	mngProbeConsoleTokenTTL   = 2 * time.Hour
)

type mngProbeConsoleToken struct {
	NodeID    string
	ExpiresAt time.Time
}

var mngProbeConsoleTokens = struct {
	mu   sync.Mutex
	data map[string]mngProbeConsoleToken
}{data: map[string]mngProbeConsoleToken{}}

func mintMngProbeConsoleToken(nodeID string) string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	token := hex.EncodeToString(buf)
	now := time.Now()
	mngProbeConsoleTokens.mu.Lock()
	for key, rec := range mngProbeConsoleTokens.data {
		if now.After(rec.ExpiresAt) {
			delete(mngProbeConsoleTokens.data, key)
		}
	}
	mngProbeConsoleTokens.data[token] = mngProbeConsoleToken{NodeID: nodeID, ExpiresAt: now.Add(mngProbeConsoleTokenTTL)}
	mngProbeConsoleTokens.mu.Unlock()
	return token
}

func resolveMngProbeConsoleToken(token string) (string, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	mngProbeConsoleTokens.mu.Lock()
	defer mngProbeConsoleTokens.mu.Unlock()
	rec, ok := mngProbeConsoleTokens.data[token]
	if !ok {
		return "", false
	}
	if time.Now().After(rec.ExpiresAt) {
		delete(mngProbeConsoleTokens.data, token)
		return "", false
	}
	return rec.NodeID, true
}

// mngProbeConsoleEntryHandler is mng-authenticated. It binds a console token to the
// chosen node and redirects the browser into the proxied console.
func mngProbeConsoleEntryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodeID := normalizeProbeNodeID(r.URL.Query().Get("node"))
	if nodeID == "" {
		http.Error(w, "node query parameter is required", http.StatusBadRequest)
		return
	}
	if _, ok := getProbeNodeByID(nodeID); !ok {
		http.Error(w, "probe node not found", http.StatusNotFound)
		return
	}
	token := mintMngProbeConsoleToken(nodeID)
	if token == "" {
		http.Error(w, "failed to create console session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     mngProbeConsoleCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPSRequest(r),
		Expires:  time.Now().Add(mngProbeConsoleTokenTTL),
	})
	http.Redirect(w, r, "/local/panel", http.StatusFound)
}

// mngProbeConsoleProxyHandler serves /local/* by forwarding to the token-selected
// probe node. It is authenticated by the console token cookie (not mng auth, whose
// cookie is path-scoped to /mng).
func mngProbeConsoleProxyHandler(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(mngProbeConsoleCookieName)
	if err != nil {
		mngProbeConsoleProxyDenied(w, r)
		return
	}
	nodeID, ok := resolveMngProbeConsoleToken(cookie.Value)
	if !ok {
		mngProbeConsoleProxyDenied(w, r)
		return
	}

	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, probeLocalConsoleProxyMaxBodyBytes))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	headers := make(map[string][]string, len(r.Header))
	for key, values := range r.Header {
		canonical := http.CanonicalHeaderKey(key)
		if mngProbeConsoleSkipRequestHeader(canonical) {
			continue
		}
		headers[canonical] = append([]string(nil), values...)
	}

	result, err := dispatchProbeLocalConsoleRequest(nodeID, r.Method, path, headers, body)
	if err != nil {
		http.Error(w, "probe console unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}

	decoded := []byte{}
	if strings.TrimSpace(result.Body) != "" {
		if b, derr := base64.StdEncoding.DecodeString(result.Body); derr == nil {
			decoded = b
		}
	}
	for key, values := range result.Headers {
		canonical := http.CanonicalHeaderKey(key)
		if mngProbeConsoleSkipResponseHeader(canonical) {
			continue
		}
		for _, value := range values {
			w.Header().Add(canonical, value)
		}
	}
	status := result.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(decoded)
}

func mngProbeConsoleProxyDenied(w http.ResponseWriter, r *http.Request) {
	// Top-level navigations bounce back to the management panel; API/asset calls
	// get a plain 401 so the page's own fetch logic can react.
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/mng/probe", http.StatusFound)
		return
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "probe console session is missing or expired"})
}

func mngProbeConsoleSkipRequestHeader(canonical string) bool {
	switch canonical {
	case "Connection", "Proxy-Connection", "Keep-Alive", "Te", "Trailer",
		"Transfer-Encoding", "Upgrade", "Content-Length", "Host", "Cookie":
		return true
	default:
		return false
	}
}

func mngProbeConsoleSkipResponseHeader(canonical string) bool {
	switch canonical {
	case "Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding",
		"Upgrade", "Content-Length", "Set-Cookie":
		return true
	default:
		return false
	}
}
