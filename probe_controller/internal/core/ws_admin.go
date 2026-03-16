package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type adminWSRequest struct {
	ID      string          `json:"id"`
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload"`
}

type adminWSAuthPayload struct {
	Token string `json:"token"`
}

type adminWSResponse struct {
	ID    string      `json:"id"`
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

type adminWSPush struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type adminProxyDownloadStreamRequest struct {
	URL       string `json:"url"`
	ChunkSize int    `json:"chunk_size"`
}

var adminWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  2048,
	WriteBufferSize: 2048,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func AdminWSHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}

	conn, err := adminWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	var authenticated atomic.Bool
	authTracked := false
	defer func() {
		if authTracked {
			onAdminWSDisconnected()
		}
	}()

	var writeMu sync.Mutex
	send := func(v interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
		return conn.WriteJSON(v)
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	stopPush := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				if !authenticated.Load() {
					continue
				}
				payload := statusPayload()
				payload["server_time"] = time.Now().UTC().Format(time.RFC3339)
				_ = send(adminWSPush{Type: "status", Data: payload})
			case <-stopPush:
				return
			}
		}
	}()
	defer close(stopPush)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req adminWSRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			_ = send(adminWSResponse{ID: "", OK: false, Error: "invalid request json"})
			continue
		}
		if strings.TrimSpace(req.Action) == "" {
			_ = send(adminWSResponse{ID: req.ID, OK: false, Error: "action is required"})
			continue
		}
		if strings.TrimSpace(req.Action) == "auth.session" {
			var authReq adminWSAuthPayload
			if err := json.Unmarshal(req.Payload, &authReq); err != nil {
				_ = send(adminWSResponse{ID: req.ID, OK: false, Error: "invalid auth payload"})
				continue
			}
			token := strings.TrimSpace(authReq.Token)
			if token == "" || !IsTokenValid(token) {
				_ = send(adminWSResponse{ID: req.ID, OK: false, Error: "invalid or expired session token"})
				continue
			}
			if !authTracked {
				onAdminWSAuthenticated()
				authTracked = true
			}
			authenticated.Store(true)
			_ = send(adminWSResponse{ID: req.ID, OK: true, Data: map[string]bool{"authenticated": true}})
			continue
		}
		if !authenticated.Load() {
			_ = send(adminWSResponse{ID: req.ID, OK: false, Error: "unauthorized: authenticate first"})
			continue
		}

		if strings.TrimSpace(req.Action) == "admin.proxy.download.stream" {
			data, err := handleAdminWSProxyDownloadStream(req.ID, req.Payload, send)
			if err != nil {
				_ = send(adminWSResponse{ID: req.ID, OK: false, Error: err.Error()})
				continue
			}
			_ = send(adminWSResponse{ID: req.ID, OK: true, Data: data})
			continue
		}

		data, err := handleAdminWSAction(req.Action, req.Payload)
		if err != nil {
			_ = send(adminWSResponse{ID: req.ID, OK: false, Error: err.Error()})
			continue
		}
		_ = send(adminWSResponse{ID: req.ID, OK: true, Data: data})
	}
}

func handleAdminWSProxyDownloadStream(requestID string, payload json.RawMessage, send func(v interface{}) error) (map[string]interface{}, error) {
	var req adminProxyDownloadStreamRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload")
	}

	rawURL := strings.TrimSpace(req.URL)
	if rawURL == "" {
		return nil, fmt.Errorf("url is required")
	}
	targetURL, err := url.Parse(rawURL)
	if err != nil || targetURL == nil || !strings.EqualFold(targetURL.Scheme, "https") {
		return nil, fmt.Errorf("invalid download url")
	}

	chunkSize := req.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 64 * 1024
	}
	if chunkSize > 256*1024 {
		chunkSize = 256 * 1024
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return nil, err
	}
	proxyReq.Header.Set("User-Agent", "cloudhelper-proxy-download")
	proxyReq.Header.Set("Accept", "application/octet-stream")
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		return nil, fmt.Errorf("proxy download failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("upstream status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	total := resp.ContentLength
	buf := make([]byte, chunkSize)
	var downloaded int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			downloaded += int64(n)
			chunk := base64.StdEncoding.EncodeToString(buf[:n])
			pushData := map[string]interface{}{
				"request_id":   strings.TrimSpace(requestID),
				"chunk_base64": chunk,
				"downloaded":   downloaded,
				"total":        total,
			}
			if err := send(adminWSPush{Type: "proxy.download.chunk", Data: pushData}); err != nil {
				return nil, fmt.Errorf("send download chunk failed: %v", err)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("proxy download failed: %v", readErr)
		}
	}

	return map[string]interface{}{
		"downloaded": downloaded,
		"total":      total,
	}, nil
}

