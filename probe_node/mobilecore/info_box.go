package mobilecore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	mobileInfoBoxAPIPath        = "/api/probe/info_box"
	mobileInfoBoxRequestTimeout = 15 * time.Second
)

type mobileInfoBoxItem struct {
	ID        string `json:"id"`
	NodeID    string `json:"node_id"`
	NodeName  string `json:"node_name,omitempty"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

type mobileInfoBoxPayload struct {
	OK        bool                `json:"ok"`
	Error     string              `json:"error,omitempty"`
	Version   int                 `json:"version,omitempty"`
	UpdatedAt string              `json:"updated_at,omitempty"`
	Items     []mobileInfoBoxItem `json:"items"`
}

var mobileInfoBoxRevision atomic.Uint64

func InfoBoxList(controllerURL string, nodeID string, nodeSecret string) string {
	return mobileInfoBoxRequestJSON(controllerURL, nodeID, nodeSecret, http.MethodGet, "")
}

func InfoBoxSend(controllerURL string, nodeID string, nodeSecret string, message string) string {
	return mobileInfoBoxRequestJSON(controllerURL, nodeID, nodeSecret, http.MethodPost, message)
}

func InfoBoxClear(controllerURL string, nodeID string, nodeSecret string) string {
	return mobileInfoBoxRequestJSON(controllerURL, nodeID, nodeSecret, http.MethodDelete, "")
}

func InfoBoxRevision() string {
	return fmt.Sprintf("%d", mobileInfoBoxRevision.Load())
}

func markMobileInfoBoxChanged() {
	mobileInfoBoxRevision.Add(1)
}

func mobileInfoBoxRequestJSON(controllerURL string, nodeID string, nodeSecret string, method string, message string) string {
	ctx, cancel := context.WithTimeout(context.Background(), mobileInfoBoxRequestTimeout)
	defer cancel()
	payload, err := requestMobileInfoBox(ctx, controllerURL, nodeID, nodeSecret, method, message)
	if err != nil {
		payload = mobileInfoBoxPayload{OK: false, Error: err.Error(), Items: []mobileInfoBoxItem{}}
	}
	raw, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return `{"ok":false,"error":"encode info box response failed","items":[]}`
	}
	return string(raw)
}

func requestMobileInfoBox(ctx context.Context, controllerURL string, nodeID string, nodeSecret string, method string, message string) (mobileInfoBoxPayload, error) {
	baseURL, err := normalizeControllerBaseURL(controllerURL)
	if err != nil {
		return mobileInfoBoxPayload{}, err
	}
	cleanNodeID := strings.TrimSpace(nodeID)
	cleanSecret := strings.TrimSpace(nodeSecret)
	if cleanNodeID == "" || cleanSecret == "" {
		return mobileInfoBoxPayload{}, errors.New("node identity is not configured")
	}
	var body io.Reader
	if method == http.MethodPost {
		raw, err := json.Marshal(map[string]string{"message": message})
		if err != nil {
			return mobileInfoBoxPayload{}, err
		}
		body = bytes.NewReader(raw)
	}
	endpoint := strings.TrimRight(baseURL, "/") + mobileInfoBoxAPIPath
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return mobileInfoBoxPayload{}, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := applyAuthHeaders(req, cleanNodeID, cleanSecret); err != nil {
		return mobileInfoBoxPayload{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return mobileInfoBoxPayload{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var failure map[string]string
		_ = json.Unmarshal(raw, &failure)
		if text := strings.TrimSpace(failure["error"]); text != "" {
			return mobileInfoBoxPayload{}, errors.New(text)
		}
		return mobileInfoBoxPayload{}, fmt.Errorf("info box request failed: status=%d", resp.StatusCode)
	}
	var payload mobileInfoBoxPayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return mobileInfoBoxPayload{}, err
	}
	payload.OK = true
	if payload.Items == nil {
		payload.Items = []mobileInfoBoxItem{}
	}
	return payload, nil
}
