package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const probeNonceTTL = 30 * time.Second

const (
	probeNonceRateWindow = 1 * time.Minute
	probeNonceRateLimit  = 20
)

type probeReportMessage struct {
	Type   string   `json:"type"`
	NodeID string   `json:"node_id"`
	IPv4   []string `json:"ipv4,omitempty"`
	IPv6   []string `json:"ipv6,omitempty"`
	System struct {
		CPUPercent        float64 `json:"cpu_percent"`
		MemoryTotalBytes  uint64  `json:"memory_total_bytes"`
		MemoryUsedBytes   uint64  `json:"memory_used_bytes"`
		MemoryUsedPercent float64 `json:"memory_used_percent"`
		SwapTotalBytes    uint64  `json:"swap_total_bytes"`
		SwapUsedBytes     uint64  `json:"swap_used_bytes"`
		SwapUsedPercent   float64 `json:"swap_used_percent"`
		DiskTotalBytes    uint64  `json:"disk_total_bytes"`
		DiskUsedBytes     uint64  `json:"disk_used_bytes"`
		DiskUsedPercent   float64 `json:"disk_used_percent"`
	} `json:"system"`
	Version   string `json:"version,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type probeAckMessage struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	ServerUTC string `json:"server_utc"`
}

type probeNonceManager struct {
	mu     sync.Mutex
	nonces map[string]time.Time
}

type probeNonceRateLimiter struct {
	mu      sync.Mutex
	counter map[string]probeRateBucket
}

type probeRateBucket struct {
	WindowStart time.Time
	Count       int
}

var probeNonces = &probeNonceManager{nonces: make(map[string]time.Time)}
var probeNonceLimiter = &probeNonceRateLimiter{counter: make(map[string]probeRateBucket)}

var probeWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func ProbeNonceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{
			"error": "https is required",
		})
		return
	}

	nodeID := normalizeProbeNodeID(r.Header.Get("X-Probe-Node-Id"))
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "X-Probe-Node-Id is required"})
		return
	}

	if _, ok := resolveProbeSecret(nodeID); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "probe secret is not configured for node"})
		return
	}

	clientIP, _ := getClientIP(r)
	rateKey := nodeID + "|" + clientIP
	if !probeNonceLimiter.allow(rateKey) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "probe nonce rate limit exceeded"})
		return
	}

	nonce, err := randomHex(32)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate nonce"})
		return
	}

	expiresAt := time.Now().Add(probeNonceTTL)
	probeNonces.add(nonce, expiresAt)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"nonce":      nonce,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
}

func ProbeWSHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{
			"error": "https is required",
		})
		return
	}

	nodeID, err := authenticateProbeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	conn, err := probeWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	session := registerProbeSession(nodeID, conn)
	defer unregisterProbeSession(nodeID, session)

	clientIP, _ := getClientIP(r)
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var msg probeReportMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			_ = session.writeJSON(probeAckMessage{Type: "error", Message: "invalid json payload", ServerUTC: time.Now().UTC().Format(time.RFC3339)})
			continue
		}

		reportedNodeID := strings.TrimSpace(msg.NodeID)
		if reportedNodeID == "" {
			reportedNodeID = nodeID
		}

		log.Printf(
			"probe ws report: node_id=%s remote=%s ipv4=%s ipv6=%s cpu=%.2f%% mem=%.2f%% disk=%.2f%% swap=%.2f%% version=%s",
			reportedNodeID,
			clientIP,
			strings.Join(msg.IPv4, ","),
			strings.Join(msg.IPv6, ","),
			msg.System.CPUPercent,
			msg.System.MemoryUsedPercent,
			msg.System.DiskUsedPercent,
			msg.System.SwapUsedPercent,
			strings.TrimSpace(msg.Version),
		)

		_ = session.writeJSON(probeAckMessage{
			Type:      "ack",
			Message:   "report accepted",
			ServerUTC: time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func (m *probeNonceManager) add(nonce string, expiresAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gcLocked(time.Now())
	m.nonces[nonce] = expiresAt
}

func (m *probeNonceManager) consume(nonce string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gcLocked(time.Now())

	expiresAt, ok := m.nonces[nonce]
	if !ok {
		return errors.New("probe nonce not found")
	}
	delete(m.nonces, nonce)
	if time.Now().After(expiresAt) {
		return errors.New("probe nonce expired")
	}
	return nil
}

func (m *probeNonceManager) gcLocked(now time.Time) {
	for nonce, expiresAt := range m.nonces {
		if now.After(expiresAt) {
			delete(m.nonces, nonce)
		}
	}
}

func (l *probeNonceRateLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	for k, bucket := range l.counter {
		if now.Sub(bucket.WindowStart) > 3*probeNonceRateWindow {
			delete(l.counter, k)
		}
	}

	bucket, ok := l.counter[key]
	if !ok || now.Sub(bucket.WindowStart) >= probeNonceRateWindow {
		l.counter[key] = probeRateBucket{WindowStart: now, Count: 1}
		return true
	}

	if bucket.Count >= probeNonceRateLimit {
		return false
	}
	bucket.Count++
	l.counter[key] = bucket
	return true
}

func resolveProbeSecret(nodeID string) (string, bool) {
	if Store == nil {
		return "", false
	}

	normalized := normalizeProbeNodeID(nodeID)
	Store.mu.RLock()
	secrets := loadProbeSecretsLocked()
	v, ok := secrets[normalized]
	Store.mu.RUnlock()
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return strings.TrimSpace(v), true
}

func verifyProbeHMAC(secret, nonce, signatureHex string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(nonce))
	expected := mac.Sum(nil)
	provided, err := hex.DecodeString(strings.TrimSpace(signatureHex))
	if err != nil {
		return false
	}
	return hmac.Equal(expected, provided)
}
