package core

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const probeAuthChallengeTTL = 2 * time.Minute

var probeAuthChallengeState = struct {
	sync.Mutex
	key  []byte
	used map[string]time.Time
}{used: make(map[string]time.Time)}

func ProbeAuthChallengeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodeID := normalizeProbeNodeID(r.URL.Query().Get("node_id"))
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}
	challenge, err := issueProbeAuthChallenge(nodeID, time.Now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to issue probe challenge"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"challenge": challenge})
}

func issueProbeAuthChallenge(nodeID string, now time.Time) (string, error) {
	key, err := probeAuthChallengeKey()
	if err != nil {
		return "", err
	}
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	expires := strconv.FormatInt(now.Add(probeAuthChallengeTTL).Unix(), 10)
	randomText := hex.EncodeToString(randomBytes)
	payload := strings.Join([]string{normalizeProbeNodeID(nodeID), expires, randomText}, "\n")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	return expires + "." + randomText + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func verifyAndConsumeProbeAuthChallenge(nodeID string, challenge string, now time.Time) error {
	parts := strings.Split(strings.TrimSpace(challenge), ".")
	if len(parts) != 3 {
		return errors.New("invalid probe auth challenge")
	}
	expiresUnix, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return errors.New("invalid probe auth challenge")
	}
	expiresAt := time.Unix(expiresUnix, 0)
	if !expiresAt.After(now) || expiresAt.After(now.Add(probeAuthChallengeTTL+10*time.Second)) {
		return errors.New("expired probe auth challenge")
	}
	randomBytes, err := hex.DecodeString(parts[1])
	if err != nil || len(randomBytes) != 32 {
		return errors.New("invalid probe auth challenge")
	}
	providedMAC, err := hex.DecodeString(parts[2])
	if err != nil {
		return errors.New("invalid probe auth challenge")
	}
	key, err := probeAuthChallengeKey()
	if err != nil {
		return err
	}
	payload := strings.Join([]string{normalizeProbeNodeID(nodeID), parts[0], parts[1]}, "\n")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(mac.Sum(nil), providedMAC) {
		return errors.New("invalid probe auth challenge")
	}

	usedKey := normalizeProbeNodeID(nodeID) + "|" + strings.TrimSpace(challenge)
	probeAuthChallengeState.Lock()
	defer probeAuthChallengeState.Unlock()
	for key, expiry := range probeAuthChallengeState.used {
		if !expiry.After(now) {
			delete(probeAuthChallengeState.used, key)
		}
	}
	if _, exists := probeAuthChallengeState.used[usedKey]; exists {
		return errors.New("probe auth challenge was already used")
	}
	probeAuthChallengeState.used[usedKey] = expiresAt
	return nil
}

func probeAuthChallengeKey() ([]byte, error) {
	probeAuthChallengeState.Lock()
	defer probeAuthChallengeState.Unlock()
	if len(probeAuthChallengeState.key) == 32 {
		return append([]byte(nil), probeAuthChallengeState.key...), nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate probe challenge key: %w", err)
	}
	probeAuthChallengeState.key = key
	return append([]byte(nil), key...), nil
}

func resetProbeAuthChallengeStateForTest() {
	probeAuthChallengeState.Lock()
	probeAuthChallengeState.key = nil
	probeAuthChallengeState.used = make(map[string]time.Time)
	probeAuthChallengeState.Unlock()
}