func handleAdminWSAction(action string, payload json.RawMessage) (interface{}, error) {
	switch strings.TrimSpace(action) {
	case "admin.status":
		return map[string]interface{}{
			"status":      "ok",
			"uptime":      int(time.Since(serverStartTime).Seconds()),
			"server_time": time.Now().UTC().Format(time.RFC3339),
		}, nil
	case "admin.version":
		repo := releaseRepo()
		current := currentControllerVersion()
		resp := adminVersionResponse{CurrentVersion: current, ReleaseRepo: repo}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		release, err := fetchLatestRelease(ctx, repo)
		if err != nil {
			resp.Message = fmt.Sprintf("failed to query latest release: %v", err)
			return resp, nil
		}
		resp.LatestVersion = strings.TrimSpace(release.TagName)
		resp.UpgradeAvailable = normalizeVersion(current) != normalizeVersion(resp.LatestVersion)
		return resp, nil
	case "admin.upgrade":
		setControllerUpgradeProgress(adminUpgradeProgressResponse{Active: true, Phase: "prepare", Percent: 2, Message: "准备升级"})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		result, err := performControllerUpgrade(ctx)
		if err != nil {
			setControllerUpgradeProgress(adminUpgradeProgressResponse{Active: false, Phase: "failed", Percent: 0, Message: "升级失败"})
			return nil, err
		}
		setControllerUpgradeProgress(adminUpgradeProgressResponse{Active: false, Phase: "done", Percent: 100, Message: result.Message})
		if result.Updated && shouldAutoRestartAfterUpgrade() {
			go func() {
				time.Sleep(1200 * time.Millisecond)
				os.Exit(0)
			}()
		}
		return result, nil
	case "admin.upgrade.progress":
		return getControllerUpgradeProgress(), nil
	case "admin.logs":
		var req struct {
			Lines        int `json:"lines"`
			SinceMinutes int `json:"since_minutes"`
		}
		_ = json.Unmarshal(payload, &req)
		logPath, err := resolveControllerLogPath()
		if err != nil {
			return nil, err
		}
		content, err := readControllerLogTailLines(logPath, normalizeAdminLogLines(strconv.Itoa(req.Lines)), normalizeAdminSinceMinutes(strconv.Itoa(req.SinceMinutes)))
		if err != nil {
			return nil, err
		}
		return adminLogsResponse{Source: "server", FilePath: logPath, Lines: req.Lines, Content: content, Fetched: time.Now().Format(time.RFC3339)}, nil
	case "admin.tunnel.nodes":
		return map[string]interface{}{"nodes": currentTunnelNodes()}, nil
	case "admin.probe.nodes.get":
		Store.mu.RLock()
		nodes := loadProbeNodesLocked()
		Store.mu.RUnlock()
		return map[string]interface{}{"nodes": nodes}, nil
	case "admin.probe.status.get":
		var req struct {
			NodeID string `json:"node_id"`
		}
		_ = json.Unmarshal(payload, &req)
		Store.mu.RLock()
		if strings.TrimSpace(req.NodeID) != "" {
			item, ok := loadProbeNodeStatusByIDLocked(req.NodeID)
			Store.mu.RUnlock()
			if !ok {
				return map[string]interface{}{"items": []probeNodeStatusRecord{}}, nil
			}
			return map[string]interface{}{"items": []probeNodeStatusRecord{item}}, nil
		}
		items := loadProbeNodeStatusLocked()
		Store.mu.RUnlock()
		return map[string]interface{}{"items": items}, nil
	case "admin.probe.report_interval.get":
		return getProbeReportIntervalSnapshot(), nil
	case "admin.probe.report_interval.set":
		var req struct {
			IntervalSec int `json:"interval_sec"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		snapshot, err := setTemporaryProbeReportInterval(req.IntervalSec)
		if err != nil {
			return nil, err
		}
		return snapshot, nil
	case "admin.probe.nodes.sync":
		var req probeNodesSyncRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		nodes, secrets := normalizeProbeNodes(req.Nodes)
		Store.mu.Lock()
		Store.Data[probeNodesStoreField] = nodes
		Store.Data[probeSecretsStoreField] = secrets
		Store.mu.Unlock()
		if err := Store.Save(); err != nil {
			return nil, err
		}
		return map[string]interface{}{"nodes": nodes}, nil
	case "admin.probe.secret.upsert":
		var req probeSecretUpsertRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		nodeID := normalizeProbeNodeID(req.NodeID)
		secret := strings.TrimSpace(req.Secret)
		if nodeID == "" || secret == "" {
			return nil, fmt.Errorf("node_id and secret are required")
		}
		Store.mu.Lock()
		secrets := loadProbeSecretsLocked()
		secrets[nodeID] = secret
		Store.Data[probeSecretsStoreField] = secrets
		Store.mu.Unlock()
		if err := Store.Save(); err != nil {
			return nil, err
		}
		return map[string]interface{}{"ok": true, "node_id": nodeID}, nil
	case "admin.probe.upgrade":
		var req probeUpgradeDispatchRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		nodeID := normalizeProbeNodeID(req.NodeID)
		node, ok := getProbeNodeByID(nodeID)
		if !ok {
			return nil, fmt.Errorf("probe node not found")
		}
		if err := dispatchUpgradeToProbe(node, ""); err != nil {
			return nil, err
		}
		return map[string]interface{}{"ok": true, "node_id": nodeID}, nil
	case "admin.probe.upgrade.all":
		Store.mu.RLock()
		nodes := loadProbeNodesLocked()
		Store.mu.RUnlock()
		success := 0
		failures := make([]string, 0)
		for _, node := range nodes {
			if err := dispatchUpgradeToProbe(node, ""); err != nil {
				failures = append(failures, fmt.Sprintf("%d:%v", node.NodeNo, err))
				continue
			}
			success++
		}
		return map[string]interface{}{"success": success, "total": len(nodes), "failures": failures}, nil
	case "admin.proxy.github.latest":
		var req proxyLatestRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		repo, err := normalizeGitHubRepo(req.Repo, req.Project)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		release, err := fetchLatestRelease(ctx, repo)
		if err != nil {
			return nil, err
		}
		assets := make([]proxyAsset, 0, len(release.Assets))
		for _, a := range release.Assets {
			assets = append(assets, proxyAsset{Name: a.Name, Size: a.Size, DownloadURL: a.BrowserDownloadURL})
		}
		return proxyLatestResponse{Repo: repo, TagName: strings.TrimSpace(release.TagName), ReleaseName: strings.TrimSpace(release.Name), HTMLURL: strings.TrimSpace(release.HTMLURL), PublishedAt: strings.TrimSpace(release.PublishedAt), Assets: assets}, nil
	default:
		return nil, fmt.Errorf("unsupported action: %s", action)
	}
}
