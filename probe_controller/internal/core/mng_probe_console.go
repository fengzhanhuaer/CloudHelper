package core

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Control-channel request/response for bridging a probe's local console.
// ---------------------------------------------------------------------------

type probeLocalConsoleBridgeCommand struct {
	Type           string              `json:"type"`
	RequestID      string              `json:"request_id"`
	ConsoleMethod  string              `json:"console_method"`
	ConsolePath    string              `json:"console_path"`
	ConsoleHeaders map[string][]string `json:"console_headers,omitempty"`
	ConsoleBody    string              `json:"console_body,omitempty"` // base64
	Timestamp      string              `json:"timestamp"`
}

type probeLocalConsoleBridgeResultMessage struct {
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

const probeLocalConsoleBridgeMaxBodyBytes = 8 << 20 // 8 MiB

var probeLocalConsoleBridgeRequestSeq atomic.Uint64

var probeLocalConsoleBridgeWaiters = struct {
	mu   sync.Mutex
	data map[string]chan probeLocalConsoleBridgeResultMessage
}{data: make(map[string]chan probeLocalConsoleBridgeResultMessage)}

func newProbeLocalConsoleBridgeRequestID(nodeID string) string {
	seq := probeLocalConsoleBridgeRequestSeq.Add(1)
	return fmt.Sprintf("probe-console-%s-%d-%d", normalizeProbeNodeID(nodeID), time.Now().UnixNano(), seq)
}

// dispatchProbeLocalConsoleRequest forwards one HTTP request to the probe's local
// console over the control channel and waits for the response.
func dispatchProbeLocalConsoleRequest(nodeID, method, path string, headers map[string][]string, body []byte) (probeLocalConsoleBridgeResultMessage, error) {
	normalizedID := normalizeProbeNodeID(nodeID)
	if normalizedID == "" {
		return probeLocalConsoleBridgeResultMessage{}, fmt.Errorf("node_id is required")
	}
	session, ok := getProbeSession(normalizedID)
	if !ok {
		return probeLocalConsoleBridgeResultMessage{}, fmt.Errorf("probe is offline")
	}

	requestID := newProbeLocalConsoleBridgeRequestID(normalizedID)
	waiter := make(chan probeLocalConsoleBridgeResultMessage, 1)
	probeLocalConsoleBridgeWaiters.mu.Lock()
	probeLocalConsoleBridgeWaiters.data[requestID] = waiter
	probeLocalConsoleBridgeWaiters.mu.Unlock()
	defer func() {
		probeLocalConsoleBridgeWaiters.mu.Lock()
		delete(probeLocalConsoleBridgeWaiters.data, requestID)
		probeLocalConsoleBridgeWaiters.mu.Unlock()
	}()

	cmd := probeLocalConsoleBridgeCommand{
		Type:           "local_console_bridge",
		RequestID:      requestID,
		ConsoleMethod:  method,
		ConsolePath:    path,
		ConsoleHeaders: headers,
		ConsoleBody:    base64.StdEncoding.EncodeToString(body),
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
	}
	if err := session.writeJSON(cmd); err != nil {
		unregisterProbeSession(normalizedID, session)
		return probeLocalConsoleBridgeResultMessage{}, err
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
		return probeLocalConsoleBridgeResultMessage{}, fmt.Errorf("probe local console bridge timeout")
	}
}

func consumeProbeLocalConsoleBridgeResult(result probeLocalConsoleBridgeResultMessage) {
	requestID := strings.TrimSpace(result.RequestID)
	if requestID == "" {
		return
	}
	probeLocalConsoleBridgeWaiters.mu.Lock()
	waiter, ok := probeLocalConsoleBridgeWaiters.data[requestID]
	if ok {
		delete(probeLocalConsoleBridgeWaiters.data, requestID)
	}
	probeLocalConsoleBridgeWaiters.mu.Unlock()
	if !ok {
		return
	}
	select {
	case waiter <- result:
	default:
	}
}

// ---------------------------------------------------------------------------
// Browser-facing console bridge: /mng/probe-console (entry, mng-authed) mints a
// capability token and redirects into /mng/probe-console/session/{token}/local/*.
// Keeping the token in the URL path lets multiple probe consoles stay open in
// the same browser. Legacy /local/* cookie-based bridging is still accepted for
// old tabs and links.
// ---------------------------------------------------------------------------

const (
	mngProbeConsoleCookieName     = "mng_probe_console"
	mngProbeConsoleNodeCookieName = "mng_probe_console_node"
	mngProbeConsoleSessionPrefix  = "/mng/probe-console/session/"
	mngProbeConsoleTokenTTL       = 30 * time.Minute
)

type mngProbeConsoleToken struct {
	NodeID      string
	DisplayName string
	ExpiresAt   time.Time
}

var mngProbeConsoleTokens = struct {
	mu   sync.Mutex
	data map[string]mngProbeConsoleToken
}{data: map[string]mngProbeConsoleToken{}}

type mngProbeConsoleBridgeRoute struct {
	TokenRecord mngProbeConsoleToken
	ConsolePath string
	URLPrefix   string
}

func mintMngProbeConsoleToken(nodeID string, displayName ...string) string {
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
	mngProbeConsoleTokens.data[token] = mngProbeConsoleToken{
		NodeID:      nodeID,
		DisplayName: strings.TrimSpace(firstString(displayName...)),
		ExpiresAt:   now.Add(mngProbeConsoleTokenTTL),
	}
	mngProbeConsoleTokens.mu.Unlock()
	return token
}

func resolveMngProbeConsoleToken(token string) (string, bool) {
	rec, ok := resolveMngProbeConsoleTokenRecord(token)
	if !ok {
		return "", false
	}
	return rec.NodeID, true
}

func resolveMngProbeConsoleTokenRecord(token string) (mngProbeConsoleToken, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return mngProbeConsoleToken{}, false
	}
	now := time.Now()
	mngProbeConsoleTokens.mu.Lock()
	defer mngProbeConsoleTokens.mu.Unlock()
	rec, ok := mngProbeConsoleTokens.data[token]
	if !ok {
		return mngProbeConsoleToken{}, false
	}
	if now.After(rec.ExpiresAt) {
		delete(mngProbeConsoleTokens.data, token)
		return mngProbeConsoleToken{}, false
	}
	return rec, true
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
	node, ok := getProbeNodeByID(nodeID)
	if !ok {
		http.Error(w, "probe node not found", http.StatusNotFound)
		return
	}
	token := mintMngProbeConsoleToken(nodeID, probeNodeConsoleDisplayName(nodeID, node))
	if token == "" {
		http.Error(w, "failed to create console session", http.StatusInternalServerError)
		return
	}
	secure := isHTTPSRequest(r)
	// Session cookies (no Expires): the server-side token slides with activity, and
	// the node cookie lets an idle-expired tab transparently re-mint on next
	// navigation (the entry remains mng-authenticated).
	http.SetCookie(w, &http.Cookie{
		Name:     mngProbeConsoleCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     mngProbeConsoleNodeCookieName,
		Value:    nodeID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
	http.Redirect(w, r, mngProbeConsoleSessionPrefix+token+"/local/panel", http.StatusFound)
}

// mngProbeConsoleBridgeHandler serves /local/* by forwarding to the token-selected
// probe node. New console tabs carry the token in a session-scoped URL prefix so
// multiple probe consoles can coexist in the same browser. The legacy /local/*
// cookie mode is kept for already-open tabs and old links.
func mngProbeConsoleBridgeHandler(w http.ResponseWriter, r *http.Request) {
	route, ok := resolveMngProbeConsoleBridgeRoute(r)
	if !ok {
		mngProbeConsoleBridgeDenied(w, r)
		return
	}
	setMngProbeConsoleBridgeCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	tokenRecord := route.TokenRecord
	nodeID := tokenRecord.NodeID

	path := route.ConsolePath
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, probeLocalConsoleBridgeMaxBodyBytes))
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
	decoded = applyMngProbeConsoleTitle(decoded, tokenRecord.DisplayName, result.Headers)
	decoded = rewriteMngProbeConsoleHTMLLinks(decoded, route.URLPrefix, result.Headers)
	for key, values := range result.Headers {
		canonical := http.CanonicalHeaderKey(key)
		if mngProbeConsoleSkipResponseHeader(canonical) {
			continue
		}
		for _, value := range values {
			if canonical == "Location" {
				value = rewriteMngProbeConsoleLocation(value, route.URLPrefix)
			}
			w.Header().Add(canonical, value)
		}
	}
	setMngProbeConsoleBridgeSecurityHeaders(w, result.Headers)
	w.Header().Set("X-Probe-Console-Bridge", "controller")
	status := result.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(decoded)
}

func setMngProbeConsoleBridgeCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "null")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Range")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.Header().Add("Vary", "Origin")
}

func setMngProbeConsoleBridgeSecurityHeaders(w http.ResponseWriter, responseHeaders map[string][]string) {
	setMngProbeConsoleBridgeCORSHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	if !mngProbeConsoleLooksLikeHTML(responseHeaders) {
		return
	}
	w.Header().Set("Content-Security-Policy", strings.Join([]string{
		"sandbox allow-scripts allow-modals allow-downloads",
		"default-src 'self' data: blob:",
		"script-src 'self' 'unsafe-inline'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"connect-src http: https:",
		"object-src 'none'",
		"frame-src 'none'",
		"frame-ancestors 'none'",
		"form-action 'none'",
		"base-uri 'none'",
	}, "; "))
}

func resolveMngProbeConsoleBridgeRoute(r *http.Request) (mngProbeConsoleBridgeRoute, bool) {
	path := strings.TrimSpace(r.URL.Path)
	if strings.HasPrefix(path, mngProbeConsoleSessionPrefix) {
		rest := strings.TrimPrefix(path, mngProbeConsoleSessionPrefix)
		token := rest
		consolePath := "/local/panel"
		if idx := strings.Index(rest, "/"); idx >= 0 {
			token = rest[:idx]
			consolePath = rest[idx:]
		}
		tokenRecord, ok := resolveMngProbeConsoleTokenRecord(token)
		if !ok {
			return mngProbeConsoleBridgeRoute{}, false
		}
		if !strings.HasPrefix(consolePath, "/local/") {
			consolePath = "/local/panel"
		}
		return mngProbeConsoleBridgeRoute{
			TokenRecord: tokenRecord,
			ConsolePath: consolePath,
			URLPrefix:   mngProbeConsoleSessionPrefix + strings.TrimSpace(token),
		}, true
	}

	cookie, err := r.Cookie(mngProbeConsoleCookieName)
	if err != nil {
		return mngProbeConsoleBridgeRoute{}, false
	}
	tokenRecord, ok := resolveMngProbeConsoleTokenRecord(cookie.Value)
	if !ok {
		return mngProbeConsoleBridgeRoute{}, false
	}
	return mngProbeConsoleBridgeRoute{
		TokenRecord: tokenRecord,
		ConsolePath: path,
	}, true
}

