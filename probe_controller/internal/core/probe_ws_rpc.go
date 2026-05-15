package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type probeControllerRPCRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	URL       string `json:"url,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Offset    int64  `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type probeControllerRPCResponse struct {
	Type        string `json:"type"`
	RequestID   string `json:"request_id"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	StatusCode  int    `json:"status_code,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	TotalSize   int64  `json:"total_size,omitempty"`
	EOF         bool   `json:"eof,omitempty"`
	PayloadJSON string `json:"payload_json,omitempty"`
	DataBase64  string `json:"data_base64,omitempty"`
}

func handleProbeControllerRPCRequest(nodeID string, req probeControllerRPCRequest) probeControllerRPCResponse {
	resp := probeControllerRPCResponse{
		Type:      "controller_rpc_response",
		RequestID: strings.TrimSpace(req.RequestID),
		OK:        false,
	}
	action := strings.TrimSpace(strings.ToLower(req.Action))
	switch action {
	case "link_config_get":
		payload, err := buildProbeLinkChainsPayloadForNode(nodeID)
		if err != nil {
			resp.Error = err.Error()
			return resp
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			resp.Error = err.Error()
			return resp
		}
		resp.OK = true
		resp.StatusCode = http.StatusOK
		resp.ContentType = "application/json"
		resp.PayloadJSON = string(raw)
		return resp
	case "proxy_download_chunk":
		return handleProbeControllerProxyDownloadChunk(resp, req)
	default:
		resp.Error = "unsupported controller rpc action"
		return resp
	}
}

func buildProbeLinkChainsPayloadForNode(nodeID string) (map[string]any, error) {
	if ProbeLinkChainStore == nil {
		return nil, fmt.Errorf("chain store not initialized")
	}
	ProbeLinkChainStore.mu.RLock()
	all := loadProbeLinkChainsLocked()
	ProbeLinkChainStore.mu.RUnlock()

	available := filterAvailableProbeLinkChains(all)
	related := filterProbeLinkChainsByNodeID(available, nodeID)
	enriched := fillChainRelayHosts(related)
	return map[string]any{"chains": enriched}, nil
}

func handleProbeControllerProxyDownloadChunk(resp probeControllerRPCResponse, req probeControllerRPCRequest) probeControllerRPCResponse {
	rawURL := strings.TrimSpace(req.URL)
	if rawURL == "" {
		resp.Error = "url is required"
		return resp
	}
	targetURL, err := url.Parse(rawURL)
	if err != nil || targetURL == nil || targetURL.Scheme != "https" {
		resp.Error = "invalid download url"
		return resp
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 512 * 1024
	}
	if limit > 2*1024*1024 {
		limit = 2 * 1024 * 1024
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	proxyReq.Header.Set("User-Agent", "cloudhelper-probe-proxy-download")
	proxyReq.Header.Set("Accept", "application/octet-stream")
	proxyReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+int64(limit)-1))
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+token)
	}

	upstreamResp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		resp.Error = fmt.Sprintf("proxy download failed: %v", err)
		return resp
	}
	defer upstreamResp.Body.Close()
	if upstreamResp.StatusCode < 200 || upstreamResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(upstreamResp.Body, 4096))
		resp.Error = fmt.Sprintf("upstream status=%d body=%s", upstreamResp.StatusCode, strings.TrimSpace(string(body)))
		return resp
	}

	chunk, err := io.ReadAll(io.LimitReader(upstreamResp.Body, int64(limit)))
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	resp.OK = true
	resp.StatusCode = upstreamResp.StatusCode
	resp.ContentType = strings.TrimSpace(upstreamResp.Header.Get("Content-Type"))
	resp.DataBase64 = base64.StdEncoding.EncodeToString(chunk)
	resp.EOF = len(chunk) < limit
	resp.TotalSize = offset + int64(len(chunk))
	return resp
}
