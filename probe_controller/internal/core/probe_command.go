package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	probeNodeInstallScriptLinuxURL   = "https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/main/scripts/install_probe_node_service.sh"
	probeNodeInstallScriptWindowsURL = "https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/main/scripts/install_probe_node_service_windows.ps1"
)

type probeSession struct {
	nodeID string
	stream net.Conn
	enc    *json.Encoder
	mu     sync.Mutex
}

type probeUpgradeDispatchRequest struct {
	NodeID string `json:"node_id"`
}

type probeUpgradeCommand struct {
	Type              string `json:"type"`
	Mode              string `json:"mode"`
	ReleaseRepo       string `json:"release_repo"`
	ControllerBaseURL string `json:"controller_base_url"`
	Timestamp         string `json:"timestamp"`
}

type probeLogsCommand struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	Lines        int    `json:"lines"`
	SinceMinutes int    `json:"since_minutes"`
	Timestamp    string `json:"timestamp"`
}

type probeLogsResultMessage struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	NodeID       string `json:"node_id"`
	OK           bool   `json:"ok"`
	Source       string `json:"source,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
	Lines        int    `json:"lines,omitempty"`
	SinceMinutes int    `json:"since_minutes,omitempty"`
	Content      string `json:"content,omitempty"`
	Error        string `json:"error,omitempty"`
	Timestamp    string `json:"timestamp,omitempty"`
}

var probeSessions = struct {
	mu   sync.RWMutex
	data map[string]*probeSession
}{data: make(map[string]*probeSession)}

var probeAuthReplayStore = struct {
	mu   sync.Mutex
	seen map[string]time.Time
}{seen: make(map[string]time.Time)}

var probeLogRequestSeq atomic.Uint64

var probeLogWaiters = struct {
	mu   sync.Mutex
	data map[string]chan probeLogsResultMessage
}{data: make(map[string]chan probeLogsResultMessage)}

func registerProbeSession(nodeID string, stream net.Conn) *probeSession {
	s := &probeSession{nodeID: nodeID, stream: stream, enc: json.NewEncoder(stream)}
	probeSessions.mu.Lock()
	probeSessions.data[nodeID] = s
	probeSessions.mu.Unlock()
	setProbeRuntimeOnline(nodeID, true)
	go func() {
		_ = s.writeJSON(map[string]interface{}{
			"type":         "report_interval",
			"interval_sec": currentProbeReportIntervalSec(),
			"server_utc":   time.Now().UTC().Format(time.RFC3339),
		})
	}()
	return s
}

func unregisterProbeSession(nodeID string, session *probeSession) {
	probeSessions.mu.Lock()
	defer probeSessions.mu.Unlock()
	current, ok := probeSessions.data[nodeID]
	if !ok || current != session {
		return
	}
	delete(probeSessions.data, nodeID)
	if current.stream != nil {
		_ = current.stream.Close()
	}
	setProbeRuntimeOnline(nodeID, false)
}

func getProbeSession(nodeID string) (*probeSession, bool) {
	probeSessions.mu.RLock()
	defer probeSessions.mu.RUnlock()
	s, ok := probeSessions.data[nodeID]
	return s, ok
}

func (s *probeSession) writeJSON(v interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream == nil {
		return errors.New("probe stream is closed")
	}
	_ = s.stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := s.enc.Encode(v)
	_ = s.stream.SetWriteDeadline(time.Time{})
	return err
}

func AdminUpgradeProbeNodeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req probeUpgradeDispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	nodeID := normalizeProbeNodeID(req.NodeID)
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}

	node, ok := getProbeNodeByID(nodeID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "probe node not found"})
		return
	}

	if err := dispatchUpgradeToProbe(node, controllerBaseURLFromRequest(r)); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "node_id": nodeID})
}

func AdminUpgradeAllProbeNodesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ProbeStore.mu.RLock()
	nodes := loadProbeNodesLocked()
	ProbeStore.mu.RUnlock()

	controllerBaseURL := controllerBaseURLFromRequest(r)
	success := 0
	failures := make([]string, 0)
	for _, node := range nodes {
		if err := dispatchUpgradeToProbe(node, controllerBaseURL); err != nil {
			failures = append(failures, fmt.Sprintf("%d:%v", node.NodeNo, err))
			continue
		}
		success++
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":       len(failures) == 0,
		"success":  success,
		"total":    len(nodes),
		"failures": failures,
	})
}

func ProbeProxyGitHubLatestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}
	if _, err := authenticateProbeRequest(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	repo, err := normalizeGitHubRepo(r.URL.Query().Get("repo"), r.URL.Query().Get("project"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	release, err := fetchLatestRelease(ctx, repo)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("failed to fetch github latest release: %v", err)})
		return
	}

	assets := make([]proxyAsset, 0, len(release.Assets))
	for _, a := range release.Assets {
		assets = append(assets, proxyAsset{Name: a.Name, Size: a.Size, DownloadURL: a.BrowserDownloadURL})
	}
	writeJSON(w, http.StatusOK, proxyLatestResponse{
		Repo:        repo,
		TagName:     strings.TrimSpace(release.TagName),
		ReleaseName: strings.TrimSpace(release.Name),
		HTMLURL:     strings.TrimSpace(release.HTMLURL),
		PublishedAt: strings.TrimSpace(release.PublishedAt),
		Assets:      assets,
	})
}

func ProbeProxyDownloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}
	if _, err := authenticateProbeRequest(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url query parameter is required"})
		return
	}
	targetURL, err := url.Parse(rawURL)
	if err != nil || targetURL == nil || targetURL.Scheme != "https" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid download url"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	proxyReq.Header.Set("User-Agent", "cloudhelper-probe-proxy-download")
	proxyReq.Header.Set("Accept", "application/octet-stream")
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("proxy download failed: %v", err)})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("upstream status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))})
		return
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	fileName := sanitizeFilename(path.Base(strings.TrimSpace(targetURL.Path)))
	if fileName != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	}
	w.Header().Set("Content-Type", contentType)
	if cl := strings.TrimSpace(resp.Header.Get("Content-Length")); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

func ProbeProxyInstallScriptHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}

	nodeID := normalizeProbeNodeID(r.URL.Query().Get("node_id"))
	secret := strings.TrimSpace(r.URL.Query().Get("secret"))
	if nodeID == "" || secret == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id and secret query parameters are required"})
		return
	}
	storedSecret, ok := resolveProbeSecret(nodeID)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "probe secret is not configured for node"})
		return
	}
	if !hmac.Equal([]byte(storedSecret), []byte(secret)) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid probe secret"})
		return
	}

	target := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("target")))
	scriptURL := probeNodeInstallScriptLinuxURL
	contentType := "text/x-shellscript; charset=utf-8"
	if target == "windows" {
		scriptURL = probeNodeInstallScriptWindowsURL
		contentType = "text/plain; charset=utf-8"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodGet, scriptURL, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	proxyReq.Header.Set("User-Agent", "cloudhelper-probe-install-script-proxy")
	proxyReq.Header.Set("Accept", "text/plain")

	resp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("proxy install script failed: %v", err)})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("upstream status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))})
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

func dispatchUpgradeToProbe(node probeNodeRecord, controllerBaseURL string) error {
	nodeID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
	session, ok := getProbeSession(nodeID)
	if !ok {
		return fmt.Errorf("probe is offline")
	}

	mode := "proxy"
	if node.DirectConnect {
		mode = "direct"
	}
	cmd := probeUpgradeCommand{
		Type:              "upgrade",
		Mode:              mode,
		ReleaseRepo:       releaseRepo(),
		ControllerBaseURL: controllerBaseURL,
		Timestamp:         time.Now().UTC().Format(time.RFC3339),
	}
	if err := session.writeJSON(cmd); err != nil {
		unregisterProbeSession(nodeID, session)
		return err
	}
	return nil
}

func fetchProbeLogsFromNode(nodeID string, lines int, sinceMinutes int) (probeLogsResultMessage, error) {
	normalizedID := normalizeProbeNodeID(nodeID)
	if normalizedID == "" {
		return probeLogsResultMessage{}, fmt.Errorf("node_id is required")
	}

	session, ok := getProbeSession(normalizedID)
	if !ok {
		return probeLogsResultMessage{}, fmt.Errorf("probe is offline")
	}

	safeLines := normalizeAdminLogLines(strconv.Itoa(lines))
	safeSinceMinutes := normalizeAdminSinceMinutes(strconv.Itoa(sinceMinutes))
	requestID := newProbeLogRequestID(normalizedID)
	waiter := make(chan probeLogsResultMessage, 1)

	probeLogWaiters.mu.Lock()
	probeLogWaiters.data[requestID] = waiter
	probeLogWaiters.mu.Unlock()
	defer func() {
		probeLogWaiters.mu.Lock()
		delete(probeLogWaiters.data, requestID)
		probeLogWaiters.mu.Unlock()
	}()

	cmd := probeLogsCommand{
		Type:         "logs_get",
		RequestID:    requestID,
		Lines:        safeLines,
		SinceMinutes: safeSinceMinutes,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	}
	if err := session.writeJSON(cmd); err != nil {
		unregisterProbeSession(normalizedID, session)
		return probeLogsResultMessage{}, err
	}

	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()

	select {
	case result := <-waiter:
		if strings.TrimSpace(result.NodeID) == "" {
			result.NodeID = normalizedID
		}
		if !result.OK {
			errMsg := strings.TrimSpace(result.Error)
			if errMsg == "" {
				errMsg = "probe log fetch failed"
			}
			return result, errors.New(errMsg)
		}
		return result, nil
	case <-timer.C:
		return probeLogsResultMessage{}, fmt.Errorf("probe log fetch timeout")
	}
}

func consumeProbeLogsResult(result probeLogsResultMessage) {
	requestID := strings.TrimSpace(result.RequestID)
	if requestID == "" {
		return
	}
	probeLogWaiters.mu.Lock()
	waiter, ok := probeLogWaiters.data[requestID]
	if ok {
		delete(probeLogWaiters.data, requestID)
	}
	probeLogWaiters.mu.Unlock()
	if !ok {
		return
	}
	select {
	case waiter <- result:
	default:
	}
}

func newProbeLogRequestID(nodeID string) string {
	seq := probeLogRequestSeq.Add(1)
	return fmt.Sprintf("probe-log-%s-%d-%d", normalizeProbeNodeID(nodeID), time.Now().UnixNano(), seq)
}

func getProbeNodeByID(nodeID string) (probeNodeRecord, bool) {
	ProbeStore.mu.RLock()
	defer ProbeStore.mu.RUnlock()
	for _, node := range loadProbeNodesLocked() {
		if normalizeProbeNodeID(strconv.Itoa(node.NodeNo)) == nodeID {
			return node, true
		}
	}
	return probeNodeRecord{}, false
}

func controllerBaseURLFromRequest(r *http.Request) string {
	scheme := "http"
	if isHTTPSRequest(r) {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		host = "127.0.0.1:15030"
	}
	return scheme + "://" + host
}

func authenticateProbeRequest(r *http.Request) (string, error) {
	nodeID := normalizeProbeNodeID(r.Header.Get("X-Probe-Node-Id"))
	timestamp := strings.TrimSpace(r.Header.Get("X-Probe-Timestamp"))
	randomToken := strings.TrimSpace(r.Header.Get("X-Probe-Rand"))
	signature := strings.TrimSpace(r.Header.Get("X-Probe-Signature"))

	if nodeID == "" || timestamp == "" || randomToken == "" || signature == "" {
		return "", fmt.Errorf("missing probe auth headers")
	}
	secret, ok := resolveProbeSecret(nodeID)
	if !ok {
		return "", fmt.Errorf("probe secret is not configured for node")
	}
	if !verifyProbeConnectHMAC(secret, nodeID, timestamp, randomToken, signature) {
		return "", fmt.Errorf("invalid probe signature")
	}
	if !checkAndRememberProbeAuthReplay(nodeID, timestamp, randomToken) {
		return "", fmt.Errorf("probe auth replay detected")
	}
	return nodeID, nil
}

func resolveProbeSecret(nodeID string) (string, bool) {
	if ProbeStore == nil {
		return "", false
	}

	normalized := normalizeProbeNodeID(nodeID)
	ProbeStore.mu.RLock()
	secrets := loadProbeSecretsLocked()
	v, ok := secrets[normalized]
	ProbeStore.mu.RUnlock()
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return strings.TrimSpace(v), true
}

func verifyProbeConnectHMAC(secret, nodeID, timestamp, randomToken, signatureHex string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.TrimSpace(nodeID)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(timestamp)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(randomToken)))
	expected := mac.Sum(nil)
	provided, err := hex.DecodeString(strings.TrimSpace(signatureHex))
	if err != nil {
		return false
	}
	return hmac.Equal(expected, provided)
}

func checkAndRememberProbeAuthReplay(nodeID, timestamp, randomToken string) bool {
	tsInt, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return false
	}
	now := time.Now()
	ts := time.Unix(tsInt, 0)
	if ts.Before(now.Add(-2*time.Minute)) || ts.After(now.Add(2*time.Minute)) {
		return false
	}

	key := strings.TrimSpace(nodeID) + "|" + strings.TrimSpace(randomToken)
	if key == "|" || strings.HasSuffix(key, "|") {
		return false
	}

	probeAuthReplayStore.mu.Lock()
	defer probeAuthReplayStore.mu.Unlock()

	for k, seenAt := range probeAuthReplayStore.seen {
		if now.Sub(seenAt) > 10*time.Minute {
			delete(probeAuthReplayStore.seen, k)
		}
	}

	if _, exists := probeAuthReplayStore.seen[key]; exists {
		return false
	}
	probeAuthReplayStore.seen[key] = now
	return true
}