func probeNodeConsoleDisplayName(nodeID string, node probeNodeRecord) string {
	if name := strings.TrimSpace(node.NodeName); name != "" {
		return name
	}
	if node.NodeNo > 0 {
		return fmt.Sprintf("探针 #%d", node.NodeNo)
	}
	if id := normalizeProbeNodeID(nodeID); id != "" {
		return "探针 " + id
	}
	return ""
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func applyMngProbeConsoleTitle(body []byte, displayName string, headers map[string][]string) []byte {
	name := strings.TrimSpace(displayName)
	if name == "" || len(body) == 0 || !mngProbeConsoleLooksLikeHTML(headers) {
		return body
	}

	page := string(body)
	lower := strings.ToLower(page)
	titleStart := strings.Index(lower, "<title>")
	titleEnd := strings.Index(lower, "</title>")
	if titleStart >= 0 && titleEnd > titleStart {
		contentStart := titleStart + len("<title>")
		prefix := page[:contentStart]
		current := strings.TrimSpace(page[contentStart:titleEnd])
		suffix := page[titleEnd:]
		if strings.Contains(current, name) {
			return body
		}
		return []byte(prefix + html.EscapeString(name) + " - " + current + suffix)
	}

	headEnd := strings.Index(lower, "</head>")
	if headEnd < 0 {
		return body
	}
	title := "<title>" + html.EscapeString(name) + " - Probe Node 控制台</title>\n  "
	return []byte(page[:headEnd] + title + page[headEnd:])
}

func rewriteMngProbeConsoleHTMLLinks(body []byte, urlPrefix string, headers map[string][]string) []byte {
	prefix := strings.TrimRight(strings.TrimSpace(urlPrefix), "/")
	if prefix == "" || len(body) == 0 || !mngProbeConsoleLooksLikeHTML(headers) {
		return body
	}
	page := string(body)
	replacements := []struct {
		old string
		new string
	}{
		{`"/local/`, `"` + prefix + `/local/`},
		{`'/local/`, `'` + prefix + `/local/`},
		{"`/local/", "`" + prefix + "/local/"},
		{`=/local/`, `=` + prefix + `/local/`},
	}
	for _, item := range replacements {
		page = strings.ReplaceAll(page, item.old, item.new)
	}
	return []byte(page)
}

func rewriteMngProbeConsoleLocation(value string, urlPrefix string) string {
	prefix := strings.TrimRight(strings.TrimSpace(urlPrefix), "/")
	clean := strings.TrimSpace(value)
	if prefix == "" || !strings.HasPrefix(clean, "/local/") {
		return value
	}
	return prefix + clean
}

func mngProbeConsoleLooksLikeHTML(headers map[string][]string) bool {
	for key, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), "Content-Type") {
			continue
		}
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), "text/html") {
				return true
			}
		}
	}
	return false
}

func mngProbeConsoleBridgeDenied(w http.ResponseWriter, r *http.Request) {
	// Top-level navigations recover gracefully: if we still remember the node, send
	// the browser through the mng-authenticated entry to transparently re-mint a
	// token (or to the mng login if the admin session also lapsed). API/asset calls
	// get a plain 401 so the page's own fetch logic can react.
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
		if nodeCookie, err := r.Cookie(mngProbeConsoleNodeCookieName); err == nil {
			if node := normalizeProbeNodeID(nodeCookie.Value); node != "" {
				http.Redirect(w, r, "/mng/probe-console?node="+node, http.StatusFound)
				return
			}
		}
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
		"Upgrade", "Content-Length", "Set-Cookie", "Content-Security-Policy",
		"Content-Security-Policy-Report-Only", "Access-Control-Allow-Origin",
		"Access-Control-Allow-Credentials", "Access-Control-Allow-Headers",
		"Access-Control-Allow-Methods", "Access-Control-Max-Age",
		"Cross-Origin-Embedder-Policy", "Cross-Origin-Opener-Policy",
		"Cross-Origin-Resource-Policy", "Origin-Agent-Cluster", "Permissions-Policy",
		"Referrer-Policy", "X-Content-Type-Options", "X-Frame-Options":
		return true
	default:
		return false
	}
}
