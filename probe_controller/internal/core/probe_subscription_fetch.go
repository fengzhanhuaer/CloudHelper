package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const probeSubscriptionFetchTimeout = 55 * time.Second

type probeSubscriptionFetchCommand struct {
	Type       string `json:"type"`
	RequestID  string `json:"request_id"`
	URL        string `json:"url"`
	MaxBytes   int64  `json:"max_bytes"`
	TimeoutSec int    `json:"timeout_sec"`
	Timestamp  string `json:"timestamp"`
}

type probeSubscriptionFetchResultMessage struct {
	Type          string `json:"type"`
	RequestID     string `json:"request_id"`
	NodeID        string `json:"node_id"`
	OK            bool   `json:"ok"`
	ContentBase64 string `json:"content_base64,omitempty"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
	Size          int64  `json:"size,omitempty"`
	Error         string `json:"error,omitempty"`
	Timestamp     string `json:"timestamp,omitempty"`
}

var probeSubscriptionFetchRequestSeq atomic.Uint64

var probeSubscriptionFetchWaiters = struct {
	mu   sync.Mutex
	data map[string]chan probeSubscriptionFetchResultMessage
}{data: make(map[string]chan probeSubscriptionFetchResultMessage)}

func fetchProbeSpecialExitSubscriptionFromNode(ctx context.Context, nodeID, rawURL string) ([]byte, error) {
	normalizedID := normalizeProbeNodeID(nodeID)
	if normalizedID == "" {
		return nil, fmt.Errorf("subscription fetch probe is required")
	}
	node, ok := getProbeNodeByID(normalizedID)
	if !ok {
		return nil, fmt.Errorf("subscription fetch probe not found")
	}
	if strings.EqualFold(strings.TrimSpace(node.TargetSystem), "android") {
		return nil, fmt.Errorf("subscription fetch probe uses an unsupported Android build")
	}
	session, ok := getProbeSession(normalizedID)
	if !ok {
		return nil, fmt.Errorf("subscription fetch probe is offline")
	}

	requestID := newProbeSubscriptionFetchRequestID(normalizedID)
	waiter := make(chan probeSubscriptionFetchResultMessage, 1)
	probeSubscriptionFetchWaiters.mu.Lock()
	probeSubscriptionFetchWaiters.data[requestID] = waiter
	probeSubscriptionFetchWaiters.mu.Unlock()
	defer func() {
		probeSubscriptionFetchWaiters.mu.Lock()
		delete(probeSubscriptionFetchWaiters.data, requestID)
		probeSubscriptionFetchWaiters.mu.Unlock()
	}()

	command := probeSubscriptionFetchCommand{
		Type: "subscription_fetch", RequestID: requestID, URL: strings.TrimSpace(rawURL),
		MaxBytes: probeSpecialExitSubscriptionMaxBytes, TimeoutSec: int(probeSpecialExitSubscriptionTimeout / time.Second),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if err := session.writeJSON(command); err != nil {
		unregisterProbeSession(normalizedID, session)
		return nil, fmt.Errorf("subscription fetch command could not be sent")
	}

	timer := time.NewTimer(probeSubscriptionFetchTimeout)
	defer timer.Stop()
	select {
	case result := <-waiter:
		if normalizeProbeNodeID(result.NodeID) != normalizedID {
			return nil, fmt.Errorf("subscription fetch probe identity mismatch")
		}
		if !result.OK {
			return nil, errors.New(sanitizeProbeSubscriptionFetchResultError(result.Error, rawURL))
		}
		return decodeProbeSubscriptionFetchContent(result)
	case <-timer.C:
		return nil, fmt.Errorf("subscription fetch probe timed out")
	case <-ctx.Done():
		return nil, fmt.Errorf("subscription fetch was canceled")
	}
}

func sanitizeProbeSubscriptionFetchResultError(raw, rawURL string) string {
	message := strings.TrimSpace(raw)
	if message == "" {
		message = "subscription fetch failed"
	}
	redactions := []string{strings.TrimSpace(rawURL)}
	if target, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && target != nil {
		redactions = append(redactions, target.Host, target.Hostname(), target.RawQuery)
		for _, values := range target.Query() {
			redactions = append(redactions, values...)
		}
	}
	for _, value := range redactions {
		value = strings.TrimSpace(value)
		if len(value) >= 4 {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	message = strings.Join(strings.Fields(message), " ")
	runes := []rune(message)
	if len(runes) > 240 {
		message = string(runes[:240])
	}
	return message
}

func decodeProbeSubscriptionFetchContent(result probeSubscriptionFetchResultMessage) ([]byte, error) {
	maxEncoded := base64.StdEncoding.EncodedLen(probeSpecialExitSubscriptionMaxBytes)
	if len(result.ContentBase64) == 0 || len(result.ContentBase64) > maxEncoded {
		return nil, fmt.Errorf("subscription fetch result size is invalid")
	}
	content, err := base64.StdEncoding.DecodeString(result.ContentBase64)
	if err != nil || len(content) == 0 || len(content) > probeSpecialExitSubscriptionMaxBytes || result.Size != int64(len(content)) {
		return nil, fmt.Errorf("subscription fetch result content is invalid")
	}
	expectedHash := strings.ToLower(strings.TrimSpace(result.ContentSHA256))
	if len(expectedHash) != sha256.Size*2 {
		return nil, fmt.Errorf("subscription fetch result hash is invalid")
	}
	sum := sha256.Sum256(content)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), expectedHash) {
		return nil, fmt.Errorf("subscription fetch result hash mismatch")
	}
	return content, nil
}

func consumeProbeSubscriptionFetchResult(result probeSubscriptionFetchResultMessage) {
	requestID := strings.TrimSpace(result.RequestID)
	if requestID == "" {
		return
	}
	probeSubscriptionFetchWaiters.mu.Lock()
	waiter, ok := probeSubscriptionFetchWaiters.data[requestID]
	if ok {
		delete(probeSubscriptionFetchWaiters.data, requestID)
	}
	probeSubscriptionFetchWaiters.mu.Unlock()
	if !ok {
		return
	}
	select {
	case waiter <- result:
	default:
	}
}

func newProbeSubscriptionFetchRequestID(nodeID string) string {
	sequence := probeSubscriptionFetchRequestSeq.Add(1)
	return fmt.Sprintf("subscription-fetch-%s-%d-%d", normalizeProbeNodeID(nodeID), time.Now().UTC().UnixNano(), sequence)
}
