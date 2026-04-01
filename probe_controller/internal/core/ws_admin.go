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
	Offset    int64  `json:"offset"`
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
	controllerBaseURL := controllerBaseURLFromRequest(r)

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

		data, err := handleAdminWSAction(req.Action, req.Payload, controllerBaseURL)
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
	if req.Offset < 0 {
		return nil, fmt.Errorf("offset must be >= 0")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return nil, err
	}
	proxyReq.Header.Set("User-Agent", "cloudhelper-proxy-download")
	proxyReq.Header.Set("Accept", "application/octet-stream")
	if req.Offset > 0 {
		proxyReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", req.Offset))
	}
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		return nil, fmt.Errorf("proxy download failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return map[string]interface{}{
			"downloaded": req.Offset,
			"total":      req.Offset,
			"status":     resp.StatusCode,
		}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("upstream status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	total := resp.ContentLength
	if total >= 0 && resp.StatusCode == http.StatusPartialContent && req.Offset > 0 {
		total += req.Offset
	}
	downloaded := req.Offset
	buf := make([]byte, chunkSize)
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
				"status":       resp.StatusCode,
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
		"status":     resp.StatusCode,
	}, nil
}

func handleAdminWSAction(action string, payload json.RawMessage, controllerBaseURL string) (interface{}, error) {
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
		return triggerControllerUpgradeTask()
	case "admin.upgrade.progress":
		return getControllerUpgradeProgress(), nil
	case "admin.logs":
		var req struct {
			Lines        int    `json:"lines"`
			SinceMinutes int    `json:"since_minutes"`
			MinLevel     string `json:"min_level"`
		}
		_ = json.Unmarshal(payload, &req)
		lineLimit := normalizeAdminLogLines(strconv.Itoa(req.Lines))
		sinceMinutes := normalizeAdminSinceMinutes(strconv.Itoa(req.SinceMinutes))
		logPath, err := resolveControllerLogPath()
		if err != nil {
			return nil, err
		}
		content, entries, err := readControllerLogTailLines(logPath, lineLimit, sinceMinutes, req.MinLevel)
		if err != nil {
			return nil, err
		}
		return adminLogsResponse{Source: "server", FilePath: logPath, Lines: lineLimit, Content: content, Fetched: time.Now().Format(time.RFC3339), Entries: entries}, nil
	case "admin.tunnel.nodes":
		return map[string]interface{}{"nodes": currentTunnelNodes()}, nil
	case "admin.probe.nodes.get":
		ProbeStore.mu.RLock()
		nodes := loadProbeNodesLocked()
		ProbeStore.mu.RUnlock()
		return map[string]interface{}{"nodes": nodes}, nil
	case "admin.probe.node.create":
		var req probeNodeCreateRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		ProbeStore.mu.Lock()
		node, err := createProbeNodeLocked(req.NodeName)
		ProbeStore.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if err := ProbeStore.Save(); err != nil {
			return nil, err
		}
		return map[string]interface{}{"node": node}, nil
	case "admin.probe.node.update":
		var req probeNodeUpdateRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		ProbeStore.mu.Lock()
		node, err := updateProbeNodeLocked(req)
		ProbeStore.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if err := ProbeStore.Save(); err != nil {
			return nil, err
		}
		return map[string]interface{}{"node": node}, nil
	case "admin.probe.link.update":
		var req probeNodeLinkUpdateRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		ProbeStore.mu.Lock()
		node, err := updateProbeNodeLinkLocked(req)
		ProbeStore.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if err := ProbeStore.Save(); err != nil {
			return nil, err
		}
		return map[string]interface{}{"node": node}, nil
	case "admin.probe.link.test.start":
		var req struct {
			NodeID       string `json:"node_id"`
			Protocol     string `json:"protocol"`
			InternalPort int    `json:"internal_port"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		nodeID := normalizeProbeNodeID(req.NodeID)
		if nodeID == "" {
			return nil, fmt.Errorf("node_id is required")
		}
		if _, ok := getProbeNodeByID(nodeID); !ok {
			return nil, fmt.Errorf("probe node not found")
		}
		result, err := dispatchProbeLinkTestControl(nodeID, "start", req.Protocol, req.InternalPort, controllerBaseURL)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"ok":            result.OK,
			"node_id":       nodeID,
			"action":        "start",
			"protocol":      strings.TrimSpace(result.Protocol),
			"listen_host":   strings.TrimSpace(result.ListenHost),
			"internal_port": result.InternalPort,
			"message":       strings.TrimSpace(result.Message),
			"timestamp":     strings.TrimSpace(result.Timestamp),
		}, nil
	case "admin.probe.link.test.stop":
		var req struct {
			NodeID string `json:"node_id"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		nodeID := normalizeProbeNodeID(req.NodeID)
		if nodeID == "" {
			return nil, fmt.Errorf("node_id is required")
		}
		if _, ok := getProbeNodeByID(nodeID); !ok {
			return nil, fmt.Errorf("probe node not found")
		}
		result, err := dispatchProbeLinkTestControl(nodeID, "stop", "", 0, controllerBaseURL)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"ok":            result.OK,
			"node_id":       nodeID,
			"action":        "stop",
			"protocol":      strings.TrimSpace(result.Protocol),
			"listen_host":   strings.TrimSpace(result.ListenHost),
			"internal_port": result.InternalPort,
			"message":       strings.TrimSpace(result.Message),
			"timestamp":     strings.TrimSpace(result.Timestamp),
		}, nil
	case "admin.probe.shell.session.start":
		var req struct {
			NodeID string `json:"node_id"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		nodeID := normalizeProbeNodeID(req.NodeID)
		if nodeID == "" {
			return nil, fmt.Errorf("node_id is required")
		}
		if _, ok := getProbeNodeByID(nodeID); !ok {
			return nil, fmt.Errorf("probe node not found")
		}

		result, err := dispatchProbeShellSessionControl(nodeID, "start", "", "", 0)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"ok":         result.OK,
			"node_id":    nodeID,
			"action":     "start",
			"session_id": strings.TrimSpace(result.SessionID),
			"message":    strings.TrimSpace(result.Message),
			"timestamp":  strings.TrimSpace(result.Timestamp),
		}, nil
	case "admin.probe.shell.session.exec":
		var req struct {
			NodeID     string `json:"node_id"`
			SessionID  string `json:"session_id"`
			Command    string `json:"command"`
			TimeoutSec int    `json:"timeout_sec"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		nodeID := normalizeProbeNodeID(req.NodeID)
		if nodeID == "" {
			return nil, fmt.Errorf("node_id is required")
		}
		if _, ok := getProbeNodeByID(nodeID); !ok {
			return nil, fmt.Errorf("probe node not found")
		}

		result, err := dispatchProbeShellSessionControl(nodeID, "exec", req.SessionID, req.Command, req.TimeoutSec)
		if err != nil {
			return map[string]interface{}{
				"ok":          false,
				"node_id":     nodeID,
				"action":      "exec",
				"session_id":  strings.TrimSpace(req.SessionID),
				"command":     strings.TrimSpace(req.Command),
				"stdout":      result.Stdout,
				"stderr":      result.Stderr,
				"error":       err.Error(),
				"started_at":  strings.TrimSpace(result.StartedAt),
				"finished_at": strings.TrimSpace(result.FinishedAt),
				"duration_ms": result.DurationMS,
				"timestamp":   strings.TrimSpace(result.Timestamp),
			}, nil
		}
		return map[string]interface{}{
			"ok":          true,
			"node_id":     nodeID,
			"action":      "exec",
			"session_id":  strings.TrimSpace(result.SessionID),
			"command":     strings.TrimSpace(req.Command),
			"stdout":      result.Stdout,
			"stderr":      result.Stderr,
			"error":       strings.TrimSpace(result.Error),
			"started_at":  strings.TrimSpace(result.StartedAt),
			"finished_at": strings.TrimSpace(result.FinishedAt),
			"duration_ms": result.DurationMS,
			"timestamp":   strings.TrimSpace(result.Timestamp),
		}, nil
	case "admin.probe.shell.session.stop":
		var req struct {
			NodeID    string `json:"node_id"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		nodeID := normalizeProbeNodeID(req.NodeID)
		if nodeID == "" {
			return nil, fmt.Errorf("node_id is required")
		}
		if _, ok := getProbeNodeByID(nodeID); !ok {
			return nil, fmt.Errorf("probe node not found")
		}
		result, err := dispatchProbeShellSessionControl(nodeID, "stop", req.SessionID, "", 0)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"ok":         result.OK,
			"node_id":    nodeID,
			"action":     "stop",
			"session_id": strings.TrimSpace(req.SessionID),
			"message":    strings.TrimSpace(result.Message),
			"timestamp":  strings.TrimSpace(result.Timestamp),
		}, nil
	case "admin.probe.shell.exec":
		var req struct {
			NodeID     string `json:"node_id"`
			Command    string `json:"command"`
			TimeoutSec int    `json:"timeout_sec"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		nodeID := normalizeProbeNodeID(req.NodeID)
		if nodeID == "" {
			return nil, fmt.Errorf("node_id is required")
		}
		if _, ok := getProbeNodeByID(nodeID); !ok {
			return nil, fmt.Errorf("probe node not found")
		}

		result, err := dispatchProbeShellExec(nodeID, req.Command, req.TimeoutSec)
		if err != nil {
			return map[string]interface{}{
				"ok":          false,
				"node_id":     nodeID,
				"command":     strings.TrimSpace(req.Command),
				"exit_code":   result.ExitCode,
				"stdout":      strings.TrimSpace(result.Stdout),
				"stderr":      strings.TrimSpace(result.Stderr),
				"error":       err.Error(),
				"started_at":  strings.TrimSpace(result.StartedAt),
				"finished_at": strings.TrimSpace(result.FinishedAt),
				"duration_ms": result.DurationMS,
				"timestamp":   strings.TrimSpace(result.Timestamp),
			}, nil
		}

		return map[string]interface{}{
			"ok":          true,
			"node_id":     nodeID,
			"command":     strings.TrimSpace(result.Command),
			"exit_code":   result.ExitCode,
			"stdout":      strings.TrimSpace(result.Stdout),
			"stderr":      strings.TrimSpace(result.Stderr),
			"error":       strings.TrimSpace(result.Error),
			"started_at":  strings.TrimSpace(result.StartedAt),
			"finished_at": strings.TrimSpace(result.FinishedAt),
			"duration_ms": result.DurationMS,
			"timestamp":   strings.TrimSpace(result.Timestamp),
		}, nil
	case "admin.probe.shell.shortcuts.get":
		ProbeStore.mu.RLock()
		items := loadProbeShellShortcutsLocked()
		ProbeStore.mu.RUnlock()
		return map[string]interface{}{
			"items": items,
		}, nil
	case "admin.probe.shell.shortcuts.upsert":
		var req struct {
			Name    string `json:"name"`
			Command string `json:"command"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		ProbeStore.mu.Lock()
		items, err := upsertProbeShellShortcutLocked(req.Name, req.Command)
		ProbeStore.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if err := ProbeStore.Save(); err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"items": items,
		}, nil
	case "admin.probe.shell.shortcuts.delete":
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		ProbeStore.mu.Lock()
		items, err := removeProbeShellShortcutLocked(req.Name)
		ProbeStore.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if err := ProbeStore.Save(); err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"items": items,
		}, nil
	case "admin.probe.link.users.get":
		return map[string]interface{}{
			"users": listProbeLinkUserIdentities(),
		}, nil
	case "admin.probe.link.user.public_key.get":
		var req struct {
			Username string `json:"username"`
		}
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &req); err != nil {
				return nil, fmt.Errorf("invalid payload")
			}
		}
		user, publicKey, err := resolveProbeLinkUserIdentityAndPublicKey(req.Username)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"username":   strings.TrimSpace(user.Username),
			"user_role":  strings.TrimSpace(user.UserRole),
			"cert_type":  strings.TrimSpace(user.CertType),
			"public_key": strings.TrimSpace(publicKey),
		}, nil
	case "admin.probe.link.chains.get":
		if ProbeLinkChainStore == nil {
			return map[string]interface{}{"items": []probeLinkChainRecord{}}, nil
		}
		ProbeLinkChainStore.mu.RLock()
		items := loadProbeLinkChainsLocked()
		ProbeLinkChainStore.mu.RUnlock()
		items = fillChainRelayHosts(items)
		return map[string]interface{}{
			"items": items,
		}, nil
	case "admin.probe.link.chain.upsert":
		var req struct {
			ChainID        string   `json:"chain_id"`
			Name           string   `json:"name"`
			UserID         string   `json:"user_id"`
			UserPublicKey  string   `json:"user_public_key"`
			Secret         string   `json:"secret"`
			EntryNodeID    string   `json:"entry_node_id"`
			ExitNodeID     string   `json:"exit_node_id"`
			CascadeNodeIDs []string `json:"cascade_node_ids"`
			ListenHost     string   `json:"listen_host"`
			ListenPort     int      `json:"listen_port"`
			LinkLayer      string   `json:"link_layer"`
			HopConfigs     []struct {
				NodeNo     int    `json:"node_no"`
				ListenHost string `json:"listen_host"`
				// listen_port is the canonical field (renamed from legacy service_port).
				ListenPort int `json:"listen_port"`
				// service_port is the legacy field name from older frontend versions; used as fallback.
				ServicePort  int    `json:"service_port"`
				ExternalPort int    `json:"external_port"`
				LinkLayer    string `json:"link_layer"`
				DialMode     string `json:"dial_mode"`
			} `json:"hop_configs"`
			PortForwards []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				EntrySide  string `json:"entry_side"`
				ListenHost string `json:"listen_host"`
				ListenPort int    `json:"listen_port"`
				TargetHost string `json:"target_host"`
				TargetPort int    `json:"target_port"`
				Network    string `json:"network"`
				Enabled    bool   `json:"enabled"`
			} `json:"port_forwards"`
			EgressHost string `json:"egress_host"`
			EgressPort int    `json:"egress_port"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		if ProbeLinkChainStore == nil {
			return nil, fmt.Errorf("probe link chain store is not initialized")
		}

		var previous probeLinkChainRecord
		var hadPrevious bool
		ProbeLinkChainStore.mu.Lock()
		if strings.TrimSpace(req.ChainID) != "" {
			if item, ok := findProbeLinkChainByIDLocked(req.ChainID); ok {
				previous = item
				hadPrevious = true
			}
		}
		item, items, err := upsertProbeLinkChainLocked(probeLinkChainRecord{
			ChainID:        strings.TrimSpace(req.ChainID),
			Name:           strings.TrimSpace(req.Name),
			UserID:         strings.TrimSpace(req.UserID),
			UserPublicKey:  strings.TrimSpace(req.UserPublicKey),
			Secret:         strings.TrimSpace(req.Secret),
			EntryNodeID:    strings.TrimSpace(req.EntryNodeID),
			ExitNodeID:     strings.TrimSpace(req.ExitNodeID),
			CascadeNodeIDs: req.CascadeNodeIDs,
			ListenHost:     strings.TrimSpace(req.ListenHost),
			ListenPort:     req.ListenPort,
			LinkLayer:      strings.TrimSpace(req.LinkLayer),
			HopConfigs: func() []probeLinkChainHopConfig {
				out := make([]probeLinkChainHopConfig, 0, len(req.HopConfigs))
				for _, cfg := range req.HopConfigs {
					listenPort := cfg.ListenPort
					if listenPort <= 0 {
						listenPort = cfg.ServicePort // fallback for legacy frontend
					}
					out = append(out, probeLinkChainHopConfig{
						NodeNo:       cfg.NodeNo,
						ListenHost:   strings.TrimSpace(cfg.ListenHost),
						ListenPort:   listenPort,
						ExternalPort: cfg.ExternalPort,
						LinkLayer:    strings.TrimSpace(cfg.LinkLayer),
						DialMode:     strings.TrimSpace(cfg.DialMode),
					})
				}
				return out
			}(),
			PortForwards: func() []probeLinkChainPortForwardConfig {
				out := make([]probeLinkChainPortForwardConfig, 0, len(req.PortForwards))
				for _, item := range req.PortForwards {
					out = append(out, probeLinkChainPortForwardConfig{
						ID:         strings.TrimSpace(item.ID),
						Name:       strings.TrimSpace(item.Name),
						EntrySide:  strings.TrimSpace(item.EntrySide),
						ListenHost: strings.TrimSpace(item.ListenHost),
						ListenPort: item.ListenPort,
						TargetHost: strings.TrimSpace(item.TargetHost),
						TargetPort: item.TargetPort,
						Network:    strings.TrimSpace(item.Network),
						Enabled:    item.Enabled,
					})
				}
				return out
			}(),
			EgressHost: strings.TrimSpace(req.EgressHost),
			EgressPort: req.EgressPort,
		})
		ProbeLinkChainStore.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if err := ProbeLinkChainStore.Save(); err != nil {
			return nil, err
		}

		applyErrorText := ""
		if hadPrevious && strings.TrimSpace(previous.ChainID) != "" {
			if err := removeProbeLinkChainRecord(previous); err != nil {
				applyErrorText = err.Error()
			}
		}
		if err := applyProbeLinkChainRecord(item, controllerBaseURL); err != nil {
			if applyErrorText == "" {
				applyErrorText = err.Error()
			} else {
				applyErrorText = applyErrorText + "; " + err.Error()
			}
		}
		return map[string]interface{}{
			"item":        item,
			"items":       items,
			"apply_ok":    strings.TrimSpace(applyErrorText) == "",
			"apply_error": strings.TrimSpace(applyErrorText),
		}, nil
	case "admin.probe.link.chain.delete":
		var req struct {
			ChainID string `json:"chain_id"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		if ProbeLinkChainStore == nil {
			return nil, fmt.Errorf("probe link chain store is not initialized")
		}
		ProbeLinkChainStore.mu.Lock()
		removed, items, err := removeProbeLinkChainLocked(req.ChainID)
		ProbeLinkChainStore.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if err := ProbeLinkChainStore.Save(); err != nil {
			return nil, err
		}
		applyErrorText := ""
		if err := removeProbeLinkChainRecord(removed); err != nil {
			applyErrorText = err.Error()
		}
		return map[string]interface{}{
			"removed":     removed,
			"items":       items,
			"apply_ok":    strings.TrimSpace(applyErrorText) == "",
			"apply_error": strings.TrimSpace(applyErrorText),
		}, nil
	case "admin.probe.status.get":
		var req struct {
			NodeID string `json:"node_id"`
		}
		_ = json.Unmarshal(payload, &req)
		ProbeStore.mu.RLock()
		if strings.TrimSpace(req.NodeID) != "" {
			item, ok := loadProbeNodeStatusByIDLocked(req.NodeID)
			ProbeStore.mu.RUnlock()
			if !ok {
				return map[string]interface{}{"items": []probeNodeStatusRecord{}}, nil
			}
			return map[string]interface{}{"items": []probeNodeStatusRecord{item}}, nil
		}
		items := loadProbeNodeStatusLocked()
		ProbeStore.mu.RUnlock()
		return map[string]interface{}{"items": items}, nil
	case "admin.probe.logs.get":
		var req struct {
			NodeID       string `json:"node_id"`
			Lines        int    `json:"lines"`
			SinceMinutes int    `json:"since_minutes"`
			MinLevel     string `json:"min_level"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		nodeID := normalizeProbeNodeID(req.NodeID)
		if nodeID == "" {
			return nil, fmt.Errorf("node_id is required")
		}

		result, err := fetchProbeLogsFromNode(nodeID, req.Lines, req.SinceMinutes, req.MinLevel)
		if err != nil {
			return nil, err
		}

		nodeName := ""
		if node, ok := getProbeNodeByID(nodeID); ok {
			nodeName = strings.TrimSpace(node.NodeName)
		}
		return map[string]interface{}{
			"node_id":       nodeID,
			"node_name":     nodeName,
			"source":        strings.TrimSpace(result.Source),
			"file_path":     strings.TrimSpace(result.FilePath),
			"lines":         result.Lines,
			"since_minutes": result.SinceMinutes,
			"min_level":     strings.TrimSpace(result.MinLevel),
			"content":       result.Content,
			"entries":       result.Entries,
			"fetched":       time.Now().UTC().Format(time.RFC3339),
			"timestamp":     strings.TrimSpace(result.Timestamp),
		}, nil
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
		ProbeStore.mu.Lock()
		ProbeStore.data.ProbeNodes = nodes
		ProbeStore.data.ProbeSecrets = secrets
		ProbeStore.mu.Unlock()
		if err := ProbeStore.Save(); err != nil {
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
		ProbeStore.mu.Lock()
		secrets := loadProbeSecretsLocked()
		secrets[nodeID] = secret
		ProbeStore.data.ProbeSecrets = secrets
		ProbeStore.mu.Unlock()
		if err := ProbeStore.Save(); err != nil {
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
		result, err := dispatchUpgradeToProbe(node, controllerBaseURL)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "admin.probe.upgrade.all":
		ProbeStore.mu.RLock()
		nodes := loadProbeNodesLocked()
		ProbeStore.mu.RUnlock()
		success := 0
		items := make([]probeUpgradeDispatchResult, 0, len(nodes))
		failures := make([]string, 0)
		for _, node := range nodes {
			result, err := dispatchUpgradeToProbe(node, controllerBaseURL)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%d:%v", node.NodeNo, err))
				continue
			}
			items = append(items, result)
			success++
		}
		message := fmt.Sprintf("upgrade dispatch completed: success=%d total=%d failures=%d", success, len(nodes), len(failures))
		return map[string]interface{}{"success": success, "total": len(nodes), "failures": failures, "items": items, "message": message}, nil
	case "admin.backup.settings.get":
		settings := getBackupSettings()
		return map[string]interface{}{
			"enabled":       settings.Enabled,
			"rclone_remote": settings.RcloneRemote,
		}, nil
	case "admin.backup.settings.set":
		var req struct {
			Enabled      *bool  `json:"enabled"`
			RcloneRemote string `json:"rclone_remote"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		current := getBackupSettings()
		enabled := current.Enabled
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		remote := req.RcloneRemote
		if strings.TrimSpace(remote) == "" && strings.TrimSpace(current.RcloneRemote) != "" {
			remote = current.RcloneRemote
		}
		settings, err := setBackupSettings(enabled, remote)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"ok":            true,
			"enabled":       settings.Enabled,
			"rclone_remote": settings.RcloneRemote,
		}, nil
	case "admin.backup.settings.test":
		var req struct {
			RcloneRemote string `json:"rclone_remote"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		remote := strings.TrimSpace(req.RcloneRemote)
		if remote == "" {
			remote = getBackupSettings().RcloneRemote
		}
		if err := testBackupRcloneRemote(remote); err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"ok":            true,
			"rclone_remote": strings.TrimSpace(remote),
			"message":       "rclone remote test ok",
		}, nil
	case "admin.manager.backup.upload":
		return handleAdminWSManagerBackupUpload(payload)
	case "admin.manager.rule_routes.upload":
		return handleAdminWSManagerRuleRoutesUpload(payload)
	case "admin.manager.rule_routes.download":
		return handleAdminWSManagerRuleRoutesDownload(payload)
	case "admin.cloudflare.api.get":
		return getCloudflareAPIKey(), nil
	case "admin.cloudflare.api.set":
		var req cloudflareAPIKeyRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		result, err := setCloudflareAPIKey(req)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "admin.cloudflare.zone.get":
		return getCloudflareZone(), nil
	case "admin.cloudflare.zone.set":
		var req cloudflareZoneRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		result, err := setCloudflareZone(req)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "admin.cloudflare.ddns.records":
		return map[string]interface{}{
			"records": listCloudflareRecords(),
		}, nil
	case "admin.cloudflare.ddns.apply":
		var req cloudflareDDNSApplyRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		result, err := applyCloudflareDDNS(req)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "admin.cloudflare.zerotrust.whitelist.get":
		return getCloudflareZeroTrustWhitelist(), nil
	case "admin.cloudflare.zerotrust.whitelist.set":
		var req cloudflareZeroTrustWhitelistRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		result, err := setCloudflareZeroTrustWhitelist(req)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "admin.cloudflare.zerotrust.whitelist.run":
		var req cloudflareZeroTrustRunRequest
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &req); err != nil {
				return nil, fmt.Errorf("invalid payload")
			}
		}
		result, err := runCloudflareZeroTrustWhitelistSync(req.Force)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "admin.tg.accounts.list":
		return map[string]interface{}{
			"accounts": listTGAssistantAccounts(),
		}, nil
	case "admin.tg.api.get":
		return getTGAssistantAPIKey(), nil
	case "admin.tg.api.set":
		var req tgAssistantAPIKeyRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		result, err := setTGAssistantAPIKey(req)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "admin.tg.accounts.refresh":
		accounts, err := refreshTGAssistantAccounts()
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"accounts": accounts,
		}, nil
	case "admin.tg.account.add":
		var req tgAssistantAddAccountRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		account, err := addTGAssistantAccount(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"account": account,
		}, nil
	case "admin.tg.account.remove":
		var req tgAssistantAccountIDRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		accounts, err := removeTGAssistantAccount(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"accounts": accounts,
		}, nil
	case "admin.tg.account.send_code":
		var req tgAssistantAccountIDRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		account, err := sendTGAssistantLoginCode(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"account": account,
		}, nil
	case "admin.tg.account.sign_in":
		var req tgAssistantSignInRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		account, err := completeTGAssistantLogin(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"account": account,
		}, nil
	case "admin.tg.account.logout":
		var req tgAssistantAccountIDRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		account, err := logoutTGAssistantAccount(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"account": account,
		}, nil
	case "admin.tg.bot.get":
		var req tgAssistantAccountIDRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		result, err := getTGAssistantBotAPIKey(req)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "admin.tg.bot.set":
		var req tgAssistantBotAPIKeyRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		result, err := setTGAssistantBotAPIKey(req)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "admin.tg.bot.test_send":
		var req tgAssistantBotTestSendRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		result, err := testSendTGAssistantBotMessage(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"result": result,
		}, nil
	case "admin.tg.targets.list":
		var req tgAssistantAccountIDRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		targets, err := listTGAssistantTargets(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"targets": targets,
		}, nil
	case "admin.tg.targets.refresh":
		var req tgAssistantAccountIDRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		targets, err := refreshTGAssistantTargets(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"targets": targets,
		}, nil
	case "admin.tg.schedule.list":
		var req tgAssistantAccountIDRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		schedules, err := listTGAssistantSchedules(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"schedules": schedules,
		}, nil
	case "admin.tg.schedule.add":
		var req tgAssistantScheduleAddRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		schedules, err := addTGAssistantSchedule(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"schedules": schedules,
		}, nil
	case "admin.tg.schedule.update":
		var req tgAssistantScheduleUpdateRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		schedules, err := updateTGAssistantSchedule(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"schedules": schedules,
		}, nil
	case "admin.tg.schedule.remove":
		var req tgAssistantScheduleRemoveRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		schedules, err := removeTGAssistantSchedule(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"schedules": schedules,
		}, nil
	case "admin.tg.schedule.set_enabled":
		var req tgAssistantScheduleSetEnabledRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		schedules, err := setTGAssistantScheduleEnabled(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"schedules": schedules,
		}, nil
	case "admin.tg.schedule.send_now":
		var req tgAssistantScheduleSendNowRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		result, err := sendNowTGAssistantSchedule(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"result": result,
		}, nil
	case "admin.tg.schedule.history":
		var req tgAssistantScheduleHistoryRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		history, err := listTGAssistantScheduleTaskHistory(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"history": history,
		}, nil
	case "admin.tg.schedule.pending":
		var req tgAssistantAccountIDRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
		pending, err := listTGAssistantPendingTasks(req)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"pending": pending,
		}, nil
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
